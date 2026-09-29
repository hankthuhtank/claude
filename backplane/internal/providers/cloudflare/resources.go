package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Resource kinds.
const (
	KindWorker       = "cloudflare.worker"
	KindR2Bucket     = "cloudflare.r2_bucket"
	KindKV           = "cloudflare.kv_namespace"
	KindD1           = "cloudflare.d1_database"
	KindQueue        = "cloudflare.queue"
	KindSubdomain    = "cloudflare.workers_subdomain"
	KindR2Object     = "cloudflare.r2_object"
	KindDNSRecord    = "cloudflare.dns_record"
	KindWorkerDomain = "cloudflare.worker_domain"
)

// Handlers returns every Cloudflare resource handler.
func Handlers() []providers.Handler {
	return []providers.Handler{workerH{}, workerSecretH{}, r2H{}, kvH{}, d1H{}, queueH{}, subdomainH{}, r2ObjectH{}, dnsH{}, domainH{}}
}

func conn(s *providers.Session) (*providers.Conn, string, error) {
	c, err := s.Conn("cloudflare")
	if err != nil {
		return nil, "", err
	}
	acct, err := AccountID(c)
	return c, acct, err
}

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:12]
}

// WorkerURL returns the public workers.dev URL for a script.
func WorkerURL(c *providers.Conn, script, subdomain string) string {
	if b := c.Base("workers_dev", ""); b != "" {
		return b + "/" + script
	}
	return "https://" + script + "." + subdomain + ".workers.dev"
}

// ================= Worker =================

type workerH struct{}

func (workerH) Kind() string { return KindWorker }

type binding struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	BucketName  string `json:"bucket_name,omitempty"`
	NamespaceID string `json:"namespace_id,omitempty"`
	DatabaseID  string `json:"database_id,omitempty"`
	ID          string `json:"id,omitempty"`
	QueueName   string `json:"queue_name,omitempty"`
	Text        string `json:"text,omitempty"`
	Service     string `json:"service,omitempty"`
	ClassName   string `json:"class_name,omitempty"`
	IndexName   string `json:"index_name,omitempty"`
	Dataset     string `json:"dataset,omitempty"`
}

func (b binding) target() string {
	switch b.Type {
	case "r2_bucket":
		return b.BucketName
	case "kv_namespace":
		return b.NamespaceID
	case "d1":
		if b.DatabaseID != "" {
			return b.DatabaseID
		}
		return b.ID
	case "queue":
		return b.QueueName
	case "plain_text":
		return b.Text
	case "service":
		return b.Service
	case "durable_object_namespace":
		return b.ClassName
	case "vectorize":
		return b.IndexName
	case "analytics_engine":
		return b.Dataset
	}
	return ""
}

func desiredBindings(props map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range providers.Objects(props, "bindings") {
		cp := map[string]any{}
		for k, v := range b {
			if k == "purpose" {
				continue
			}
			cp[k] = v
		}
		// D1 bindings require database_id (schema) — accept "id" too.
		if cp["type"] == "d1" && cp["database_id"] == nil && cp["id"] != nil {
			cp["database_id"] = cp["id"]
			delete(cp, "id")
		}
		out = append(out, cp)
	}
	vars := providers.Map(props, "vars")
	for _, k := range providers.SortedKeys(vars) {
		out = append(out, map[string]any{"type": "plain_text", "name": k, "text": vars[k]})
	}
	return out
}

