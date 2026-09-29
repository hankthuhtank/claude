package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/cloudflare"
	"safisolutions.org/backplane/internal/providers/github"
	"safisolutions.org/backplane/internal/providers/resend"
	"safisolutions.org/backplane/internal/providers/stripe"
	"safisolutions.org/backplane/internal/providers/supabase"
)

func init() {
	RegisterLinkCheck("worker_probe:r2", probeLink("r2", "R2 storage"))
	RegisterLinkCheck("worker_probe:kv", probeLink("kv", "KV"))
	RegisterLinkCheck("worker_probe:d1", probeLink("d1", "D1"))
	RegisterLinkCheck("worker_probe:queue", probeLink("queue", "the queue"))
	RegisterLinkCheck("worker_probe:supabase", probeLink("supabase", "Supabase"))
	RegisterLinkCheck("worker_probe:stripe", probeLink("stripe", "Stripe"))
	RegisterLinkCheck("worker_probe:resend", probeLink("resend", "Resend"))
	RegisterLinkCheck("stripe_webhook", checkStripeWebhook)
	RegisterLinkCheck("checkout", checkCheckout)
	RegisterLinkCheck("github_deploy", checkGitHubDeploy)
	RegisterLinkCheck("supabase_smtp", checkSupabaseSMTP)
	RegisterLinkCheck("resend_webhook", checkResendWebhook)

	RegisterSynthetic(SyntheticCheck{ID: "worker_endpoint", Title: "API answers correctly", Provider: "cloudflare", Applies: hasKind("cloudflare.worker"), Run: synthWorker})
	RegisterSynthetic(SyntheticCheck{ID: "db_crud", Title: "Database insert · read · update · delete", Provider: "supabase", Applies: hasKind("supabase.api_key"), Run: synthDB})
	RegisterSynthetic(SyntheticCheck{ID: "storage_roundtrip", Title: "Storage upload · download · delete", Provider: "cloudflare", Applies: hasKind("cloudflare.r2_bucket"), Run: synthStorage})
	RegisterSynthetic(SyntheticCheck{ID: "email_delivery", Title: "Email accepted and delivered", Provider: "resend", Applies: hasKind("resend.domain"), Run: synthEmail})
	RegisterSynthetic(SyntheticCheck{ID: "auth_roundtrip", Title: "Account sign-up · sign-in · delete", Provider: "supabase", Applies: hasKind("supabase.auth_config"), Run: synthAuth})
	RegisterSynthetic(SyntheticCheck{ID: "payments_session", Title: "Checkout session can be created", Provider: "stripe", Applies: hasKind("stripe.price"), Run: synthCheckout})

	RegisterScenario(Scenario{ID: "store_purchase", Title: "Customer buys and receives a download", Run: scenarioPurchase})
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Checker) stripeLive() bool {
	conn := c.Conn("stripe")
	return conn != nil && stripe.ModeOf(conn.Secret) == "live"
}

// ---- Level 4: links ----

