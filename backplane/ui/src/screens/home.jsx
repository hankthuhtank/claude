import { useState } from "preact/hooks";
import { call } from "../api.js";
import { useStore, go, ago, providerName, toast } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Unit, Led, Badge, Empty, Modal, Field, useAction, Status } from "../ui.jsx";
import { refreshBoot } from "../boot.js";

export function Home() {
  const boot = useStore((s) => s.boot);
  const practice = useStore((s) => s.practice);
  const [importing, setImporting] = useState(false);
  const projects = boot?.projects || [];
  const real = projects.filter((p) => !p.practice);
  const prac = projects.filter((p) => p.practice);
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <div class="eyebrow">Your rack</div>
          <h1>Backends</h1>
          <p class="lede">Every backend you run, with the result of its latest certification. Pick one to trace its connections, check it, or change it.</p>
        </div>
        <div class="row">
          <Btn icon="import" onClick={() => setImporting(true)}>Import existing</Btn>
          <Btn kind="primary" icon="plus" size="lg" onClick={() => go("new")}>New backend</Btn>
        </div>
      </div>

      {projects.length === 0 ? (
        <Empty title="The rack is empty" icon="rack"
          action={<div class="row" style="justify-content:center">
            <Btn kind="primary" icon="plus" onClick={() => go("new")}>Set up a backend</Btn>
            <Btn icon="flask" onClick={() => go("bench")}>Try it in the practice sandbox</Btn>
          </div>}>
          Start from a preset for your kind of business — a store, bookings, a CRM, a membership site — or describe what you need in plain English. Nothing is created until you approve a plan.
        </Empty>
      ) : null}

      {real.length ? <RackList projects={real} /> : null}
      {prac.length ? (
        <div style="margin-top:28px">
          <div class="spread" style="margin-bottom:10px">
            <div class="row"><h2>Practice</h2><Badge led={practice ? "ok" : undefined}>{practice ? "Sandbox running" : "Sandbox off"}</Badge></div>
            {!practice ? <Btn size="sm" icon="play" onClick={() => go("bench")}>Start sandbox</Btn> : null}
          </div>
          <RackList projects={prac} />
        </div>
      ) : null}
      {importing ? <ImportModal onClose={() => setImporting(false)} /> : null}
    </div>
  );
}

function RackList({ projects }) {
  return (
    <div class="stack" style="gap:8px">
      {projects.map((p) => {
        const envs = p.environments || [];
        const main = envs.find((e) => e.name === p.activeEnv) || envs[envs.length - 1] || {};
        return (
          <Unit onClick={() => go("project", { id: p.id, env: main.name })} label={`Open ${p.name}`}>
            <div class="face" style="grid-template-columns:1fr auto;display:grid;gap:4px 18px;align-items:center">
              <div style="min-width:0">
                <div class="silk">{p.templateName}{p.imported ? " · monitor only" : ""}</div>
                <div class="title">{p.name}</div>
                <div class="row small ink2" style="margin-top:2px">
                  {p.providers.map((pr) => <span class="chip">{providerName(pr)}</span>)}
                </div>
              </div>
              <div class="stack" style="gap:6px;justify-items:end">
                {envs.map((e) => (
                  <div class="row tight" style="justify-content:flex-end">
                    <span class="silk">{e.name}</span>
                    <Led h={e.runningRun ? undefined : e.overall} busy={!!e.runningRun} />
                    <span class="small" style="min-width:140px;text-align:right">
                      {e.runningRun ? "Building…" : e.failedRun ? "Build stopped — resume" : e.headline}
                    </span>
                  </div>
                ))}
                <span class="small muted">{main.checkedAt ? "Checked " + ago(main.checkedAt) : main.built ? "Not checked yet" : "Not built yet"}</span>
              </div>
            </div>
          </Unit>
        );
      })}
    </div>
  );
}

