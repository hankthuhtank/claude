package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
)

// CheckOptions controls a verification run.
type CheckOptions struct {
	Kind    string // quick | full
	Trigger string // manual | schedule | after-build | after-change
	// AllowLiveProbes permits the no-charge Stripe delivery probe
	// (create + expire a Checkout Session) in live mode.
	AllowLiveProbes bool
	// Levels limits the run to these levels (empty = all for the kind).
	Levels []int
}

// Checker is the context shared by all checks in one verification run.
type Checker struct {
	e       *Engine
	P       *core.Project
	Env     string
	Man     *core.Manifest
	Sess    *providers.Session
	Opts    CheckOptions
	ctx     context.Context
	mu      sync.Mutex
	results []core.CheckResult
	obs     map[string]*providers.Observation
	drift   []core.DriftItem
	cleanup []func(context.Context) error
	health  *ProbeHealth // cached worker health report
	runID   string
}

// LinkCheck proves one link between components.
type LinkCheck func(c *Checker, link *core.LinkSpec) core.CheckResult

// SyntheticCheck is a Level 5 functional test.
type SyntheticCheck struct {
	ID       string
	Title    string
	Target   string // component key
	Provider string
	Applies  func(bp *core.Blueprint) bool
	Run      func(c *Checker) core.CheckResult
}

// Scenario is a Level 6 end-to-end journey.
type Scenario struct {
	ID    string
	Title string
	Run   func(c *Checker, sc *core.ScenarioSpec) core.CheckResult
}

var (
	linkChecks = map[string]LinkCheck{}
	synthetics []SyntheticCheck
	scenarios  = map[string]Scenario{}
)

// RegisterLinkCheck adds a link check by id.
func RegisterLinkCheck(id string, fn LinkCheck) { linkChecks[id] = fn }

// RegisterSynthetic adds a synthetic test.
func RegisterSynthetic(s SyntheticCheck) { synthetics = append(synthetics, s) }

// RegisterScenario adds an end-to-end scenario.
func RegisterScenario(s Scenario) { scenarios[s.ID] = s }

func (c *Checker) add(r core.CheckResult) {
	c.mu.Lock()
	c.results = append(c.results, r)
	c.mu.Unlock()
	c.e.Bus.Publish(Event{Type: "check", Project: c.P.ID, Env: c.Env, RunID: c.runID, Data: r})
}

// Defer registers a cleanup that always runs at the end of the check run.
func (c *Checker) Defer(fn func(context.Context) error) {
	c.mu.Lock()
	c.cleanup = append(c.cleanup, fn)
	c.mu.Unlock()
}

// Ctx returns the run context.
func (c *Checker) Ctx() context.Context { return c.ctx }

// Log writes a check log line.
func (c *Checker) Log(level, provider, msg string) {
	c.e.Log(core.LogEntry{Level: level, Source: "check", Project: c.P.ID, Environment: c.Env, RunID: c.runID, Provider: provider, Message: msg})
}

// State returns a resource state from the manifest.
func (c *Checker) State(key string) *core.ResourceState { return c.Man.Resources[key] }

// Secret reads a resource secret output.
func (c *Checker) Secret(resourceKey, output string) (string, error) {
	return c.e.Vault.GetString(ResourceSecretKey(c.P.ID, c.Env, resourceKey, output))
}

// Gen reads a generated secret.
func (c *Checker) Gen(name string) (string, error) {
	return c.e.Vault.GetString(GenSecretKey(c.P.ID, c.Env, name))
}

// Full reports whether this is a full check.
func (c *Checker) Full() bool { return c.Opts.Kind == core.CheckFull }

// Conn returns a provider connection.
func (c *Checker) Conn(p string) *providers.Conn { return c.Sess.Conns[p] }

// Problem enriches a problem with impact from the architecture graph.
func (c *Checker) Problem(target string, prob *core.Problem) *core.Problem {
	if prob == nil {
		return nil
	}
	if len(prob.Affected) == 0 && target != "" {
		prob.Affected, prob.Unaffected = c.P.Blueprint.ImpactOf(target)
	}
	return prob
}

