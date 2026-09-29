package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Health is the traffic-light state of anything Backplane can test.
type Health string

const (
	HealthUnknown Health = "unknown" // never tested, or result is stale
	HealthOK      Health = "ok"      // green
	HealthWarn    Health = "warn"    // yellow: works, but needs attention
	HealthFail    Health = "fail"    // red: broken
	HealthSkipped Health = "skipped" // intentionally not run (e.g. live-mode payment test)
)

// Rank orders health from best to worst so aggregation can take the worst.
func (h Health) Rank() int {
	switch h {
	case HealthOK:
		return 1
	case HealthSkipped:
		return 0
	case HealthUnknown:
		return 2
	case HealthWarn:
		return 3
	case HealthFail:
		return 4
	}
	return 2
}

// Worst returns the most severe of the given states. Skipped never outranks a
// real result; an empty set is unknown.
func Worst(states ...Health) Health {
	out := HealthSkipped
	seen := false
	for _, s := range states {
		if s == "" {
			continue
		}
		if !seen || s.Rank() > out.Rank() {
			out = s
		}
		seen = true
	}
	if !seen {
		return HealthUnknown
	}
	return out
}

// Environment names. Custom environments are allowed; these three get
// special safety treatment.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// IsProduction reports whether destructive actions need the strongest
// confirmation in this environment.
func IsProduction(env string) bool {
	e := strings.ToLower(env)
	return e == EnvProduction || e == "prod" || e == "live"
}

// Project is one backend the user manages. It owns a blueprint (the desired
// architecture) and one manifest per environment (what actually exists).
type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	TemplateID   string    `json:"templateId"`
	Description  string    `json:"description,omitempty"`
	Environments []string  `json:"environments"`
	ActiveEnv    string    `json:"activeEnv"`
	Blueprint    Blueprint `json:"blueprint"`
	Imported     bool      `json:"imported,omitempty"`
	Practice     bool      `json:"practice,omitempty"` // built against the simulator
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// Connections maps environment -> provider -> connection ID. Keeping this
	// per environment is what makes "production Stripe must never use the
	// development database" checkable.
	Connections map[string]map[string]string `json:"connections"`
	Monitor     MonitorSettings              `json:"monitor"`
	// Answers are the guided-setup replies the blueprint was built from, so
	// it can be rebuilt when the user changes a setting.
	Answers map[string]any `json:"answers,omitempty"`
}

// MonitorSettings controls continuous checking for a project.
type MonitorSettings struct {
	Enabled         bool `json:"enabled"`
	QuickEveryMin   int  `json:"quickEveryMin"`   // lightweight L1-L4 checks
	FullEveryHours  int  `json:"fullEveryHours"`  // includes synthetic + end-to-end tests
	CheckAfterBuild bool `json:"checkAfterBuild"` // run a full check after every build
	Notify          bool `json:"notify"`          // desktop notification on status change
}

// DefaultMonitor is what new projects start with.
func DefaultMonitor() MonitorSettings {
	return MonitorSettings{Enabled: true, QuickEveryMin: 30, FullEveryHours: 24, CheckAfterBuild: true, Notify: true}
}

