package sim

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// wenv is a simulated Worker's environment (vars, secrets and bindings).
type wenv struct {
	name    string
	vars    map[string]string
	r2      map[string]string // binding -> bucket
	kv      map[string]string // binding -> namespace id
	baseURL string
}

func (e *wenv) get(k string) string { return e.vars[k] }

// worker runs the simulated generated Worker. It mirrors the JavaScript
// Worker Backplane generates (same routes, same responses, same checks) and
// talks to the simulated providers over HTTP exactly like the real one.
func (s *Server) worker(w http.ResponseWriter, r *http.Request, rest string) {
	p := segs(rest)
	if len(p) == 0 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	sc := s.cf.scripts[p[0]]
	if sc == nil || !sc.WorkersDev {
		s.mu.Unlock()
		w.WriteHeader(404)
		io.WriteString(w, "There is nothing here yet.")
		return
	}
	env := &wenv{name: sc.Name, vars: map[string]string{}, r2: map[string]string{}, kv: map[string]string{}, baseURL: s.Base + "/__workers/" + sc.Name}
	for _, b := range sc.Bindings {
		switch b["type"] {
		case "plain_text":
			env.vars[str(b["name"])] = str(b["text"])
		case "r2_bucket":
			env.r2[str(b["name"])] = str(b["bucket_name"])
		case "kv_namespace":
			env.kv[str(b["name"])] = str(b["namespace_id"])
		}
	}
	for k, v := range sc.Secrets {
		env.vars[k] = v
	}
	s.mu.Unlock()
	path := "/" + strings.Join(p[1:], "/")
	if env.get("TEMPLATE") != "" {
		s.dataWorker(w, r, env, path)
		return
	}
	s.commerceWorker(w, r, env, path)
}

func wjson(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func safeEq(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

func hmacHexS(secret, data string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil))
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ---- helpers that call the simulated providers over HTTP ----

func (s *Server) wcall(method, u string, headers map[string]string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func (s *Server) sbw(env *wenv, method, path string, body any, prefer string) ([]map[string]any, error) {
	key := env.get("SUPABASE_SECRET_KEY")
	h := map[string]string{"apikey": key, "Content-Type": "application/json"}
	if strings.HasPrefix(key, "eyJ") {
		h["Authorization"] = "Bearer " + key
	}
	if prefer != "" {
		h["Prefer"] = prefer
	}
	var bs []byte
	if body != nil {
		bs, _ = json.Marshal(body)
	}
	status, out, err := s.wcall(method, env.get("SUPABASE_URL")+"/rest/v1/"+path, h, bs)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("Supabase %s %s → %d: %s", method, strings.SplitN(path, "?", 2)[0], status, string(out))
	}
	var rows []map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &rows)
	}
	return rows, nil
}

func (s *Server) stripeW(env *wenv, method, path string, form url.Values) (map[string]any, error) {
	h := map[string]string{"Authorization": "Bearer " + env.get("STRIPE_SECRET_KEY"), "Stripe-Version": "2026-08-26.dahlia", "Content-Type": "application/x-www-form-urlencoded"}
	var body []byte
	if form != nil {
		body = []byte(form.Encode())
	}
	status, out, err := s.wcall(method, s.Base+"/stripe"+path, h, body)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	_ = json.Unmarshal(out, &data)
	if status >= 300 {
		msg := "error"
		if e, ok := data["error"].(map[string]any); ok {
			msg = str(e["message"])
		}
		return nil, fmt.Errorf("Stripe %s → %d: %s", path, status, msg)
	}
	return data, nil
}

func (s *Server) resendW(env *wenv, method, path string, body any) (int, map[string]any) {
	var bs []byte
	if body != nil {
		bs, _ = json.Marshal(body)
	}
	status, out, err := s.wcall(method, s.Base+"/resend"+path, map[string]string{"Authorization": "Bearer " + env.get("RESEND_API_KEY"), "Content-Type": "application/json"}, bs)
	if err != nil {
		return 599, map[string]any{"message": err.Error()}
	}
	var data map[string]any
	_ = json.Unmarshal(out, &data)
	return status, data
}

func verifyStripeSig(payload []byte, header, secret string) bool {
	if header == "" || secret == "" {
		return false
	}
	var t string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			t = kv[1]
		} else if kv[0] == "v1" {
			sigs = append(sigs, kv[1])
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)) > 5*time.Minute || time.Until(time.Unix(n, 0)) > 5*time.Minute {
		return false
	}
	want := hmacHexS(secret, t+"."+string(payload))
	for _, s := range sigs {
		if safeEq(s, want) {
			return true
		}
	}
	return false
}

func bearerTok(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return h[7:]
	}
	return ""
}

// ---- commerce Worker ----

var commerceRequired = []string{"SUPABASE_URL", "SUPABASE_SECRET_KEY", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "RESEND_API_KEY", "FROM_EMAIL", "DOWNLOAD_SIGNING_SECRET", "BACKPLANE_PROBE_TOKEN", "PRODUCT_NAME"}

// ---- catalog (mirrors the generated Workers' catalog handling) ----

type catItem struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Minutes     int               `json:"minutes,omitempty"`
	Billing     string            `json:"billing"`
	Prices      map[string]string `json:"prices"`
	Amounts     map[string]int64  `json:"amounts"`
}

type catalogT struct {
	Currency string    `json:"currency"`
	Items    []catItem `json:"items"`
}

func (c *catalogT) item(key string) *catItem {
	for i := range c.Items {
		if c.Items[i].Key == key {
			return &c.Items[i]
		}
	}
	return nil
}

