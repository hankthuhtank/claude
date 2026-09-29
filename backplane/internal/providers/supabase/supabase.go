// Package supabase implements the Supabase adapter against the Management API
// (api.supabase.com/v1) plus the per-project REST, Storage and Auth APIs used
// for synthetic tests.
//
// Reference: OpenAPI at github.com/supabase/supabase apps/docs/spec/api_v1_openapi.json.
// Keys: Supabase is retiring the anon/service_role JWT keys by the end of 2026
// in favour of sb_publishable_ / sb_secret_ keys sent in the `apikey` header;
// Backplane creates a dedicated secret key per Worker and falls back to the
// legacy service_role key only on projects that do not have the new keys.
package supabase

import (
	"context"
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
)

// APIBase is the Management API.
const APIBase = "https://api.supabase.com"

// Resource kinds.
const (
	KindProject    = "supabase.project"
	KindMigration  = "supabase.migration"
	KindAPIKey     = "supabase.api_key"
	KindAuthConfig = "supabase.auth_config"
	KindBucket     = "supabase.storage_bucket"
	KindSecrets    = "supabase.function_secrets"
)

// Provider is the Supabase adapter.
type Provider struct{}

// Info describes Supabase.
func (Provider) Info() providers.Info {
	return providers.Info{
		ID: "supabase", Name: "Supabase", Category: "Database & auth", Phase: 1, Maturity: providers.MaturityBuild,
		Tagline: "Postgres database with row-level security, auth, storage and realtime.",
		Capabilities: []core.Capability{core.CapDatabase, core.CapAuth, core.CapStorage, core.CapRealtime, core.CapFunctions,
			core.CapVector, core.CapScheduler, core.CapQueue, core.CapSecrets, core.CapAPI},
		Fields: []providers.Field{
			{Key: "token", Label: "Personal access token", Secret: true, Required: true, Placeholder: "sbp_…", Pattern: `^sbp_[A-Za-z0-9]{20,}$`,
				Help: "Account → Access Tokens. Starts with sbp_."},
			{Key: "organization_slug", Label: "Organization", Help: "Optional. Backplane lists your organizations after the token is checked."},
		},
		Guide: []providers.GuideStep{
			{Title: "Open Access Tokens", Body: "Sign in to Supabase and open Account → Access Tokens.", Link: "https://supabase.com/dashboard/account/tokens", Label: "Open Supabase tokens"},
			{Title: "Generate a token named “Backplane”", Body: "Pick an expiry you're comfortable with. Supabase shows it once — copy it."},
			{Title: "Paste it here", Body: "Backplane lists your organizations and projects to confirm the token works. It's stored encrypted on this computer."},
			{Title: "Free plan note", Body: "Free projects pause after a week without activity and each organization gets two active free projects. Backplane's monitor tells you if a project is paused and can restore it."},
		},
		TokenURL: "https://supabase.com/dashboard/account/tokens", DocsURL: "https://supabase.com/docs/reference/api/introduction", StatusURL: "https://status.supabase.com",
		OAuth: false, OAuthNote: "Supabase supports OAuth for registered integrations. This build uses personal access tokens; OAuth becomes available once a Backplane OAuth app is registered with Supabase.",
		Scopes: []string{"Personal access token (full access to your organizations)"},
		Kinds: []providers.KindInfo{
			{Kind: KindProject, Label: "Project", Capability: core.CapDatabase, Destructive: "Deleting a project permanently deletes its database, users and files."},
			{Kind: KindMigration, Label: "Schema migration", Capability: core.CapDatabase},
			{Kind: KindAPIKey, Label: "Secret API key", Capability: core.CapSecrets},
			{Kind: KindAuthConfig, Label: "Auth settings", Capability: core.CapAuth},
			{Kind: KindBucket, Label: "Storage bucket", Capability: core.CapStorage, Destructive: "Files in the bucket are lost."},
			{Kind: KindSecrets, Label: "Edge Function secrets", Capability: core.CapSecrets},
		},
		Discovers: []string{"Projects (with health)", "Tables and RLS status", "Storage buckets", "Edge Functions", "Auth providers"},
	}
}

// Connect builds the Management API client.
func (Provider) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("supabase", conn.Base("supabase", APIBase))
	cl.Auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
	cl.ParseError = parseError
	cl.Limiter = httpx.NewLimiter(5, 10)
	cl.Log = opts.Log
	conn.Client = cl
	return conn, nil
}

