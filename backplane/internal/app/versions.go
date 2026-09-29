package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/stripe"
)

// Provider change detection has three layers:
//
//  1. Pinned versions. Every adapter sends an explicit API version (Stripe,
//     GitHub) or targets a dated runtime (Workers compatibility date), so a
//     provider's new default never changes Backplane's behaviour silently.
//  2. Signals. Deprecation, Sunset and similar headers seen on any call are
//     recorded per endpoint and surfaced here and in the log.
//  3. Notices. Dated, curated announcements shipped with Backplane, each
//     marked as applying (or not) to the user's actual projects.
//
// Health checks are the backstop: if a provider changes behaviour anyway, the
// link and end-to-end tests fail with a plain-English diagnosis.

// ProviderVersion is one provider's pinned API surface.
type ProviderVersion struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Pinned   string `json:"pinned"`
	How      string `json:"how"`
	Status   string `json:"status"` // current | upgrade-available | action-needed
	Note     string `json:"note,omitempty"`
}

// Notice is a dated provider announcement.
type Notice struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Date     string   `json:"date"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Severity string   `json:"severity"` // info | warn | action
	Link     string   `json:"link,omitempty"`
	Applies  bool     `json:"applies"`
	Projects []string `json:"projects,omitempty"`
}

// VersionsResult is the Provider changes screen.
type VersionsResult struct {
	Providers []ProviderVersion   `json:"providers"`
	Notices   []Notice            `json:"notices"`
	Signals   []engine.APISignal  `json:"signals"`
	Checked   time.Time           `json:"checked"`
	Summary   string              `json:"summary"`
	Extra     map[string][]string `json:"extra,omitempty"`
}

var notices = []Notice{
	{ID: "stripe-endive", Provider: "stripe", Date: "2026-09-30", Severity: "info",
		Title: "Stripe API 2026-09-30.endive (new major version)",
		Body: "Stripe's next major API version adds breaking changes. Backplane pins every call and every webhook endpoint it creates to 2026-08-26.dahlia, " +
			"so your backends keep receiving the same event shapes when your account default changes. Backplane will move to endive in an update, through a plan you review.",
		Link: "https://docs.stripe.com/changelog"},
	{ID: "supabase-legacy-keys", Provider: "supabase", Date: "2026-12-31", Severity: "action",
		Title: "Supabase is retiring the legacy anon and service_role keys",
		Body: "Projects should move to publishable (sb_publishable_…) and secret (sb_secret_…) keys. Backplane creates a dedicated secret key for each Worker when the project supports it. " +
			"If a project below still uses the legacy service_role key, enable the new API keys in the Supabase dashboard (Project Settings → API Keys) and rebuild.",
		Link: "https://supabase.com/docs/guides/api/api-keys"},
	{ID: "cloudflare-cf-cli", Provider: "cloudflare", Date: "2026-09-29", Severity: "info",
		Title: "Cloudflare released the cf CLI and open-sourced Forge",
		Body: "The new command-line tool mirrors the whole Cloudflare API. Backplane talks to the same REST API (v4) directly, so nothing changes for your projects; " +
			"exports keep using Wrangler/OpenTofu formats that work with either tool.",
		Link: "https://blog.cloudflare.com/"},
	{ID: "workers-compat", Provider: "cloudflare", Date: blueprints.CompatibilityDate, Severity: "info",
		Title: "Workers compatibility date " + blueprints.CompatibilityDate,
		Body:  "Generated Workers run with this runtime date. Cloudflare never changes a Worker's behaviour behind its compatibility date; Backplane moves it forward only in an update you approve."},
	{ID: "github-api-version", Provider: "github", Date: "2022-11-28", Severity: "info",
		Title: "GitHub REST API version 2022-11-28 is pinned",
		Body:  "Every call sends X-GitHub-Api-Version. If GitHub announces a removal for this version, it appears under Signals below."},
}

// Versions lists pinned versions, dated notices and live API-change signals.
func (a *App) Versions(ctx context.Context) (*VersionsResult, error) {
	res := &VersionsResult{Checked: time.Now().UTC(), Signals: a.Engine.Signals()}
	pinned := map[string][2]string{
		"stripe":     {stripe.APIVersion, "Stripe-Version header on every call; webhook endpoints created with api_version"},
		"github":     {"2022-11-28", "X-GitHub-Api-Version header on every call"},
		"cloudflare": {"API v4 · Workers " + blueprints.CompatibilityDate, "REST API v4; generated Workers pin their compatibility date"},
		"supabase":   {"Management API v1", "Versioned URL path (/v1)"},
		"resend":     {"Unversioned REST", "Behaviour verified by health checks on every run"},
	}
	for _, info := range a.Reg.All() {
		pv := ProviderVersion{Provider: info.ID, Name: info.Name, Status: "current"}
		if p, ok := pinned[info.ID]; ok {
			pv.Pinned, pv.How = p[0], p[1]
		} else if info.APIVersion != "" {
			pv.Pinned, pv.How = info.APIVersion, "Pinned by the adapter"
		} else {
			continue
		}
		res.Providers = append(res.Providers, pv)
	}
	ps, _ := a.Store.ListProjects()
	for _, n := range notices {
		n := n
		switch n.ID {
		case "stripe-endive":
			for _, p := range ps {
				if hasProvider(p, "stripe") {
					n.Applies = true
					n.Projects = append(n.Projects, p.Name)
				}
			}
			for i := range res.Providers {
				if res.Providers[i].Provider == "stripe" {
					res.Providers[i].Status, res.Providers[i].Note = "upgrade-available", "2026-09-30.endive is available; Backplane stays pinned until it has been verified."
				}
			}
		case "supabase-legacy-keys":
			for _, p := range ps {
				if a.usesLegacySupabaseKey(p) {
					n.Applies = true
					n.Projects = append(n.Projects, p.Name)
				}
			}
			if n.Applies {
				for i := range res.Providers {
					if res.Providers[i].Provider == "supabase" {
						res.Providers[i].Status, res.Providers[i].Note = "action-needed", "A project still uses the legacy service_role key."
					}
				}
			}
		default:
			for _, p := range ps {
				if hasProvider(p, n.Provider) {
					n.Applies = true
					n.Projects = append(n.Projects, p.Name)
				}
			}
		}
		res.Notices = append(res.Notices, n)
	}
	sort.SliceStable(res.Notices, func(i, j int) bool {
		rank := map[string]int{"action": 0, "warn": 1, "info": 2}
		if rank[res.Notices[i].Severity] != rank[res.Notices[j].Severity] {
			return rank[res.Notices[i].Severity] < rank[res.Notices[j].Severity]
		}
		return res.Notices[i].Date > res.Notices[j].Date
	})
	action := 0
	for _, n := range res.Notices {
		if n.Applies && n.Severity == "action" {
			action++
		}
	}
	switch {
	case action > 0:
		res.Summary = fmt.Sprintf("%d provider change needs your attention.", action)
	case len(res.Signals) > 0:
		res.Summary = fmt.Sprintf("%d API change signal(s) seen from providers — review below.", len(res.Signals))
	default:
		res.Summary = "No provider changes affect your projects."
	}
	return res, nil
}

func hasProvider(p *core.Project, prov string) bool {
	for _, x := range p.Blueprint.Providers() {
		if x == prov {
			return true
		}
	}
	return false
}

func (a *App) usesLegacySupabaseKey(p *core.Project) bool {
	if p.Practice {
		return false
	}
	for _, env := range p.Environments {
		man, err := a.Store.LoadManifest(p.ID, env)
		if err != nil {
			continue
		}
		for _, st := range man.Resources {
			if st.Kind == "supabase.api_key" && st.Output("type") != "" && st.Output("type") != "secret" {
				return true
			}
		}
	}
	return false
}

// refreshVersions re-evaluates notices daily and tells the UI when something
// applies (called by the monitor).
func (a *App) refreshVersions(ctx context.Context) {
	v, err := a.Versions(ctx)
	if err != nil {
		return
	}
	for _, n := range v.Notices {
		if n.Applies && n.Severity == "action" {
			a.log("warn", "", "", "Provider change: "+n.Title+" (affects "+strings.Join(n.Projects, ", ")+")")
		}
	}
	a.Engine.Bus.Publish(engine.Event{Type: "versions", Data: map[string]any{"summary": v.Summary}})
}

// ---- security review ----

// SecurityFinding is one line of the security review.
type SecurityFinding struct {
	Severity string `json:"severity"` // ok | info | warn | fail
	Area     string `json:"area"`     // Secrets | Credentials | Environments | Data | Logging
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	Target   string `json:"target,omitempty"` // connection id or project id
}

// SecurityResult is the Security screen.
type SecurityResult struct {
	Vault    string            `json:"vault"`
	Secrets  int               `json:"secrets"`
	Findings []SecurityFinding `json:"findings"`
	Score    string            `json:"score"`
}

// Security reviews how credentials and data are protected.
func (a *App) Security(ctx context.Context) (*SecurityResult, error) {
	res := &SecurityResult{Vault: a.Vault.Scheme(), Secrets: len(a.Vault.List(""))}
	add := func(f SecurityFinding) { res.Findings = append(res.Findings, f) }
	if strings.Contains(strings.ToLower(res.Vault), "dpapi") {
		add(SecurityFinding{Severity: "ok", Area: "Secrets", Title: "Secrets are encrypted with Windows DPAPI",
			Detail: "Only your Windows account on this computer can decrypt them. They are never written to project files, exports or logs."})
	} else {
		add(SecurityFinding{Severity: "info", Area: "Secrets", Title: "Secrets are encrypted with AES-256-GCM",
			Detail: "The key file lives next to the vault in Backplane's data folder with owner-only permissions."})
	}
	add(SecurityFinding{Severity: "ok", Area: "Logging", Title: "Logs are redacted",
		Detail: "Every stored secret, bearer token and signing secret is masked before a log line is written."})
	cs, _ := a.Store.LoadConnections()
	for _, c := range cs {
		if c.Practice {
			continue
		}
		name := providers.DisplayName(c.Provider) + " · " + c.Label
		for _, w := range c.Warnings {
			sev := "warn"
			if strings.Contains(strings.ToLower(w), "expires") {
				sev = "warn"
			}
			add(SecurityFinding{Severity: sev, Area: "Credentials", Title: name, Detail: w, Fix: "Open the connection and replace or narrow the key.", Target: c.ID})
		}
		switch c.Provider {
		case "cloudflare":
			if c.Setting("has_deploy_token") != "true" {
				add(SecurityFinding{Severity: "warn", Area: "Credentials", Title: name + ": GitHub gets the main token",
					Detail: "Without a deploy-only token, GitHub Actions receives the same Cloudflare token Backplane uses.", Fix: "Add a token with only “Workers Scripts: Edit” as the deploy token.", Target: c.ID})
			}
		case "stripe":
			if c.Setting("has_worker_key") != "true" {
				add(SecurityFinding{Severity: map[bool]string{true: "fail", false: "warn"}[c.Mode == "live"], Area: "Credentials", Title: name + ": the Worker gets the full key",
					Detail: "Your backend holds the same Stripe key Backplane uses. A restricted key limited to Checkout, Prices and Events is safer.", Fix: "Create a restricted key and add it as the Worker key.", Target: c.ID})
			}
		}
		if c.Status == core.HealthFail {
			add(SecurityFinding{Severity: "fail", Area: "Credentials", Title: name + " is not working", Detail: c.StatusNote, Fix: "Reconnect it on the Connections screen.", Target: c.ID})
		}
	}
	ps, _ := a.Store.ListProjects()
	for _, p := range ps {
		if p.Practice {
			continue
		}
		for _, env := range p.Environments {
			sid := p.Connections[env]["stripe"]
			if sid != "" {
				if c, err := a.Engine.Connection(sid); err == nil {
					if core.IsProduction(env) && c.Mode == "test" {
						add(SecurityFinding{Severity: "info", Area: "Environments", Title: p.Name + " production uses a Stripe test key", Detail: "No real payments are taken until a live key is connected.", Target: p.ID})
					}
					if !core.IsProduction(env) && c.Mode == "live" {
						add(SecurityFinding{Severity: "fail", Area: "Environments", Title: p.Name + " " + env + " uses a LIVE Stripe key", Detail: "Tests could charge real cards.", Fix: "Assign a test-mode Stripe connection to " + env + ".", Target: p.ID})
					}
				}
			}
			if h, _ := a.Store.History(p.ID, env, 1); len(h) > 0 && h[0].ReportID != "" {
				if rep, err := a.Store.LoadReport(p.ID, env, h[0].ReportID); err == nil {
					for _, d := range rep.Drift {
						if strings.Contains(d.Field, "row level security") {
							add(SecurityFinding{Severity: "fail", Area: "Data", Title: p.Name + " (" + env + "): " + d.Field + " is " + d.Actual,
								Detail: strings.Join(d.Breaks, "; "), Fix: "Open the project and apply the repair.", Target: p.ID})
						}
					}
				}
			}
		}
		for _, other := range p.Environments {
			for _, env := range p.Environments {
				if env >= other || !(core.IsProduction(env) || core.IsProduction(other)) {
					continue
				}
				if id := p.Connections[env]["supabase"]; id != "" && id == p.Connections[other]["supabase"] {
					add(SecurityFinding{Severity: "info", Area: "Environments", Title: p.Name + ": " + env + " and " + other + " share a Supabase account",
						Detail: "Each environment gets its own Supabase project, so data is still separate. A separate organization adds another wall.", Target: p.ID})
				}
			}
		}
	}
	fails, warns := 0, 0
	for _, f := range res.Findings {
		switch f.Severity {
		case "fail":
			fails++
		case "warn":
			warns++
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		rank := map[string]int{"fail": 0, "warn": 1, "info": 2, "ok": 3}
		return rank[res.Findings[i].Severity] < rank[res.Findings[j].Severity]
	})
	switch {
	case fails > 0:
		res.Score = fmt.Sprintf("%d issue(s) to fix", fails)
	case warns > 0:
		res.Score = fmt.Sprintf("Good · %d suggestion(s)", warns)
	default:
		res.Score = "Strong"
	}
	return res, nil
}
