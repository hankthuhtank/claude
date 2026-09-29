package supabase

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Handlers returns every Supabase resource handler.
func Handlers() []providers.Handler {
	return []providers.Handler{projectH{}, migrationH{}, apiKeyH{}, authH{}, bucketH{}, secretsH{}}
}

func sconn(s *providers.Session) (*providers.Conn, error) { return s.Conn("supabase") }

// refOf resolves the project ref a dependent resource targets.
func refOf(s *providers.Session, spec *core.ResourceSpec) (string, error) {
	if r := providers.Str(spec.Props, "project_ref"); r != "" {
		return r, nil
	}
	return "", &core.Problem{Title: "Supabase project not ready", Provider: "supabase", Code: "invalid",
		Summary: "This step needs the Supabase project to exist first. It will run automatically after the project is created."}
}

// ================= Project =================

type projectH struct{}

func (projectH) Kind() string { return KindProject }

// waitHealthy polls until the project is ACTIVE_HEALTHY.
func waitHealthy(ctx context.Context, s *providers.Session, c *providers.Conn, ref string, max time.Duration) (*Project, error) {
	deadline := time.Now().Add(max)
	last := ""
	for {
		p, err := GetProject(ctx, c, ref)
		if err != nil && !isRetryable(err) {
			return nil, err
		}
		if p != nil {
			if p.Status != last {
				s.Say("Supabase project %s is %s", ref, strings.ToLower(strings.ReplaceAll(p.Status, "_", " ")))
				last = p.Status
			}
			switch p.Status {
			case "ACTIVE_HEALTHY":
				return p, nil
			case "INIT_FAILED", "REMOVED":
				return nil, &core.Problem{Title: "Supabase could not start the project", Provider: "supabase", Code: "server",
					Summary: "Supabase reports the project as " + p.Status + ". Open the Supabase dashboard for details, then re-run the build."}
			}
		}
		if time.Now().After(deadline) {
			return nil, &core.Problem{Title: "Supabase is still starting the project", Provider: "supabase", Code: "timeout", Retryable: true,
				Summary: fmt.Sprintf("The project was created but is not ready after %s (last status: %s). Resume the build in a few minutes — it continues from here.", max, last)}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.PollEvery()):
		}
	}
}

func isRetryable(err error) bool {
	e, ok := httpx.AsError(err)
	return ok && e.Retryable
}

func (projectH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	ref := providers.Str(spec.Props, "existing_ref")
	if st != nil && st.ID != "" {
		ref = st.ID
	}
	created := false
	secrets := map[string]string{}
	if ref == "" {
		// Look for a project with this name first so a crash between
		// "create" and "checkpoint" never creates a second project.
		var projects []Project
		if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects", Quiet: true}, &projects); err != nil {
			return nil, err
		}
		for _, p := range projects {
			if p.Name == name {
				if st == nil && !spec.Adopt {
					return nil, &core.Problem{Title: "Supabase project name taken", Provider: "supabase", Code: httpx.KindConflict,
						Summary: "A Supabase project named “" + name + "” already exists. Choose ‘Use existing project’ in the plan or rename it."}
				}
				ref = p.Ref
			}
		}
	}
	if ref == "" {
		slug := providers.Str(spec.Props, "organization_slug")
		if slug == "" {
			slug = c.Connection.Setting("organization_slug")
		}
		if slug == "" {
			return nil, &core.Problem{Title: "Choose a Supabase organization", Provider: "supabase", Code: "invalid", Summary: "Your token can see more than one organization. Pick one on the Supabase connection."}
		}
		pass := providers.Str(spec.Props, "db_pass")
		if pass == "" {
			pass = providers.StrongPassword(28)
		}
		region := providers.Str(spec.Props, "region")
		if region == "" {
			region = "us-east-1"
		}
		body := map[string]any{"name": name, "organization_slug": slug, "db_pass": pass,
			"region_selection": map[string]any{"type": "specific", "code": region}}
		s.Say("Creating Supabase project %s in %s", name, region)
		var p Project
		if err := do(ctx, c, httpx.Request{Method: "POST", Path: "/v1/projects", JSON: body, Resource: name}, &p); err != nil {
			return nil, err
		}
		ref, created = p.Ref, true
		secrets["db_pass"] = pass
	}
	p, err := GetProject(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if p.Status == "INACTIVE" {
		s.Say("Supabase project %s is paused — restoring it", ref)
		if err := do(ctx, c, httpx.Request{Method: "POST", Path: "/v1/projects/" + ref + "/restore", JSON: map[string]any{}}, nil); err != nil {
			return nil, err
		}
	}
	if p.Status != "ACTIVE_HEALTHY" {
		wait := time.Duration(providers.Int(spec.Props, "wait_seconds")) * time.Second
		if wait == 0 {
			wait = 10 * time.Minute
		}
		if p, err = waitHealthy(ctx, s, c, ref, wait); err != nil {
			// Record what exists so a resume picks the same project up.
			next := providers.Touch(spec, st, ref, name)
			next.Status = core.StateCreating
			next.SetOutput("ref", ref)
			next.SetOutput("url", ProjectURL(c, ref))
			return &providers.ApplyResult{State: next, Secrets: secrets, Created: created}, err
		}
	}
	next := providers.Touch(spec, st, ref, p.Name)
	next.SetOutput("ref", ref)
	next.SetOutput("url", ProjectURL(c, ref))
	next.SetOutput("region", p.Region)
	next.SetOutput("dashboard", "https://supabase.com/dashboard/project/"+ref)
	if st == nil && !created {
		next.CreatedBy = "adopted"
	}
	return &providers.ApplyResult{State: next, Secrets: secrets, Created: created}, nil
}

