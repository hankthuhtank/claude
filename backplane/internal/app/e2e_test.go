package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"safisolutions.org/backplane/internal/core"
)

// newPracticeApp starts an app with a temporary data folder and the
// practice simulator, tuned for fast tests.
func newPracticeApp(t *testing.T) *App {
	t.Helper()
	a, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.Engine.Poll = 20 * time.Millisecond
	a.Engine.RetryBase = 20 * time.Millisecond
	st, err := a.StartPractice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Connections {
		if c.Status != core.HealthOK {
			t.Fatalf("practice connection %s is %s: %s", c.Provider, c.Status, c.StatusNote)
		}
	}
	return a
}

func dumpRun(t *testing.T, r *RunView) {
	t.Helper()
	for i, o := range r.Ops {
		op := r.Plan.Operations[i]
		line := string(o.Status) + "  " + op.Title
		if o.Error != nil {
			line += "  → " + o.Error.Title + ": " + o.Error.Summary + " " + o.Error.Technical
		}
		t.Log(line)
	}
	if r.Error != nil {
		t.Logf("run error: %s: %s", r.Error.Title, r.Error.Summary)
	}
}

func dumpReport(t *testing.T, rep *core.HealthReport) {
	t.Helper()
	t.Logf("overall %s — %s", rep.Overall, rep.Headline)
	for _, r := range rep.Results {
		if r.Health == core.HealthOK {
			continue
		}
		line := "L" + string(rune('0'+r.Level)) + " " + string(r.Health) + " " + r.Title + ": " + r.Summary
		if r.Problem != nil {
			line += " [" + r.Problem.Title + "] " + r.Problem.Technical
		}
		t.Log(line)
		for _, s := range r.Steps {
			if s.Health != core.HealthOK {
				t.Logf("    step %s %s: %s", s.Health, s.Title, s.Detail)
			}
		}
	}
}

