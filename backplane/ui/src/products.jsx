import { useState } from "preact/hooks";
import { call } from "./api.js";
import { toast } from "./state.js";
import { Icon } from "./icons.jsx";
import { Btn, Led, Badge, Empty, useAction, useCall, copyText } from "./ui.jsx";

// Products & prices: a designation strip, one numbered row per item.
// Customers only ever send an item's key and a quantity; the amount always
// comes from Stripe, so what's typed here is what they pay.

const BILLING = { once: "One-time", month: "Monthly", year: "Yearly" };
const SYMBOL = { usd: "$", eur: "€", gbp: "£", cad: "CA$", aud: "A$" };

export function money(cents, currency = "usd") {
  const c = String(currency || "usd").toLowerCase();
  const v = (cents || 0) / 100;
  const s = Number.isInteger(v) ? String(v) : v.toFixed(2);
  return SYMBOL[c] ? SYMBOL[c] + s : `${s} ${c.toUpperCase()}`;
}

export function emptyItem(spec) {
  return { name: "", price: undefined, billing: spec?.billing?.[0] || "once", ...(spec?.minutes ? { minutes: 30 } : {}) };
}

/** Validates the list the way the server will; returns { index: { field: message } }. */
export function productErrors(spec, items) {
  const errs = {};
  const put = (i, f, m) => ((errs[i] ||= {})[f] = m);
  if (!items?.length) errs.list = `Add at least one ${spec.noun}.`;
  (items || []).forEach((it, i) => {
    if (!String(it.name || "").trim()) put(i, "name", "Name it");
    const p = Number(it.price);
    if (it.price === undefined || it.price === "" || Number.isNaN(p) || p < 0.5) put(i, "price", "At least 0.50");
    if (it.yearlyPrice !== undefined && it.yearlyPrice !== "" && Number(it.yearlyPrice) < 0.5) put(i, "yearlyPrice", "At least 0.50");
  });
  return errs;
}

