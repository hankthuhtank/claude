// Runs the generated Cloudflare Workers (the real JavaScript that ships) with
// mocked Stripe, Supabase and Resend, so the code customers hit is tested
// directly — not only through the simulator's Go mirror.
//
//   node --test internal/blueprints/testdata/
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const here = (p) => fileURLToPath(new URL(p, import.meta.url));

// Loads a Worker module from its source (it's plain ESM).
async function loadWorker(name) {
  const src = readFileSync(here(`../assets/${name}/worker.js`), "utf8");
  return (await import("data:text/javascript;base64," + Buffer.from(src).toString("base64"))).default;
}

const commerce = await loadWorker("commerce");
const data = await loadWorker("data");

const CATALOG = JSON.stringify({
  currency: "usd",
  items: [
    { key: "candle", name: "Candle", billing: "once", prices: { once: "price_candle" }, amounts: { once: 2400 } },
    { key: "soap", name: "Soap", billing: "once", prices: { once: "price_soap" }, amounts: { once: 850 } },
    { key: "pro", name: "Pro", billing: "month", prices: { month: "price_pro_m", year: "price_pro_y" }, amounts: { month: 900, year: 9000 } },
  ],
});

const ctx = { waitUntil() {} };

// A fetch mock that records calls and answers by URL.
function mockFetch(routes) {
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const u = String(url);
    const body = init.body === undefined ? undefined : String(init.body);
    calls.push({ url: u, method: init.method || "GET", body, headers: init.headers || {} });
    for (const [re, fn] of routes) {
      if (re.test(u)) {
        const out = await fn(u, init, body);
        return new Response(JSON.stringify(out.body ?? out), { status: out.status || 200, headers: { "content-type": "application/json" } });
      }
    }
    return new Response("[]", { status: 200 });
  };
  return calls;
}

const stripeSession = [/api\.stripe\.com\/v1\/checkout\/sessions$/, () => ({ id: "cs_test_1", url: "https://checkout.stripe.com/c/pay/cs_test_1" })];

function commerceEnv(extra = {}) {
  return { CATALOG, MAX_QUANTITY: "20", CART: "true", STRIPE_SECRET_KEY: "sk_test_x", PRODUCT_NAME: "Shop", FULFILLMENT_MODE: "shipping", SHIP_COUNTRIES: "US,CA", ...extra };
}

const post = (path, body, headers = {}) =>
  new Request("https://api.example.workers.dev" + path, { method: "POST", headers: { "Content-Type": "application/json", Accept: "application/json", ...headers }, body: JSON.stringify(body) });

test("checkout maps item keys to catalog prices and ignores browser prices", async () => {
  const calls = mockFetch([stripeSession]);
  const res = await commerce.fetch(post("/checkout", { items: [{ key: "candle", quantity: 2, price: "price_evil", amount: 1 }, { key: "soap", quantity: 1 }], amount: 1 }), commerceEnv(), ctx);
  assert.equal(res.status, 200);
  const form = new URLSearchParams(calls[0].body);
  assert.equal(form.get("mode"), "payment");
  assert.equal(form.get("line_items[0][price]"), "price_candle");
  assert.equal(form.get("line_items[0][quantity]"), "2");
  assert.equal(form.get("line_items[1][price]"), "price_soap");
  assert.equal(form.get("metadata[item_keys]"), "candle:2,soap:1");
  assert.deepEqual(form.getAll("shipping_address_collection[allowed_countries][]"), ["US", "CA"]);
  for (const [k, v] of form) assert.ok(!/amount/.test(k) && v !== "price_evil", `browser value leaked: ${k}=${v}`);
});

test("checkout refuses unknown items and bad quantities", async () => {
  mockFetch([stripeSession]);
  for (const body of [
    { items: [{ key: "nope" }] },
    { items: [{ key: "candle", quantity: 21 }] },
    { items: [{ key: "candle", quantity: 1.5 }] },
    { items: [{ key: "candle", quantity: 0 }] },
    { items: [{ key: "candle" }], interval: "month" },
  ]) {
    const res = await commerce.fetch(post("/checkout", body), commerceEnv(), ctx);
    assert.equal(res.status, 400, JSON.stringify(body));
  }
  // Digital stores: one item per checkout.
  const res = await commerce.fetch(post("/checkout", { items: [{ key: "candle" }, { key: "soap" }] }), commerceEnv({ CART: "false", MAX_QUANTITY: "1" }), ctx);
  assert.equal(res.status, 400);
});

