// CodePanel — native React panel for the code app. Three-pane:
// left = repo list, middle = file tree of the selected repo,
// right = file editor. Loaded by the dashboard via dynamic import;
// uses host React via importmap; talks to the code sidecar through
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
  is_template?: boolean;
  template_scope?: "private" | "project" | "global";
  template_tagline?: string;
  template_icon?: string;
}

interface TemplateEntry {
  kind: "user" | "embedded";
  name: string;
  slug: string;
  tagline?: string;
  icon?: string;
  scope?: string;
  file_count: number;
  project_id?: string;
}

interface FileMeta {
  path: string;
  size: number;
  mod_time: number;
  is_dir?: boolean;
  sha256?: string;
}

interface FileEventData {
  slug?: string;
  path?: string;
  from?: string;
  to?: string;
}

const API = "/api/apps/code/api";

// Always-on fallback so the picker still works if the templates fetch
// fails (e.g. the sidecar is mid-restart). Embedded names beyond
// "blank" are discovered at runtime via /api/templates.
const FALLBACK_TEMPLATES: TemplateEntry[] = [
  { kind: "embedded", name: "blank", slug: "blank", tagline: "Empty repo", file_count: 0 },
];

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

// Strip leading slashes / collapse "./" — the backend normalises too
// but matching here keeps optimistic UI in sync with the eventual path.
function cleanRel(p: string): string {
  return p.replace(/^\.?\/+/, "").replace(/\/+/g, "/").replace(/\/$/, "");
}