func (workerH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	code := providers.Str(spec.Props, "code")
	if name == "" || code == "" {
		return nil, fmt.Errorf("worker spec needs name and code")
	}
	compat := providers.Str(spec.Props, "compatibility_date")
	if compat == "" {
		compat = "2026-09-01"
	}
	meta := map[string]any{
		"main_module":        "index.js",
		"compatibility_date": compat,
		"bindings":           desiredBindings(spec.Props),
		"keep_bindings":      []string{"secret_text", "secret_key"},
		"observability":      map[string]any{"enabled": true, "head_sampling_rate": 1},
		"tags":               []string{"backplane"},
	}
	if flags := providers.List(spec.Props, "compatibility_flags"); len(flags) > 0 {
		meta["compatibility_flags"] = flags
	}
	created := st == nil || st.ID == ""
	s.Say("Uploading Worker %s (%d KB)", name, len(code)/1024+1)
	if _, err := UploadWorker(ctx, c, acct, name, code, meta); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, name, name)
	if next.Applied == nil {
		next.Applied = map[string]any{}
	}
	// Secrets go through the secrets API so they are never part of a script
	// upload body and survive later uploads (keep_bindings above).
	secrets := providers.Map(spec.Props, "secrets")
	prevDigests := map[string]string{}
	if st != nil && st.Applied != nil {
		prevDigests = providers.Map(st.Applied, "secret_digests")
	}
	existing, _ := listSecrets(ctx, c, acct, name)
	force := providers.Bool(spec.Props, "__force")
	digests := map[string]string{}
	for _, k := range providers.SortedKeys(secrets) {
		v := secrets[k]
		d := digest(v)
		digests[k] = d
		if prevDigests[k] == d && existing[k] && !force {
			continue
		}
		s.Say("Setting Worker secret %s", k)
		if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/secrets",
			JSON: map[string]any{"name": k, "text": v, "type": "secret_text"}, Resource: name}, nil); err != nil {
			return nil, err
		}
	}
	next.Applied["secret_digests"] = digests
	next.Applied["secret_names"] = providers.SortedKeys(secrets)
	next.Applied["bindings"] = redactBindings(desiredBindings(spec.Props))
	next.Applied["compatibility_date"] = compat

	sub := c.Connection.Setting("workers_subdomain")
	if sub == "" {
		sub = s.Output(providers.Str(spec.Props, "subdomain_resource"), "subdomain")
	}
	if sub == "" {
		var r struct {
			Subdomain string `json:"subdomain"`
		}
		if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/subdomain"}, &r); err == nil {
			sub = r.Subdomain
		}
	}
	if providers.Bool(spec.Props, "workers_dev") {
		s.Say("Turning on %s.%s.workers.dev", name, sub)
		if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/subdomain",
			JSON: map[string]any{"enabled": true, "previews_enabled": false}, Idempotent: true, Resource: name}, nil); err != nil {
			return nil, err
		}
		if sub == "" {
			return nil, &core.Problem{Title: "No workers.dev subdomain", Provider: "cloudflare", Code: "invalid",
				Summary: "This Cloudflare account has no workers.dev subdomain, so the Worker has no public address yet. Add the subdomain step to the plan or set one in the Cloudflare dashboard (Workers & Pages → Settings)."}
		}
		next.SetOutput("url", WorkerURL(c, name, sub))
	}
	crons := providers.List(spec.Props, "crons")
	if len(crons) > 0 || (st != nil && len(providers.List(st.Applied, "crons")) > 0) {
		body := make([]map[string]string, 0, len(crons))
		for _, cr := range crons {
			body = append(body, map[string]string{"cron": cr})
		}
		s.Say("Setting %d cron trigger(s)", len(crons))
		if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/schedules", JSON: body, Resource: name}, nil); err != nil {
			return nil, err
		}
	}
	next.Applied["crons"] = crons
	next.Applied["workers_dev"] = providers.Bool(spec.Props, "workers_dev")
	next.SetOutput("name", name)
	next.SetOutput("subdomain", sub)
	next.SetOutput("code_digest", digest(code))
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func redactBindings(bs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(bs))
	for _, b := range bs {
		cp := map[string]any{}
		for k, v := range b {
			cp[k] = v
		}
		out = append(out, cp)
	}
	return out
}

func listSecrets(ctx context.Context, c *providers.Conn, acct, name string) (map[string]bool, error) {
	var list []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/secrets", Quiet: true}, &list); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range list {
		out[s.Name] = true
	}
	return out, nil
}

type scriptSettings struct {
	Bindings          []binding `json:"bindings"`
	CompatibilityDate string    `json:"compatibility_date"`
	Observability     *struct {
		Enabled bool `json:"enabled"`
	} `json:"observability"`
}

// ReadWorker returns a Worker's bindings, secret names, workers.dev state and crons.
func ReadWorker(ctx context.Context, c *providers.Conn, acct, name string) (*providers.Observation, error) {
	var set scriptSettings
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/settings", Quiet: true, Resource: name}, &set); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	obs := &providers.Observation{Exists: true, ID: name, Name: name, Props: map[string]any{}, Stats: map[string]string{}}
	bindings := map[string]string{}
	types := map[string]string{}
	var secretNames []string
	for _, b := range set.Bindings {
		if b.Type == "secret_text" || b.Type == "secret_key" {
			secretNames = append(secretNames, b.Name)
			continue
		}
		bindings[b.Name] = b.target()
		types[b.Name] = b.Type
	}
	if sec, err := listSecrets(ctx, c, acct, name); err == nil {
		for k := range sec {
			found := false
			for _, n := range secretNames {
				if n == k {
					found = true
				}
			}
			if !found {
				secretNames = append(secretNames, k)
			}
		}
	}
	sort.Strings(secretNames)
	obs.Props["bindings"] = bindings
	obs.Props["binding_types"] = types
	obs.Props["secret_names"] = secretNames
	obs.Props["compatibility_date"] = set.CompatibilityDate
	var sub struct {
		Enabled bool `json:"enabled"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/subdomain", Quiet: true}, &sub); err == nil {
		obs.Props["workers_dev"] = sub.Enabled
	}
	var sched struct {
		Schedules []struct {
			Cron string `json:"cron"`
		} `json:"schedules"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(name) + "/schedules", Quiet: true}, &sched); err == nil {
		var crons []string
		for _, s := range sched.Schedules {
			crons = append(crons, s.Cron)
		}
		sort.Strings(crons)
		obs.Props["crons"] = crons
	}
	obs.Stats["bindings"] = strconv.Itoa(len(bindings))
	obs.Stats["secrets"] = strconv.Itoa(len(secretNames))
	obs.Summary = fmt.Sprintf("%d bindings · %d secrets", len(bindings), len(secretNames))
	return obs, nil
}

func (workerH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	if st != nil && st.ID != "" {
		name = st.ID
	}
	return ReadWorker(ctx, c, acct, name)
}

