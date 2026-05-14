// CertsPanel — operator UI for the certs app.
//
// One view: the project's TLS certs, with an issue form and per-row
// renew / revoke. Issuance is async (pending → issuing → live|failed)
// so the panel light-polls while anything is in flight.
//
// Pure REST against /api/apps/certs — every handler resolves the
// project from the ?project_id= query param, so this works for both
// project-scoped and global installs without the _project_id-in-args
// dance the tool dispatcher needs.

import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Cert {
  id: number;
  fqdn: string;
  status: string; // pending | issuing | live | failed | revoked
  serial?: string;
  issued_at?: string;
  expires_at?: string;
  last_renewed_at?: string;
  last_attempt_at?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

interface Meta {
  challenge_type: string;    // resolved: "dns-01" | "http-01"
  domains_available: boolean;
  domains: string[];         // registered apexes — dns-01 only
}

// The certs app registers its HTTP routes at /api/certs and /api/certs/
// (handlers.go), so the panel-visible base includes that /api segment:
// /api/apps/certs (platform mount) + /api (app's own prefix).
const API = "/api/apps/certs/api";

// Shared input class — same tokens the domains panel uses so the look
// matches across the dashboard's dark theme.
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

function statusColor(s: string): string {
  switch (s) {
    case "live": return "text-green";
    case "issuing":
    case "pending": return "text-blue";
    case "failed": return "text-red";
    default: return "text-text-dim"; // revoked, unknown
  }
}

function fmtDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleDateString();
}

// daysLeft returns a short "in 42d" / "expired" hint, or "" when there
// is no expiry yet (pending / failed certs).
function daysLeft(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const ms = d.getTime() - Date.now();
  const days = Math.round(ms / 86400000);
  if (days < 0) return "expired";
  if (days === 0) return "today";
  return `in ${days}d`;
}

// challengeStatusLine summarises which ACME path issuance will take,
// so the panel confirms (or warns about) the Domains-app link.
function challengeStatusLine(m: Meta): string {
  if (m.challenge_type === "http-01") {
    return "Challenge: HTTP-01 · served from webroot";
  }
  // dns-01
  if (m.domains_available && m.domains.length > 0) {
    return `Challenge: DNS-01 · via Domains app · ${m.domains.join(", ")}`;
  }
  return "Challenge: DNS-01 · Domains app not linked or has no registered domains — issuance will fail";
}