test("a buy link works and subscriptions check out in subscription mode", async () => {
  const calls = mockFetch([stripeSession]);
  const res = await commerce.fetch(new Request("https://api.example.workers.dev/checkout?item=pro&interval=year"), commerceEnv({ FULFILLMENT_MODE: "download" }), ctx);
  assert.equal(res.status, 303);
  const form = new URLSearchParams(calls[0].body);
  assert.equal(form.get("mode"), "subscription");
  assert.equal(form.get("line_items[0][price]"), "price_pro_y");
  assert.equal(form.get("customer_creation"), null, "customer_creation is payment-mode only");
});

test("/products lists items and amounts but no Stripe ids", async () => {
  const res = await commerce.fetch(new Request("https://api.example.workers.dev/products"), commerceEnv(), ctx);
  const text = await res.text();
  assert.ok(!text.includes("price_candle"));
  const out = JSON.parse(text);
  assert.equal(out.items.length, 3);
  assert.equal(out.items[0].prices.once.amount, 2400);
  assert.equal(out.cart, true);
});

test("a shipping store's health check doesn't ask for a download bucket", async () => {
  mockFetch([[/\/v1\/prices\//, (u) => ({ id: u.split("/").pop(), active: true, livemode: false })], [/rest\/v1\//, () => []], [/resend/, () => ({ status: 401, body: { name: "restricted_api_key" } })]]);
  const env = commerceEnv({ BACKPLANE_PROBE_TOKEN: "tok", SUPABASE_URL: "https://db.example", SUPABASE_SECRET_KEY: "sb_secret_x", STRIPE_WEBHOOK_SECRET: "whsec", RESEND_API_KEY: "re_x",
    FROM_EMAIL: "a@b.c", DOWNLOAD_SIGNING_SECRET: "s", PROBES: { get: async () => null, put: async () => {}, delete: async () => {} } });
  const res = await commerce.fetch(new Request("https://api.example.workers.dev/__backplane/health", { headers: { Authorization: "Bearer tok" } }), env, ctx);
  const out = await res.json();
  assert.deepEqual(out.missing, []);
  assert.equal(out.checks.r2, undefined);
  assert.equal(out.checks.stripe.ok, true, JSON.stringify(out.checks.stripe));
  assert.equal(out.ok, true, JSON.stringify(out));
});

test("a 1.0.0 configuration (PRICE_ID, no CATALOG) still sells", async () => {
  const calls = mockFetch([stripeSession]);
  const env = commerceEnv({ CATALOG: "", PRICE_ID: "price_old", FULFILLMENT_MODE: "download" });
  const res = await commerce.fetch(post("/checkout", {}), env, ctx);
  assert.equal(res.status, 200);
  assert.equal(new URLSearchParams(calls[0].body).get("line_items[0][price]"), "price_old");
});

// ---- data Worker: guest bookings and orders ----

const services = JSON.stringify({ currency: "usd", items: [{ key: "haircut", name: "Haircut", billing: "once", minutes: 45, prices: { once: "price_cut" }, amounts: { once: 1000 } }] });
const bookingEnv = { TEMPLATE: "booking", CATALOG: services, MAX_QUANTITY: "1", CART: "false", STRIPE_SECRET_KEY: "sk_test_x", SUPABASE_URL: "https://db.example", SUPABASE_SECRET_KEY: "sb_secret_x", APP_URL: "https://salon.example" };

test("a booking is held at the catalog deposit and paid through Stripe", async () => {
  const calls = mockFetch([
    [/rest\/v1\/bookings$/, (u, init, body) => [{ id: "11111111-1111-1111-1111-111111111111", ...JSON.parse(body)[0] }]],
    [/auth\/v1\/user/, () => ({ status: 401, body: {} })],
    stripeSession,
  ]);
  const when = new Date(Date.now() + 3 * 86400e3).toISOString();
  const res = await data.fetch(post("/book", { service: "haircut", starts_at: when, email: "ana@example.com", deposit_cents: 1 }), bookingEnv, ctx);
  assert.equal(res.status, 200);
  const insert = JSON.parse(calls.find((c) => c.url.endsWith("/rest/v1/bookings")).body)[0];
  assert.equal(insert.deposit_cents, 1000);
  assert.equal(insert.minutes, 45);
  assert.equal(insert.status, "pending");
  const form = new URLSearchParams(calls.find((c) => c.url.includes("checkout/sessions")).body);
  assert.equal(form.get("line_items[0][price]"), "price_cut");
  assert.equal(form.get("metadata[row_table]"), "bookings");
  assert.ok(Number(form.get("expires_at")) > Date.now() / 1000);
});

test("booking validation", async () => {
  mockFetch([[/auth\/v1\/user/, () => ({ status: 401, body: {} })]]);
  const soon = new Date(Date.now() + 60e3).toISOString();
  for (const body of [{}, { service: "nope", starts_at: soon, email: "a@b.co" }, { service: "haircut", starts_at: soon, email: "a@b.co" }, { service: "haircut", starts_at: "2099-01-01T10:00:00Z", email: "a@b.co" }, { service: "haircut", starts_at: new Date(Date.now() + 86400e3).toISOString(), email: "nope" }]) {
    const res = await data.fetch(post("/book", body), bookingEnv, ctx);
    assert.equal(res.status, 400, JSON.stringify(body));
  }
});

test("a restaurant order refuses sold-out items and totals at menu prices", async () => {
  const menu = JSON.stringify({ currency: "usd", items: [{ key: "pizza", name: "Pizza", billing: "once", prices: { once: "price_pizza" }, amounts: { once: 1200 } }, { key: "cake", name: "Cake", billing: "once", prices: { once: "price_cake" }, amounts: { once: 650 } }] });
  const env = { ...bookingEnv, TEMPLATE: "restaurant", CATALOG: menu, MAX_QUANTITY: "20", CART: "true" };
  let soldOut = [{ item_key: "cake" }];
  const calls = mockFetch([
    [/rest\/v1\/sold_out/, () => soldOut],
    [/rest\/v1\/orders$/, (u, init, body) => [{ id: "22222222-2222-2222-2222-222222222222", ...JSON.parse(body)[0] }]],
    [/auth\/v1\/user/, () => ({ status: 401, body: {} })],
    stripeSession,
  ]);
  let res = await data.fetch(post("/order", { email: "sam@example.com", items: [{ key: "pizza", quantity: 2 }, { key: "cake" }] }), env, ctx);
  assert.equal(res.status, 409);
  soldOut = [];
  res = await data.fetch(post("/order", { email: "sam@example.com", items: [{ key: "pizza", quantity: 2, unit_amount: 1 }, { key: "cake" }] }), env, ctx);
  assert.equal(res.status, 200);
  const insert = JSON.parse(calls.filter((c) => c.url.endsWith("/rest/v1/orders")).pop().body)[0];
  assert.equal(insert.total_cents, 3050);
  assert.equal(insert.status, "awaiting_payment");
});

async function signed(secret, payload) {
  const t = Math.floor(Date.now() / 1000);
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const sig = [...new Uint8Array(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(`${t}.${payload}`)))].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `t=${t},v1=${sig}`;
}

test("a paid booking is confirmed once, even when Stripe retries", async () => {
  const id = "11111111-1111-1111-1111-111111111111";
  let paid = false;
  const calls = mockFetch([
    [/rest\/v1\/bookings\?id=eq\./, () => (paid ? [] : ((paid = true), [{ id, email: "ana@example.com", service_name: "Haircut", starts_at: new Date().toISOString(), minutes: 45, deposit_cents: 1000, currency: "usd" }]))],
    [/resend\.com\/emails/, () => ({ id: "email_1" })],
    [/rest\/v1\/bp_events/, () => []],
  ]);
  const env = { ...bookingEnv, STRIPE_WEBHOOK_SECRET: "whsec_test", RESEND_API_KEY: "re_x", FROM_EMAIL: "Salon <b@salon.example>" };
  const payload = JSON.stringify({ id: "evt_1", type: "checkout.session.completed", livemode: false, data: { object: { id: "cs_test_1", payment_status: "paid", metadata: { row_table: "bookings", row_id: id } } } });
  const send = async () => data.fetch(new Request("https://api.example.workers.dev/stripe/webhook", { method: "POST", headers: { "Stripe-Signature": await signed("whsec_test", payload) }, body: payload }), env, ctx);
  const first = await (await send()).json();
  assert.equal(first.received, true);
  assert.equal(first.duplicate, undefined);
  const patch = calls.find((c) => c.method === "PATCH");
  assert.match(patch.url, /paid=eq\.false/);
  assert.equal(JSON.parse(patch.body).status, "confirmed");
  assert.equal(calls.filter((c) => c.url.includes("resend.com/emails")).length, 1);
  const again = await (await send()).json();
  assert.equal(again.duplicate, true);
  assert.equal(calls.filter((c) => c.url.includes("resend.com/emails")).length, 1, "no second confirmation email");
  // A forged row reference is ignored.
  const forged = JSON.stringify({ id: "evt_2", type: "checkout.session.completed", livemode: false, data: { object: { id: "cs_x", payment_status: "paid", metadata: { row_table: "staff", row_id: id } } } });
  const r = await data.fetch(new Request("https://api.example.workers.dev/stripe/webhook", { method: "POST", headers: { "Stripe-Signature": await signed("whsec_test", forged) }, body: forged }), env, ctx);
  assert.equal((await r.json()).duplicate, undefined);
  assert.equal(calls.filter((c) => c.method === "PATCH" && c.url.includes("staff")).length, 0);
});
