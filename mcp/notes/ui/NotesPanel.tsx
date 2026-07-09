import { useCallback, useEffect, useMemo, useState } from "react";

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

const API = "/api/apps/notes";
const kinds = ["note", "customer_idea", "meeting_note", "research", "decision", "follow_up", "spec_draft"];

export default function NotesPanel({ projectId }: NativePanelProps) {
  const [notes, setNotes] = useState<Note[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [query, setQuery] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");
  const [status, setStatus] = useState("active");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState({
    title: "",
    body: "",
    kind: "note",
    source: "manual",
    tags: "",
  });

  const withProject = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const load = useCallback(async () => {
    const params = new URLSearchParams();
    if (query.trim()) params.set("q", query.trim());
    if (kindFilter) params.set("kind", kindFilter);
    if (tagFilter.trim()) params.set("tag", tagFilter.trim());
    params.set("status", status);
    try {
      const res = await fetch(withProject(`/notes?${params.toString()}`), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const list = data.notes ?? [];
      setNotes(list);
      setSelectedId((current) => current && list.some((n: Note) => n.id === current) ? current : list[0]?.id ?? 0);
      setMessage(`${list.length} note${list.length === 1 ? "" : "s"}`);
    } catch (e) {
      setMessage((e as Error).message);
    }
  }, [kindFilter, query, status, tagFilter, withProject]);

  useEffect(() => { load(); }, [load]);

  const selected = useMemo(
    () => notes.find((n) => n.id === selectedId) ?? null,
    [notes, selectedId],
  );

  useEffect(() => {
    if (!selected) {
      setDraft({ title: "", body: "", kind: "note", source: "manual", tags: "" });
      return;
    }
    setDraft({
      title: selected.title,
      body: selected.body,
      kind: selected.kind || "note",
      source: selected.source || "manual",
      tags: Array.isArray(selected.tags) ? selected.tags.join(", ") : "",
    });
  }, [selected]);

  const createNote = async () => {
    setSelectedId(0);
    setDraft({ title: "", body: "", kind: "note", source: "manual", tags: "" });
    setMessage("New note");
  };

  const save = async () => {
    if (!draft.title.trim()) {
      setMessage("Title is required.");
      return;
    }
    setSaving(true);
    try {
      const body = {
        title: draft.title.trim(),
        body: draft.body,
        kind: draft.kind || "note",
        source: draft.source || "manual",
        tags: splitTags(draft.tags),
      };
      const res = selected
        ? await fetch(withProject(`/notes/${selected.id}`), {
            method: "PATCH",
            credentials: "same-origin",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          })
        : await fetch(withProject("/notes"), {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setSelectedId(data.note?.id ?? 0);
      await load();
      setMessage("Saved.");
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const archive = async () => {
    if (!selected) return;
    setSaving(true);
    try {
      const res = await fetch(withProject(`/notes/${selected.id}/archive`), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      await load();
      setMessage("Archived.");
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">Notes</h1>
          <p className="text-xs text-text-muted">Capture searchable notes, ideas, research, and decisions.</p>
        </div>
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search notes"
          className="ml-auto w-64 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
        <button type="button" onClick={load} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
          Refresh
        </button>
        <button type="button" onClick={createNote} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">
          New
        </button>
      </header>

      <main className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="border-r border-border min-h-0 flex flex-col">
          <div className="p-3 border-b border-border grid grid-cols-2 gap-2">
            <select
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
            >
              <option value="active">Active</option>
              <option value="archived">Archived</option>
              <option value="all">All</option>
            </select>
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
            >
              <option value="">All kinds</option>
              {kinds.map((kind) => <option key={kind} value={kind}>{kindLabel(kind)}</option>)}
            </select>
            <input
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
              placeholder="Filter tag"
              className="col-span-2 bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
            />
          </div>

          <div className="flex-1 min-h-0 overflow-auto">
            {notes.length === 0 ? (
              <div className="p-4 text-sm text-text-muted">No notes match the current filters.</div>
            ) : (
              <ul className="divide-y divide-border">
                {notes.map((note) => (
                  <li key={note.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(note.id)}
                      className={`w-full text-left px-4 py-3 hover:bg-bg-input ${note.id === selectedId ? "bg-bg-input" : ""}`}
                    >
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-sm truncate">{note.title}</span>
                        <span className="ml-auto text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">{kindLabel(note.kind)}</span>
                      </div>
                      <p className="mt-1 text-xs text-text-muted line-clamp-2">{note.body || "No body yet."}</p>
                      <div className="mt-2 flex items-center gap-1 text-[10px] text-text-dim">
                        <span>{timeAgo(note.updated_at)}</span>
                        {Array.isArray(note.tags) && note.tags.slice(0, 3).map((tag) => (
                          <span key={tag} className="px-1.5 py-0.5 rounded bg-bg border border-border">{tag}</span>
                        ))}
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        <section className="min-h-0 flex flex-col">
          <div className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-2">
            <input
              value={draft.title}
              onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
              placeholder="Untitled note"
              className="flex-1 bg-transparent text-lg font-semibold outline-none"
            />
            <button
              type="button"
              onClick={save}
              disabled={saving}
              className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50"
            >
              {saving ? "Saving..." : "Save"}
            </button>
            {selected && selected.status !== "archived" && (
              <button
                type="button"
                onClick={archive}
                disabled={saving}
                className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
              >
                Archive
              </button>
            )}
          </div>

          <div className="shrink-0 border-b border-border px-4 py-3 grid grid-cols-1 md:grid-cols-3 gap-3">
            <label className="text-xs text-text-muted">
              Kind
              <select
                value={draft.kind}
                onChange={(e) => setDraft((d) => ({ ...d, kind: e.target.value }))}
                className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
              >
                {kinds.map((kind) => <option key={kind} value={kind}>{kindLabel(kind)}</option>)}
              </select>
            </label>
            <label className="text-xs text-text-muted">
              Source
              <input
                value={draft.source}
                onChange={(e) => setDraft((d) => ({ ...d, source: e.target.value }))}
                className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
              />
            </label>
            <label className="text-xs text-text-muted">
              Tags
              <input
                value={draft.tags}
                onChange={(e) => setDraft((d) => ({ ...d, tags: e.target.value }))}
                placeholder="product-feedback, onboarding"
                className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
              />
            </label>
          </div>

          <textarea
            value={draft.body}
            onChange={(e) => setDraft((d) => ({ ...d, body: e.target.value }))}
            placeholder="Write a note..."
            className="flex-1 min-h-0 resize-none bg-bg text-text outline-none p-4 text-sm leading-6"
          />

          <footer className="shrink-0 border-t border-border px-4 py-2 text-xs text-text-muted flex items-center gap-3">
            <span>{message}</span>
            {selected && <span className="ml-auto">Updated {formatDate(selected.updated_at)}</span>}
          </footer>
        </section>
      </main>
    </div>
  );
}

function splitTags(value: string): string[] {
  return value.split(",").map((s) => s.trim()).filter(Boolean);
}

function kindLabel(kind: string): string {
  return kind.replace(/_/g, " ").replace(/\b\w/g, (m) => m.toUpperCase());
}

function formatDate(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function timeAgo(value: string): string {
  const date = new Date(value);
  const ms = Date.now() - date.getTime();
  if (!value || Number.isNaN(ms)) return "";
  const min = Math.floor(ms / 60000);
  if (min < 1) return "just now";
  if (min < 60) return `${min}m ago`;
  const hours = Math.floor(min / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  return date.toLocaleDateString();
}
