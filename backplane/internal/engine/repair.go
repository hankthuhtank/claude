package engine

import (
	"fmt"
	"strings"

	"safisolutions.org/backplane/internal/core"
)

// RepairTarget is one resource a repair re-applies.
type RepairTarget struct {
	Key    string `json:"key"`
	Rotate bool   `json:"rotate,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// rotatable kinds can be replaced safely: a new credential is issued and the
// old one retired. Everything else is only re-applied.
var rotatable = map[string]bool{
	"supabase.api_key":        true,
	"resend.api_key":          true,
	"stripe.webhook_endpoint": true,
}

// ErrNotRepairable is returned for fixes the user has to carry out.
var ErrNotRepairable = fmt.Errorf("this fix cannot be applied automatically")

// RepairTargets turns a suggested fix into the resources it re-applies.
//
// Fix ids used by checks and drift detection:
//
//	reapply:<resource>          re-apply the resource's expected configuration
//	rotate:<resource>           replace a credential or webhook endpoint
//	rotate:<provider>           replace that provider's server key (Worker probe)
//	supabase.rls:<resource>:<t> turn row level security back on (re-apply schema)
func RepairTargets(bp *core.Blueprint, fix core.Fix) ([]RepairTarget, error) {
	prefix, rest, _ := strings.Cut(fix.ID, ":")
	reason := strings.TrimSuffix(fix.Label, ".")
	switch prefix {
	case "reapply":
		if bp.ResourceByKey(rest) != nil {
			return []RepairTarget{{Key: rest, Reason: reason}}, nil
		}
	case "rotate":
		if spec := bp.ResourceByKey(rest); spec != nil {
			return []RepairTarget{{Key: rest, Rotate: rotatable[spec.Kind], Reason: reason}}, nil
		}
		var out []RepairTarget
		for _, r := range bp.Resources {
			if r.Provider == rest && rotatable[r.Kind] && strings.HasSuffix(r.Kind, ".api_key") {
				out = append(out, RepairTarget{Key: r.Key, Rotate: true, Reason: reason})
			}
		}
		if fix.Target != "" && bp.ResourceByKey(fix.Target) != nil {
			out = append(out, RepairTarget{Key: fix.Target, Reason: reason})
		}
		if len(out) > 0 {
			return out, nil
		}
	case "supabase.rls":
		key, _, _ := strings.Cut(rest, ":")
		if bp.ResourceByKey(key) != nil {
			return []RepairTarget{{Key: key, Reason: "Turn row level security back on"}}, nil
		}
	}
	if fix.Action == "repair" && fix.Target != "" && bp.ResourceByKey(fix.Target) != nil {
		return []RepairTarget{{Key: fix.Target, Reason: reason}}, nil
	}
	return nil, ErrNotRepairable
}

// applyRepair narrows an operation to a repair. Targets are forced (and
// rotated when asked); resources of the same provider that depend on a target
// are forced too because they configure it (a Worker's secrets, a product's
// prices); resources elsewhere that use a target are re-checked and updated
// only if their inputs changed. Everything else is left alone.
func applyRepair(bp *core.Blueprint, spec *core.ResourceSpec, st *core.ResourceState, op *core.Operation, targets map[string]RepairTarget, inRepair map[string]bool, deps []string) {
	sameProvider, elsewhere := false, false
	for _, d := range deps {
		if !inRepair[d] {
			continue
		}
		if ds := bp.ResourceByKey(d); ds != nil && ds.Provider == spec.Provider {
			sameProvider = true
		} else {
			elsewhere = true
		}
	}
	t, direct := targets[spec.Key]
	switch {
	case direct:
		inRepair[spec.Key] = true
		if op.Action == core.ActKeep || op.Action == core.ActUpdate {
			op.Action = core.ActUpdate
			op.Force = true
		}
		if t.Rotate && st != nil && st.Status != core.StateDeleted && rotatable[spec.Kind] {
			op.Action = core.ActReplace
			op.Rotate = true
			op.Force = true
		}
		op.Why = "Repair: " + orStr(t.Reason, "re-apply the expected configuration") + "."
	case sameProvider:
		inRepair[spec.Key] = true
		if op.Action == core.ActKeep || op.Action == core.ActUpdate {
			op.Action = core.ActUpdate
			op.Force = true
			op.Why = "Refreshed with the repair because it configures something being repaired."
		}
	case elsewhere:
		inRepair[spec.Key] = true
		if op.Action == core.ActKeep {
			op.Action = core.ActUpdate
			op.Why = "Re-checked after the repair; changed only if something it uses changed."
		}
	default:
		switch op.Action {
		case core.ActCreate, core.ActAdopt, core.ActReplace:
			// Needed regardless (the repair may depend on it).
		default:
			op.Action = core.ActKeep
			op.Why = "Not part of this repair."
			op.Changes = nil
		}
	}
}