// Blueprint is the desired backend: components the user sees, resources that
// get provisioned, the links between components and the scenarios that prove
// the whole thing works.
type Blueprint struct {
	Version    int                    `json:"version"`
	TemplateID string                 `json:"templateId"`
	Params     map[string]any         `json:"params"`
	Components []Component            `json:"components"`
	Resources  []ResourceSpec         `json:"resources"`
	Links      []LinkSpec             `json:"links"`
	Scenarios  []ScenarioSpec         `json:"scenarios,omitempty"`
	Generated  []GeneratedFileSpec    `json:"generated,omitempty"`
	Notes      []string               `json:"notes,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

// Component is a box on the architecture map ("Stripe", "Worker API").
type Component struct {
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Role       string     `json:"role"` // short plain-English job, e.g. "Takes payments"
	Capability Capability `json:"capability"`
	Provider   string     `json:"provider"` // "" for external actors such as the customer
	External   bool       `json:"external,omitempty"`
	Resources  []string   `json:"resources,omitempty"` // resource keys that make up this component
	Order      int        `json:"order"`               // vertical position in the rack (0 = top)
	// Breaks lists the plain-English features that stop working when this
	// component fails ("Payments", "Refunds"). It drives Affected/Unaffected.
	Breaks []string `json:"breaks,omitempty"`
}

// ResourceSpec is one thing a provider must create or keep configured.
type ResourceSpec struct {
	Key       string         `json:"key"`
	Kind      string         `json:"kind"` // e.g. "cloudflare.worker"
	Provider  string         `json:"provider"`
	Component string         `json:"component"`
	Name      string         `json:"name"`
	Title     string         `json:"title"` // plain-English line for the plan
	Props     map[string]any `json:"props,omitempty"`
	DependsOn []string       `json:"dependsOn,omitempty"`
	// Adopt lets the planner take over an existing resource with the same
	// name instead of failing with a conflict (used for accounts that
	// already have, say, a bucket called "downloads").
	Adopt bool `json:"adopt,omitempty"`
	// Keep marks resources that must survive a rollback (for example an
	// adopted, pre-existing Supabase project).
	Keep bool `json:"keep,omitempty"`
	// Count is how the plan summary counts this resource ("6 tables").
	Count []CountItem `json:"count,omitempty"`
}

// CountItem feeds the human plan summary: "Supabase — 6 tables, 14 policies".
type CountItem struct {
	N    int    `json:"n"`
	Noun string `json:"noun"`
}

// LinkSpec is a connection between two components; every link is testable.
type LinkSpec struct {
	Key      string   `json:"key"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Label    string   `json:"label"`    // "checkout.session.completed", "SQL", "binding"
	Kind     string   `json:"kind"`     // webhook, binding, http, sql, smtp, deploy, dns, email
	Check    string   `json:"check"`    // verification check that proves this link
	Critical bool     `json:"critical"` // failing it breaks the main customer flow
	Breaks   []string `json:"breaks,omitempty"`
}

// ScenarioSpec is an end-to-end test (Level 6).
type ScenarioSpec struct {
	Key   string   `json:"key"`
	Title string   `json:"title"`
	Steps []string `json:"steps"`
	Check string   `json:"check"`
}

// GeneratedFileSpec is code or config Backplane writes for the user.
type GeneratedFileSpec struct {
	Path     string `json:"path"`
	Role     string `json:"role"` // generated | system-config
	Language string `json:"language"`
}

// ResourceByKey finds a resource spec in the blueprint.
func (b *Blueprint) ResourceByKey(key string) *ResourceSpec {
	for i := range b.Resources {
		if b.Resources[i].Key == key {
			return &b.Resources[i]
		}
	}
	return nil
}

// ComponentByKey finds a component in the blueprint.
func (b *Blueprint) ComponentByKey(key string) *Component {
	for i := range b.Components {
		if b.Components[i].Key == key {
			return &b.Components[i]
		}
	}
	return nil
}

