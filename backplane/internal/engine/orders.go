package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/resend"
	"safisolutions.org/backplane/internal/providers/stripe"
)

// Guest orders: the Booking and Restaurant presets take bookings and orders
// from customers without accounts through the Worker (/book, /order).

func init() {
	RegisterLinkCheck("catalog_endpoint", checkCatalogEndpoint)
	RegisterScenario(Scenario{ID: "catalog_order", Title: "Customer orders and pays", Run: scenarioCatalogOrder})
}

// checkCatalogEndpoint proves the page → Worker link: the public catalog
// lists every item, and an order for something not for sale is refused
// without creating anything.
func checkCatalogEndpoint(c *Checker, l *core.LinkSpec) core.CheckResult {
	workerURL, workerKey := c.WorkerURL()
	path := c.P.Blueprint.Param("order_path")
	if workerURL == "" || path == "" {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Not built yet."}
	}
	start := time.Now()
	resp, err := c.workerCall("GET", "/products", nil, nil)
	if err != nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "The Worker's /products does not answer.",
			Problem: &core.Problem{Title: "MENU / SERVICES UNAVAILABLE", Provider: "cloudflare", Code: "network", Summary: "Your page can't list what's for sale: " + err.Error()}}
	}
	var cat struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	_ = json.Unmarshal(resp.Body, &cat)
	want := len(Catalog(&c.P.Blueprint))
	res := core.CheckResult{Health: core.HealthOK, LatencyMS: time.Since(start).Milliseconds(), Details: map[string]string{"catalog": workerURL + "/products", "orders": workerURL + path}}
	if len(cat.Items) != want {
		res.Health = core.HealthFail
		res.Expected, res.Actual = fmt.Sprintf("%d items", want), fmt.Sprintf("%d items", len(cat.Items))
		res.Summary = "The Worker is selling a different list than the Products tab."
		res.Problem = &core.Problem{Title: "CATALOG OUT OF DATE", Code: "drift", Summary: "The deployed Worker lists " + res.Actual + " but the backend sells " + res.Expected + ". Redeploy the Worker so its catalog matches.",
			Fixes: []core.Fix{{ID: "reapply:" + workerKey, Label: "Redeploy the Worker with the current catalog", Automatic: true, Action: "repair", Target: workerKey}}}
		return res
	}
	_, uerr := c.workerCall("POST", path, []byte(`{"service":"bp_no_such_item","items":[{"key":"bp_no_such_item","quantity":1}],"email":"probe@example.com","starts_at":"2099-01-01T10:00:00Z"}`),
		http.Header{"Content-Type": {"application/json"}}, true)
	if e, ok := httpx.AsError(uerr); !ok || e.Status != 400 {
		res.Health = core.HealthFail
		res.Summary = "The Worker did not refuse an item that isn't for sale."
		res.Problem = &core.Problem{Title: "ORDERS ACCEPT UNKNOWN ITEMS", Code: "drift", Summary: "POST " + path + " accepted an item that isn't in the catalog. The deployed code isn't the version Backplane generated; redeploy it.",
			Fixes: []core.Fix{{ID: "reapply:" + workerKey, Label: "Redeploy the generated Worker", Automatic: true, Action: "repair", Target: workerKey}}}
		return res
	}
	res.Summary = fmt.Sprintf("Lists %d item%s for your page; unknown items are refused.", want, map[bool]string{true: "", false: "s"}[want == 1])
	return res
}