export function ProductsEditor({ spec, value, onChange, currency = "usd", errors = {}, live }) {
  const items = value?.length ? value : [];
  const set = (i, patch) => onChange(items.map((it, j) => (j === i ? { ...it, ...patch } : it)));
  const remove = (i) => onChange(items.filter((_, j) => j !== i));
  const add = () => onChange([...items, emptyItem(spec)]);
  const noun = spec.noun || "product";
  const priceLabel = spec.priceLabel || "Price";
  const multi = spec.billing.length > 1;
  const num = (v) => (v === "" ? undefined : Number(v));
  const cols = ["52px", "minmax(0,1fr)", "132px", multi ? "124px" : null, spec.yearly ? "150px" : null, spec.minutes ? "92px" : null, "36px"].filter(Boolean).join(" ");
  return (
    <div class="pricelist" role="group" aria-label={`${noun}s and prices`} style={`--pl-cols:${cols}`}>
      <div class="pl-head" aria-hidden="true">
        <span />
        <span class="silk">{noun}</span>
        <span class="silk">{priceLabel}</span>
        {multi ? <span class="silk">Billing</span> : null}
        {spec.yearly ? <span class="silk">Yearly option</span> : null}
        {spec.minutes ? <span class="silk">Minutes</span> : null}
        <span />
      </div>
      {items.map((it, i) => {
        const e = errors[i] || {};
        const state = live?.[it.key];
        return (
          <div class="pl-row">
            <span class="pl-no silk" aria-hidden="true">{state ? <Led h={state === "live" ? "ok" : state === "changed" ? "warn" : undefined} title={state === "live" ? "Live in Stripe" : state === "changed" ? "Changed — build to apply" : "New — build to create"} /> : null}{String(i + 1).padStart(2, "0")}</span>
            <div class="pl-cell">
              <input class="input" value={it.name || ""} placeholder={i === 0 ? spec.placeholder : ""} aria-label={`${noun} ${i + 1} name`} aria-invalid={e.name ? "true" : undefined}
                onInput={(ev) => set(i, { name: ev.currentTarget.value })} maxLength={80} />
              {e.name ? <div class="pl-err">{e.name}</div> : null}
            </div>
            <div class="pl-cell" data-label={priceLabel}>
              <div class="pl-money"><span aria-hidden="true">{SYMBOL[currency] || currency.toUpperCase()}</span>
                <input class="input num" type="number" step="0.01" min="0.5" inputMode="decimal" value={it.price ?? ""} aria-label={`${noun} ${i + 1} ${priceLabel.toLowerCase()}`} aria-invalid={e.price ? "true" : undefined}
                  onInput={(ev) => set(i, { price: num(ev.currentTarget.value) })} />
              </div>
              {e.price ? <div class="pl-err">{e.price}</div> : null}
            </div>
            {multi ? (
              <div class="pl-cell" data-label="Billing">
                <select class="select" value={it.billing || spec.billing[0]} aria-label={`${noun} ${i + 1} billing`}
                  onChange={(ev) => set(i, { billing: ev.currentTarget.value, ...(ev.currentTarget.value !== "month" ? { yearlyPrice: undefined } : {}) })}>
                  {spec.billing.map((b) => <option value={b}>{BILLING[b]}</option>)}
                </select>
              </div>
            ) : null}
            {spec.yearly ? (
              <div class="pl-cell" data-label="Yearly option">
                {(it.billing || spec.billing[0]) === "month" ? (
                  <>
                    <div class="pl-money"><span aria-hidden="true">{SYMBOL[currency] || currency.toUpperCase()}</span>
                      <input class="input num" type="number" step="0.01" min="0.5" placeholder="optional" value={it.yearlyPrice ?? ""} aria-label={`${noun} ${i + 1} yearly price (optional)`}
                        aria-invalid={e.yearlyPrice ? "true" : undefined} onInput={(ev) => set(i, { yearlyPrice: num(ev.currentTarget.value) })} />
                      <span class="small muted" aria-hidden="true">/yr</span>
                    </div>
                    {e.yearlyPrice ? <div class="pl-err">{e.yearlyPrice}</div> : null}
                  </>
                ) : <span class="small muted">—</span>}
              </div>
            ) : null}
            {spec.minutes ? (
              <div class="pl-cell" data-label="Minutes">
                <input class="input num" type="number" step="5" min="5" value={it.minutes ?? 30} aria-label={`${noun} ${i + 1} length in minutes`} onInput={(ev) => set(i, { minutes: num(ev.currentTarget.value) })} />
              </div>
            ) : null}
            <Btn size="sm" kind="ghost" icon="x" aria-label={`Remove ${it.name || noun + " " + (i + 1)}`} disabled={items.length === 1} onClick={() => remove(i)} />
          </div>
        );
      })}
      <div class="row" style="justify-content:space-between;margin-top:6px">
        <Btn size="sm" icon="plus" disabled={items.length >= 40} onClick={add}>Add {noun}</Btn>
        {errors.list ? <span class="pl-err">{errors.list}</span> : <span class="small muted">{spec.quantity ? `Customers can buy up to ${spec.maxQuantity} of each in one order.` : `One ${noun} per checkout.`}</span>}
      </div>
    </div>
  );
}