func wCatalog(env *wenv) *catalogT {
	var c catalogT
	if err := json.Unmarshal([]byte(env.get("CATALOG")), &c); err == nil {
		return &c
	}
	if id := env.get("PRICE_ID"); id != "" {
		return &catalogT{Currency: "usd", Items: []catItem{{Key: "main", Name: env.get("PRODUCT_NAME"), Billing: "once", Prices: map[string]string{"once": id}}}}
	}
	return &catalogT{Currency: "usd"}
}

type orderLine struct {
	Key      string
	Name     string
	Quantity int
	Price    string
	Amount   int64
}

type pricedOrder struct {
	Interval string
	Lines    []orderLine
	Summary  string
	Keys     string
}

// readOrder mirrors the Worker: JSON body, form post or query string.
func readOrder(r *http.Request) ([]map[string]any, string) {
	src := map[string]any{}
	for k, v := range r.URL.Query() {
		src[k] = v[0]
	}
	if r.Method == "POST" {
		ct := r.Header.Get("Content-Type")
		switch {
		case strings.Contains(ct, "application/json"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k, v := range body {
				src[k] = v
			}
		case strings.Contains(ct, "form"):
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				src[k] = v[0]
			}
		}
	}
	var lines []map[string]any
	if arr, ok := src["items"].([]any); ok {
		for _, x := range arr {
			m, _ := x.(map[string]any)
			lines = append(lines, m)
		}
	} else if src["item"] != nil {
		lines = []map[string]any{{"key": src["item"], "quantity": src["quantity"]}}
	}
	return lines, str(src["interval"])
}

// priceLines mirrors the Worker's validation: known keys only, whole
// quantities within MAX_QUANTITY, one interval per order. Amounts are
// never read from the request.
func priceLines(env *wenv, req []map[string]any, interval string) (*pricedOrder, error) {
	cat := wCatalog(env)
	if len(cat.Items) == 0 {
		return nil, fmt.Errorf("Nothing is for sale yet.")
	}
	maxQ, _ := strconv.Atoi(env.get("MAX_QUANTITY"))
	if maxQ < 1 {
		maxQ = 1
	}
	cart := env.get("CART") == "true"
	if len(req) == 0 {
		req = []map[string]any{{"key": cat.Items[0].Key, "quantity": 1.0}}
	}
	limit := 1
	if cart {
		limit = 20
	}
	if len(req) > limit {
		if cart {
			return nil, fmt.Errorf("Up to 20 different items per order.")
		}
		return nil, fmt.Errorf("Choose one item.")
	}
	var order []string
	qty := map[string]int{}
	for _, l := range req {
		key := str(l["key"])
		if key == "" {
			key = str(l["item"])
		}
		if cat.item(key) == nil {
			if len(key) > 40 {
				key = key[:40]
			}
			return nil, fmt.Errorf("Unknown item %q.", key)
		}
		q := 1.0
		switch v := l["quantity"].(type) {
		case float64:
			q = v
		case string:
			if v != "" {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					q = -1
				} else {
					q = f
				}
			}
		case nil:
		default:
			q = -1
		}
		if _, seen := qty[key]; !seen {
			order = append(order, key)
		}
		if q != float64(int(q)) || q < 1 || qty[key]+int(q) > maxQ {
			if maxQ == 1 {
				return nil, fmt.Errorf("Quantity must be 1.")
			}
			return nil, fmt.Errorf("Quantity must be between 1 and %d.", maxQ)
		}
		qty[key] += int(q)
	}
	iv := interval
	if iv == "" {
		iv = cat.item(order[0]).Billing
	}
	if iv == "" {
		for k := range cat.item(order[0]).Prices {
			iv = k
		}
	}
	out := &pricedOrder{Interval: iv}
	var names, keys []string
	for _, k := range order {
		it := cat.item(k)
		id := it.Prices[iv]
		if id == "" {
			return nil, fmt.Errorf("%q isn't sold that way.", it.Name)
		}
		out.Lines = append(out.Lines, orderLine{Key: k, Name: it.Name, Quantity: qty[k], Price: id, Amount: it.Amounts[iv]})
		n := it.Name
		if qty[k] > 1 {
			n += fmt.Sprintf(" × %d", qty[k])
		}
		names = append(names, n)
		keys = append(keys, fmt.Sprintf("%s:%d", k, qty[k]))
	}
	out.Summary, out.Keys = strings.Join(names, ", "), strings.Join(keys, ",")
	return out, nil
}

func publicCatalog(env *wenv) map[string]any {
	cat := wCatalog(env)
	var items []any
	for _, it := range cat.Items {
		prices := map[string]any{}
		for iv, a := range it.Amounts {
			prices[iv] = map[string]any{"amount": a}
		}
		items = append(items, map[string]any{"key": it.Key, "name": it.Name, "billing": it.Billing, "prices": prices, "minutes": it.Minutes})
	}
	maxQ, _ := strconv.Atoi(env.get("MAX_QUANTITY"))
	return map[string]any{"currency": cat.Currency, "items": orAny(items), "cart": env.get("CART") == "true", "max_quantity": max(1, maxQ)}
}

// linesFromMeta reads "key:qty,…" back into order lines.
func linesFromMeta(env *wenv, meta map[string]any) []any {
	cat := wCatalog(env)
	var out []any
	for _, part := range strings.Split(str(meta["item_keys"]), ",") {
		if part == "" {
			continue
		}
		k, q, _ := strings.Cut(part, ":")
		n, _ := strconv.Atoi(q)
		if n == 0 {
			n = 1
		}
		name := k
		if it := cat.item(k); it != nil {
			name = it.Name
		}
		out = append(out, map[string]any{"key": k, "name": name, "quantity": n})
	}
	return orAny(out)
}

