'use strict';
// ── Frame, tempo, grid ────────────────────────────────────────────────────
const W = 1920, H = 1080, CX = W / 2, CY = H / 2;
const FPS = 60, DUR = 20, NF = FPS * DUR;
const BEAT = 0.5, N8 = 0.25, N16 = 0.125;          // 120 BPM
const M = 120;                                      // safe margin
const TAU = Math.PI * 2;

// thetradingdesk.org palette (sampled from the live site)
const PAL = { cyan: '#22D0E8', green: '#30D098', gold: '#F0B840', pink: '#F8608C' };
const COL = {
  ink: '#070D12', glow: '#0F2F36', acc: PAL.cyan, hot: '#D8F8F8', paper: '#E8ECF0',
  print: '#070D12', stop: PAL.pink, up: PAL.green, flap: '#0D1117', sfx: '',
};
// run fn with a section accent (the site colour-codes its instruments)
function withAcc(name, fn) {
  const a = COL.acc, s = COL.sfx; COL.acc = PAL[name]; COL.sfx = name === 'cyan' ? '' : '_' + name;
  try { fn(); } finally { COL.acc = a; COL.sfx = s; }
}

// ── Math ──────────────────────────────────────────────────────────────────
const clamp = (x, a = 0, b = 1) => (x < a ? a : x > b ? b : x);
const lerp = (a, b, t) => a + (b - a) * t;
const inv = (a, b, x) => clamp((x - a) / (b - a));
const fract = x => x - Math.floor(x);

function hex(h) { h = h.replace('#', ''); return [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16)); }
function rgba(h, a = 1) { const c = hex(h); return `rgba(${c[0]},${c[1]},${c[2]},${a})`; }
function mixc(h1, h2, t, a = 1) {
  const x = hex(h1), y = hex(h2);
  return `rgba(${x.map((v, i) => Math.round(lerp(v, y[i], t))).join(',')},${a})`;
}

// CSS-style cubic-bezier easing
function bezier(x1, y1, x2, y2) {
  const cx = 3 * x1, bx = 3 * (x2 - x1) - cx, ax = 1 - cx - bx;
  const cy = 3 * y1, by = 3 * (y2 - y1) - cy, ay = 1 - cy - by;
  const sx = t => ((ax * t + bx) * t + cx) * t, sy = t => ((ay * t + by) * t + cy) * t;
  const dx = t => (3 * ax * t + 2 * bx) * t + cx;
  return x => {
    if (x <= 0) return 0; if (x >= 1) return 1;
    let t = x;
    for (let i = 0; i < 8; i++) { const d = sx(t) - x; if (Math.abs(d) < 1e-6) return sy(t); const s = dx(t); if (Math.abs(s) < 1e-6) break; t -= d / s; }
    let lo = 0, hi = 1; t = x;
    for (let i = 0; i < 40; i++) { if (sx(t) < x) lo = t; else hi = t; t = (lo + hi) / 2; }
    return sy(t);
  };
}

