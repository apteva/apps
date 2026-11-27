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
}

// What the editor is showing. `token` identifies one editing session so a
// slow POST for a new note still lands on the right draft after the user has
// moved on to another note.
interface Draft {
  token: number;
  id: number;
  title: string;
  body: string;
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
const FOLDER_LABELS: Record<string, string> = {
  all: "All Notes",
  note: "Notes",
  customer_idea: "Customer Ideas",
  meeting_note: "Meeting Notes",
  research: "Research",
  decision: "Decisions",
  follow_up: "Follow-ups",
  spec_draft: "Spec Drafts",
  archived: "Archived",
};
const FOLDERS = ["all", ...KINDS, "archived"];

// Panel widths (container, not viewport: the panel can be narrow on desktop too).
const NARROW = 720;
const WITH_FOLDERS = 1100;
const AUTOSAVE_MS = 700;

const FONT = '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter", "Segoe UI", system-ui, sans-serif';
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

const emptyDraft = (token: number, kind = "note"): Draft => ({
  token,
  id: 0,
  title: "",
  body: "",
  kind,
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
  const [folder, setFolder] = useState("all");
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

  useEffect(() => { loadActive(); }, [loadActive]);
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
      if (KINDS.includes(folder)) params.kind = folder;
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
    return activeNotes.filter((n) => n.kind === folder);
  }, [searchResults, folder, archivedNotes, activeNotes]);