func levelWanted(opts CheckOptions, level int) bool {
	if len(opts.Levels) > 0 {
		for _, l := range opts.Levels {
			if l == level {
				return true
			}
		}
		return false
	}
	if opts.Kind == core.CheckQuick {
		return level <= core.LevelLinks
	}
	return true
}

// Check runs the green-light verification for one environment and stores the
// report and history. Only one check per environment runs at a time.
func (e *Engine) Check(ctx context.Context, p *core.Project, env string, opts CheckOptions) (*core.HealthReport, error) {
	if opts.Kind == "" {
		opts.Kind = core.CheckQuick
	}
	if opts.Trigger == "" {
		opts.Trigger = "manual"
	}
	lockKey := p.ID + "/" + env
	e.mu.Lock()
	if _, busy := e.checks[lockKey]; busy {
		e.mu.Unlock()
		return nil, fmt.Errorf("a health check is already running for %s", env)
	}
	cctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	e.checks[lockKey] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.checks, lockKey)
		e.mu.Unlock()
	}()

	man, err := e.Store.LoadManifest(p.ID, env)
	if err != nil {
		return nil, err
	}
	reportID := NewID("rep")
	sess, missing := e.Session(p, env, man, reportID)
	c := &Checker{e: e, P: p, Env: env, Man: man, Sess: sess, Opts: opts, ctx: cctx, obs: map[string]*providers.Observation{}, runID: reportID}
	rep := &core.HealthReport{ID: reportID, Project: p.ID, Environment: env, Kind: opts.Kind, Trigger: opts.Trigger, StartedAt: e.Now().UTC(),
		Components: map[string]core.Health{}, Links: map[string]core.Health{}, Levels: map[int]core.Health{}}
	e.Bus.Publish(Event{Type: "check.start", Project: p.ID, Env: env, RunID: reportID, Data: map[string]any{"kind": opts.Kind}})
	c.Log("info", "", fmt.Sprintf("%s health check started (%s)", titleCase(opts.Kind), opts.Trigger))

	for _, m := range missing {
		sev := core.HealthFail
		if !providerHasResources(&p.Blueprint, m) {
			sev = core.HealthWarn
		}
		c.add(core.CheckResult{ID: "conn:" + m, Level: core.LevelConnection, Title: providers.DisplayName(m) + " connected", Target: m, TargetType: "provider", Provider: m,
			Health: sev, Summary: "No " + providers.DisplayName(m) + " account is connected to " + env + ".",
			Problem: &core.Problem{Title: strings.ToUpper(providers.DisplayName(m)) + " NOT CONNECTED", Code: "auth", Provider: m, Summary: "Connect an account for this environment on the Connections screen.",
				Fixes: []core.Fix{{ID: "connect:" + m, Label: "Connect " + providers.DisplayName(m), Action: "reconnect", Target: m}}}})
	}
	if levelWanted(opts, core.LevelConnection) {
		c.level1()
	}
	built := len(man.Resources) > 0
	if built && levelWanted(opts, core.LevelExistence) {
		c.level2and3(levelWanted(opts, core.LevelConfig))
	}
	coreMissing := anyResourceProviderMissing(&p.Blueprint, missing)
	if built && levelWanted(opts, core.LevelLinks) && !coreMissing {
		c.level4()
	}
	if built && opts.Kind == core.CheckFull && levelWanted(opts, core.LevelSynthetic) && !coreMissing {
		c.level5()
	}
	if built && opts.Kind == core.CheckFull && levelWanted(opts, core.LevelEndToEnd) && !coreMissing {
		c.level6()
	}
	// Cleanup synthetic data no matter what happened.
	cleanCtx, cancelClean := context.WithTimeout(context.Background(), 90*time.Second)
	for i := len(c.cleanup) - 1; i >= 0; i-- {
		if err := c.cleanup[i](cleanCtx); err != nil {
			c.Log("warn", "", "Cleanup step failed: "+err.Error()+" — Backplane will retry cleanup on the next full check.")
		}
	}
	cancelClean()

	e.aggregate(p, rep, c)
	if !built {
		rep.Headline = "NOT BUILT YET"
		if rep.Overall == core.HealthOK {
			rep.Overall = core.HealthUnknown
		}
	}
	rep.FinishedAt = e.Now().UTC()
	note := historyNote(e, p, env, rep)
	if opts.Kind == core.CheckFull && built {
		t := rep.FinishedAt
		man.LastFullCheck = &t
		// Record provider link health for the dashboard.
		_ = e.Store.SaveManifest(man)
		if rep.Overall == core.HealthOK {
			_, _ = e.Snapshot(p, env, man, "Known good — full certification passed", true)
		}
	}
	if err := e.Store.SaveReport(rep, note); err != nil {
		return nil, err
	}
	e.Store.PruneReports(p.ID, env, 60)
	level := map[core.Health]string{core.HealthOK: "info", core.HealthWarn: "warn", core.HealthFail: "error"}[rep.Overall]
	c.Log(level, "", "Health check finished: "+rep.Headline)
	e.Bus.Publish(Event{Type: "report", Project: p.ID, Env: env, RunID: rep.ID, Data: rep})
	return rep, nil
}

