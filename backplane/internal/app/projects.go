package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
)

// ProjectSummary is a project card.
type ProjectSummary struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	TemplateID   string               `json:"templateId"`
	TemplateName string               `json:"templateName"`
	Industry     string               `json:"industry"`
	Level        string               `json:"level"`
	Description  string               `json:"description,omitempty"`
	Environments []EnvSummary         `json:"environments"`
	ActiveEnv    string               `json:"activeEnv"`
	Practice     bool                 `json:"practice"`
	Imported     bool                 `json:"imported"`
	Providers    []string             `json:"providers"`
	Monitor      core.MonitorSettings `json:"monitor"`
	CreatedAt    time.Time            `json:"createdAt"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

// EnvSummary is one environment's state on a project card.
type EnvSummary struct {
	Name      string      `json:"name"`
	Overall   core.Health `json:"overall"`
	Headline  string      `json:"headline"`
	CheckedAt *time.Time  `json:"checkedAt,omitempty"`
	LastBuild *time.Time  `json:"lastBuild,omitempty"`
	Resources int         `json:"resources"`
	Built     bool        `json:"built"`
	Missing   []string    `json:"missingConnections,omitempty"`
	Running   string      `json:"runningRun,omitempty"`
	Failed    string      `json:"failedRun,omitempty"` // latest run failed/canceled and can be resumed
}

func (a *App) summarize(p *core.Project) ProjectSummary {
	s := ProjectSummary{ID: p.ID, Name: p.Name, TemplateID: p.TemplateID, Description: p.Description, ActiveEnv: p.ActiveEnv,
		Practice: p.Practice, Imported: p.Imported, Providers: p.Blueprint.Providers(), Monitor: p.Monitor, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if t, ok := blueprints.Get(p.TemplateID); ok {
		s.TemplateName, s.Industry, s.Level = t.Name, t.Industry, t.Level
	} else if p.Imported {
		s.TemplateName, s.Industry = "Imported backend", "imported"
	}
	for _, env := range sortedEnvs(p) {
		s.Environments = append(s.Environments, a.envSummary(p, env))
	}
	return s
}

func (a *App) envSummary(p *core.Project, env string) EnvSummary {
	es := EnvSummary{Name: env, Overall: core.HealthUnknown}
	if man, err := a.Store.LoadManifest(p.ID, env); err == nil {
		for _, st := range man.Resources {
			if st.Status != core.StateDeleted {
				es.Resources++
			}
		}
		es.Built = es.Resources > 0
		es.LastBuild = man.LastBuild
	}
	if h, _ := a.Store.History(p.ID, env, 1); len(h) > 0 {
		at := h[0].At
		es.Overall, es.Headline, es.CheckedAt = h[0].Overall, h[0].Headline, &at
	} else if es.Built {
		es.Headline = "NOT CHECKED YET"
	} else {
		es.Headline = "NOT BUILT YET"
	}
	for _, prov := range p.Blueprint.Providers() {
		if p.Connections[env][prov] == "" {
			es.Missing = append(es.Missing, prov)
		}
	}
	if runs, _ := a.Store.ListRuns(p.ID, env); len(runs) > 0 {
		r := runs[0]
		switch r.Status {
		case core.RunRunning, core.RunRollingBack:
			es.Running = r.ID
		case core.RunFailed, core.RunCanceled:
			es.Failed = r.ID
		}
	}
	return es
}

// ListProjects returns project cards, most recently changed first.
func (a *App) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	ps, err := a.Store.ListProjects()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProjectSummary{}, nil
		}
		return nil, err
	}
	out := make([]ProjectSummary, 0, len(ps))
	for _, p := range ps {
		out = append(out, a.summarize(p))
	}
	return out, nil
}

// ProjectDetail is a project with its template and card summary.
type ProjectDetail struct {
	Project  *core.Project        `json:"project"`
	Summary  ProjectSummary       `json:"summary"`
	Template *blueprints.Template `json:"template,omitempty"`
}

// GetProject loads one project.
func (a *App) GetProject(ctx context.Context, p IDParams) (*ProjectDetail, error) {
	pr, err := a.project(p.ID)
	if err != nil {
		return nil, err
	}
	d := &ProjectDetail{Project: pr, Summary: a.summarize(pr)}
	if t, ok := blueprints.Get(pr.TemplateID); ok {
		d.Template = &t
	}
	return d, nil
}

func (a *App) project(id string) (*core.Project, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("no project selected")
	}
	p, err := a.Store.LoadProject(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("project not found")
		}
		return nil, err
	}
	if p.Connections == nil {
		p.Connections = map[string]map[string]string{}
	}
	return p, nil
}

func (a *App) envOf(p *core.Project, env string) (string, error) {
	if env == "" {
		env = p.ActiveEnv
	}
	for _, e := range p.Environments {
		if e == env {
			return env, nil
		}
	}
	return "", fmt.Errorf("%s has no %q environment", p.Name, env)
}

// CreateProjectParams starts a project from a preset.
type CreateProjectParams struct {
	Name         string             `json:"name"`
	TemplateID   string             `json:"templateId"`
	Answers      blueprints.Answers `json:"answers"`
	Environments []string           `json:"environments"`
	Practice     bool               `json:"practice"`
	Description  string             `json:"description"`
}

// CreateProjectResult is the new project plus what still needs doing.
type CreateProjectResult struct {
	Project ProjectSummary `json:"project"`
	Missing []string       `json:"missingConnections"` // providers with no connection yet
	Files   int            `json:"files"`
}

var envNameRE = func(s string) bool {
	if len(s) < 2 || len(s) > 24 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// CreateProject builds the blueprint for a preset, assigns sensible
// connections per environment and writes the generated code. Nothing is
// created at any provider until a plan is approved.
func (a *App) CreateProject(ctx context.Context, p CreateProjectParams) (*CreateProjectResult, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 60 {
		return nil, fmt.Errorf("give the project a name (up to 60 characters)")
	}
	t, ok := blueprints.Get(p.TemplateID)
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", p.TemplateID)
	}
	existing, _ := a.Store.ListProjects()
	for _, x := range existing {
		if strings.EqualFold(x.Name, name) {
			return nil, fmt.Errorf("a project called %q already exists", name)
		}
	}
	envs := normalizeEnvs(p.Environments)
	if len(envs) == 0 {
		return nil, fmt.Errorf("choose at least one environment")
	}
	answers := blueprints.Answers{}
	for k, v := range p.Answers {
		answers[k] = v
	}
	if _, ok := answers["slug"]; !ok {
		answers["slug"] = uniqueSlug(blueprints.Slug(name), existing)
	}
	bp, err := blueprints.Build(t, name, answers)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	pr := &core.Project{ID: projectID(name), Name: name, TemplateID: t.ID, Description: strings.TrimSpace(p.Description), Environments: envs,
		ActiveEnv: envs[len(envs)-1], Blueprint: bp, Practice: p.Practice, CreatedAt: now, Connections: map[string]map[string]string{},
		Monitor: core.DefaultMonitor(), Answers: answers}
	if p.Practice {
		pr.Monitor.QuickEveryMin = 15
	}
	for _, env := range envs {
		pr.Connections[env] = map[string]string{}
		for _, prov := range bp.Providers() {
			if id := a.pickConnection(prov, env, p.Practice); id != "" {
				pr.Connections[env][prov] = id
			}
		}
	}
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	written, _, err := a.writeGenerated(pr, nil)
	if err != nil {
		return nil, err
	}
	a.log("info", pr.ID, "", fmt.Sprintf("Created project %s from the %s preset (%d files generated)", name, t.Name, len(written)))
	res := &CreateProjectResult{Project: a.summarize(pr), Files: len(written)}
	for _, prov := range bp.Providers() {
		if pr.Connections[envs[0]][prov] == "" {
			res.Missing = append(res.Missing, prov)
		}
	}
	return res, nil
}

func normalizeEnvs(in []string) []string {
	if len(in) == 0 {
		return []string{core.EnvProduction}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "prod" {
			e = core.EnvProduction
		}
		if e == "dev" {
			e = core.EnvDevelopment
		}
		if !envNameRE(e) || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	// Keep a predictable order: development, staging, custom…, production.
	rank := func(e string) int {
		switch e {
		case core.EnvDevelopment:
			return 0
		case core.EnvStaging:
			return 1
		case core.EnvProduction:
			return 3
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func uniqueSlug(base string, existing []*core.Project) string {
	used := map[string]bool{}
	for _, p := range existing {
		used[p.Blueprint.Param("slug")] = true
	}
	s := base
	for i := 2; used[s]; i++ {
		s = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 21)], "-"), i)
	}
	return s
}

func projectID(name string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return blueprints.Slug(name) + "-" + hex.EncodeToString(b)
}

// UpdateProjectParams changes a project. Answers are merged into the saved
// answers and the blueprint is rebuilt; generated code is refreshed except
// files the user edited.
type UpdateProjectParams struct {
	ID          string                `json:"id"`
	Name        *string               `json:"name,omitempty"`
	Description *string               `json:"description,omitempty"`
	Answers     blueprints.Answers    `json:"answers,omitempty"`
	Monitor     *core.MonitorSettings `json:"monitor,omitempty"`
	ActiveEnv   string                `json:"activeEnv,omitempty"`
}

// UpdateProjectResult reports what changed.
type UpdateProjectResult struct {
	Project ProjectSummary `json:"project"`
	Rebuilt bool           `json:"rebuilt"`
	Kept    []string       `json:"keptUserFiles,omitempty"` // user-edited files not overwritten
}

// UpdateProject saves settings and, when answers change, rebuilds the
// blueprint (the next plan shows exactly what that changes).
func (a *App) UpdateProject(ctx context.Context, p UpdateProjectParams) (*UpdateProjectResult, error) {
	pr, err := a.project(p.ID)
	if err != nil {
		return nil, err
	}
	res := &UpdateProjectResult{}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" || len(n) > 60 {
			return nil, fmt.Errorf("the name must be 1–60 characters")
		}
		pr.Name = n
	}
	if p.Description != nil {
		pr.Description = strings.TrimSpace(*p.Description)
	}
	if p.Monitor != nil {
		m := *p.Monitor
		if m.QuickEveryMin != 0 && m.QuickEveryMin < 5 {
			m.QuickEveryMin = 5
		}
		if m.FullEveryHours != 0 && m.FullEveryHours < 1 {
			m.FullEveryHours = 1
		}
		pr.Monitor = m
	}
	if p.ActiveEnv != "" {
		env, err := a.envOf(pr, p.ActiveEnv)
		if err != nil {
			return nil, err
		}
		pr.ActiveEnv = env
	}
	if len(p.Answers) > 0 && !pr.Imported {
		t, ok := blueprints.Get(pr.TemplateID)
		if !ok {
			return nil, fmt.Errorf("unknown preset %q", pr.TemplateID)
		}
		answers := blueprints.Answers{}
		for k, v := range pr.Answers {
			answers[k] = v
		}
		for k, v := range p.Answers {
			if k == "slug" {
				continue // resource names never change after creation
			}
			answers[k] = v
		}
		if _, ok := answers["slug"]; !ok {
			answers["slug"] = pr.Blueprint.Param("slug")
		}
		bp, err := blueprints.Build(t, pr.Name, answers)
		if err != nil {
			return nil, err
		}
		pr.Blueprint, pr.Answers = bp, answers
		for _, env := range pr.Environments {
			if pr.Connections[env] == nil {
				pr.Connections[env] = map[string]string{}
			}
			for _, prov := range bp.Providers() {
				if pr.Connections[env][prov] == "" {
					if id := a.pickConnection(prov, env, pr.Practice); id != "" {
						pr.Connections[env][prov] = id
					}
				}
			}
		}
		res.Rebuilt = true
	}
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	if res.Rebuilt {
		_, kept, err := a.writeGenerated(pr, nil)
		if err != nil {
			return nil, err
		}
		res.Kept = kept
		a.log("info", pr.ID, "", "Settings changed; blueprint rebuilt. Plan the build to review the changes.")
	}
	a.dropPlans(pr.ID)
	res.Project = a.summarize(pr)
	return res, nil
}

// DeleteProjectParams removes Backplane's local records for a project.
type DeleteProjectParams struct {
	ID      string `json:"id"`
	Confirm string `json:"confirm"`
}

// DeleteProject forgets a project. Remote resources are never deleted here
// (use Tear down for that); built projects require typing the name.
func (a *App) DeleteProject(ctx context.Context, p DeleteProjectParams) (bool, error) {
	pr, err := a.project(p.ID)
	if err != nil {
		return false, err
	}
	built := false
	for _, env := range pr.Environments {
		if man, err := a.Store.LoadManifest(pr.ID, env); err == nil && len(man.Resources) > 0 {
			built = true
		}
		if runs, _ := a.Store.ListRuns(pr.ID, env); len(runs) > 0 && (runs[0].Status == core.RunRunning || runs[0].Status == core.RunRollingBack) {
			return false, fmt.Errorf("a build is running in %s — cancel it first", env)
		}
	}
	if built && strings.TrimSpace(p.Confirm) != pr.Name {
		return false, fmt.Errorf("%w: type %q to remove this project from Backplane (its live resources stay where they are)", engine.ErrConfirm, pr.Name)
	}
	_ = a.Vault.DeletePrefix("p/" + pr.ID + "/")
	a.dropPlans(pr.ID)
	if err := a.Store.DeleteProject(pr.ID); err != nil {
		return false, err
	}
	a.log("info", "", "", "Removed project "+pr.Name+" from Backplane (live resources were not touched).")
	return true, nil
}

// AssignConnectionParams picks which saved account an environment uses.
type AssignConnectionParams struct {
	ProjectID    string `json:"projectId"`
	Env          string `json:"env"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connectionId"` // "" unassigns
}