// checkStripeWebhook proves Stripe → Worker. Quick: the endpoint exists, is
// enabled, points at this Worker and has no failed deliveries in 24 hours.
// Full: Stripe really delivers an event (a Checkout Session is created and
// expired, which never charges anything) and the Worker confirms receipt.
func checkStripeWebhook(c *Checker, l *core.LinkSpec) core.CheckResult {
	conn := c.Conn("stripe")
	spec := c.resourceOf(stripe.KindWebhook)
	if conn == nil || spec == nil || c.State(spec.Key) == nil {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Webhook not built yet."}
	}
	st := c.State(spec.Key)
	obs := c.obs[spec.Key]
	workerURL, _ := c.WorkerURL()
	res := core.CheckResult{Provider: "stripe", Details: map[string]string{}}
	if obs == nil {
		var ep map[string]any
		if err := stripe.Call(c.ctx, conn, "GET", "/v1/webhook_endpoints/"+st.ID, nil, "", &ep); err != nil {
			res.Health = core.HealthFail
			res.Problem = providers.Translate("stripe", "read the webhook endpoint", err)
			res.Summary = res.Problem.Summary
			return res
		}
		obs = &providers.Observation{Exists: true, Props: map[string]any{"url": ep["url"], "status": ep["status"]}}
	}
	if !obs.Exists {
		res.Health = core.HealthFail
		res.Summary = "Stripe has no webhook endpoint for this backend any more."
		res.Problem = &core.Problem{Title: "STRIPE WEBHOOK MISSING", Provider: "stripe", Code: "not_found", Summary: "The webhook endpoint was deleted in Stripe, so paid orders never reach the backend.",
			Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Recreate the webhook", Automatic: true, Action: "repair", Target: spec.Key, Changes: []string{"Create a new Stripe webhook endpoint", "Update the Worker's STRIPE_WEBHOOK_SECRET"}}}}
		return res
	}
	url := providers.Str(obs.Props, "url")
	status := providers.Str(obs.Props, "status")
	res.Details["endpoint"] = url
	res.Details["status"] = status
	if workerURL != "" && !strings.HasPrefix(url, workerURL) {
		res.Health = core.HealthFail
		res.Expected, res.Actual = workerURL+"/stripe/webhook", url
		res.Summary = "Stripe sends events to the wrong address."
		res.Problem = &core.Problem{Title: "WEBHOOK URL INCORRECT", Provider: "stripe", Code: "drift", Summary: "Stripe is sending payment events to " + url + " instead of this backend's Worker.",
			Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Point the webhook at the Worker", Automatic: true, Action: "repair", Target: spec.Key, Changes: []string{"Set the endpoint URL to " + workerURL + "/stripe/webhook"}}}}
		return res
	}
	if status != "enabled" {
		res.Health = core.HealthFail
		res.Summary = "The webhook is " + status + " in Stripe."
		res.Problem = &core.Problem{Title: "STRIPE WEBHOOK DISABLED", Provider: "stripe", Code: "drift", Summary: "Stripe disabled the webhook (it does this after days of failed deliveries), so orders are not being processed.",
			Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Re-enable the webhook", Automatic: true, Action: "repair", Target: spec.Key, Changes: []string{"Enable the webhook endpoint"}}}}
		return res
	}
	failed, err := stripe.FailedDeliveries(c.ctx, conn, time.Now().Add(-24*time.Hour), providers.List(spec.Props, "events"))
	if err == nil && len(failed) > 0 {
		res.Health = core.HealthWarn
		res.Summary = fmt.Sprintf("%d event(s) in the last 24 hours have not been delivered successfully yet.", len(failed))
		res.Details["undelivered"] = failed[0].ID + " (" + failed[0].Type + ")"
	} else {
		res.Health = core.HealthOK
		res.Summary = "Enabled and pointing at the Worker; no failed deliveries in 24 hours."
	}
	if !c.Full() {
		return res
	}
	if c.stripeLive() && !c.Opts.AllowLiveProbes {
		res.Details["delivery test"] = "skipped in live mode (enable “live probes” to run the no-charge delivery test)"
		return res
	}
	// Real delivery test.
	price, mode := c.probePrice()
	if price == nil || workerURL == "" {
		return res
	}
	probeID := randomID()
	start := time.Now()
	cs, err := stripe.CreateCheckoutSessionMode(c.ctx, conn, c.State(price.Key).ID, mode, workerURL+"/thanks?probe=1",
		map[string]string{"backplane_probe": "1", "backplane_probe_id": probeID}, "bp-probe-"+probeID)
	if err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("stripe", "create a test checkout session", err)
		res.Summary = res.Problem.Summary
		return res
	}
	if err := stripe.ExpireCheckoutSession(c.ctx, conn, cs.ID); err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("stripe", "expire the test checkout session", err)
		res.Summary = res.Problem.Summary
		return res
	}
	received := false
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !ctxSleep(c.ctx, 2*time.Second) {
			break
		}
		resp, err := c.workerCall("GET", "/__backplane/probe/stripe/"+cs.ID, nil, nil)
		if err == nil {
			var out struct {
				Found bool `json:"found"`
			}
			if json.Unmarshal(resp.Body, &out) == nil && out.Found {
				received = true
				break
			}
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	if !received {
		res.Health = core.HealthFail
		res.Summary = "Stripe sent a test event but the Worker never confirmed receiving it."
		detail := ""
		if ev, err := stripe.FindEvent(c.ctx, conn, "checkout.session.expired", cs.ID, start); err == nil && ev != nil {
			detail = fmt.Sprintf(" Stripe still has %d pending delivery attempt(s) for event %s.", ev.PendingWebhooks, ev.ID)
		}
		res.Problem = &core.Problem{Title: "STRIPE → WORKER DELIVERY FAILED", Provider: "stripe", Code: "drift",
			Summary: "Stripe created and expired a test Checkout Session (nothing was charged) but the Worker did not record the event." + detail + " The most common cause is a webhook signing secret that no longer matches.",
			Fixes: []core.Fix{{ID: "rotate:" + spec.Key, Label: "Recreate the webhook and update the Worker's signing secret", Automatic: true, Action: "repair", Target: spec.Key,
				Changes: []string{"Replace the Stripe webhook endpoint", "Store the new signing secret in the vault", "Update the Worker secret STRIPE_WEBHOOK_SECRET"}}}}
		return res
	}
	res.Summary = fmt.Sprintf("Stripe delivered a real test event to the Worker in %.1fs (nothing was charged).", float64(res.LatencyMS)/1000)
	res.Details["delivery test"] = "passed via expired Checkout Session " + cs.ID
	return res
}

// checkCheckout proves customers can reach checkout through the Worker.
func checkCheckout(c *Checker, l *core.LinkSpec) core.CheckResult {
	url, _ := c.WorkerURL()
	if url == "" {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Not built yet."}
	}
	start := time.Now()
	resp, err := c.workerCall("GET", "/", nil, nil)
	if err != nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "The public API address does not answer.",
			Problem: &core.Problem{Title: "API OFFLINE", Provider: "cloudflare", Code: "network", Summary: "Customers cannot start a checkout because " + url + " does not answer: " + err.Error()}}
	}
	res := core.CheckResult{Health: core.HealthOK, Summary: "Public API answers", LatencyMS: time.Since(start).Milliseconds(), Details: map[string]string{"url": url}}
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		res.Details["cors"] = "no CORS header on / (fine for links; browsers calling /checkout from your site get CORS)"
	}
	if !c.Full() || (c.stripeLive() && !c.Opts.AllowLiveProbes) {
		return res
	}
	token, _ := c.Gen("probe_token")
	// Customers can only name an item and a quantity. Unknown items are
	// refused, and a price or amount sent from the browser is ignored.
	_, uerr := c.workerCall("POST", "/checkout", []byte(`{"items":[{"key":"bp_no_such_item","quantity":1}]}`), http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}}, true)
	if uerr == nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "Checkout accepted an item that isn't for sale.",
			Problem: &core.Problem{Title: "CHECKOUT ACCEPTS UNKNOWN ITEMS", Code: "drift", Summary: "The Worker's /checkout created a session for an item that isn't in the catalog. The deployed code is not the version Backplane generated; rebuild the Worker.",
				Fixes: []core.Fix{{ID: "reapply:" + workerKeyOf(c), Label: "Redeploy the generated Worker", Automatic: true, Action: "repair", Target: workerKeyOf(c)}}}}
	}
	body := []byte(`{}`)
	var expect int64
	if items := Catalog(&c.P.Blueprint); len(items) > 0 {
		body, _ = json.Marshal(map[string]any{"items": []any{map[string]any{"key": items[0].Key, "quantity": 1, "price": "price_from_browser", "amount": 1, "unit_amount": 1}}, "amount": 1})
		expect = items[0].Cents()
	}
	resp, err = c.workerCall("POST", "/checkout", body, http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}, "X-Backplane-Probe": {token}})
	if err != nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "The Worker could not create a checkout session.",
			Problem: &core.Problem{Title: "CHECKOUT BROKEN", Provider: "stripe", Code: "drift", Summary: "Calling the Worker's /checkout failed: " + err.Error() + ". The Worker's Stripe key or price may be wrong.",
				Fixes: diagnoseProbe("stripe", err.Error(), c)}}
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(resp.Body, &out)
	if out.ID == "" || !strings.Contains(out.URL, "http") {
		return core.CheckResult{Health: core.HealthFail, Summary: "The Worker answered /checkout without a Stripe session.",
			Problem: &core.Problem{Title: "CHECKOUT BROKEN", Code: "drift", Summary: "The Worker's /checkout response had no Stripe Checkout URL."}}
	}
	if conn := c.Conn("stripe"); conn != nil {
		c.Defer(func(ctx2 contextLike) error { return stripe.ExpireCheckoutSession(ctx2, conn, out.ID) })
		if expect > 0 {
			if cs, err := stripe.GetCheckoutSession(c.ctx, conn, out.ID); err == nil && cs.AmountTotal != expect {
				return core.CheckResult{Health: core.HealthFail, Summary: "Checkout charged a price sent by the browser.",
					Expected: stripe.Money(expect, cs.Currency), Actual: stripe.Money(cs.AmountTotal, cs.Currency),
					Problem: &core.Problem{Title: "PRICES NOT ENFORCED", Code: "drift", Summary: "A checkout request carrying its own amount produced a session for " + stripe.Money(cs.AmountTotal, cs.Currency) + " instead of the catalog price. Rebuild the Worker so prices only come from Stripe.",
						Fixes: []core.Fix{{ID: "reapply:" + workerKeyOf(c), Label: "Redeploy the generated Worker", Automatic: true, Action: "repair", Target: workerKeyOf(c)}}}}
			}
		}
	}
	res.Summary = "Customers can start checkout at catalog prices; unknown items and browser-sent prices are refused (session " + out.ID + " expired afterwards)"
	return res
}

