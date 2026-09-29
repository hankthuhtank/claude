package stripe

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Handlers returns every Stripe resource handler.
func Handlers() []providers.Handler {
	return []providers.Handler{productH{}, priceH{}, webhookH{}, linkH{}, portalH{}}
}

func sconn(s *providers.Session) (*providers.Conn, error) { return s.Conn("stripe") }

// idem builds a deterministic idempotency key: retrying a create after a
// crash returns Stripe's original response instead of a duplicate object.
func idem(s *providers.Session, spec *core.ResourceSpec, extra string) string {
	p := "noproj"
	if s.Project != nil {
		p = s.Project.ID
	}
	return "bp-" + p + "-" + s.Env + "-" + spec.Key + "-" + core.HashProps(spec.Props)[:10] + extra
}

func meta(s *providers.Session, spec *core.ResourceSpec) map[string]string {
	m := map[string]string{"backplane_resource": spec.Key, "backplane_env": s.Env}
	if s.Project != nil {
		m["backplane_project"] = s.Project.ID
	}
	for k, v := range providers.Map(spec.Props, "metadata") {
		m[k] = v
	}
	return m
}

// ================= Product =================

type productH struct{}

func (productH) Kind() string { return KindProduct }

type product struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Active      bool              `json:"active"`
	Metadata    map[string]string `json:"metadata"`
}

func (productH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"name": providers.Str(spec.Props, "name"), "metadata": meta(s, spec)}
	if d := providers.Str(spec.Props, "description"); d != "" {
		params["description"] = d
	}
	var p product
	if st != nil && st.ID != "" {
		params["active"] = true
		if err := Call(ctx, c, "POST", "/v1/products/"+st.ID, params, "", &p); err == nil {
			next := providers.Touch(spec, st, p.ID, p.Name)
			next.SetOutput("id", p.ID)
			return &providers.ApplyResult{State: next}, nil
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	s.Say("Creating Stripe product “%s”", params["name"])
	if err := Call(ctx, c, "POST", "/v1/products", params, idem(s, spec, ""), &p); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, p.ID, p.Name)
	next.SetOutput("id", p.ID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (productH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var p product
	if err := Call(ctx, c, "GET", "/v1/products/"+st.ID, nil, "", &p); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: p.ID, Name: p.Name, Props: map[string]any{"name": p.Name, "active": p.Active}}, nil
}

func (productH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	var items []core.DriftItem
	if !providers.Bool(obs.Props, "active") {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "stripe", Field: "product active", Expected: "active", Actual: "archived",
			Severity: core.HealthFail, Breaks: []string{"Customers cannot buy " + providers.Str(spec.Props, "name")}, Recommended: "Unarchive the product", FixID: "reapply:" + spec.Key})
	}
	return providers.DriftIf(items, spec, "product name", providers.Str(spec.Props, "name"), providers.Str(obs.Props, "name"), core.HealthWarn, []string{"Receipts and checkout show a different name"}, "Restore the name")
}

