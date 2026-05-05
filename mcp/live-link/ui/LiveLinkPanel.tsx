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
  mode?: "quick" | "named";
  hostname?: string;
  cloudflare_bound?: boolean;
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
  const [busy, setBusy] = useState<"start" | "stop" | "install" | "configure" | null>(null);

  // Named-mode form state. zones lazy-loads when the operator opens
  // the configure form; selectedZone + subdomain are the inputs.
  const [zones, setZones] = useState<CFZone[] | null>(null);
  const [selectedZoneID, setSelectedZoneID] = useState("");
  const [subdomain, setSubdomain] = useState("");
  const [showConfigure, setShowConfigure] = useState(false);

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
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [qs],
  );

  const refresh = useCallback(async () => {
    try {
      const [s, r] = await Promise.all([
        api<StatusResp>("GET", "/status"),
        api<{ runs: RunRow[] }>("GET", "/runs"),
      ]);
      setStatus(s);
      setRuns(r.runs || []);
      setError("");
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
    const t = window.setInterval(refresh, 2000);
    return () => window.clearInterval(t);
  }, [status.status, status.public_url, refresh]);

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
    if (!window.confirm("Download the latest cloudflared release? The current binary will be replaced.")) return;
    setBusy("install"); setError("");
    try { await api("POST", "/install", {}); }
    catch (e: unknown) { setError("Reinstall failed: " + (e instanceof Error ? e.message : String(e))); }
    finally { setBusy(null); }
  };
  const copyURL = async () => {
    if (!status.public_url) return;
    try { await navigator.clipboard.writeText(status.public_url); } catch {}
  };

  // Lazy-load zones the first time the operator opens the configure
  // form. Cheap (1 API call) but needs the cloudflare integration to
  // be bound — which we surface as a clean error if it isn't.
  const loadZones = useCallback(async () => {
    try {
      const r = await api<{ zones: CFZone[] }>("GET", "/named/zones");
      setZones(r.zones || []);
      setError("");
    } catch (e: unknown) {
      setError("Could not load zones: " + (e instanceof Error ? e.message : String(e)));
    }
  }, [api]);

  const openConfigure = async () => {
    setShowConfigure(true);
    if (zones === null) await loadZones();
    // Pre-fill from existing config if any.
    if (status.hostname && zones && zones.length > 0) {
      const zone = zones.find((z) => status.hostname!.endsWith("." + z.name));
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
      await api("POST", "/named/configure", { zone_id: selectedZoneID, hostname });
      setShowConfigure(false);
      await refresh();
    } catch (e: unknown) {
      setError("Configure failed: " + (e instanceof Error ? e.message : String(e)));
    } finally { setBusy(null); }
  };

  const isRunning = status.status === "running";
  const isStarting = isRunning && !status.public_url;
  const isNamed = status.mode === "named";
  const cfBound = !!status.cloudflare_bound;
  const hasNamedHostname = isNamed && !!status.hostname;

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
    <div className="h-full flex flex-col p-6 gap-4 min-w-0 overflow-y-auto">
      <header className="flex items-baseline justify-between gap-3 flex-wrap">
        <div>
          <h2 className="text-text text-base font-bold">Live Link</h2>
          <p className="text-text-muted text-xs mt-1">
            Public HTTPS URL for this Apteva instance via Cloudflare.
            {status.mode === "named" && status.hostname ? (
              <> Stable hostname: <code className="font-mono text-text">{status.hostname}</code>.</>
            ) : null}
          </p>
        </div>
        <span className="text-text-dim text-xs uppercase tracking-wide">
          {status.mode === "named" ? "Named" : "Quick"}
        </span>
      </header>

      {error && (
        <div className="text-error text-xs border border-error/40 bg-error/10 rounded px-3 py-2">
          {error}
        </div>
      )}

      {/* ─── Named-mode hostname management ─────────────────────── */}
      {isNamed && (
        <section className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
          {!cfBound ? (
            <div className="text-text-muted text-sm">
              <div className="text-text font-bold mb-1">Cloudflare not connected</div>
              Named mode needs the cloudflare integration. Open this app's
              settings and bind a connection (one API token with
              Cloudflare Tunnel:Edit + DNS:Edit), then come back.
            </div>
          ) : !hasNamedHostname && !showConfigure ? (
            <div className="flex items-center justify-between gap-3 flex-wrap">
              <div className="text-text-muted text-sm">
                <span className="text-text font-bold">No hostname configured.</span>{" "}
                Pick a zone and a subdomain to get a stable URL.
              </div>
              <button
                onClick={openConfigure}
                className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover"
              >
                Configure hostname
              </button>
            </div>
          ) : showConfigure ? (
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
          ) : (
            // Configured: small hostname row with a Change affordance.
            <div className="flex items-center justify-between gap-3 flex-wrap">
              <div className="text-sm">
                <span className="text-text-muted">Hostname:</span>{" "}
                <code className="font-mono text-text">{status.hostname}</code>
              </div>
              <button
                onClick={openConfigure}
                disabled={isRunning}
                title={isRunning ? "Stop the tunnel before changing hostname" : ""}
                className="text-text-muted text-xs underline hover:text-text disabled:opacity-50"
              >
                Change
              </button>
            </div>
          )}
        </section>
      )}

      {/* ─── Main toggle / URL display ──────────────────────────── */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-3">
        <div className="flex items-center gap-3">
          <span className={`inline-block w-2 h-2 rounded-full ${statusColor(status.status === "idle" ? "stopped" : status.status)}`} />
          <strong className="text-text">
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
              disabled={busy !== null || (isNamed && !hasNamedHostname)}
              title={
                isNamed && !hasNamedHostname
                  ? "Configure a hostname first (named mode)"
                  : "First click may download cloudflared (~30MB)."
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
              className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover"
            >
              Copy
            </button>
          </div>
        )}

        {/* QR code — scan with a phone camera to open. White wrapper
            so the SVG (transparent light cells) reads on dark themes. */}
        {isRunning && qr && (
          <div className="flex items-center gap-4 px-3 py-3 bg-bg-input border border-border rounded">
            <div
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
            Cloudflared usually assigns a URL within a few seconds.
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
              {busy === "install" ? "Downloading…" : "Reinstall cloudflared"}
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
                <span className={`inline-block w-2 h-2 rounded-full mt-1.5 shrink-0 ${statusColor(r.status)}`} />
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
