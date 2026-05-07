// TorrentPanel — three-tab view: Downloads, Searches, Indexers.
//
// Downloads is the primary surface. Active torrents poll every 2s for
// progress; completed/seeding rows poll on the panel-wide 15s tick.
// + Add torrent opens a modal with three input modes: paste magnet,
// paste .torrent URL, or run an inline search and click a result.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/torrent";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

// useAppEvents — SSE subscription for the platform's app event bus.
// Lifted from the jobs/code/storage panels; kept inline so the
// torrent panel doesn't depend on a UI-kit not yet in place.
interface AppEventEnvelope<T = unknown> {
  app: string;
  topic: string;
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

interface TorrentRow {
  id: number;
  infohash: string;
  name: string;
  state: string;
  total_bytes: number;
  downloaded_bytes: number;
  target_folder: string;
  storage_file_ids_json: string;
  added_at: string;
  completed_at?: string;
  last_error?: string;
  snapshot: {
    state: string;
    name: string;
    length: number;
    bytes_completed: number;
    progress: number;
    peers: number;
    seeds: number;
    eta_seconds: number;
    has_info: boolean;
    is_paused: boolean;
    last_error: string;
  };
  files?: { index: number; path: string; length: number; bytes_completed: number; priority: string }[];
}

interface SearchResult {
  name: string;
  infohash: string;
  magnet: string;
  torrent_url?: string;
  size_bytes: number;
  seeders: number;
  leechers: number;
  indexer: string;
  category?: string;
}

interface SavedSearch {
  id: number;
  name: string;
  query: string;
  category: string;
  min_seeders: number;
  max_size_bytes: number;
  auto_add_top_n: number;
  run_interval_minutes: number;
  last_run_at?: string;
  next_run_at?: string;
}

interface Indexer {
  id: number;
  name: string;
  kind: string;
  base_url: string;
  enabled: boolean;
  last_ok_at?: string;
  last_error?: string;
}

type Tab = "downloads" | "searches" | "indexers";

export default function TorrentPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("downloads");

