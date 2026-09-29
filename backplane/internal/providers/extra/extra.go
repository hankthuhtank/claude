// Package extra holds the Phase 2 and Phase 3 providers. Each one really
// connects, verifies the credential against the provider's API, reports the
// account, and discovers existing resources for import and monitoring. They
// are marked "Connect & monitor": Backplane does not provision with them yet,
// and the UI says so.
package extra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Providers returns every secondary adapter.
func Providers() []providers.Provider {
	return []providers.Provider{vercel{}, netlify{}, neon{}, upstash{}, clerk{}, auth0{}, twilio{}, cloudinary{}, sentry{}, posthog{}, planetscale{}, firebase{}, aws{}}
}

// simple is shared plumbing for token-based adapters.
type simple struct {
	info    providers.Info
	base    string
	auth    func(c core.Connection, secret string, r *http.Request)
	baseFor func(c core.Connection) string
}

func connect(s simple, c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	base := s.base
	if s.baseFor != nil {
		base = s.baseFor(c)
	}
	cl := httpx.New(s.info.ID, conn.Base(s.info.ID, base))
	cl.Auth = func(r *http.Request) { s.auth(c, secret, r) }
	cl.Log = opts.Log
	cl.Limiter = httpx.NewLimiter(5, 5)
	cl.MaxAttempts = 3
	conn.Client = cl
	return conn, nil
}

func get(ctx context.Context, c *providers.Conn, path string, out any) (*httpx.Response, error) {
	resp, err := c.Client.Do(ctx, httpx.Request{Method: "GET", Path: path, Quiet: true})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, fmt.Errorf("unexpected response from %s: %w", c.Connection.Provider, err)
		}
	}
	return resp, nil
}

func fail(provider, action string, err error, start time.Time) *providers.VerifyResult {
	p := providers.Translate(provider, action, err)
	return &providers.VerifyResult{Health: core.HealthFail, Problem: p, Summary: p.Summary, LatencyMS: time.Since(start).Milliseconds()}
}

func ok(summary string, start time.Time, acctID, acctName string) *providers.VerifyResult {
	return &providers.VerifyResult{Health: core.HealthOK, Summary: summary, AccountID: acctID, AccountName: acctName, LatencyMS: time.Since(start).Milliseconds(),
		Details: map[string]string{}, Settings: map[string]string{}}
}

func bearer(prefix string) func(core.Connection, string, *http.Request) {
	return func(_ core.Connection, secret string, r *http.Request) { r.Header.Set("Authorization", prefix+secret) }
}

func basic(user func(core.Connection) string) func(core.Connection, string, *http.Request) {
	return func(c core.Connection, secret string, r *http.Request) {
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user(c)+":"+secret)))
	}
}

func connectMaturity(id, name, tagline, category string, phase int, caps []core.Capability, fields []providers.Field, guide []providers.GuideStep, tokenURL, docs string, discovers []string) providers.Info {
	return providers.Info{ID: id, Name: name, Tagline: tagline, Category: category, Phase: phase, Maturity: providers.MaturityConnect, Capabilities: caps,
		Fields: fields, Guide: guide, TokenURL: tokenURL, DocsURL: docs, Discovers: discovers,
		OAuthNote: "Connect with an API credential. Backplane verifies it, discovers your resources and monitors them; building with " + name + " is on the roadmap."}
}

// ================= Vercel =================

type vercel struct{}

var vercelS = simple{base: "https://api.vercel.com", auth: bearer("Bearer "), info: connectMaturity("vercel", "Vercel", "Frontend hosting, serverless functions and deployments.", "Deployment", 2,
	[]core.Capability{core.CapDeployment, core.CapFunctions, core.CapDNS, core.CapFeatureFlags},
	[]providers.Field{{Key: "token", Label: "Access token", Secret: true, Required: true}, {Key: "team_id", Label: "Team ID (optional)"}},
	[]providers.GuideStep{{Title: "Create a token", Body: "Account Settings → Tokens → Create. Scope it to the team that owns your projects.", Link: "https://vercel.com/account/settings/tokens", Label: "Open Vercel tokens"}},
	"https://vercel.com/account/settings/tokens", "https://vercel.com/docs/rest-api", []string{"Projects", "Recent deployments and their state"})}

