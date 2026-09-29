import { useEffect, useState } from "preact/hooks";
import { call } from "../api.js";
import { useStore, toast, providerName, when, set as setState, get } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Toggle, Field, Seg, Status, Badge, useAction, useCall, ExtLink, copyText } from "../ui.jsx";
import { applySettings, refreshBoot } from "../boot.js";

const THEMES = [
  ["rack", "Rack", "Black anodized gear", ["#1d2023", "#2cc4d3", "#eceae4"]],
  ["aluminum", "Aluminum", "Brushed panels, daylight", ["#f5f5f4", "#00707e", "#1b1d1f"]],
  ["bench", "Bench", "ESD mat, probe blue", ["#eef2ef", "#1d5fa8", "#18211e"]],
  ["copper", "Copper", "Warm cable run", ["#231b16", "#e0915a", "#f2e7db"]],
  ["fiber", "Fiber", "Aqua OM3 at night", ["#141e23", "#33cfe0", "#e2eef1"]],
  ["contrast", "High contrast", "Maximum legibility", ["#000000", "#ffe100", "#ffffff"]],
  ["system", "System", "Follows Windows light/dark", ["#1d2023", "#f5f5f4", "#2cc4d3"]],
];

async function saveSettings(patch) {
  const cur = get().settings;
  const next = { ...cur, ...patch };
  applySettings(next);
  setState({ settings: next });
  try {
    const saved = await call("SaveSettings", next);
    setState({ settings: saved });
    return saved;
  } catch (e) {
    applySettings(cur);
    setState({ settings: cur });
    toast("fail", "Could not save", e.message);
    throw e;
  }
}

export function ThemePicker() {
  const settings = useStore((s) => s.settings);
  return (
    <div class="swatches" role="group" aria-label="Theme">
      {THEMES.map(([id, name, sub, [panel, accent, ink]]) => (
        <button type="button" class="swatch" aria-pressed={settings.theme === id ? "true" : "false"} onClick={() => saveSettings({ theme: id })}>
          <div class="sample" style={`background:${panel}`}>
            <i style={`background:${accent}`} />
            <div><b style={`background:${ink};width:70%`} /><b style={`background:${ink};opacity:.45;width:45%`} /></div>
          </div>
          <div class="name">{name}<div class="small muted" style="letter-spacing:0;text-transform:none;font-family:var(--sans);font-weight:400">{sub}</div></div>
        </button>
      ))}
    </div>
  );
}