export default function CodePanel({ projectId, installId }: NativePanelProps) {
  const [repos, setRepos] = useState<Repo[]>([]);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [query, setQuery] = useState("");
  const [selectedSlug, setSelectedSlug] = useState<string | null>(null);
  const [tree, setTree] = useState<FileMeta[]>([]);
  const [openFile, setOpenFile] = useState<{ path: string; content: string } | null>(null);
  const [draft, setDraft] = useState<string>("");
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [loadingTree, setLoadingTree] = useState(false);
  const [loadingFile, setLoadingFile] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [showNewFile, setShowNewFile] = useState(false);
  const [showNewFolder, setShowNewFolder] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameTo, setRenameTo] = useState("");
  const [forkSlug, setForkSlug] = useState<string | null>(null);
  const uploadRef = useRef<HTMLInputElement | null>(null);

  const selectedRepo = repos.find((r) => r.slug === selectedSlug);

  const dirty = editing && openFile !== null && draft !== openFile.content;

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

  // PUT /files/<path> takes raw bytes, not JSON — the api() helper
  // would coerce the body so we hand-roll the fetch.
  const putFile = useCallback(
    async (slug: string, path: string, body: BodyInit, contentType = "application/octet-stream") => {
      const url = `${API}/repos/${slug}/files/${path}?${withParams()}`;
      const res = await fetch(url, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": contentType },
        body,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json() as Promise<{ file: FileMeta }>;
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
    if (dirty && !confirm("Unsaved changes will be lost. Switch repo anyway?")) return;
    setSelectedSlug(slug);
    setOpenFile(null);
    setEditing(false);
    setDraft("");
    loadTree(slug);
  };

  const openPath = useCallback(
    async (slug: string, path: string) => {
      if (!isLikelyText(path)) {
        setOpenFile({ path, content: "(binary file — preview not supported)" });
        setDraft("");
        setEditing(false);
        return;
      }
      setLoadingFile(true);
      try {
        const url = `${API}/repos/${slug}/files/${path}?${withParams()}`;
        const res = await fetch(url, { credentials: "same-origin" });
        if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
        const text = await res.text();
        setOpenFile({ path, content: text });
        setDraft(text);
        setEditing(false);
      } catch (e) {
        setOpenFile({ path, content: "Error: " + (e as Error).message });
        setDraft("");
        setEditing(false);
      } finally {
        setLoadingFile(false);
      }
    },
    [withParams],
  );

  // Live refresh — react to repo + file mutations from agents, other
  // tabs, AND this panel's own writes (the backend emits on every
  // mutation, REST-driven or MCP-driven).
  useAppEvents<FileEventData>("code", projectId, (ev) => {
    switch (ev.topic) {
      case "repo.added":
      case "repo.archived":
      case "repo.deleted":
        loadRepos();
        break;
      case "file.changed":
        if (selectedSlug && ev.data?.slug === selectedSlug) {
          loadTree(selectedSlug);
          // Re-fetch the open file when it's the one that changed and
          // the user isn't mid-edit. If they are, leave the buffer
          // alone — clobbering would lose work.
          if (openFile && ev.data?.path === openFile.path && !dirty) {
            openPath(selectedSlug, openFile.path);
          }
        }
        break;
      case "file.deleted":
        if (selectedSlug && ev.data?.slug === selectedSlug) {
          loadTree(selectedSlug);
          if (openFile && ev.data?.path === openFile.path) {
            setOpenFile(null);
            setDraft("");
            setEditing(false);
          }
        }
        break;
      case "file.renamed":
        if (selectedSlug && ev.data?.slug === selectedSlug) {
          loadTree(selectedSlug);
          if (openFile && ev.data?.from && openFile.path === ev.data.from && ev.data.to) {
            // Follow the rename so the editor stays on the same content.
            openPath(selectedSlug, ev.data.to);
          }
        }
        break;
    }
  });

  const selectFile = (path: string) => {
    if (!selectedSlug) return;
    if (dirty && !confirm("Unsaved changes will be lost. Open another file anyway?")) return;
    openPath(selectedSlug, path);
  };

  const handleArchive = async (slug: string) => {
    if (!confirm(`Archive repo "${slug}"? Files stay on disk.`)) return;
    try {
      await api("DELETE", `/repos/${slug}`);
      if (selectedSlug === slug) {
        setSelectedSlug(null);
        setTree([]);
        setOpenFile(null);
      }
    } catch (e) {
      alert("Archive failed: " + (e as Error).message);
    }
  };

  const handleToggleTemplate = async (slug: string) => {
    const repo = repos.find((r) => r.slug === slug);
    if (!repo) return;
    if (repo.is_template) {
      try {
        await api("POST", `/repos/${slug}/unmark-template`);
        await loadRepos();
      } catch (e) {
        alert("Unmark failed: " + (e as Error).message);
      }
      return;
    }
    const scope = prompt(
      "Template scope: 'private' (this project only), 'project' (anyone in this project), or 'global' (every project)",
      "private",
    );
    if (!scope) return;
    if (!["private", "project", "global"].includes(scope)) {
      alert("scope must be one of: private, project, global");
      return;
    }
    const tagline = prompt("Short tagline for the picker (optional)", "") ?? "";
    try {
      await api("POST", `/repos/${slug}/mark-template`, { scope, tagline });
      await loadRepos();
    } catch (e) {
      alert("Mark failed: " + (e as Error).message);
    }
  };

  const handleSave = async () => {
    if (!selectedSlug || !openFile) return;
    setSaving(true);
    try {
      await putFile(selectedSlug, openFile.path, draft, "text/plain");
      setOpenFile({ path: openFile.path, content: draft });
      setEditing(false);
    } catch (e) {
      alert("Save failed: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = () => {
    if (!openFile) return;
    if (dirty && !confirm("Discard unsaved changes?")) return;
    setDraft(openFile.content);
    setEditing(false);
  };

  const handleCreateFile = async (rawPath: string, content: string) => {
    if (!selectedSlug) return;
    const path = cleanRel(rawPath);
    if (!path) throw new Error("path required");
    await putFile(selectedSlug, path, content, "text/plain");
    setShowNewFile(false);
    // The event will refresh the tree; open the new file immediately
    // for a snappier feel.
    openPath(selectedSlug, path);
  };

  const handleCreateFolder = async (rawPath: string) => {
    if (!selectedSlug) return;
    let path = cleanRel(rawPath);
    if (!path) throw new Error("folder required");
    // S3-style: a folder exists when a file is in it. Drop a hidden
    // placeholder so the tree shows the path immediately.
    path = path + "/.gitkeep";
    await putFile(selectedSlug, path, "", "text/plain");
    setShowNewFolder(false);
  };

  const handleRename = async (from: string, to: string) => {
    if (!selectedSlug) return;
    const cleanTo = cleanRel(to);
    if (!cleanTo || cleanTo === from) {
      setRenaming(null);
      return;
    }
    try {
      await api("POST", `/repos/${selectedSlug}/move`, { from, to: cleanTo });
      setRenaming(null);
      setRenameTo("");
      // Optimistic: if the renamed file is open, follow it. The event
      // handler does this too, but doing it here avoids a flash.
      if (openFile?.path === from) {
        openPath(selectedSlug, cleanTo);
      }
    } catch (e) {
      alert("Rename failed: " + (e as Error).message);
    }
  };

  const handleDeleteFile = async (path: string) => {
    if (!selectedSlug) return;
    if (!confirm(`Delete "${path}"? This can't be undone.`)) return;
    try {
      await fetch(`${API}/repos/${selectedSlug}/files/${path}?${withParams()}`, {
        method: "DELETE",
        credentials: "same-origin",
      }).then(async (res) => {
        if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      });
    } catch (e) {
      alert("Delete failed: " + (e as Error).message);
    }
  };

  const handleUpload = async (ev: React.ChangeEvent<HTMLInputElement>) => {
    if (!selectedSlug) return;
    const files = Array.from(ev.target.files || []);
    if (files.length === 0) return;
    try {
      for (const f of files) {
        const buf = await f.arrayBuffer();
        await putFile(selectedSlug, cleanRel(f.name), buf);
      }
    } catch (e) {
      alert("Upload failed: " + (e as Error).message);
    } finally {
      ev.target.value = "";
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
                    {r.is_template && (
                      <span
                        className="text-[10px] px-1 py-0.5 rounded bg-yellow/15 text-yellow"
                        title={`template — ${r.template_scope ?? "private"}`}
                      >★ template</span>
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
            <div className="p-3 border-b border-border flex items-center gap-1">
              <span className="text-xs uppercase tracking-wide text-text-dim flex-1 truncate">
                {selectedSlug}
              </span>
              <button
                type="button"
                onClick={() => setShowNewFile(true)}
                className="px-1 py-0.5 text-xs text-accent hover:text-accent/80"
                title="New file"
              >+ File</button>
              <button
                type="button"
                onClick={() => setShowNewFolder(true)}
                className="px-1 py-0.5 text-xs text-accent hover:text-accent/80"
                title="New folder"
              >+ Dir</button>
              <button
                type="button"
                onClick={() => uploadRef.current?.click()}
                className="px-1 py-0.5 text-xs text-accent hover:text-accent/80"
                title="Upload files"
              >↑</button>
              <input
                ref={uploadRef}
                type="file"
                multiple
                onChange={handleUpload}
                className="hidden"
              />
              <button
                type="button"
                onClick={() => loadTree(selectedSlug)}
                className="px-1 py-0.5 text-xs text-text-dim hover:text-text"
                title="Refresh"
              >↻</button>
              <button
                type="button"
                onClick={() => handleToggleTemplate(selectedSlug)}
                className="px-1 py-0.5 text-xs text-yellow/80 hover:text-yellow"
                title={selectedRepo?.is_template ? "Unmark as template" : "Save as template"}
              >★</button>
              <button
                type="button"
                onClick={() => setForkSlug(selectedSlug)}
                className="px-1 py-0.5 text-xs text-accent/80 hover:text-accent"
                title="Fork into a new repo"
              >⑂</button>
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
                  {tree.map((f) => {
                    const isOpen = openFile?.path === f.path;
                    const isRenaming = renaming === f.path;
                    return (
                      <li
                        key={f.path}
                        className={`group px-3 py-1 text-xs border-b border-border/40 ${
                          isOpen ? "bg-bg-input" : "hover:bg-bg-input/50"
                        }`}
                      >
                        {isRenaming ? (
                          <form
                            onSubmit={(e) => {
                              e.preventDefault();
                              handleRename(f.path, renameTo);
                            }}
                            className="flex items-center gap-1"
                          >
                            <input
                              autoFocus
                              type="text"
                              value={renameTo}
                              onChange={(e) => setRenameTo(e.target.value)}
                              onBlur={() => { setRenaming(null); setRenameTo(""); }}
                              onKeyDown={(e) => { if (e.key === "Escape") { setRenaming(null); setRenameTo(""); } }}
                              className="flex-1 bg-bg-input border border-border rounded px-1 py-0.5 text-xs"
                            />
                          </form>
                        ) : (
                          <div className="flex items-center gap-2">
                            <button
                              type="button"
                              onClick={() => selectFile(f.path)}
                              className="text-text truncate flex-1 text-left"
                              title={f.path}
                            >{f.path}</button>
                            <span className="text-text-dim text-[10px] group-hover:hidden">
                              {formatSize(f.size)}
                            </span>
                            <span className="hidden group-hover:flex items-center gap-1">
                              <button
                                type="button"
                                onClick={(e) => { e.stopPropagation(); setRenaming(f.path); setRenameTo(f.path); }}
                                className="text-text-dim hover:text-text px-1"
                                title="Rename"
                              >✎</button>
                              <button
                                type="button"
                                onClick={(e) => { e.stopPropagation(); handleDeleteFile(f.path); }}
                                className="text-red/70 hover:text-red px-1"
                                title="Delete"
                              >🗑</button>
                            </span>
                          </div>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
            <div className="p-2 text-xs text-text-dim border-t border-border">
              {tree.length} file{tree.length !== 1 ? "s" : ""} · {formatSize(totalSize)}
            </div>
          </>
        )}
      </aside>

      {/* File content / editor */}
      <main className="flex-1 overflow-hidden flex flex-col">
        {!openFile ? (
          <div className="p-8 text-text-muted text-sm text-center mt-12">
            {selectedSlug
              ? "Click a file in the tree to view it. + File to create one."
              : "Pick a repo, then a file."}
          </div>
        ) : (
          <>
            <header className="p-3 border-b border-border flex items-center gap-2">
              <span className="text-xs uppercase tracking-wide text-text-dim">file</span>
              <span className="text-text font-mono text-sm truncate flex-1">
                {openFile.path}{dirty ? " •" : ""}
              </span>
              {isLikelyText(openFile.path) && (
                editing ? (
                  <>
                    <button
                      type="button"
                      onClick={handleSave}
                      disabled={saving || !dirty}
                      className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
                    >{saving ? "Saving…" : "Save"}</button>
                    <button
                      type="button"
                      onClick={handleDiscard}
                      disabled={saving}
                      className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input"
                    >Cancel</button>
                  </>
                ) : (
                  <button
                    type="button"
                    onClick={() => { setDraft(openFile.content); setEditing(true); }}
                    className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input"
                  >Edit</button>
                )
              )}
            </header>
            <div className="flex-1 overflow-auto">
              {loadingFile ? (
                <div className="p-4 text-text-muted text-sm">Loading…</div>
              ) : editing ? (
                <textarea
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  spellCheck={false}
                  className="w-full h-full bg-bg text-text font-mono text-[11px] p-4 border-0 outline-none resize-none whitespace-pre"
                  onKeyDown={(e) => {
                    if ((e.metaKey || e.ctrlKey) && e.key === "s") {
                      e.preventDefault();
                      handleSave();
                    }
                  }}
                />
              ) : (
                <pre className="text-[11px] font-mono p-4 text-text whitespace-pre overflow-auto">
                  {openFile.content}
                </pre>
              )}
            </div>
          </>
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

      {showNewFile && selectedSlug && (
        <PromptDialog
          title="New file"
          label="Path"
          placeholder="src/index.ts"
          submitLabel="Create"
          onClose={() => setShowNewFile(false)}
          onSubmit={(path) => handleCreateFile(path, "")}
        />
      )}

      {showNewFolder && selectedSlug && (
        <PromptDialog
          title="New folder"
          label="Folder path"
          placeholder="src/components"
          submitLabel="Create"
          onClose={() => setShowNewFolder(false)}
          onSubmit={handleCreateFolder}
          hint="A hidden .gitkeep is added so the folder shows up in the tree."
        />
      )}

      {forkSlug && (
        <ForkRepoDialog
          parentSlug={forkSlug}
          onClose={() => setForkSlug(null)}
          onCreated={(slug) => {
            setForkSlug(null);
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
  const [description, setDescription] = useState("");
  const [templates, setTemplates] = useState<TemplateEntry[]>(FALLBACK_TEMPLATES);
  // Selection is encoded as "<kind>:<slug>" so we can round-trip both
  // kinds through one piece of state without tracking them separately.
  const [picked, setPicked] = useState<string>("embedded:blank");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    api<{ templates: TemplateEntry[] }>("GET", "/templates")
      .then((r) => {
        if (r.templates && r.templates.length) setTemplates(r.templates);
      })
      .catch(() => { /* keep fallback */ });
  }, [api]);

  const submit = async () => {
    if (!name.trim()) {
      setErr("name required");
      return;
    }
    const [kind, slug] = picked.split(":", 2);
    setBusy(true);
    try {
      let created: { repository: Repo };
      if (kind === "user") {
        created = await api<{ repository: Repo }>("POST", `/repos/${slug}/fork`, {
          name: name.trim(),
          description: description.trim(),
        });
      } else {
        // embedded — keeps the framework column populated for the badge
        created = await api<{ repository: Repo }>("POST", "/repos", {
          name: name.trim(),
          framework: slug,
          description: description.trim(),
        });
      }
      onCreated(created.repository.slug);
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
        className="w-[560px] max-h-[80vh] bg-bg border border-border rounded p-5 space-y-4 flex flex-col"
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
            <label className="text-xs text-text-muted block mb-1">Description (optional)</label>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Template</label>
            <div className="grid grid-cols-2 gap-2 overflow-auto max-h-64 p-1">
              {templates.map((t) => {
                const id = `${t.kind}:${t.slug}`;
                const sel = picked === id;
                return (
                  <button
                    key={id}
                    type="button"
                    onClick={() => setPicked(id)}
                    className={`text-left p-2 rounded border ${
                      sel ? "border-accent bg-accent/10" : "border-border hover:bg-bg-input/50"
                    }`}
                  >
                    <div className="flex items-center gap-1">
                      <span className="text-sm text-text font-medium truncate flex-1">
                        {t.icon ? `${t.icon} ` : ""}{t.name}
                      </span>
                      <span
                        className={`text-[10px] px-1 py-0.5 rounded ${
                          t.kind === "embedded" ? "bg-blue/15 text-blue" : "bg-yellow/15 text-yellow"
                        }`}
                      >{t.kind === "embedded" ? "system" : (t.scope ?? "yours")}</span>
                    </div>
                    {t.tagline && (
                      <div className="text-[11px] text-text-muted truncate mt-0.5">{t.tagline}</div>
                    )}
                    <div className="text-[10px] text-text-dim mt-0.5">{t.file_count} files</div>
                  </button>
                );
              })}
            </div>
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

function ForkRepoDialog({
  parentSlug,
  onClose,
  onCreated,
  api,
}: {
  parentSlug: string;
  onClose: () => void;
  onCreated: (slug: string) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
}) {
  const [name, setName] = useState(`${parentSlug}-fork`);
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
      const r = await api<{ repository: Repo }>("POST", `/repos/${parentSlug}/fork`, {
        name: name.trim(),
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
        <h2 className="text-text font-semibold">Fork {parentSlug}</h2>
        <p className="text-xs text-text-muted">
          Snapshots every file from <span className="font-mono">{parentSlug}</span> into a fresh repo.
          Edits to the new repo don't affect the source.
        </p>
        <div className="space-y-3">
          <div>
            <label className="text-xs text-text-muted block mb-1">New name</label>
            <input
              autoFocus
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
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
          >{busy ? "Forking…" : "Fork"}</button>
        </div>
      </div>
    </div>
  );
}

// Generic single-input dialog used for "new file" and "new folder".
// onSubmit may throw — the dialog surfaces the error and stays open.
function PromptDialog({
  title,
  label,
  placeholder,
  submitLabel,
  hint,
  onClose,
  onSubmit,
}: {
  title: string;
  label: string;
  placeholder?: string;
  submitLabel: string;
  hint?: string;
  onClose: () => void;
  onSubmit: (value: string) => void | Promise<void>;
}) {
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    if (!value.trim()) {
      setErr("required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await onSubmit(value.trim());
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">{title}</h2>
        <div>
          <label className="text-xs text-text-muted block mb-1">{label}</label>
          <input
            autoFocus
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={placeholder}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
          {hint && <div className="text-[11px] text-text-dim mt-1">{hint}</div>}
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "…" : submitLabel}</button>
        </div>
      </form>
    </div>
  );
}
