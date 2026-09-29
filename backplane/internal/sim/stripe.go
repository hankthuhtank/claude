package sim

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type stEndpoint struct {
	ID, URL, Secret, Status, APIVersion, Description string
	Events                                           []string
	Metadata                                         map[string]any
	Created                                          int64
	Livemode                                         bool
}

type stEvent struct {
	ID       string
	Type     string
	Created  int64
	Livemode bool
	Object   map[string]any
	Pending  map[string]bool // endpoint id -> still pending
}

type stripeState struct {
	products  map[string]map[string]any
	prices    map[string]map[string]any
	endpoints map[string]*stEndpoint
	sessions  map[string]map[string]any
	links     map[string]map[string]any
	portals   map[string]map[string]any
	events    []*stEvent
	idem      map[string][]byte
}

func newStripe() *stripeState {
	return &stripeState{products: map[string]map[string]any{}, prices: map[string]map[string]any{}, endpoints: map[string]*stEndpoint{},
		sessions: map[string]map[string]any{}, links: map[string]map[string]any{}, portals: map[string]map[string]any{}, idem: map[string][]byte{}}
}

// parseForm turns Stripe's bracket notation into nested values.
func parseForm(vals url.Values) map[string]any {
	root := map[string]any{}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := []string{}
		base := k
		if i := strings.Index(k, "["); i >= 0 {
			base = k[:i]
			for _, p := range strings.Split(strings.TrimSuffix(k[i+1:], "]"), "][") {
				parts = append(parts, p)
			}
		}
		path := append([]string{base}, parts...)
		for _, v := range vals[k] {
			setPath(root, path, v)
		}
	}
	return normalizeArrays(root).(map[string]any)
}

func setPath(m map[string]any, path []string, v string) {
	cur := m
	for i, p := range path {
		last := i == len(path)-1
		if last {
			if p == "" { // a[] append
				return
			}
			cur[p] = v
			return
		}
		next := path[i+1]
		if next == "" && i+1 == len(path)-1 {
			arr, _ := cur[p].([]any)
			cur[p] = append(arr, v)
			return
		}
		child, ok := cur[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			cur[p] = child
		}
		cur = child
	}
}

// normalizeArrays converts maps with keys "0","1"... into slices.
func normalizeArrays(v any) any {
	switch t := v.(type) {
	case map[string]any:
		allNum := len(t) > 0
		for k := range t {
			if _, err := strconv.Atoi(k); err != nil {
				allNum = false
			}
			t[k] = normalizeArrays(t[k])
		}
		if allNum {
			out := make([]any, len(t))
			for k, x := range t {
				i, _ := strconv.Atoi(k)
				if i < len(out) {
					out[i] = x
				}
			}
			return out
		}
		return t
	case []any:
		for i := range t {
			t[i] = normalizeArrays(t[i])
		}
		return t
	}
	return v
}

func stErr(w http.ResponseWriter, status int, typ, code, msg, param string) {
	e := map[string]any{"type": typ, "message": msg}
	if code != "" {
		e["code"] = code
	}
	if param != "" {
		e["param"] = param
	}
	writeJSON(w, status, map[string]any{"error": e})
}

func (s *Server) stripeKey(r *http.Request) (string, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok, s.auth(r, "stripe")
}

