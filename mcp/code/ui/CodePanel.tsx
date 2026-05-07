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

// ─── Tree builder + renderer ──────────────────────────────────────
//
// The /api/repos/<slug>/tree endpoint returns a flat list of file
// paths. The panel renders them as a classic IDE tree: folders
// before files at each level, expand/collapse per directory, file
// icons by extension. With 195 files in marcoschwartz-new, the flat
// list version was a 195-row scroll wall — this gives the user the
// shape of the project at a glance.

interface TreeNode {
  name: string;
  path: string;        // repo-relative; "" for synthetic root
  isDir: boolean;
  size: number;
  children: TreeNode[];
}

function buildTree(files: FileMeta[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", isDir: true, size: 0, children: [] };
  for (const f of files) {
    const parts = f.path.split("/").filter(Boolean);
    let node = root;
    for (let i = 0; i < parts.length; i++) {
      const last = i === parts.length - 1;
      const partPath = parts.slice(0, i + 1).join("/");
      let child = node.children.find((c) => c.name === parts[i]);
      if (!child) {
        child = {
          name: parts[i],
          path: partPath,
          isDir: !last,
          size: last ? f.size : 0,
          children: [],
        };
        node.children.push(child);
      }
      node = child;
    }
  }
  sortTree(root);
  return root.children;
}

function sortTree(node: TreeNode) {
  // Folders first, then files; alpha within each. Classic IDE order.
  node.children.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  for (const c of node.children) sortTree(c);
}

// flattenTree walks the tree DFS and returns only rows that should
// be visible given the current expansion state. Each row carries its
// nesting depth so the renderer can indent.
interface FlatRow {
  node: TreeNode;
  depth: number;
}
function flattenTree(nodes: TreeNode[], expanded: Set<string>, depth = 0): FlatRow[] {
  const out: FlatRow[] = [];
  for (const n of nodes) {
    out.push({ node: n, depth });
    if (n.isDir && expanded.has(n.path)) {
      out.push(...flattenTree(n.children, expanded, depth + 1));
    }
  }
  return out;
}

// Initial expansion: expand top-level dirs (gives the user the
// big-picture shape) plus any ancestor of the currently-open file
// (so you can always see where you are).
function initialExpansion(tree: TreeNode[], openPath: string | null): Set<string> {
  const out = new Set<string>();
  for (const n of tree) {
    if (n.isDir) out.add(n.path);
  }
  if (openPath) {
    const parts = openPath.split("/").filter(Boolean);
    for (let i = 1; i < parts.length; i++) {
      out.add(parts.slice(0, i).join("/"));
    }
  }
  return out;
}

// File-type glyphs. Single-letter monospace tags coloured by category
// — cheap, dependency-free, and readable at the small font sizes the
// tree uses. Real file-icons SVGs would be nicer but pull in either
// a library or a couple of KB of inline paths per icon; this is the
// pragmatic middle ground.
function fileGlyph(name: string): { letter: string; cls: string } {
  const ext = name.toLowerCase().split(".").pop() || "";
  switch (ext) {
    case "ts":
    case "tsx":
      return { letter: "TS", cls: "text-blue" };
    case "js":
    case "mjs":
    case "cjs":
    case "jsx":
      return { letter: "JS", cls: "text-yellow" };
    case "json":
      return { letter: "{}", cls: "text-yellow/80" };
    case "md":
    case "mdx":
      return { letter: "M↓", cls: "text-text-muted" };
    case "css":
    case "scss":
    case "sass":
      return { letter: "#", cls: "text-blue/80" };
    case "html":
    case "htm":
      return { letter: "<>", cls: "text-orange" };
    case "yaml":
    case "yml":
    case "toml":
      return { letter: "≡", cls: "text-text-muted" };
    case "svg":
    case "png":
    case "jpg":
    case "jpeg":
    case "gif":
    case "webp":
    case "ico":
      return { letter: "🖼", cls: "text-green/80" };
    case "go":
      return { letter: "Go", cls: "text-blue/80" };
    case "py":
      return { letter: "Py", cls: "text-blue/80" };
    case "sh":
    case "bash":
    case "zsh":
      return { letter: "$_", cls: "text-text-muted" };
    case "lock":
      return { letter: "🔒", cls: "text-text-dim" };
    case "env":
      return { letter: "ENV", cls: "text-yellow/70" };
    case "dockerfile":
      return { letter: "🐳", cls: "text-blue/80" };
  }
  // Special-case some no-extension files (Dockerfile, README, etc.)
  if (name === "Dockerfile") return { letter: "🐳", cls: "text-blue/80" };
  if (name.toLowerCase().startsWith("readme")) return { letter: "M↓", cls: "text-text-muted" };
  if (name.startsWith(".")) return { letter: "·", cls: "text-text-dim" };
  return { letter: "•", cls: "text-text-dim" };
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
  const [showImportGithub, setShowImportGithub] = useState(false);
  const [showNewFile, setShowNewFile] = useState(false);
  const [showNewFolder, setShowNewFolder] = useState(false);
  const [showDevLogs, setShowDevLogs] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameTo, setRenameTo] = useState("");
  const [forkSlug, setForkSlug] = useState<string | null>(null);
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());
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
        const files = (r.files || []).filter((f) => !f.is_dir);
        setTree(files);
        // First load: expand top-level dirs so the user sees the
        // shape immediately. Preserve any user-driven collapses on
        // subsequent loads (event-driven refreshes after edits).
        setExpandedDirs((prev) => {
          if (prev.size > 0) return prev;
          return initialExpansion(buildTree(files), null);
        });
      } catch (e) {
        setError((e as Error).message);
        setTree([]);
      } finally {
        setLoadingTree(false);
      }
    },
    [api],
  );

  const [confirmState, setConfirmState] = useState<ConfirmRequest | null>(null);
  const [markTemplateFor, setMarkTemplateFor] = useState<string | null>(null);

  const doSelectRepo = (slug: string) => {
    setSelectedSlug(slug);
    setOpenFile(null);
    setEditing(false);
    setDraft("");
    setExpandedDirs(new Set()); // reset; loadTree seeds top-level dirs.
    loadTree(slug);
  };
  const selectRepo = (slug: string) => {
    if (dirty) {
      setConfirmState({
        title: "Discard unsaved changes?",
        body: "Unsaved changes in the current file will be lost. Switch repo anyway?",
        confirmLabel: "Switch repo",
        tone: "warning",
        onConfirm: () => doSelectRepo(slug),
      });
      return;
    }
    doSelectRepo(slug);
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
    if (dirty) {
      setConfirmState({
        title: "Discard unsaved changes?",
        body: "Unsaved changes in the current file will be lost. Open another file anyway?",
        confirmLabel: "Open file",
        tone: "warning",
        onConfirm: () => openPath(selectedSlug, path),
      });
      return;
    }
    openPath(selectedSlug, path);
  };

  const handleArchive = (slug: string) => {
    setConfirmState({
      title: `Archive "${slug}"`,
      body: "Archive this repository? Files stay on disk and the row is hidden — you can hard-delete later from the dashboard.",
      confirmLabel: "Archive",
      tone: "warning",
      onConfirm: async () => {
        try {
          await api("DELETE", `/repos/${slug}`);
          if (selectedSlug === slug) {
            setSelectedSlug(null);
            setTree([]);
            setOpenFile(null);
          }
        } catch (e) {
          setError("Archive failed: " + (e as Error).message);
        }
      },
    });
  };

  const handleToggleTemplate = async (slug: string) => {
    const repo = repos.find((r) => r.slug === slug);
    if (!repo) return;
    if (repo.is_template) {
      try {
        await api("POST", `/repos/${slug}/unmark-template`);
        await loadRepos();
      } catch (e) {
        setError("Unmark failed: " + (e as Error).message);
      }
      return;
    }
    setMarkTemplateFor(slug);
  };

  const submitMarkTemplate = async (scope: string, tagline: string) => {
    if (!markTemplateFor) return;
    try {
      await api("POST", `/repos/${markTemplateFor}/mark-template`, { scope, tagline });
      await loadRepos();
      setMarkTemplateFor(null);
    } catch (e) {
      setError("Mark failed: " + (e as Error).message);
      throw e;
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
      setError("Save failed: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = () => {
    if (!openFile) return;
    if (dirty) {
      setConfirmState({
        title: "Discard unsaved changes?",
        body: "Your edits to this file will be reverted to the last saved version.",
        confirmLabel: "Discard",
        tone: "warning",
        onConfirm: () => {
          setDraft(openFile.content);
          setEditing(false);
        },
      });
      return;
    }
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
      setError("Rename failed: " + (e as Error).message);
    }
  };

  const handleDeleteFile = (path: string) => {
    if (!selectedSlug) return;
    setConfirmState({
      title: `Delete "${path}"`,
      body: "This file will be removed from the repository. This can't be undone.",
      confirmLabel: "Delete",
      tone: "danger",
      onConfirm: async () => {
        try {
          const res = await fetch(`${API}/repos/${selectedSlug}/files/${path}?${withParams()}`, {
            method: "DELETE",
            credentials: "same-origin",
          });
          if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
        } catch (e) {
          setError("Delete failed: " + (e as Error).message);
        }
      },
    });
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
      setError("Upload failed: " + (e as Error).message);
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
              onClick={() => setShowImportGithub(true)}
              title="Import a repository from GitHub"
              className="px-2 py-0.5 text-xs border border-border text-text-muted rounded hover:text-text hover:border-text"
            >GitHub</button>
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
                <FileTree
                  tree={buildTree(tree)}
                  expanded={expandedDirs}
                  onToggle={(p) => setExpandedDirs((prev) => {
                    const next = new Set(prev);
                    if (next.has(p)) next.delete(p);
                    else next.add(p);
                    return next;
                  })}
                  openPath={openFile?.path}
                  renaming={renaming}
                  renameTo={renameTo}
                  setRenameTo={setRenameTo}
                  onRenameSubmit={(from) => handleRename(from, renameTo)}
                  onRenameCancel={() => { setRenaming(null); setRenameTo(""); }}
                  onSelect={(p) => selectFile(p)}
                  onStartRename={(p) => { setRenaming(p); setRenameTo(p); }}
                  onDelete={(p) => handleDeleteFile(p)}
                />
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
        {selectedSlug && (
          <DevBar
            slug={selectedSlug}
            api={api}
            withParams={withParams}
            showLogs={showDevLogs}
            onToggleLogs={() => setShowDevLogs((v) => !v)}
            onError={(msg) => setError(msg)}
          />
        )}
        {selectedSlug && showDevLogs ? (
          <DevLogsView slug={selectedSlug} withParams={withParams} />
        ) : !openFile ? (
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

      {showImportGithub && (
        <ImportGithubDialog
          onClose={() => setShowImportGithub(false)}
          onImported={(slug) => {
            setShowImportGithub(false);
            loadRepos().then(() => selectRepo(slug));
          }}
          api={api}
          withParams={withParams}
        />
      )}

      {confirmState && (
        <ConfirmDialog
          request={confirmState}
          onClose={() => setConfirmState(null)}
        />
      )}

      {markTemplateFor && (
        <MarkTemplateDialog
          slug={markTemplateFor}
          onClose={() => setMarkTemplateFor(null)}
          onSubmit={submitMarkTemplate}
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

// ─── ConfirmDialog ─────────────────────────────────────────────────
//
// Drop-in replacement for window.confirm. Centred modal with title,
// body, two action buttons. Tone "danger" for destructive actions
// (Delete file), "warning" for reversible interrupts (Discard, Switch
// repo). Spawned by setting confirmState; the dialog clears its own
// state through onClose.

interface ConfirmRequest {
  title: string;
  body: string;
  confirmLabel: string;
  tone?: "warning" | "danger";
  onConfirm: () => void | Promise<void>;
}

function ConfirmDialog({ request, onClose }: { request: ConfirmRequest; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const accent =
    request.tone === "danger"
      ? "bg-red text-white hover:bg-red/90"
      : "bg-blue text-white hover:bg-blue/90";

  const submit = async () => {
    setBusy(true);
    try {
      await request.onConfirm();
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
        role="dialog"
        aria-modal="true"
      >
        <h2 className="text-text font-semibold">{request.title}</h2>
        <p className="text-text-muted text-sm">{request.body}</p>
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            autoFocus
            className={`px-3 py-1.5 text-sm rounded ${accent} disabled:opacity-50`}
          >
            {busy ? "Working…" : request.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

// ─── MarkTemplateDialog ────────────────────────────────────────────
//
// Replaces the two stacked window.prompt() calls (scope + tagline).
// Scope is a constrained <select> so users can't type "publik" — the
// old prompt accepted free text and rejected after the fact.

const TEMPLATE_SCOPES = [
  { value: "private", label: "Private — only this project sees it" },
  { value: "project", label: "Project — anyone in this project" },
  { value: "global", label: "Global — every project on this install" },
] as const;

function MarkTemplateDialog({
  slug,
  onClose,
  onSubmit,
}: {
  slug: string;
  onClose: () => void;
  onSubmit: (scope: string, tagline: string) => Promise<void>;
}) {
  const [scope, setScope] = useState<(typeof TEMPLATE_SCOPES)[number]["value"]>("private");
  const [tagline, setTagline] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await onSubmit(scope, tagline.trim());
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        className="w-[480px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Mark "{slug}" as a template</h2>
        <p className="text-xs text-text-muted">
          Templates appear in the picker when creating a new repo. Forks copy the file tree at the moment of fork.
        </p>
        <div>
          <label className="text-xs text-text-muted block mb-1">Scope</label>
          <select
            value={scope}
            onChange={(e) => setScope(e.target.value as typeof scope)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            {TEMPLATE_SCOPES.map((s) => (
              <option key={s.value} value={s.value}>{s.label}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="text-xs text-text-muted block mb-1">Tagline (optional)</label>
          <input
            type="text"
            value={tagline}
            onChange={(e) => setTagline(e.target.value)}
            placeholder="One-line description shown in the picker"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >
            {busy ? "Working…" : "Mark as template"}
          </button>
        </div>
      </form>
    </div>
  );
}

// ─── ImportGithubDialog ────────────────────────────────────────────
//
// Two-mode picker for importing a repository from GitHub. Mode A
// renders a typeahead populated by `list_repos` (the bound github
// integration's tool); mode B is free-text "owner/repo" for repos the
// user has read access to but doesn't own.
//
// Wires through:
//   GET  /api/github/repos        → integration → list_repos
//   POST /api/github/import       → repos_import_github
//
// When the github connection isn't bound, the GET returns 424 (Failed
// Dependency) and the dialog renders a "Connect GitHub" CTA pointing
// to the dashboard's connections settings rather than 404-ing the
// user's day.

interface GithubRepo {
  id: number;
  name: string;
  full_name: string;
  private: boolean;
  default_branch: string;
  description?: string;
  language?: string;
  pushed_at?: string;
  owner?: { login: string };
}

const FRAMEWORKS_IMPORT = ["", "blank", "nextjs", "static", "go", "python"] as const;

function ImportGithubDialog({
  onClose,
  onImported,
  api,
  withParams,
}: {
  onClose: () => void;
  onImported: (slug: string) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  withParams: (extra?: Record<string, string>) => string;
}) {
  const [mode, setMode] = useState<"picker" | "url">("picker");
  const [repos, setRepos] = useState<GithubRepo[] | null>(null);
  const [reposErr, setReposErr] = useState("");
  const [filter, setFilter] = useState("");

  const [owner, setOwner] = useState("");
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("");
  const [slug, setSlug] = useState("");
  const [framework, setFramework] = useState<(typeof FRAMEWORKS_IMPORT)[number]>("");

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // One-shot fetch of the user's repos. The list_repos integration
  // call is rate-limit-cheap (one HTTP call per page) and apps with
  // <500 repos fit in a single page.
  useEffect(() => {
    if (mode !== "picker" || repos !== null) return;
    let cancelled = false;
    fetch(`/api/apps/code/api/github/repos?${withParams({ sort: "pushed", per_page: "100" })}`, {
      credentials: "same-origin",
    })
      .then(async (r) => {
        if (!r.ok) {
          if (r.status === 424) throw new Error("github_not_connected");
          throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        }
        return r.json() as Promise<GithubRepo[]>;
      })
      .then((j) => {
        if (cancelled) return;
        setRepos(Array.isArray(j) ? j : []);
      })
      .catch((e) => {
        if (cancelled) return;
        setReposErr((e as Error).message);
        setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [mode, repos, withParams]);

  const filtered = (repos || []).filter((r) =>
    r.full_name.toLowerCase().includes(filter.toLowerCase()),
  );

  const pickRepo = (r: GithubRepo) => {
    setOwner(r.owner?.login || r.full_name.split("/")[0] || "");
    setRepo(r.name);
    setRef(r.default_branch || "");
    if (!slug) setSlug(r.name);
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!owner.trim() || !repo.trim()) {
      setErr("owner and repo are required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ repository: { slug: string } }>("POST", "/github/import", {
        owner: owner.trim(),
        repo: repo.trim(),
        ref: ref.trim(),
        slug: slug.trim(),
        framework,
      });
      onImported(r.repository.slug);
    } catch (e2) {
      setErr((e2 as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        className="w-[560px] bg-bg border border-border rounded p-5 space-y-4 max-h-[80vh] overflow-auto"
      >
        <div className="flex items-baseline justify-between">
          <h2 className="text-text font-semibold">Import from GitHub</h2>
          <div className="flex gap-1 text-[11px]">
            <button
              type="button"
              onClick={() => setMode("picker")}
              className={`px-2 py-0.5 rounded border ${mode === "picker" ? "border-accent text-accent" : "border-border text-text-muted hover:text-text"}`}
            >Pick from your repos</button>
            <button
              type="button"
              onClick={() => setMode("url")}
              className={`px-2 py-0.5 rounded border ${mode === "url" ? "border-accent text-accent" : "border-border text-text-muted hover:text-text"}`}
            >owner/repo</button>
          </div>
        </div>

        {mode === "picker" ? (
          repos === null ? (
            <div className="text-xs text-text-muted">Loading repositories…</div>
          ) : reposErr === "github_not_connected" ? (
            <div className="text-xs text-text-muted space-y-2">
              <div>GitHub isn't connected on this install.</div>
              <div>Open Settings → Integrations in the dashboard, connect GitHub, then bind it to this install's "GitHub" role.</div>
              <button
                type="button"
                onClick={() => setMode("url")}
                className="text-accent hover:underline"
              >Switch to owner/repo entry instead →</button>
            </div>
          ) : repos.length === 0 ? (
            <div className="text-xs text-text-muted">{reposErr || "No repositories accessible to this connection."}</div>
          ) : (
            <div className="space-y-2">
              <input
                type="text"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Filter (owner/repo)"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <ul className="max-h-48 overflow-auto border border-border rounded divide-y divide-border">
                {filtered.slice(0, 50).map((r) => {
                  const picked = owner === (r.owner?.login || "") && repo === r.name;
                  return (
                    <li key={r.id}>
                      <button
                        type="button"
                        onClick={() => pickRepo(r)}
                        className={`w-full text-left px-2 py-1.5 text-xs hover:bg-bg-input ${picked ? "bg-bg-input" : ""}`}
                      >
                        <div className="flex items-baseline justify-between gap-2">
                          <span className="font-mono truncate">{r.full_name}</span>
                          <span className="flex gap-1 shrink-0 text-[10px] text-text-dim">
                            {r.private && <span className="px-1 border border-border rounded">private</span>}
                            {r.language && <span>{r.language}</span>}
                          </span>
                        </div>
                        {r.description && (
                          <div className="text-[11px] text-text-muted truncate">{r.description}</div>
                        )}
                      </button>
                    </li>
                  );
                })}
              </ul>
              {filtered.length > 50 && (
                <div className="text-[10px] text-text-dim">Showing 50 of {filtered.length}. Refine the filter to see more.</div>
              )}
            </div>
          )
        ) : (
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-text-muted block mb-1">Owner</label>
              <input
                type="text"
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
                placeholder="apteva"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            </div>
            <div>
              <label className="text-xs text-text-muted block mb-1">Repo</label>
              <input
                type="text"
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
                placeholder="apps"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            </div>
          </div>
        )}

        <div className="grid grid-cols-2 gap-2">
          <div>
            <label className="text-xs text-text-muted block mb-1">Branch / ref</label>
            <input
              type="text"
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              placeholder="main"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Local slug</label>
            <input
              type="text"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={repo || "my-import"}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-text-muted block mb-1">Framework</label>
            <select
              value={framework}
              onChange={(e) => setFramework(e.target.value as typeof framework)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              {FRAMEWORKS_IMPORT.map((f) => (
                <option key={f} value={f}>{f === "" ? "(auto-detect)" : f}</option>
              ))}
            </select>
          </div>
        </div>

        {err && <div className="text-red text-xs">{err}</div>}

        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy || !owner.trim() || !repo.trim()}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >{busy ? "Importing…" : "Import"}</button>
        </div>
      </form>
    </div>
  );
}

// ─── DevBar / DevLogsView ─────────────────────────────────────────
//
// Dev runtime UI for the right pane. DevBar is the thin status strip
// above the file editor; it polls /api/repos/<slug>/dev/status every
// 2s and offers Run / Stop. DevLogsView replaces the file editor when
// the user toggles "Logs", streaming the dev process's stdout/stderr
// via SSE on /api/repos/<slug>/dev/log?follow=1.

interface DevRunWire {
  id: number;
  status: "starting" | "live" | "stopped" | "crashed";
  port: number;
  pid: number;
  framework: string;
  run_cmd?: string;
  started_at?: string;
  stopped_at?: string;
  error?: string;
}

function devStatusColor(s?: string): string {
  if (s === "live") return "text-green";
  if (s === "starting") return "text-blue";
  if (s === "crashed") return "text-red";
  return "text-text-dim";
}

function uptimeStr(startedAt?: string): string {
  if (!startedAt) return "";
  const ms = Date.now() - new Date(startedAt).getTime();
  if (ms < 0) return "";
  if (ms < 60_000) return `${Math.floor(ms / 1000)}s`;
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`;
  return `${Math.floor(ms / 3_600_000)}h${Math.floor((ms % 3_600_000) / 60_000)}m`;
}

function DevBar({
  slug,
  api,
  withParams,
  showLogs,
  onToggleLogs,
  onError,
}: {
  slug: string;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  withParams: (extra?: Record<string, string>) => string;
  showLogs: boolean;
  onToggleLogs: () => void;
  onError: (msg: string) => void;
}) {
  const [run, setRun] = useState<DevRunWire | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const r = await api<{ dev_run: DevRunWire | null }>("GET", `/repos/${slug}/dev/status`);
      setRun(r.dev_run);
    } catch (e) {
      // Swallow — this polls in the background; the panel-wide error
      // banner is for explicit user actions.
    }
  }, [api, slug]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 2000);
    return () => clearInterval(t);
  }, [refresh]);

  const start = async () => {
    setBusy(true);
    try {
      await api("POST", `/repos/${slug}/dev/start`, {});
      await refresh();
    } catch (e) {
      onError("Run failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const stop = async () => {
    setBusy(true);
    try {
      await api("POST", `/repos/${slug}/dev/stop`, {});
      await refresh();
    } catch (e) {
      onError("Stop failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const status = run?.status ?? "stopped";
  const isLive = status === "live";
  const isBusy = status === "starting" || busy;

  return (
    <div className="px-3 py-2 border-b border-border flex items-center gap-2 bg-bg-input/40">
      <span className={`text-xs ${devStatusColor(status)}`}>●</span>
      <span className="text-xs text-text-muted">
        {status === "live" ? (
          <>
            Running on{" "}
            <a
              href={`http://127.0.0.1:${run?.port}/`}
              target="_blank"
              rel="noreferrer"
              className="text-accent hover:underline font-mono"
            >127.0.0.1:{run?.port}</a>
            {" "}· {uptimeStr(run?.started_at)} · {run?.framework}
          </>
        ) : status === "starting" ? (
          <>Starting {run?.framework}…</>
        ) : status === "crashed" ? (
          <span className="text-red">
            Crashed: <span className="font-mono">{(run?.error || "").split("\n")[0].slice(0, 80)}</span>
          </span>
        ) : (
          <>Dev runtime stopped</>
        )}
      </span>
      <span className="flex-1" />
      <button
        type="button"
        onClick={onToggleLogs}
        className={`px-2 py-0.5 text-xs border rounded ${showLogs ? "border-accent text-accent" : "border-border text-text-muted hover:text-text"}`}
      >Logs</button>
      {isLive ? (
        <button
          type="button"
          onClick={stop}
          disabled={isBusy}
          className="px-2 py-0.5 text-xs border border-red text-red rounded hover:bg-red hover:text-white disabled:opacity-50"
        >Stop</button>
      ) : (
        <button
          type="button"
          onClick={start}
          disabled={isBusy}
          className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >{isBusy ? "Starting…" : "Run"}</button>
      )}
    </div>
  );
}

// DevLogsView streams the dev run log via SSE. Auto-scrolls to bottom
// unless the user scrolls up; resumes auto-scroll when they scroll
// back to the bottom. No history retention beyond the current session
// — the log file is truncated on each `repos_dev_start`, so older
// runs aren't accessible (by design).
function DevLogsView({
  slug,
  withParams,
}: {
  slug: string;
  withParams: (extra?: Record<string, string>) => string;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const containerRef = useRef<HTMLPreElement | null>(null);
  const stickToBottom = useRef(true);

  useEffect(() => {
    setLines([]);
    const url = `/api/apps/code/api/repos/${slug}/dev/log?${withParams({ follow: "1" })}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      // SSE delivers each `data:` line as a separate message; the
      // server emits them line-by-line so we just append.
      setLines((prev) => prev.concat([e.data]));
    };
    es.onerror = () => {
      // EventSource auto-reconnects; nothing to do.
    };
    return () => es.close();
  }, [slug, withParams]);

  useEffect(() => {
    if (stickToBottom.current && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [lines]);

  const onScroll = (e: React.UIEvent<HTMLPreElement>) => {
    const el = e.currentTarget;
    const distFromBottom = el.scrollHeight - el.clientHeight - el.scrollTop;
    stickToBottom.current = distFromBottom < 24;
  };

  return (
    <pre
      ref={containerRef}
      onScroll={onScroll}
      className="flex-1 overflow-auto bg-bg text-text font-mono text-[11px] p-3 whitespace-pre"
    >
      {lines.length === 0 ? (
        <span className="text-text-dim">Waiting for output…</span>
      ) : (
        lines.join("\n")
      )}
    </pre>
  );
}

// ─── FileTree ──────────────────────────────────────────────────────
//
// Classic IDE folder tree. Folders expand/collapse with a chevron;
// files render with an ext-typed glyph. Indentation by depth, hover
// reveals rename/delete actions on file rows.

interface FileTreeProps {
  tree: TreeNode[];
  expanded: Set<string>;
  onToggle: (path: string) => void;
  openPath?: string;
  renaming: string | null;
  renameTo: string;
  setRenameTo: (s: string) => void;
  onRenameSubmit: (from: string) => void;
  onRenameCancel: () => void;
  onSelect: (path: string) => void;
  onStartRename: (path: string) => void;
  onDelete: (path: string) => void;
}

function FileTree(props: FileTreeProps) {
  const rows = flattenTree(props.tree, props.expanded);
  return (
    <ul className="select-none">
      {rows.map(({ node, depth }) => (
        <FileTreeRow key={node.path} node={node} depth={depth} {...props} />
      ))}
    </ul>
  );
}

function FileTreeRow({
  node, depth,
  expanded, onToggle,
  openPath,
  renaming, renameTo, setRenameTo, onRenameSubmit, onRenameCancel,
  onSelect, onStartRename, onDelete,
}: FileTreeProps & { node: TreeNode; depth: number }) {
  const isOpen = openPath === node.path;
  const isRenaming = renaming === node.path;
  const indent = depth * 12 + 8;

  if (node.isDir) {
    const open = expanded.has(node.path);
    return (
      <li className="text-xs">
        <button
          type="button"
          onClick={() => onToggle(node.path)}
          className="w-full flex items-center gap-1 px-2 py-0.5 text-left hover:bg-bg-input/50 text-text"
          style={{ paddingLeft: `${indent}px` }}
        >
          <span className="w-3 text-text-dim">{open ? "▾" : "▸"}</span>
          <span className={open ? "text-yellow/80" : "text-yellow/60"}>📁</span>
          <span className="truncate">{node.name}</span>
        </button>
      </li>
    );
  }

  const glyph = fileGlyph(node.name);
  return (
    <li className={`group text-xs ${isOpen ? "bg-bg-input" : "hover:bg-bg-input/50"}`}>
      {isRenaming ? (
        <form
          onSubmit={(e) => { e.preventDefault(); onRenameSubmit(node.path); }}
          className="flex items-center gap-1 px-2 py-0.5"
          style={{ paddingLeft: `${indent + 16}px` }}
        >
          <input
            autoFocus
            type="text"
            value={renameTo}
            onChange={(e) => setRenameTo(e.target.value)}
            onBlur={onRenameCancel}
            onKeyDown={(e) => { if (e.key === "Escape") onRenameCancel(); }}
            className="flex-1 bg-bg-input border border-border rounded px-1 py-0.5 text-xs"
          />
        </form>
      ) : (
        <div className="flex items-center gap-2 pr-2" style={{ paddingLeft: `${indent + 16}px` }}>
          <span className={`w-5 text-[10px] font-mono ${glyph.cls}`}>{glyph.letter}</span>
          <button
            type="button"
            onClick={() => onSelect(node.path)}
            className="text-text truncate flex-1 text-left py-0.5"
            title={node.path}
          >{node.name}</button>
          <span className="text-text-dim text-[10px] group-hover:hidden">
            {formatSize(node.size)}
          </span>
          <span className="hidden group-hover:flex items-center gap-1">
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onStartRename(node.path); }}
              className="text-text-dim hover:text-text px-1"
              title="Rename"
            >✎</button>
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onDelete(node.path); }}
              className="text-red/70 hover:text-red px-1"
              title="Delete"
            >🗑</button>
          </span>
        </div>
      )}
    </li>
  );
}
