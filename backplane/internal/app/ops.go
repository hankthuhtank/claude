package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/store"
)

// planTTL is how long an unapproved plan stays valid.
const planTTL = 30 * time.Minute

// PlanParams asks for a plan.
type PlanParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	// Offline previews the plan without reading live state.
	Offline bool `json:"offline"`
	// Teardown plans the removal of everything Backplane created.
	Teardown bool `json:"teardown"`
}

// Plan compares the blueprint with what exists and returns the work order.
// Nothing changes until it is approved.
func (a *App) Plan(ctx context.Context, p PlanParams) (*core.Plan, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	if id := a.runningRun(pr.ID, env); id != "" {
		return nil, fmt.Errorf("a build is already running in %s — wait for it or cancel it first", env)
	}
	plan, err := a.Engine.Plan(ctx, pr, env, engine.PlanOptions{Offline: p.Offline, Teardown: p.Teardown})
	if err != nil {
		return nil, err
	}
	if p.Offline {
		plan.Warnings = append(plan.Warnings, "Preview only: live state was not read. Connect every account and plan again before building.")
		plan.Blockers = append(plan.Blockers, "This is an offline preview.")
	}
	a.cachePlan(plan, pr.ID, env, false)
	return plan, nil
}

func (a *App) cachePlan(plan *core.Plan, projectID, env string, repair bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for id, c := range a.plans {
		if now.Sub(c.created) > planTTL {
			delete(a.plans, id)
		}
	}
	a.plans[plan.ID] = &cachedPlan{plan: plan, project: projectID, env: env, created: now, repair: repair}
}

// dropPlans invalidates cached plans after the project changed.
func (a *App) dropPlans(projectID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, c := range a.plans {
		if c.project == projectID {
			delete(a.plans, id)
		}
	}
}

func (a *App) runningRun(projectID, env string) string {
	runs, _ := a.Store.ListRuns(projectID, env)
	for _, r := range runs {
		if (r.Status == core.RunRunning || r.Status == core.RunRollingBack) && a.Engine.IsRunning(r.ID) {
			return r.ID
		}
	}
	return ""
}

// ApproveParams approves a plan. Destructive production plans need the
// project name typed into Confirm.
type ApproveParams struct {
	PlanID  string `json:"planId"`
	Confirm string `json:"confirm"`
}

// Approve starts executing an approved plan in the background. Progress
// streams over the event channel; GetRun returns the latest state.
func (a *App) Approve(ctx context.Context, p ApproveParams) (*core.Run, error) {
	a.mu.Lock()
	c := a.plans[p.PlanID]
	if c != nil && time.Since(c.created) > planTTL {
		delete(a.plans, p.PlanID)
		c = nil
	}
	a.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("this plan expired or the project changed since it was made — plan again")
	}
	pr, err := a.project(c.project)
	if err != nil {
		return nil, err
	}
	if id := a.runningRun(pr.ID, c.env); id != "" {
		return nil, fmt.Errorf("a build is already running in %s", c.env)
	}
	run, err := a.Engine.StartRun(pr, c.env, c.plan, p.Confirm)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	delete(a.plans, p.PlanID)
	a.mu.Unlock()
	a.afterRun(pr, c.env, run.ID)
	return run, nil
}

// afterRun runs a full check once a build finishes (if the project asks for
// it) and notifies the user.
func (a *App) afterRun(pr *core.Project, env, runID string) {
	go func() {
		a.Engine.Wait(runID, 2*time.Hour)
		run, err := a.Store.LoadRun(pr.ID, env, runID)
		if err != nil {
			return
		}
		switch run.Status {
		case core.RunSucceeded:
			a.notifyUser(pr, "ok", "Build finished", pr.Name+" ("+env+") was built successfully.")
			if pr.Monitor.CheckAfterBuild {
				kind := core.CheckFull
				if run.Plan.Repair {
					kind = core.CheckQuick
				}
				if cur, err := a.project(pr.ID); err == nil {
					_, _ = a.Engine.Check(context.Background(), cur, env, engine.CheckOptions{Kind: kind, Trigger: "after-build"})
				}
			}
		case core.RunFailed:
			msg := pr.Name + " (" + env + ") stopped."
			if run.Error != nil {
				msg += " " + run.Error.Title
			}
			a.notifyUser(pr, "fail", "Build stopped", msg)
		}
	}()
}

// RunParams selects a run.
type RunParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	RunID     string `json:"runId"`
	Confirm   string `json:"confirm"`
}

