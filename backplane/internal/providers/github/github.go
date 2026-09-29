// Package github implements the GitHub adapter: repositories, commits of
// generated code to the default branch (never a new branch unless asked),
// encrypted Actions secrets, Actions variables, environments, workflow runs
// and releases.
//
// Generated files are committed only when unchanged by the user: before
// writing, Backplane compares the repository's blob SHA for each path with
// the blob SHA of what Backplane last wrote. A mismatch means a person edited
// the file, and it is skipped and reported instead of overwritten.
package github

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// APIBase is the GitHub REST API.
const APIBase = "https://api.github.com"

// APIVersion is sent on every request.
const APIVersion = "2022-11-28"

// Resource kinds.
const (
	KindRepo        = "github.repo"
	KindFiles       = "github.files"
	KindSecret      = "github.actions_secret"
	KindVariable    = "github.actions_variable"
	KindEnvironment = "github.environment"
)

// TokenTemplate pre-fills GitHub's fine-grained token form (documented
// template-URL parameters) with the permissions Backplane uses.
var TokenTemplate = "https://github.com/settings/personal-access-tokens/new?name=Backplane&description=" +
	url.QueryEscape("Backplane: export backend code, set Actions secrets and variables, trigger deploys.") +
	"&expires_in=90&administration=write&contents=write&secrets=write&actions_variables=write&environments=write&actions=write&workflows=write&metadata=read"

// Provider is the GitHub adapter.
type Provider struct{}

// Info describes GitHub.
func (Provider) Info() providers.Info {
	return providers.Info{
		ID: "github", Name: "GitHub", Category: "Code & CI/CD", Phase: 1, Maturity: providers.MaturityBuild,
		Tagline:      "Keeps your backend's code, secrets for CI, and automatic deploys.",
		Capabilities: []core.Capability{core.CapCICD, core.CapDeployment, core.CapSecrets},
		Fields: []providers.Field{
			{Key: "token", Label: "Fine-grained personal access token", Secret: true, Required: true, Placeholder: "github_pat_…", Pattern: `^(github_pat_|ghp_|gho_)[A-Za-z0-9_]{20,}$`,
				Help: "Use the pre-filled link. Choose “All repositories” (needed to create the backend repo) or add the repo later."},
			{Key: "owner", Label: "Owner (optional)", Help: "Leave blank for your personal account, or enter an organization."},
		},
		Guide: []providers.GuideStep{
			{Title: "Open the pre-filled token form", Body: "This link opens GitHub's fine-grained token page with Contents, Administration, Secrets, Variables, Environments, Actions and Workflows already set.", Link: TokenTemplate, Label: "Create token on GitHub"},
			{Title: "Repository access", Body: "Pick “All repositories” so Backplane can create your backend repository, or “Only select repositories” if the repo already exists."},
			{Title: "Generate and paste", Body: "GitHub shows the token once. Backplane stores it encrypted and warns you two weeks before it expires."},
			{Title: "What Backplane does with it", Body: "Commits generated code to main (never a new branch unless you ask), sets encrypted Actions secrets for automatic deploys, and reads workflow results for the health check."},
		},
		TokenURL: TokenTemplate, DocsURL: "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens", StatusURL: "https://www.githubstatus.com",
		OAuth: false, OAuthNote: "GitHub's device flow works for desktop apps once a Backplane OAuth app client ID is configured in Settings → Advanced.",
		Scopes:     []string{"Contents: Write", "Administration: Write", "Secrets: Write", "Variables: Write", "Environments: Write", "Actions: Write", "Workflows: Write", "Metadata: Read"},
		APIVersion: APIVersion,
		Kinds: []providers.KindInfo{
			{Kind: KindRepo, Label: "Repository", Capability: core.CapCICD, Destructive: "Backplane never deletes repositories."},
			{Kind: KindFiles, Label: "Generated code commit", Capability: core.CapCICD},
			{Kind: KindSecret, Label: "Actions secret", Capability: core.CapSecrets},
			{Kind: KindVariable, Label: "Actions variable", Capability: core.CapCICD},
			{Kind: KindEnvironment, Label: "Deployment environment", Capability: core.CapDeployment},
		},
		Discovers: []string{"Repositories you own", "Actions secrets (names)", "Workflows and their last run"},
	}
}

