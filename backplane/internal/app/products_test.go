package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/sim"
)

// workerURL returns a built project's Worker address in the simulator.
func workerURL(t *testing.T, a *App, id string) string {
	t.Helper()
	man, err := a.Store.LoadManifest(id, core.EnvProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range man.Resources {
		if st.Kind == "cloudflare.worker" && st.Output("url") != "" {
			return st.Output("url")
		}
	}
	t.Fatal("no Worker url")
	return ""
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// stripeSession reads a Checkout Session from the simulator.
func stripeSession(t *testing.T, a *App, id string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", a.Engine.PracticeBaseURLs["stripe"]+"/v1/checkout/sessions/"+id, nil)
	cs, _ := a.Store.LoadConnections()
	for _, c := range cs {
		if c.Practice && c.Provider == "stripe" {
			key, _ := a.Vault.GetString(c.SecretRef)
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func simServer(a *App) *sim.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.simServer
}

// rows reads a table from the practice project's database with the Worker's key.
func rows(t *testing.T, a *App, id, table, query string) []map[string]any {
	t.Helper()
	man, _ := a.Store.LoadManifest(id, core.EnvProduction)
	var ref string
	for _, st := range man.Resources {
		if st.Kind == "supabase.project" {
			ref = st.ID
		}
	}
	key, err := a.Vault.GetString(engine.ResourceSecretKey(id, core.EnvProduction, "db_key", "key"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", a.Engine.PracticeBaseURLs["supabase_project"]+"/"+ref+"/rest/v1/"+table+"?"+query, nil)
	req.Header.Set("apikey", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func requireOperational(t *testing.T, a *App, id string) *core.HealthReport {
	t.Helper()
	rep := fullCheck(t, a, id)
	if rep.Headline != "FULLY OPERATIONAL" {
		dumpReport(t, rep)
		t.Fatalf("expected FULLY OPERATIONAL, got %s", rep.Headline)
	}
	return rep
}

// A cart of several products at catalog prices; the browser can't change them.
func TestPracticeCartCheckoutUsesCatalogPrices(t *testing.T) {
	a := newPracticeApp(t)
	id := buildPractice(t, a, "ecommerce", map[string]any{"domain": "candleshop.com", "ship_countries": "US,CA", "products": []any{
		map[string]any{"name": "Hand-poured candle", "price": 24.0},
		map[string]any{"name": "Wick trimmer", "price": 8.5},
	}})
	requireOperational(t, a, id)
	api := workerURL(t, a, id)

	cat := getJSON(t, api+"/products")
	if items, _ := cat["items"].([]any); len(items) != 2 || cat["cart"] != true {
		t.Fatalf("/products = %v", cat)
	}
	cases := []struct {
		body   any
		status int
		why    string
	}{
		{map[string]any{"items": []any{map[string]any{"key": "nope", "quantity": 1}}}, 400, "unknown item"},
		{map[string]any{"items": []any{map[string]any{"key": "hand_poured_candle", "quantity": 21}}}, 400, "quantity over the limit"},
		{map[string]any{"items": []any{map[string]any{"key": "hand_poured_candle", "quantity": 1.5}}}, 400, "fractional quantity"},
		{map[string]any{"items": []any{map[string]any{"key": "hand_poured_candle", "quantity": 0}}}, 400, "zero quantity"},
		{map[string]any{"items": []any{map[string]any{"key": "hand_poured_candle"}}, "interval": "month"}, 400, "interval not sold"},
	}
	for _, tc := range cases {
		if st, out := post(t, api+"/checkout", tc.body); st != tc.status {
			t.Errorf("%s: status %d (%v), want %d", tc.why, st, out, tc.status)
		}
	}

	st, out := post(t, api+"/checkout", map[string]any{"items": []any{
		map[string]any{"key": "hand_poured_candle", "quantity": 2, "price": "price_fake", "unit_amount": 1},
		map[string]any{"key": "wick_trimmer", "quantity": 1},
	}, "amount": 1})
	if st != 200 {
		t.Fatalf("checkout: %d %v", st, out)
	}
	sessionID := out["id"].(string)
	cs := stripeSession(t, a, sessionID)
	if got := fmt.Sprint(cs["amount_total"]); got != "5650" {
		t.Fatalf("session total %s, want 5650 (2 × 24.00 + 8.50)", got)
	}
	if _, err := simServer(a).Apply(sim.Control{Action: "pay_session", Target: sessionID, Value: "buyer@example.com"}); err != nil {
		t.Fatal(err)
	}
	var order map[string]any
	waitFor(t, "the paid order", func() bool {
		rs := rows(t, a, id, "orders", "stripe_session_id=eq."+sessionID)
		if len(rs) == 1 {
			order = rs[0]
		}
		return order != nil
	})
	if fmt.Sprint(order["amount_total"]) != "5650" || order["status"] != "paid" || !strings.Contains(fmt.Sprint(order["product"]), "Hand-poured candle × 2") {
		t.Fatalf("order: %v", order)
	}
	if items, _ := order["items"].([]any); len(items) != 2 {
		t.Fatalf("order items: %v", order["items"])
	}
	if addr, _ := order["shipping_address"].(map[string]any); addr["line1"] != "1 Practice Lane" {
		t.Fatalf("shipping address not recorded: %v", order["shipping_address"])
	}
}

// Changing a price after the build: the plan replaces the Stripe price and
// updates the Worker; checkout then charges the new amount.
func TestPracticeChangePriceAfterBuild(t *testing.T) {
	ctx := context.Background()
	a := newPracticeApp(t)
	id := buildPractice(t, a, "digital-downloads", map[string]any{"domain": "templates.shop", "products": []any{
		map[string]any{"key": "pack", "name": "Template pack", "price": 12.0},
	}})
	requireOperational(t, a, id)
	man, _ := a.Store.LoadManifest(id, core.EnvProduction)
	oldPrice := man.Resources["price_pack_once"].ID

	// New price, plus a second item.
	if _, err := a.UpdateProject(ctx, UpdateProjectParams{ID: id, Answers: map[string]any{"products": []any{
		map[string]any{"key": "pack", "name": "Template pack", "price": 15.0},
		map[string]any{"name": "Bundle", "price": 30.0},
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := a.Plan(ctx, PlanParams{ProjectID: id, Env: core.EnvProduction})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, op := range plan.Operations {
		actions[op.ResourceKey] = op.Action
	}
	if actions["price_pack_once"] != core.ActUpdate || actions["api"] != core.ActUpdate || actions["price_bundle_once"] != core.ActCreate || actions["product_pack"] != core.ActKeep {
		t.Fatalf("plan actions: %v", actions)
	}
	run, err := a.Approve(ctx, ApproveParams{PlanID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	a.Engine.Wait(run.ID, 2*time.Minute)
	if rv, _ := a.GetRun(ctx, RunParams{ProjectID: id, Env: core.EnvProduction, RunID: run.ID}); rv.Status != core.RunSucceeded {
		dumpRun(t, rv)
		t.Fatalf("price change run: %s", rv.Status)
	}
	man, _ = a.Store.LoadManifest(id, core.EnvProduction)
	newPrice := man.Resources["price_pack_once"]
	if newPrice.ID == oldPrice || newPrice.Output("amount") != "1500" {
		t.Fatalf("price not replaced: %s → %s (%v)", oldPrice, newPrice.ID, newPrice.Outputs)
	}
	api := workerURL(t, a, id)
	st, out := post(t, api+"/checkout", map[string]any{"item": "pack"})
	if st != 200 {
		t.Fatalf("checkout after price change: %d %v", st, out)
	}
	if cs := stripeSession(t, a, out["id"].(string)); fmt.Sprint(cs["amount_total"]) != "1500" {
		t.Fatalf("checkout charges %v after the change, want 1500", cs["amount_total"])
	}
	requireOperational(t, a, id)
	// Re-planning is a no-op.
	plan, _ = a.Plan(ctx, PlanParams{ProjectID: id, Env: core.EnvProduction})
	for _, op := range plan.Operations {
		if op.Action != core.ActKeep {
			t.Errorf("re-plan wants to %s %s", op.Action, op.Title)
		}
	}
	// The Products view reports both items as live.
	pv, err := a.Products(ctx, DashboardParams{ProjectID: id, Env: core.EnvProduction})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Items) != 2 || pv.Items[0].Prices[0].Status != "live" || pv.Items[0].Prices[0].Amount != 1500 {
		t.Fatalf("products view: %+v", pv.Items)
	}
}

// Monthly and yearly plans in a download store: subscription checkout, and a
// canceled subscription ends access.
func TestPracticeSubscriptionProduct(t *testing.T) {
	a := newPracticeApp(t)
	id := buildPractice(t, a, "license-server", map[string]any{"domain": "tool.dev", "products": []any{
		map[string]any{"key": "pro", "name": "Pro license", "price": 9.0, "billing": "month"},
		map[string]any{"key": "lifetime", "name": "Lifetime license", "price": 199.0},
	}})
	requireOperational(t, a, id)
	api := workerURL(t, a, id)
	if st, out := post(t, api+"/checkout", map[string]any{"item": "pro", "quantity": 2}); st != 400 {
		t.Fatalf("digital items are one per checkout, got %d %v", st, out)
	}
	st, out := post(t, api+"/checkout", map[string]any{"item": "pro"})
	if st != 200 {
		t.Fatalf("subscription checkout: %d %v", st, out)
	}
	sid := out["id"].(string)
	if cs := stripeSession(t, a, sid); cs["mode"] != "subscription" || fmt.Sprint(cs["amount_total"]) != "900" {
		t.Fatalf("session: mode %v total %v", cs["mode"], cs["amount_total"])
	}
	s := simServer(a)
	if _, err := s.Apply(sim.Control{Action: "pay_session", Target: sid, Value: "dev@example.com"}); err != nil {
		t.Fatal(err)
	}
	var order map[string]any
	waitFor(t, "the subscription order", func() bool {
		if rs := rows(t, a, id, "orders", "stripe_session_id=eq."+sid); len(rs) == 1 && rs[0]["stripe_subscription_id"] != nil {
			order = rs[0]
			return true
		}
		return false
	})
	if _, err := s.Apply(sim.Control{Action: "cancel_subscription", Target: fmt.Sprint(order["stripe_subscription_id"])}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the order to be canceled", func() bool {
		rs := rows(t, a, id, "orders", "stripe_session_id=eq."+sid)
		return len(rs) == 1 && rs[0]["status"] == "canceled"
	})
	for _, d := range rows(t, a, id, "downloads", "order_id=eq."+fmt.Sprint(order["id"])) {
		if d["revoked"] != true {
			t.Fatalf("download still valid after cancellation: %v", d)
		}
	}
}

// Booking is ready to sell: services with deposits, a held slot, payment,
// confirmation — and the end-to-end check proves it.
func TestPracticeBookingReadyToSell(t *testing.T) {
	a := newPracticeApp(t)
	id := buildPractice(t, a, "booking", map[string]any{"domain": "cutsalon.com", "support_email": "hello@cutsalon.com", "products": []any{
		map[string]any{"name": "Haircut", "price": 10.0, "minutes": 45},
		map[string]any{"name": "Colour", "price": 25.0, "minutes": 90},
	}})
	rep := requireOperational(t, a, id)
	var journey *core.CheckResult
	for i := range rep.Results {
		if rep.Results[i].Level == core.LevelEndToEnd {
			journey = &rep.Results[i]
		}
	}
	if journey == nil || !strings.Contains(journey.Summary, "book") {
		t.Fatalf("expected the booking journey to run, got %+v", journey)
	}
	api := workerURL(t, a, id)
	when := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)
	book := map[string]any{"service": "haircut", "starts_at": when, "email": "ana@example.com", "name": "Ana", "deposit_cents": 1}
	st, out := post(t, api+"/book", book)
	if st != 200 {
		t.Fatalf("book: %d %v", st, out)
	}
	if st, _ := post(t, api+"/book", book); st != 409 {
		t.Fatalf("double booking the same slot: status %d, want 409", st)
	}
	if st, _ := post(t, api+"/book", map[string]any{"service": "haircut", "starts_at": time.Now().Add(time.Minute).Format(time.RFC3339), "email": "a@example.com"}); st != 400 {
		t.Fatalf("booking in the past accepted: %d", st)
	}
	sid := out["session_id"].(string)
	if cs := stripeSession(t, a, sid); fmt.Sprint(cs["amount_total"]) != "1000" {
		t.Fatalf("deposit %v, want 1000", cs["amount_total"])
	}
	if _, err := simServer(a).Apply(sim.Control{Action: "pay_session", Target: sid, Value: "ana@example.com"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the booking to be confirmed", func() bool {
		rs := rows(t, a, id, "bookings", "id=eq."+fmt.Sprint(out["id"]))
		return len(rs) == 1 && rs[0]["status"] == "confirmed" && rs[0]["paid"] == true && fmt.Sprint(rs[0]["minutes"]) == "45"
	})
}

// Restaurant is ready to sell: a cart at menu prices, sold-out items refused,
// payment moves the order to the kitchen feed.
func TestPracticeRestaurantReadyToSell(t *testing.T) {
	a := newPracticeApp(t)
	id := buildPractice(t, a, "restaurant", map[string]any{"domain": "pizzeria.com", "products": []any{
		map[string]any{"name": "Margherita", "price": 12.0},
		map[string]any{"name": "Tiramisu", "price": 6.5},
	}})
	requireOperational(t, a, id)
	api := workerURL(t, a, id)
	st, out := post(t, api+"/order", map[string]any{"email": "sam@example.com", "items": []any{
		map[string]any{"key": "margherita", "quantity": 2, "unit_amount": 1}, map[string]any{"key": "tiramisu", "quantity": 1},
	}})
	if st != 200 {
		t.Fatalf("order: %d %v", st, out)
	}
	row := rows(t, a, id, "orders", "id=eq."+fmt.Sprint(out["id"]))
	if len(row) != 1 || fmt.Sprint(row[0]["total_cents"]) != "3050" || row[0]["status"] != "awaiting_payment" {
		t.Fatalf("order row: %v", row)
	}
	sid := out["session_id"].(string)
	if _, err := simServer(a).Apply(sim.Control{Action: "pay_session", Target: sid, Value: "sam@example.com"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the order to reach the kitchen", func() bool {
		rs := rows(t, a, id, "orders", "id=eq."+fmt.Sprint(out["id"]))
		return len(rs) == 1 && rs[0]["status"] == "received" && rs[0]["paid"] == true
	})
	if st, _ := post(t, api+"/order", map[string]any{"email": "sam@example.com", "items": []any{map[string]any{"key": "margherita", "quantity": 21}}}); st != 400 {
		t.Fatalf("21 pizzas accepted: %d", st)
	}
}