// checkGitHubDeploy proves GitHub → Cloudflare deploys.
func checkGitHubDeploy(c *Checker, l *core.LinkSpec) core.CheckResult {
	conn := c.Conn("github")
	repoSpec := c.resourceOf(github.KindRepo)
	if conn == nil || repoSpec == nil || c.State(repoSpec.Key) == nil {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Repository not built yet."}
	}
	repo := c.State(repoSpec.Key).ID
	res := core.CheckResult{Provider: "github", Details: map[string]string{"repository": repo}}
	var missing []string
	for _, r := range c.P.Blueprint.Resources {
		if r.Kind == github.KindSecret {
			if obs := c.obs[r.Key]; obs != nil && !obs.Exists {
				missing = append(missing, providers.Str(r.Props, "name"))
			}
		}
	}
	if len(missing) > 0 {
		res.Health = core.HealthFail
		res.Summary = "Deploy secrets missing: " + strings.Join(missing, ", ")
		res.Problem = &core.Problem{Title: "AUTOMATIC DEPLOYS WILL FAIL", Provider: "github", Code: "drift", Summary: "GitHub Actions is missing " + strings.Join(missing, ", ") + ", so pushes to main cannot deploy the Worker."}
		return res
	}
	run, err := github.LatestRun(c.ctx, conn, repo, "main")
	switch {
	case err != nil:
		res.Health = core.HealthWarn
		res.Summary = "Could not read workflow runs: " + providers.Translate("github", "read workflow runs", err).Title
	case run == nil:
		res.Health = core.HealthOK
		res.Summary = "Secrets in place; no deploy has run yet."
	case run.Status != "completed":
		res.Health = core.HealthOK
		res.Summary = "A deploy is running now (" + run.Name + ")."
		res.Details["run"] = run.HTMLURL
	case run.Conclusion == "success":
		res.Health = core.HealthOK
		res.Summary = "Last deploy succeeded (" + shortTime(run.CreatedAt) + ")."
		res.Details["run"] = run.HTMLURL
	default:
		res.Health = core.HealthFail
		res.Summary = "Last deploy " + run.Conclusion + " (" + shortTime(run.CreatedAt) + ")."
		res.Details["run"] = run.HTMLURL
		res.Problem = &core.Problem{Title: "LAST DEPLOY FAILED", Provider: "github", Code: "server", Summary: "The most recent GitHub Actions deploy ended with “" + run.Conclusion + "”. Open the run to see the error; the live Worker keeps its previous version.",
			Fixes: []core.Fix{{ID: "link:run", Label: "Open the failed run", Link: run.HTMLURL, Action: "link"}}}
	}
	return res
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("Jan 2 3:04 PM")
}

