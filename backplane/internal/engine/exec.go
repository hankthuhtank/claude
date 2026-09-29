package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
)

// ErrConfirm is returned when a destructive production change was not confirmed.
var ErrConfirm = errors.New("confirmation required")

// StartRun approves a plan and executes it in the background. Destructive
// changes in production require typing the project name.
func (e *Engine) StartRun(p *core.Project, env string, plan *core.Plan, confirm string) (*core.Run, error) {
	if len(plan.Blockers) > 0 {
		return nil, &core.Problem{Title: "The plan has blockers", Code: "invalid", Summary: strings.Join(plan.Blockers, " ")}
	}
	if plan.Destructive && core.IsProduction(env) && !p.Practice && strings.TrimSpace(confirm) != p.Name {
		return nil, fmt.Errorf("%w: type the project name %q to delete production resources", ErrConfirm, p.Name)
	}
	man, err := e.Store.LoadManifest(p.ID, env)
	if err != nil {
		return nil, err
	}
	snap, err := e.Snapshot(p, env, man, "Before "+strings.ToLower(orStr(plan.Purpose, "build")), false)
	if err != nil {
		return nil, err
	}
	run := &core.Run{ID: NewID("run"), Project: p.ID, Environment: env, Plan: *plan, Status: core.RunRunning, StartedAt: e.Now().UTC(), Checkpoint: snap.ID}
	for _, op := range plan.Operations {
		os := core.OpState{OpID: op.ID, Status: core.OpPending}
		if op.Action == core.ActKeep {
			os.Status = core.OpSkipped
			os.Note = "No change needed"
		}
		run.Ops = append(run.Ops, os)
	}
	if err := e.Store.SaveRun(run); err != nil {
		return nil, err
	}
	e.logRun(run, "info", fmt.Sprintf("Build started: %d operations (%s)", len(plan.Operations), countsLine(plan.Counts)), "")
	e.launch(p, run)
	return run, nil
}

func countsLine(c map[string]int) string {
	var parts []string
	for _, k := range []string{core.ActCreate, core.ActAdopt, core.ActUpdate, core.ActReplace, core.ActDelete, core.ActDetach, core.ActKeep} {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c[k], k))
		}
	}
	return strings.Join(parts, ", ")
}

// Resume continues a failed or canceled run from where it stopped.
func (e *Engine) Resume(p *core.Project, env, runID string) (*core.Run, error) {
	run, err := e.Store.LoadRun(p.ID, env, runID)
	if err != nil {
		return nil, err
	}
	if e.isRunning(runID) {
		return run, nil
	}
	if run.Status != core.RunFailed && run.Status != core.RunCanceled && run.Status != core.RunRunning {
		return nil, fmt.Errorf("run is %s and cannot be resumed", run.Status)
	}
	for i := range run.Ops {
		switch run.Ops[i].Status {
		case core.OpFailed, core.OpBlocked, core.OpRunning:
			run.Ops[i].Status = core.OpPending
			run.Ops[i].Error = nil
			run.Ops[i].Note = "Resumed"
		}
	}
	run.Status = core.RunRunning
	run.Error = nil
	run.Resumes++
	run.FinishedAt = nil
	if err := e.Store.SaveRun(run); err != nil {
		return nil, err
	}
	done := 0
	for _, o := range run.Ops {
		if o.Status == core.OpSucceeded || o.Status == core.OpSkipped {
			done++
		}
	}
	e.logRun(run, "info", fmt.Sprintf("Resuming build: %d of %d steps already done, continuing from the first unfinished step", done, len(run.Ops)), "")
	e.launch(p, run)
	return run, nil
}

// Cancel stops a running build after the operations in flight.
func (e *Engine) Cancel(runID string) bool {
	e.mu.Lock()
	cancel, ok := e.running[runID]
	e.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// IsRunning reports whether a run is executing in this process.
func (e *Engine) IsRunning(runID string) bool { return e.isRunning(runID) }

func (e *Engine) isRunning(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[runID]
	return ok
}

// RunningCount reports builds in progress.
func (e *Engine) RunningCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running)
}