// createCheckout creates a Stripe Checkout Session for priced lines.
func (s *Server) createCheckout(env *wenv, po *pricedOrder, success string, meta map[string]string, extra url.Values) (map[string]any, error) {
	mode := "payment"
	if po.Interval != "once" {
		mode = "subscription"
	}
	form := url.Values{"mode": {mode}, "success_url": {success}}
	for i, l := range po.Lines {
		form.Set(fmt.Sprintf("line_items[%d][price]", i), l.Price)
		form.Set(fmt.Sprintf("line_items[%d][quantity]", i), strconv.Itoa(l.Quantity))
	}
	for k, v := range meta {
		form.Set("metadata["+k+"]", v)
	}
	for k, v := range extra {
		form[k] = v
	}
	return s.stripeW(env, "POST", "/v1/checkout/sessions", form)
}

// checkPrices mirrors the Worker's health check of its catalog prices.
func (s *Server) checkPrices(env *wenv) (string, error) {
	cat := wCatalog(env)
	if len(cat.Items) == 0 {
		return "", fmt.Errorf("nothing for sale: CATALOG has no items")
	}
	n, checked := 0, 0
	for _, it := range cat.Items {
		for iv, id := range it.Prices {
			n++
			if checked >= 8 {
				continue
			}
			checked++
			price, err := s.stripeW(env, "GET", "/v1/prices/"+id, nil)
			if err != nil {
				return "", err
			}
			if price["active"] != true {
				return "", fmt.Errorf("price for %s (%s) is inactive", it.Name, iv)
			}
		}
	}
	return fmt.Sprintf("%d prices for %d items active", n, len(cat.Items)), nil
}