func (workerH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	var items []core.DriftItem
	if obs == nil || !obs.Exists {
		return items
	}
	purposes := providers.Map(spec.Props, "purposes")
	live := map[string]string{}
	if m, ok := obs.Props["bindings"].(map[string]string); ok {
		live = m
	}
	for _, b := range desiredBindings(spec.Props) {
		name := fmt.Sprint(b["name"])
		want := binding{Type: fmt.Sprint(b["type"]), BucketName: str(b["bucket_name"]), NamespaceID: str(b["namespace_id"]), DatabaseID: str(b["database_id"]), QueueName: str(b["queue_name"]), Text: str(b["text"]), Service: str(b["service"])}.target()
		got, ok := live[name]
		breaks := []string{}
		if p := purposes[name]; p != "" {
			breaks = append(breaks, p)
		}
		label := strings.ToUpper(strings.ReplaceAll(fmt.Sprint(b["type"]), "_", " "))
		if !ok {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "cloudflare", Field: "binding " + name,
				Expected: label + " " + name + " → " + want, Actual: "Missing — removed outside Backplane", Severity: core.HealthFail, Breaks: breaks,
				Recommended: "Restore binding", FixID: "reapply:" + spec.Key})
			continue
		}
		if want != "" && got != want {
			sev := core.HealthFail
			if b["type"] == "plain_text" {
				sev = core.HealthWarn
			}
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "cloudflare", Field: "binding " + name,
				Expected: want, Actual: got, Severity: sev, Breaks: breaks, Recommended: "Restore expected value", FixID: "reapply:" + spec.Key})
		}
	}
	liveSecrets := map[string]bool{}
	if names, ok := obs.Props["secret_names"].([]string); ok {
		for _, n := range names {
			liveSecrets[n] = true
		}
	}
	for _, k := range providers.SortedKeys(providers.Map(spec.Props, "secrets")) {
		if !liveSecrets[k] {
			var breaks []string
			if p := purposes[k]; p != "" {
				breaks = append(breaks, p)
			}
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "cloudflare", Field: "secret " + k,
				Expected: k + " configured", Actual: "Missing", Severity: core.HealthFail, Breaks: breaks, Recommended: "Set the secret again from the vault", FixID: "reapply:" + spec.Key})
		}
	}
	if providers.Bool(spec.Props, "workers_dev") {
		if v, ok := obs.Props["workers_dev"].(bool); ok && !v {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "cloudflare", Field: "workers.dev route",
				Expected: "Enabled", Actual: "Disabled", Severity: core.HealthFail, Breaks: []string{"Every public endpoint of this API"}, Recommended: "Turn workers.dev back on", FixID: "reapply:" + spec.Key})
		}
	}
	want := providers.List(spec.Props, "crons")
	sort.Strings(want)
	if got, ok := obs.Props["crons"].([]string); ok && strings.Join(got, ",") != strings.Join(want, ",") {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "cloudflare", Field: "cron triggers",
			Expected: orNone(want), Actual: orNone(got), Severity: core.HealthWarn, Breaks: []string{"Scheduled tasks"}, Recommended: "Restore schedule", FixID: "reapply:" + spec.Key})
	}
	return items
}

func orNone(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ", ")
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (workerH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(st.ID), Query: url.Values{"force": {"true"}}, Resource: st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= R2 bucket =================

type r2H struct{}

func (r2H) Kind() string { return KindR2Bucket }

func (r2H) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	if obs, err := readBucket(ctx, c, acct, name); err == nil && obs.Exists {
		if st == nil && !spec.Adopt {
			return nil, &core.Problem{Title: "Bucket name already taken", Provider: "cloudflare", Code: httpx.KindConflict,
				Summary: "An R2 bucket named “" + name + "” already exists in this account and Backplane did not create it. Choose ‘Use existing’ in the plan or rename it."}
		}
		next := providers.Touch(spec, st, name, name)
		next.SetOutput("name", name)
		if st == nil {
			next.CreatedBy = "adopted"
		}
		return &providers.ApplyResult{State: next}, applyCORS(ctx, c, acct, name, spec)
	}
	body := map[string]any{"name": name}
	if h := providers.Str(spec.Props, "location_hint"); h != "" {
		body["locationHint"] = h
	}
	s.Say("Creating R2 bucket %s", name)
	if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/r2/buckets", JSON: body, Resource: name}, nil); err != nil && !httpx.IsConflict(err) {
		return nil, err
	}
	next := providers.Touch(spec, st, name, name)
	next.SetOutput("name", name)
	return &providers.ApplyResult{State: next, Created: true}, applyCORS(ctx, c, acct, name, spec)
}

func applyCORS(ctx context.Context, c *providers.Conn, acct, name string, spec *core.ResourceSpec) error {
	origins := providers.List(spec.Props, "cors_origins")
	if len(origins) == 0 {
		return nil
	}
	rules := map[string]any{"rules": []map[string]any{{
		"allowed":       map[string]any{"origins": origins, "methods": []string{"GET", "PUT", "HEAD"}, "headers": []string{"*"}},
		"maxAgeSeconds": 3600,
	}}}
	_, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(name) + "/cors", JSON: rules, Resource: name}, nil)
	return err
}

