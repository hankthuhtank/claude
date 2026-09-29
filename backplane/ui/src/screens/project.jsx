import { useEffect, useRef, useState } from "preact/hooks";
import { call, onEvent, upload } from "../api.js";
import { useStore, go, providerName, when, ago, toast, healthWord } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Tabs, Led, Badge, Status, Seg, Field, Toggle, Modal, ConfirmName, useCall, useAction, Empty, ErrorNote, copyText } from "../ui.jsx";
import { RackView, Inventory } from "./rack.jsx";
import { HealthTab, HistoryTab } from "./health.jsx";
import { PlanView, RunView } from "./plan.jsx";
import { refreshBoot } from "../boot.js";
import { Question } from "./newbackend.jsx";

export function Project({ id, env: env0, tab: tab0, autoplan, runId, plan: plan0 }) {
  const boot = useStore((s) => s.boot);
  const summary = boot?.projects?.find((p) => p.id === id);
  const [env, setEnv] = useState(env0 || summary?.activeEnv || "production");
  const [tab, setTab] = useState(tab0 || "rack");
  const { data: dash, error, reload } = useCall("Dashboard", { projectId: id, env }, []);
  const [plan, setPlan] = useState(plan0 || null);
  const [activeRun, setActiveRun] = useState(null);
  const [fixBusy, runFix] = useAction();
  const timer = useRef(0);

  useEffect(() => {
    const off = onEvent((ev) => {
      if (ev.project !== id || (ev.env && ev.env !== env)) return;
      if (ev.type === "run" || ev.type === "report" || ev.type === "monitor") {
        clearTimeout(timer.current);
        timer.current = setTimeout(() => reload(true), 400);
      }
    });
    return off;
  }, [id, env]);

  // Show a live build when one is running.
  useEffect(() => {
    if (!dash) return;
    const live = (dash.runs || []).find((r) => r.status === "running" || r.status === "rolling_back");
    if (live && !activeRun) setActiveRun(live);
    if (runId && !activeRun) {
      const r = (dash.runs || []).find((x) => x.id === runId);
      if (r) setActiveRun(r);
    }
  }, [dash?.runs?.[0]?.id, dash?.runs?.[0]?.status]);

  useEffect(() => { if (autoplan && dash && !plan && !activeRun) makePlan(); }, [!!dash]);

  const [planBusy, runPlan] = useAction();
  const makePlan = async (opts = {}) => {
    const p = await runPlan(() => call("Plan", { projectId: id, env, ...opts }));
    setPlan(p);
    setActiveRun(null);
    setTab("build");
  };
  const onFix = async (fix) => {
    if (fix.action === "reconnect" || fix.action === "guide") { go("accounts", { add: fix.target, then: { id, env } }); return; }
    if (fix.action === "upload") { setTab("settings"); return; }
    const p = await runFix(() => call("Repair", { projectId: id, env, fix }));
    setPlan(p);
    setActiveRun(null);
    setTab("build");
  };

  if (error && !dash) return <div class="page"><ErrorNote error={error} /><Btn onClick={() => go("home")}>Back</Btn></div>;
  if (!dash) return <div class="page"><div class="boot" style="height:40vh"><span class="boot-led" />Reading the rack…</div></div>;
  const p = dash.project;
  const envState = p.environments.find((e) => e.name === env) || {};
  const building = !!(activeRun && (activeRun.status === "running" || activeRun.status === "rolling_back"));
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <button type="button" class="linkish small" onClick={() => go("home")}><Icon name="left" size={13} /> Backends</button>
          <div class="eyebrow" style="margin-top:8px">{p.templateName}{p.practice ? " · practice" : ""}{p.imported ? " · monitor only" : ""}</div>
          <h1>{p.name}</h1>
          <div class="row" style="margin-top:8px">
            <Led h={envState.overall} lg busy={building} />
            <b style="font-family:var(--display);letter-spacing:.08em;text-transform:uppercase">{building ? "Building…" : envState.headline}</b>
            <span class="small muted">{envState.checkedAt ? "checked " + ago(envState.checkedAt) : ""}</span>
            {dash.monitor?.enabled ? <span class="small muted">· monitored{dash.monitor.nextQuick ? `, next check ${when(dash.monitor.nextQuick)}` : ""}</span> : <span class="small muted">· monitoring off</span>}
          </div>
        </div>
        <div class="stack" style="gap:8px;justify-items:end">
          {p.environments.length > 1 ? <Seg label="Environment" value={env} onChange={(e) => { setEnv(e); setPlan(null); setActiveRun(null); }} options={p.environments.map((e) => [e.name, e.name])} /> : <span class={`stamp ${env === "production" ? "prod" : ""}`}>{env}</span>}
          <div class="row">
            {!p.imported ? <Btn icon="bolt" busy={planBusy} disabled={building} onClick={() => makePlan()}>Plan build</Btn> : null}
            <Btn kind="primary" icon="shield" disabled={building || !envState.built} onClick={() => setTab("health")}>Certify</Btn>
          </div>
        </div>
      </div>

      {envState.missingConnections?.length && !p.practice ? (
        <div class="notice warn" style="margin-bottom:14px"><Icon name="plug" /><div>
          {envState.missingConnections.map((m) => providerName(m)).join(", ")} {envState.missingConnections.length === 1 ? "is" : "are"} not connected for {env}.{" "}
          <button class="linkish" onClick={() => go("accounts", { add: envState.missingConnections[0], then: { id, env } })}>Connect now</button> or assign an existing account in <button class="linkish" onClick={() => setTab("settings")}>Settings</button>.
        </div></div>
      ) : null}

      <Tabs value={tab} onChange={setTab} tabs={[
        ["rack", "Rack"],
        ["health", "Health", dash.report ? <Led h={dash.report.overall} /> : null],
        ["history", "History"],
        ["build", "Build", building ? <Led busy /> : plan ? <Badge>plan ready</Badge> : null],
        ["code", "Code"],
        ["settings", "Settings"],
      ]} />

      {tab === "rack" ? (
        <div class="stack">
          <RackView dash={dash} />
          <div class="card">
            <div class="card-head"><h3>Inventory</h3><span class="small muted">{dash.resources.length} resources · {dash.secrets} secrets in the vault for {env}</span></div>
            <Inventory rows={dash.resources} />
            {dash.blueprint.notes?.length ? <ul class="small ink2" style="margin:10px 0 0;padding-left:18px">{dash.blueprint.notes.map((n) => <li>{n}</li>)}</ul> : null}
          </div>
        </div>
      ) : null}
      {tab === "health" ? <HealthTab dash={dash} project={p} env={env} onFix={onFix} busyFix={fixBusy} reload={() => reload(true)} /> : null}
      {tab === "history" ? <HistoryTab dash={dash} project={p} env={env} /> : null}
      {tab === "build" ? (
        <BuildTab dash={dash} project={p} env={env} plan={plan} setPlan={setPlan} activeRun={activeRun} setActiveRun={setActiveRun}
          makePlan={makePlan} planBusy={planBusy} onCertify={() => { setTab("health"); call("Check", { projectId: id, env, kind: "full" }).catch((e) => toast("warn", "Could not start the check", e.message)); }} reload={reload} />
      ) : null}
      {tab === "code" ? <CodeTab project={p} env={env} /> : null}
      {tab === "settings" ? <SettingsTab dash={dash} project={p} env={env} reload={reload} onPlan={(pl) => { setPlan(pl); setTab("build"); }} /> : null}
    </div>
  );
}

