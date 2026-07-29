import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/tunnel";

interface PanelProps {
  projectId?: string;
}

interface DNSStatus {
  managed: boolean;
  domain?: string;
  name?: string;
  type?: string;
  value?: string;
  error?: string;
}

interface TunnelConfig {
  configured: boolean;
  base_domain?: string;
  can_configure?: boolean;
  domains_bound: boolean;
  dns: DNSStatus;
}

interface Tunnel {
  id: string;
  name: string;
  hostname: string;
  url: string;
  connected: boolean;
  request_count: number;
  bytes_in: number;
  bytes_out: number;
  last_connected_at?: string;
  created_at: string;
}

interface Stats {
  active_tunnels: number;
  connected_tunnels: number;
  max_tunnels: number;
  request_count: number;
  bytes_in: number;
  bytes_out: number;
}

interface Credential {
  tunnel: Tunnel;
  connector_token: string;
  connect_url: string;
  notice: string;
}

const emptyConfig: TunnelConfig = {
  configured: false,
  can_configure: true,
  domains_bound: false,
  dns: { managed: false },
};

const emptyStats: Stats = {
  active_tunnels: 0,
  connected_tunnels: 0,
  max_tunnels: 0,
  request_count: 0,
  bytes_in: 0,
  bytes_out: 0,
};