// Connect builds the client.
func (Provider) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("github", conn.Base("github", APIBase))
	cl.Auth = func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Accept", "application/vnd.github+json")
		r.Header.Set("X-GitHub-Api-Version", APIVersion)
	}
	cl.ParseError = parseError
	cl.Classify = classify
	cl.Limiter = httpx.NewLimiter(10, 10)
	cl.Log = opts.Log
	conn.Client = cl
	return conn, nil
}

func parseError(status int, body []byte) (string, string) {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	msg := e.Message
	code := ""
	for _, x := range e.Errors {
		if x.Message != "" {
			msg += " — " + x.Message
		}
		if x.Code != "" {
			code = x.Code
		}
	}
	return code, msg
}

func classify(status int, code, msg string) string {
	lm := strings.ToLower(msg)
	switch {
	case status == 403 && (strings.Contains(lm, "rate limit") || strings.Contains(lm, "secondary rate")):
		return httpx.KindRateLimit
	case status == 403 && strings.Contains(lm, "resource not accessible"):
		return httpx.KindPermission
	case status == 422 && (code == "already_exists" || strings.Contains(lm, "already exists")):
		return httpx.KindConflict
	case status == 409 && strings.Contains(lm, "empty"):
		return httpx.KindNotFound // empty repository has no refs yet
	}
	return ""
}