// checkSupabaseSMTP proves Supabase Auth sends through Resend.
func checkSupabaseSMTP(c *Checker, l *core.LinkSpec) core.CheckResult {
	spec := c.resourceOf(supabase.KindAuthConfig)
	if spec == nil || c.obs[spec.Key] == nil {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Auth settings not read yet."}
	}
	host := providers.Str(c.obs[spec.Key].Props, "smtp_host")
	if host != "smtp.resend.com" {
		return core.CheckResult{Health: core.HealthFail, Expected: "smtp.resend.com", Actual: orStr(host, "Supabase default mailer"), Summary: "Sign-in emails are not going through Resend.",
			Problem: &core.Problem{Title: "AUTH EMAIL NOT CONNECTED", Provider: "supabase", Code: "drift", Summary: "Supabase Auth's SMTP was changed, so verification and password-reset emails use Supabase's rate-limited default mailer (a few emails per hour) or fail.",
				Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Reconnect Supabase Auth to Resend", Automatic: true, Action: "repair", Target: spec.Key, Changes: []string{"Set SMTP host, user and sender back to Resend"}}}}}
	}
	dom := c.resourceOf(resend.KindDomain)
	if dom != nil {
		if obs := c.obs[dom.Key]; obs != nil && providers.Str(obs.Props, "status") != "verified" {
			return core.CheckResult{Health: core.HealthWarn, Summary: "Connected to Resend, but the sending domain is not verified yet."}
		}
	}
	return core.CheckResult{Health: core.HealthOK, Summary: "Sign-in emails go through Resend SMTP."}
}

// checkResendWebhook proves delivery events reach the Worker.
func checkResendWebhook(c *Checker, l *core.LinkSpec) core.CheckResult {
	spec := c.resourceOf(resend.KindWebhook)
	if spec == nil || c.State(spec.Key) == nil {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Not built yet."}
	}
	if obs := c.obs[spec.Key]; obs != nil && !obs.Exists {
		return core.CheckResult{Health: core.HealthFail, Summary: "Resend webhook deleted.", Problem: &core.Problem{Title: "RESEND WEBHOOK MISSING", Provider: "resend", Code: "not_found",
			Summary: "Bounces and complaints are no longer recorded.", Fixes: []core.Fix{{ID: "reapply:" + spec.Key, Label: "Recreate it", Automatic: true, Action: "repair", Target: spec.Key}}}}
	}
	resp, err := c.workerCall("POST", "/resend/webhook", []byte(`{"type":"email.delivered"}`), nil, true)
	if err == nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "The Worker accepted an unsigned delivery event.",
			Problem: &core.Problem{Title: "WEBHOOK SIGNATURE NOT CHECKED", Code: "drift", Summary: "The Worker's /resend/webhook accepted a request without a valid signature. Rebuild the Worker so only Resend can post events."}}
	}
	if e, ok := httpx.AsError(err); ok && (e.Status == 400 || e.Status == 401) {
		_ = resp
		return core.CheckResult{Health: core.HealthOK, Summary: "Enabled; the Worker rejects unsigned events as it should."}
	}
	return core.CheckResult{Health: core.HealthWarn, Summary: "Could not confirm the Worker's webhook route: " + err.Error()}
}

