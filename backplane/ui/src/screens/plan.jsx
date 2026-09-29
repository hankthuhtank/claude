import { useEffect, useRef, useState } from "preact/hooks";
import { call, onEvent } from "../api.js";
import { providerName, clock, toast, when } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Led, Badge, Status, ProblemCard, ConfirmName, useAction } from "../ui.jsx";

const ACT = { create: "Create", adopt: "Use existing", update: "Update", replace: "Recreate", keep: "Keep", delete: "Delete", detach: "Stop tracking" };

/** A plan rendered as a work order. Nothing runs until Approve. */
export function PlanView({ plan, project, onApproved, onDiscard }) {
  const [all, setAll] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, run] = useAction();
  const blocked = plan.blockers?.length > 0;
  const changes = (plan.operations || []).filter((o) => o.action !== "keep");
  const approve = async (confirm = "") => {
    try {
      const r = await run(() => call("Approve", { planId: plan.id, confirm }));
      setConfirming(false);
      onApproved?.(r);
    } catch (e) {
      if (e.confirm) setConfirming(true);
    }
  };
  const start = () => (plan.destructive && plan.production ? setConfirming(true) : approve());
  return (
    <div class="wo">
      <div class="wo-head">
        <div>
          <div class="wo-no">WORK ORDER {plan.id.split("_").pop().toUpperCase()} · {when(plan.createdAt)}</div>
          <div class="h2" style="margin-top:4px">{plan.purpose || (changes.length ? `${changes.length} change${changes.length === 1 ? "" : "s"} to make` : "Nothing to change")}</div>
          <div class="small ink2" style="margin-top:2px">{project?.name} · {countsLine(plan.counts)}</div>
        </div>
        {plan.practice ? <span class="stamp practice">Practice</span> : <span class={`stamp ${plan.production ? "prod" : ""}`}>{plan.environment}</span>}
      </div>
      <div class="wo-body">
        <div class="stack" style="gap:8px;margin:12px 0">
          {(plan.blockers || []).map((b) => <div class="notice fail"><Icon name="fail" /><div><b>Must fix first:</b> {b}</div></div>)}
          {(plan.warnings || []).map((w) => <div class="notice warn"><Icon name="warn" /><div>{w}</div></div>)}
          {plan.destructive ? <div class="notice fail"><Icon name="warn" /><div><b>This plan deletes resources.</b> {plan.production ? "Production deletions ask you to type the project name." : "Review the list below."}</div></div> : null}
        </div>
        {(plan.summary || []).filter((g) => g.action !== "KEEP" || all).map((g) => (
          <div class="wo-group">
            <div class="who"><b class="h3">{providerName(g.provider)}</b><span class={`act ${g.action.toLowerCase().split(" ")[0]}`}>{g.action}</span></div>
            <ul>{g.lines.map((l) => <li>{l}</li>)}</ul>
          </div>
        ))}
        {plan.connections?.length ? (
          <div class="wo-group">
            <div class="who"><b class="h3">Connections</b><span class="act">Wired</span></div>
            <ul>{plan.connections.map((c) => <li><span>{c.from} <span class="muted">→</span> {c.to}</span>{c.label ? <span class="small muted mono">{c.label}</span> : null}</li>)}</ul>
          </div>
        ) : null}
        {plan.costs?.length ? (
          <div class="wo-group">
            <div class="who"><b class="h3">{plan.practice ? "Cost" : "Estimated cost"}</b><span class="small muted">{plan.practice ? "Nothing is billed in practice mode" : "Estimates, not guarantees"}</span></div>
            <table class="t">
              <thead><tr><th>Service</th><th>Estimate</th><th>Assumes</th></tr></thead>
              <tbody>{plan.costs.map((c) => <tr><td><b>{providerName(c.provider)}</b></td><td>{c.estimate}</td><td class="small ink2">{c.basis}{c.upgrade ? <div class="muted">{c.upgrade}</div> : null}</td></tr>)}</tbody>
            </table>
          </div>
        ) : null}
        <div style="margin-top:12px">
          <button type="button" class="linkish small" onClick={() => setAll(!all)}>{all ? "Hide" : "Show"} every step ({plan.operations?.length || 0})</button>
          {all ? (
            <div style="margin-top:8px">
              {(plan.operations || []).map((o) => (
                <div class="opline">
                  <Icon name={o.action === "keep" ? "check" : o.action === "delete" ? "trash" : "bolt"} size={16} />
                  <div>
                    <div><b>{o.title}</b> <span class="small muted">{providerName(o.provider)}</span></div>
                    {o.why ? <div class="why">{o.why}</div> : null}
                    {o.changes?.length ? <div class="changes">{o.changes.map((c) => <div>{c.field}: {c.actual} → {c.expected}</div>)}</div> : null}
                  </div>
                  <span class={`act ${o.action}`}>{ACT[o.action] || o.action}</span>
                </div>
              ))}
            </div>
          ) : null}
        </div>
        <div class="spread" style="margin-top:18px;border-top:2px solid var(--line-2);padding-top:14px">
          <span class="small muted">Backplane checkpoints after every step: a stopped build resumes where it left off, and a finished one can be rolled back.</span>
          <div class="row">
            {onDiscard ? <Btn onClick={onDiscard}>Discard</Btn> : null}
            <Btn kind={plan.destructive ? "danger" : "primary"} size="lg" icon="bolt" busy={busy} disabled={blocked || changes.length === 0} onClick={start}>
              {changes.length === 0 ? "Already up to date" : "Approve & build"}
            </Btn>
          </div>
        </div>
      </div>
      {confirming ? <ConfirmName name={project.name} what="Approve & build" busy={busy} onConfirm={(v) => approve(v)} onClose={() => setConfirming(false)} /> : null}
    </div>
  );
}