// scenarioCatalogOrder runs a guest booking or order through the deployed
// backend with synthetic data that is removed afterwards:
//
//	order via the Worker → row held at catalog prices → signed payment event
//	→ row confirmed → confirmation email → replayed event ignored.
func scenarioCatalogOrder(c *Checker, sc *core.ScenarioSpec) core.CheckResult {
	res := core.CheckResult{}
	var steps []core.StepResult
	title := func(i int) string {
		if i < len(sc.Steps) {
			return sc.Steps[i]
		}
		return fmt.Sprintf("Step %d", i+1)
	}
	fail := func(detail string, prob *core.Problem) core.CheckResult {
		t := title(len(steps))
		steps = append(steps, step(t, core.HealthFail, detail, 0))
		for i := len(steps); i < len(sc.Steps); i++ {
			steps = append(steps, step(sc.Steps[i], core.HealthSkipped, "not reached", 0))
		}
		res.Steps, res.Health, res.Summary = steps, core.HealthFail, "Stopped at: "+t+" — "+detail
		if prob == nil {
			prob = &core.Problem{Title: "ORDER JOURNEY BROKEN AT: " + upper(t), Code: "drift", Summary: detail}
		}
		res.Problem = prob
		return res
	}
	ok := func(detail string, start time.Time) {
		steps = append(steps, step(title(len(steps)), core.HealthOK, detail, time.Since(start).Milliseconds()))
	}
	bp := &c.P.Blueprint
	workerURL, workerKey := c.WorkerURL()
	path := bp.Param("order_path")
	hook := c.resourceOf(stripe.KindWebhook)
	items := Catalog(bp)
	if workerURL == "" || path == "" || hook == nil || c.State(hook.Key) == nil || len(items) == 0 {
		res.Health, res.Summary = core.HealthUnknown, "Build the backend before running the end-to-end test."
		return res
	}
	if c.stripeLive() && !c.Opts.AllowLiveProbes {
		res.Health, res.Summary = core.HealthSkipped, "Skipped in live mode (enable “live probes” to run it; nothing is ever charged)."
		return res
	}
	secret, err := c.Secret(hook.Key, "secret")
	if err != nil {
		return fail("the webhook signing secret is missing from the vault", &core.Problem{Title: "WEBHOOK SECRET MISSING", Code: "auth",
			Summary: "Backplane no longer has this backend's Stripe webhook secret. Recreate the webhook (the Worker is updated automatically).",
			Fixes:   []core.Fix{{ID: "reapply:" + hook.Key, Label: "Recreate the webhook", Automatic: true, Action: "repair", Target: hook.Key}}})
	}
	token, _ := c.Gen("probe_token")
	booking := bp.Param("template") == "booking"
	table, amountCol, pending, confirmed := "orders", "total_cents", "awaiting_payment", "received"
	var body map[string]any
	var expect int64
	if booking {
		table, amountCol, pending, confirmed = "bookings", "deposit_cents", "pending", "confirmed"
		// A slot far enough ahead that no reminder is due, unique per run.
		at := time.Now().UTC().Add(9*24*time.Hour + time.Duration(time.Now().UnixNano()%600)*time.Minute).Truncate(time.Minute)
		body = map[string]any{"service": items[0].Key, "starts_at": at.Format(time.RFC3339), "email": resend.TestDelivered, "name": "Backplane Probe", "notes": "Synthetic health check — removed automatically"}
		expect = items[0].Cents()
	} else {
		body = map[string]any{"items": []any{map[string]any{"key": items[0].Key, "quantity": 2, "price": "price_from_browser", "amount": 1}}, "email": resend.TestDelivered, "name": "Backplane Probe",
			"notes": "Synthetic health check — removed automatically"}
		expect = 2 * items[0].Cents()
	}
	currency := orStr(bp.Param("currency"), "usd")

	// 1. The customer places the order through the Worker.
	start := time.Now()
	raw, _ := json.Marshal(body)
	resp, err := c.workerCall("POST", path, raw, http.Header{"Content-Type": {"application/json"}, "X-Backplane-Probe": {token}})
	if err != nil {
		return fail("the Worker refused the order: "+err.Error(), &core.Problem{Title: "CUSTOMERS CANNOT " + map[bool]string{true: "BOOK", false: "ORDER"}[booking], Code: "drift",
			Summary: "POST " + path + " failed: " + err.Error(), Fixes: diagnoseProbe("stripe", err.Error(), c)})
	}
	var placed struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
	}
	_ = json.Unmarshal(resp.Body, &placed)
	if placed.ID == "" || placed.SessionID == "" {
		return fail("the Worker answered without a booking/order id and checkout session", nil)
	}
	cl, _, prob := c.dbClient()
	if prob != nil {
		return fail(prob.Title, prob)
	}
	c.Defer(func(ctx contextLike) error {
		_, err := cl.Do(ctx, httpx.Request{Method: "DELETE", Path: "/rest/v1/" + table, Query: url.Values{"id": {"eq." + placed.ID}}})
		return err
	})
	if conn := c.Conn("stripe"); conn != nil {
		c.Defer(func(ctx contextLike) error { _ = stripe.ExpireCheckoutSession(ctx, conn, placed.SessionID); return nil })
	}
	ok("checkout session "+placed.SessionID, start)

	// 2. The row is held at catalog prices while the customer pays.
	start = time.Now()
	readRow := func() (map[string]any, error) {
		r, err := cl.Do(c.ctx, httpx.Request{Method: "GET", Path: "/rest/v1/" + table, Query: url.Values{"id": {"eq." + placed.ID}, "select": {"id,status,paid,stripe_session_id," + amountCol}}})
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		_ = json.Unmarshal(r.Body, &rows)
		if len(rows) != 1 {
			return nil, fmt.Errorf("%d rows for one %s", len(rows), map[bool]string{true: "booking", false: "order"}[booking])
		}
		return rows[0], nil
	}
	row, err := readRow()
	if err != nil {
		return fail(err.Error(), nil)
	}
	if got := providers.Int(row, amountCol); got != expect {
		return fail(fmt.Sprintf("recorded %s instead of %s", stripe.Money(got, currency), stripe.Money(expect, currency)), &core.Problem{Title: "PRICES NOT ENFORCED", Code: "drift",
			Summary: "The Worker recorded an amount that doesn't match the catalog. Redeploy the generated Worker.",
			Fixes:   []core.Fix{{ID: "reapply:" + workerKey, Label: "Redeploy the generated Worker", Automatic: true, Action: "repair", Target: workerKey}}})
	}
	if providers.Str(row, "status") != pending || providers.Str(row, "stripe_session_id") != placed.SessionID {
		return fail("status "+providers.Str(row, "status")+", session "+providers.Str(row, "stripe_session_id"), nil)
	}
	ok(stripe.Money(expect, currency)+" from the catalog; "+pending, start)

	// 3. Payment → signed checkout.session.completed.
	eventID := "evt_bp" + randomID()
	event := map[string]any{
		"id": eventID, "object": "event", "type": "checkout.session.completed", "api_version": stripe.APIVersion, "created": time.Now().Unix(), "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": placed.SessionID, "object": "checkout.session", "livemode": false, "mode": "payment", "status": "complete", "payment_status": "paid",
			"amount_total": expect, "currency": currency, "customer_details": map[string]any{"email": resend.TestDelivered, "name": "Backplane Probe"},
			"metadata": map[string]any{"row_table": table, "row_id": placed.ID, "backplane_probe": "1", "items": items[0].Name, "item_keys": items[0].Key + ":1"},
		}},
	}
	payload, _ := json.Marshal(event)
	steps = append(steps, step(title(len(steps)), core.HealthOK, stripe.Money(expect, currency)+" (test event, nothing charged)", 0))
	start = time.Now()
	send := func() (*httpx.Response, error) {
		hdr := http.Header{"Stripe-Signature": {stripe.SignPayload(secret, payload, time.Now())}}
		return c.ProbeClient().Do(c.ctx, httpx.Request{Method: "POST", Path: workerURL + "/stripe/webhook", Body: payload, ContentType: "application/json", Header: hdr, Resource: workerKey})
	}
	resp, err = send()
	if err != nil {
		return fail(err.Error(), nil)
	}
	var out struct {
		Received bool `json:"received"`
		Probe    struct {
			RowID      string `json:"row_id"`
			EmailID    string `json:"email_id"`
			EmailError string `json:"email_error"`
		} `json:"probe"`
	}
	_ = json.Unmarshal(resp.Body, &out)
	if !out.Received {
		return fail("unexpected response from the Worker", nil)
	}
	ok("signature verified", start)

	// 4. The row is paid and confirmed.
	start = time.Now()
	if row, err = readRow(); err != nil {
		return fail(err.Error(), nil)
	}
	if !providers.Bool(row, "paid") || providers.Str(row, "status") != confirmed {
		return fail(fmt.Sprintf("paid=%v, status %s (expected %s)", providers.Bool(row, "paid"), providers.Str(row, "status"), confirmed), nil)
	}
	ok(confirmed, start)

	// 5. Confirmation email.
	start = time.Now()
	emailStep := step(title(len(steps)), core.HealthOK, "", 0)
	switch {
	case out.Probe.EmailError != "":
		emailStep.Health, emailStep.Detail = core.HealthFail, "Resend refused the email: "+out.Probe.EmailError
	case out.Probe.EmailID == "":
		emailStep.Health, emailStep.Detail = core.HealthFail, "the Worker did not send a confirmation"
	default:
		emailStep.Detail = "accepted by Resend"
		if conn := c.Conn("resend"); conn != nil {
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				if ev, err := resend.GetEmail(c.ctx, conn, out.Probe.EmailID); err == nil && (ev == "delivered" || ev == "bounced" || ev == "failed") {
					if ev == "delivered" {
						emailStep.Detail = "delivered to Resend's test inbox"
					} else {
						emailStep.Health, emailStep.Detail = core.HealthFail, "Resend reports "+ev
					}
					break
				}
				if !ctxSleep(c.ctx, 3*time.Second) {
					break
				}
			}
		}
	}
	emailStep.LatencyMS = time.Since(start).Milliseconds()
	if emailStep.Health == core.HealthFail {
		detail := emailStep.Detail
		return fail("confirmation email: "+detail, &core.Problem{Title: "CONFIRMATION EMAIL FAILED", Provider: "resend", Code: "drift",
			Summary: "Payments are recorded, but customers are not getting their confirmation: " + detail + ".", Fixes: diagnoseProbe("resend", detail, c)})
	}
	steps = append(steps, emailStep)

	// 6. A retried delivery must not confirm (or email) twice.
	start = time.Now()
	resp, err = send()
	dup := false
	if err == nil {
		var again struct {
			Duplicate bool `json:"duplicate"`
		}
		_ = json.Unmarshal(resp.Body, &again)
		dup = again.Duplicate
	}
	if !dup {
		return fail("a repeated Stripe event was processed twice (Stripe retries deliveries, so customers would get duplicate confirmations)", nil)
	}
	final := core.HealthOK
	for _, s := range steps {
		final = core.Worst(final, s.Health)
	}
	steps = append(steps, step(title(len(steps)), final, "duplicate event ignored; synthetic "+map[bool]string{true: "booking", false: "order"}[booking]+" removed", time.Since(start).Milliseconds()))
	res.Steps, res.Health = steps, final
	if booking {
		res.Summary = "A customer can book a time, pay the deposit, get confirmed and receive the email."
	} else {
		res.Summary = "A customer can order from the menu at menu prices, pay, reach the kitchen feed and receive the email."
	}
	return res
}

func upper(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'a' && ch <= 'z' {
			b[i] = ch - 32
		}
	}
	return string(b)
}
