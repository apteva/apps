// CodePanel — native React panel for the code app. Three-pane:
// left = repo list, middle = file tree of the selected repo,
// right = file editor. Loaded by the dashboard via dynamic import;
// uses host React via importmap; talks to the code sidecar through
// /api/apps/code/api/* with same-origin cookies.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { DeviceFrame } from "./components/DeviceFrame";
import { parseCodePanelLink } from "./components/codeLinks";

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

interface GitRemote {
  fetch_url: string;
  provider_slug?: string;
  default_branch?: string;
  last_fetch_at?: string;
  last_push_at?: string;
  last_error?: string;
}

interface GitStatus {
  git_backed: boolean;
  branch?: string;
  detached?: boolean;
  head_sha?: string;
  upstream?: string;
  ahead: number;
  behind: number;
  dirty: boolean;
  conflicted: boolean;
  changes?: Array<{ path: string; original_path?: string; index: string; worktree: string }>;
  remote?: GitRemote;
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

type IssueStatus = "todo" | "triage" | "planned" | "in_progress" | "in_review" | "blocked" | "done";
type IssueState = "open" | "closed";
type IssueStateReason = "" | "completed" | "not_planned";
type IssueKind = "bug" | "feature" | "task" | "chore";
type IssuePriority = "low" | "medium" | "high" | "urgent";

interface CodeIssue {
  id: number;
  repo_id: number;
  repo_slug?: string;
  number: number;
  title: string;
  body: string;
  type: IssueKind;
  status: IssueStatus;
  state: IssueState;
  state_reason?: IssueStateReason;
  priority: IssuePriority;
  assignee?: string;
  created_by?: string;
  comments_count?: number;
  links_count?: number;
  closed_at?: string;
  created_at: string;
  updated_at: string;
}

interface IssueComment {
  id: number;
  issue_id: number;
  author?: string;
  body: string;
  created_at: string;
}

interface IssueLink {
  id: number;
  issue_id: number;
  kind: string;
  target: string;
  title?: string;
  data_json?: string;
  created_at: string;
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

type TreeIconName =
  | "file"
  | "folder"
  | "folderOpen"
  | "code"
  | "image"
  | "lock"
  | "box"
  | "config"
  | "text"
  | "key"
  | "pencil"
  | "trash";

// File-type icons. Small SVG strokes keep the source tree looking
// like an editor sidebar without adding another dependency to the
// standalone panel bundle.
function fileGlyph(name: string): { icon: TreeIconName; cls: string } {
  const ext = name.toLowerCase().split(".").pop() || "";
  switch (ext) {
    case "ts":
    case "tsx":
    case "js":
    case "mjs":
    case "cjs":
    case "jsx":
    case "go":
    case "py":
    case "sh":
    case "bash":
    case "zsh":
    case "html":
    case "htm":
      return { icon: "code", cls: "text-blue/80" };
    case "json":
    case "yaml":
    case "yml":
    case "toml":
      return { icon: "config", cls: "text-yellow/80" };
    case "md":
    case "mdx":
      return { icon: "text", cls: "text-text-muted" };
    case "css":
    case "scss":
    case "sass":
      return { icon: "config", cls: "text-blue/80" };
    case "svg":
    case "png":
    case "jpg":
    case "jpeg":
    case "gif":
    case "webp":
    case "ico":
      return { icon: "image", cls: "text-green/80" };
    case "lock":
      return { icon: "lock", cls: "text-text-dim" };
    case "env":
      return { icon: "key", cls: "text-yellow/70" };
    case "dockerfile":
      return { icon: "box", cls: "text-blue/80" };
  }
  // Special-case some no-extension files (Dockerfile, README, etc.)
  if (name === "Dockerfile") return { icon: "box", cls: "text-blue/80" };
  if (name.toLowerCase().startsWith("readme")) return { icon: "text", cls: "text-text-muted" };
  if (name.startsWith(".")) return { icon: "config", cls: "text-text-dim" };
  return { icon: "file", cls: "text-text-dim" };
}

interface FileEventData {
  slug?: string;
  path?: string;
  from?: string;
  to?: string;
  number?: number;
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
  const initialLink = useRef(
    parseCodePanelLink(typeof window === "undefined" ? "" : window.location.search),
  ).current;
  const [repos, setRepos] = useState<Repo[]>([]);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [query, setQuery] = useState("");
  const [selectedSlug, setSelectedSlug] = useState<string | null>(initialLink.repo || null);
  const [tree, setTree] = useState<FileMeta[]>([]);
  const [openFile, setOpenFile] = useState<{ path: string; content: string } | null>(null);
  const [draft, setDraft] = useState<string>("");
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [loadingTree, setLoadingTree] = useState(false);
  const [loadingFile, setLoadingFile] = useState(false);
  const [gitStatus, setGitStatus] = useState<GitStatus | null>(null);
  const [gitBusy, setGitBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [showImportGithub, setShowImportGithub] = useState(false);
  const [zipImportTarget, setZipImportTarget] = useState<"new" | string | null>(null);
  const [showNewFile, setShowNewFile] = useState(false);
  const [showNewFolder, setShowNewFolder] = useState(false);
  const [showDevLogs, setShowDevLogs] = useState(false);
  const [activeView, setActiveView] = useState<"code" | "issues">(
    initialLink.view === "issues" ? "issues" : "code",
  );
  // Lifted from DevBar so the main content area can render the live
  // device view for remote (Simulator-app) dev runs.
  const [devRun, setDevRun] = useState<DevRunWire | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameTo, setRenameTo] = useState("");
  const [forkSlug, setForkSlug] = useState<string | null>(null);
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());
  const uploadRef = useRef<HTMLInputElement | null>(null);
  const fileViewRef = useRef<HTMLPreElement | null>(null);

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

