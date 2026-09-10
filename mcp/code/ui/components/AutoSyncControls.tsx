import React, { useEffect, useRef, useState } from "react";

type API = <T>(method: string, path: string, body?: unknown) => Promise<T>;
interface SyncState {
  enabled: boolean;
  branch: string;
  remote_branch: string;
  status: string;
  last_sync_at: number;
  retry_at: number;
  last_error?: string;
}
const labels: Record<string, string> = {
  paused: "Paused", pending: "Pending changes", syncing: "Syncing",
  synced: "Synced", offline: "Offline", needs_attention: "Needs attention",
};
export function AutoSyncControls({ slug, branch, upstream, api, onSynced }: {
  slug: string; branch?: string; upstream?: string; api: API; onSynced: () => void;
}) {
  const [state, setState] = useState<SyncState | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [history, setHistory] = useState<Array<{ sha: string; subject: string; authored_at: string }> | null>(null);
  const onSyncedRef = useRef(onSynced);
  onSyncedRef.current = onSynced;
  const latest = useRef<number | null>(null);
  const endpoint = `/repos/${encodeURIComponent(slug)}/git/sync`;
  useEffect(() => {
    let cancelled = false;
    let loading = false;
    latest.current = null;
    setState(null); setError(""); setHistory(null); setConfirm(false);
    const refresh = async () => {
      if (loading) return;
      loading = true;
      try {
        const next = await api<SyncState>("GET", endpoint);
        if (cancelled) return;
        if (next.last_sync_at && latest.current !== null && next.last_sync_at !== latest.current) onSyncedRef.current();
        latest.current = next.last_sync_at;
        setState(next);
        setError("");
      } catch (e) { if (!cancelled) setError((e as Error).message); }
      finally { loading = false; }
    };
    void refresh();
    const timer = setInterval(refresh, 3000);
    return () => { cancelled = true; clearInterval(timer); };
  }, [api, endpoint]);
  const act = async (action: "enable" | "pause" | "now") => {
    setBusy(true); setError(""); setConfirm(false);
    try {
      const next = await api<SyncState>(action === "now" ? "POST" : "PATCH", endpoint + (action === "now" ? "/now" : ""), action === "now" ? undefined : { enabled: action === "enable", branch });
      setState(next);
      if (next.status === "synced") onSyncedRef.current();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };
  const showHistory = async () => {
    setBusy(true);
    try { const result = await api<{ commits: Array<{ sha: string; subject: string; authored_at: string }> }>("GET", `/repos/${encodeURIComponent(slug)}/git/log`); setHistory(result.commits); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };
  return <div className="border-b border-border px-3 py-2 text-xs bg-bg-panel">
    <div className="flex items-center flex-wrap gap-3">
      <span className="font-medium">Auto-sync</span>
      <span className={state?.status === "needs_attention" ? "text-red" : state?.status === "synced" ? "text-green" : "text-text-muted"} role="status">{busy ? "Working…" : state ? labels[state.status] || state.status : "Loading…"}</span>
      {state?.enabled && <span className="font-mono text-text-muted">{state.branch} → origin/{state.remote_branch}</span>}
      {state?.last_sync_at ? <span className="text-text-dim">Last synced {new Date(state.last_sync_at).toLocaleTimeString()}</span> : null}
      <span className="flex-1" />
      {state?.enabled ? <>
        <button disabled={busy} onClick={() => void act("now")} className="text-accent disabled:opacity-40">{state.status === "needs_attention" ? "Retry sync" : "Sync now"}</button>
        <button disabled={busy} onClick={() => void act("pause")} className="text-text-muted disabled:opacity-40">Pause</button>
      </> : <button disabled={busy || !state || !upstream?.startsWith("origin/")} onClick={() => setConfirm(true)} className="text-accent disabled:opacity-40" title={!upstream ? "Push or connect a branch with origin tracking first" : `Enable for ${branch} → ${upstream}`}>Enable auto-sync</button>}
      <button disabled={busy} onClick={() => void showHistory()} className="text-text-muted disabled:opacity-40">View history</button>
    </div>
    {(error || state?.last_error) && <div className="mt-2 text-red break-words" role="alert">{error || state?.last_error}{state?.status === "offline" && state.retry_at ? ` Retrying after ${new Date(state.retry_at).toLocaleTimeString()}. Local commits are retained.` : ""}</div>}
    {confirm && <div role="dialog" aria-label="Enable auto-sync" className="mt-3 p-3 border border-border rounded space-y-2">
      <p>Automatically commit saved changes on <strong>{branch}</strong> and sync with <strong>{upstream}</strong>?</p>
      <p className="text-text-muted">Changes are grouped after 10 seconds of inactivity, with a checkpoint every minute during continuous editing. Remote updates are checked every minute. Diverging histories pause synchronization for you to resolve.</p>
      <div className="flex gap-4"><button onClick={() => void act("enable")} className="text-accent">Enable</button><button onClick={() => setConfirm(false)}>Cancel</button></div>
    </div>}
    {history && <div role="dialog" aria-label="Git history" className="mt-3 p-3 border border-border rounded">
      <div className="flex justify-between mb-2"><strong>Recent commits</strong><button onClick={() => setHistory(null)}>Close</button></div>
      <div className="max-h-64 overflow-auto space-y-2">{history.map(commit => <div key={commit.sha}><code className="text-text-muted">{commit.sha.slice(0, 8)}</code> {commit.subject}<span className="ml-2 text-text-dim">{new Date(commit.authored_at).toLocaleString()}</span></div>)}</div>
    </div>}
  </div>;
}