func readBucket(ctx context.Context, c *providers.Conn, acct, name string) (*providers.Observation, error) {
	var b struct {
		Name         string `json:"name"`
		CreationDate string `json:"creation_date"`
		Location     string `json:"location"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(name), Quiet: true, Resource: name}, &b); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	obs := &providers.Observation{Exists: true, ID: name, Name: name, Props: map[string]any{"location": b.Location}, Stats: map[string]string{}}
	var objs []map[string]any
	env, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(name) + "/objects", Query: url.Values{"per_page": {"1000"}}, Quiet: true}, &objs)
	if err == nil {
		n := strconv.Itoa(len(objs))
		if env != nil && env.Info != nil && env.Info.Cursor != "" {
			n += "+"
		}
		obs.Stats["files"] = n
		obs.Summary = n + " files"
	}
	return obs, nil
}

func (r2H) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	if st != nil && st.ID != "" {
		name = st.ID
	}
	return readBucket(ctx, c, acct, name)
}

func (r2H) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (r2H) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	del := func() error {
		_, err := api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(st.ID), Resource: st.ID}, nil)
		if httpx.IsNotFound(err) {
			return nil
		}
		return err
	}
	err = del()
	if e, ok := httpx.AsError(err); ok && e.Kind == httpx.KindConflict {
		// A bucket must be empty before Cloudflare deletes it. Only buckets
		// Backplane created reach this point (adopted ones are detached).
		n, eerr := emptyBucket(ctx, c, acct, st.ID, 20000)
		if eerr != nil {
			return eerr
		}
		s.Say("Deleted %d object(s) from %s before removing it", n, st.ID)
		return del()
	}
	return err
}

// emptyBucket deletes up to max objects from a bucket.
func emptyBucket(ctx context.Context, c *providers.Conn, acct, bucket string, max int) (int, error) {
	deleted := 0
	for deleted < max {
		var objs []struct {
			Key string `json:"key"`
		}
		if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(bucket) + "/objects",
			Query: url.Values{"per_page": {"1000"}}, Quiet: true}, &objs); err != nil {
			return deleted, err
		}
		if len(objs) == 0 {
			return deleted, nil
		}
		for _, o := range objs {
			if err := DeleteObject(ctx, c, acct, bucket, o.Key); err != nil {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, &core.Problem{Title: "Bucket too large to empty automatically", Provider: "cloudflare", Code: "conflict",
		Summary: "The bucket " + bucket + " still holds more than " + strconv.Itoa(max) + " files. Empty it in the Cloudflare dashboard (or set a lifecycle rule), then resume."}
}

// PutObject uploads bytes to R2 through the REST API.
func PutObject(ctx context.Context, c *providers.Conn, acct, bucket, key string, body []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(bucket) + "/objects/" + escapeKey(key),
		Body: body, ContentType: contentType, Resource: bucket + "/" + key, Timeout: 10 * time.Minute}, nil)
	return err
}

// DeleteObject removes an R2 object.
func DeleteObject(ctx context.Context, c *providers.Conn, acct, bucket, key string) error {
	_, err := api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(bucket) + "/objects/" + escapeKey(key), Resource: bucket + "/" + key}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// GetObject downloads an R2 object (used by synthetic storage tests).
func GetObject(ctx context.Context, c *providers.Conn, acct, bucket, key string) ([]byte, error) {
	resp, err := c.Client.Do(ctx, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(bucket) + "/objects/" + escapeKey(key), Quiet: true})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func escapeKey(k string) string {
	parts := strings.Split(k, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// ================= R2 object =================

type r2ObjectH struct{}

func (r2ObjectH) Kind() string { return KindR2Object }

func objectBody(spec *core.ResourceSpec) ([]byte, error) {
	if b64 := providers.Str(spec.Props, "content_base64"); b64 != "" {
		return base64.StdEncoding.DecodeString(b64)
	}
	if p := providers.Str(spec.Props, "path"); p != "" {
		return os.ReadFile(p)
	}
	return []byte(providers.Str(spec.Props, "content")), nil
}

func (r2ObjectH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	bucket, key := providers.Str(spec.Props, "bucket"), providers.Str(spec.Props, "key")
	body, err := objectBody(spec)
	if err != nil {
		return nil, &core.Problem{Title: "File not readable", Code: "invalid", Summary: "Backplane could not read the file to upload: " + err.Error()}
	}
	s.Say("Uploading %s to R2 bucket %s (%s)", key, bucket, humanSize(len(body)))
	if err := PutObject(ctx, c, acct, bucket, key, body, providers.Str(spec.Props, "content_type")); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, bucket+"/"+key, key)
	next.SetOutput("key", key)
	next.SetOutput("bucket", bucket)
	next.SetOutput("size", strconv.Itoa(len(body)))
	next.SetOutput("sha256", core.HashBytes(body))
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (r2ObjectH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	bucket, key := providers.Str(spec.Props, "bucket"), providers.Str(spec.Props, "key")
	var objs []struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets/" + url.PathEscape(bucket) + "/objects",
		Query: url.Values{"prefix": {key}, "per_page": {"5"}}, Quiet: true}, &objs); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	for _, o := range objs {
		if o.Key == key {
			return &providers.Observation{Exists: true, ID: key, Name: key, Props: map[string]any{"size": o.Size}, Summary: humanSize(int(o.Size))}, nil
		}
	}
	return &providers.Observation{Exists: false}, nil
}

func (r2ObjectH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (r2ObjectH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	return DeleteObject(ctx, c, acct, st.Output("bucket"), st.Output("key"))
}

func humanSize(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ================= KV =================

type kvH struct{}

func (kvH) Kind() string { return KindKV }

type kvNamespace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func findKV(ctx context.Context, c *providers.Conn, acct, title string) (*kvNamespace, error) {
	for page := 1; page <= 10; page++ {
		var list []kvNamespace
		env, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/storage/kv/namespaces", Query: url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, Quiet: true}, &list)
		if err != nil {
			return nil, err
		}
		for i := range list {
			if list[i].Title == title {
				return &list[i], nil
			}
		}
		if env.Info == nil || page >= env.Info.TotalPages || len(list) == 0 {
			break
		}
	}
	return nil, nil
}

func (kvH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	title := providers.Str(spec.Props, "title")
	if st != nil && st.ID != "" {
		if obs, err := readKV(ctx, c, acct, st.ID); err == nil && obs.Exists {
			next := providers.Touch(spec, st, st.ID, title)
			next.SetOutput("id", st.ID)
			return &providers.ApplyResult{State: next}, nil
		}
	}
	existing, err := findKV(ctx, c, acct, title)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if st == nil && !spec.Adopt {
			return nil, &core.Problem{Title: "KV namespace already exists", Provider: "cloudflare", Code: httpx.KindConflict,
				Summary: "A KV namespace titled “" + title + "” already exists. Choose ‘Use existing’ or rename it."}
		}
		next := providers.Touch(spec, st, existing.ID, title)
		next.SetOutput("id", existing.ID)
		return &providers.ApplyResult{State: next}, nil
	}
	s.Say("Creating KV namespace %s", title)
	var ns kvNamespace
	if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/storage/kv/namespaces", JSON: map[string]any{"title": title}, Resource: title}, &ns); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, ns.ID, title)
	next.SetOutput("id", ns.ID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func readKV(ctx context.Context, c *providers.Conn, acct, id string) (*providers.Observation, error) {
	var ns kvNamespace
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/storage/kv/namespaces/" + id, Quiet: true}, &ns); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: ns.ID, Name: ns.Title}, nil
}

func (kvH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		ns, err := findKV(ctx, c, acct, providers.Str(spec.Props, "title"))
		if err != nil || ns == nil {
			return &providers.Observation{Exists: false}, err
		}
		return &providers.Observation{Exists: true, ID: ns.ID, Name: ns.Title}, nil
	}
	return readKV(ctx, c, acct, st.ID)
}

func (kvH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (kvH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/storage/kv/namespaces/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= D1 =================

type d1H struct{}

func (d1H) Kind() string { return KindD1 }

type d1DB struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	NumTables int    `json:"num_tables"`
	FileSize  int64  `json:"file_size"`
}

func findD1(ctx context.Context, c *providers.Conn, acct, name string) (*d1DB, error) {
	var list []d1DB
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/d1/database", Query: url.Values{"name": {name}, "per_page": {"100"}}, Quiet: true}, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

// QueryD1 runs SQL against a D1 database.
func QueryD1(ctx context.Context, c *providers.Conn, acct, id, sql string, params ...string) ([]map[string]any, error) {
	var res []struct {
		Results []map[string]any `json:"results"`
		Success bool             `json:"success"`
	}
	body := map[string]any{"sql": sql}
	if len(params) > 0 {
		body["params"] = params
	}
	if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/d1/database/" + id + "/query", JSON: body, Idempotent: true}, &res); err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, r := range res {
		rows = append(rows, r.Results...)
	}
	return rows, nil
}

func (d1H) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	id := ""
	created := false
	if st != nil && st.ID != "" {
		id = st.ID
	} else if db, err := findD1(ctx, c, acct, name); err != nil {
		return nil, err
	} else if db != nil {
		if st == nil && !spec.Adopt {
			return nil, &core.Problem{Title: "D1 database already exists", Provider: "cloudflare", Code: httpx.KindConflict, Summary: "A D1 database named “" + name + "” already exists. Choose ‘Use existing’ or rename it."}
		}
		id = db.UUID
	}
	if id == "" {
		s.Say("Creating D1 database %s", name)
		var db d1DB
		if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/d1/database", JSON: map[string]any{"name": name}, Resource: name}, &db); err != nil {
			return nil, err
		}
		id, created = db.UUID, true
	}
	if schema := providers.Str(spec.Props, "schema"); schema != "" {
		s.Say("Applying D1 schema (%d statements)", strings.Count(schema, ";"))
		if _, err := QueryD1(ctx, c, acct, id, schema); err != nil {
			return nil, err
		}
	}
	next := providers.Touch(spec, st, id, name)
	next.SetOutput("id", id)
	next.SetOutput("name", name)
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (d1H) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		db, err := findD1(ctx, c, acct, providers.Str(spec.Props, "name"))
		if err != nil || db == nil {
			return &providers.Observation{Exists: false}, err
		}
		return &providers.Observation{Exists: true, ID: db.UUID, Name: db.Name}, nil
	}
	var db d1DB
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/d1/database/" + st.ID, Quiet: true}, &db); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: db.UUID, Name: db.Name, Stats: map[string]string{"tables": strconv.Itoa(db.NumTables)},
		Summary: fmt.Sprintf("%d tables · %s", db.NumTables, humanSize(int(db.FileSize)))}, nil
}

func (d1H) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (d1H) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/d1/database/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Queue =================

type queueH struct{}

func (queueH) Kind() string { return KindQueue }

type queue struct {
	QueueID   string `json:"queue_id"`
	QueueName string `json:"queue_name"`
}

func findQueue(ctx context.Context, c *providers.Conn, acct, name string) (*queue, error) {
	var list []queue
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/queues", Quiet: true}, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].QueueName == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (queueH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	q, err := findQueue(ctx, c, acct, name)
	if err != nil {
		return nil, err
	}
	created := false
	if q == nil {
		s.Say("Creating queue %s", name)
		var nq queue
		if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/accounts/" + acct + "/queues", JSON: map[string]any{"queue_name": name}, Resource: name}, &nq); err != nil {
			return nil, err
		}
		q, created = &nq, true
	} else if st == nil && !spec.Adopt {
		return nil, &core.Problem{Title: "Queue already exists", Provider: "cloudflare", Code: httpx.KindConflict, Summary: "A queue named “" + name + "” already exists. Choose ‘Use existing’ or rename it."}
	}
	next := providers.Touch(spec, st, q.QueueID, name)
	next.SetOutput("id", q.QueueID)
	next.SetOutput("name", name)
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (queueH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	q, err := findQueue(ctx, c, acct, providers.Str(spec.Props, "name"))
	if err != nil || q == nil {
		return &providers.Observation{Exists: false}, err
	}
	return &providers.Observation{Exists: true, ID: q.QueueID, Name: q.QueueName}, nil
}

func (queueH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (queueH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/queues/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= workers.dev subdomain =================

type subdomainH struct{}

func (subdomainH) Kind() string { return KindSubdomain }

func (subdomainH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	var r struct {
		Subdomain string `json:"subdomain"`
	}
	_, err = api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/subdomain", Quiet: true}, &r)
	created := false
	if err != nil || r.Subdomain == "" {
		want := providers.Str(spec.Props, "subdomain")
		if want == "" {
			return nil, &core.Problem{Title: "Choose a workers.dev subdomain", Provider: "cloudflare", Code: "invalid", Summary: "This Cloudflare account needs a workers.dev subdomain before Workers get a public address. Set one in the plan's options."}
		}
		s.Say("Creating workers.dev subdomain %s", want)
		if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/subdomain", JSON: map[string]any{"subdomain": want}}, &r); err != nil {
			return nil, err
		}
		created = true
	}
	next := providers.Touch(spec, st, r.Subdomain, r.Subdomain+".workers.dev")
	next.SetOutput("subdomain", r.Subdomain)
	next.Note = "Account-wide setting; never deleted by Backplane."
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (subdomainH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	var r struct {
		Subdomain string `json:"subdomain"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/subdomain", Quiet: true}, &r); err != nil || r.Subdomain == "" {
		if err != nil && !httpx.IsNotFound(err) {
			return nil, err
		}
		return &providers.Observation{Exists: false}, nil
	}
	return &providers.Observation{Exists: true, ID: r.Subdomain, Name: r.Subdomain + ".workers.dev"}, nil
}

