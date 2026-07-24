// LiveLinkPanel — single-toggle panel for the live-link app.
//
// Talks to the live-link sidecar via /api/apps/live-link/* (the
// platform proxy injects the per-install bearer token). Inherits the
// dashboard theme via Tailwind tokens — bg-bg-card / text-text-muted
// / border-border / etc. — so the panel recolors automatically across
// every theme the dashboard ships.
//
// Layout mirrors storage's panel:
//   p-6 outer padding, gap-4 between sections, raised cards with
//   `border border-border rounded-lg p-4 bg-bg-card`. Buttons follow
//   the same primary/secondary/tertiary tiers used in InstallModal.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { qrSVG } from "./qr";

// Inlined SDK app-event subscription (mirrors storage's panel). Each
// app installs from its own source so we copy the hook rather than
// share across apps.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface StatusResp {
  status: "idle" | "running" | "stopped" | "failed" | "orphaned";
  public_url: string;
  target_url: string;
  resolved_target?: string;
  run_id?: number;
  last_error?: string;
  started_at?: string;
  // Legacy v0.3 field — quick | named. Pre-ngrok panels read this.
  // Newer panels prefer `provider` which carries the actual provider
  // name (cloudflare-quick / cloudflare-named / ngrok).
  mode?: "quick" | "named";
  // v0.4+ — provider name reported by activeProvider(). Drives the
  // "Active provider" badge + which config form the panel shows.
  provider?: "cloudflare-quick" | "cloudflare-named" | "ngrok" | "zrok" | string;
  hostname?: string;
  cloudflare_bound?: boolean;
  ngrok_bound?: boolean;
  zrok_bound?: boolean;
  zrok_configured?: boolean;
  zrok_name?: string;
  zrok_url?: string;
  // ngrok reserved domain (paid plans), set via the app's ngrok_domain
  // config. Surfaced for display when active provider is ngrok.
  ngrok_domain?: string;
  named_configured?: boolean;
  desired_live?: boolean;
}

interface CFZone {
  id: string;
  name: string;
}

interface RunRow {
  id: number;
  provider: string;
  mode?: string;
  target_url: string;
  public_url: string;
  started_at: string;
  finished_at?: string;
  status: string;
  exit_reason?: string;
}

const API = "/api/apps/live-link";

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  try { return new Date(iso).toLocaleString(); } catch { return iso; }
}