// RunView is a run with a live flag.
type RunView struct {
	*core.Run
	Live bool `json:"live"` // still executing in this session
}

// GetRun returns a run's latest checkpointed state.
func (a *App) GetRun(ctx context.Context, p RunParams) (*RunView, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	r, err := a.Store.LoadRun(pr.ID, env, p.RunID)
	if err != nil {
		return nil, err
	}
	return &RunView{Run: r, Live: a.Engine.IsRunning(r.ID)}, nil
}

// ListRuns returns recent runs for an environment.
func (a *App) ListRuns(ctx context.Context, p DashboardParams) ([]*core.Run, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	runs, err := a.Store.ListRuns(pr.ID, env)
	if len(runs) > 50 {
		runs = runs[:50]
	}
	return runs, err
}

// Resume continues a stopped build from the first unfinished step.
func (a *App) Resume(ctx context.Context, p RunParams) (*core.Run, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	if id := a.runningRun(pr.ID, env); id != "" && id != p.RunID {
		return nil, fmt.Errorf("another build is running in %s", env)
	}
	run, err := a.Engine.Resume(pr, env, p.RunID)
	if err != nil {
		return nil, err
	}
	a.afterRun(pr, env, run.ID)
	return run, nil
}

// Cancel stops a running build after the steps in flight.
func (a *App) Cancel(ctx context.Context, p RunParams) (bool, error) {
	if !a.Engine.Cancel(p.RunID) {
		return false, fmt.Errorf("that build is not running")
	}
	return true, nil
}

// Rollback undoes a build (production requires typing the project name).
func (a *App) Rollback(ctx context.Context, p RunParams) (*core.Run, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	return a.Engine.Rollback(ctx, pr, env, p.RunID, p.Confirm)
}

// ---- health checks ----

// CheckParams starts a health check.
type CheckParams struct {
	ProjectID       string `json:"projectId"`
	Env             string `json:"env"`
	Kind            string `json:"kind"` // quick | full
	AllowLiveProbes bool   `json:"allowLiveProbes"`
	Levels          []int  `json:"levels"`
	// Wait blocks until the check finishes (CLI and tests).
	Wait bool `json:"wait"`
}

// CheckStarted acknowledges a background check.
type CheckStarted struct {
	Started bool               `json:"started"`
	Report  *core.HealthReport `json:"report,omitempty"`
}

// Check runs a health check. By default it runs in the background and
// streams results; the final report arrives as a "report" event.
func (a *App) Check(ctx context.Context, p CheckParams) (*CheckStarted, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	if a.Engine.CheckRunning(pr.ID, env) {
		return nil, fmt.Errorf("a health check is already running for %s", env)
	}
	opts := engine.CheckOptions{Kind: p.Kind, Trigger: "manual", AllowLiveProbes: p.AllowLiveProbes, Levels: p.Levels}
	if opts.Kind != core.CheckFull {
		opts.Kind = core.CheckQuick
	}
	if p.Wait {
		rep, err := a.Engine.Check(ctx, pr, env, opts)
		if err != nil {
			return nil, err
		}
		a.monitor.observe(pr, env, rep)
		return &CheckStarted{Started: true, Report: rep}, nil
	}
	go func() {
		rep, err := a.Engine.Check(context.Background(), pr, env, opts)
		if err != nil {
			a.log("warn", pr.ID, env, "Health check could not run: "+err.Error())
			return
		}
		a.monitor.observe(pr, env, rep)
	}()
	return &CheckStarted{Started: true}, nil
}

// CancelCheck stops a running check (synthetic data is still cleaned up).
func (a *App) CancelCheck(ctx context.Context, p DashboardParams) (bool, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return false, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return false, err
	}
	return a.Engine.CancelCheck(pr.ID, env), nil
}

// LatestReport returns the newest health report for an environment.
func (a *App) LatestReport(ctx context.Context, p DashboardParams) (*core.HealthReport, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	hist, _ := a.Store.History(pr.ID, env, 20)
	for _, h := range hist {
		if h.ReportID == "" {
			continue
		}
		if r, err := a.Store.LoadReport(pr.ID, env, h.ReportID); err == nil {
			return r, nil
		}
	}
	return nil, nil
}

// ReportParams selects a report.
type ReportParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	ReportID  string `json:"reportId"`
}

// GetReport loads one stored report.
func (a *App) GetReport(ctx context.Context, p ReportParams) (*core.HealthReport, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	r, err := a.Store.LoadReport(pr.ID, env, p.ReportID)
	if err != nil {
		return nil, fmt.Errorf("that report is no longer stored (Backplane keeps the newest 60 per environment)")
	}
	return r, nil
}

