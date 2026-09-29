// Shared building blocks.
import { useEffect, useRef, useState, useCallback } from "preact/hooks";
import { Icon, statusIcon } from "./icons.jsx";
import { call } from "./api.js";
import { toast, healthWord, useStore, dismiss, when } from "./state.js";

export function Btn({ kind = "", size = "", icon, children, busy, ...rest }) {
  return (
    <button type="button" class={`btn ${kind} ${size}`} disabled={busy || rest.disabled} {...rest}>
      {busy ? <span class="led busy" /> : icon ? <Icon name={icon} /> : null}
      {children}
    </button>
  );
}

export function Toggle({ checked, onChange, label, id }) {
  return (
    <label class="toggle" for={id}>
      <input id={id} type="checkbox" checked={!!checked} onChange={(e) => onChange(e.currentTarget.checked)} />
      <span class="track" />
      {label ? <span>{label}</span> : null}
    </label>
  );
}

let fieldSeq = 0;
export function Field({ label, help, error, children, id }) {
  const fid = useRef(id || `f${++fieldSeq}`).current;
  const child = typeof children === "function" ? children(fid) : children;
  return (
    <div class="field">
      {label ? <label for={fid}>{label}</label> : null}
      {child}
      {error ? <div class="err" role="alert"><Icon name="warn" size={15} />{error}</div> : help ? <div class="help">{help}</div> : null}
    </div>
  );
}

export function Seg({ value, options, onChange, label }) {
  return (
    <div class="seg" role="group" aria-label={label}>
      {options.map(([v, l]) => (
        <button type="button" aria-pressed={value === v ? "true" : "false"} onClick={() => onChange(v)}>{l}</button>
      ))}
    </div>
  );
}

export function Led({ h, lg, busy, title }) {
  const cls = busy ? "busy" : h === "ok" ? "ok" : h === "warn" ? "warn" : h === "fail" ? "fail" : "";
  return <span class={`led ${cls} ${lg ? "lg" : ""}`} title={title || healthWord[h] || ""} role="img" aria-label={title || healthWord[h] || "Not tested"} />;
}

export function Status({ h, children }) {
  const k = h || "unknown";
  return (
    <span class={`status ${k}`}>
      <Icon name={statusIcon[k] || "ring"} />
      <span>{children ?? healthWord[k]}</span>
    </span>
  );
}

export function Badge({ children, kind = "", led }) {
  return <span class={`badge ${kind}`}>{led ? <Led h={led} /> : null}{children}</span>;
}

export function Unit({ children, onClick, class: cls = "", label, ...rest }) {
  const Tag = onClick ? "button" : "div";
  return (
    <Tag type={onClick ? "button" : undefined} class={`unit ${onClick ? "clickable" : ""} ${cls}`} onClick={onClick} aria-label={label} {...rest}>
      <div class="ear"><span class="screw" /><span class="screw" /></div>
      {children}
      <div class="ear"><span class="screw" /><span class="screw" /></div>
    </Tag>
  );
}

export function Tabs({ tabs, value, onChange }) {
  return (
    <div class="tabs" role="tablist">
      {tabs.map(([v, l, extra]) => (
        <button type="button" role="tab" class="tab" aria-selected={v === value ? "true" : "false"} onClick={() => onChange(v)}>
          {l}{extra || null}
        </button>
      ))}
    </div>
  );
}

export function Modal({ title, eyebrow, onClose, children, foot, wide }) {
  const ref = useRef(null);
  useEffect(() => {
    const prev = document.activeElement;
    const el = ref.current?.querySelector("input, textarea, select, button.primary, button");
    el?.focus();
    const onKey = (e) => e.key === "Escape" && onClose?.();
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); prev?.focus?.(); };
  }, []);
  return (
    <div class="scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose?.()}>
      <div class={`modal ${wide ? "wide" : ""}`} role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <div class="modal-head">
          <div>{eyebrow ? <div class="eyebrow">{eyebrow}</div> : null}<div class="h2">{title}</div></div>
          {onClose ? <Btn kind="ghost" size="sm" icon="x" aria-label="Close" onClick={onClose} /> : null}
        </div>
        <div class="modal-body">{children}</div>
        {foot ? <div class="modal-foot">{foot}</div> : null}
      </div>
    </div>
  );
}