func parseError(status int, body []byte) (string, string) {
	var e struct {
		Message   string `json:"message"`
		Msg       string `json:"msg"`
		Error     string `json:"error"`
		Code      any    `json:"code"`
		ErrorCode string `json:"error_code"`
		Hint      string `json:"hint"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	msg := e.Message
	if msg == "" {
		msg = e.Msg
	}
	if msg == "" {
		msg = e.Error
	}
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	code := e.ErrorCode
	if code == "" && e.Code != nil {
		code = fmt.Sprint(e.Code)
	}
	return code, msg
}

func do(ctx context.Context, c *providers.Conn, rq httpx.Request, out any) error {
	resp, err := c.Client.Do(ctx, rq)
	if err != nil {
		return err
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("supabase returned unexpected JSON: %w", err)
	}
	return nil
}

// Project mirrors the fields Backplane uses.
type Project struct {
	ID               string `json:"id"`
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	OrganizationID   string `json:"organization_id"`
	OrganizationSlug string `json:"organization_slug"`
	Region           string `json:"region"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

type org struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Verify implements Level 1.
func (Provider) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	res := &providers.VerifyResult{Details: map[string]string{}, Settings: map[string]string{}}
	var orgs []org
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/organizations", Quiet: true}, &orgs); err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("supabase", "list organizations", err)
		res.Summary = res.Problem.Summary
		return res, nil
	}
	var projects []Project
	_ = do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects", Quiet: true}, &projects)
	slug := c.Connection.Setting("organization_slug")
	if slug == "" && len(orgs) == 1 {
		slug = orgs[0].Slug
	}
	names := make([]string, 0, len(orgs))
	for _, o := range orgs {
		names = append(names, o.Slug+" — "+o.Name)
		if o.Slug == slug {
			res.AccountName = o.Name
			res.AccountID = o.Slug
		}
	}
	res.Details["organizations"] = strings.Join(names, "\n")
	res.Details["projects"] = strconv.Itoa(len(projects))
	paused := 0
	for _, p := range projects {
		if p.Status == "INACTIVE" {
			paused++
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	if slug == "" {
		res.Health = core.HealthWarn
		res.Summary = fmt.Sprintf("Token works and can see %d organizations. Choose which one new projects go in.", len(orgs))
		return res, nil
	}
	res.Settings["organization_slug"] = slug
	res.Health = core.HealthOK
	res.Summary = fmt.Sprintf("Connected to %s (%d projects).", orName(res.AccountName, slug), len(projects))
	if paused > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d project(s) are paused (Free plan pauses after a week of inactivity).", paused))
	}
	return res, nil
}

func orName(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// GetProject reads a project by ref.
func GetProject(ctx context.Context, c *providers.Conn, ref string) (*Project, error) {
	var p Project
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref, Quiet: true, Resource: ref}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ProjectURL is the project's public API URL.
func ProjectURL(c *providers.Conn, ref string) string {
	if b := c.Base("supabase_project", ""); b != "" {
		return b + "/" + ref
	}
	return "https://" + ref + ".supabase.co"
}

// Health is one service's health.
type Health struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

// ServiceHealth calls the project health endpoint.
func ServiceHealth(ctx context.Context, c *providers.Conn, ref string, services ...string) ([]Health, error) {
	if len(services) == 0 {
		services = []string{"auth", "db", "rest", "storage", "realtime"}
	}
	var out []Health
	err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/health", Query: url.Values{"services": {strings.Join(services, ",")}, "timeout_ms": {"8000"}}, Quiet: true}, &out)
	return out, err
}

// Query runs SQL through the Management API and returns rows.
func Query(ctx context.Context, c *providers.Conn, ref, sql string, readOnly bool) ([]map[string]any, error) {
	var rows []map[string]any
	path := "/v1/projects/" + ref + "/database/query"
	if readOnly {
		path += "/read-only"
	}
	resp, err := c.Client.Do(ctx, httpx.Request{Method: "POST", Path: path, JSON: map[string]any{"query": sql}, Idempotent: readOnly, Resource: ref, Timeout: 2 * time.Minute})
	if err != nil {
		// Older deployments have no read-only variant; fall back.
		if readOnly && httpx.IsNotFound(err) {
			return Query(ctx, c, ref, sql, false)
		}
		return nil, err
	}
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &rows); err != nil {
			return nil, nil // statements without a result set
		}
	}
	return rows, nil
}