func (subdomainH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (subdomainH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil // account-wide; never removed automatically
}

// ================= DNS record =================

type dnsH struct{}

func (dnsH) Kind() string { return KindDNSRecord }

// ZoneID looks up a zone by domain name.
func ZoneID(ctx context.Context, c *providers.Conn, zone string) (string, error) {
	var zones []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/zones", Query: url.Values{"name": {zone}}, Quiet: true}, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if z.Name == zone {
			return z.ID, nil
		}
	}
	return "", &core.Problem{Title: "Domain not on this Cloudflare account", Provider: "cloudflare", Code: httpx.KindNotFound,
		Summary: "The domain " + zone + " is not a zone in this Cloudflare account, so Backplane cannot add DNS records automatically. Add the records shown in the plan at your DNS provider instead."}
}

type dnsRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

func (dnsH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := s.Conn("cloudflare")
	if err != nil {
		return nil, err
	}
	zone := providers.Str(spec.Props, "zone")
	zid, err := ZoneID(ctx, c, zone)
	if err != nil {
		return nil, err
	}
	want := dnsRecord{Type: providers.Str(spec.Props, "type"), Name: providers.Str(spec.Props, "name"), Content: providers.Str(spec.Props, "content"),
		TTL: int(providers.Int(spec.Props, "ttl")), Proxied: providers.Bool(spec.Props, "proxied")}
	if want.TTL == 0 {
		want.TTL = 1 // automatic
	}
	if p := providers.Int(spec.Props, "priority"); p > 0 || want.Type == "MX" {
		pr := int(p)
		want.Priority = &pr
	}
	var existing []dnsRecord
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/zones/" + zid + "/dns_records", Query: url.Values{"type": {want.Type}, "name": {want.Name}}, Quiet: true}, &existing); err != nil {
		return nil, err
	}
	for _, r := range existing {
		if strings.EqualFold(strings.Trim(r.Content, `"`), strings.Trim(want.Content, `"`)) {
			next := providers.Touch(spec, st, r.ID, want.Name)
			next.SetOutput("id", r.ID)
			next.SetOutput("zone_id", zid)
			return &providers.ApplyResult{State: next}, nil
		}
	}
	// Records that must be unique per name (CNAME, and the TXT we manage by id)
	// are updated in place when we own them.
	if st != nil && st.ID != "" {
		s.Say("Updating DNS %s %s", want.Type, want.Name)
		var out dnsRecord
		if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/zones/" + zid + "/dns_records/" + st.ID, JSON: want}, &out); err == nil {
			next := providers.Touch(spec, st, out.ID, want.Name)
			next.SetOutput("id", out.ID)
			next.SetOutput("zone_id", zid)
			return &providers.ApplyResult{State: next}, nil
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	s.Say("Adding DNS %s %s", want.Type, want.Name)
	var out dnsRecord
	if _, err := api(ctx, c, httpx.Request{Method: "POST", Path: "/zones/" + zid + "/dns_records", JSON: want}, &out); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, out.ID, want.Name)
	next.SetOutput("id", out.ID)
	next.SetOutput("zone_id", zid)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (dnsH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := s.Conn("cloudflare")
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" || st.Output("zone_id") == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var r dnsRecord
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/zones/" + st.Output("zone_id") + "/dns_records/" + st.ID, Quiet: true}, &r); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: r.ID, Name: r.Name, Props: map[string]any{"content": r.Content, "type": r.Type}}, nil
}

