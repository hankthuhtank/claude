import { call } from "./api.js";
import { set } from "./state.js";

export function applySettings(s) {
  const b = document.body;
  let theme = s?.theme || "rack";
  if (theme === "system") theme = matchMedia("(prefers-color-scheme: light)").matches ? "aluminum" : "rack";
  b.dataset.theme = theme;
  b.dataset.density = s?.density || "comfortable";
  b.dataset.motion = s?.reduceMotion ? "reduce" : "full";
  document.documentElement.style.setProperty("--scale", String(s?.textScale || 1));
}

export async function refreshBoot() {
  const b = await call("Bootstrap");
  set({ boot: b, settings: b.settings, practice: b.practiceRunning });
  return b;
}