function ImportModal({ onClose }) {
  const boot = useStore((s) => s.boot);
  const conns = (boot?.connections || []).filter((c) => c.providerInfo?.maturity !== "planned");
  const [picked, setPicked] = useState(() => new Set(conns.filter((c) => !c.practice).map((c) => c.id)));
  const [found, setFound] = useState(null);
  const [chosen, setChosen] = useState(new Set());
  const [name, setName] = useState("");
  const [env, setEnv] = useState("production");
  const [busy, run] = useAction();

  const scan = async () => {
    const r = await run(() => call("Discover", { connectionIds: [...picked] }));
    setFound(r);
    setChosen(new Set(r.items.map((it, i) => (it.monitorable && !it.managedBy ? i : -1)).filter((i) => i >= 0)));
  };
  const doImport = async () => {
    const items = [...chosen].map((i) => found.items[i]).map((it) => ({ connectionId: it.connectionId, provider: it.provider, kind: it.kind, id: it.id, name: it.name, props: it.props }));
    const idx = new Map([...chosen].map((i, n) => [i, n]));
    const links = (found.links || []).filter((l) => idx.has(l.from) && idx.has(l.to)).map((l) => ({ from: idx.get(l.from), to: idx.get(l.to), label: l.label }));
    const p = await run(() => call("ImportProject", { name, env, items, links }), "Imported — first check running");
    await refreshBoot();
    onClose();
    go("project", { id: p.id, env });
  };
  const toggle = (set, v) => { const n = new Set(set); n.has(v) ? n.delete(v) : n.add(v); return n; };

  return (
    <Modal wide title="Import an existing backend" eyebrow="Monitor what you already run" onClose={onClose}
      foot={found ? <><Btn onClick={() => setFound(null)}>Back</Btn><Btn kind="primary" icon="import" busy={busy} disabled={!name.trim() || chosen.size === 0} onClick={doImport}>Import {chosen.size} resource{chosen.size === 1 ? "" : "s"}</Btn></>
        : <><Btn onClick={onClose}>Cancel</Btn><Btn kind="primary" icon="search" busy={busy} disabled={picked.size === 0} onClick={scan}>Scan accounts</Btn></>}>
      {!found ? (
        <>
          <p class="lede">Backplane reads your accounts (read-only), lists what it finds and lets you pick what belongs together. Imported resources are monitored — never changed or deleted.</p>
          {conns.length === 0 ? <div class="notice info"><Icon name="info" /><div>Connect an account first on the <button class="linkish" onClick={() => { onClose(); go("accounts"); }}>Accounts</button> screen.</div></div> : null}
          <div class="stack" style="gap:6px">
            {conns.map((c) => (
              <label class="row" style="gap:10px;cursor:pointer">
                <input type="checkbox" checked={picked.has(c.id)} onChange={() => setPicked(toggle(picked, c.id))} />
                <Led h={c.status} /><b>{c.label}</b><span class="muted small">{c.accountName || ""}{c.practice ? " · practice" : ""}</span>
              </label>
            ))}
          </div>
        </>
      ) : (
        <>
          {Object.entries(found.errors || {}).map(([id, msg]) => <div class="notice warn"><Icon name="warn" /><div>{conns.find((c) => c.id === id)?.label}: {msg}</div></div>)}
          <div class="grid-2">
            <Field label="Name for this backend">{(id) => <input id={id} class="input" value={name} onInput={(e) => setName(e.currentTarget.value)} placeholder="e.g. Main store" />}</Field>
            <Field label="Environment">{(id) => <select id={id} class="select" value={env} onChange={(e) => setEnv(e.currentTarget.value)}><option value="production">Production</option><option value="staging">Staging</option><option value="development">Development</option></select>}</Field>
          </div>
          <div class="stack" style="gap:14px;max-height:48vh;overflow:auto">
            {Object.entries(found.groups).map(([group, idxs]) => (
              <div>
                <div class="silk" style="margin-bottom:6px">{group}</div>
                {idxs.map((i) => {
                  const it = found.items[i];
                  return (
                    <label class="row" style="gap:10px;padding:5px 0;cursor:pointer;align-items:flex-start">
                      <input type="checkbox" checked={chosen.has(i)} onChange={() => setChosen(toggle(chosen, i))} />
                      <div style="flex:1;min-width:0">
                        <div class="row tight"><b>{it.name}</b><span class="badge">{it.kindLabel}</span>{!it.monitorable ? <span class="badge">connection-level only</span> : null}</div>
                        <div class="small muted">{it.detail}{it.managedBy ? ` · already in ${it.managedBy}` : ""}</div>
                      </div>
                    </label>
                  );
                })}
              </div>
            ))}
          </div>
        </>
      )}
    </Modal>
  );
}
