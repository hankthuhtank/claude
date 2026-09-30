'use strict';
// ══════════════════════════════════════════════════════════════════════════
//  OPEN TO CLOSE: every scene is a pure function of time t (seconds)
// ══════════════════════════════════════════════════════════════════════════
const CUES = [];
const cue = (t, type, x = {}) => CUES.push(Object.assign({ t: +t.toFixed(4), type }, x));

const OPEN_S = 9 * 3600 + 30 * 60, CLOSE_S = 16 * 3600;
function marketSecs(t) {
  if (t < 1.5) return OPEN_S - 3 + Math.floor(Math.max(0, t) / BEAT);
  if (t < 2.0) return OPEN_S;
  if (t < 17.5) return OPEN_S + Math.floor(((t - 2.0) / 15.5) * (CLOSE_S - OPEN_S));
  return CLOSE_S;
}
const hms = s => [Math.floor(s / 3600), Math.floor(s / 60) % 60, s % 60].map(v => String(v).padStart(2, '0')).join(':');

// cached char layouts
const XS = new Map();
function xsFor(ctx, s) { const k = ctx.font + '|' + ctx.letterSpacing + '|' + s; let v = XS.get(k); if (!v) { v = charXs(ctx, s); XS.set(k, v); } return v; }

// module header: small index + big name, rises in, fades out
function header(ctx, t, idx, name, t0, t1, col = COL.acc) {
  const out = P(t, t1 - 0.14, t1, E.inCubic);
  if (t < t0 || out >= 1) return;
  ctx.save(); ctx.globalAlpha *= 1 - out; ctx.translate(0, -out * 30);
  const ia = P(t, t0, t0 + 0.2, E.outCubic);
  const iw = text(ctx, idx, M, 150, { fam: 'mc', wt: 600, size: 20, track: 0.3, col, alpha: 0.7 * ia });
  ctx.fillStyle = col; ctx.globalAlpha *= 0.7; ctx.fillRect(M + iw + 18, 143, 110 * ia, 2); ctx.globalAlpha /= 0.7;
  riseText(ctx, name, M - 4, 262, t, t0 + 0.02, { size: 104, wt: 900, col, stagger: 0.014, dur: 0.3 });
  ctx.restore();
}
// caption line (terminal voice), typed
function caption(ctx, t, s, t0, t1, o = {}) {
  if (t < t0 || t > t1) return;
  const a = 1 - P(t, t1 - 0.12, t1, E.inCubic);
  const vis = typed(s, t, t0, o.cps || 75);
  const x = o.x ?? M, y = o.y ?? 948, size = o.size || 30, col = o.col || COL.acc;
  ctx.save(); ctx.globalAlpha *= a;
  font(ctx, 'mn', o.wt || 400, size, 0.04);
  let w = tw(ctx, s);
  let xx = x - w * (o.align || 0);
  if (o.red) {           // part of the line printed in STOP red
    const cut = s.indexOf(o.red);
    const a1 = vis.slice(0, Math.min(vis.length, cut)), a2 = vis.length > cut ? vis.slice(cut) : '';
    ctx.fillStyle = col; ctx.fillText(a1, xx, y);
    if (a2) { ctx.fillStyle = COL.stop; ctx.fillText(a2, xx + ctx.measureText(s.slice(0, cut)).width, y); }
  } else { ctx.fillStyle = col; ctx.fillText(vis, xx, y); }
  const vw = ctx.measureText(vis).width;
  if (vis.length < s.length || cursorOn(t)) { ctx.fillStyle = col; ctx.fillRect(xx + vw + 4, y - size * 0.78, size * 0.55, size * 0.95); }
  ctx.restore();
}

// ── background: registration-cross grid ──────────────────────────────────
function bgGrid(ctx, t, a, ox = 0, oy = 0, col = COL.acc) {
  if (a <= 0.001) return;
  ctx.save(); ctx.strokeStyle = rgba(col, 0.16 * a); ctx.lineWidth = 1.5;
  const step = 120, dx = ((ox % step) + step) % step, dy = ((oy % step) + step) % step;
  ctx.beginPath();
  for (let x = dx - step; x <= W + step; x += step) for (let y = dy - step; y <= H + step; y += step) {
    ctx.moveTo(x - 6, y); ctx.lineTo(x + 6, y); ctx.moveTo(x, y - 6); ctx.lineTo(x, y + 6);
  }
  ctx.stroke(); ctx.restore();
}

// ══ S1 · PRE-MARKET (0.00–2.00) ═══════════════════════════════════════════
const S1 = { strs: ['09:29:57', '09:29:58', '09:29:59', '09:30:00'], tk: [0, 0.5, 1.0, 1.5], p: 41, r: 15.5, bells: [1.5, 1.625, 1.75, 1.875] };
function s1Cam(t) { return 1 + 0.10 * E.inQuad(inv(0, 1.875, t)) - 0.035 * E.outQuad(inv(1.875, 2.0, t)); }
function buildS1() {
  const L = ledCells(S1.strs[0]); S1.cols = L.cols; S1.rows = L.rows;
  S1.x0 = CX - ((S1.cols - 1) / 2) * S1.p; S1.y0 = CY - ((S1.rows - 1) / 2) * S1.p;
  S1.on = S1.strs.map(s => ledCells(s).on);
  const R = rng(11); S1.pw = [];
  for (let r = 0; r < S1.rows; r++) for (let c = 0; c < S1.cols; c++) S1.pw.push(r * 0.018 + R() * 0.06);
  cue(0, 'power'); cue(0.5, 'tick'); cue(1.0, 'tick');
  S1.bells.forEach((b, i) => { cue(b, 'bell', { i, of: 4 }); impulse(b, i === 0 ? 7 : 4, 16); });
}
const bellPulse = (t, bells) => bells.reduce((s, b) => s + (t >= b ? Math.exp(-(t - b) * 14) : 0), 0);
function drawS1(ctx, t) {
  if (t >= 2.0) return;
  const k = t < 0.5 ? 0 : t < 1.0 ? 1 : t < 1.5 ? 2 : 3, tk = S1.tk[k];
  const on = S1.on[k], prev = k > 0 ? S1.on[k - 1] : null;
  const cam = s1Cam(t), sh = shake(t);
  ctx.save();
  ctx.translate(CX + sh.x, CY + sh.y); ctx.rotate(sh.r); ctx.scale(cam, cam); ctx.translate(-CX, -CY);
  for (const b of S1.bells) {                       // bell pressure waves
    const u = inv(b, b + 0.8, t); if (u <= 0 || u >= 1) continue;
    ctx.strokeStyle = rgba(COL.acc, 0.28 * (1 - u)); ctx.lineWidth = 1.5 + 5 * (1 - u);
    ctx.beginPath(); ctx.arc(CX, CY, 260 + E.outCubic(u) * 1000, 0, TAU); ctx.stroke();
  }
  const bp = bellPulse(t, S1.bells);
  let i = 0;
  for (let r = 0; r < S1.rows; r++) for (let c = 0; c < S1.cols; c++, i++) {
    const age = t - S1.pw[i]; if (age < 0) continue;
    const x = S1.x0 + c * S1.p, y = S1.y0 + r * S1.p, key = c + ',' + r;
    const fl = age < 0.07 ? (hash1(i * 13 + Math.floor(t * 60)) > -0.2 ? 1 : 0.15) : 1;
    dot(ctx, 'off', x, y, S1.r, fl);
    if (on.has(key)) {
      const fresh = prev && !prev.has(key) ? 1 - inv(tk, tk + 0.16, t) : 0;
      if (k === 3) dot(ctx, 'hot', x, y, S1.r * (1 + 0.1 * Math.min(1, bp)), fl);
      else dot(ctx, 'lit', x, y, S1.r * (1 + 0.22 * fresh), fl);
      if (fresh > 0) dot(ctx, 'hot', x, y, S1.r * 1.1, fresh);
    } else if (prev && prev.has(key)) {
      const f = 1 - inv(tk, tk + 0.06, t); if (f > 0) dot(ctx, 'lit', x, y, S1.r, f);
    }
  }
  const la = P(t, 0.18, 0.5, E.outCubic) * (1 - P(t, 1.92, 2.0, E.lin));
  const bx0 = S1.x0 - S1.r, bx1 = S1.x0 + (S1.cols - 1) * S1.p + S1.r, top = S1.y0 - 64, bot = S1.y0 + (S1.rows - 1) * S1.p + 92;
  text(ctx, 'PRE-MARKET', bx0, top, { fam: 'mc', wt: 600, size: 22, track: 0.32, alpha: la });
  text(ctx, 'NEW YORK · ET', bx1, top, { fam: 'mc', wt: 600, size: 22, track: 0.32, alpha: la, align: 1 });
  const lab = ['OPENING BELL IN 3', 'OPENING BELL IN 2', 'OPENING BELL IN 1', 'THE MARKET IS OPEN'][k];
  text(ctx, lab, CX, bot, { fam: 'mc', wt: 600, size: 22, track: 0.32, alpha: la * (k === 3 ? 1 : 0.75), align: 0.5, col: k === 3 ? COL.hot : COL.acc });
  ctx.restore();
}

