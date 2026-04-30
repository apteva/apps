// CodePanel — native React panel for the code app. Three-pane:
// left = repo list, middle = file tree of the selected repo,
// right = file content viewer (read-only for v0.1; the agent does
// edits via MCP). Loaded by the dashboard via dynamic import; uses
// host React via importmap; talks to the code sidecar through
// /api/apps/code/api/* with same-origin cookies.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps install independently.
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

interface Repo {
  id: number;
  slug: string;
  name: string;
  description?: string;
  framework?: string;
  build_cmd?: string;
  start_cmd?: string;
  port?: number;
  archived?: boolean;
  created_at?: string;
  updated_at?: string;
}

interface FileMeta {
  path: string;
  size: number;
  mod_time: number;
  is_dir?: boolean;
  sha256?: string;
}

const API = "/api/apps/code/api";

const FRAMEWORKS = ["blank", "nextjs", "static", "go", "python"] as const;
type Framework = (typeof FRAMEWORKS)[number];

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Treat anything that's clearly binary (images, archives, pdfs) as
// "open externally" instead of dumping bytes into the viewer.
function isLikelyText(path: string): boolean {
  const ext = path.toLowerCase().split(".").pop() || "";
  switch (ext) {
    case "png": case "jpg": case "jpeg": case "gif": case "webp":
    case "ico": case "icns": case "pdf": case "zip": case "gz":
    case "tar": case "mp4": case "mov": case "mp3": case "wav":
    case "woff": case "woff2": case "ttf": case "otf":
    case "exe": case "bin": case "so": case "dylib":
      return false;
  }
  return true;
}