const E = {
  lin: t => t,
  inQuad: t => t * t, outQuad: t => 1 - (1 - t) * (1 - t),
  ioQuad: t => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2),
  inCubic: t => t * t * t, outCubic: t => 1 - Math.pow(1 - t, 3),
  ioCubic: t => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2),
  inQuart: t => t * t * t * t, outQuart: t => 1 - Math.pow(1 - t, 4),
  ioQuart: t => (t < 0.5 ? 8 * t ** 4 : 1 - Math.pow(-2 * t + 2, 4) / 2),
  inQuint: t => t ** 5, outQuint: t => 1 - Math.pow(1 - t, 5),
  ioQuint: t => (t < 0.5 ? 16 * t ** 5 : 1 - Math.pow(-2 * t + 2, 5) / 2),
  inExpo: t => (t <= 0 ? 0 : Math.pow(2, 10 * t - 10)),
  outExpo: t => (t >= 1 ? 1 : 1 - Math.pow(2, -10 * t)),
  ioExpo: t => (t <= 0 ? 0 : t >= 1 ? 1 : t < 0.5 ? Math.pow(2, 20 * t - 10) / 2 : (2 - Math.pow(2, -20 * t + 10)) / 2),
  outBack: t => { const c1 = 1.70158, c3 = c1 + 1; return 1 + c3 * Math.pow(t - 1, 3) + c1 * Math.pow(t - 1, 2); },
  inBack: t => { const c1 = 1.70158, c3 = c1 + 1; return c3 * t * t * t - c1 * t * t; },
  ioSine: t => -(Math.cos(Math.PI * t) - 1) / 2,
};
E.snap = bezier(0.16, 1, 0.3, 1);     // decisive arrival, long tail
E.whip = bezier(0.7, 0, 0.84, 0);     // accelerate away
E.swift = bezier(0.65, 0, 0.35, 1);   // camera in-out

// progress of t through [a,b] with easing
const P = (t, a, b, e = E.outExpo) => e(inv(a, b, t));

// damped spring 0→1 (f Hz, z damping ratio) evaluated analytically
function spring(t, f = 2.4, z = 0.45) {
  if (t <= 0) return 0;
  const w = TAU * f;
  if (z < 1) {
    const wd = w * Math.sqrt(1 - z * z);
    return 1 - Math.exp(-z * w * t) * (Math.cos(wd * t) + (z * w / wd) * Math.sin(wd * t));
  }
  return 1 - Math.exp(-w * t) * (1 + w * t);
}

// deterministic RNG + value noise
function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0; let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
function hash1(n) { n = (n << 13) ^ n; return 1 - ((n * (n * n * 15731 + 789221) + 1376312589) & 0x7fffffff) / 1073741824; }
function vnoise(x, seed = 0) {
  const i = Math.floor(x), f = x - i, u = f * f * (3 - 2 * f);
  return lerp(hash1(i + seed * 1013), hash1(i + 1 + seed * 1013), u);
}

// camera shake: sum of decaying noise impulses
const IMPULSES = [];
function impulse(t, amp, decay = 14) { IMPULSES.push({ t, amp, decay }); }
function shake(t) {
  let x = 0, y = 0, r = 0;
  for (const k of IMPULSES) {
    const d = t - k.t; if (d < 0 || d > 0.6) continue;
    const a = k.amp * Math.exp(-d * k.decay);
    x += a * vnoise(d * 38, k.t * 7 + 1); y += a * vnoise(d * 38, k.t * 7 + 2);
    r += a * 0.0006 * vnoise(d * 30, k.t * 7 + 3);
  }
  return { x, y, r };
}

// ── Canvas helpers ────────────────────────────────────────────────────────
function mkCanvas(w, h) { const c = document.createElement('canvas'); c.width = w; c.height = h; return c; }
const FF = { bs: 'BS', mc: 'MMc', mn: 'MMn', mw: 'MMw', dot: 'DOTO' };
function font(ctx, fam, wt, size, track = 0) {
  ctx.font = `${wt} ${size}px ${FF[fam]}`;
  ctx.letterSpacing = `${(track * size).toFixed(2)}px`;
  ctx.textBaseline = 'alphabetic';
  ctx.textAlign = 'left';
}
// width without the trailing letter-space
function tw(ctx, s) {
  const ls = parseFloat(ctx.letterSpacing) || 0;
  return ctx.measureText(s).width - (s.length ? ls : 0);
}
// x offset of every char (kerning-aware): x_i = w(s[0..i]) - w(s[i])
function charXs(ctx, s) {
  const xs = [];
  for (let i = 0; i < s.length; i++) xs.push(ctx.measureText(s.slice(0, i + 1)).width - ctx.measureText(s[i]).width);
  return xs;
}
// draw text; align: 0 left, 0.5 center, 1 right
function text(ctx, s, x, y, o = {}) {
  font(ctx, o.fam || 'mn', o.wt || 400, o.size || 30, o.track || 0);
  const w = tw(ctx, s);
  ctx.fillStyle = o.col || COL.acc;
  const a0 = ctx.globalAlpha; if (o.alpha != null) ctx.globalAlpha = a0 * o.alpha;
  ctx.fillText(s, x - w * (o.align || 0), y);
  ctx.globalAlpha = a0;
  return w;
}
// typewriter: returns visible substring given chars/sec
function typed(s, t, t0, cps = 60) { return s.slice(0, clamp(Math.floor((t - t0) * cps), 0, s.length)); }
function cursorOn(t) { return fract(t / BEAT) < 0.5; }

