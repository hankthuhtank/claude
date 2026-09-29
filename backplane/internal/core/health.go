package core

import "time"

// Health check levels, matching the six-level green-light system.
const (
	LevelConnection = 1 // provider credentials, account, scopes, reachability
	LevelExistence  = 2 // every expected resource still exists
	LevelConfig     = 3 // live configuration matches expected (drift)
	LevelLinks      = 4 // connections between services actually work
	LevelSynthetic  = 5 // safe functional tests with automatic cleanup
	LevelEndToEnd   = 6 // the whole customer journey
)

// LevelNames are shown in the UI.
var LevelNames = map[int]string{
	LevelConnection: "Provider connection",
	LevelExistence:  "Resources exist",
	LevelConfig:     "Configuration matches",
	LevelLinks:      "Connections work",
	LevelSynthetic:  "Functions work",
	LevelEndToEnd:   "End-to-end journey",
}

// Check kinds for scheduling.
const (
	CheckQuick = "quick" // levels 1-4, no writes
	CheckFull  = "full"  // levels 1-6, synthetic data cleaned up
)

// CheckResult is the outcome of one test.
type CheckResult struct {
	ID         string            `json:"id"`
	Level      int               `json:"level"`
	Title      string            `json:"title"`
	Target     string            `json:"target"`     // component key or link key
	TargetType string            `json:"targetType"` // component | link | provider | resource | scenario
	Provider   string            `json:"provider,omitempty"`
	Health     Health            `json:"health"`
	Summary    string            `json:"summary"`
	Expected   string            `json:"expected,omitempty"`
	Actual     string            `json:"actual,omitempty"`
	LatencyMS  int64             `json:"latencyMs,omitempty"`
	Details    map[string]string `json:"details,omitempty"`
	Steps      []StepResult      `json:"steps,omitempty"`
	Problem    *Problem          `json:"problem,omitempty"`
	StartedAt  time.Time         `json:"startedAt"`
	DurationMS int64             `json:"durationMs"`
}

// StepResult is one step of a synthetic or end-to-end test.
type StepResult struct {
	Title     string `json:"title"`
	Health    Health `json:"health"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
}

// HealthReport is the result of a whole check run for one environment.
type HealthReport struct {
	ID          string            `json:"id"`
	Project     string            `json:"project"`
	Environment string            `json:"environment"`
	Kind        string            `json:"kind"`    // quick | full
	Trigger     string            `json:"trigger"` // manual | schedule | after-build | after-change
	StartedAt   time.Time         `json:"startedAt"`
	FinishedAt  time.Time         `json:"finishedAt"`
	Overall     Health            `json:"overall"`
	Headline    string            `json:"headline"` // "FULLY OPERATIONAL"
	Results     []CheckResult     `json:"results"`
	Components  map[string]Health `json:"components"`
	Links       map[string]Health `json:"links"`
	Levels      map[int]Health    `json:"levels"`
	Drift       []DriftItem       `json:"drift,omitempty"`
	Problems    []Problem         `json:"problems,omitempty"`
	// Capabilities is the "SYSTEM HEALTH" block: Payments, Webhooks, Database...
	Capabilities []CapabilityHealth `json:"capabilities"`
}

// CapabilityHealth is one line of the system-health summary.
type CapabilityHealth struct {
	Label  string `json:"label"`
	Health Health `json:"health"`
}

// DriftItem is a difference between expected and live configuration.
type DriftItem struct {
	Resource    string   `json:"resource"`
	Kind        string   `json:"kind"`
	Provider    string   `json:"provider"`
	Field       string   `json:"field"`
	Expected    string   `json:"expected"`
	Actual      string   `json:"actual"`
	Severity    Health   `json:"severity"`
	Breaks      []string `json:"breaks,omitempty"` // plain-English consequences
	Recommended string   `json:"recommended,omitempty"`
	FixID       string   `json:"fixId,omitempty"`
}

// HistoryEntry is one line of the stored health history.
type HistoryEntry struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Trigger  string    `json:"trigger"`
	Overall  Health    `json:"overall"`
	Headline string    `json:"headline"`
	Note     string    `json:"note,omitempty"` // "Stripe API version changed"
	ReportID string    `json:"reportId"`
}

// LogEntry is one line of the unified log. Messages are redacted before they
// are stored.
type LogEntry struct {
	At          time.Time `json:"at"`
	Level       string    `json:"level"` // debug info warn error
	Source      string    `json:"source"`
	Provider    string    `json:"provider,omitempty"`
	Resource    string    `json:"resource,omitempty"`
	Project     string    `json:"project,omitempty"`
	Environment string    `json:"environment,omitempty"`
	RequestID   string    `json:"requestId,omitempty"`
	RunID       string    `json:"runId,omitempty"`
	Message     string    `json:"message"`
	Detail      string    `json:"detail,omitempty"`
}