// APIKey is a project key.
type APIKey struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // legacy | publishable | secret
	APIKey string `json:"api_key"`
	Prefix string `json:"prefix"`
}

// ListKeys returns a project's API keys (revealed when reveal is true).
func ListKeys(ctx context.Context, c *providers.Conn, ref string, reveal bool) ([]APIKey, error) {
	var keys []APIKey
	q := url.Values{}
	if reveal {
		q.Set("reveal", "true")
	}
	err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/api-keys", Query: q, Quiet: true}, &keys)
	return keys, err
}

// ServerKey returns a key with full (RLS-bypassing) access for server-side
// use: a new-style secret key when the project has one, else the legacy
// service_role JWT.
func ServerKey(ctx context.Context, c *providers.Conn, ref string) (string, string, error) {
	keys, err := ListKeys(ctx, c, ref, true)
	if err != nil {
		return "", "", err
	}
	for _, k := range keys {
		if k.Type == "secret" && k.APIKey != "" && !strings.Contains(k.APIKey, "·") {
			return k.APIKey, "secret", nil
		}
	}
	for _, k := range keys {
		if k.Name == "service_role" && k.APIKey != "" {
			return k.APIKey, "legacy", nil
		}
	}
	return "", "", &core.Problem{Title: "No server key available", Provider: "supabase", Code: "not_found", Summary: "Supabase returned no secret or service_role key for this project."}
}

// ProjectClient builds a client for a project's REST/Storage/Auth APIs.
func ProjectClient(c *providers.Conn, ref, key string, log func(httpx.LogEvent)) *httpx.Client {
	cl := httpx.New("supabase", ProjectURL(c, ref))
	cl.Auth = func(r *http.Request) {
		r.Header.Set("apikey", key)
		// Legacy JWT keys are also sent as a bearer token; new sb_ keys are
		// not JWTs and belong only in the apikey header.
		if strings.HasPrefix(key, "eyJ") {
			r.Header.Set("Authorization", "Bearer "+key)
		}
	}
	cl.ParseError = parseError
	cl.Log = log
	cl.MaxAttempts = 3
	return cl
}

// Discover lists projects and their contents.
func (Provider) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var projects []Project
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects", Quiet: true}, &projects); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, p := range projects {
		d := providers.Discovered{Provider: "supabase", Kind: KindProject, ID: p.Ref, Name: p.Name, Detail: p.Region + " · " + strings.ToLower(strings.ReplaceAll(p.Status, "_", " ")),
			Props: map[string]string{"status": p.Status, "region": p.Region, "org": p.OrganizationSlug}, Group: p.Name}
		if p.Status == "ACTIVE_HEALTHY" {
			if rows, err := Query(ctx, c, p.Ref, `select c.relname as name, c.relrowsecurity as rls from pg_class c join pg_namespace n on n.oid=c.relnamespace where n.nspname='public' and c.relkind='r' order by 1`, true); err == nil {
				noRLS := 0
				for _, r := range rows {
					if b, ok := r["rls"].(bool); ok && !b {
						noRLS++
					}
				}
				d.Detail += fmt.Sprintf(" · %d tables", len(rows))
				if noRLS > 0 {
					d.Detail += fmt.Sprintf(" (%d without RLS!)", noRLS)
					d.Props["tables_without_rls"] = strconv.Itoa(noRLS)
				}
			}
			var buckets []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Public bool   `json:"public"`
			}
			if do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + p.Ref + "/storage/buckets", Quiet: true}, &buckets) == nil {
				for _, b := range buckets {
					vis := "private"
					if b.Public {
						vis = "public"
					}
					out = append(out, providers.Discovered{Provider: "supabase", Kind: KindBucket, ID: p.Ref + "/" + b.ID, Name: b.Name, Detail: vis + " bucket in " + p.Name, Group: p.Name})
				}
			}
			var fns []struct {
				Slug   string `json:"slug"`
				Name   string `json:"name"`
				Status string `json:"status"`
			}
			if do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + p.Ref + "/functions", Quiet: true}, &fns) == nil {
				for _, f := range fns {
					out = append(out, providers.Discovered{Provider: "supabase", Kind: "supabase.function", ID: p.Ref + "/" + f.Slug, Name: f.Name, Detail: "Edge Function · " + strings.ToLower(f.Status), Group: p.Name})
				}
			}
		}
		out = append([]providers.Discovered{d}, out...)
	}
	return out, nil
}
