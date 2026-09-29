// Hand-drawn 24px line icons (stroke = currentColor).
const P = {
  rack: <><rect x="4" y="3.5" width="16" height="5" rx="1" /><rect x="4" y="10" width="16" height="5" rx="1" /><rect x="4" y="16.5" width="16" height="4" rx="1" /><path d="M15 6h2M15 12.5h2" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  plug: <><path d="M9 3v5M15 3v5M6.5 8h11v3a5.5 5.5 0 0 1-11 0z" /><path d="M12 16.5V21" /></>,
  tape: <><rect x="3.5" y="4" width="17" height="16" rx="1.5" /><path d="M7 8.5h10M7 12h7M7 15.5h9" /></>,
  bench: <><path d="M3 19h18M5 19v-4h14v4" /><path d="M9 15V9l3-4 3 4v6" /><path d="M12 5v-1" /></>,
  binder: <><path d="M6 3.5h11a1.5 1.5 0 0 1 1.5 1.5v15H7a1.5 1.5 0 0 1-1.5-1.5v-13A2 2 0 0 1 6 3.5z" /><path d="M5.5 17.5A1.5 1.5 0 0 1 7 16h11.5" /><path d="M10 8h5" /></>,
  gear: <><circle cx="12" cy="12" r="3" /><path d="M12 2.8v2.4M12 18.8v2.4M4.2 7.5l2 1.2M17.8 15.3l2 1.2M4.2 16.5l2-1.2M17.8 8.7l2-1.2" /><circle cx="12" cy="12" r="7" /></>,
  check: <path d="M4.5 12.5l5 5 10-11" />,
  warn: <><path d="M12 3.5l9.5 16.5h-19z" /><path d="M12 10v4.5M12 17.2v.3" /></>,
  x: <path d="M6 6l12 12M18 6L6 18" />,
  tag: <><path d="M3.5 12.2V4.5a1 1 0 0 1 1-1h7.7l8.3 8.3a1.5 1.5 0 0 1 0 2.1l-6.1 6.1a1.5 1.5 0 0 1-2.1 0z" /><circle cx="8" cy="8" r="1.4" /></>,
  fail: <><circle cx="12" cy="12" r="9" /><path d="M8.5 8.5l7 7M15.5 8.5l-7 7" /></>,
  ok: <><circle cx="12" cy="12" r="9" /><path d="M7.8 12.3l3 3 5.5-6" /></>,
  dot: <circle cx="12" cy="12" r="4" />,
  ring: <circle cx="12" cy="12" r="8" />,
  right: <path d="M9 5l7 7-7 7" />,
  left: <path d="M15 5l-7 7 7 7" />,
  down: <path d="M5 9l7 7 7-7" />,
  bolt: <path d="M13 2.5L5 13.5h6l-1 8 8-11h-6z" />,
  refresh: <><path d="M19.5 11A7.5 7.5 0 0 0 6 7.2L4.5 9" /><path d="M4.5 4.5V9H9" /><path d="M4.5 13A7.5 7.5 0 0 0 18 16.8l1.5-1.8" /><path d="M19.5 19.5V15H15" /></>,
  search: <><circle cx="11" cy="11" r="6.5" /><path d="M16 16l4.5 4.5" /></>,
  ext: <><path d="M13.5 4.5h6v6" /><path d="M19.5 4.5L11 13" /><path d="M17 14v5.5H4.5V7H10" /></>,
  folder: <path d="M3.5 6.5a1.5 1.5 0 0 1 1.5-1.5h4l2 2.5h8a1.5 1.5 0 0 1 1.5 1.5v9.5a1.5 1.5 0 0 1-1.5 1.5H5a1.5 1.5 0 0 1-1.5-1.5z" />,
  download: <><path d="M12 4v11M7 10.5l5 5 5-5" /><path d="M4.5 19.5h15" /></>,
  upload: <><path d="M12 15.5v-11M7 9l5-5 5 5" /><path d="M4.5 19.5h15" /></>,
  trash: <><path d="M4.5 7h15M10 4h4M6.5 7l1 13h9l1-13" /><path d="M10 11v5.5M14 11v5.5" /></>,
  lock: <><rect x="5" y="10.5" width="14" height="10" rx="1.5" /><path d="M8 10.5V7.5a4 4 0 0 1 8 0v3" /></>,
  key: <><circle cx="8" cy="15" r="4" /><path d="M11 12l8.5-8.5M16 7l2.5 2.5M14 9l2 2" /></>,
  cable: <><path d="M4 20v-5a4 4 0 0 1 4-4h8a4 4 0 0 0 4-4V4" /><rect x="2.5" y="17.5" width="3" height="4" rx=".5" /><rect x="18.5" y="2.5" width="3" height="4" rx=".5" /></>,
  clock: <><circle cx="12" cy="12" r="8.5" /><path d="M12 7.5V12l3 2" /></>,
  shield: <><path d="M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z" /><path d="M8.5 12l2.5 2.5 4.5-5" /></>,
  undo: <><path d="M9 14L4 9l5-5" /><path d="M4 9h10a6 6 0 0 1 0 12h-3" /></>,
  play: <path d="M7 4.5l12 7.5-12 7.5z" />,
  stop: <rect x="6" y="6" width="12" height="12" rx="1.5" />,
  info: <><circle cx="12" cy="12" r="9" /><path d="M12 11v5.5M12 7.8v.3" /></>,
  copy: <><rect x="8.5" y="8.5" width="11" height="11" rx="1.5" /><path d="M15.5 8.5v-3a1 1 0 0 0-1-1h-9a1 1 0 0 0-1 1v9a1 1 0 0 0 1 1h3" /></>,
  wand: <><path d="M4 20L15.5 8.5" /><path d="M14 5.5l1-2.5 1 2.5 2.5 1-2.5 1-1 2.5-1-2.5-2.5-1z" /><path d="M19 12.5v3M17.5 14h3" /></>,
  code: <path d="M8.5 7L3.5 12l5 5M15.5 7l5 5-5 5M13.5 4.5l-3 15" />,
  pulse: <path d="M3 12h4l2.5-6 4 12 2.5-6h5" />,
  box: <><path d="M3.5 7.5L12 3l8.5 4.5v9L12 21l-8.5-4.5z" /><path d="M3.5 7.5L12 12l8.5-4.5M12 12v9" /></>,
  flask: <><path d="M9.5 3.5h5M10.5 3.5v6L5 19a1.5 1.5 0 0 0 1.3 2.3h11.4A1.5 1.5 0 0 0 19 19l-5.5-9.5v-6" /><path d="M7.5 15h9" /></>,
  import: <><path d="M12 3.5v10M7.5 9l4.5 4.5L16.5 9" /><path d="M4.5 14v5.5h15V14" /></>,
  eye: <><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" /><circle cx="12" cy="12" r="3" /></>,
};

export function Icon({ name, size, title, class: cls }) {
  return (
    <svg viewBox="0 0 24 24" width={size || 20} height={size || 20} fill="none" stroke="currentColor" stroke-width="1.8"
      stroke-linecap="round" stroke-linejoin="round" aria-hidden={title ? undefined : "true"} role={title ? "img" : undefined} class={cls}>
      {title ? <title>{title}</title> : null}
      {P[name] || P.dot}
    </svg>
  );
}

export const statusIcon = { ok: "ok", warn: "warn", fail: "fail", unknown: "ring", skipped: "ring", off: "ring" };
