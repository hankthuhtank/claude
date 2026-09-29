package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/stripe"
)

// PlanOptions tunes planning.
type PlanOptions struct {
	// Offline skips reading live state (fast preview before accounts are connected).
	Offline bool
	// Repair focuses the plan on these resources and what depends on them.
	Repair []RepairTarget
	// Purpose is shown above the plan.
	Purpose string
	// Teardown plans the removal of everything in the environment: resources
	// Backplane created are deleted (dependents first); anything it adopted
	// or shares with the account is only detached.
	Teardown bool
}

// Plan compares the blueprint with the manifest and live state and returns
// the ordered operations needed. It never changes anything.
func (e *Engine) Plan(ctx context.Context, p *core.Project, env string, opts PlanOptions) (*core.Plan, error) {
	bp := &p.Blueprint
	if err := bp.ValidateGraph(); err != nil {
		return nil, &core.Problem{Title: "The blueprint has a mistake", Code: "invalid", Summary: err.Error()}
	}
	man, err := e.Store.LoadManifest(p.ID, env)
	if err != nil {
		return nil, err
	}
	plan := &core.Plan{ID: NewID("plan"), Project: p.ID, Environment: env, CreatedAt: e.Now().UTC(), Production: core.IsProduction(env), Counts: map[string]int{},
		Purpose: opts.Purpose, Repair: len(opts.Repair) > 0}
	targets := map[string]RepairTarget{}
	for _, t := range opts.Repair {
		targets[t.Key] = t
	}
	inRepair := map[string]bool{}
	sess, missing := e.Session(p, env, man, "")
	for _, m := range missing {
		if !providerHasResources(bp, m) {
			plan.Warnings = append(plan.Warnings, "Connect "+providers.DisplayName(m)+" to monitor it as part of this backend (optional add-on).")
			continue
		}
		plan.Blockers = append(plan.Blockers, "Connect "+providers.DisplayName(m)+" for the "+env+" environment.")
	}
	plan.Warnings = append(plan.Warnings, e.separationIssues(p, env)...)
	for _, issue := range e.separationBlockers(p, env) {
		plan.Blockers = append(plan.Blockers, issue)
	}

	keys := make([]string, 0, len(bp.Resources))
	deps := map[string][]string{}
	for _, r := range bp.Resources {
		keys = append(keys, r.Key)
		d := append([]string{}, r.DependsOn...)
		for _, ref := range referencedKeys(&r) {
			if ref != r.Key && bp.ResourceByKey(ref) != nil && !contains(d, ref) {
				d = append(d, ref)
			}
		}
		deps[r.Key] = d
	}
	order, err := core.TopoSort(keys, deps)
	if err != nil {
		return nil, &core.Problem{Title: "Circular dependency", Code: "invalid", Summary: err.Error()}
	}
	if opts.Teardown {
		order = nil
		plan.Blockers = nil
		for _, m := range missing {
			if providerHasResources(bp, m) {
				plan.Blockers = append(plan.Blockers, "Connect "+providers.DisplayName(m)+" for the "+env+" environment so its resources can be removed.")
			}
		}
		if plan.Purpose == "" {
			plan.Purpose = "Tear down: remove everything Backplane created in " + env + "."
		}
	}

	// Observe existing resources concurrently (read-only).
	observed := map[string]*providers.Observation{}
	obsErr := map[string]error{}
	if !opts.Offline && !anyResourceProviderMissing(bp, missing) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, key := range order {
			st := man.Resources[key]
			if st == nil || st.Status == core.StateDeleted {
				continue
			}
			spec := bp.ResourceByKey(key)
			h, ok := e.Registry.Handler(spec.Kind)
			if !ok {
				continue
			}
			rs, err := e.resolveSpec(p, env, man, spec, forHash, false)
			if err != nil {
				rs = spec
			}
			wg.Add(1)
			go func(key string, h providers.Handler, rs *core.ResourceSpec, st *core.ResourceState) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				obs, err := h.Observe(ctx, sess, rs, st)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					obsErr[key] = err
					return
				}
				observed[key] = obs
			}(key, h, rs, st)
		}
		wg.Wait()
	}

	changing := map[string]bool{}
	opIDs := map[string]string{}
	for _, key := range order {
		spec := bp.ResourceByKey(key)
		st := man.Resources[key]
		op := core.Operation{ID: "op_" + key, ResourceKey: key, Kind: spec.Kind, Provider: spec.Provider, Title: spec.Title}
		if op.Title == "" {
			op.Title = spec.Kind + " " + spec.Name
		}
		if _, ok := e.Registry.Handler(spec.Kind); !ok {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("No adapter can build %s (%s).", spec.Title, spec.Kind))
			continue
		}
		depChanging := false
		for _, d := range deps[key] {
			if changing[d] {
				depChanging = true
			}
			if id, ok := opIDs[d]; ok {
				op.DependsOn = append(op.DependsOn, id)
			}
		}
		switch {
		case st == nil || st.Status == core.StateDeleted:
			op.Action = core.ActCreate
			if spec.Adopt {
				op.Action = core.ActAdopt
				op.Why = "Use the existing resource instead of creating a new one."
			}
		case st.Status == core.StateCreating || st.Status == core.StateFailed:
			op.Action = core.ActUpdate
			op.Why = "A previous build did not finish this step; it will be completed."
		default:
			obs := observed[key]
			if err := obsErr[key]; err != nil {
				op.Action = core.ActUpdate
				op.Why = "Could not read its current state (" + providers.Translate(spec.Provider, "read it", err).Title + "); it will be re-applied."
				break
			}
			if obs != nil && !obs.Exists {
				op.Action = core.ActReplace
				op.Why = "It was deleted or renamed outside Backplane and will be created again."
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s no longer exists in %s and will be recreated.", spec.Title, providers.DisplayName(spec.Provider)))
				break
			}
			hash, herr := e.desiredHash(p, env, man, spec)
			var unresolved *Unresolved
			switch {
			case herr != nil && !errors.As(herr, &unresolved):
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s: %s", spec.Title, providers.Translate(spec.Provider, "prepare it", herr).Summary))
				op.Action = core.ActUpdate
			case herr != nil || depChanging:
				op.Action = core.ActUpdate
				op.Why = "Something it depends on is changing; it will be re-checked and updated only if needed."
			case hash != st.Hash:
				op.Action = core.ActUpdate
				op.Why = "Its settings changed since the last build."
				op.Changes = diffApplied(st.Applied, e.safeApplied(p, env, man, spec))
			default:
				op.Action = core.ActKeep
				if h, ok := e.Registry.Handler(spec.Kind); ok && obs != nil {
					rs, rerr := e.resolveSpec(p, env, man, spec, forHash, false)
					if rerr != nil {
						rs = spec
					}
					if drift := h.Drift(rs, st, obs); len(drift) > 0 {
						op.Action = core.ActUpdate
						op.Why = "Its live configuration drifted from what Backplane set up; it will be restored."
						for _, d := range drift {
							op.Changes = append(op.Changes, core.Change{Field: d.Field, Expected: d.Expected, Actual: d.Actual})
						}
					}
				}
			}
		}
		if len(targets) > 0 {
			applyRepair(bp, spec, st, &op, targets, inRepair, deps[key])
		}
		if op.Action != core.ActKeep {
			changing[key] = true
		}
		if op.Why == "" {
			op.Why = whyFor(spec)
		}
		opIDs[key] = op.ID
		plan.Operations = append(plan.Operations, op)
		plan.Counts[op.Action]++
	}
	// Resources in the manifest but no longer in the blueprint (or all of
	// them for a teardown).
	var removed []string
	isRemoved := map[string]bool{}
	for key, st := range man.Resources {
		if len(targets) > 0 {
			break // repairs never remove anything
		}
		if (opts.Teardown || bp.ResourceByKey(key) == nil) && st.Status != core.StateDeleted {
			removed = append(removed, key)
			isRemoved[key] = true
		}
	}
	sort.Strings(removed)
	rmOps := map[string]*core.Operation{}
	var rmList []*core.Operation
	for _, key := range removed {
		st := man.Resources[key]
		spec := bp.ResourceByKey(key)
		title := st.Kind + " " + st.Name
		if spec != nil && spec.Title != "" {
			title = spec.Title
		}
		op := &core.Operation{ID: "op_rm_" + key, ResourceKey: key, Kind: st.Kind, Provider: st.Provider, Title: title}
		switch {
		case st.CreatedBy != "backplane":
			op.Action = core.ActDetach
			op.Why = "Backplane did not create it, so it is only removed from monitoring — never deleted."
		case spec != nil && spec.Keep:
			op.Action = core.ActDetach
			op.Why = "Shared with the rest of your account, so it is kept and only removed from monitoring."
		default:
			op.Action = core.ActDelete
			op.Destructive = true
			op.Why = "It is no longer part of this backend."
			if opts.Teardown {
				op.Why = "Created by Backplane for this backend."
				if k, ok := e.Registry.Handler(st.Kind); ok {
					if info, ok := kindInfo(e.Registry, st.Provider, k.Kind()); ok && info.Destructive != "" {
						op.Why += " Deleting it loses " + info.Destructive + "."
					}
				}
			}
			plan.Destructive = true
		}
		rmOps[key] = op
		rmList = append(rmList, op)
	}
	// Remove dependents before what they depend on.
	for _, key := range removed {
		for _, d := range deps[key] {
			if isRemoved[d] && rmOps[d] != nil {
				rmOps[d].DependsOn = append(rmOps[d].DependsOn, rmOps[key].ID)
			}
		}
	}
	for _, op := range rmList {
		plan.Operations = append(plan.Operations, *op)
		plan.Counts[op.Action]++
	}
	plan.Summary = summarize(bp, plan)
	for _, l := range bp.Links {
		from, to := bp.ComponentByKey(l.From), bp.ComponentByKey(l.To)
		if from != nil && to != nil {
			plan.Connections = append(plan.Connections, core.PlanLink{From: from.Label, To: to.Label, Label: l.Label})
		}
	}
	plan.Costs = EstimateCosts(bp)
	return plan, nil
}