// AssignConnectionResult carries separation warnings.
type AssignConnectionResult struct {
	Project  ProjectSummary `json:"project"`
	Warnings []string       `json:"warnings,omitempty"`
}

// AssignConnection links a connection to a project environment and checks
// environment separation (test keys in production, one account shared by
// production and development).
func (a *App) AssignConnection(ctx context.Context, p AssignConnectionParams) (*AssignConnectionResult, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	res := &AssignConnectionResult{}
	if pr.Connections[env] == nil {
		pr.Connections[env] = map[string]string{}
	}
	if p.ConnectionID == "" {
		delete(pr.Connections[env], p.Provider)
	} else {
		c, err := a.Engine.Connection(p.ConnectionID)
		if err != nil {
			return nil, err
		}
		if c.Provider != p.Provider {
			return nil, fmt.Errorf("that is a %s connection, not %s", providers.DisplayName(c.Provider), providers.DisplayName(p.Provider))
		}
		if c.Practice != pr.Practice {
			if pr.Practice {
				return nil, fmt.Errorf("practice projects use practice connections only — real accounts are never touched from practice mode")
			}
			return nil, fmt.Errorf("practice connections only work in practice projects")
		}
		if man, _ := a.Store.LoadManifest(pr.ID, env); man != nil && len(man.Resources) > 0 {
			if old := pr.Connections[env][p.Provider]; old != "" && old != c.ID {
				if oc, err := a.Engine.Connection(old); err == nil && oc.AccountID != "" && c.AccountID != "" && oc.AccountID != c.AccountID {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s in %s is already built in another account (%s). The next plan will create everything again in %s; the old resources stay where they are.",
						providers.DisplayName(p.Provider), env, orStr(oc.AccountName, oc.AccountID), orStr(c.AccountName, c.AccountID)))
				}
			}
		}
		if p.Provider == "stripe" {
			switch {
			case core.IsProduction(env) && c.Mode == "test":
				res.Warnings = append(res.Warnings, "Production is using a Stripe TEST key: no real payments will be taken until you switch to a live key.")
			case !core.IsProduction(env) && c.Mode == "live":
				res.Warnings = append(res.Warnings, "A LIVE Stripe key in "+env+" can charge real cards. Use a test key outside production.")
			}
		}
		for other, links := range pr.Connections {
			if other != env && links[p.Provider] == c.ID && (core.IsProduction(env) || core.IsProduction(other)) && c.Provider != "github" && c.Provider != "cloudflare" && c.Provider != "resend" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s and %s share the same %s account. Separate accounts (or projects) keep test data out of production.", env, other, providers.DisplayName(c.Provider)))
			}
		}
		pr.Connections[env][p.Provider] = c.ID
	}
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	a.dropPlans(pr.ID)
	res.Project = a.summarize(pr)
	return res, nil
}

