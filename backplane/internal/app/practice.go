package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/sim"
)

// Practice mode runs Backplane against a built-in simulator of Cloudflare,
// Supabase, Stripe, Resend and GitHub. Nothing leaves the computer, nothing
// costs money, and the "break it" lab injects real-world failures so people
// can watch Backplane detect, explain and repair them. The simulator lives in
// memory, so practice projects reset when Backplane closes.

// practiceProviders are the providers the simulator implements.
var practiceProviders = []string{"cloudflare", "supabase", "stripe", "resend", "github"}

// practiceFields are the credential fields generated for each provider.
var practiceFields = map[string][]string{
	"cloudflare": {"token", "deploy_token"},
	"supabase":   {"token"},
	"stripe":     {"token", "worker_key"},
	"resend":     {"token"},
	"github":     {"token"},
}

func practiceSecret(provider, field string) string {
	r := providers.RandomToken(24)
	r = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return c
		}
		return 'x'
	}, r)
	switch provider + "." + field {
	case "cloudflare.token", "cloudflare.deploy_token":
		return "practice_cf_" + r
	case "supabase.token":
		return "sbp_practice" + r
	case "stripe.token":
		return "sk_test_practice" + r
	case "stripe.worker_key":
		return "rk_test_practice" + r
	case "resend.token":
		return "re_practice_" + r
	case "github.token":
		return "github_pat_practice_" + r
	}
	return "practice_" + r
}

// PracticeStateResult describes the sandbox.
type PracticeStateResult struct {
	Running     bool             `json:"running"`
	Connections []ConnectionView `json:"connections"`
	Breaks      []BreakOption    `json:"breaks"`
	Note        string           `json:"note"`
}

// StartPractice starts the simulator and makes sure a practice connection
// exists for every simulated provider.
func (a *App) StartPractice(ctx context.Context) (*PracticeStateResult, error) {
	a.mu.Lock()
	s := a.simServer
	if s == nil {
		s = sim.New()
		if err := s.Start(); err != nil {
			a.mu.Unlock()
			return nil, fmt.Errorf("could not start the practice sandbox: %w", err)
		}
		a.simServer = s
		a.Engine.PracticeBaseURLs = s.BaseURLs()
	}
	a.mu.Unlock()
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	have := map[string]*core.Connection{}
	for i := range cs {
		if cs[i].Practice {
			have[cs[i].Provider] = &cs[i]
		}
	}
	changed := false
	for _, prov := range practiceProviders {
		c := have[prov]
		if c == nil {
			id := engine.NewID("conn")
			nc := core.Connection{ID: id, Provider: prov, Label: providers.DisplayName(prov) + " (practice)", AuthMethod: "token",
				SecretRef: engine.ConnKey(id, "token"), Settings: map[string]string{}, Practice: true, CreatedAt: time.Now().UTC(), Status: core.HealthUnknown}
			for _, f := range practiceFields[prov] {
				if err := a.Vault.PutString(engine.ConnKey(id, f), practiceSecret(prov, f), providers.DisplayName(prov)+" practice "+f); err != nil {
					return nil, err
				}
				if f != "token" {
					nc.Settings["has_"+f] = "true"
				}
			}
			if prov == "stripe" {
				nc.Mode, nc.Settings["mode"] = "test", "test"
			}
			cs = append(cs, nc)
			c = &cs[len(cs)-1]
			changed = true
		}
		// The simulator forgets everything when Backplane closes, so its
		// credentials are registered again on every start.
		for _, f := range practiceFields[prov] {
			if v, err := a.Vault.GetString(engine.ConnKey(c.ID, f)); err == nil && v != "" {
				s.AddToken(prov, v)
			}
		}
	}
	for i := range cs {
		if cs[i].Practice {
			a.verifyInto(ctx, &cs[i])
			changed = true
		}
	}
	if changed {
		if err := a.Store.SaveConnections(cs); err != nil {
			return nil, err
		}
	}
	a.log("info", "", "", "Practice sandbox started — simulated Cloudflare, Supabase, Stripe, Resend and GitHub on this computer only.")
	a.Engine.Bus.Publish(engine.Event{Type: "practice", Data: map[string]any{"running": true}})
	return a.PracticeState(ctx)
}

