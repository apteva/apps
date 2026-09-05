export function parseJSON(s?: string): Record<string, unknown> {
  try { const value = JSON.parse(s || "{}"); return value && typeof value === "object" && !Array.isArray(value) ? value : {}; }
  catch { return {}; }
}
export function corsEnabledFrom(s?: string): boolean {
  const obj = parseJSON(s);
  if (obj.enabled !== undefined) return obj.enabled === true || obj.enabled === "true";
  return corsOriginsFrom(s).length > 0;
}
export function corsOriginsFrom(s?: string): string[] {
  const obj = parseJSON(s);
  if (Array.isArray(obj.origins)) return obj.origins.filter((value): value is string => typeof value === "string");
  if (typeof obj.origins === "string") return obj.origins.split(",").map(s => s.trim()).filter(Boolean);
  return typeof obj.allow_origin === "string" && obj.allow_origin !== "*" ? [obj.allow_origin] : [];
}
export function corsCredentialsFrom(s?: string): boolean {
  const obj = parseJSON(s);
  const value = obj.credentials ?? obj.allow_credentials;
  return value === true || value === "true";
}
export function updatedCORS(previous: string | undefined, enabled: boolean, origins: string, credentials: boolean) {
  const { allow_origin, allow_credentials, ...preserved } = parseJSON(previous);
  return { ...preserved, enabled, origins: origins.split(/[\s,]+/).filter(Boolean), credentials };
}

// Cancellation plus a generation guard handles fetchers which finish parsing
// or resolve despite an aborted signal. A former owner can never publish data.
export class LatestRequest {
  private epoch = 0;
  private controller?: AbortController;
  begin() {
    this.cancel();
    const epoch = this.epoch;
    this.controller = new AbortController();
    return { signal: this.controller.signal, current: () => epoch === this.epoch };
  }
  cancel() { this.epoch++; this.controller?.abort(); }
}

export async function fetchPanelRows(fetcher: typeof fetch, url: string, field: string, signal: AbortSignal) {
  const response = await fetcher(url, { credentials: "same-origin", signal });
  if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
  const data = await response.json();
  if (data[field] != null && !Array.isArray(data[field])) throw new Error(`Invalid ${field} response`);
  return data[field] || [];
}
