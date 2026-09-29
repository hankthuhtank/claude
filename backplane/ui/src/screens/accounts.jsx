import { useState } from "preact/hooks";
import { call } from "../api.js";
import { useStore, go, providerName, ago, toast } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Unit, Led, Badge, Status, Modal, Field, Guide, ExtLink, useAction, Empty, openLink } from "../ui.jsx";
import { refreshBoot } from "../boot.js";

const MATURITY = {
  build: ["Builds & monitors", "accent"],
  connect: ["Connect & monitor", ""],
  planned: ["Planned", ""],
};

export function Accounts({ add, then }) {
  const boot = useStore((s) => s.boot);
  const practiceOn = useStore((s) => s.practice);
  const providers = useStore((s) => s.providers);
  const [adding, setAdding] = useState(add || null); // provider id or "pick"
  const [editing, setEditing] = useState(null);
  const conns = (boot?.connections || []).filter((c) => !c.practice);
  const practice = (boot?.connections || []).filter((c) => c.practice);
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <div class="eyebrow">Accounts</div>
          <h1>Connected accounts</h1>
          <p class="lede">The services your backends run on. Keys are encrypted on this computer, never written to project files, exports or logs, and each connection is re-verified before every build and check.</p>
        </div>
        <Btn kind="primary" icon="plus" size="lg" onClick={() => setAdding("pick")}>Connect an account</Btn>
      </div>
      {conns.length === 0 ? (
        <Empty icon="plug" title="No accounts yet" action={<Btn kind="primary" icon="plug" onClick={() => setAdding("pick")}>Connect your first account</Btn>}>
          Most presets use Cloudflare, Supabase, Stripe, Resend and GitHub. Every one has a free tier; the guide for each takes about a minute.
        </Empty>
      ) : (
        <div class="stack" style="gap:8px">{conns.map((c) => <ConnUnit c={c} onEdit={() => setEditing(c)} />)}</div>
      )}
      {practice.length ? (
        <div style="margin-top:26px">
          <div class="spread" style="margin-bottom:10px">
            <div class="row"><h2>Practice connections</h2><Badge led={practiceOn ? "ok" : undefined}>{practiceOn ? "Practice mode on" : "Practice mode off"}</Badge></div>
            {!practiceOn ? <Btn size="sm" icon="play" onClick={() => go("practice")}>Start practice mode</Btn> : null}
          </div>
          <p class="small muted" style="margin-top:-4px">{practiceOn ? "They point at the built-in simulator on this computer." : "They only work while practice mode is running, so they're switched off now."}</p>
          <div class="stack" style={`gap:8px${practiceOn ? "" : ";opacity:.62"}`}>{practice.map((c) => <ConnUnit c={c} />)}</div>
        </div>
      ) : null}
      {adding === "pick" ? <PickProvider providers={providers} onPick={(id) => setAdding(id)} onClose={() => setAdding(null)} /> : null}
      {adding && adding !== "pick" ? <ConnForm provider={providers.find((p) => p.id === adding)} then={then} onClose={() => setAdding(null)} /> : null}
      {editing ? <ConnForm provider={editing.providerInfo} existing={editing} onClose={() => setEditing(null)} /> : null}
    </div>
  );
}

