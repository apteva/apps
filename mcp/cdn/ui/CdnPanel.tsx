// CdnPanel — operator UI for CDN zones.
//
// Lists every zone in the project, shows the composite + per-leg
// (dns / cert / route) status, and lets the operator stand up new
// zones or tear existing ones down. Zone orchestration runs in cdn
// via cross-app calls to domains / certs / routes; this panel is a
// thin REST consumer of the cdn sidecar.

import { useCallback, useEffect, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Zone {
  id: number;
  hostname: string;
  origin_url: string;
  record_type: string;
  record_value: string;
  allow_http?: boolean;
  status: "pending" | "active" | "error" | string;
  status_detail?: string;
  dns_status?: string;
  cert_status?: string;
  route_status?: string;
  created_at?: string;
  updated_at?: string;
}

const API = "/api/apps/cdn";

const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

// ─── Live app-event subscription ──────────────────────────────────
// Same shape RoutesPanel + DeployPanel use. No shared hook module.

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

// ─── Panel ────────────────────────────────────────────────────────

export default function CdnPanel({ projectId }: NativePanelProps) {
  const [zones, setZones] = useState<Zone[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const withParams = useCallback(
    () => new URLSearchParams({ project_id: projectId }).toString(),
    [projectId],
  );

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/zones?${withParams()}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { zones: Zone[] };
      setZones(j.zones || []);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
      setZones([]);
    }
  }, [withParams]);

  useEffect(() => { load(); }, [load]);

  // Light auto-refresh on route changes (registrations land async on
  // the routes side via routes.changed).
  useAppEvents("routes", projectId, () => load());

  const callTool = useCallback(
    async (tool: string, args: Record<string, unknown>) => {
      const r = await fetch(`${API}/tools/call?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tool, args }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      return r.json();
    },
    [withParams],
  );

  const addZone = useCallback(
    async (
      hostname: string,
      originURL: string,
      recordType: string,
      allowHTTP: boolean,
      skipDNS: boolean,
    ) => {
      await callTool("cdn_zone_create", {
        hostname,
        origin_url: originURL,
        record_type: recordType,
        allow_http: allowHTTP,
        skip_dns: skipDNS,
      });
      load();
    },
    [callTool, load],
  );

  const removeZone = useCallback(
    async (z: Zone) => {
      if (!confirm(`Tear down zone ${z.hostname}? DNS, cert and route get removed.`)) return;
      setBusy(true);
      try {
        await callTool("cdn_zone_delete", { id: z.id });
        load();
      } catch (e) {
        setErr("Delete failed: " + (e as Error).message);
      } finally {
        setBusy(false);
      }
    },
    [callTool, load],
  );

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <h1 className="text-lg font-semibold">CDN</h1>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          <span>{zones ? `${zones.length} zone${zones.length === 1 ? "" : "s"}` : "loading…"}</span>
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={load}
          >Refresh</button>
        </div>
      </div>

      <div className="px-6 py-2 border-b border-border text-xs text-text-dim">
        Public hostnames pointing at app origins. Each zone wires DNS (via domains), TLS (via certs) and the host→target route (via routes). v0.1 is local-mode — apteva-server is always the origin.
      </div>

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      <AddZoneForm onAdd={addZone} />

      <div className="flex-1 min-h-0 overflow-auto">
        {zones === null ? (
          <div className="p-6 text-text-dim text-sm">Loading…</div>
        ) : zones.length === 0 ? (
          <div className="p-6 text-text-dim text-sm">
            No zones yet. Use the form above to expose an app on a custom hostname — e.g. <span className="font-mono">files.acme.com</span> pointing at the storage sidecar.
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-xs text-text-dim">
              <tr className="border-b border-border">
                <th className="text-left px-4 py-2">Hostname</th>
                <th className="text-left px-4 py-2">Origin</th>
                <th className="text-left px-4 py-2">DNS</th>
                <th className="text-left px-4 py-2">Cert</th>
                <th className="text-left px-4 py-2">Route</th>
                <th className="text-left px-4 py-2">Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {zones.map((z) => (
                <tr key={z.id} className="border-b border-border hover:bg-surface-2 align-top">
                  <td className="px-4 py-2">
                    <a
                      href={`${z.allow_http ? "http" : "https"}://${z.hostname}/`}
                      target="_blank"
                      rel="noreferrer"
                      className="font-mono text-accent hover:underline break-all"
                    >{z.hostname}</a>
                    {z.allow_http && (
                      <span className="ml-2 text-xs text-text-dim">http ok</span>
                    )}
                    <div className="text-xs text-text-dim mt-0.5">
                      {z.record_type} → {z.record_value || "(skip_dns)"}
                    </div>
                  </td>
                  <td className="px-4 py-2 font-mono text-text-dim break-all">{z.origin_url}</td>
                  <td className="px-4 py-2 whitespace-nowrap"><LegBadge value={z.dns_status} /></td>
                  <td className="px-4 py-2 whitespace-nowrap"><LegBadge value={z.cert_status} /></td>
                  <td className="px-4 py-2 whitespace-nowrap"><LegBadge value={z.route_status} /></td>
                  <td className="px-4 py-2 whitespace-nowrap">
                    <StatusBadge value={z.status} detail={z.status_detail} />
                  </td>
                  <td className="px-4 py-2 text-right whitespace-nowrap">
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => removeZone(z)}
                      className="text-xs text-text-dim hover:text-red-400 disabled:opacity-50"
                    >Tear down</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─── Per-leg badge ────────────────────────────────────────────────

function LegBadge({ value }: { value?: string }) {
  if (!value) return <span className="text-xs text-text-dim">—</span>;
  switch (value) {
    case "ok":
      return <span className="text-xs text-green">ok</span>;
    case "error":
      return <span className="text-xs text-red">error</span>;
    case "skipped":
      return <span className="text-xs text-text-dim">skipped</span>;
    default:
      return <span className="text-xs text-text-dim">{value}</span>;
  }
}

// ─── Composite status ─────────────────────────────────────────────

function StatusBadge({ value, detail }: { value: string; detail?: string }) {
  switch (value) {
    case "active":
      return <span className="text-xs text-green">active</span>;
    case "pending":
      return <span className="text-xs text-blue">pending</span>;
    case "error":
      return (
        <span className="text-xs text-red" title={detail || "error"}>
          error
        </span>
      );
    default:
      return <span className="text-xs text-text-dim">{value}</span>;
  }
}

// ─── Add zone form ────────────────────────────────────────────────

function AddZoneForm({
  onAdd,
}: {
  onAdd: (
    hostname: string,
    originURL: string,
    recordType: string,
    allowHTTP: boolean,
    skipDNS: boolean,
  ) => Promise<void>;
}) {
  const [hostname, setHostname] = useState("");
  const [originURL, setOriginURL] = useState("");
  const [recordType, setRecordType] = useState("A");
  const [allowHTTP, setAllowHTTP] = useState(false);
  const [skipDNS, setSkipDNS] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hostname.trim() || !originURL.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await onAdd(hostname.trim(), originURL.trim(), recordType, allowHTTP, skipDNS);
      setHostname("");
      setOriginURL("");
      setAllowHTTP(false);
      setSkipDNS(false);
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
          placeholder="files.acme.com"
        />
      </label>
      <label className="block">
        <div className="text-xs text-text-dim mb-1">Origin URL</div>
        <input
          className={inputCls + " w-72 font-mono"}
          value={originURL}
          onChange={(e) => setOriginURL(e.target.value)}
          placeholder="http://127.0.0.1:8080"
        />
      </label>
      <label className="block">
        <div className="text-xs text-text-dim mb-1">DNS type</div>
        <select
          className={inputCls + " w-24"}
          value={recordType}
          onChange={(e) => setRecordType(e.target.value)}
          disabled={skipDNS}
        >
          <option value="A">A</option>
          <option value="CNAME">CNAME</option>
        </select>
      </label>
      <div className="flex flex-col gap-1 text-xs text-text-dim py-1">
        <label className="flex items-center gap-1.5 cursor-pointer">
          <input
            type="checkbox"
            checked={allowHTTP}
            onChange={(e) => setAllowHTTP(e.target.checked)}
          />
          allow_http (no cert)
        </label>
        <label className="flex items-center gap-1.5 cursor-pointer">
          <input
            type="checkbox"
            checked={skipDNS}
            onChange={(e) => setSkipDNS(e.target.checked)}
          />
          skip_dns (etc/hosts owns it)
        </label>
      </div>
      <button
        type="submit"
        disabled={busy || !hostname.trim() || !originURL.trim()}
        className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
      >
        {busy ? "Creating…" : "Create zone"}
      </button>
      {err && <div className="text-xs text-red-400 w-full">{err}</div>}
    </form>
  );
}