// AddEnvironmentParams adds (or clones) an environment.
type AddEnvironmentParams struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	CloneFrom string `json:"cloneFrom"` // copy connection choices from this environment
}

// AddEnvironment adds an environment. The blueprint is shared, so a clone is
// the same backend with its own resources (names carry the environment) and
// its own accounts where the user chooses.
func (a *App) AddEnvironment(ctx context.Context, p AddEnvironmentParams) (*ProjectSummary, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(p.Name))
	if !envNameRE(name) {
		return nil, fmt.Errorf("environment names use 2–24 lowercase letters, digits or dashes")
	}
	for _, e := range pr.Environments {
		if e == name {
			return nil, fmt.Errorf("%s already has a %s environment", pr.Name, name)
		}
	}
	for _, e := range pr.Environments {
		if engine.EnvShort(e) == engine.EnvShort(name) {
			return nil, fmt.Errorf("%q would share resource names with %q — choose a different name", name, e)
		}
	}
	pr.Environments = normalizeEnvs(append(pr.Environments, name))
	pr.Connections[name] = map[string]string{}
	src := pr.Connections[p.CloneFrom]
	for _, prov := range pr.Blueprint.Providers() {
		id := ""
		if src != nil && src[prov] != "" && prov != "stripe" {
			id = src[prov]
		}
		if id == "" {
			id = a.pickConnection(prov, name, pr.Practice)
		}
		if id != "" {
			pr.Connections[name][prov] = id
		}
	}
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	a.log("info", pr.ID, name, "Added environment "+name)
	s := a.summarize(pr)
	return &s, nil
}