export default function TunnelPanel({ projectId = "" }: PanelProps) {
  const [config, setConfig] = useState<TunnelConfig>(emptyConfig);
  const [tunnels, setTunnels] = useState<Tunnel[]>([]);
  const [stats, setStats] = useState<Stats>(emptyStats);
  const [baseDomain, setBaseDomain] = useState("");
  const [dnsTarget, setDnsTarget] = useState("");
  const [name, setName] = useState("");
  const [credential, setCredential] = useState<Credential | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");

  const appURL = useCallback((path: string) => {
    if (!projectId) return `${API}${path}`;
    const separator = path.includes("?") ? "&" : "?";
    return `${API}${path}${separator}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [configResponse, tunnelsResponse, statsResponse] = await Promise.all([
        fetch(appURL("/admin/config"), { credentials: "same-origin" }),
        fetch(appURL("/admin/tunnels"), { credentials: "same-origin" }),
        fetch(appURL("/admin/stats"), { credentials: "same-origin" }),
      ]);
      if (!configResponse.ok || !tunnelsResponse.ok || !statsResponse.ok) {
        throw new Error(`Tunnel returned ${[configResponse, tunnelsResponse, statsResponse].find((r) => !r.ok)?.status}`);
      }
      const nextConfig = await configResponse.json() as TunnelConfig;
      const tunnelBody = await tunnelsResponse.json() as { tunnels?: Tunnel[] };
      setConfig(nextConfig);
      setBaseDomain(nextConfig.base_domain || "");
      setTunnels(tunnelBody.tunnels || []);
      setStats(await statsResponse.json() as Stats);
      setMessage("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not load Tunnel");
    } finally {
      setLoading(false);
    }
  }, [appURL]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const configure = useCallback(async () => {
    setBusy("configure");
    setMessage("Configuring tunnel domain…");
    try {
      const response = await fetch(appURL("/admin/config"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          base_domain: baseDomain.trim(),
          project_id: projectId,
          auto_dns: true,
          dns_target: dnsTarget.trim(),
        }),
      });
      const body = await response.json().catch(() => ({})) as TunnelConfig & { error?: string };
      if (!response.ok) throw new Error(body.error || `Configuration failed (${response.status})`);
      setConfig(body);
      setMessage(body.dns.error ? `Domain saved. ${body.dns.error}` : "Tunnel domain configured");
      await reload();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not configure domain");
    } finally {
      setBusy("");
    }
  }, [appURL, baseDomain, dnsTarget, projectId, reload]);

  const createTunnel = useCallback(async () => {
    setBusy("create");
    setMessage("Reserving tunnel…");
    try {
      const response = await fetch(appURL("/admin/tunnels"), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), project_id: projectId }),
      });
      const body = await response.json().catch(() => ({})) as Credential & { error?: string };
      if (!response.ok) throw new Error(body.error || `Reservation failed (${response.status})`);
      setCredential(body);
      setName("");
      setMessage("Tunnel reserved. Save the connector token now.");
      await reload();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not reserve tunnel");
    } finally {
      setBusy("");
    }
  }, [appURL, name, projectId, reload]);

  const rotate = useCallback(async (item: Tunnel) => {
    if (!window.confirm(`Rotate the connector token for ${item.hostname}? The current connector will disconnect.`)) return;
    setBusy(item.id);
    try {
      const response = await fetch(appURL(`/admin/tunnels/${encodeURIComponent(item.id)}/rotate`), {
        method: "POST",
        credentials: "same-origin",
      });
      const body = await response.json().catch(() => ({})) as Credential & { error?: string };
      if (!response.ok) throw new Error(body.error || `Rotation failed (${response.status})`);
      setCredential(body);
      setMessage("Token rotated. Save the new token now.");
      await reload();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not rotate token");
    } finally {
      setBusy("");
    }
  }, [appURL, reload]);

  const remove = useCallback(async (item: Tunnel) => {
    if (!window.confirm(`Delete ${item.hostname}? Its URL and connector token will stop working.`)) return;
    setBusy(item.id);
    try {
      const response = await fetch(appURL(`/admin/tunnels/${encodeURIComponent(item.id)}`), {
        method: "DELETE",
        credentials: "same-origin",
      });
      const body = await response.json().catch(() => ({})) as { error?: string; warning?: string };
      if (!response.ok && response.status !== 202) throw new Error(body.error || `Delete failed (${response.status})`);
      setMessage(body.warning || "Tunnel deleted");
      await reload();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not delete tunnel");
    } finally {
      setBusy("");
    }
  }, [appURL, reload]);

  const dnsRecord = useMemo(() => {
    const dns = config.dns;
    if (!dns.name || !dns.type || !dns.value) return "";
    return `${dns.name}.${dns.domain}  ${dns.type}  ${dns.value}`;
  }, [config.dns]);

  if (loading) {
    return <div className="p-6 text-sm text-text-muted">Loading Tunnel…</div>;
  }

  return (
    <div className="mx-auto max-w-5xl space-y-5 p-4 sm:p-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div className="text-[11px] font-semibold uppercase tracking-[0.18em] text-accent">Self-hosted edge</div>
          <h1 className="mt-1 text-xl font-bold text-text">Tunnel</h1>
          <p className="mt-1 max-w-2xl text-sm text-text-muted">
            Stable public HTTPS URLs carried to local services through outbound encrypted connections.
          </p>
        </div>
        <div className="flex gap-2 text-xs">
          <Metric label="Online" value={`${stats.connected_tunnels}/${stats.active_tunnels}`} />
          <Metric label="Requests" value={compactNumber(stats.request_count)} />
          <Metric label="Transfer" value={formatBytes(stats.bytes_in + stats.bytes_out)} />
        </div>
      </header>

      {message && (
        <div className="rounded-lg border border-border bg-bg-card px-3 py-2 text-xs text-text-muted" role="status">
          {message}
        </div>
      )}

      <section className="rounded-xl border border-border bg-bg-card p-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-bold text-text">Public domain</h2>
            <p className="mt-1 text-xs text-text-muted">
              Any domain you control works. No <code className="font-mono">apteva.ai</code> hostname is required.
            </p>
          </div>
          <span className={`rounded-full px-2 py-1 text-[10px] font-semibold ${config.dns.managed ? "bg-green/10 text-green" : "bg-yellow/10 text-yellow"}`}>
            {config.dns.managed ? "DNS managed" : "Manual DNS"}
          </span>
        </div>
        <div className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_auto]">
          <label className="space-y-1">
            <span className="text-[11px] font-medium text-text-muted">Base domain</span>
            <input
              value={baseDomain}
              onChange={(event) => setBaseDomain(event.target.value)}
              placeholder="tunnel.example.com"
              disabled={config.configured && config.can_configure === false}
              className="w-full rounded-lg border border-border bg-bg px-3 py-2 font-mono text-sm text-text outline-none focus:border-accent"
            />
          </label>
          <label className="space-y-1">
            <span className="text-[11px] font-medium text-text-muted">DNS target (optional)</span>
            <input
              value={dnsTarget}
              onChange={(event) => setDnsTarget(event.target.value)}
              placeholder="instance.example.com or 203.0.113.10"
              disabled={config.configured && config.can_configure === false}
              className="w-full rounded-lg border border-border bg-bg px-3 py-2 font-mono text-sm text-text outline-none focus:border-accent"
            />
          </label>
          <button
            onClick={() => void configure()}
            disabled={!baseDomain.trim() || busy !== "" || (config.configured && config.can_configure === false)}
            className="self-end rounded-lg bg-accent px-4 py-2 text-sm font-semibold text-bg disabled:opacity-40"
          >
            {busy === "configure" ? "Saving…" : config.configured ? "Update" : "Configure"}
          </button>
        </div>
        {dnsRecord && (
          <div className="mt-3 rounded-lg bg-bg px-3 py-2 font-mono text-[11px] text-text-muted">
            {dnsRecord}
          </div>
        )}
        {config.dns.error && <p className="mt-2 text-xs text-yellow">{config.dns.error}</p>}
        {config.configured && config.can_configure === false && (
          <p className="mt-2 text-xs text-text-muted">Only the operator project that configured this shared domain can change its DNS settings.</p>
        )}
      </section>

      <section className="rounded-xl border border-border bg-bg-card">
        <div className="flex flex-col gap-3 border-b border-border p-4 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h2 className="text-sm font-bold text-text">Reserved tunnels</h2>
            <p className="mt-1 text-xs text-text-muted">
              {stats.active_tunnels} of {stats.max_tunnels} project reservations used
            </p>
          </div>
          <div className="flex gap-2">
            <div className="flex items-center overflow-hidden rounded-lg border border-border bg-bg">
              <input
                value={name}
                onChange={(event) => setName(event.target.value.toLowerCase())}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && name.trim()) void createTunnel();
                }}
                placeholder="my-app"
                className="min-w-0 bg-transparent px-3 py-2 font-mono text-sm text-text outline-none"
              />
              <span className="max-w-[180px] truncate border-l border-border px-2 text-xs text-text-dim">
                .{config.base_domain || "domain"}
              </span>
            </div>
            <button
              onClick={() => void createTunnel()}
              disabled={!config.configured || !name.trim() || busy !== ""}
              className="rounded-lg bg-accent px-4 py-2 text-sm font-semibold text-bg disabled:opacity-40"
            >
              {busy === "create" ? "Creating…" : "Reserve"}
            </button>
          </div>
        </div>

        {tunnels.length === 0 ? (
          <div className="p-8 text-center text-sm text-text-muted">
            {config.configured ? "No tunnels reserved for this project." : "Configure a base domain first."}
          </div>
        ) : (
          <div className="divide-y divide-border">
            {tunnels.map((item) => (
              <div key={item.id} className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className={`h-2 w-2 rounded-full ${item.connected ? "bg-green" : "bg-text-dim"}`} />
                    <a href={item.url} target="_blank" rel="noreferrer" className="truncate font-mono text-sm font-semibold text-accent hover:underline">
                      {item.hostname}
                    </a>
                  </div>
                  <div className="mt-1 flex flex-wrap gap-x-3 text-[11px] text-text-muted">
                    <span>{item.connected ? "Connector online" : "Connector offline"}</span>
                    <span>{compactNumber(item.request_count)} requests</span>
                    <span>{formatBytes(item.bytes_in + item.bytes_out)}</span>
                    {item.last_connected_at && <span>Last online {formatDate(item.last_connected_at)}</span>}
                  </div>
                </div>
                <div className="flex gap-2">
                  <button
                    onClick={() => void rotate(item)}
                    disabled={busy !== ""}
                    className="rounded-lg border border-border px-3 py-1.5 text-xs text-text-muted hover:border-text-muted disabled:opacity-40"
                  >
                    Rotate token
                  </button>
                  <button
                    onClick={() => void remove(item)}
                    disabled={busy !== ""}
                    className="rounded-lg border border-red/30 px-3 py-1.5 text-xs text-red hover:bg-red/5 disabled:opacity-40"
                  >
                    Delete
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {credential && (
        <CredentialDialog
          credential={credential}
          onClose={() => setCredential(null)}
        />
      )}
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-[76px] rounded-lg border border-border bg-bg-card px-3 py-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim">{label}</div>
      <div className="mt-0.5 font-mono text-sm font-bold text-text">{value}</div>
    </div>
  );
}

function CredentialDialog({ credential, onClose }: { credential: Credential; onClose: () => void }) {
  const [copied, setCopied] = useState("");
  const copy = async (value: string, label: string) => {
    await navigator.clipboard.writeText(value);
    setCopied(label);
    window.setTimeout(() => setCopied(""), 1500);
  };
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-modal="true" aria-label="Connector credential">
      <div className="w-full max-w-xl rounded-xl border border-border bg-bg-card p-5 shadow-2xl">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-bold text-text">Save connector credential</h2>
            <p className="mt-1 text-xs text-yellow">This token will not be shown again.</p>
          </div>
          <button onClick={onClose} className="text-lg text-text-muted" aria-label="Close">×</button>
        </div>
        <SecretRow label="Connector token" value={credential.connector_token} onCopy={() => void copy(credential.connector_token, "token")} copied={copied === "token"} />
        <SecretRow label="Server URL" value={credential.connect_url.replace(/\/v1\/connect$/, "")} onCopy={() => void copy(credential.connect_url.replace(/\/v1\/connect$/, ""), "server")} copied={copied === "server"} />
        <div className="mt-4 rounded-lg bg-bg p-3 text-xs text-text-muted">
          Store the token in a mode-0600 file, then run the <code className="font-mono text-text">apteva-tunnel</code> connector with <code className="font-mono text-text">--token-file</code> and your local target.
        </div>
        <button onClick={onClose} className="mt-4 w-full rounded-lg bg-accent px-4 py-2 text-sm font-semibold text-bg">
          I saved the token
        </button>
      </div>
    </div>
  );
}

function SecretRow({ label, value, onCopy, copied }: { label: string; value: string; onCopy: () => void; copied: boolean }) {
  return (
    <div className="mt-4">
      <div className="mb-1 text-[11px] font-medium text-text-muted">{label}</div>
      <div className="flex items-center gap-2 rounded-lg border border-border bg-bg p-2">
        <code className="min-w-0 flex-1 break-all text-xs text-text">{value}</code>
        <button onClick={onCopy} className="shrink-0 rounded-md border border-border px-2 py-1 text-[11px] text-text-muted">
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}

function compactNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value || 0);
}

function formatBytes(value: number): string {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
