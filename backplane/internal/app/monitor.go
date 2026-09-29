package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
)

// Monitor keeps checking every built, monitored environment on its schedule
// while Backplane is open (the optional Windows background task covers the
// time it is closed), records history and notifies when health changes.
type Monitor struct {
	a    *App
	tick time.Duration

	mu       sync.Mutex
	active   bool
	last     map[string]lastChecks // project/env
	versions time.Time
}

type lastChecks struct {
	quick, full time.Time
	loaded      bool
}

// MonitorState is shown on the dashboard.
type MonitorState struct {
	Enabled   bool       `json:"enabled"`
	Active    bool       `json:"active"` // the monitor loop is running
	LastQuick *time.Time `json:"lastQuick,omitempty"`
	LastFull  *time.Time `json:"lastFull,omitempty"`
	NextQuick *time.Time `json:"nextQuick,omitempty"`
	NextFull  *time.Time `json:"nextFull,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func newMonitor(a *App) *Monitor {
	return &Monitor{a: a, tick: 30 * time.Second, last: map[string]lastChecks{}}
}

func (m *Monitor) run(ctx context.Context) {
	m.mu.Lock()
	if m.active {
		m.mu.Unlock()
		return
	}
	m.active = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.active = false
		m.mu.Unlock()
	}()
	// Give the window a moment to open before the first pass.
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	t := time.NewTicker(m.tick)
	defer t.Stop()
	for {
		m.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pass runs whatever checks are due, one at a time so provider rate limits
// are respected.
func (m *Monitor) pass(ctx context.Context) {
	ps, err := m.a.Store.ListProjects()
	if err != nil {
		return
	}
	now := time.Now()
	for _, p := range ps {
		if !p.Monitor.Enabled || (p.Practice && !m.a.practiceRunning()) {
			continue
		}
		for _, env := range p.Environments {
			if ctx.Err() != nil {
				return
			}
			kind := m.due(p, env, now)
			if kind == "" || m.a.Engine.CheckRunning(p.ID, env) || m.a.runningRun(p.ID, env) != "" {
				continue
			}
			rep, err := m.a.Engine.Check(ctx, p, env, engine.CheckOptions{Kind: kind, Trigger: "schedule"})
			if err != nil {
				m.a.log("warn", p.ID, env, "Scheduled check could not run: "+err.Error())
				continue
			}
			m.observe(p, env, rep)
		}
	}
	m.mu.Lock()
	refresh := now.Sub(m.versions) > 24*time.Hour
	if refresh {
		m.versions = now
	}
	m.mu.Unlock()
	if refresh {
		go m.a.refreshVersions(context.Background())
	}
}

// due decides which check an environment needs now ("" for none).
func (m *Monitor) due(p *core.Project, env string, now time.Time) string {
	man, err := m.a.Store.LoadManifest(p.ID, env)
	if err != nil || len(man.Resources) == 0 {
		return ""
	}
	lc := m.lastFor(p.ID, env)
	full := time.Duration(p.Monitor.FullEveryHours) * time.Hour
	quick := time.Duration(p.Monitor.QuickEveryMin) * time.Minute
	if full > 0 && now.Sub(lc.full) >= full {
		return core.CheckFull
	}
	if quick > 0 && now.Sub(lc.quick) >= quick {
		return core.CheckQuick
	}
	return ""
}

func (m *Monitor) lastFor(projectID, env string) lastChecks {
	key := projectID + "/" + env
	m.mu.Lock()
	lc, ok := m.last[key]
	m.mu.Unlock()
	if ok && lc.loaded {
		return lc
	}
	hist, _ := m.a.Store.History(projectID, env, 500)
	for _, h := range hist {
		if lc.quick.IsZero() {
			lc.quick = h.At // any check refreshes the quick levels
		}
		if h.Kind == core.CheckFull && lc.full.IsZero() {
			lc.full = h.At
		}
		if !lc.quick.IsZero() && !lc.full.IsZero() {
			break
		}
	}
	lc.loaded = true
	m.mu.Lock()
	m.last[key] = lc
	m.mu.Unlock()
	return lc
}

// observe records a finished check and notifies when the overall status
// changed since the previous one.
func (m *Monitor) observe(p *core.Project, env string, rep *core.HealthReport) {
	if rep == nil {
		return
	}
	key := p.ID + "/" + env
	lc := m.lastFor(p.ID, env)
	lc.quick = rep.FinishedAt
	if rep.Kind == core.CheckFull {
		lc.full = rep.FinishedAt
	}
	m.mu.Lock()
	m.last[key] = lc
	m.mu.Unlock()
	hist, _ := m.a.Store.History(p.ID, env, 2)
	prev := core.HealthUnknown
	if len(hist) > 1 {
		prev = hist[1].Overall
	}
	m.a.Engine.Bus.Publish(engine.Event{Type: "monitor", Project: p.ID, Env: env, Data: map[string]any{"overall": rep.Overall, "headline": rep.Headline, "previous": prev}})
	if prev == rep.Overall || rep.Overall == core.HealthUnknown {
		return
	}
	if prev == core.HealthUnknown && rep.Overall == core.HealthOK {
		return // first green result is not news
	}
	title := p.Name + " (" + env + "): " + rep.Headline
	body := "All checks passed again."
	if rep.Overall != core.HealthOK {
		body = firstProblem(rep)
	}
	m.a.notifyUser(p, string(rep.Overall), title, body)
}

func firstProblem(rep *core.HealthReport) string {
	for _, want := range []core.Health{core.HealthFail, core.HealthWarn} {
		for _, r := range rep.Results {
			if r.Health == want {
				if r.Problem != nil {
					return strings.TrimSpace(r.Problem.Title + " — " + r.Problem.Summary)
				}
				return r.Title + ": " + r.Summary
			}
		}
	}
	return rep.Headline
}

// state describes monitoring for one environment.
func (m *Monitor) state(p *core.Project, env string) MonitorState {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	st := MonitorState{Enabled: p.Monitor.Enabled, Active: active}
	lc := m.lastFor(p.ID, env)
	if !lc.quick.IsZero() {
		t := lc.quick
		st.LastQuick = &t
	}
	if !lc.full.IsZero() {
		t := lc.full
		st.LastFull = &t
	}
	switch {
	case !p.Monitor.Enabled:
		st.Note = "Monitoring is off for this project."
	case p.Practice && !m.a.practiceRunning():
		st.Note = "Practice projects are monitored while the practice sandbox is running."
	case !active:
		st.Note = "Checks run while Backplane is open" + map[bool]string{true: " and from the Windows background task.", false: "."}[m.a.Store.LoadSettings().BackgroundTask]
	}
	if p.Monitor.Enabled {
		if p.Monitor.QuickEveryMin > 0 {
			t := lc.quick.Add(time.Duration(p.Monitor.QuickEveryMin) * time.Minute)
			if t.Before(time.Now()) {
				t = time.Now().Add(m.tick)
			}
			st.NextQuick = &t
		}
		if p.Monitor.FullEveryHours > 0 {
			t := lc.full.Add(time.Duration(p.Monitor.FullEveryHours) * time.Hour)
			if t.Before(time.Now()) {
				t = time.Now().Add(m.tick)
			}
			st.NextFull = &t
		}
	}
	return st
}

// notifyUser shows an in-app toast (kind: ok, warn, fail, info) and, when
// the project allows it, a desktop notification.
func (a *App) notifyUser(p *core.Project, kind, title, body string) {
	a.Engine.Bus.Publish(engine.Event{Type: "toast", Project: p.ID, Data: map[string]any{"kind": kind, "title": title, "body": body}})
	if p.Monitor.Notify && a.notify != nil {
		a.notify(title, body)
	}
}

func (a *App) practiceRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.simServer != nil
}

// CheckAll runs one quick check for every monitored, built environment and
// returns how many were checked. The Windows background task calls this
// (backplane.exe --check-all) while the app is closed.
func (a *App) CheckAll(ctx context.Context) int {
	ps, err := a.Store.ListProjects()
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range ps {
		if !p.Monitor.Enabled || p.Practice {
			continue
		}
		for _, env := range p.Environments {
			man, err := a.Store.LoadManifest(p.ID, env)
			if err != nil || len(man.Resources) == 0 {
				continue
			}
			rep, err := a.Engine.Check(ctx, p, env, engine.CheckOptions{Kind: core.CheckQuick, Trigger: "background"})
			if err != nil {
				continue
			}
			a.monitor.observe(p, env, rep)
			n++
		}
	}
	return n
}