func (projectH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "existing_ref")
	if st != nil && st.ID != "" {
		ref = st.ID
	}
	if ref == "" {
		return &providers.Observation{Exists: false}, nil
	}
	p, err := GetProject(ctx, c, ref)
	if err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	obs := &providers.Observation{Exists: p.Status != "REMOVED", ID: p.Ref, Name: p.Name, Props: map[string]any{"status": p.Status, "region": p.Region}, Stats: map[string]string{}}
	obs.Summary = strings.ToLower(strings.ReplaceAll(p.Status, "_", " "))
	return obs, nil
}

func (projectH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	status := providers.Str(obs.Props, "status")
	if status == "ACTIVE_HEALTHY" {
		return nil
	}
	item := core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "project status", Expected: "ACTIVE_HEALTHY", Actual: status, Severity: core.HealthFail,
		Breaks: []string{"Everything that reads or writes the database"}}
	if status == "INACTIVE" {
		item.Recommended = "Restore the paused project"
		item.FixID = "supabase.restore:" + spec.Key
		item.Breaks = append(item.Breaks, "Free-plan projects pause after a week without activity")
	}
	if status == "COMING_UP" || status == "RESTORING" || status == "UPGRADING" {
		item.Severity = core.HealthWarn
	}
	return []core.DriftItem{item}
}