function fmtDuration(start?: string, end?: string): string {
  if (!start || !end) return "—";
  try {
    const ms = new Date(end).getTime() - new Date(start).getTime();
    if (ms < 1000) return `${ms} ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
    if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
    return `${(ms / 3_600_000).toFixed(1)} h`;
  } catch { return "—"; }
}

// Map the manager's status enum to a semantic color token. running →
// success, failed → error, orphaned → warn (the run was alive but the
// sidecar died with it), stopped/idle → muted.
function statusColor(s: string): string {
  if (s === "running")  return "bg-success";
  if (s === "failed")   return "bg-error";
  if (s === "orphaned") return "bg-warn";
  return "bg-text-dim";
}

export default function LiveLinkPanel({ projectId, installId }: NativePanelProps) {
  const [status, setStatus] = useState<StatusResp>({
    status: "idle", public_url: "", target_url: "",
  });
  const [runs, setRuns] = useState<RunRow[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"start" | "stop" | "install" | "configure" | "zrok-configure" | "provider" | "destroy" | null>(null);
  const [loading, setLoading] = useState(true);
  const [exposureAck, setExposureAck] = useState(false);
  const [copied, setCopied] = useState(false);
  const refreshSeq = useRef(0);

  // Named-mode form state. zones lazy-loads when the operator opens
  // the configure form; selectedZone + subdomain are the inputs.
  const [zones, setZones] = useState<CFZone[] | null>(null);
  const [selectedZoneID, setSelectedZoneID] = useState("");
  const [subdomain, setSubdomain] = useState("");
  const [showConfigure, setShowConfigure] = useState(false);
  const [zrokName, setZrokName] = useState("");

  const qs = useCallback(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const api = useCallback(
    async <T = unknown>(method: string, path: string, body?: unknown): Promise<T> => {
      const res = await fetch(`${API}${path}?${qs()}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) {
        const text = await res.text().catch(() => "");
        let message = text;
        try { message = (JSON.parse(text) as { error?: string }).error || text; } catch {}
        throw new Error(`${res.status}: ${message}`);
      }
      return res.json();
    },
    [qs],
  );

  const refresh = useCallback(async () => {
    const seq = ++refreshSeq.current;
    try {
      const [s, r] = await Promise.all([
        api<StatusResp>("GET", "/status"),
        api<{ runs: RunRow[] }>("GET", "/runs"),
      ]);
      if (seq === refreshSeq.current) {
        setStatus(s);
        setRuns(r.runs || []);
        setError("");
      }
    } catch (e: unknown) {
      if (seq === refreshSeq.current) setError("Error: " + (e instanceof Error ? e.message : String(e)));
    } finally {
      if (seq === refreshSeq.current) setLoading(false);
    }
  }, [api]);

  const refreshStatus = useCallback(async () => {
    try {
      setStatus(await api<StatusResp>("GET", "/status"));
    } catch (e: unknown) {
      setError("Error: " + (e instanceof Error ? e.message : String(e)));
    }
  }, [api]);

  useEffect(() => { refresh(); }, [refresh]);

  // Re-poll every 2s while the tunnel is starting (URL not yet
  // assigned) — the SSE event could be missed and we don't want a
  // stale "starting…" pill forever.
  useEffect(() => {
    if (status.status !== "running" || status.public_url) return;
    const t = window.setInterval(refreshStatus, 2000);
    return () => window.clearInterval(t);
  }, [status.status, status.public_url, refreshStatus]);

  useAppEvents("live-link", projectId, refresh);

  const onStart = async () => {
    setBusy("start"); setError("");
    try { await api("POST", "/start", {}); await refresh(); }
    catch (e: unknown) { setError("Start failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };
  const onStop = async () => {
    setBusy("stop"); setError("");
    try { await api("POST", "/stop", {}); await refresh(); }
    catch (e: unknown) { setError("Stop failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };
  const onReinstall = async () => {
    const label = status.provider === "ngrok" ? "ngrok" : status.provider === "zrok" ? "zrok2" : "cloudflared";
    if (!window.confirm(`Download and verify the pinned ${label} release? The managed binary will be replaced.`)) return;
    setBusy("install"); setError("");
    try { await api("POST", "/install", {}); }
    catch (e: unknown) { setError("Reinstall failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };
  const copyURL = async () => {
    if (!status.public_url) return;
    try {
      await navigator.clipboard.writeText(status.public_url);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch { setError("Could not copy the URL to the clipboard."); }
  };

  // Lazy-load zones the first time the operator opens the configure
  // form. Cheap (1 API call) but needs the cloudflare integration to
  // be bound — which we surface as a clean error if it isn't.
  const loadZones = useCallback(async (): Promise<CFZone[]> => {
    try {
      const r = await api<{ zones: CFZone[] }>("GET", "/named/zones");
      setZones(r.zones || []);
      setError("");
      return r.zones || [];
    } catch (e: unknown) {
      setError("Could not load zones: " + (e instanceof Error ? e.message : String(e)));
      return [];
    }
  }, [api]);

  const openConfigure = async () => {
    setShowConfigure(true);
    const availableZones = zones === null ? await loadZones() : zones;
    // Pre-fill from existing config if any.
    if (status.hostname && availableZones.length > 0) {
      const zone = availableZones.find((z) => status.hostname === z.name || status.hostname!.endsWith("." + z.name));
      if (zone) {
        setSelectedZoneID(zone.id);
        setSubdomain(status.hostname!.slice(0, -(zone.name.length + 1)));
      }
    }
  };

  const saveConfigure = async () => {
    if (!selectedZoneID) {
      setError("Pick a zone first");
      return;
    }
    if (!subdomain.trim()) {
      setError("Enter a subdomain");
      return;
    }
    const zone = (zones || []).find((z) => z.id === selectedZoneID);
    if (!zone) return;
    const hostname = `${subdomain.trim()}.${zone.name}`;
    setBusy("configure"); setError("");
    try {
      await api("POST", "/provider/configure", {
        provider: "cloudflare-named",
        config: { zone_id: selectedZoneID, hostname },
      });
      setShowConfigure(false);
      await refresh();
    } catch (e: unknown) {
      setError("Configure failed: " + (e instanceof Error ? e.message : String(e)));
    } finally { setBusy(null); }
  };

  const configureZrok = async () => {
    const name = zrokName.trim();
    if (!/^[a-z0-9-]{3,63}$/.test(name) || name.startsWith("-") || name.endsWith("-")) {
      setError("zrok names use 3–63 lowercase letters, digits, or hyphens and cannot start or end with a hyphen.");
      return;
    }
    setBusy("zrok-configure"); setError("");
    try {
      await api("POST", "/provider/configure", { provider: "zrok", config: { name } });
      await refresh();
    } catch (e: unknown) {
      setError("zrok configuration failed: " + (e instanceof Error ? e.message : String(e)));
    } finally { setBusy(null); }
  };

  const selectProvider = async (next: "cloudflare-quick" | "cloudflare-named" | "ngrok" | "zrok") => {
    setBusy("provider"); setError("");
    try {
      await api("POST", "/provider", { provider: next });
      await refresh();
    } catch (e: unknown) {
      setError("Provider change failed: " + (e instanceof Error ? e.message : String(e)));
    } finally { setBusy(null); }
  };

  const destroyNamed = async () => {
    if (!window.confirm(`Delete the Cloudflare tunnel and DNS record for ${status.hostname}?`)) return;
    setBusy("destroy"); setError("");
    try { await api("POST", "/destroy", { provider: "cloudflare-named" }); await refresh(); }
    catch (e: unknown) { setError("Delete failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };

  const destroyZrok = async () => {
    if (!window.confirm(`Release the zrok name ${status.zrok_name} and remove its local environment?`)) return;
    setBusy("destroy"); setError("");
    try { await api("POST", "/destroy", { provider: "zrok" }); await refresh(); }
    catch (e: unknown) { setError("Delete failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };

  const isRunning = status.status === "running";
  const isStarting = isRunning && !status.public_url;
  // Active-provider classification. Prefer the v0.4+ provider field
  // when present; fall back to the legacy mode field so this panel
  // also works against a v0.3 sidecar.
  const provider = status.provider || (status.mode === "named" ? "cloudflare-named" : "cloudflare-quick");
  const isNgrok = provider === "ngrok";
  const isZrok = provider === "zrok";
  const isNamed = provider === "cloudflare-named" || status.mode === "named";
  const cfBound = !!status.cloudflare_bound;
  const ngrokBound = !!status.ngrok_bound;
  const zrokBound = !!status.zrok_bound;
  const zrokConfigured = !!status.zrok_configured && !!status.zrok_name;
  const hasNamedHostname = !!status.hostname && !!status.named_configured;
  // Human-friendly provider label for the header badge.
  const providerLabel =
    isNgrok ? "ngrok"
    : isZrok ? "zrok"
    : isNamed ? "Cloudflare (named)"
    : "Cloudflare (quick)";

  // Memoize the QR SVG — encoding is fast (~0.5ms) but recomputing on
  // every status poll is wasteful. Force light=#fff so the QR is
  // always readable on dark themes (transparent light cells would
  // disappear into the bg).
  const qr = useMemo(() => {
    if (!status.public_url) return "";
    try { return qrSVG(status.public_url, { size: 176, light: "#ffffff" }); }
    catch { return ""; } // URL too long → just hide the code
  }, [status.public_url]);

  return (
    <div className="h-full flex flex-col p-6 gap-4 min-w-0 overflow-y-auto" aria-busy={loading}>
      <header className="flex items-baseline justify-between gap-3 flex-wrap">
        <div>
          <h2 className="text-text text-base font-bold">Live Link</h2>
          <p className="text-text-muted text-xs mt-1">
            {isNgrok ? (
              <>Public HTTPS URL for this Apteva instance via ngrok.</>
            ) : isZrok ? (
              <>Stable public HTTPS URL for this Apteva instance via zrok.</>
            ) : (
              <>Public HTTPS URL for this Apteva instance via Cloudflare.</>
            )}
            {isNamed && status.hostname ? (
              <> Stable hostname: <code className="font-mono text-text">{status.hostname}</code>.</>
            ) : null}
            {isNgrok && status.ngrok_domain ? (
              <> Reserved domain: <code className="font-mono text-text">{status.ngrok_domain}</code>.</>
            ) : null}
            {isZrok && status.zrok_url ? (
              <> Reserved URL: <code className="font-mono text-text">{status.zrok_url}</code>.</>
            ) : null}
          </p>
        </div>
        <span className="text-text-dim text-xs uppercase tracking-wide">
          {providerLabel}
        </span>
      </header>

      {error && (
        <div role="alert" aria-live="assertive" className="text-error text-xs border border-error/40 bg-error/10 rounded px-3 py-2">
          {error}
        </div>
      )}

      <section aria-labelledby="provider-heading" className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
        <div>
          <h3 id="provider-heading" className="text-text font-bold text-sm">Tunnel provider</h3>
          <p className="text-text-muted text-xs mt-1">Choose explicitly. Binding an integration never switches providers by itself.</p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          {([
            ["cloudflare-quick", "Cloudflare Quick", "Temporary URL · no account", true],
            ["cloudflare-named", "Cloudflare Named", "Stable URL · your domain", cfBound && hasNamedHostname],
            ["ngrok", "ngrok", status.ngrok_domain ? `Reserved · ${status.ngrok_domain}` : "Temporary or reserved URL", ngrokBound],
            ["zrok", "zrok", status.zrok_url ? `Stable · ${status.zrok_url}` : "Stable free public URL", zrokBound && zrokConfigured],
          ] as const).map(([value, label, hint, available]) => {
            const selected = provider === value;
            return (
              <button
                key={value}
                type="button"
                aria-pressed={selected}
                disabled={isRunning || busy !== null || !available}
                onClick={() => selectProvider(value)}
                className={`text-left rounded border p-3 transition-colors disabled:opacity-50 ${selected ? "border-accent bg-accent/10" : "border-border hover:bg-bg-hover"}`}
              >
                <span className="block text-text text-sm font-bold">{label}</span>
                <span className="block text-text-muted text-xs mt-1">
                  {available ? hint : value === "ngrok" ? "Bind ngrok first" : value === "zrok" ? (zrokBound ? "Reserve a name below" : "Bind zrok first") : "Configure a hostname first"}
                </span>
              </button>
            );
          })}
        </div>
        {isRunning && <p className="text-text-dim text-xs">Stop the tunnel before changing providers.</p>}
      </section>

      {/* ─── ngrok section ──────────────────────────────────────────
          Shows when ngrok is the active provider. ngrok has no panel-
          driven config (the authtoken lives on the integration; the
          optional reserved domain lives in app config_schema), so this
          section is mostly informational: "this is the provider that
          will start" + a path to set the optional ngrok_domain. */}
      {isNgrok && ngrokBound && (
        <section className="border border-border rounded-lg p-4 bg-bg-card space-y-2 text-sm">
          <div className="text-text font-bold">ngrok is the active provider.</div>
          {status.ngrok_domain ? (
            <div className="text-text-muted text-xs">
              Reserved domain configured: <code className="font-mono text-text">{status.ngrok_domain}</code> —
              the public URL will be <code className="font-mono text-text">https://{status.ngrok_domain}</code> on every start.
            </div>
          ) : (
            <div className="text-text-muted text-xs">
              Free plan / no reserved domain set — ngrok assigns a fresh
              <code className="font-mono text-text"> *.ngrok-free.app </code>
              URL on each "Go live". Paid plans can pin a hostname via the
              <code className="font-mono text-text"> ngrok_domain </code>
              setting (Settings tab).
            </div>
          )}
        </section>
      )}

      {zrokBound && (
        <section className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
          <div>
            <div className="text-text font-bold text-sm">zrok stable name</div>
            <div className="text-text-muted text-xs mt-1">
              Reserve a free public name. The URL stays the same when Live Link or the server restarts.
            </div>
          </div>
          <div className="flex items-end gap-2 flex-wrap">
            <label className="flex-1 min-w-48">
              <span className="text-text-muted text-xs">Name</span>
              <div className="flex items-center gap-2 mt-1">
                <input
                  type="text"
                  value={zrokName || status.zrok_name || ""}
                  onChange={(e) => setZrokName(e.target.value.toLowerCase())}
                  placeholder="my-apteva"
                  disabled={isRunning || busy !== null}
                  className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
                />
                <span className="text-text-dim text-xs font-mono">.share.zrok.io</span>
              </div>
            </label>
            <button
              onClick={configureZrok}
              disabled={isRunning || busy !== null || !(zrokName || status.zrok_name)}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
            >
              {busy === "zrok-configure" ? "Reserving…" : zrokConfigured ? "Change name" : "Reserve name"}
            </button>
          </div>
          {zrokConfigured && (
            <div className="flex items-center justify-between gap-3 text-xs flex-wrap">
              <a href={status.zrok_url} target="_blank" rel="noreferrer" className="font-mono text-accent hover:underline">
                {status.zrok_url}
              </a>
              <button
                onClick={destroyZrok}
                disabled={isRunning || busy !== null}
                className="text-error underline disabled:opacity-50"
              >
                {busy === "destroy" ? "Releasing…" : "Release name"}
              </button>
            </div>
          )}
          <p className="text-text-dim text-xs">
            zrok’s public frontend shows its own visitor interstitial; use Cloudflare Named when you need a fully branded URL.
          </p>
        </section>
      )}

      {/* ─── Custom-domain section ──────────────────────────────────
          Renders whenever Cloudflare is bound, even when ngrok is the
          active provider — the operator might want to migrate. The
          header badge tells them which one is actually active. Three
          states for the Cloudflare flow:
            cfBound, no hostname, form closed  → "Configure hostname" CTA
            cfBound, hostname set, form closed → hostname row + Change / Switch
            cfBound, form open                 → zone picker + subdomain input */}
      {cfBound && (
        <section className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
          {!showConfigure && !hasNamedHostname && (
            <div className="flex items-center justify-between gap-3 flex-wrap">
              <div className="text-text-muted text-sm">
                <span className="text-text font-bold">Use a stable URL on your domain.</span>{" "}
                Pick a Cloudflare zone and a subdomain — the URL persists across restarts.
              </div>
              <button
                onClick={openConfigure}
                className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover"
              >
                Configure hostname
              </button>
            </div>
          )}

          {showConfigure ? (
            <div className="space-y-3">
              <div className="text-text font-bold text-sm">Configure named tunnel</div>
              {zones === null ? (
                <div className="text-text-muted text-xs">Loading your Cloudflare zones…</div>
              ) : zones.length === 0 ? (
                <div className="text-text-muted text-xs">
                  No zones found on this Cloudflare account. Add a domain
                  on Cloudflare first, then refresh.
                </div>
              ) : (
                <>
                  <label className="block">
                    <span className="text-text-muted text-xs">Zone</span>
                    <select
                      value={selectedZoneID}
                      onChange={(e) => setSelectedZoneID(e.target.value)}
                      className="w-full mt-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
                    >
                      <option value="">Pick a zone…</option>
                      {zones.map((z) => (
                        <option key={z.id} value={z.id}>{z.name}</option>
                      ))}
                    </select>
                  </label>
                  <label className="block">
                    <span className="text-text-muted text-xs">Subdomain</span>
                    <div className="flex items-center gap-2 mt-1">
                      <input
                        type="text"
                        value={subdomain}
                        onChange={(e) => setSubdomain(e.target.value)}
                        placeholder="tunnel"
                        className="flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
                      />
                      <span className="text-text-dim text-xs font-mono">
                        .{(zones.find((z) => z.id === selectedZoneID)?.name) || "<zone>"}
                      </span>
                    </div>
                  </label>
                  <div className="flex justify-end gap-2 pt-1">
                    <button
                      onClick={() => { setShowConfigure(false); setError(""); }}
                      className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={saveConfigure}
                      disabled={busy !== null || !selectedZoneID || !subdomain.trim()}
                      className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
                    >
                      {busy === "configure" ? "Saving…" : "Save"}
                    </button>
                  </div>
                </>
              )}
            </div>
          ) : hasNamedHostname ? (
            // Configured: hostname + change / switch-back affordances.
            <div className="flex items-center justify-between gap-3 flex-wrap">
              <div className="text-sm">
                <span className="text-text-muted">Hostname:</span>{" "}
                <code className="font-mono text-text">{status.hostname}</code>
              </div>
              <div className="flex items-center gap-3">
                <button
                  onClick={openConfigure}
                  disabled={isRunning || busy !== null}
                  title={isRunning ? "Stop the tunnel before changing hostname" : ""}
                  className="text-text-muted text-xs underline hover:text-text disabled:opacity-50"
                >
                  Change
                </button>
                <button
                  onClick={() => selectProvider("cloudflare-quick")}
                  disabled={isRunning || busy !== null}
                  title={isRunning ? "Stop the tunnel before switching" : "Keep this hostname configured and use a temporary Cloudflare URL"}
                  className="text-text-muted text-xs underline hover:text-text disabled:opacity-50"
                >
                  {busy === "provider" ? "Switching…" : "Use Quick instead"}
                </button>
                <button
                  onClick={destroyNamed}
                  disabled={isRunning || busy !== null}
                  title={isRunning ? "Stop the tunnel before deleting it" : "Delete the Cloudflare tunnel and DNS record"}
                  className="text-error text-xs underline disabled:opacity-50"
                >
                  {busy === "destroy" ? "Deleting…" : "Delete hostname"}
                </button>
              </div>
            </div>
          ) : null}
        </section>
      )}

      {/* ─── Main toggle / URL display ──────────────────────────── */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
        {!isRunning && (
          <label className="flex items-start gap-2 rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-text-muted">
            <input
              type="checkbox"
              checked={exposureAck}
              onChange={(e) => setExposureAck(e.target.checked)}
              className="mt-0.5"
            />
            <span><strong className="text-text">Public exposure:</strong> anyone on the internet can reach the selected target URL. Confirm that the target has appropriate authentication before going live.</span>
          </label>
        )}
        <div className="flex items-center gap-3">
          <span aria-hidden="true" className={`inline-block w-2 h-2 rounded-full ${statusColor(status.status === "idle" ? "stopped" : status.status)}`} />
          <strong className="text-text" role="status" aria-live="polite">
            {isStarting ? "Starting tunnel…" :
             isRunning  ? "Live" :
             status.status === "failed"   ? "Failed" :
             status.status === "orphaned" ? "Orphaned" :
             "Off"}
          </strong>
          <div className="flex-1" />
          {!isRunning && (
            <button
              onClick={onStart}
              disabled={loading || busy !== null || !exposureAck || (isNamed && !hasNamedHostname) || (isZrok && !zrokConfigured)}
              title={
                isNamed && !hasNamedHostname
                  ? "Configure a hostname first (named mode)"
                  : isZrok && !zrokConfigured
                    ? "Reserve a zrok name first"
                  : !exposureAck
                    ? "Acknowledge public exposure first"
                    : `First click may download the verified ${isNgrok ? "ngrok" : isZrok ? "zrok2" : "cloudflared"} binary.`
              }
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
            >
              {busy === "start" ? "Starting…" : "Go live"}
            </button>
          )}
          {isRunning && (
            <button
              onClick={onStop}
              disabled={busy === "stop"}
              className="px-3 py-1.5 text-sm border border-border text-text rounded hover:bg-bg-hover disabled:opacity-50"
            >
              {busy === "stop" ? "Stopping…" : "Stop"}
            </button>
          )}
        </div>

        {/* URL bar */}
        {isRunning && status.public_url && (
          <div className="flex items-center gap-2 px-3 py-2 bg-bg-input border border-border rounded font-mono text-sm">
            <a
              href={status.public_url}
              target="_blank"
              rel="noreferrer"
              className="flex-1 text-accent hover:underline truncate min-w-0"
            >
              {status.public_url}
            </a>
            <button
              onClick={copyURL}
              aria-live="polite"
              className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover"
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
        )}

        {/* QR code — scan with a phone camera to open. White wrapper
            so the SVG (transparent light cells) reads on dark themes. */}
        {isRunning && qr && (
          <div className="flex flex-col sm:flex-row items-center gap-4 px-3 py-3 bg-bg-input border border-border rounded">
            <div
              role="img"
              aria-label="QR code for the tunnel URL"
              className="w-44 h-44 shrink-0 bg-white rounded p-2"
              dangerouslySetInnerHTML={{ __html: qr }}
            />
            <div className="text-text-muted text-xs leading-relaxed">
              <div className="text-text font-bold mb-1">Scan to open on your phone</div>
              <div>
                Point any camera app at the code — the URL opens in your default
                browser. Useful for testing on a different network or sharing in person.
              </div>
            </div>
          </div>
        )}

        {isStarting && (
          <div className="text-text-dim text-xs">
            {isNgrok ? "ngrok" : isZrok ? "zrok" : "Cloudflared"} usually assigns a URL within a few seconds.
          </div>
        )}

        {status.status === "failed" && status.last_error && (
          <div className="text-error text-xs">{status.last_error}</div>
        )}

        <div className="flex items-center justify-between gap-2 text-xs text-text-dim flex-wrap">
          <div>
            Forwards to{" "}
            <code className="font-mono text-text-muted">
              {status.target_url || status.resolved_target || "—"}
            </code>
          </div>
          {!isRunning && (
            <button
              onClick={onReinstall}
              disabled={busy !== null}
              className="text-text-muted underline hover:text-text disabled:opacity-50"
            >
              {busy === "install" ? "Downloading and verifying…" : `Reinstall ${isNgrok ? "ngrok" : isZrok ? "zrok2" : "cloudflared"}`}
            </button>
          )}
        </div>
      </section>

      {/* ─── History ───────────────────────────────────────────── */}
      <section>
        <h3 className="text-text text-sm font-bold mb-2">History</h3>
        {runs.length === 0 ? (
          <div className="text-text-muted text-xs italic py-2">No tunnel runs yet.</div>
        ) : (
          <div className="border border-border rounded overflow-hidden">
            {runs.map((r) => (
              <div
                key={r.id}
                className="flex items-start gap-3 px-3 py-2 border-b border-border last:border-b-0 text-sm"
              >
                <span aria-hidden="true" className={`inline-block w-2 h-2 rounded-full mt-1.5 shrink-0 ${statusColor(r.status)}`} />
                <div className="min-w-0 flex-1">
                  <div className="text-text-muted text-xs">
                    {fmtTime(r.started_at)}
                    {r.finished_at && (
                      <> → {fmtTime(r.finished_at)} · {fmtDuration(r.started_at, r.finished_at)}</>
                    )}
                    {r.mode && (
                      <span className="ml-2 px-1.5 py-0.5 text-[10px] uppercase tracking-wide rounded bg-bg-input text-text-dim">
                        {r.mode}
                      </span>
                    )}
                    <span className="sr-only"> Status: {r.status}.</span>
                  </div>
                  {r.public_url && (
                    <div className="font-mono text-xs text-text-muted truncate mt-0.5">
                      {r.public_url}
                    </div>
                  )}
                  {r.exit_reason && r.status !== "stopped" && (
                    <div className="text-error text-xs mt-0.5">{r.exit_reason}</div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
