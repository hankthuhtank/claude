import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { providerName, healthWord } from "../state.js";
import { Icon, statusIcon } from "../icons.jsx";
import { Led, Status, Badge } from "../ui.jsx";

// The architecture as a rack: one faceplate per component, patch cords for
// every link. "Trace" works like a cable tracer's tone probe: pick a unit or a
// cord and the signal path lights up downstream, stopping at the first fault.

function resultFor(report, target) {
  if (!report) return null;
  const rs = report.results.filter((r) => r.target === target);
  const rank = { fail: 3, warn: 2, ok: 1, unknown: 0, skipped: 0 };
  return rs.sort((a, b) => (rank[b.health] || 0) - (rank[a.health] || 0))[0] || null;
}

/** Downstream path from a unit (or a single link) through the link graph. */
function tracePath(bp, start) {
  const links = bp.links || [];
  if (start.type === "link") {
    const l = links.find((x) => x.key === start.key);
    return l ? { units: new Set([l.from, l.to]), links: [l.key], order: [l.from, l.to], hops: [l] } : null;
  }
  const units = new Set([start.key]);
  const seenLinks = [];
  const hops = [];
  const queue = [start.key];
  while (queue.length) {
    const k = queue.shift();
    for (const l of links) {
      if (l.from !== k || seenLinks.includes(l.key)) continue;
      seenLinks.push(l.key);
      hops.push(l);
      if (!units.has(l.to)) { units.add(l.to); queue.push(l.to); }
    }
  }
  return { units, links: seenLinks, hops };
}