func (projectH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	err = do(ctx, c, httpx.Request{Method: "DELETE", Path: "/v1/projects/" + st.ID, Resource: st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Migration =================

type migrationH struct{}

func (migrationH) Kind() string { return KindMigration }

type migration struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

func listMigrations(ctx context.Context, c *providers.Conn, ref string) ([]migration, error) {
	var ms []migration
	err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/database/migrations", Quiet: true}, &ms)
	return ms, err
}

func (migrationH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref, err := refOf(s, spec)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	sql := providers.Str(spec.Props, "sql")
	hash := core.HashBytes([]byte(sql))[:12]
	// Migration names carry the content hash so a changed schema becomes a
	// new, tracked migration instead of silently rewriting history.
	full := name + "_" + hash
	applied := false
	if ms, err := listMigrations(ctx, c, ref); err == nil {
		for _, m := range ms {
			if m.Name == full {
				applied = true
			}
		}
		if applied && providers.Bool(spec.Props, "__force") {
			// Repair: the schema SQL is idempotent (if not exists, drop
			// policy if exists, enable row level security), so running it
			// again restores dropped tables, columns, policies and RLS
			// without touching data or the migration history.
			s.Say("Re-applying schema %s to restore tables, policies and row level security", name)
			if _, err := Query(ctx, c, ref, sql, false); err != nil {
				return nil, err
			}
		}
		if !applied {
			s.Say("Applying migration %s (%d tables)", name, len(providers.List(spec.Props, "tables")))
			if err := do(ctx, c, httpx.Request{Method: "POST", Path: "/v1/projects/" + ref + "/database/migrations",
				JSON: map[string]any{"query": sql, "name": full}, Resource: name, Timeout: 3 * time.Minute}, nil); err != nil {
				return nil, err
			}
		}
	} else if httpx.IsNotFound(err) {
		// Older API: run the (idempotent) SQL directly.
		s.Say("Applying schema %s", name)
		if _, err := Query(ctx, c, ref, sql, false); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	next := providers.Touch(spec, st, ref+"/"+full, full)
	next.SetOutput("migration", full)
	next.Applied = map[string]any{"tables": providers.List(spec.Props, "tables"), "policies": providers.Int(spec.Props, "policies")}
	return &providers.ApplyResult{State: next, Created: !applied}, nil
}

// schemaFacts reads tables, RLS status, policy count and columns.
func schemaFacts(ctx context.Context, c *providers.Conn, ref string) (map[string]bool, int, map[string][]string, error) {
	rows, err := Query(ctx, c, ref, `select c.relname as name, c.relrowsecurity as rls from pg_class c join pg_namespace n on n.oid=c.relnamespace where n.nspname='public' and c.relkind in ('r','p') order by 1`, true)
	if err != nil {
		return nil, 0, nil, err
	}
	tables := map[string]bool{}
	for _, r := range rows {
		rls, _ := r["rls"].(bool)
		tables[fmt.Sprint(r["name"])] = rls
	}
	pol, err := Query(ctx, c, ref, `select count(*)::int as n from pg_policies where schemaname='public'`, true)
	policies := 0
	if err == nil && len(pol) > 0 {
		policies = int(providers.Int(pol[0], "n"))
	}
	colRows, err := Query(ctx, c, ref, `select table_name, column_name from information_schema.columns where table_schema='public' order by table_name, ordinal_position`, true)
	cols := map[string][]string{}
	if err == nil {
		for _, r := range colRows {
			t := fmt.Sprint(r["table_name"])
			cols[t] = append(cols[t], fmt.Sprint(r["column_name"]))
		}
	}
	return tables, policies, cols, nil
}

func (migrationH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "project_ref")
	if ref == "" && st != nil {
		ref = strings.SplitN(st.ID, "/", 2)[0]
	}
	if ref == "" {
		return &providers.Observation{Exists: false}, nil
	}
	tables, policies, cols, err := schemaFacts(ctx, c, ref)
	if err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	want := providers.List(spec.Props, "tables")
	present := 0
	for _, t := range want {
		if _, ok := tables[t]; ok {
			present++
		}
	}
	obs := &providers.Observation{Exists: present > 0 || len(want) == 0, ID: ref, Props: map[string]any{"tables": tables, "policies": policies, "columns": cols},
		Stats: map[string]string{"tables": strconv.Itoa(len(tables)), "policies": strconv.Itoa(policies)}}
	obs.Summary = fmt.Sprintf("%d tables · %d policies", len(tables), policies)
	return obs, nil
}

func (migrationH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	var items []core.DriftItem
	tables, _ := obs.Props["tables"].(map[string]bool)
	cols, _ := obs.Props["columns"].(map[string][]string)
	purposes := providers.Map(spec.Props, "purposes")
	for _, t := range providers.List(spec.Props, "tables") {
		rls, ok := tables[t]
		if !ok {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "table " + t, Expected: "exists", Actual: "Missing",
				Severity: core.HealthFail, Breaks: nonEmpty(purposes[t]), Recommended: "Re-apply the schema migration", FixID: "reapply:" + spec.Key})
			continue
		}
		if !rls {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "row level security on " + t, Expected: "enabled", Actual: "DISABLED",
				Severity: core.HealthFail, Breaks: []string{"Anyone with the public key could read or change every row in " + t},
				Recommended: "Turn row level security back on", FixID: "supabase.rls:" + spec.Key + ":" + t})
		}
	}
	for table, wantCols := range providers.Map(spec.Props, "columns") {
		have := map[string]bool{}
		for _, c := range cols[table] {
			have[c] = true
		}
		for _, col := range strings.Split(wantCols, ",") {
			col = strings.TrimSpace(col)
			if col != "" && !have[col] && tables[table] {
				items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "column " + table + "." + col, Expected: "exists", Actual: "Missing",
					Severity: core.HealthFail, Breaks: nonEmpty(purposes[table]), Recommended: "Re-apply the schema migration", FixID: "reapply:" + spec.Key})
			}
		}
	}
	if want := providers.Int(spec.Props, "policies"); want > 0 {
		if got := providers.Int(obs.Props, "policies"); got < want {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "RLS policies", Expected: strconv.FormatInt(want, 10), Actual: strconv.FormatInt(got, 10),
				Severity: core.HealthWarn, Breaks: []string{"Signed-in users may lose access to their own rows"}, Recommended: "Re-apply the policies", FixID: "reapply:" + spec.Key})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Field < items[j].Field })
	return items
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (migrationH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil // schema is removed with the project; never dropped automatically
}

