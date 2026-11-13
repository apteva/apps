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

/** Host-injected routing identity; never forward it to a provider origin. */
export function scopeComputerURL(raw: string, projectId?: string, installId?: number, appName = "computer") {
  if (!raw) return raw;
  const url = new URL(raw, window.location.origin);
  if (url.origin !== window.location.origin || !/^\/(?:api\/apps|apps)\/computer(?:\/|$)/.test(url.pathname)) return raw;
  url.pathname = url.pathname.replace(/^(\/(?:api\/apps|apps)\/)computer(?=\/|$)/, `$1${encodeURIComponent(appName)}`);
  if (projectId) url.searchParams.set("project_id", projectId);
  if (Number.isSafeInteger(installId) && installId! > 0) url.searchParams.set("install_id", String(installId));
  return url.pathname + url.search + url.hash;
}

export function sessionURL(sessionId: string, resource: string, projectId?: string, installId?: number, appName = "computer") {
  return scopeComputerURL(`/api/apps/computer/sessions/${encodeURIComponent(sessionId)}/${resource}`, projectId, installId, appName);
}

export function panelURL(sessionId: string, projectId?: string, installId?: number, appName = "computer") {
  return scopeComputerURL(`/apps/computer/?instance=${encodeURIComponent(sessionId)}`, projectId, installId, appName);
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
