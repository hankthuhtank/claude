# Backplane changelog

## 1.1.0 — 2026-09-29

Everything from the 1.0.0 audit (owner-reported issues marked ★), plus the
products & prices upgrade.

### Fixed

- ★ **Stopping practice mode now switches everything off.** Practice
  connections show as *Off* (grey, not "Connected") on Accounts, in the rail
  badge and in the saved data — also after a restart. Practice backends'
  scheduled checks pause and the page says so instead of showing a next-check
  time. The Activity log records the stop. Practice credentials are never sent
  anywhere while practice mode is off.
- ★ **Preset forms don't name your product.** No "My App" default: what you
  sell starts as an empty row you must fill in. The E-commerce Store (physical
  goods) no longer asks about download links or download limits, and asks
  which countries you ship to.
- **Skipping the optional file upload no longer turns health yellow.** A
  fresh build with no product file is FULLY OPERATIONAL, with "Upload the file
  customers buy" on the backend's to-do list.
- **Practice backends look like practice.** A dashed *Practice* stamp instead
  of a red PRODUCTION one, and "Free — simulated" instead of real monthly cost
  estimates. No typed confirmation for practice teardowns.
- **No duplicate check right after a build.** The after-build full check now
  counts for the schedule.
- **A full check can't undo a build.** A check still running when a build
  finished used to save its older copy of the backend's records over the
  build's (seen as a price change that "didn't stick"). Checks now record
  only their own result.
- Stripe product/price records no longer keep stale values after an update.

### Added — products & prices ★

- Every preset that takes payments sells a **list of items**: name, price and
  one-time, monthly or yearly billing. Plans can offer monthly plus an optional
  yearly price; bookable services have a deposit and a length.
- Each item becomes a Stripe product with one price per billing option. The
  Worker gets a catalog mapping item keys to those prices.
- **Checkout takes an item key and a quantity** (JSON, form post or a plain
  link like `/checkout?item=candle`). Prices are resolved on the server; any
  amount or price sent by a browser is ignored, unknown items and bad
  quantities are refused. Physical-goods stores and restaurants accept a cart.
  `GET /products` lists what's for sale for your site's pricing table or menu.
- Digital items sold monthly or yearly check out as subscriptions; a canceled
  subscription revokes the customer's downloads and license keys.
- **Products tab** on every selling backend: edit items and prices, see which
  prices are live in Stripe, then review the plan. Stripe prices can't be
  edited, so Backplane creates the new price first, switches the Worker, then
  retires the old one — checkout never goes without a price.
- Health checks prove it: checkout refuses an unknown item, charges the
  catalog price even when the request carries its own, and every price is
  checked for being active.

### Added — two more Ready-to-sell presets

- **Booking System**: customers pick a service and time and pay a deposit
  (no account needed). The slot is held for 30 minutes while they pay, double
  bookings are refused, unpaid slots are released, a confirmation goes out and
  a reminder the day before. Staff see every booking.
- **Restaurant Ordering**: a menu cart at menu prices, sold-out items refused,
  paid orders appear on the live kitchen feed, confirmation email.
- Both get an end-to-end check (order → hold → payment → confirmed → email →
  a retried payment event ignored), with test data removed afterwards.

### Simpler

- Plain words: Rack tab → **Overview**, Register → **Activity**, Bench →
  **Practice** (no more clash with the Bench theme), Certify → **Full check**,
  Put it on the rack → **Create backend**, Work order → **Plan**, Complete
  setup / Foundation → **Ready to sell** / **Backend only — you bring the
  app**. The look is unchanged.
- **Health tab** leads with what needs a look; passing checks fold behind
  "Show all N checks".
- **Plans** separate real changes from connected parts that are only
  re-checked ("3 changes to make · 4 connected parts re-checked").
- **Activity filter** lists only services you've connected or use.
- The plain-English describer fills the products list from a price it hears.
- A **to-do list** on the Overview tab for things left for later on purpose.

### Build

- GitHub Actions workflow (`.github/workflows/backplane.yml`): vet and tests on
  every push, plus the Windows installer zip as a downloadable artifact.

## 1.0.0 — 2026-09-28

First release.
