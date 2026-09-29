// A tiny store: one object, subscribe/notify, and a hook.
import { useEffect, useState } from "preact/hooks";

const state = {
  ready: false,
  boot: null, // Bootstrap result
  settings: null,
  catalog: null,
  providers: null,
  route: { name: "home", params: {} },
  toasts: [],
  practice: false,
};

const subs = new Set();

export function get() { return state; }

export function set(patch) {
  Object.assign(state, typeof patch === "function" ? patch(state) : patch);
  for (const fn of subs) fn(state);
}

export function useStore(select = (s) => s) {
  const [v, setV] = useState(() => select(state));
  useEffect(() => {
    const fn = (s) => setV(select(s));
    subs.add(fn);
    fn(state);
    return () => subs.delete(fn);
  }, []);
  return v;
}

export function go(name, params = {}) {
  set({ route: { name, params } });
  document.querySelector(".work")?.scrollTo({ top: 0 });
}

let toastId = 0;
export function toast(kind, title, body = "", ms = 6000) {
  const id = ++toastId;
  set((s) => ({ toasts: [...s.toasts, { id, kind, title, body }] }));
  if (ms) setTimeout(() => dismiss(id), ms);
  return id;
}

export function dismiss(id) {
  set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }));
}

// Formatting helpers used across screens.
export const providerName = (id) => {
  const p = state.providers?.find((x) => x.id === id);
  return p ? p.name : ({ cloudflare: "Cloudflare", supabase: "Supabase", stripe: "Stripe", resend: "Resend", github: "GitHub" }[id] || id);
};

export function ago(t) {
  if (!t) return "never";
  const d = (Date.now() - new Date(t).getTime()) / 1000;
  if (d < 45) return "just now";
  if (d < 90) return "a minute ago";
  if (d < 3600) return `${Math.round(d / 60)} min ago`;
  if (d < 5400) return "an hour ago";
  if (d < 86400) return `${Math.round(d / 3600)} hours ago`;
  if (d < 172800) return "yesterday";
  return new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function when(t) {
  if (!t) return "";
  return new Date(t).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

export function clock(t) {
  return new Date(t).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

export const healthWord = { ok: "Pass", warn: "Attention", fail: "Fail", unknown: "Not tested", skipped: "Skipped", off: "Off" };

export function pct(v) {
  if (v == null || v < 0) return "—";
  const p = v * 100;
  return (p >= 99.95 ? "100" : p.toFixed(p >= 99 ? 2 : 1)) + "%";
}