func (e *Engine) launch(p *core.Project, run *core.Run) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.running[run.ID] = cancel
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.running, run.ID)
			e.mu.Unlock()
			cancel()
		}()
		e.execute(ctx, p, run)
	}()
}

// Wait blocks until a run is no longer executing (tests and CLI).
func (e *Engine) Wait(runID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for e.isRunning(runID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

type runState struct {
	mu   sync.Mutex
	run  *core.Run
	man  *core.Manifest
	ops  map[string]*core.Operation
	idx  map[string]int
	busy map[string]int // provider -> in flight
}

func (e *Engine) execute(ctx context.Context, p *core.Project, run *core.Run) {
	man, err := e.Store.LoadManifest(p.ID, run.Environment)
	if err != nil {
		e.finish(p, run, nil, core.RunFailed, &core.Problem{Title: "Could not read the manifest", Summary: err.Error()})
		return
	}
	sess, missing := e.Session(p, run.Environment, man, run.ID)
	var needed []string
	for _, m := range missing {
		if providerHasResources(&p.Blueprint, m) {
			needed = append(needed, m) // add-ons without resources never block a build
		}
	}
	if len(needed) > 0 {
		e.finish(p, run, man, core.RunFailed, &core.Problem{Title: "Missing connections", Code: "auth", Summary: "Connect " + strings.Join(mapDisplay(needed), ", ") + " before building."})
		return
	}
	rs := &runState{run: run, man: man, ops: map[string]*core.Operation{}, idx: map[string]int{}, busy: map[string]int{}}
	for i := range run.Plan.Operations {
		op := &run.Plan.Operations[i]
		rs.ops[op.ID] = op
	}
	for i, o := range run.Ops {
		rs.idx[o.OpID] = i
	}
	parallel := e.Parallel
	if parallel < 1 {
		parallel = 1
	}
	var wg sync.WaitGroup
	wake := make(chan struct{}, 64)
	inflight := 0
	for {
		rs.mu.Lock()
		// Block operations whose dependencies failed.
		for i := range run.Ops {
			o := &run.Ops[i]
			if o.Status != core.OpPending {
				continue
			}
			for _, d := range rs.ops[o.OpID].DependsOn {
				if j, ok := rs.idx[d]; ok {
					ds := run.Ops[j].Status
					if ds == core.OpFailed || ds == core.OpBlocked {
						o.Status = core.OpBlocked
						o.Note = "Waiting on a failed step"
					}
				}
			}
		}
		var ready []int
		if ctx.Err() == nil {
			for i := range run.Ops {
				o := &run.Ops[i]
				if o.Status != core.OpPending {
					continue
				}
				ok := true
				for _, d := range rs.ops[o.OpID].DependsOn {
					if j, found := rs.idx[d]; found {
						ds := run.Ops[j].Status
						if ds != core.OpSucceeded && ds != core.OpSkipped {
							ok = false
						}
					}
				}
				if ok {
					ready = append(ready, i)
				}
			}
		}
		// Stop scheduling new work once anything failed (dependents are
		// blocked; independent work also stops so the failure is fixed first).
		failed := false
		for _, o := range run.Ops {
			if o.Status == core.OpFailed {
				failed = true
			}
		}
		if failed {
			ready = nil
		}
		started := 0
		for _, i := range ready {
			if inflight >= parallel {
				break
			}
			op := rs.ops[run.Ops[i].OpID]
			if rs.busy[op.Provider] >= 2 {
				continue
			}
			now := e.Now().UTC()
			run.Ops[i].Status = core.OpRunning
			run.Ops[i].StartedAt = &now
			run.Ops[i].Attempts++
			rs.busy[op.Provider]++
			inflight++
			started++
			wg.Add(1)
			go func(i int, op *core.Operation) {
				defer wg.Done()
				e.runOp(ctx, p, sess, rs, i, op)
				rs.mu.Lock()
				rs.busy[op.Provider]--
				inflight--
				rs.mu.Unlock()
				wake <- struct{}{}
			}(i, op)
		}
		if started > 0 {
			_ = e.Store.SaveRun(run)
			e.Bus.Publish(Event{Type: "run", Project: p.ID, Env: run.Environment, RunID: run.ID, Data: run})
		}
		idle := inflight == 0
		rs.mu.Unlock()
		if idle && started == 0 {
			break
		}
		<-wake
	}
	wg.Wait()
	status := core.RunSucceeded
	var prob *core.Problem
	for _, o := range run.Ops {
		if o.Status == core.OpFailed {
			status = core.RunFailed
			if prob == nil {
				prob = o.Error
			}
		}
		if o.Status == core.OpPending && ctx.Err() != nil {
			status = core.RunCanceled
		}
	}
	if ctx.Err() != nil && status == core.RunSucceeded {
		for _, o := range run.Ops {
			if o.Status != core.OpSucceeded && o.Status != core.OpSkipped {
				status = core.RunCanceled
			}
		}
	}
	e.finish(p, run, man, status, prob)
}

func mapDisplay(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = providers.DisplayName(id)
	}
	return out
}

func (e *Engine) runOp(ctx context.Context, p *core.Project, sess *providers.Session, rs *runState, i int, op *core.Operation) {
	run := rs.run
	env := run.Environment
	e.Bus.Publish(Event{Type: "op", Project: p.ID, Env: env, RunID: run.ID, Data: map[string]any{"opId": op.ID, "status": core.OpRunning}})
	e.logRun(run, "info", "▶ "+op.Title, op.Provider)
	fail := func(prob *core.Problem) {
		rs.mu.Lock()
		now := e.Now().UTC()
		run.Ops[i].Status = core.OpFailed
		run.Ops[i].FinishedAt = &now
		run.Ops[i].Error = prob
		_ = e.Store.SaveRun(run)
		rs.mu.Unlock()
		e.logRun(run, "error", "✖ "+op.Title+" — "+prob.Title+": "+prob.Summary, op.Provider)
		e.Bus.Publish(Event{Type: "op", Project: p.ID, Env: env, RunID: run.ID, Data: map[string]any{"opId": op.ID, "status": core.OpFailed, "problem": prob}})
	}
	succeed := func(note string, created bool) {
		rs.mu.Lock()
		now := e.Now().UTC()
		run.Ops[i].Status = core.OpSucceeded
		run.Ops[i].FinishedAt = &now
		run.Ops[i].Note = note
		if created {
			run.Ops[i].Created = true
		}
		_ = e.Store.SaveRun(run)
		rs.mu.Unlock()
		msg := "✔ " + op.Title
		if note != "" {
			msg += " — " + note
		}
		e.logRun(run, "info", msg, op.Provider)
		e.Bus.Publish(Event{Type: "op", Project: p.ID, Env: env, RunID: run.ID, Data: map[string]any{"opId": op.ID, "status": core.OpSucceeded, "note": note}})
	}

	// Removals.
	if op.Action == core.ActDelete || op.Action == core.ActDetach {
		rs.mu.Lock()
		st := rs.man.Resources[op.ResourceKey]
		rs.mu.Unlock()
		if st == nil {
			succeed("Already gone", false)
			return
		}
		if op.Action == core.ActDelete {
			h, ok := e.Registry.Handler(st.Kind)
			if ok {
				if err := h.Delete(ctx, sess, st); err != nil {
					fail(e.explain(p, op, err))
					return
				}
			}
			_ = e.Vault.DeletePrefix(ResourceSecretKey(p.ID, env, op.ResourceKey, ""))
		}
		rs.mu.Lock()
		delete(rs.man.Resources, op.ResourceKey)
		_ = e.Store.SaveManifest(rs.man)
		rs.mu.Unlock()
		succeed("", false)
		return
	}

	spec := p.Blueprint.ResourceByKey(op.ResourceKey)
	if spec == nil {
		fail(&core.Problem{Title: "Step no longer in the blueprint", Summary: "Re-plan the build."})
		return
	}
	h, ok := e.Registry.Handler(spec.Kind)
	if !ok {
		fail(&core.Problem{Title: "No adapter for " + spec.Kind})
		return
	}
	rs.mu.Lock()
	resolved, err := e.resolveSpec(p, env, rs.man, spec, forApply, true)
	hash, _ := e.desiredHash(p, env, rs.man, spec)
	applied := e.safeApplied(p, env, rs.man, spec)
	prev := rs.man.Resources[op.ResourceKey]
	rs.mu.Unlock()
	if err != nil {
		var un *Unresolved
		if errors.As(err, &un) {
			fail(&core.Problem{Title: "Missing input", Code: "invalid", Summary: "This step needs " + un.Ref + ", which is not available yet. The step that produces it may have been skipped; re-plan the build."})
			return
		}
		fail(e.explain(p, op, err))
		return
	}
	if op.Force || op.Rotate {
		if resolved.Props == nil {
			resolved.Props = map[string]any{}
		}
		resolved.Props["__force"] = true
		if op.Rotate {
			resolved.Props["__rotate"] = true
		}
	}
	// Reconcile: nothing to do when the resolved hash matches what is live.
	if !op.Force && prev != nil && prev.Status == core.StateReady && prev.Hash == hash && op.Action == core.ActUpdate && len(op.Changes) == 0 {
		succeed("Already up to date", false)
		return
	}
	var prevCopy *core.ResourceState
	if prev != nil && prev.Status != core.StateDeleted {
		cp := *prev
		prevCopy = &cp
	}
	attempts := e.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var res *providers.ApplyResult
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err = h.Apply(ctx, sess, resolved, prevCopy)
		if err == nil {
			break
		}
		// Keep partial progress (for example a Supabase project that is
		// still starting) so a resume continues with the same resource.
		if res != nil && res.State != nil {
			e.recordState(p, env, rs, spec, res, hash, applied)
			cp := *res.State
			prevCopy = &cp
		}
		prob := e.explain(p, op, err)
		if !prob.Retryable || attempt == attempts || ctx.Err() != nil {
			fail(prob)
			return
		}
		delay := e.RetryBase << (attempt - 1)
		if delay > time.Minute {
			delay = time.Minute
		}
		delay = delay/2 + time.Duration(rand.Int63n(int64(delay/2)+1))
		e.logRun(run, "warn", fmt.Sprintf("↻ %s — %s. Retrying in %s (attempt %d of %d).", op.Title, prob.Title, delay.Round(time.Second), attempt+1, attempts), op.Provider)
		rs.mu.Lock()
		run.Ops[i].Attempts++
		rs.mu.Unlock()
		select {
		case <-ctx.Done():
			fail(&core.Problem{Title: "Canceled", Summary: "The build was canceled during a retry wait."})
			return
		case <-time.After(delay):
		}
	}
	e.recordState(p, env, rs, spec, res, hash, applied)
	note := res.Note
	if res.State != nil && res.State.Note != "" {
		note = res.State.Note
	}
	succeed(note, res.Created)
}