export default function CodePanel({ projectId, installId }: NativePanelProps) {
  const [repos, setRepos] = useState<Repo[]>([]);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [query, setQuery] = useState("");
  const [selectedSlug, setSelectedSlug] = useState<string | null>(null);
  const [tree, setTree] = useState<FileMeta[]>([]);
  const [openFile, setOpenFile] = useState<{ path: string; content: string } | null>(null);
  const [error, setError] = useState("");
  const [loadingTree, setLoadingTree] = useState(false);
  const [loadingFile, setLoadingFile] = useState(false);
  const [showCreate, setShowCreate] = useState(false);

  const withParams = useCallback(
    (extra: Record<string, string> = {}) => {
      const u = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      });
      return u.toString();
    },
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<T> => {
      const res = await fetch(`${API}${path}?${withParams(extra)}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [withParams],
  );

  const loadRepos = useCallback(async () => {
    try {
      const extra: Record<string, string> = {};
      if (includeArchived) extra.archived = "1";
      if (query.trim()) extra.q = query.trim();
      const r = await api<{ repositories?: Repo[] }>("GET", "/repos", undefined, extra);
      setRepos(r.repositories || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, includeArchived, query]);

  useEffect(() => { loadRepos(); }, [loadRepos]);

  const loadTree = useCallback(
    async (slug: string) => {
      setLoadingTree(true);
      try {
        const r = await api<{ files?: FileMeta[] }>("GET", `/repos/${slug}/tree`);
        setTree((r.files || []).filter((f) => !f.is_dir));
      } catch (e) {
        setError((e as Error).message);
        setTree([]);
      } finally {
        setLoadingTree(false);
      }
    },
    [api],
  );

  const selectRepo = (slug: string) => {
    setSelectedSlug(slug);
    setOpenFile(null);
    loadTree(slug);
  };

  // Live refresh — react to repo + file mutations from agents or
  // other tabs. Repo-level events refresh the left list; file-level
  // events refresh the tree of the selected repo when it matches.
  useAppEvents<{ slug?: string }>("code", projectId, (ev) => {
    switch (ev.topic) {
      case "repo.added":
      case "repo.archived":
      case "repo.deleted":
        loadRepos();
        break;
      case "file.changed":
      case "file.deleted":
      case "file.renamed":
        if (selectedSlug && ev.data?.slug === selectedSlug) {
          loadTree(selectedSlug);
        }
        break;
    }
  });

  const selectFile = async (path: string) => {
    if (!selectedSlug) return;
    if (!isLikelyText(path)) {
      setOpenFile({ path, content: "(binary file — preview not supported)" });
      return;
    }
    setLoadingFile(true);
    try {
      // The /files/<path> route returns raw bytes, not JSON, so we
      // bypass the api() helper and do a plain fetch. Same auth flows
      // through cookies.
      const url = `${API}/repos/${selectedSlug}/files/${path}?${withParams()}`;
      const res = await fetch(url, { credentials: "same-origin" });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      const text = await res.text();
      setOpenFile({ path, content: text });
    } catch (e) {
      setOpenFile({ path, content: "Error: " + (e as Error).message });
    } finally {
      setLoadingFile(false);
    }
  };

  const handleArchive = async (slug: string) => {
    if (!confirm(`Archive repo "${slug}"? Files stay on disk.`)) return;
    try {
      await api("POST", `/repos/${slug}/archive`);
      if (selectedSlug === slug) {
        setSelectedSlug(null);
        setTree([]);
        setOpenFile(null);
      }
      await loadRepos();
    } catch (e) {
      alert("Archive failed: " + (e as Error).message);
    }
  };

  const totalSize = useMemo(
    () => tree.reduce((acc, f) => acc + (f.size || 0), 0),
    [tree],
  );

  return (
    <div className="h-full flex">
      {/* Repo list */}
      <aside className="w-64 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border space-y-2">
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search repos…"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <div className="flex items-center justify-between">
            <label className="text-[11px] text-text-muted flex items-center gap-1">
              <input
                type="checkbox"
                checked={includeArchived}
                onChange={(e) => setIncludeArchived(e.target.checked)}
              />
              archived
            </label>
            <button
              type="button"
              onClick={() => setShowCreate(true)}
              className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
            >+ New</button>
          </div>
        </div>
        <div className="flex-1 overflow-auto">
          {error ? (
            <div className="p-3 text-red text-xs">{error}</div>
          ) : repos.length === 0 ? (
            <div className="p-3 text-text-muted text-sm">No repos.</div>
          ) : (
            <ul>
              {repos.map((r) => (
                <li
                  key={r.slug}
                  onClick={() => selectRepo(r.slug)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    r.slug === selectedSlug ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center gap-1">
                    <span className="text-sm text-text font-medium truncate flex-1">{r.name}</span>
                    {r.framework && (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-blue/15 text-blue">
                        {r.framework}
                      </span>
                    )}
                    {r.archived && (
                      <span className="text-[10px] px-1 py-0.5 rounded bg-border text-text-muted">
                        archived
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-text-muted truncate">{r.slug}</div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      {/* File tree */}
      <aside className="w-72 border-r border-border flex flex-col">
        {!selectedSlug ? (
          <div className="p-4 text-text-muted text-sm">Select a repo on the left.</div>
        ) : (
          <>
            <div className="p-3 border-b border-border flex items-center gap-2">
              <span className="text-xs uppercase tracking-wide text-text-dim flex-1 truncate">
                {selectedSlug}
              </span>
              <button
                type="button"
                onClick={() => loadTree(selectedSlug)}
                className="px-1 py-0.5 text-xs text-text-dim hover:text-text"
                title="Refresh"
              >↻</button>
              <button
                type="button"
                onClick={() => handleArchive(selectedSlug)}
                className="px-1 py-0.5 text-xs text-red/70 hover:text-red"
                title="Archive"
              >✕</button>
            </div>
            <div className="flex-1 overflow-auto">
              {loadingTree ? (
                <div className="p-3 text-text-muted text-sm">Loading tree…</div>
              ) : tree.length === 0 ? (
                <div className="p-3 text-text-muted text-sm">Empty repo.</div>
              ) : (
                <ul>
                  {tree.map((f) => (
                    <li
                      key={f.path}
                      onClick={() => selectFile(f.path)}
                      className={`px-3 py-1 cursor-pointer text-xs hover:bg-bg-input/50 ${
                        openFile?.path === f.path ? "bg-bg-input" : ""
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <span className="text-text truncate flex-1" title={f.path}>{f.path}</span>
                        <span className="text-text-dim text-[10px]">{formatSize(f.size)}</span>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div className="p-2 text-xs text-text-dim border-t border-border">
              {tree.length} file{tree.length !== 1 ? "s" : ""} · {formatSize(totalSize)}
            </div>
          </>
        )}
      </aside>

      {/* File content */}
      <main className="flex-1 overflow-auto">
        {!openFile ? (
          <div className="p-8 text-text-muted text-sm text-center mt-12">
            {selectedSlug
              ? "Click a file in the tree to view it."
              : "Pick a repo, then a file."}
          </div>
        ) : (
          <div className="h-full flex flex-col">
            <header className="p-3 border-b border-border flex items-center gap-2">
              <span className="text-xs uppercase tracking-wide text-text-dim">file</span>
              <span className="text-text font-mono text-sm truncate flex-1">{openFile.path}</span>
            </header>
            <div className="flex-1 overflow-auto">
              {loadingFile ? (
                <div className="p-4 text-text-muted text-sm">Loading…</div>
              ) : (
                <pre className="text-[11px] font-mono p-4 text-text whitespace-pre overflow-auto">
                  {openFile.content}
                </pre>
              )}
            </div>
          </div>
        )}
      </main>

      {showCreate && (
        <CreateRepoDialog
          onClose={() => setShowCreate(false)}
          onCreated={(slug) => {
            setShowCreate(false);
            loadRepos().then(() => selectRepo(slug));
          }}
          api={api}
        />
      )}
    </div>
  );
}

function CreateRepoDialog({
  onClose,
  onCreated,
  api,
}: {
  onClose: () => void;
  onCreated: (slug: string) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
}) {
  const [name, setName] = useState("");
  const [framework, setFramework] = useState<Framework>("blank");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    if (!name.trim()) {
      setErr("name required");
      return;
    }
    setBusy(true);
    try {
      const r = await api<{ repository: Repo }>("POST", "/repos", {
        name: name.trim(),
        framework,
        description: description.trim(),
      });
      onCreated(r.repository.slug);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">New repository</h2>
        <div className="space-y-3">
          <div>
            <label className="text-xs text-text-muted block mb-1">Name</label>
            <input
              autoFocus
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              placeholder="my-app"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Framework</label>
            <select
              value={framework}
              onChange={(e) => setFramework(e.target.value as Framework)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {FRAMEWORKS.map((f) => (
                <option key={f} value={f}>{f}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Description (optional)</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Creating…" : "Create"}</button>
        </div>
      </div>
    </div>
  );
}
