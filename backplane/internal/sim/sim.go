// Package sim is a local, in-memory stand-in for Cloudflare, Supabase,
// Stripe, Resend and GitHub. It speaks the same wire formats as the real APIs
// for every endpoint Backplane uses, runs a simulated Worker for generated
// backends, delivers signed webhooks, and can inject faults (outages, rate
// limits, revoked credentials, deleted resources, drift).
//
// It powers two things: Backplane's automated tests, and Practice mode — a
// clearly labelled sandbox where people can build, verify and deliberately
// break a backend without touching real accounts. Nothing here is ever used
// when a real connection is selected.
package sim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Server is the simulator.
type Server struct {
	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	Base string

	tokens map[string]string // token -> provider
	faults map[string]*Fault // provider -> fault
	cf     *cfState
	sb     *sbState
	st     *stripeState
	rs     *rsState
	gh     *ghState
	now    func() time.Time
	client *http.Client
	// DeliverDelay is how long Stripe/Resend webhook delivery waits.
	DeliverDelay time.Duration
	// Log receives a line per request (optional).
	Log func(string)
}

// Fault is an injected failure for one provider.
type Fault struct {
	Mode      string // outage | ratelimit | slow | revoke
	Remaining int    // for ratelimit: number of 429s to return (-1 = forever)
	Until     time.Time
}

// New creates a simulator (not started).
func New() *Server {
	s := &Server{tokens: map[string]string{}, faults: map[string]*Fault{}, now: time.Now,
		client: &http.Client{Timeout: 10 * time.Second}, DeliverDelay: 150 * time.Millisecond}
	s.cf = newCF()
	s.sb = newSB()
	s.st = newStripe()
	s.rs = newResend()
	s.gh = newGH()
	return s
}

// Start listens on a random loopback port.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.Base = "http://" + ln.Addr().String()
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return nil
}

// Close stops the simulator.
func (s *Server) Close() {
	if s.srv != nil {
		s.srv.Close()
	}
}

// BaseURLs is the provider base-URL map for practice connections.
func (s *Server) BaseURLs() map[string]string {
	return map[string]string{
		"cloudflare":       s.Base + "/cloudflare/client/v4",
		"supabase":         s.Base + "/supabase-api",
		"supabase_project": s.Base + "/supabase-project",
		"stripe":           s.Base + "/stripe",
		"resend":           s.Base + "/resend",
		"github":           s.Base + "/github",
		"workers_dev":      s.Base + "/__workers",
	}
}

// AddToken registers a valid credential for a provider.
func (s *Server) AddToken(provider, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = provider
}

// RevokeToken invalidates a credential.
func (s *Server) RevokeToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// SetFault injects a fault for a provider ("" clears).
func (s *Server) SetFault(provider string, f *Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f == nil {
		delete(s.faults, provider)
		return
	}
	s.faults[provider] = f
}

func newID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ServeHTTP routes to the simulated provider.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if s.Log != nil {
		s.Log(r.Method + " " + path)
	}
	var provider, rest string
	switch {
	case strings.HasPrefix(path, "/cloudflare/client/v4"):
		provider, rest = "cloudflare", strings.TrimPrefix(path, "/cloudflare/client/v4")
	case strings.HasPrefix(path, "/supabase-api"):
		provider, rest = "supabase", strings.TrimPrefix(path, "/supabase-api")
	case strings.HasPrefix(path, "/supabase-project/"):
		provider, rest = "supabase_project", strings.TrimPrefix(path, "/supabase-project")
	case strings.HasPrefix(path, "/stripe"):
		provider, rest = "stripe", strings.TrimPrefix(path, "/stripe")
	case strings.HasPrefix(path, "/resend"):
		provider, rest = "resend", strings.TrimPrefix(path, "/resend")
	case strings.HasPrefix(path, "/github"):
		provider, rest = "github", strings.TrimPrefix(path, "/github")
	case strings.HasPrefix(path, "/__workers/"):
		provider, rest = "worker", strings.TrimPrefix(path, "/__workers")
	case strings.HasPrefix(path, "/__sim/"):
		s.control(w, r, strings.TrimPrefix(path, "/__sim"))
		return
	default:
		http.NotFound(w, r)
		return
	}
	faultKey := provider
	if provider == "supabase_project" {
		faultKey = "supabase"
	}
	if provider == "worker" {
		faultKey = "cloudflare"
	}
	if s.applyFault(w, faultKey) {
		return
	}
	switch provider {
	case "cloudflare":
		s.cloudflare(w, r, rest)
	case "supabase":
		s.supabaseAPI(w, r, rest)
	case "supabase_project":
		s.supabaseProject(w, r, rest)
	case "stripe":
		s.stripe(w, r, rest)
	case "resend":
		s.resend(w, r, rest)
	case "github":
		s.github(w, r, rest)
	case "worker":
		s.worker(w, r, rest)
	}
}