// recordState saves an apply result into the manifest (checkpoint) and its
// secrets into the vault.
func (e *Engine) recordState(p *core.Project, env string, rs *runState, spec *core.ResourceSpec, res *providers.ApplyResult, hash string, applied map[string]any) {
	st := res.State
	if st == nil {
		return
	}
	st.Key, st.Kind, st.Provider = spec.Key, spec.Kind, spec.Provider
	if st.SecretRefs == nil {
		st.SecretRefs = map[string]string{}
	}
	for name, v := range res.Secrets {
		k := ResourceSecretKey(p.ID, env, spec.Key, name)
		if err := e.Vault.PutString(k, v, spec.Title+" · "+name); err == nil {
			st.SecretRefs[name] = k
		}
	}
	if st.Status == core.StateReady {
		st.Hash = hash
	}
	merged := map[string]any{}
	for k, v := range applied {
		merged[k] = v
	}
	for k, v := range st.Applied { // adapter-provided facts win (digests, blobs)
		merged[k] = v
	}
	st.Applied = merged
	rs.mu.Lock()
	rs.man.Resources[spec.Key] = st
	_ = e.Store.SaveManifest(rs.man)
	rs.mu.Unlock()
}

func (e *Engine) explain(p *core.Project, op *core.Operation, err error) *core.Problem {
	prob := providers.Translate(op.Provider, strings.ToLower(firstWord(op.Title))+" "+restWords(op.Title), err)
	if spec := p.Blueprint.ResourceByKey(op.ResourceKey); spec != nil && spec.Component != "" && len(prob.Affected) == 0 {
		prob.Affected, prob.Unaffected = p.Blueprint.ImpactOf(spec.Component)
	}
	return prob
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func restWords(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[i+1:]
	}
	return ""
}

