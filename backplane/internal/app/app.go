// Package app is Backplane's application service: every action the UI can
// take is a method here, exposed over the local RPC endpoint. It owns the
// engine, the vault, persistence, practice mode and the monitor.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/all"
	"safisolutions.org/backplane/internal/sim"
	"safisolutions.org/backplane/internal/store"
	"safisolutions.org/backplane/internal/vault"
)

// Version is the application version (set at build time).
var Version = "1.0.0"

// App is the service behind the UI.
type App struct {
	Store  *store.Store
	Vault  *vault.Vault
	Redact *vault.Redactor
	Engine *engine.Engine
	Reg    *providers.Registry

	mu        sync.Mutex
	plans     map[string]*cachedPlan
	simServer *sim.Server
	monitor   *Monitor
	notify    func(title, body string)
	started   time.Time
}

type cachedPlan struct {
	plan    *core.Plan
	project string
	env     string
	created time.Time
	repair  bool
}

// Options configure New.
type Options struct {
	DataDir string
	// Notify shows a desktop notification (optional).
	Notify func(title, body string)
}

// New opens (or creates) Backplane's data and wires every part together.
func New(opts Options) (*App, error) {
	dir := opts.DataDir
	if dir == "" {
		dir = store.DefaultDir()
	}
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	red := vault.NewRedactor()
	v, err := vault.Open(dir, red)
	if err != nil {
		return nil, err
	}
	reg := all.Registry()
	eng := engine.New(st, v, reg, red)
	a := &App{Store: st, Vault: v, Redact: red, Engine: eng, Reg: reg, plans: map[string]*cachedPlan{}, notify: opts.Notify, started: time.Now()}
	a.monitor = newMonitor(a)
	st.PruneLogs(45)
	a.resetPracticeIfStale()
	return a, nil
}

// StartMonitor begins continuous checking.
func (a *App) StartMonitor(ctx context.Context) { go a.monitor.run(ctx) }

// Close releases resources.
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.simServer != nil {
		a.simServer.Close()
	}
}

// ---- RPC dispatch ----

// Call invokes a method by name with JSON params. Methods are exported App
// methods with signature func(ctx, Params) (Result, error) or func(ctx) (Result, error).
func (a *App) Call(ctx context.Context, method string, params json.RawMessage) (any, error) {
	m := reflect.ValueOf(a).MethodByName(method)
	if !m.IsValid() || !rpcAllowed[method] {
		return nil, fmt.Errorf("unknown method %s", method)
	}
	t := m.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}
	if t.NumIn() == 2 {
		pv := reflect.New(t.In(1))
		if len(params) > 0 && string(params) != "null" {
			if err := json.Unmarshal(params, pv.Interface()); err != nil {
				return nil, fmt.Errorf("bad parameters for %s: %w", method, err)
			}
		}
		args = append(args, pv.Elem())
	}
	out := m.Call(args)
	var err error
	if e := out[len(out)-1].Interface(); e != nil {
		err = e.(error)
	}
	if len(out) == 1 {
		return nil, err
	}
	return out[0].Interface(), err
}

// rpcAllowed lists methods the UI may call.
var rpcAllowed = map[string]bool{}

func init() {
	for _, m := range []string{
		"Bootstrap", "SaveSettings", "Catalog", "Providers",
		"ListConnections", "AddConnection", "UpdateConnection", "DeleteConnection", "VerifyConnection",
		"ListProjects", "GetProject", "CreateProject", "UpdateProject", "DeleteProject", "AssignConnection", "AddEnvironment",
		"Plan", "Approve", "GetRun", "ListRuns", "Resume", "Cancel", "Rollback",
		"Check", "CancelCheck", "LatestReport", "GetReport", "History", "Repair",
		"Logs", "Code", "RegenerateCode", "UploadProductFile",
		"Discover", "ImportProject", "Export", "Versions", "Security",
		"StartPractice", "StopPractice", "PracticeBreak", "PracticeState",
		"Interpret", "DesignFromDescription", "SetAIKey", "AIState", "Snapshots", "RestoreSnapshot", "Dashboard",
		"OpenExternal", "SystemInfo", "SaveCode", "OpenCodeFolder", "ShowFile",
	} {
		rpcAllowed[m] = true
	}
}