// ══ S2 · ONE TERMINAL (2.00–4.00) ═════════════════════════════════════════
const S2 = { s: 'ONE TERMINAL', sub: 'FOR THE WHOLE TRADER’S EDUCATION —', keys: ['PATTERNS', 'OPTIONS', 'RISK', 'THE PATH'], keyT: [3.25, 3.375, 3.5, 3.625], pressT: 3.75, pitch: 12, dr: 4.4 };
function buildS2(ctx) {
  font(ctx, 'bs', 900, 100); const w100 = tw(ctx, S2.s);
  S2.size = Math.round(100 * 1560 / w100);
  font(ctx, 'bs', 900, S2.size);
  S2.w = tw(ctx, S2.s); S2.x0 = CX - S2.w / 2; S2.yb = 560; S2.xs = charXs(ctx, S2.s);
  const iI = S2.s.indexOf('I'), m = ctx.measureText('I');
  S2.iI = iI;
  S2.Irect = { x: S2.x0 + S2.xs[iI] - m.actualBoundingBoxLeft, y: S2.yb - m.actualBoundingBoxAscent, w: m.actualBoundingBoxLeft + m.actualBoundingBoxRight, h: m.actualBoundingBoxAscent + m.actualBoundingBoxDescent };
  S2.capTop = S2.yb - m.actualBoundingBoxAscent;
  // sample the title into an LED grid
  const c = mkCanvas(W, H), g = c.getContext('2d');
  font(g, 'bs', 900, S2.size); g.fillStyle = '#fff'; g.fillText(S2.s, S2.x0, S2.yb);
  const d = g.getImageData(0, 0, W, H).data, tg = [];
  for (let y = Math.floor(S2.capTop) - 4; y < S2.yb + 8; y += S2.pitch) for (let x = Math.floor(S2.x0) - 4; x < S2.x0 + S2.w + 8; x += S2.pitch) {
    if (d[(Math.round(y) * W + Math.round(x)) * 4 + 3] > 140) tg.push([x, y]);
  }
  // sources: every LED on the board at the moment of the bell (screen space)
  const cam = s1Cam(2.0), src = [];
  for (let r = 0; r < S1.rows; r++) for (let cc = 0; cc < S1.cols; cc++) {
    const x = (S1.x0 + cc * S1.p - CX) * cam + CX, y = (S1.y0 + r * S1.p - CY) * cam + CY;
    src.push({ x, y, lit: S1.on[3].has(cc + ',' + r) });
  }
  src.sort((a, b) => a.x - b.x); tg.sort((a, b) => a[0] - b[0]);
  const R = rng(22); S2.parts = [];
  for (let j = 0; j < tg.length; j++) {
    const s = src[Math.min(src.length - 1, Math.floor((j / tg.length) * src.length + (R() - 0.5) * 30))] || src[0];
    const ang = Math.atan2(s.y - CY, s.x - CX) + (R() - 0.5) * 1.4;
    const v = (s.lit ? 1500 : 800) + R() * 1700;
    S2.parts.push({ sx: s.x + (R() - 0.5) * 8, sy: s.y + (R() - 0.5) * 8, tx: tg[j][0], ty: tg[j][1], vx: Math.cos(ang) * v, vy: Math.sin(ang) * v, lit: s.lit,
      ts: 0.06 + R() * 0.2 + (tg[j][0] - S2.x0) / S2.w * 0.12, td: 0.3 + R() * 0.2, sw: (R() - 0.5) * 300 });
  }
  // keys layout
  font(ctx, 'mc', 700, 24, 0.14);
  let x = S2.x0 + 6; S2.keyRects = [];
  for (const k of S2.keys) { const kw = tw(ctx, k) + 52; S2.keyRects.push({ x, y: S2.yb + 136, w: kw, h: 62 }); x += kw + 18; }
  cue(2.0, 'drop'); cue(2.0, 'burst'); impulse(2.0, 16, 9);
  cue(2.7, 'scan');
  for (let i = 0; i < S2.sub.length; i++) if (S2.sub[i] !== ' ') cue(2.8 + i / 90, 'key', { v: 0.35 });
  S2.keyT.forEach((kt, i) => cue(kt, 'pop', { i }));
  cue(S2.pressT, 'press'); impulse(S2.pressT, 3, 20);
}
function partPos(p, tau) {
  const k = 5.5, e = (1 - Math.exp(-k * tau)) / k;
  const ex = p.sx + p.vx * e, ey = p.sy + p.vy * e;
  const s = E.ioCubic(inv(p.ts, p.ts + p.td, tau));
  let x = lerp(ex, p.tx, s), y = lerp(ey, p.ty, s);
  const dx = p.tx - ex, dy = p.ty - ey, d = Math.hypot(dx, dy) || 1, sw = Math.sin(Math.PI * s) * p.sw;
  return [x - (dy / d) * sw, y + (dx / d) * sw, s];
}
// hero candle geometry (shared with S3)
const HERO = {};
function drawS2(ctx, t) {
  if (t < 2.0 || t >= 4.02) return;
  const tau = t - 2.0, sh = shake(t);
  ctx.save(); ctx.translate(sh.x, sh.y);
  const fuse0 = 2.66, fuse1 = 2.92;
  const scan = lerp(S2.x0 - 60, S2.x0 + S2.w + 60, E.ioCubic(inv(fuse0, fuse1, t)));
  const exitU = inv(3.83, 3.96, t);
  // shockwave from the bell
  { const u = inv(2.0, 2.55, t); if (u < 1) { ctx.strokeStyle = rgba(COL.hot, 0.5 * (1 - u)); ctx.lineWidth = 3 + 10 * (1 - u); ctx.beginPath(); ctx.arc(CX, CY, 40 + E.outExpo(u) * 1300, 0, TAU); ctx.stroke(); } }
  // particles (right of the scan line)
  if (t < fuse1 + 0.05) {
    ctx.save();
    if (t > fuse0) { ctx.beginPath(); ctx.rect(scan, 0, W, H); ctx.clip(); }
    for (const p of S2.parts) {
      const [x, y, s] = partPos(p, tau);
      const rad = lerp(p.lit ? 6.5 : 3.6, S2.dr, s);
      dot(ctx, p.lit && s < 0.9 ? 'hot' : 'lit', x, y, rad, lerp(p.lit ? 1 : 0.55, 1, s));
    }
    ctx.restore();
  }
  // solid title (left of the scan line)
  if (t > fuse0) {
    ctx.save();
    if (t < fuse1) { ctx.beginPath(); ctx.rect(0, 0, scan, H); ctx.clip(); }
    font(ctx, 'bs', 900, S2.size);
    for (let i = 0; i < S2.s.length; i++) {
      if (i === S2.iI && t >= 3.8) continue;
      const st = (i / S2.s.length) * 0.03, u = E.inCubic(inv(3.83 + st, 3.95 + st, t));
      ctx.globalAlpha = 1 - u; ctx.fillStyle = COL.acc;
      ctx.fillText(S2.s[i], S2.x0 + S2.xs[i], S2.yb + u * 90);
    }
    ctx.restore();
    if (t < fuse1 + 0.04) {                  // the scan bar itself
      const a = 1 - inv(fuse1, fuse1 + 0.04, t);
      const g = ctx.createLinearGradient(scan - 40, 0, scan + 6, 0);
      g.addColorStop(0, rgba(COL.acc, 0)); g.addColorStop(1, rgba(COL.hot, 0.9 * a));
      ctx.fillStyle = g; ctx.fillRect(scan - 40, S2.capTop - 30, 46, S2.yb - S2.capTop + 60);
    }
  }
  // the I → candle body
  if (t >= 3.8) {
    const u = E.ioQuart(inv(3.8, 4.0, t)), R = S2.Irect, B = HERO.bodyScreen;
    const x = lerp(R.x, B.x, u), y = lerp(R.y, B.y, u), w = lerp(R.w, B.w, u), h = lerp(R.h, B.h, u);
    const wk = E.snap(inv(3.9, 4.0, t));
    ctx.fillStyle = mixc(COL.acc, PAL.green, u);
    if (wk > 0) {
      const cxw = x + w / 2, ww = HERO.wickW;
      ctx.fillRect(cxw - ww / 2, y - (y - HERO.highY) * wk, ww, (y - HERO.highY) * wk);
      ctx.fillRect(cxw - ww / 2, y + h, ww, (HERO.lowY - (y + h)) * wk);
    }
    ctx.fillRect(x, y, w, h);
  }
  // subline, typed
  if (t >= 2.78) {
    ctx.save(); ctx.globalAlpha *= 1 - E.inCubic(exitU);
    ctx.translate(0, E.inCubic(exitU) * 60);
    const vis = typed(S2.sub, t, 2.78, 90);
    font(ctx, 'mn', 400, 34, 0.04); ctx.fillStyle = COL.acc;
    ctx.fillText(vis, S2.x0 + 6, S2.yb + 92);
    const vw = ctx.measureText(vis).width;
    if (vis.length < S2.sub.length || cursorOn(t)) ctx.fillRect(S2.x0 + 6 + vw + 6, S2.yb + 92 - 27, 19, 33);
    ctx.restore();
  }
  // function keys
  for (let i = 0; i < S2.keys.length; i++) {
    const kt = S2.keyT[i]; if (t < kt) continue;
    const R = S2.keyRects[i], pop = spring(t - kt, 3.2, 0.5);
    const pressed = i === 0 && t >= S2.pressT;
    const fall = E.inCubic(inv(3.83 + i * 0.01, 3.95 + i * 0.01, t));
    ctx.save();
    ctx.globalAlpha *= clamp(pop * 1.6) * (1 - fall);
    ctx.translate(R.x + R.w / 2, R.y + R.h / 2 + fall * 70); ctx.scale(0.7 + 0.3 * pop, 0.7 + 0.3 * pop); ctx.translate(-R.w / 2, -R.h / 2);
    const dy = pressed ? 5 : 0;
    rrect(ctx, 0, 6, R.w, R.h, 9); ctx.fillStyle = rgba(COL.acc, 0.35); ctx.fill();
    rrect(ctx, 0, dy, R.w, R.h, 9); ctx.fillStyle = pressed ? COL.acc : COL.ink; ctx.fill();
    ctx.strokeStyle = COL.acc; ctx.lineWidth = 2; ctx.stroke();
    font(ctx, 'mc', 700, 24, 0.14); ctx.fillStyle = pressed ? COL.ink : COL.acc;
    ctx.fillText(S2.keys[i], 26, dy + R.h / 2 + 8);
    if (pressed) { const f = 1 - inv(S2.pressT, S2.pressT + 0.15, t); if (f > 0) { rrect(ctx, -6, dy - 6, R.w + 12, R.h + 12, 12); ctx.strokeStyle = rgba(COL.hot, f); ctx.lineWidth = 3; ctx.stroke(); } }
    ctx.restore();
  }
  ctx.restore();
}

