// Package providers defines the adapter contract every provider implements
// and a registry the rest of Backplane uses to find them. Nothing outside the
// adapters knows provider-specific API details: the planner, executor and
// verification engine talk in terms of capabilities, resource kinds and these
// interfaces, so adding a provider never touches the engine.
package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/vault"
)

// Maturity tells users exactly what an integration can do. Nothing is shown
// as available when it is not.
const (
	MaturityBuild   = "build"   // connect, discover, provision, verify, repair
	MaturityConnect = "connect" // connect, discover and monitor; no provisioning yet
	MaturityPlanned = "planned" // listed for roadmap only, clearly unavailable
)

// Field is a non-secret or secret input the connection form asks for.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Help        string   `json:"help,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	Pattern     string   `json:"pattern,omitempty"` // JS regex for inline validation
}

// GuideStep is one numbered step in a provider setup guide.
type GuideStep struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link,omitempty"`
	Label string `json:"label,omitempty"` // link label
}

// Info describes a provider to the UI and planner.
type Info struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Tagline      string            `json:"tagline"`
	Category     string            `json:"category"`
	Capabilities []core.Capability `json:"capabilities"`
	Maturity     string            `json:"maturity"`
	Phase        int               `json:"phase"`
	Fields       []Field           `json:"fields"`
	Guide        []GuideStep       `json:"guide"`
	TokenURL     string            `json:"tokenUrl,omitempty"`
	DocsURL      string            `json:"docsUrl,omitempty"`
	StatusURL    string            `json:"statusUrl,omitempty"`
	OAuth        bool              `json:"oauth"` // OAuth available in this build
	OAuthNote    string            `json:"oauthNote,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"` // minimum permissions we ask for
	APIVersion   string            `json:"apiVersion,omitempty"`
	Kinds        []KindInfo        `json:"kinds,omitempty"`
	Discovers    []string          `json:"discovers,omitempty"` // what import can find
}

// KindInfo documents a resource kind.
type KindInfo struct {
	Kind        string          `json:"kind"`
	Label       string          `json:"label"`
	Capability  core.Capability `json:"capability"`
	Destructive string          `json:"destructive,omitempty"` // what deleting it loses
}

// Conn is a live, authenticated connection to one provider account.
type Conn struct {
	Connection core.Connection
	Secret     string // decrypted credential; never logged or persisted
	Client     *httpx.Client
	// BaseURLs lets the simulator (practice mode) stand in for real hosts.
	BaseURLs map[string]string
}

