package app

import (
	"context"
	"testing"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/store"
)

// Stopping practice mode must switch everything that depends on it off:
// connections, scheduled checks and the activity log.
func TestStopPracticeSwitchesEverythingOff(t *testing.T) {
	ctx := context.Background()
	a := newPracticeApp(t)
	res, err := a.CreateProject(ctx, CreateProjectParams{Name: "Stop test", TemplateID: "ecommerce", Practice: true,
		Answers: map[string]any{"products": []any{map[string]any{"name": "Candle", "price": 24.0}}, "domain": "candles.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.StopPractice(ctx); err != nil || !ok {
		t.Fatalf("StopPractice = %v, %v", ok, err)
	}

	views, err := a.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range views {
		if !v.Practice {
			continue
		}
		n++
		if v.Status != core.HealthOff || v.VerifiedAt != nil {
			t.Errorf("%s practice connection is %s (verified %v) after stopping", v.Provider, v.Status, v.VerifiedAt)
		}
	}
	if n != len(practiceProviders) {
		t.Fatalf("expected %d practice connections, got %d", len(practiceProviders), n)
	}
	// Verifying while off must not reach any server or turn red.
	for _, v := range views {
		if v.Practice {
			r, err := a.VerifyConnection(ctx, IDParams{ID: v.ID})
			if err != nil || r.Verify.Health != core.HealthOff {
				t.Fatalf("verify while off: %v %+v", err, r)
			}
			break
		}
	}

	pr, _ := a.project(res.Project.ID)
	st := a.monitor.state(pr, core.EnvProduction)
	if !st.Paused || st.NextQuick != nil || st.NextFull != nil {
		t.Errorf("monitor state while off: %+v", st)
	}
	if _, err := a.Check(ctx, CheckParams{ProjectID: pr.ID, Env: core.EnvProduction, Wait: true}); err == nil {
		t.Error("a check of a practice backend ran with practice mode off")
	}

	logs, _ := a.Logs(ctx, store.LogQuery{Text: "Practice sandbox stopped", Limit: 5})
	if len(logs) == 0 {
		t.Error("stopping practice mode was not logged")
	}

	// A new session (the app restarting) keeps them off.
	b, err := New(Options{DataDir: a.Store.Dir})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := b.Store.LoadConnections()
	for _, c := range cs {
		if c.Practice && c.Status != core.HealthOff {
			t.Errorf("after restart %s is %s", c.Provider, c.Status)
		}
	}

	// Starting again brings them back.
	if _, err := a.StartPractice(ctx); err != nil {
		t.Fatal(err)
	}
	cs, _ = a.Store.LoadConnections()
	for _, c := range cs {
		if c.Practice && c.Status != core.HealthOK {
			t.Errorf("after restart of practice mode %s is %s: %s", c.Provider, c.Status, c.StatusNote)
		}
	}
}