export function RackView({ dash, onOpenUnit }) {
  const bp = dash.blueprint;
  const report = dash.report;
  const comps = useMemo(() => [...(bp.components || [])].sort((a, b) => a.order - b.order), [bp]);
  const [trace, setTrace] = useState(null);
  const path = useMemo(() => (trace ? tracePath(bp, trace) : null), [trace, bp]);
  const wrap = useRef(null);
  const unitRefs = useRef({});
  const [geo, setGeo] = useState(null);

  useLayoutEffect(() => {
    const measure = () => {
      const w = wrap.current;
      if (!w) return;
      const box = w.getBoundingClientRect();
      const pos = {};
      for (const c of comps) {
        const el = unitRefs.current[c.key];
        if (!el) continue;
        const r = el.getBoundingClientRect();
        pos[c.key] = { top: r.top - box.top, bottom: r.bottom - box.top, right: r.right - box.left, mid: r.top - box.top + r.height / 2 };
      }
      setGeo({ pos, width: box.width, height: box.height });
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(wrap.current);
    return () => ro.disconnect();
  }, [comps.length]);

  // Cords: stagger each unit's ports so cords never share an end, and give
  // longer cords a wider loop so they nest instead of crossing.
  const cords = useMemo(() => {
    if (!geo) return [];
    const links = [...(bp.links || [])].filter((l) => geo.pos[l.from] && geo.pos[l.to]);
    const portCount = {};
    const portIndex = (k) => (portCount[k] = (portCount[k] ?? -1) + 1);
    const spans = links.map((l) => ({ l, span: Math.abs(geo.pos[l.from].mid - geo.pos[l.to].mid) })).sort((a, b) => a.span - b.span);
    return spans.map(({ l }, i) => {
      const a = geo.pos[l.from], b = geo.pos[l.to];
      const pa = portIndex(l.from), pb = portIndex(l.to);
      const ya = a.mid - 9 + (pa % 3) * 9, yb = b.mid - 9 + (pb % 3) * 9;
      const x0 = a.right + 2;
      const loop = 28 + i * 13;
      const d = `M${x0},${ya} C${x0 + loop},${ya} ${x0 + loop},${yb} ${x0},${yb}`;
      const h = report?.links?.[l.key] || "unknown";
      return { l, d, h, tagX: x0 + loop * 0.75 + 4, tagY: (ya + yb) / 2 };
    }).map((c, _, all) => c);
  }, [geo, bp.links, report]);

  // Fault tags: only failing or warning cords get one, spread apart vertically
  // and kept inside the cable gutter.
  const tags = useMemo(() => {
    const t = cords.filter((c) => (c.h === "fail" || c.h === "warn") && (c.l.label || c.l.kind))
      .map((c) => ({ key: c.l.key, h: c.h, text: (c.h === "fail" ? "✕ " : "! ") + (c.l.label || c.l.kind).slice(0, 22), y: c.tagY, x: c.tagX }))
      .sort((a, b) => a.y - b.y);
    for (let i = 1; i < t.length; i++) if (t[i].y - t[i - 1].y < 22) t[i].y = t[i - 1].y + 22;
    for (const x of t) x.x = Math.min(x.x, (geo?.width || 0) - x.text.length * 6.4 - 16);
    return t;
  }, [cords, geo]);

  // The tone stops at the first fault along the traced path.
  const faultHop = path?.hops?.find((h) => report?.links?.[h.key] === "fail" || report?.components?.[h.to] === "fail");

  return (
    <div class="rackview">
      <div class="rack-units" ref={wrap}>
        {comps.map((c, i) => {
          const h = c.external ? undefined : report?.components?.[c.key];
          const res = resultFor(report, c.key);
          const cls = path ? (path.units.has(c.key) ? "traced" : "dim") : "";
          const nRes = (c.resources || []).length;
          return (
            <div ref={(el) => (unitRefs.current[c.key] = el)} class={`unit rack-unit ${c.external ? "external" : ""} ${cls}`}>
              <div class="ear"><span class="screw" /><span class="silk" style="writing-mode:vertical-rl;font-size:.6rem;letter-spacing:.1em">{String(i + 1).padStart(2, "0")}</span><span class="screw" /></div>
              <button type="button" class="face" style="background:none;border:0;text-align:left;cursor:pointer;color:inherit;font:inherit"
                onClick={() => setTrace(trace?.type === "unit" && trace.key === c.key ? null : { type: "unit", key: c.key })}
                onDblClick={() => !c.external && onOpenUnit?.(c)} aria-pressed={trace?.key === c.key ? "true" : "false"}
                aria-label={`${c.label}: ${c.role}. ${h ? healthWord[h] : ""}. Select to trace its connections.`}>
                <div class="spread" style="gap:8px">
                  <span class="silk">{c.external ? "Outside world" : `${providerName(c.provider)} · ${c.capability || "service"}`}</span>
                  <span class="leds">{c.external ? null : <><Led h={h} /><span class="small ink2">{h ? healthWord[h] : "Not tested"}</span></>}</span>
                </div>
                <div class="title">{c.label}</div>
                <div class="role">{c.role}</div>
                <div class="meta">
                  {nRes ? <span>{nRes} resource{nRes === 1 ? "" : "s"}</span> : null}
                  {res && res.health !== "ok" && res.summary ? <span>{res.summary}</span> : null}
                  {c.breaks?.length && !c.external ? <span class="muted">Carries: {c.breaks.join(", ")}</span> : null}
                </div>
              </button>
              <div class="ports" aria-hidden="true">
                {[0, 1, 2].map((p) => <span class={`port ${(bp.links || []).some((l) => l.from === c.key || l.to === c.key) && p === 0 ? "live" : ""}`} />)}
              </div>
              <div class="ear"><span class="screw" /><span class="screw" /></div>
            </div>
          );
        })}
        {geo ? (
          <svg class="cords" width={geo.width} height={geo.height} aria-label="Connections">
            {cords.map(({ l, d }) => {
              const dim = path && !path.links.includes(l.key);
              return <path class={`cord-shadow ${dim ? "dim" : ""}`} d={d} />;
            })}
            {cords.map(({ l, d, h, tagX, tagY }) => {
              const on = path?.links.includes(l.key);
              const dim = path && !on;
              const cls = `cord ${h === "fail" ? "fail" : h === "warn" ? "warn" : ""} ${on && h !== "fail" ? "traced" : ""} ${dim ? "dim" : ""}`;
              const label = (l.label || l.kind || "").slice(0, 26);
              return (
                <g>
                  <path class={cls} d={d} tabindex="0" role="button" aria-label={`${l.label || l.kind}: ${healthWord[h]}. Select to trace.`}
                    onClick={() => setTrace(trace?.type === "link" && trace.key === l.key ? null : { type: "link", key: l.key })}
                    onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && setTrace({ type: "link", key: l.key })}>
                    <title>{`${l.label || l.kind} — ${healthWord[h]}`}</title>
                  </path>
                  {on && h !== "fail" && (!faultHop || path.hops.indexOf(l) <= path.hops.indexOf(faultHop)) ? <path class="cord-pulse" d={d} /> : null}
                </g>
              );
            })}
            {tags.map((t) => (
              <g class={path && !path.links.includes(t.key) ? "dimtag" : ""}>
                <rect class={`cord-tag-bg ${t.h}`} x={t.x - 2} y={t.y - 9} width={t.text.length * 6.4 + 12} height="18" rx="3" />
                <text class="cord-tag" x={t.x + 4} y={t.y + 4}>{t.text}</text>
              </g>
            ))}
          </svg>
        ) : null}
      </div>
      <Tracer dash={dash} path={path} trace={trace} faultHop={faultHop} onClear={() => setTrace(null)} />
    </div>
  );
}

