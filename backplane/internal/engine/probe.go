package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// ProbeItem is one dependency check reported by a generated Worker.
type ProbeItem struct {
	OK      bool   `json:"ok"`
	MS      int64  `json:"ms"`
	Detail  string `json:"detail"`
	Error   string `json:"error"`
	Skipped bool   `json:"skipped"`
	Warn    bool   `json:"warn"` // works, but needs attention (for example no product file yet)
	EmailID string `json:"emailId,omitempty"`
}

// ProbeHealth is the Worker's /__backplane/health response. Generated Workers
// test their own bindings and credentials from inside Cloudflare, which is the
// only way to prove "Worker → Supabase" rather than "Backplane → Supabase".
type ProbeHealth struct {
	OK      bool                 `json:"ok"`
	Worker  string               `json:"worker"`
	Version string               `json:"version"`
	Checks  map[string]ProbeItem `json:"checks"`
	Missing []string             `json:"missing"`
	Time    string               `json:"time"`
	status  int
	latency time.Duration
	headers http.Header
}

// WorkerURL returns the generated Worker's public URL.
func (c *Checker) WorkerURL() (string, string) {
	for _, r := range c.P.Blueprint.Resources {
		if r.Kind == "cloudflare.worker" {
			if st := c.Man.Resources[r.Key]; st != nil {
				return st.Output("url"), r.Key
			}
		}
	}
	return "", ""
}

// ProbeClient is an unauthenticated client for the Worker (the probe token
// is sent explicitly where needed).
func (c *Checker) ProbeClient() *httpx.Client {
	cl := httpx.New("worker", "")
	cl.MaxAttempts = 2
	cl.Timeout = 30 * time.Second
	cl.Log = c.e.httpLogger(core.LogEntry{Project: c.P.ID, Environment: c.Env, RunID: c.runID})
	return cl
}

// WorkerHealth calls the Worker's probe endpoint once per check run.
func (c *Checker) WorkerHealth() (*ProbeHealth, *core.Problem) {
	c.mu.Lock()
	cached := c.health
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	url, key := c.WorkerURL()
	if url == "" {
		return nil, &core.Problem{Title: "Worker has no public address", Provider: "cloudflare", Code: "not_found", Summary: "Build the backend first."}
	}
	token, err := c.Gen("probe_token")
	if err != nil {
		return nil, &core.Problem{Title: "Probe token missing", Code: "auth", Summary: "The Worker's health-check token is missing from the vault. Rebuild the Worker to issue a new one."}
	}
	mode := "quick"
	if c.Full() {
		mode = "full"
	}
	start := time.Now()
	resp, err := c.ProbeClient().Do(c.ctx, httpx.Request{Method: "GET", Path: url + "/__backplane/health?mode=" + mode,
		Header: http.Header{"Authorization": {"Bearer " + token}}, Resource: key})
	if err != nil {
		e, _ := httpx.AsError(err)
		prob := &core.Problem{Title: "WORKER NOT RESPONDING", Provider: "cloudflare", Code: "network",
			Summary: "Backplane could not get a healthy answer from the Worker at " + url + "."}
		if e != nil {
			prob.HTTPStatus = e.Status
			prob.Technical = e.Error()
			switch {
			case e.Status == 401:
				prob.Title = "WORKER REJECTED THE HEALTH TOKEN"
				prob.Summary = "The Worker answered but did not accept Backplane's probe token. Its BACKPLANE_PROBE_TOKEN secret was changed or removed."
				prob.Code = "drift"
				prob.Fixes = []core.Fix{{ID: "reapply:" + key, Label: "Restore the Worker's secrets", Automatic: true, Action: "repair", Target: key, Changes: []string{"Set BACKPLANE_PROBE_TOKEN (and any other missing secret) from the vault"}}}
			case e.Status == 404:
				prob.Title = "WORKER ROUTE MISSING"
				prob.Summary = "The Worker is reachable but has no health endpoint — the deployed code is not the version Backplane generated (someone deployed different code)."
				prob.Code = "drift"
			case e.Status >= 500:
				prob.Title = "WORKER IS CRASHING"
				prob.Summary = fmt.Sprintf("The Worker returned HTTP %d. Open the Logs screen for the Worker's error.", e.Status)
				prob.Code = "server"
			}
		}
		return nil, prob
	}
	var h ProbeHealth
	if err := json.Unmarshal(resp.Body, &h); err != nil {
		return nil, &core.Problem{Title: "Unexpected Worker response", Code: "drift", Summary: "The Worker's health endpoint did not return Backplane's JSON. The deployed code may have been replaced."}
	}
	h.status, h.latency, h.headers = resp.Status, time.Since(start), resp.Header
	c.mu.Lock()
	c.health = &h
	c.mu.Unlock()
	return &h, nil
}