// per-letter mask rise: letters come up from under the baseline
function riseText(ctx, s, x, y, t, t0, o = {}) {
  const size = o.size || 200, fam = o.fam || 'bs', wt = o.wt || 900, track = o.track || 0;
  const stag = o.stagger ?? 0.018, dur = o.dur ?? 0.32, ease = o.ease || E.snap;
  font(ctx, fam, wt, size, track);
  const w = tw(ctx, s), xs = charXs(ctx, s), x0 = x - w * (o.align || 0);
  const top = y - size * 0.95, bot = y + size * (o.clipBelow ?? 0.12);
  ctx.save();
  ctx.beginPath(); ctx.rect(x0 - size, top, w + size * 2, bot - top); ctx.clip();
  ctx.fillStyle = o.col || COL.acc;
  for (let i = 0; i < s.length; i++) {
    const p = ease(inv(t0 + i * stag, t0 + i * stag + dur, t));
    if (p <= 0) continue;
    const dy = (1 - p) * size * 1.05;
    ctx.fillText(s[i], x0 + xs[i], y + dy);
  }
  ctx.restore();
  return { x0, w, xs };
}

// polyline utilities (partial draw by arc length)
function polyLen(pts) { const L = [0]; for (let i = 1; i < pts.length; i++) L.push(L[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1])); return L; }
function polyAt(pts, L, d) {
  if (d <= 0) return pts[0].slice(); const tot = L[L.length - 1]; if (d >= tot) return pts[pts.length - 1].slice();
  let i = 1; while (L[i] < d) i++;
  const u = (d - L[i - 1]) / (L[i] - L[i - 1]);
  return [lerp(pts[i - 1][0], pts[i][0], u), lerp(pts[i - 1][1], pts[i][1], u)];
}
function polyPath(ctx, pts, L, d0, d1) {
  ctx.beginPath(); const a = polyAt(pts, L, d0); ctx.moveTo(a[0], a[1]);
  for (let i = 1; i < pts.length; i++) if (L[i] > d0 && L[i] < d1) ctx.lineTo(pts[i][0], pts[i][1]);
  const b = polyAt(pts, L, d1); ctx.lineTo(b[0], b[1]);
}

// soft glowing dot sprites
const SPR = {};
function makeDot(name, core, edge, glowA, rel = 1) {
  const S = 128, c = mkCanvas(S, S), g = c.getContext('2d'), r = S / 2;
  let gr = g.createRadialGradient(r, r, 0, r, r, r);
  gr.addColorStop(0, rgba(edge, glowA)); gr.addColorStop(1, rgba(edge, 0));
  g.fillStyle = gr; g.fillRect(0, 0, S, S);
  gr = g.createRadialGradient(r, r, 0, r, r, r * 0.42 * rel);
  gr.addColorStop(0, core); gr.addColorStop(0.55, core); gr.addColorStop(0.92, edge); gr.addColorStop(1, rgba(edge, 0));
  g.fillStyle = gr; g.beginPath(); g.arc(r, r, r * 0.42 * rel, 0, TAU); g.fill();
  SPR[name] = c;
}
// sprite drawn so that its solid core has radius `rad`
function dot(ctx, name, x, y, rad, a = 1) {
  const s = rad / 0.42 * 2;
  const a0 = ctx.globalAlpha; ctx.globalAlpha = a0 * a;
  ctx.drawImage(SPR[name + COL.sfx] || SPR[name], x - s / 2, y - s / 2, s, s);
  ctx.globalAlpha = a0;
}