function ConnUnit({ c, onEdit }) {
  const [busy, run] = useAction();
  const verify = async () => {
    const r = await run(() => call("VerifyConnection", { id: c.id }));
    await refreshBoot();
    toast(r.verify.health === "ok" ? "ok" : r.verify.health === "warn" ? "warn" : "fail", c.label, r.verify.summary);
  };
  const remove = async () => {
    if (!confirm(`Remove ${c.label}? Its key is deleted from this computer. Nothing changes in your ${providerName(c.provider)} account.`)) return;
    await run(() => call("DeleteConnection", { id: c.id }), "Connection removed");
    await refreshBoot();
  };
  return (
    <Unit>
      <div class="face" style="display:grid;grid-template-columns:1fr auto;gap:6px 18px;align-items:center">
        <div style="min-width:0">
          <div class="silk">{providerName(c.provider)}{c.mode ? ` · ${c.mode} mode` : ""}</div>
          <div class="title" style="font-size:1.15rem">{c.label}</div>
          <div class="row small ink2">
            <Status h={c.status}>{c.statusNote || "Not verified yet"}</Status>
          </div>
          <div class="row small muted" style="margin-top:4px">
            {c.accountName ? <span>{c.accountName}</span> : null}
            <span class="mono">{c.hint}</span>
            {c.status === "off" ? null : <span>verified {ago(c.verifiedAt)}</span>}
            {c.usedBy?.length ? <span>used by {c.usedBy.join(", ")}</span> : <span>not used by a backend yet</span>}
          </div>
          {c.warnings?.length ? <ul class="small" style="margin:6px 0 0;padding-left:18px">{c.warnings.map((w) => <li>{w}</li>)}</ul> : null}
        </div>
        <div class="row">
          {c.status === "off" ? null : <Btn size="sm" icon="refresh" busy={busy} onClick={verify}>Verify</Btn>}
          {onEdit ? <Btn size="sm" icon="key" onClick={onEdit}>Replace key</Btn> : null}
          {onEdit ? <Btn size="sm" kind="ghost" icon="trash" aria-label="Remove" onClick={remove} /> : null}
        </div>
      </div>
    </Unit>
  );
}

function PickProvider({ providers, onPick, onClose }) {
  const groups = {};
  for (const p of providers) (groups[p.maturity] ||= []).push(p);
  return (
    <Modal wide title="Connect an account" eyebrow="Choose a service" onClose={onClose}>
      {["build", "connect", "planned"].map((m) => groups[m]?.length ? (
        <div>
          <div class="row" style="margin-bottom:8px"><h3>{MATURITY[m][0]}</h3>
            <span class="small muted">{m === "build" ? "Backplane creates, configures, tests and repairs resources here." : m === "connect" ? "Verified and watched as part of your backend's health; nothing is created inside them yet." : "Listed so you can see what's coming. Not available in this version."}</span>
          </div>
          <div class="grid-3">
            {groups[m].map((p) => (
              <button type="button" class="card preset" disabled={m === "planned"} onClick={() => onPick(p.id)} style={m === "planned" ? "opacity:.55;cursor:not-allowed" : ""}>
                <div class="spread"><b class="h3">{p.name}</b><Badge kind={MATURITY[m][1]}>{MATURITY[m][0]}</Badge></div>
                <div class="small ink2">{p.tagline}</div>
              </button>
            ))}
          </div>
        </div>
      ) : null)}
    </Modal>
  );
}

