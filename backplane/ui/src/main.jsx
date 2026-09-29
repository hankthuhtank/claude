import { render } from "preact";
import { useEffect } from "preact/hooks";
import { call, onEvent } from "./api.js";
import { set, get, useStore, go, toast } from "./state.js";
import { Icon } from "./icons.jsx";
import { Toasts, Led, Btn } from "./ui.jsx";
import { Home } from "./screens/home.jsx";
import { NewBackend } from "./screens/newbackend.jsx";
import { Accounts } from "./screens/accounts.jsx";
import { Project } from "./screens/project.jsx";
import { Activity } from "./screens/register.jsx";
import { Practice } from "./screens/bench.jsx";
import { Binder } from "./screens/binder.jsx";
import { Settings } from "./screens/settings.jsx";
import { applySettings, refreshBoot } from "./boot.js";

const NAV = [
  ["home", "Backends", "rack"],
  ["new", "New", "plus"],
  ["accounts", "Accounts", "plug"],
  ["activity", "Activity", "tape"],
  ["practice", "Practice", "flask"],
  ["binder", "Guides", "binder"],
];

function worst(projects) {
  let w = "unknown";
  const rank = { unknown: 0, ok: 1, warn: 2, fail: 3 };
  for (const p of projects || []) for (const e of p.environments || []) if ((rank[e.overall] || 0) > rank[w]) w = e.overall;
  return w;
}

function Rail() {
  const route = useStore((s) => s.route);
  const boot = useStore((s) => s.boot);
  const practice = useStore((s) => s.practice);
  const current = route.name === "project" ? "home" : route.name;
  const w = worst(boot?.projects);
  // Practice connections only count while the sandbox runs; otherwise they are off, not "connected".
  const conns = (boot?.connections || []).filter((c) => !c.practice || practice);
  const connWorst = conns.some((c) => c.status === "fail") ? "fail" : conns.some((c) => c.status === "warn") ? "warn" : conns.length ? "ok" : "unknown";
  return (
    <nav class="rail" aria-label="Main">
      <div class="rail-mark"><b>BP</b>Backplane</div>
      {NAV.map(([id, label, icon]) => (
        <button type="button" class="rail-item" aria-current={current === id ? "page" : undefined} onClick={() => go(id)}>
          <Icon name={icon} />
          <span>{label}</span>
          {id === "home" && boot?.projects?.length ? <Led h={w} /> : null}
          {id === "accounts" && conns.length ? <Led h={connWorst} /> : null}
          {id === "practice" && practice ? <Led busy title="Practice mode is on" /> : null}
        </button>
      ))}
      <div class="rail-spacer" />
      <button type="button" class="rail-item" aria-current={current === "settings" ? "page" : undefined} onClick={() => go("settings")}>
        <Icon name="gear" /><span>Settings</span>
      </button>
    </nav>
  );
}

function Screen() {
  const route = useStore((s) => s.route);
  const p = route.params || {};
  switch (route.name) {
    case "new": return <NewBackend {...p} />;
    case "accounts": return <Accounts {...p} />;
    case "project": return <Project key={p.id} {...p} />;
    case "activity": return <Activity {...p} />;
    case "practice": return <Practice {...p} />;
    case "binder": return <Binder {...p} />;
    case "settings": return <Settings {...p} />;
    default: return <Home />;
  }
}

function PracticeBanner() {
  const practice = useStore((s) => s.practice);
  const route = useStore((s) => s.route);
  if (!practice || route.name === "practice") return null;
  return (
    <div class="practice-banner" role="status">
      <Led busy />
      <b>Practice mode on</b>
      <span class="ink2">Simulated services on this computer — nothing reaches real accounts. Resets when Backplane closes.</span>
      <span style="flex:1" />
      <Btn size="sm" kind="ghost" onClick={() => go("practice")}>Open Practice</Btn>
    </div>
  );
}

function App() {
  const ready = useStore((s) => s.ready);
  useEffect(() => {
    const off = onEvent((ev) => {
      if (ev.type === "toast") toast(ev.data?.kind || "info", ev.data?.title || "", ev.data?.body || "", 10000);
      if (ev.type === "practice") set({ practice: !!ev.data?.running });
      if (ev.type === "report" || ev.type === "run" || ev.type === "monitor") scheduleBoot();
    });
    const mq = matchMedia("(prefers-color-scheme: light)");
    const onScheme = () => get().settings?.theme === "system" && applySettings(get().settings);
    mq.addEventListener("change", onScheme);
    return () => { off(); mq.removeEventListener("change", onScheme); };
  }, []);
  if (!ready) return <div class="boot"><span class="boot-led" />Powering up…</div>;
  return (
    <div class="shell">
      <Rail />
      <main class="work" id="main">
        <PracticeBanner />
        <Screen />
      </main>
      <Toasts />
    </div>
  );
}

let bootTimer = 0;
function scheduleBoot() {
  clearTimeout(bootTimer);
  bootTimer = setTimeout(() => refreshBoot().catch(() => {}), 600);
}

async function boot() {
  try {
    const [b, catalog, providers] = await Promise.all([call("Bootstrap"), call("Catalog"), call("Providers")]);
    applySettings(b.settings);
    set({
      ready: true, boot: b, settings: b.settings, catalog, providers, practice: b.practiceRunning,
      route: b.settings.onboarded ? { name: "home", params: {} } : { name: "binder", params: { first: true } },
    });
  } catch (e) {
    document.getElementById("app").innerHTML = "";
    const div = document.createElement("div");
    div.className = "boot";
    div.textContent = "Backplane could not start its local service: " + e.message;
    document.getElementById("app").appendChild(div);
  }
}

render(<App />, document.getElementById("app"));
boot();