// CheckRunning reports whether a check is running for an environment.
func (e *Engine) CheckRunning(projectID, env string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.checks[projectID+"/"+env]
	return ok
}

// CancelCheck stops a running check.
func (e *Engine) CancelCheck(projectID, env string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.checks[projectID+"/"+env]; ok {
		c()
		return true
	}
	return false
}

// historyNote explains what changed since the previous report ("Stripe API
// version changed", "Webhook verification failed").
func historyNote(e *Engine, p *core.Project, env string, rep *core.HealthReport) string {
	hist, _ := e.Store.History(p.ID, env, 1)
	var failing []string
	for _, r := range rep.Results {
		if r.Health == core.HealthFail {
			failing = append(failing, r.Title)
		}
	}
	if len(failing) > 0 {
		sort.Strings(failing)
		if len(failing) > 2 {
			return failing[0] + " and " + fmt.Sprint(len(failing)-1) + " more failing"
		}
		return strings.Join(failing, "; ") + " failing"
	}
	var warn []string
	for _, r := range rep.Results {
		if r.Health == core.HealthWarn {
			warn = append(warn, r.Title)
		}
	}
	if len(warn) > 0 {
		sort.Strings(warn)
		return warn[0] + ifMore(len(warn))
	}
	if len(hist) > 0 && hist[0].Overall != core.HealthOK && rep.Overall == core.HealthOK {
		return "Recovered"
	}
	return ""
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ifMore(n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf(" (+%d more)", n-1)
}

// ---- Level 1 ----

func (c *Checker) level1() {
	var wg sync.WaitGroup
	for prov, conn := range c.Sess.Conns {
		p, ok := c.e.Registry.Provider(prov)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(prov string, p providers.Provider, conn *providers.Conn) {
			defer wg.Done()
			start := time.Now()
			vr, err := p.Verify(c.ctx, conn)
			r := core.CheckResult{ID: "conn:" + prov, Level: core.LevelConnection, Title: providers.DisplayName(prov) + " connected", Target: prov, TargetType: "provider",
				Provider: prov, StartedAt: start.UTC(), DurationMS: time.Since(start).Milliseconds()}
			if err != nil {
				r.Health = core.HealthFail
				r.Problem = providers.Translate(prov, "verify the connection", err)
				r.Summary = r.Problem.Summary
			} else {
				r.Health, r.Summary, r.LatencyMS, r.Problem = vr.Health, vr.Summary, vr.LatencyMS, vr.Problem
				r.Details = vr.Details
				if len(vr.Warnings) > 0 {
					if r.Details == nil {
						r.Details = map[string]string{}
					}
					r.Details["warnings"] = strings.Join(vr.Warnings, "\n")
				}
				// Environment separation: a live key must never serve tests and
				// production must not run on test payments.
				if prov == "stripe" && vr.Mode != "" {
					if core.IsProduction(c.Env) && vr.Mode == "test" && !c.P.Practice && c.P.Blueprint.Param("allow_test_payments_in_production") != "true" {
						r.Health = core.Worst(r.Health, core.HealthWarn)
						r.Summary += " Production is using TEST mode — customers cannot really pay."
					}
					if !core.IsProduction(c.Env) && vr.Mode == "live" {
						r.Health = core.HealthFail
						r.Problem = &core.Problem{Title: "LIVE STRIPE KEY OUTSIDE PRODUCTION", Provider: "stripe", Code: "invalid",
							Summary: "The " + c.Env + " environment uses a live Stripe key. Development and staging must use test keys so tests can never charge real cards."}
						r.Summary = r.Problem.Summary
					}
				}
			}
			if r.Problem != nil && len(r.Problem.Affected) == 0 {
				r.Problem.Affected, r.Problem.Unaffected = c.providerImpact(prov)
			}
			c.add(r)
		}(prov, p, conn)
	}
	wg.Wait()
}

// providerImpact lists capabilities that break when a provider is unreachable.
func (c *Checker) providerImpact(prov string) (affected, unaffected []string) {
	bp := &c.P.Blueprint
	if a, u := bp.ImpactOfProvider(prov); len(a) > 0 {
		return a, u
	}
	broken := map[string]bool{}
	for _, comp := range bp.Components {
		if comp.Provider == prov {
			broken[comp.Key] = true
			for _, d := range bp.Downstream(comp.Key) {
				broken[d] = true
			}
		}
	}
	seenA, seenU := map[string]bool{}, map[string]bool{}
	for _, comp := range bp.Components {
		if comp.External {
			continue
		}
		label := comp.Role
		if label == "" {
			label = comp.Label
		}
		if broken[comp.Key] {
			if !seenA[label] {
				affected = append(affected, label)
				seenA[label] = true
			}
		} else if !seenU[label] {
			unaffected = append(unaffected, label)
			seenU[label] = true
		}
	}
	return
}

// ---- Levels 2 & 3 ----

func (c *Checker) level2and3(withConfig bool) {
	bp := &c.P.Blueprint
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range bp.Resources {
		spec := &bp.Resources[i]
		st := c.Man.Resources[spec.Key]
		if st == nil {
			c.add(core.CheckResult{ID: "exists:" + spec.Key, Level: core.LevelExistence, Title: spec.Title, Target: spec.Component, TargetType: "resource", Provider: spec.Provider,
				Health: core.HealthWarn, Summary: "Not built yet. Run a build to create it.", Details: map[string]string{"resource": spec.Key}})
			continue
		}
		h, ok := c.e.Registry.Handler(spec.Kind)
		if !ok || c.Sess.Conns[spec.Provider] == nil {
			continue
		}
		wg.Add(1)
		go func(spec *core.ResourceSpec, st *core.ResourceState, h providers.Handler) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			rs, err := c.e.resolveSpec(c.P, c.Env, c.Man, spec, forHash, false)
			if err != nil {
				rs = spec
			}
			obs, err := h.Observe(c.ctx, c.Sess, rs, st)
			r := core.CheckResult{ID: "exists:" + spec.Key, Level: core.LevelExistence, Title: spec.Title, Target: spec.Component, TargetType: "resource", Provider: spec.Provider,
				StartedAt: start.UTC(), DurationMS: time.Since(start).Milliseconds(), Details: map[string]string{"resource": spec.Key, "id": st.ID}}
			switch {
			case err != nil:
				r.Health = core.HealthFail
				r.Problem = c.Problem(spec.Component, providers.Translate(spec.Provider, "look up "+strings.ToLower(spec.Title), err))
				r.Summary = r.Problem.Summary
			case !obs.Exists:
				r.Health = core.HealthFail
				r.Summary = "Missing — deleted or renamed outside Backplane."
				r.Expected, r.Actual = "exists ("+st.ID+")", "not found"
				r.Problem = c.Problem(spec.Component, &core.Problem{Title: strings.ToUpper(spec.Title) + " IS MISSING", Provider: spec.Provider, Code: "not_found",
					Summary: spec.Title + " (" + st.ID + ") no longer exists in " + providers.DisplayName(spec.Provider) + ". It was probably deleted or renamed outside Backplane.",
					Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Recreate it", Explain: "Backplane plans the recreation first so you can review it; dependent settings (webhook URLs, bindings) are restored too.", Automatic: true, Action: "repair", Target: spec.Key,
						Changes: []string{"Create " + spec.Title + " again", "Reconnect everything that pointed at it"}}}})
				if providers.Bool(rs.Props, "__imported") {
					r.Problem.Fixes = nil
					r.Problem.Summary += " Backplane only monitors imported resources, so recreate it where it was made (or rebuild it from a preset)."
				}
			default:
				r.Health = core.HealthOK
				r.Summary = "Exists"
				if obs.Summary != "" {
					r.Summary += " · " + obs.Summary
				}
				for k, v := range obs.Stats {
					r.Details[k] = v
				}
			}
			c.mu.Lock()
			c.obs[spec.Key] = obs
			c.mu.Unlock()
			c.add(r)
			// Imported resources have no expected configuration to compare.
			if withConfig && err == nil && obs != nil && obs.Exists && !providers.Bool(rs.Props, "__imported") {
				items := h.Drift(rs, st, obs)
				cr := core.CheckResult{ID: "config:" + spec.Key, Level: core.LevelConfig, Title: spec.Title + " configuration", Target: spec.Component, TargetType: "resource", Provider: spec.Provider,
					StartedAt: start.UTC(), Details: map[string]string{"resource": spec.Key}}
				if len(items) == 0 {
					cr.Health = core.HealthOK
					cr.Summary = "Matches the expected configuration"
				} else {
					sev := core.HealthOK
					var lines []string
					for _, d := range items {
						sev = core.Worst(sev, d.Severity)
						lines = append(lines, d.Field+": expected "+d.Expected+", actual "+d.Actual)
					}
					cr.Health = sev
					cr.Summary = fmt.Sprintf("%d difference(s) from what Backplane set up", len(items))
					cr.Expected, cr.Actual = items[0].Expected, items[0].Actual
					cr.Details["drift"] = strings.Join(lines, "\n")
					prob := &core.Problem{Title: "CHANGE DETECTED: " + strings.ToUpper(spec.Title), Provider: spec.Provider, Code: "drift",
						Summary: items[0].Field + " changed outside Backplane (expected " + items[0].Expected + ", now " + items[0].Actual + ")."}
					if len(items[0].Breaks) > 0 {
						prob.Summary += " This breaks: " + strings.Join(items[0].Breaks, "; ") + "."
					}
					if items[0].FixID != "" {
						prob.Fixes = append(prob.Fixes, core.Fix{ID: items[0].FixID, Label: orStr(items[0].Recommended, "Restore expected configuration"), Automatic: true, Action: "repair", Target: spec.Key,
							Explain: "Backplane re-applies only this resource's expected configuration. Nothing else is touched.", Changes: driftChanges(items)})
					}
					cr.Problem = c.Problem(spec.Component, prob)
					c.mu.Lock()
					c.drift = append(c.drift, items...)
					c.mu.Unlock()
				}
				c.add(cr)
			}
		}(spec, st, h)
	}
	wg.Wait()
}

