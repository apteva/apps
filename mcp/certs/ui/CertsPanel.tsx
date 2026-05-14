// CertsPanel — operator UI for the certs app.
//
// Layout: when the Domains app is linked, the body is a list of
// registered apexes. Each apex is a group: a header with a "+ Issue
// cert" toggle (expands an inline subdomain field) and, nested under
// it, the certs that live under that apex. Certs that don't sit under
// any registered apex (http-01 certs, or certs whose domain was
// removed from the Domains app) fall into an "Other" group.
//
// When no domains are linked, the body falls back to a plain FQDN
// issue form + a flat cert list.
//
// Pure REST against /api/apps/certs/api — every handler resolves the
// project from ?project_id=, so this works for project-scoped and
// global installs alike.

import { useCallback, useEffect, useMemo, useState } from "react";

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

// Shared input class — same tokens the domains panel uses.
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

// challengeStatusLine summarises which ACME path issuance will take.
function challengeStatusLine(m: Meta): string {
  if (m.challenge_type === "http-01") {
    return "Challenge: HTTP-01 · served from webroot";
  }
  if (m.domains_available && m.domains.length > 0) {
    return `Challenge: DNS-01 · via Domains app · ${m.domains.join(", ")}`;
  }
  return "Challenge: DNS-01 · Domains app not linked or has no registered domains — issuance will fail";
}

// apexForFqdn finds the longest registered apex that fqdn sits under,
// or null when none matches. Mirrors the backend's resolveApex.
function apexForFqdn(fqdn: string, apexes: string[]): string | null {
  const f = fqdn.toLowerCase().replace(/\.$/, "");
  let best: string | null = null;
  for (const a of apexes) {
    const ap = a.toLowerCase();
    if (f === ap || f.endsWith("." + ap)) {
      if (!best || ap.length > best.length) best = ap;
    }
  }
  return best;
}

