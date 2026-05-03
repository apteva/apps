// Same-origin fetch wrapper. The trading sidecar is reverse-proxied
// at /api/apps/trading/* by apteva-server, so cookies + the user's
// session token authenticate every request without extra plumbing.

const API = "/api/apps/trading";

declare const __PROJECT_ID__: string | undefined;

function projectId(): string {
  // Build-time injection (set via $API_BASE/$PROJECT_ID), or runtime
  // injection from the platform via window.__PROJECT_ID__. Falls back
  // to the URL ?project_id= the dashboard appends.
  if (typeof window !== "undefined") {
    const w = (window as any).__PROJECT_ID__;
    if (typeof w === "string" && w) return w;
    const fromQuery = new URLSearchParams(window.location.search).get("project_id");
    if (fromQuery) return fromQuery;
  }
  if (typeof __PROJECT_ID__ !== "undefined" && __PROJECT_ID__) return __PROJECT_ID__;
  return "";
}

function withProject(path: string, extra: Record<string, string | number | undefined> = {}): string {
  const u = new URL(API + path, window.location.origin);
  const pid = projectId();
  if (pid) u.searchParams.set("project_id", pid);
  for (const [k, v] of Object.entries(extra)) {
    if (v !== undefined && v !== null && v !== "") u.searchParams.set(k, String(v));
  }
  return u.toString();
}

export async function apiGet<T>(path: string, params: Record<string, string | number | undefined> = {}): Promise<T> {
  const r = await fetch(withProject(path, params), { credentials: "same-origin" });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`${r.status}: ${text || r.statusText}`);
  }
  return r.json();
}

export async function apiPost<T>(path: string, body: unknown): Promise<T> {
  const r = await fetch(withProject(path), {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`${r.status}: ${text || r.statusText}`);
  }
  return r.json();
}