func kindInfo(reg *providers.Registry, provider, kind string) (providers.KindInfo, bool) {
	p, ok := reg.Provider(provider)
	if !ok {
		return providers.KindInfo{}, false
	}
	for _, k := range p.Info().Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return providers.KindInfo{}, false
}

func anyResourceProviderMissing(bp *core.Blueprint, missing []string) bool {
	for _, m := range missing {
		if providerHasResources(bp, m) {
			return true
		}
	}
	return false
}

func providerHasResources(bp *core.Blueprint, provider string) bool {
	for _, r := range bp.Resources {
		if r.Provider == provider {
			return true
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func whyFor(spec *core.ResourceSpec) string {
	if w := providers.Str(spec.Props, "why"); w != "" {
		return w
	}
	return ""
}

func diffApplied(old, cur map[string]any) []core.Change {
	var out []core.Change
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range cur {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		a, b := providers.Str(old, k), providers.Str(cur, k)
		if a != b {
			out = append(out, core.Change{Field: k, Expected: shorten(b), Actual: shorten(a)})
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func shorten(s string) string {
	if len(s) > 90 {
		return s[:87] + "…"
	}
	if s == "" {
		return "—"
	}
	return s
}

// summarize builds the human block: "Supabase — 1 project, 6 tables, 14 RLS policies".
func summarize(bp *core.Blueprint, plan *core.Plan) []core.PlanGroup {
	type key struct{ provider, action string }
	counts := map[key]map[string]int{}
	order := []key{}
	for _, op := range plan.Operations {
		label := strings.ToUpper(op.Action)
		switch op.Action {
		case core.ActReplace, core.ActAdopt:
			label = "CREATE"
			if op.Action == core.ActAdopt {
				label = "USE EXISTING"
			}
		}
		k := key{op.Provider, label}
		if counts[k] == nil {
			counts[k] = map[string]int{}
			order = append(order, k)
		}
		spec := bp.ResourceByKey(op.ResourceKey)
		if spec != nil && len(spec.Count) > 0 {
			for _, c := range spec.Count {
				counts[k][c.Noun] += c.N
			}
		} else if spec != nil {
			counts[k][nounFor(spec.Kind)]++
		} else {
			counts[k][nounFor(op.Kind)]++
		}
	}
	actionRank := map[string]int{"CREATE": 0, "USE EXISTING": 1, "UPDATE": 2, "DELETE": 3, "DETACH": 4, "KEEP": 5}
	sort.SliceStable(order, func(i, j int) bool {
		if actionRank[order[i].action] != actionRank[order[j].action] {
			return actionRank[order[i].action] < actionRank[order[j].action]
		}
		return false
	})
	var out []core.PlanGroup
	for _, k := range order {
		g := core.PlanGroup{Provider: providers.DisplayName(k.provider), Action: k.action}
		nouns := make([]string, 0, len(counts[k]))
		for n := range counts[k] {
			nouns = append(nouns, n)
		}
		sort.Strings(nouns)
		for _, n := range nouns {
			c := counts[k][n]
			g.Lines = append(g.Lines, fmt.Sprintf("%d %s", c, plural(n, c)))
		}
		out = append(out, g)
	}
	return out
}

var nouns = map[string]string{
	"cloudflare.worker": "Worker", "cloudflare.r2_bucket": "R2 bucket", "cloudflare.kv_namespace": "KV namespace", "cloudflare.d1_database": "D1 database",
	"cloudflare.queue": "queue", "cloudflare.workers_subdomain": "workers.dev subdomain", "cloudflare.r2_object": "file upload", "cloudflare.dns_record": "DNS record",
	"cloudflare.worker_domain": "custom domain", "supabase.project": "project", "supabase.migration": "schema migration", "supabase.api_key": "secret key",
	"supabase.auth_config": "auth configuration", "supabase.storage_bucket": "storage bucket", "supabase.function_secrets": "secret set",
	stripe.KindProduct: "product", stripe.KindPrice: "price", stripe.KindWebhook: "webhook", stripe.KindLink: "payment link", stripe.KindPortal: "customer portal",
	"resend.domain": "domain", "resend.api_key": "send-only key", "resend.template": "template", "resend.webhook": "webhook", "resend.segment": "segment",
	"github.repo": "repository", "github.files": "code commit", "github.actions_secret": "environment secret", "github.actions_variable": "variable", "github.environment": "environment",
}

func nounFor(kind string) string {
	if n, ok := nouns[kind]; ok {
		return n
	}
	if i := strings.Index(kind, "."); i >= 0 {
		return strings.ReplaceAll(kind[i+1:], "_", " ")
	}
	return kind
}

func plural(noun string, n int) string {
	if n == 1 {
		return noun
	}
	switch {
	case strings.HasSuffix(noun, "y") && !strings.HasSuffix(noun, "ey"):
		return noun[:len(noun)-1] + "ies"
	case strings.HasSuffix(noun, "s"), strings.HasSuffix(noun, "sh"), strings.HasSuffix(noun, "ch"):
		return noun + "es"
	case noun == "Worker":
		return "Workers"
	}
	return noun + "s"
}

// separationIssues are warnings about environment mixing.
func (e *Engine) separationIssues(p *core.Project, env string) []string {
	var out []string
	if id := p.Connections[env]["stripe"]; id != "" && core.IsProduction(env) && !p.Practice {
		if c, err := e.Connection(id); err == nil && (c.Mode == "test" || c.Setting("mode") == "test") {
			out = append(out, "Production uses a Stripe TEST key: everything works end to end, but no real money moves until you connect a live key.")
		}
	}
	for other, links := range p.Connections {
		if other == env {
			continue
		}
		for prov, id := range links {
			if id != "" && p.Connections[env][prov] == id && (core.IsProduction(env) || core.IsProduction(other)) {
				out = append(out, fmt.Sprintf("%s: %s and %s share the same %s account connection. Their resources are named separately, but consider separate accounts or projects for production.", providers.DisplayName(prov), env, other, providers.DisplayName(prov)))
			}
		}
	}
	return out
}

// separationBlockers stop plans that would mix test and live payments.
func (e *Engine) separationBlockers(p *core.Project, env string) []string {
	var out []string
	id := p.Connections[env]["stripe"]
	if id == "" {
		return nil
	}
	c, err := e.Connection(id)
	if err != nil {
		return nil
	}
	mode := c.Mode
	if mode == "" {
		mode = c.Setting("mode")
	}
	if !core.IsProduction(env) && mode == "live" {
		out = append(out, "The "+env+" environment is connected to a Stripe LIVE key. Tests here could charge real cards. Use a test key outside production.")
	}
	return out
}
