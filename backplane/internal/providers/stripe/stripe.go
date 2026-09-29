// Package stripe implements the Stripe adapter: products, prices, webhook
// endpoints, payment links and billing-portal configurations, plus the probes
// the verification engine uses (checkout session create/expire, event lookup,
// webhook signing for synthetic end-to-end events).
//
// Every request pins Stripe-Version so response shapes cannot change under a
// working backend; the version is shown and tracked on the Versions screen.
// Reference: OpenAPI at github.com/stripe/openapi (spec3.json).
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// APIBase is the Stripe API.
const APIBase = "https://api.stripe.com"

// APIVersion is pinned for every request and every webhook endpoint Backplane
// creates. Stripe's latest GA version on 2026-09-29 is 2026-08-26.dahlia.
const APIVersion = "2026-08-26.dahlia"

// Resource kinds.
const (
	KindProduct = "stripe.product"
	KindPrice   = "stripe.price"
	KindWebhook = "stripe.webhook_endpoint"
	KindLink    = "stripe.payment_link"
	KindPortal  = "stripe.portal_configuration"
)

// Provider is the Stripe adapter.
type Provider struct{}

// Info describes Stripe.
func (Provider) Info() providers.Info {
	return providers.Info{
		ID: "stripe", Name: "Stripe", Category: "Payments", Phase: 1, Maturity: providers.MaturityBuild,
		Tagline:      "Payments, subscriptions, invoices and payouts.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks},
		Fields: []providers.Field{
			{Key: "token", Label: "Secret or restricted key", Secret: true, Required: true, Placeholder: "sk_test_… or rk_test_…", Pattern: `^(sk|rk)_(test|live)_[A-Za-z0-9]{10,}$`,
				Help: "Start with a test-mode key (sk_test_ / rk_test_). Add a live key as a separate connection for production."},
			{Key: "worker_key", Label: "Worker key (recommended)", Secret: true, Placeholder: "rk_…", Pattern: `^rk_(test|live)_[A-Za-z0-9]{10,}$`,
				Help: "Optional restricted key for your backend with only Checkout Sessions: Write and Prices/Products: Read. If blank, the main key is used and the Security panel shows a warning."},
		},
		Guide: []providers.GuideStep{
			{Title: "Start in test mode", Body: "Turn on Test mode in the Stripe dashboard. Nothing charges real cards and every Backplane test is allowed.", Link: "https://dashboard.stripe.com/test/apikeys", Label: "Open test API keys"},
			{Title: "Create a restricted key for Backplane", Body: "Developers → API keys → Create restricted key. Give Write to Products, Prices, Webhook Endpoints, Checkout Sessions, Payment Links and Customer portal, and Read to Events and Balance. A standard secret key also works."},
			{Title: "Optional: a smaller key for your Worker", Body: "Create a second restricted key with only Checkout Sessions: Write and Products/Prices: Read. Backplane gives this one to your backend so a leak there can't touch payouts or refunds."},
			{Title: "Paste the keys here", Body: "Backplane checks the key, finds your account and whether it can take live payments. Keys are encrypted with Windows DPAPI on this PC."},
			{Title: "Going live later", Body: "Add your live key as a second Stripe connection and attach it only to the production environment. Backplane blocks a live key in development and a test key in production."},
		},
		TokenURL: "https://dashboard.stripe.com/test/apikeys", DocsURL: "https://docs.stripe.com/keys", StatusURL: "https://status.stripe.com",
		OAuth: false, OAuthNote: "Stripe Apps / Connect OAuth require a registered platform. Restricted API keys give the same least-privilege result for your own account.",
		Scopes:     []string{"Products: Write", "Prices: Write", "Webhook Endpoints: Write", "Checkout Sessions: Write", "Payment Links: Write", "Customer portal: Write", "Events: Read"},
		APIVersion: APIVersion,
		Kinds: []providers.KindInfo{
			{Kind: KindProduct, Label: "Product", Capability: core.CapPayments, Destructive: "Products are archived, never deleted, so past orders keep their history."},
			{Kind: KindPrice, Label: "Price", Capability: core.CapPayments, Destructive: "Prices are deactivated, never deleted."},
			{Kind: KindWebhook, Label: "Webhook endpoint", Capability: core.CapWebhooks, Destructive: "Removing the webhook stops order processing immediately."},
			{Kind: KindLink, Label: "Payment link", Capability: core.CapPayments},
			{Kind: KindPortal, Label: "Customer portal", Capability: core.CapPayments},
		},
		Discovers: []string{"Products and prices", "Webhook endpoints (and where they point)", "Active subscriptions", "Recent failed webhook deliveries"},
	}
}

