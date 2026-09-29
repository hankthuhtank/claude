package sim

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type rsDomain struct {
	ID, Name, Status, Region string
	Created                  time.Time
	verifyAsked              bool
}

type rsKey struct {
	ID, Name, Token, Permission, DomainID string
	Created                               time.Time
}

type rsEmail struct {
	ID, From, To, Subject, Kind string
	Created                     time.Time
	final                       string
}

type rsState struct {
	domains   map[string]*rsDomain
	keys      map[string]*rsKey
	emails    map[string]*rsEmail
	templates map[string]map[string]any
	webhooks  map[string]map[string]any
	segments  map[string]map[string]any
	daily     int
	DailyCap  int
}

func newResend() *rsState {
	return &rsState{domains: map[string]*rsDomain{}, keys: map[string]*rsKey{}, emails: map[string]*rsEmail{}, templates: map[string]map[string]any{},
		webhooks: map[string]map[string]any{}, segments: map[string]map[string]any{}, DailyCap: 100}
}

func rsErr(w http.ResponseWriter, status int, name, msg string) {
	writeJSON(w, status, map[string]any{"statusCode": status, "name": name, "message": msg})
}

func rootZone(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func (d *rsDomain) records() []map[string]any {
	sub := strings.TrimSuffix(strings.TrimSuffix(d.Name, rootZone(d.Name)), ".")
	suffix := ""
	if sub != "" {
		suffix = "." + sub
	}
	return []map[string]any{
		{"record": "SPF", "name": "send" + suffix, "type": "MX", "ttl": "Auto", "status": d.Status, "value": "feedback-smtp.us-east-1.amazonses.com", "priority": 10},
		{"record": "SPF", "name": "send" + suffix, "type": "TXT", "ttl": "Auto", "status": d.Status, "value": "\"v=spf1 include:amazonses.com ~all\""},
		{"record": "DKIM", "name": "resend._domainkey" + suffix, "type": "TXT", "ttl": "Auto", "status": d.Status, "value": "p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDsim" + d.ID[:12]},
	}
}

// dnsReadyLocked checks the simulated Cloudflare zones for a domain's records.
func (s *Server) dnsReadyLocked(d *rsDomain) bool {
	zone := rootZone(d.Name)
	for _, rec := range d.records() {
		if !s.dnsHasLocked(fmt.Sprint(rec["name"])+"."+zone, fmt.Sprint(rec["type"]), fmt.Sprint(rec["value"])) {
			return false
		}
	}
	return true
}

func (s *Server) domainJSON(d *rsDomain) map[string]any {
	if d.verifyAsked && d.Status != "verified" && s.dnsReadyLocked(d) {
		d.Status = "verified"
	}
	return map[string]any{"object": "domain", "id": d.ID, "name": d.Name, "status": d.Status, "region": d.Region, "created_at": d.Created.Format(time.RFC3339), "records": d.records()}
}

func (s *Server) resend(w http.ResponseWriter, r *http.Request, rest string) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens[tok] != "resend" {
		rsErr(w, 401, "validation_error", "API key is invalid")
		return
	}
	var key *rsKey
	for _, k := range s.rs.keys {
		if k.Token == tok {
			key = k
		}
	}
	p := segs(rest)
	m := r.Method
	sendingOnly := key != nil && key.Permission == "sending_access"
	if sendingOnly && !(eq(p, "emails") && m == "POST") {
		rsErr(w, 401, "restricted_api_key", "This API key is restricted to only send emails")
		return
	}
	rs := s.rs
	switch {
	case eq(p, "domains") && m == "GET":
		var data []any
		for _, id := range sortedKeys(rs.domains) {
			data = append(data, s.domainJSON(rs.domains[id]))
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data)})
	case eq(p, "domains") && m == "POST":
		var b struct{ Name, Region string }
		_ = readJSON(r, &b)
		for _, d := range rs.domains {
			if d.Name == b.Name {
				rsErr(w, 403, "validation_error", "The domain "+b.Name+" is already registered")
				return
			}
		}
		if b.Region == "" {
			b.Region = "us-east-1"
		}
		d := &rsDomain{ID: uuid(), Name: b.Name, Status: "not_started", Region: b.Region, Created: s.now().UTC()}
		rs.domains[d.ID] = d
		writeJSON(w, 201, s.domainJSON(d))
	case len(p) >= 2 && p[0] == "domains":
		d := rs.domains[p[1]]
		if d == nil {
			rsErr(w, 404, "not_found", "Domain not found")
			return
		}
		switch {
		case len(p) == 3 && p[2] == "verify":
			d.verifyAsked = true
			if d.Status == "not_started" {
				d.Status = "pending"
			}
			writeJSON(w, 200, map[string]any{"object": "domain", "id": d.ID})
		case m == "DELETE":
			delete(rs.domains, d.ID)
			writeJSON(w, 200, map[string]any{"object": "domain", "id": d.ID, "deleted": true})
		default:
			writeJSON(w, 200, s.domainJSON(d))
		}
	case eq(p, "api-keys") && m == "GET":
		var data []any
		for _, id := range sortedKeys(rs.keys) {
			k := rs.keys[id]
			data = append(data, map[string]any{"id": k.ID, "name": k.Name, "created_at": k.Created.Format(time.RFC3339)})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case eq(p, "api-keys") && m == "POST":
		var b struct {
			Name       string `json:"name"`
			Permission string `json:"permission"`
			DomainID   string `json:"domain_id"`
		}
		_ = readJSON(r, &b)
		if b.DomainID != "" && rs.domains[b.DomainID] == nil {
			rsErr(w, 422, "validation_error", "Domain not found")
			return
		}
		k := &rsKey{ID: uuid(), Name: b.Name, Token: "re_" + newID("", 16), Permission: b.Permission, DomainID: b.DomainID, Created: s.now().UTC()}
		rs.keys[k.ID] = k
		s.tokens[k.Token] = "resend"
		writeJSON(w, 201, map[string]any{"id": k.ID, "token": k.Token})
	case len(p) == 2 && p[0] == "api-keys" && m == "DELETE":
		if k := rs.keys[p[1]]; k != nil {
			delete(s.tokens, k.Token)
			delete(rs.keys, p[1])
		}
		writeJSON(w, 200, map[string]any{})
	case eq(p, "emails") && m == "POST":
		var b struct {
			From     string         `json:"from"`
			To       []string       `json:"to"`
			Subject  string         `json:"subject"`
			HTML     string         `json:"html"`
			Template map[string]any `json:"template"`
		}
		_ = readJSON(r, &b)
		if len(b.To) == 0 || b.From == "" {
			rsErr(w, 422, "validation_error", "Missing `to` or `from` field.")
			return
		}
		addr := b.From
		if i := strings.LastIndex(addr, "<"); i >= 0 {
			addr = strings.TrimSuffix(addr[i+1:], ">")
		}
		dom := addr[strings.LastIndex(addr, "@")+1:]
		var d *rsDomain
		for _, x := range rs.domains {
			if x.Name == dom {
				d = x
			}
		}
		if d == nil || s.domainJSON(d)["status"] != "verified" {
			rsErr(w, 403, "validation_error", "The "+dom+" domain is not verified. Please, add and verify your domain on https://resend.com/domains")
			return
		}
		if key != nil && key.DomainID != "" && key.DomainID != d.ID {
			rsErr(w, 403, "validation_error", "This API key can only send from its domain")
			return
		}
		if b.Template != nil {
			t := rs.templates[str(b.Template["id"])]
			if t == nil || t["status"] != "published" {
				rsErr(w, 422, "validation_error", "Template not found or not published")
				return
			}
			b.Subject = str(t["subject"])
		} else if b.Subject == "" {
			rsErr(w, 422, "validation_error", "Missing `subject` field.")
			return
		}
		if rs.daily >= rs.DailyCap {
			rsErr(w, 429, "daily_quota_exceeded", "You have reached your daily email sending quota.")
			return
		}
		rs.daily++
		final := "delivered"
		switch strings.ToLower(b.To[0]) {
		case "bounced@resend.dev":
			final = "bounced"
		case "complained@resend.dev":
			final = "complained"
		}
		e := &rsEmail{ID: uuid(), From: b.From, To: b.To[0], Subject: b.Subject, Created: s.now(), final: final}
		rs.emails[e.ID] = e
		go s.deliverResendEvents(e)
		writeJSON(w, 200, map[string]any{"id": e.ID})
	case len(p) == 2 && p[0] == "emails" && m == "GET":
		e := rs.emails[p[1]]
		if e == nil {
			rsErr(w, 404, "not_found", "Email not found")
			return
		}
		last := "sent"
		if s.now().Sub(e.Created) > 250*time.Millisecond {
			last = e.final
		}
		writeJSON(w, 200, map[string]any{"object": "email", "id": e.ID, "to": []string{e.To}, "from": e.From, "subject": e.Subject, "created_at": e.Created.Format(time.RFC3339), "last_event": last})
	case eq(p, "templates") && m == "POST":
		var b map[string]any
		_ = readJSON(r, &b)
		if alias := str(b["alias"]); alias != "" {
			for _, t := range rs.templates {
				if t["alias"] == alias {
					rsErr(w, 409, "validation_error", "A template with this alias already exists")
					return
				}
			}
		}
		id := uuid()
		b["id"], b["object"], b["status"] = id, "template", "draft"
		rs.templates[id] = b
		writeJSON(w, 201, map[string]any{"id": id, "object": "template"})
	case len(p) >= 2 && p[0] == "templates":
		t := rs.templates[p[1]]
		if t == nil {
			for _, x := range rs.templates {
				if x["alias"] == p[1] {
					t = x
				}
			}
		}
		if t == nil {
			rsErr(w, 404, "not_found", "Template not found")
			return
		}
		switch {
		case len(p) == 3 && p[2] == "publish":
			t["status"] = "published"
			t["has_unpublished_versions"] = false
			writeJSON(w, 200, map[string]any{"id": t["id"], "object": "template"})
		case m == "PATCH":
			var b map[string]any
			_ = readJSON(r, &b)
			for k, v := range b {
				t[k] = v
			}
			t["has_unpublished_versions"] = true
			writeJSON(w, 200, map[string]any{"id": t["id"], "object": "template"})
		case m == "DELETE":
			delete(rs.templates, str(t["id"]))
			writeJSON(w, 200, map[string]any{"id": t["id"], "deleted": true})
		default:
			writeJSON(w, 200, t)
		}
	case eq(p, "templates") && m == "GET":
		var data []any
		for _, id := range sortedKeys(rs.templates) {
			data = append(data, rs.templates[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data)})
	case eq(p, "webhooks") && m == "POST":
		var b struct {
			Endpoint string   `json:"endpoint"`
			Events   []string `json:"events"`
		}
		_ = readJSON(r, &b)
		secret := make([]byte, 24)
		copy(secret, []byte(newID("", 12)))
		hook := map[string]any{"object": "webhook", "id": uuid(), "endpoint": b.Endpoint, "events": b.Events, "status": "enabled", "created_at": s.now().UTC().Format(time.RFC3339),
			"signing_secret": "whsec_" + base64.StdEncoding.EncodeToString(secret)}
		rs.webhooks[str(hook["id"])] = hook
		writeJSON(w, 201, map[string]any{"object": "webhook", "id": hook["id"], "signing_secret": hook["signing_secret"]})
	case eq(p, "webhooks") && m == "GET":
		var data []any
		for _, id := range sortedKeys(rs.webhooks) {
			data = append(data, rs.webhooks[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data), "has_more": false})
	case len(p) == 2 && p[0] == "webhooks":
		h := rs.webhooks[p[1]]
		if h == nil {
			rsErr(w, 404, "not_found", "Webhook not found")
			return
		}
		switch m {
		case "PATCH":
			var b map[string]any
			_ = readJSON(r, &b)
			for k, v := range b {
				h[k] = v
			}
			writeJSON(w, 200, map[string]any{"object": "webhook", "id": h["id"]})
		case "DELETE":
			delete(rs.webhooks, p[1])
			writeJSON(w, 200, map[string]any{"object": "webhook", "id": h["id"], "deleted": true})
		default:
			writeJSON(w, 200, h)
		}
	case eq(p, "segments") && m == "POST":
		var b map[string]any
		_ = readJSON(r, &b)
		id := uuid()
		rs.segments[id] = map[string]any{"object": "segment", "id": id, "name": b["name"]}
		writeJSON(w, 201, map[string]any{"object": "segment", "id": id})
	case eq(p, "segments") && m == "GET":
		var data []any
		for _, id := range sortedKeys(rs.segments) {
			data = append(data, rs.segments[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": orAny(data)})
	case len(p) == 2 && p[0] == "segments":
		sg := rs.segments[p[1]]
		if sg == nil {
			rsErr(w, 404, "not_found", "Segment not found")
			return
		}
		if m == "DELETE" {
			delete(rs.segments, p[1])
		}
		writeJSON(w, 200, sg)
	case eq(p, "usage"):
		writeJSON(w, 200, map[string]any{"object": "usage", "emails": map[string]any{"daily": map[string]any{"used": rs.daily, "limit": rs.DailyCap, "sent": rs.daily, "received": 0},
			"monthly": map[string]any{"used": rs.daily, "limit": 3000}}, "rate_limit": map[string]any{"limit": 2, "duration": "1000ms"}})
	default:
		rsErr(w, 404, "not_found", "The requested endpoint does not exist.")
	}
}

// deliverResendEvents posts Svix-signed delivery events to webhooks.
func (s *Server) deliverResendEvents(e *rsEmail) {
	time.Sleep(s.DeliverDelay + 300*time.Millisecond)
	s.mu.Lock()
	type t struct{ url, secret string }
	var targets []t
	evType := "email." + e.final
	for _, h := range s.rs.webhooks {
		if h["status"] != "enabled" {
			continue
		}
		for _, ev := range toStrings(h["events"]) {
			if ev == evType {
				targets = append(targets, t{str(h["endpoint"]), str(h["signing_secret"])})
			}
		}
	}
	s.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"type": evType, "created_at": time.Now().UTC().Format(time.RFC3339), "data": map[string]any{"email_id": e.ID, "to": []string{e.To}, "subject": e.Subject}})
	for _, tg := range targets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(tg.secret, "whsec_"))
		if err != nil {
			continue
		}
		id := "msg_" + newID("", 10)
		ts := fmt.Sprint(time.Now().Unix())
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(payload)
		req, _ := http.NewRequest("POST", tg.url, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("svix-id", id)
		req.Header.Set("svix-timestamp", ts)
		req.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		if resp, err := s.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
}