export function Settings({ section }) {
  const settings = useStore((s) => s.settings);
  const boot = useStore((s) => s.boot);
  const [tab, setTab] = useState(section || "look");
  const secs = [["look", "Appearance"], ["behave", "Behaviour"], ["assistant", "Assistant"], ["security", "Security review"], ["changes", "Provider changes"], ["about", "About"]];
  return (
    <div class="page">
      <div class="page-head"><div class="grow"><div class="eyebrow">Settings</div><h1>Settings</h1></div></div>
      <div class="catalog">
        <div class="industries">
          {secs.map(([k, l]) => <button type="button" class="industry" aria-pressed={tab === k ? "true" : "false"} onClick={() => setTab(k)}><b>{l}</b></button>)}
        </div>
        <div class="stack">
          {tab === "look" ? (
            <div class="card stack">
              <h3>Theme</h3>
              <ThemePicker />
              <div class="grid-2">
                <div class="field"><span class="label">Density</span><Seg label="Density" value={settings.density} onChange={(v) => saveSettings({ density: v })} options={[["comfortable", "Comfortable"], ["compact", "Compact"]]} /></div>
                <Field label={`Text size · ${Math.round(settings.textScale * 100)}%`}>{(id) => <input id={id} type="range" min="0.9" max="1.35" step="0.05" value={settings.textScale} onChange={(e) => saveSettings({ textScale: Number(e.currentTarget.value) })} />}</Field>
              </div>
              <Toggle id="motion" checked={settings.reduceMotion} onChange={(v) => saveSettings({ reduceMotion: v })} label="Reduce motion (no blinking LEDs or cable tone)" />
            </div>
          ) : null}
          {tab === "behave" ? (
            <div class="card stack">
              <h3>Behaviour</h3>
              <Toggle id="adv" checked={settings.showAdvanced} onChange={(v) => saveSettings({ showAdvanced: v })} label="Show advanced options everywhere" />
              <div class="field">
                <Toggle id="bg" checked={settings.backgroundTask} onChange={(v) => saveSettings({ backgroundTask: v }).then(() => toast("ok", v ? "Background checks on" : "Background checks off", v ? "A quick check of every monitored backend runs hourly while Backplane is closed." : ""))} label="Keep checking while Backplane is closed" />
                <div class="help">{boot.os === "windows" ? "Adds a Windows scheduled task for your user (no administrator rights). Notifications appear when health changes." : "Available on Windows."}</div>
              </div>
              <div class="field"><span class="label">Default environment for new backends</span>
                <Seg label="Default environment" value={settings.defaultEnv} onChange={(v) => saveSettings({ defaultEnv: v })} options={[["production", "Production"], ["development", "Development"]]} />
              </div>
            </div>
          ) : null}
          {tab === "assistant" ? <Assistant /> : null}
          {tab === "security" ? <Security /> : null}
          {tab === "changes" ? <Changes /> : null}
          {tab === "about" ? <About /> : null}
        </div>
      </div>
    </div>
  );
}