/** Typed confirmation for destructive production actions. */
export function ConfirmName({ name, what, onConfirm, onClose, busy }) {
  const [v, setV] = useState("");
  return (
    <Modal title="Type the project name to continue" eyebrow="Production safety" onClose={onClose}
      foot={<><Btn onClick={onClose}>Cancel</Btn><Btn kind="danger" icon="warn" disabled={v.trim() !== name} busy={busy} onClick={() => onConfirm(v.trim())}>{what}</Btn></>}>
      <p class="lede">This changes or removes live production resources. Type <b class="mono">{name}</b> to confirm.</p>
      <Field label="Project name">{(id) => <input id={id} class="input" value={v} onInput={(e) => setV(e.currentTarget.value)} autocomplete="off" spellcheck={false} />}</Field>
    </Modal>
  );
}

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div class="toasts" aria-live="polite">
      {toasts.map((t) => (
        <div class={`toast ${t.kind}`} key={t.id}>
          <Icon name={t.kind === "ok" ? "ok" : t.kind === "fail" ? "fail" : t.kind === "warn" ? "warn" : "info"} />
          <div><b>{t.title}</b>{t.body ? <span class="small ink2">{t.body}</span> : null}</div>
          <Btn kind="ghost" size="sm" icon="x" aria-label="Dismiss" onClick={() => dismiss(t.id)} />
        </div>
      ))}
    </div>
  );
}

/** Loads data from an RPC and re-loads on demand or when deps change. */
export function useCall(method, params, deps = []) {
  const [st, setSt] = useState({ data: null, error: null, loading: true });
  const seq = useRef(0);
  const load = useCallback(async (quiet) => {
    const n = ++seq.current;
    if (!quiet) setSt((s) => ({ ...s, loading: true }));
    try {
      const data = await call(method, params);
      if (n === seq.current) setSt({ data, error: null, loading: false });
    } catch (e) {
      if (n === seq.current) setSt((s) => ({ data: s.data, error: e, loading: false }));
    }
  }, [method, JSON.stringify(params)]);
  useEffect(() => { load(); }, [load, ...deps]);
  return { ...st, reload: load };
}

/** Runs an action with busy state and toasts errors. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const run = useCallback(async (fn, okMsg) => {
    setBusy(true);
    try {
      const r = await fn();
      if (okMsg) toast("ok", okMsg);
      return r;
    } catch (e) {
      if (!e.confirm) toast("fail", "Could not complete that", e.message, 9000);
      throw e;
    } finally {
      setBusy(false);
    }
  }, []);
  return [busy, run];
}

export function ErrorNote({ error }) {
  if (!error) return null;
  return <div class="notice fail"><Icon name="fail" /><div>{error.message}</div></div>;
}

export function Empty({ icon = "rack", title, children, action }) {
  return (
    <div class="empty">
      <Icon name={icon} size={30} />
      <div class="h2">{title}</div>
      {children ? <p class="lede" style="margin:0 auto">{children}</p> : null}
      {action || null}
    </div>
  );
}

export function openLink(url) {
  call("OpenExternal", { url }).catch((e) => toast("fail", "Could not open the link", e.message));
}

export function ExtLink({ href, children }) {
  return <button type="button" class="linkish" onClick={() => openLink(href)}>{children}<Icon name="ext" size={13} /></button>;
}

// ---- problem cards (the punch list) ----

export function ProblemCard({ problem, health = "fail", onFix, busy, showTech }) {
  if (!problem) return null;
  const [tech, setTech] = useState(false);
  return (
    <div class={`problem ${health === "warn" ? "warn" : ""}`}>
      <div class="spread">
        <Status h={health}><span class="ptitle">{problem.title}</span></Status>
        {problem.httpStatus ? <Badge>HTTP {problem.httpStatus}</Badge> : null}
      </div>
      <div>{problem.summary}</div>
      {problem.affected?.length || problem.unaffected?.length ? (
        <div class="impact">
          {problem.affected?.length ? <div><div class="silk">Affected</div><ul>{problem.affected.map((a) => <li>{a}</li>)}</ul></div> : <div />}
          {problem.unaffected?.length ? <div><div class="silk">Still working</div><ul>{problem.unaffected.map((a) => <li>{a}</li>)}</ul></div> : null}
        </div>
      ) : null}
      {problem.fixes?.length ? (
        <div class="stack" style="gap:8px">
          {problem.fixes.map((f) => (
            <div class="fix">
              <div class="spread">
                <b>{f.label}</b>
                {f.action === "link" && f.link ? <Btn size="sm" icon="ext" onClick={() => openLink(f.link)}>Open</Btn>
                  : f.action ? <Btn size="sm" kind={f.action === "repair" ? "primary" : ""} icon={f.action === "repair" ? "bolt" : f.action === "reconnect" ? "plug" : f.action === "upload" ? "upload" : "right"} busy={busy} onClick={() => onFix?.(f)}>
                    {f.action === "repair" ? "Plan repair" : f.action === "reconnect" ? "Fix connection" : f.action === "replan" ? "Re-plan" : f.action === "guide" ? "Open guide" : f.action === "upload" ? "Upload file" : "Go"}</Btn> : null}
              </div>
              {f.explain ? <div class="small ink2">{f.explain}</div> : null}
              {f.changes?.length ? <ul class="small" style="margin:0;padding-left:18px">{f.changes.map((c) => <li>{c}</li>)}</ul> : null}
            </div>
          ))}
        </div>
      ) : null}
      {problem.technical && showTech !== false ? (
        <div>
          <button type="button" class="linkish small" onClick={() => setTech(!tech)}>{tech ? "Hide" : "Show"} technical detail</button>
          {tech ? <pre class="code" style="margin-top:6px;max-height:200px">{problem.technical}</pre> : null}
        </div>
      ) : null}
    </div>
  );
}

// ---- figures ----

export function Stat({ label, value, sub, children }) {
  return (
    <div class="stat">
      <div class="label">{label}</div>
      <div class="value">{value}</div>
      {sub ? <div class="sub">{sub}</div> : null}
      {children}
    </div>
  );
}

/** Single-series sparkline: de-emphasis line, latest point in the accent. */
export function Spark({ values, label }) {
  const pts = values.filter((v) => v != null);
  if (pts.length < 2) return null;
  const w = 120, h = 30, max = Math.max(...pts), min = Math.min(...pts);
  const span = max - min || 1;
  const xy = pts.map((v, i) => [(i / (pts.length - 1)) * w, h - 3 - ((v - min) / span) * (h - 6)]);
  const d = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
  const [lx, ly] = xy[xy.length - 1];
  return (
    <svg class="spark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={label}>
      <path d={d} vector-effect="non-scaling-stroke" />
      <circle cx={lx} cy={ly} r="4" />
    </svg>
  );
}