// ---- build ----

function BuildTab({ dash, project, env, plan, setPlan, activeRun, setActiveRun, makePlan, planBusy, onCertify, reload }) {
  const [busy, run] = useAction();
  const [confirm, setConfirm] = useState(null);
  const rollback = async (r, typed = "") => {
    try {
      const res = await run(() => call("Rollback", { projectId: project.id, env, runId: r.id, confirm: typed }));
      setConfirm(null);
      setActiveRun(res);
    } catch (e) {
      if (e.confirm) setConfirm(r);
    }
  };
  const restore = async (sn) => {
    const p = await run(() => call("RestoreSnapshot", { projectId: project.id, env, snapshotId: sn.id }));
    setPlan(p);
  };
  const snaps = useCall("Snapshots", { projectId: project.id, env }, [dash.runs?.[0]?.id]);
  if (project.imported) return <Empty icon="import" title="Monitor only">Imported backends are watched, never changed. Build and repair are available for backends created from a preset.</Empty>;
  return (
    <div class="stack">
      {activeRun ? <RunView project={project} env={env} run={activeRun} onDone={(r) => { setActiveRun(r); reload(true); refreshBoot(); }} onCertify={onCertify} /> : null}
      {plan && !activeRun ? <PlanView plan={plan} project={project} onApproved={(r) => { setPlan(null); setActiveRun(r); }} onDiscard={() => setPlan(null)} /> : null}
      {!plan && !activeRun ? (
        <Empty icon="bolt" title="Plan before you build"
          action={<Btn kind="primary" size="lg" icon="bolt" busy={planBusy} onClick={() => makePlan()}>Plan the build</Btn>}>
          Backplane reads what already exists, then writes a work order listing every resource it would create or change, why, and what it costs. You approve it; then it builds, step by step, with checkpoints.
        </Empty>
      ) : null}
      <div class="grid-2" style="align-items:start">
        <div class="card">
          <div class="card-head"><h3>Recent builds</h3></div>
          {(dash.runs || []).length === 0 ? <div class="muted small">No builds yet.</div> : null}
          {(dash.runs || []).map((r) => (
            <div class="oprow">
              <Led h={r.status === "succeeded" ? "ok" : r.status === "failed" ? "fail" : r.status === "canceled" || r.status === "rolled_back" ? "warn" : undefined} busy={r.status === "running"} />
              <div>
                <div>{r.plan?.purpose || "Build"} <span class="muted small">· {when(r.startedAt)}</span></div>
                <div class="note">{r.status.replace("_", " ")}{r.error ? ` — ${r.error.title}` : ""}</div>
              </div>
              <div class="row tight">
                <Btn size="sm" kind="ghost" onClick={() => setActiveRun(r)}>View</Btn>
                {r.status === "succeeded" || r.status === "failed" ? <Btn size="sm" kind="ghost" icon="undo" busy={busy} onClick={() => rollback(r)}>Roll back</Btn> : null}
              </div>
            </div>
          ))}
        </div>
        <div class="card">
          <div class="card-head"><h3>Snapshots</h3><span class="small muted">Restorable configurations</span></div>
          {(snaps.data || []).slice(0, 12).map((s) => (
            <div class="oprow">
              <Icon name={s.knownGood ? "shield" : "clock"} size={14} />
              <div><div>{s.reason}</div><div class="note">{when(s.createdAt)} · {s.resources} resources{s.knownGood ? " · known good" : ""}</div></div>
              <Btn size="sm" kind="ghost" busy={busy} onClick={() => restore(s)}>Restore…</Btn>
            </div>
          ))}
          {!snaps.data?.length ? <div class="muted small">Backplane snapshots the configuration before and after every build, and after every passing full check.</div> : null}
        </div>
      </div>
      {confirm ? <ConfirmName name={project.name} what="Roll back production" busy={busy} onConfirm={(v) => rollback(confirm, v)} onClose={() => setConfirm(null)} /> : null}
    </div>
  );
}

