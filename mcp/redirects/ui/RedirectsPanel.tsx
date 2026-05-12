// RedirectsPanel — manage branded short links and domain redirects.
//
// Lists every rule for the current project (when the install is
// project-scoped) or every rule globally. The add form picks
// hostname/destination/path; advanced options (match mode, status
// code, preserve_path, preserve_query) hide behind a disclosure.
//
// We deliberately don't wire DNS / route status into the table cells
// here — the wiring warning from the create endpoint surfaces inline.
// The Domains / Routes panels remain the source of truth for those.

import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Redirect {
  id: number;
  hostname: string;
  path: string;
  match_mode: "exact" | "prefix";
  destination: string;
  status_code: 301 | 302 | 307 | 308;
  preserve_path: boolean;
  preserve_query: boolean;
  project_id?: string;
  notes?: string;
  hits: number;
  last_hit_at?: string;
  created_at?: string;
  updated_at?: string;
}

const API = "/api/apps/redirects/api";

export default function RedirectsPanel({ projectId, installId }: NativePanelProps) {
  const [rules, setRules] = useState<Redirect[] | null>(null);
  const [error, setError] = useState("");
  const [warning, setWarning] = useState("");
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
      const r = await fetch(`${API}/redirects?${withParams()}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { redirects: Redirect[] | null };
      setRules(j.redirects || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setRules([]);
    }
  }, [withParams]);

  useEffect(() => { load(); }, [load]);

  const remove = async (id: number) => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/redirects/${id}?${withParams()}`, {
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
        <h1 className="text-text font-semibold">Redirects</h1>
        <span className="text-xs text-text-muted flex-1">
          Branded short links and domain redirects. Composes on top of Routes (ingress) and Domains (DNS).
        </span>
        <button
          type="button"
          onClick={load}
          disabled={busy}
          className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >Refresh</button>
      </header>

      {error && <div className="px-4 py-2 text-red text-xs border-b border-border">{error}</div>}
      {warning && <div className="px-4 py-2 text-amber text-xs border-b border-border">Wiring warning: {warning}</div>}

      <main className="flex-1 overflow-auto">
        {rules === null ? (
          <div className="p-6 text-text-muted text-sm">Loading…</div>
        ) : rules.length === 0 ? (
          <EmptyState />
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="sticky top-0 bg-bg-input/40">
              <tr className="text-text-muted">
                <th className="text-left px-3 py-2 font-normal">Hostname</th>
                <th className="text-left px-3 py-2 font-normal">Path</th>
                <th className="text-left px-3 py-2 font-normal">Destination</th>
                <th className="text-left px-3 py-2 font-normal">Status</th>
                <th className="text-right px-3 py-2 font-normal">Hits</th>
                <th className="text-right px-3 py-2 font-normal">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id} className="border-t border-border hover:bg-bg-input/30">
                  <td className="px-3 py-2">
                    <a
                      href={`https://${r.hostname}${r.path === "/" ? "" : r.path}`}
                      target="_blank"
                      rel="noreferrer"
                      className="text-accent hover:underline"
                    >{r.hostname}</a>
                  </td>
                  <td className="px-3 py-2">
                    <span>{r.path}</span>
                    {r.match_mode === "prefix" && (
                      <span className="ml-1 text-text-dim text-[10px]">(prefix)</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-text-muted truncate max-w-[24rem]" title={r.destination}>
                    {r.destination}
                  </td>
                  <td className="px-3 py-2">
                    <StatusBadge code={r.status_code} />
                  </td>
                  <td className="px-3 py-2 text-right text-text-muted">
                    {r.hits.toLocaleString()}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => remove(r.id)}
                      disabled={busy}
                      className="text-red/70 hover:text-red disabled:opacity-50"
                    >Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </main>

      <AddForm onAdded={(warn) => { setWarning(warn || ""); load(); }} withParams={withParams} setError={setError} />
    </div>
  );
}

function StatusBadge({ code }: { code: number }) {
  const permanent = code === 301 || code === 308;
  return (
    <span
      className={
        "px-1.5 py-0.5 text-[10px] rounded border " +
        (permanent
          ? "border-accent/40 text-accent"
          : "border-border text-text-muted")
      }
      title={permanent ? "permanent (cached by browsers)" : "temporary"}
    >
      {code}
    </span>
  );
}

function EmptyState() {
  return (
    <div className="p-8 flex flex-col items-center text-center gap-3">
      <svg width="48" height="48" viewBox="0 0 24 24" fill="none"
        stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"
        style={{ color: "var(--color-text-dim)" }}>
        <path d="M15 14l5-5-5-5" />
        <path d="M4 20v-7a4 4 0 0 1 4-4h12" />
      </svg>
      <div className="text-text-muted text-sm">No redirect rules yet.</div>
      <div className="text-text-dim text-xs max-w-md">
        Add one below. The hostname will be claimed with Routes automatically; if it's
        managed by the Domains app, a CNAME pointing at the platform will be upserted too.
      </div>
    </div>
  );
}

function AddForm({
  onAdded,
  withParams,
  setError,
}: {
  onAdded: (warning: string) => void;
  withParams: () => string;
  setError: (s: string) => void;
}) {
  const [hostname, setHostname] = useState("");
  const [path, setPath] = useState("/");
  const [destination, setDestination] = useState("");
  const [match, setMatch] = useState<"exact" | "prefix">("exact");
  const [status, setStatus] = useState(302);
  const [preservePath, setPreservePath] = useState(false);
  const [preserveQuery, setPreserveQuery] = useState(true);
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hostname.trim() || !destination.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/redirects?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          hostname: hostname.trim(),
          path: path.trim() || "/",
          match_mode: match,
          destination: destination.trim(),
          status_code: status,
          preserve_path: preservePath,
          preserve_query: preserveQuery,
        }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { warning?: string };
      setHostname("");
      setPath("/");
      setDestination("");
      setPreservePath(false);
      onAdded(j.warning || "");
    } catch (e) {
      setError("Add failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="px-4 py-3 border-t border-border flex flex-col gap-2">
      <div className="flex items-end gap-2">
        <div className="flex-1">
          <label className="text-[11px] text-text-muted block mb-1">Hostname</label>
          <input
            type="text"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            placeholder="go.example.com"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
          />
        </div>
        <div className="w-32">
          <label className="text-[11px] text-text-muted block mb-1">Path</label>
          <input
            type="text"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
          />
        </div>
        <div className="flex-1">
          <label className="text-[11px] text-text-muted block mb-1">Destination URL</label>
          <input
            type="text"
            value={destination}
            onChange={(e) => setDestination(e.target.value)}
            placeholder="https://example.com/landing"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
          />
        </div>
        <button
          type="submit"
          disabled={busy || !hostname.trim() || !destination.trim()}
          className="px-3 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >Add redirect</button>
      </div>

      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="text-[11px] text-text-muted hover:text-text self-start"
      >
        {expanded ? "Hide" : "Show"} advanced options
      </button>

      {expanded && (
        <div className="flex items-end gap-3 pl-1">
          <div>
            <label className="text-[11px] text-text-muted block mb-1">Match</label>
            <select
              value={match}
              onChange={(e) => {
                const v = e.target.value as "exact" | "prefix";
                setMatch(v);
                if (v !== "prefix") setPreservePath(false);
              }}
              className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
            >
              <option value="exact">exact</option>
              <option value="prefix">prefix</option>
            </select>
          </div>
          <div>
            <label className="text-[11px] text-text-muted block mb-1">Status</label>
            <select
              value={status}
              onChange={(e) => setStatus(Number(e.target.value))}
              className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
            >
              <option value={301}>301 (permanent)</option>
              <option value={302}>302 (temporary)</option>
              <option value={307}>307 (temporary, preserve method)</option>
              <option value={308}>308 (permanent, preserve method)</option>
            </select>
          </div>
          <label className="text-[11px] text-text-muted flex items-center gap-1 cursor-pointer">
            <input
              type="checkbox"
              checked={preservePath}
              onChange={(e) => setPreservePath(e.target.checked)}
              disabled={match !== "prefix"}
            />
            preserve path
          </label>
          <label className="text-[11px] text-text-muted flex items-center gap-1 cursor-pointer">
            <input
              type="checkbox"
              checked={preserveQuery}
              onChange={(e) => setPreserveQuery(e.target.checked)}
            />
            preserve query
          </label>
        </div>
      )}
    </form>
  );
}