// StopPractice shuts the simulator down (practice projects reset).
func (a *App) StopPractice(ctx context.Context) (bool, error) {
	a.mu.Lock()
	s := a.simServer
	a.simServer = nil
	a.mu.Unlock()
	if s == nil {
		return false, nil
	}
	s.Close()
	a.Engine.PracticeBaseURLs = nil
	a.resetPractice()
	a.markPracticeOffline()
	a.log("info", "", "", "Practice sandbox stopped — practice backends were reset and practice connections switched off.")
	a.Engine.Bus.Publish(engine.Event{Type: "practice", Data: map[string]any{"running": false}})
	return true, nil
}

// practiceOffNote is shown on practice connections while the sandbox is off.
const practiceOffNote = "Sandbox off — start practice mode to use it."

// markPracticeOffline switches practice connections to "off" so nothing shows
// them as connected while the simulator isn't running (and the saved file
// doesn't claim they work).
func (a *App) markPracticeOffline() {
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return
	}
	changed := false
	for i := range cs {
		c := &cs[i]
		if !c.Practice || (c.Status == core.HealthOff && c.StatusNote == practiceOffNote) {
			continue
		}
		c.Status, c.StatusNote, c.VerifiedAt = core.HealthOff, practiceOffNote, nil
		c.Warnings, c.Details = nil, nil
		changed = true
	}
	if changed {
		_ = a.Store.SaveConnections(cs)
	}
}

// PracticeState reports the sandbox and the failures it can inject.
func (a *App) PracticeState(ctx context.Context) (*PracticeStateResult, error) {
	a.mu.Lock()
	running := a.simServer != nil
	a.mu.Unlock()
	res := &PracticeStateResult{Running: running, Breaks: breakCatalog,
		Note: "Practice projects use a simulator on this computer. Nothing is created in real accounts and nothing costs money. The sandbox resets when Backplane closes."}
	views, _ := a.ListConnections(ctx)
	for _, v := range views {
		if v.Practice {
			res.Connections = append(res.Connections, v)
		}
	}
	return res, nil
}

// BreakOption is one failure the practice lab can inject.
type BreakOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Provider string `json:"provider"`
	Explain  string `json:"explain"`
	Expect   string `json:"expect"`         // what Backplane should report
	Kind     string `json:"kind,omitempty"` // resource kind it needs
	Check    string `json:"check"`          // quick | full: which check notices it
	Undo     string `json:"undo,omitempty"`
}

