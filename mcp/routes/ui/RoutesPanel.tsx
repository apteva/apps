// RoutesPanel — operator UI for the hostname → target route table.
//
// Lists every route with its owner and cert status. Manual entries
// (owner_install_id = 0) get full CRUD here; app-owned rows (deploy,
// code, …) are read-only — their lifecycle belongs to the owning
// app (deploy attaches/detaches via attach_domain, etc.).
//
// Routes data is global and not project-scoped, so the routes REST
// calls don't carry a project. The per-route cert lookup does hit
// the certs app, which is project-scoped, so project_id rides along
// on the query string for that.

import { useCallback, useEffect, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Route {
  id: number;
  hostname: string;
  target: string;
  owner_install_id: number;
  owner_kind: string;
  cert_fqdn?: string;
  allow_http?: boolean;
  created_at?: string;
  updated_at?: string;
}

// Cert status values mirror the certs app's schema:
// pending | issuing | live | failed | revoked.
interface CertWire {
  id: number;
  fqdn: string;
  status: string;
  expires_at?: string;
  error?: string;
}

const API = "/api/apps/routes/api";

const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

// ─── Live app-event subscription (copied from DeployPanel — no
// shared hooks module; each panel keeps its own copy) ─────────────

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

export default function RoutesPanel({ projectId, installId }: NativePanelProps) {
  const [routes, setRoutes] = useState<Route[] | null>(null);
  const [certs, setCerts] = useState<Record<string, CertWire>>({});
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const withParams = useCallback(
    () =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
      }).toString(),
    [projectId, installId],
  );

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/routes?${withParams()}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { routes: Route[] };
      setRoutes(j.routes || []);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
      setRoutes([]);
    }
  }, [withParams]);

  useEffect(() => { load(); }, [load]);

  // Live updates: routes emits routes.changed whenever a route is
  // registered / updated / removed — by this panel, by an agent's
  // routes_register, or by deploy_attach_domain.
  useAppEvents("routes", projectId, (ev) => {
    if (ev.topic.startsWith("routes.")) load();
  });

  // Per-route cert status — best-effort against the certs app. A
  // missing certs app, or no cert for a hostname, collapses silently
  // to "no cert".
  useEffect(() => {
    if (!routes) return;
    const fqdns = Array.from(new Set(routes.map((r) => r.cert_fqdn || r.hostname)));
    let cancelled = false;
    Promise.all(
      fqdns.map(async (fqdn) => {
        try {
          const r = await fetch(
            `/api/apps/certs/api/certs/${encodeURIComponent(fqdn)}?${withParams()}`,
            { credentials: "same-origin" },
          );
          if (!r.ok) return [fqdn, null] as const;
          const j = (await r.json()) as { cert: CertWire };
          return [fqdn, j.cert] as const;
        } catch {
          return [fqdn, null] as const;
        }
      }),
    ).then((results) => {
      if (cancelled) return;
      const next: Record<string, CertWire> = {};
      for (const [fqdn, cert] of results) {
        if (cert) next[fqdn] = cert;
      }
      setCerts(next);
    });
    return () => { cancelled = true; };
  }, [routes, withParams]);

  const addRoute = useCallback(async (hostname: string, target: string, allowHTTP: boolean) => {
    const r = await fetch(`${API}/routes?${withParams()}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ hostname, target, allow_http: allowHTTP }),
    });
    if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
    load();
  }, [withParams, load]);

  const removeRoute = useCallback(async (hostname: string) => {
    if (!confirm(`Remove the route for ${hostname}? apteva-server stops proxying it.`)) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/routes/${encodeURIComponent(hostname)}?${withParams()}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      load();
    } catch (e) {
      setErr("Delete failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [withParams, load]);

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <h1 className="text-lg font-semibold">Routes</h1>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          <span>{routes ? `${routes.length} route${routes.length === 1 ? "" : "s"}` : "loading…"}</span>
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={load}
          >Refresh</button>
        </div>
      </div>

      <div className="px-6 py-2 border-b border-border text-xs text-text-dim">
        Hostname → backend target table. apteva-server reads this and reverse-proxies inbound traffic. Apps register their own routes; rows below marked “manual” are yours to manage here.
      </div>

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      <AddRouteForm onAdd={addRoute} />

      <div className="flex-1 min-h-0 overflow-auto">
        {routes === null ? (
          <div className="p-6 text-text-dim text-sm">Loading…</div>
        ) : routes.length === 0 ? (
          <div className="p-6 text-text-dim text-sm">
            No routes yet. Routes-aware apps (Deploy, Code) register them automatically — e.g. <span className="font-mono">deploy_attach_domain</span> adds one when a deployment goes live. Or add a manual route above.
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-xs text-text-dim">
              <tr className="border-b border-border">
                <th className="text-left px-4 py-2">Hostname</th>
                <th className="text-left px-4 py-2">Target</th>
                <th className="text-left px-4 py-2">Owner</th>
                <th className="text-left px-4 py-2">Cert</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {routes.map((r) => {
                const certFqdn = r.cert_fqdn || r.hostname;
                return (
                  <tr key={r.id} className="border-b border-border hover:bg-surface-2 align-top">
                    <td className="px-4 py-2">
                      <a
                        href={`${r.allow_http ? "http" : "https"}://${r.hostname}/`}
                        target="_blank"
                        rel="noreferrer"
                        className="font-mono text-accent hover:underline break-all"
                      >{r.hostname}</a>
                      {r.allow_http && (
                        <span className="ml-2 text-xs text-text-dim">http ok</span>
                      )}
                    </td>
                    <td className="px-4 py-2 font-mono text-text-dim break-all">{r.target}</td>
                    <td className="px-4 py-2 whitespace-nowrap">
                      <OwnerBadge kind={r.owner_kind} installId={r.owner_install_id} />
                    </td>
                    <td className="px-4 py-2 whitespace-nowrap">
                      <CertCell cert={certs[certFqdn]} fqdn={certFqdn} />
                    </td>
                    <td className="px-4 py-2 text-right whitespace-nowrap">
                      {r.owner_install_id === 0 ? (
                        <button
                          type="button"
                          disabled={busy}
                          onClick={() => removeRoute(r.hostname)}
                          className="text-xs text-text-dim hover:text-red-400 disabled:opacity-50"
                        >Remove</button>
                      ) : (
                        <span className="text-xs text-text-dim" title="App-owned routes are managed by the owning app">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─── Owner badge ─────────────────────────────────────────────────

function OwnerBadge({ kind, installId }: { kind: string; installId: number }) {
  if (installId === 0) {
    return <span className="text-xs text-text-dim">manual</span>;
  }
  return (
    <span className="text-xs text-accent" title={`install #${installId}`}>
      {kind || "app"} · #{installId}
    </span>
  );
}

// ─── Cert status cell ────────────────────────────────────────────

function CertCell({ cert, fqdn }: { cert: CertWire | undefined; fqdn: string }) {
  if (!cert) {
    return <span className="text-xs text-text-dim" title={`no cert for ${fqdn}`}>no cert</span>;
  }
  switch (cert.status) {
    case "live":
      return <span className="text-xs text-green" title={`expires ${cert.expires_at || "?"}`}>live</span>;
    case "issuing":
    case "pending":
      return <span className="text-xs text-blue" title="ACME issuance in progress">{cert.status}</span>;
    case "failed":
      return <span className="text-xs text-red" title={cert.error || "issuance failed"}>failed</span>;
    default:
      return <span className="text-xs text-text-dim">{cert.status}</span>;
  }
}

// ─── Manual add form ─────────────────────────────────────────────

function AddRouteForm({
  onAdd,
}: {
  onAdd: (hostname: string, target: string, allowHTTP: boolean) => Promise<void>;
}) {
  const [hostname, setHostname] = useState("");
  const [target, setTarget] = useState("");
  const [allowHTTP, setAllowHTTP] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hostname.trim() || !target.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await onAdd(hostname.trim(), target.trim(), allowHTTP);
      setHostname("");
      setTarget("");
      setAllowHTTP(false);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="p-4 border-b border-border flex gap-2 items-end flex-wrap">
      <label className="block">
        <div className="text-xs text-text-dim mb-1">Hostname</div>
        <input
          className={inputCls + " w-64 font-mono"}
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          placeholder="internal.example.com"
        />
      </label>
      <label className="block">
        <div className="text-xs text-text-dim mb-1">Target</div>
        <input
          className={inputCls + " w-64 font-mono"}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder="http://127.0.0.1:9000"
        />
      </label>
      <label className="flex items-center gap-1.5 cursor-pointer text-xs text-text-dim py-1.5">
        <input
          type="checkbox"
          checked={allowHTTP}
          onChange={(e) => setAllowHTTP(e.target.checked)}
        />
        Allow HTTP (skip HTTPS redirect)
      </label>
      <button
        type="submit"
        disabled={busy || !hostname.trim() || !target.trim()}
        className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
      >
        {busy ? "Adding…" : "Add route"}
      </button>
      {err && <div className="text-xs text-red-400 w-full">{err}</div>}
    </form>
  );
}