// ---- Level 5: synthetic tests ----

func synthWorker(c *Checker) core.CheckResult {
	url, key := c.WorkerURL()
	res := core.CheckResult{Target: c.componentOf("cloudflare.worker")}
	var steps []core.StepResult
	start := time.Now()
	_, err := c.ProbeClient().Do(c.ctx, httpx.Request{Method: "GET", Path: url + "/__backplane/health", Resource: key, Quiet: true})
	if e, ok := httpx.AsError(err); ok && e.Status == 401 {
		steps = append(steps, step("Rejects calls without the probe token", core.HealthOK, "401 as expected", time.Since(start).Milliseconds()))
	} else {
		steps = append(steps, step("Rejects calls without the probe token", core.HealthFail, "expected 401", time.Since(start).Milliseconds()))
	}
	h, prob := c.WorkerHealth()
	if prob != nil {
		steps = append(steps, step("Health endpoint answers", core.HealthFail, prob.Summary, 0))
		res.Steps, res.Health, res.Summary, res.Problem = steps, core.HealthFail, prob.Summary, prob
		return res
	}
	steps = append(steps, step("Health endpoint answers with the probe token", core.HealthOK, fmt.Sprintf("HTTP %d", h.status), h.latency.Milliseconds()))
	ct := h.headers.Get("Content-Type")
	cc := h.headers.Get("Cache-Control")
	hdr := core.HealthOK
	if !strings.Contains(ct, "application/json") || !strings.Contains(cc, "no-store") {
		hdr = core.HealthWarn
	}
	steps = append(steps, step("Expected headers", hdr, "Content-Type: "+ct+" · Cache-Control: "+cc, 0))
	if len(h.Missing) > 0 {
		steps = append(steps, step("Configuration complete", core.HealthFail, "missing: "+strings.Join(h.Missing, ", "), 0))
	} else {
		steps = append(steps, step("Configuration complete", core.HealthOK, "all variables and secrets present", 0))
	}
	res.Steps = steps
	res.Health = core.HealthOK
	for _, s := range steps {
		res.Health = core.Worst(res.Health, s.Health)
	}
	res.Summary = fmt.Sprintf("Answered in %d ms", h.latency.Milliseconds())
	res.LatencyMS = h.latency.Milliseconds()
	if res.Health == core.HealthFail {
		res.Summary = "The API is up but misconfigured."
		res.Problem = &core.Problem{Title: "API MISCONFIGURED", Code: "drift", Summary: "The Worker reports missing configuration: " + strings.Join(h.Missing, ", ") + ".", Fixes: diagnoseProbe("config", "missing", c)}
	}
	return res
}

// dbClient returns a PostgREST client authorised with the Worker's server key.
func (c *Checker) dbClient() (*httpx.Client, string, *core.Problem) {
	conn := c.Conn("supabase")
	proj := c.resourceOf(supabase.KindProject)
	key := c.resourceOf(supabase.KindAPIKey)
	if conn == nil || proj == nil || key == nil || c.State(proj.Key) == nil {
		return nil, "", &core.Problem{Title: "Database not built yet"}
	}
	secret, err := c.Secret(key.Key, "key")
	if err != nil {
		return nil, "", &core.Problem{Title: "Database key missing from the vault", Code: "auth", Summary: "Rebuild to issue a new server key."}
	}
	cl := supabase.ProjectClient(conn, c.State(proj.Key).ID, secret, c.e.httpLogger(core.LogEntry{Project: c.P.ID, Environment: c.Env, RunID: c.runID}))
	return cl, c.State(proj.Key).ID, nil
}

