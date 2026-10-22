export interface PanelProps { appName: string; installId: number; projectId: string }
export interface CertificateStatus { configured: boolean; valid: boolean; hostname: string; port: number; issuer?: string; expires_at?: number; days_remaining?: number; fingerprint_sha256?: string }
export interface RelayStatus {
  state: string; ready: boolean; admitting: boolean; active_allocations: number; max_allocations: number;
  public_host: string; public_ip: string; port: number; udp_port: number; tcp_port: number; tls_port: number; tls_ports: number[]; transports: string[];
  relay_port_min: number; relay_port_max: number; bytes_sent: number; bytes_received: number;
  bytes_dropped: number; credentials_issued: number; tls: boolean; tls_certificate: CertificateStatus;
  configuration: Record<string, string>;
  managed_tls: {mode: string; automatic: boolean; storage_key_configured: boolean; phase?: string; message?: string; next_check?: number; last_success?: number};
}
export interface Credentials { expires_at: number; ttl_seconds: number; ice_servers: { urls: string[]; username: string; credential: string }[] }

export function endpoint(installId: number, projectId: string, path: string): string {
  if (!Number.isSafeInteger(installId) || installId < 1) throw new Error("TURN installation is unavailable.");
  const query = projectId ? `?${new URLSearchParams({ project_id: projectId })}` : "";
  return `/api/apps/turn/_install/${installId}${path}${query}`;
}

export async function request<T>(url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { method: body === undefined ? "GET" : "POST", credentials: "same-origin", cache: "no-store", signal,
    headers: body === undefined ? undefined : { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() }, body: body === undefined ? undefined : JSON.stringify(body) });
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
  const validation = await request<{stop_before_save?:boolean}>(endpoint(props.installId, props.projectId, "/settings/validate"), { config: patch });
  if (validation.stop_before_save) {
    // The native backend reserves the fixed media pool before preparing a
    // replacement. Confirm stop before persistence queues its restart.
    const stopped = await request<{id:string}>(`/api/apps/installs/${props.installId}/deployment/stop`, {});
    const deadline = Date.now()+90000;
    for (;;) {
      const result = await request<{status:string;error?:string}>(`/api/apps/installs/${props.installId}/deployment/operations/${encodeURIComponent(stopped.id)}`);
      if (result.status === "succeeded") break;
      if (["failed","cancelled"].includes(result.status) || Date.now()>deadline) throw new Error("TURN stop was not confirmed. Settings were not saved; inspect deployment controls.");
      await new Promise(resolve => setTimeout(resolve,500));
    }
  }
  const response = await fetch(`/api/apps/installs/${props.installId}/config`, { method: "PUT", credentials: "same-origin", cache: "no-store",
    headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ config: patch }) });
  if (!response.ok) throw new Error((await response.text()).slice(0, 400) || "Could not save TURN settings.");
  // The platform may return the complete decrypted configuration. Keep only the
  // restart acknowledgement; never put its configuration into React state/logs.
  const result = await response.json() as { restarted?: boolean };
  return result.restarted === true;
}

export function bytes(value: number): string { if (!value) return "0 B"; const unit = Math.min(3, Math.floor(Math.log(value) / Math.log(1024))); return `${(value / 1024 ** unit).toFixed(unit ? 1 : 0)} ${["B", "KiB", "MiB", "GiB"][unit]}`; }

export function managedSettings(patch: Record<string,string>, draft: Record<string,string>, storageKeyConfigured: boolean, projectId: string): Record<string,string> {
  if (draft.tls_mode !== "managed") return patch;
  if (!storageKeyConfigured) patch.tls_storage_key = Array.from(crypto.getRandomValues(new Uint8Array(48)), byte => byte.toString(16).padStart(2, "0")).join("");
  if (!draft.dns_project_id && projectId) patch.dns_project_id = projectId;
  return patch;
}

export function connectionOptions(status: Pick<RelayStatus, "tls" | "tls_ports">): {value:string;label:string}[] {
  return [{value:"all",label:"All transports (automatic ICE)"},{value:"udp",label:"UDP only"},{value:"tcp",label:"TCP only"},
    ...(status.tls ? status.tls_ports.map(port => ({value:`tls:${port}`,label:`TLS only · port ${port}`})) : [])];
}
export function transportRequest(selection: string): {transport:string;tls_port?:number} {
  if (["all","udp","tcp"].includes(selection)) return {transport:selection};
  const match = /^tls:([0-9]+)$/.exec(selection);
  const port = Number(match?.[1]);
  if (!match || !Number.isInteger(port) || port<1 || port>65535) throw new Error("Choose a valid test transport.");
  return {transport:"tls",tls_port:port};
}
export function relayTestConfiguration(result: Credentials) {
  return {iceServers:result.ice_servers,iceTransportPolicy:"relay" as const};
}