// Base returns a base URL override or the default.
func (c *Conn) Base(name, def string) string {
	if c != nil && c.BaseURLs != nil {
		if v := c.BaseURLs[name]; v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return def
}

// VerifyResult is the Level 1 outcome for a connection.
type VerifyResult struct {
	Health      core.Health       `json:"health"`
	AccountID   string            `json:"accountId,omitempty"`
	AccountName string            `json:"accountName,omitempty"`
	Mode        string            `json:"mode,omitempty"`
	Scopes      []string          `json:"scopes,omitempty"`
	Missing     []string          `json:"missing,omitempty"` // permissions we need but lack
	Summary     string            `json:"summary"`
	Details     map[string]string `json:"details,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"` // discovered settings to save (account id...)
	LatencyMS   int64             `json:"latencyMs"`
	Warnings    []string          `json:"warnings,omitempty"`
	Problem     *core.Problem     `json:"problem,omitempty"`
}

// Discovered is one resource found in an existing account (import).
type Discovered struct {
	Kind     string            `json:"kind"`
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Detail   string            `json:"detail,omitempty"`
	Links    []DiscoveredLink  `json:"links,omitempty"` // relationships we can infer
	Props    map[string]string `json:"props,omitempty"`
	Group    string            `json:"group,omitempty"` // suggested grouping hint
	Provider string            `json:"provider"`
}

// DiscoveredLink is an inferred relationship ("worker store-api binds bucket downloads").
type DiscoveredLink struct {
	ToKind string `json:"toKind"`
	ToName string `json:"toName"`
	Label  string `json:"label"`
}

// Provider is implemented by every adapter.
type Provider interface {
	Info() Info
	// Connect builds an authenticated Conn from a stored connection.
	Connect(c core.Connection, secret string, opts ConnectOptions) (*Conn, error)
	// Verify runs the Level 1 checks: credential valid, right account,
	// required permissions, API reachable, rate limits acceptable.
	Verify(ctx context.Context, conn *Conn) (*VerifyResult, error)
}

// ConnectOptions are shared settings applied to every client.
type ConnectOptions struct {
	Log      func(httpx.LogEvent)
	BaseURLs map[string]string // simulator overrides
	Practice bool
}

// Discoverer is implemented by providers that can import existing resources.
type Discoverer interface {
	Discover(ctx context.Context, conn *Conn) ([]Discovered, error)
}

// Observation is the live state of a resource.
type Observation struct {
	Exists  bool              `json:"exists"`
	ID      string            `json:"id,omitempty"`
	Name    string            `json:"name,omitempty"`
	Props   map[string]any    `json:"props,omitempty"`
	Summary string            `json:"summary,omitempty"`
	Stats   map[string]string `json:"stats,omitempty"`
}

// ApplyResult is what a create/update returns.
type ApplyResult struct {
	State   *core.ResourceState
	Secrets map[string]string // output -> secret value (stored in the vault by the engine)
	Created bool
	Note    string
}

// Session gives adapters access to an environment's context while applying.
type Session struct {
	Project  *core.Project
	Env      string
	Manifest *core.Manifest
	Conns    map[string]*Conn
	Vault    *vault.Vault
	Log      func(level, provider, resource, msg string)
	Progress func(msg string)
	// Secret reads a secret output of another resource.
	Secret func(resourceKey, output string) (string, error)
	// Now is replaceable in tests.
	Now func() time.Time
	// Poll is the wait used while waiting on provider provisioning; tests
	// shorten it.
	Poll time.Duration
	// DNS publishes records through a connected DNS provider (nil if none).
	DNS DNSManager
}

// DNSRecord is a record one provider needs published at another.
type DNSRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"` // fully qualified
	Content  string `json:"content"`
	Priority int    `json:"priority,omitempty"`
	TTL      int    `json:"ttl,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
}

// DNSManager lets an adapter (say, Resend) publish records on the DNS
// provider the user connected (say, Cloudflare) without knowing its API.
type DNSManager interface {
	// Zone returns the managed zone containing host, if the provider hosts it.
	Zone(ctx context.Context, host string) (string, bool)
	// Ensure creates any missing records and returns their ids.
	Ensure(ctx context.Context, zone string, recs []DNSRecord) ([]string, error)
	// Missing reports which of the records are not published.
	Missing(ctx context.Context, zone string, recs []DNSRecord) ([]DNSRecord, error)
}

// Conn returns the connection for a provider or a clear error.
func (s *Session) Conn(provider string) (*Conn, error) {
	c := s.Conns[provider]
	if c == nil {
		return nil, &core.Problem{Title: "No " + provider + " connection", Provider: provider, Code: "auth",
			Summary: "This environment has no " + provider + " account connected yet. Connect one on the Connections screen."}
	}
	return c, nil
}

// Output reads a non-secret output of another resource in the manifest.
func (s *Session) Output(resourceKey, output string) string {
	if s.Manifest == nil {
		return ""
	}
	return s.Manifest.Resources[resourceKey].Output(output)
}

// State returns another resource's state.
func (s *Session) State(resourceKey string) *core.ResourceState {
	if s.Manifest == nil {
		return nil
	}
	return s.Manifest.Resources[resourceKey]
}

// Logf logs through the session.
func (s *Session) Logf(level, provider, resource, format string, args ...any) {
	if s.Log != nil {
		s.Log(level, provider, resource, fmt.Sprintf(format, args...))
	}
}

// Say reports progress to the build screen.
func (s *Session) Say(format string, args ...any) {
	if s.Progress != nil {
		s.Progress(fmt.Sprintf(format, args...))
	}
}

// PollEvery returns the provisioning poll interval.
func (s *Session) PollEvery() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 5 * time.Second
}

// Handler provisions and inspects one resource kind.
type Handler interface {
	Kind() string
	// Observe reads live state. A missing resource is Exists=false, not an error.
	Observe(ctx context.Context, s *Session, spec *core.ResourceSpec, st *core.ResourceState) (*Observation, error)
	// Apply creates or updates the resource to match spec. It must be safe to
	// call again after a crash: look for an existing resource first.
	Apply(ctx context.Context, s *Session, spec *core.ResourceSpec, st *core.ResourceState) (*ApplyResult, error)
	// Delete removes the resource.
	Delete(ctx context.Context, s *Session, st *core.ResourceState) error
	// Drift compares expected with live configuration (Level 3).
	Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *Observation) []core.DriftItem
}

// Registry holds providers and resource handlers.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	handlers  map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}, handlers: map[string]Handler{}}
}

// Add registers a provider and its handlers.
func (r *Registry) Add(p Provider, hs ...Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Info().ID] = p
	for _, h := range hs {
		r.handlers[h.Kind()] = h
	}
}

// Provider returns a provider by id.
func (r *Registry) Provider(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// Handler returns the handler for a resource kind.
func (r *Registry) Handler(kind string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[kind]
	return h, ok
}

// All returns provider infos sorted by phase then name.
func (r *Registry) All() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ForCapability lists providers that implement a capability, best first.
func (r *Registry) ForCapability(c core.Capability) []Info {
	var out []Info
	for _, i := range r.All() {
		for _, x := range i.Capabilities {
			if x == c {
				out = append(out, i)
				break
			}
		}
	}
	return out
}