// ================= Secret API key =================

type apiKeyH struct{}

func (apiKeyH) Kind() string { return KindAPIKey }

func (apiKeyH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref, err := refOf(s, spec)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	keys, err := ListKeys(ctx, c, ref, true)
	if err != nil {
		return nil, err
	}
	rotate := providers.Bool(spec.Props, "__rotate")
	// Reuse our own key when we still hold its value in the vault.
	if st != nil && st.ID != "" && !rotate {
		for _, k := range keys {
			if k.ID == st.ID {
				if v, err := s.Secret(spec.Key, "key"); err == nil && v != "" {
					next := providers.Touch(spec, st, k.ID, k.Name)
					next.SetOutput("type", k.Type)
					next.SetOutput("ref", ref)
					return &providers.ApplyResult{State: next}, nil
				}
			}
		}
	}
	var existing *APIKey
	for i := range keys {
		if keys[i].Name == name && keys[i].Type == "secret" {
			existing = &keys[i]
		}
	}
	if existing != nil && existing.APIKey != "" && !strings.Contains(existing.APIKey, "·") && !rotate {
		next := providers.Touch(spec, st, existing.ID, existing.Name)
		next.SetOutput("type", "secret")
		next.SetOutput("hint", existing.Prefix)
		next.SetOutput("ref", ref)
		return &providers.ApplyResult{State: next, Secrets: map[string]string{"key": existing.APIKey}}, nil
	}
	s.Say("Creating a dedicated Supabase secret key “%s” for the Worker", name)
	var k APIKey
	err = do(ctx, c, httpx.Request{Method: "POST", Path: "/v1/projects/" + ref + "/api-keys", Query: map[string][]string{"reveal": {"true"}},
		JSON: map[string]any{"type": "secret", "name": name, "description": "Created by Backplane for server-side access. Revoke here to cut off the Worker."}, Resource: name}, &k)
	if err != nil {
		// Projects without the new key system: fall back to service_role.
		if e, ok := httpx.AsError(err); ok && (e.Kind == httpx.KindNotFound || e.Kind == httpx.KindInvalid) {
			key, kind, kerr := ServerKey(ctx, c, ref)
			if kerr != nil {
				return nil, err
			}
			next := providers.Touch(spec, st, "service_role", "service_role")
			next.SetOutput("type", kind)
			next.SetOutput("ref", ref)
			next.Note = "This project has no secret API keys yet, so the legacy service_role key is used. Supabase is retiring it by the end of 2026 — enable the new API keys in the dashboard and rebuild."
			return &providers.ApplyResult{State: next, Secrets: map[string]string{"key": key}}, nil
		}
		return nil, err
	}
	if rotate {
		// Retire the keys this one replaces (ours by id, or same name).
		for _, old := range keys {
			if old.ID != k.ID && old.Type == "secret" && ((st != nil && old.ID == st.ID) || old.Name == name) {
				s.Say("Revoking the replaced Supabase key %s", old.Name)
				_ = do(ctx, c, httpx.Request{Method: "DELETE", Path: "/v1/projects/" + ref + "/api-keys/" + old.ID, Resource: name}, nil)
			}
		}
	}
	next := providers.Touch(spec, st, k.ID, k.Name)
	next.SetOutput("type", "secret")
	next.SetOutput("hint", k.Prefix)
	next.SetOutput("ref", ref)
	return &providers.ApplyResult{State: next, Secrets: map[string]string{"key": k.APIKey}, Created: true}, nil
}

