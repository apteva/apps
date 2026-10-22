export interface PanelProps { appName: string; installId: number; projectId: string }
export interface CertificateStatus { configured: boolean; valid: boolean; hostname: string; port: number; issuer?: string; expires_at?: number; days_remaining?: number; fingerprint_sha256?: string }
export interface RelayStatus {
  state: string; ready: boolean; admitting: boolean; active_allocations: number; max_allocations: number;
  public_host: string; public_ip: string; port: number; tls_port: number; transports: string[];
  relay_port_min: number; relay_port_max: number; bytes_sent: number; bytes_received: number;
  bytes_dropped: number; credentials_issued: number; tls: boolean; tls_certificate: CertificateStatus;
  configuration: Record<string, string>;
}
export interface Credentials { expires_at: number; ttl_seconds: number; ice_servers: { urls: string[]; username: string; credential: string }[] }

export function endpoint(installId: number, projectId: string, path: string): string {
  if (!Number.isSafeInteger(installId) || installId < 1) throw new Error("TURN installation is unavailable.");
  const query = projectId ? `?${new URLSearchParams({ project_id: projectId })}` : "";
  return `/api/apps/turn/_install/${installId}${path}${query}`;
}

export async function request<T>(url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { method: body === undefined ? "GET" : "POST", credentials: "same-origin", cache: "no-store", signal,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await response.text();
  if (!response.ok) throw new Error(text.slice(0, 400) || `Request failed (${response.status}).`);
  return JSON.parse(text) as T;
}

export function settingsPatch(original: Record<string, string>, draft: Record<string, string>, certificate: string, privateKey: string, secret: string): Record<string, string> {
  const patch = Object.fromEntries(Object.entries(draft).filter(([key, value]) => value !== original[key]));
  if (certificate.trim() || privateKey.trim()) {
    if (!certificate.trim() || !privateKey.trim()) throw new Error("Supply both the certificate chain and its private key.");
    patch.tls_certificate_pem = certificate.trim(); patch.tls_private_key_pem = privateKey.trim();
  }
  if (secret) patch.shared_secret = secret;
  return patch;
}

export async function saveSettings(props: PanelProps, patch: Record<string, string>): Promise<boolean> {
  await request(endpoint(props.installId, props.projectId, "/settings/validate"), { config: patch });
  const response = await fetch(`/api/apps/installs/${props.installId}/config`, { method: "PUT", credentials: "same-origin", cache: "no-store",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify({ config: patch }) });
  if (!response.ok) throw new Error((await response.text()).slice(0, 400) || "Could not save TURN settings.");
  // The platform may return the complete decrypted configuration. Keep only the
  // restart acknowledgement; never put its configuration into React state/logs.
  const result = await response.json() as { restarted?: boolean };
  return result.restarted === true;
}

export function bytes(value: number): string { if (!value) return "0 B"; const unit = Math.min(3, Math.floor(Math.log(value) / Math.log(1024))); return `${(value / 1024 ** unit).toFixed(unit ? 1 : 0)} ${["B", "KiB", "MiB", "GiB"][unit]}`; }