// Connect builds the client.
func (Provider) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("stripe", conn.Base("stripe", APIBase))
	cl.Auth = func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Stripe-Version", APIVersion)
	}
	cl.ParseError = parseError
	cl.Classify = classify
	cl.Limiter = httpx.NewLimiter(20, 20)
	cl.Log = opts.Log
	conn.Client = cl
	return conn, nil
}

// ModeOf returns "test" or "live" from a key prefix.
func ModeOf(key string) string {
	if strings.Contains(key, "_live_") {
		return "live"
	}
	return "test"
}

func parseError(status int, body []byte) (string, string) {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	msg := e.Error.Message
	if e.Error.Param != "" && !strings.Contains(msg, e.Error.Param) {
		msg += " (field: " + e.Error.Param + ")"
	}
	code := e.Error.Code
	if code == "" {
		code = e.Error.Type
	}
	return code, msg
}

func classify(status int, code, msg string) string {
	switch code {
	case "resource_missing":
		return httpx.KindNotFound
	case "resource_already_exists":
		return httpx.KindConflict
	case "api_key_expired":
		return httpx.KindAuth
	case "idempotency_key_in_use", "lock_timeout":
		return httpx.KindRateLimit // retry later
	}
	if status == 403 && strings.Contains(strings.ToLower(msg), "does not have the required permissions") {
		return httpx.KindPermission
	}
	return ""
}

// Form encodes nested values the way Stripe expects (a[b]=c, a[]=d).
func Form(v map[string]any) url.Values {
	out := url.Values{}
	var walk func(prefix string, x any)
	walk = func(prefix string, x any) {
		switch t := x.(type) {
		case nil:
		case map[string]any:
			for _, k := range providers.SortedKeys(t) {
				walk(prefix+"["+k+"]", t[k])
			}
		case map[string]string:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out.Add(prefix+"["+k+"]", t[k])
			}
		case []string:
			for _, s := range t {
				out.Add(prefix+"[]", s)
			}
		case []map[string]any:
			for i, m := range t {
				walk(prefix+"["+strconv.Itoa(i)+"]", m)
			}
		case []any:
			for i, m := range t {
				if mm, ok := m.(map[string]any); ok {
					walk(prefix+"["+strconv.Itoa(i)+"]", mm)
				} else {
					out.Add(prefix+"[]", fmt.Sprint(m))
				}
			}
		case bool:
			out.Add(prefix, strconv.FormatBool(t))
		case int:
			out.Add(prefix, strconv.Itoa(t))
		case int64:
			out.Add(prefix, strconv.FormatInt(t, 10))
		case float64:
			out.Add(prefix, strconv.FormatFloat(t, 'f', -1, 64))
		default:
			out.Add(prefix, fmt.Sprint(t))
		}
	}
	for _, k := range providers.SortedKeys(v) {
		walk(k, v[k])
	}
	return out
}

// Call performs a Stripe request and decodes JSON into out.
func Call(ctx context.Context, c *providers.Conn, method, path string, params map[string]any, idemKey string, out any) error {
	rq := httpx.Request{Method: method, Path: path, IdempotencyKey: idemKey}
	if params != nil {
		if method == "GET" || method == "DELETE" {
			rq.Query = Form(params)
		} else {
			rq.Form = Form(params)
		}
	}
	if method == "POST" && params == nil {
		rq.Form = url.Values{}
	}
	resp, err := c.Client.Do(ctx, rq)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}