func synthDB(c *Checker) core.CheckResult {
	res := core.CheckResult{Target: c.componentOf(supabase.KindProject)}
	cl, _, prob := c.dbClient()
	if prob != nil {
		res.Health, res.Summary = core.HealthUnknown, prob.Title
		return res
	}
	id := "bp-" + randomID()
	var steps []core.StepResult
	pref := http.Header{"Prefer": {"return=representation"}}
	run := func(title string, rq httpx.Request, check func([]byte) error) bool {
		start := time.Now()
		resp, err := cl.Do(c.ctx, rq)
		ms := time.Since(start).Milliseconds()
		if err == nil && check != nil {
			err = check(resp.Body)
		}
		if err != nil {
			steps = append(steps, step(title, core.HealthFail, err.Error(), ms))
			return false
		}
		steps = append(steps, step(title, core.HealthOK, "", ms))
		return true
	}
	c.Defer(func(ctx2 contextLike) error {
		_, err := cl.Do(ctx2, httpx.Request{Method: "DELETE", Path: "/rest/v1/backplane_probe", Query: url.Values{"id": {"eq." + id}}})
		return err
	})
	ok := run("Insert test record", httpx.Request{Method: "POST", Path: "/rest/v1/backplane_probe", JSON: map[string]any{"id": id, "note": "synthetic"}, Header: pref}, nil) &&
		run("Read test record", httpx.Request{Method: "GET", Path: "/rest/v1/backplane_probe", Query: url.Values{"id": {"eq." + id}, "select": {"id,note"}}}, func(b []byte) error {
			if !bytes.Contains(b, []byte(id)) {
				return fmt.Errorf("record not returned")
			}
			return nil
		}) &&
		run("Update test record", httpx.Request{Method: "PATCH", Path: "/rest/v1/backplane_probe", Query: url.Values{"id": {"eq." + id}}, JSON: map[string]any{"note": "updated"}, Header: pref}, func(b []byte) error {
			if !bytes.Contains(b, []byte("updated")) {
				return fmt.Errorf("update not applied")
			}
			return nil
		}) &&
		run("Delete test record", httpx.Request{Method: "DELETE", Path: "/rest/v1/backplane_probe", Query: url.Values{"id": {"eq." + id}}, Header: pref}, nil)
	res.Steps = steps
	if ok {
		res.Health, res.Summary = core.HealthOK, "Insert, read, update and delete all worked (test row removed)."
		return res
	}
	res.Health = core.HealthFail
	last := steps[len(steps)-1]
	res.Summary = last.Title + " failed: " + last.Detail
	res.Problem = &core.Problem{Title: "DATABASE WRITE TEST FAILED", Provider: "supabase", Code: "drift", Summary: res.Summary + " The backend's server key or the backplane_probe table may have changed.",
		Fixes: []core.Fix{{ID: "reapply:" + c.resourceOf(supabase.KindMigration).Key, Label: "Re-apply the database schema", Automatic: true, Action: "repair", Target: c.resourceOf(supabase.KindMigration).Key}}}
	return res
}