// ══ S3 · PATTERNS (4.00–6.00) ═════════════════════════════════════════════
const S3 = { N: 64, hero: 27, dx: 24, bw: 14, wick: 1.7 };
const cx3 = i => 204 + i * S3.dx, py3 = p => 800 - (p - 100) * 16;
function buildS3() {
  const R = rng(33), ctrl = [[0, 101], [6, 106.5], [14, 118.2], [20, 110.2], [26, 118.2], [32, 128.4], [40, 110.6], [45, 113.8], [50, 118.3], [55, 111.4], [63, 101.5]];
  const base = i => { let k = 0; while (ctrl[k + 1][0] < i) k++; const [a, pa] = ctrl[k], [b, pb] = ctrl[k + 1]; return lerp(pa, pb, E.ioSine((i - a) / (b - a))); };
  const C = [], O = [], Hh = [], L = [];
  for (let i = 0; i < S3.N; i++) C.push(base(i) + (R() - 0.5) * 1.8 + Math.sin(i * 1.7) * 0.5);
  C[26] = 118.4; C[27] = 124.6; C[28] = 125.3;
  for (let i = 0; i < S3.N; i++) {
    O.push(i ? C[i - 1] + (R() - 0.5) * 0.7 : 100.6);
    Hh.push(Math.max(O[i], C[i]) + 0.2 + R() * 1.3); L.push(Math.min(O[i], C[i]) - 0.2 - R() * 1.3);
  }
  O[27] = 118.4; Hh[27] = 125.0; L[27] = 118.1;
  Object.assign(S3, { C, O, Hh, L });
  // camera: pure zoom about fixed point Q so the hero lands centred at s1
  const hc = [cx3(S3.hero), (py3(124.6) + py3(118.4)) / 2], bodyH = py3(118.4) - py3(124.6);
  S3.s1 = 560 / bodyH;
  S3.Q = [(CX - S3.s1 * hc[0]) / (1 - S3.s1), (CY - S3.s1 * hc[1]) / (1 - S3.s1)];
  const sc = (x, y) => [(x - S3.Q[0]) * S3.s1 + S3.Q[0], (y - S3.Q[1]) * S3.s1 + S3.Q[1]];
  const tl = sc(cx3(S3.hero) - S3.bw / 2, py3(124.6)), br = sc(cx3(S3.hero) + S3.bw / 2, py3(118.4));
  HERO.bodyScreen = { x: tl[0], y: tl[1], w: br[0] - tl[0], h: br[1] - tl[1] };
  HERO.highY = sc(0, py3(125.0))[1]; HERO.lowY = sc(0, py3(118.1))[1]; HERO.wickW = S3.wick * S3.s1;
  // neckline through the two troughs
  S3.neck = [[cx3(17), py3(110.35)], [cx3(63) + 30, py3(110.9)]];
  cue(4.0, 'hit'); impulse(4.0, 5, 16);
  [4.06, 4.12, 4.18, 4.24, 4.3].forEach(t => cue(t, 'tick', { v: 0.5 }));
  cue(4.45, 'whoosh', { d: 0.8, dir: -1 });
  cue(5.16, 'draw'); [5.24, 5.34, 5.44, 5.54].forEach(t => cue(t, 'tick', { v: 0.6 }));
  cue(5.62, 'collapse');
}
function s3Scale(t) {
  if (t < 4.45) return S3.s1 * (1 + 0.012 * E.outQuad(inv(4.0, 4.45, t)));
  return Math.exp(lerp(Math.log(S3.s1 * 1.012), 0, E.swift(inv(4.45, 5.3, t))));
}
function drawS3(ctx, t) {
  if (t < 3.99 || t >= 6.1) return;
  const s = s3Scale(t), Q = S3.Q, sh = shake(t);
  const toS = (x, y) => [(x - Q[0]) * s + Q[0] + sh.x, (y - Q[1]) * s + Q[1] + sh.y];
  const col = inv(5.6, 5.8, t);                       // collapse to close line
  const lineA = t < 5.84 ? P(t, 5.7, 5.8, E.outCubic) : 0;
  // price grid
  ctx.save();
  font(ctx, 'mc', 500, 18, 0.1);
  for (let p = 100; p <= 130; p += 5) {
    const y = toS(0, py3(p))[1]; if (y < 110 || y > H - 110) continue;
    const a = 0.5 * P(t, 4.3, 4.9, E.outCubic) * (1 - col);
    ctx.fillStyle = rgba(COL.acc, 0.12 * a / 0.5); ctx.fillRect(0, Math.round(y), W - 150, 1);
    ctx.fillStyle = rgba(COL.acc, a); ctx.fillText(p.toFixed(2), W - 130, y + 6);
  }
  ctx.restore();
  // candles (collapse staggered left→right)
  ctx.save();
  for (let i = 0; i < S3.N; i++) {
    if (i >= 54 && t < 5.25 + (i - 54) * 0.03 && i !== S3.hero) continue;     // live prints at the end
    const ci = E.inCubic(clamp((t - 5.6 - i * 0.0025) / 0.16));
    const o = S3.O[i], c = S3.C[i], hi = S3.Hh[i], lo = S3.L[i];
    const yc = py3(c), up = c >= o;
    const yt = lerp(py3(Math.max(o, c)), yc, ci), yb = lerp(py3(Math.min(o, c)), yc, ci);
    const yh = lerp(py3(hi), yc, ci), yl = lerp(py3(lo), yc, ci);
    const [x0, y0] = toS(cx3(i) - S3.bw / 2, yt), [x1, y1] = toS(cx3(i) + S3.bw / 2, yb);
    if (x1 < -50 || x0 > W + 50) continue;
    const [, yH] = toS(0, yh), [, yL] = toS(0, yl), xm = (x0 + x1) / 2, ww = Math.max(1.6, S3.wick * s);
    const a = (i === S3.hero ? 1 : P(t, 4.44, 4.8, E.outCubic)) * (1 - ci * 0.9);
    if (a <= 0) continue;
    ctx.globalAlpha = a;
    ctx.fillStyle = COL.acc; ctx.strokeStyle = COL.acc;
    ctx.fillRect(xm - ww / 2, yH, ww, Math.max(0, y0 - yH)); ctx.fillRect(xm - ww / 2, y1, ww, Math.max(0, yL - y1));
    if (up || i === S3.hero) ctx.fillRect(x0, y0, x1 - x0, Math.max(ww, y1 - y0));
    else { const lw = Math.max(1.6, 1.6 * s); ctx.lineWidth = lw; ctx.strokeStyle = COL.stop; ctx.fillStyle = COL.stop; ctx.fillRect(xm - ww / 2, yH, ww, Math.max(0, y0 - yH)); ctx.fillRect(xm - ww / 2, y1, ww, Math.max(0, yL - y1)); ctx.strokeRect(x0 + lw / 2, y0 + lw / 2, x1 - x0 - lw, Math.max(0, y1 - y0 - lw)); }
  }
  ctx.globalAlpha = 1; ctx.restore();
  // close line appears as candles collapse (hand-off to OPTIONS)
  if (lineA > 0) {
    ctx.save(); ctx.globalAlpha = lineA; ctx.strokeStyle = COL.acc; ctx.lineWidth = 3; ctx.lineJoin = 'round';
    ctx.beginPath(); for (let i = 0; i < S3.N; i++) { const [x, y] = toS(cx3(i), py3(S3.C[i])); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); } ctx.stroke(); ctx.restore();
  }
  // O/H/L/C read-outs on the hero
  const la = P(t, 4.04, 4.2, E.outCubic) * (1 - P(t, 4.42, 4.56, E.inCubic));
  if (la > 0) {
    const B = HERO.bodyScreen, xm = B.x + B.w / 2;
    const tag = (lab, val, y, side, t0) => {
      if (t < t0) return;
      const u = E.snap(inv(t0, t0 + 0.14, t)), x0 = side < 0 ? B.x - 24 : B.x + B.w + 24, x1 = x0 + side * (side < 0 ? 84 : 150) * u;
      ctx.save(); ctx.globalAlpha *= la;
      ctx.strokeStyle = COL.acc; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(x0, y); ctx.lineTo(x1, y); ctx.stroke();
      ctx.fillStyle = COL.acc; ctx.fillRect(x0 - 3, y - 3, 6, 6);
      font(ctx, 'mc', 600, 22, 0.16);
      const s1 = typed(lab, t, t0 + 0.02, 70), s2 = typed(val, t, t0 + 0.06, 70);
      if (side < 0) { const w2 = ctx.measureText(val).width; ctx.fillStyle = COL.hot; ctx.fillText(s2, x1 - 14 - w2, y + 8); ctx.fillStyle = COL.acc; ctx.fillText(s1, x1 - 14 - w2 - 18 - ctx.measureText(lab).width, y + 8); }
      else { ctx.fillStyle = COL.acc; ctx.fillText(s1, x1 + 14, y + 8); ctx.fillStyle = COL.hot; ctx.fillText(s2, x1 + 14 + ctx.measureText(lab).width + 18, y + 8); }
      ctx.restore();
    };
    tag('HIGH', '125.00', HERO.highY, -1, 4.06); tag('CLOSE', '124.60', B.y, 1, 4.12);
    tag('OPEN', '118.40', B.y + B.h, 1, 4.18); tag('LOW', '118.10', HERO.lowY, -1, 4.24);
    const nm = 'BULLISH MARUBOZU';
    if (t > 4.3) { ctx.save(); ctx.globalAlpha *= la; font(ctx, 'mc', 700, 20, 0.3); ctx.fillStyle = COL.acc; const v = typed(nm, t, 4.3, 90); ctx.fillText(v, xm - tw(ctx, nm) / 2, HERO.highY - 44); ctx.restore(); }
  }
  // crosshair locked to the hero close while the camera pulls out
  { const ca = P(t, 4.0, 4.12, E.outCubic) * (1 - P(t, 4.9, 5.1, E.lin));
    if (ca > 0) { const [x, y] = toS(cx3(S3.hero), py3(124.6)); crosshair(ctx, x, y, { alpha: ca, yl: '124.60', xl: '10:41 ET', y1: H - 90, x1: W - 150 }); } }
  // structure annotations
  if (t > 5.14) {
    const na = 1 - P(t, 5.62, 5.75, E.lin);
    ctx.save(); ctx.globalAlpha = na;
    const u = E.outCubic(inv(5.16, 5.46, t));
    const [ax, ay] = toS(...S3.neck[0]), [bx, by] = toS(...S3.neck[1]);
    ctx.strokeStyle = COL.hot; ctx.lineWidth = 2.5; ctx.setLineDash([12, 9]);
    ctx.beginPath(); ctx.moveTo(ax, ay); ctx.lineTo(lerp(ax, bx, u), lerp(ay, by, u)); ctx.stroke(); ctx.setLineDash([]);
    if (u > 0.95) text(ctx, 'NECKLINE', bx, by + 36, { fam: 'mc', wt: 700, size: 18, track: 0.3, align: 1, col: COL.hot, alpha: P(t, 5.44, 5.55) });
    const peak = (i0, i1, lab, t0) => {
      if (t < t0) return; const pu = E.snap(inv(t0, t0 + 0.18, t));
      let hi = 0; for (let i = i0; i <= i1; i++) hi = Math.max(hi, S3.Hh[i]);
      const [x0, y] = toS(cx3(i0), py3(hi) - 26), [x1] = toS(cx3(i1), 0), xm = (x0 + x1) / 2;
      ctx.strokeStyle = COL.acc; ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(xm - (xm - x0) * pu, y + 12); ctx.lineTo(xm - (xm - x0) * pu, y); ctx.lineTo(xm + (x1 - xm) * pu, y); ctx.lineTo(xm + (x1 - xm) * pu, y + 12); ctx.stroke();
      font(ctx, 'mc', 700, 18, 0.28); const v = typed(lab, t, t0 + 0.03, 90); ctx.fillStyle = COL.acc;
      ctx.fillText(v, xm - tw(ctx, lab) / 2, y - 16);
    };
    peak(11, 17, 'LEFT SHOULDER', 5.24); peak(29, 35, 'HEAD', 5.34); peak(47, 53, 'RIGHT SHOULDER', 5.44);
    ctx.restore();
  }
  header(ctx, t, '01 / 05', 'PATTERNS', 4.0, 5.95);
  caption(ctx, t, 'FROM SINGLE CANDLES TO CHART STRUCTURES.', 4.56, 5.95, { cps: 70 });
}

