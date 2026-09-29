import { useState } from "preact/hooks";
import { call } from "../api.js";
import { useStore, go, providerName, toast, set as setState } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Led, Badge, Status, Empty, useAction, useCall, ProblemCard } from "../ui.jsx";
import { refreshBoot } from "../boot.js";

// The Bench: a practice sandbox with a "break it" lab. Inject a real-world
// failure, watch Backplane find it, and apply the repair.
export function Bench() {
  const practice = useStore((s) => s.practice);
  const boot = useStore((s) => s.boot);
  const [busy, run] = useAction();
  const state = useCall("PracticeState", {}, [practice]);
  const projects = (boot?.projects || []).filter((p) => p.practice);
  const [pid, setPid] = useState(projects[0]?.id || "");
  const [outcome, setOutcome] = useState(null);
  const start = async () => {
    await run(() => call("StartPractice"), "Practice sandbox running");
    setState({ practice: true });
    await refreshBoot();
    state.reload(true);
  };
  const stop = async () => {
    await run(() => call("StopPractice"), "Sandbox stopped — practice backends were reset");
    setState({ practice: false });
    await refreshBoot();
  };
  const project = projects.find((p) => p.id === pid) || projects[0];
  const env = project?.environments?.find((e) => e.built) || project?.environments?.[0];
  const doBreak = async (b) => {
    setOutcome(null);
    const r = await run(() => call("PracticeBreak", { projectId: project.id, env: env.name, break: b.id }));
    toast("warn", b.label, r.message);
    setOutcome({ b, r, report: null });
  };
  const check = async () => {
    const res = await run(() => call("Check", { projectId: project.id, env: env.name, kind: outcome.b.check === "full" ? "full" : "quick", wait: true }));
    setOutcome({ ...outcome, report: res.report });
    refreshBoot();
  };
  const repair = async (fix) => {
    if (fix.action !== "repair") { go("project", { id: project.id, env: env.name, tab: "health" }); return; }
    const plan = await run(() => call("Repair", { projectId: project.id, env: env.name, fix }));
    go("project", { id: project.id, env: env.name, tab: "build", plan });
    toast("info", "Repair planned", "Review the work order and approve it.");
  };
  const groups = {};
  for (const b of state.data?.breaks || []) (groups[b.group] ||= []).push(b);
  const problems = (outcome?.report?.results || []).filter((r) => r.problem && r.health !== "ok");
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <div class="eyebrow">Bench</div>
          <h1>Practice & break it</h1>
          <p class="lede">A private simulator of Cloudflare, Supabase, Stripe, Resend and GitHub runs on this computer. Build real presets against it, then break things on purpose and watch Backplane find and fix them. Free, safe, and it resets when Backplane closes.</p>
        </div>
        {practice ? <Btn icon="stop" busy={busy} onClick={stop}>Stop sandbox</Btn> : <Btn kind="primary" size="lg" icon="play" busy={busy} onClick={start}>Start the sandbox</Btn>}
      </div>
      {!practice ? null : (
        <div class="stack">
          <div class="card">
            <div class="card-head"><h3>Simulated accounts</h3><span class="small muted">{state.data?.note}</span></div>
            <div class="chips">{(state.data?.connections || []).map((c) => <span class="chip"><Led h={c.status} /><b>{providerName(c.provider)}</b></span>)}</div>
          </div>
          {projects.length === 0 ? (
            <Empty icon="flask" title="Build something to break" action={<Btn kind="primary" icon="plus" onClick={() => go("new", { template: "software-store" })}>Practice with the Software Store</Btn>}>
              Create a backend with “Practice run” switched on — the Software Store preset exercises every service.
            </Empty>
          ) : (
            <>
              <div class="row">
                <span class="silk">Practice backend</span>
                <select class="select" style="width:auto" value={project?.id} onChange={(e) => setPid(e.currentTarget.value)}>
                  {projects.map((p) => <option value={p.id}>{p.name}</option>)}
                </select>
                {env ? <><Led h={env.overall} /><span class="small">{env.headline}</span></> : null}
                <Btn size="sm" kind="ghost" onClick={() => go("project", { id: project.id, env: env?.name })}>Open</Btn>
              </div>
              {!env?.built ? <div class="notice info"><Icon name="info" /><div>Build <b>{project.name}</b> first (open it and approve the plan) — then come back to break it.</div></div> : null}
              {outcome ? (
                <div class="card stack">
                  <div class="spread">
                    <div><div class="silk">Injected</div><div class="h2">{outcome.b.label}</div><div class="small ink2">{outcome.r.message}</div></div>
                    {!outcome.report ? <Btn kind="primary" icon={outcome.b.check === "full" ? "shield" : "pulse"} busy={busy} onClick={check}>Run {outcome.b.check === "full" ? "full certification" : "quick check"}</Btn> : null}
                  </div>
                  <div class="notice info"><Icon name="eye" /><div><b>What Backplane should report:</b> {outcome.b.expect}</div></div>
                  {outcome.report ? (
                    <>
                      <div class="row"><Status h={outcome.report.overall}><b>{outcome.report.headline}</b></Status></div>
                      {problems.slice(0, 3).map((r) => <ProblemCard problem={r.problem} health={r.health} onFix={repair} busy={busy} />)}
                      {!problems.length ? <div class="notice ok"><Icon name="ok" /><div>Everything passed — this failure recovered on its own (for example, retries rode out the rate limit).</div></div> : null}
                    </>
                  ) : null}
                </div>
              ) : null}
              {Object.entries(groups).map(([g, items]) => (
                <div>
                  <h3 style="margin:8px 0 10px">{g}</h3>
                  <div class="grid-3">
                    {items.map((b) => (
                      <div class="card stack" style="gap:8px">
                        <div class="spread"><b>{b.label}</b><Badge>{providerName(b.provider)}</Badge></div>
                        <div class="small ink2">{b.explain}</div>
                        <div class="row" style="justify-content:space-between">
                          <span class="small muted">Found by the {b.check === "full" ? "full certification" : "quick check"}</span>
                          <Btn size="sm" kind="danger" icon="bolt" busy={busy} disabled={!env?.built} onClick={() => doBreak(b)}>Break it</Btn>
                        </div>
                        {b.undo ? <button type="button" class="linkish small" style="justify-self:start" onClick={() => run(() => call("PracticeBreak", { projectId: project.id, env: env.name, break: b.undo }), "Restored")}>Undo this failure</button> : null}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  );
}
