// Renders Backplane's artwork with the app's own fonts:
//   packaging/icon/icon-<size>.png       app icon (small sizes use icon-small.svg)
//   packaging/installer/welcome.png      installer welcome/finish panel (2x)
//   packaging/installer/header.png       installer header strip (2x)
// The PNGs are committed; run this only after changing the artwork:
//   npx -y -p playwright node packaging/art/render.mjs
// Set CHROMIUM=/path/to/chromium to use an existing browser.
import { chromium } from "playwright";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const pkg = join(here, "..");
const F = join(pkg, "..", "internal", "uiassets", "dist", "fonts") + "/";
const font = (fam, w, file) => `@font-face{font-family:"${fam}";font-weight:${w};src:url(data:font/woff2;base64,${readFileSync(F + file).toString("base64")}) format("woff2")}`;
const fonts = [font("Barlow Condensed", 600, "barlow-condensed-latin-600-normal.woff2"), font("Barlow Condensed", 700, "barlow-condensed-latin-700-normal.woff2"),
  font("Atkinson Next", 400, "atkinson-hyperlegible-next-latin-400-normal.woff2"), font("Atkinson Next", 700, "atkinson-hyperlegible-next-latin-700-normal.woff2")].join("");
const base = `*{box-sizing:border-box;margin:0}body{background:#121416;overflow:hidden}.silk{font-family:"Barlow Condensed";font-weight:600;letter-spacing:.14em;text-transform:uppercase;color:#a9aca8}`;

// Welcome / finish panel: 164 x 314 (rendered at 2x).
const units = ["Payments", "Database", "Email", "Hosting", "Monitor"];
const uy = (i) => 26 + i * 38;
let plates = "";
units.forEach((u, i) => {
  plates += `<div style="position:absolute;left:14px;top:${uy(i)}px;width:136px;height:30px;border-radius:3px;background:linear-gradient(#2b3034,#23272b);box-shadow:inset 0 1px 0 rgb(255 255 255 / .06),0 2px 4px rgb(0 0 0 / .5)">
    <i style="position:absolute;left:5px;top:12px;width:5px;height:5px;border-radius:50%;background:#0b0c0d"></i>
    <i style="position:absolute;right:5px;top:12px;width:5px;height:5px;border-radius:50%;background:#0b0c0d"></i>
    <span class="silk" style="position:absolute;left:16px;top:8px;font-size:10.5px">${u}</span>
    <i style="position:absolute;left:88px;top:11px;width:7px;height:7px;border-radius:50%;background:#19c219;box-shadow:0 0 6px 1px rgb(25 194 25 / .75)"></i>
    <i style="position:absolute;left:102px;top:10px;width:10px;height:10px;border-radius:2px;background:#0b0c0d;box-shadow:inset 0 0 0 2px #3b4045"></i>
  </div>`;
});
const jack = (i) => ({ x: 14 + 107, y: uy(i) + 15 });
const cord = (a, b, color, bow) => { const p = jack(a), q = jack(b); return `<path d="M${p.x} ${p.y} C ${p.x + bow} ${p.y + 6}, ${q.x + bow} ${q.y - 6}, ${q.x} ${q.y}" fill="none" stroke="${color}" stroke-width="3.2" stroke-linecap="round"/>`; };
const rail = (x) => `<div style="position:absolute;left:${x}px;top:0;width:10px;height:314px;background:#1a1d20;border-left:1px solid #24282c;border-right:1px solid #0c0d0e">${Array.from({ length: 21 }, (_, k) => `<i style="position:absolute;left:2.5px;top:${6 + k * 15}px;width:5px;height:5px;background:#0b0c0d"></i>`).join("")}</div>`;
const welcome = `<html><head><style>${fonts}${base}</style></head><body style="width:164px;height:314px;position:relative;background:linear-gradient(#15181a,#0f1112)">
  ${rail(2)}${rail(152)}
  ${plates}
  <svg width="164" height="314" style="position:absolute;left:0;top:0">
    <g opacity=".95">${cord(0, 2, "#2cc4d3", 30)}${cord(1, 3, "#6b737b", 22)}${cord(3, 4, "#2cc4d3", 16)}</g>
  </svg>
  <div style="position:absolute;left:18px;top:214px;right:18px">
    <div style="height:2px;background:#2cc4d3;width:34px;margin-bottom:9px"></div>
    <div style="font-family:'Barlow Condensed';font-weight:700;font-size:27px;letter-spacing:.06em;color:#eceae4;line-height:1">BACKPLANE</div>
    <div style="font-family:'Atkinson Next';font-size:10.5px;color:#a9aca8;margin-top:6px;line-height:1.3">Your whole backend,<br>on one rack.</div>
    <div class="silk" style="font-size:9px;margin-top:10px;color:#8a8e8b">Safi Solutions</div>
  </div>
</body></html>`;

