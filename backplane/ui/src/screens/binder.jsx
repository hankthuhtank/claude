import { useState } from "preact/hooks";
import { call } from "../api.js";
import { useStore, go, set as setState, providerName } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Guide, ExtLink } from "../ui.jsx";
import { ThemePicker } from "./settings.jsx";
import { refreshBoot } from "../boot.js";

// The Binder: tabbed guides. On first launch it doubles as the welcome.
const PAGES = [
  ["what", "What it does"],
  ["how", "How a build works"],
  ["checks", "The six checks"],
  ["accounts", "Accounts you'll need"],
  ["safety", "Safety & secrets"],
  ["look", "Pick your look"],
];

export function Binder({ first, page: page0 }) {
  const [page, setPage] = useState(page0 || "what");
  const settings = useStore((s) => s.settings);
  const finish = async (dest) => {
    if (!settings.onboarded) {
      await call("SaveSettings", { ...settings, onboarded: true });
      await refreshBoot();
    }
    if (dest === "practice") {
      await call("StartPractice");
      setState({ practice: true });
      await refreshBoot();
      go("new", { template: "software-store" });
    } else go(dest || "home");
  };
  const idx = PAGES.findIndex(([k]) => k === page);
  return (
    <div class="page" style="max-width:1180px">
      <div class="binder">
        <div class="binder-tabs" role="tablist" aria-label="Guides">
          <div class="silk" style="padding:0 18px 10px">{first ? "Welcome" : "Guides"}</div>
          {PAGES.map(([k, l], i) => (
            <button type="button" role="tab" class="binder-tab" aria-selected={k === page ? "true" : "false"} onClick={() => setPage(k)}>
              <span class="no">{String(i + 1).padStart(2, "0")}</span>{l}
            </button>
          ))}
        </div>
        <div class="binder-page" role="tabpanel">
          {page === "what" ? <What /> : null}
          {page === "how" ? <How /> : null}
          {page === "checks" ? <Checks /> : null}
          {page === "accounts" ? <Accounts /> : null}
          {page === "safety" ? <Safety /> : null}
          {page === "look" ? <Look /> : null}
          <div class="spread" style="margin-top:34px;border-top:1px solid var(--line);padding-top:18px">
            {idx > 0 ? <Btn icon="left" onClick={() => setPage(PAGES[idx - 1][0])}>{PAGES[idx - 1][1]}</Btn> : <span />}
            {idx < PAGES.length - 1 ? <Btn kind="primary" onClick={() => setPage(PAGES[idx + 1][0])}>{PAGES[idx + 1][1]}<Icon name="right" /></Btn> : (
              <div class="row">
                <Btn icon="flask" onClick={() => finish("practice")}>Practice first</Btn>
                <Btn kind="primary" icon="plus" onClick={() => finish("new")}>Set up a real backend</Btn>
              </div>
            )}
          </div>
          {first && idx < PAGES.length - 1 ? <div style="margin-top:10px;text-align:right"><button class="linkish small" onClick={() => finish("home")}>Skip the tour</button></div> : null}
        </div>
      </div>
    </div>
  );
}

function What() {
  return (
    <>
      <div class="eyebrow">Backplane</div>
      <h1 style="font-size:3rem">Your backend, racked and certified.</h1>
      <p class="lede" style="font-size:1.1rem;margin-top:14px">Most businesses need the same plumbing: take payments, store orders or customers, send email, keep files safe, run a little code on a server. Wiring it together across five dashboards is where things break.</p>
      <p class="lede" style="font-size:1.1rem">Backplane sets it up from one place and then keeps proving it works. Pick a preset for your kind of business (or describe it), review a plain-English plan, approve it, and Backplane builds every piece in the right order — then tests every connection, including a real test purchase with nothing charged.</p>
      <div class="card flat well" style="margin-top:18px">
        <div class="silk">Built on</div>
        <div class="chips" style="margin-top:6px">{["Cloudflare", "Supabase", "Stripe", "Resend", "GitHub"].map((p) => <span class="chip"><b>{p}</b></span>)}</div>
        <div class="small ink2" style="margin-top:8px">Plus add-ons for text messages (Twilio), error monitoring (Sentry), analytics (PostHog), media (Cloudinary), caching (Upstash), hosting (Vercel) and enterprise sign-in (Clerk). Everything runs in your own accounts — you own it, and you can export it any time.</div>
      </div>
    </>
  );
}