  const counts = useMemo(() => {
    const out: Record<string, number> = { all: activeNotes.length };
    for (const n of activeNotes) out[n.kind] = (out[n.kind] ?? 0) + 1;
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
    const kind = KINDS.includes(folder) ? folder : "note";
    if (folder === "archived") setFolder("all");
    setQuery("");
    const next = emptyDraft(nextTokenRef.current++, kind);
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
      setArchivedNotes([]);
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

  const chooseFolder = (f: string) => {
    flush();
    setFolder(f);
    setQuery("");
    if (!narrow) {
      draftRef.current = null;
      setDraft(null);
    }
  };

  // --- render -----------------------------------------------------------------

  const folderList = (
    <nav style={{ width: 220, flexShrink: 0, borderRight: `1px solid ${C.border}`, background: C.subtle, overflowY: "auto", padding: "14px 8px" }}>
      <div style={{ fontSize: 11, fontWeight: 600, color: C.dim, padding: "0 10px 6px", letterSpacing: 0.3 }}>FOLDERS</div>
      {FOLDERS.map((f) => {
        const active = f === folder;
        return (
          <button
            key={f}
            type="button"
            onClick={() => chooseFolder(f)}
            style={{
              ...resetButton,
              width: "100%",
              display: "flex",
              alignItems: "center",
              gap: 8,
              padding: "7px 10px",
              borderRadius: 7,
              fontSize: 13,
              color: C.text,
              background: active ? C.selected : "transparent",
              marginTop: f === "archived" ? 10 : 0,
            }}
          >
            <span style={{ color: C.accent, display: "inline-flex" }}>{f === "archived" ? <ArchiveIcon size={15} /> : <FolderIcon />}</span>
            <span style={{ flex: 1, textAlign: "left", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{FOLDER_LABELS[f]}</span>
            {f !== "archived" && <span style={{ color: C.dim, fontSize: 12 }}>{counts[f] ?? 0}</span>}
          </button>
        );
      })}
    </nav>
  );

  const groups = groupByDate(visibleNotes);
  const listHeaderTitle = FOLDER_LABELS[folder];

  const list = (
    <section
      style={{
        display: "flex",
        flexDirection: "column",
        minHeight: 0,
        minWidth: 0,
        width: narrow ? "100%" : 320,
        flexShrink: 0,
        borderRight: narrow ? "none" : `1px solid ${C.border}`,
        background: narrow ? C.bg : C.bg,
      }}
    >
      <div style={{ padding: narrow ? "14px 16px 10px" : "12px 14px 10px", flexShrink: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, minHeight: 32 }}>
          {showFolders ? (
            <div style={{ fontSize: narrow ? 30 : 17, fontWeight: 700, flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{listHeaderTitle}</div>
          ) : (
            <label style={{ position: "relative", flex: 1, minWidth: 0, display: "flex", alignItems: "center", gap: 4, cursor: "pointer" }}>
              <span style={{ fontSize: narrow ? 30 : 17, fontWeight: 700, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{listHeaderTitle}</span>
              <span style={{ color: C.accent, display: "inline-flex" }}><ChevronDown /></span>
              <select
                aria-label="Folder"
                value={folder}
                onChange={(e) => chooseFolder(e.target.value)}
                style={{ position: "absolute", inset: 0, opacity: 0, cursor: "pointer", fontSize: 16 }}
              >
                {FOLDERS.map((f) => (
                  <option key={f} value={f}>{FOLDER_LABELS[f]}{f !== "archived" ? ` (${counts[f] ?? 0})` : ""}</option>
                ))}
              </select>
            </label>
          )}
          {!narrow && (
            <IconButton label="New note" onClick={newNote}><ComposeIcon /></IconButton>
          )}
        </div>
        <div style={{ position: "relative", marginTop: 10 }}>
          <span style={{ position: "absolute", left: 9, top: "50%", transform: "translateY(-50%)", color: C.dim, display: "inline-flex" }}><SearchIcon /></span>
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search"
            type="search"
            style={{
              width: "100%",
              boxSizing: "border-box",
              background: C.input,
              border: "none",
              borderRadius: 9,
              padding: "8px 10px 8px 30px",
              fontSize: narrow ? 16 : 13,
              color: C.text,
              outline: "none",
              fontFamily: FONT,
            }}
          />
        </div>
      </div>

      <div style={{ flex: 1, minHeight: 0, overflowY: "auto", padding: narrow ? "0 16px 16px" : "0 8px 12px" }}>
        {loadError && <div style={{ padding: "8px 8px", fontSize: 12, color: C.error }}>{loadError}</div>}
        {loaded && visibleNotes.length === 0 && (
          <EmptyList query={query} folder={folder} onNew={newNote} />
        )}
        {groups.map((group) => (
          <div key={group.label} style={{ marginBottom: narrow ? 18 : 10 }}>
            <div style={{ fontSize: narrow ? 20 : 12, fontWeight: 700, color: narrow ? C.text : C.dim, padding: narrow ? "6px 2px 8px" : "8px 10px 4px" }}>
              {group.label}
            </div>
            <div style={narrow ? { background: C.card, borderRadius: 12, overflow: "hidden" } : undefined}>
              {group.notes.map((note, i) => (
                <NoteRow
                  key={note.id}
                  note={note}
                  selected={!narrow && draft?.id === note.id}
                  narrow={narrow}
                  last={i === group.notes.length - 1}
                  onClick={() => openNote(note)}
                />
              ))}
            </div>
          </div>
        ))}
      </div>

      {narrow && (
        <div style={{ flexShrink: 0, display: "flex", alignItems: "center", padding: "8px 12px", paddingBottom: "max(8px, env(safe-area-inset-bottom))", borderTop: `1px solid ${C.border}`, background: C.subtle }}>
          <div style={{ width: 40 }} />
          <div style={{ flex: 1, textAlign: "center", fontSize: 12, color: C.muted }}>
            {visibleNotes.length} {visibleNotes.length === 1 ? "Note" : "Notes"}
          </div>
          <IconButton label="New note" onClick={newNote} size={40}><ComposeIcon size={22} /></IconButton>
        </div>
      )}
    </section>
  );

  const editor = (
    <section style={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column", background: C.bg }}>
      <div style={{ flexShrink: 0, display: "flex", alignItems: "center", gap: 4, padding: narrow ? "8px 8px" : "8px 14px", minHeight: 44, borderBottom: narrow ? "none" : `1px solid ${C.border}` }}>
        {narrow && (
          <button type="button" onClick={backToList} style={{ ...resetButton, display: "flex", alignItems: "center", gap: 2, color: C.accent, fontSize: 16, padding: "6px 4px" }}>
            <ChevronLeft /> {FOLDER_LABELS[folder]}
          </button>
        )}
        <div style={{ flex: 1 }} />
        {draft && <SaveBadge state={saveState} error={saveError} />}
        {draft && !draft.archived && (
          <IconButton label="Details: kind and tags" onClick={() => setShowInfo((v) => !v)} active={showInfo}><TagIcon /></IconButton>
        )}
        {draft && !draft.archived && (
          <IconButton label="Archive note" onClick={archiveCurrent}><ArchiveIcon /></IconButton>
        )}
        {!narrow && <IconButton label="New note" onClick={newNote}><ComposeIcon /></IconButton>}
      </div>

      {draft ? (
        <div
          style={{ flex: 1, minHeight: 0, overflowY: "auto", cursor: "text" }}
          onClick={(e) => {
            if (e.target === e.currentTarget) bodyRef.current?.focus();
          }}
        >
          <div
            style={{ maxWidth: 760, margin: "0 auto", padding: narrow ? "4px 18px 120px" : "14px 36px 160px" }}
            onClick={(e) => {
              if (e.target === e.currentTarget) bodyRef.current?.focus();
            }}
          >
            <div style={{ textAlign: "center", fontSize: 12, color: C.dim, marginBottom: 14 }}>
              {draft.updatedAt ? formatLong(draft.updatedAt) : "New note"}
            </div>

            {draft.archived && (
              <div style={{ fontSize: 12, color: C.muted, background: C.subtle, border: `1px solid ${C.border}`, borderRadius: 8, padding: "8px 10px", marginBottom: 14 }}>
                This note is archived. Archived notes are read-only.
              </div>
            )}

            {showInfo && !draft.archived && (
              <div style={{ display: "flex", flexWrap: "wrap", gap: 10, alignItems: "center", background: C.subtle, border: `1px solid ${C.border}`, borderRadius: 10, padding: 10, marginBottom: 16 }}>
                <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 12, color: C.muted }}>
                  Kind
                  <select
                    value={draft.kind}
                    onChange={(e) => edit({ kind: e.target.value })}
                    style={{ background: C.input, color: C.text, border: `1px solid ${C.border}`, borderRadius: 6, padding: "4px 6px", fontSize: narrow ? 16 : 13, fontFamily: FONT }}
                  >
                    {KINDS.map((k) => <option key={k} value={k}>{kindLabel(k)}</option>)}
                  </select>
                </label>
                <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 12, color: C.muted, flex: 1, minWidth: 200 }}>
                  Tags
                  <input
                    value={draft.tags}
                    onChange={(e) => edit({ tags: e.target.value })}
                    placeholder="onboarding, product-feedback"
                    style={{ flex: 1, minWidth: 0, background: C.input, color: C.text, border: `1px solid ${C.border}`, borderRadius: 6, padding: "5px 8px", fontSize: narrow ? 16 : 13, outline: "none", fontFamily: FONT }}
                  />
                </label>
                {draft.source && draft.source !== "manual" && (
                  <span style={{ fontSize: 12, color: C.dim }}>Source: {draft.source}</span>
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
              style={{ ...fieldReset, fontSize: narrow ? 26 : 24, fontWeight: 700, lineHeight: 1.25, color: C.text }}
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
              style={{ ...fieldReset, marginTop: 8, minHeight: 120, fontSize: narrow ? 17 : 15, lineHeight: 1.6, color: C.text }}
            />

            {!showInfo && (draft.tags.trim() || draft.kind !== "note") && (
              <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 18 }}>
                {draft.kind !== "note" && <Chip onClick={draft.archived ? undefined : () => setShowInfo(true)}>{kindLabel(draft.kind)}</Chip>}
                {splitTags(draft.tags).map((t) => (
                  <Chip key={t} onClick={draft.archived ? undefined : () => setShowInfo(true)}>#{t}</Chip>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : (
        <div style={{ flex: 1, display: "flex", alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 12, color: C.dim, fontSize: 14 }}>
          <span>{loaded && visibleNotes.length === 0 ? "No notes yet" : "Select a note"}</span>
          <button type="button" onClick={newNote} style={{ ...resetButton, color: C.accent, fontSize: 14, display: "flex", alignItems: "center", gap: 6 }}>
            <ComposeIcon /> New note
          </button>
        </div>
      )}
    </section>
  );

  return (
    <div
      ref={rootRef}
      style={{ height: "100%", minHeight: 0, display: "flex", background: C.bg, color: C.text, fontFamily: FONT, WebkitFontSmoothing: "antialiased", overflow: "hidden" }}
    >
      {narrow ? (
        mobileView === "editor" && draft ? editor : list
      ) : (
        <>
          {showFolders && folderList}
          {list}
          {editor}
        </>
      )}
    </div>
  );
}

// --- pieces -------------------------------------------------------------------

function NoteRow({ note, selected, narrow, last, onClick }: { note: Note; selected: boolean; narrow: boolean; last: boolean; onClick: () => void }) {
  const [hover, setHover] = useState(false);
  const preview = firstLine(note.body) || "No additional text";
  return (
    <button
      type="button"
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        ...resetButton,
        width: "100%",
        display: "block",
        textAlign: "left",
        padding: narrow ? "12px 16px 0" : "9px 10px 0",
        borderRadius: narrow ? 0 : 8,
        background: selected ? C.selected : hover && !narrow ? C.hover : "transparent",
      }}
    >
      <div style={{ paddingBottom: narrow ? 12 : 9, borderBottom: last || selected ? "1px solid transparent" : `1px solid ${C.border}` }}>
        <div style={{ fontSize: narrow ? 16 : 14, fontWeight: 600, color: C.text, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {note.title || "New Note"}
        </div>
        <div style={{ marginTop: 2, fontSize: narrow ? 14 : 12, display: "flex", gap: 8, minWidth: 0 }}>
          <span style={{ color: C.text, flexShrink: 0 }}>{formatShort(note.updated_at)}</span>
          <span style={{ color: C.muted, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{preview}</span>
        </div>
      </div>
    </button>
  );
}

function EmptyList({ query, folder, onNew }: { query: string; folder: string; onNew: () => void }) {
  const text = query.trim() ? "No results" : folder === "archived" ? "No archived notes" : "No notes";
  return (
    <div style={{ padding: "48px 12px", textAlign: "center", color: C.dim, fontSize: 14 }}>
      <div>{text}</div>
      {!query.trim() && folder !== "archived" && (
        <button type="button" onClick={onNew} style={{ ...resetButton, marginTop: 10, color: C.accent, fontSize: 14 }}>
          Write your first note
        </button>
      )}
    </div>
  );
}

function SaveBadge({ state, error }: { state: SaveState; error: string }) {
  if (state === "idle") return null;
  const text = state === "error" ? "Not saved" : state === "saved" ? "Saved" : "Saving…";
  return (
    <span title={state === "error" ? error : undefined} style={{ fontSize: 12, color: state === "error" ? C.error : C.dim, marginRight: 6 }}>
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
        borderRadius: 7,
        color: C.accent,
        background: active ? C.selected : hover ? C.hover : "transparent",
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
      style={{ ...resetButton, fontSize: 12, color: C.accent, background: C.selected, borderRadius: 999, padding: "3px 9px", cursor: onClick ? "pointer" : "default" }}
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
  fontFamily: FONT,
};

// --- icons (stroke = currentColor; colour comes from the parent's style) ---------

function svgProps(size: number) {
  return { width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 1.8, strokeLinecap: "round" as const, strokeLinejoin: "round" as const };
}

function ComposeIcon({ size = 19 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="M12 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-6" />
      <path d="M18.4 3.6a2 2 0 0 1 2.8 2.8L12.5 15.1 9 16l.9-3.5z" />
    </svg>
  );
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

function ChevronDown({ size = 16 }: { size?: number }) {
  return (
    <svg {...svgProps(size)}>
      <path d="m6 9 6 6 6-6" />
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