func driftChanges(items []core.DriftItem) []string {
	var out []string
	for _, d := range items {
		out = append(out, "Set "+d.Field+" back to "+d.Expected+" (currently "+d.Actual+")")
	}
	return out
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- Level 4 ----

func (c *Checker) level4() {
	var wg sync.WaitGroup
	for i := range c.P.Blueprint.Links {
		l := &c.P.Blueprint.Links[i]
		fn, ok := linkChecks[l.Check]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(l *core.LinkSpec, fn LinkCheck) {
			defer wg.Done()
			start := time.Now()
			r := fn(c, l)
			r.ID = "link:" + l.Key
			r.Level = core.LevelLinks
			r.Target, r.TargetType = l.Key, "link"
			if r.Title == "" {
				from, to := c.P.Blueprint.ComponentByKey(l.From), c.P.Blueprint.ComponentByKey(l.To)
				if from != nil && to != nil {
					r.Title = from.Label + " → " + to.Label
				}
			}
			r.StartedAt = start.UTC()
			r.DurationMS = time.Since(start).Milliseconds()
			if r.Problem != nil && len(r.Problem.Affected) == 0 {
				r.Problem.Affected, r.Problem.Unaffected = c.P.Blueprint.ImpactOf(l.Key)
			}
			c.add(r)
		}(l, fn)
	}
	wg.Wait()
}

// ---- Level 5 ----

func (c *Checker) level5() {
	for _, s := range synthetics {
		if s.Applies != nil && !s.Applies(&c.P.Blueprint) {
			continue
		}
		if c.ctx.Err() != nil {
			return
		}
		start := time.Now()
		r := s.Run(c)
		r.ID = "synthetic:" + s.ID
		r.Level = core.LevelSynthetic
		if r.Title == "" {
			r.Title = s.Title
		}
		r.Target, r.TargetType, r.Provider = s.Target, "component", s.Provider
		r.StartedAt = start.UTC()
		r.DurationMS = time.Since(start).Milliseconds()
		if r.Problem != nil && len(r.Problem.Affected) == 0 {
			r.Problem.Affected, r.Problem.Unaffected = c.P.Blueprint.ImpactOf(s.Target)
		}
		c.add(r)
	}
}

// ---- Level 6 ----

func (c *Checker) level6() {
	for i := range c.P.Blueprint.Scenarios {
		sc := &c.P.Blueprint.Scenarios[i]
		s, ok := scenarios[sc.Check]
		if !ok {
			continue
		}
		start := time.Now()
		r := s.Run(c, sc)
		r.ID = "e2e:" + sc.Key
		r.Level = core.LevelEndToEnd
		r.Title = sc.Title
		r.Target, r.TargetType = sc.Key, "scenario"
		r.StartedAt = start.UTC()
		r.DurationMS = time.Since(start).Milliseconds()
		c.add(r)
	}
}

// ---- aggregation ----

func (e *Engine) aggregate(p *core.Project, rep *core.HealthReport, c *Checker) {
	rep.Results = c.results
	sort.SliceStable(rep.Results, func(i, j int) bool {
		if rep.Results[i].Level != rep.Results[j].Level {
			return rep.Results[i].Level < rep.Results[j].Level
		}
		return rep.Results[i].Title < rep.Results[j].Title
	})
	rep.Drift = c.drift
	bp := &p.Blueprint
	compResults := map[string][]core.Health{}
	for _, r := range rep.Results {
		lvl := rep.Levels[r.Level]
		if lvl == "" {
			lvl = core.HealthSkipped
		}
		rep.Levels[r.Level] = worstNoUnknown(lvl, r.Health)
		switch r.TargetType {
		case "link":
			rep.Links[r.Target] = core.Worst(rep.Links[r.Target], r.Health)
		case "provider":
			for _, comp := range bp.Components {
				if comp.Provider == r.Target {
					compResults[comp.Key] = append(compResults[comp.Key], r.Health)
				}
			}
		case "resource", "component":
			if r.Target != "" {
				compResults[r.Target] = append(compResults[r.Target], r.Health)
			}
		}
		if r.Problem != nil && r.Health == core.HealthFail {
			rep.Problems = append(rep.Problems, *r.Problem)
		}
	}
	for _, comp := range bp.Components {
		if comp.External {
			continue
		}
		rep.Components[comp.Key] = core.Worst(compResults[comp.Key]...)
	}
	// A link is only as healthy as its endpoints allow for display; but keep
	// the link's own state separate: a service can be healthy while the
	// connection to it is broken.
	overall := core.HealthOK
	critical := false
	for _, r := range rep.Results {
		if r.Health == core.HealthFail {
			overall = core.HealthFail
			if r.TargetType == "link" {
				if l := bp.LinkByKey(r.Target); l != nil && l.Critical {
					critical = true
				}
			} else {
				critical = true
			}
		} else if r.Health == core.HealthWarn && overall == core.HealthOK {
			overall = core.HealthWarn
		}
	}
	if len(rep.Results) == 0 {
		overall = core.HealthUnknown
	}
	rep.Overall = overall
	e2eOK := false
	hasE2E := false
	for _, r := range rep.Results {
		if r.Level == core.LevelEndToEnd {
			hasE2E = true
			e2eOK = r.Health == core.HealthOK
		}
	}
	switch {
	case overall == core.HealthFail && critical:
		rep.Headline = "BROKEN"
	case overall == core.HealthFail:
		rep.Headline = "DEGRADED"
	case overall == core.HealthWarn:
		rep.Headline = "NEEDS ATTENTION"
	case overall == core.HealthOK && rep.Kind == core.CheckFull && (e2eOK || !hasE2E && len(bp.Scenarios) == 0):
		rep.Headline = "FULLY OPERATIONAL"
	case overall == core.HealthOK:
		rep.Headline = "OPERATIONAL"
	default:
		rep.Headline = "UNKNOWN"
	}
	rep.Capabilities = capabilitySummary(bp, rep)
}

func worstNoUnknown(a, b core.Health) core.Health {
	if a == core.HealthSkipped {
		return b
	}
	if b == core.HealthSkipped {
		return a
	}
	return core.Worst(a, b)
}

// capabilitySummary produces the SYSTEM HEALTH block.
func capabilitySummary(bp *core.Blueprint, rep *core.HealthReport) []core.CapabilityHealth {
	type bucket struct {
		label string
		hs    []core.Health
	}
	var order []string
	buckets := map[string]*bucket{}
	add := func(label string, h core.Health) {
		if label == "" {
			return
		}
		if buckets[label] == nil {
			buckets[label] = &bucket{label: label}
			order = append(order, label)
		}
		buckets[label].hs = append(buckets[label].hs, h)
	}
	capLabel := func(cap core.Capability) string {
		switch cap {
		case core.CapPayments:
			return "Payments"
		case core.CapWebhooks:
			return "Webhooks"
		case core.CapDatabase:
			return "Database"
		case core.CapStorage:
			return "Storage"
		case core.CapEmail:
			return "Email"
		case core.CapAuth:
			return "Accounts & sign-in"
		case core.CapAPI, core.CapFunctions, core.CapCompute:
			return "API"
		case core.CapCICD, core.CapDeployment:
			return "Deployment"
		}
		if info, ok := core.CapabilityByID(cap); ok {
			return info.Label
		}
		return string(cap)
	}
	for _, comp := range bp.Components {
		if comp.External {
			continue
		}
		add(capLabel(comp.Capability), rep.Components[comp.Key])
	}
	for _, l := range bp.Links {
		if l.Kind == "webhook" {
			add("Webhooks", rep.Links[l.Key])
		}
	}
	for _, r := range rep.Results {
		if r.Level == core.LevelEndToEnd {
			for _, s := range r.Steps {
				if strings.Contains(strings.ToLower(s.Title), "download") {
					add("Download delivery", s.Health)
				}
			}
		}
	}
	var out []core.CapabilityHealth
	for _, k := range order {
		out = append(out, core.CapabilityHealth{Label: k, Health: core.Worst(buckets[k].hs...)})
	}
	return out
}
