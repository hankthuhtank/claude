import { useEffect, useState } from "preact/hooks";
import { call, onEvent } from "../api.js";
import { providerName, when, ago, pct, healthWord, toast } from "../state.js";
import { Icon, statusIcon } from "../icons.jsx";
import { Btn, Status, Badge, Levels, ProblemCard, Strip, Stat, Modal, Toggle, useAction, Empty, useCall } from "../ui.jsx";

const VERDICT = { ok: "PASS", warn: "ATTN", fail: "FAIL", unknown: "—" };

/** The certification report: verdict, six levels, every result, the punch list. */
export function CertReport({ report, names, onFix, busyFix, live }) {
  const [level, setLevel] = useState(0);
  if (!report) return null;
  const results = (report.results || []).filter((r) => !level || r.level === level);
  const problems = (report.results || []).filter((r) => r.problem && (r.health === "fail" || r.health === "warn"));
  problems.sort((a, b) => (a.health === "fail" ? 0 : 1) - (b.health === "fail" ? 0 : 1));
  return (
    <div class="cert">
      <div class="stack">
        <div class="card">
          <div class="verdict">
            <span class={`big ${report.overall}`}>{VERDICT[report.overall] || "—"}</span>
            <div>
              <div class="headline">{report.headline}</div>
              <div class="small ink2">{report.kind === "full" ? "Full certification" : "Quick check"} · {when(report.finishedAt || report.startedAt)} · {report.trigger}</div>
            </div>
          </div>
          <Levels levels={report.levels} names={names} value={level} onPick={setLevel} />
        </div>
        <div class="card">
          <div class="card-head"><h3>{level ? `Level ${level} · ${names?.[level]}` : "Every test"}</h3><span class="small muted">{results.length} result{results.length === 1 ? "" : "s"}</span></div>
          {results.length === 0 ? <div class="muted small">No results at this level{live ? " yet" : ""}.</div> : null}
          {results.map((r) => <ResultRow r={r} />)}
        </div>
      </div>
      <div class="stack">
        <div class="card">
          <h3 style="margin-bottom:8px">System health</h3>
          <div class="caps">
            {(report.capabilities || []).map((c) => <div class="cap"><span>{c.label}</span><Status h={c.health} /></div>)}
            {!report.capabilities?.length ? <div class="muted small">Appears after the first check of a built backend.</div> : null}
          </div>
        </div>
        {problems.length ? (
          <div class="stack" style="gap:10px">
            <h3>Punch list</h3>
            {problems.map((r) => <ProblemCard problem={r.problem} health={r.health} onFix={onFix} busy={busyFix} />)}
          </div>
        ) : report.overall === "ok" ? (
          <div class="notice ok"><Icon name="ok" /><div>Nothing to fix. {report.kind === "quick" ? "Run a full certification to exercise payments, email and storage end to end." : ""}</div></div>
        ) : null}
      </div>
    </div>
  );
}

function ResultRow({ r }) {
  const [open, setOpen] = useState(r.health === "fail");
  const h = r.health || "unknown";
  return (
    <div class={`result ${h}`}>
      <Icon name={statusIcon[h] || "ring"} class="s" />
      <div style="min-width:0">
        <button type="button" class="linkish" style="color:inherit;text-decoration:none" onClick={() => setOpen(!open)} aria-expanded={open ? "true" : "false"}>
          <span class="t">{r.title}</span>
        </button>
        <div class="sum">{r.summary}</div>
        {open ? (
          <>
            {r.expected || r.actual ? <dl class="xa"><dt>expected</dt><dd>{r.expected || "—"}</dd><dt>actual</dt><dd>{r.actual || "—"}</dd></dl> : null}
            {r.steps?.length ? <ul class="steps">{r.steps.map((s) => <li class={`result ${s.health}`} style="display:flex;border:0;padding:0"><Icon name={statusIcon[s.health] || "ring"} class="s" /><span>{s.title}{s.detail ? <span class="muted"> — {s.detail}</span> : null}{s.latencyMs ? <span class="muted"> · {s.latencyMs} ms</span> : null}</span></li>)}</ul> : null}
            {r.details ? <dl class="xa">{Object.entries(r.details).filter(([k]) => k !== "resource").map(([k, v]) => <><dt>{k}</dt><dd>{v}</dd></>)}</dl> : null}
          </>
        ) : null}
      </div>
      <div class="small muted num" style="text-align:right">L{r.level}{r.latencyMs ? <div>{r.latencyMs} ms</div> : null}</div>
    </div>
  );
}