// buildPractice creates a practice project from a preset, builds it and
// returns its id.
func buildPractice(t *testing.T, a *App, template string, answers map[string]any) string {
	t.Helper()
	ctx := context.Background()
	res, err := a.CreateProject(ctx, CreateProjectParams{Name: "Pixel Presets " + template, TemplateID: template, Practice: true, Answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Project.ID
	off := core.DefaultMonitor()
	off.CheckAfterBuild = false
	if _, err := a.UpdateProject(ctx, UpdateProjectParams{ID: id, Monitor: &off}); err != nil {
		t.Fatal(err)
	}
	plan, err := a.Plan(ctx, PlanParams{ProjectID: id, Env: core.EnvProduction})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) > 0 {
		t.Fatalf("plan blockers: %v", plan.Blockers)
	}
	run, err := a.Approve(ctx, ApproveParams{PlanID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	a.Engine.Wait(run.ID, 2*time.Minute)
	rv, err := a.GetRun(ctx, RunParams{ProjectID: id, Env: core.EnvProduction, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rv.Status != core.RunSucceeded {
		dumpRun(t, rv)
		t.Fatalf("build finished as %s", rv.Status)
	}
	return id
}

func fullCheck(t *testing.T, a *App, id string) *core.HealthReport {
	t.Helper()
	res, err := a.Check(context.Background(), CheckParams{ProjectID: id, Env: core.EnvProduction, Kind: core.CheckFull, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.Report
}

func quickCheck(t *testing.T, a *App, id string) *core.HealthReport {
	t.Helper()
	res, err := a.Check(context.Background(), CheckParams{ProjectID: id, Env: core.EnvProduction, Kind: core.CheckQuick, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.Report
}

func TestPracticeSoftwareStoreFullyOperational(t *testing.T) {
	a := newPracticeApp(t)
	file := filepath.Join(t.TempDir(), "PixelPresets-Setup.exe")
	if err := os.WriteFile(file, []byte(strings.Repeat("MZ-installer-bytes ", 4000)), 0o600); err != nil {
		t.Fatal(err)
	}
	id := buildPractice(t, a, "software-store", map[string]any{"product_name": "Pixel Presets", "price": 29.0, "domain": "pixelpresets.com", "product_file": file})
	rep := fullCheck(t, a, id)
	if rep.Headline != "FULLY OPERATIONAL" {
		dumpReport(t, rep)
		t.Fatalf("expected FULLY OPERATIONAL, got %s", rep.Headline)
	}
	// A second plan must be a no-op: everything is up to date.
	plan, err := a.Plan(context.Background(), PlanParams{ProjectID: id, Env: core.EnvProduction})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Operations {
		if op.Action != core.ActKeep {
			t.Errorf("re-plan wants to %s %s: %s %v", op.Action, op.Title, op.Why, op.Changes)
		}
	}
}

// firstRepair returns the first automatic repair suggested for a failing
// (then warning) result.
func firstRepair(rep *core.HealthReport) *core.Fix {
	for _, want := range []core.Health{core.HealthFail, core.HealthWarn} {
		for _, r := range rep.Results {
			if r.Health != want || r.Problem == nil {
				continue
			}
			for _, f := range r.Problem.Fixes {
				if f.Action == "repair" {
					f := f
					return &f
				}
			}
		}
	}
	return nil
}

func hasProblem(rep *core.HealthReport, title string) bool {
	for _, r := range rep.Results {
		if r.Problem != nil && strings.Contains(r.Problem.Title, title) {
			return true
		}
	}
	return false
}

func applyRepair(t *testing.T, a *App, id string, fix *core.Fix) {
	t.Helper()
	ctx := context.Background()
	plan, err := a.Repair(ctx, RepairParams{ProjectID: id, Env: core.EnvProduction, Fix: *fix})
	if err != nil {
		t.Fatalf("repair %s: %v", fix.ID, err)
	}
	if len(plan.Blockers) > 0 {
		t.Fatalf("repair plan blockers: %v", plan.Blockers)
	}
	run, err := a.Approve(ctx, ApproveParams{PlanID: plan.ID})
	if err != nil {
		t.Fatalf("approve repair: %v", err)
	}
	a.Engine.Wait(run.ID, 2*time.Minute)
	rv, _ := a.GetRun(ctx, RunParams{ProjectID: id, Env: core.EnvProduction, RunID: run.ID})
	if rv.Status != core.RunSucceeded {
		dumpRun(t, rv)
		t.Fatalf("repair run finished as %s", rv.Status)
	}
}

func TestPracticeBreakDetectRepair(t *testing.T) {
	a := newPracticeApp(t)
	file := filepath.Join(t.TempDir(), "Setup.exe")
	if err := os.WriteFile(file, []byte("installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := buildPractice(t, a, "software-store", map[string]any{"product_name": "Pixel Presets", "price": 29.0, "domain": "pixelpresets.com", "product_file": file})
	if rep := quickCheck(t, a, id); rep.Overall != core.HealthOK {
		dumpReport(t, rep)
		t.Fatalf("baseline quick check: %s", rep.Headline)
	}
	cases := []struct {
		brk    string
		expect string // problem title fragment the check must report
		full   bool   // needs a full check to notice
	}{
		{"disable_webhook", "STRIPE WEBHOOK DISABLED", false},
		{"move_webhook", "WEBHOOK URL INCORRECT", false},
		{"remove_binding", "WORKER", false},
		{"remove_secret", "", false},
		{"archive_product", "", false},
		{"disable_rls", "", false},
		{"unverify_domain", "", false},
		{"revoke_db_key", "", false},
		{"delete_bucket", "IS MISSING", false},
		{"pause_project", "", false},
		{"delete_worker", "IS MISSING", false},
		{"rotate_webhook_secret", "DELIVERY FAILED", true},
	}
	for _, tc := range cases {
		t.Run(tc.brk, func(t *testing.T) {
			if _, err := a.PracticeBreak(context.Background(), BreakParams{ProjectID: id, Env: core.EnvProduction, Break: tc.brk}); err != nil {
				t.Fatal(err)
			}
			check := quickCheck
			if tc.full {
				check = fullCheck
			}
			rep := check(t, a, id)
			if rep.Overall == core.HealthOK {
				t.Fatalf("%s was not detected", tc.brk)
			}
			if tc.expect != "" && !hasProblem(rep, tc.expect) {
				dumpReport(t, rep)
				t.Fatalf("expected a problem mentioning %q", tc.expect)
			}
			fix := firstRepair(rep)
			if fix == nil {
				dumpReport(t, rep)
				t.Fatalf("no automatic repair offered for %s", tc.brk)
			}
			applyRepair(t, a, id, fix)
			after := check(t, a, id)
			if after.Overall != core.HealthOK {
				dumpReport(t, after)
				t.Fatalf("still %s after repairing %s with %s", after.Headline, tc.brk, fix.ID)
			}
		})
	}
	// Everything must end fully operational.
	if rep := fullCheck(t, a, id); rep.Headline != "FULLY OPERATIONAL" {
		dumpReport(t, rep)
		t.Fatalf("final state %s", rep.Headline)
	}
}

func TestPracticeEcommerceShippingFullyOperational(t *testing.T) {
	a := newPracticeApp(t)
	id := buildPractice(t, a, "ecommerce", map[string]any{"product_name": "Hand-poured Candle", "price": 24.0, "domain": "candleshop.com", "ship_countries": "US,CA"})
	rep := fullCheck(t, a, id)
	if rep.Headline != "FULLY OPERATIONAL" {
		dumpReport(t, rep)
		t.Fatalf("expected FULLY OPERATIONAL, got %s", rep.Headline)
	}
	var journey *core.CheckResult
	for i := range rep.Results {
		if rep.Results[i].Level == core.LevelEndToEnd {
			journey = &rep.Results[i]
		}
	}
	if journey == nil || !strings.Contains(journey.Summary, "where to ship") {
		t.Fatalf("expected the shipped-order journey to run, got %+v", journey)
	}
}