// ══ S4 · OPTIONS (6.00–8.00) ══════════════════════════════════════════════
const S4 = { n: 161, x0: 300, x1: 1620, y0: 600, k: 68 };
const ncdf = x => { const t = 1 / (1 + 0.2316419 * Math.abs(x)), d = 0.3989423 * Math.exp(-x * x / 2); const p = d * t * (0.3193815 + t * (-0.3565638 + t * (1.781478 + t * (-1.821256 + t * 1.330274)))); return x > 0 ? 1 - p : p; };
const npdf = x => Math.exp(-x * x / 2) / Math.sqrt(TAU);
function buildS4() {
  const n = S4.n, S = i => 80 + (40 * i) / (n - 1), X = i => lerp(S4.x0, S4.x1, i / (n - 1));
  const legs = [
    { f: s => Math.max(90 - s, 0) - 0.8, lab: '+1  90 PUT', kx: 90 },
    { f: s => -Math.max(95 - s, 0) + 2.0, lab: '−1  95 PUT', kx: 95 },
    { f: s => -Math.max(s - 105, 0) + 1.8, lab: '−1  105 CALL', kx: 105 },
    { f: s => Math.max(s - 110, 0) - 1.0, lab: '+1  110 CALL', kx: 110 },
  ];
  S4.legs = legs.map(l => ({ ...l, y: Array.from({ length: n }, (_, i) => S4.y0 - S4.k * l.f(S(i))) }));
  S4.sum = Array.from({ length: n }, (_, i) => S4.y0 - S4.k * legs.reduce((a, l) => a + l.f(S(i)), 0));
  S4.X = Array.from({ length: n }, (_, i) => X(i));
  // greeks for a 30-day ATM call (vol 30%); gamma & vega shown at different tenors to separate shapes
  const bs = (s, T) => { const v = 0.3, d1 = (Math.log(s / 100) + 0.5 * v * v * T) / (v * Math.sqrt(T)); return { d1, v, T }; };
  const norm = (arr, lo, hi) => { const mn = Math.min(...arr), mx = Math.max(...arr); return arr.map(a => lerp(lo, hi, (a - mn) / (mx - mn))); };
  const top = S4.y0 - 170, bot = S4.y0 + 170;
  const delta = [], gamma = [], theta = [], vega = [];
  for (let i = 0; i < n; i++) {
    const s = S(i);
    let b = bs(s, 30 / 365); delta.push(ncdf(b.d1));
    b = bs(s, 12 / 365); gamma.push(npdf(b.d1) / (s * b.v * Math.sqrt(b.T)));
    b = bs(s, 30 / 365); theta.push(-(s * npdf(b.d1) * b.v) / (2 * Math.sqrt(b.T)));
    b = bs(s, 90 / 365); vega.push(s * npdf(b.d1) * Math.sqrt(b.T));
  }
  S4.g = [
    { name: 'DELTA', y: norm(delta, bot, top), t0: 6.95 },
    { name: 'GAMMA', y: norm(gamma, bot, top), t0: 7.2 },
    { name: 'THETA', y: norm(theta, bot, top), t0: 7.45 },
    { name: 'VEGA', y: norm(vega, bot, top), t0: 7.7 },
  ];
  // close-line from PATTERNS, resampled onto the same n points
  S4.close = Array.from({ length: n }, (_, i) => { const f = (i / (n - 1)) * (S3.N - 1), a = Math.floor(f), b = Math.min(S3.N - 1, a + 1); return [lerp(cx3(a), cx3(b), f - a), py3(lerp(S3.C[a], S3.C[b], f - a))]; });
  cue(6.0, 'hit'); impulse(6.0, 4, 16);
  [6.1, 6.18, 6.26, 6.34].forEach((t, i) => cue(t, 'zip', { i }));
  cue(6.5, 'merge'); cue(6.82, 'tick', { v: 0.7 }); cue(6.86, 'tick', { v: 0.7 });
  S4.g.forEach(g => cue(g.t0, 'morph'));
  cue(7.82, 'riser2');
}
const curveY = (arr, i) => arr[i];
function s4Line(t) {  // current bold curve y[] (after the legs have summed)
  let y = S4.sum.slice();
  for (const g of S4.g) {
    const u = E.ioCubic(inv(g.t0, g.t0 + 0.16, t)); if (u <= 0) break;
    y = y.map((v, i) => lerp(v, g.y[i], E.ioCubic(clamp(u * 1.4 - (i / S4.n) * 0.4))));
  }
  return y;
}
function drawS4(ctx, t) {
  if (t < 5.84 || t >= 8.02) return;
  const sh = shake(t), n = S4.n;
  ctx.save(); ctx.translate(sh.x, sh.y);
  // axes: close line flattens into the zero line
  const m = E.ioCubic(inv(5.84, 6.12, t));
  const axA = 1 - P(t, 7.8, 7.95, E.lin);
  ctx.save(); ctx.globalAlpha = axA;
  ctx.strokeStyle = mixc(PAL.green, COL.acc, m, lerp(1, 0.45, m)); ctx.lineWidth = lerp(3, 1.5, m);
  ctx.beginPath();
  for (let i = 0; i < n; i++) { const st = clamp(m * 1.3 - (i / n) * 0.3); const x = lerp(S4.close[i][0], S4.X[i], st), y = lerp(S4.close[i][1], S4.y0, st); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); }
  ctx.stroke();
  // strikes on the axis
  const ta = P(t, 6.05, 6.3);
  font(ctx, 'mc', 600, 18, 0.1);
  for (const k of [90, 95, 100, 105, 110]) {
    const x = lerp(S4.x0, S4.x1, (k - 80) / 40);
    ctx.fillStyle = rgba(COL.acc, 0.6 * ta); ctx.fillRect(x - 1, S4.y0 - 6, 2, 12);
    ctx.fillText(String(k), x - tw(ctx, String(k)) / 2, S4.y0 + 246);
  }
  ctx.fillStyle = rgba(COL.acc, 0.5 * ta); ctx.fillText('STRIKE', S4.x1 + 20, S4.y0 + 246);
  ctx.fillText('P&L', S4.x0 - 70, S4.y0 - 160);
  ctx.restore();
  // chart clip
  ctx.save(); ctx.beginPath(); ctx.rect(S4.x0 - 20, 330, S4.x1 - S4.x0 + 40, 510); ctx.clip();
  // legs fly in, then sum
  const mg = E.ioCubic(inv(6.5, 6.8, t));
  const legA = 1 - P(t, 6.72, 6.84, E.lin);
  if (legA > 0 && t > 6.1) {
    S4.legs.forEach((l, j) => {
      const t0 = 6.1 + j * 0.08, u = E.outCubic(inv(t0, t0 + 0.22, t)); if (u <= 0) return;
      const pts = S4.X.map((x, i) => [x, lerp(l.y[i], S4.sum[i], E.ioCubic(clamp(mg * 1.3 - (i / n) * 0.3)))]);
      ctx.save(); ctx.globalAlpha = legA * 0.85; ctx.strokeStyle = COL.acc; ctx.lineWidth = 2; ctx.setLineDash(j % 2 ? [8, 6] : []);
      const L = polyLen(pts); polyPath(ctx, pts, L, 0, L[L.length - 1] * u); ctx.stroke(); ctx.restore();
      const kx = lerp(S4.x0, S4.x1, (l.kx - 80) / 40), ki = Math.round(((l.kx - 80) / 40) * (n - 1));
      const ly = pts[ki][1] + (j % 2 ? 44 : -26);
      ctx.save(); ctx.globalAlpha = legA * P(t, t0 + 0.08, t0 + 0.2); font(ctx, 'mc', 600, 19, 0.12); ctx.fillStyle = COL.hot;
      ctx.fillText(l.lab, kx - tw(ctx, l.lab) / 2, ly); ctx.restore();
    });
  }
  // the summed payoff, morphing through the greeks
  const bA = P(t, 6.6, 6.82, E.outCubic) * (1 - P(t, 7.83, 7.9, E.lin));
  if (bA > 0) {
    const y = s4Line(t), fillA = P(t, 6.8, 6.95) * (1 - P(t, 6.95, 7.05, E.lin));
    if (fillA > 0) {
      for (const pos of [true, false]) {
        ctx.save(); ctx.globalAlpha = fillA;
        ctx.beginPath(); ctx.moveTo(S4.X[0], S4.y0);
        for (let i = 0; i < n; i++) ctx.lineTo(S4.X[i], pos ? Math.min(y[i], S4.y0) : Math.max(y[i], S4.y0));
        ctx.lineTo(S4.X[n - 1], S4.y0); ctx.closePath();
        ctx.fillStyle = pos ? rgba(COL.acc, 0.2) : rgba(COL.stop, 0.22); ctx.fill();
        ctx.clip(); ctx.strokeStyle = pos ? rgba(COL.acc, 0.35) : rgba(COL.stop, 0.4); ctx.lineWidth = 1.5;
        ctx.beginPath(); for (let d = -600; d < 1800; d += 14) { ctx.moveTo(S4.x0 + d, 300); ctx.lineTo(S4.x0 + d + 520, 820); } ctx.stroke();
        ctx.restore();
      }
    }
    ctx.save(); ctx.globalAlpha = bA; ctx.strokeStyle = COL.acc; ctx.lineWidth = 5; ctx.lineJoin = 'round';
    ctx.beginPath(); for (let i = 0; i < n; i++) i ? ctx.lineTo(S4.X[i], y[i]) : ctx.moveTo(S4.X[i], y[i]); ctx.stroke();
    ctx.strokeStyle = rgba(COL.hot, 0.8); ctx.lineWidth = 1.5; ctx.stroke(); ctx.restore();
    // break-evens
    const ba = fillA;
    if (ba > 0) for (const be of [93, 107]) {
      const x = lerp(S4.x0, S4.x1, (be - 80) / 40);
      ctx.save(); ctx.globalAlpha = ba; ctx.fillStyle = COL.hot; ctx.beginPath(); ctx.arc(x, S4.y0, 7, 0, TAU); ctx.fill();
      font(ctx, 'mc', 700, 18, 0.14); const lw = tw(ctx, 'B/E ' + be);
      ctx.fillText('B/E ' + be, be < 100 ? x - lw - 16 : x + 16, S4.y0 - 16); ctx.restore();
    }
    // crosshair riding the curve
    const cA = P(t, 6.95, 7.1) * (1 - P(t, 7.75, 7.85, E.lin));
    if (cA > 0) {
      const f = 0.5 + 0.36 * Math.sin((t - 6.95) * 5.2), i = Math.round(f * (n - 1));
      ctx.restore(); ctx.save();
      crosshair(ctx, S4.X[i], y[i], { alpha: cA, x0: S4.x0 - 20, x1: S4.x1 + 20, y0: 330, y1: 840, xl: 'S ' + (80 + 40 * f).toFixed(2) });
    }
  }
  ctx.restore();
  // greek name, slot-rolled (a tick, not a trend)
  const gi = S4.g.reduce((k, g, i) => (t >= g.t0 + 0.08 ? i : k), -1);
  if (gi >= 0 && t < 7.86) {
    const g = S4.g[gi], roll = inv(g.t0 + 0.08, g.t0 + 0.13, t);
    ctx.save(); ctx.beginPath(); ctx.rect(900, 128, 900, 150); ctx.clip();
    if (gi > 0 && roll < 1) text(ctx, S4.g[gi - 1].name, W - M, 262 - roll * 150, { fam: 'bs', wt: 200, size: 170, align: 1 });
    text(ctx, g.name, W - M, 262 + (1 - roll) * 150, { fam: 'bs', wt: 200, size: 170, align: 1 });
    ctx.restore();
  }
  ctx.restore();
  // paper flood: the vega bell fills and rises to cover the frame
  if (t >= 7.82) {
    const u = E.inExpo(inv(7.82, 8.0, t)), y = S4.g[3].y;
    ctx.save(); ctx.fillStyle = COL.paper; ctx.beginPath(); ctx.moveTo(-50, H + 50);
    for (let i = 0; i < n; i++) { const x = lerp(S4.X[i], lerp(-60, W + 60, i / (n - 1)), u); ctx.lineTo(x, lerp(y[i], -400, u)); }
    ctx.lineTo(W + 50, H + 50); ctx.closePath(); ctx.fill();
    ctx.strokeStyle = mixc(COL.acc, COL.paper, u); ctx.lineWidth = 5; ctx.stroke(); ctx.restore();
  }
  header(ctx, t, '02 / 05', 'OPTIONS', 6.0, 7.9);
  caption(ctx, t, 'MULTI-LEG PAYOFF.', 6.3, 6.95, { cps: 70 });
  caption(ctx, t, 'THE GREEKS, AS CURVES.', 6.98, 7.84, { cps: 70 });
}

