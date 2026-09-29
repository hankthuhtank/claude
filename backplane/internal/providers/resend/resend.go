// Package resend implements the Resend adapter: sending domains (with
// automatic DNS through a connected DNS provider), least-privilege sending
// keys, published templates, webhooks and segments, plus the probes used by
// the verification engine.
//
// Reference: OpenAPI at github.com/resend/resend-openapi. Resend's test
// addresses (delivered@resend.dev, bounced@resend.dev, complained@resend.dev)
// let Backplane prove email delivery without writing to a real inbox.
package resend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// APIBase is the Resend API.
const APIBase = "https://api.resend.com"

// Test recipients documented by Resend.
const (
	TestDelivered  = "delivered@resend.dev"
	TestBounced    = "bounced@resend.dev"
	TestComplained = "complained@resend.dev"
)

// Resource kinds.
const (
	KindDomain   = "resend.domain"
	KindAPIKey   = "resend.api_key"
	KindTemplate = "resend.template"
	KindWebhook  = "resend.webhook"
	KindSegment  = "resend.segment"
)

// Provider is the Resend adapter.
type Provider struct{}

// Info describes Resend.
func (Provider) Info() providers.Info {
	return providers.Info{
		ID: "resend", Name: "Resend", Category: "Email", Phase: 1, Maturity: providers.MaturityBuild,
		Tagline:      "Transactional email with verified domains, templates and delivery events.",
		Capabilities: []core.Capability{core.CapEmail, core.CapWebhooks},
		Fields: []providers.Field{
			{Key: "token", Label: "API key (Full access)", Secret: true, Required: true, Placeholder: "re_…", Pattern: `^re_[A-Za-z0-9_]{16,}$`,
				Help: "Backplane needs Full access to add domains and create a separate send-only key for your backend."},
		},
		Guide: []providers.GuideStep{
			{Title: "Open API Keys", Body: "In Resend, open API Keys and click Create API Key.", Link: "https://resend.com/api-keys", Label: "Open Resend API keys"},
			{Title: "Name it “Backplane” with Full access", Body: "Full access lets Backplane add your sending domain and create a second, send-only key limited to that domain — the only key your backend ever gets."},
			{Title: "Paste it here", Body: "Backplane lists your domains and reads your plan's daily limit so tests never use up your quota."},
			{Title: "Your domain", Body: "If the domain is on your Cloudflare account, Backplane adds the SPF/DKIM records for you. Otherwise it shows the exact records to paste at your DNS host."},
		},
		TokenURL: "https://resend.com/api-keys", DocsURL: "https://resend.com/docs/api-reference/introduction", StatusURL: "https://resend-status.com",
		OAuth: false, OAuthNote: "Resend supports OAuth grants for registered apps. This build uses an API key; OAuth turns on once Backplane is registered with Resend.",
		Scopes: []string{"Full access (Backplane)", "Sending access to one domain (your backend)"},
		Kinds: []providers.KindInfo{
			{Kind: KindDomain, Label: "Sending domain", Capability: core.CapEmail, Destructive: "Removing the domain stops all email from it."},
			{Kind: KindAPIKey, Label: "Send-only API key", Capability: core.CapEmail},
			{Kind: KindTemplate, Label: "Email template", Capability: core.CapEmail},
			{Kind: KindWebhook, Label: "Delivery webhook", Capability: core.CapWebhooks},
			{Kind: KindSegment, Label: "Contact segment", Capability: core.CapEmail},
		},
		Discovers: []string{"Domains and verification status", "Templates", "Webhooks", "Segments", "API keys (names only)"},
	}
}

// Connect builds the client.
func (Provider) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("resend", conn.Base("resend", APIBase))
	cl.Auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
	cl.ParseError = parseError
	cl.Classify = classify
	cl.Limiter = httpx.NewLimiter(2, 2) // Resend's default team limit
	cl.Log = opts.Log
	conn.Client = cl
	return conn, nil
}

func parseError(status int, body []byte) (string, string) {
	var e struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	return e.Name, e.Message
}

func classify(status int, code, msg string) string {
	switch code {
	case "restricted_api_key":
		return httpx.KindPermission
	case "invalid_api_key", "missing_api_key":
		return httpx.KindAuth
	case "daily_quota_exceeded", "monthly_quota_exceeded":
		return httpx.KindInvalid // retrying today will not help
	case "not_found":
		return httpx.KindNotFound
	}
	if status == 403 && strings.Contains(strings.ToLower(msg), "domain") {
		return httpx.KindPermission
	}
	return ""
}

// Call performs a request and decodes JSON.
func Call(ctx context.Context, c *providers.Conn, method, path string, body any, out any) error {
	rq := httpx.Request{Method: method, Path: path}
	if body != nil {
		rq.JSON = body
	}
	resp, err := c.Client.Do(ctx, rq)
	if err != nil {
		return err
	}
	if out != nil && len(resp.Body) > 0 {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}

// Domain mirrors Resend's domain object.
type Domain struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Region  string   `json:"region"`
	Records []Record `json:"records"`
}

// Record is a DNS record Resend needs.
type Record struct {
	Record   string `json:"record"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      string `json:"ttl"`
	Status   string `json:"status"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
}

type listDomains struct {
	Data []Domain `json:"data"`
}

// Usage mirrors the /usage endpoint subset Backplane uses.
type Usage struct {
	Emails struct {
		Daily struct {
			Used  int  `json:"used"`
			Limit *int `json:"limit"`
		} `json:"daily"`
		Monthly struct {
			Used  int  `json:"used"`
			Limit *int `json:"limit"`
		} `json:"monthly"`
	} `json:"emails"`
	RateLimit struct {
		Limit    int    `json:"limit"`
		Duration string `json:"duration"`
	} `json:"rate_limit"`
}