/** Health tab: run checks and read the latest report. */
export function HealthTab({ dash, project, env, onFix, busyFix, reload }) {
  const [live, setLive] = useState(null); // in-progress results
  const [liveProbes, setLiveProbes] = useState(false);
  const [busy, run] = useAction();
  const stripeLive = (dash.providers || []).some((p) => p.provider === "stripe" && p.mode === "live");
  useEffect(() => {
    const off = onEvent((ev) => {
      if (ev.project !== project.id || ev.env !== env) return;
      if (ev.type === "check.start") setLive({ kind: ev.data?.kind, results: [] });
      if (ev.type === "check") setLive((l) => l ? { ...l, results: [...l.results, ev.data] } : { kind: "", results: [ev.data] });
      if (ev.type === "report") { setLive(null); reload(); }
    });
    return off;
  }, [project.id, env]);
  const start = (kind) => run(() => call("Check", { projectId: project.id, env, kind, allowLiveProbes: liveProbes })).then(() => setLive({ kind, results: [] }));
  const cancel = () => run(() => call("CancelCheck", { projectId: project.id, env }));
  const built = (dash.resources || []).some((r) => r.status !== "planned");
  const checking = dash.checking || !!live;
  const liveReport = live ? {
    overall: live.results.some((r) => r.health === "fail") ? "fail" : live.results.some((r) => r.health === "warn") ? "warn" : "unknown",
    headline: `${live.kind === "full" ? "Certifying" : "Checking"}… ${live.results.length} test${live.results.length === 1 ? "" : "s"} done`,
    kind: live.kind, trigger: "running", results: live.results, levels: levelsOf(live.results), capabilities: [],
  } : null;
  return (
    <div class="stack">
      <div class="spread">
        <div class="small ink2" style="max-width:70ch">
          <b>Quick check</b> (levels 1–4) reads configuration and tests every connection without writing anything. <b>Full certification</b> adds functional tests and a real end-to-end journey with test data that is cleaned up afterwards — nothing is ever charged.
        </div>
        <div class="row">
          {stripeLive ? <Toggle id="liveprobes" checked={liveProbes} onChange={setLiveProbes} label={<span class="small">Live-mode delivery probe</span>} /> : null}
          {checking ? <Btn icon="stop" busy={busy} onClick={cancel}>Stop</Btn> : null}
          <Btn icon="pulse" busy={busy} disabled={checking || !built} onClick={() => start("quick")}>Quick check</Btn>
          <Btn kind="primary" icon="shield" busy={busy} disabled={checking || !built} onClick={() => start("full")}>Full certification</Btn>
        </div>
      </div>
      {!built ? <Empty icon="shield" title="Build it first">Certification tests what exists. Plan and build this backend from the Build tab.</Empty> : null}
      {liveReport ? <CertReport report={liveReport} names={dash.levelNames} live onFix={onFix} busyFix={busyFix} /> : dash.report ? <CertReport report={dash.report} names={dash.levelNames} onFix={onFix} busyFix={busyFix} /> : built ? <Empty icon="shield" title="Not certified yet" action={<Btn kind="primary" icon="shield" onClick={() => start("full")}>Run full certification</Btn>}>Run the first certification to see every connection proven.</Empty> : null}
    </div>
  );
}

function levelsOf(results) {
  const out = {};
  const rank = { unknown: 0, skipped: 0, ok: 1, warn: 2, fail: 3 };
  for (const r of results) if ((rank[r.health] || 0) >= (rank[out[r.level]] || 0)) out[r.level] = r.health;
  return out;
}

/** History tab: uptime tiles, the status strip, and the table twin. */
export function HistoryTab({ dash, project, env }) {
  const [open, setOpen] = useState(null);
  const hist = dash.history || [];
  const latencies = {};
  for (const r of dash.report?.results || []) if (r.level === 4 && r.latencyMs) latencies[r.title] = r.latencyMs;
  return (
    <div class="stack">
      <div class="stats">
        <Stat label="Healthy, last 24 hours" value={pct(dash.uptime?.["24h"])} sub="Share of checks that passed or only warned" />
        <Stat label="Healthy, last 7 days" value={pct(dash.uptime?.["7d"])} />
        <Stat label="Healthy, last 30 days" value={pct(dash.uptime?.["30d"])} />
        <Stat label="Last full certification" value={dash.manifest?.lastFullCheck ? ago(dash.manifest.lastFullCheck) : "Never"} sub={dash.monitor?.nextFull ? "Next " + when(dash.monitor.nextFull) : ""} />
      </div>
      <div class="card">
        <div class="card-head"><h3>Every check</h3><span class="small muted">{hist.length} recorded · newest on the right</span></div>
        <Strip history={hist} onPick={(h) => h.reportId && setOpen(h)} />
      </div>
      <div class="card">
        <table class="t">
          <thead><tr><th>When</th><th>Check</th><th>Result</th><th>What changed</th></tr></thead>
          <tbody>
            {hist.slice(0, 200).map((h) => (
              <tr style="cursor:pointer" onClick={() => h.reportId && setOpen(h)}>
                <td class="num">{when(h.at)}</td>
                <td>{h.kind === "full" ? "Full" : "Quick"} <span class="muted small">· {h.trigger}</span></td>
                <td><Status h={h.overall}>{h.headline}</Status></td>
                <td class="small ink2">{h.note || ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!hist.length ? <div class="muted small">No history yet.</div> : null}
      </div>
      {open ? <ReportModal project={project} env={env} entry={open} names={dash.levelNames} onClose={() => setOpen(null)} /> : null}
    </div>
  );
}

function ReportModal({ project, env, entry, names, onClose }) {
  const { data, error } = useCall("GetReport", { projectId: project.id, env, reportId: entry.reportId });
  return (
    <Modal wide title={entry.headline} eyebrow={when(entry.at)} onClose={onClose}>
      {error ? <div class="notice warn"><Icon name="warn" /><div>{error.message}</div></div> : null}
      {data ? <CertReport report={data} names={names} /> : <div class="muted">Loading…</div>}
    </Modal>
  );
}