func (apiKeyH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "project_ref")
	if ref == "" || st == nil {
		return &providers.Observation{Exists: false}, nil
	}
	if st.ID == "service_role" {
		return &providers.Observation{Exists: true, ID: st.ID, Name: "service_role (legacy)"}, nil
	}
	keys, err := ListKeys(ctx, c, ref, false)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.ID == st.ID {
			return &providers.Observation{Exists: true, ID: k.ID, Name: k.Name, Props: map[string]any{"type": k.Type}}, nil
		}
	}
	return &providers.Observation{Exists: false}, nil
}

func (apiKeyH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (apiKeyH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	if st.ID == "" || st.ID == "service_role" {
		return nil
	}
	c, err := sconn(s)
	if err != nil {
		return err
	}
	ref := st.Output("ref")
	if ref == "" {
		return nil
	}
	err = do(ctx, c, httpx.Request{Method: "DELETE", Path: "/v1/projects/" + ref + "/api-keys/" + st.ID}, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Auth configuration =================

type authH struct{}

func (authH) Kind() string { return KindAuthConfig }

// authFields are the non-secret auth settings Backplane manages and compares.
var authFields = []string{"site_url", "uri_allow_list", "disable_signup", "external_email_enabled", "mailer_autoconfirm",
	"password_min_length", "smtp_host", "smtp_port", "smtp_user", "smtp_admin_email", "smtp_sender_name", "mfa_totp_enroll_enabled", "mfa_totp_verify_enabled"}

func (authH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref, err := refOf(s, spec)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	settings, _ := spec.Props["settings"].(map[string]any)
	for k, v := range settings {
		body[k] = v
	}
	if pass := providers.Str(spec.Props, "smtp_pass"); pass != "" {
		body["smtp_pass"] = pass
	}
	s.Say("Configuring Supabase Auth (%d settings)", len(body))
	if err := do(ctx, c, httpx.Request{Method: "PATCH", Path: "/v1/projects/" + ref + "/config/auth", JSON: body, Idempotent: true, Resource: "auth"}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, ref+"/auth", "Auth settings")
	applied := map[string]any{}
	for k, v := range settings {
		applied[k] = v
	}
	next.Applied = applied
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (authH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "project_ref")
	if ref == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var cfg map[string]any
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/config/auth", Quiet: true}, &cfg); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	props := map[string]any{}
	for _, f := range authFields {
		if v, ok := cfg[f]; ok {
			props[f] = v
		}
	}
	summary := "email sign-in"
	if providers.Str(props, "smtp_host") != "" {
		summary += " · SMTP " + providers.Str(props, "smtp_host")
	}
	return &providers.Observation{Exists: true, ID: ref + "/auth", Props: props, Summary: summary}, nil
}

func (authH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	var items []core.DriftItem
	settings, _ := spec.Props["settings"].(map[string]any)
	for _, k := range providers.SortedKeys(settings) {
		want := providers.Str(settings, k)
		got := providers.Str(obs.Props, k)
		if _, tracked := obs.Props[k]; !tracked {
			continue
		}
		if normalize(want) == normalize(got) {
			continue
		}
		sev := core.HealthWarn
		breaks := []string{"Sign-in and account emails"}
		switch k {
		case "site_url", "uri_allow_list":
			breaks = []string{"OAuth and magic-link redirects (callback URL)"}
			sev = core.HealthFail
		case "smtp_host", "smtp_user", "smtp_admin_email":
			breaks = []string{"Verification and password-reset emails"}
			sev = core.HealthFail
		}
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "auth " + k, Expected: want, Actual: got, Severity: sev,
			Breaks: breaks, Recommended: "Restore auth setting", FixID: "reapply:" + spec.Key})
	}
	return items
}

func normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (authH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil
}

// ================= Storage bucket =================

type bucketH struct{}

func (bucketH) Kind() string { return KindBucket }

func (bucketH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref, err := refOf(s, spec)
	if err != nil {
		return nil, err
	}
	key, _, err := ServerKey(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	pc := ProjectClient(c, ref, key, c.Client.Log)
	id := providers.Str(spec.Props, "name")
	body := map[string]any{"id": id, "name": id, "public": providers.Bool(spec.Props, "public")}
	if lim := providers.Int(spec.Props, "file_size_limit"); lim > 0 {
		body["file_size_limit"] = lim
	}
	if mimes := providers.List(spec.Props, "allowed_mime_types"); len(mimes) > 0 {
		body["allowed_mime_types"] = mimes
	}
	created := false
	if _, err := pc.Do(ctx, httpx.Request{Method: "GET", Path: "/storage/v1/bucket/" + id, Quiet: true}); err != nil {
		s.Say("Creating Supabase storage bucket %s", id)
		if _, err := pc.Do(ctx, httpx.Request{Method: "POST", Path: "/storage/v1/bucket", JSON: body}); err != nil && !httpx.IsConflict(err) {
			return nil, err
		}
		created = true
	} else {
		if _, err := pc.Do(ctx, httpx.Request{Method: "PUT", Path: "/storage/v1/bucket/" + id, JSON: body}); err != nil {
			return nil, err
		}
	}
	next := providers.Touch(spec, st, id, id)
	next.SetOutput("name", id)
	next.SetOutput("ref", ref)
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (bucketH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "project_ref")
	if ref == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var buckets []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Public bool   `json:"public"`
	}
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/storage/buckets", Quiet: true}, &buckets); err != nil {
		return nil, err
	}
	for _, b := range buckets {
		if b.ID == providers.Str(spec.Props, "name") {
			return &providers.Observation{Exists: true, ID: b.ID, Name: b.Name, Props: map[string]any{"public": b.Public}}, nil
		}
	}
	return &providers.Observation{Exists: false}, nil
}