// ══ S5 · RISK, on paper (8.00–10.50) ══════════════════════════════════════
const S5 = { flaps: ['AAPL', '/ES', 'SPY 580C', 'NVDA', '/CL', 'QQQ 520P', 'BTC', 'TSLA'], flipT: [9.25, 9.375, 9.5, 9.625, 9.75, 9.875, 10.0, 10.125], cells: 8 };
function buildS5(ctx) {
  // paper stock: fibre, speckle, tractor-feed holes, ruling
  const c = mkCanvas(W, H), g = c.getContext('2d'), R = rng(55);
  g.fillStyle = COL.paper; g.fillRect(0, 0, W, H);
  const id = g.getImageData(0, 0, W, H), d = id.data;
  for (let i = 0; i < d.length; i += 4) { const n = (R() - 0.5) * 10; d[i] += n; d[i + 1] += n; d[i + 2] += n * 0.9; }
  g.putImageData(id, 0, 0);
  for (let i = 0; i < 2400; i++) {
    g.strokeStyle = `rgba(60,90,110,${0.03 + R() * 0.05})`; g.lineWidth = 0.5 + R() * 0.9;
    const x = R() * W, y = R() * H, a = R() * TAU, l = 5 + R() * 22;
    g.beginPath(); g.moveTo(x, y); g.quadraticCurveTo(x + Math.cos(a + 0.6) * l * 0.5, y + Math.sin(a + 0.6) * l * 0.5, x + Math.cos(a) * l, y + Math.sin(a) * l); g.stroke();
  }
  g.strokeStyle = 'rgba(7,13,18,0.06)'; g.lineWidth = 1;
  for (let y = 60; y < H; y += 60) { g.beginPath(); g.moveTo(96, y + 0.5); g.lineTo(W - 96, y + 0.5); g.stroke(); }
  g.setLineDash([3, 7]); g.strokeStyle = 'rgba(7,13,18,0.22)';
  for (const x of [80, W - 80]) { g.beginPath(); g.moveTo(x, 0); g.lineTo(x, H); g.stroke(); }
  g.setLineDash([]);
  for (let y = 24; y < H; y += 48) for (const x of [40, W - 40]) {
    g.fillStyle = COL.ink; g.beginPath(); g.arc(x, y, 10, 0, TAU); g.fill();
    g.strokeStyle = 'rgba(7,13,18,0.25)'; g.lineWidth = 1.5; g.stroke();
  }
  S5.paper = c;
  // rubber-stamp speckle mask for STOP.
  const sm = mkCanvas(700, 260), sg = sm.getContext('2d');
  for (let i = 0; i < 1400; i++) { sg.fillStyle = `rgba(0,0,0,${0.3 + R() * 0.7})`; sg.beginPath(); sg.arc(R() * 700, R() * 260, R() * 2.6, 0, TAU); sg.fill(); }
  S5.stampMask = sm;
  // equation layout
  font(ctx, 'bs', 800, 150);
  const toks = [['$200', 'RISK'], ['÷', ''], ['$2', 'STOP DISTANCE'], ['=', ''], ['100', 'SHARES']];
  let x = 150; S5.toks = [];
  toks.forEach(([s, l], i) => { const w = s === '100' ? tw(ctx, '000') : tw(ctx, s); S5.toks.push({ s, l, x, w, t0: 8.5 + i * N16 }); x += w + 46; });
  font(ctx, 'bs', 800, 150); S5.dw = tw(ctx, '0'); S5.oxs = charXs(ctx, '100'); S5.ow = tw(ctx, '100');
  { // find the centre of the left stroke of the last 0 by scanning pixels
    const c2 = mkCanvas(600, 220), g2 = c2.getContext('2d'); font(g2, 'bs', 800, 150); g2.fillStyle = '#000';
    g2.fillText('100', 20, 180);
    const row = g2.getImageData(0, 180 - 54, 600, 1).data; let a = -1, b = -1;
    for (let x = Math.floor(20 + S5.oxs[2]); x < 600; x++) { const on = row[x * 4 + 3] > 200; if (on && a < 0) a = x; if (a >= 0 && !on) { b = x; break; } }
    S5.zoff = (a + b) / 2 - 20;
  }
  cue(8.0, 'paper'); impulse(8.0, 3, 18);
  ['SIZE', 'FROM', 'THE'].forEach((w, i) => cue(8.0 + i * N16, 'print'));
  cue(8.375, 'stamp'); impulse(8.375, 12, 14);
  S5.toks.forEach(k => cue(k.t0, 'print'));
  cue(9.0, 'spin'); [9.14, 9.22, 9.3].forEach(t => cue(t, 'land'));
  S5.flipT.forEach(t => cue(t, 'flap', { n: S5.cells }));
  cue(10.26, 'whoosh', { d: 0.26, dir: 1 });
}
function flapCell(ctx, x, y, w, h, a, b, u) {  // split-flap from char a → b, u∈[0,1]
  const half = h / 2, r = 7;
  const glyph = (ch, clipTop) => {
    ctx.save(); ctx.beginPath(); ctx.rect(x, clipTop ? y : y + half, w, half); ctx.clip();
    rrect(ctx, x, y, w, h, r); ctx.fillStyle = COL.flap; ctx.fill();
    font(ctx, 'mn', 700, 62, 0); ctx.fillStyle = COL.paper;
    if (ch && ch !== ' ') ctx.fillText(ch, x + (w - tw(ctx, ch)) / 2, y + h / 2 + 22);
    ctx.restore();
  };
  if (u <= 0 || a === b) { glyph(a, true); glyph(a, false); }
  else if (u >= 1) { glyph(b, true); glyph(b, false); }
  else {
    glyph(b, true); glyph(a, false);
    ctx.save();
    if (u < 0.5) { const k = Math.cos(u * Math.PI); ctx.translate(0, y + half); ctx.scale(1, k); ctx.translate(0, -(y + half)); glyph(a, true); ctx.restore(); ctx.save(); ctx.globalAlpha = 0.35 * (1 - k); ctx.fillStyle = '#000'; ctx.fillRect(x, y, w, half); }
    else { const k = -Math.cos(u * Math.PI); ctx.translate(0, y + half); ctx.scale(1, k); ctx.translate(0, -(y + half)); glyph(b, false); ctx.restore(); ctx.save(); ctx.globalAlpha = 0.3 * (1 - k); ctx.fillStyle = '#000'; ctx.fillRect(x, y + half, w, half); }
    ctx.restore();
  }
  ctx.fillStyle = 'rgba(0,0,0,0.55)'; ctx.fillRect(x, y + half - 1, w, 2);
}
function drawS5(ctx, t) {
  if (t < 7.98 || t >= 10.52) return;
  const sh = shake(t);
  // zoom-through into an ink stroke of "100" at the end
  const zu = E.inExpo(inv(10.28, 10.5, t)), zt = S5.toks[4];
  const zx = zt.x + S5.zoff, zy = 790 - 54;
  const zs = 1 + zu * 170;
  const drift = 1 + 0.02 * inv(8.0, 10.28, t);
  ctx.save();
  ctx.translate(zx + sh.x, zy + sh.y); ctx.scale(zs * drift, zs * drift); ctx.translate(-zx, -zy);
  ctx.drawImage(S5.paper, 0, 0);
  const ink = COL.print;
  const iw = text(ctx, '03 / 05', 150, 150, { fam: 'mc', wt: 600, size: 20, track: 0.3, col: ink, alpha: 0.7 * P(t, 8.0, 8.2) });
  ctx.fillStyle = ink; ctx.fillRect(150 + iw + 20, 143, 60 * P(t, 8.0, 8.2), 2);
  text(ctx, 'RISK', 150 + iw + 100, 150, { fam: 'mc', wt: 800, size: 20, track: 0.3, col: ink, alpha: P(t, 8.05, 8.25) });
  // headline, printed word by word
  const words = [['SIZE', 150, 330, 8.0], ['FROM', 0, 330, 8.125], ['THE', 150, 510, 8.25]];
  font(ctx, 'bs', 900, 190, 0.0);
  const sw = tw(ctx, 'SIZE '), fx = 150 + sw;
  for (const [w, x0, y, t0] of words) {
    if (t < t0) continue;
    const x = w === 'FROM' ? fx : x0, bleed = 1 - inv(t0, t0 + 0.12, t);
    font(ctx, 'bs', 900, 190, 0.0);
    if (bleed > 0) { ctx.save(); ctx.filter = 'blur(5px)'; ctx.fillStyle = rgba(ink, 0.5 * bleed); ctx.fillText(w, x, y); ctx.restore(); }
    ctx.fillStyle = ink; ctx.fillText(w, x, y);
  }
  // STOP. rubber stamp
  if (t >= 8.375) {
    const u = inv(8.375, 8.43, t), sc = lerp(1.5, 1, E.inQuad(u)), a = clamp(u * 3);
    font(ctx, 'bs', 900, 190, 0); const tw1 = tw(ctx, 'THE ');
    const sx = 150 + tw1, sy = 510, ww = tw(ctx, 'STOP.');
    const st = mkStamp(ww);
    ctx.save(); ctx.globalAlpha = a; ctx.translate(sx + ww / 2, sy - 66); ctx.rotate(-0.035); ctx.scale(sc, sc);
    ctx.drawImage(st, -st.width / 2, -st.height / 2); ctx.restore();
  }
  // equation, printed on 16ths; 100 rolls like an odometer
  for (let i = 0; i < S5.toks.length; i++) {
    const k = S5.toks[i]; if (t < k.t0) continue;
    const pc = i === 2 ? COL.stop : ink, y = 790;
    const land = E.outCubic(inv(k.t0, k.t0 + 0.08, t));
    ctx.save(); ctx.translate(0, (1 - land) * -16); ctx.globalAlpha = clamp(land * 2);
    if (k.s === '100') odometer(ctx, k.x, y, t, pc);
    else { font(ctx, 'bs', 800, 150, 0); ctx.fillStyle = pc; ctx.fillText(k.s, k.x, y); }
    ctx.restore();
    if (k.l) text(ctx, k.l, k.x + 4, y + 50, { fam: 'mc', wt: 700, size: 20, track: 0.28, col: pc, alpha: 0.75 * P(t, k.t0 + 0.05, k.t0 + 0.2) });
  }
  // split-flap instrument board
  const ba = P(t, 8.5, 8.62, E.outCubic);
  if (ba > 0) {
    const bx = 1180, by = 240, cw = 64, chh = 100, gap = 6;
    ctx.save(); ctx.globalAlpha = ba;
    text(ctx, 'INSTRUMENT', bx, by - 22, { fam: 'mc', wt: 700, size: 20, track: 0.3, col: ink, alpha: 0.75 });
    text(ctx, 'ANY TICKER. ANY CONTRACT.', bx + S5.cells * (cw + gap) - gap, by + chh + 44, { fam: 'mc', wt: 600, size: 18, track: 0.24, col: ink, alpha: 0.55, align: 1 });
    let fi = -1; for (let i = 0; i < S5.flipT.length; i++) if (t >= S5.flipT[i]) fi = i;
    const cur = S5.flaps[Math.max(0, fi)].padEnd(S5.cells), nxt = fi >= 0 && fi + 1 < S5.flaps.length ? S5.flaps[fi + 1].padEnd(S5.cells) : cur;
    const prevS = fi > 0 ? S5.flaps[fi - 1].padEnd(S5.cells) : S5.flaps[0].padEnd(S5.cells);
    for (let c = 0; c < S5.cells; c++) {
      const x = bx + c * (cw + gap), tf = fi >= 0 ? S5.flipT[fi] + c * 0.008 : 99;
      const u = fi >= 0 ? inv(tf, tf + 0.09, t) : 0;
      flapCell(ctx, x, by, cw, chh, fi > 0 ? prevS[c] : (fi === 0 ? S5.flaps[0].padEnd(S5.cells)[c] : cur[c]), cur[c], fi >= 1 ? u : 1);
    }
    void nxt;
    ctx.restore();
  }
  caption(ctx, t, 'THE INSTRUMENT CHANGES. THE ARITHMETIC DOES NOT.', 8.98, 10.36, { cps: 80, col: ink, x: 150, y: 960 });
  ctx.restore();
}
const STAMPS = new Map();
function mkStamp(ww) {
  const k = Math.round(ww); if (STAMPS.has(k)) return STAMPS.get(k);
  const c = mkCanvas(k + 80, 260), g = c.getContext('2d');
  font(g, 'bs', 900, 190, 0); g.fillStyle = COL.stop; g.fillText('STOP.', 40, 196);
  g.globalCompositeOperation = 'destination-out'; g.globalAlpha = 0.55; g.drawImage(S5.stampMask, 0, 0, c.width, c.height);
  STAMPS.set(k, c); return c;
}
function odometer(ctx, x, y, t, col) {
  font(ctx, 'bs', 800, 150, 0); ctx.fillStyle = col;
  const lh = 150, fin = [1, 0, 0], land = [9.14, 9.22, 9.3], spins = [2, 3, 4];
  ctx.save(); ctx.beginPath(); ctx.rect(x - 10, y - 126, S5.ow + 30, 144); ctx.clip();
  for (let d = 0; d < 3; d++) {
    const pos = (fin[d] + 10 * spins[d]) * E.outCubic(inv(9.0, land[d], t)), a = Math.floor(pos), r = pos - a, dx = x + S5.oxs[d];
    ctx.fillText(String(a % 10), dx, y - r * lh);
    if (r > 0.001) ctx.fillText(String((a + 1) % 10), dx, y + (1 - r) * lh);
  }
  ctx.restore();
}