// Header strip: 150 x 57.
const header = `<html><head><style>${fonts}${base}</style></head><body style="width:150px;height:57px;position:relative;background:#121416">
  <div style="position:absolute;left:6px;top:8px;width:138px;height:41px;border-radius:3px;background:linear-gradient(#2b3034,#23272b);box-shadow:inset 0 1px 0 rgb(255 255 255 / .06)">
    <i style="position:absolute;left:5px;top:18px;width:5px;height:5px;border-radius:50%;background:#0b0c0d"></i>
    <i style="position:absolute;right:5px;top:18px;width:5px;height:5px;border-radius:50%;background:#0b0c0d"></i>
    <div style="position:absolute;left:17px;top:12px;font-family:'Barlow Condensed';font-weight:700;font-size:16px;letter-spacing:.05em;color:#eceae4;line-height:1">BACKPLANE</div>
    <i style="position:absolute;left:104px;top:17px;width:7px;height:7px;border-radius:50%;background:#19c219;box-shadow:0 0 6px 1px rgb(25 194 25 / .75)"></i>
    <i style="position:absolute;left:117px;top:15px;width:11px;height:11px;border-radius:2px;background:#0b0c0d;box-shadow:inset 0 0 0 2px #3b4045"></i>
  </div>
  <svg width="150" height="57" style="position:absolute;left:0;top:0"><path d="M122.5 20.5 C 151 24, 147 50, 129 57" fill="none" stroke="#2cc4d3" stroke-width="3" stroke-linecap="round"/></svg>
</body></html>`;

const browser = await chromium.launch(process.env.CHROMIUM ? { executablePath: process.env.CHROMIUM } : {});

// App icon: detailed artwork from 32 px up, the simplified plate below.
const big = readFileSync(join(pkg, "icon", "icon.svg"), "utf8");
const small = readFileSync(join(pkg, "icon", "icon-small.svg"), "utf8");
const iconPage = await browser.newPage({ viewport: { width: 300, height: 300 }, deviceScaleFactor: 1 });
for (const s of [16, 20, 24, 32, 40, 48, 64, 128, 256]) {
  const svg = Buffer.from(s <= 24 ? small : big).toString("base64");
  await iconPage.setContent(`<html><body style="margin:0;background:transparent"><img id=i src="data:image/svg+xml;base64,${svg}" width="${s}" height="${s}" style="display:block"></body></html>`);
  await iconPage.waitForTimeout(100);
  await iconPage.locator("#i").screenshot({ path: join(pkg, "icon", `icon-${s}.png`), omitBackground: true });
}
await iconPage.close();

// Installer artwork at 2x, so it stays sharp on high-DPI screens.
for (const [name, html, w, h] of [["welcome", welcome, 164, 314], ["header", header, 150, 57]]) {
  const page = await browser.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: 2 });
  await page.setContent(html);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(200);
  await page.screenshot({ path: join(pkg, "installer", `${name}.png`) });
  await page.close();
}
await browser.close();
console.log("artwork rendered");