// HistoryParams selects history.
type HistoryParams struct {
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	Limit     int    `json:"limit"`
}

// History returns health history, newest first.
func (a *App) History(ctx context.Context, p HistoryParams) ([]core.HistoryEntry, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	if p.Limit <= 0 || p.Limit > 5000 {
		p.Limit = 500
	}
	h, err := a.Store.History(pr.ID, env, p.Limit)
	if h == nil {
		h = []core.HistoryEntry{}
	}
	return h, err
}

// ---- repair ----

// RepairParams asks for a repair plan for one suggested fix.
type RepairParams struct {
	ProjectID string   `json:"projectId"`
	Env       string   `json:"env"`
	Fix       core.Fix `json:"fix"`
}

// Repair turns a suggested fix into a focused plan: only the broken
// resources and what depends on them are touched. The plan is shown to the
// user and runs only after approval, like any other plan.
func (a *App) Repair(ctx context.Context, p RepairParams) (*core.Plan, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	if p.Fix.Action == "replan" {
		return a.Plan(ctx, PlanParams{ProjectID: pr.ID, Env: env})
	}
	targets, err := engine.RepairTargets(&pr.Blueprint, p.Fix)
	if err != nil {
		if errors.Is(err, engine.ErrNotRepairable) {
			return nil, fmt.Errorf("“%s” needs you: %s", p.Fix.Label, orStr(p.Fix.Explain, "follow the guide on the problem card"))
		}
		return nil, err
	}
	if id := a.runningRun(pr.ID, env); id != "" {
		return nil, fmt.Errorf("a build is running in %s — wait for it to finish", env)
	}
	plan, err := a.Engine.Plan(ctx, pr, env, engine.PlanOptions{Repair: targets, Purpose: "Repair: " + strings.TrimSuffix(p.Fix.Label, ".")})
	if err != nil {
		return nil, err
	}
	a.cachePlan(plan, pr.ID, env, true)
	return plan, nil
}

// ---- logs ----

// Logs queries the unified, redacted log.
func (a *App) Logs(ctx context.Context, q store.LogQuery) ([]core.LogEntry, error) {
	if q.Limit <= 0 || q.Limit > 5000 {
		q.Limit = 800
	}
	out, err := a.Store.QueryLogs(q)
	if out == nil {
		out = []core.LogEntry{}
	}
	return out, err
}

// ---- snapshots ----

// SnapshotView is a snapshot without its heavy bodies.
type SnapshotView struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
	KnownGood bool      `json:"knownGood"`
	Resources int       `json:"resources"`
}

// Snapshots lists restorable checkpoints for an environment.
func (a *App) Snapshots(ctx context.Context, p DashboardParams) ([]SnapshotView, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	sns, err := a.Store.ListSnapshots(pr.ID, env)
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotView, 0, len(sns))
	for _, s := range sns {
		out = append(out, SnapshotView{ID: s.ID, Reason: s.Reason, CreatedAt: s.CreatedAt, KnownGood: s.KnownGood, Resources: len(s.Manifest.Resources)})
	}
	return out, nil
}

// RestoreParams selects a snapshot to restore.
type RestoreParams struct {
	ProjectID  string `json:"projectId"`
	Env        string `json:"env"`
	SnapshotID string `json:"snapshotId"`
}

// RestoreSnapshot puts the project's configuration back to a snapshot and
// returns the plan that would apply it. The current configuration is
// snapshotted first so the restore itself can be undone.
func (a *App) RestoreSnapshot(ctx context.Context, p RestoreParams) (*core.Plan, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	sn, err := a.Store.LoadSnapshot(pr.ID, env, p.SnapshotID)
	if err != nil {
		return nil, fmt.Errorf("snapshot not found")
	}
	if _, err := a.Engine.Snapshot(pr, env, nil, "Before restoring "+sn.ID, false); err != nil {
		return nil, err
	}
	pr.Blueprint = sn.Blueprint
	if err := a.Store.SaveProject(pr); err != nil {
		return nil, err
	}
	a.dropPlans(pr.ID)
	a.log("info", pr.ID, env, "Configuration restored from snapshot “"+sn.Reason+"”. Review the plan to apply it.")
	plan, err := a.Engine.Plan(ctx, pr, env, engine.PlanOptions{Purpose: "Restore: " + sn.Reason})
	if err != nil {
		return nil, err
	}
	a.cachePlan(plan, pr.ID, env, false)
	return plan, nil
}