export default function CertsPanel({ projectId, installId }: NativePanelProps) {
  const [certs, setCerts] = useState<Cert[]>([]);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [acmeEmail, setAcmeEmail] = useState("");
  const [emailLoaded, setEmailLoaded] = useState(false);
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

  // Resolved challenge type + linked Domains-app state. On failure we
  // store an empty Meta (not null) so `meta === null` strictly means
  // "first load hasn't finished" — and the body falls back to the
  // plain issue form rather than rendering nothing forever.
  const loadMeta = useCallback(async () => {
    try {
      setMeta(await api<Meta>("GET", "/_meta", {}));
    } catch {
      setMeta({ challenge_type: "", domains_available: false, domains: [] });
    }
  }, [api]);

  useEffect(() => { loadMeta(); }, [loadMeta]);

  // acme_email lives in the platform's install config, not the certs
  // app's own surface — read/write it via /api/apps/installs/:id/config
  // so the operator can set it without leaving the panel. Soft-fail on
  // read: emailLoaded still flips, so the banner shows either way.
  const loadEmail = useCallback(async () => {
    try {
      const res = await fetch(`/api/apps/installs/${installId}/config`, {
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(String(res.status));
      const j = await res.json();
      setAcmeEmail((j.config?.acme_email as string) || "");
    } catch {
      // leave acmeEmail empty — the banner will prompt for it
    } finally {
      setEmailLoaded(true);
    }
  }, [installId]);

  useEffect(() => { loadEmail(); }, [loadEmail]);

  const saveEmail = useCallback(async (email: string) => {
    const res = await fetch(`/api/apps/installs/${installId}/config`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: { acme_email: email } }),
    });
    if (!res.ok) {
      const t = await res.text().catch(() => "");
      throw new Error(`${res.status}: ${t}`);
    }
    setAcmeEmail(email);
  }, [installId]);

  // Light-poll while any cert is mid-issuance. Re-arms on every `certs`
  // change, so it ticks every 4s until everything settles, then stops.
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

  const domains = useMemo(() => meta?.domains ?? [], [meta]);

  // Issuance fails server-side without acme_email; gate the issue
  // affordances on it so the blocking banner is actually blocking.
  const issueDisabled = emailLoaded && !acmeEmail;

  // Group certs under their apex; anything unmatched goes to `other`.
  const { byApex, other } = useMemo(() => {
    const byApex: Record<string, Cert[]> = {};
    for (const d of domains) byApex[d] = [];
    const other: Cert[] = [];
    for (const c of certs) {
      const apex = apexForFqdn(c.fqdn, domains);
      if (apex && byApex[apex]) byApex[apex].push(c);
      else other.push(c);
    }
    return { byApex, other };
  }, [certs, domains]);

  return (
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <h1 className="text-lg font-semibold">Certs</h1>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading…</span>}
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={() => { reload(); loadMeta(); loadEmail(); }}
          >Refresh</button>
        </div>
      </div>

      {meta && meta.challenge_type && (
        <div className="px-6 py-2 border-b border-border text-xs text-text-dim">
          {challengeStatusLine(meta)}
        </div>
      )}

      {emailLoaded && !acmeEmail && <EmailBanner onSave={saveEmail} />}

      {err && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

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
        {domains.map((apex) => (
          <DomainGroup
            key={apex}
            apex={apex}
            certs={byApex[apex] || []}
            issueDisabled={issueDisabled}
            onIssue={issue}
            onRenew={renew}
            onRevoke={revoke}
          />
        ))}

        {other.length > 0 && (
          <OtherGroup certs={other} onRenew={renew} onRevoke={revoke} />
        )}

        {/* No domains linked — fall back to a plain FQDN issue form. */}
        {meta && domains.length === 0 && (
          <>
            <IssueCertForm onIssue={issue} issueDisabled={issueDisabled} />
            {certs.length === 0 ? (
              <div className="p-6 text-text-dim text-sm">
                No certs yet. Issue one above — for DNS-01, link the Domains app and register a domain so you can pick it here.
              </div>
            ) : (
              <div className="pb-1">
                {certs.map((c) => (
                  <CertRow key={c.id} cert={c} onRenew={renew} onRevoke={revoke} />
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ─── Domain group ────────────────────────────────────────────────

function DomainGroup({
  apex, certs, issueDisabled, onIssue, onRenew, onRevoke,
}: {
  apex: string;
  certs: Cert[];
  issueDisabled?: boolean;
  onIssue: (fqdn: string) => Promise<void>;
  onRenew: (c: Cert) => void;
  onRevoke: (c: Cert) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [sub, setSub] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const s = sub.trim().replace(/\.$/, "");
    const fqdn = s ? `${s}.${apex}` : apex;
    setBusy(true);
    setErr("");
    try {
      await onIssue(fqdn);
      setSub("");
      setExpanded(false);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="border-b border-border">
      <div className="flex items-center justify-between px-4 py-3">
        <span className="font-medium">{apex}</span>
        <button
          type="button"
          disabled={issueDisabled}
          className="text-xs text-accent hover:underline disabled:opacity-50 disabled:cursor-not-allowed"
          onClick={() => { setExpanded((v) => !v); setErr(""); }}
        >{expanded ? "Cancel" : "+ Issue cert"}</button>
      </div>

      {expanded && (
        <form onSubmit={submit} className="px-4 pb-3 flex items-end gap-2 flex-wrap">
          <div className="flex items-center gap-1">
            <input
              className={inputCls + " w-44"}
              value={sub}
              onChange={(e) => setSub(e.target.value)}
              placeholder="app  (blank = apex)"
              autoFocus
            />
            <span className="text-text-dim text-sm">.{apex}</span>
          </div>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50 text-xs"
          >{busy ? "Issuing…" : "Issue"}</button>
          {err && <div className="text-xs text-red-400 w-full">{err}</div>}
        </form>
      )}

      {certs.length === 0 ? (
        <div className="px-4 pb-3 text-xs text-text-dim">no certs yet</div>
      ) : (
        <div className="pb-1">
          {certs.map((c) => (
            <CertRow key={c.id} cert={c} onRenew={onRenew} onRevoke={onRevoke} />
          ))}
        </div>
      )}
    </div>
  );
}

// ─── Other group (certs under no registered apex) ────────────────

function OtherGroup({
  certs, onRenew, onRevoke,
}: {
  certs: Cert[];
  onRenew: (c: Cert) => void;
  onRevoke: (c: Cert) => void;
}) {
  return (
    <div className="border-b border-border">
      <div className="px-4 py-3">
        <span className="font-medium">Other</span>
        <span className="text-xs text-text-dim ml-2">not under a registered domain</span>
      </div>
      <div className="pb-1">
        {certs.map((c) => (
          <CertRow key={c.id} cert={c} onRenew={onRenew} onRevoke={onRevoke} />
        ))}
      </div>
    </div>
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
    <div className="px-4 py-1.5 flex items-center justify-between gap-2 hover:bg-surface-2">
      <div className="min-w-0">
        <div className="font-mono text-sm text-text break-all">{cert.fqdn}</div>
        {cert.status === "failed" && cert.error && (
          <div className="text-xs text-red-400 whitespace-pre-wrap break-all">{cert.error}</div>
        )}
      </div>
      <div className="flex items-center gap-2 text-xs whitespace-nowrap">
        <span className={statusColor(cert.status) + " font-medium"}>{cert.status}</span>
        <span className="text-text-dim">
          {fmtDate(cert.expires_at)}{expiry && ` (${expiry})`}
        </span>
        {!revoked && (
          <>
            <button
              type="button"
              className="text-text-dim hover:text-text ml-1"
              onClick={() => onRenew(cert)}
            >Renew</button>
            <button
              type="button"
              className="text-text-dim hover:text-red-400 ml-1"
              onClick={() => onRevoke(cert)}
            >Revoke</button>
          </>
        )}
      </div>
    </div>
  );
}

// ─── Plain issue form (fallback when no domains are linked) ──────

function IssueCertForm({
  onIssue, issueDisabled,
}: {
  onIssue: (fqdn: string) => Promise<void>;
  issueDisabled?: boolean;
}) {
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
        disabled={busy || !fqdn.trim() || issueDisabled}
        className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
      >{busy ? "Issuing…" : "Issue cert"}</button>
      {err && <div className="text-xs text-red-400 w-full">{err}</div>}
    </form>
  );
}

// ─── ACME email banner (blocking — certs can't issue without it) ──

function EmailBanner({ onSave }: { onSave: (email: string) => Promise<void> }) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await onSave(email.trim());
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10">
      <div className="text-sm text-red-300 mb-2">
        Set an ACME contact email before issuing certs — Let's Encrypt requires it to register an account.
      </div>
      <div className="flex items-end gap-2 flex-wrap">
        <input
          className={inputCls + " w-72"}
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
          required
        />
        <button
          type="submit"
          disabled={busy || !email.trim()}
          className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50 text-xs"
        >{busy ? "Saving…" : "Save"}</button>
        {err && <div className="text-xs text-red-400 w-full">{err}</div>}
      </div>
    </form>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      {children}
    </label>
  );
}