// ══ S6 · VOCABULARY (10.50–12.50) ═════════════════════════════════════════
const S6 = {
  terms: [
    ['BID', 'bs', 900, 'fill'], ['ASK', 'bs', 200, 'fill'], ['0DTE', 'mw', 800, 'fill'], ['VWAP', 'bs', 900, 'outline'],
    ['IV CRUSH', 'bs', 900, 'block'], ['FLOAT', 'mc', 300, 'fill'], ['HALT', 'bs', 900, 'red'], ['PDT RULE', 'bs', 900, 'struck'],
    ['SLIPPAGE', 'mn', 700, 'fill'], ['SHORT SQUEEZE', 'bs', 900, 'fill'],
  ],
  count: [4, 9, 15, 21, 27, 33, 40, 47, 54, 61],
};
function buildS6(ctx) {
  S6.fit = S6.terms.map(([s, fam, wt]) => { font(ctx, fam, wt, 100); const w = tw(ctx, s); return Math.min(fam === 'bs' ? 400 : 250, Math.round(100 * 1500 / w)); });
  S6.terms.forEach((_, i) => cue(10.5 + i * N16, 'term', { i }));
  cue(10.5, 'hit'); impulse(10.5, 6, 16);
  cue(11.75, 'drop2'); impulse(11.75, 8, 14);
  S6.led = ledCells('61');
}
function drawS6(ctx, t) {
  if (t < 10.46 || t >= 12.52) return;
  const sh = shake(t);
  ctx.save(); ctx.translate(sh.x, sh.y);
  const k = Math.floor((t - 10.5) / N16);
  if (t >= 10.5 && k < S6.terms.length) {
    const [s, fam, wt, sty] = S6.terms[k], size = S6.fit[k], u = inv(10.5 + k * N16, 10.5 + (k + 1) * N16, t);
    const sc = 1.06 - 0.06 * E.outCubic(u);
    ctx.save(); ctx.translate(CX, 560); ctx.scale(sc, sc);
    font(ctx, fam, wt, size, 0); const w = tw(ctx, s), yb = size * 0.35;
    if (sty === 'block') { ctx.fillStyle = COL.acc; ctx.fillRect(-w / 2 - 40, yb - size * 0.86, w + 80, size * 1.02); ctx.fillStyle = COL.ink; ctx.fillText(s, -w / 2, yb); }
    else if (sty === 'outline') { ctx.strokeStyle = COL.acc; ctx.lineWidth = 4; ctx.lineJoin = 'round'; ctx.strokeText(s, -w / 2, yb); }
    else { ctx.fillStyle = sty === 'red' ? COL.stop : COL.acc; ctx.fillText(s, -w / 2, yb); }
    if (sty === 'struck') {
      ctx.fillStyle = COL.stop; ctx.fillRect(-w / 2 - 30, yb - size * 0.36, (w + 60) * E.outExpo(clamp(u * 3)), 16);
      text(ctx, 'RETIRED 06.04.26 — FINRA 4210', w / 2, yb + 64, { fam: 'mc', wt: 700, size: 22, track: 0.24, col: COL.stop, align: 1 });
    }
    ctx.restore();
  }
  // corner counter (LED face)
  if (t >= 10.5 && t < 11.78) {
    const v = S6.count[Math.min(S6.terms.length - 1, Math.max(0, k))];
    text(ctx, String(v).padStart(2, '0') + ' / 61', W - M, 900, { fam: 'dot', wt: 900, size: 64, align: 1 });
    text(ctx, 'TERMS', W - M, 800, { fam: 'mc', wt: 700, size: 20, track: 0.3, align: 1, alpha: 0.7 });
  }
  // landing: 61 on the LED board + TERMS.
  if (t >= 11.75) {
    const L = S6.led, p = 36, r = 14;
    font(ctx, 'bs', 900, 300, 0); const tw2 = tw(ctx, 'TERMS.');
    const bw = (L.cols - 1) * p, total = bw + 70 + tw2, x0 = CX - total / 2, y0 = 560 - ((L.rows - 1) / 2) * p;
    for (let r0 = 0; r0 < L.rows; r0++) for (let c = 0; c < L.cols; c++) {
      const on = L.on.has(c + ',' + r0), tt = 11.75 + (c + r0) * 0.0018;
      if (t < tt) continue;
      const fresh = 1 - inv(tt, tt + 0.22, t);
      dot(ctx, 'off', x0 + c * p, y0 + r0 * p, r, 0.9);
      if (on) { dot(ctx, 'lit', x0 + c * p, y0 + r0 * p, r * (1 + 0.25 * fresh)); if (fresh > 0) dot(ctx, 'hot', x0 + c * p, y0 + r0 * p, r, fresh); }
    }
    riseText(ctx, 'TERMS.', x0 + bw + 70, 560 + 107, t, 11.75, { size: 300, stagger: 0.01, dur: 0.2 });
    text(ctx, 'THREE LEVELS, IN READING ORDER.', x0 + bw + 76, 560 + 170, { fam: 'mc', wt: 600, size: 22, track: 0.26, alpha: P(t, 11.88, 12.02) * 0.8 });
  }
  ctx.restore();
  header(ctx, t, '04 / 05', 'VOCABULARY', 10.5, 12.45);
  caption(ctx, t, 'WHAT IT IS · WHY IT MATTERS · THE TRAP MOST PEOPLE MISS', 10.56, 12.45, { cps: 110, red: 'THE TRAP MOST PEOPLE MISS', size: 28 });
}

// ══ S7 · THE PATH (12.50–14.50) ═══════════════════════════════════════════
const S7 = { nodes: [[250, 880], [470, 880], [640, 710], [860, 710], [1030, 540], [1250, 540], [1420, 370], [1660, 370]], T: [], bm: 4 };
function buildS7() {
  S7.T = S7.nodes.map((_, k) => 12.625 + k * N16);
  // metro route with rounded corners
  const pts = [[-40, 880]], rad = 34;
  const raw = [[-40, 880], ...S7.nodes, [2100, 370]];
  S7.nodeD = [];
  const route = [raw[0]];
  for (let i = 1; i < raw.length - 1; i++) {
    const a = raw[i - 1], b = raw[i], c = raw[i + 1];
    const v1 = [b[0] - a[0], b[1] - a[1]], v2 = [c[0] - b[0], c[1] - b[1]];
    const l1 = Math.hypot(...v1), l2 = Math.hypot(...v2), cross = Math.abs(v1[0] * v2[1] - v1[1] * v2[0]) / (l1 * l2);
    if (cross < 0.01) { route.push(b); continue; }
    const p1 = [b[0] - (v1[0] / l1) * rad, b[1] - (v1[1] / l1) * rad], p2 = [b[0] + (v2[0] / l2) * rad, b[1] + (v2[1] / l2) * rad];
    for (let s = 0; s <= 8; s++) { const u = s / 8; const q = [lerp(lerp(p1[0], b[0], u), lerp(b[0], p2[0], u), u), lerp(lerp(p1[1], b[1], u), lerp(b[1], p2[1], u), u)]; route.push(q); }
  }
  route.push(raw[raw.length - 1]); void pts;
  S7.route = route; S7.L = polyLen(route);
  // arc-length of each station (nearest route vertex)
  S7.nodeD = S7.nodes.map(n => { let best = 0, bd = 1e9; route.forEach((p, i) => { const d = Math.hypot(p[0] - n[0], p[1] - n[1]); if (d < bd) { bd = d; best = i; } }); return S7.L[best]; });
  S7.T.forEach((t, k) => cue(t, 'note', { k }));
  cue(12.5, 'hit'); impulse(12.5, 4, 16);
  cue(13.72, 'drop3');
  cue(14.36, 'whoosh', { d: 0.3, dir: -1 });
}
function s7Head(t) {
  const T = S7.T, D = S7.nodeD;
  if (t < 12.5) return 0;
  if (t < T[0]) return lerp(0, D[0], E.outCubic(inv(12.5, T[0], t)));
  for (let k = 0; k < T.length - 1; k++) if (t < T[k + 1]) return lerp(D[k], D[k + 1], E.ioQuad(inv(T[k], T[k + 1], t)));
  return lerp(D[7], D[7] + 120, E.outCubic(inv(T[7], T[7] + 0.3, t)));
}
function drawS7(ctx, t) {
  if (t < 12.48 || t >= 14.52) return;
  const sh = shake(t), whip = E.inExpo(inv(14.36, 14.5, t));
  const cam = 1 + 0.04 * E.outCubic(inv(12.5, 14.4, t));
  ctx.save(); ctx.translate(-whip * 2600 + sh.x, sh.y);
  ctx.translate(CX, 620); ctx.scale(cam, cam); ctx.translate(-CX, -620);
  // future route (faint), travelled route (lit)
  ctx.save(); ctx.lineCap = 'round'; ctx.lineJoin = 'round';
  ctx.strokeStyle = rgba(COL.acc, 0.2 * P(t, 12.5, 12.7)); ctx.lineWidth = 3; ctx.setLineDash([2, 12]);
  polyPath(ctx, S7.route, S7.L, 0, S7.L[S7.L.length - 1]); ctx.stroke(); ctx.setLineDash([]);
  const hd = s7Head(t);
  ctx.strokeStyle = COL.acc; ctx.lineWidth = 9; polyPath(ctx, S7.route, S7.L, 0, hd); ctx.stroke();
  ctx.strokeStyle = rgba(COL.hot, 0.7); ctx.lineWidth = 2.5; ctx.stroke();
  ctx.restore();
  // stations
  S7.nodes.forEach(([x, y], k) => {
    const reached = t >= S7.T[k], u = reached ? t - S7.T[k] : 0;
    const pop = reached ? spring(u, 3.4, 0.45) : 0;
    ctx.save();
    if (reached) { const ru = inv(0, 0.45, u); if (ru < 1) { ctx.strokeStyle = rgba(COL.hot, 0.7 * (1 - ru)); ctx.lineWidth = 3; ctx.beginPath(); ctx.arc(x, y, 18 + 50 * E.outCubic(ru), 0, TAU); ctx.stroke(); } }
    ctx.fillStyle = COL.ink; ctx.beginPath(); ctx.arc(x, y, 17, 0, TAU); ctx.fill();
    ctx.strokeStyle = reached ? COL.acc : rgba(COL.acc, 0.4 * P(t, 12.5, 12.7)); ctx.lineWidth = 4; ctx.stroke();
    if (reached) { dot(ctx, 'hot', x, y, 8 * pop); }
    const lab = String(k + 1).padStart(2, '0'), la = reached ? clamp(pop) : 0.3 * P(t, 12.5, 12.7);
    ctx.translate(x, y - 44); const s = reached ? 0.6 + 0.4 * pop : 1; ctx.scale(s, s);
    text(ctx, lab, 0, 0, { fam: 'mc', wt: 700, size: 26, track: 0.1, align: 0.5, alpha: la, col: reached ? COL.hot : COL.acc });
    ctx.restore();
  });
  // head
  if (t < S7.T[7] + 0.3) { const [x, y] = polyAt(S7.route, S7.L, hd); dot(ctx, 'hot', x, y, 10); }
  // bookmark: your place is kept
  if (t >= 13.72) {
    const [nx, ny] = S7.nodes[S7.bm], u = t - 13.72, drop = spring(u, 2.6, 0.38);
    const y = lerp(ny - 760, ny - 78, drop);
    ctx.save(); ctx.translate(nx, y);
    ctx.fillStyle = COL.acc; ctx.beginPath(); ctx.moveTo(-22, -92); ctx.lineTo(22, -92); ctx.lineTo(22, 0); ctx.lineTo(0, -16); ctx.lineTo(-22, 0); ctx.closePath(); ctx.fill();
    ctx.fillStyle = COL.ink; font(ctx, 'mc', 800, 13, 0.1); ctx.save(); ctx.translate(5, -84); ctx.rotate(Math.PI / 2); ctx.fillText('SAVED', 0, 0); ctx.restore();
    ctx.restore();
  }
  ctx.restore();
  ctx.save(); ctx.translate(-whip * 2600, 0);
  header(ctx, t, '05 / 05', 'THE PATH', 12.5, 14.6);
  caption(ctx, t, 'EIGHT STEPS, IN ORDER.', 13.4, 14.6, { cps: 70, x: W - M, align: 1, y: 880 });
  caption(ctx, t, 'YOUR PLACE IS KEPT.', 13.72, 14.6, { cps: 70, x: W - M, align: 1, y: 930 });
  ctx.restore();
}