// Call performs a request and decodes JSON.
func Call(ctx context.Context, c *providers.Conn, method, path string, body any, out any) (*httpx.Response, error) {
	rq := httpx.Request{Method: method, Path: path}
	if body != nil {
		rq.JSON = body
	}
	resp, err := c.Client.Do(ctx, rq)
	if err != nil {
		return nil, err
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// Verify implements Level 1.
func (Provider) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	res := &providers.VerifyResult{Details: map[string]string{}, Settings: map[string]string{}}
	var user struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		ID    int64  `json:"id"`
	}
	resp, err := Call(ctx, c, "GET", "/user", nil, &user)
	if err != nil {
		res.Health = core.HealthFail
		res.Problem = providers.Translate("github", "read your account", err)
		res.Summary = res.Problem.Summary
		return res, nil
	}
	res.AccountID = user.Login
	res.AccountName = user.Login
	res.Settings["login"] = user.Login
	if owner := c.Connection.Setting("owner"); owner != "" {
		res.Details["owner"] = owner
	}
	if scopes := resp.Header.Get("X-OAuth-Scopes"); scopes != "" {
		res.Details["token_type"] = "classic"
		res.Scopes = strings.Split(strings.ReplaceAll(scopes, " ", ""), ",")
		if !strings.Contains(scopes, "repo") {
			res.Missing = append(res.Missing, "repo")
		}
		if !strings.Contains(scopes, "workflow") {
			res.Missing = append(res.Missing, "workflow")
		}
	} else {
		res.Details["token_type"] = "fine-grained"
		res.Details["permissions"] = "checked on first use (GitHub does not list fine-grained permissions)"
	}
	if exp := resp.Header.Get("GitHub-Authentication-Token-Expiration"); exp != "" {
		res.Details["expires"] = exp
		if t, err := time.Parse("2006-01-02 15:04:05 MST", exp); err == nil && time.Until(t) < 14*24*time.Hour {
			res.Warnings = append(res.Warnings, "This token expires "+t.Format("Jan 2")+". Create a new one before then so deploys keep working.")
		}
	}
	var rl struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if _, err := Call(ctx, c, "GET", "/rate_limit", nil, &rl); err == nil {
		res.Details["rate_limit"] = fmt.Sprintf("%d of %d left this hour", rl.Resources.Core.Remaining, rl.Resources.Core.Limit)
		if rl.Resources.Core.Remaining < 100 {
			res.Warnings = append(res.Warnings, "Only "+strconv.Itoa(rl.Resources.Core.Remaining)+" GitHub API calls left this hour.")
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case len(res.Missing) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Signed in as " + user.Login + ", but the token lacks: " + strings.Join(res.Missing, ", ") + "."
	case len(res.Warnings) > 0:
		res.Health = core.HealthWarn
		res.Summary = "Signed in as " + user.Login + " with a note."
	default:
		res.Health = core.HealthOK
		res.Summary = "Signed in as " + user.Login + "."
	}
	return res, nil
}

// BlobSHA returns git's blob id for content (sha1 of "blob <len>\0<content>").
func BlobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// SealSecret encrypts a value for GitHub Actions with the repository public
// key (libsodium sealed box, as GitHub requires).
func SealSecret(publicKeyB64, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("github returned an invalid public key")
	}
	var pk [32]byte
	copy(pk[:], raw)
	out, err := box.SealAnonymous(nil, []byte(value), &pk, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// LatestRun returns the most recent workflow run on a branch.
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	HeadSHA    string `json:"head_sha"`
	CreatedAt  string `json:"created_at"`
	Event      string `json:"event"`
}

// LatestRun reads the latest workflow run for a repository branch.
func LatestRun(ctx context.Context, c *providers.Conn, repo, branch string) (*Run, error) {
	var out struct {
		WorkflowRuns []Run `json:"workflow_runs"`
	}
	q := "?per_page=5"
	if branch != "" {
		q += "&branch=" + url.QueryEscape(branch)
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+repo+"/actions/runs"+q, nil, &out); err != nil {
		return nil, err
	}
	if len(out.WorkflowRuns) == 0 {
		return nil, nil
	}
	return &out.WorkflowRuns[0], nil
}

// Dispatch triggers a workflow_dispatch run.
func Dispatch(ctx context.Context, c *providers.Conn, repo, workflow, ref string) error {
	_, err := Call(ctx, c, "POST", "/repos/"+repo+"/actions/workflows/"+url.PathEscape(workflow)+"/dispatches", map[string]any{"ref": ref}, nil)
	return err
}

// Discover lists the user's repositories and their Actions state.
func (Provider) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var repos []struct {
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		PushedAt      string `json:"pushed_at"`
	}
	if _, err := Call(ctx, c, "GET", "/user/repos?per_page=100&sort=pushed&affiliation=owner,organization_member", nil, &repos); err != nil {
		return nil, err
	}
	var out []providers.Discovered
	for i, r := range repos {
		vis := "public"
		if r.Private {
			vis = "private"
		}
		d := providers.Discovered{Provider: "github", Kind: KindRepo, ID: r.FullName, Name: r.FullName, Detail: vis + " · " + r.DefaultBranch}
		if i < 15 {
			var secrets struct {
				Secrets []struct {
					Name string `json:"name"`
				} `json:"secrets"`
			}
			if _, err := Call(ctx, c, "GET", "/repos/"+r.FullName+"/actions/secrets?per_page=100", nil, &secrets); err == nil && len(secrets.Secrets) > 0 {
				var names []string
				for _, s := range secrets.Secrets {
					names = append(names, s.Name)
					if strings.HasPrefix(s.Name, "CLOUDFLARE_") {
						d.Links = appendOnce(d.Links, providers.DiscoveredLink{ToKind: "cloudflare.worker", ToName: "", Label: "deploys to Cloudflare"})
					}
				}
				sort.Strings(names)
				d.Props = map[string]string{"secrets": strings.Join(names, ", ")}
			}
			if run, err := LatestRun(ctx, c, r.FullName, r.DefaultBranch); err == nil && run != nil {
				d.Detail += " · last run " + firstNonEmpty(run.Conclusion, run.Status)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func appendOnce(ls []providers.DiscoveredLink, l providers.DiscoveredLink) []providers.DiscoveredLink {
	for _, x := range ls {
		if x == l {
			return ls
		}
	}
	return append(ls, l)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
