# Backplane

A Windows desktop app that sets up and monitors a small business's backend
from one place: payments (Stripe), database and sign-in (Supabase), server
code, storage and DNS (Cloudflare), email (Resend) and the code repository
(GitHub). You describe the business or pick one of 25 presets, review a
plain-English work order, approve it, and Backplane builds everything in
dependency order, then proves it works with six levels of checks and keeps
watching it.

Safi Solutions · https://www.safisolutions.org

## How it's put together

One Go executable. It serves the UI from memory on a loopback-only port
(random port, per-launch token, Host/Origin checks, strict CSP) and shows it
in a WebView2 window. Without WebView2 it falls back to the default browser.

| Path | What's there |
| --- | --- |
| `cmd/backplane` | Entry point, window host (WebView2 on Windows), single-instance handling |
| `internal/core` | Blueprint, manifest, plan/run and health models |
| `internal/blueprints` | The 25 presets, their questions, and generated code (Worker, SQL, emails, Wrangler config) |
| `internal/engine` | Planner, executor (checkpoints, retries, resume, rollback), six-level verifier, drift, repairs |
| `internal/providers` | Adapters for Cloudflare, Supabase, Stripe, Resend, GitHub, plus connect-level add-ons |
| `internal/sim` | In-memory versions of those APIs for the practice sandbox and tests |
| `internal/app` | The RPC surface the UI calls, monitoring, import/export, Windows integration |
| `internal/server` | Loopback HTTP server, event stream, static UI |
| `internal/vault`, `internal/store` | Encrypted credential vault (DPAPI on Windows), JSON data store |
| `ui/` | Preact UI source; `npm run build` writes `internal/uiassets/dist` (committed) |
| `packaging/` | Icon, exe resources, NSIS installer and uninstaller, release docs, build script |

Data lives in `%APPDATA%\Backplane`; the WebView2 cache in
`%LOCALAPPDATA%\Backplane\WebView2`.

## Develop

Needs Go 1.26+ and Node 22+.

```sh
cd ui && npm ci && npm run build && cd ..   # UI bundle → internal/uiassets/dist
go run ./cmd/backplane --serve              # prints the URL to open
go run ./cmd/backplane --serve --data /tmp/bp   # throwaway data folder
```

The **Bench** (practice sandbox) runs every provider in-memory, so the whole
build → certify → break → repair loop works offline with no accounts.

Other flags: `--browser` (open in the default browser instead of a window),
`--check-all` (quick check of every monitored backend, then exit; used by the
optional hourly Windows task).

## Test

```sh
go vet ./... && GOOS=windows go vet ./...
go test ./...        # about 4 minutes
```

The end-to-end tests build the Software Store (downloads) and E-commerce
Store (shipping) presets in the sandbox, run full certification to FULLY
OPERATIONAL, and run twelve break → detect → repair cases (disabled or moved
webhook, removed binding or secret, RLS turned off, revoked database key,
deleted bucket or Worker, paused project, rotated webhook secret, and more).
Every preset is also checked for a valid, complete Wrangler config.

## Release (Windows)

```sh
go install github.com/tc-hib/go-winres@latest
sudo apt install nsis zip        # or: brew install makensis
packaging/build-windows.sh 1.0.0
```

Output in `dist/` (git-ignored):

```
Backplane-1.0.0-Windows.zip
Backplane-1.0.0-Windows/
  Install Backplane.exe        per-user install, no admin, 64-bit
  Uninstall Backplane.exe      runs the installed uninstaller (or cleans up leftovers)
  READ ME FIRST.txt
  Guide/Backplane Guide.html   offline guide, fonts embedded
  Legal/LICENSE.txt, THIRD-PARTY-NOTICES.txt   notices generated from the modules in the exe
  SHA256SUMS.txt
```

The installer puts Backplane in `%LOCALAPPDATA%\Programs\Backplane`, adds a
Start menu shortcut (desktop shortcut optional), registers in Apps, and offers
Microsoft's WebView2 download if it's missing. The uninstaller removes the
program, shortcuts, the `Backplane Monitor` task and the notification
registration; the user's data is deleted only if they tick the box, and
exports in `Downloads\Backplane exports` are never touched.

Artwork (icon sizes, installer panels) is committed as PNG. After editing
`packaging/icon/*.svg` or the panel designs, regenerate with
`npx -y -p playwright node packaging/art/render.mjs`.

The exe is not code-signed yet, so SmartScreen shows "Windows protected your
PC" until it earns reputation; the read-me explains More info → Run anyway.

## Design

"The Rack": a backend is a rack of gear. Each service is a faceplate with
silkscreen labels (Barlow Condensed) and status LEDs, links are patch cords,
and health checks read like a cable certifier. Body text is Atkinson
Hyperlegible Next, code is Atkinson Hyperlegible Mono. Themes are materials:
Rack, Aluminum, Bench, Copper, Fiber, High contrast, and System. Status
colours are fixed across themes and always paired with an icon and a word.
The signature interaction is **Trace**: click a unit or a cord and a tone
runs downstream along the path, stopping at the first fault.