type accountInfo struct {
	ID               string `json:"id"`
	Email            string `json:"email"`
	Country          string `json:"country"`
	ChargesEnabled   bool   `json:"charges_enabled"`
	DetailsSubmitted bool   `json:"details_submitted"`
	BusinessProfile  struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"business_profile"`
	Settings struct {
		Dashboard struct {
			DisplayName string `json:"display_name"`
		} `json:"dashboard"`
	} `json:"settings"`
}

// Verify implements Level 1.
func (Provider) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	res := &providers.VerifyResult{Details: map[string]string{}, Settings: map[string]string{}}
	mode := ModeOf(c.Secret)
	res.Mode = mode
	res.Settings["mode"] = mode
	res.Details["api_version"] = APIVersion
	restricted := strings.HasPrefix(c.Secret, "rk_")
	var acct accountInfo
	err := Call(ctx, c, "GET", "/v1/account", nil, "", &acct)
	if err != nil {
		e, ok := httpx.AsError(err)
		if !ok || (e.Kind != httpx.KindPermission) {
			res.Health = core.HealthFail
			res.Problem = providers.Translate("stripe", "read the account", err)
			res.Summary = res.Problem.Summary
			return res, nil
		}
		// Restricted keys without Account read still work for everything else.
	} else {
		res.AccountID = acct.ID
		res.AccountName = firstNonEmpty(acct.Settings.Dashboard.DisplayName, acct.BusinessProfile.Name, acct.Email, acct.ID)
		res.Settings["account_id"] = acct.ID
		res.Details["country"] = acct.Country
		if mode == "live" && !acct.ChargesEnabled {
			res.Warnings = append(res.Warnings, "This Stripe account cannot accept live payments yet. Finish account activation in the Stripe dashboard.")
		}
	}
	probes := []struct{ name, path string }{
		{"Products", "/v1/products"}, {"Prices", "/v1/prices"}, {"Webhook Endpoints", "/v1/webhook_endpoints"},
		{"Checkout Sessions", "/v1/checkout/sessions"}, {"Events", "/v1/events"},
	}
	for _, p := range probes {
		if err := Call(ctx, c, "GET", p.path, map[string]any{"limit": 1}, "", nil); err != nil {
			if e, ok := httpx.AsError(err); ok && (e.Kind == httpx.KindPermission || e.Kind == httpx.KindAuth) {
				res.Missing = append(res.Missing, p.name)
				continue
			}
			res.Health = core.HealthFail
			res.Problem = providers.Translate("stripe", "read "+strings.ToLower(p.name), err)
			res.Summary = res.Problem.Summary
			return res, nil
		}
		res.Scopes = append(res.Scopes, p.name)
	}
	if restricted {
		res.Details["key_type"] = "restricted"
	} else {
		res.Details["key_type"] = "standard secret"
		if mode == "live" {
			res.Warnings = append(res.Warnings, "A full live secret key can move money. A restricted key with only the permissions in the guide is safer.")
		}
	}
	if wk := c.Connection.Setting("worker_key_hint"); wk == "" && c.Connection.Setting("has_worker_key") != "true" {
		res.Details["worker_key"] = "not set — the main key will be given to your backend"
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	name := firstNonEmpty(res.AccountName, "your Stripe account")
	switch {
	case len(res.Missing) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Connected to " + name + " (" + mode + " mode), but the key cannot read: " + strings.Join(res.Missing, ", ") + "."
	case len(res.Warnings) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Connected to " + name + " in " + mode + " mode, with a note."
	default:
		res.Health = core.HealthOK
		res.Summary = "Connected to " + name + " in " + mode + " mode."
	}
	return res, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---- probes used by the verification engine ----

// CheckoutSession is the subset of fields Backplane reads.
type CheckoutSession struct {
	ID            string            `json:"id"`
	URL           string            `json:"url"`
	Status        string            `json:"status"`
	PaymentStatus string            `json:"payment_status"`
	Metadata      map[string]string `json:"metadata"`
	Livemode      bool              `json:"livemode"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
}