func (s *Server) commerceWorker(w http.ResponseWriter, r *http.Request, env *wenv, path string) {
	switch {
	case path == "/":
		wjson(w, 200, map[string]any{"service": env.get("WORKER_NAME"), "ok": true})
	case path == "/products" && r.Method == "GET":
		wjson(w, 200, publicCatalog(env))
	case path == "/checkout":
		probe := r.Header.Get("X-Backplane-Probe")
		isProbe := probe != "" && safeEq(probe, env.get("BACKPLANE_PROBE_TOKEN"))
		lines, interval := readOrder(r)
		po, err := priceLines(env, lines, interval)
		if err != nil {
			wjson(w, 400, map[string]any{"error": err.Error()})
			return
		}
		meta := map[string]string{"product": po.Summary, "items": po.Summary, "item_keys": po.Keys, "interval": po.Interval}
		if isProbe {
			meta["backplane_probe"] = "1"
		}
		var extra url.Values
		if env.get("FULFILLMENT_MODE") == "shipping" {
			extra = url.Values{"shipping_address_collection[allowed_countries][0]": {"US"}}
		}
		cs, err := s.createCheckout(env, po, env.baseURL+"/thanks?session_id={CHECKOUT_SESSION_ID}", meta, extra)
		if err != nil {
			wjson(w, 500, map[string]any{"error": "internal error"})
			return
		}
		if isProbe || strings.Contains(r.Header.Get("Accept"), "application/json") {
			wjson(w, 200, map[string]any{"id": cs["id"], "url": cs["url"]})
			return
		}
		http.Redirect(w, r, str(cs["url"]), 303)
	case path == "/stripe/webhook" && r.Method == "POST":
		s.commerceWebhook(w, r, env)
	case path == "/download":
		s.commerceDownload(w, r, env)
	case path == "/resend/webhook" && r.Method == "POST":
		payload, _ := io.ReadAll(r.Body)
		id, ts, sig := r.Header.Get("svix-id"), r.Header.Get("svix-timestamp"), r.Header.Get("svix-signature")
		secret := env.get("RESEND_WEBHOOK_SECRET")
		if id == "" || ts == "" || secret == "" {
			wjson(w, 400, map[string]any{"error": "invalid signature"})
			return
		}
		key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		m := hmac.New(sha256.New, key)
		m.Write([]byte(id + "." + ts + "." + string(payload)))
		want := base64.StdEncoding.EncodeToString(m.Sum(nil))
		ok := false
		for _, part := range strings.Split(sig, " ") {
			if kv := strings.SplitN(part, ",", 2); len(kv) == 2 && safeEq(kv[1], want) {
				ok = true
			}
		}
		if !ok {
			wjson(w, 400, map[string]any{"error": "invalid signature"})
			return
		}
		var evt map[string]any
		_ = json.Unmarshal(payload, &evt)
		if d, ok := evt["data"].(map[string]any); ok && d["email_id"] != nil {
			_, _ = s.sbw(env, "PATCH", "email_log?resend_email_id=eq."+str(d["email_id"]), map[string]any{"status": strings.TrimPrefix(str(evt["type"]), "email.")}, "return=minimal")
		}
		wjson(w, 200, map[string]any{"received": true})
	case path == "/license/validate" && r.Method == "POST":
		var b struct{ Key string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		hash := hmacHexS(env.get("DOWNLOAD_SIGNING_SECRET"), "license-hash:"+strings.ToUpper(strings.TrimSpace(b.Key)))
		rows, _ := s.sbw(env, "GET", "licenses?key_hash=eq."+hash+"&select=revoked,product,orders(status)", nil, "")
		valid := len(rows) > 0 && rows[0]["revoked"] != true
		wjson(w, 200, map[string]any{"valid": valid})
	case path == "/links" && r.Method == "POST":
		wjson(w, 200, map[string]any{"sent": true})
	case path == "/__backplane/health":
		s.commerceHealth(w, r, env)
	case strings.HasPrefix(path, "/__backplane/probe/stripe/"):
		if !safeEq(bearerTok(r), env.get("BACKPLANE_PROBE_TOKEN")) || env.get("BACKPLANE_PROBE_TOKEN") == "" {
			wjson(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		id := strings.TrimPrefix(path, "/__backplane/probe/stripe/")
		s.mu.Lock()
		ns := s.cf.kv[env.kv["PROBES"]]
		var v kvVal
		found := false
		if ns != nil {
			v, found = ns.Values["probe:cs:"+id]
		}
		s.mu.Unlock()
		if !found {
			wjson(w, 200, map[string]any{"found": false})
			return
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(v.V), &out)
		out["found"] = true
		wjson(w, 200, out)
	default:
		wjson(w, 404, map[string]any{"error": "not found"})
	}
}

func (s *Server) commerceWebhook(w http.ResponseWriter, r *http.Request, env *wenv) {
	payload, _ := io.ReadAll(r.Body)
	if !verifyStripeSig(payload, r.Header.Get("Stripe-Signature"), env.get("STRIPE_WEBHOOK_SECRET")) {
		wjson(w, 400, map[string]any{"error": "invalid signature"})
		return
	}
	var ev map[string]any
	_ = json.Unmarshal(payload, &ev)
	obj, _ := ev["data"].(map[string]any)["object"].(map[string]any)
	meta, _ := obj["metadata"].(map[string]any)
	isProbe := ev["livemode"] == false && str(meta["backplane_probe"]) == "1"
	typ := str(ev["type"])
	if typ == "checkout.session.expired" {
		if str(meta["backplane_probe"]) == "1" {
			s.mu.Lock()
			if ns := s.cf.kv[env.kv["PROBES"]]; ns != nil {
				bs, _ := json.Marshal(map[string]any{"event_id": ev["id"], "at": time.Now().UTC().Format(time.RFC3339)})
				ns.Values["probe:cs:"+str(obj["id"])] = kvVal{V: string(bs), Exp: time.Now().Add(time.Hour)}
			}
			s.mu.Unlock()
		}
		wjson(w, 200, map[string]any{"received": true, "type": typ})
		return
	}
	ins, err := s.sbw(env, "POST", "webhook_events?on_conflict=event_id", []map[string]any{{"event_id": ev["id"], "type": typ, "livemode": ev["livemode"] == true}}, "resolution=ignore-duplicates,return=representation")
	if err != nil {
		wjson(w, 500, map[string]any{"error": "internal error"})
		return
	}
	if len(ins) == 0 {
		prev, _ := s.sbw(env, "GET", "webhook_events?event_id=eq."+str(ev["id"])+"&select=processed_at", nil, "")
		if len(prev) > 0 && prev[0]["processed_at"] != nil {
			wjson(w, 200, map[string]any{"received": true, "duplicate": true, "type": typ})
			return
		}
	}
	result := map[string]any{"received": true, "type": typ}
	switch typ {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		probe, err := s.commerceFulfil(env, obj, isProbe)
		if err != nil {
			wjson(w, 500, map[string]any{"error": "internal error"})
			return
		}
		if isProbe {
			result["probe"] = probe
		}
	case "charge.refunded":
		pi := str(obj["payment_intent"])
		orders, _ := s.sbw(env, "PATCH", "orders?stripe_payment_intent=eq."+pi, map[string]any{"status": "refunded"}, "return=representation")
		for _, o := range orders {
			_, _ = s.sbw(env, "PATCH", "downloads?order_id=eq."+str(o["id"]), map[string]any{"revoked": true}, "return=minimal")
		}
	case "customer.subscription.deleted":
		orders, _ := s.sbw(env, "PATCH", "orders?stripe_subscription_id=eq."+str(obj["id"])+"&status=eq.paid", map[string]any{"status": "canceled"}, "return=representation")
		for _, o := range orders {
			_, _ = s.sbw(env, "PATCH", "downloads?order_id=eq."+str(o["id"]), map[string]any{"revoked": true}, "return=minimal")
			_, _ = s.sbw(env, "PATCH", "licenses?order_id=eq."+str(o["id"]), map[string]any{"revoked": true}, "return=minimal")
		}
	}
	_, _ = s.sbw(env, "PATCH", "webhook_events?event_id=eq."+str(ev["id"]), map[string]any{"processed_at": time.Now().UTC().Format(time.RFC3339), "outcome": "ok"}, "return=minimal")
	wjson(w, 200, result)
}

func (s *Server) commerceFulfil(env *wenv, cs map[string]any, isProbe bool) (map[string]any, error) {
	if ps := str(cs["payment_status"]); ps != "paid" && ps != "no_payment_required" {
		return nil, nil
	}
	details, _ := cs["customer_details"].(map[string]any)
	email := str(details["email"])
	meta, _ := cs["metadata"].(map[string]any)
	product := str(meta["items"])
	if product == "" {
		product = env.get("PRODUCT_NAME")
	}
	orders, err := s.sbw(env, "POST", "orders?on_conflict=stripe_session_id", []map[string]any{{
		"stripe_session_id": cs["id"], "stripe_payment_intent": cs["payment_intent"], "stripe_subscription_id": cs["subscription"], "email": email, "customer_name": details["name"],
		"product": product, "items": linesFromMeta(env, meta), "amount_total": cs["amount_total"], "currency": cs["currency"], "status": "paid", "is_probe": isProbe}}, "resolution=merge-duplicates,return=representation")
	if err != nil || len(orders) == 0 {
		return nil, fmt.Errorf("order insert failed: %v", err)
	}
	order := orders[0]
	if env.get("FULFILLMENT_MODE") == "shipping" {
		return s.commerceShip(env, cs, order, email, isProbe)
	}
	key := env.get("PRODUCT_FILE_KEY")
	if isProbe && str(meta["backplane_probe_object"]) != "" {
		key = str(meta["backplane_probe_object"])
	}
	ttl, _ := strconv.Atoi(env.get("DOWNLOAD_TTL_HOURS"))
	if ttl == 0 {
		ttl = 72
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Hour).Unix()
	if _, err := s.sbw(env, "POST", "downloads?on_conflict=stripe_session_id,object_key", []map[string]any{{"order_id": order["id"], "stripe_session_id": cs["id"], "object_key": key,
		"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339), "max_downloads": 10}}, "resolution=merge-duplicates,return=minimal"); err != nil {
		return nil, err
	}
	claim, _ := json.Marshal(map[string]any{"s": cs["id"], "k": key, "e": exp})
	body := b64u(claim)
	m := hmac.New(sha256.New, []byte(env.get("DOWNLOAD_SIGNING_SECRET")))
	m.Write([]byte(body))
	link := env.baseURL + "/download?t=" + body + "." + b64u(m.Sum(nil))
	emailID, emailErr := "", ""
	sent, _ := s.sbw(env, "GET", "email_log?stripe_session_id=eq."+str(cs["id"])+"&kind=eq.purchase&select=resend_email_id", nil, "")
	if len(sent) > 0 {
		emailID = str(sent[0]["resend_email_id"])
	} else {
		payload := map[string]any{"from": env.get("FROM_EMAIL"), "to": []string{email}}
		if tid := env.get("RESEND_TEMPLATE_PURCHASE"); tid != "" {
			payload["template"] = map[string]any{"id": tid, "variables": map[string]any{"DOWNLOAD_URL": link}}
		} else {
			payload["subject"], payload["html"] = "Your "+env.get("PRODUCT_NAME")+" download", "<a href=\""+link+"\">Download</a>"
		}
		status, data := s.resendW(env, "POST", "/emails", payload)
		if status >= 300 && env.get("RESEND_TEMPLATE_PURCHASE") != "" && status < 500 && status != 429 {
			delete(payload, "template")
			payload["subject"], payload["html"] = "Your "+env.get("PRODUCT_NAME")+" download", "<a href=\""+link+"\">Download</a>"
			status, data = s.resendW(env, "POST", "/emails", payload)
		}
		if status >= 300 {
			emailErr = fmt.Sprintf("Resend %d: %s", status, str(data["message"]))
		} else {
			emailID = str(data["id"])
			_, _ = s.sbw(env, "POST", "email_log", []map[string]any{{"resend_email_id": emailID, "stripe_session_id": cs["id"], "recipient": email, "kind": "purchase"}}, "return=minimal")
		}
	}
	if emailErr != "" && !isProbe {
		return nil, fmt.Errorf("email failed: %s", emailErr)
	}
	return map[string]any{"order_id": order["id"], "download_url": link, "email_id": emailID, "email_error": emailErr}, nil
}

// commerceShip mirrors the generated Worker's physical-goods path: the
// shipping address is stored with the order and a confirmation is emailed.
func (s *Server) commerceShip(env *wenv, cs, order map[string]any, email string, isProbe bool) (map[string]any, error) {
	var ship map[string]any
	if ci, ok := cs["collected_information"].(map[string]any); ok {
		ship, _ = ci["shipping_details"].(map[string]any)
	}
	if ship == nil {
		ship, _ = cs["shipping_details"].(map[string]any)
	}
	details, _ := cs["customer_details"].(map[string]any)
	patch := map[string]any{"fulfillment_status": "unfulfilled", "phone": details["phone"]}
	if ship != nil {
		patch["shipping_name"], patch["shipping_address"] = ship["name"], ship["address"]
	}
	if _, err := s.sbw(env, "PATCH", "orders?id=eq."+str(order["id"]), patch, "return=minimal"); err != nil {
		return nil, err
	}
	emailID, emailErr := "", ""
	sent, _ := s.sbw(env, "GET", "email_log?stripe_session_id=eq."+str(cs["id"])+"&kind=eq.purchase&select=resend_email_id", nil, "")
	if len(sent) > 0 {
		emailID = str(sent[0]["resend_email_id"])
	} else {
		status, data := s.resendW(env, "POST", "/emails", map[string]any{"from": env.get("FROM_EMAIL"), "to": []string{email},
			"subject": "Order confirmed — " + env.get("PRODUCT_NAME"), "html": "<p>Your order is confirmed. We'll ship it soon.</p>"})
		if status >= 300 {
			emailErr = fmt.Sprintf("Resend %d: %s", status, str(data["message"]))
		} else {
			emailID = str(data["id"])
			_, _ = s.sbw(env, "POST", "email_log", []map[string]any{{"resend_email_id": emailID, "stripe_session_id": cs["id"], "recipient": email, "kind": "purchase"}}, "return=minimal")
		}
	}
	if emailErr != "" && !isProbe {
		return nil, fmt.Errorf("email failed: %s", emailErr)
	}
	return map[string]any{"order_id": order["id"], "email_id": emailID, "email_error": emailErr}, nil
}

func (s *Server) commerceDownload(w http.ResponseWriter, r *http.Request, env *wenv) {
	tok := r.URL.Query().Get("t")
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		wjson(w, 400, map[string]any{"error": "invalid link"})
		return
	}
	m := hmac.New(sha256.New, []byte(env.get("DOWNLOAD_SIGNING_SECRET")))
	m.Write([]byte(parts[0]))
	if !safeEq(parts[1], b64u(m.Sum(nil))) {
		wjson(w, 403, map[string]any{"error": "invalid link"})
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		wjson(w, 400, map[string]any{"error": "invalid link"})
		return
	}
	var claim struct {
		S string `json:"s"`
		K string `json:"k"`
		E int64  `json:"e"`
	}
	_ = json.Unmarshal(raw, &claim)
	if claim.E < time.Now().Unix() {
		wjson(w, 410, map[string]any{"error": "This download link has expired."})
		return
	}
	rows, err := s.sbw(env, "GET", "downloads?stripe_session_id=eq."+url.QueryEscape(claim.S)+"&object_key=eq."+url.QueryEscape(claim.K)+"&select=revoked,download_count,max_downloads,orders(status)", nil, "")
	if err != nil || len(rows) == 0 || rows[0]["revoked"] == true {
		wjson(w, 403, map[string]any{"error": "This download is no longer available."})
		return
	}
	if o, ok := rows[0]["orders"].(map[string]any); !ok || o["status"] != "paid" {
		wjson(w, 403, map[string]any{"error": "This download is no longer available."})
		return
	}
	s.mu.Lock()
	b := s.cf.buckets[env.r2["DOWNLOADS"]]
	var obj []byte
	if b != nil {
		obj = b.Objects[claim.K]
	}
	s.mu.Unlock()
	if obj == nil {
		wjson(w, 404, map[string]any{"error": "File not found."})
		return
	}
	go s.sbw(env, "POST", "rpc/bp_record_download", map[string]any{"p_session": claim.S, "p_key": claim.K}, "")
	w.Header().Set("Content-Disposition", `attachment; filename="`+claim.K[strings.LastIndex(claim.K, "/")+1:]+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(200)
	w.Write(obj)
}

func (s *Server) commerceHealth(w http.ResponseWriter, r *http.Request, env *wenv) {
	if env.get("BACKPLANE_PROBE_TOKEN") == "" || !safeEq(bearerTok(r), env.get("BACKPLANE_PROBE_TOKEN")) {
		wjson(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	var missing []string
	for _, k := range commerceRequired {
		if env.get(k) == "" {
			missing = append(missing, k)
		}
	}
	if env.get("CATALOG") == "" && env.get("PRICE_ID") == "" {
		missing = append(missing, "CATALOG")
	}
	checks := map[string]any{}
	timed := func(fn func() (string, error)) map[string]any {
		t0 := time.Now()
		d, err := fn()
		if err != nil {
			return map[string]any{"ok": false, "ms": time.Since(t0).Milliseconds(), "error": err.Error()}
		}
		return map[string]any{"ok": true, "ms": time.Since(t0).Milliseconds(), "detail": d}
	}
	shipping := env.get("FULFILLMENT_MODE") == "shipping"
	if !shipping {
		checks["r2"] = timed(func() (string, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			bucket, bound := env.r2["DOWNLOADS"]
			if !bound {
				return "", fmt.Errorf("R2 binding DOWNLOADS is not bound")
			}
			b := s.cf.buckets[bucket]
			if b == nil {
				return "", fmt.Errorf("bucket %s does not exist", bucket)
			}
			if k := env.get("PRODUCT_FILE_KEY"); k != "" {
				if _, ok := b.Objects[k]; !ok {
					return "bucket reachable; product file " + k + " NOT uploaded yet", nil
				}
				return "bucket reachable; product file present", nil
			}
			return "bucket reachable", nil
		})
	}
	if r2, ok := checks["r2"].(map[string]any); ok && strings.Contains(fmt.Sprint(r2["detail"]), "NOT uploaded") {
		r2["warn"] = true
	}
	checks["kv"] = timed(func() (string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id, bound := env.kv["PROBES"]
		if !bound {
			return "", fmt.Errorf("KV binding PROBES is not bound")
		}
		if s.cf.kv[id] == nil {
			return "", fmt.Errorf("namespace %s does not exist", id)
		}
		return "namespace reachable", nil
	})
	checks["supabase"] = timed(func() (string, error) {
		_, err := s.sbw(env, "GET", "orders?select=id&limit=1", nil, "")
		if err != nil {
			return "", err
		}
		return "orders table reachable with the server key", nil
	})
	checks["stripe"] = timed(func() (string, error) { return s.checkPrices(env) })
	checks["resend"] = timed(func() (string, error) {
		s.mu.Lock()
		kind := s.resendKeyKindLocked(env.get("RESEND_API_KEY"))
		s.mu.Unlock()
		switch kind {
		case "sending":
			return "send-only key valid", nil
		case "full":
			return "key valid (full access — a send-only key is safer)", nil
		}
		return "", fmt.Errorf("key rejected (401 invalid_api_key)")
	})
	ok := len(missing) == 0
	for _, c := range checks {
		if c.(map[string]any)["ok"] != true {
			ok = false
		}
	}
	w.Header().Set("X-Backplane-Worker", env.get("WORKER_NAME"))
	wjson(w, 200, map[string]any{"ok": ok, "worker": env.get("WORKER_NAME"), "version": env.get("CODE_VERSION"), "checks": checks, "missing": orStrings(missing), "time": time.Now().UTC().Format(time.RFC3339)})
}

func orStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ---- data Worker ----

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (s *Server) dataWorker(w http.ResponseWriter, r *http.Request, env *wenv, path string) {
	tmpl := env.get("TEMPLATE")
	switch {
	case path == "/":
		wjson(w, 200, map[string]any{"service": env.get("WORKER_NAME"), "ok": true})
	case path == "/__backplane/health":
		if env.get("BACKPLANE_PROBE_TOKEN") == "" || !safeEq(bearerTok(r), env.get("BACKPLANE_PROBE_TOKEN")) {
			wjson(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		checks := map[string]any{}
		t0 := time.Now()
		if _, err := s.sbw(env, "GET", "backplane_probe?select=id&limit=1", nil, ""); err != nil {
			checks["supabase"] = map[string]any{"ok": false, "error": err.Error(), "ms": time.Since(t0).Milliseconds()}
		} else {
			checks["supabase"] = map[string]any{"ok": true, "detail": "database reachable with the server key", "ms": time.Since(t0).Milliseconds()}
		}
		s.mu.Lock()
		_, kvOK := s.cf.kv[env.kv["PROBES"]]
		kind := s.resendKeyKindLocked(env.get("RESEND_API_KEY"))
		s.mu.Unlock()
		checks["kv"] = map[string]any{"ok": kvOK, "detail": "namespace reachable"}
		if env.get("RESEND_API_KEY") != "" {
			checks["resend"] = map[string]any{"ok": kind != "invalid", "detail": "send-only key valid", "error": map[bool]string{true: "", false: "key rejected"}[kind != "invalid"]}
		}
		if env.get("STRIPE_SECRET_KEY") != "" {
			if len(wCatalog(env).Items) == 0 {
				_, err := s.stripeW(env, "GET", "/v1/checkout/sessions?limit=1", nil)
				checks["stripe"] = map[string]any{"ok": err == nil, "detail": "key valid (no catalog prices to check)", "error": errText(err)}
			} else {
				d, err := s.checkPrices(env)
				checks["stripe"] = map[string]any{"ok": err == nil, "detail": d, "error": errText(err)}
			}
		}
		ok := true
		for _, c := range checks {
			if c.(map[string]any)["ok"] != true {
				ok = false
			}
		}
		wjson(w, 200, map[string]any{"ok": ok, "worker": env.get("WORKER_NAME"), "checks": checks, "missing": []string{}})
	case strings.HasPrefix(path, "/__backplane/probe/stripe/"):
		s.commerceWorker(w, r, env, path)
	case path == "/products" && r.Method == "GET":
		wjson(w, 200, publicCatalog(env))
	case tmpl == "booking" && path == "/book" && r.Method == "POST":
		s.dataBook(w, r, env)
	case tmpl == "restaurant" && path == "/order" && r.Method == "POST":
		s.dataOrder(w, r, env)
	case path == "/billing/checkout":
		if len(wCatalog(env).Items) == 0 || env.get("STRIPE_SECRET_KEY") == "" {
			wjson(w, 501, map[string]any{"error": "billing is not configured"})
			return
		}
		if bearerTok(r) == "" {
			wjson(w, 401, map[string]any{"error": "sign in first"})
			return
		}
		wjson(w, 501, map[string]any{"error": "simulated"})
	case path == "/stripe/webhook":
		s.dataWebhook(w, r, env)
	default:
		wjson(w, 404, map[string]any{"error": "not found"})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isProbeReq(r *http.Request, env *wenv) bool {
	p := r.Header.Get("X-Backplane-Probe")
	return p != "" && env.get("BACKPLANE_PROBE_TOKEN") != "" && safeEq(p, env.get("BACKPLANE_PROBE_TOKEN"))
}

func trimTo(v any, n int) any {
	sv := strings.TrimSpace(str(v))
	if sv == "" {
		return nil
	}
	if len(sv) > n {
		sv = sv[:n]
	}
	return sv
}

// dataBook mirrors POST /book: validate, hold the slot, return a checkout.
func (s *Server) dataBook(w http.ResponseWriter, r *http.Request, env *wenv) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if str(body["service"]) == "" {
		wjson(w, 400, map[string]any{"error": "Choose a service."})
		return
	}
	po, err := priceLines(env, []map[string]any{{"key": body["service"], "quantity": 1.0}}, "once")
	if err != nil {
		wjson(w, 400, map[string]any{"error": err.Error()})
		return
	}
	email := strings.TrimSpace(str(body["email"]))
	if !emailRE.MatchString(email) {
		wjson(w, 400, map[string]any{"error": "Enter a valid email."})
		return
	}
	starts, err := time.Parse(time.RFC3339, str(body["starts_at"]))
	if err != nil {
		wjson(w, 400, map[string]any{"error": "Choose a date and time."})
		return
	}
	if starts.Before(time.Now().Add(15*time.Minute)) || starts.After(time.Now().Add(366*24*time.Hour)) {
		wjson(w, 400, map[string]any{"error": "Choose a time at least 15 minutes from now and within a year."})
		return
	}
	line := po.Lines[0]
	at := starts.UTC().Format(time.RFC3339)
	// The database's unique index on (service, time) for live bookings.
	if taken, _ := s.sbw(env, "GET", "bookings?service_key=eq."+line.Key+"&starts_at=eq."+url.QueryEscape(at)+"&status=in.(pending,confirmed)&select=id", nil, ""); len(taken) > 0 {
		wjson(w, 409, map[string]any{"error": "That time was just taken. Please choose another."})
		return
	}
	minutes := wCatalog(env).item(line.Key).Minutes
	if minutes == 0 {
		minutes = 30
	}
	rows, err := s.sbw(env, "POST", "bookings", []map[string]any{{"owner_id": nil, "service_key": line.Key, "service_name": line.Name, "starts_at": at, "minutes": minutes,
		"email": email, "customer_name": trimTo(body["name"], 120), "notes": trimTo(body["notes"], 1000), "status": "pending", "deposit_cents": line.Amount,
		"currency": wCatalog(env).Currency, "is_probe": isProbeReq(r, env)}}, "return=representation")
	if err != nil || len(rows) == 0 {
		wjson(w, 500, map[string]any{"error": "internal error"})
		return
	}
	s.dataPayFor(w, r, env, "bookings", rows[0], po, email)
}

// dataOrder mirrors POST /order: menu prices only, sold-out items refused.
func (s *Server) dataOrder(w http.ResponseWriter, r *http.Request, env *wenv) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	arr, _ := body["items"].([]any)
	if len(arr) == 0 {
		wjson(w, 400, map[string]any{"error": "Add something to the order."})
		return
	}
	var req []map[string]any
	for _, x := range arr {
		m, _ := x.(map[string]any)
		req = append(req, m)
	}
	po, err := priceLines(env, req, "once")
	if err != nil {
		wjson(w, 400, map[string]any{"error": err.Error()})
		return
	}
	email := strings.TrimSpace(str(body["email"]))
	if !emailRE.MatchString(email) {
		wjson(w, 400, map[string]any{"error": "Enter a valid email."})
		return
	}
	var keys []string
	for _, l := range po.Lines {
		keys = append(keys, l.Key)
	}
	if out, _ := s.sbw(env, "GET", "sold_out?item_key=in.("+strings.Join(keys, ",")+")&select=item_key", nil, ""); len(out) > 0 {
		wjson(w, 409, map[string]any{"error": "Sold out: " + str(out[0]["item_key"]) + "."})
		return
	}
	var items []any
	var total int64
	for _, l := range po.Lines {
		items = append(items, map[string]any{"key": l.Key, "name": l.Name, "quantity": l.Quantity, "unit_amount": l.Amount})
		total += l.Amount * int64(l.Quantity)
	}
	rows, err := s.sbw(env, "POST", "orders", []map[string]any{{"owner_id": nil, "items": items, "total_cents": total, "currency": wCatalog(env).Currency, "email": email,
		"customer_name": trimTo(body["name"], 120), "notes": trimTo(body["notes"], 1000), "status": "awaiting_payment", "is_probe": isProbeReq(r, env)}}, "return=representation")
	if err != nil || len(rows) == 0 {
		wjson(w, 500, map[string]any{"error": "internal error"})
		return
	}
	s.dataPayFor(w, r, env, "orders", rows[0], po, email)
}

func (s *Server) dataPayFor(w http.ResponseWriter, r *http.Request, env *wenv, table string, row map[string]any, po *pricedOrder, email string) {
	meta := map[string]string{"row_table": table, "row_id": str(row["id"]), "items": po.Summary, "item_keys": po.Keys}
	if isProbeReq(r, env) {
		meta["backplane_probe"] = "1"
	}
	back := env.get("APP_URL")
	if back == "" {
		back = env.baseURL
	}
	cs, err := s.createCheckout(env, po, back+"?session_id={CHECKOUT_SESSION_ID}", meta, url.Values{"customer_email": {email}, "expires_at": {strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)}})
	if err != nil {
		wjson(w, 500, map[string]any{"error": "internal error"})
		return
	}
	_, _ = s.sbw(env, "PATCH", table+"?id=eq."+str(row["id"]), map[string]any{"stripe_session_id": cs["id"]}, "return=minimal")
	wjson(w, 200, map[string]any{"id": row["id"], "session_id": cs["id"], "url": cs["url"]})
}

var uuidRE = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// dataWebhook mirrors the data Worker's Stripe webhook.
func (s *Server) dataWebhook(w http.ResponseWriter, r *http.Request, env *wenv) {
	payload, _ := io.ReadAll(r.Body)
	if !verifyStripeSig(payload, r.Header.Get("Stripe-Signature"), env.get("STRIPE_WEBHOOK_SECRET")) {
		wjson(w, 400, map[string]any{"error": "invalid signature"})
		return
	}
	var ev map[string]any
	_ = json.Unmarshal(payload, &ev)
	typ := str(ev["type"])
	obj, _ := ev["data"].(map[string]any)["object"].(map[string]any)
	meta, _ := obj["metadata"].(map[string]any)
	rowTable := ""
	if t := str(meta["row_table"]); (t == "bookings" || t == "orders" || t == "purchases") && uuidRE.MatchString(str(meta["row_id"])) {
		rowTable = t
	}
	if typ == "checkout.session.expired" {
		if rowTable == "bookings" || rowTable == "orders" {
			_, _ = s.sbw(env, "PATCH", rowTable+"?id=eq."+str(meta["row_id"])+"&paid=eq.false", map[string]any{"status": "expired"}, "return=minimal")
		}
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(payload))
		s.commerceWebhook(w, r2, env)
		return
	}
	if typ != "checkout.session.completed" || rowTable == "" {
		wjson(w, 200, map[string]any{"received": true, "type": typ})
		return
	}
	if ps := str(obj["payment_status"]); ps != "paid" && ps != "no_payment_required" {
		wjson(w, 200, map[string]any{"received": true, "type": typ, "outcome": "awaiting_payment"})
		return
	}
	isProbe := ev["livemode"] == false && str(meta["backplane_probe"]) == "1"
	patch := map[string]any{"paid": true, "stripe_session_id": obj["id"]}
	if rowTable == "bookings" {
		patch["status"] = "confirmed"
	}
	if rowTable == "orders" && env.get("TEMPLATE") == "restaurant" {
		patch["status"] = "received"
	}
	rows, err := s.sbw(env, "PATCH", rowTable+"?id=eq."+str(meta["row_id"])+"&paid=eq.false", patch, "return=representation")
	if err != nil {
		wjson(w, 500, map[string]any{"error": "internal error"})
		return
	}
	if len(rows) == 0 {
		wjson(w, 200, map[string]any{"received": true, "duplicate": true, "type": typ})
		return
	}
	row := rows[0]
	emailID, emailErr := "", ""
	if rowTable == "bookings" || (rowTable == "orders" && env.get("TEMPLATE") == "restaurant") {
		subject := "Order confirmed"
		if rowTable == "bookings" {
			subject = "Booking confirmed — " + str(row["service_name"])
		}
		status, data := s.resendW(env, "POST", "/emails", map[string]any{"from": env.get("FROM_EMAIL"), "to": []string{str(row["email"])}, "subject": subject, "html": "<p>Confirmed.</p>"})
		if status >= 300 {
			emailErr = fmt.Sprintf("Resend %d: %s", status, str(data["message"]))
		} else {
			emailID = str(data["id"])
		}
	}
	out := map[string]any{"received": true, "type": typ}
	if isProbe {
		out["probe"] = map[string]any{"row_id": row["id"], "email_id": emailID, "email_error": emailErr}
	}
	wjson(w, 200, out)
}
