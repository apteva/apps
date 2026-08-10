export const BACKEND_LABEL: Record<string, string> = {
  local: "Local Chrome",
  browserbase: "Browserbase",
  steel: "Steel",
  "browser-engine": "Browser Engine",
  service: "Browser Service",
};

export function appURL(path: string, projectId?: string, extra?: Record<string, string | number | undefined>) {
  const url = new URL(path, window.location.origin);
  if (projectId) url.searchParams.set("project_id", projectId);
  for (const [key, value] of Object.entries(extra ?? {})) {
    if (value !== undefined) url.searchParams.set(key, String(value));
  }
  return url.pathname + url.search;
}

export function sessionURL(sessionId: string, resource: string, projectId?: string) {
  return appURL(
    `/api/apps/computer/sessions/${encodeURIComponent(sessionId)}/${resource}`,
    projectId,
  );
}

export function panelURL(sessionId: string) {
  return `/apps/computer/?instance=${encodeURIComponent(sessionId)}`;
}

export function hostFor(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return raw || "-";
  }
}

export const PREVIEW_SCREENSHOT =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 800"><rect width="1280" height="800" fill="#f5f5f4"/><rect x="170" y="120" width="940" height="560" rx="8" fill="white" stroke="#d6d3d1"/><h1/><text x="640" y="315" text-anchor="middle" fill="#111827" font-family="Georgia,serif" font-size="54" font-weight="700">Example Domain</text><text x="640" y="380" text-anchor="middle" fill="#4b5563" font-family="Arial,sans-serif" font-size="22">This domain is for use in illustrative examples.</text><text x="640" y="440" text-anchor="middle" fill="#2563eb" font-family="Arial,sans-serif" font-size="20">More information…</text></svg>`,
  );