func (e *Engine) finish(p *core.Project, run *core.Run, man *core.Manifest, status string, prob *core.Problem) {
	now := e.Now().UTC()
	run.Status = status
	run.FinishedAt = &now
	run.Error = prob
	_ = e.Store.SaveRun(run)
	switch status {
	case core.RunSucceeded:
		if man != nil {
			man.LastBuild = &now
			_ = e.Store.SaveManifest(man)
			_, _ = e.Snapshot(p, run.Environment, man, "After "+strings.ToLower(orStr(run.Plan.Purpose, "build")), false)
		}
		e.logRun(run, "info", "Build finished. Every step succeeded.", "")
	case core.RunFailed:
		msg := "Build stopped."
		if prob != nil {
			msg += " " + prob.Title + ": " + prob.Summary
		}
		e.logRun(run, "error", msg+" Fix the problem and choose Resume — finished steps are not repeated.", "")
	case core.RunCanceled:
		e.logRun(run, "warn", "Build canceled. Resume continues from the first unfinished step.", "")
	}
	e.Bus.Publish(Event{Type: "run", Project: p.ID, Env: run.Environment, RunID: run.ID, Data: run})
}

func (e *Engine) logRun(run *core.Run, level, msg, provider string) {
	e.Log(core.LogEntry{Level: level, Source: "build", Project: run.Project, Environment: run.Environment, RunID: run.ID, Provider: provider, Message: msg})
}