// chart crosshair with axis read-outs (the film's cursor)
function crosshair(ctx, x, y, o = {}) {
  const a = o.alpha ?? 1; if (a <= 0.001) return;
  const col = o.col || COL.acc, ink = o.ink || COL.ink, g = o.gap ?? 16;
  const x0 = o.x0 ?? 0, x1 = o.x1 ?? W, y0 = o.y0 ?? 0, y1 = o.y1 ?? H;
  ctx.save(); ctx.globalAlpha *= a;
  ctx.strokeStyle = rgba(col, 0.6); ctx.lineWidth = 1.5; ctx.setLineDash([5, 7]);
  ctx.beginPath();
  ctx.moveTo(x0, y); ctx.lineTo(x - g, y); ctx.moveTo(x + g, y); ctx.lineTo(x1, y);
  ctx.moveTo(x, y0); ctx.lineTo(x, y - g); ctx.moveTo(x, y + g); ctx.lineTo(x, y1);
  ctx.stroke(); ctx.setLineDash([]);
  ctx.strokeStyle = col; ctx.lineWidth = 2;
  ctx.strokeRect(x - 6, y - 6, 12, 12);
  if (o.yl) {
    font(ctx, 'mc', 600, 20, 0.04); const w = tw(ctx, o.yl) + 20;
    ctx.fillStyle = col; ctx.fillRect(x1 - w, y - 15, w, 30);
    ctx.fillStyle = ink; ctx.fillText(o.yl, x1 - w + 10, y + 7);
  }
  if (o.xl) {
    font(ctx, 'mc', 600, 20, 0.04); const w = tw(ctx, o.xl) + 20;
    ctx.fillStyle = col; ctx.fillRect(x - w / 2, y1 - 32, w, 30);
    ctx.fillStyle = ink; ctx.fillText(o.xl, x - w / 2 + 10, y1 - 10);
  }
  ctx.restore();
}

// rounded rect path
function rrect(ctx, x, y, w, h, r) {
  ctx.beginPath(); ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
}

// 5×7 LED font (HD44780 style)
const LED = {
  '0': ['01110', '10001', '10011', '10101', '11001', '10001', '01110'],
  '1': ['00100', '01100', '00100', '00100', '00100', '00100', '01110'],
  '2': ['01110', '10001', '00001', '00010', '00100', '01000', '11111'],
  '3': ['11111', '00010', '00100', '00010', '00001', '10001', '01110'],
  '4': ['00010', '00110', '01010', '10010', '11111', '00010', '00010'],
  '5': ['11111', '10000', '11110', '00001', '00001', '10001', '01110'],
  '6': ['00110', '01000', '10000', '11110', '10001', '10001', '01110'],
  '7': ['11111', '00001', '00010', '00100', '01000', '01000', '01000'],
  '8': ['01110', '10001', '10001', '01110', '10001', '10001', '01110'],
  '9': ['01110', '10001', '10001', '01111', '00001', '00010', '01100'],
  ':': ['0', '0', '1', '0', '1', '0', '0'],
};
// lit cells of a string on a board with a 1-cell border
function ledCells(str) {
  const on = new Set(); let c = 1;
  for (const ch of str) {
    const g = LED[ch]; const gw = g[0].length;
    for (let r = 0; r < 7; r++) for (let k = 0; k < gw; k++) if (g[r][k] === '1') on.add(`${c + k},${r + 1}`);
    c += gw + 1;
  }
  return { on, cols: c + 0, rows: 9 };
}