// CreateCheckoutSession creates a hosted Checkout Session. Creating and then
// expiring a session never charges anything, in test or live mode.
func CreateCheckoutSession(ctx context.Context, c *providers.Conn, price, successURL string, meta map[string]string, idem string) (*CheckoutSession, error) {
	return CreateCheckoutSessionMode(ctx, c, price, "payment", successURL, meta, idem)
}

// CreateCheckoutSessionMode is CreateCheckoutSession for a given mode
// ("payment" for one-time prices, "subscription" for recurring ones).
func CreateCheckoutSessionMode(ctx context.Context, c *providers.Conn, price, mode, successURL string, meta map[string]string, idem string) (*CheckoutSession, error) {
	var cs CheckoutSession
	if mode == "" {
		mode = "payment"
	}
	params := map[string]any{
		"mode":        mode,
		"line_items":  []map[string]any{{"price": price, "quantity": 1}},
		"success_url": successURL,
		"metadata":    meta,
	}
	err := Call(ctx, c, "POST", "/v1/checkout/sessions", params, idem, &cs)
	return &cs, err
}

// GetCheckoutSession reads a session (amount, status, metadata).
func GetCheckoutSession(ctx context.Context, c *providers.Conn, id string) (*CheckoutSession, error) {
	var cs CheckoutSession
	err := Call(ctx, c, "GET", "/v1/checkout/sessions/"+id, nil, "", &cs)
	return &cs, err
}

// ExpireCheckoutSession expires an open session; Stripe then sends
// checkout.session.expired to subscribed webhook endpoints.
func ExpireCheckoutSession(ctx context.Context, c *providers.Conn, id string) error {
	return Call(ctx, c, "POST", "/v1/checkout/sessions/"+id+"/expire", nil, "expire-"+id, nil)
}

// Event is a Stripe event.
type Event struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Created         int64  `json:"created"`
	PendingWebhooks int    `json:"pending_webhooks"`
	Livemode        bool   `json:"livemode"`
	Data            struct {
		Object map[string]any `json:"object"`
	} `json:"data"`
}

// FindEvent finds the most recent event of a type about an object.
func FindEvent(ctx context.Context, c *providers.Conn, typ, objectID string, since time.Time) (*Event, error) {
	var list struct {
		Data []Event `json:"data"`
	}
	params := map[string]any{"type": typ, "limit": 20, "created": map[string]any{"gte": since.Add(-time.Minute).Unix()}}
	if err := Call(ctx, c, "GET", "/v1/events", params, "", &list); err != nil {
		return nil, err
	}
	for i := range list.Data {
		if fmt.Sprint(list.Data[i].Data.Object["id"]) == objectID {
			return &list.Data[i], nil
		}
	}
	return nil, nil
}

// FailedDeliveries returns recent events whose webhook delivery has not
// succeeded (still pending or failed every attempt).
func FailedDeliveries(ctx context.Context, c *providers.Conn, since time.Time, types []string) ([]Event, error) {
	var list struct {
		Data []Event `json:"data"`
	}
	params := map[string]any{"delivery_success": false, "limit": 50, "created": map[string]any{"gte": since.Unix()}}
	if len(types) > 0 {
		params["types"] = types
	}
	if err := Call(ctx, c, "GET", "/v1/events", params, "", &list); err != nil {
		return nil, err
	}
	out := list.Data[:0]
	for _, ev := range list.Data {
		if IsProbeEvent(ev) {
			continue // Backplane's own delivery probes are not customer events
		}
		out = append(out, ev)
	}
	return out, nil
}

// IsProbeEvent reports whether an event is about one of Backplane's own
// no-charge probe checkout sessions.
func IsProbeEvent(ev Event) bool {
	meta, _ := ev.Data.Object["metadata"].(map[string]any)
	v, _ := meta["backplane_probe"].(string)
	return v == "1"
}