func (vercel) Info() providers.Info { return vercelS.info }
func (vercel) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(vercelS, c, s, o)
}

func teamQ(c *providers.Conn, sep string) string {
	if t := c.Connection.Setting("team_id"); t != "" {
		return sep + "teamId=" + t
	}
	return ""
}

func (vercel) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var u struct {
		User struct {
			ID, Username, Email string
		} `json:"user"`
	}
	if _, err := get(ctx, c, "/v2/user", &u); err != nil {
		return fail("vercel", "read your account", err, start), nil
	}
	return ok("Connected as "+u.User.Username+".", start, u.User.ID, u.User.Username), nil
}

func (vercel) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var ps struct {
		Projects []struct {
			ID, Name  string
			Framework string `json:"framework"`
		} `json:"projects"`
	}
	if _, err := get(ctx, c, "/v9/projects?limit=100"+teamQ(c, "&"), &ps); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, p := range ps.Projects {
		out = append(out, providers.Discovered{Provider: "vercel", Kind: "vercel.project", ID: p.ID, Name: p.Name, Detail: p.Framework})
	}
	var ds struct {
		Deployments []struct {
			UID, Name, URL, State string
			Created               int64 `json:"created"`
		} `json:"deployments"`
	}
	if _, err := get(ctx, c, "/v6/deployments?limit=20"+teamQ(c, "&"), &ds); err == nil {
		for _, d := range ds.Deployments {
			out = append(out, providers.Discovered{Provider: "vercel", Kind: "vercel.deployment", ID: d.UID, Name: d.URL, Detail: d.Name + " · " + strings.ToLower(d.State),
				Links: []providers.DiscoveredLink{{ToKind: "vercel.project", ToName: d.Name, Label: "deployment of"}}})
		}
	}
	return out, nil
}

// ================= Netlify =================

type netlify struct{}

var netlifyS = simple{base: "https://api.netlify.com/api/v1", auth: bearer("Bearer "), info: connectMaturity("netlify", "Netlify", "Sites, deploys, functions and forms.", "Deployment", 2,
	[]core.Capability{core.CapDeployment, core.CapFunctions, core.CapDNS},
	[]providers.Field{{Key: "token", Label: "Personal access token", Secret: true, Required: true}},
	[]providers.GuideStep{{Title: "Create a token", Body: "User settings → Applications → Personal access tokens → New access token.", Link: "https://app.netlify.com/user/applications#personal-access-tokens", Label: "Open Netlify tokens"}},
	"https://app.netlify.com/user/applications#personal-access-tokens", "https://open-api.netlify.com/", []string{"Sites", "Latest deploy state", "DNS zones"})}

func (netlify) Info() providers.Info { return netlifyS.info }
func (netlify) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(netlifyS, c, s, o)
}
func (netlify) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var u struct {
		ID       string `json:"id"`
		FullName string `json:"full_name"`
		Email    string `json:"email"`
	}
	if _, err := get(ctx, c, "/user", &u); err != nil {
		return fail("netlify", "read your account", err, start), nil
	}
	return ok("Connected as "+firstNonEmpty(u.FullName, u.Email)+".", start, u.ID, firstNonEmpty(u.FullName, u.Email)), nil
}
func (netlify) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var sites []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		URL       string `json:"url"`
		Published *struct {
			State string `json:"state"`
		} `json:"published_deploy"`
	}
	if _, err := get(ctx, c, "/sites?per_page=100", &sites); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, s := range sites {
		state := "no deploys"
		if s.Published != nil {
			state = s.Published.State
		}
		out = append(out, providers.Discovered{Provider: "netlify", Kind: "netlify.site", ID: s.ID, Name: s.Name, Detail: s.URL + " · " + state})
	}
	return out, nil
}

// ================= Neon =================

type neon struct{}

