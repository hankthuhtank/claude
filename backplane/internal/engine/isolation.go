package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/supabase"
)

func init() {
	RegisterScenario(Scenario{ID: "data_isolation", Title: "Each user sees only their own data", Run: scenarioIsolation})
	RegisterLinkCheck("checkout_data", checkDataCheckout)
}

// scenarioIsolation proves row-level security end to end: two temporary
// users, A writes a row, A can read it, B and an anonymous visitor cannot.
// Both users (and, through ON DELETE CASCADE, their rows) are removed.
func scenarioIsolation(c *Checker, sc *core.ScenarioSpec) core.CheckResult {
	res := core.CheckResult{}
	table, column := c.P.Blueprint.Param("isolation_table"), c.P.Blueprint.Param("isolation_column")
	conn := c.Conn("supabase")
	proj := c.resourceOf(supabase.KindProject)
	if table == "" || conn == nil || proj == nil || c.State(proj.Key) == nil {
		res.Health, res.Summary = core.HealthUnknown, "Build the backend first."
		return res
	}
	ref := c.State(proj.Key).ID
	admin, _, prob := c.dbClient()
	if prob != nil {
		res.Health, res.Summary = core.HealthUnknown, prob.Title
		return res
	}
	keys, err := supabase.ListKeys(c.ctx, conn, ref, true)
	if err != nil {
		res.Health, res.Problem = core.HealthFail, providers.Translate("supabase", "read the project's public key", err)
		res.Summary = res.Problem.Summary
		return res
	}
	public := ""
	for _, k := range keys {
		if k.Type == "publishable" || k.Name == "anon" {
			public = k.APIKey
			if k.Type == "publishable" {
				break
			}
		}
	}
	if public == "" {
		res.Health, res.Summary = core.HealthFail, "The project has no publishable (anon) key."
		return res
	}
	var steps []core.StepResult
	fail := func(title, detail string, prob *core.Problem) core.CheckResult {
		steps = append(steps, step(title, core.HealthFail, detail, 0))
		res.Steps, res.Health, res.Summary = steps, core.HealthFail, title+": "+detail
		if prob == nil {
			prob = &core.Problem{Title: "DATA ISOLATION FAILED", Provider: "supabase", Code: "drift", Summary: title + ": " + detail}
		}
		res.Problem = prob
		return res
	}
	userClient := func(token string) *httpx.Client {
		cl := httpx.New("supabase", supabase.ProjectURL(conn, ref))
		cl.MaxAttempts = 2
		cl.Log = c.e.httpLogger(core.LogEntry{Project: c.P.ID, Environment: c.Env, RunID: c.runID})
		cl.Auth = func(r *http.Request) {
			r.Header.Set("apikey", public)
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			} else if strings.HasPrefix(public, "eyJ") {
				r.Header.Set("Authorization", "Bearer "+public)
			}
		}
		return cl
	}
	start := time.Now()
	type user struct{ id, email, token string }
	var users [2]user
	for i := range users {
		email := fmt.Sprintf("bp-iso-%s-%d@example.com", randomID(), i)
		pass := "Bp!" + randomID() + randomID()
		resp, err := admin.Do(c.ctx, httpx.Request{Method: "POST", Path: "/auth/v1/admin/users", JSON: map[string]any{"email": email, "password": pass, "email_confirm": true}})
		if err != nil {
			return fail("Create two temporary users", err.Error(), providers.Translate("supabase", "create a temporary user", err))
		}
		var u struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(resp.Body, &u)
		users[i] = user{id: u.ID, email: email}
		uid := u.ID
		c.Defer(func(ctx contextLike) error {
			_, err := admin.Do(ctx, httpx.Request{Method: "DELETE", Path: "/auth/v1/admin/users/" + uid})
			return err
		})
		tr, err := userClient("").Do(c.ctx, httpx.Request{Method: "POST", Path: "/auth/v1/token", Query: url.Values{"grant_type": {"password"}}, JSON: map[string]any{"email": email, "password": pass}})
		if err != nil {
			return fail("Create two temporary users", "sign-in failed: "+err.Error(), nil)
		}
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(tr.Body, &tok)
		users[i].token = tok.AccessToken
	}
	steps = append(steps, step("Create two temporary users", core.HealthOK, "both signed in", time.Since(start).Milliseconds()))

	start = time.Now()
	row := map[string]any{}
	if column != "" {
		row[column] = "backplane isolation probe"
	}
	resp, err := userClient(users[0].token).Do(c.ctx, httpx.Request{Method: "POST", Path: "/rest/v1/" + table, JSON: row, Header: http.Header{"Prefer": {"return=representation"}}})
	if err != nil {
		return fail("User A saves a record", err.Error(), &core.Problem{Title: "USERS CANNOT SAVE DATA", Provider: "supabase", Code: "drift",
			Summary: "A signed-in user could not insert into " + table + ". The insert policy may have been removed: " + err.Error()})
	}
	var created []map[string]any
	_ = json.Unmarshal(resp.Body, &created)
	if len(created) != 1 {
		return fail("User A saves a record", "no row returned", nil)
	}
	id := fmt.Sprint(created[0]["id"])
	steps = append(steps, step("User A saves a record", core.HealthOK, table+" row "+id, time.Since(start).Milliseconds()))

	read := func(token string) (int, error) {
		r, err := userClient(token).Do(c.ctx, httpx.Request{Method: "GET", Path: "/rest/v1/" + table, Query: url.Values{"id": {"eq." + id}, "select": {"id"}}})
		if err != nil {
			if e, ok := httpx.AsError(err); ok && (e.Status == 401 || e.Status == 403) {
				return 0, nil
			}
			return -1, err
		}
		var rows []map[string]any
		_ = json.Unmarshal(r.Body, &rows)
		return len(rows), nil
	}
	start = time.Now()
	if n, err := read(users[0].token); err != nil || n != 1 {
		return fail("User A reads it back", fmt.Sprintf("expected 1 row, got %d %v", n, err), nil)
	}
	steps = append(steps, step("User A reads it back", core.HealthOK, "", time.Since(start).Milliseconds()))
	start = time.Now()
	if n, err := read(users[1].token); err != nil || n != 0 {
		return fail("User B cannot see it", fmt.Sprintf("user B could read %d row(s) belonging to user A", n), &core.Problem{Title: "PRIVATE DATA IS VISIBLE TO OTHER USERS", Provider: "supabase", Code: "drift",
			Summary: "A second signed-in user could read a row that belongs to someone else in " + table + ". Row-level security was disabled or a policy was loosened.",
			Fixes:   []core.Fix{{ID: "reapply:schema", Label: "Restore the generated security policies", Automatic: true, Action: "repair", Target: "schema", Changes: []string{"Re-apply row-level security and policies on " + table}}}})
	}
	steps = append(steps, step("User B cannot see it", core.HealthOK, "0 rows", time.Since(start).Milliseconds()))
	start = time.Now()
	if n, err := read(""); err != nil || n != 0 {
		return fail("A visitor without an account cannot see it", fmt.Sprintf("an anonymous request returned %d row(s)", n), &core.Problem{Title: "PRIVATE DATA IS PUBLIC", Provider: "supabase", Code: "drift",
			Summary: "Anyone with your public key can read " + table + ". Restore row-level security immediately.", Fixes: []core.Fix{{ID: "reapply:schema", Label: "Restore security policies", Automatic: true, Action: "repair", Target: "schema"}}})
	}
	steps = append(steps, step("A visitor without an account cannot see it", core.HealthOK, "0 rows", time.Since(start).Milliseconds()))
	for _, u := range users {
		if u.id != "" {
			_, _ = admin.Do(c.ctx, httpx.Request{Method: "DELETE", Path: "/auth/v1/admin/users/" + u.id})
		}
	}
	steps = append(steps, step("Temporary users removed", core.HealthOK, "rows removed with them", 0))
	res.Steps, res.Health = steps, core.HealthOK
	res.Summary = "Row-level security holds: each user reaches only their own " + table + "."
	return res
}

// checkDataCheckout proves the app can reach the Worker's billing endpoint.
func checkDataCheckout(c *Checker, l *core.LinkSpec) core.CheckResult {
	url, _ := c.WorkerURL()
	if url == "" {
		return core.CheckResult{Health: core.HealthUnknown, Summary: "Not built yet."}
	}
	_, err := c.workerCall("POST", "/billing/checkout", []byte(`{}`), nil, true)
	if e, ok := httpx.AsError(err); ok && e.Status == 401 {
		return core.CheckResult{Health: core.HealthOK, Summary: "Billing endpoint answers and requires a signed-in user."}
	}
	if err == nil {
		return core.CheckResult{Health: core.HealthFail, Summary: "Billing checkout answered without a signed-in user.",
			Problem: &core.Problem{Title: "BILLING ENDPOINT UNPROTECTED", Code: "drift", Summary: "/billing/checkout should require a signed-in user. Rebuild the Worker."}}
	}
	return core.CheckResult{Health: core.HealthFail, Summary: "Billing endpoint unreachable: " + err.Error(),
		Problem: &core.Problem{Title: "BILLING UNREACHABLE", Provider: "cloudflare", Code: "network", Summary: err.Error()}}
}