export default function CertsPanel({ projectId, installId }: NativePanelProps) {
  const [certs, setCerts] = useState<Cert[]>([]);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const withParams = useCallback((extra: Record<string, string>) => {
    return new URLSearchParams({
      project_id: projectId,
      install_id: String(installId),
      ...extra,
    }).toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(
    method: string, path: string,
    params?: Record<string, string>, body?: unknown,
  ): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const qs = withParams(params || {});
    const res = await fetch(`${API}${path}?${qs}`, opts);
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`${res.status}: ${text}`);
    }
    return res.json();
  }, [withParams]);

  const reload = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ certs: Cert[] }>(
        "GET", "/certs",
        includeRevoked ? { include_revoked: "1" } : {},
      );
      setCerts(r.certs || []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [api, includeRevoked]);

  useEffect(() => { reload(); }, [reload]);

  // Resolved challenge type + linked Domains-app state. Soft-fail: if
  // the endpoint errors, the status line just doesn't render.
  const loadMeta = useCallback(async () => {
    try {
      setMeta(await api<Meta>("GET", "/_meta", {}));
    } catch {
      setMeta(null);
    }
  }, [api]);

  useEffect(() => { loadMeta(); }, [loadMeta]);

  // Light-poll while any cert is mid-issuance. The effect re-arms each
  // time `certs` changes, so it keeps ticking every 4s until every row
  // has settled to live / failed / revoked, then stops on its own.
  useEffect(() => {
    const inflight = certs.some((c) => c.status === "pending" || c.status === "issuing");
    if (!inflight) return;
    const t = setTimeout(() => { reload(); }, 4000);
    return () => clearTimeout(t);
  }, [certs, reload]);

  const issue = useCallback(async (fqdn: string) => {
    await api("POST", "/certs", {}, { fqdn });
    reload();
  }, [api, reload]);

  const renew = useCallback(async (c: Cert) => {
    try {
      await api("POST", `/certs/${c.id}/renew`, {});
      reload();
    } catch (e) {
      alert((e as Error).message);
    }
  }, [api, reload]);

  const revoke = useCallback(async (c: Cert) => {
    if (!confirm(`Revoke the cert for ${c.fqdn}? The TLS cache stops serving it. (The registration at Let's Encrypt is left alone.)`)) return;
    try {
      await api("POST", `/certs/${c.id}/revoke`, {});
      reload();
    } catch (e) {
      alert((e as Error).message);
    }
  }, [api, reload]);

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <h1 className="text-lg font-semibold">Certs</h1>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading…</span>}
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={() => { reload(); loadMeta(); }}
          >Refresh</button>
        </div>
      </div>

      {meta && (
        <div className="px-6 py-2 border-b border-border text-xs text-text-dim">
          {challengeStatusLine(meta)}
        </div>
      )}

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      <IssueCertForm onIssue={issue} />

      <div className="px-4 py-2 flex items-center gap-2 border-b border-border text-xs text-text-dim">
        <label className="flex items-center gap-1.5 cursor-pointer">
          <input
            type="checkbox"
            checked={includeRevoked}
            onChange={(e) => setIncludeRevoked(e.target.checked)}
          />
          Show revoked
        </label>
      </div>

      <div className="flex-1 min-h-0 overflow-auto">
        {certs.length === 0 ? (
          <div className="p-6 text-text-dim text-sm">
            No certs yet. Issue one above — the FQDN must resolve to this host (HTTP-01) or sit under a domain managed by the Domains app (DNS-01).
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-xs text-text-dim">
              <tr className="border-b border-border">
                <th className="text-left px-4 py-2">FQDN</th>
                <th className="text-left px-4 py-2">Status</th>
                <th className="text-left px-4 py-2">Expires</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {certs.map((c) => (
                <CertRow key={c.id} cert={c} onRenew={renew} onRevoke={revoke} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─── Issue form ──────────────────────────────────────────────────

function IssueCertForm({ onIssue }: { onIssue: (fqdn: string) => Promise<void> }) {
  const [fqdn, setFqdn] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await onIssue(fqdn.trim());
      setFqdn("");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="p-4 border-b border-border flex gap-2 items-end flex-wrap">
      <Field label="FQDN">
        <input
          className={inputCls + " w-72"}
          value={fqdn}
          onChange={(e) => setFqdn(e.target.value)}
          placeholder="app.acme.com"
          required
        />
      </Field>
      <button
        type="submit"
        disabled={busy || !fqdn.trim()}
        className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
      >
        {busy ? "Issuing…" : "Issue cert"}
      </button>
      {err && <div className="text-xs text-red-400 w-full">{err}</div>}
      <div className="text-xs text-text-dim w-full">
        Issuance is async — the cert appears as <span className="text-blue">pending</span>, then
        {" "}<span className="text-blue">issuing</span>, then{" "}
        <span className="text-green">live</span>. This panel refreshes itself while it runs.
      </div>
    </form>
  );
}

// ─── Cert row ────────────────────────────────────────────────────

function CertRow({
  cert, onRenew, onRevoke,
}: {
  cert: Cert;
  onRenew: (c: Cert) => void;
  onRevoke: (c: Cert) => void;
}) {
  const expiry = daysLeft(cert.expires_at);
  const revoked = cert.status === "revoked";
  return (
    <tr className="border-b border-border align-top">
      <td className="px-4 py-2">
        <div className="font-mono text-text break-all">{cert.fqdn}</div>
        {cert.status === "failed" && cert.error && (
          <div className="text-xs text-red-400 mt-1 whitespace-pre-wrap break-all">{cert.error}</div>
        )}
      </td>
      <td className="px-4 py-2">
        <span className={statusColor(cert.status) + " font-medium"}>{cert.status}</span>
      </td>
      <td className="px-4 py-2 text-text-dim whitespace-nowrap">
        {fmtDate(cert.expires_at)}
        {expiry && <span className="text-xs ml-1">({expiry})</span>}
      </td>
      <td className="px-4 py-2 text-right whitespace-nowrap">
        {!revoked && (
          <>
            <button
              type="button"
              className="text-xs text-text-dim hover:text-text"
              onClick={() => onRenew(cert)}
            >Renew</button>
            <button
              type="button"
              className="text-xs text-text-dim hover:text-red-400 ml-3"
              onClick={() => onRevoke(cert)}
            >Revoke</button>
          </>
        )}
      </td>
    </tr>
  );
}

// ─── Tiny shared primitives ──────────────────────────────────────

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      {children}
    </label>
  );
}
