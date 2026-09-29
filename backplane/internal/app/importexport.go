package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
)

// ---- discovery & import ----

// DiscoverParams lists the connections to scan.
type DiscoverParams struct {
	ConnectionIDs []string `json:"connectionIds"`
}

// DiscoveredItem is a found resource plus whether Backplane can monitor it.
type DiscoveredItem struct {
	providers.Discovered
	ConnectionID string `json:"connectionId"`
	Monitorable  bool   `json:"monitorable"` // Backplane has an adapter for this kind
	KindLabel    string `json:"kindLabel"`
	Managed      string `json:"managedBy,omitempty"` // project already tracking it
}

// DiscoverResult groups discoveries per connection.
type DiscoverResult struct {
	Items  []DiscoveredItem   `json:"items"`
	Errors map[string]string  `json:"errors,omitempty"` // connection id -> problem
	Groups map[string][]int   `json:"groups"`           // suggested grouping -> item indexes
	Notes  []string           `json:"notes,omitempty"`
	Links  []DiscoveredLinkUI `json:"links,omitempty"`
}

// DiscoveredLinkUI is an inferred relationship between two items.
type DiscoveredLinkUI struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Label string `json:"label"`
}

// Discover scans existing accounts (read-only) so an existing backend can be
// imported and monitored.
func (a *App) Discover(ctx context.Context, p DiscoverParams) (*DiscoverResult, error) {
	if len(p.ConnectionIDs) == 0 {
		return nil, fmt.Errorf("choose at least one connection to scan")
	}
	res := &DiscoverResult{Errors: map[string]string{}, Groups: map[string][]int{}}
	managed := a.managedIndex()
	type found struct {
		conn  core.Connection
		items []providers.Discovered
		err   error
	}
	results := make([]found, len(p.ConnectionIDs))
	var wg sync.WaitGroup
	for i, id := range p.ConnectionIDs {
		c, err := a.Engine.Connection(id)
		if err != nil {
			results[i].err = err
			continue
		}
		results[i].conn = *c
		prov, ok := a.Reg.Provider(c.Provider)
		if !ok {
			results[i].err = fmt.Errorf("unknown provider")
			continue
		}
		disc, ok := prov.(providers.Discoverer)
		if !ok {
			results[i].err = fmt.Errorf("%s cannot list existing resources yet", providers.DisplayName(c.Provider))
			continue
		}
		wg.Add(1)
		go func(i int, c core.Connection, disc providers.Discoverer) {
			defer wg.Done()
			conn, err := a.Engine.OpenConn(c, core.LogEntry{Source: "discover"})
			if err != nil {
				results[i].err = err
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			results[i].items, results[i].err = disc.Discover(cctx, conn)
		}(i, *c, disc)
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			res.Errors[p.ConnectionIDs[i]] = providers.Translate(r.conn.Provider, "list existing resources", r.err).Summary
			continue
		}
		for _, d := range r.items {
			_, monitorable := a.Reg.Handler(d.Kind)
			item := DiscoveredItem{Discovered: d, ConnectionID: r.conn.ID, Monitorable: monitorable, KindLabel: a.kindLabel(d.Provider, d.Kind)}
			item.Managed = managed[d.Kind+"|"+d.ID]
			idx := len(res.Items)
			res.Items = append(res.Items, item)
			group := d.Group
			if group == "" {
				group = providers.DisplayName(d.Provider)
			}
			res.Groups[group] = append(res.Groups[group], idx)
		}
	}
	// Resolve inferred links ("worker binds bucket downloads").
	byName := map[string]int{}
	for i, it := range res.Items {
		byName[it.Kind+"|"+it.Name] = i
		byName[it.Kind+"|"+it.ID] = i
	}
	for i, it := range res.Items {
		for _, l := range it.Links {
			if j, ok := byName[l.ToKind+"|"+l.ToName]; ok {
				res.Links = append(res.Links, DiscoveredLinkUI{From: i, To: j, Label: l.Label})
			}
		}
	}
	if len(res.Items) == 0 && len(res.Errors) == 0 {
		res.Notes = append(res.Notes, "Nothing was found in these accounts.")
	}
	return res, nil
}