func (s *Server) applyFault(w http.ResponseWriter, provider string) bool {
	s.mu.Lock()
	f := s.faults[provider]
	if f != nil && !f.Until.IsZero() && s.now().After(f.Until) {
		delete(s.faults, provider)
		f = nil
	}
	var mode string
	if f != nil {
		mode = f.Mode
		if mode == "ratelimit" {
			if f.Remaining == 0 {
				delete(s.faults, provider)
				mode = ""
			} else if f.Remaining > 0 {
				f.Remaining--
			}
		}
	}
	s.mu.Unlock()
	switch mode {
	case "outage":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"message":"Service temporarily unavailable (simulated outage)"}`)
		return true
	case "ratelimit":
		w.Header().Set("Retry-After", "0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"message":"Too many requests (simulated)"}`)
		return true
	case "slow":
		time.Sleep(2 * time.Second)
	}
	return false
}

// auth checks a bearer (or basic) credential for a provider.
func (s *Server) auth(r *http.Request, provider string) bool {
	h := r.Header.Get("Authorization")
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok == h {
		tok = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return tok != "" && s.tokens[tok] == provider
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func segs(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func match(parts []string, pattern ...string) (map[string]string, bool) {
	if len(parts) != len(pattern) {
		return nil, false
	}
	vars := map[string]string{}
	for i, p := range pattern {
		if strings.HasPrefix(p, ":") {
			vars[p[1:]] = parts[i]
			continue
		}
		if p != parts[i] {
			return nil, false
		}
	}
	return vars, true
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- control API used by tests and the practice "break it" lab ----

// Control actions.
type Control struct {
	Action   string `json:"action"`
	Provider string `json:"provider"`
	Target   string `json:"target"`
	Value    string `json:"value"`
	Count    int    `json:"count"`
}

func (s *Server) control(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/state" {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"workers": len(s.cf.scripts), "projects": len(s.sb.projects), "webhooks": len(s.st.endpoints)})
		return
	}
	var c Control
	if err := readJSON(r, &c); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	msg, err := s.Apply(c)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"ok": msg})
}

// Apply performs a control action (fault injection or drift).
func (s *Server) Apply(c Control) (string, error) {
	switch c.Action {
	case "outage":
		s.SetFault(c.Provider, &Fault{Mode: "outage", Until: s.now().Add(10 * time.Minute)})
		return c.Provider + " is now down", nil
	case "ratelimit":
		n := c.Count
		if n == 0 {
			n = 3
		}
		s.SetFault(c.Provider, &Fault{Mode: "ratelimit", Remaining: n})
		return fmt.Sprintf("%s will rate-limit the next %d calls", c.Provider, n), nil
	case "restore":
		s.SetFault(c.Provider, nil)
		return c.Provider + " restored", nil
	case "revoke":
		s.mu.Lock()
		for tok, p := range s.tokens {
			if p == c.Provider {
				delete(s.tokens, tok)
				s.tokens["revoked:"+tok] = "revoked"
			}
		}
		s.mu.Unlock()
		return c.Provider + " credential revoked", nil
	case "unrevoke":
		s.mu.Lock()
		for tok, p := range s.tokens {
			if p == "revoked" && strings.HasPrefix(tok, "revoked:") {
				delete(s.tokens, tok)
				s.tokens[strings.TrimPrefix(tok, "revoked:")] = c.Provider
			}
		}
		s.mu.Unlock()
		return c.Provider + " credential restored", nil
	case "remove_binding":
		return s.cfRemoveBinding(c.Target, c.Value)
	case "remove_secret":
		return s.cfRemoveSecret(c.Target, c.Value)
	case "delete_bucket":
		return s.cfDeleteBucket(c.Target)
	case "disable_webhook":
		return s.stripeDisableWebhook(c.Target)
	case "move_webhook":
		return s.stripeMoveWebhook(c.Target, c.Value)
	case "pause_project":
		return s.sbPause(c.Target)
	case "disable_rls":
		return s.sbDisableRLS(c.Target, c.Value)
	case "revoke_db_key":
		return s.sbRevokeKey(c.Target, c.Value)
	case "archive_product":
		return s.stripeArchive(c.Target)
	case "unverify_domain":
		return s.rsUnverify(c.Target)
	case "rotate_webhook_secret":
		return s.cfCorruptSecret(c.Target, "STRIPE_WEBHOOK_SECRET")
	case "delete_worker":
		return s.cfDeleteWorker(c.Target)
	case "fail_deploy":
		return s.ghFailDeploy(c.Target)
	case "pay_session":
		return s.stripePaySession(c.Target, c.Value)
	case "cancel_subscription":
		return s.stripeCancelSubscription(c.Target)
	}
	return "", fmt.Errorf("unknown action %q", c.Action)
}
