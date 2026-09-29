// Package cloudflare implements the Cloudflare adapter: Workers (scripts,
// bindings, secrets, cron triggers, workers.dev), R2, KV, D1, Queues, DNS
// and Worker custom domains, against the v4 REST API.
//
// API reference: https://developers.cloudflare.com/api/ (OpenAPI schema at
// github.com/cloudflare/api-schemas). Worker uploads use the stable
// multipart form described at
// https://developers.cloudflare.com/workers/configuration/multipart-upload-metadata/
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// APIBase is the Cloudflare v4 API.
const APIBase = "https://api.cloudflare.com/client/v4"

// TokenTemplate pre-fills the dashboard token form with exactly the
// permissions Backplane needs (documented template-URL format:
// developers.cloudflare.com/fundamentals/api/how-to/account-owned-token-template/).
var TokenTemplate = func() string {
	perms := `[{"key":"account_settings","type":"read"},{"key":"workers_scripts","type":"edit"},{"key":"workers_kv_storage","type":"edit"},{"key":"workers_r2","type":"edit"},{"key":"d1","type":"edit"},{"key":"queues","type":"edit"},{"key":"workers_routes","type":"edit"},{"key":"dns","type":"edit"},{"key":"zone","type":"read"}]`
	return "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=" + url.QueryEscape(perms) + "&accountId=*&zoneId=all&name=" + url.QueryEscape("Backplane")
}()

// Provider is the Cloudflare adapter.
type Provider struct{}

// Info describes Cloudflare.
func (Provider) Info() providers.Info {
	return providers.Info{
		ID: "cloudflare", Name: "Cloudflare", Category: "Compute, storage & edge", Phase: 1, Maturity: providers.MaturityBuild,
		Tagline: "Workers run your API at the edge; R2, KV, D1 and Queues store and move data.",
		Capabilities: []core.Capability{core.CapAPI, core.CapFunctions, core.CapCompute, core.CapStorage, core.CapCache,
			core.CapDatabase, core.CapQueue, core.CapScheduler, core.CapDNS, core.CapSecrets, core.CapWebhooks, core.CapDeployment, core.CapVector},
		Fields: []providers.Field{
			{Key: "token", Label: "API token", Secret: true, Required: true, Help: "An API token (not the Global API Key).", Pattern: `^[A-Za-z0-9_\-]{30,}$`},
			{Key: "account_id", Label: "Account ID", Help: "Optional. Leave blank and Backplane finds it; needed if the token can see several accounts.", Pattern: `^[a-f0-9]{32}$`},
			{Key: "deploy_token", Label: "Deploy-only token (recommended)", Secret: true, Help: "Optional token with only Workers Scripts: Edit, given to GitHub Actions for automatic deploys. If blank, GitHub gets the main token and the Security panel shows a warning."},
		},
		Guide: []providers.GuideStep{
			{Title: "Open the pre-filled token page", Body: "This link opens Cloudflare's Create Token form with only the permissions Backplane uses already selected: Workers Scripts, KV, R2, D1, Queues, Workers Routes and DNS (edit), plus Account Settings and Zone (read).", Link: TokenTemplate, Label: "Create token on Cloudflare"},
			{Title: "Choose the account and zones", Body: "Under Account Resources pick the account for this backend. Under Zone Resources pick the domain you will use (or All zones)."},
			{Title: "Create and copy", Body: "Click Continue to summary → Create Token. Cloudflare shows the token once — copy it."},
			{Title: "Paste it here", Body: "Backplane verifies it immediately, finds your account and checks each permission. The token is stored encrypted on this computer only."},
			{Title: "workers.dev subdomain", Body: "New Cloudflare accounts must pick a workers.dev subdomain once. If yours has none, Backplane offers to create it during the build."},
		},
		TokenURL: TokenTemplate, DocsURL: "https://developers.cloudflare.com/fundamentals/api/get-started/create-token/", StatusURL: "https://www.cloudflarestatus.com",
		OAuth: false, OAuthNote: "Cloudflare does not offer third-party OAuth for general accounts; a scoped API token is the supported method.",
		Scopes: []string{"Workers Scripts: Edit", "Workers KV Storage: Edit", "Workers R2 Storage: Edit", "D1: Edit", "Queues: Edit", "Workers Routes: Edit", "DNS: Edit", "Account Settings: Read", "Zone: Read"},
		Kinds: []providers.KindInfo{
			{Kind: KindWorker, Label: "Worker", Capability: core.CapAPI, Destructive: "Deleting a Worker takes your API offline immediately."},
			{Kind: KindR2Bucket, Label: "R2 bucket", Capability: core.CapStorage, Destructive: "A bucket must be empty to delete; files are lost."},
			{Kind: KindKV, Label: "KV namespace", Capability: core.CapCache, Destructive: "All keys in the namespace are lost."},
			{Kind: KindD1, Label: "D1 database", Capability: core.CapDatabase, Destructive: "All tables and rows are lost."},
			{Kind: KindQueue, Label: "Queue", Capability: core.CapQueue},
			{Kind: KindSubdomain, Label: "workers.dev subdomain", Capability: core.CapDNS},
			{Kind: KindR2Object, Label: "R2 object", Capability: core.CapStorage},
			{Kind: KindDNSRecord, Label: "DNS record", Capability: core.CapDNS, Destructive: "Removing a record can take a site or email offline."},
			{Kind: KindWorkerDomain, Label: "Worker custom domain", Capability: core.CapDNS},
		},
		Discovers: []string{"Workers (with bindings)", "R2 buckets", "KV namespaces", "D1 databases", "Queues", "Zones", "Worker custom domains"},
	}
}