// ---- dashboard ----

// DashboardParams selects a project environment.
type DashboardParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
}

// ProviderStatus is one account tile on the dashboard.
type ProviderStatus struct {
	Provider     string      `json:"provider"`
	Name         string      `json:"name"`
	ConnectionID string      `json:"connectionId,omitempty"`
	Label        string      `json:"label,omitempty"`
	Account      string      `json:"account,omitempty"`
	Mode         string      `json:"mode,omitempty"`
	Status       core.Health `json:"status"`
	Note         string      `json:"note,omitempty"`
	AddOn        bool        `json:"addOn,omitempty"`
	Maturity     string      `json:"maturity"`
}

// DashboardResult is everything on the project screen.
type DashboardResult struct {
	Project     ProjectSummary          `json:"project"`
	Env         string                  `json:"env"`
	Blueprint   core.Blueprint          `json:"blueprint"`
	Manifest    *core.Manifest          `json:"manifest"`
	Report      *core.HealthReport      `json:"report,omitempty"`
	History     []core.HistoryEntry     `json:"history"`
	Runs        []*core.Run             `json:"runs"`
	Providers   []ProviderStatus        `json:"providers"`
	Costs       []core.CostLine         `json:"costs"`
	Code        []CodeFile              `json:"code"`
	Monitor     MonitorState            `json:"monitor"`
	Checking    bool                    `json:"checking"`
	Template    *blueprints.Template    `json:"template,omitempty"`
	Capability  []core.CapabilityHealth `json:"capabilities"`
	Uptime      map[string]float64      `json:"uptime"` // "24h", "7d": share of OK checks
	LevelNames  map[int]string          `json:"levelNames"`
	Resources   []ResourceRow           `json:"resources"`
	Secrets     int                     `json:"secrets"`
	Environment map[string]core.Health  `json:"environments"`
	Extra       map[string]string       `json:"extra,omitempty"`
	// Connections is environment -> provider -> connection id.
	Connections map[string]map[string]string `json:"connections"`
	// Answers are the preset answers the blueprint was built from.
	Answers map[string]any `json:"answers"`
}

