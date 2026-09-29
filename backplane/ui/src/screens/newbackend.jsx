import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { call, upload } from "../api.js";
import { useStore, go, providerName, toast, get } from "../state.js";
import { Icon } from "../icons.jsx";
import { Btn, Field, Toggle, Badge, Modal, useAction, ErrorNote, ExtLink } from "../ui.jsx";
import { refreshBoot } from "../boot.js";
import { ProductsEditor, productErrors, emptyItem } from "../products.jsx";

// Choosing a backend: describe it in plain English, or browse presets by the
// kind of business; then review which server does which job and answer a
// few questions. Nothing is created until a plan is approved.

export function NewBackend({ template, industry: ind0 }) {
  const catalog = useStore((s) => s.catalog);
  const [tplId, setTplId] = useState(template || null);
  const [design, setDesign] = useState(null);
  const tpl = catalog.templates.find((t) => t.id === tplId);
  if (tpl) return <PresetForm tpl={tpl} design={design?.templateId === tpl.id ? design : null} onBack={() => setTplId(null)} />;
  return <Chooser initialIndustry={ind0} onPick={(id, d) => { setDesign(d || null); setTplId(id); }} />;
}

function Chooser({ initialIndustry, onPick }) {
  const catalog = useStore((s) => s.catalog);
  const [industry, setIndustry] = useState(initialIndustry || catalog.industries[0].id);
  const presets = catalog.templates.filter((t) => t.industry === industry);
  const ind = catalog.industries.find((i) => i.id === industry);
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <div class="eyebrow">New backend</div>
          <h1>What are you running?</h1>
          <p class="lede">Describe it, or pick the preset closest to your business. Each preset says exactly which server handles which job, so there are no surprises.</p>
        </div>
      </div>
      <Describe onPick={onPick} />
      <div class="catalog" style="margin-top:26px">
        <div class="industries" role="group" aria-label="Kinds of business">
          <div class="silk" style="padding:0 12px 6px">By kind of business</div>
          {catalog.industries.map((i) => (
            <button type="button" class="industry" aria-pressed={i.id === industry ? "true" : "false"} onClick={() => setIndustry(i.id)}>
              <b>{i.name}</b>
              <small>{catalog.templates.filter((t) => t.industry === i.id).length} presets</small>
            </button>
          ))}
        </div>
        <div>
          <div class="card flat well" style="margin-bottom:14px">
            <div class="h3">{ind?.name}</div>
            <div class="ink2 small" style="margin-top:2px">{ind?.summary}</div>
            <div class="row small" style="margin-top:8px"><span class="silk">Typical line-up</span><span>{ind?.servers}</span></div>
          </div>
          <div class="presets">
            {presets.map((t) => <PresetCard t={t} onClick={() => onPick(t.id)} />)}
          </div>
        </div>
      </div>
    </div>
  );
}

function PresetCard({ t, onClick, pressed }) {
  return (
    <button type="button" class="card preset" onClick={onClick} aria-pressed={pressed ? "true" : "false"}>
      <div class="spread">
        <span class="silk">{t.category}</span>
        <LevelBadge level={t.level} />
      </div>
      <div class="h2" style="font-size:1.3rem">{t.name}</div>
      <div class="ink2 small">{t.summary}</div>
      <div class="flow">{t.flow.join("  →  ")}</div>
      <div class="chips">{t.providers.map((p) => <span class="chip">{providerName(p)}</span>)}</div>
    </button>
  );
}

/** What a preset delivers, in plain words. */
export function LevelBadge({ level }) {
  return level === "full"
    ? <Badge kind="accent">Ready to sell</Badge>
    : <span title="Backplane builds the backend; your own app or site plugs into it."><Badge>Backend only — you bring the app</Badge></span>;
}

// ---- plain-English builder ----