func (a *App) kindLabel(provider, kind string) string {
	if p, ok := a.Reg.Provider(provider); ok {
		for _, k := range p.Info().Kinds {
			if k.Kind == kind {
				return k.Label
			}
		}
	}
	_, rest, _ := strings.Cut(kind, ".")
	return strings.ReplaceAll(rest, "_", " ")
}

// managedIndex maps kind|id to the project already tracking it.
func (a *App) managedIndex() map[string]string {
	out := map[string]string{}
	ps, _ := a.Store.ListProjects()
	for _, p := range ps {
		for _, env := range p.Environments {
			man, err := a.Store.LoadManifest(p.ID, env)
			if err != nil {
				continue
			}
			for _, st := range man.Resources {
				out[st.Kind+"|"+st.ID] = p.Name + " (" + env + ")"
			}
		}
	}
	return out
}

// ImportItem is one discovered resource chosen for import.
type ImportItem struct {
	ConnectionID string            `json:"connectionId"`
	Provider     string            `json:"provider"`
	Kind         string            `json:"kind"`
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Props        map[string]string `json:"props,omitempty"`
}

// ImportParams creates a monitored project from existing resources.
type ImportParams struct {
	Name  string             `json:"name"`
	Env   string             `json:"env"`
	Items []ImportItem       `json:"items"`
	Links []DiscoveredLinkUI `json:"links"` // indexes into Items
}

var keyRE = regexp.MustCompile(`[^a-z0-9_]+`)

