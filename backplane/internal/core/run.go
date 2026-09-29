package core

import "time"

// Operation actions in a plan.
const (
	ActCreate   = "create"
	ActUpdate   = "update"
	ActReplace  = "replace" // resource vanished outside Backplane; create again
	ActKeep     = "keep"
	ActDelete   = "delete"
	ActAdopt    = "adopt" // take over an existing resource with the same name
	ActDetach   = "detach"
	ActValidate = "validate"
)

// Plan is what Backplane intends to do. Nothing runs until the user approves.
type Plan struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	CreatedAt   time.Time       `json:"createdAt"`
	Operations  []Operation     `json:"operations"`
	Summary     []PlanGroup     `json:"summary"`
	Connections []PlanLink      `json:"connections"`
	Costs       []CostLine      `json:"costs"`
	Warnings    []string        `json:"warnings,omitempty"`
	Blockers    []string        `json:"blockers,omitempty"` // must be fixed before approval
	Destructive bool            `json:"destructive"`
	Production  bool            `json:"production"`
	Counts      map[string]int  `json:"counts"` // action -> n
	Inputs      map[string]bool `json:"inputs,omitempty"`
	// Purpose is shown above the plan ("Repair: Recreate the webhook").
	Purpose string `json:"purpose,omitempty"`
	// Repair plans only touch the broken resources and what depends on them.
	Repair bool `json:"repair,omitempty"`
}

// PlanGroup is one provider block of the human summary.
type PlanGroup struct {
	Provider string   `json:"provider"`
	Action   string   `json:"action"` // CREATE / UPDATE / KEEP / DELETE
	Lines    []string `json:"lines"`
}

// PlanLink is one "Stripe → Worker" line in the plan.
type PlanLink struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

// CostLine is an honest estimate for one provider.
type CostLine struct {
	Provider string `json:"provider"`
	Estimate string `json:"estimate"` // "$0–5 / month"
	Basis    string `json:"basis"`    // what the estimate assumes
	Upgrade  string `json:"upgrade"`  // when a paid plan becomes necessary
	Source   string `json:"source"`   // where the pricing came from
}

// Operation is one deterministic infrastructure action.
type Operation struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`
	ResourceKey string   `json:"resourceKey"`
	Kind        string   `json:"kind"`
	Provider    string   `json:"provider"`
	Title       string   `json:"title"`
	Why         string   `json:"why,omitempty"`
	DependsOn   []string `json:"dependsOn,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
	Changes     []Change `json:"changes,omitempty"`
	// Force re-applies the resource even when its saved state says it is up
	// to date (repairs of changes made outside Backplane).
	Force bool `json:"force,omitempty"`
	// Rotate replaces a credential-like resource (API key, webhook endpoint)
	// with a new one and retires the old one.
	Rotate bool           `json:"rotate,omitempty"`
	Props  map[string]any `json:"-"`
}

// Change is one property difference shown in the plan.
type Change struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// Run states.
const (
	RunPending     = "pending"
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed" // stopped on an error; can resume
	RunCanceled    = "canceled"
	RunRollingBack = "rolling_back"
	RunRolledBack  = "rolled_back"
)

// Operation states inside a run.
const (
	OpPending    = "pending"
	OpRunning    = "running"
	OpSucceeded  = "succeeded"
	OpFailed     = "failed"
	OpSkipped    = "skipped"
	OpRolledBack = "rolled_back"
	OpBlocked    = "blocked" // a dependency failed
)

// Run is one execution of a plan. It is checkpointed after every operation
// so a failure at step 17 of 30 resumes at 17, not 1.
type Run struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Plan        Plan       `json:"plan"`
	Status      string     `json:"status"`
	Ops         []OpState  `json:"ops"`
	StartedAt   time.Time  `json:"startedAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Checkpoint  string     `json:"checkpoint"` // snapshot id taken before the run
	Error       *Problem   `json:"error,omitempty"`
	Resumes     int        `json:"resumes"`
}

// OpState is the live state of one operation in a run.
type OpState struct {
	OpID       string     `json:"opId"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      *Problem   `json:"error,omitempty"`
	Note       string     `json:"note,omitempty"`
	// Created is true when this run created the resource, which makes it
	// eligible for rollback.
	Created bool `json:"created,omitempty"`
}

// Problem is an error translated for humans.
type Problem struct {
	Title      string   `json:"title"`   // "Stripe connection failed"
	Summary    string   `json:"summary"` // plain-English explanation
	Provider   string   `json:"provider,omitempty"`
	Code       string   `json:"code,omitempty"` // auth, permission, not_found, rate_limit, conflict, invalid, server, network, timeout, drift
	HTTPStatus int      `json:"httpStatus,omitempty"`
	Technical  string   `json:"technical,omitempty"` // redacted raw detail for the Advanced view
	Affected   []string `json:"affected,omitempty"`
	Unaffected []string `json:"unaffected,omitempty"`
	Fixes      []Fix    `json:"fixes,omitempty"`
	Retryable  bool     `json:"retryable,omitempty"`
}

func (p *Problem) Error() string {
	if p == nil {
		return ""
	}
	if p.Summary != "" {
		return p.Title + ": " + p.Summary
	}
	return p.Title
}

// Fix is a suggested repair. Automatic fixes must describe exactly what they
// change before they run.
type Fix struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Explain     string   `json:"explain"`
	Changes     []string `json:"changes,omitempty"`
	Automatic   bool     `json:"automatic"`
	Destructive bool     `json:"destructive,omitempty"`
	Link        string   `json:"link,omitempty"` // provider dashboard or guide link for manual fixes
	Action      string   `json:"action,omitempty"`
	Target      string   `json:"target,omitempty"`
}

// Snapshot is a restorable copy of a manifest plus the specs that produced it.
type Snapshot struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"createdAt"`
	Manifest    Manifest  `json:"manifest"`
	Blueprint   Blueprint `json:"blueprint"`
	KnownGood   bool      `json:"knownGood"`
}
