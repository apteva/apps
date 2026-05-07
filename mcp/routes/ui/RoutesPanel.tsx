// RoutesPanel — admin table for hostname → target routes.
//
// Mounted at the install.settings slot (cross-app concern at the
// install level, not project-scoped). Lists every route with owner
// + cert status. Manual entries (owner_install_id=0) get full CRUD;
// app-owned rows are read-only here — their lifecycle belongs to
// the owning app's panel (deploy attaches/detaches via attach_domain;
// code starts/stops via repos_dev_start/_stop).

import { useCallback, useEffect, useState } from "react";

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

interface CertWire {
  id: number;
  fqdn: string;
  status: "pending" | "active" | "failed" | "revoked";
  expires_at?: string;
  error?: string;
}

const API = "/api/apps/routes/api";

export default function RoutesPanel({ projectId, installId }: NativePanelProps) {
  const [routes, setRoutes] = useState<Route[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [certs, setCerts] = useState<Record<string, CertWire>>({});

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
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setRoutes([]);
    }
  }, [withParams]);

  useEffect(() => { load(); }, [load]);

  // Cert status — best-effort. Hits the certs app's REST surface for
  // each route's cert_fqdn. Failures (cert app not installed, no cert
  // for this fqdn) collapse to "missing" silently — the column shows
  // "no cert" without spamming the panel.
  useEffect(() => {
    if (!routes) return;
    const fqdns = Array.from(new Set(routes.map((r) => r.cert_fqdn || r.hostname)));
    let cancelled = false;
    Promise.all(
      fqdns.map(async (fqdn) => {
        try {
          const r = await fetch(`/api/apps/certs/api/certs/${encodeURIComponent(fqdn)}?${withParams()}`, {
            credentials: "same-origin",
          });
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

  const removeRoute = async (hostname: string) => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/routes/${encodeURIComponent(hostname)}?${withParams()}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      await load();
    } catch (e) {
      setError("Delete failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex flex-col">
      <header className="px-4 py-3 border-b border-border flex items-baseline gap-3">
        <h1 className="text-text font-semibold">Routes</h1>
        <span className="text-xs text-text-muted flex-1">Hostname → target table. Apps register routes via routes_register.</span>
        <button
          type="button"
          onClick={load}
          disabled={busy}
          className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >Refresh</button>
      </header>

      {error && <div className="px-4 py-2 text-red text-xs border-b border-border">{error}</div>}

      <main className="flex-1 overflow-auto">
        {routes === null ? (
          <div className="p-6 text-text-muted text-sm">Loading…</div>
        ) : routes.length === 0 ? (
          <div className="p-6 text-text-muted text-sm">
            No routes yet. Install Routes-aware apps (Deploy, Code) and register routes from there,
            or use the form below to add a manual entry.
          </div>
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="sticky top-0 bg-bg-input/40">
              <tr className="text-text-muted">
                <th className="text-left px-3 py-2 font-normal">Hostname</th>
                <th className="text-left px-3 py-2 font-normal">Target</th>
                <th className="text-left px-3 py-2 font-normal">Owner</th>
                <th className="text-left px-3 py-2 font-normal">Cert</th>
                <th className="text-right px-3 py-2 font-normal">Actions</th>
              </tr>
            </thead>
            <tbody>
              {routes.map((r) => {
                const certFqdn = r.cert_fqdn || r.hostname;
                const cert = certs[certFqdn];
                return (
                  <tr key={r.id} className="border-t border-border hover:bg-bg-input/30">
                    <td className="px-3 py-2">
                      <a
                        href={`${r.allow_http ? "http" : "https"}://${r.hostname}/`}
                        target="_blank"
                        rel="noreferrer"
                        className="text-accent hover:underline"
                      >{r.hostname}</a>
                    </td>
                    <td className="px-3 py-2 text-text-muted">{r.target}</td>
                    <td className="px-3 py-2">
                      <OwnerBadge kind={r.owner_kind} installId={r.owner_install_id} />
                    </td>
                    <td className="px-3 py-2"><CertCell cert={cert} fqdn={certFqdn} /></td>
                    <td className="px-3 py-2 text-right">
                      {r.owner_install_id === 0 ? (
                        <button
                          type="button"
                          onClick={() => removeRoute(r.hostname)}
                          disabled={busy}
                          className="text-red/70 hover:text-red disabled:opacity-50"
                        >Delete</button>
                      ) : (
                        <span className="text-text-dim" title="App-owned routes are managed by the owning app">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </main>

      <ManualAddForm onAdded={() => { load(); }} withParams={withParams} setError={setError} />
    </div>
  );
}

function OwnerBadge({ kind, installId }: { kind: string; installId: number }) {
  if (installId === 0) {
    return <span className="px-1.5 py-0.5 text-[10px] rounded border border-border text-text-muted">manual</span>;
  }
  return (
    <span className="px-1.5 py-0.5 text-[10px] rounded border border-accent/40 text-accent" title={`install #${installId}`}>
      {kind || "app"} · #{installId}
    </span>
  );
}

function CertCell({ cert, fqdn }: { cert: CertWire | undefined; fqdn: string }) {
  if (!cert) {
    return <span className="text-text-dim" title={`no cert for ${fqdn}`}>missing</span>;
  }
  if (cert.status === "active") {
    return <span className="text-green" title={`expires ${cert.expires_at || "?"}`}>active</span>;
  }
  if (cert.status === "pending") {
    return <span className="text-blue" title="ACME issuing…">pending…</span>;
  }
  if (cert.status === "failed") {
    return <span className="text-red" title={cert.error || ""}>failed</span>;
  }
  return <span className="text-text-dim">{cert.status}</span>;
}

function ManualAddForm({
  onAdded,
  withParams,
  setError,
}: {
  onAdded: () => void;
  withParams: () => string;
  setError: (s: string) => void;
}) {
  const [hostname, setHostname] = useState("");
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hostname.trim() || !target.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/routes?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ hostname: hostname.trim(), target: target.trim() }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      setHostname("");
      setTarget("");
      onAdded();
    } catch (e) {
      setError("Add failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="px-4 py-3 border-t border-border flex items-end gap-2">
      <div className="flex-1">
        <label className="text-[11px] text-text-muted block mb-1">Hostname</label>
        <input
          type="text"
          value={hostname}
          onChange={(e) => setHostname(e.target.value)}
          placeholder="internal.example.com"
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
        />
      </div>
      <div className="flex-1">
        <label className="text-[11px] text-text-muted block mb-1">Target</label>
        <input
          type="text"
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder="http://127.0.0.1:9000"
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
        />
      </div>
      <button
        type="submit"
        disabled={busy || !hostname.trim() || !target.trim()}
        className="px-3 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
      >Add manual route</button>
    </form>
  );
}
