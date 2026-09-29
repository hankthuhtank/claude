// Package engine is Backplane's deterministic core: it plans changes,
// executes them in dependency order with checkpoints, verifies the result at
// six levels, detects drift, diagnoses failures and keeps monitoring.
//
// AI never drives this package. Plans come from blueprints (templates or the
// plain-English builder), the user approves them, and only then does the
// executor call provider adapters.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/store"
	"safisolutions.org/backplane/internal/vault"
)

// Engine wires storage, secrets and providers together.
type Engine struct {
	Store    *store.Store
	Vault    *vault.Vault
	Registry *providers.Registry
	Redact   *vault.Redactor
	Bus      *Bus
	// BaseURLs redirects providers to the simulator in practice mode.
	PracticeBaseURLs map[string]string
	// Poll overrides provider polling (tests).
	Poll time.Duration
	// RetryBase is the first retry delay for failed operations.
	RetryBase time.Duration
	// MaxAttempts per operation (engine level; httpx retries individual calls too).
	MaxAttempts int
	// Parallel is how many operations may run at once.
	Parallel int
	Now      func() time.Time

	mu      sync.Mutex
	running map[string]context.CancelFunc // run id -> cancel
	checks  map[string]context.CancelFunc // project/env -> running check
	signals map[string]*APISignal         // provider|header|value -> signal
}