// ---- snapshots & rollback ----

// Snapshot stores a restorable copy of the manifest and blueprint.
func (e *Engine) Snapshot(p *core.Project, env string, man *core.Manifest, reason string, knownGood bool) (*core.Snapshot, error) {
	if man == nil {
		var err error
		if man, err = e.Store.LoadManifest(p.ID, env); err != nil {
			return nil, err
		}
	}
	sn := &core.Snapshot{ID: NewID("snap"), Project: p.ID, Environment: env, Reason: reason, CreatedAt: e.Now().UTC(), Manifest: *man, Blueprint: p.Blueprint, KnownGood: knownGood}
	return sn, e.Store.SaveSnapshot(sn)
}

// Rollback undoes a run: resources it created are deleted in reverse order and
// resources it updated are re-applied from the snapshot taken before the run.
// Production rollbacks require typing the project name.
func (e *Engine) Rollback(ctx context.Context, p *core.Project, env, runID, confirm string) (*core.Run, error) {
	run, err := e.Store.LoadRun(p.ID, env, runID)
	if err != nil {
		return nil, err
	}
	if e.isRunning(runID) {
		return nil, errors.New("cancel the build before rolling it back")
	}
	if core.IsProduction(env) && !p.Practice && strings.TrimSpace(confirm) != p.Name {
		return nil, fmt.Errorf("%w: type the project name %q to roll back production", ErrConfirm, p.Name)
	}
	snap, err := e.Store.LoadSnapshot(p.ID, env, run.Checkpoint)
	if err != nil {
		return nil, fmt.Errorf("the checkpoint for this build is missing: %w", err)
	}
	man, err := e.Store.LoadManifest(p.ID, env)
	if err != nil {
		return nil, err
	}
	run.Status = core.RunRollingBack
	_ = e.Store.SaveRun(run)
	e.Bus.Publish(Event{Type: "run", Project: p.ID, Env: env, RunID: run.ID, Data: run})
	sess, _ := e.Session(p, env, man, run.ID)
	// Reverse order of the plan.
	ops := map[string]core.Operation{}
	order := make([]int, 0, len(run.Ops))
	for i, o := range run.Plan.Operations {
		ops[o.ID] = o
		order = append(order, i)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(order)))
	var firstErr *core.Problem
	for _, idx := range order {
		op := run.Plan.Operations[idx]
		var os *core.OpState
		for j := range run.Ops {
			if run.Ops[j].OpID == op.ID {
				os = &run.Ops[j]
			}
		}
		if os == nil || os.Status != core.OpSucceeded {
			continue
		}
		spec := snap.Blueprint.ResourceByKey(op.ResourceKey)
		cur := man.Resources[op.ResourceKey]
		switch {
		case os.Created && cur != nil:
			if (spec != nil && spec.Keep) || cur.CreatedBy == "adopted" {
				os.Note = "Kept (pre-existing resource)"
				continue
			}
			h, ok := e.Registry.Handler(cur.Kind)
			if !ok {
				continue
			}
			e.logRun(run, "warn", "↩ Removing "+op.Title, op.Provider)
			if err := h.Delete(ctx, sess, cur); err != nil {
				prob := e.explain(p, &op, err)
				os.Error = prob
				if firstErr == nil {
					firstErr = prob
				}
				continue
			}
			delete(man.Resources, op.ResourceKey)
			_ = e.Vault.DeletePrefix(ResourceSecretKey(p.ID, env, op.ResourceKey, ""))
			os.Status = core.OpRolledBack
		case !os.Created && spec != nil && snap.Manifest.Resources[op.ResourceKey] != nil:
			h, ok := e.Registry.Handler(spec.Kind)
			if !ok {
				continue
			}
			prev := snap.Manifest.Resources[op.ResourceKey]
			pp := *p
			pp.Blueprint = snap.Blueprint
			resolved, err := e.resolveSpec(&pp, env, &snap.Manifest, spec, forApply, false)
			if err != nil {
				continue
			}
			e.logRun(run, "warn", "↩ Restoring previous configuration of "+op.Title, op.Provider)
			if _, err := h.Apply(ctx, sess, resolved, cur); err != nil {
				prob := e.explain(p, &op, err)
				os.Error = prob
				if firstErr == nil {
					firstErr = prob
				}
				continue
			}
			restored := *prev
			man.Resources[op.ResourceKey] = &restored
			os.Status = core.OpRolledBack
		}
		_ = e.Store.SaveManifest(man)
		_ = e.Store.SaveRun(run)
	}
	now := e.Now().UTC()
	run.FinishedAt = &now
	if firstErr != nil {
		run.Status = core.RunFailed
		run.Error = &core.Problem{Title: "Rollback incomplete", Summary: firstErr.Title + ": " + firstErr.Summary + " Some resources may need removing by hand; they are listed in the log."}
	} else {
		run.Status = core.RunRolledBack
		e.logRun(run, "info", "Rolled back to the configuration from before this build.", "")
	}
	_ = e.Store.SaveManifest(man)
	_ = e.Store.SaveRun(run)
	e.Bus.Publish(Event{Type: "run", Project: p.ID, Env: env, RunID: run.ID, Data: run})
	return run, nil
}