func synthStorage(c *Checker) core.CheckResult {
	res := core.CheckResult{Target: c.componentOf(cloudflare.KindR2Bucket)}
	conn := c.Conn("cloudflare")
	spec := c.resourceOf(cloudflare.KindR2Bucket)
	if conn == nil || spec == nil || c.State(spec.Key) == nil {
		res.Health, res.Summary = core.HealthUnknown, "Storage not built yet."
		return res
	}
	acct, err := cloudflare.AccountID(conn)
	if err != nil {
		res.Health, res.Summary = core.HealthFail, err.Error()
		return res
	}
	bucket := c.State(spec.Key).ID
	key := "__backplane/probe-" + randomID() + ".txt"
	body := []byte("backplane synthetic storage test " + time.Now().UTC().Format(time.RFC3339Nano))
	var steps []core.StepResult
	c.Defer(func(ctx2 contextLike) error { return cloudflare.DeleteObject(ctx2, conn, acct, bucket, key) })
	start := time.Now()
	if err := cloudflare.PutObject(c.ctx, conn, acct, bucket, key, body, "text/plain"); err != nil {
		steps = append(steps, step("Upload test file", core.HealthFail, providers.Translate("cloudflare", "upload", err).Summary, time.Since(start).Milliseconds()))
		res.Steps, res.Health, res.Summary = steps, core.HealthFail, "Upload failed."
		res.Problem = providers.Translate("cloudflare", "upload a test file to R2", err)
		return res
	}
	steps = append(steps, step("Upload test file", core.HealthOK, fmt.Sprintf("%d bytes", len(body)), time.Since(start).Milliseconds()))
	start = time.Now()
	got, err := cloudflare.GetObject(c.ctx, conn, acct, bucket, key)
	switch {
	case err != nil:
		steps = append(steps, step("Download test file", core.HealthFail, err.Error(), time.Since(start).Milliseconds()))
	case !bytes.Equal(got, body):
		steps = append(steps, step("Download test file", core.HealthFail, "content differs", time.Since(start).Milliseconds()))
	default:
		steps = append(steps, step("Download test file", core.HealthOK, "content matches", time.Since(start).Milliseconds()))
	}
	start = time.Now()
	if err := cloudflare.DeleteObject(c.ctx, conn, acct, bucket, key); err != nil {
		steps = append(steps, step("Delete test file", core.HealthFail, err.Error(), time.Since(start).Milliseconds()))
	} else {
		steps = append(steps, step("Delete test file", core.HealthOK, "", time.Since(start).Milliseconds()))
	}
	res.Steps = steps
	res.Health = core.HealthOK
	for _, s := range steps {
		res.Health = core.Worst(res.Health, s.Health)
	}
	if res.Health == core.HealthOK {
		res.Summary = "Upload, download and delete worked (test file removed). Signed download links are tested end to end."
	} else {
		res.Summary = "Storage round-trip failed."
		res.Problem = &core.Problem{Title: "STORAGE TEST FAILED", Provider: "cloudflare", Code: "server", Summary: "R2 did not complete an upload/download round trip."}
	}
	return res
}

func synthEmail(c *Checker) core.CheckResult {
	res := core.CheckResult{Target: c.componentOf(resend.KindDomain)}
	conn := c.Conn("resend")
	dom := c.resourceOf(resend.KindDomain)
	if conn == nil || dom == nil || c.State(dom.Key) == nil {
		res.Health, res.Summary = core.HealthUnknown, "Email not built yet."
		return res
	}
	if obs := c.obs[dom.Key]; obs != nil && providers.Str(obs.Props, "status") != "verified" {
		res.Health = core.HealthWarn
		res.Summary = "Skipped: the sending domain is not verified yet, so real emails cannot be sent."
		return res
	}
	from := "Backplane Health Check <healthcheck@" + c.State(dom.Key).Name + ">"
	start := time.Now()
	id, err := resend.Send(c.ctx, conn, from, resend.TestDelivered, "Backplane health check", "<p>Synthetic delivery test from Backplane. Safe to ignore.</p>", map[string]string{"backplane": "probe"})
	if err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("resend", "send a test email", err)
		res.Summary = res.Problem.Summary
		return res
	}
	steps := []core.StepResult{step("Resend accepted the email", core.HealthOK, "to "+resend.TestDelivered+" (Resend's test inbox)", time.Since(start).Milliseconds())}
	last := ""
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if !ctxSleep(c.ctx, 3*time.Second) {
			break
		}
		if ev, err := resend.GetEmail(c.ctx, conn, id); err == nil {
			last = ev
			if ev == "delivered" || ev == "bounced" || ev == "failed" || ev == "complained" {
				break
			}
		}
	}
	switch last {
	case "delivered":
		steps = append(steps, step("Delivery confirmed", core.HealthOK, "last event: delivered", time.Since(start).Milliseconds()))
		res.Health, res.Summary = core.HealthOK, "Resend accepted and delivered a test email."
	case "":
		steps = append(steps, step("Delivery confirmed", core.HealthWarn, "no delivery event yet", time.Since(start).Milliseconds()))
		res.Health, res.Summary = core.HealthWarn, "Resend accepted the email; delivery was not confirmed within 45 seconds."
	default:
		steps = append(steps, step("Delivery confirmed", core.HealthFail, "last event: "+last, time.Since(start).Milliseconds()))
		res.Health, res.Summary = core.HealthFail, "The test email was "+last+"."
		res.Problem = &core.Problem{Title: "EMAIL NOT DELIVERED", Provider: "resend", Code: "server", Summary: "Resend reported “" + last + "” for a test email to its own test inbox. Check the domain's DNS records."}
	}
	res.Steps = steps
	return res
}