function Describe({ onPick }) {
  const settings = useStore((s) => s.settings);
  const catalog = useStore((s) => s.catalog);
  const [text, setText] = useState("");
  const [d, setD] = useState(null);
  const [asking, run] = useAction();
  const [ai, setAi] = useState(null);
  const timer = useRef(0);
  useEffect(() => { call("AIState").then(setAi).catch(() => {}); }, []);
  const onInput = (v) => {
    setText(v);
    clearTimeout(timer.current);
    if (v.trim().length < 12) { setD(null); return; }
    timer.current = setTimeout(() => call("Interpret", { text: v }).then(setD).catch(() => {}), 350);
  };
  const askClaude = async () => {
    const r = await run(() => call("DesignFromDescription", { text, useAI: true }));
    setD(r);
    if (r.note) toast("warn", "Used the offline reader", r.note, 9000);
  };
  const t = d && catalog.templates.find((x) => x.id === d.templateId);
  return (
    <div class="card describe">
      <div class="spread" style="margin-bottom:8px">
        <label class="h3" for="describe">Describe it in plain English</label>
        <span class="small muted">{ai?.enabled ? "Offline reader, or ask Claude for a second opinion" : "Read on this computer — nothing is sent anywhere"}</span>
      </div>
      <textarea id="describe" class="input" value={text} onInput={(e) => onInput(e.currentTarget.value)}
        placeholder="e.g. I sell Lightroom presets for $15 as instant downloads from photopresets.com, and I'd like a text when someone buys." />
      {d && t ? (
        <div class="stack" style="gap:12px;margin-top:14px">
          <div class="spread">
            <div>
              <div class="silk">{d.source === "claude" ? `Claude suggests (${d.model})` : "Best match"}</div>
              <div class="h2" style="font-size:1.35rem">{t.name}</div>
            </div>
            <div class="row">
              {ai?.enabled && d.source !== "claude" ? <Btn icon="wand" busy={asking} onClick={askClaude}>Ask Claude</Btn> : null}
              <Btn kind="primary" icon="right" onClick={() => onPick(t.id, d)}>Use {t.name}</Btn>
            </div>
          </div>
          <div class="ink2">{d.explanation}</div>
          {d.heard?.length ? (
            <div class="heard" aria-label="What Backplane understood">
              {d.heard.map((h) => <div class="heard-line"><q>{h.phrase}</q><span class="arrow">→</span><span>{h.means}</span></div>)}
            </div>
          ) : null}
          {d.addOns?.length ? <div class="row small"><span class="silk">Add-ons</span>{d.addOns.map((a) => <span class="chip on">{catalog.addOns.find((x) => x.id === a)?.task}</span>)}</div> : null}
          {d.unsupported?.length ? d.unsupported.map((u) => <div class="notice warn"><Icon name="warn" /><div>{u}</div></div>) : null}
          {d.questions?.length ? <div class="small"><span class="silk">Still to decide</span><ul style="margin:4px 0 0;padding-left:18px">{d.questions.map((q) => <li>{q}</li>)}</ul></div> : null}
          {d.alternatives?.length ? (
            <div class="row small"><span class="silk">Also close</span>
              {d.alternatives.map((a) => <button type="button" class="chip" onClick={() => onPick(a.templateId, { ...d, templateId: a.templateId })}>{a.name}</button>)}
            </div>
          ) : null}
        </div>
      ) : !ai?.enabled && text.length > 30 ? (
        <div class="small muted" style="margin-top:8px">Want a second opinion? <button class="linkish" onClick={() => go("settings", { section: "assistant" })}>Connect Claude</button> (optional).</div>
      ) : null}
    </div>
  );
}

// ---- preset spec sheet + guided questions ----

const ENV_OPTIONS = [["development", "Development", "Test keys, safe to break"], ["staging", "Staging", "A rehearsal copy"], ["production", "Production", "Real customers"]];