var neonS = simple{base: "https://console.neon.tech/api/v2", auth: bearer("Bearer "), info: connectMaturity("neon", "Neon", "Serverless Postgres with branching.", "Database", 2,
	[]core.Capability{core.CapDatabase},
	[]providers.Field{{Key: "token", Label: "API key", Secret: true, Required: true}, {Key: "org_id", Label: "Organization ID (for organization keys)"}},
	[]providers.GuideStep{{Title: "Create an API key", Body: "Account settings → API keys → Create new API key.", Link: "https://console.neon.tech/app/settings/api-keys", Label: "Open Neon API keys"}},
	"https://console.neon.tech/app/settings/api-keys", "https://api-docs.neon.tech/reference/getting-started-with-neon-api", []string{"Projects", "Branches", "Compute endpoints and their state"})}

func (neon) Info() providers.Info { return neonS.info }
func (neon) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(neonS, c, s, o)
}
func neonProjectsPath(c *providers.Conn) string {
	if o := c.Connection.Setting("org_id"); o != "" {
		return "/projects?org_id=" + o
	}
	return "/projects"
}
func (neon) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var ps struct {
		Projects []struct{ ID, Name string } `json:"projects"`
	}
	if _, err := get(ctx, c, neonProjectsPath(c), &ps); err != nil {
		return fail("neon", "list projects", err, start), nil
	}
	var me struct {
		ID, Email, Name string
	}
	_, _ = get(ctx, c, "/users/me", &me)
	return ok(fmt.Sprintf("Connected%s. %d project(s).", prefixed(" as ", firstNonEmpty(me.Name, me.Email)), len(ps.Projects)), start, me.ID, firstNonEmpty(me.Name, me.Email)), nil
}
func (neon) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var ps struct {
		Projects []struct {
			ID, Name string
			Region   string `json:"region_id"`
		} `json:"projects"`
	}
	if _, err := get(ctx, c, neonProjectsPath(c), &ps); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, p := range ps.Projects {
		out = append(out, providers.Discovered{Provider: "neon", Kind: "neon.project", ID: p.ID, Name: p.Name, Detail: p.Region, Group: p.Name})
		var eps struct {
			Endpoints []struct {
				ID, Host     string
				CurrentState string `json:"current_state"`
				BranchID     string `json:"branch_id"`
			} `json:"endpoints"`
		}
		if _, err := get(ctx, c, "/projects/"+p.ID+"/endpoints", &eps); err == nil {
			for _, e := range eps.Endpoints {
				out = append(out, providers.Discovered{Provider: "neon", Kind: "neon.endpoint", ID: e.ID, Name: e.Host, Detail: "compute " + e.CurrentState, Group: p.Name,
					Links: []providers.DiscoveredLink{{ToKind: "neon.project", ToName: p.Name, Label: "compute for"}}})
			}
		}
	}
	return out, nil
}

// ================= Upstash =================

type upstash struct{}

var upstashS = simple{base: "https://api.upstash.com/v2", auth: basic(func(c core.Connection) string { return c.Setting("email") }),
	info: connectMaturity("upstash", "Upstash", "Serverless Redis, QStash queues, Vector and Search.", "Cache & queues", 2,
		[]core.Capability{core.CapCache, core.CapQueue, core.CapScheduler, core.CapVector},
		[]providers.Field{{Key: "email", Label: "Account email", Required: true}, {Key: "token", Label: "Management API key", Secret: true, Required: true}},
		[]providers.GuideStep{{Title: "Create a management API key", Body: "Console → Account → Management API → Create API key. Use it with your account email.", Link: "https://console.upstash.com/account/api", Label: "Open Upstash API keys"}},
		"https://console.upstash.com/account/api", "https://upstash.com/docs/devops/developer-api/introduction", []string{"Redis databases and state"})}

func (upstash) Info() providers.Info { return upstashS.info }
func (upstash) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(upstashS, c, s, o)
}

type upstashDB struct {
	ID     string `json:"database_id"`
	Name   string `json:"database_name"`
	Region string `json:"region"`
	State  string `json:"state"`
}