func (dnsH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	return providers.DriftIf(nil, spec, "DNS "+providers.Str(spec.Props, "type")+" "+providers.Str(spec.Props, "name"),
		strings.Trim(providers.Str(spec.Props, "content"), `"`), strings.Trim(providers.Str(obs.Props, "content"), `"`), core.HealthFail,
		[]string{providers.Str(spec.Props, "purpose")}, "Restore DNS record")
}

func (dnsH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := s.Conn("cloudflare")
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/zones/" + st.Output("zone_id") + "/dns_records/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Worker custom domain =================

type domainH struct{}

func (domainH) Kind() string { return KindWorkerDomain }

type workerDomain struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	ZoneID   string `json:"zone_id"`
}

func (domainH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	host := providers.Str(spec.Props, "hostname")
	zid, err := ZoneID(ctx, c, providers.Str(spec.Props, "zone"))
	if err != nil {
		return nil, err
	}
	s.Say("Attaching %s to Worker %s", host, providers.Str(spec.Props, "service"))
	var d workerDomain
	if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/domains",
		JSON: map[string]any{"hostname": host, "service": providers.Str(spec.Props, "service"), "zone_id": zid, "environment": "production"}}, &d); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, d.ID, host)
	next.SetOutput("url", "https://"+host)
	next.SetOutput("zone_id", zid)
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (domainH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	var list []workerDomain
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/domains", Query: url.Values{"hostname": {providers.Str(spec.Props, "hostname")}}, Quiet: true}, &list); err != nil {
		return nil, err
	}
	for _, d := range list {
		if d.Hostname == providers.Str(spec.Props, "hostname") {
			return &providers.Observation{Exists: true, ID: d.ID, Name: d.Hostname, Props: map[string]any{"service": d.Service}}, nil
		}
	}
	return &providers.Observation{Exists: false}, nil
}

