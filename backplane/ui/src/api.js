// Talks to the local Backplane service. The per-launch token arrives in the
// URL fragment, is kept in memory and sessionStorage only, and is removed
// from the address bar immediately.

let token = "";
{
  const m = /(?:^|[#&])t=([a-f0-9]{64})/.exec(location.hash);
  if (m) {
    token = m[1];
    try { sessionStorage.setItem("bp-token", token); } catch {}
    history.replaceState(null, "", location.pathname);
  } else {
    try { token = sessionStorage.getItem("bp-token") || ""; } catch {}
  }
}

export class CallError extends Error {
  constructor(body) {
    super(body?.message || "Something went wrong");
    this.problem = body?.problem || null;
    this.confirm = !!body?.confirm;
  }
}

export async function call(method, params) {
  let res;
  try {
    res = await fetch(`/api/call/${method}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Backplane-Token": token },
      body: JSON.stringify(params ?? {}),
    });
  } catch {
    throw new CallError({ message: "Backplane's local service is not responding. Restart the app if this continues." });
  }
  if (res.status === 401) throw new CallError({ message: "This window lost its session. Close and reopen Backplane." });
  const body = await res.json().catch(() => null);
  if (!body) throw new CallError({ message: `Unexpected response (${res.status})` });
  if (!body.ok) throw new CallError(body.error);
  return body.result;
}

export async function upload(projectId, file, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/upload?project=${encodeURIComponent(projectId)}&name=${encodeURIComponent(file.name)}`);
    xhr.setRequestHeader("X-Backplane-Token", token);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total);
    xhr.onload = () => {
      try {
        const body = JSON.parse(xhr.responseText);
        body.ok ? resolve(body.result) : reject(new CallError(body.error));
      } catch { reject(new CallError({ message: "Upload failed" })); }
    };
    xhr.onerror = () => reject(new CallError({ message: "Upload failed" }));
    xhr.send(file);
  });
}

const listeners = new Set();
let source = null;

export function onEvent(fn) {
  listeners.add(fn);
  if (!source) connect();
  return () => listeners.delete(fn);
}

function connect() {
  source = new EventSource(`/api/events?t=${token}`);
  source.onmessage = (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch { return; }
    for (const fn of listeners) {
      try { fn(ev); } catch (e) { console.error(e); }
    }
  };
  source.onerror = () => { /* EventSource reconnects on its own (retry: 2000) */ };
}