func (upstash) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var dbs []upstashDB
	if _, err := get(ctx, c, "/redis/databases", &dbs); err != nil {
		return fail("upstash", "list Redis databases", err, start), nil
	}
	return ok(fmt.Sprintf("Connected. %d Redis database(s).", len(dbs)), start, c.Connection.Setting("email"), c.Connection.Setting("email")), nil
}
func (upstash) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var dbs []upstashDB
	if _, err := get(ctx, c, "/redis/databases", &dbs); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, d := range dbs {
		out = append(out, providers.Discovered{Provider: "upstash", Kind: "upstash.redis", ID: d.ID, Name: d.Name, Detail: d.Region + " · " + d.State})
	}
	return out, nil
}

// ================= Clerk =================

type clerk struct{}

var clerkS = simple{base: "https://api.clerk.com/v1", auth: bearer("Bearer "), info: connectMaturity("clerk", "Clerk", "Hosted user management, organizations and sessions.", "Auth", 2,
	[]core.Capability{core.CapAuth},
	[]providers.Field{{Key: "token", Label: "Secret key", Secret: true, Required: true, Placeholder: "sk_test_… or sk_live_…"}},
	[]providers.GuideStep{{Title: "Copy the secret key", Body: "Dashboard → Configure → API keys → copy the Secret key for the instance you want to monitor.", Link: "https://dashboard.clerk.com/last-active?path=api-keys", Label: "Open Clerk API keys"}},
	"https://dashboard.clerk.com/last-active?path=api-keys", "https://clerk.com/docs/reference/backend-api", []string{"Instance (development or production)", "User and organization counts", "Domains"})}

func (clerk) Info() providers.Info { return clerkS.info }
func (clerk) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(clerkS, c, s, o)
}
func (clerk) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var inst struct {
		ID              string `json:"id"`
		EnvironmentType string `json:"environment_type"`
	}
	if _, err := get(ctx, c, "/instance", &inst); err != nil {
		return fail("clerk", "read the instance", err, start), nil
	}
	var cnt struct {
		TotalCount int `json:"total_count"`
	}
	_, _ = get(ctx, c, "/users/count", &cnt)
	r := ok(fmt.Sprintf("Connected to a %s instance with %d user(s).", inst.EnvironmentType, cnt.TotalCount), start, inst.ID, inst.EnvironmentType+" instance")
	r.Mode = inst.EnvironmentType
	return r, nil
}
func (clerk) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var out []providers.Discovered
	var cnt struct {
		TotalCount int `json:"total_count"`
	}
	if _, err := get(ctx, c, "/users/count", &cnt); err != nil {
		return nil, err
	}
	out = append(out, providers.Discovered{Provider: "clerk", Kind: "clerk.users", ID: "users", Name: fmt.Sprintf("%d users", cnt.TotalCount)})
	var domains struct {
		Data []struct {
			ID, Name string
		} `json:"data"`
	}
	if _, err := get(ctx, c, "/domains", &domains); err == nil {
		for _, d := range domains.Data {
			out = append(out, providers.Discovered{Provider: "clerk", Kind: "clerk.domain", ID: d.ID, Name: d.Name})
		}
	}
	return out, nil
}

// ================= Twilio =================

type twilio struct{}

var twilioS = simple{base: "https://api.twilio.com", auth: basic(func(c core.Connection) string {
	if k := c.Setting("api_key_sid"); k != "" {
		return k
	}
	return c.Setting("account_sid")
}), info: connectMaturity("twilio", "Twilio", "SMS, voice, WhatsApp and Verify.", "Messaging", 2,
	[]core.Capability{core.CapSMS},
	[]providers.Field{{Key: "account_sid", Label: "Account SID", Required: true, Placeholder: "AC…"}, {Key: "api_key_sid", Label: "API key SID (recommended, SK…)"},
		{Key: "token", Label: "Auth token or API key secret", Secret: true, Required: true}},
	[]providers.GuideStep{{Title: "Create an API key", Body: "Console → Account → API keys & tokens → Create API key (Standard). Paste the SID and secret; the Account SID is on the console home page.", Link: "https://console.twilio.com/us1/account/keys-credentials/api-keys", Label: "Open Twilio API keys"}},
	"https://console.twilio.com/us1/account/keys-credentials/api-keys", "https://www.twilio.com/docs/usage/api", []string{"Account status and balance", "Phone numbers", "Verify services"})}