// ImportProject adopts existing resources as a monitor-only project: they
// are checked for existence and connectivity, never changed or deleted.
func (a *App) ImportProject(ctx context.Context, p ImportParams) (*ProjectSummary, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 60 {
		return nil, fmt.Errorf("give the imported backend a name (up to 60 characters)")
	}
	if len(p.Items) == 0 {
		return nil, fmt.Errorf("choose at least one resource to import")
	}
	envs := normalizeEnvs([]string{p.Env})
	env := envs[0]
	pr := &core.Project{ID: projectID(name), Name: name, TemplateID: "imported", Environments: envs, ActiveEnv: env, Imported: true,
		CreatedAt: time.Now().UTC(), Connections: map[string]map[string]string{env: {}}, Monitor: core.DefaultMonitor()}
	pr.Monitor.FullEveryHours = 0 // imported backends have no synthetic tests
	bp := core.Blueprint{Version: 1, TemplateID: "imported", Params: map[string]any{"slug": blueprints.Slug(name)}}
	man := core.NewManifest(pr.ID, env)
	comps := map[string]bool{}
	used := map[string]bool{}
	keys := make([]string, len(p.Items))
	for i, it := range p.Items {
		c, err := a.Engine.Connection(it.ConnectionID)
		if err != nil {
			return nil, err
		}
		if c.Provider != it.Provider {
			return nil, fmt.Errorf("connection mismatch for %s", it.Name)
		}
		pr.Connections[env][it.Provider] = c.ID
		pr.Practice = pr.Practice || c.Practice
		if !comps[it.Provider] {
			comps[it.Provider] = true
			comp := core.Component{Key: it.Provider, Label: providers.DisplayName(it.Provider), Provider: it.Provider, Order: len(bp.Components)}
			if pv, ok := a.Reg.Provider(it.Provider); ok {
				info := pv.Info()
				comp.Role = info.Tagline
				if len(info.Capabilities) > 0 {
					comp.Capability = info.Capabilities[0]
				}
			}
			bp.Components = append(bp.Components, comp)
		}
		if _, ok := a.Reg.Handler(it.Kind); !ok {
			bp.Notes = append(bp.Notes, fmt.Sprintf("%s %s is watched through the %s connection (no resource-level checks for this kind yet).", a.kindLabel(it.Provider, it.Kind), it.Name, providers.DisplayName(it.Provider)))
			continue
		}
		base := keyRE.ReplaceAllString(strings.ToLower(strings.TrimPrefix(it.Kind, it.Provider+".")+"_"+it.Name), "_")
		if len(base) > 40 {
			base = base[:40]
		}
		key := strings.Trim(base, "_")
		for n := 2; used[key]; n++ {
			key = fmt.Sprintf("%s_%d", strings.Trim(base, "_"), n)
		}
		used[key] = true
		keys[i] = key
		props := map[string]any{"__imported": true, "name": it.Name, "title": it.Name}
		switch it.Kind {
		case "supabase.project":
			props["existing_ref"] = it.ID
		case "supabase.storage_bucket", "supabase.bucket":
			ref, bucket, _ := strings.Cut(it.ID, "/")
			props["project_ref"], props["name"] = ref, bucket
		}
		spec := core.ResourceSpec{Key: key, Kind: it.Kind, Provider: it.Provider, Component: it.Provider, Name: it.Name,
			Title: a.kindLabel(it.Provider, it.Kind) + " " + it.Name, Adopt: true, Keep: true, Props: props}
		bp.Resources = append(bp.Resources, spec)
		for ci := range bp.Components {
			if bp.Components[ci].Key == it.Provider {
				bp.Components[ci].Resources = append(bp.Components[ci].Resources, key)
			}
		}
		st := &core.ResourceState{Key: key, Kind: it.Kind, Provider: it.Provider, ID: it.ID, Name: it.Name, Status: core.StateImported, CreatedBy: "adopted",
			Outputs: map[string]string{}, CreatedAt: time.Now().UTC()}
		for k, v := range it.Props {
			st.Outputs[k] = v
		}
		if it.Kind == "cloudflare.worker" {
			st.SetOutput("name", it.ID)
		}
		man.Resources[key] = st
	}
	for _, l := range p.Links {
		if l.From < 0 || l.To < 0 || l.From >= len(p.Items) || l.To >= len(p.Items) {
			continue
		}
		from, to := p.Items[l.From].Provider, p.Items[l.To].Provider
		if from == to {
			continue
		}
		bp.Links = append(bp.Links, core.LinkSpec{Key: fmt.Sprintf("link_%d", len(bp.Links)+1), From: from, To: to, Label: l.Label, Kind: "binding"})
	}
	pr.Blueprint = bp
	if err := bp.ValidateGraph(); err != nil {
		return nil, err
	}
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	if err := a.Store.SaveManifest(man); err != nil {
		return nil, err
	}
	a.log("info", pr.ID, env, fmt.Sprintf("Imported %d existing resource(s) as %s. Backplane monitors them and never changes or deletes them.", len(man.Resources), name))
	go func() {
		if rep, err := a.Engine.Check(context.Background(), pr, env, engine.CheckOptions{Kind: core.CheckQuick, Trigger: "import"}); err == nil {
			a.monitor.observe(pr, env, rep)
		}
	}()
	s := a.summarize(pr)
	return &s, nil
}

// ---- export ----

// ExportParams selects what to export.
type ExportParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	// Include: manifest, code, wrangler, opentofu (default: all).
	Include []string `json:"include"`
}

// ExportResult points at the written archive.
type ExportResult struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
	Size  int64    `json:"size"`
	Notes []string `json:"notes,omitempty"`
}