function How() {
  return (
    <>
      <h2>How a build works</h2>
      <p class="lede">This really is a sequence, and Backplane never skips a step.</p>
      <Guide steps={[
        { title: "Describe", body: "Choose a preset by kind of business, or describe what you need. Answer a few questions — price, domain, what customers receive." },
        { title: "Review the work order", body: "Backplane reads what already exists and lists exactly what it would create or change, why, and an honest cost estimate. Nothing has happened yet." },
        { title: "Approve & build", body: "Steps run in dependency order with retries and a checkpoint after each one. If something fails, fix it and resume — finished steps aren't repeated. Finished builds can be rolled back." },
        { title: "Certify", body: "Six levels of checks, from “is the key valid” to a full test purchase that is cleaned up afterwards. The result: FULLY OPERATIONAL, or a punch list with plain-English fixes." },
        { title: "Monitor", body: "Quick checks run on a schedule, full certifications daily. If something changes outside Backplane — a deleted bucket, a disabled webhook, row security turned off — you're told what broke, what still works, and the safe repair." },
      ]} />
    </>
  );
}

function Checks() {
  const names = useStore((s) => s.boot?.levelNames || {});
  const desc = {
    1: "Keys are valid, the right account, the permissions Backplane needs, APIs reachable.",
    2: "Everything that was built still exists (nothing deleted or renamed outside Backplane).",
    3: "Live settings match what was set up — bindings, secrets, webhooks, row-level security, email DNS.",
    4: "The connections themselves work: the Worker really reaches the database, storage and email; Stripe really delivers to the Worker.",
    5: "Each function works with safe test data: write and read a row, store and fetch a file, send to a test inbox, sign in a test user.",
    6: "The whole customer journey: a signed test purchase flows through checkout, order, download link and email — then everything is cleaned up.",
  };
  return (
    <>
      <h2>The six checks</h2>
      <p class="lede">Every connection is proven, not assumed. Quick checks run levels 1–4 and never write anything; full certifications run all six.</p>
      <div class="stack" style="margin-top:14px;gap:10px">
        {[1, 2, 3, 4, 5, 6].map((n) => (
          <div class="row" style="align-items:flex-start;gap:14px">
            <span class="badge solid" style="min-width:38px;justify-content:center">L{n}</span>
            <div><b>{names[n]}</b><div class="small ink2">{desc[n]}</div></div>
          </div>
        ))}
      </div>
    </>
  );
}

function Accounts() {
  const providers = useStore((s) => s.providers);
  const core = ["cloudflare", "supabase", "stripe", "resend", "github"].map((id) => providers.find((p) => p.id === id)).filter(Boolean);
  return (
    <>
      <h2>Accounts you'll need</h2>
      <p class="lede">All five have free tiers that cover a new business. Connect them when a backend asks — each guide opens the right page with the permissions pre-selected where the provider allows.</p>
      <div class="stack" style="margin-top:14px">
        {core.map((p) => (
          <div class="card flat">
            <div class="spread"><b class="h3">{p.name}</b>{p.docsUrl ? <ExtLink href={p.docsUrl}>Docs</ExtLink> : null}</div>
            <div class="small ink2">{p.tagline}</div>
          </div>
        ))}
      </div>
      <div class="row" style="margin-top:14px"><Btn icon="plug" onClick={() => go("accounts")}>Go to Accounts</Btn></div>
    </>
  );
}

function Safety() {
  const vault = useStore((s) => s.boot?.vault);
  return (
    <>
      <h2>Safety & secrets</h2>
      <ul class="stack" style="padding-left:18px;gap:8px;margin-top:12px">
        <li><b>Keys stay on this computer,</b> encrypted ({vault}). They are never written to project files, exports, generated code or logs.</li>
        <li><b>Nothing happens without a plan you approve.</b> The optional Claude assistant only suggests a preset — it never builds or deploys.</li>
        <li><b>Production is protected.</b> Deleting or rolling back production asks you to type the project name. Live Stripe keys are blocked outside production.</li>
        <li><b>Your edits are respected.</b> Generated code you change is marked USER MODIFIED and never overwritten without asking.</li>
        <li><b>Test data is cleaned up.</b> Certification uses clearly marked probe records, test inboxes and expired checkout sessions — no card is ever charged.</li>
        <li><b>You're not locked in.</b> Export any backend as a manifest, its code, a Wrangler config and an OpenTofu starting point.</li>
      </ul>
    </>
  );
}

function Look() {
  return (
    <>
      <h2>Pick your look</h2>
      <p class="lede">Every theme is a material from real rack gear. Change it any time in Settings.</p>
      <div style="margin-top:14px"><ThemePicker /></div>
    </>
  );
}