func synthAuth(c *Checker) core.CheckResult {
	res := core.CheckResult{Target: c.componentOf(supabase.KindAuthConfig)}
	cl, _, prob := c.dbClient()
	if prob != nil {
		res.Health, res.Summary = core.HealthUnknown, prob.Title
		return res
	}
	email := "bp-probe-" + randomID() + "@example.com"
	password := "Bp!" + randomID() + randomID()
	var steps []core.StepResult
	start := time.Now()
	resp, err := cl.Do(c.ctx, httpx.Request{Method: "POST", Path: "/auth/v1/admin/users", JSON: map[string]any{"email": email, "password": password, "email_confirm": true, "user_metadata": map[string]any{"backplane_probe": true}}})
	if err != nil {
		steps = append(steps, step("Create temporary account", core.HealthFail, err.Error(), time.Since(start).Milliseconds()))
		res.Steps, res.Health, res.Summary = steps, core.HealthFail, "Could not create a temporary account."
		res.Problem = providers.Translate("supabase", "create a temporary account", err)
		return res
	}
	var user struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp.Body, &user)
	steps = append(steps, step("Create temporary account", core.HealthOK, email, time.Since(start).Milliseconds()))
	c.Defer(func(ctx2 contextLike) error {
		if user.ID == "" {
			return nil
		}
		_, err := cl.Do(ctx2, httpx.Request{Method: "DELETE", Path: "/auth/v1/admin/users/" + user.ID})
		return err
	})
	start = time.Now()
	resp, err = cl.Do(c.ctx, httpx.Request{Method: "POST", Path: "/auth/v1/token", Query: url.Values{"grant_type": {"password"}}, JSON: map[string]any{"email": email, "password": password}})
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err == nil {
		_ = json.Unmarshal(resp.Body, &tok)
	}
	if err != nil || tok.AccessToken == "" {
		msg := "no token returned"
		if err != nil {
			msg = err.Error()
		}
		steps = append(steps, step("Sign in and receive a token", core.HealthFail, msg, time.Since(start).Milliseconds()))
	} else {
		steps = append(steps, step("Sign in and receive a token", core.HealthOK, "", time.Since(start).Milliseconds()))
	}
	start = time.Now()
	if user.ID != "" {
		if _, err := cl.Do(c.ctx, httpx.Request{Method: "DELETE", Path: "/auth/v1/admin/users/" + user.ID}); err != nil {
			steps = append(steps, step("Delete temporary account", core.HealthFail, err.Error(), time.Since(start).Milliseconds()))
		} else {
			steps = append(steps, step("Delete temporary account", core.HealthOK, "", time.Since(start).Milliseconds()))
			user.ID = ""
		}
	}
	res.Steps = steps
	res.Health = core.HealthOK
	for _, s := range steps {
		res.Health = core.Worst(res.Health, s.Health)
	}
	if res.Health == core.HealthOK {
		res.Summary = "A temporary account signed up, signed in and was deleted."
	} else {
		res.Summary = "Sign-in flow failed."
		res.Problem = &core.Problem{Title: "SIGN-IN TEST FAILED", Provider: "supabase", Code: "drift", Summary: "Supabase Auth did not complete a password sign-in for a temporary account. Email/password sign-in may have been disabled."}
	}
	return res
}

func workerKeyOf(c *Checker) string {
	_, key := c.WorkerURL()
	return key
}

func synthCheckout(c *Checker) core.CheckResult {
	res := core.CheckResult{Target: c.componentOf(stripe.KindPrice)}
	conn := c.Conn("stripe")
	price, mode := c.probePrice()
	if conn == nil || price == nil {
		res.Health, res.Summary = core.HealthUnknown, "Payments not built yet."
		return res
	}
	if c.stripeLive() && !c.Opts.AllowLiveProbes {
		res.Health, res.Summary = core.HealthSkipped, "Skipped in live mode."
		return res
	}
	url, _ := c.WorkerURL()
	start := time.Now()
	cs, err := stripe.CreateCheckoutSessionMode(c.ctx, conn, c.State(price.Key).ID, mode, orStr(url, "https://example.com")+"/thanks", map[string]string{"backplane_probe": "1"}, "bp-synth-"+randomID())
	if err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("stripe", "create a checkout session", err)
		res.Summary = res.Problem.Summary
		return res
	}
	c.Defer(func(ctx2 contextLike) error { return stripe.ExpireCheckoutSession(ctx2, conn, cs.ID) })
	res.Health = core.HealthOK
	res.LatencyMS = time.Since(start).Milliseconds()
	res.Summary = fmt.Sprintf("Created test session for %s (expired afterwards; nothing charged).", stripe.Money(cs.AmountTotal, cs.Currency))
	return res
}
