# Backplane — fix list (audit of 1.0.0, 2026-09-29)

Found by running the installed 1.0.0 build with a throwaway data folder and
clicking through it, then tracing each issue to the source. Owner-reported
issues are marked ★.

Suggested order: **Round 1** (fixes + plain words) → **Round 2** (products & prices).

---

## Round 1 — Fix

### ★ 1. Stopping practice mode leaves "on" state everywhere else
Repro: Bench → Start the sandbox → build a practice Software Store → Stop sandbox.
The Bench says stopped, but:

- **Accounts** still lists all 5 practice connections as "Connected · verified
  N min ago" with a green rail badge. `StopPractice` never touches connection
  status, and `StartPractice` saves them as `ok`, so the stale status is also
  persisted in `connections.json` and survives an app restart.
  → `internal/app/practice.go` `StopPractice` (line ~138). On stop (and on app
  start when the sandbox isn't running) mark practice connections as offline /
  "Sandbox off" (grey, not green). Exclude practice connections from the
  Accounts rail badge while the sandbox is off (`ui/src/main.jsx` ~45).
- **Practice backends** still show "monitored, next check <time>". The monitor
  already skips them (`internal/app/monitor.go:86`), but `status()` still
  computes `NextQuick`/`NextFull` (`monitor.go` ~233–247). Don't set them when
  `p.Practice && !practiceRunning()`; show the existing note instead.
- **Register** logs "Practice sandbox started" but never logs a stop, so the
  newest entry always says it's on. Add `a.log("info", …, "Practice sandbox
  stopped — practice backends were reset.")` in `StopPractice`.

### ★ 2. Preset pricing questions are wrong or misleading
- `internal/blueprints/catalog.go:145` — `product_name` defaults to `"My App"`
  for every commerce preset. If left alone, customers are charged for "My App"
  (a practice build did exactly this). Remove the default; keep it required;
  give each preset a placeholder instead.
- **E-commerce Store** (physical goods) inherits `commonCommerce`, so it shows
  "One-time price customers pay", Download link lifetime and Downloads allowed
  per purchase. Give it its own question list without download fields.
- **Data-engine presets** (`internal/blueprints/data.go` ~137, ~388): Restaurant,
  Booking, Marketplace ask one unexplained "Price"; Membership / Subscription
  SaaS ask one "Monthly price" (Subscription SaaS's summary promises "monthly or
  yearly plans"). Until Round 2, label and explain what the price is for
  (e.g. "Booking deposit", "Membership price per month") or hide it where it
  isn't meaningful (Restaurant, Marketplace).

### 3. "Optional" upload turns a fresh build yellow
Software Store says the product file is "Optional now — you can upload it
later", but the post-build full check reports NEEDS ATTENTION because
`releases/<slug>/product.bin` isn't uploaded. Show it as a to-do on the
backend page instead of a health problem, or stop calling it optional.

### 4. Practice backends look like production
Practice builds show a red **PRODUCTION** badge and real monthly cost
estimates (Cloudflare $0–5, Supabase $25…). Show a "Practice" badge and
"Free — simulated" instead of the cost table.

### 5. Duplicate check right after build
A scheduled full check started 18 s after the post-build full check. Seed the
monitor's last-full time from the after-build check.

---

## Round 1 — Simplify (plain words)

The rack theme can stay in the visuals; labels shouldn't need decoding.

| Now | Suggested |
| --- | --- |
| Rack (tab) | Overview |
| Register | Activity |
| Bench | Practice (also clashes with the "Bench" theme in Settings) |
| Certify / Certify it now | Run full check |
| Put it on the rack | Create backend |
| Work order | Plan |
| Complete setup / Foundation | Ready to sell / Backend only — you bring the app |

Also:
- **Health tab** lists all 63 results. Lead with problems; collapse passes
  behind "Show all 63 checks".
- **Register/Activity filter** lists 18 services incl. ones never connected.
  Only list providers that are connected or used by a backend.

---

## Round 2 — Upgrade: products & prices ★

Today every deployed backend holds exactly one Stripe price at quantity 1:
- commerce worker: `line_items: [{ price: env.PRICE_ID, quantity: 1 }]`
  (`internal/blueprints/assets/commerce/worker.js`)
- data worker `billingCheckout`: same single `env.PRICE_ID`
  (`internal/blueprints/assets/data/worker.js`)

Plan:
1. Form: a repeatable "Products & prices" list — name, price, billing
   (one-time / monthly / yearly). Subscription presets: several plans, each
   with optional monthly + yearly price.
2. Build: create one Stripe product + price(s) per item; store the catalog
   (Supabase `products` table or a Worker env JSON of `{key → price_id}`).
3. Worker checkout: accept an item key (and quantity / multiple line items for
   a cart). Map keys to price IDs **server-side only** — never accept an amount
   or raw price ID from the browser.
4. Backend page: a **Products** tab to add items or change prices after the
   build (Stripe prices are immutable: create new price, switch, deactivate old).
5. Checks + simulator (`internal/engine/checks.go`, `internal/sim/stripe.go`)
   verify every price is active; extend the e2e tests in
   `internal/app/e2e_test.go`.

Only 5 of 25 presets are "full" (all single-product stores). After Round 2,
Booking and Restaurant are the obvious next "Ready to sell" presets.

---

## Working without local installs

The owner prefers nothing extra installed on their PC. Build, test and
package in the cloud:
- Go 1.26+ and Node 22+ (see README). `go test ./...` takes ~4 minutes.
- Consider a GitHub Actions workflow (repo root `.github/workflows/`, working
  directory `backplane/`) that runs `go vet` + `go test` and then
  `packaging/build-windows.sh <version>` on ubuntu-latest (`apt install nsis
  zip`, `go install github.com/tc-hib/go-winres@latest`), uploading
  `dist/Backplane-<version>-Windows.zip` as a build artifact the owner can
  download and install.
- After UI changes run `cd ui && npm ci && npm run build` — the bundle in
  `internal/uiassets/dist` is committed and embedded in the exe.