// probeLink turns one Worker probe item into a link result.
func probeLink(item, what string) LinkCheck {
	return func(c *Checker, l *core.LinkSpec) core.CheckResult {
		h, prob := c.WorkerHealth()
		if prob != nil {
			return core.CheckResult{Health: core.HealthFail, Summary: prob.Summary, Problem: prob}
		}
		it, ok := h.Checks[item]
		if !ok {
			return core.CheckResult{Health: core.HealthWarn, Summary: "The Worker did not report on " + what + ". It may be running older generated code — rebuild to update it."}
		}
		if it.Skipped {
			return core.CheckResult{Health: core.HealthOK, Summary: orStr(it.Detail, "Configured"), LatencyMS: it.MS}
		}
		if it.OK && it.Warn {
			prob := &core.Problem{Title: "PRODUCT FILE NOT UPLOADED", Code: "invalid",
				Summary:  "Storage works, but the file customers buy isn't there yet, so download links would fail. Upload it, then build to put it in private storage.",
				Affected: []string{"Download delivery"},
				Fixes:    []core.Fix{{ID: "upload:product", Label: "Upload the product file", Explain: "Choose the file on the project's Settings tab; the next build uploads it to private storage.", Action: "upload"}}}
			return core.CheckResult{Health: core.HealthWarn, Summary: orStr(it.Detail, "Needs attention"), LatencyMS: it.MS, Problem: prob}
		}
		if it.OK {
			return core.CheckResult{Health: core.HealthOK, Summary: orStr(it.Detail, "Working"), LatencyMS: it.MS, Details: map[string]string{"measured from": "inside the Worker"}}
		}
		prob = &core.Problem{Title: strings.ToUpper("Worker → " + what + " failed"), Code: "drift",
			Summary: "The Worker is running, but its connection to " + what + " is broken: " + orStr(it.Error, "no detail") + "."}
		prob.Fixes = diagnoseProbe(item, it.Error, c)
		return core.CheckResult{Health: core.HealthFail, Summary: prob.Summary, LatencyMS: it.MS, Problem: prob,
			Expected: what + " reachable with valid credentials", Actual: orStr(it.Error, "failing")}
	}
}

// diagnoseProbe maps common Worker-side failures to fixes.
func diagnoseProbe(item, msg string, c *Checker) []core.Fix {
	_, workerKey := c.WorkerURL()
	lm := strings.ToLower(msg)
	restore := core.Fix{ID: "reapply:" + workerKey, Label: "Restore the Worker's configuration", Automatic: true, Action: "repair", Target: workerKey,
		Explain: "Re-uploads the generated Worker with its bindings and sets any missing secrets from the vault.", Changes: []string{"Re-bind " + item, "Re-set missing secrets"}}
	switch {
	case strings.Contains(lm, "binding") || strings.Contains(lm, "not bound") || strings.Contains(lm, "undefined"):
		return []core.Fix{restore}
	case item == "stripe" && (strings.Contains(lm, "401") || strings.Contains(lm, "invalid api key") || strings.Contains(lm, "expired")):
		return []core.Fix{{ID: "reconnect:stripe", Label: "Replace the Worker's Stripe key", Action: "reconnect", Target: "stripe",
			Explain: "Stripe no longer accepts the restricted key the Worker uses. Create a new restricted key in Stripe, paste it into the Stripe connection's “Worker key” field, then choose Repair to update the Worker."}, restore}
	case strings.Contains(lm, "401") || strings.Contains(lm, "invalid api key") || strings.Contains(lm, "unauthorized") || strings.Contains(lm, "jwt"):
		return []core.Fix{{ID: "rotate:" + item, Label: "Issue a new " + item + " credential to the Worker", Automatic: true, Action: "repair", Target: workerKey,
			Explain: "The Worker's credential for " + item + " was revoked. Backplane creates a replacement key where the provider allows and updates the Worker secret.", Changes: []string{"Create a new " + item + " key", "Update the Worker secret"}}, restore}
	case strings.Contains(lm, "missing") || strings.Contains(lm, "not set"):
		return []core.Fix{restore}
	}
	return []core.Fix{restore}
}

// providerProbe is a helper to call a Worker path with the probe token.
// Pass quiet=true for negative tests that are expected to be rejected, so the
// log doesn't show them as warnings.
func (c *Checker) workerCall(method, path string, body []byte, header http.Header, quiet ...bool) (*httpx.Response, error) {
	url, key := c.WorkerURL()
	if url == "" {
		return nil, fmt.Errorf("worker not built")
	}
	token, _ := c.Gen("probe_token")
	if header == nil {
		header = http.Header{}
	}
	if header.Get("Authorization") == "" && token != "" && strings.HasPrefix(path, "/__backplane") {
		header.Set("Authorization", "Bearer "+token)
	}
	rq := httpx.Request{Method: method, Path: url + path, Header: header, Resource: key, Body: body, Quiet: len(quiet) > 0 && quiet[0]}
	if body != nil && header.Get("Content-Type") == "" {
		rq.ContentType = "application/json"
	}
	return c.ProbeClient().Do(c.ctx, rq)
}

// componentOf returns the component key holding a resource kind.
func (c *Checker) componentOf(kind string) string {
	for _, r := range c.P.Blueprint.Resources {
		if r.Kind == kind {
			return r.Component
		}
	}
	return ""
}

// resourceOf returns the first resource spec of a kind.
func (c *Checker) resourceOf(kind string) *core.ResourceSpec {
	for i := range c.P.Blueprint.Resources {
		if c.P.Blueprint.Resources[i].Kind == kind {
			return &c.P.Blueprint.Resources[i]
		}
	}
	return nil
}

func hasKind(kind string) func(*core.Blueprint) bool {
	return func(bp *core.Blueprint) bool {
		for _, r := range bp.Resources {
			if r.Kind == kind {
				return true
			}
		}
		return false
	}
}

func step(title string, h core.Health, detail string, ms int64) core.StepResult {
	return core.StepResult{Title: title, Health: h, Detail: detail, LatencyMS: ms}
}

func ctxSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

var _ = providers.DisplayName