// SignPayload produces a Stripe-Signature header value for a payload, exactly
// as Stripe does (HMAC-SHA256 over "timestamp.payload"). Backplane uses it to
// send signed synthetic events to your own endpoint during end-to-end tests.
func SignPayload(secret string, payload []byte, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a Stripe-Signature header (used by the simulator and tests).
func VerifySignature(secret string, payload []byte, header string, tolerance time.Duration, now time.Time) bool {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := now.Sub(time.Unix(n, 0)); d > tolerance || d < -tolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return true
		}
	}
	return false
}

// Discover lists products, prices, webhooks and subscription counts.
func (Provider) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var out []providers.Discovered
	var products struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Active bool   `json:"active"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/v1/products", map[string]any{"limit": 100, "active": true}, "", &products); err != nil {
		return nil, err
	}
	for _, p := range products.Data {
		out = append(out, providers.Discovered{Provider: "stripe", Kind: KindProduct, ID: p.ID, Name: p.Name, Group: p.Name})
	}
	var prices struct {
		Data []struct {
			ID         string `json:"id"`
			Product    string `json:"product"`
			UnitAmount int64  `json:"unit_amount"`
			Currency   string `json:"currency"`
			Recurring  *struct {
				Interval string `json:"interval"`
			} `json:"recurring"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/v1/prices", map[string]any{"limit": 100, "active": true}, "", &prices); err == nil {
		names := map[string]string{}
		for _, p := range products.Data {
			names[p.ID] = p.Name
		}
		for _, p := range prices.Data {
			label := Money(p.UnitAmount, p.Currency)
			if p.Recurring != nil {
				label += " / " + p.Recurring.Interval
			}
			out = append(out, providers.Discovered{Provider: "stripe", Kind: KindPrice, ID: p.ID, Name: label, Detail: "for " + names[p.Product],
				Links: []providers.DiscoveredLink{{ToKind: KindProduct, ToName: names[p.Product], Label: "price of"}}, Group: names[p.Product]})
		}
	}
	var hooks struct {
		Data []struct {
			ID            string   `json:"id"`
			URL           string   `json:"url"`
			Status        string   `json:"status"`
			EnabledEvents []string `json:"enabled_events"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/v1/webhook_endpoints", map[string]any{"limit": 100}, "", &hooks); err == nil {
		for _, h := range hooks.Data {
			u, _ := url.Parse(h.URL)
			host := ""
			if u != nil {
				host = u.Host
			}
			d := providers.Discovered{Provider: "stripe", Kind: KindWebhook, ID: h.ID, Name: host, Detail: fmt.Sprintf("%s · %d events · %s", h.URL, len(h.EnabledEvents), h.Status),
				Props: map[string]string{"url": h.URL, "status": h.Status}}
			if strings.HasSuffix(host, ".workers.dev") {
				script := strings.SplitN(host, ".", 2)[0]
				d.Links = append(d.Links, providers.DiscoveredLink{ToKind: "cloudflare.worker", ToName: script, Label: "sends events to"})
			}
			out = append(out, d)
		}
	}
	var subs struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := Call(ctx, c, "GET", "/v1/subscriptions", map[string]any{"limit": 100, "status": "active"}, "", &subs); err == nil && len(subs.Data) > 0 {
		n := strconv.Itoa(len(subs.Data))
		if subs.HasMore {
			n += "+"
		}
		out = append(out, providers.Discovered{Provider: "stripe", Kind: "stripe.subscriptions", ID: "subscriptions", Name: n + " active subscriptions"})
	}
	return out, nil
}

// Money formats minor units.
func Money(amount int64, currency string) string {
	cur := strings.ToUpper(currency)
	zeroDecimal := map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true}
	sym := map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "CAD": "CA$", "AUD": "A$", "JPY": "¥"}[cur]
	if zeroDecimal[cur] {
		if sym != "" {
			return sym + strconv.FormatInt(amount, 10)
		}
		return strconv.FormatInt(amount, 10) + " " + cur
	}
	s := fmt.Sprintf("%d.%02d", amount/100, amount%100)
	if strings.HasSuffix(s, ".00") {
		s = strings.TrimSuffix(s, ".00")
	}
	if sym != "" {
		return sym + s
	}
	return s + " " + cur
}