// Connect builds a client.
func (Provider) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("cloudflare", conn.Base("cloudflare", APIBase))
	cl.Auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
	cl.ParseError = parseError
	cl.Classify = classify
	cl.Limiter = httpx.NewLimiter(4, 8) // 1,200 requests per 5 minutes
	cl.Log = opts.Log
	conn.Client = cl
	return conn, nil
}

type envelope struct {
	Success  bool            `json:"success"`
	Errors   []apiMessage    `json:"errors"`
	Messages []apiMessage    `json:"messages"`
	Result   json.RawMessage `json:"result"`
	Info     *resultInfo     `json:"result_info"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type resultInfo struct {
	Page       int    `json:"page"`
	PerPage    int    `json:"per_page"`
	TotalPages int    `json:"total_pages"`
	Count      int    `json:"count"`
	Cursor     string `json:"cursor"`
}

func parseError(status int, body []byte) (string, string) {
	var e envelope
	if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
		msgs := make([]string, 0, len(e.Errors))
		for _, m := range e.Errors {
			msgs = append(msgs, m.Message)
		}
		return strconv.Itoa(e.Errors[0].Code), strings.Join(msgs, "; ")
	}
	return "", ""
}

// classify refines Cloudflare's status codes. A malformed, revoked or expired
// token is an authentication failure wherever it shows up. Code 10000
// ("Authentication error") with 403 is what Cloudflare returns when a valid
// token lacks a permission, so it keeps the default "permission" meaning.
func classify(status int, code, msg string) string {
	lm := strings.ToLower(msg)
	switch {
	case code == "9109" || code == "6003" || code == "6111" || code == "1000" ||
		strings.Contains(lm, "invalid api token") || strings.Contains(lm, "invalid access token") || strings.Contains(lm, "token expired"):
		return httpx.KindAuth
	case status == 401 || status == 403:
		return ""
	case strings.Contains(lm, "already exists") || strings.Contains(lm, "already in use") || strings.Contains(lm, "already taken") || code == "10004" || code == "10014":
		return httpx.KindConflict
	case strings.Contains(lm, "not found") || strings.Contains(lm, "does not exist") || code == "10007" || code == "10006" || code == "10013":
		return httpx.KindNotFound
	}
	return ""
}

// api performs a call and decodes the envelope's result into out.
func api(ctx context.Context, conn *providers.Conn, rq httpx.Request, out any) (*envelope, error) {
	resp, err := conn.Client.Do(ctx, rq)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("cloudflare returned unreadable JSON: %w", err)
	}
	if !env.Success {
		code, msg := parseError(resp.Status, resp.Body)
		return nil, &httpx.Error{Provider: "cloudflare", Method: rq.Method, Path: rq.Path, Status: resp.Status, Kind: httpx.KindInvalid, Code: code, Message: msg}
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare result did not match the expected shape: %w", err)
		}
	}
	return &env, nil
}

// AccountID returns the connection's account id.
func AccountID(conn *providers.Conn) (string, error) {
	if id := conn.Connection.Setting("account_id"); id != "" {
		return id, nil
	}
	if conn.Connection.AccountID != "" {
		return conn.Connection.AccountID, nil
	}
	return "", &core.Problem{Title: "Cloudflare account not selected", Provider: "cloudflare", Code: "invalid",
		Summary: "The Cloudflare token can see more than one account (or none). Open the connection and choose the account ID to use."}
}

type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tokenStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresOn string `json:"expires_on"`
}

// Verify implements Level 1.
func (Provider) Verify(ctx context.Context, conn *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	res := &providers.VerifyResult{Details: map[string]string{}, Settings: map[string]string{}}
	var ts tokenStatus
	_, err := api(ctx, conn, httpx.Request{Method: "GET", Path: "/user/tokens/verify", Quiet: true}, &ts)
	if err != nil {
		// Account-owned tokens verify at the account endpoint instead.
		if acct := conn.Connection.Setting("account_id"); acct != "" {
			_, err2 := api(ctx, conn, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/tokens/verify", Quiet: true}, &ts)
			if err2 == nil {
				err = nil
				res.Details["token_type"] = "account-owned"
			}
		}
	} else {
		res.Details["token_type"] = "user"
	}
	if err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("cloudflare", "verify the API token", err)
		res.Summary = res.Problem.Summary
		return res, nil
	}
	if ts.Status != "active" {
		res.Health = core.HealthFail
		res.Summary = "This token is " + ts.Status + "."
		res.Problem = &core.Problem{Title: "CLOUDFLARE TOKEN " + strings.ToUpper(ts.Status), Provider: "cloudflare", Code: "auth", Summary: "Cloudflare reports the token status as “" + ts.Status + "”. Create a new token with the setup guide."}
		return res, nil
	}
	if ts.ExpiresOn != "" {
		res.Details["expires_on"] = ts.ExpiresOn
		if t, err := time.Parse(time.RFC3339, ts.ExpiresOn); err == nil && time.Until(t) < 14*24*time.Hour {
			res.Warnings = append(res.Warnings, "The token expires "+t.Format("Jan 2")+". Replace it before then or builds and checks will stop.")
		}
	}
	var accounts []account
	if _, err := api(ctx, conn, httpx.Request{Method: "GET", Path: "/accounts", Query: url.Values{"per_page": {"50"}}, Quiet: true}, &accounts); err != nil {
		res.Warnings = append(res.Warnings, "The token cannot list accounts (Account Settings: Read). Enter the account ID manually.")
	}
	acctID := conn.Connection.Setting("account_id")
	if acctID == "" && len(accounts) == 1 {
		acctID = accounts[0].ID
	}
	for _, a := range accounts {
		if a.ID == acctID {
			res.AccountName = a.Name
		}
	}
	if acctID == "" {
		res.Health = core.HealthWarn
		res.Summary = fmt.Sprintf("Token is valid and can see %d accounts. Choose which one to use.", len(accounts))
		opts := make([]string, 0, len(accounts))
		for _, a := range accounts {
			opts = append(opts, a.ID+" — "+a.Name)
		}
		res.Details["accounts"] = strings.Join(opts, "\n")
		res.LatencyMS = time.Since(start).Milliseconds()
		return res, nil
	}
	res.AccountID = acctID
	res.Settings["account_id"] = acctID
	// Probe each permission with a cheap read. A 403 means the token lacks it.
	checks := []struct{ perm, path string }{
		{"Workers Scripts", "/accounts/" + acctID + "/workers/scripts"},
		{"Workers R2 Storage", "/accounts/" + acctID + "/r2/buckets"},
		{"Workers KV Storage", "/accounts/" + acctID + "/storage/kv/namespaces"},
		{"D1", "/accounts/" + acctID + "/d1/database"},
		{"Queues", "/accounts/" + acctID + "/queues"},
	}
	var have []string
	for _, c := range checks {
		_, err := api(ctx, conn, httpx.Request{Method: "GET", Path: c.path, Query: url.Values{"per_page": {"1"}}, Quiet: true}, nil)
		if err != nil {
			if e, ok := httpx.AsError(err); ok && (e.Kind == httpx.KindPermission || e.Kind == httpx.KindAuth) {
				res.Missing = append(res.Missing, c.perm)
				continue
			}
			// R2 returns a specific error when R2 has never been enabled.
			if e, ok := httpx.AsError(err); ok && strings.Contains(strings.ToLower(e.Message), "r2") && strings.Contains(strings.ToLower(e.Message), "enable") {
				res.Warnings = append(res.Warnings, "R2 is not enabled on this account yet. Enable it once in the Cloudflare dashboard (R2 → Get started).")
				continue
			}
			res.Warnings = append(res.Warnings, c.perm+": "+err.Error())
			continue
		}
		have = append(have, c.perm)
	}
	res.Scopes = have
	var sub struct {
		Subdomain string `json:"subdomain"`
	}
	if _, err := api(ctx, conn, httpx.Request{Method: "GET", Path: "/accounts/" + acctID + "/workers/subdomain", Quiet: true}, &sub); err == nil && sub.Subdomain != "" {
		res.Details["workers_dev"] = sub.Subdomain + ".workers.dev"
		res.Settings["workers_subdomain"] = sub.Subdomain
	} else {
		res.Warnings = append(res.Warnings, "No workers.dev subdomain yet. Backplane can create one during the first build.")
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case len(res.Missing) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Connected to " + orName(res.AccountName, acctID) + ", but the token is missing: " + strings.Join(res.Missing, ", ") + "."
	case len(res.Warnings) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Connected to " + orName(res.AccountName, acctID) + " with a note."
	default:
		res.Health = core.HealthOK
		res.Summary = "Connected to " + orName(res.AccountName, acctID) + ". All required permissions granted."
	}
	return res, nil
}

func orName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// ---- multipart Worker upload ----

// Binding is one Worker binding in upload metadata.
type Binding map[string]any

// UploadWorker uploads (creates or replaces) a module Worker. Secrets are
// kept across uploads with keep_bindings and managed separately.
func UploadWorker(ctx context.Context, conn *providers.Conn, acct, name, code string, meta map[string]any) (map[string]any, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mbs, _ := json.Marshal(meta)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	pw, _ := mw.CreatePart(h)
	pw.Write(mbs)
	main := fmt.Sprint(meta["main_module"])
	h2 := textproto.MIMEHeader{}
	h2.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, main, main))
	h2.Set("Content-Type", "application/javascript+module")
	fw, _ := mw.CreatePart(h2)
	fw.Write([]byte(code))
	mw.Close()
	var out map[string]any
	_, err := api(ctx, conn, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name),
		Body: buf.Bytes(), ContentType: mw.FormDataContentType(), Resource: name, Timeout: 90 * time.Second}, &out)
	return out, err
}