// GetUsage reads account usage (daily/monthly quota and rate limit).
func GetUsage(ctx context.Context, c *providers.Conn) (*Usage, error) {
	var u Usage
	err := Call(ctx, c, "GET", "/usage", nil, &u)
	return &u, err
}

// Verify implements Level 1.
func (Provider) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	res := &providers.VerifyResult{Details: map[string]string{}, Settings: map[string]string{}}
	var domains listDomains
	if err := Call(ctx, c, "GET", "/domains", nil, &domains); err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("resend", "list domains", err)
		if e, ok := httpx.AsError(err); ok && e.Kind == httpx.KindPermission {
			res.Problem.Summary = "This is a send-only key. Backplane needs a Full access key to set up domains and create a send-only key for your backend."
		}
		res.Summary = res.Problem.Summary
		return res, nil
	}
	verified := 0
	var names []string
	for _, d := range domains.Data {
		names = append(names, d.Name+" ("+d.Status+")")
		if d.Status == "verified" {
			verified++
		}
	}
	res.Details["domains"] = strings.Join(names, ", ")
	res.Scopes = []string{"Full access"}
	if u, err := GetUsage(ctx, c); err == nil {
		if u.Emails.Daily.Limit != nil {
			res.Details["daily_quota"] = fmt.Sprintf("%d of %d used today", u.Emails.Daily.Used, *u.Emails.Daily.Limit)
			if *u.Emails.Daily.Limit > 0 && u.Emails.Daily.Used*100 >= *u.Emails.Daily.Limit*80 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("You have used %d of today's %d emails. Customer emails stop when the limit is reached.", u.Emails.Daily.Used, *u.Emails.Daily.Limit))
			}
		}
		if u.RateLimit.Limit > 0 {
			res.Details["rate_limit"] = fmt.Sprintf("%d requests per %s", u.RateLimit.Limit, u.RateLimit.Duration)
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	res.Health = core.HealthOK
	res.Summary = fmt.Sprintf("Connected. %d domain(s), %d verified.", len(domains.Data), verified)
	if len(res.Warnings) > 0 {
		res.Health = core.HealthWarn
	}
	return res, nil
}

// GetEmail reads a sent email's latest delivery event.
func GetEmail(ctx context.Context, c *providers.Conn, id string) (string, error) {
	var e struct {
		ID        string `json:"id"`
		LastEvent string `json:"last_event"`
	}
	if err := Call(ctx, c, "GET", "/emails/"+id, nil, &e); err != nil {
		return "", err
	}
	return e.LastEvent, nil
}

// Send sends an email (used for synthetic tests with test recipients).
func Send(ctx context.Context, c *providers.Conn, from, to, subject, html string, tags map[string]string) (string, error) {
	body := map[string]any{"from": from, "to": []string{to}, "subject": subject, "html": html}
	if len(tags) > 0 {
		var ts []map[string]string
		for k, v := range tags {
			ts = append(ts, map[string]string{"name": k, "value": v})
		}
		body["tags"] = ts
	}
	var out struct {
		ID string `json:"id"`
	}
	err := Call(ctx, c, "POST", "/emails", body, &out)
	return out.ID, err
}

// Discover lists Resend resources.
func (Provider) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var out []providers.Discovered
	var domains listDomains
	if err := Call(ctx, c, "GET", "/domains", nil, &domains); err != nil {
		return nil, err
	}
	for _, d := range domains.Data {
		out = append(out, providers.Discovered{Provider: "resend", Kind: KindDomain, ID: d.ID, Name: d.Name, Detail: d.Status + " · " + d.Region, Props: map[string]string{"status": d.Status}})
	}
	var tpls struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Alias  string `json:"alias"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/templates", nil, &tpls); err == nil {
		for _, t := range tpls.Data {
			out = append(out, providers.Discovered{Provider: "resend", Kind: KindTemplate, ID: t.ID, Name: t.Name, Detail: t.Status})
		}
	}
	var hooks struct {
		Data []struct {
			ID       string   `json:"id"`
			Endpoint string   `json:"endpoint"`
			Status   string   `json:"status"`
			Events   []string `json:"events"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/webhooks", nil, &hooks); err == nil {
		for _, h := range hooks.Data {
			d := providers.Discovered{Provider: "resend", Kind: KindWebhook, ID: h.ID, Name: h.Endpoint, Detail: strconv.Itoa(len(h.Events)) + " events · " + h.Status}
			if i := strings.Index(h.Endpoint, ".workers.dev"); i > 0 {
				host := strings.TrimPrefix(strings.TrimPrefix(h.Endpoint[:i], "https://"), "http://")
				d.Links = append(d.Links, providers.DiscoveredLink{ToKind: "cloudflare.worker", ToName: strings.SplitN(host, ".", 2)[0], Label: "delivery events to"})
			}
			out = append(out, d)
		}
	}
	var segs struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/segments", nil, &segs); err == nil {
		for _, s := range segs.Data {
			out = append(out, providers.Discovered{Provider: "resend", Kind: KindSegment, ID: s.ID, Name: s.Name})
		}
	}
	var keys struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/api-keys", nil, &keys); err == nil {
		for _, k := range keys.Data {
			out = append(out, providers.Discovered{Provider: "resend", Kind: KindAPIKey, ID: k.ID, Name: k.Name, Detail: "API key"})
		}
	}
	return out, nil
}