function Assistant() {
  const { data, reload } = useCall("AIState", {});
  const [key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [busy, run] = useAction();
  if (!data) return null;
  const save = async () => { await run(() => call("SetAIKey", { key, model: model || data.model }), "Claude connected"); setKey(""); reload(); };
  const remove = async () => { await run(() => call("SetAIKey", { key: "" }), "Assistant removed"); reload(); };
  return (
    <div class="card stack">
      <div class="spread"><h3>Plain-English assistant</h3>{data.enabled ? <Badge led="ok">Connected · {data.model}</Badge> : <Badge>Offline reader only</Badge>}</div>
      <p class="small ink2" style="margin:0">The built-in reader turns descriptions into presets on this computer. Optionally, Claude can give a second opinion on harder descriptions. It only ever suggests a preset and answers — you still review the plan, and nothing is built without your approval.</p>
      <div class="notice info"><Icon name="lock" /><div class="small">{data.privacy}</div></div>
      <Field label={data.enabled ? `Replace API key (current ${data.hint})` : "Anthropic API key"} help="Create one at console.anthropic.com → API keys. Stored encrypted on this computer.">
        {(id) => <input id={id} class="input mono" type="password" autocomplete="off" placeholder="sk-ant-…" value={key} onInput={(e) => setKey(e.currentTarget.value.trim())} />}
      </Field>
      <Field label="Model">{(id) => <select id={id} class="select" value={model || data.model} onChange={(e) => setModel(e.currentTarget.value)}>{data.models.map((m) => <option value={m}>{m}</option>)}</select>}</Field>
      <div class="row" style="justify-content:flex-end">
        {data.enabled ? <Btn kind="ghost" busy={busy} onClick={remove}>Remove key</Btn> : null}
        <Btn kind="primary" busy={busy} disabled={!key && !(data.enabled && model && model !== data.model)} onClick={save}>{data.enabled ? "Save" : "Verify & connect"}</Btn>
      </div>
      <ExtLink href="https://console.anthropic.com/settings/keys">Open the Anthropic console</ExtLink>
    </div>
  );
}

function Security() {
  const { data, reload, loading } = useCall("Security", {});
  if (!data) return <div class="muted">Reviewing…</div>;
  const sev = { ok: "ok", info: "unknown", warn: "warn", fail: "fail" };
  return (
    <div class="card stack">
      <div class="spread"><div><h3>Security review</h3><div class="small ink2">{data.score} · {data.secrets} secrets in the vault ({data.vault})</div></div><Btn size="sm" icon="refresh" busy={loading} onClick={() => reload()}>Re-run</Btn></div>
      {data.findings.map((f) => (
        <div class="result" style={`grid-template-columns:18px 1fr auto`}>
          <Icon name={f.severity === "ok" ? "ok" : f.severity === "fail" ? "fail" : f.severity === "warn" ? "warn" : "info"} class="s" />
          <div><div class="t">{f.title}</div><div class="sum">{f.detail}</div>{f.fix ? <div class="small" style="margin-top:3px"><b>Fix:</b> {f.fix}</div> : null}</div>
          <span class="silk">{f.area}</span>
        </div>
      ))}
    </div>
  );
}

function Changes() {
  const { data } = useCall("Versions", {});
  if (!data) return <div class="muted">Checking…</div>;
  return (
    <div class="stack">
      <div class="card stack">
        <h3>Provider changes</h3>
        <div class="small ink2">{data.summary} Backplane pins every provider's API version, records any deprecation notices providers send, and shows dated announcements that affect your backends.</div>
        <table class="t">
          <thead><tr><th>Service</th><th>Pinned</th><th>Status</th></tr></thead>
          <tbody>{data.providers.map((p) => <tr><td><b>{p.name}</b><div class="small muted">{p.how}</div></td><td class="mono small">{p.pinned}</td><td><Status h={p.status === "current" ? "ok" : p.status === "action-needed" ? "fail" : "warn"}>{p.status.replace("-", " ")}</Status>{p.note ? <div class="small ink2">{p.note}</div> : null}</td></tr>)}</tbody>
        </table>
      </div>
      {data.notices.map((n) => (
        <div class={`notice ${n.severity === "action" && n.applies ? "fail" : n.severity === "warn" ? "warn" : "info"}`}>
          <Icon name={n.severity === "action" && n.applies ? "warn" : "info"} />
          <div>
            <div class="spread"><b>{n.title}</b><span class="small muted">{n.date}</span></div>
            <div class="small">{n.body}</div>
            <div class="small muted" style="margin-top:4px">{n.applies ? `Affects: ${n.projects.join(", ")}` : "Doesn't affect your backends."} {n.link ? <ExtLink href={n.link}>Details</ExtLink> : null}</div>
          </div>
        </div>
      ))}
      {data.signals?.length ? (
        <div class="card">
          <h3 style="margin-bottom:8px">Signals from providers</h3>
          <table class="t"><thead><tr><th>Service</th><th>Header</th><th>Endpoint</th><th>Seen</th></tr></thead>
            <tbody>{data.signals.map((s) => <tr><td>{providerName(s.provider)}</td><td class="mono small">{s.header}: {s.value}</td><td class="mono small">{s.endpoint}</td><td class="small">{when(s.lastSeen)} · {s.count}×</td></tr>)}</tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}

function About() {
  const { data } = useCall("SystemInfo", {});
  if (!data) return null;
  return (
    <div class="card stack">
      <h3>About Backplane</h3>
      <dl class="kv">
        <dt>Version</dt><dd>{data.version}</dd>
        <dt>Data folder</dt><dd class="mono small">{data.dataDir} <button class="linkish small" onClick={() => copyText(data.dataDir)}>copy</button></dd>
        <dt>Secrets</dt><dd>{data.secrets} stored · {data.vault}</dd>
        <dt>Running for</dt><dd>{data.uptime}</dd>
        <dt>Platform</dt><dd>{data.os}/{data.arch} · {data.go}</dd>
      </dl>
      <div class="small ink2">Made by Safi Solutions. Fonts: Barlow Condensed, Atkinson Hyperlegible Next & Mono (SIL Open Font License). UI built with Preact (MIT).</div>
      <ExtLink href="https://www.safisolutions.org">safisolutions.org</ExtLink>
    </div>
  );
}