// ══ S8 · NO LOGIN (14.50–16.50) ═══════════════════════════════════════════
const S8 = { hits: [15.0, 15.5, 16.0], words: ['NO LOGIN.', 'NO EMAIL.', 'NOTHING FAKE.'], card: { x: 1150, y: 214, w: 620, h: 640 } };
function buildS8() {
  S8.hits.forEach((t, i) => { cue(t, 'slam', { i }); cue(t + 0.02, 'slash', { i }); impulse(t, 10 + i * 3, 12); });
  const s = 'you@example.com'; for (let i = 0; i < s.length; i++) cue(14.62 + i / 55, 'key', { v: 0.25 });
  for (let i = 0; i < 10; i++) cue(14.9 + i / 70, 'key', { v: 0.2 });
}
function drawS8(ctx, t) {
  if (t < 14.5 || t >= 16.52) return;
  const sh = shake(t), enter = 1 - E.outExpo(inv(14.5, 14.8, t));
  const C = S8.card, fx = C.x + 40, fw = C.w - 80;
  ctx.save(); ctx.translate(enter * 2400 + sh.x, sh.y);
  // pieces: [0] password group, [1] email group, [2] button+fine print, frame falls with [2]
  const part = (i) => { const tt = S8.hits[i]; return t < tt ? null : t - tt; };
  const group = (i, draw, cutY) => {
    const d = part(i);
    if (d == null) { draw(); return; }
    const sl = E.outExpo(clamp(d / 0.06));
    for (const side of [-1, 1]) {
      const fall = E.inQuad(clamp((d - 0.05) / 0.45));
      ctx.save();
      ctx.beginPath(); side < 0 ? ctx.rect(0, 0, W, cutY) : ctx.rect(0, cutY, W, H); ctx.clip();
      ctx.translate(fx + fw / 2, cutY); ctx.rotate(side * 0.18 * fall); ctx.translate(-(fx + fw / 2), -cutY + fall * 520 * (side < 0 ? 0.8 : 1.1));
      ctx.globalAlpha *= 1 - fall; draw(); ctx.restore();
    }
    const la = 1 - clamp((d - 0.1) / 0.25);
    if (la > 0) { ctx.save(); ctx.globalAlpha *= la; ctx.strokeStyle = COL.stop; ctx.lineWidth = 12; ctx.lineCap = 'round'; ctx.beginPath(); ctx.moveTo(fx - 30, cutY + 14); ctx.lineTo(lerp(fx - 30, fx + fw + 30, sl), cutY + 14 - 28 * sl); ctx.stroke(); ctx.restore(); }
  };
  // frame
  const fd = part(2), ff = fd == null ? 0 : E.inQuad(clamp((fd - 0.05) / 0.4));
  ctx.save(); ctx.globalAlpha *= 1 - ff; ctx.translate(0, ff * 300);
  rrect(ctx, C.x, C.y, C.w, C.h, 16); ctx.fillStyle = rgba(COL.acc, 0.04); ctx.fill(); ctx.strokeStyle = COL.acc; ctx.lineWidth = 2; ctx.stroke();
  text(ctx, 'CREATE ACCOUNT', fx, C.y + 70, { fam: 'mc', wt: 800, size: 24, track: 0.22 });
  text(ctx, 'STEP 1 OF 3', fx + fw, C.y + 70, { fam: 'mc', wt: 600, size: 18, track: 0.2, align: 1, alpha: 0.6 });
  ctx.fillStyle = rgba(COL.acc, 0.3); ctx.fillRect(fx, C.y + 96, fw, 1.5);
  ctx.restore();
  group(1, () => {
    text(ctx, 'EMAIL', fx, C.y + 160, { fam: 'mc', wt: 700, size: 18, track: 0.3, alpha: 0.75 });
    rrect(ctx, fx, C.y + 176, fw, 74, 8); ctx.strokeStyle = rgba(COL.acc, 0.7); ctx.lineWidth = 2; ctx.stroke();
    const v = typed('you@example.com', t, 14.62, 55); font(ctx, 'mn', 400, 28, 0); ctx.fillStyle = COL.acc; ctx.fillText(v, fx + 22, C.y + 224);
    if (t < 14.9 && cursorOn(t * 2)) ctx.fillRect(fx + 26 + ctx.measureText(v).width, C.y + 198, 3, 32);
  }, C.y + 213);
  group(0, () => {
    text(ctx, 'PASSWORD', fx, C.y + 300, { fam: 'mc', wt: 700, size: 18, track: 0.3, alpha: 0.75 });
    rrect(ctx, fx, C.y + 316, fw, 74, 8); ctx.strokeStyle = rgba(COL.acc, 0.7); ctx.lineWidth = 2; ctx.stroke();
    const v = typed('••••••••••', t, 14.9, 70); font(ctx, 'mn', 700, 28, 0.1); ctx.fillStyle = COL.acc; ctx.fillText(v, fx + 22, C.y + 364);
  }, C.y + 353);
  group(2, () => {
    ctx.strokeStyle = rgba(COL.acc, 0.7); ctx.lineWidth = 2; ctx.strokeRect(fx, C.y + 424, 22, 22);
    text(ctx, 'SEND ME MARKETING EMAILS', fx + 38, C.y + 442, { fam: 'mc', wt: 600, size: 17, track: 0.16, alpha: 0.7 });
    rrect(ctx, fx, C.y + 482, fw, 82, 10); ctx.fillStyle = COL.acc; ctx.fill();
    text(ctx, 'START FREE TRIAL', fx + fw / 2, C.y + 533, { fam: 'mc', wt: 800, size: 26, track: 0.18, align: 0.5, col: COL.ink });
    text(ctx, 'CARD REQUIRED · CANCEL ANYTIME', fx + fw / 2, C.y + 606, { fam: 'mc', wt: 600, size: 15, track: 0.2, align: 0.5, alpha: 0.5 });
  }, C.y + 523);
  ctx.restore();
  // the three answers
  S8.words.forEach((w, i) => {
    const t0 = S8.hits[i]; if (t < t0) return;
    ctx.save(); ctx.translate(sh.x * 0.5, sh.y * 0.5);
    riseText(ctx, w, M - 4, 400 + i * 180, t, t0, { size: 170, stagger: 0.012, dur: 0.2, col: i === 2 ? COL.hot : COL.acc });
    ctx.restore();
  });
}

// ══ S9 · FREE. ALWAYS. (16.50–18.00) ══════════════════════════════════════
const S9 = { bells: [17.5, 17.625, 17.75, 17.875] };
function buildS9(ctx) {
  font(ctx, 'bs', 900, 100);
  const wF = tw(ctx, 'FREE.'), wA = tw(ctx, 'ALWAYS.'), cap = ctx.measureText('FREE.').actualBoundingBoxAscent / 100, gap = 44;
  let fs = 100 * 1500 / wF, as = 100 * 1500 / wA;
  const k = Math.min(1, (780 - gap) / (cap * (fs + as))); fs *= k; as *= k;
  const total = cap * (fs + as) + gap;
  Object.assign(S9, { fs, as, y1: 548 - total / 2 + cap * fs, y2: 548 + total / 2 });
  cue(16.5, 'slam', { i: 3 }); impulse(16.5, 14, 11); cue(16.75, 'slam', { i: 4 }); impulse(16.75, 10, 12);
  cue(17.05, 'implode');
  S9.bells.forEach((b, i) => { cue(b, 'bell', { i, of: 4, close: true }); impulse(b, 3, 18); });
}
function drawS9(ctx, t) {
  if (t < 16.5 || t >= 18.02) return;
  const sh = shake(t), imp = E.inExpo(inv(17.05, 17.42, t));
  ctx.save(); ctx.translate(CX + sh.x, 520 + sh.y); ctx.rotate(imp * 0.25); const s = 1 - imp; ctx.scale(s, s); ctx.translate(-CX, -520);
  if (s > 0.001) {
    const f1 = E.outExpo(inv(16.5, 16.7, t)), sc1 = lerp(1.14, 1, f1);
    ctx.save(); ctx.translate(CX, S9.y1); ctx.scale(sc1, sc1);
    font(ctx, 'bs', 900, S9.fs, 0); ctx.fillStyle = COL.acc; ctx.fillText('FREE.', -tw(ctx, 'FREE.') / 2, 0); ctx.restore();
    if (t >= 16.75) {
      const f2 = E.outExpo(inv(16.75, 16.95, t)), sc2 = lerp(1.14, 1, f2);
      ctx.save(); ctx.translate(CX, S9.y2); ctx.scale(sc2, sc2);
      font(ctx, 'bs', 900, S9.as, 0); ctx.strokeStyle = COL.acc; ctx.lineWidth = 5; ctx.lineJoin = 'round'; ctx.strokeText('ALWAYS.', -tw(ctx, 'ALWAYS.') / 2, 0); ctx.restore();
    }
  }
  ctx.restore();
  // the last cursor, rung by the closing bell
  if (t >= 17.3) {
    const a = P(t, 17.3, 17.42, E.outCubic), bp = bellPulse(t, S9.bells);
    for (const b of S9.bells) { const u = inv(b, b + 0.7, t); if (u <= 0 || u >= 1) continue; ctx.strokeStyle = rgba(COL.acc, 0.3 * (1 - u)); ctx.lineWidth = 1.5 + 4 * (1 - u); ctx.beginPath(); ctx.arc(CX, CY, 30 + E.outCubic(u) * 900, 0, TAU); ctx.stroke(); }
    ctx.fillStyle = mixc(COL.acc, COL.hot, Math.min(1, bp), a);
    const sc = 1 + 0.25 * Math.min(1, bp);
    ctx.fillRect(CX - 11 * sc, CY - 20 * sc, 22 * sc, 40 * sc);
  }
}

// ══ S10 · SIGN-OFF (18.00–20.00) ═════════════════════════════════════════
const S10 = { word: 'THE TRADING DESK', size: 176, ry: 560 };
function buildS10() {
  cue(18.0, 'final'); impulse(18.0, 12, 10);
  const u = '> thetradingdesk.org'; for (let i = 2; i < u.length; i++) cue(18.46 + (i - 2) / 55, 'key', { v: 0.3 });
  cue(18.56, 'tick', { v: 0.8 }); cue(18.9, 'sheen');
  // cache the settled wordmark for the sheen pass
  const c = mkCanvas(W, H), g = c.getContext('2d'); S10.mask = c;
  font(g, 'bs', 800, S10.size, 0.02); const ww = tw(g, S10.word); g.fillStyle = '#fff'; g.fillText(S10.word, CX - ww / 2, S10.ry - 40);
  S10.sheen = mkCanvas(W, H);
}
function wordmark(ctx, t, withMask) {
  const { word, size, ry } = S10;
  font(ctx, 'bs', 800, size, 0.02); const ww = tw(ctx, word), xs = xsFor(ctx, word), x0 = CX - ww / 2, yb = ry - 40;
  for (let i = 0; i < word.length; i++) {
    const t0 = 18.1 + Math.abs(i - 7.5) * 0.012, u = E.snap(inv(t0, t0 + 0.42, t)); if (u <= 0) continue;
    ctx.fillStyle = COL.acc; ctx.fillText(word[i], x0 + xs[i], yb + (1 - u) * (size + 30));
  }
  // the I of TRADING grows wicks: the letter is a candle
  const wk = E.snap(inv(18.56, 18.7, t));
  if (wk > 0) {
    const iI = word.indexOf('I'), m = ctx.measureText('I');
    const l = x0 + xs[iI] - m.actualBoundingBoxLeft, r = x0 + xs[iI] + m.actualBoundingBoxRight, cx = (l + r) / 2, ww2 = Math.max(4, (r - l) * 0.16);
    const top = yb - m.actualBoundingBoxAscent;
    ctx.fillStyle = COL.acc;
    ctx.fillRect(cx - ww2 / 2, top - 40 * wk, ww2, 40 * wk);
    ctx.fillRect(cx - ww2 / 2, yb, ww2, 28 * wk);
  }
}
function drawS10(ctx, t) {
  if (t < 18.0) return;
  const sh = shake(t), push = 1 + 0.025 * E.outCubic(inv(18.3, 20, t)), ry = S10.ry;
  ctx.save(); ctx.translate(CX + sh.x, CY + sh.y); ctx.scale(push, push); ctx.translate(-CX, -CY);
  const rw = lerp(22, 1320, E.snap(inv(18.0, 18.36, t)));
  { const u = inv(18.0, 18.6, t); if (u < 1) { ctx.strokeStyle = rgba(COL.hot, 0.4 * (1 - u)); ctx.lineWidth = 2 + 8 * (1 - u); ctx.beginPath(); ctx.arc(CX, ry, 20 + E.outExpo(u) * 1200, 0, TAU); ctx.stroke(); } }
  ctx.fillStyle = COL.acc; ctx.fillRect(CX - rw / 2, ry - 2, rw, 4);
  ctx.save(); ctx.beginPath(); ctx.rect(0, 0, W, ry - 6); ctx.clip();
  wordmark(ctx, t);
  ctx.restore();
  // one phosphor sheen across the wordmark
  const su = inv(18.9, 19.45, t);
  if (su > 0 && su < 1) {
    const sx = lerp(CX - 800, CX + 800, E.ioCubic(su)), g = S10.sheen.getContext('2d');
    g.setTransform(1, 0, 0, 1, 0, 0); g.globalCompositeOperation = 'copy'; g.drawImage(S10.mask, 0, 0);
    g.globalCompositeOperation = 'source-in';
    const gr = g.createLinearGradient(sx - 140, 0, sx + 140, 0);
    gr.addColorStop(0, 'rgba(216,248,248,0)'); gr.addColorStop(0.5, 'rgba(216,248,248,0.85)'); gr.addColorStop(1, 'rgba(216,248,248,0)');
    g.fillStyle = gr; g.fillRect(0, 0, W, H);
    ctx.save(); ctx.globalCompositeOperation = 'lighter'; ctx.drawImage(S10.sheen, 0, 0); ctx.restore();
  }
  ctx.save(); ctx.beginPath(); ctx.rect(0, ry + 6, W, 200); ctx.clip();
  const tu = E.snap(inv(18.2, 18.6, t));
  text(ctx, 'THE ALL-IN-ONE TRADING TERMINAL', CX, ry + 62 - (1 - tu) * 70, { fam: 'mc', wt: 600, size: 27, track: 0.36, align: 0.5 });
  ctx.restore();
  if (t >= 18.42) {
    const s = '> thetradingdesk.org', vis = typed(s, t, 18.42, 55);
    font(ctx, 'mn', 500, 48, 0.02); const w = tw(ctx, s), x0 = CX - w / 2 - 14, y = ry + 196;
    ctx.fillStyle = rgba(COL.acc, 0.55); ctx.fillText(vis.slice(0, 2), x0, y);
    ctx.fillStyle = COL.hot; ctx.fillText(vis.slice(2), x0 + ctx.measureText('> ').width, y);
    const vw = ctx.measureText(vis).width;
    if (vis.length < s.length || cursorOn(t)) { ctx.fillStyle = COL.acc; ctx.fillRect(x0 + vw + 8, y - 37, 27, 48); }
  }
  text(ctx, 'FREE, ALWAYS  \u00B7  NO LOGIN  \u00B7  OPTIONAL 1:1 SESSIONS', CX, 934, { fam: 'mc', wt: 600, size: 19, track: 0.3, align: 0.5, alpha: 0.6 * P(t, 18.8, 19.2, E.outCubic) });
  ctx.restore();
}