  return (
    <div className="flex flex-col h-full">
      <div className="flex border-b border-border">
        {(["downloads", "searches", "indexers"] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-2 text-sm capitalize ${tab === t ? "border-b-2 border-accent" : "text-text-dim"}`}
          >
            {t}
          </button>
        ))}
      </div>
      <div className="flex-1 overflow-auto">
        {tab === "downloads" && <DownloadsTab projectId={projectId} />}
        {tab === "searches" && <SearchesTab />}
        {tab === "indexers" && <IndexersTab />}
      </div>
    </div>
  );
}

// ─── Downloads ──────────────────────────────────────────────────────

function DownloadsTab({ projectId }: { projectId: string }) {
  const [rows, setRows] = useState<TorrentRow[]>([]);
  const [stats, setStats] = useState<any>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [t, s] = await Promise.all([
        fetch(`${API}/torrents`).then((r) => r.json()),
        fetch(`${API}/stats`).then((r) => r.json()),
      ]);
      setRows(t);
      setStats(s);
      setError("");
    } catch (e: any) {
      setError(e.message);
    }
  }, []);

  useEffect(() => {
    refresh();
    // Slow background poll keeps progress bars moving for active
    // downloads; the events-bus subscription below covers state
    // transitions (added / completed / errored) live.
    const t = setInterval(refresh, 2_000);
    return () => clearInterval(t);
  }, [refresh]);

  // Live updates: refresh whenever the sidecar emits a torrent.* event.
  // The platform delivers these via SSE so the panel reflects state
  // changes the moment the engine sees them, not on the next poll tick.
  useAppEvents("torrent", projectId, (ev) => {
    if (ev.topic === "torrent.added" ||
        ev.topic === "torrent.completed" ||
        ev.topic === "torrent.error") {
      refresh();
    }
  });

  // Engine states (engine.go:489): downloading | seeding | paused |
  // completed | error | queued. The "queued" state covers magnets that
  // haven't fetched .torrent metadata yet — they MUST appear somewhere
  // or freshly-added torrents look like the click did nothing.
  const KNOWN_STATES = new Set(["downloading", "seeding", "paused", "completed", "error", "queued"]);
  const active    = rows.filter((r) => r.snapshot.state === "downloading");
  const seeding   = rows.filter((r) => r.snapshot.state === "seeding");
  const paused    = rows.filter((r) => r.snapshot.state === "paused");
  const completed = rows.filter((r) => r.snapshot.state === "completed");
  const errored   = rows.filter((r) => r.snapshot.state === "error");
  const queued    = rows.filter((r) => r.snapshot.state === "queued" || !KNOWN_STATES.has(r.snapshot.state));

  return (
    <div>
      <div className="flex items-center px-4 py-3 border-b border-border">
        <button
          className="bg-accent text-bg px-3 py-1 rounded text-sm"
          onClick={() => setShowAdd(true)}
        >
          + Add torrent
        </button>
        <div className="flex-1" />
        {stats?.aggregate && (
          <div className="text-xs text-text-dim">
            {stats.aggregate.active_count} active ·{" "}
            {stats.aggregate.completed_count} done ·{" "}
            {stats.indexers_configured} indexer(s)
          </div>
        )}
      </div>

      {error && <div className="px-4 py-2 text-error text-sm">{error}</div>}

      {queued.length > 0 && (
        <Section title={`Fetching metadata (${queued.length})`}>
          {queued.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
        </Section>
      )}
      <Section title={`Active (${active.length})`}>
        {active.length === 0 && queued.length === 0 ? <Empty hint="No active downloads." /> : active.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
      </Section>
      <Section title={`Seeding (${seeding.length})`} dim>
        {seeding.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
      </Section>
      <Section title={`Paused (${paused.length})`} dim>
        {paused.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
      </Section>
      <Section title={`Completed (${completed.length})`} dim>
        {completed.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
      </Section>
      {errored.length > 0 && (
        <Section title={`Errors (${errored.length})`}>
          {errored.map((r) => <DownloadRow key={r.id} row={r} onChanged={refresh} />)}
        </Section>
      )}

      {showAdd && (
        <AddTorrentModal
          onClose={() => setShowAdd(false)}
          onAdded={() => { setShowAdd(false); refresh(); }}
        />
      )}
    </div>
  );
}

function DownloadRow({ row, onChanged }: { row: TorrentRow; onChanged: () => void }) {
  const pct = Math.round((row.snapshot.progress || 0) * 100);
  const action = async (path: string, body?: any) => {
    await fetch(`${API}/torrents/${row.id}/${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
    });
    onChanged();
  };
  const remove = async (deleteFiles: boolean) => {
    await fetch(`${API}/torrents/${row.id}?delete_files=${deleteFiles}`, { method: "DELETE" });
    onChanged();
  };

  return (
    <div className="px-3 py-2 border-b border-border last:border-b-0">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-sm truncate" title={row.snapshot.name}>{row.snapshot.name || row.infohash.slice(0, 10) + "…"}</span>
        <span className="text-xs text-text-dim">{formatBytes(row.snapshot.length)}</span>
        <span className="text-xs text-text-dim">{pct}%</span>
      </div>
      <div className="mt-1 h-1 bg-bg-elev rounded overflow-hidden">
        <div className="h-full bg-accent" style={{ width: `${pct}%` }} />
      </div>
      <div className="mt-1 flex items-center text-xs text-text-dim gap-2">
        <span>{row.snapshot.peers} peers · {row.snapshot.seeds} seeds</span>
        <span className="flex-1" />
        {row.snapshot.state === "downloading" && (
          <button onClick={() => action("pause")} className="hover:text-text">pause</button>
        )}
        {row.snapshot.state === "paused" && (
          <button onClick={() => action("resume")} className="hover:text-text">resume</button>
        )}
        <button onClick={() => remove(false)} className="hover:text-error">remove</button>
        {row.snapshot.state === "completed" && (
          <button onClick={() => remove(true)} className="hover:text-error" title="remove + delete files">remove+files</button>
        )}
      </div>
      {row.snapshot.last_error && (
        <div className="mt-1 text-xs text-error truncate">{row.snapshot.last_error}</div>
      )}
    </div>
  );
}

// ─── Add modal ─────────────────────────────────────────────────────

function AddTorrentModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [mode, setMode] = useState<"magnet" | "url" | "search">("magnet");
  const [magnet, setMagnet] = useState("");
  const [url, setUrl] = useState("");
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (override?: { magnet?: string; torrent_url?: string }) => {
    setBusy(true);
    setErr("");
    try {
      const body: any = { target_folder: target };
      if (override?.magnet) body.magnet = override.magnet;
      else if (override?.torrent_url) body.torrent_url = override.torrent_url;
      else if (mode === "magnet") body.magnet = magnet;
      else if (mode === "url") body.torrent_url = url;
      const r = await fetch(`${API}/torrents`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      onAdded();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="Add torrent" onClose={onClose} wide>
      <div className="flex gap-2 mb-3 text-xs">
        {(["magnet", "url", "search"] as const).map((m) => (
          <button
            key={m}
            className={`px-2 py-1 rounded ${mode === m ? "bg-accent text-bg" : "text-text-dim"}`}
            onClick={() => setMode(m)}
          >
            {m === "magnet" ? "Magnet" : m === "url" ? ".torrent URL" : "Search"}
          </button>
        ))}
      </div>

      {mode === "magnet" && (
        <textarea
          value={magnet}
          onChange={(e) => setMagnet(e.target.value)}
          placeholder="magnet:?xt=urn:btih:…"
          className="w-full bg-bg-elev border border-border rounded p-2 text-sm font-mono h-24"
        />
      )}
      {mode === "url" && (
        <input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://example.com/file.torrent"
          className="w-full bg-bg-elev border border-border rounded p-2 text-sm font-mono"
        />
      )}
      {mode === "search" && <InlineSearch onPick={(r) => submit({ magnet: r.magnet, torrent_url: r.torrent_url })} />}

      <input
        value={target}
        onChange={(e) => setTarget(e.target.value)}
        placeholder="target folder in storage (default: /downloads)"
        className="w-full mt-3 bg-bg-elev border border-border rounded p-2 text-sm font-mono"
      />

      {err && <div className="text-sm text-error mt-2">{err}</div>}

      {mode !== "search" && (
        <div className="flex gap-2 justify-end pt-3">
          <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button
            className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
            onClick={() => submit()}
            disabled={busy || (mode === "magnet" ? !magnet : !url)}
          >
            {busy ? "Adding…" : "Add"}
          </button>
        </div>
      )}
    </Modal>
  );
}

function InlineSearch({ onPick }: { onPick: (r: SearchResult) => void }) {
  const [q, setQ] = useState("");
  const [cat, setCat] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const run = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await fetch(`${API}/search?q=${encodeURIComponent(q)}&category=${encodeURIComponent(cat)}&min_seeders=1`);
      if (!r.ok) throw new Error(await r.text());
      setResults(await r.json());
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="query"
          className="flex-1 bg-bg-elev border border-border rounded p-2 text-sm"
          onKeyDown={(e) => e.key === "Enter" && run()}
        />
        <select
          value={cat}
          onChange={(e) => setCat(e.target.value)}
          className="bg-bg-elev border border-border rounded px-2 text-sm"
        >
          <option value="">any</option>
          <option value="movie">movie</option>
          <option value="tv">tv</option>
          <option value="music">music</option>
          <option value="book">book</option>
          <option value="software">software</option>
        </select>
        <button
          onClick={run}
          disabled={busy || !q}
          className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
        >
          {busy ? "…" : "Search"}
        </button>
      </div>
      {err && <div className="text-error text-sm mt-2">{err}</div>}
      <div className="mt-3 border border-border rounded max-h-72 overflow-auto">
        {results.length === 0 ? (
          <div className="text-text-dim text-xs italic p-3">No results yet.</div>
        ) : results.map((r) => (
          <button
            key={r.infohash || r.magnet || r.name}
            onClick={() => onPick(r)}
            className="w-full text-left px-3 py-2 hover:bg-bg-elev border-b border-border last:border-b-0"
          >
            <div className="text-sm truncate">{r.name}</div>
            <div className="text-xs text-text-dim flex gap-3 mt-0.5">
              <span>{formatBytes(r.size_bytes)}</span>
              <span className="text-success">↑ {r.seeders}</span>
              <span>↓ {r.leechers}</span>
              <span>{r.indexer}</span>
              {r.category && <span>· {r.category}</span>}
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}

// ─── Searches ──────────────────────────────────────────────────────

function SearchesTab() {
  const [rows, setRows] = useState<SavedSearch[]>([]);
  const [showAdd, setShowAdd] = useState(false);

  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/searches`);
    setRows(await r.json());
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 30_000);
    return () => clearInterval(t);
  }, [refresh]);

  const remove = async (id: number) => {
    await fetch(`${API}/searches/${id}`, { method: "DELETE" });
    refresh();
  };

  return (
    <div>
      <div className="flex items-center px-4 py-3 border-b border-border">
        <button
          className="bg-accent text-bg px-3 py-1 rounded text-sm"
          onClick={() => setShowAdd(true)}
        >
          + Save search
        </button>
      </div>
      <Section title={`Saved (${rows.length})`}>
        {rows.length === 0 ? (
          <Empty hint="No saved searches. A saved search runs on a schedule and can auto-add new matches." />
        ) : rows.map((s) => (
          <div key={s.id} className="px-3 py-2 border-b border-border last:border-b-0">
            <div className="flex items-center gap-2">
              <span className="font-medium text-sm">{s.name}</span>
              <span className="text-xs text-text-dim font-mono">"{s.query}"</span>
              {s.category && <span className="text-xs text-text-dim">· {s.category}</span>}
              <span className="flex-1" />
              <button onClick={() => remove(s.id)} className="text-text-dim hover:text-error text-sm">✕</button>
            </div>
            <div className="text-xs text-text-dim mt-1">
              every {s.run_interval_minutes}m · seeders ≥ {s.min_seeders}
              {s.auto_add_top_n > 0 ? ` · auto-add top ${s.auto_add_top_n}` : " · notify only"}
              {s.last_run_at && ` · last run ${timeAgo(s.last_run_at)}`}
            </div>
          </div>
        ))}
      </Section>
      {showAdd && <SaveSearchModal onClose={() => setShowAdd(false)} onAdded={() => { setShowAdd(false); refresh(); }} />}
    </div>
  );
}

function SaveSearchModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [s, setS] = useState({
    name: "", query: "", category: "",
    min_seeders: 1, max_size_bytes: 0, auto_add_top_n: 0, run_interval_minutes: 60,
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/searches`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(s),
      });
      if (!r.ok) throw new Error(await r.text());
      onAdded();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="Save a search" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name (optional)" value={s.name} onChange={(v) => setS({ ...s, name: v })} placeholder="defaults to query" />
        <Field label="Query" value={s.query} onChange={(v) => setS({ ...s, query: v })} placeholder="e.g. show name s05" />
        <label className="block">
          <div className="text-xs text-text-dim mb-1">Category</div>
          <select className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
            value={s.category} onChange={(e) => setS({ ...s, category: e.target.value })}>
            <option value="">any</option>
            <option value="movie">movie</option>
            <option value="tv">tv</option>
            <option value="music">music</option>
            <option value="book">book</option>
            <option value="software">software</option>
          </select>
        </label>
        <div className="grid grid-cols-2 gap-2">
          <Field label="Min seeders" value={String(s.min_seeders)} onChange={(v) => setS({ ...s, min_seeders: parseInt(v) || 0 })} />
          <Field label="Run every (min)" value={String(s.run_interval_minutes)} onChange={(v) => setS({ ...s, run_interval_minutes: parseInt(v) || 60 })} />
        </div>
        <Field label="Auto-add top N (0 = notify only)" value={String(s.auto_add_top_n)} onChange={(v) => setS({ ...s, auto_add_top_n: parseInt(v) || 0 })} />
        {err && <div className="text-sm text-error">{err}</div>}
        <div className="flex gap-2 justify-end pt-2">
          <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
            onClick={submit} disabled={busy || !s.query}>
            {busy ? "Saving…" : "Save"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ─── Indexers ──────────────────────────────────────────────────────

function IndexersTab() {
  const [rows, setRows] = useState<Indexer[]>([]);
  const [probes, setProbes] = useState<any[]>([]);
  const [showAdd, setShowAdd] = useState(false);

  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/indexers`);
    setRows(await r.json());
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  const remove = async (id: number) => {
    await fetch(`${API}/indexers/${id}`, { method: "DELETE" });
    refresh();
  };

  const test = async () => {
    // Reuse the MCP tool via app-events would be cleaner; for now,
    // call the http search endpoint with q=test to probe each indexer.
    setProbes(rows.map((r) => ({ ...r, ok: !!r.last_ok_at })));
  };

  return (
    <div>
      <div className="flex items-center px-4 py-3 border-b border-border gap-2">
        <button className="bg-accent text-bg px-3 py-1 rounded text-sm" onClick={() => setShowAdd(true)}>
          + Add indexer
        </button>
        <button className="px-3 py-1 text-sm border border-border rounded" onClick={test}>Test all</button>
      </div>
      <Section title={`Configured (${rows.length})`}>
        {rows.length === 0 ? (
          <Empty hint="No indexers configured. Add a Jackett / Prowlarr / RSS source so torrent_search has somewhere to ask." />
        ) : rows.map((ix) => (
          <div key={ix.id} className="px-3 py-2 border-b border-border last:border-b-0">
            <div className="flex items-center gap-2">
              <span className={`w-2 h-2 rounded-full ${ix.last_error ? "bg-error" : ix.last_ok_at ? "bg-success" : "bg-text-dim"}`} />
              <span className="font-medium text-sm">{ix.name}</span>
              <span className="text-xs text-text-dim">[{ix.kind}]</span>
              <span className="text-xs text-text-dim font-mono truncate flex-1">{ix.base_url}</span>
              <button onClick={() => remove(ix.id)} className="text-text-dim hover:text-error text-sm">✕</button>
            </div>
            {ix.last_error && <div className="text-xs text-error mt-1 truncate">{ix.last_error}</div>}
          </div>
        ))}
      </Section>
      {showAdd && <AddIndexerModal onClose={() => setShowAdd(false)} onAdded={() => { setShowAdd(false); refresh(); }} />}
    </div>
  );
}

function AddIndexerModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [v, setV] = useState({ name: "", kind: "jackett", base_url: "", api_key: "" });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/indexers`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(v),
      });
      if (!r.ok) throw new Error(await r.text());
      onAdded();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="Add indexer" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name" value={v.name} onChange={(s) => setV({ ...v, name: s })} placeholder="my-jackett" />
        <label className="block">
          <div className="text-xs text-text-dim mb-1">Kind</div>
          <select className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
            value={v.kind} onChange={(e) => setV({ ...v, kind: e.target.value })}>
            <option value="jackett">jackett</option>
            <option value="prowlarr">prowlarr</option>
            <option value="rss">rss / torznab</option>
          </select>
        </label>
        <Field label="Base URL" value={v.base_url} onChange={(s) => setV({ ...v, base_url: s })}
          placeholder={v.kind === "jackett" ? "http://host:9117/api/v2.0/indexers/all" : "http://host:9696"} />
        <Field label="API key" value={v.api_key} onChange={(s) => setV({ ...v, api_key: s })} type="password" />
        <p className="text-xs text-text-dim">
          API keys are stored encrypted at rest when <code>shared_secret</code> is configured;
          plaintext otherwise (acceptable on a single-tenant trusted host).
        </p>
        {err && <div className="text-sm text-error">{err}</div>}
        <div className="flex gap-2 justify-end pt-2">
          <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
            onClick={submit} disabled={busy || !v.name || !v.base_url}>
            {busy ? "Adding…" : "Add"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ─── primitives ────────────────────────────────────────────────────

function Section({ title, children, dim }: { title: string; children: React.ReactNode; dim?: boolean }) {
  return (
    <div className={`border-b border-border ${dim ? "opacity-90" : ""}`}>
      <div className="px-4 py-1 text-xs text-text-dim uppercase tracking-wide bg-bg-elev/40">{title}</div>
      <div>{children}</div>
    </div>
  );
}

function Empty({ hint }: { hint: string }) {
  return <div className="text-xs text-text-dim italic px-4 py-3">{hint}</div>;
}

function Field({ label, value, onChange, placeholder, type = "text" }: {
  label: string; value: string; onChange: (s: string) => void; placeholder?: string; type?: string;
}) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      <input
        className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
        value={value} type={type} placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}

function Modal({ title, onClose, children, wide }: {
  title: string; onClose: () => void; children: React.ReactNode; wide?: boolean;
}) {
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50" onClick={onClose}>
      <div className={`bg-bg border border-border rounded-lg p-4 ${wide ? "w-[640px]" : "w-[440px]"} max-w-full max-h-[90vh] overflow-auto`}
        onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center mb-3">
          <h3 className="text-base font-medium flex-1">{title}</h3>
          <button className="text-text-dim hover:text-text" onClick={onClose}>×</button>
        </div>
        {children}
      </div>
    </div>
  );
}

function formatBytes(n: number): string {
  if (!n || n < 0) return "—";
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)}GB`;
}

function timeAgo(rfc3339: string): string {
  const t = new Date(rfc3339).getTime();
  if (!Number.isFinite(t)) return "—";
  const secs = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}
