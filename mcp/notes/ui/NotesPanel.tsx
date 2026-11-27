import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Note {
  id: number;
  title: string;
  body: string;
  kind: string;
  status: string;
  source: string;
  tags: string[];
  metadata: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  archived_at?: string;
  folder_id?: number;
}

interface Folder {
  id: number;
  name: string;
  note_count: number;
}

// Sidebar selection: "all", "archived", or "f:<folder id>".
type FolderKey = string;
const folderKey = (id: number) => `f:${id}`;
const folderIdOf = (key: FolderKey) => (key.startsWith("f:") ? Number(key.slice(2)) : 0);

// What the editor is showing. `token` identifies one editing session so a
// slow POST for a new note still lands on the right draft after the user has
// moved on to another note.
interface Draft {
  token: number;
  id: number;
  title: string;
  body: string;
  folderId: number;
  kind: string;
  tags: string;
  source: string;
  archived: boolean;
  createdAt: string;
  updatedAt: string;
}

type SaveState = "idle" | "pending" | "saving" | "saved" | "error";

const API = "/api/apps/notes";
const KINDS = ["note", "customer_idea", "meeting_note", "research", "decision", "follow_up", "spec_draft"];

// Panel widths (container, not viewport: the panel can be narrow on desktop too).
const NARROW = 720;
const WITH_FOLDERS = 1100;
const AUTOSAVE_MS = 700;

// The dashboard's runtime theme variables are the short names (--bg, --accent…);
// Tailwind's --color-* aliases are inlined at build time and don't exist at runtime.
const C = {
  bg: "var(--bg, #0a0a0a)",
  card: "var(--bg-card, #141414)",
  subtle: "var(--bg-subtle, #101010)",
  hover: "var(--bg-hover, #1c1c1c)",
  input: "var(--bg-input, #111)",
  border: "var(--border, #2a2a2a)",
  text: "var(--text, #e8e8e8)",
  muted: "var(--text-muted, #aaa)",
  dim: "var(--text-dim, #888)",
  accent: "var(--accent, #f97316)",
  selected: "color-mix(in srgb, var(--accent, #f97316) 22%, transparent)",
  error: "var(--error, #ef4444)",
};

const emptyDraft = (token: number, folderId = 0): Draft => ({
  token,
  id: 0,
  title: "",
  body: "",
  folderId,
  kind: "note",
  tags: "",
  source: "manual",
  archived: false,
  createdAt: "",
  updatedAt: "",
});