// ResourceRow is one resource in the dashboard's inventory table.
type ResourceRow struct {
	Key       string            `json:"key"`
	Title     string            `json:"title"`
	Kind      string            `json:"kind"`
	Provider  string            `json:"provider"`
	Component string            `json:"component"`
	ID        string            `json:"id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Status    string            `json:"status"` // planned | ready | creating | failed | imported
	Health    core.Health       `json:"health"`
	Outputs   map[string]string `json:"outputs,omitempty"`
	CreatedBy string            `json:"createdBy,omitempty"`
	Note      string            `json:"note,omitempty"`
}

// Dashboard loads the project screen.
func (a *App) Dashboard(ctx context.Context, p DashboardParams) (*DashboardResult, error) {
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
	d := &DashboardResult{Project: a.summarize(pr), Env: env, Blueprint: pr.Blueprint, Manifest: man, LevelNames: core.LevelNames,
		Costs: engine.EstimateCostsFor(pr), Uptime: map[string]float64{}, Environment: map[string]core.Health{},
		Connections: pr.Connections, Answers: pr.Answers}
	if t, ok := blueprints.Get(pr.TemplateID); ok {
		d.Template = &t
	}
	hist, _ := a.Store.History(pr.ID, env, 400)
	d.History = hist
	if len(hist) > 0 && hist[0].ReportID != "" {
		d.Report, _ = a.Store.LoadReport(pr.ID, env, hist[0].ReportID)
	}
	if d.Report != nil {
		d.Capability = d.Report.Capabilities
	}
	d.Uptime["24h"] = uptime(hist, 24*time.Hour)
	d.Uptime["7d"] = uptime(hist, 7*24*time.Hour)
	d.Uptime["30d"] = uptime(hist, 30*24*time.Hour)
	runs, _ := a.Store.ListRuns(pr.ID, env)
	if len(runs) > 8 {
		runs = runs[:8]
	}
	d.Runs = runs
	d.Providers = a.providerStatus(pr, env)
	d.Code, _ = a.codeFiles(pr)
	d.Monitor = a.monitor.state(pr, env)
	d.Checking = a.Engine.CheckRunning(pr.ID, env)
	d.Resources = resourceRows(pr, man, d.Report)
	d.Secrets = len(a.Vault.List("p/" + pr.ID + "/" + env + "/"))
	for _, e := range pr.Environments {
		if h, _ := a.Store.History(pr.ID, e, 1); len(h) > 0 {
			d.Environment[e] = h[0].Overall
		} else {
			d.Environment[e] = core.HealthUnknown
		}
	}
	return d, nil
}

func uptime(hist []core.HistoryEntry, window time.Duration) float64 {
	cutoff := time.Now().Add(-window)
	total, ok := 0, 0
	for _, h := range hist {
		if h.At.Before(cutoff) || h.Overall == core.HealthUnknown {
			continue
		}
		total++
		if h.Overall == core.HealthOK || h.Overall == core.HealthWarn {
			ok++
		}
	}
	if total == 0 {
		return -1
	}
	return float64(ok) / float64(total)
}

func (a *App) providerStatus(pr *core.Project, env string) []ProviderStatus {
	var out []ProviderStatus
	for _, prov := range pr.Blueprint.Providers() {
		ps := ProviderStatus{Provider: prov, Name: providers.DisplayName(prov), Status: core.HealthUnknown, AddOn: !providerHasResources(&pr.Blueprint, prov)}
		if pv, ok := a.Reg.Provider(prov); ok {
			ps.Maturity = pv.Info().Maturity
		}
		if id := pr.Connections[env][prov]; id != "" {
			if c, err := a.Engine.Connection(id); err == nil {
				ps.ConnectionID, ps.Label, ps.Account, ps.Mode, ps.Status, ps.Note = c.ID, c.Label, orStr(c.AccountName, c.AccountID), c.Mode, c.Status, c.StatusNote
			} else {
				ps.Status, ps.Note = core.HealthFail, "The assigned connection was deleted."
			}
		} else {
			ps.Note = "Not connected"
			if !ps.AddOn {
				ps.Status = core.HealthFail
			}
		}
		out = append(out, ps)
	}
	return out
}

func providerHasResources(bp *core.Blueprint, prov string) bool {
	for _, r := range bp.Resources {
		if r.Provider == prov {
			return true
		}
	}
	return false
}

func resourceRows(pr *core.Project, man *core.Manifest, rep *core.HealthReport) []ResourceRow {
	health := map[string]core.Health{}
	if rep != nil {
		for _, r := range rep.Results {
			key := r.Details["resource"]
			if key == "" {
				continue
			}
			if cur, ok := health[key]; ok {
				health[key] = core.Worst(cur, r.Health)
			} else {
				health[key] = r.Health
			}
		}
	}
	var out []ResourceRow
	seen := map[string]bool{}
	for _, spec := range pr.Blueprint.Resources {
		row := ResourceRow{Key: spec.Key, Title: spec.Title, Kind: spec.Kind, Provider: spec.Provider, Component: spec.Component, Status: "planned", Health: core.HealthUnknown}
		if st := man.Resources[spec.Key]; st != nil {
			row.ID, row.Name, row.Status, row.Outputs, row.CreatedBy, row.Note = st.ID, st.Name, st.Status, st.Outputs, st.CreatedBy, st.Note
			if h, ok := health[spec.Key]; ok {
				row.Health = h
			}
		}
		seen[spec.Key] = true
		out = append(out, row)
	}
	for key, st := range man.Resources {
		if seen[key] {
			continue
		}
		out = append(out, ResourceRow{Key: key, Title: st.Kind + " " + st.Name, Kind: st.Kind, Provider: st.Provider, ID: st.ID, Name: st.Name,
			Status: st.Status, Health: core.HealthUnknown, Outputs: st.Outputs, CreatedBy: st.CreatedBy, Note: "No longer in the blueprint — the next plan removes it"})
	}
	return out
}

func orStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