func (s *Server) stripe(w http.ResponseWriter, r *http.Request, rest string) {
	key, ok := s.stripeKey(r)
	if !ok {
		stErr(w, 401, "invalid_request_error", "", "Invalid API Key provided: "+maskKey(key), "")
		return
	}
	live := strings.Contains(key, "_live_")
	body, _ := io.ReadAll(r.Body)
	vals, _ := url.ParseQuery(string(body))
	if r.Method == "GET" || r.Method == "DELETE" {
		vals = r.URL.Query()
	}
	params := parseForm(vals)
	idem := r.Header.Get("Idempotency-Key")
	s.mu.Lock()
	if idem != "" {
		if cached, ok := s.st.idem[key+"|"+idem]; ok {
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(200)
			w.Write(cached)
			return
		}
	}
	rec := &recorder{header: http.Header{}}
	deliver := s.stripeRoute(rec, r.Method, segs(rest), params, live)
	if idem != "" && rec.status < 300 {
		s.st.idem[key+"|"+idem] = rec.body.Bytes()
	}
	s.mu.Unlock()
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	w.Write(rec.body.Bytes())
	for _, ev := range deliver {
		go s.deliverStripe(ev)
	}
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(s int)           { r.status = s }

func maskKey(k string) string {
	if len(k) < 8 {
		return "****"
	}
	return k[:8] + "****" + k[len(k)-4:]
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (s *Server) stripeRoute(w http.ResponseWriter, method string, p []string, params map[string]any, live bool) []*stEvent {
	st := s.st
	now := s.now().Unix()
	meta := func(v any) map[string]any {
		if m, ok := v.(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	switch {
	case eq(p, "v1", "account"):
		writeJSON(w, 200, map[string]any{"id": "acct_practice", "object": "account", "email": "owner@example.com", "country": "US", "charges_enabled": true, "details_submitted": true,
			"business_profile": map[string]any{"name": "Practice Business"}, "settings": map[string]any{"dashboard": map[string]any{"display_name": "Practice Business"}}})
	case eq(p, "v1", "products") && method == "POST":
		id := newID("prod_", 7)
		prod := map[string]any{"id": id, "object": "product", "name": str(params["name"]), "description": params["description"], "active": true, "metadata": meta(params["metadata"]), "livemode": live, "created": now}
		st.products[id] = prod
		writeJSON(w, 200, prod)
	case eq(p, "v1", "products") && method == "GET":
		var data []any
		for _, id := range sortedKeys(st.products) {
			data = append(data, st.products[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case len(p) == 3 && p[1] == "products":
		prod := st.products[p[2]]
		if prod == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such product: '"+p[2]+"'", "id")
			return nil
		}
		if method == "POST" {
			for k, v := range params {
				if k == "active" {
					prod[k] = v == "true"
					continue
				}
				prod[k] = v
			}
		}
		writeJSON(w, 200, prod)
	case eq(p, "v1", "prices") && method == "POST":
		prodID := str(params["product"])
		if st.products[prodID] == nil {
			stErr(w, 400, "invalid_request_error", "resource_missing", "No such product: '"+prodID+"'", "product")
			return nil
		}
		amt, _ := strconv.ParseInt(str(params["unit_amount"]), 10, 64)
		id := newID("price_", 12)
		price := map[string]any{"id": id, "object": "price", "product": prodID, "unit_amount": amt, "currency": str(params["currency"]), "active": true,
			"nickname": params["nickname"], "metadata": meta(params["metadata"]), "livemode": live, "type": "one_time", "recurring": nil}
		if rec, ok := params["recurring"].(map[string]any); ok {
			price["type"] = "recurring"
			price["recurring"] = map[string]any{"interval": str(rec["interval"])}
		}
		st.prices[id] = price
		writeJSON(w, 200, price)
	case eq(p, "v1", "prices") && method == "GET":
		var data []any
		for _, id := range sortedKeys(st.prices) {
			data = append(data, st.prices[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case len(p) == 3 && p[1] == "prices":
		price := st.prices[p[2]]
		if price == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such price: '"+p[2]+"'", "price")
			return nil
		}
		if method == "POST" {
			if v, ok := params["active"]; ok {
				price["active"] = v == "true"
			}
		}
		writeJSON(w, 200, price)
	case eq(p, "v1", "webhook_endpoints") && method == "POST":
		ep := &stEndpoint{ID: newID("we_", 12), URL: str(params["url"]), Secret: "whsec_" + newID("", 16), Status: "enabled", APIVersion: str(params["api_version"]),
			Description: str(params["description"]), Metadata: meta(params["metadata"]), Created: now, Livemode: live}
		for _, e := range toStrings(params["enabled_events"]) {
			ep.Events = append(ep.Events, e)
		}
		if ep.URL == "" || len(ep.Events) == 0 {
			stErr(w, 400, "invalid_request_error", "parameter_missing", "Missing required param: url or enabled_events.", "url")
			return nil
		}
		st.endpoints[ep.ID] = ep
		out := s.endpointJSON(ep)
		out["secret"] = ep.Secret
		writeJSON(w, 200, out)
	case eq(p, "v1", "webhook_endpoints") && method == "GET":
		var data []any
		for _, id := range sortedKeys(st.endpoints) {
			data = append(data, s.endpointJSON(st.endpoints[id]))
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case len(p) == 3 && p[1] == "webhook_endpoints":
		ep := st.endpoints[p[2]]
		if ep == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such webhook endpoint: '"+p[2]+"'", "id")
			return nil
		}
		switch method {
		case "POST":
			if u := str(params["url"]); u != "" {
				ep.URL = u
			}
			if evs := toStrings(params["enabled_events"]); len(evs) > 0 {
				ep.Events = evs
			}
			if d := str(params["disabled"]); d != "" {
				if d == "true" {
					ep.Status = "disabled"
				} else {
					ep.Status = "enabled"
				}
			}
		case "DELETE":
			delete(st.endpoints, ep.ID)
			for _, ev := range st.events {
				delete(ev.Pending, ep.ID) // Stripe stops delivering to a deleted endpoint
			}
			writeJSON(w, 200, map[string]any{"id": ep.ID, "object": "webhook_endpoint", "deleted": true})
			return nil
		}
		writeJSON(w, 200, s.endpointJSON(ep))
	case eq(p, "v1", "checkout", "sessions") && method == "POST":
		items, _ := params["line_items"].([]any)
		var total int64
		currency := "usd"
		for _, it := range items {
			m, _ := it.(map[string]any)
			price := st.prices[str(m["price"])]
			if price == nil {
				stErr(w, 400, "invalid_request_error", "resource_missing", "No such price: '"+str(m["price"])+"'", "line_items[0][price]")
				return nil
			}
			if active, _ := price["active"].(bool); !active {
				stErr(w, 400, "invalid_request_error", "", "The price specified is inactive. This field only accepts active prices.", "line_items[0][price]")
				return nil
			}
			q, _ := strconv.ParseInt(str(m["quantity"]), 10, 64)
			if q == 0 {
				q = 1
			}
			total += price["unit_amount"].(int64) * q
			currency = str(price["currency"])
		}
		mode := "test"
		if live {
			mode = "live"
		}
		id := "cs_" + mode + "_" + newID("", 20)
		cs := map[string]any{"id": id, "object": "checkout.session", "mode": str(params["mode"]), "status": "open", "payment_status": "unpaid", "amount_total": total, "currency": currency,
			"url": "https://checkout.stripe.com/c/pay/" + id, "metadata": meta(params["metadata"]), "livemode": live, "success_url": params["success_url"], "expires_at": now + 86400}
		st.sessions[id] = cs
		writeJSON(w, 200, cs)
	case eq(p, "v1", "checkout", "sessions") && method == "GET":
		var data []any
		for _, id := range sortedKeys(st.sessions) {
			data = append(data, st.sessions[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case len(p) == 4 && p[1] == "checkout" && p[2] == "sessions" && method == "GET":
		cs := st.sessions[p[3]]
		if cs == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such checkout.session: '"+p[3]+"'", "session")
			return nil
		}
		writeJSON(w, 200, cs)
	case len(p) == 5 && p[1] == "checkout" && p[4] == "expire" && method == "POST":
		cs := st.sessions[p[3]]
		if cs == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such checkout.session: '"+p[3]+"'", "session")
			return nil
		}
		if cs["status"] != "open" {
			stErr(w, 400, "invalid_request_error", "", "Only Checkout Sessions with a status of open can be expired.", "")
			return nil
		}
		cs["status"] = "expired"
		ev := s.newEvent("checkout.session.expired", cs, live)
		writeJSON(w, 200, cs)
		return []*stEvent{ev}
	case eq(p, "v1", "events") && method == "GET":
		typ := str(params["type"])
		types := toStrings(params["types"])
		var gte int64
		if c, ok := params["created"].(map[string]any); ok {
			gte, _ = strconv.ParseInt(str(c["gte"]), 10, 64)
		}
		onlyFailed := str(params["delivery_success"]) == "false"
		var data []any
		for i := len(st.events) - 1; i >= 0; i-- {
			ev := st.events[i]
			if typ != "" && ev.Type != typ {
				continue
			}
			if len(types) > 0 && !containsStr(types, ev.Type) {
				continue
			}
			if ev.Created < gte {
				continue
			}
			pending := 0
			for _, v := range ev.Pending {
				if v {
					pending++
				}
			}
			if onlyFailed && pending == 0 {
				continue
			}
			data = append(data, map[string]any{"id": ev.ID, "object": "event", "type": ev.Type, "created": ev.Created, "livemode": ev.Livemode, "pending_webhooks": pending, "data": map[string]any{"object": ev.Object}})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case eq(p, "v1", "payment_links") && method == "POST":
		id := newID("plink_", 12)
		link := map[string]any{"id": id, "object": "payment_link", "active": true, "url": "https://buy.stripe.com/test_" + newID("", 10), "livemode": live}
		st.links[id] = link
		writeJSON(w, 200, link)
	case len(p) == 3 && p[1] == "payment_links":
		link := st.links[p[2]]
		if link == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such payment link", "id")
			return nil
		}
		if v, ok := params["active"]; ok {
			link["active"] = v == "true"
		}
		writeJSON(w, 200, link)
	case eq(p, "v1", "billing_portal", "configurations") && method == "POST":
		id := newID("bpc_", 12)
		cfg := map[string]any{"id": id, "object": "billing_portal.configuration", "active": true}
		st.portals[id] = cfg
		writeJSON(w, 200, cfg)
	case len(p) == 4 && p[1] == "billing_portal" && p[2] == "configurations":
		cfg := st.portals[p[3]]
		if cfg == nil {
			stErr(w, 404, "invalid_request_error", "resource_missing", "No such configuration", "id")
			return nil
		}
		if v, ok := params["active"]; ok {
			cfg["active"] = v == "true"
		}
		writeJSON(w, 200, cfg)
	case eq(p, "v1", "billing_portal", "sessions") && method == "POST":
		writeJSON(w, 200, map[string]any{"id": newID("bps_", 12), "object": "billing_portal.session", "url": "https://billing.stripe.com/p/session/test_" + newID("", 10)})
	case eq(p, "v1", "subscriptions"):
		writeJSON(w, 200, map[string]any{"object": "list", "data": []any{}, "has_more": false})
	case eq(p, "v1", "balance"):
		writeJSON(w, 200, map[string]any{"object": "balance", "available": []any{}, "pending": []any{}})
	default:
		stErr(w, 404, "invalid_request_error", "", "Unrecognized request URL ("+method+": /"+strings.Join(p, "/")+").", "")
	}
	return nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, str(x))
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

func orAny(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

func (s *Server) endpointJSON(ep *stEndpoint) map[string]any {
	return map[string]any{"id": ep.ID, "object": "webhook_endpoint", "url": ep.URL, "status": ep.Status, "enabled_events": ep.Events, "api_version": ep.APIVersion,
		"metadata": ep.Metadata, "livemode": ep.Livemode, "created": ep.Created, "description": ep.Description}
}

// newEvent records an event and marks it pending for subscribed endpoints.
func (s *Server) newEvent(typ string, obj map[string]any, live bool) *stEvent {
	cp := map[string]any{}
	for k, v := range obj {
		cp[k] = v
	}
	ev := &stEvent{ID: newID("evt_", 12), Type: typ, Created: s.now().Unix(), Livemode: live, Object: cp, Pending: map[string]bool{}}
	for _, ep := range s.st.endpoints {
		if ep.Status == "enabled" && ep.Livemode == live && (containsStr(ep.Events, typ) || containsStr(ep.Events, "*")) {
			ev.Pending[ep.ID] = true
		}
	}
	s.st.events = append(s.st.events, ev)
	return ev
}

// deliverStripe posts a signed event to each subscribed endpoint and, like
// Stripe, retries failed deliveries with growing delays.
func (s *Server) deliverStripe(ev *stEvent) {
	for attempt, wait := range []time.Duration{1, 10, 30, 80} {
		time.Sleep(s.DeliverDelay * wait)
		if s.deliverStripeOnce(ev) || attempt == 3 {
			return
		}
	}
}

func (s *Server) deliverStripeOnce(ev *stEvent) bool {
	s.mu.Lock()
	type target struct{ id, url, secret string }
	var targets []target
	for id, pending := range ev.Pending {
		if ep := s.st.endpoints[id]; ep != nil && pending && ep.Status != "disabled" {
			targets = append(targets, target{id, ep.URL, ep.Secret})
		}
	}
	payload, _ := json.Marshal(map[string]any{"id": ev.ID, "object": "event", "type": ev.Type, "created": ev.Created, "livemode": ev.Livemode, "api_version": "2026-08-26.dahlia",
		"pending_webhooks": len(targets), "data": map[string]any{"object": ev.Object}})
	s.mu.Unlock()
	for _, t := range targets {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(t.secret))
		mac.Write([]byte(ts + "."))
		mac.Write(payload)
		req, err := http.NewRequest("POST", t.url, bytes.NewReader(payload))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Stripe-Signature", "t="+ts+",v1="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := s.client.Do(req)
		ok := err == nil && resp.StatusCode < 300
		if resp != nil {
			resp.Body.Close()
		}
		if ok {
			s.mu.Lock()
			ev.Pending[t.id] = false
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pending := range ev.Pending {
		if pending {
			return false
		}
	}
	return true
}

// ---- fault helpers ----

func (s *Server) stripeDisableWebhook(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range sortedKeys(s.st.endpoints) {
		if target != "" && id != target {
			continue
		}
		s.st.endpoints[id].Status = "disabled"
		return "Disabled Stripe webhook " + id, nil
	}
	return "", fmt.Errorf("no webhook")
}

func (s *Server) stripeMoveWebhook(target, to string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if to == "" {
		to = "https://old-server.example.com/stripe"
	}
	for _, id := range sortedKeys(s.st.endpoints) {
		if target != "" && id != target {
			continue
		}
		s.st.endpoints[id].URL = to
		return "Pointed Stripe webhook " + id + " at " + to, nil
	}
	return "", fmt.Errorf("no webhook")
}

func (s *Server) stripeArchive(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range sortedKeys(s.st.products) {
		if target != "" && id != target {
			continue
		}
		s.st.products[id]["active"] = false
		return "Archived Stripe product " + id, nil
	}
	return "", fmt.Errorf("no product")
}

// stripeKeyValidLocked is used by the simulated Worker to check its key.
func (s *Server) stripeKeyValidLocked(key string) bool { return s.tokens[key] == "stripe" }

// stripeCreateSessionLocked is used by the simulated Worker's /checkout.
func (s *Server) stripeCreateSessionLocked(priceID string, meta map[string]any, live bool) (map[string]any, error) {
	price := s.st.prices[priceID]
	if price == nil {
		return nil, fmt.Errorf("No such price: '%s'", priceID)
	}
	if active, _ := price["active"].(bool); !active {
		return nil, fmt.Errorf("The price specified is inactive")
	}
	mode := "test"
	if live {
		mode = "live"
	}
	id := "cs_" + mode + "_" + newID("", 20)
	cs := map[string]any{"id": id, "object": "checkout.session", "mode": "payment", "status": "open", "payment_status": "unpaid", "amount_total": price["unit_amount"], "currency": price["currency"],
		"url": "https://checkout.stripe.com/c/pay/" + id, "metadata": meta, "livemode": live}
	s.st.sessions[id] = cs
	return cs, nil
}