/** The backend's Products tab: edit items and prices, then review the plan. */
export function ProductsTab({ project, env, dash, onPlan }) {
  const { data, error, reload } = useCall("Products", { projectId: project.id, env }, [dash.manifest?.updatedAt]);
  const [draft, setDraft] = useState(null);
  const [errors, setErrors] = useState({});
  const [busy, run] = useAction();
  if (error) return <div class="notice fail"><Icon name="fail" /><div>{error.message}</div></div>;
  if (!data) return <div class="muted small">Loading…</div>;
  if (!data.spec) return <Empty icon="tag" title="Nothing for sale here">This preset doesn't sell a catalog through Backplane.</Empty>;
  const spec = data.spec;
  const current = data.items.map((r) => ({ key: r.key, name: r.name, price: r.price, billing: r.billing, ...(r.yearlyPrice ? { yearlyPrice: r.yearlyPrice } : {}), ...(r.minutes ? { minutes: r.minutes } : {}), ...(r.description ? { description: r.description } : {}) }));
  const items = draft || current;
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(current);
  const liveState = {};
  for (const r of data.items) liveState[r.key] = r.prices.some((p) => p.status === "changed") ? "changed" : r.prices.some((p) => p.status === "new") ? "new" : "live";
  const save = async (andPlan) => {
    const errs = productErrors(spec, items);
    setErrors(errs);
    if (Object.keys(errs).length) { toast("warn", "A few rows need attention"); return; }
    await run(() => call("UpdateProject", { id: project.id, answers: { products: items } }));
    setDraft(null);
    reload(true);
    if (andPlan) {
      const plan = await run(() => call("Plan", { projectId: project.id, env }));
      onPlan(plan);
    } else toast("ok", "Saved", "Build to apply the changes in Stripe.");
  };
  const first = data.items[0];
  const order = data.order;
  const example = !data.api ? "" : order === "/checkout"
    ? `<a href="${data.api}/checkout?item=${first?.key}">Buy ${first?.name}</a>`
    : order === "/book"
      ? `fetch("${data.api}/book", { method: "POST", headers: { "Content-Type": "application/json" },\n  body: JSON.stringify({ service: "${first?.key}", starts_at: "2026-10-12T15:00:00Z", email }) })\n  .then((r) => r.json()).then((r) => location.href = r.url)`
      : order === "/order"
        ? `fetch("${data.api}/order", { method: "POST", headers: { "Content-Type": "application/json" },\n  body: JSON.stringify({ items: [{ key: "${first?.key}", quantity: 2 }], email }) })\n  .then((r) => r.json()).then((r) => location.href = r.url)`
        : `// signed-in users\nfetch("${data.api}/billing/checkout", { method: "POST", headers: { Authorization: "Bearer " + session.access_token },\n  body: JSON.stringify({ plan: "${first?.key}", interval: "month" }) })`;
  return (
    <div class="stack">
      <div class="card stack">
        <div class="spread">
          <h3>What customers can buy</h3>
          {data.pending && !dirty ? <Badge kind="accent">Changes not built yet</Badge> : null}
        </div>
        <ProductsEditor spec={spec} value={items} onChange={setDraft} currency={data.currency} errors={errors} live={data.built ? liveState : null} />
        {data.removed?.length ? <div class="notice info"><Icon name="info" /><div class="small">The next build retires {data.removed.join(", ")} in Stripe. Past orders keep their records.</div></div> : null}
        <div class="row" style="justify-content:flex-end">
          {dirty ? <Btn onClick={() => { setDraft(null); setErrors({}); }}>Undo changes</Btn> : null}
          {dirty ? <Btn busy={busy} onClick={() => save(false)}>Save</Btn> : null}
          <Btn kind="primary" icon="bolt" busy={busy} disabled={!dirty && !data.pending} onClick={() => (dirty ? save(true) : run(() => call("Plan", { projectId: project.id, env })).then(onPlan))}>
            {dirty ? "Save & review changes" : "Review changes"}
          </Btn>
        </div>
        <div class="small muted">Stripe prices can't be edited, so a new price is created first and the old one is retired right after — checkout never goes without a price. Orders already paid are unaffected.</div>
      </div>
      <div class="grid-2" style="align-items:start">
        <div class="card stack">
          <h3>Live prices</h3>
          {!data.built ? <div class="small muted">Build the backend to create these in Stripe.</div> : (
            <table class="t">
              <thead><tr><th>{spec.noun}</th><th>Billing</th><th class="num">Charges</th><th>Status</th></tr></thead>
              <tbody>
                {data.items.flatMap((r) => r.prices.map((p) => (
                  <tr>
                    <td><b>{r.name}</b><div class="small muted mono">{r.key}</div></td>
                    <td>{BILLING[p.interval]}</td>
                    <td class="num">{p.status === "new" ? "—" : money(p.liveAmount, data.currency)}{p.status === "changed" ? <div class="small muted">→ {money(p.amount, data.currency)}</div> : null}</td>
                    <td><span class="row tight"><Led h={p.status === "live" ? "ok" : p.status === "changed" ? "warn" : undefined} />{p.status === "live" ? "Live" : p.status === "changed" ? "Build to apply" : "Not created yet"}</span></td>
                  </tr>
                )))}
              </tbody>
            </table>
          )}
        </div>
        {data.api ? (
          <div class="card stack">
            <h3>How customers buy</h3>
            <div class="small ink2">Your site sends an item's key and a quantity — never a price. <span class="mono">{data.api}/products</span> lists everything for sale as JSON.</div>
            <pre class="code" style="max-height:200px;white-space:pre-wrap;overflow-wrap:anywhere">{example}</pre>
            <div class="row"><Btn size="sm" icon="copy" onClick={() => copyText(example)}>Copy</Btn></div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
