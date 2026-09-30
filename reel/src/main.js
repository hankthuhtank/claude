'use strict';
// Frame driver: sub-frame accumulation (180° shutter) + canvas-2D post.
const Q = new URLSearchParams(location.search);
const SCALE = parseFloat(Q.get('scale') || '1'), SUB = parseInt(Q.get('sub') || '1', 10), SHUTTER = 0.5;
const RW = Math.round(W * SCALE), RH = Math.round(H * SCALE);
const out = document.getElementById('out'); out.width = RW; out.height = RH;
out.style.width = RW + 'px'; out.style.height = RH + 'px';
const octx = out.getContext('2d', { alpha: false });
const scn = mkCanvas(RW, RH), sctx = scn.getContext('2d', { alpha: false });
const b1 = mkCanvas(Math.round(RW / 4), Math.round(RH / 4)), b1x = b1.getContext('2d');
const b2 = mkCanvas(Math.round(RW / 8), Math.round(RH / 8)), b2x = b2.getContext('2d');
const bt = mkCanvas(Math.round(RW / 4), Math.round(RH / 4)), btx = bt.getContext('2d');
const tmp = mkCanvas(RW, RH), tmpx = tmp.getContext('2d');
const chn = mkCanvas(RW, RH), chx = chn.getContext('2d');
const POST = { grain: [], vig: null, scan: null };

async function loadFonts() {
  const list = [['BS', 'fonts/BigShoulders-opsz72.woff2'], ['MMc', 'fonts/MartianMono-cond.woff2'], ['MMn', 'fonts/MartianMono-norm.woff2'], ['MMw', 'fonts/MartianMono-wide.woff2'], ['DOTO', 'fonts/Doto-round.woff2']];
  for (const [n, u] of list) { const f = new FontFace(n, `url(${u})`, { weight: '100 900' }); await f.load(); document.fonts.add(f); }
}
function buildPost() {
  for (let k = 0; k < 6; k++) {
    const c = mkCanvas(256, 256), g = c.getContext('2d'), id = g.createImageData(256, 256), R = rng(900 + k);
    for (let i = 0; i < id.data.length; i += 4) { const v = 128 + (R() + R() + R() - 1.5) * 150; id.data[i] = id.data[i + 1] = id.data[i + 2] = v; id.data[i + 3] = 255; }
    g.putImageData(id, 0, 0); POST.grain.push(c);
  }
  POST.vig = mkCanvas(RW, RH); const vg = POST.vig.getContext('2d');
  const gr = vg.createRadialGradient(RW / 2, RH / 2, RH * 0.22, RW / 2, RH / 2, RW * 0.66);
  gr.addColorStop(0, '#fff'); gr.addColorStop(0.55, '#e6e6e6'); gr.addColorStop(1, '#303030');
  vg.fillStyle = gr; vg.fillRect(0, 0, RW, RH);
  POST.scan = mkCanvas(1, 3); const sg = POST.scan.getContext('2d');
  sg.fillStyle = '#fff'; sg.fillRect(0, 0, 1, 2); sg.fillStyle = '#9a9a9a'; sg.fillRect(0, 2, 1, 1);
}
function post(t, f) {
  const p = postParams(t);
  octx.save();
  // bloom: two soft layers screened back on top
  if (p.bloom > 0) {
    btx.globalCompositeOperation = 'copy'; btx.filter = 'none'; btx.drawImage(out, 0, 0, bt.width, bt.height);
    b1x.globalCompositeOperation = 'copy'; b1x.filter = `contrast(1.5) blur(${3 * SCALE}px)`; b1x.drawImage(bt, 0, 0);
    b2x.globalCompositeOperation = 'copy'; b2x.filter = `contrast(1.6) blur(${5 * SCALE}px)`; b2x.drawImage(bt, 0, 0, b2.width, b2.height);
    octx.globalCompositeOperation = 'screen';
    octx.globalAlpha = p.bloom * 0.75; octx.drawImage(b1, 0, 0, RW, RH);
    octx.globalAlpha = p.bloom * 0.6; octx.drawImage(b2, 0, 0, RW, RH);
  }
  // chromatic aberration (radial RGB split), only when it matters
  if (p.ca > 0.06) {
    const k = p.ca * 0.0016;
    tmpx.globalCompositeOperation = 'copy'; tmpx.drawImage(out, 0, 0);
    octx.globalAlpha = 1; octx.globalCompositeOperation = 'copy'; octx.fillStyle = '#000'; octx.fillRect(0, 0, RW, RH);
    const chans = [['#ff0000', 1 + 2 * k], ['#00ff00', 1 + k], ['#0000ff', 1]];
    for (const [c, s] of chans) {
      chx.globalCompositeOperation = 'copy'; chx.drawImage(tmp, RW / 2 - (RW * s) / 2, RH / 2 - (RH * s) / 2, RW * s, RH * s);
      chx.globalCompositeOperation = 'multiply'; chx.fillStyle = c; chx.fillRect(0, 0, RW, RH);
      octx.globalCompositeOperation = 'lighter'; octx.drawImage(chn, 0, 0);
    }
  }
  if (p.flash > 0.002) { octx.globalCompositeOperation = 'screen'; octx.globalAlpha = p.flash; octx.fillStyle = COL.hot; octx.fillRect(0, 0, RW, RH); }
  if (p.scan > 0) { octx.globalCompositeOperation = 'multiply'; octx.globalAlpha = p.scan * 2.2; octx.fillStyle = octx.createPattern(POST.scan, 'repeat'); octx.fillRect(0, 0, RW, RH); }
  octx.globalCompositeOperation = 'multiply'; octx.globalAlpha = p.vig; octx.drawImage(POST.vig, 0, 0);
  const R = rng(f * 7919 + 17), tile = POST.grain[f % POST.grain.length];
  const pat = octx.createPattern(tile, 'repeat'); pat.setTransform(new DOMMatrix().translate(Math.floor(R() * 256), Math.floor(R() * 256)).scale(Math.max(1, SCALE), Math.max(1, SCALE)));
  octx.globalCompositeOperation = 'overlay'; octx.globalAlpha = p.grain; octx.fillStyle = pat; octx.fillRect(0, 0, RW, RH);
  octx.restore();
}
// windows of extreme motion get 4x the samples so fast blur stays continuous
const FAST = [[1.99, 2.16], [3.8, 4.02], [7.8, 8.02], [10.28, 10.52], [14.34, 14.62], [17.15, 17.46]];
window.renderFrame = f => {
  const tf = f / FPS, n = SUB > 1 && FAST.some(([a, b]) => tf >= a && tf <= b) ? SUB * 4 : SUB;
  for (let i = 0; i < n; i++) {
    const off = n > 1 ? ((i + 0.5) / n) * SHUTTER : 0;   // shutter opens on the frame: cuts stay clean
    const t = Math.max(0, (f + off) / FPS);
    sctx.setTransform(SCALE, 0, 0, SCALE, 0, 0);
    sctx.globalAlpha = 1; sctx.globalCompositeOperation = 'source-over'; sctx.filter = 'none';
    drawScene(sctx, t);
    octx.globalCompositeOperation = 'source-over'; octx.globalAlpha = 1 / (i + 1);  // running mean
    octx.drawImage(scn, 0, 0);
  }
  octx.globalAlpha = 1;
  post(f / FPS, f);
  return true;
};
window.exportCues = () => CUES;
(async () => {
  await loadFonts();
  const mctx = mkCanvas(8, 8).getContext('2d');
  buildAll(mctx); buildPost();
  window.READY = true;
})().catch(e => { window.ERR = String(e && e.stack || e); });