func (productH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	// Archive rather than delete: products with prices cannot be deleted and
	// archived products keep order history intact.
	err = Call(ctx, c, "POST", "/v1/products/"+st.ID, map[string]any{"active": false}, "", nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Price =================

type priceH struct{}

func (priceH) Kind() string { return KindPrice }

type price struct {
	ID         string `json:"id"`
	Product    string `json:"product"`
	Active     bool   `json:"active"`
	UnitAmount int64  `json:"unit_amount"`
	Currency   string `json:"currency"`
	Nickname   string `json:"nickname"`
	LookupKey  string `json:"lookup_key"`
	Recurring  *struct {
		Interval string `json:"interval"`
	} `json:"recurring"`
}

func (priceH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	amount := providers.Int(spec.Props, "unit_amount")
	currency := strings.ToLower(providers.Str(spec.Props, "currency"))
	if currency == "" {
		currency = "usd"
	}
	interval := providers.Str(spec.Props, "interval")
	productID := providers.Str(spec.Props, "product")
	if amount <= 0 || productID == "" {
		return nil, fmt.Errorf("price needs an amount and a product")
	}
	// Prices are immutable. Keep the existing one when amount, currency and
	// interval still match; otherwise create a new price and deactivate the old.
	if st != nil && st.ID != "" {
		var p price
		if err := Call(ctx, c, "GET", "/v1/prices/"+st.ID, nil, "", &p); err == nil {
			same := p.UnitAmount == amount && p.Currency == currency && p.Product == productID && (interval == "") == (p.Recurring == nil)
			if same && p.Recurring != nil {
				same = p.Recurring.Interval == interval
			}
			if same {
				if !p.Active {
					_ = Call(ctx, c, "POST", "/v1/prices/"+p.ID, map[string]any{"active": true}, "", nil)
				}
				next := providers.Touch(spec, st, p.ID, Money(amount, currency))
				next.SetOutput("id", p.ID)
				return &providers.ApplyResult{State: next}, nil
			}
			s.Say("Price changed: retiring %s and creating a new one", p.ID)
			_ = Call(ctx, c, "POST", "/v1/prices/"+p.ID, map[string]any{"active": false}, "", nil)
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	params := map[string]any{"product": productID, "unit_amount": amount, "currency": currency, "metadata": meta(s, spec)}
	if n := providers.Str(spec.Props, "nickname"); n != "" {
		params["nickname"] = n
	}
	if interval != "" {
		params["recurring"] = map[string]any{"interval": interval}
	}
	if lk := providers.Str(spec.Props, "lookup_key"); lk != "" {
		params["lookup_key"] = lk
		params["transfer_lookup_key"] = true
	}
	if tb := providers.Str(spec.Props, "tax_behavior"); tb != "" {
		params["tax_behavior"] = tb
	}
	label := Money(amount, currency)
	if interval != "" {
		label += " / " + interval
	}
	s.Say("Creating Stripe price %s", label)
	var p price
	if err := Call(ctx, c, "POST", "/v1/prices", params, idem(s, spec, "-"+productID), &p); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, p.ID, label)
	next.SetOutput("id", p.ID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (priceH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var p price
	if err := Call(ctx, c, "GET", "/v1/prices/"+st.ID, nil, "", &p); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: p.ID, Name: Money(p.UnitAmount, p.Currency), Props: map[string]any{"active": p.Active, "unit_amount": p.UnitAmount, "currency": p.Currency}}, nil
}

func (priceH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	if !providers.Bool(obs.Props, "active") {
		return []core.DriftItem{{Resource: spec.Key, Kind: spec.Kind, Provider: "stripe", Field: "price active", Expected: "active", Actual: "inactive",
			Severity: core.HealthFail, Breaks: []string{"Checkout for this product"}, Recommended: "Reactivate the price", FixID: "reapply:" + spec.Key}}
	}
	return nil
}

func (priceH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "POST", "/v1/prices/"+st.ID, map[string]any{"active": false}, "", nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Webhook endpoint =================

type webhookH struct{}

func (webhookH) Kind() string { return KindWebhook }

type endpoint struct {
	ID            string            `json:"id"`
	URL           string            `json:"url"`
	Status        string            `json:"status"`
	EnabledEvents []string          `json:"enabled_events"`
	Secret        string            `json:"secret"`
	APIVersion    string            `json:"api_version"`
	Metadata      map[string]string `json:"metadata"`
	Livemode      bool              `json:"livemode"`
}

func (webhookH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	target := providers.Str(spec.Props, "url")
	events := providers.List(spec.Props, "events")
	sort.Strings(events)
	if target == "" || len(events) == 0 {
		return nil, fmt.Errorf("webhook endpoint needs a url and events")
	}
	haveSecret := false
	if s.Secret != nil {
		if v, err := s.Secret(spec.Key, "secret"); err == nil && v != "" {
			haveSecret = true
		}
	}
	if st != nil && st.ID != "" {
		var ep endpoint
		err := Call(ctx, c, "GET", "/v1/webhook_endpoints/"+st.ID, nil, "", &ep)
		switch {
		case err == nil && providers.Bool(spec.Props, "__rotate"):
			s.Say("Replacing the Stripe webhook endpoint to issue a new signing secret")
			_ = Call(ctx, c, "DELETE", "/v1/webhook_endpoints/"+st.ID, nil, "", nil)
		case err == nil && haveSecret:
			s.Say("Updating Stripe webhook → %s", target)
			params := map[string]any{"url": target, "enabled_events": events, "disabled": false, "metadata": meta(s, spec)}
			if err := Call(ctx, c, "POST", "/v1/webhook_endpoints/"+st.ID, params, "", &ep); err != nil {
				return nil, err
			}
			next := providers.Touch(spec, st, ep.ID, target)
			next.SetOutput("id", ep.ID)
			next.SetOutput("url", ep.URL)
			next.SetOutput("mode", mode(ep.Livemode))
			return &providers.ApplyResult{State: next}, nil
		case err == nil && !haveSecret:
			// We lost the signing secret (Stripe only reveals it on
			// creation). The only safe fix is a fresh endpoint.
			s.Say("Signing secret missing from the vault — replacing the webhook endpoint")
			_ = Call(ctx, c, "DELETE", "/v1/webhook_endpoints/"+st.ID, nil, "", nil)
		case !httpx.IsNotFound(err):
			return nil, err
		}
	}
	params := map[string]any{"url": target, "enabled_events": events, "api_version": APIVersion,
		"description": providers.Str(spec.Props, "description"), "metadata": meta(s, spec)}
	s.Say("Creating Stripe webhook → %s (%d events)", target, len(events))
	var ep endpoint
	extra := "-" + core.HashProps(target)[:6]
	if st != nil && st.ID != "" {
		extra += "-r" + core.HashBytes([]byte(st.ID))[:6]
	}
	if err := Call(ctx, c, "POST", "/v1/webhook_endpoints", params, idem(s, spec, extra), &ep); err != nil {
		return nil, err
	}
	if ep.Secret == "" {
		return nil, &core.Problem{Title: "Stripe did not return a signing secret", Provider: "stripe", Code: "server",
			Summary: "The webhook endpoint was created but Stripe did not include its signing secret. Delete the endpoint in the Stripe dashboard and rebuild."}
	}
	next := providers.Touch(spec, st, ep.ID, target)
	next.SetOutput("id", ep.ID)
	next.SetOutput("url", ep.URL)
	next.SetOutput("mode", mode(ep.Livemode))
	return &providers.ApplyResult{State: next, Secrets: map[string]string{"secret": ep.Secret}, Created: true}, nil
}

func mode(live bool) string {
	if live {
		return "live"
	}
	return "test"
}

func (webhookH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var ep endpoint
	if err := Call(ctx, c, "GET", "/v1/webhook_endpoints/"+st.ID, nil, "", &ep); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	sort.Strings(ep.EnabledEvents)
	return &providers.Observation{Exists: true, ID: ep.ID, Name: ep.URL,
		Props:   map[string]any{"url": ep.URL, "status": ep.Status, "events": ep.EnabledEvents, "api_version": ep.APIVersion},
		Summary: ep.Status + " · " + fmt.Sprint(len(ep.EnabledEvents)) + " events"}, nil
}

func (webhookH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	var items []core.DriftItem
	purpose := providers.Str(spec.Props, "purpose")
	if purpose == "" {
		purpose = "Payment notifications"
	}
	items = providers.DriftIf(items, spec, "webhook URL", providers.Str(spec.Props, "url"), providers.Str(obs.Props, "url"), core.HealthFail,
		[]string{purpose + " go to the wrong address"}, "Point the webhook back at the Worker")
	if st := providers.Str(obs.Props, "status"); st != "enabled" {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "stripe", Field: "webhook status", Expected: "enabled", Actual: st,
			Severity: core.HealthFail, Breaks: []string{purpose + " are not being sent (Stripe disables endpoints that keep failing)"}, Recommended: "Re-enable the webhook", FixID: "reapply:" + spec.Key})
	}
	have := map[string]bool{}
	for _, e := range providers.List(obs.Props, "events") {
		have[e] = true
	}
	for _, e := range providers.List(spec.Props, "events") {
		if !have[e] && !have["*"] {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "stripe", Field: "webhook event " + e, Expected: "subscribed", Actual: "Missing",
				Severity: core.HealthFail, Breaks: []string{"The backend never hears about " + e}, Recommended: "Subscribe the event again", FixID: "reapply:" + spec.Key})
		}
	}
	return items
}

func (webhookH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/v1/webhook_endpoints/"+st.ID, nil, "", nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Payment link =================

type linkH struct{}

func (linkH) Kind() string { return KindLink }

func (linkH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	priceID := providers.Str(spec.Props, "price")
	var link struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	if st != nil && st.ID != "" {
		if err := Call(ctx, c, "GET", "/v1/payment_links/"+st.ID, nil, "", &link); err == nil && st.Output("price") == priceID {
			if !link.Active {
				_ = Call(ctx, c, "POST", "/v1/payment_links/"+st.ID, map[string]any{"active": true}, "", nil)
			}
			next := providers.Touch(spec, st, link.ID, link.URL)
			return &providers.ApplyResult{State: next}, nil
		} else if err == nil {
			_ = Call(ctx, c, "POST", "/v1/payment_links/"+st.ID, map[string]any{"active": false}, "", nil)
		}
	}
	params := map[string]any{"line_items": []map[string]any{{"price": priceID, "quantity": 1}}, "metadata": meta(s, spec)}
	if r := providers.Str(spec.Props, "redirect_url"); r != "" {
		params["after_completion"] = map[string]any{"type": "redirect", "redirect": map[string]any{"url": r}}
	}
	s.Say("Creating Stripe payment link")
	if err := Call(ctx, c, "POST", "/v1/payment_links", params, idem(s, spec, "-"+priceID), &link); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, link.ID, link.URL)
	next.SetOutput("url", link.URL)
	next.SetOutput("price", priceID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (linkH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var link struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	if err := Call(ctx, c, "GET", "/v1/payment_links/"+st.ID, nil, "", &link); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: link.ID, Name: link.URL, Props: map[string]any{"active": link.Active}}, nil
}

func (linkH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs != nil && obs.Exists && !providers.Bool(obs.Props, "active") {
		return []core.DriftItem{{Resource: spec.Key, Kind: spec.Kind, Provider: "stripe", Field: "payment link", Expected: "active", Actual: "deactivated",
			Severity: core.HealthFail, Breaks: []string{"The Buy button link shows an error"}, Recommended: "Reactivate the link", FixID: "reapply:" + spec.Key}}
	}
	return nil
}

func (linkH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "POST", "/v1/payment_links/"+st.ID, map[string]any{"active": false}, "", nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Customer portal =================

type portalH struct{}

func (portalH) Kind() string { return KindPortal }

func (portalH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"business_profile": map[string]any{"headline": providers.Str(spec.Props, "headline")},
		"features": map[string]any{
			"invoice_history":       map[string]any{"enabled": true},
			"payment_method_update": map[string]any{"enabled": true},
			"customer_update":       map[string]any{"enabled": true, "allowed_updates": []string{"email", "address"}},
			"subscription_cancel":   map[string]any{"enabled": true, "mode": "at_period_end"},
		},
		"default_return_url": providers.Str(spec.Props, "return_url"),
		"metadata":           meta(s, spec),
	}
	var cfg struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if st != nil && st.ID != "" {
		if err := Call(ctx, c, "POST", "/v1/billing_portal/configurations/"+st.ID, params, "", &cfg); err == nil {
			return &providers.ApplyResult{State: providers.Touch(spec, st, cfg.ID, "Customer portal")}, nil
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	s.Say("Configuring the Stripe customer portal")
	if err := Call(ctx, c, "POST", "/v1/billing_portal/configurations", params, idem(s, spec, ""), &cfg); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, cfg.ID, "Customer portal")
	next.SetOutput("id", cfg.ID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (portalH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var cfg struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if err := Call(ctx, c, "GET", "/v1/billing_portal/configurations/"+st.ID, nil, "", &cfg); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: cfg.ID, Props: map[string]any{"active": cfg.Active}}, nil
}

func (portalH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (portalH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	return Call(ctx, c, "POST", "/v1/billing_portal/configurations/"+st.ID, map[string]any{"active": false}, "", nil)
}