var breakCatalog = []BreakOption{
	{ID: "outage:stripe", Label: "Stripe goes down", Group: "Outages", Provider: "stripe", Check: "quick", Undo: "restore:stripe",
		Explain: "Stripe's API starts answering 503 for ten minutes.",
		Expect:  "Stripe turns red. Payments, refunds and webhook configuration are listed as affected; database, email and storage stay green."},
	{ID: "outage:supabase", Label: "Supabase goes down", Group: "Outages", Provider: "supabase", Check: "quick", Undo: "restore:supabase",
		Explain: "The database platform stops answering.", Expect: "Supabase and everything that reads orders turns red; payments intake is still shown as working."},
	{ID: "ratelimit:cloudflare", Label: "Cloudflare rate-limits Backplane", Group: "Outages", Provider: "cloudflare", Check: "quick",
		Explain: "The next few Cloudflare calls return 429 Too Many Requests.", Expect: "Nothing fails: Backplane waits as told by Retry-After and retries. The log shows the retries."},
	{ID: "revoke:stripe", Label: "Stripe key revoked", Group: "Credentials", Provider: "stripe", Check: "quick", Undo: "unrevoke:stripe",
		Explain: "The Stripe key Backplane uses is rolled in the dashboard.", Expect: "STRIPE AUTH FAILED with the exact reason and a Reconnect fix."},
	{ID: "revoke_db_key", Label: "Worker's database key revoked", Group: "Credentials", Provider: "supabase", Kind: "supabase.api_key", Check: "quick",
		Explain: "Someone revokes the secret key the Worker uses to write orders.", Expect: "Worker → Supabase fails. The fix issues a new key and updates the Worker secret."},
	{ID: "remove_binding", Label: "Storage binding removed from the Worker", Group: "Configuration drift", Provider: "cloudflare", Kind: "cloudflare.r2_bucket", Check: "quick",
		Explain: "Someone redeploys the Worker without the R2 bucket binding.", Expect: "CHANGE DETECTED on the Worker: binding DOWNLOADS missing; download delivery affected. One-click restore."},
	{ID: "remove_secret", Label: "Worker secret deleted", Group: "Configuration drift", Provider: "cloudflare", Kind: "cloudflare.worker", Check: "quick",
		Explain: "RESEND_API_KEY is deleted from the Worker.", Expect: "Worker → Resend fails and the Worker's secrets drift; the repair sets it again from the vault."},
	{ID: "rotate_webhook_secret", Label: "Webhook signing secret no longer matches", Group: "Configuration drift", Provider: "cloudflare", Kind: "stripe.webhook_endpoint", Check: "full",
		Explain: "The Worker's STRIPE_WEBHOOK_SECRET is changed to a wrong value, so every payment event is rejected.", Expect: "The full check's real delivery test fails: STRIPE → WORKER DELIVERY FAILED. The fix replaces the endpoint and updates the secret."},
	{ID: "disable_webhook", Label: "Stripe disables the webhook", Group: "Configuration drift", Provider: "stripe", Kind: "stripe.webhook_endpoint", Check: "quick",
		Explain: "Stripe disables an endpoint after days of failed deliveries.", Expect: "STRIPE WEBHOOK DISABLED; paid orders are not processed; one-click re-enable."},
	{ID: "move_webhook", Label: "Webhook points at an old server", Group: "Configuration drift", Provider: "stripe", Kind: "stripe.webhook_endpoint", Check: "quick",
		Explain: "Someone edits the endpoint URL in the Stripe dashboard.", Expect: "WEBHOOK URL INCORRECT with expected vs actual address."},
	{ID: "disable_rls", Label: "Row level security turned off", Group: "Security", Provider: "supabase", Kind: "supabase.migration", Check: "quick",
		Explain: "RLS is disabled on the orders table, exposing it to anyone with the public key.", Expect: "A red security finding naming the table; the repair turns RLS back on."},
	{ID: "pause_project", Label: "Supabase project paused", Group: "Deletions & pauses", Provider: "supabase", Kind: "supabase.project", Check: "quick",
		Explain: "The Free plan pauses projects after a week without activity.", Expect: "Database unavailable; the repair restores the project and waits until it is healthy."},
	{ID: "delete_bucket", Label: "Download bucket deleted", Group: "Deletions & pauses", Provider: "cloudflare", Kind: "cloudflare.r2_bucket", Check: "quick",
		Explain: "The private R2 bucket is deleted outside Backplane.", Expect: "PRIVATE R2 BUCKET IS MISSING; the repair recreates it, re-uploads the product file and re-binds the Worker."},
	{ID: "delete_worker", Label: "Worker deleted", Group: "Deletions & pauses", Provider: "cloudflare", Kind: "cloudflare.worker", Check: "quick",
		Explain: "The Worker is deleted in the Cloudflare dashboard.", Expect: "Checkout, order processing and downloads are affected; the repair redeploys it with all bindings and secrets."},
	{ID: "archive_product", Label: "Stripe product archived", Group: "Deletions & pauses", Provider: "stripe", Kind: "stripe.product", Check: "quick",
		Explain: "The product is archived in Stripe, so checkout cannot sell it.", Expect: "CHANGE DETECTED on the product; the repair re-activates it."},
	{ID: "unverify_domain", Label: "Email domain loses verification", Group: "Configuration drift", Provider: "resend", Kind: "resend.domain", Check: "quick",
		Explain: "The domain's DNS records are removed, so Resend stops sending.", Expect: "Receipts affected; the repair republishes the DNS records and re-verifies."},
	{ID: "fail_deploy", Label: "GitHub deploy fails", Group: "Deletions & pauses", Provider: "github", Kind: "github.repo", Check: "quick",
		Explain: "The latest GitHub Actions deploy fails.", Expect: "Automatic deploys affected, with a link to the failed run. The live Worker keeps running."},
}