// APISignal is an API-change announcement a provider sent in a response
// header (for example Deprecation or Sunset on an endpoint Backplane uses).
type APISignal struct {
	Provider  string    `json:"provider"`
	Header    string    `json:"header"`
	Value     string    `json:"value"`
	Endpoint  string    `json:"endpoint"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Count     int       `json:"count"`
}

// New creates an engine.
func New(st *store.Store, v *vault.Vault, reg *providers.Registry, red *vault.Redactor) *Engine {
	e := &Engine{Store: st, Vault: v, Registry: reg, Redact: red, Bus: NewBus(), RetryBase: 2 * time.Second,
		MaxAttempts: 4, Parallel: 3, Now: time.Now, running: map[string]context.CancelFunc{}, checks: map[string]context.CancelFunc{}, signals: map[string]*APISignal{}}
	var saved []*APISignal
	if store.ReadJSON(filepath.Join(st.Dir, "signals.json"), &saved) == nil {
		for _, s := range saved {
			e.signals[s.Provider+"|"+s.Header+"|"+s.Value] = s
		}
	}
	return e
}

// Signals returns recorded API-change signals, newest first.
func (e *Engine) Signals() []APISignal {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]APISignal, 0, len(e.signals))
	for _, s := range e.signals {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (e *Engine) recordSignals(provider, endpoint string, sig map[string]string) {
	if len(sig) == 0 {
		return
	}
	now := e.Now().UTC()
	e.mu.Lock()
	added := false
	for h, v := range sig {
		if h == "X-GitHub-Api-Version-Selected" && v == "2022-11-28" {
			continue // the version Backplane asks for; not news
		}
		k := provider + "|" + h + "|" + v
		s := e.signals[k]
		if s == nil {
			s = &APISignal{Provider: provider, Header: h, Value: v, Endpoint: endpoint, FirstSeen: now}
			e.signals[k] = s
			added = true
		}
		s.LastSeen = now
		s.Count++
	}
	var all []*APISignal
	for _, s := range e.signals {
		all = append(all, s)
	}
	e.mu.Unlock()
	if added || now.Second()%10 == 0 {
		_ = store.WriteJSON(filepath.Join(e.Store.Dir, "signals.json"), all)
	}
	if added {
		e.Log(core.LogEntry{Level: "warn", Source: "change-detection", Provider: provider,
			Message: providers.DisplayName(provider) + " announced an API change on " + endpoint + " — see Settings → Provider changes."})
	}
}

// NewID returns a short random id with a prefix.
func NewID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + time.Now().UTC().Format("20060102T150405") + "_" + hex.EncodeToString(b)
}

// Log writes a redacted entry to the unified log and the live event stream.
func (e *Engine) Log(entry core.LogEntry) {
	if entry.At.IsZero() {
		entry.At = e.Now().UTC()
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	entry.Message = e.Redact.Mask(entry.Message)
	entry.Detail = e.Redact.Mask(entry.Detail)
	_ = e.Store.AppendLog(entry)
	e.Bus.Publish(Event{Type: "log", Project: entry.Project, Env: entry.Environment, RunID: entry.RunID, Data: entry})
}

// ---- connections ----

// Connections returns saved connections.
func (e *Engine) Connections() ([]core.Connection, error) { return e.Store.LoadConnections() }

// Connection finds one connection by id.
func (e *Engine) Connection(id string) (*core.Connection, error) {
	cs, err := e.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i], nil
		}
	}
	return nil, fmt.Errorf("connection %s not found", id)
}

// ConnKey is the vault key of a connection's credential field.
func ConnKey(id, field string) string { return "conn/" + id + "/" + field }

// OpenConn authenticates a stored connection.
func (e *Engine) OpenConn(c core.Connection, logCtx core.LogEntry) (*providers.Conn, error) {
	p, ok := e.Registry.Provider(c.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %s", c.Provider)
	}
	secret, err := e.Vault.GetString(c.SecretRef)
	if err != nil {
		return nil, &core.Problem{Title: providers.DisplayName(c.Provider) + " credential missing", Provider: c.Provider, Code: "auth",
			Summary: "The saved credential could not be read from this computer's vault. Re-enter it on the Connections screen."}
	}
	opts := providers.ConnectOptions{Log: e.httpLogger(logCtx), Practice: c.Practice}
	if c.Practice {
		// Practice credentials only ever go to the simulator. With the
		// sandbox stopped there is nowhere safe to send them.
		if len(e.PracticeBaseURLs) == 0 {
			return nil, &core.Problem{Title: "Practice sandbox is off", Provider: c.Provider, Code: "offline",
				Summary: "This is a practice connection. Start practice mode to use it."}
		}
		opts.BaseURLs = e.PracticeBaseURLs
	}
	return p.Connect(c, secret, opts)
}

func (e *Engine) httpLogger(ctxEntry core.LogEntry) func(httpx.LogEvent) {
	return func(ev httpx.LogEvent) {
		if len(ev.Signals) > 0 {
			e.recordSignals(ev.Provider, endpointOf(ev.Method, ev.URL), ev.Signals)
		}
		if ev.Quiet && ev.Err == nil {
			return
		}
		level := "debug"
		msg := fmt.Sprintf("%s %s → %d (%dms)", ev.Method, ev.URL, ev.Status, ev.Latency.Milliseconds())
		if ev.Err != nil {
			level = "warn"
			if ev.Quiet {
				level = "debug" // an expected rejection (negative test) or a probe
			}
			msg = fmt.Sprintf("%s %s failed (attempt %d): %v", ev.Method, ev.URL, ev.Attempt, ev.Err)
		}
		le := ctxEntry
		le.Level, le.Source, le.Provider, le.Message, le.RequestID = level, "http", ev.Provider, msg, ev.RequestID
		if ev.Resource != "" {
			le.Resource = ev.Resource
		}
		e.Log(le)
	}
}

// endpointOf reduces a URL to "GET /v1/things/{id}" for grouping.
func endpointOf(method, u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.IndexByte(u, '/'); j >= 0 {
			u = u[j:]
		}
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	parts := strings.Split(u, "/")
	for i, p := range parts {
		if len(p) > 16 || strings.ContainsAny(p, "0123456789") && len(p) > 6 {
			parts[i] = "{id}"
		}
	}
	return method + " " + strings.Join(parts, "/")
}

// ---- sessions ----

// Session builds the provider session for a project environment.
func (e *Engine) Session(p *core.Project, env string, man *core.Manifest, runID string) (*providers.Session, []string) {
	var missing []string
	conns := map[string]*providers.Conn{}
	links := p.Connections[env]
	base := core.LogEntry{Project: p.ID, Environment: env, RunID: runID}
	for _, prov := range p.Blueprint.Providers() {
		id := links[prov]
		if id == "" {
			missing = append(missing, prov)
			continue
		}
		c, err := e.Connection(id)
		if err != nil {
			missing = append(missing, prov)
			continue
		}
		cn, err := e.OpenConn(*c, base)
		if err != nil {
			missing = append(missing, prov)
			continue
		}
		conns[prov] = cn
	}
	s := &providers.Session{Project: p, Env: env, Manifest: man, Conns: conns, Vault: e.Vault, Now: e.Now, Poll: e.Poll}
	s.Log = func(level, provider, resource, msg string) {
		le := base
		le.Level, le.Source, le.Provider, le.Resource, le.Message = level, "engine", provider, resource, msg
		e.Log(le)
	}
	s.Progress = func(msg string) {
		le := base
		le.Level, le.Source, le.Message = "info", "build", msg
		e.Log(le)
	}
	s.Secret = func(resourceKey, output string) (string, error) {
		return e.Vault.GetString(ResourceSecretKey(p.ID, env, resourceKey, output))
	}
	if cf := conns["cloudflare"]; cf != nil {
		s.DNS = dnsFor(cf)
	}
	return s, missing
}

// ResourceSecretKey is the vault key of a resource's secret output.
func ResourceSecretKey(project, env, resource, output string) string {
	return "p/" + project + "/" + env + "/r/" + resource + "/" + output
}

// GenSecretKey is the vault key of a generated secret.
func GenSecretKey(project, env, name string) string {
	return "p/" + project + "/" + env + "/gen/" + name
}

// ---- generated code ----

// CodePath returns the on-disk path of a generated file.
func (e *Engine) CodePath(projectID, rel string) (string, error) {
	dir, err := e.Store.CodeDir(projectID)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean("/" + rel)
	return filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

// ReadCode reads a generated file.
func (e *Engine) ReadCode(projectID, rel string) (string, error) {
	p, err := e.CodePath(projectID, rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	return string(b), err
}