// ---- code ----

const ROLE = { generated: ["Generated", ""], "user-modified": ["User modified", "accent"], "system-config": ["System config", ""], missing: ["Missing", ""] };

function CodeTab({ project, env }) {
  const [path, setPath] = useState(null);
  const [editing, setEditing] = useState(null);
  const { data, reload } = useCall("Code", { projectId: project.id, path }, []);
  const [busy, run] = useAction();
  const [conflicts, setConflicts] = useState(null);
  const [exported, setExported] = useState(null);
  const regen = async (overwrite = []) => {
    const r = await run(() => call("RegenerateCode", { projectId: project.id, overwrite }));
    reload();
    if (r.kept?.length) setConflicts(r.kept);
    else toast("ok", r.written.length ? `Regenerated ${r.written.length} file(s)` : "Everything is current");
  };
  const save = async () => {
    await run(() => call("SaveCode", { projectId: project.id, path, content: editing }), "Saved — marked USER MODIFIED");
    setEditing(null);
    reload();
  };
  const doExport = async () => {
    const r = await run(() => call("Export", { projectId: project.id, env }));
    setExported(r);
  };
  const files = data?.files || [];
  return (
    <div class="stack">
      <div class="spread">
        <p class="small ink2" style="margin:0;max-width:72ch">This is the backend's own code — it lives in your GitHub repository and on this computer. Edit anything: Backplane marks edited files <b>USER MODIFIED</b> and never overwrites them without asking.</p>
        <div class="row">
          <Btn icon="folder" onClick={() => call("OpenCodeFolder", { id: project.id }).catch((e) => toast("warn", e.message))}>Open folder</Btn>
          <Btn icon="refresh" busy={busy} onClick={() => regen()}>Regenerate</Btn>
          <Btn icon="download" busy={busy} onClick={doExport}>Export…</Btn>
        </div>
      </div>
      <div class="codeview">
        <div class="files">
          {files.map((f) => (
            <button type="button" class="file" aria-pressed={f.path === path ? "true" : "false"} onClick={() => { setPath(f.path); setEditing(null); }}>
              <span class="p">{f.path}</span>
              <span class="row tight"><Badge kind={ROLE[f.status]?.[1] || ""}>{f.role === "system-config" && f.status === "generated" ? "System config" : ROLE[f.status]?.[0] || f.status}</Badge>{f.updateAvailable ? <Badge>update available</Badge> : null}</span>
            </button>
          ))}
        </div>
        <div>
          {!path ? <Empty icon="code" title="Pick a file">The Worker, the database schema, email templates and the deploy workflow are all here.</Empty> : editing !== null ? (
            <div class="stack" style="gap:10px">
              <textarea class="code" value={editing} onInput={(e) => setEditing(e.currentTarget.value)} spellcheck={false} aria-label={`Editing ${path}`} />
              <div class="row" style="justify-content:flex-end"><Btn onClick={() => setEditing(null)}>Cancel</Btn><Btn kind="primary" busy={busy} onClick={save}>Save</Btn></div>
            </div>
          ) : (
            <div class="stack" style="gap:10px">
              <div class="spread">
                <b class="mono">{path}</b>
                <div class="row"><Btn size="sm" icon="copy" onClick={() => copyText(data?.content || "")}>Copy</Btn><Btn size="sm" icon="code" onClick={() => setEditing(data?.content || "")}>Edit</Btn></div>
              </div>
              <pre class="code">{data?.content}</pre>
            </div>
          )}
        </div>
      </div>
      {conflicts ? (
        <Modal title="You edited these files" eyebrow="Nothing was overwritten" onClose={() => setConflicts(null)}
          foot={<><Btn onClick={() => setConflicts(null)}>Keep my edits</Btn><Btn kind="danger" busy={busy} onClick={() => { const c = conflicts; setConflicts(null); regen(c); }}>Replace with generated</Btn></>}>
          <p class="lede">Backplane generated newer versions, but these files have your changes. Keep them, or replace them with the generated versions.</p>
          <ul class="mono small">{conflicts.map((c) => <li>{c}</li>)}</ul>
        </Modal>
      ) : null}
      {exported ? (
        <Modal title="Exported" eyebrow="Your backend, portable" onClose={() => setExported(null)}
          foot={<><Btn icon="folder" onClick={() => call("ShowFile", { url: exported.path })}>Show in folder</Btn><Btn kind="primary" onClick={() => setExported(null)}>Done</Btn></>}>
          <p class="mono small" style="overflow-wrap:anywhere">{exported.path}</p>
          <p class="small ink2">Manifest (no secrets), generated code, a ready Wrangler config and an OpenTofu starting point with import blocks.</p>
          {exported.notes?.map((n) => <div class="notice info"><Icon name="info" /><div class="small">{n}</div></div>)}
        </Modal>
      ) : null}
    </div>
  );
}