// Providers lists the providers the blueprint needs, sorted.
func (b *Blueprint) Providers() []string {
	set := map[string]bool{}
	for _, r := range b.Resources {
		set[r.Provider] = true
	}
	for _, c := range b.Components {
		if c.Provider != "" {
			set[c.Provider] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Param reads a blueprint parameter as a string.
func (b *Blueprint) Param(key string) string {
	if b.Params == nil {
		return ""
	}
	switch v := b.Params[key].(type) {
	case string:
		return v
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(v), ".0"), ".")
	case int:
		return jsonNumber(float64(v))
	case bool:
		if v {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		bs, _ := json.Marshal(v)
		return string(bs)
	}
}

func jsonNumber(f float64) string {
	bs, _ := json.Marshal(f)
	return string(bs)
}

// Manifest records what exists for one project in one environment. It never
// contains raw secrets — only references into the encrypted vault.
type Manifest struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Project       string                    `json:"project"`
	Environment   string                    `json:"environment"`
	Providers     map[string]ProviderLink   `json:"providers"`
	Resources     map[string]*ResourceState `json:"resources"`
	Generated     map[string]GeneratedFile  `json:"generated,omitempty"`
	LastBuild     *time.Time                `json:"lastBuild,omitempty"`
	LastFullCheck *time.Time                `json:"lastFullCheck,omitempty"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
}

// ProviderLink records which saved connection an environment uses.
type ProviderLink struct {
	ConnectionID string    `json:"connectionId"`
	AccountID    string    `json:"accountId,omitempty"`
	AccountName  string    `json:"accountName,omitempty"`
	Mode         string    `json:"mode,omitempty"` // "test" or "live" for payment providers
	Status       Health    `json:"status"`
	CheckedAt    time.Time `json:"checkedAt,omitempty"`
}

// NewManifest returns an empty manifest.
func NewManifest(project, env string) *Manifest {
	return &Manifest{SchemaVersion: 1, Project: project, Environment: env,
		Providers: map[string]ProviderLink{}, Resources: map[string]*ResourceState{},
		Generated: map[string]GeneratedFile{}}
}

// Resource states.
const (
	StateCreating = "creating" // create was sent; confirm before assuming success
	StateReady    = "ready"
	StateFailed   = "failed"
	StateDeleted  = "deleted"
	StateImported = "imported" // discovered, not created by Backplane
)

// ResourceState is what Backplane knows about one live resource.
type ResourceState struct {
	Key        string            `json:"key"`
	Kind       string            `json:"kind"`
	Provider   string            `json:"provider"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Outputs    map[string]string `json:"outputs,omitempty"`    // non-secret outputs (urls, ids)
	SecretRefs map[string]string `json:"secretRefs,omitempty"` // output name -> vault key
	Applied    map[string]any    `json:"applied,omitempty"`    // non-secret props last applied
	Hash       string            `json:"hash"`                 // hash of desired props when last applied
	Status     string            `json:"status"`
	CreatedBy  string            `json:"createdBy"` // backplane | imported
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	Note       string            `json:"note,omitempty"`
}

// Output returns a non-secret output value.
func (r *ResourceState) Output(name string) string {
	if r == nil || r.Outputs == nil {
		return ""
	}
	return r.Outputs[name]
}

// SetOutput stores a non-secret output.
func (r *ResourceState) SetOutput(name, value string) {
	if r.Outputs == nil {
		r.Outputs = map[string]string{}
	}
	r.Outputs[name] = value
}

// GeneratedFile tracks generated code so user edits are never overwritten
// silently.
type GeneratedFile struct {
	Path          string    `json:"path"`
	Role          string    `json:"role"` // generated | user-modified | system-config
	Version       int       `json:"version"`
	GeneratedHash string    `json:"generatedHash"` // hash of what Backplane last wrote
	CurrentHash   string    `json:"currentHash"`   // hash of what is on disk now
	UpdatedAt     time.Time `json:"updatedAt"`
}

// HashProps gives a stable hash of a property map (keys sorted by encoding/json).
func HashProps(v any) string {
	bs, _ := json.Marshal(v)
	sum := sha256.Sum256(bs)
	return hex.EncodeToString(sum[:])[:16]
}

// HashBytes hashes content for generated-file tracking.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Connection is a saved provider credential set. The secret itself lives in
// the vault under SecretRef.
type Connection struct {
	ID          string            `json:"id"`
	Provider    string            `json:"provider"`
	Label       string            `json:"label"`
	AuthMethod  string            `json:"authMethod"` // token | oauth
	SecretRef   string            `json:"secretRef"`
	Settings    map[string]string `json:"settings,omitempty"` // account id, org slug, mode...
	AccountID   string            `json:"accountId,omitempty"`
	AccountName string            `json:"accountName,omitempty"`
	Mode        string            `json:"mode,omitempty"` // test | live where relevant
	Scopes      []string          `json:"scopes,omitempty"`
	Status      Health            `json:"status"`
	StatusNote  string            `json:"statusNote,omitempty"`
	Practice    bool              `json:"practice,omitempty"` // points at the simulator
	CreatedAt   time.Time         `json:"createdAt"`
	VerifiedAt  *time.Time        `json:"verifiedAt,omitempty"`
	// Warnings and Details are the latest verification notes (expiry dates,
	// over-broad keys, quotas) shown on the Connections and Security screens.
	Warnings []string          `json:"warnings,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
}

// Setting reads a non-secret connection setting.
func (c *Connection) Setting(key string) string {
	if c == nil || c.Settings == nil {
		return ""
	}
	return c.Settings[key]
}