export function countsLine(c = {}) {
  const parts = [];
  for (const k of ["create", "adopt", "update", "replace", "delete", "detach", "keep"]) if (c[k]) parts.push(`${c[k]} ${ACT[k].toLowerCase()}`);
  return parts.join(" · ") || "no steps";
}

/** A running or finished build, updated live. */
export function RunView({ project, env, run: initial, onDone, onCertify }) {
  const [run, setRun] = useState(initial);
  const [lines, setLines] = useState([]);
  const [busy, act] = useAction();
  const tape = useRef(null);
  useEffect(() => {
    setRun(initial);
    const off = onEvent((ev) => {
      if (ev.runId !== initial.id) return;
      if (ev.type === "run") setRun(ev.data);
      if (ev.type === "log" && ev.data?.level !== "debug") setLines((l) => [...l.slice(-400), ev.data]);
      if (ev.type === "op") setRun((r) => r && ({ ...r, ops: r.ops.map((o) => o.opId === ev.data.opId ? { ...o, status: ev.data.status, note: ev.data.note ?? o.note, error: ev.data.problem ?? o.error } : o) }));
    });
    call("Logs", { runId: initial.id, level: "info", limit: 300 }).then((l) => setLines(l.reverse())).catch(() => {});
    return off;
  }, [initial.id]);
  useEffect(() => { tape.current && (tape.current.scrollTop = tape.current.scrollHeight); }, [lines.length]);
  useEffect(() => { if (run && run.status !== "running" && run.status !== "rolling_back") onDone?.(run); }, [run?.status]);
  if (!run) return null;
  const ops = run.ops || [];
  const done = ops.filter((o) => o.status === "succeeded" || o.status === "skipped").length;
  const live = run.status === "running" || run.status === "rolling_back";
  const failed = ops.find((o) => o.status === "failed");
  const opById = Object.fromEntries((run.plan?.operations || []).map((o) => [o.id, o]));
  const resume = () => act(() => call("Resume", { projectId: project.id, env, runId: run.id })).then(setRun);
  const cancel = () => act(() => call("Cancel", { projectId: project.id, env, runId: run.id }), "Stopping after the steps in flight");
  return (
    <div class="card">
      <div class="card-head">
        <div>
          <div class="silk">{run.plan?.purpose || "Build"} · {env}</div>
          <div class="h2">{live ? (run.status === "rolling_back" ? "Rolling back…" : "Building…") : run.status === "succeeded" ? "Build finished" : run.status === "failed" ? "Build stopped" : run.status === "canceled" ? "Build canceled" : run.status === "rolled_back" ? "Rolled back" : run.status}</div>
        </div>
        <div class="row">
          {live ? <Btn icon="stop" busy={busy} onClick={cancel}>Cancel</Btn> : null}
          {(run.status === "failed" || run.status === "canceled") ? <Btn kind="primary" icon="play" busy={busy} onClick={resume}>Resume</Btn> : null}
          {run.status === "succeeded" && onCertify ? <Btn kind="primary" icon="shield" onClick={onCertify}>Certify it now</Btn> : null}
        </div>
      </div>
      <div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax={ops.length} aria-valuenow={done}><i style={`width:${ops.length ? (done / ops.length) * 100 : 0}%`} /></div>
      <div class="small muted" style="margin:6px 0 12px">{done} of {ops.length} steps{run.resumes ? ` · resumed ${run.resumes}×` : ""}</div>
      {failed?.error ? <div style="margin-bottom:12px"><ProblemCard problem={failed.error} /></div> : null}
      <div class="grid-2" style="align-items:start">
        <div>
          {ops.map((o) => {
            const op = opById[o.opId] || {};
            return (
              <div class={`oprow ${o.status === "failed" ? "failed" : ""}`}>
                <Led busy={o.status === "running"} h={o.status === "succeeded" ? "ok" : o.status === "failed" ? "fail" : o.status === "blocked" ? "warn" : undefined} />
                <div>
                  <div>{op.title || o.opId}</div>
                  {o.note ? <div class="note">{o.note}</div> : null}
                </div>
                <span class="small muted">{o.status === "skipped" ? "no change" : o.status.replace("_", " ")}{o.attempts > 1 ? ` · ${o.attempts} tries` : ""}</span>
              </div>
            );
          })}
        </div>
        <div class="tape" ref={tape} aria-live="off" aria-label="Build log">
          {lines.map((l) => <div class="ln" style="grid-template-columns:70px 1fr"><span class="muted">{clock(l.at)}</span><span class="msg">{l.message}</span></div>)}
        </div>
      </div>
    </div>
  );
}