// ---- settings ----

function SettingsTab({ dash, project, env, reload, onPlan }) {
  const boot = useStore((s) => s.boot);
  const [busy, run] = useAction();
  const [name, setName] = useState(project.name);
  const [mon, setMon] = useState(project.monitor);
  const [newEnv, setNewEnv] = useState("");
  const [warn, setWarn] = useState([]);
  const [removing, setRemoving] = useState(false);
  const conns = boot?.connections || [];
  const template = dash.template;
  const fileQ = template?.questions?.find((q) => q.kind === "file");
  const [uploadPct, setUploadPct] = useState(null);

  const saveGeneral = () => run(() => call("UpdateProject", { id: project.id, name, monitor: mon }), "Saved").then(() => { refreshBoot(); reload(true); });
  const assign = async (e, prov, connectionId) => {
    const r = await run(() => call("AssignConnection", { projectId: project.id, env: e, provider: prov, connectionId }));
    setWarn(r.warnings || []);
    refreshBoot();
    reload(true);
  };
  const addEnv = async () => {
    await run(() => call("AddEnvironment", { projectId: project.id, name: newEnv, cloneFrom: env }), `Added ${newEnv}`);
    setNewEnv("");
    refreshBoot();
    reload(true);
  };
  const teardown = async () => {
    const p = await run(() => call("Plan", { projectId: project.id, env, teardown: true }));
    onPlan(p);
  };
  const remove = async (confirm = "") => {
    try {
      await run(() => call("DeleteProject", { id: project.id, confirm }));
      await refreshBoot();
      toast("ok", `${project.name} removed from Backplane`, "Its live resources were not touched.");
      go("home");
    } catch (e) {
      if (e.confirm) setRemoving(true);
    }
  };
  const pickFile = async (f) => {
    if (!f) return;
    setUploadPct(0);
    try {
      await upload(project.id, f, setUploadPct);
      toast("ok", "Product file stored", "It uploads to private storage on the next build.");
      reload(true);
    } catch (e) {
      toast("fail", "Upload failed", e.message);
    } finally {
      setUploadPct(null);
    }
  };
  const provs = dash.blueprint ? [...new Set((dash.blueprint.components || []).filter((c) => c.provider).map((c) => c.provider))] : [];
  return (
    <div class="grid-2" style="align-items:start">
      <div class="stack">
        <div class="card stack">
          <h3>General</h3>
          <Field label="Name">{(id) => <input id={id} class="input" value={name} onInput={(e) => setName(e.currentTarget.value)} />}</Field>
          <div class="field">
            <span class="label">Monitoring</span>
            <Toggle id="mon-on" checked={mon.enabled} onChange={(v) => setMon({ ...mon, enabled: v })} label="Check this backend automatically" />
            <div class="row small">
              <span>Quick check every</span>
              <select class="select" style="width:auto" value={mon.quickEveryMin} onChange={(e) => setMon({ ...mon, quickEveryMin: Number(e.currentTarget.value) })}>
                {[5, 15, 30, 60, 180, 720].map((m) => <option value={m}>{m < 60 ? `${m} minutes` : `${m / 60} hour${m === 60 ? "" : "s"}`}</option>)}
              </select>
              <span>· full certification every</span>
              <select class="select" style="width:auto" value={mon.fullEveryHours} onChange={(e) => setMon({ ...mon, fullEveryHours: Number(e.currentTarget.value) })}>
                {[0, 6, 12, 24, 72, 168].map((h) => <option value={h}>{h === 0 ? "never" : h < 24 ? `${h} hours` : `${h / 24} day${h === 24 ? "" : "s"}`}</option>)}
              </select>
            </div>
            <Toggle id="mon-after" checked={mon.checkAfterBuild} onChange={(v) => setMon({ ...mon, checkAfterBuild: v })} label="Certify after every build" />
            <Toggle id="mon-notify" checked={mon.notify} onChange={(v) => setMon({ ...mon, notify: v })} label="Desktop notification when health changes" />
          </div>
          <div class="row" style="justify-content:flex-end"><Btn kind="primary" busy={busy} onClick={saveGeneral}>Save</Btn></div>
        </div>

        {template && !project.imported ? <AnswersCard template={template} dash={dash} project={project} reload={reload} /> : null}

        {fileQ ? (
          <div class="card stack">
            <h3>{fileQ.label}</h3>
            <div class="small ink2">{dash.blueprint.params?.product_file ? <>Current file: <span class="mono">{String(dash.blueprint.params.product_file).split(/[\\/]/).pop()}</span></> : "No file yet — customers need one before the first sale."}</div>
            <input type="file" onChange={(e) => pickFile(e.currentTarget.files?.[0])} />
            {uploadPct !== null ? <div class="progress"><i style={`width:${Math.round(uploadPct * 100)}%`} /></div> : null}
          </div>
        ) : null}

        <div class="card stack">
          <h3>Danger zone</h3>
          {!project.imported ? <div class="spread"><div><b>Tear down {env}</b><div class="small ink2">Plan the removal of everything Backplane created here. You review it first.</div></div><Btn kind="danger" icon="trash" busy={busy} onClick={teardown}>Plan teardown</Btn></div> : null}
          <div class="spread"><div><b>Remove from Backplane</b><div class="small ink2">Forget this backend on this computer. Live resources stay where they are.</div></div><Btn kind="danger" busy={busy} onClick={() => remove()}>Remove</Btn></div>
        </div>
      </div>

      <div class="stack">
        <div class="card stack">
          <h3>Accounts per environment</h3>
          {warn.map((w) => <div class="notice warn"><Icon name="warn" /><div class="small">{w}</div></div>)}
          <table class="t">
            <thead><tr><th>Service</th>{project.environments.map((e) => <th>{e.name}</th>)}</tr></thead>
            <tbody>
              {provs.map((prov) => (
                <tr>
                  <td><b>{providerName(prov)}</b></td>
                  {project.environments.map((e) => {
                    const current = { id: dash.connections?.[e.name]?.[prov] || "" };
                    const options = conns.filter((c) => c.provider === prov && !!c.practice === !!project.practice);
                    return (
                      <td>
                        <select class="select" style="min-width:150px" value={current?.id || ""} onChange={(ev) => assign(e.name, prov, ev.currentTarget.value)}>
                          <option value="">— not connected —</option>
                          {options.map((c) => <option value={c.id}>{c.label}{c.mode ? ` (${c.mode})` : ""}</option>)}
                        </select>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
          <div class="small muted">Use test-mode keys outside production. Backplane blocks live Stripe keys in development and staging.</div>
        </div>

        <div class="card stack">
          <h3>Environments</h3>
          <div class="chips">{project.environments.map((e) => <span class="chip"><Led h={e.overall} /><b>{e.name}</b>{e.built ? "" : " · not built"}</span>)}</div>
          <div class="row">
            <input class="input" style="max-width:220px" placeholder="e.g. staging" value={newEnv} onInput={(e) => setNewEnv(e.currentTarget.value.toLowerCase())} aria-label="New environment name" />
            <Btn icon="plus" busy={busy} disabled={!newEnv.trim()} onClick={addEnv}>Add (copy of {env})</Btn>
          </div>
          <div class="small muted">A new environment reuses this blueprint with its own resources and, where you choose, its own accounts.</div>
        </div>
      </div>
      {removing ? <ConfirmName name={project.name} what="Remove from Backplane" busy={busy} onConfirm={(v) => { setRemoving(false); remove(v); }} onClose={() => setRemoving(false)} /> : null}
    </div>
  );
}

function AnswersCard({ template, dash, project, reload }) {
  const [answers, setAnswers] = useState(() => ({ ...(dash.answers || {}) }));
  const [adv, setAdv] = useState(false);
  const [busy, run] = useAction();
  const qs = template.questions.filter((q) => q.kind !== "file" && (!q.advanced || adv));
  const save = async () => {
    const r = await run(() => call("UpdateProject", { id: project.id, answers }));
    if (r.keptUserFiles?.length) toast("warn", "Your edited files were kept", r.keptUserFiles.join(", "), 10000);
    toast("ok", "Settings saved", "Plan the build to see exactly what changes.");
    reload(true);
  };
  return (
    <div class="card stack">
      <div class="spread"><h3>Preset answers</h3><Toggle id="ans-adv" checked={adv} onChange={setAdv} label="Advanced" /></div>
      {qs.map((q) => <Question q={q} value={answers[q.key]} onChange={(v) => setAnswers({ ...answers, [q.key]: v })} />)}
      <div class="row" style="justify-content:flex-end"><Btn kind="primary" busy={busy} onClick={save}>Save answers</Btn></div>
      <div class="small muted">Changes apply on the next approved build — the plan shows every difference first.</div>
    </div>
  );
}
