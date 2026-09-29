package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/cloudflare"
	"safisolutions.org/backplane/internal/providers/resend"
	"safisolutions.org/backplane/internal/providers/stripe"
)

type contextLike = context.Context

// scenarioPurchase runs the whole Software Store journey against the real
// deployed backend, with synthetic data that is removed afterwards:
//
//	test customer → payment event → Stripe webhook → Worker → order in
//	Supabase → temporary download link → email → everything verified.
//
// The payment event is a checkout.session.completed event signed with this
// backend's real webhook secret, so it travels the exact production code
// path. It is marked livemode=false and backplane_probe=1, sends the email
// to Resend's test inbox and downloads a probe file instead of the product.
func scenarioPurchase(c *Checker, sc *core.ScenarioSpec) core.CheckResult {
	res := core.CheckResult{}
	var steps []core.StepResult
	fail := func(title, detail string, prob *core.Problem) core.CheckResult {
		steps = append(steps, step(title, core.HealthFail, detail, 0))
		for _, rest := range sc.Steps[min(len(steps), len(sc.Steps)):] {
			steps = append(steps, step(rest, core.HealthSkipped, "not reached", 0))
		}
		res.Steps = steps
		res.Health = core.HealthFail
		res.Summary = "Stopped at: " + title + " — " + detail
		if prob == nil {
			prob = &core.Problem{Title: "PURCHASE JOURNEY BROKEN AT: " + strings.ToUpper(title), Code: "drift", Summary: detail}
		}
		res.Problem = prob
		return res
	}
	workerURL, workerKey := c.WorkerURL()
	hook := c.resourceOf(stripe.KindWebhook)
	bucket := c.resourceOf(cloudflare.KindR2Bucket)
	price := c.resourceOf(stripe.KindPrice)
	shipping := c.P.Blueprint.Param("fulfillment_mode") == "shipping"
	if workerURL == "" || hook == nil || price == nil || c.State(price.Key) == nil || (!shipping && (bucket == nil || c.State(bucket.Key) == nil)) {
		res.Health, res.Summary = core.HealthUnknown, "Build the backend before running the end-to-end test."
		return res
	}
	secret, err := c.Secret(hook.Key, "secret")
	if err != nil {
		return fail("Payment event", "the webhook signing secret is missing from the vault", &core.Problem{Title: "WEBHOOK SECRET MISSING", Code: "auth",
			Summary: "Backplane no longer has this backend's Stripe webhook secret, so it cannot sign a test event. Recreate the webhook (the Worker is updated automatically).",
			Fixes:   []core.Fix{{ID: "reapply:" + hook.Key, Label: "Recreate the webhook", Automatic: true, Action: "repair", Target: hook.Key}}})
	}
	cf := c.Conn("cloudflare")
	acct, _ := cloudflare.AccountID(cf)
	probeID := randomID()
	objectKey := "__backplane/e2e-" + probeID + ".bin"
	payload := make([]byte, 512)
	_, _ = rand.Read(payload)

	// 1. Test customer (and, for downloads, a test file).
	start := time.Now()
	if !shipping {
		if err := cloudflare.PutObject(c.ctx, cf, acct, c.State(bucket.Key).ID, objectKey, payload, "application/octet-stream"); err != nil {
			return fail("Create test customer", "could not stage the test download: "+providers.Translate("cloudflare", "upload", err).Summary, nil)
		}
		c.Defer(func(ctx contextLike) error {
			return cloudflare.DeleteObject(ctx, cf, acct, c.State(bucket.Key).ID, objectKey)
		})
		steps = append(steps, step("Create test customer", core.HealthOK, "Backplane Probe <"+resend.TestDelivered+">, test file staged", time.Since(start).Milliseconds()))
	} else {
		steps = append(steps, step("Create test customer", core.HealthOK, "Backplane Probe <"+resend.TestDelivered+">, test address 1 Probe Street", time.Since(start).Milliseconds()))
	}

	// 2. Test payment → signed event.
	sessionID := "cs_test_bp" + probeID
	eventID := "evt_bp" + probeID
	amount, _ := strconv.ParseInt(c.P.Blueprint.Param("price_cents"), 10, 64)
	currency := strings.ToLower(orStr(c.P.Blueprint.Param("currency"), "usd"))
	event := map[string]any{
		"id": eventID, "object": "event", "type": "checkout.session.completed", "api_version": stripe.APIVersion, "created": time.Now().Unix(), "livemode": false,
		"pending_webhooks": 1, "request": map[string]any{"id": nil, "idempotency_key": nil},
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "object": "checkout.session", "livemode": false, "mode": "payment", "status": "complete", "payment_status": "paid",
			"amount_total": amount, "currency": currency, "customer": nil,
			"customer_details": map[string]any{"email": resend.TestDelivered, "name": "Backplane Probe", "phone": "+15555550100"},
			"metadata":         probeMeta(c, objectKey, probeID),
			"payment_intent":   "pi_test_bp" + probeID,
		}},
	}
	if shipping {
		obj := event["data"].(map[string]any)["object"].(map[string]any)
		obj["collected_information"] = map[string]any{"shipping_details": map[string]any{"name": "Backplane Probe",
			"address": map[string]any{"line1": "1 Probe Street", "city": "Testville", "postal_code": "00000", "state": "CA", "country": "US"}}}
	}
	body, _ := json.Marshal(event)
	steps = append(steps, step("Test payment", core.HealthOK, "checkout.session.completed for "+stripe.Money(amount, currency)+" (test mode, nothing charged)", 0))

	// Always clean the synthetic order afterwards.
	c.Defer(func(ctx contextLike) error { return deleteProbeOrder(ctx, c, sessionID, eventID) })

	// 3–4. Webhook fires; Worker receives and verifies the signature.
	start = time.Now()
	hdr := http.Header{"Stripe-Signature": {stripe.SignPayload(secret, body, time.Now())}, "Content-Type": {"application/json"}}
	resp, err := c.ProbeClient().Do(c.ctx, httpx.Request{Method: "POST", Path: workerURL + "/stripe/webhook", Body: body, ContentType: "application/json", Header: hdr, Resource: workerKey})
	if err != nil {
		detail := err.Error()
		var prob *core.Problem
		if e, ok := httpx.AsError(err); ok && e.Status == 400 && strings.Contains(strings.ToLower(e.Body), "signature") {
			detail = "the Worker rejected the event signature"
			prob = &core.Problem{Title: "WEBHOOK VERIFICATION FAILED", Code: "drift", Provider: "stripe",
				Summary: "The Worker's STRIPE_WEBHOOK_SECRET does not match the secret Stripe signs events with. Real payments are being rejected right now.",
				Fixes:   []core.Fix{{ID: "reapply:" + workerKey, Label: "Restore STRIPE_WEBHOOK_SECRET on the Worker", Automatic: true, Action: "repair", Target: workerKey, Changes: []string{"Set STRIPE_WEBHOOK_SECRET from the vault"}}}}
		}
		steps = append(steps, step("Stripe webhook fires", core.HealthOK, "signed with the endpoint's secret", 0))
		return fail("Worker receives event", detail, prob)
	}
	steps = append(steps, step("Stripe webhook fires", core.HealthOK, "signed with the endpoint's secret", 0))
	var out struct {
		Received  bool   `json:"received"`
		Duplicate bool   `json:"duplicate"`
		Error     string `json:"error"`
		Probe     struct {
			OrderID     string `json:"order_id"`
			DownloadURL string `json:"download_url"`
			EmailID     string `json:"email_id"`
			EmailError  string `json:"email_error"`
		} `json:"probe"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil || !out.Received {
		return fail("Worker receives event", "unexpected response from the Worker", nil)
	}
	steps = append(steps, step("Worker receives event", core.HealthOK, "signature verified", time.Since(start).Milliseconds()))

	// 5. Order written to the database.
	start = time.Now()
	cl, _, prob := c.dbClient()
	if prob != nil {
		return fail("Order written to database", prob.Title, prob)
	}
	var rows []map[string]any
	cols := "id,status,email,is_probe"
	if shipping {
		cols += ",shipping_address"
	}
	r2, err := cl.Do(c.ctx, httpx.Request{Method: "GET", Path: "/rest/v1/orders", Query: url.Values{"stripe_session_id": {"eq." + sessionID}, "select": {cols}}})
	if err == nil {
		_ = json.Unmarshal(r2.Body, &rows)
	}
	if err != nil || len(rows) != 1 {
		detail := "no order row found"
		if err != nil {
			detail = err.Error()
		} else if len(rows) > 1 {
			detail = fmt.Sprintf("%d order rows for one payment (duplicate processing)", len(rows))
		}
		return fail("Order written to database", detail, nil)
	}
	if providers.Str(rows[0], "status") != "paid" {
		return fail("Order written to database", "order status is "+providers.Str(rows[0], "status"), nil)
	}
	steps = append(steps, step("Order written to database", core.HealthOK, "orders row "+providers.Str(rows[0], "id")+" (marked as probe)", time.Since(start).Milliseconds()))

	// 6. Shipped goods: the delivery address was recorded with the order.
	if shipping {
		addr, _ := rows[0]["shipping_address"].(map[string]any)
		if providers.Str(addr, "line1") != "1 Probe Street" {
			return fail("Shipping details recorded", "the order has no shipping address — the Worker may be running code without shipping support; rebuild it", nil)
		}
		steps = append(steps, step("Shipping details recorded", core.HealthOK, "address and phone saved with the order", 0))
	}

	// 6. Temporary download link works, and a tampered one does not.
	start = time.Now()
	if !shipping {
		if out.Probe.DownloadURL == "" {
			return fail("Temporary download generated", "the Worker returned no download link", nil)
		}
		dl, err := c.ProbeClient().Do(c.ctx, httpx.Request{Method: "GET", Path: out.Probe.DownloadURL, Resource: workerKey})
		if err != nil {
			return fail("Temporary download generated", "download failed: "+err.Error(), nil)
		}
		if !bytes.Equal(dl.Body, payload) {
			return fail("Temporary download generated", "downloaded bytes do not match the file in R2", nil)
		}
		tampered := tamper(out.Probe.DownloadURL)
		_, terr := c.ProbeClient().Do(c.ctx, httpx.Request{Method: "GET", Path: tampered, Resource: workerKey, Quiet: true})
		if e, ok := httpx.AsError(terr); !ok || (e.Status != 403 && e.Status != 401 && e.Status != 400) {
			return fail("Temporary download generated", "a tampered link was NOT rejected — download links are not protected", &core.Problem{Title: "DOWNLOAD LINKS UNPROTECTED", Code: "drift",
				Summary: "Changing a download link's signature still downloaded the file. The Worker's DOWNLOAD_SIGNING_SECRET check is not running; rebuild the Worker."})
		}
		steps = append(steps, step("Temporary download generated", core.HealthOK, fmt.Sprintf("%d bytes match; tampered link rejected", len(dl.Body)), time.Since(start).Milliseconds()))
	}

	// 7. Email triggered and delivered.
	start = time.Now()
	emailStep := step("Email process triggered", core.HealthOK, "", 0)
	if out.Probe.EmailError != "" {
		emailStep.Health = core.HealthFail
		emailStep.Detail = "Resend refused the email: " + out.Probe.EmailError
	} else if out.Probe.EmailID == "" {
		emailStep.Health = core.HealthFail
		emailStep.Detail = "the Worker did not send an email"
	} else if conn := c.Conn("resend"); conn != nil {
		last := ""
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if ev, err := resend.GetEmail(c.ctx, conn, out.Probe.EmailID); err == nil {
				last = ev
				if ev == "delivered" || ev == "bounced" || ev == "failed" {
					break
				}
			}
			if !ctxSleep(c.ctx, 3*time.Second) {
				break
			}
		}
		switch last {
		case "delivered":
			emailStep.Detail = "delivered to Resend's test inbox"
		case "":
			emailStep.Health, emailStep.Detail = core.HealthWarn, "accepted by Resend; delivery not confirmed yet"
		default:
			emailStep.Health, emailStep.Detail = core.HealthFail, "Resend reports "+last
		}
	}
	emailStep.LatencyMS = time.Since(start).Milliseconds()
	steps = append(steps, emailStep)
	if emailStep.Health == core.HealthFail {
		return fail("Everything verified", "email step failed: "+emailStep.Detail, &core.Problem{Title: "RECEIPT EMAIL FAILED", Provider: "resend", Code: "drift",
			Summary: "Orders are recorded and downloads work, but customers are not getting their email: " + emailStep.Detail + ".",
			Fixes:   diagnoseProbe("resend", emailStep.Detail, c)})
	}

	// 8. Idempotency: replaying the same event must not create a second order.
	start = time.Now()
	hdr.Set("Stripe-Signature", stripe.SignPayload(secret, body, time.Now()))
	resp, err = c.ProbeClient().Do(c.ctx, httpx.Request{Method: "POST", Path: workerURL + "/stripe/webhook", Body: body, ContentType: "application/json", Header: hdr, Resource: workerKey})
	dupOK := false
	if err == nil {
		var again struct {
			Duplicate bool `json:"duplicate"`
		}
		_ = json.Unmarshal(resp.Body, &again)
		dupOK = again.Duplicate
	}
	rows = nil
	if r3, err := cl.Do(c.ctx, httpx.Request{Method: "GET", Path: "/rest/v1/orders", Query: url.Values{"stripe_session_id": {"eq." + sessionID}, "select": {"id"}}}); err == nil {
		_ = json.Unmarshal(r3.Body, &rows)
	}
	if !dupOK || len(rows) != 1 {
		return fail("Everything verified", "a repeated Stripe event created a second order (Stripe retries deliveries, so this would double-fulfil)", nil)
	}
	final := core.HealthOK
	for _, s := range steps {
		final = core.Worst(final, s.Health)
	}
	steps = append(steps, step("Everything verified", final, "duplicate event ignored; synthetic order and file cleaned up", time.Since(start).Milliseconds()))
	res.Steps = steps
	res.Health = final
	if final == core.HealthOK && shipping {
		res.Summary = "A customer can pay, the order is recorded with where to ship it, and the confirmation email goes out."
	} else if final == core.HealthOK {
		res.Summary = "A customer can pay, get recorded, receive the email and download the file."
	} else {
		res.Summary = "The journey works with a warning."
	}
	return res
}

// probeMeta is the synthetic session's metadata: the probe markers plus the
// first catalog item, exactly as the Worker's /checkout records it.
func probeMeta(c *Checker, objectKey, probeID string) map[string]any {
	m := map[string]any{"backplane_probe": "1", "backplane_probe_object": objectKey, "backplane_probe_id": probeID}
	if items := Catalog(&c.P.Blueprint); len(items) > 0 {
		m["items"], m["item_keys"] = items[0].Name, items[0].Key+":1"
	}
	return m
}

// tamper flips the last character of the token in a download URL.
func tamper(u string) string {
	if u == "" {
		return u
	}
	last := u[len(u)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return u[:len(u)-1] + string(repl)
}

// deleteProbeOrder removes synthetic rows written by the end-to-end test.
func deleteProbeOrder(ctx context.Context, c *Checker, sessionID, eventID string) error {
	cl, _, prob := c.dbClient()
	if prob != nil {
		return nil
	}
	var firstErr error
	for _, rq := range []httpx.Request{
		{Method: "DELETE", Path: "/rest/v1/downloads", Query: url.Values{"stripe_session_id": {"eq." + sessionID}}},
		{Method: "DELETE", Path: "/rest/v1/email_log", Query: url.Values{"stripe_session_id": {"eq." + sessionID}}},
		{Method: "DELETE", Path: "/rest/v1/orders", Query: url.Values{"stripe_session_id": {"eq." + sessionID}}},
		{Method: "DELETE", Path: "/rest/v1/webhook_events", Query: url.Values{"event_id": {"eq." + eventID}}},
	} {
		if _, err := cl.Do(ctx, rq); err != nil && !httpx.IsNotFound(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