func (s *Server) rsUnverify(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range sortedKeys(s.rs.domains) {
		if target != "" && id != target {
			continue
		}
		d := s.rs.domains[id]
		d.Status, d.verifyAsked = "failed", false
		return "Resend domain " + d.Name + " failed verification (DNS records removed)", nil
	}
	return "", fmt.Errorf("no domain")
}

// resendSendLocked is used by the simulated Worker.
func (s *Server) resendSendLocked(token, from, to, subject string, template map[string]any) (string, int, string) {
	if s.tokens[token] != "resend" {
		return "", 401, "invalid_api_key"
	}
	addr := from
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		addr = strings.TrimSuffix(addr[i+1:], ">")
	}
	dom := addr[strings.LastIndex(addr, "@")+1:]
	var d *rsDomain
	for _, x := range s.rs.domains {
		if x.Name == dom {
			d = x
		}
	}
	if d == nil || s.domainJSON(d)["status"] != "verified" {
		return "", 403, "The " + dom + " domain is not verified."
	}
	for _, k := range s.rs.keys {
		if k.Token == token && k.DomainID != "" && k.DomainID != d.ID {
			return "", 403, "This API key can only send from its domain"
		}
	}
	if template != nil {
		t := s.rs.templates[str(template["id"])]
		if t == nil || t["status"] != "published" {
			return "", 422, "Template not found or not published"
		}
		subject = str(t["subject"])
	}
	if s.rs.daily >= s.rs.DailyCap {
		return "", 429, "daily_quota_exceeded"
	}
	s.rs.daily++
	final := "delivered"
	if strings.EqualFold(to, "bounced@resend.dev") {
		final = "bounced"
	}
	e := &rsEmail{ID: uuid(), From: from, To: to, Subject: subject, Created: s.now(), final: final}
	s.rs.emails[e.ID] = e
	go s.deliverResendEvents(e)
	return e.ID, 200, ""
}

func (s *Server) resendKeyKindLocked(token string) string {
	if s.tokens[token] != "resend" {
		return "invalid"
	}
	for _, k := range s.rs.keys {
		if k.Token == token && k.Permission == "sending_access" {
			return "sending"
		}
	}
	return "full"
}