// ══ CHROME: brand bug, mnemonic <GO>, market clock, session bar ═══════════
const CODES = [[0, 'PRE'], [2.0, 'OPEN'], [3.75, 'PTRN'], [6.0, 'OPTN'], [8.0, 'RISK'], [10.5, 'GLOS'], [12.5, 'PATH'], [14.5, 'LOGIN'], [16.5, 'FREE'], [17.5, 'CLOSE']];
function drawChrome(ctx, t) {
  const a = P(t, 1.98, 2.3, E.outCubic); if (a <= 0) return;
  const paper = t >= 8.0 && t < 10.5, col = paper ? COL.print : COL.acc;
  ctx.save(); ctx.globalAlpha = a;
  ctx.strokeStyle = rgba(col, 0.55); ctx.lineWidth = 2;
  const m = 40, L = 22;
  for (const [x, y, sx, sy] of [[m, m, 1, 1], [W - m, m, -1, 1], [m, H - m, 1, -1], [W - m, H - m, -1, -1]]) { ctx.beginPath(); ctx.moveTo(x, y + sy * L); ctx.lineTo(x, y); ctx.lineTo(x + sx * L, y); ctx.stroke(); }
  // brand + mnemonic
  const bw = text(ctx, 'THE TRADING DESK', 72, 78, { fam: 'mc', wt: 700, size: 17, track: 0.3, col });
  let ci = 0; for (let i = 0; i < CODES.length; i++) if (t >= CODES[i][0]) ci = i;
  const [tc, code] = CODES[ci];
  const x1 = 72 + bw + 26;
  ctx.fillStyle = col; ctx.beginPath(); ctx.moveTo(x1, 66); ctx.lineTo(x1 + 9, 72); ctx.lineTo(x1, 78); ctx.fill();
  const cw = text(ctx, code, x1 + 22, 78, { fam: 'mc', wt: 800, size: 17, track: 0.3, col: code === 'LOGIN' ? mixc(col, COL.stop, inv(14.98, 15.02, t)) : col });
  if (code === 'LOGIN' && t > 15.0) { ctx.fillStyle = COL.stop; ctx.fillRect(x1 + 18, 70, (cw + 8) * E.outExpo(inv(15.0, 15.1, t)), 3); }
  const go = t - tc;
  if (go < 0.5 && ci > 0) {
    const on = go < 0.12 || (go > 0.2 && go < 0.32) || go > 0.4;
    font(ctx, 'mc', 800, 15, 0.2); const gw = tw(ctx, '<GO>') + 16, gx = x1 + 22 + cw + 16;
    if (on) { ctx.fillStyle = col; ctx.fillRect(gx, 60, gw, 26); ctx.fillStyle = paper ? COL.paper : COL.ink; ctx.fillText('<GO>', gx + 8, 78); }
  }
  // market clock
  const secs = marketSecs(t), open = t >= 2.0 && t < 17.5;
  const cw2 = ledClock(ctx, hms(secs), W - 150, 82, col);
  text(ctx, 'ET', W - 72, 80, { fam: 'mc', wt: 700, size: 17, track: 0.3, align: 1, col, alpha: 0.7 });
  const st = open ? 'OPEN' : t >= 17.5 ? 'CLOSED' : 'PRE';
  const sx = W - 150 - cw2 - 30;
  text(ctx, st, sx, 80, { fam: 'mc', wt: 800, size: 15, track: 0.3, align: 1, col, alpha: 0.85 });
  font(ctx, 'mc', 800, 15, 0.3); const stw = tw(ctx, st);
  ctx.fillStyle = open ? col : rgba(col, 0.45); ctx.beginPath(); ctx.arc(sx - stw - 16, 74, 5 + (open ? 1.5 * (1 - fract(t / BEAT)) : 0), 0, TAU); ctx.fill();
  // session bar 09:30 → 16:00
  const y = H - 70, bx0 = 72, bx1 = W - 72, prog = clamp((secs - OPEN_S) / (CLOSE_S - OPEN_S));
  ctx.fillStyle = rgba(col, 0.25); ctx.fillRect(bx0, y, bx1 - bx0, 1.5);
  ctx.fillStyle = col; ctx.fillRect(bx0, y - 1, (bx1 - bx0) * prog, 3.5);
  font(ctx, 'mc', 600, 14, 0.2);
  for (let h = 10; h <= 16; h++) {
    const x = lerp(bx0, bx1, (h * 3600 - OPEN_S) / (CLOSE_S - OPEN_S));
    ctx.fillStyle = rgba(col, 0.5); ctx.fillRect(x - 0.75, y - 8, 1.5, 16);
    const lb = String(h).padStart(2, '0') + ':00'; ctx.fillStyle = rgba(col, 0.5); ctx.fillText(lb, h === 16 ? x - tw(ctx, lb) : x - tw(ctx, lb) / 2, y + 30);
  }
  const lb0 = '09:30'; ctx.fillText(lb0, bx0, y + 30);
  ctx.fillStyle = col; ctx.fillRect(bx0 + (bx1 - bx0) * prog - 1.5, y - 12, 3, 24);
  ctx.restore();
}

function ledClock(ctx, s, xr, y, col) {   // right-aligned HH:MM:SS, Doto digits + drawn colons
  font(ctx, 'dot', 900, 34, 0); const dw = tw(ctx, '00'), cg = 16, w = dw * 3 + cg * 2;
  let x = xr - w; ctx.fillStyle = col;
  s.split(':').forEach((pair, i) => {
    ctx.fillText(pair, x, y); x += dw;
    if (i < 2) { for (const dy of [-19, -7]) { ctx.beginPath(); ctx.arc(x + cg / 2, y + dy, 2.6, 0, TAU); ctx.fill(); } x += cg; }
  });
  return w;
}

// ══ post parameters over time ═════════════════════════════════════════════
const HITS = [[1.5, 0.3], [2.0, 1], [3.75, 0.35], [4.0, 0.5], [6.0, 0.4], [8.0, 0.5], [8.375, 0.6], [10.5, 0.6], [11.75, 0.7], [12.5, 0.4], [15.0, 0.8], [15.5, 0.8], [16.0, 0.9], [16.5, 1], [16.75, 0.7], [18.0, 1]];
function postParams(t) {
  const paper = t >= 7.99 && t < 10.49;
  let ca = 0.12;
  for (const [h, k] of HITS) if (t >= h && t < h + 0.5) ca += k * 2.6 * Math.exp(-(t - h) * 16);
  let flash = 0;
  for (const [h, k] of [[2.0, 0.3], [16.5, 0.1], [18.0, 0.22]]) if (t >= h && t < h + 0.3) flash += k * Math.exp(-(t - h) * 34);
  return { paper, bloom: paper ? 0 : 0.6 * (1 - P(t, 7.84, 7.97, E.lin)), ca, flash, vig: paper ? 0.22 : 0.4, grain: paper ? 0.075 : 0.06, scan: paper ? 0 : 0.07 };
}

// ══ assemble ══════════════════════════════════════════════════════════════
function buildAll(ctx) {
  for (const [nm, c] of Object.entries(PAL)) {
    const sx = nm === 'cyan' ? '' : '_' + nm;
    makeDot('lit' + sx, c, c, 0.2, 1);
    makeDot('hot' + sx, mixc(c, '#FFFFFF', 0.85), c, 0.35, 1);
  }
  for (const [nm, col] of Object.entries(PAL)) {
    const S = 128, c = mkCanvas(S, S), g = c.getContext('2d'), r = S / 2, rr = r * 0.42;
    const gr = g.createRadialGradient(r, r, 0, r, r, rr);
    gr.addColorStop(0, rgba(col, 0.05)); gr.addColorStop(0.78, rgba(col, 0.09)); gr.addColorStop(0.94, rgba(col, 0.17)); gr.addColorStop(1, rgba(col, 0));
    g.fillStyle = gr; g.beginPath(); g.arc(r, r, rr, 0, TAU); g.fill(); SPR['off' + (nm === 'cyan' ? '' : '_' + nm)] = c;
  }
  buildS1(); buildS3(); buildS2(ctx); buildS4(); buildS5(ctx); buildS6(ctx); buildS7(); buildS8(); buildS9(ctx); buildS10();
  CUES.sort((a, b) => a.t - b.t);
}
function drawScene(ctx, t) {
  ctx.fillStyle = COL.ink; ctx.fillRect(0, 0, W, H);
  const bg = ctx.createRadialGradient(CX, CY * 0.8, 0, CX, CY * 0.8, W * 0.62);
  bg.addColorStop(0, rgba(COL.glow, 0.95)); bg.addColorStop(1, rgba(COL.glow, 0));
  ctx.fillStyle = bg; ctx.fillRect(0, 0, W, H);
  const ga = P(t, 2.0, 2.6, E.outCubic) * (1 - P(t, 17.1, 17.4, E.lin)) + P(t, 18.2, 19.0, E.outCubic) * 0.7;
  bgGrid(ctx, t, ga, -t * 12, 0);
  drawS1(ctx, t); drawS2(ctx, t); withAcc('green', () => drawS3(ctx, t)); drawS4(ctx, t); drawS5(ctx, t);
  withAcc('gold', () => drawS6(ctx, t)); drawS7(ctx, t); drawS8(ctx, t); drawS9(ctx, t); drawS10(ctx, t);
  drawChrome(ctx, t);
}
