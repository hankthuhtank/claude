// Deterministic frame capture: node render.cjs --scale 1 --sub 8 --workers 3 --out frames/
//   --frames 0,60,120   render only these frames      --cues cues.json  dump sound cues
const { chromium } = require('playwright');
const http = require('http'), fs = require('fs'), path = require('path');
const arg = (k, d) => { const i = process.argv.indexOf('--' + k); return i > 0 ? process.argv[i + 1] : d; };
const VERT = process.argv.includes('--vertical'), SCALE = +arg('scale', 1), SUB = +arg('sub', 8), WORKERS = +arg('workers', 3), OUT = path.resolve(arg('out', 'frames'));
const FROM = +arg('from', 0), TO = +arg('to', 1199), STEP = +arg('step', 1), CUESF = arg('cues', null);
const list = arg('frames', null) ? arg('frames').split(',').map(Number) : Array.from({ length: Math.floor((TO - FROM) / STEP) + 1 }, (_, i) => FROM + i * STEP);
const root = path.join(__dirname, 'src');
const types = { '.html': 'text/html', '.js': 'text/javascript', '.woff2': 'font/woff2', '.png': 'image/png' };
const srv = http.createServer((q, r) => {
  const p = path.join(root, decodeURIComponent(q.url.split('?')[0]));
  fs.readFile(p, (e, d) => { if (e) { r.writeHead(404); r.end(); return; } r.writeHead(200, { 'Content-Type': types[path.extname(p)] || 'application/octet-stream' }); r.end(d); });
});
srv.listen(0, async () => {
  const port = srv.address().port, W = Math.round((VERT ? 1080 : 1920) * SCALE), H = Math.round((VERT ? 1920 : 1080) * SCALE);
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch({ args: ['--disable-gpu', '--font-render-hinting=none', '--disable-lcd-text'] });
  const t0 = Date.now(); let done = 0;
  const chunks = Array.from({ length: WORKERS }, (_, w) => list.filter((_, i) => i % WORKERS === w));
  await Promise.all(chunks.map(async (frames, w) => {
    if (!frames.length) return;
    const page = await browser.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: 1 });
    page.on('pageerror', e => console.error('pageerror:', e.message));
    page.on('console', m => { if (m.type() === 'error') console.error('console:', m.text()); });
    await page.goto(`http://127.0.0.1:${port}/index.html?scale=${SCALE}&sub=${SUB}${VERT ? '&v=1' : ''}`);
    await page.waitForFunction(() => window.READY || window.ERR, null, { timeout: 60000 });
    const err = await page.evaluate(() => window.ERR); if (err) throw new Error(err);
    if (CUESF && w === 0) fs.writeFileSync(path.resolve(CUESF), JSON.stringify(await page.evaluate(() => exportCues()), null, 0));
    const cdp = await page.context().newCDPSession(page);
    for (const f of frames) {
      await page.evaluate(f => renderFrame(f), f);
      const r = await cdp.send('Page.captureScreenshot', { format: 'png', optimizeForSpeed: true, clip: { x: 0, y: 0, width: W, height: H, scale: 1 } });
      fs.writeFileSync(path.join(OUT, `f${String(f).padStart(4, '0')}.png`), Buffer.from(r.data, 'base64'));
      if (++done % 50 === 0) console.log(`${done}/${list.length} frames, ${((Date.now() - t0) / done).toFixed(0)} ms/frame avg`);
    }
  }));
  console.log(`rendered ${list.length} frames in ${((Date.now() - t0) / 1000).toFixed(1)}s`);
  await browser.close(); srv.close();
});
