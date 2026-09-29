import { useEffect, useRef, useState } from "preact/hooks";
import { call, onEvent } from "../api.js";
import { useStore, providerName, clock } from "../state.js";
import { Btn, Toggle, Empty, useCall } from "../ui.jsx";

// Activity: one redacted log for every provider, build and check.
export function Activity({ project: project0, runId }) {
  const boot = useStore((s) => s.boot);
  const providers = useStore((s) => s.providers);
  const [q, setQ] = useState({ project: project0 || "", provider: "", level: "info", text: "", runId: runId || "", limit: 800 });
  const [live, setLive] = useState(true);
  const { data, reload, loading } = useCall("Logs", q, []);
  const [extra, setExtra] = useState([]);
  const tape = useRef(null);
  useEffect(() => setExtra([]), [JSON.stringify(q)]);
  useEffect(() => {
    if (!live) return;
    return onEvent((ev) => {
      if (ev.type !== "log") return;
      const e = ev.data;
      if (q.project && e.project !== q.project) return;
      if (q.provider && e.provider !== q.provider) return;
      if (q.runId && e.runId !== q.runId) return;
      const rank = { debug: 0, info: 1, warn: 2, error: 3 };
      if ((rank[e.level] || 0) < (rank[q.level] || 0)) return;
      if (q.text && !(e.message + " " + (e.detail || "")).toLowerCase().includes(q.text.toLowerCase())) return;
      setExtra((x) => [e, ...x].slice(0, 500));
    });
  }, [live, JSON.stringify(q)]);
  const rows = [...extra, ...(data || [])];
  const set = (k, v) => setQ({ ...q, [k]: v });
  const projName = (id) => boot?.projects?.find((p) => p.id === id)?.name || "";
  // Only services you use: connected accounts and providers in a backend.
  const used = new Set([...(boot?.connections || []).map((c) => c.provider), ...(boot?.projects || []).flatMap((p) => p.providers || [])]);
  if (q.provider) used.add(q.provider);
  const services = providers.filter((p) => used.has(p.id));
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <div class="eyebrow">Activity</div>
          <h1>Everything that happened</h1>
          <p class="lede">Builds, checks, repairs and every provider call in one place. Secrets are masked before anything is written.</p>
        </div>
        <Toggle id="live" checked={live} onChange={setLive} label="Live" />
      </div>
      <div class="filters" role="search">
        <select class="select" value={q.project} onChange={(e) => set("project", e.currentTarget.value)} aria-label="Backend">
          <option value="">All backends</option>
          {(boot?.projects || []).map((p) => <option value={p.id}>{p.name}</option>)}
        </select>
        <select class="select" value={q.provider} onChange={(e) => set("provider", e.currentTarget.value)} aria-label="Service">
          <option value="">All services</option>
          {services.map((p) => <option value={p.id}>{p.name}</option>)}
        </select>
        <select class="select" value={q.level} onChange={(e) => set("level", e.currentTarget.value)} aria-label="Minimum level">
          <option value="debug">Everything (incl. API calls)</option><option value="info">Info and up</option><option value="warn">Warnings and errors</option><option value="error">Errors only</option>
        </select>
        <input class="input" type="search" placeholder="Search messages" value={q.text} onInput={(e) => set("text", e.currentTarget.value)} aria-label="Search" />
        {q.runId ? <Btn size="sm" onClick={() => set("runId", "")}>Clear build filter</Btn> : null}
        <Btn size="sm" icon="refresh" busy={loading} onClick={() => reload()}>Refresh</Btn>
      </div>
      {rows.length === 0 && !loading ? <Empty icon="tape" title="Nothing logged yet">Connect an account or build a backend and every step appears here.</Empty> : (
        <div class="tape" ref={tape} role="log">
          {rows.map((e) => (
            <div class="ln">
              <span class="muted num">{clock(e.at)}</span>
              <span class={`lv ${e.level}`}>{e.level}</span>
              <span class="src">{e.provider ? providerName(e.provider) : e.source}{e.project && !q.project ? ` · ${projName(e.project)}` : ""}</span>
              <span class="msg">{e.message}{e.requestId ? <span class="muted"> · req {e.requestId}</span> : null}{e.detail ? "\n" + e.detail : ""}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