function PresetForm({ tpl, design, onBack }) {
  const catalog = useStore((s) => s.catalog);
  const settings = useStore((s) => s.settings);
  const practiceOn = useStore((s) => s.practice);
  const boot = useStore((s) => s.boot);
  const [name, setName] = useState(design?.projectName || "");
  const [answers, setAnswers] = useState(() => {
    const a = {};
    for (const q of tpl.questions) if (q.default !== undefined && q.default !== null) a[q.key] = q.default;
    // What's for sale starts as one empty row: Backplane never names it for you.
    for (const q of tpl.questions) if (q.kind === "products") a[q.key] = [emptyItem(q.products)];
    for (const [k, v] of Object.entries(design?.answers || {})) if (k !== "add_ons") a[k] = v;
    return a;
  });
  const [addOns, setAddOns] = useState(new Set(design?.addOns || []));
  const [envs, setEnvs] = useState(new Set(["production"]));
  const [advanced, setAdvanced] = useState(!!settings.showAdvanced);
  const hasReal = (boot?.connections || []).some((c) => !c.practice);
  const [practice, setPractice] = useState(practiceOn && !hasReal);
  const [file, setFile] = useState(null);
  const [errors, setErrors] = useState({});
  const [busy, run] = useAction();
  const [missing, setMissing] = useState(null);

  const set = (k, v) => setAnswers({ ...answers, [k]: v });
  const recommended = catalog.addOns.filter((a) => tpl.addOns.includes(a.id));
  const others = catalog.addOns.filter((a) => !tpl.addOns.includes(a.id));

  const validate = () => {
    const e = {};
    if (!name.trim()) e.__name = "Give it a name you'll recognise";
    for (const q of tpl.questions) {
      const v = answers[q.key];
      if (q.kind === "file") continue;
      if (q.kind === "products") {
        const pe = productErrors(q.products, v);
        if (Object.keys(pe).length) e[q.key] = pe;
        continue;
      }
      if (q.required && (v === undefined || v === null || v === "")) e[q.key] = "Required";
      else if (v && q.kind === "domain" && !/^([a-z0-9-]+\.)+[a-z]{2,}$/i.test(String(v).replace(/^https?:\/\//, "").replace(/\/$/, ""))) e[q.key] = "Enter a domain like example.com";
      else if (v && q.kind === "email" && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(String(v))) e[q.key] = "Enter an email address";
      else if (v && q.kind === "url" && !/^https:\/\//.test(String(v))) e[q.key] = "Use a full https:// address";
      else if (q.kind === "money" && v !== undefined && Number(v) < 0.5) e[q.key] = "At least 0.50";
    }
    if (envs.size === 0) e.__envs = "Choose at least one environment";
    setErrors(e);
    return Object.keys(e).length === 0;
  };

  const create = async () => {
    if (!validate()) {
      toast("warn", "A few answers need attention");
      // Take them to the first one (the products list sits above the fold).
      setTimeout(() => { const el = document.querySelector('.page [aria-invalid="true"]'); el?.scrollIntoView({ block: "center" }); el?.focus(); }, 0);
      return;
    }
    const a = { ...answers, add_ons: [...addOns] };
    delete a.product_file;
    if (practice && !get().practice) {
      await run(() => call("StartPractice"));
      await refreshBoot();
    }
    const res = await run(() => call("CreateProject", { name: name.trim(), templateId: tpl.id, answers: a, environments: [...envs], practice, description: "" }));
    if (file) {
      try {
        await upload(res.project.id, file, () => {});
      } catch (e) {
        toast("warn", "The product file did not upload", e.message + " — you can upload it from the project's Settings tab.", 12000);
      }
    }
    await refreshBoot();
    const env = [...envs].includes("production") ? "production" : [...envs][0];
    if (res.missingConnections?.length && !practice) {
      setMissing({ id: res.project.id, env, providers: res.missingConnections });
      return;
    }
    toast("ok", `${name.trim()} created`, "Next: review the build plan.");
    go("project", { id: res.project.id, env, tab: "build", autoplan: true });
  };

  const productsQ = tpl.questions.find((q) => q.kind === "products");
  const visible = tpl.questions.filter((q) => (!q.advanced || advanced) && q.kind !== "products");
  return (
    <div class="page">
      <div class="page-head">
        <div class="grow">
          <button type="button" class="linkish small" onClick={onBack}><Icon name="left" size={13} /> All presets</button>
          <div class="eyebrow" style="margin-top:8px">{catalog.industries.find((i) => i.id === tpl.industry)?.name}</div>
          <h1>{tpl.name}</h1>
          <p class="lede">{tpl.summary} <span class="muted">{tpl.example}</span></p>
        </div>
      </div>

      {productsQ ? (
        <div class="card" style="margin-bottom:var(--gap)">
          <div class="card-head"><h3>{productsQ.label}</h3><span class="small muted">You can change these after the build on the Products tab.</span></div>
          <ProductsEditor spec={productsQ.products} value={answers.products} onChange={(v) => set("products", v)} currency={answers.currency || "usd"} errors={errors.products || {}} />
          <div class="help small muted" style="margin-top:8px">Checkout only accepts these {productsQ.products.noun}s, and prices are checked on the server — customers can't change what they pay.</div>
        </div>
      ) : null}

      <div class="grid-2" style="align-items:start">
        <div class="stack">
          <div class="card">
            <div class="card-head"><h3>Which server does which job</h3><LevelBadge level={tpl.level} /></div>
            <div class="spec">
              {tpl.stack.map((r) => (
                <div class="spec-row">
                  <div class="task">{r.task}</div>
                  <div class="server"><span class="chip"><b>{providerName(r.provider)}</b></span><span class="small ink2">{r.service}</span></div>
                  <div class="why">{r.why}</div>
                </div>
              ))}
            </div>
            <div class="divider" />
            <div class="small ink2">{tpl.levelNote}</div>
            <div class="flow mono small muted" style="margin-top:10px">{tpl.flow.join("  →  ")}</div>
          </div>

          <div class="card">
            <div class="card-head"><h3>Add-ons</h3><span class="small muted">Optional extra servers for specific jobs</span></div>
            <div class="stack" style="gap:10px">
              {recommended.map((a) => <AddOnRow a={a} on={addOns.has(a.id)} toggle={() => { const n = new Set(addOns); n.has(a.id) ? n.delete(a.id) : n.add(a.id); setAddOns(n); }} />)}
              {advanced ? others.map((a) => <AddOnRow a={a} on={addOns.has(a.id)} toggle={() => { const n = new Set(addOns); n.has(a.id) ? n.delete(a.id) : n.add(a.id); setAddOns(n); }} />) : null}
              <div class="small muted">Add-ons are connected and watched as part of this backend's health. Backplane doesn't create anything inside them yet.</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-head"><h3>A few questions</h3><Toggle id="adv" checked={advanced} onChange={setAdvanced} label="Advanced" /></div>
          <div class="stack">
            <Field label="Backend name" error={errors.__name} help="Shown in Backplane; resource names use a short version of it.">
              {(id) => <input id={id} class="input" value={name} onInput={(e) => setName(e.currentTarget.value)} placeholder="e.g. Photo Presets Store" aria-invalid={errors.__name ? "true" : undefined} />}
            </Field>
            {visible.map((q) => <Question q={q} value={answers[q.key]} error={errors[q.key]} onChange={(v) => set(q.key, v)} file={file} setFile={setFile} currency={answers.currency} />)}
            <div class="field">
              <span class="label">Environments</span>
              <div class="stack" style="gap:6px">
                {ENV_OPTIONS.map(([v, l, h]) => (
                  <label class="row" style="gap:10px;cursor:pointer">
                    <input type="checkbox" checked={envs.has(v)} onChange={() => { const n = new Set(envs); n.has(v) ? n.delete(v) : n.add(v); setEnvs(n); }} />
                    <b>{l}</b><span class="small muted">{h}</span>
                  </label>
                ))}
              </div>
              {errors.__envs ? <div class="err"><Icon name="warn" size={15} />{errors.__envs}</div> : <div class="help">Each environment gets its own resources and can use its own accounts (test keys for development, live keys for production).</div>}
            </div>
            <div class="card flat well" style="padding:12px 14px">
              <Toggle id="practice" checked={practice} onChange={setPractice} label={<b>Practice run</b>} />
              <div class="small ink2" style="margin-top:4px">Builds against Backplane's built-in simulator of Cloudflare, Supabase, Stripe, Resend and GitHub — free, private, and safe to break. {!practiceOn && practice ? "Practice mode starts when you create it." : ""}</div>
            </div>
            <div class="row" style="justify-content:flex-end">
              <Btn kind="primary" size="lg" icon="bolt" busy={busy} onClick={create}>Create backend</Btn>
            </div>
            <div class="small muted" style="text-align:right">Next you'll see the exact plan. Nothing is created until you approve it.</div>
          </div>
        </div>
      </div>
      {missing ? <MissingModal m={missing} onClose={() => setMissing(null)} /> : null}
    </div>
  );
}

function AddOnRow({ a, on, toggle }) {
  return (
    <label class="row" style="gap:12px;align-items:flex-start;cursor:pointer">
      <input type="checkbox" checked={on} onChange={toggle} style="margin-top:4px" />
      <div>
        <div class="row tight"><b>{a.task}</b><span class="chip">{providerName(a.provider)}</span></div>
        <div class="small ink2">{a.why}</div>
      </div>
    </label>
  );
}

export function Question({ q, value, error, onChange, file, setFile, currency }) {
  if (q.kind === "products") {
    return (
      <div class="field">
        <span class="label">{q.label}</span>
        <ProductsEditor spec={q.products} value={value} onChange={onChange} currency={currency || "usd"} errors={error || {}} />
        {q.help ? <div class="help">{q.help}</div> : null}
      </div>
    );
  }
  if (q.kind === "toggle") {
    return <div class="field"><Toggle id={"q-" + q.key} checked={!!value} onChange={onChange} label={<b>{q.label}</b>} />{q.help ? <div class="help">{q.help}</div> : null}</div>;
  }
  return (
    <Field label={q.label + (q.required ? "" : " (optional)")} help={q.help} error={error}>
      {(id) => {
        switch (q.kind) {
          case "select":
            return <select id={id} class="select" value={value ?? ""} onChange={(e) => onChange(e.currentTarget.value)}>{q.options.map((o) => <option value={o}>{o.toUpperCase()}</option>)}</select>;
          case "money":
          case "number":
            return <input id={id} class="input num" type="number" step={q.kind === "money" ? "0.01" : "1"} min="0" value={value ?? ""} aria-invalid={error ? "true" : undefined}
              onInput={(e) => onChange(e.currentTarget.value === "" ? undefined : Number(e.currentTarget.value))} />;
          case "file":
            return (
              <div class="row">
                <input id={id} type="file" onChange={(e) => setFile(e.currentTarget.files?.[0] || null)} />
                {file ? <span class="small muted">{(file.size / 1048576).toFixed(1)} MB</span> : null}
              </div>
            );
          default:
            return <input id={id} class="input" type={q.kind === "email" ? "email" : q.kind === "url" ? "url" : "text"} value={value ?? ""} aria-invalid={error ? "true" : undefined}
              placeholder={q.placeholder || (q.kind === "domain" ? "example.com" : q.kind === "url" ? "https://example.com" : "")} onInput={(e) => onChange(e.currentTarget.value)} />;
        }
      }}
    </Field>
  );
}

function MissingModal({ m, onClose }) {
  return (
    <Modal title="Connect these accounts next" eyebrow="Almost there" onClose={onClose}
      foot={<><Btn onClick={() => go("project", { id: m.id, env: m.env })}>Open the project</Btn><Btn kind="primary" icon="plug" onClick={() => go("accounts", { add: m.providers[0], then: { id: m.id, env: m.env } })}>Connect {providerName(m.providers[0])}</Btn></>}>
      <p class="lede">The backend is saved. To build it, Backplane needs access to:</p>
      <div class="chips">{m.providers.map((p) => <span class="chip"><b>{providerName(p)}</b></span>)}</div>
      <p class="small ink2">Each connection takes a minute: the guide opens the right page with the exact permissions pre-selected where the provider supports it. Keys are encrypted on this computer.</p>
    </Modal>
  );
}