func (twilio) Info() providers.Info { return twilioS.info }
func (twilio) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(twilioS, c, s, o)
}
func (twilio) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	sid := c.Connection.Setting("account_sid")
	var a struct {
		FriendlyName string `json:"friendly_name"`
		Status       string `json:"status"`
		Type         string `json:"type"`
	}
	if _, err := get(ctx, c, "/2010-04-01/Accounts/"+sid+".json", &a); err != nil {
		return fail("twilio", "read the account", err, start), nil
	}
	r := ok("Connected to "+a.FriendlyName+" ("+a.Type+", "+a.Status+").", start, sid, a.FriendlyName)
	if a.Status != "active" {
		r.Health, r.Summary = core.HealthFail, "The Twilio account is "+a.Status+"."
	}
	var bal struct {
		Balance  string `json:"balance"`
		Currency string `json:"currency"`
	}
	if _, err := get(ctx, c, "/2010-04-01/Accounts/"+sid+"/Balance.json", &bal); err == nil {
		r.Details["balance"] = bal.Balance + " " + bal.Currency
		if strings.HasPrefix(bal.Balance, "-") || bal.Balance == "0.00" {
			r.Health = core.Worst(r.Health, core.HealthWarn)
			r.Warnings = append(r.Warnings, "Your Twilio balance is "+bal.Balance+" "+bal.Currency+"; messages stop when it runs out.")
		}
	}
	if a.Type == "Trial" {
		r.Warnings = append(r.Warnings, "Trial accounts can only text verified numbers.")
	}
	return r, nil
}
func (twilio) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	sid := c.Connection.Setting("account_sid")
	var nums struct {
		Numbers []struct {
			SID          string `json:"sid"`
			PhoneNumber  string `json:"phone_number"`
			FriendlyName string `json:"friendly_name"`
		} `json:"incoming_phone_numbers"`
	}
	if _, err := get(ctx, c, "/2010-04-01/Accounts/"+sid+"/IncomingPhoneNumbers.json?PageSize=100", &nums); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, n := range nums.Numbers {
		out = append(out, providers.Discovered{Provider: "twilio", Kind: "twilio.number", ID: n.SID, Name: n.PhoneNumber, Detail: n.FriendlyName})
	}
	return out, nil
}

// ================= Cloudinary =================

type cloudinary struct{}

var cloudinaryS = simple{auth: basic(func(c core.Connection) string { return c.Setting("api_key") }),
	baseFor: func(c core.Connection) string { return "https://api.cloudinary.com/v1_1/" + c.Setting("cloud_name") },
	info: connectMaturity("cloudinary", "Cloudinary", "Image and video upload, transformation and delivery.", "Media", 2,
		[]core.Capability{core.CapMedia, core.CapStorage},
		[]providers.Field{{Key: "cloud_name", Label: "Cloud name", Required: true}, {Key: "api_key", Label: "API key", Required: true}, {Key: "token", Label: "API secret", Secret: true, Required: true}},
		[]providers.GuideStep{{Title: "Copy your API credentials", Body: "Console → Settings → API Keys. Copy the cloud name, API key and API secret.", Link: "https://console.cloudinary.com/settings/api-keys", Label: "Open Cloudinary API keys"}},
		"https://console.cloudinary.com/settings/api-keys", "https://cloudinary.com/documentation/admin_api", []string{"Plan and credit usage", "Upload presets"})}