function Tracer({ dash, path, trace, faultHop, onClear }) {
  const bp = dash.blueprint;
  const report = dash.report;
  const comp = (k) => bp.components.find((c) => c.key === k);
  if (!trace) {
    return (
      <aside class="tracer" aria-live="polite">
        <div class="silk">Tracer</div>
        <div class="screen" style="margin-top:8px">
          <div class="ink2" style="font-family:var(--sans)">Select any unit or cord to trace the signal path through your backend.</div>
          <div class="small muted" style="font-family:var(--sans)">Tip: start at <b>{comp(bp.links?.[0]?.from)?.label || "the first unit"}</b> to follow a customer through everything.</div>
        </div>
      </aside>
    );
  }
  const hops = path?.hops || [];
  return (
    <aside class="tracer" aria-live="polite">
      <div class="spread"><span class="silk">Tracer</span><button type="button" class="linkish small" onClick={onClear}>Clear</button></div>
      <div class="screen" style="margin-top:8px">
        <div class="t" style="font-family:var(--display);font-weight:700;letter-spacing:.06em;text-transform:uppercase">
          {trace.type === "unit" ? comp(trace.key)?.label : hops[0]?.label || "Connection"}
        </div>
        {hops.length === 0 ? <div class="d">Nothing downstream — this unit is an end point.</div> : null}
        {hops.map((l, i) => {
          const h = report?.links?.[l.key] || "unknown";
          const r = resultFor(report, l.key);
          return (
            <div>
              {i === 0 ? <Hop h={report?.components?.[l.from] || "unknown"} title={comp(l.from)?.label} /> : null}
              <div class="hop-arrow" />
              <div class="hop">
                <span class={h}><Icon name={statusIcon[h]} size={16} /></span>
                <div>
                  <div class="t">{l.label || l.kind}</div>
                  <div class="d">{r ? r.summary : healthWord[h]}{r?.latencyMs ? ` · ${r.latencyMs} ms` : ""}</div>
                </div>
              </div>
              <div class="hop-arrow" />
              <Hop h={report?.components?.[l.to] || "unknown"} title={comp(l.to)?.label} />
            </div>
          );
        })}
        {faultHop ? (
          <div class="notice fail" style="font-family:var(--sans)"><Icon name="fail" /><div>Signal stops at <b>{faultHop.label || faultHop.kind}</b>. Open the Health tab for the fix.</div></div>
        ) : hops.length && report ? (
          <div class="notice ok" style="font-family:var(--sans)"><Icon name="ok" /><div>Signal gets through every hop.</div></div>
        ) : null}
      </div>
    </aside>
  );
}

function Hop({ h, title }) {
  return (
    <div class="hop">
      <span class={h}><Icon name={statusIcon[h] || "ring"} size={16} /></span>
      <div class="t">{title}</div>
    </div>
  );
}

export function Inventory({ rows }) {
  return (
    <table class="t">
      <thead><tr><th>Resource</th><th>Service</th><th>Status</th><th>Live name / id</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr>
            <td><b>{r.title}</b><div class="small muted mono">{r.kind}</div>{r.note ? <div class="small ink2">{r.note}</div> : null}</td>
            <td>{providerName(r.provider)}</td>
            <td><Status h={r.status === "planned" ? "unknown" : r.health}>{r.status === "planned" ? "Planned" : r.status === "failed" ? "Failed" : r.status === "creating" ? "Unfinished" : healthWord[r.health] || r.status}</Status>
              {r.createdBy === "adopted" ? <div class="small muted">adopted</div> : null}</td>
            <td class="mono small" style="overflow-wrap:anywhere">{r.name || ""}{r.id && r.id !== r.name ? <div class="muted">{r.id}</div> : null}{r.outputs?.url ? <div>{r.outputs.url}</div> : null}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