func (domainH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	return providers.DriftIf(nil, spec, "domain → worker", providers.Str(spec.Props, "service"), providers.Str(obs.Props, "service"), core.HealthFail,
		[]string{"Requests to " + providers.Str(spec.Props, "hostname")}, "Point the domain back at the Worker")
}

func (domainH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/workers/domains/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Discovery =================

// Discover lists existing Cloudflare resources for import.
func (Provider) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	acct, err := AccountID(c)
	if err != nil {
		return nil, err
	}
	var out []providers.Discovered
	var scripts []struct {
		ID         string `json:"id"`
		ModifiedOn string `json:"modified_on"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/scripts", Quiet: true}, &scripts); err != nil {
		return nil, err
	}
	for i, sc := range scripts {
		d := providers.Discovered{Provider: "cloudflare", Kind: KindWorker, ID: sc.ID, Name: sc.ID, Detail: "modified " + shortDate(sc.ModifiedOn)}
		if i < 40 {
			if obs, err := ReadWorker(ctx, c, acct, sc.ID); err == nil && obs.Exists {
				types, _ := obs.Props["binding_types"].(map[string]string)
				targets, _ := obs.Props["bindings"].(map[string]string)
				for _, n := range providers.SortedKeys(types) {
					switch types[n] {
					case "r2_bucket":
						d.Links = append(d.Links, providers.DiscoveredLink{ToKind: KindR2Bucket, ToName: targets[n], Label: n})
					case "kv_namespace":
						d.Links = append(d.Links, providers.DiscoveredLink{ToKind: KindKV, ToName: targets[n], Label: n})
					case "d1":
						d.Links = append(d.Links, providers.DiscoveredLink{ToKind: KindD1, ToName: targets[n], Label: n})
					case "queue":
						d.Links = append(d.Links, providers.DiscoveredLink{ToKind: KindQueue, ToName: targets[n], Label: n})
					case "service":
						d.Links = append(d.Links, providers.DiscoveredLink{ToKind: KindWorker, ToName: targets[n], Label: n})
					}
				}
				d.Detail = obs.Summary + " · " + d.Detail
				d.Props = map[string]string{"secrets": strings.Join(toStrings(obs.Props["secret_names"]), ", ")}
			}
		}
		out = append(out, d)
	}
	var buckets struct {
		Buckets []struct {
			Name         string `json:"name"`
			CreationDate string `json:"creation_date"`
		} `json:"buckets"`
	}
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/r2/buckets", Quiet: true}, &buckets); err == nil {
		for _, b := range buckets.Buckets {
			out = append(out, providers.Discovered{Provider: "cloudflare", Kind: KindR2Bucket, ID: b.Name, Name: b.Name, Detail: "created " + shortDate(b.CreationDate)})
		}
	}
	var kvs []kvNamespace
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/storage/kv/namespaces", Query: url.Values{"per_page": {"100"}}, Quiet: true}, &kvs); err == nil {
		for _, k := range kvs {
			out = append(out, providers.Discovered{Provider: "cloudflare", Kind: KindKV, ID: k.ID, Name: k.Title})
		}
	}
	var dbs []d1DB
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/d1/database", Query: url.Values{"per_page": {"100"}}, Quiet: true}, &dbs); err == nil {
		for _, d := range dbs {
			out = append(out, providers.Discovered{Provider: "cloudflare", Kind: KindD1, ID: d.UUID, Name: d.Name, Detail: fmt.Sprintf("%d tables", d.NumTables)})
		}
	}
	var qs []queue
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/queues", Quiet: true}, &qs); err == nil {
		for _, q := range qs {
			out = append(out, providers.Discovered{Provider: "cloudflare", Kind: KindQueue, ID: q.QueueID, Name: q.QueueName})
		}
	}
	var domains []workerDomain
	if _, err := api(ctx, c, httpx.Request{Method: "GET", Path: "/accounts/" + acct + "/workers/domains", Quiet: true}, &domains); err == nil {
		for _, d := range domains {
			out = append(out, providers.Discovered{Provider: "cloudflare", Kind: KindWorkerDomain, ID: d.ID, Name: d.Hostname, Detail: "→ " + d.Service,
				Links: []providers.DiscoveredLink{{ToKind: KindWorker, ToName: d.Service, Label: "routes to"}}})
		}
	}
	// Match binding targets (KV/D1 ids) to discovered names for readable links.
	names := map[string]string{}
	for _, d := range out {
		names[d.Kind+"|"+d.ID] = d.Name
	}
	for i := range out {
		for j, l := range out[i].Links {
			if n, ok := names[l.ToKind+"|"+l.ToName]; ok {
				out[i].Links[j].ToName = n
			}
		}
	}
	return out, nil
}

func toStrings(v any) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	return nil
}

func shortDate(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("Jan 2, 2006")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// ================= Worker secret =================
//
// A secret set on a Worker by its own resource. Used when the secret only
// exists after something that needs the Worker's URL (for example a Stripe
// webhook signing secret), which would otherwise be a dependency cycle.

type workerSecretH struct{}

// KindWorkerSecret is a single Worker secret.
const KindWorkerSecret = "cloudflare.worker_secret"

func (workerSecretH) Kind() string { return KindWorkerSecret }

func (workerSecretH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	script, name, value := providers.Str(spec.Props, "script"), providers.Str(spec.Props, "name"), providers.Str(spec.Props, "value")
	if script == "" || name == "" || value == "" {
		return nil, fmt.Errorf("worker secret needs script, name and value")
	}
	d := digest(value)
	if st != nil && providers.Str(st.Applied, "digest") == d && !providers.Bool(spec.Props, "__force") {
		if have, err := listSecrets(ctx, c, acct, script); err == nil && have[name] {
			return &providers.ApplyResult{State: providers.Touch(spec, st, script+"#"+name, name)}, nil
		}
	}
	s.Say("Setting Worker secret %s on %s", name, script)
	if _, err := api(ctx, c, httpx.Request{Method: "PUT", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(script) + "/secrets",
		JSON: map[string]any{"name": name, "text": value, "type": "secret_text"}, Resource: script}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, script+"#"+name, name)
	next.Applied = map[string]any{"digest": d, "script": script}
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (workerSecretH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, acct, err := conn(s)
	if err != nil {
		return nil, err
	}
	script, name := providers.Str(spec.Props, "script"), providers.Str(spec.Props, "name")
	if script == "" && st != nil {
		script = providers.Str(st.Applied, "script")
	}
	have, err := listSecrets(ctx, c, acct, script)
	if err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: have[name], ID: script + "#" + name, Name: name}, nil
}

func (workerSecretH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (workerSecretH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, acct, err := conn(s)
	if err != nil {
		return err
	}
	script := providers.Str(st.Applied, "script")
	_, err = api(ctx, c, httpx.Request{Method: "DELETE", Path: "/accounts/" + acct + "/workers/scripts/" + url.PathEscape(script) + "/secrets/" + url.PathEscape(st.Name)}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}