func (cloudinary) Info() providers.Info { return cloudinaryS.info }
func (cloudinary) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(cloudinaryS, c, s, o)
}
func (cloudinary) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var u struct {
		Plan    string `json:"plan"`
		Credits struct {
			UsedPercent float64 `json:"used_percent"`
		} `json:"credits"`
	}
	if _, err := get(ctx, c, "/usage", &u); err != nil {
		return fail("cloudinary", "read usage", err, start), nil
	}
	r := ok(fmt.Sprintf("Connected (%s plan, %.0f%% of credits used).", u.Plan, u.Credits.UsedPercent), start, c.Connection.Setting("cloud_name"), c.Connection.Setting("cloud_name"))
	if u.Credits.UsedPercent >= 80 {
		r.Health = core.HealthWarn
		r.Warnings = append(r.Warnings, fmt.Sprintf("%.0f%% of this month's Cloudinary credits are used.", u.Credits.UsedPercent))
	}
	return r, nil
}
func (cloudinary) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var presets struct {
		Presets []struct {
			Name string `json:"name"`
		} `json:"presets"`
	}
	if _, err := get(ctx, c, "/upload_presets", &presets); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, p := range presets.Presets {
		out = append(out, providers.Discovered{Provider: "cloudinary", Kind: "cloudinary.preset", ID: p.Name, Name: p.Name, Detail: "upload preset"})
	}
	return out, nil
}

// ================= Sentry =================

type sentry struct{}