// Export writes a zip with the manifest (no secrets), the generated code, a
// ready-to-use Wrangler config and an OpenTofu starting point, so a backend
// is never locked into Backplane.
func (a *App) Export(ctx context.Context, p ExportParams) (*ExportResult, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	man, err := a.Store.LoadManifest(pr.ID, env)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, i := range p.Include {
		want[i] = true
	}
	all := len(want) == 0
	files := map[string][]byte{}
	var notes []string

	if all || want["manifest"] {
		clean := *man
		clean.Resources = map[string]*core.ResourceState{}
		for k, st := range man.Resources {
			cp := *st
			cp.SecretRefs = nil
			clean.Resources[k] = &cp
		}
		doc := map[string]any{
			"schema":      "backplane.export/1",
			"exportedAt":  time.Now().UTC(),
			"project":     map[string]any{"id": pr.ID, "name": pr.Name, "template": pr.TemplateID, "environment": env},
			"blueprint":   redactBlueprint(pr.Blueprint),
			"manifest":    clean,
			"secretsNote": "Secret values are never exported. Each secret is listed by name so you know what to set.",
			"secrets":     secretNames(pr.Blueprint),
		}
		bs, _ := json.MarshalIndent(doc, "", "  ")
		files["backplane-manifest.json"] = bs
	}
	if all || want["code"] {
		track, _ := a.Store.LoadGenerated(pr.ID)
		for _, rel := range sortedKeys(track) {
			if b, err := os.ReadFile(mustPath(a.Engine.CodePath(pr.ID, rel))); err == nil {
				files["code/"+rel] = b
			}
		}
	}
	if all || want["wrangler"] {
		if raw, err := a.Engine.ReadCode(pr.ID, "worker/wrangler.jsonc"); err == nil {
			if out, err := a.Engine.RenderPublic(pr, env, "worker/wrangler.jsonc", raw); err == nil {
				files["deploy/wrangler.jsonc"] = []byte(out)
			} else {
				notes = append(notes, "The Wrangler config needs a finished build to fill in resource IDs: "+err.Error())
			}
		}
		files["deploy/SECRETS.md"] = []byte(secretsDoc(pr))
	}
	if all || want["opentofu"] {
		acct := ""
		if id := pr.Connections[env]["cloudflare"]; id != "" {
			if c, err := a.Engine.Connection(id); err == nil {
				acct = orStr(c.Setting("account_id"), c.AccountID)
			}
		}
		render := func(s string) string {
			out, err := a.Engine.RenderPublic(pr, env, "", s)
			if err != nil {
				return ""
			}
			return out
		}
		tf, tfNotes := opentofu(pr, man, acct, render)
		for k, v := range tf {
			files["opentofu/"+k] = []byte(v)
		}
		notes = append(notes, tfNotes...)
	}
	files["README.md"] = []byte(exportReadme(pr, env, files))

	dir := exportDir(a.Store.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("Backplane-%s-%s-%s.zip", blueprints.Slug(pr.Name), env, time.Now().Format("2006-01-02-1504"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	var list []string
	for _, k := range sortedKeys(files) {
		w, err := zw.Create(k)
		if err != nil {
			f.Close()
			return nil, err
		}
		if _, err := w.Write(files[k]); err != nil {
			f.Close()
			return nil, err
		}
		list = append(list, k)
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	st, _ := os.Stat(path)
	a.log("info", pr.ID, env, "Exported "+pr.Name+" ("+env+") to "+path)
	res := &ExportResult{Path: path, Files: list, Notes: notes}
	if st != nil {
		res.Size = st.Size()
	}
	return res, nil
}

// ShowFile reveals an exported file in the file manager.
func (a *App) ShowFile(ctx context.Context, p OpenExternalParams) (bool, error) {
	dir := exportDir(a.Store.Dir)
	clean := filepath.Clean(p.URL)
	if !strings.HasPrefix(clean, dir) {
		return false, fmt.Errorf("only Backplane exports can be shown")
	}
	return true, openPath(filepath.Dir(clean))
}

func mustPath(p string, err error) string {
	if err != nil {
		return ""
	}
	return p
}

// redactBlueprint removes secret placeholders' targets from props (the
// placeholders themselves carry no values, but generated code bodies are
// large and already exported under code/).
func redactBlueprint(bp core.Blueprint) core.Blueprint {
	out := bp
	out.Resources = make([]core.ResourceSpec, len(bp.Resources))
	for i, r := range bp.Resources {
		cp := r
		cp.Props = map[string]any{}
		for k, v := range r.Props {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "{{code") {
				cp.Props[k] = "(see code/" + strings.TrimSuffix(strings.SplitN(s, ":", 2)[1], "}}") + ")"
				continue
			}
			cp.Props[k] = v
		}
		out.Resources[i] = cp
	}
	return out
}

var secretRefRE = regexp.MustCompile(`\{\{\s*(secret|gen|conn):([^}]+?)\s*\}\}`)

// secretNames lists every secret a backend uses, by name only.
func secretNames(bp core.Blueprint) []map[string]string {
	var out []map[string]string
	seen := map[string]bool{}
	for _, r := range bp.Resources {
		walkStrings(r.Props, "", func(path, s string) {
			for _, m := range secretRefRE.FindAllStringSubmatch(s, -1) {
				name := path
				if r.Kind == "cloudflare.worker_secret" || r.Kind == "github.actions_secret" {
					name = providers.Str(r.Props, "name")
				}
				k := r.Key + "|" + name
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, map[string]string{"resource": r.Title, "name": name, "source": m[1] + ":" + m[2]})
			}
		})
	}
	return out
}

func walkStrings(v any, path string, fn func(path, s string)) {
	switch t := v.(type) {
	case string:
		fn(path, t)
	case map[string]any:
		for _, k := range sortedKeys(t) {
			walkStrings(t[k], k, fn)
		}
	case []any:
		for _, x := range t {
			walkStrings(x, path, fn)
		}
	}
}

func secretsDoc(pr *core.Project) string {
	var b strings.Builder
	b.WriteString("# Secrets for " + pr.Name + "\n\nBackplane keeps these values encrypted on your computer and never exports them.\n")
	b.WriteString("If you deploy this backend yourself, set each one with Wrangler:\n\n```\n")
	names := map[string]bool{}
	for _, s := range secretNames(pr.Blueprint) {
		n := s["name"]
		if n == "" || strings.ContainsAny(n, " /") || strings.ToUpper(n) != n {
			continue
		}
		if !names[n] {
			names[n] = true
			b.WriteString("npx wrangler secret put " + n + "\n")
		}
	}
	b.WriteString("```\n")
	return b.String()
}

func exportReadme(pr *core.Project, env string, files map[string][]byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%s) — Backplane export\n\nExported %s.\n\n", pr.Name, env, time.Now().Format("January 2, 2006 15:04"))
	b.WriteString("| File | What it is |\n| --- | --- |\n")
	desc := map[string]string{
		"backplane-manifest.json": "Everything Backplane knows about this backend: components, links, resource IDs and outputs. No secret values.",
		"deploy/wrangler.jsonc":   "Wrangler config with this environment's real bucket, namespace and database IDs filled in.",
		"deploy/SECRETS.md":       "The secrets the Worker needs, by name, with the commands to set them.",
		"opentofu/main.tf":        "An OpenTofu / Terraform starting point for the Cloudflare, GitHub and Supabase parts.",
		"opentofu/imports.tf":     "Import blocks so OpenTofu adopts the existing resources instead of creating new ones.",
		"opentofu/variables.tf":   "Inputs (tokens and secrets) — values are never included.",
	}
	for _, k := range sortedKeys(files) {
		if d, ok := desc[k]; ok {
			fmt.Fprintf(&b, "| `%s` | %s |\n", k, d)
		}
	}
	b.WriteString("| `code/` | The generated backend code (Worker, schema, emails, deploy workflow). |\n\n")
	b.WriteString("Stripe products, prices and webhook endpoints, and Resend domains and templates, are listed in the manifest with their IDs.\n")
	return b.String()
}

// ---- OpenTofu ----

func hclString(s string) string {
	bs, _ := json.Marshal(s)
	out := string(bs)
	out = strings.ReplaceAll(out, "${", "$${")
	out = strings.ReplaceAll(out, "%{", "%%{")
	return out
}

func tfName(key string) string {
	n := keyRE.ReplaceAllString(strings.ToLower(key), "_")
	if n == "" || (n[0] >= '0' && n[0] <= '9') {
		n = "r_" + n
	}
	return n
}

// opentofu renders Cloudflare, GitHub and Supabase resources as HCL.
func opentofu(pr *core.Project, man *core.Manifest, acct string, render func(string) string) (map[string]string, []string) {
	var main, imports, vars strings.Builder
	var notes []string
	providersUsed := map[string]bool{}
	type secretVar struct{ name, desc string }
	var secretVars []secretVar
	seenVar := map[string]bool{}
	addVar := func(name, desc string) string {
		v := tfName(name)
		if !seenVar[v] {
			seenVar[v] = true
			secretVars = append(secretVars, secretVar{v, desc})
		}
		return "var." + v
	}
	keys := make([]string, 0, len(man.Resources))
	for k := range man.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tfRef := map[string]string{} // resource key -> tf address
	for _, k := range keys {
		st := man.Resources[k]
		if st.Status == core.StateDeleted {
			continue
		}
		spec := pr.Blueprint.ResourceByKey(k)
		n := tfName(k)
		switch st.Kind {
		case "cloudflare.r2_bucket":
			providersUsed["cloudflare"] = true
			fmt.Fprintf(&main, "resource \"cloudflare_r2_bucket\" %q {\n  account_id = var.cloudflare_account_id\n  name       = %s\n}\n\n", n, hclString(st.ID))
			fmt.Fprintf(&imports, "import {\n  to = cloudflare_r2_bucket.%s\n  id = \"${var.cloudflare_account_id}/%s/default\"\n}\n\n", n, st.ID)
			tfRef[k] = "cloudflare_r2_bucket." + n
		case "cloudflare.kv_namespace":
			providersUsed["cloudflare"] = true
			fmt.Fprintf(&main, "resource \"cloudflare_workers_kv_namespace\" %q {\n  account_id = var.cloudflare_account_id\n  title      = %s\n}\n\n", n, hclString(st.Name))
			fmt.Fprintf(&imports, "import {\n  to = cloudflare_workers_kv_namespace.%s\n  id = \"${var.cloudflare_account_id}/%s\"\n}\n\n", n, st.ID)
			tfRef[k] = "cloudflare_workers_kv_namespace." + n
		case "cloudflare.d1_database":
			providersUsed["cloudflare"] = true
			fmt.Fprintf(&main, "resource \"cloudflare_d1_database\" %q {\n  account_id = var.cloudflare_account_id\n  name       = %s\n}\n\n", n, hclString(st.Name))
			fmt.Fprintf(&imports, "import {\n  to = cloudflare_d1_database.%s\n  id = \"${var.cloudflare_account_id}/%s\"\n}\n\n", n, st.ID)
			tfRef[k] = "cloudflare_d1_database." + n
		case "cloudflare.queue":
			providersUsed["cloudflare"] = true
			fmt.Fprintf(&main, "resource \"cloudflare_queue\" %q {\n  account_id = var.cloudflare_account_id\n  queue_name = %s\n}\n\n", n, hclString(st.Name))
			fmt.Fprintf(&imports, "import {\n  to = cloudflare_queue.%s\n  id = \"${var.cloudflare_account_id}/%s\"\n}\n\n", n, st.ID)
			tfRef[k] = "cloudflare_queue." + n
		case "supabase.project":
			providersUsed["supabase"] = true
			fmt.Fprintf(&main, "resource \"supabase_project\" %q {\n  organization_id   = var.supabase_organization_id\n  name              = %s\n  region            = %s\n  database_password = var.supabase_db_password\n\n  lifecycle {\n    ignore_changes = [database_password]\n  }\n}\n\n",
				n, hclString(st.Name), hclString(orStr(st.Output("region"), pr.Blueprint.Param("region"))))
			fmt.Fprintf(&imports, "import {\n  to = supabase_project.%s\n  id = %s\n}\n\n", n, hclString(st.ID))
		case "github.repo":
			providersUsed["github"] = true
			repo := st.Output("full_name")
			if repo == "" {
				repo = st.ID
			}
			_, short, _ := strings.Cut(repo, "/")
			if short == "" {
				short = repo
			}
			fmt.Fprintf(&main, "resource \"github_repository\" %q {\n  name       = %s\n  visibility = \"private\"\n}\n\n", n, hclString(short))
			fmt.Fprintf(&imports, "import {\n  to = github_repository.%s\n  id = %s\n}\n\n", n, hclString(short))
			tfRef[k] = "github_repository." + n
		}
		_ = spec
	}
	// Workers (after the resources they bind).
	for _, k := range keys {
		st := man.Resources[k]
		spec := pr.Blueprint.ResourceByKey(k)
		if st.Kind != "cloudflare.worker" || spec == nil || st.Status == core.StateDeleted {
			continue
		}
		providersUsed["cloudflare"] = true
		n := tfName(k)
		compat := orStr(providers.Str(spec.Props, "compatibility_date"), blueprints.CompatibilityDate)
		fmt.Fprintf(&main, "resource \"cloudflare_worker\" %q {\n  account_id = var.cloudflare_account_id\n  name       = %s\n\n  observability = {\n    enabled = true\n  }\n}\n\n", n, hclString(st.ID))
		var binds []string
		for _, b := range providers.Objects(spec.Props, "bindings") {
			bt, bn := providers.Str(b, "type"), providers.Str(b, "name")
			switch bt {
			case "kv_namespace":
				ref := refFor(spec, b, "namespace_id", tfRef, "id")
				binds = append(binds, fmt.Sprintf("    {\n      type         = \"kv_namespace\"\n      name         = %s\n      namespace_id = %s\n    }", hclString(bn), ref))
			case "r2_bucket":
				ref := refFor(spec, b, "bucket_name", tfRef, "name")
				binds = append(binds, fmt.Sprintf("    {\n      type        = \"r2_bucket\"\n      name        = %s\n      bucket_name = %s\n    }", hclString(bn), ref))
			case "d1":
				ref := refFor(spec, b, "id", tfRef, "id")
				binds = append(binds, fmt.Sprintf("    {\n      type = \"d1\"\n      name = %s\n      id   = %s\n    }", hclString(bn), ref))
			case "queue":
				ref := refFor(spec, b, "queue_name", tfRef, "queue_name")
				binds = append(binds, fmt.Sprintf("    {\n      type       = \"queue\"\n      name       = %s\n      queue_name = %s\n    }", hclString(bn), ref))
			}
		}
		vars := providers.Map(spec.Props, "vars")
		for _, vn := range providers.SortedKeys(vars) {
			v := vars[vn]
			if strings.Contains(v, "{{") {
				v = render(v)
			}
			binds = append(binds, fmt.Sprintf("    {\n      type = \"plain_text\"\n      name = %s\n      text = %s\n    }", hclString(vn), hclString(v)))
		}
		secretNames := providers.SortedKeys(providers.Map(spec.Props, "secrets"))
		for _, r := range pr.Blueprint.Resources {
			if r.Kind == "cloudflare.worker_secret" && strings.Contains(providers.Str(r.Props, "script"), k+".") {
				secretNames = append(secretNames, providers.Str(r.Props, "name"))
			}
		}
		sort.Strings(secretNames)
		for _, sn := range secretNames {
			binds = append(binds, fmt.Sprintf("    {\n      type = \"secret_text\"\n      name = %s\n      text = %s\n    }", hclString(sn), addVar(sn, "Worker secret "+sn)))
		}
		fmt.Fprintf(&main, "resource \"cloudflare_worker_version\" %q {\n  account_id         = var.cloudflare_account_id\n  worker_id          = cloudflare_worker.%s.id\n  compatibility_date = %s\n  main_module        = \"index.js\"\n\n  modules = [{\n    name         = \"index.js\"\n    content_type = \"application/javascript+module\"\n    content_file = \"${path.module}/../code/worker/src/index.js\"\n  }]\n\n  bindings = [\n%s\n  ]\n}\n\n",
			n, n, hclString(compat), strings.Join(binds, ",\n"))
		fmt.Fprintf(&main, "resource \"cloudflare_workers_deployment\" %q {\n  account_id  = var.cloudflare_account_id\n  script_name = cloudflare_worker.%s.name\n  strategy    = \"percentage\"\n\n  versions = [{\n    percentage = 100\n    version_id = cloudflare_worker_version.%s.id\n  }]\n}\n\n", n, n, n)
		notes = append(notes, "OpenTofu: the Worker "+st.ID+" is described with the Workers resources from Cloudflare's provider v5; import the existing Worker from the Cloudflare dashboard ID if you want OpenTofu to take it over.")
	}
	if len(providersUsed) == 0 {
		return map[string]string{}, []string{"OpenTofu: nothing to export for this environment yet (build it first)."}
	}
	var head strings.Builder
	head.WriteString("# Generated by Backplane. Review with `tofu plan` before applying: nothing here has secrets.\n")
	head.WriteString("terraform {\n  required_providers {\n")
	if providersUsed["cloudflare"] {
		head.WriteString("    cloudflare = {\n      source  = \"cloudflare/cloudflare\"\n      version = \"~> 5.0\"\n    }\n")
	}
	if providersUsed["github"] {
		head.WriteString("    github = {\n      source  = \"integrations/github\"\n      version = \"~> 6.0\"\n    }\n")
	}
	if providersUsed["supabase"] {
		head.WriteString("    supabase = {\n      source  = \"supabase/supabase\"\n      version = \"~> 1.0\"\n    }\n")
	}
	head.WriteString("  }\n}\n\n")
	if providersUsed["cloudflare"] {
		head.WriteString("provider \"cloudflare\" {\n  api_token = var.cloudflare_api_token\n}\n\n")
		fmt.Fprintf(&vars, "variable \"cloudflare_api_token\" {\n  type      = string\n  sensitive = true\n}\n\nvariable \"cloudflare_account_id\" {\n  type    = string\n  default = %s\n}\n\n", hclString(acct))
	}
	if providersUsed["github"] {
		head.WriteString("provider \"github\" {\n  token = var.github_token\n  owner = var.github_owner\n}\n\n")
		owner := ""
		for _, st := range man.Resources {
			if st.Kind == "github.repo" {
				owner, _, _ = strings.Cut(orStr(st.Output("full_name"), st.ID), "/")
			}
		}
		fmt.Fprintf(&vars, "variable \"github_token\" {\n  type      = string\n  sensitive = true\n}\n\nvariable \"github_owner\" {\n  type    = string\n  default = %s\n}\n\n", hclString(owner))
	}
	if providersUsed["supabase"] {
		head.WriteString("provider \"supabase\" {\n  access_token = var.supabase_access_token\n}\n\n")
		vars.WriteString("variable \"supabase_access_token\" {\n  type      = string\n  sensitive = true\n}\n\nvariable \"supabase_organization_id\" {\n  type = string\n}\n\nvariable \"supabase_db_password\" {\n  type      = string\n  sensitive = true\n}\n\n")
	}
	for _, sv := range secretVars {
		fmt.Fprintf(&vars, "variable %q {\n  description = %s\n  type        = string\n  sensitive   = true\n}\n\n", sv.name, hclString(sv.desc))
	}
	out := map[string]string{"main.tf": head.String() + main.String(), "variables.tf": vars.String()}
	if imports.Len() > 0 {
		out["imports.tf"] = "# Import blocks let OpenTofu (1.5+) adopt what Backplane already created.\n# Run `tofu plan` and confirm it shows imports, not replacements.\n\n" + imports.String()
	}
	return out, notes
}

// refFor points a binding at the exported resource when possible, or the
// literal value otherwise.
func refFor(spec *core.ResourceSpec, b map[string]any, field string, tfRef map[string]string, attr string) string {
	raw := providers.Str(b, field)
	if m := regexp.MustCompile(`\{\{\s*out:([a-z0-9_]+)\.`).FindStringSubmatch(raw); m != nil {
		if addr, ok := tfRef[m[1]]; ok {
			return addr + "." + attr
		}
	}
	return hclString(raw)
}

// exportDir is the user's Downloads folder (or Backplane's exports folder).
func exportDir(dataDir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if dirExists(d) {
			return filepath.Join(d, "Backplane exports")
		}
	}
	return filepath.Join(dataDir, "exports")
}