// BreakParams injects one failure.
type BreakParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	Break     string `json:"break"`
}

// BreakResult says what was done.
type BreakResult struct {
	Message string `json:"message"`
	Check   string `json:"check"`
	Expect  string `json:"expect"`
}

// PracticeBreak injects a failure into a practice project's simulated
// services. It refuses to touch anything that is not a practice project.
func (a *App) PracticeBreak(ctx context.Context, p BreakParams) (*BreakResult, error) {
	a.mu.Lock()
	s := a.simServer
	a.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("start the practice sandbox first")
	}
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	if !pr.Practice {
		return nil, fmt.Errorf("the break-it lab only works on practice projects")
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	man, err := a.Store.LoadManifest(pr.ID, env)
	if err != nil {
		return nil, err
	}
	var opt *BreakOption
	for i := range breakCatalog {
		if breakCatalog[i].ID == p.Break {
			opt = &breakCatalog[i]
		}
	}
	undo := strings.HasPrefix(p.Break, "restore:") || strings.HasPrefix(p.Break, "unrevoke:")
	if opt == nil && !undo {
		return nil, fmt.Errorf("unknown failure %q", p.Break)
	}
	first := func(kind string) *core.ResourceState {
		for _, key := range sortedKeys(man.Resources) {
			if st := man.Resources[key]; st.Kind == kind && st.Status != core.StateDeleted {
				return st
			}
		}
		return nil
	}
	if opt != nil && opt.Kind != "" && first(opt.Kind) == nil {
		return nil, fmt.Errorf("build the project first — it has nothing of that kind to break yet")
	}
	id := func(kind string) string {
		if st := first(kind); st != nil {
			return st.ID
		}
		return ""
	}
	action, provider, _ := strings.Cut(p.Break, ":")
	c := sim.Control{Action: action, Provider: provider}
	worker := id("cloudflare.worker")
	switch action {
	case "remove_binding":
		c.Target, c.Value = worker, "DOWNLOADS"
		if first("cloudflare.r2_bucket") == nil {
			c.Value = ""
		}
	case "remove_secret":
		c.Target, c.Value = worker, "RESEND_API_KEY"
	case "rotate_webhook_secret", "delete_worker":
		c.Target = worker
	case "delete_bucket":
		c.Target = id("cloudflare.r2_bucket")
	case "disable_webhook", "move_webhook":
		c.Target = id("stripe.webhook_endpoint")
	case "archive_product":
		c.Target = id("stripe.product")
	case "pause_project":
		c.Target = id("supabase.project")
	case "disable_rls":
		c.Target, c.Value = id("supabase.project"), pr.Blueprint.Param("isolation_table")
		if c.Value == "" {
			c.Value = "orders"
		}
	case "revoke_db_key":
		c.Target, c.Value = id("supabase.project"), id("supabase.api_key")
	case "unverify_domain":
		c.Target = id("resend.domain")
	case "fail_deploy":
		if st := first("github.repo"); st != nil {
			c.Target = st.Output("full_name")
		}
	}
	msg, err := s.Apply(c)
	if err != nil {
		return nil, fmt.Errorf("could not inject the failure: %w", err)
	}
	a.log("warn", pr.ID, env, "Practice lab: "+msg)
	res := &BreakResult{Message: msg}
	if opt != nil {
		res.Check, res.Expect = opt.Check, opt.Expect
	}
	return res, nil
}

// resetPractice clears practice projects' environments: their simulated
// resources no longer exist once the simulator stops.
func (a *App) resetPractice() {
	ps, err := a.Store.ListProjects()
	if err != nil {
		return
	}
	for _, p := range ps {
		if !p.Practice {
			continue
		}
		for _, env := range p.Environments {
			_ = a.Vault.DeletePrefix("p/" + p.ID + "/" + env + "/")
			_ = a.Store.ResetEnv(p.ID, env)
		}
	}
}

// resetPracticeIfStale runs at start-up: the simulator is not running yet,
// so anything practice projects built in an earlier session is gone.
func (a *App) resetPracticeIfStale() {
	a.mu.Lock()
	running := a.simServer != nil
	a.mu.Unlock()
	if !running {
		a.resetPractice()
		a.markPracticeOffline()
	}
}