var sentryS = simple{auth: bearer("Bearer "), baseFor: func(c core.Connection) string {
	if b := c.Setting("base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://sentry.io"
}, info: connectMaturity("sentry", "Sentry", "Error monitoring, releases and alerts.", "Monitoring", 2,
	[]core.Capability{core.CapMonitoring},
	[]providers.Field{{Key: "token", Label: "Auth token", Secret: true, Required: true}, {Key: "base_url", Label: "Region URL", Help: "https://sentry.io (US) or https://de.sentry.io (EU)"}},
	[]providers.GuideStep{{Title: "Create an auth token", Body: "Settings → Account → Personal Tokens → Create New Token with project:read and org:read.", Link: "https://sentry.io/settings/account/api/auth-tokens/", Label: "Open Sentry tokens"}},
	"https://sentry.io/settings/account/api/auth-tokens/", "https://docs.sentry.io/api/", []string{"Organizations and projects", "Unresolved issues in the last 24 hours"})}

func (sentry) Info() providers.Info { return sentryS.info }
func (sentry) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(sentryS, c, s, o)
}
func (sentry) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var orgs []struct{ Slug, Name string }
	if _, err := get(ctx, c, "/api/0/organizations/", &orgs); err != nil {
		return fail("sentry", "list organizations", err, start), nil
	}
	name := ""
	if len(orgs) > 0 {
		name = orgs[0].Name
	}
	return ok(fmt.Sprintf("Connected. %d organization(s).", len(orgs)), start, name, name), nil
}
func (sentry) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var orgs []struct{ Slug, Name string }
	if _, err := get(ctx, c, "/api/0/organizations/", &orgs); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, o := range orgs {
		var projects []struct{ Slug, Name, Platform string }
		if _, err := get(ctx, c, "/api/0/organizations/"+o.Slug+"/projects/", &projects); err != nil {
			continue
		}
		for _, p := range projects {
			d := providers.Discovered{Provider: "sentry", Kind: "sentry.project", ID: o.Slug + "/" + p.Slug, Name: p.Name, Detail: p.Platform, Group: o.Name}
			var issues []struct{ ID string }
			if _, err := get(ctx, c, "/api/0/projects/"+o.Slug+"/"+p.Slug+"/issues/?query=is:unresolved&statsPeriod=24h&limit=100", &issues); err == nil {
				d.Detail += fmt.Sprintf(" · %d unresolved issue(s) in 24h", len(issues))
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// ================= PostHog =================

type posthog struct{}

var posthogS = simple{auth: bearer("Bearer "), baseFor: func(c core.Connection) string {
	if b := c.Setting("host"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://us.posthog.com"
}, info: connectMaturity("posthog", "PostHog", "Product analytics, feature flags, experiments and session replay.", "Analytics", 2,
	[]core.Capability{core.CapAnalytics, core.CapFeatureFlags, core.CapMonitoring},
	[]providers.Field{{Key: "token", Label: "Personal API key", Secret: true, Required: true, Placeholder: "phx_…"}, {Key: "host", Label: "Host", Help: "https://us.posthog.com or https://eu.posthog.com"}},
	[]providers.GuideStep{{Title: "Create a personal API key", Body: "Settings → Personal API keys → Create personal API key with read access.", Link: "https://us.posthog.com/settings/user-api-keys", Label: "Open PostHog API keys"}},
	"https://us.posthog.com/settings/user-api-keys", "https://posthog.com/docs/api", []string{"Projects", "Feature flags"})}

func (posthog) Info() providers.Info { return posthogS.info }
func (posthog) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(posthogS, c, s, o)
}
func (posthog) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var me struct {
		Email        string `json:"email"`
		Organization struct {
			Name string `json:"name"`
		} `json:"organization"`
	}
	if _, err := get(ctx, c, "/api/users/@me/", &me); err != nil {
		return fail("posthog", "read your account", err, start), nil
	}
	return ok("Connected to "+me.Organization.Name+" as "+me.Email+".", start, me.Organization.Name, me.Organization.Name), nil
}
func (posthog) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var projects struct {
		Results []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if _, err := get(ctx, c, "/api/projects/", &projects); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, p := range projects.Results {
		out = append(out, providers.Discovered{Provider: "posthog", Kind: "posthog.project", ID: fmt.Sprint(p.ID), Name: p.Name, Group: p.Name})
		var flags struct {
			Results []struct {
				Key    string `json:"key"`
				Active bool   `json:"active"`
			} `json:"results"`
		}
		if _, err := get(ctx, c, fmt.Sprintf("/api/projects/%d/feature_flags/?limit=100", p.ID), &flags); err == nil {
			for _, f := range flags.Results {
				state := "off"
				if f.Active {
					state = "on"
				}
				out = append(out, providers.Discovered{Provider: "posthog", Kind: "posthog.flag", ID: fmt.Sprint(p.ID) + "/" + f.Key, Name: f.Key, Detail: "feature flag · " + state, Group: p.Name})
			}
		}
	}
	return out, nil
}

// ================= PlanetScale =================

type planetscale struct{}

var planetscaleS = simple{base: "https://api.planetscale.com/v1", auth: func(c core.Connection, secret string, r *http.Request) {
	r.Header.Set("Authorization", c.Setting("token_id")+":"+secret)
}, info: connectMaturity("planetscale", "PlanetScale", "Managed MySQL and Postgres with branching and deploy requests.", "Database", 2,
	[]core.Capability{core.CapDatabase},
	[]providers.Field{{Key: "token_id", Label: "Service token ID", Required: true}, {Key: "token", Label: "Service token", Secret: true, Required: true}, {Key: "organization", Label: "Organization", Required: true}},
	[]providers.GuideStep{{Title: "Create a service token", Body: "Organization settings → Service tokens → New token, then grant it read_databases and read_branches.", Link: "https://app.planetscale.com/", Label: "Open PlanetScale"}},
	"https://app.planetscale.com/", "https://api-docs.planetscale.com/", []string{"Databases", "Branches"})}

func (planetscale) Info() providers.Info { return planetscaleS.info }
func (planetscale) Connect(c core.Connection, s string, o providers.ConnectOptions) (*providers.Conn, error) {
	return connect(planetscaleS, c, s, o)
}
func (planetscale) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	org := c.Connection.Setting("organization")
	var dbs struct {
		Data []struct{ Name string } `json:"data"`
	}
	if _, err := get(ctx, c, "/organizations/"+org+"/databases", &dbs); err != nil {
		return fail("planetscale", "list databases", err, start), nil
	}
	return ok(fmt.Sprintf("Connected to %s. %d database(s).", org, len(dbs.Data)), start, org, org), nil
}
func (planetscale) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	org := c.Connection.Setting("organization")
	var dbs struct {
		Data []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"data"`
	}
	if _, err := get(ctx, c, "/organizations/"+org+"/databases", &dbs); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for _, d := range dbs.Data {
		out = append(out, providers.Discovered{Provider: "planetscale", Kind: "planetscale.database", ID: d.Name, Name: d.Name, Detail: d.State})
	}
	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func prefixed(p, v string) string {
	if v == "" {
		return ""
	}
	return p + v
}