export default function NotesPanel({ projectId }: NativePanelProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLTextAreaElement>(null);
  const bodyRef = useRef<HTMLTextAreaElement>(null);

  const [width, setWidth] = useState(1200);
  const [folder, setFolder] = useState<FolderKey>("all");
  const [folders, setFolders] = useState<Folder[]>([]);
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeNotes, setActiveNotes] = useState<Note[]>([]);
  const [archivedNotes, setArchivedNotes] = useState<Note[]>([]);
  const [searchResults, setSearchResults] = useState<Note[] | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saveState, setSaveState] = useState<SaveState>("idle");
  const [saveError, setSaveError] = useState("");
  const [mobileView, setMobileView] = useState<"list" | "editor">("list");
  const [showInfo, setShowInfo] = useState(false);

  // Save bookkeeping lives in refs so the debounce timer and the save chain
  // always see the latest values.
  const draftRef = useRef<Draft | null>(null);
  const dirtyRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const chainRef = useRef<Promise<void>>(Promise.resolve());
  const tokenIdsRef = useRef(new Map<number, number>());
  const nextTokenRef = useRef(1);

  const narrow = width < NARROW;
  const showFolders = width >= WITH_FOLDERS;

  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    setWidth(el.clientWidth || 1200);
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) setWidth(w);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const url = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const fetchNotes = useCallback(async (params: Record<string, string>) => {
    const qs = new URLSearchParams({ limit: "500", ...params });
    const res = await fetch(url(`/notes?${qs.toString()}`), { credentials: "same-origin" });
    if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
    const data = await res.json();
    return (data.notes ?? []) as Note[];
  }, [url]);

  const loadActive = useCallback(async () => {
    try {
      setActiveNotes(await fetchNotes({ status: "active" }));
      setLoadError("");
    } catch (e) {
      setLoadError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }, [fetchNotes]);

  const loadArchived = useCallback(async () => {
    try {
      setArchivedNotes(await fetchNotes({ status: "archived" }));
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, [fetchNotes]);

  const loadFolders = useCallback(async () => {
    try {
      const res = await fetch(url("/folders"), { credentials: "same-origin" });
      if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
      const data = await res.json();
      setFolders(data.folders ?? []);
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, [url]);

  useEffect(() => { loadActive(); loadFolders(); loadArchived(); }, [loadActive, loadFolders, loadArchived]);
  useEffect(() => { if (folder === "archived") loadArchived(); }, [folder, loadArchived]);

  // Server-side search (title, body, tags) once the user types a query.
  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setSearchResults(null);
      return;
    }
    const handle = setTimeout(async () => {
      const params: Record<string, string> = { q, status: folder === "archived" ? "archived" : "active" };
      if (folderIdOf(folder)) params.folder_id = String(folderIdOf(folder));
      try {
        setSearchResults(await fetchNotes(params));
      } catch (e) {
        setLoadError((e as Error).message);
      }
    }, 250);
    return () => clearTimeout(handle);
  }, [query, folder, fetchNotes]);

  const visibleNotes = useMemo(() => {
    if (searchResults) return searchResults;
    if (folder === "archived") return archivedNotes;
    if (folder === "all") return activeNotes;
    const fid = folderIdOf(folder);
    return activeNotes.filter((n) => (n.folder_id ?? 0) === fid);
  }, [searchResults, folder, archivedNotes, activeNotes]);

  const counts = useMemo(() => {
    const out: Record<string, number> = { all: activeNotes.length };
    for (const n of activeNotes) {
      const k = folderKey(n.folder_id ?? 0);
      out[k] = (out[k] ?? 0) + 1;
    }
    return out;
  }, [activeNotes]);

  // --- saving -----------------------------------------------------------------

  const upsertLocal = useCallback((note: Note) => {
    const put = (list: Note[]) => [note, ...list.filter((n) => n.id !== note.id)];
    setActiveNotes(put);
    setSearchResults((list) => (list ? list.map((n) => (n.id === note.id ? note : n)) : list));
  }, []);

  const doSave = useCallback(async (snap: Draft) => {
    const id = tokenIdsRef.current.get(snap.token) ?? snap.id;
    const title = deriveTitle(snap.title, snap.body);
    if (!id && !snap.title.trim() && !snap.body.trim()) return; // nothing to create yet
    const payload = {
      title,
      body: snap.body,
      folder_id: snap.folderId || 0,
      kind: snap.kind || "note",
      source: snap.source || "manual",
      tags: splitTags(snap.tags),
    };
    setSaveState("saving");
    const res = await fetch(url(id ? `/notes/${id}` : "/notes"), {
      method: id ? "PATCH" : "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
    const data = await res.json();
    const note = data.note as Note | undefined;
    if (!note) return;
    tokenIdsRef.current.set(snap.token, note.id);
    upsertLocal(note);
    // Stamp the id onto the open draft without touching what the user typed since.
    setDraft((d) => (d && d.token === snap.token ? { ...d, id: note.id, updatedAt: note.updated_at, createdAt: d.createdAt || note.created_at } : d));
    if (!dirtyRef.current) setSaveState("saved");
  }, [url, upsertLocal]);

  const flush = useCallback(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    if (dirtyRef.current && draftRef.current && !draftRef.current.archived) {
      dirtyRef.current = false;
      const snap = { ...draftRef.current };
      chainRef.current = chainRef.current
        .then(() => doSave(snap))
        .then(() => setSaveError(""))
        .catch((e) => {
          setSaveState("error");
          setSaveError((e as Error).message);
        });
    }
    return chainRef.current;
  }, [doSave]);

  // Save when the tab is hidden or the panel unmounts.
  useEffect(() => {
    const onHide = () => { if (document.visibilityState === "hidden") flush(); };
    document.addEventListener("visibilitychange", onHide);
    return () => {
      document.removeEventListener("visibilitychange", onHide);
      flush();
    };
  }, [flush]);

  const edit = (patch: Partial<Draft>) => {
    setDraft((d) => {
      if (!d || d.archived) return d;
      const next = { ...d, ...patch };
      draftRef.current = next;
      return next;
    });
    dirtyRef.current = true;
    setSaveState("pending");
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(flush, AUTOSAVE_MS);
  };

  // --- navigation -------------------------------------------------------------

  const openNote = useCallback((note: Note) => {
    flush();
    const token = nextTokenRef.current++;
    tokenIdsRef.current.set(token, note.id);
    const next: Draft = {
      token,
      id: note.id,
      title: note.title,
      body: note.body,
      folderId: note.folder_id ?? 0,
      kind: note.kind || "note",
      tags: Array.isArray(note.tags) ? note.tags.join(", ") : "",
      source: note.source || "manual",
      archived: note.status === "archived",
      createdAt: note.created_at,
      updatedAt: note.updated_at,
    };
    draftRef.current = next;
    setDraft(next);
    setSaveState("idle");
    setShowInfo(false);
    setMobileView("editor");
  }, [flush]);

  const newNote = useCallback(() => {
    flush();
    const fid = folderIdOf(folder);
    if (folder === "archived") setFolder("all");
    setQuery("");
    const next = emptyDraft(nextTokenRef.current++, fid);
    draftRef.current = next;
    setDraft(next);
    setSaveState("idle");
    setShowInfo(false);
    setMobileView("editor");
    requestAnimationFrame(() => titleRef.current?.focus());
  }, [flush, folder]);

  const backToList = () => {
    flush();
    setMobileView("list");
  };

  const archiveCurrent = async () => {
    const current = draftRef.current;
    if (!current || current.archived) return;
    if (timerRef.current) clearTimeout(timerRef.current);
    await flush();
    const id = tokenIdsRef.current.get(current.token) ?? current.id;
    const remaining = visibleNotes.filter((n) => n.id !== id);
    if (id) {
      try {
        const res = await fetch(url(`/notes/${id}/archive`), { method: "POST", credentials: "same-origin" });
        if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
      } catch (e) {
        setSaveState("error");
        setSaveError((e as Error).message);
        return;
      }
      setActiveNotes((list) => list.filter((n) => n.id !== id));
      setSearchResults((list) => (list ? list.filter((n) => n.id !== id) : list));
      loadArchived();
    }
    draftRef.current = null;
    setDraft(null);
    if (!narrow && remaining[0]) openNote(remaining[0]);
    else setMobileView("list");
  };

  // Wide layout: keep something open, like Notes on macOS.
  useEffect(() => {
    if (narrow || draft || !loaded) return;
    if (visibleNotes[0]) openNote(visibleNotes[0]);
  }, [narrow, draft, loaded, visibleNotes, openNote]);

  // Grow the body textarea with its content so the whole note scrolls as one page.
  useLayoutEffect(() => {
    for (const el of [titleRef.current, bodyRef.current]) {
      if (!el) continue;
      el.style.height = "auto";
      el.style.height = `${el.scrollHeight}px`;
    }
  }, [draft?.token, draft?.title, draft?.body, width, mobileView]);

  // Cmd/Ctrl+Alt+N for a new note.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.altKey && e.key.toLowerCase() === "n") {
        e.preventDefault();
        newNote();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [newNote]);

  const chooseFolder = (f: FolderKey) => {
    if (f === "new") {
      setDialog({ mode: "create" });
      return;
    }
    flush();
    setMenuOpen(false);
    setFolder(f);
    setQuery("");
    if (!narrow) {
      draftRef.current = null;
      setDraft(null);
    }
  };

  // --- folders ----------------------------------------------------------------

  const folderName = (key: FolderKey) => {
    if (key === "all") return "All Notes";
    if (key === "archived") return "Archived";
    return folders.find((f) => f.id === folderIdOf(key))?.name ?? "Folder";
  };

  const folderRequest = async (path: string, method: string, body?: unknown) => {
    const res = await fetch(url(path), {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
    return res.json();
  };

  const submitDialog = async (value: string) => {
    if (!dialog) return;
    if (dialog.mode === "create") {
      const data = await folderRequest("/folders", "POST", { name: value });
      await loadFolders();
      setDialog(null);
      if (data.folder?.id) chooseFolder(folderKey(data.folder.id));
    } else if (dialog.mode === "rename") {
      await folderRequest(`/folders/${dialog.folder.id}`, "PATCH", { name: value });
      await loadFolders();
      setDialog(null);
    } else {
      await flush();
      await folderRequest(`/folders/${dialog.folder.id}`, "DELETE");
      const gone = dialog.folder.id;
      const unfile = (list: Note[]) => list.map((n) => ((n.folder_id ?? 0) === gone ? { ...n, folder_id: 0 } : n));
      setActiveNotes(unfile);
      setArchivedNotes(unfile);
      setDraft((d) => (d && d.folderId === gone ? { ...d, folderId: 0 } : d));
      if (draftRef.current?.folderId === gone) draftRef.current = { ...draftRef.current, folderId: 0 };
      await loadFolders();
      setDialog(null);
      chooseFolder("all");
    }
  };

  const currentFolder = folders.find((f) => f.id === folderIdOf(folder)) ?? null;
  const showArchived = archivedNotes.length > 0 || folder === "archived";
  const folderKeys: FolderKey[] = ["all", ...folders.map((f) => folderKey(f.id)), ...(showArchived ? ["archived"] : [])];

  // --- render -----------------------------------------------------------------
  // Styling follows the other Apteva app panels (Conversations): the
  // dashboard's own font, a title + subtitle header, a filled accent
  // "+ New" button, muted icon buttons, divided rows with an accent bar on
  // the selected one.

  const inputStyle: CSSProperties = {
    background: C.input,
    color: C.text,
    border: `1px solid ${C.border}`,
    borderRadius: 4,
    padding: "6px 8px",
    fontSize: narrow ? 16 : 12,
    outline: "none",
    font: "inherit",
    boxSizing: "border-box",
  };

  const folderMenu = currentFolder && (
    <div style={{ position: "relative" }}>
      <IconButton label="Folder options" onClick={() => setMenuOpen((v) => !v)} active={menuOpen}><MoreIcon /></IconButton>
      {menuOpen && (
        <div style={{ position: "absolute", right: 0, top: "100%", marginTop: 6, zIndex: 20, minWidth: 180, background: C.card, border: `1px solid ${C.border}`, borderRadius: 6, padding: 4, boxShadow: "0 10px 30px rgba(0,0,0,0.4)" }}>
          <MenuItem onClick={() => { setMenuOpen(false); setDialog({ mode: "rename", folder: currentFolder }); }}>Rename folder</MenuItem>
          <MenuItem danger onClick={() => { setMenuOpen(false); setDialog({ mode: "delete", folder: currentFolder }); }}>Delete folder</MenuItem>
        </div>
      )}
    </div>
  );

  const folderList = (
    <nav style={{ width: 210, flexShrink: 0, borderRight: `1px solid ${C.border}`, display: "flex", flexDirection: "column", minHeight: 0 }}>
      <div style={{ ...barStyle, justifyContent: "space-between" }}>
        <span style={{ fontSize: 11, color: C.dim, textTransform: "uppercase", letterSpacing: 0.6 }}>Folders</span>
        <IconButton label="New folder" onClick={() => setDialog({ mode: "create" })}><PlusIcon size={15} /></IconButton>
      </div>
      <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {folderKeys.map((f) => (
          <ListButton key={f} selected={f === folder} onClick={() => chooseFolder(f)} padding="9px 14px">
            <span style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13 }}>
              <span style={{ color: f === folder ? C.accent : C.dim, display: "inline-flex" }}>
                {f === "archived" ? <ArchiveIcon size={14} /> : <FolderIcon size={14} />}
              </span>
              <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", color: C.text }}>{folderName(f)}</span>
              {f !== "archived" && <span style={{ color: C.dim, fontSize: 12 }}>{counts[f] ?? 0}</span>}
            </span>
          </ListButton>
        ))}
        {folders.length === 0 && (
          <div style={{ padding: "10px 14px", fontSize: 12, color: C.dim, lineHeight: 1.5 }}>
            No folders yet.{" "}
            <button type="button" onClick={() => setDialog({ mode: "create" })} style={{ ...resetButton, color: C.accent }}>Create one</button>
          </div>
        )}
      </div>
    </nav>
  );

  const groups = groupByDate(visibleNotes);

  const list = (
    <section style={{ display: "flex", flexDirection: "column", minHeight: 0, minWidth: 0, width: narrow ? "100%" : 300, flexShrink: 0, borderRight: narrow ? "none" : `1px solid ${C.border}` }}>
      <div style={{ ...barStyle, gap: 8 }}>
        {!narrow && <AccentButton onClick={newNote}><PlusIcon size={13} /> New note</AccentButton>}
        {!showFolders && (
          <select
            aria-label="Folder"
            value={folder}
            onChange={(e) => chooseFolder(e.target.value)}
            style={{ ...inputStyle, flex: 1, minWidth: 0, padding: "5px 6px", cursor: "pointer" }}
          >
            {folderKeys.map((f) => (
              <option key={f} value={f}>{folderName(f)}{f !== "archived" ? ` (${counts[f] ?? 0})` : ""}</option>
            ))}
            <option value="new">+ New folder…</option>
          </select>
        )}
        {showFolders && <div style={{ flex: 1 }} />}
        {folderMenu}
      </div>
      <div style={{ flexShrink: 0, padding: "8px 12px", borderBottom: `1px solid ${C.border}`, position: "relative" }}>
        <span style={{ position: "absolute", left: 21, top: "50%", transform: "translateY(-50%)", color: C.dim, display: "inline-flex" }}><SearchIcon size={13} /></span>
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={folder === "all" ? "Search notes" : `Search ${folderName(folder)}`}
          type="search"
          style={{ ...inputStyle, width: "100%", paddingLeft: 28 }}
        />
      </div>

      <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {loadError && <div style={{ padding: "8px 16px", fontSize: 12, color: C.error }}>{loadError}</div>}
        {loaded && visibleNotes.length === 0 && <EmptyList query={query} folder={folder} onNew={newNote} />}
        {groups.map((group) => (
          <div key={group.label}>
            <div style={{ padding: "6px 16px", fontSize: 11, color: C.dim, textTransform: "uppercase", letterSpacing: 0.6, background: C.subtle, borderBottom: `1px solid ${C.border}` }}>
              {group.label}
            </div>
            {group.notes.map((note) => (
              <NoteRow key={note.id} note={note} selected={!narrow && draft?.id === note.id} onClick={() => openNote(note)} />
            ))}
          </div>
        ))}
      </div>
    </section>
  );

  const editor = (
    <section style={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column" }}>
      <div style={{ ...barStyle, gap: 4 }}>
        {narrow && (
          <button type="button" onClick={backToList} style={{ ...resetButton, display: "inline-flex", alignItems: "center", gap: 2, color: C.muted, fontSize: 13, padding: "6px 6px 6px 0", minHeight: 44 }}>
            <ChevronLeft size={18} /> {folderName(folder)}
          </button>
        )}
        {!narrow && draft && (
          <span style={{ fontSize: 12, color: C.dim, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {draft.updatedAt ? `Edited ${formatLong(draft.updatedAt)}` : "New note"}
            {draft.folderId ? ` · ${folders.find((f) => f.id === draft.folderId)?.name ?? ""}` : ""}
          </span>
        )}
        <div style={{ flex: 1 }} />
        {draft && <SaveBadge state={saveState} error={saveError} />}
        {draft && !draft.archived && (
          <IconButton label="Folder, kind and tags" onClick={() => setShowInfo((v) => !v)} active={showInfo}><TagIcon /></IconButton>
        )}
        {draft && !draft.archived && (
          <IconButton label="Archive note" onClick={archiveCurrent}><ArchiveIcon /></IconButton>
        )}
      </div>

      {draft ? (
        <div
          style={{ flex: 1, minHeight: 0, overflowY: "auto", cursor: "text" }}
          onClick={(e) => { if (e.target === e.currentTarget) bodyRef.current?.focus(); }}
        >
          <div
            style={{ padding: narrow ? "16px 16px 120px" : "20px 24px 160px" }}
            onClick={(e) => { if (e.target === e.currentTarget) bodyRef.current?.focus(); }}
          >
            {narrow && (
              <div style={{ fontSize: 12, color: C.dim, marginBottom: 12 }}>
                {draft.updatedAt ? `Edited ${formatLong(draft.updatedAt)}` : "New note"}
              </div>
            )}

            {draft.archived && (
              <div style={{ fontSize: 12, color: C.muted, background: C.card, border: `1px solid ${C.border}`, borderRadius: 6, padding: "8px 10px", marginBottom: 16 }}>
                This note is archived. Archived notes are read-only.
              </div>
            )}

            {showInfo && !draft.archived && (
              <div style={{ display: "flex", flexWrap: "wrap", gap: 12, alignItems: "flex-end", background: C.card, border: `1px solid ${C.border}`, borderRadius: 6, padding: 12, marginBottom: 20 }}>
                <FieldLabel label="Folder">
                  <select value={String(draft.folderId)} onChange={(e) => edit({ folderId: Number(e.target.value) })} style={inputStyle}>
                    <option value="0">No folder</option>
                    {folders.map((f) => <option key={f.id} value={String(f.id)}>{f.name}</option>)}
                  </select>
                </FieldLabel>
                <FieldLabel label="Kind">
                  <select value={draft.kind} onChange={(e) => edit({ kind: e.target.value })} style={inputStyle}>
                    {KINDS.map((k) => <option key={k} value={k}>{kindLabel(k)}</option>)}
                  </select>
                </FieldLabel>
                <FieldLabel label="Tags" grow>
                  <input value={draft.tags} onChange={(e) => edit({ tags: e.target.value })} placeholder="onboarding, product-feedback" style={{ ...inputStyle, width: "100%" }} />
                </FieldLabel>
                {draft.source && draft.source !== "manual" && (
                  <span style={{ fontSize: 12, color: C.dim, paddingBottom: 6 }}>Source: {draft.source}</span>
                )}
              </div>
            )}

            <textarea
              ref={titleRef}
              value={draft.title}
              readOnly={draft.archived}
              rows={1}
              placeholder="Title"
              onChange={(e) => edit({ title: e.target.value.replace(/\n/g, " ") })}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  const el = bodyRef.current;
                  if (el) {
                    el.focus();
                    el.setSelectionRange(0, 0);
                  }
                }
              }}
              style={{ ...fieldReset, fontSize: 20, fontWeight: 600, lineHeight: 1.35, color: C.text }}
            />
            <textarea
              ref={bodyRef}
              value={draft.body}
              readOnly={draft.archived}
              placeholder={draft.archived ? "" : "Start writing…"}
              onChange={(e) => edit({ body: e.target.value })}
              onKeyDown={(e) => {
                const el = e.currentTarget;
                if (e.key === "Backspace" && el.selectionStart === 0 && el.selectionEnd === 0) {
                  e.preventDefault();
                  const t = titleRef.current;
                  if (t) {
                    t.focus();
                    t.setSelectionRange(t.value.length, t.value.length);
                  }
                }
              }}
              style={{ ...fieldReset, marginTop: 12, minHeight: 120, fontSize: narrow ? 16 : 14, lineHeight: 1.7, color: C.text }}
            />

            {!showInfo && (draft.tags.trim() || draft.kind !== "note") && (
              <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 20 }}>
                {draft.kind !== "note" && <Chip onClick={draft.archived ? undefined : () => setShowInfo(true)}>{kindLabel(draft.kind)}</Chip>}
                {splitTags(draft.tags).map((t) => (
                  <Chip key={t} onClick={draft.archived ? undefined : () => setShowInfo(true)}>#{t}</Chip>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : (
        <div style={{ flex: 1, display: "flex", alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 12, padding: 24, textAlign: "center" }}>
          <span style={{ color: C.dim, display: "inline-flex" }}><NoteIcon size={28} /></span>
          <span style={{ fontSize: 13, color: C.muted }}>{loaded && visibleNotes.length === 0 ? "No notes here yet." : "Select a note to read or edit it."}</span>
          <AccentButton onClick={newNote}><PlusIcon size={13} /> New note</AccentButton>
        </div>
      )}
    </section>
  );

  const inEditorOnPhone = narrow && mobileView === "editor" && !!draft;

  return (
    <div ref={rootRef} style={{ position: "relative", height: "100%", minHeight: 0, display: "flex", flexDirection: "column", background: C.bg, color: C.text, overflow: "hidden" }}>
      {!inEditorOnPhone && (
        <header style={{ flexShrink: 0, display: "flex", alignItems: "center", gap: 12, borderBottom: `1px solid ${C.border}`, padding: narrow ? "8px 12px" : "12px 16px" }}>
          <div style={{ minWidth: 0 }}>
            <h1 style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>Notes</h1>
            {!narrow && <p style={{ margin: "2px 0 0", fontSize: 12, color: C.muted }}>Searchable notes for you and your agents, in your own folders.</p>}
          </div>
          {narrow && (
            <button type="button" onClick={newNote} aria-label="New note" style={{ ...resetButton, marginLeft: "auto", width: 44, height: 44, borderRadius: 4, background: C.accent, color: C.bg, display: "inline-flex", alignItems: "center", justifyContent: "center" }}>
              <PlusIcon size={18} />
            </button>
          )}
        </header>
      )}
      <div style={{ flex: 1, minHeight: 0, display: "flex" }}>
        {narrow ? (
          inEditorOnPhone ? editor : list
        ) : (
          <>
            {showFolders && folderList}
            {list}
            {editor}
          </>
        )}
      </div>
      {dialog && <FolderDialog state={dialog} onCancel={() => setDialog(null)} onSubmit={submitDialog} narrow={narrow} />}
    </div>
  );
}

// --- pieces -------------------------------------------------------------------

// Shared top bar height for the folder, list and editor columns so their
// bottom borders line up.
const barStyle: CSSProperties = {
  flexShrink: 0,
  display: "flex",
  alignItems: "center",
  minHeight: 49,
  boxSizing: "border-box",
  padding: "0 12px",
  borderBottom: `1px solid ${C.border}`,
};

function ListButton({ selected, onClick, children, padding = "12px 16px" }: { selected: boolean; onClick: () => void; children: ReactNode; padding?: string }) {
  const [hover, setHover] = useState(false);
  return (
    <button
      type="button"
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        ...resetButton,
        display: "block",
        width: "100%",
        textAlign: "left",
        padding,
        borderLeft: `2px solid ${selected ? C.accent : "transparent"}`,
        background: selected || hover ? C.hover : "transparent",
        transition: "background-color 120ms",
      }}
    >
      {children}
    </button>
  );
}

function NoteRow({ note, selected, onClick }: { note: Note; selected: boolean; onClick: () => void }) {
  const preview = firstLine(note.body) || "No additional text";
  return (
    <div style={{ borderBottom: `1px solid ${C.border}` }}>
      <ListButton selected={selected} onClick={onClick}>
        <div style={{ fontSize: 14, fontWeight: 500, color: C.text, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {note.title || "New Note"}
        </div>
        <div style={{ marginTop: 4, fontSize: 12, color: C.dim, display: "flex", gap: 8, minWidth: 0 }}>
          <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{preview}</span>
          <span style={{ flexShrink: 0 }}>{formatShort(note.updated_at)}</span>
        </div>
      </ListButton>
    </div>
  );
}

function EmptyList({ query, folder, onNew }: { query: string; folder: string; onNew: () => void }) {
  const searching = !!query.trim();
  const text = searching ? "No notes match your search." : folder === "archived" ? "No archived notes." : "No notes here yet.";
  return (
    <div style={{ padding: 24, display: "flex", flexDirection: "column", alignItems: "center", gap: 12, textAlign: "center" }}>
      <span style={{ color: C.dim, display: "inline-flex" }}><NoteIcon size={28} /></span>
      <p style={{ margin: 0, fontSize: 13, color: C.muted }}>{text}</p>
      {!searching && folder !== "archived" && (
        <p style={{ margin: 0, fontSize: 12, color: C.dim }}>
          <button type="button" onClick={onNew} style={{ ...resetButton, color: C.accent }}>Write a note</button> to get started.
        </p>
      )}
    </div>
  );
}

function FieldLabel({ label, children, grow }: { label: string; children: ReactNode; grow?: boolean }) {
  return (
    <label style={{ display: "flex", flexDirection: "column", gap: 4, flex: grow ? 1 : undefined, minWidth: grow ? 200 : undefined }}>
      <span style={{ fontSize: 11, color: C.muted, textTransform: "uppercase", letterSpacing: 0.6 }}>{label}</span>
      {children}
    </label>
  );
}

function AccentButton({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{ ...resetButton, display: "inline-flex", alignItems: "center", gap: 6, borderRadius: 4, background: C.accent, color: C.bg, padding: "6px 10px", fontSize: 12, fontWeight: 600, whiteSpace: "nowrap" }}
    >
      {children}
    </button>
  );
}

type DialogState =
  | { mode: "create" }
  | { mode: "rename"; folder: Folder }
  | { mode: "delete"; folder: Folder };

function FolderDialog({ state, onCancel, onSubmit, narrow }: { state: DialogState; onCancel: () => void; onSubmit: (value: string) => Promise<void>; narrow: boolean }) {
  const [value, setValue] = useState(state.mode === "rename" ? state.folder.name : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const isDelete = state.mode === "delete";
  const title = state.mode === "create" ? "New folder" : state.mode === "rename" ? "Rename folder" : "Delete folder";
  const disabled = busy || (!isDelete && !value.trim());
  const submit = async () => {
    if (disabled) return;
    setBusy(true);
    setError("");
    try {
      await onSubmit(value.trim());
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  return (
    <div
      onClick={(e) => { if (e.target === e.currentTarget) onCancel(); }}
      style={{ position: "absolute", inset: 0, zIndex: 50, background: "color-mix(in srgb, var(--bg, #0a0a0a) 80%, transparent)", backdropFilter: "blur(4px)", display: "flex", alignItems: "center", justifyContent: "center", padding: 16 }}
    >
      <form
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        onKeyDown={(e) => { if (e.key === "Escape") onCancel(); }}
        style={{ width: "100%", maxWidth: 420, background: C.card, border: `1px solid ${C.border}`, borderRadius: 8, boxShadow: "0 16px 48px rgba(0,0,0,0.5)", overflow: "hidden" }}
      >
        <div style={{ display: "flex", alignItems: "center", borderBottom: `1px solid ${C.border}`, padding: "12px 16px" }}>
          <h2 style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>{title}</h2>
          <div style={{ flex: 1 }} />
          <IconButton label="Close" onClick={onCancel}><CloseIcon /></IconButton>
        </div>
        <div style={{ padding: 16 }}>
          {isDelete ? (
            <p style={{ margin: 0, fontSize: 13, color: C.muted, lineHeight: 1.6 }}>
              Delete <span style={{ color: C.text }}>{state.folder.name}</span>? Its notes are kept and stay under All Notes.
            </p>
          ) : (
            <label style={{ display: "block" }}>
              <span style={{ display: "block", marginBottom: 4, fontSize: 11, color: C.muted, textTransform: "uppercase", letterSpacing: 0.6 }}>Name</span>
              <input
                autoFocus
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="e.g. Product ideas"
                maxLength={80}
                style={{ width: "100%", boxSizing: "border-box", background: C.input, color: C.text, border: `1px solid ${C.border}`, borderRadius: 4, padding: "8px 12px", fontSize: narrow ? 16 : 14, outline: "none", font: "inherit" }}
              />
            </label>
          )}
          {error && <p style={{ margin: "8px 0 0", fontSize: 12, color: C.error }}>{error}</p>}
        </div>
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, borderTop: `1px solid ${C.border}`, padding: "12px 16px" }}>
          <button type="button" onClick={onCancel} style={{ ...resetButton, padding: "6px 12px", borderRadius: 4, fontSize: 12, color: C.muted, border: `1px solid ${C.border}` }}>
            Cancel
          </button>
          <button
            type="submit"
            disabled={disabled}
            style={{ ...resetButton, padding: "6px 12px", borderRadius: 4, fontSize: 12, fontWeight: 600, color: C.bg, background: isDelete ? C.error : C.accent, opacity: disabled ? 0.5 : 1, cursor: disabled ? "default" : "pointer" }}
          >
            {isDelete ? "Delete" : state.mode === "create" ? "Create" : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
}

function MenuItem({ children, onClick, danger }: { children: ReactNode; onClick: () => void; danger?: boolean }) {
  const [hover, setHover] = useState(false);
  return (
    <button
      type="button"
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{ ...resetButton, display: "block", width: "100%", textAlign: "left", padding: "8px 12px", borderRadius: 4, fontSize: 13, color: danger ? C.error : C.text, background: hover ? C.input : "transparent" }}
    >
      {children}
    </button>
  );
}

function SaveBadge({ state, error }: { state: SaveState; error: string }) {
  if (state === "idle") return null;
  const text = state === "error" ? "Not saved" : state === "saved" ? "Saved" : "Saving…";
  return (
    <span title={state === "error" ? error : undefined} style={{ fontSize: 12, color: state === "error" ? C.error : C.dim, marginRight: 4 }}>
      {text}
    </span>
  );
}

function IconButton({ label, onClick, children, active, size = 32 }: { label: string; onClick: () => void; children: ReactNode; active?: boolean; size?: number }) {
  const [hover, setHover] = useState(false);
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        ...resetButton,
        width: size,
        height: size,
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        borderRadius: 4,
        color: active || hover ? C.text : C.muted,
        background: active || hover ? C.input : "transparent",
        flexShrink: 0,
      }}
    >
      {children}
    </button>
  );
}

function Chip({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      style={{ ...resetButton, fontSize: 12, color: C.muted, background: C.bg, border: `1px solid ${C.border}`, borderRadius: 4, padding: "2px 6px", cursor: onClick ? "pointer" : "default" }}
    >
      {children}
    </button>
  );
}

const resetButton: CSSProperties = {
  border: "none",
  background: "transparent",
  padding: 0,
  margin: 0,
  font: "inherit",
  color: "inherit",
  cursor: "pointer",
};

const fieldReset: CSSProperties = {
  display: "block",
  width: "100%",
  boxSizing: "border-box",
  border: "none",
  outline: "none",
  background: "transparent",
  resize: "none",
  padding: 0,
  margin: 0,
  overflow: "hidden",
  fontFamily: "inherit",
};

// --- icons (stroke = currentColor; colour comes from the parent's style) ---------

function svgProps(size: number) {
  return { width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 1.8, strokeLinecap: "round" as const, strokeLinejoin: "round" as const };
}


function ArchiveIcon({ size = 19 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <rect x="3" y="4" width="18" height="4" rx="1" />
      <path d="M5 8v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8" />
      <path d="M10 12h4" />
    </svg>
  );
}

function TagIcon({ size = 19 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M3 12V4a1 1 0 0 1 1-1h8l9 9-9 9z" />
      <circle cx="7.5" cy="7.5" r="1.2" />
    </svg>
  );
}

function PlusIcon({ size = 15 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

function MoreIcon({ size = 19 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <circle cx="12" cy="12" r="9" />
      <circle cx="8" cy="12" r="0.6" fill="currentColor" />
      <circle cx="12" cy="12" r="0.6" fill="currentColor" />
      <circle cx="16" cy="12" r="0.6" fill="currentColor" />
    </svg>
  );
}

function NoteIcon({ size = 19 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M6 3h9l4 4v14H6z" />
      <path d="M14 3v5h5M9 13h7M9 17h5" />
    </svg>
  );
}

function CloseIcon({ size = 16 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M6 6l12 12M18 6 6 18" />
    </svg>
  );
}

function FolderIcon({ size = 15 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
  );
}

function SearchIcon({ size = 14 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.5-3.5" />
    </svg>
  );
}


function ChevronLeft({ size = 22 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="m15 18-6-6 6-6" />
    </svg>
  );
}

// --- helpers --------------------------------------------------------------------

function deriveTitle(title: string, body: string): string {
  const t = title.trim();
  if (t) return t.slice(0, 200);
  const line = firstLine(body);
  return line ? line.slice(0, 80) : "New Note";
}

function firstLine(text: string): string {
  for (const line of (text || "").split("\n")) {
    const t = line.trim();
    if (t) return t;
  }
  return "";
}

function splitTags(value: string): string[] {
  return value.split(",").map((s) => s.trim().replace(/^#/, "")).filter(Boolean);
}

function kindLabel(kind: string): string {
  return kind.replace(/_/g, " ").replace(/\b\w/g, (m) => m.toUpperCase());
}

function parse(value: string): Date | null {
  if (!value) return null;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

function groupByDate(notes: Note[]): { label: string; notes: Note[] }[] {
  const today = startOfDay(new Date());
  const day = 86_400_000;
  const groups: { label: string; notes: Note[] }[] = [];
  const push = (label: string, note: Note) => {
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.notes.push(note);
    else groups.push({ label, notes: [note] });
  };
  for (const note of notes) {
    const d = parse(note.updated_at);
    if (!d) {
      push("Earlier", note);
      continue;
    }
    const s = startOfDay(d);
    if (s >= today) push("Today", note);
    else if (s >= today - day) push("Yesterday", note);
    else if (s >= today - 7 * day) push("Previous 7 Days", note);
    else if (s >= today - 30 * day) push("Previous 30 Days", note);
    else if (d.getFullYear() === new Date().getFullYear()) push(d.toLocaleDateString(undefined, { month: "long" }), note);
    else push(String(d.getFullYear()), note);
  }
  return groups;
}

function formatShort(value: string): string {
  const d = parse(value);
  if (!d) return "";
  const today = startOfDay(new Date());
  const s = startOfDay(d);
  if (s >= today) return d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  if (s >= today - 86_400_000) return "Yesterday";
  if (s >= today - 6 * 86_400_000) return d.toLocaleDateString(undefined, { weekday: "long" });
  return d.toLocaleDateString(undefined, { day: "numeric", month: "numeric", year: "2-digit" });
}

function formatLong(value: string): string {
  const d = parse(value);
  if (!d) return "";
  return `${d.toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" })} at ${d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}`;
}