/** Hover tooltip shared by chart marks (text only, never HTML). */
export function useTip() {
  const [tip, setTip] = useState(null);
  const show = (e, title, lines) => {
    const r = e.currentTarget.getBoundingClientRect();
    setTip({ x: Math.min(r.left + r.width / 2, window.innerWidth - 330), y: r.top - 8, title, lines });
  };
  const hide = () => setTip(null);
  const el = tip ? (
    <div class="tip" style={`left:${tip.x}px;top:${tip.y}px;transform:translate(-50%,-100%)`} role="tooltip">
      <b>{tip.title}</b>
      {tip.lines.map((l) => <div>{l}</div>)}
    </div>
  ) : null;
  return [el, show, hide];
}

/** Health history as a strip of status cells, oldest → newest. */
export function Strip({ history, onPick }) {
  const [tipEl, show, hide] = useTip();
  const items = [...(history || [])].slice(0, 120).reverse();
  if (!items.length) return <div class="muted small">No checks recorded yet.</div>;
  return (
    <div>
      <div class="strip" role="list" aria-label="Health check history, oldest to newest">
        {items.map((h) => (
          <button type="button" role="listitem" class={`cell ${h.overall}`} aria-label={`${when(h.at)}: ${h.headline}`}
            onMouseEnter={(e) => show(e, h.headline, [when(h.at) + " · " + h.kind + " check", h.note || ""].filter(Boolean))}
            onFocus={(e) => show(e, h.headline, [when(h.at) + " · " + h.kind + " check", h.note || ""].filter(Boolean))}
            onMouseLeave={hide} onBlur={hide} onClick={() => onPick?.(h)} />
        ))}
      </div>
      <div class="strip-axis"><span>{when(items[0].at)}</span><span>{when(items[items.length - 1].at)}</span></div>
      <div class="legend" style="margin-top:8px">
        <span><i style="background:var(--ok)" />Pass</span>
        <span><i style="background:var(--warn)" />Needs attention</span>
        <span><i style="background:var(--fail)" />Failing</span>
        <span><i style="background:var(--led-off)" />Not built / unknown</span>
      </div>
      {tipEl}
    </div>
  );
}

/** The six certification levels as one segmented bar. */
export function Levels({ levels, names, value, onPick }) {
  return (
    <div class="levels" role="group" aria-label="Certification levels">
      {[1, 2, 3, 4, 5, 6].map((n) => {
        const h = levels?.[n] || "unknown";
        return (
          <button type="button" class={`level ${h}`} aria-pressed={value === n ? "true" : "false"} onClick={() => onPick?.(value === n ? 0 : n)}>
            <span class="n">L{n}</span>
            <span class="l">{names?.[n] || ""}</span>
            <span class="s"><Icon name={statusIcon[h] || "ring"} />{healthWord[h]}</span>
          </button>
        );
      })}
    </div>
  );
}

export function Guide({ steps }) {
  return (
    <ol class="guide-steps">
      {steps.map((s) => (
        <li>
          <div>
            <b>{s.title}</b>
            <div class="ink2 small">{s.body}</div>
            {s.link ? <div style="margin-top:4px"><ExtLink href={s.link}>{s.label || "Open"}</ExtLink></div> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

export function copyText(t) {
  navigator.clipboard?.writeText(t).then(() => toast("ok", "Copied"), () => toast("warn", "Could not copy"));
}