  const importZip = useCallback(
    async (slug: string, file: File) => {
      const url = `${API}/repos/${slug}/import?${withParams()}`;
      const res = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": file.type || "application/zip" },
        body: await file.arrayBuffer(),
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json() as Promise<{ files_imported: number }>;
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

  const loadGitStatus = useCallback(
    async (slug: string) => {
      try {
        setGitStatus(await api<GitStatus>("GET", `/repos/${slug}/git/status`));
      } catch {
        setGitStatus(null);
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
    setActiveView("code");
    setExpandedDirs(new Set()); // reset; loadTree seeds top-level dirs.
    loadTree(slug);
    loadGitStatus(slug);
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

  const initialLinkApplied = useRef(false);
  useEffect(() => {
    if (initialLinkApplied.current || initialLink.view !== "repositories" || !initialLink.repo) return;
    initialLinkApplied.current = true;
    loadTree(initialLink.repo);
    loadGitStatus(initialLink.repo);
    if (initialLink.path) openPath(initialLink.repo, initialLink.path);
  }, [initialLink, loadGitStatus, loadTree, openPath]);

  const initialLineApplied = useRef(false);
  useEffect(() => {
    if (
      initialLineApplied.current ||
      !initialLink.line ||
      !initialLink.path ||
      openFile?.path !== initialLink.path ||
      !fileViewRef.current
    ) return;
    initialLineApplied.current = true;
    fileViewRef.current.parentElement?.scrollTo({
      top: Math.max(0, initialLink.line - 1) * 20,
      behavior: "auto",
    });
  }, [initialLink, openFile]);

  // Live refresh — react to repo + file mutations from agents, other
  // tabs, AND this panel's own writes (the backend emits on every
  // mutation, REST-driven or MCP-driven).
  useAppEvents<FileEventData>("code", projectId, (ev) => {
    switch (ev.topic) {
      case "repo.added":
      case "repo.updated":
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
      case "repo.git.connected":
      case "repo.git.fetched":
      case "repo.git.pulled":
      case "repo.git.committed":
      case "repo.git.pushed":
      case "repo.git.switched":
        if (selectedSlug && ev.data?.slug === selectedSlug) {
          loadGitStatus(selectedSlug);
          loadTree(selectedSlug);
        }
        break;
    }
  });

  const runGitAction = async (action: "fetch" | "pull" | "push") => {
    if (!selectedSlug) return;
    setGitBusy(true);
    setError("");
    try {
      setGitStatus(await api<GitStatus>("POST", `/repos/${selectedSlug}/git/${action}`, {}));
      if (action === "pull") {
        await loadTree(selectedSlug);
        if (openFile && !dirty) await openPath(selectedSlug, openFile.path);
      }
    } catch (e) {
      setError(`Git ${action} failed: ${(e as Error).message}`);
    } finally {
      setGitBusy(false);
    }
  };

  const commitGitChanges = async () => {
    if (!selectedSlug) return;
    const message = window.prompt("Commit message");
    if (!message?.trim()) return;
    setGitBusy(true);
    setError("");
    try {
      setGitStatus(await api<GitStatus>("POST", `/repos/${selectedSlug}/git/commit`, { message: message.trim() }));
    } catch (e) {
      setError(`Git commit failed: ${(e as Error).message}`);
    } finally {
      setGitBusy(false);
    }
  };

  const connectGitRemote = async () => {
    if (!selectedSlug) return;
    const remoteUrl = window.prompt("HTTPS Git remote URL");
    if (!remoteUrl?.trim()) return;
    setGitBusy(true);
    setError("");
    try {
      const result = await api<{ status: GitStatus; reconciliation_required: boolean; safety_branch?: string }>(
        "POST", `/repos/${selectedSlug}/git/connect`, { remote_url: remoteUrl.trim() },
      );
      setGitStatus(result.status);
      if (result.reconciliation_required) {
        setError(`Remote connected without overwriting local files. Reconciliation is required; local work is preserved on ${result.safety_branch || "a safety branch"}.`);
      }
    } catch (e) {
      setError(`Connect failed: ${(e as Error).message}`);
    } finally {
      setGitBusy(false);
    }
  };

  const selectFile = (path: string) => {
    if (!selectedSlug) return;
    setActiveView("code");
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
    <div className="h-full flex flex-col">
      <nav className="px-4 border-b border-border flex items-center gap-4 shrink-0">
        <button
          type="button"
          onClick={() => setActiveView("code")}
          className={`py-2 text-sm border-b-2 ${
            activeView === "code" ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"
          }`}
        >Repositories</button>
        <button
          type="button"
          onClick={() => {
            setShowDevLogs(false);
            setActiveView("issues");
          }}
          className={`py-2 text-sm border-b-2 ${
            activeView === "issues" ? "border-accent text-text" : "border-transparent text-text-muted hover:text-text"
          }`}
        >Issues</button>
      </nav>

      <div className="flex-1 min-h-0 flex">
      {/* Repo list */}
      {activeView === "code" && (
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
              title="Clone a Git repository"
              className="px-2 py-0.5 text-xs border border-border text-text-muted rounded hover:text-text hover:border-text"
            >Git</button>
            <button
              type="button"
              onClick={() => setZipImportTarget("new")}
              title="Import a repository from ZIP"
              className="px-2 py-0.5 text-xs border border-border text-text-muted rounded hover:text-text hover:border-text"
            >ZIP</button>
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
      )}

      {activeView === "issues" ? (
        <main className="flex-1 min-w-0 overflow-hidden flex">
          <IssuesView
            repos={repos}
            projectId={projectId}
            api={api}
            initialRepoSlug={initialLink.view === "issues" ? initialLink.repo : undefined}
            initialIssueNumber={initialLink.view === "issues" ? initialLink.issue : undefined}
            currentPath={openFile?.path}
            currentRepoSlug={selectedSlug ?? undefined}
            onOpenPath={(repoSlug, path) => {
              setActiveView("code");
              if (selectedSlug !== repoSlug) {
                setSelectedSlug(repoSlug);
                setOpenFile(null);
                setEditing(false);
                setDraft("");
                setExpandedDirs(new Set());
                loadTree(repoSlug);
              }
              openPath(repoSlug, path);
            }}
          />
        </main>
      ) : !selectedSlug ? (
        <main className="flex-1 overflow-hidden flex items-center justify-center text-text-muted text-sm">
          Select a repository.
        </main>
      ) : (
        <main className="flex-1 min-w-0 overflow-hidden flex flex-col">
          <header className="px-4 py-3 border-b border-border flex items-center gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 min-w-0">
                <h1 className="text-text font-semibold truncate">{selectedRepo?.name || selectedSlug}</h1>
                {selectedRepo?.framework && (
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-blue/15 text-blue">
                    {selectedRepo.framework}
                  </span>
                )}
                {selectedRepo?.is_template && (
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-yellow/15 text-yellow">
                    template
                  </span>
                )}
              </div>
              <div className="mt-0.5 text-xs text-text-dim font-mono truncate">{selectedSlug}</div>
            </div>
            <button
              type="button"
              onClick={() => handleToggleTemplate(selectedSlug)}
              className="px-2 py-0.5 text-xs border border-border rounded text-text-muted hover:text-text"
            >{selectedRepo?.is_template ? "Unmark template" : "Save as template"}</button>
            <button
              type="button"
              onClick={() => setForkSlug(selectedSlug)}
              className="px-2 py-0.5 text-xs border border-border rounded text-text-muted hover:text-text"
            >Fork</button>
            <button
              type="button"
              onClick={() => handleArchive(selectedSlug)}
              className="px-2 py-0.5 text-xs border border-red/50 text-red rounded hover:bg-red hover:text-white"
            >Archive</button>
          </header>

          <div className="px-4 py-1.5 border-b border-border flex items-center gap-3 text-[11px] text-text-muted min-h-8">
            {gitStatus?.git_backed ? (
              <>
                <span
                  className={`font-mono truncate ${gitStatus.conflicted ? "text-red" : gitStatus.dirty ? "text-yellow" : "text-green"}`}
                  title={gitStatus.remote?.fetch_url || "Git repository"}
                >
                  {gitStatus.detached ? "detached" : gitStatus.branch || "git"}{gitStatus.dirty ? "*" : ""}
                </span>
                {gitStatus.upstream && <span className="font-mono text-text-dim truncate">{gitStatus.upstream}</span>}
                {gitStatus.ahead > 0 && <span title="Commits ahead">↑{gitStatus.ahead}</span>}
                {gitStatus.behind > 0 && <span title="Commits behind">↓{gitStatus.behind}</span>}
                <span className="flex-1" />
                <button type="button" disabled={gitBusy} onClick={() => runGitAction("fetch")} className="hover:text-text disabled:opacity-40">Fetch</button>
                <button type="button" disabled={gitBusy || gitStatus.dirty || gitStatus.conflicted} onClick={() => runGitAction("pull")} className="hover:text-text disabled:opacity-40" title="Fast-forward pull">Pull</button>
                <button type="button" disabled={gitBusy || !gitStatus.dirty || gitStatus.conflicted} onClick={commitGitChanges} className="hover:text-text disabled:opacity-40">Commit</button>
                <button type="button" disabled={gitBusy || gitStatus.conflicted} onClick={() => runGitAction("push")} className="hover:text-text disabled:opacity-40">Push</button>
              </>
            ) : (
              <button type="button" disabled={gitBusy} onClick={connectGitRemote} className="text-accent hover:underline disabled:opacity-40">Connect Git remote</button>
            )}
          </div>

          <div className="flex-1 min-h-0 flex">
              <aside className="w-72 border-r border-border flex flex-col">
                <div className="p-3 border-b border-border flex items-center gap-1">
                  <span className="text-xs uppercase tracking-wide text-text-dim flex-1 truncate">Files</span>
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
                  <button
                    type="button"
                    onClick={() => setZipImportTarget(selectedSlug)}
                    className="px-1 py-0.5 text-xs text-accent hover:text-accent/80"
                    title="Import ZIP"
                  >ZIP</button>
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
              </aside>

              <section className="flex-1 min-w-0 overflow-hidden flex flex-col">
                <DevBar
                  slug={selectedSlug}
                  api={api}
                  withParams={withParams}
                  showLogs={showDevLogs}
                  onToggleLogs={() => setShowDevLogs((v) => !v)}
                  onError={(msg) => setError(msg)}
                  onRunChange={setDevRun}
                />
                {showDevLogs ? (
                  <DevLogsView slug={selectedSlug} withParams={withParams} />
                ) : devRun?.runner === "simulator" && devRun.status === "live" ? (
                  <RemoteDeviceView run={devRun} />
                ) : !openFile ? (
                  <div className="p-8 text-text-muted text-sm text-center mt-12">
                    Click a file in the tree to view it. + File to create one.
                  </div>
                ) : (
                  <>
                    <header className="p-3 border-b border-border flex items-center gap-2">
                      <span className="text-xs uppercase tracking-wide text-text-dim">file</span>
                      <span className="text-text font-mono text-sm truncate flex-1">
                        {openFile.path}
                        {initialLink.path === openFile.path && initialLink.line ? `:${initialLink.line}` : ""}
                        {dirty ? " •" : ""}
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
                        <pre ref={fileViewRef} className="text-[11px] leading-5 font-mono p-4 text-text whitespace-pre overflow-auto">
                          {openFile.content}
                        </pre>
                      )}
                    </div>
                  </>
                )}
              </section>
            </div>
        </main>
      )}
      </div>

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

      {zipImportTarget && (
        <ImportZipDialog
          targetSlug={zipImportTarget === "new" ? undefined : zipImportTarget}
          onClose={() => setZipImportTarget(null)}
          onImported={(slug) => {
            setZipImportTarget(null);
            if (selectedSlug === slug) {
              loadRepos();
              loadTree(slug);
            } else {
              loadRepos().then(() => selectRepo(slug));
            }
          }}
          api={api}
          importZip={importZip}
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

function ImportZipDialog({
  targetSlug,
  onClose,
  onImported,
  api,
  importZip,
}: {
  targetSlug?: string;
  onClose: () => void;
  onImported: (slug: string) => void;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  importZip: (slug: string, file: File) => Promise<{ files_imported: number }>;
}) {
  const importingIntoExisting = Boolean(targetSlug);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file) {
      setErr("zip required");
      return;
    }
    if (!file.name.toLowerCase().endsWith(".zip")) {
      setErr("file must be a zip");
      return;
    }
    if (!importingIntoExisting && !name.trim()) {
      setErr("name required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      let importSlug = targetSlug || "";
      if (!importSlug) {
        const created = await api<{ repository: Repo }>("POST", "/repos", {
          name: name.trim(),
          slug: slug.trim(),
          framework: "blank",
          description: description.trim(),
        });
        importSlug = created.repository.slug;
      }
      await importZip(importSlug, file);
      onImported(importSlug);
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
        className="w-[460px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">
          {targetSlug ? `Import ZIP into ${targetSlug}` : "Import ZIP"}
        </h2>
        {!targetSlug && (
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
              <label className="text-xs text-text-muted block mb-1">Slug (optional)</label>
              <input
                type="text"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
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
          </div>
        )}
        <div>
          <label className="text-xs text-text-muted block mb-1">ZIP file</label>
          <input
            type="file"
            accept=".zip,application/zip,application/x-zip-compressed"
            onChange={(e) => setFile(e.target.files?.[0] || null)}
            className="w-full text-sm text-text file:mr-3 file:px-3 file:py-1 file:rounded file:border file:border-border file:bg-bg-input file:text-text-muted hover:file:text-text"
          />
          {targetSlug && (
            <div className="text-[11px] text-text-dim mt-1">
              Matching paths are overwritten. Other files stay.
            </div>
          )}
        </div>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Importing…" : "Import"}</button>
        </div>
      </form>
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
// Provider-neutral Git clone dialog. GitHub is used only as an optional
// discovery picker; both picker and URL modes clone through the generic Git
// service and retain history/upstream tracking.
//
// Wires through:
//   GET  /api/github/repos        → integration → list_repos
//   POST /api/git/import          → repos_git_import
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
  const [remoteUrl, setRemoteUrl] = useState("");
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
    setRemoteUrl(`https://github.com/${r.full_name}.git`);
    setRef(r.default_branch || "");
    if (!slug) setSlug(r.name);
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const cloneURL = mode === "picker"
      ? remoteUrl || (owner.trim() && repo.trim() ? `https://github.com/${owner.trim()}/${repo.trim()}.git` : "")
      : remoteUrl.trim();
    if (!cloneURL) {
      setErr(mode === "picker" ? "choose a repository" : "Git remote URL is required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ repository: { slug: string } }>("POST", "/git/import", {
        remote_url: cloneURL,
        ref: ref.trim(),
        slug: slug.trim(),
        name: mode === "picker" && owner && repo ? `${owner}/${repo}` : undefined,
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
          <h2 className="text-text font-semibold">Clone Git repository</h2>
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
            >Git URL</button>
          </div>
        </div>

        {mode === "picker" ? (
          repos === null ? (
            <div className="text-xs text-text-muted">Loading repositories…</div>
          ) : reposErr === "github_not_connected" ? (
            <div className="text-xs text-text-muted space-y-2">
              <div>GitHub discovery isn't connected on this install.</div>
              <div>Public repositories can still be cloned by URL. For private repositories, bind the matching GitHub, GitLab, or Bitbucket connection to this install's Git provider role.</div>
              <button
                type="button"
                onClick={() => setMode("url")}
                className="text-accent hover:underline"
              >Switch to Git URL entry instead →</button>
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
          <div>
            <label className="text-xs text-text-muted block mb-1">HTTPS Git remote</label>
            <input
              type="url"
              value={remoteUrl}
              onChange={(e) => setRemoteUrl(e.target.value)}
              placeholder="https://git.example.com/team/repository.git"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
            <div className="text-[10px] text-text-dim mt-1">Public remotes work directly. Private remotes automatically use a matching bound provider connection.</div>
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
            disabled={busy || (mode === "picker" ? !remoteUrl : !remoteUrl.trim())}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >{busy ? "Cloning…" : "Clone"}</button>
        </div>
      </form>
    </div>
  );
}

// ─── IssuesView ────────────────────────────────────────────────────
//
// Native local issue tracker for a selected Code repo. The backing
// REST routes mirror the MCP issue tools, so humans and agents work
// against the same lifecycle.

const ISSUE_TYPES: IssueKind[] = ["bug", "feature", "task", "chore"];
const ISSUE_STATES: IssueState[] = ["open", "closed"];
const ISSUE_STATE_REASONS: IssueStateReason[] = ["completed", "not_planned"];
const ISSUE_STATUSES: IssueStatus[] = ["todo", "triage", "planned", "in_progress", "in_review", "blocked", "done"];
const ISSUE_PRIORITIES: IssuePriority[] = ["low", "medium", "high", "urgent"];

function IssuesView({
  repos,
  projectId,
  api,
  initialRepoSlug,
  initialIssueNumber,
  currentPath,
  currentRepoSlug,
  onOpenPath,
}: {
  repos: Repo[];
  projectId: string;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  initialRepoSlug?: string;
  initialIssueNumber?: number;
  currentPath?: string;
  currentRepoSlug?: string;
  onOpenPath: (repoSlug: string, path: string) => void;
}) {
  const [issues, setIssues] = useState<CodeIssue[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<{ issue: CodeIssue; comments: IssueComment[]; links: IssueLink[] } | null>(null);
  const [repoFilter, setRepoFilter] = useState(initialRepoSlug || "");
  const [state, setState] = useState("all");
  const [status, setStatus] = useState("all");
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [showMetaEdit, setShowMetaEdit] = useState(false);
  const [comment, setComment] = useState("");
  const initialIssueApplied = useRef(false);

  const loadIssues = useCallback(async () => {
    try {
      const extra: Record<string, string> = { state, status };
      if (repoFilter) extra.repo = repoFilter;
      if (query.trim()) extra.q = query.trim();
      const r = await api<{ issues: CodeIssue[] }>("GET", "/issues", undefined, extra);
      const list = r.issues || [];
      setIssues(list);
      setSelected((cur) => {
        if (!initialIssueApplied.current && initialIssueNumber) {
          const linked = list.find((issue) =>
            issue.number === initialIssueNumber && (!initialRepoSlug || issue.repo_slug === initialRepoSlug),
          );
          if (linked) {
            initialIssueApplied.current = true;
            return linked.id;
          }
        }
        return cur && list.some((issue) => issue.id === cur) ? cur : list[0]?.id ?? null;
      });
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
      setIssues([]);
      setSelected(null);
    }
  }, [api, repoFilter, state, status, query, initialIssueNumber, initialRepoSlug]);

  const loadDetail = useCallback(async (issueId: number | null) => {
    if (!issueId) {
      setDetail(null);
      return;
    }
    const item = issues.find((i) => i.id === issueId);
    if (!item?.repo_slug) {
      setDetail(null);
      return;
    }
    try {
      const r = await api<{ issue: CodeIssue; comments?: IssueComment[]; links?: IssueLink[] }>("GET", `/repos/${item.repo_slug}/issues/${item.number}`);
      setDetail({ issue: r.issue, comments: r.comments || [], links: r.links || [] });
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
      setDetail(null);
    }
  }, [api, issues]);

  useEffect(() => { loadIssues(); }, [loadIssues]);
  useEffect(() => { loadDetail(selected); }, [selected, loadDetail]);

  useAppEvents<FileEventData>("code", projectId, (ev) => {
    if (!ev.topic.startsWith("issue.")) return;
    if (repoFilter && ev.data?.slug !== repoFilter) return;
    loadIssues();
    if (detail && ev.data?.slug === detail.issue.repo_slug && ev.data.number === detail.issue.number) {
      loadDetail(selected);
    }
  });

  const patchIssue = async (patch: Partial<CodeIssue>) => {
    if (!detail?.issue.repo_slug) return;
    setBusy(true);
    try {
      const r = await api<{ issue: CodeIssue }>("PATCH", `/repos/${detail.issue.repo_slug}/issues/${detail.issue.number}`, patch);
      setDetail((cur) => cur ? { ...cur, issue: r.issue } : cur);
      await loadIssues();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const addComment = async () => {
    if (!detail?.issue.repo_slug || !comment.trim()) return;
    setBusy(true);
    try {
      await api("POST", `/repos/${detail.issue.repo_slug}/issues/${detail.issue.number}/comments`, { body: comment.trim(), author: "human" });
      setComment("");
      await loadDetail(detail.issue.id);
      await loadIssues();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const closeOrReopen = async () => {
    if (!detail?.issue.repo_slug) return;
    const closed = detail.issue.state === "closed";
    setBusy(true);
    try {
      await api("POST", `/repos/${detail.issue.repo_slug}/issues/${detail.issue.number}/${closed ? "reopen" : "close"}`, { actor: "human", state_reason: "completed" });
      await loadDetail(detail.issue.id);
      await loadIssues();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const linkCurrentPath = async () => {
    if (!detail?.issue.repo_slug || !currentPath || detail.issue.repo_slug !== currentRepoSlug) return;
    setBusy(true);
    try {
      await api("POST", `/repos/${detail.issue.repo_slug}/issues/${detail.issue.number}/links/path`, { path: currentPath, actor: "human" });
      await loadDetail(detail.issue.id);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex-1 min-h-0 flex">
      <aside className="w-[34rem] border-r border-border flex flex-col min-h-0">
        <div className="p-3 border-b border-border space-y-2">
          <div className="flex items-center gap-2">
            <div className="text-sm font-semibold text-text flex-1">Issues</div>
            <span className="text-[11px] text-text-dim">{issues.length}</span>
          </div>
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search issues…"
              className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <button
              type="button"
              onClick={() => setShowCreate(true)}
              disabled={repos.length === 0}
              className="px-2 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
            >New</button>
          </div>
          <div className="grid grid-cols-3 gap-1">
            <select value={repoFilter} onChange={(e) => setRepoFilter(e.target.value)} className="bg-bg-input border border-border rounded px-1 py-0.5 text-xs">
              <option value="">all repos</option>
              {repos.map((r) => <option key={r.slug} value={r.slug}>{r.slug}</option>)}
            </select>
            <select value={state} onChange={(e) => setState(e.target.value)} className="bg-bg-input border border-border rounded px-1 py-0.5 text-xs">
              <option value="open">open</option>
              <option value="closed">closed</option>
              <option value="all">all</option>
            </select>
            <select value={status} onChange={(e) => setStatus(e.target.value)} className="bg-bg-input border border-border rounded px-1 py-0.5 text-xs">
              <option value="all">all workflow</option>
              {ISSUE_STATUSES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </div>
        </div>
        <div className="flex-1 overflow-auto">
          {err && <div className="p-3 text-red text-xs">{err}</div>}
          {issues.length === 0 ? (
            <div className="p-4 text-text-muted text-sm">No issues match this view.</div>
          ) : (
            <ul>
              {issues.map((iss) => (
                <li key={iss.id}>
                  <button
                    type="button"
                    onClick={() => setSelected(iss.id)}
                    className={`w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input/50 ${
                      selected === iss.id ? "bg-bg-input" : ""
                    }`}
                  >
                    <div className="text-sm text-text font-medium truncate">
                      {iss.title}
                    </div>
                    <div className="mt-0.5 text-[11px] text-text-dim font-mono truncate">
                      {iss.repo_slug} #{iss.number}
                    </div>
                    <div className="mt-1 flex items-center gap-1 text-[10px] text-text-dim">
                      <IssuePill label={iss.type} tone={iss.type} />
                      <IssuePill label={iss.status} tone={iss.status} />
                      {iss.state === "closed" ? <IssuePill label="closed" tone="closed" /> : null}
                      {iss.comments_count ? <span>{iss.comments_count} comments</span> : null}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </aside>

      <main className="flex-1 min-w-0 overflow-auto">
        {!detail ? (
          <div className="p-8 text-text-muted text-sm text-center">Select an issue.</div>
        ) : (
          <div className="p-4">
            <div className="max-w-5xl space-y-4">
              <div className="border-b border-border pb-3">
                <div className="flex items-start gap-3">
                  <div className="flex-1 min-w-0">
                    <div className="text-xs text-text-dim font-mono">{detail.issue.repo_slug} #{detail.issue.number}</div>
                    <input
                      value={detail.issue.title}
                      onChange={(e) => setDetail((cur) => cur ? { ...cur, issue: { ...cur.issue, title: e.target.value } } : cur)}
                      onBlur={(e) => patchIssue({ title: e.target.value })}
                      className="mt-1 w-full bg-transparent text-text text-lg font-semibold outline-none border-b border-transparent focus:border-border"
                    />
                    <IssueMetaSummary issue={detail.issue} />
                  </div>
                  <button
                    type="button"
                    onClick={() => setShowMetaEdit(true)}
                    disabled={busy}
                    className="px-3 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
                  >Edit</button>
                  <button
                    type="button"
                    onClick={closeOrReopen}
                    disabled={busy}
                    className="px-3 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
                  >{detail.issue.state === "closed" ? "Reopen" : "Close"}</button>
                </div>
              </div>

              <section>
                <textarea
                  value={detail.issue.body}
                  onChange={(e) => setDetail((cur) => cur ? { ...cur, issue: { ...cur.issue, body: e.target.value } } : cur)}
                  onBlur={(e) => patchIssue({ body: e.target.value })}
                  placeholder="Describe the issue…"
                  className="w-full min-h-32 bg-bg-input border border-border rounded p-3 text-sm text-text outline-none resize-y"
                />
              </section>

              <section className="space-y-2">
                <div className="flex items-center gap-2">
                  <h3 className="text-xs uppercase tracking-wide text-text-dim">Links</h3>
                  <span className="flex-1" />
                  <button
                    type="button"
                    onClick={linkCurrentPath}
                    disabled={!currentPath || detail.issue.repo_slug !== currentRepoSlug || busy}
                    className="px-2 py-0.5 text-xs border border-border rounded text-text-muted hover:text-text disabled:opacity-40"
                  >Link current file</button>
                </div>
                {detail.links.length === 0 ? (
                  <div className="text-xs text-text-muted">No code links yet.</div>
                ) : (
                  <ul className="space-y-1">
                    {detail.links.map((l) => (
                      <li key={l.id} className="text-xs flex items-center gap-2">
                        <span className="text-text-dim">{l.kind}</span>
                        {l.kind === "path" ? (
                          <button type="button" onClick={() => onOpenPath(detail.issue.repo_slug || "", l.target.split(":")[0])} className="font-mono text-accent hover:underline truncate">
                            {l.target}
                          </button>
                        ) : (
                          <span className="font-mono text-text truncate">{l.target}</span>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </section>

              <section className="space-y-2">
                <h3 className="text-xs uppercase tracking-wide text-text-dim">Comments</h3>
                {detail.comments.map((c) => (
                  <div key={c.id} className="border border-border rounded p-3">
                    <div className="text-[11px] text-text-dim">{c.author || "comment"} · {shortDate(c.created_at)}</div>
                    <div className="mt-1 text-sm text-text whitespace-pre-wrap">{c.body}</div>
                  </div>
                ))}
                <textarea
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                  placeholder="Add a comment…"
                  className="w-full min-h-20 bg-bg-input border border-border rounded p-2 text-sm text-text outline-none resize-y"
                />
                <div className="flex justify-end">
                  <button
                    type="button"
                    onClick={addComment}
                    disabled={busy || !comment.trim()}
                    className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
                  >Comment</button>
                </div>
              </section>
            </div>
          </div>
        )}
      </main>

      {showCreate && (
        <CreateIssueDialog
          repos={repos}
          defaultRepoSlug={currentRepoSlug}
          currentPath={currentPath}
          currentRepoSlug={currentRepoSlug}
          api={api}
          onClose={() => setShowCreate(false)}
          onCreated={(issue) => {
            setShowCreate(false);
            setSelected(issue.id);
            loadIssues();
          }}
        />
      )}

      {showMetaEdit && detail && (
        <IssueMetaDialog
          issue={detail.issue}
          busy={busy}
          onClose={() => setShowMetaEdit(false)}
          onPatch={patchIssue}
        />
      )}
    </div>
  );
}

function IssueSelect({ label, value, options, onChange }: { label: string; value: string; options: readonly string[]; onChange: (v: string) => void }) {
  return (
    <div>
      <label className="text-[11px] text-text-muted block mb-1">{label}</label>
      <select value={value} onChange={(e) => onChange(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs">
        {options.map((o) => <option key={o} value={o}>{o}</option>)}
      </select>
    </div>
  );
}

function IssueMetaSummary({ issue }: { issue: CodeIssue }) {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-1 text-[10px] text-text-dim">
      <IssuePill label={issue.type} tone={issue.type} />
      <IssuePill label={issue.status} tone={issue.status} />
      <IssuePill label={issue.priority} tone={issue.priority} />
      <IssuePill label={issue.state} tone={issue.state} />
      {issue.state === "closed" && issue.state_reason ? <IssuePill label={issue.state_reason} tone={issue.state_reason} /> : null}
      {issue.assignee ? <span>assigned to {issue.assignee}</span> : null}
    </div>
  );
}

function IssueMetaDialog({
  issue,
  busy,
  onClose,
  onPatch,
}: {
  issue: CodeIssue;
  busy: boolean;
  onClose: () => void;
  onPatch: (patch: Partial<CodeIssue>) => void;
}) {
  const [draft, setDraft] = useState({
    status: issue.status,
    state: issue.state,
    state_reason: issue.state_reason || "completed",
    type: issue.type,
    priority: issue.priority,
    assignee: issue.assignee || "",
  });

  const save = () => {
    onPatch({
      status: draft.status,
      state: draft.state,
      state_reason: draft.state === "closed" ? draft.state_reason : "",
      type: draft.type,
      priority: draft.priority,
      assignee: draft.assignee,
    });
    onClose();
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Edit issue fields"
        onClick={(e) => e.stopPropagation()}
        className="w-[560px] max-w-[calc(100vw-2rem)] bg-bg border border-border rounded p-5 space-y-4"
      >
        <div className="flex items-center gap-3">
          <h2 className="text-text font-semibold flex-1">Edit issue fields</h2>
          <button type="button" onClick={onClose} className="px-2 py-0.5 text-xs border border-border rounded text-text-muted hover:text-text">Cancel</button>
        </div>
        <IssueMetaSection label="Workflow">
          <IssueChipGroup value={draft.status} options={ISSUE_STATUSES} disabled={busy} onChange={(v) => setDraft((d) => ({ ...d, status: v as IssueStatus }))} />
        </IssueMetaSection>
        <IssueMetaSection label="State">
          <IssueChipGroup value={draft.state} options={ISSUE_STATES} disabled={busy} onChange={(v) => setDraft((d) => ({ ...d, state: v as IssueState }))} />
        </IssueMetaSection>
        {draft.state === "closed" && (
          <IssueMetaSection label="Reason">
            <IssueChipGroup value={draft.state_reason} options={ISSUE_STATE_REASONS} disabled={busy} onChange={(v) => setDraft((d) => ({ ...d, state_reason: v as IssueStateReason }))} />
          </IssueMetaSection>
        )}
        <IssueMetaSection label="Type">
          <IssueChipGroup value={draft.type} options={ISSUE_TYPES} disabled={busy} onChange={(v) => setDraft((d) => ({ ...d, type: v as IssueKind }))} />
        </IssueMetaSection>
        <IssueMetaSection label="Priority">
          <IssueChipGroup value={draft.priority} options={ISSUE_PRIORITIES} disabled={busy} onChange={(v) => setDraft((d) => ({ ...d, priority: v as IssuePriority }))} />
        </IssueMetaSection>
        <IssueMetaSection label="Assignee">
          <input
            value={draft.assignee}
            onChange={(e) => setDraft((d) => ({ ...d, assignee: e.target.value }))}
            placeholder="unassigned"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs"
          />
        </IssueMetaSection>
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50">Cancel</button>
          <button type="button" onClick={save} disabled={busy} className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50">Save</button>
        </div>
      </div>
    </div>
  );
}

function IssueMetaSection({ label, children }: { label: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="text-[11px] uppercase tracking-wide text-text-dim mb-1.5">{label}</h3>
      {children}
    </section>
  );
}

function IssueChipGroup({ value, options, disabled, onChange }: { value: string; options: readonly string[]; disabled?: boolean; onChange: (v: string) => void }) {
  return (
    <div className="flex flex-wrap gap-1">
      {options.map((option) => (
        <button
          key={option}
          type="button"
          disabled={disabled}
          onClick={() => {
            if (option !== value) onChange(option);
          }}
          className={`px-2 py-0.5 text-xs rounded border ${
            option === value
              ? "border-accent text-accent bg-accent/10"
              : "border-border text-text-muted hover:text-text hover:border-accent/60"
          } disabled:opacity-60`}
        >{option}</button>
      ))}
    </div>
  );
}

function IssuePill({ label, tone }: { label: string; tone: string }) {
  const cls = tone === "bug" || tone === "urgent" || tone === "blocked" || tone === "not_planned"
    ? "border-red/50 text-red/80"
    : tone === "feature" || tone === "planned" || tone === "in_review" || tone === "high"
      ? "border-blue/50 text-blue/80"
      : tone === "done" || tone === "closed" || tone === "completed"
        ? "border-green/50 text-green/80"
        : "border-border text-text-muted";
  return <span className={`px-1 py-0.5 rounded border ${cls}`}>{label}</span>;
}

function shortDate(s?: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function CreateIssueDialog({
  repos,
  defaultRepoSlug,
  currentPath,
  currentRepoSlug,
  api,
  onClose,
  onCreated,
}: {
  repos: Repo[];
  defaultRepoSlug?: string;
  currentPath?: string;
  currentRepoSlug?: string;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  onClose: () => void;
  onCreated: (issue: CodeIssue) => void;
}) {
  const initialSlug = defaultRepoSlug && repos.some((r) => r.slug === defaultRepoSlug)
    ? defaultRepoSlug
    : repos[0]?.slug ?? "";
  const [slug, setSlug] = useState(initialSlug);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [kind, setKind] = useState<IssueKind>("bug");
  const [priority, setPriority] = useState<IssuePriority>("medium");
  const [assignee, setAssignee] = useState("");
  const [linkPath, setLinkPath] = useState(Boolean(currentPath && initialSlug === currentRepoSlug));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) {
      setErr("title required");
      return;
    }
    if (!slug) {
      setErr("repository required");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ issue: CodeIssue }>("POST", `/repos/${slug}/issues`, {
        title: title.trim(),
        body,
        type: kind,
        priority,
        assignee: assignee.trim(),
        created_by: "human",
      });
      if (linkPath && currentPath && slug === currentRepoSlug) {
        await api("POST", `/repos/${slug}/issues/${r.issue.number}/links/path`, {
          path: currentPath,
          actor: "human",
        });
      }
      onCreated(r.issue);
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
        className="w-[560px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">New issue</h2>
        <div>
          <label className="text-xs text-text-muted block mb-1">Repository</label>
          <select
            value={slug}
            onChange={(e) => {
              setSlug(e.target.value);
              setLinkPath(Boolean(currentPath && e.target.value === currentRepoSlug));
            }}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            {repos.map((r) => <option key={r.slug} value={r.slug}>{r.slug}</option>)}
          </select>
        </div>
        <div>
          <label className="text-xs text-text-muted block mb-1">Title</label>
          <input
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </div>
        <div className="grid grid-cols-3 gap-2">
          <IssueSelect label="Type" value={kind} options={ISSUE_TYPES} onChange={(v) => setKind(v as IssueKind)} />
          <IssueSelect label="Priority" value={priority} options={ISSUE_PRIORITIES} onChange={(v) => setPriority(v as IssuePriority)} />
          <div>
            <label className="text-[11px] text-text-muted block mb-1">Assignee</label>
            <input value={assignee} onChange={(e) => setAssignee(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs" />
          </div>
        </div>
        <div>
          <label className="text-xs text-text-muted block mb-1">Body</label>
          <textarea value={body} onChange={(e) => setBody(e.target.value)} className="w-full min-h-32 bg-bg-input border border-border rounded p-2 text-sm resize-y" />
        </div>
        {currentPath && slug === currentRepoSlug && (
          <label className="text-xs text-text-muted flex items-center gap-2">
            <input type="checkbox" checked={linkPath} onChange={(e) => setLinkPath(e.target.checked)} />
            link <span className="font-mono">{currentPath}</span>
          </label>
        )}
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50">Cancel</button>
          <button type="submit" disabled={busy} className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50">{busy ? "Creating…" : "Create"}</button>
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
  // Remote-runner fields — populated for mobile repos delegated to the
  // Simulator app. runner="" for local web dev runs.
  runner?: string;
  sim_id?: string;
  stream_url?: string;
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
  onRunChange,
}: {
  slug: string;
  api: <T,>(m: string, p: string, b?: unknown, e?: Record<string, string>) => Promise<T>;
  withParams: (extra?: Record<string, string>) => string;
  showLogs: boolean;
  onToggleLogs: () => void;
  onError: (msg: string) => void;
  onRunChange?: (run: DevRunWire | null) => void;
}) {
  const [run, setRun] = useState<DevRunWire | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const r = await api<{ dev_run: DevRunWire | null }>("GET", `/repos/${slug}/dev/status`);
      setRun(r.dev_run);
      onRunChange?.(r.dev_run);
    } catch (e) {
      // Swallow — this polls in the background; the panel-wide error
      // banner is for explicit user actions.
    }
  }, [api, slug, onRunChange]);

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
  const isRemote = run?.runner === "simulator";

  return (
    <div className="px-3 py-2 border-b border-border flex items-center gap-2 bg-bg-input/40">
      <span className={`text-xs ${devStatusColor(status)}`}>●</span>
      <span className="text-xs text-text-muted">
        {status === "live" && isRemote ? (
          <>
            Running on {run?.framework === "ios" ? "iOS Simulator" : "Android emulator"}
            {" "}· {uptimeStr(run?.started_at)}
          </>
        ) : status === "live" ? (
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
          <>Dev preview stopped · deploy production from the Deploy app</>
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

// RemoteDeviceView renders the live mobile device for a Simulator-app
// dev run. Mounted by the panel body below the DevBar when the active
// dev run has runner=simulator and a stream URL.
function RemoteDeviceView({ run }: { run: DevRunWire }) {
  if (!run.stream_url) {
    return (
      <div className="p-4 text-xs text-text-muted">
        Device starting — waiting for the stream URL…
      </div>
    );
  }
  return (
    <div className="p-4 flex justify-center">
      <DeviceFrame streamUrl={run.stream_url} platform={run.framework} />
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
// files render with compact SVG icons. Indentation by depth, hover
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
          <TreeIcon name={open ? "folderOpen" : "folder"} className={open ? "text-yellow/80" : "text-yellow/60"} />
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
          <TreeIcon name={glyph.icon} className={glyph.cls} />
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
            ><TreeIcon name="pencil" className="w-3.5 h-3.5" /></button>
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onDelete(node.path); }}
              className="text-red/70 hover:text-red px-1"
              title="Delete"
            ><TreeIcon name="trash" className="w-3.5 h-3.5" /></button>
          </span>
        </div>
      )}
    </li>
  );
}

function TreeIcon({ name, className = "" }: { name: TreeIconName; className?: string }) {
  const common = {
    width: 16,
    height: 16,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
    className: `w-4 h-4 shrink-0 ${className}`,
  };
  switch (name) {
    case "folder":
      return (
        <svg {...common}>
          <path d="M3.5 6.5h6l2 2h9v9.5a2 2 0 0 1-2 2h-15a2 2 0 0 1-2-2v-9.5a2 2 0 0 1 2-2Z" />
        </svg>
      );
    case "folderOpen":
      return (
        <svg {...common}>
          <path d="M3.5 7h5.8l2 2h8.2a2 2 0 0 1 2 2v1.2" />
          <path d="M3 11.5h18.5l-2 6.5a2.4 2.4 0 0 1-2.3 1.7h-13a2 2 0 0 1-2-2.4l1.2-5.8Z" />
        </svg>
      );
    case "code":
      return (
        <svg {...common}>
          <path d="m9 8-4 4 4 4" />
          <path d="m15 8 4 4-4 4" />
        </svg>
      );
    case "image":
      return (
        <svg {...common}>
          <rect x="4" y="5" width="16" height="14" rx="2" />
          <path d="m7 16 4-4 3 3 2-2 3 3" />
          <circle cx="8.5" cy="9" r="1" />
        </svg>
      );
    case "lock":
      return (
        <svg {...common}>
          <rect x="5" y="10" width="14" height="10" rx="2" />
          <path d="M8 10V8a4 4 0 0 1 8 0v2" />
        </svg>
      );
    case "box":
      return (
        <svg {...common}>
          <path d="M12 3.5 20 8l-8 4.5L4 8l8-4.5Z" />
          <path d="M4 8v8l8 4.5 8-4.5V8" />
          <path d="M12 12.5v8" />
        </svg>
      );
    case "config":
      return (
        <svg {...common}>
          <path d="M5 7h14" />
          <path d="M5 12h14" />
          <path d="M5 17h14" />
          <circle cx="9" cy="7" r="1.5" />
          <circle cx="15" cy="12" r="1.5" />
          <circle cx="11" cy="17" r="1.5" />
        </svg>
      );
    case "text":
      return (
        <svg {...common}>
          <path d="M6 4.5h8l4 4V19a1.5 1.5 0 0 1-1.5 1.5h-10A1.5 1.5 0 0 1 5 19V6a1.5 1.5 0 0 1 1-1.5Z" />
          <path d="M14 4.5V9h4" />
          <path d="M8 13h8" />
          <path d="M8 16h6" />
        </svg>
      );
    case "key":
      return (
        <svg {...common}>
          <circle cx="8" cy="12" r="3" />
          <path d="M11 12h9" />
          <path d="M16 12v3" />
          <path d="M19 12v2" />
        </svg>
      );
    case "pencil":
      return (
        <svg {...common}>
          <path d="M5 19.5 6.2 15 16.8 4.4a2 2 0 0 1 2.8 2.8L9 17.8 5 19.5Z" />
          <path d="m14.5 6.5 3 3" />
        </svg>
      );
    case "trash":
      return (
        <svg {...common}>
          <path d="M5 7h14" />
          <path d="M10 11v6" />
          <path d="M14 11v6" />
          <path d="M8 7l1-3h6l1 3" />
          <path d="M7 7l1 13h8l1-13" />
        </svg>
      );
    case "file":
    default:
      return (
        <svg {...common}>
          <path d="M6.5 4.5h7l4 4V19a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 5.5 19V6A1.5 1.5 0 0 1 6.5 4.5Z" />
          <path d="M13.5 4.5V9h4" />
        </svg>
      );
  }
}