function ConnForm({ provider, existing, then, onClose }) {
  const [vals, setVals] = useState({});
  const [label, setLabel] = useState(existing?.label || "");
  const [show, setShow] = useState({});
  const [result, setResult] = useState(null);
  const [busy, run] = useAction();
  if (!provider) return null;
  const save = async () => {
    const fields = {};
    for (const f of provider.fields) if (vals[f.key] !== undefined) fields[f.key] = vals[f.key];
    const r = existing
      ? await run(() => call("UpdateConnection", { id: existing.id, label, fields }))
      : await run(() => call("AddConnection", { provider: provider.id, label, fields }));
    setResult(r);
    await refreshBoot();
  };
  const bad = (f) => f.pattern && vals[f.key] && !new RegExp(f.pattern).test(vals[f.key]);
  const ready = existing ? true : provider.fields.filter((f) => f.required).every((f) => (vals[f.key] || "").trim());
  return (
    <Modal wide title={`${existing ? "Update" : "Connect"} ${provider.name}`} eyebrow={provider.tagline} onClose={onClose}
      foot={result ? <>
        <Btn onClick={onClose}>Done</Btn>
        {then ? <Btn kind="primary" icon="right" onClick={() => go("project", { ...then, tab: "build", autoplan: true })}>Back to the backend</Btn> : null}
      </> : <>
        <Btn onClick={onClose}>Cancel</Btn>
        <Btn kind="primary" icon="shield" busy={busy} disabled={!ready} onClick={save}>{existing ? "Save & verify" : "Connect & verify"}</Btn>
      </>}>
      {result ? <VerifyResult r={result.verify} /> : (
        <div class="grid-2" style="align-items:start">
          <div>
            <h3>How to get the key</h3>
            <Guide steps={provider.guide || []} />
            {provider.tokenUrl ? <div style="margin-top:12px"><Btn icon="ext" onClick={() => openLink(provider.tokenUrl)}>Open the key page</Btn></div> : null}
            {provider.scopes?.length ? (
              <div class="small" style="margin-top:14px"><div class="silk">Permissions Backplane asks for</div>
                <ul style="margin:4px 0 0;padding-left:18px">{provider.scopes.map((s) => <li>{s}</li>)}</ul>
              </div>
            ) : null}
          </div>
          <div class="stack">
            <Field label="Label (optional)" help="How this account appears in Backplane, e.g. “Stripe — live”.">{(id) => <input id={id} class="input" value={label} onInput={(e) => setLabel(e.currentTarget.value)} />}</Field>
            {provider.fields.map((f) => (
              <Field label={f.label + (f.required ? "" : " (optional)")} help={existing && f.secret ? "Leave blank to keep the current value." : f.help}
                error={bad(f) ? "That doesn't look right — check you copied the whole value." : null}>
                {(id) => f.options?.length ? (
                  <select id={id} class="select" onChange={(e) => setVals({ ...vals, [f.key]: e.currentTarget.value })}>{f.options.map((o) => <option>{o}</option>)}</select>
                ) : (
                  <div class="input-affix">
                    <input id={id} class="input mono" type={f.secret && !show[f.key] ? "password" : "text"} autocomplete="off" spellcheck={false}
                      placeholder={existing && f.secret ? "••••••••" : f.placeholder || ""} value={vals[f.key] ?? (existing && !f.secret ? existing.settings?.[f.key] || "" : "")}
                      aria-invalid={bad(f) ? "true" : undefined} onInput={(e) => setVals({ ...vals, [f.key]: e.currentTarget.value.trim() })} />
                    {f.secret ? <Btn icon="eye" aria-label={show[f.key] ? "Hide" : "Show"} onClick={() => setShow({ ...show, [f.key]: !show[f.key] })} /> : null}
                  </div>
                )}
              </Field>
            ))}
            {provider.oauthNote ? <div class="small muted">{provider.oauthNote}</div> : null}
            <div class="notice info"><Icon name="lock" /><div class="small">Stored with Windows DPAPI encryption for your user account. Backplane checks the key right away and shows exactly what it can and can't do.</div></div>
          </div>
        </div>
      )}
    </Modal>
  );
}

function VerifyResult({ r }) {
  return (
    <div class="stack">
      <div class="row"><Status h={r.health}><span class="h3">{r.health === "ok" ? "Connected" : r.health === "warn" ? "Connected with warnings" : "Not working yet"}</span></Status></div>
      <div>{r.summary}</div>
      {r.problem ? <div class="notice fail"><Icon name="fail" /><div><b>{r.problem.title}</b><div class="small">{r.problem.summary}</div></div></div> : null}
      {r.missing?.length ? <div class="notice warn"><Icon name="warn" /><div><b>Missing permissions</b><div class="small">{r.missing.join(", ")}</div></div></div> : null}
      {r.warnings?.map((w) => <div class="notice warn"><Icon name="warn" /><div class="small">{w}</div></div>)}
      {r.scopes?.length ? <div class="small"><span class="silk">Verified access</span><div class="chips" style="margin-top:4px">{r.scopes.map((s) => <span class="chip">{s}</span>)}</div></div> : null}
      {r.details ? <dl class="kv">{Object.entries(r.details).map(([k, v]) => <><dt>{k.replaceAll("_", " ")}</dt><dd class="mono" style="white-space:pre-wrap">{v}</dd></>)}</dl> : null}
      <div class="small muted">Checked in {r.latencyMs} ms.</div>
    </div>
  );
}