func (bucketH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	want, got := providers.Bool(spec.Props, "public"), providers.Bool(obs.Props, "public")
	if want == got {
		return nil
	}
	sev := core.HealthWarn
	breaks := []string{"File access rules"}
	if got && !want {
		sev = core.HealthFail
		breaks = []string{"Private files are now readable by anyone with a link"}
	}
	return []core.DriftItem{{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "bucket visibility", Expected: visibility(want), Actual: visibility(got),
		Severity: sev, Breaks: breaks, Recommended: "Restore bucket visibility", FixID: "reapply:" + spec.Key}}
}

func visibility(public bool) string {
	if public {
		return "public"
	}
	return "private"
}

func (bucketH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := sconn(s)
	if err != nil {
		return err
	}
	ref := st.Output("ref")
	key, _, err := ServerKey(ctx, c, ref)
	if err != nil {
		return err
	}
	pc := ProjectClient(c, ref, key, c.Client.Log)
	_, err = pc.Do(ctx, httpx.Request{Method: "DELETE", Path: "/storage/v1/bucket/" + st.ID})
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Edge Function secrets =================

type secretsH struct{}

func (secretsH) Kind() string { return KindSecrets }

func (secretsH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref, err := refOf(s, spec)
	if err != nil {
		return nil, err
	}
	secrets := providers.Map(spec.Props, "secrets")
	var body []map[string]string
	for _, k := range providers.SortedKeys(secrets) {
		body = append(body, map[string]string{"name": k, "value": secrets[k]})
	}
	s.Say("Setting %d Edge Function secret(s)", len(body))
	if err := do(ctx, c, httpx.Request{Method: "POST", Path: "/v1/projects/" + ref + "/secrets", JSON: body, Idempotent: true}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, ref+"/secrets", "Edge Function secrets")
	next.Applied = map[string]any{"names": providers.SortedKeys(secrets)}
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (secretsH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := sconn(s)
	if err != nil {
		return nil, err
	}
	ref := providers.Str(spec.Props, "project_ref")
	var list []struct {
		Name string `json:"name"`
	}
	if err := do(ctx, c, httpx.Request{Method: "GET", Path: "/v1/projects/" + ref + "/secrets", Quiet: true}, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, x := range list {
		names = append(names, x.Name)
	}
	return &providers.Observation{Exists: true, ID: ref + "/secrets", Props: map[string]any{"names": names}}, nil
}

func (secretsH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	have := map[string]bool{}
	for _, n := range providers.List(obs.Props, "names") {
		have[n] = true
	}
	var items []core.DriftItem
	for _, k := range providers.SortedKeys(providers.Map(spec.Props, "secrets")) {
		if !have[k] {
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "supabase", Field: "secret " + k, Expected: "configured", Actual: "Missing",
				Severity: core.HealthFail, Recommended: "Set the secret again", FixID: "reapply:" + spec.Key})
		}
	}
	return items
}

func (secretsH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil
}