// ---- bootstrap ----

// BootstrapResult is everything the UI needs on launch.
type BootstrapResult struct {
	Version     string            `json:"version"`
	Settings    store.Settings    `json:"settings"`
	Projects    []ProjectSummary  `json:"projects"`
	Connections []ConnectionView  `json:"connections"`
	Vault       string            `json:"vault"`
	DataDir     string            `json:"dataDir"`
	OS          string            `json:"os"`
	Practice    bool              `json:"practiceRunning"`
	LevelNames  map[int]string    `json:"levelNames"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// Bootstrap loads initial state.
func (a *App) Bootstrap(ctx context.Context) (*BootstrapResult, error) {
	ps, err := a.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := a.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	practice := a.simServer != nil
	a.mu.Unlock()
	return &BootstrapResult{Version: Version, Settings: a.Store.LoadSettings(), Projects: ps, Connections: cs, Vault: a.Vault.Scheme(), DataDir: a.Store.Dir,
		OS: runtime.GOOS, Practice: practice, LevelNames: core.LevelNames}, nil
}

// SaveSettings stores preferences.
func (a *App) SaveSettings(ctx context.Context, s store.Settings) (store.Settings, error) {
	cur := a.Store.LoadSettings()
	if s.Theme == "" {
		s.Theme = cur.Theme
	}
	if s.TextScale == 0 {
		s.TextScale = 1
	}
	if s.BackgroundTask != cur.BackgroundTask {
		if err := setBackgroundTask(s.BackgroundTask); err != nil {
			return cur, err
		}
	}
	return s, a.Store.SaveSettings(s)
}

// SyncBackgroundTask re-points the hourly background check at this copy of
// Backplane (it may have been reinstalled somewhere else). Errors are ignored:
// the Settings screen reports them when the user changes the option.
func (a *App) SyncBackgroundTask() {
	if a.Store.LoadSettings().BackgroundTask {
		_ = setBackgroundTask(true)
	}
}

// SystemInfo reports environment facts for the About panel.
func (a *App) SystemInfo(ctx context.Context) (map[string]any, error) {
	exe, _ := os.Executable()
	return map[string]any{"version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "dataDir": a.Store.Dir,
		"exe": exe, "vault": a.Vault.Scheme(), "uptime": time.Since(a.started).Round(time.Second).String(), "secrets": len(a.Vault.List(""))}, nil
}

// OpenExternalParams opens a URL in the default browser.
type OpenExternalParams struct {
	URL string `json:"url"`
}

// OpenExternal opens https links in the user's browser.
func (a *App) OpenExternal(ctx context.Context, p OpenExternalParams) (bool, error) {
	if !strings.HasPrefix(p.URL, "https://") {
		return false, fmt.Errorf("only https links can be opened")
	}
	return true, openBrowser(p.URL)
}

// log writes an app-level log line.
func (a *App) log(level, project, env, msg string) {
	a.Engine.Log(core.LogEntry{Level: level, Source: "app", Project: project, Environment: env, Message: msg})
}

func sortedEnvs(p *core.Project) []string {
	envs := append([]string{}, p.Environments...)
	rank := map[string]int{core.EnvProduction: 0, core.EnvStaging: 1, core.EnvDevelopment: 2}
	sort.SliceStable(envs, func(i, j int) bool {
		ri, ok1 := rank[envs[i]]
		rj, ok2 := rank[envs[j]]
		if !ok1 {
			ri = 9
		}
		if !ok2 {
			rj = 9
		}
		return ri < rj
	})
	return envs
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
