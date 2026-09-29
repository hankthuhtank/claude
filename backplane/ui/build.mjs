// Builds the UI into internal/uiassets/dist, which the Go binary embeds.
import * as esbuild from "esbuild";
import { cpSync, mkdirSync, rmSync, copyFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "internal", "uiassets", "dist");
const watch = process.argv.includes("--watch");

rmSync(out, { recursive: true, force: true });
mkdirSync(join(out, "fonts"), { recursive: true });

// Fonts: only the weights the UI uses (SIL Open Font License 1.1).
const fonts = [
  ["barlow-condensed", ["latin-500-normal", "latin-600-normal", "latin-700-normal"]],
  ["atkinson-hyperlegible-next", ["latin-400-normal", "latin-400-italic", "latin-600-normal", "latin-700-normal", "latin-ext-400-normal", "latin-ext-700-normal"]],
  ["atkinson-hyperlegible-mono", ["latin-400-normal", "latin-600-normal"]],
];
for (const [family, files] of fonts) {
  for (const f of files) {
    copyFileSync(join(here, "node_modules", "@fontsource", family, "files", `${family}-${f}.woff2`), join(out, "fonts", `${family}-${f}.woff2`));
  }
}
if (existsSync(join(here, "public"))) {
  for (const f of readdirSync(join(here, "public"))) {
    if (f !== "fonts") cpSync(join(here, "public", f), join(out, f), { recursive: true });
  }
}

const ctx = await esbuild.context({
  entryPoints: { app: join(here, "src", "main.jsx"), styles: join(here, "src", "styles.css") },
  bundle: true,
  minify: !watch,
  sourcemap: watch ? "inline" : false,
  target: ["chrome120"],
  format: "esm",
  jsx: "automatic",
  jsxImportSource: "preact",
  outdir: out,
  loader: { ".woff2": "file", ".svg": "text" },
  external: ["/fonts/*"],
  legalComments: "none",
  logLevel: "info",
});
if (watch) {
  await ctx.watch();
} else {
  await ctx.rebuild();
  await ctx.dispose();
}
