import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Book {
  id: number;
  title: string;
  subtitle?: string;
  author_name?: string;
  description?: string;
  kind: string;
  language: string;
  target_word_count: number;
  actual_word_count: number;
  node_count: number;
  status: string;
}

interface BookNode {
  id: number;
  book_id: number;
  parent_id?: number;
  type: string;
  title: string;
  body_markdown?: string;
  summary?: string;
  position: number;
  status: string;
  target_word_count: number;
  actual_word_count: number;
  children?: BookNode[];
}

interface BookNote {
  id: number;
  book_id: number;
  node_id?: number;
  type: string;
  title: string;
  body?: string;
  url?: string;
  tags?: string[];
}

const API = "/api/apps/books";

export default function BooksPanel({ projectId }: NativePanelProps) {
  const [books, setBooks] = useState<Book[]>([]);
  const [book, setBook] = useState<Book | null>(null);
  const [nodes, setNodes] = useState<BookNode[]>([]);
  const [selected, setSelected] = useState<BookNode | null>(null);
  const [notes, setNotes] = useState<BookNote[]>([]);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [draft, setDraft] = useState({ title: "", body_markdown: "", summary: "", type: "chapter", status: "draft", target_word_count: 0 });
  const [newBookTitle, setNewBookTitle] = useState("");
  const [newNote, setNewNote] = useState("");

  const qs = useCallback(
    (extra: Record<string, string> = {}) => {
      const q = new URLSearchParams({ project_id: projectId, ...extra });
      return q.toString();
    },
    [projectId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
      const init: RequestInit = { method, credentials: "same-origin" };
      if (body !== undefined) {
        init.headers = { "Content-Type": "application/json" };
        init.body = JSON.stringify(body);
      }
      const sep = path.includes("?") ? "&" : "?";
      const res = await fetch(`${API}${path}${sep}${qs()}`, init);
      if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
      return (await res.json()) as T;
    },
    [qs],
  );

  const loadBooks = useCallback(async () => {
    setBusy(true);
    try {
      const data = await api<{ books: Book[] }>("GET", "/books");
      setBooks(data.books || []);
      if (!book && data.books?.[0]) setBook(data.books[0]);
      setStatus(`${data.books?.length || 0} books`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [api, book]);

  const loadBook = useCallback(
    async (id: number) => {
      setBusy(true);
      try {
        const data = await api<{ book: Book; nodes: BookNode[] }>("GET", `/books/${id}?include_tree=1`);
        setBook(data.book);
        setNodes(data.nodes || []);
        const first = flatten(data.nodes || [])[0] || null;
        setSelected(first);
        setDraft(setDraftFromNode(first));
        setStatus(data.book.title);
      } catch (e) {
        setStatus((e as Error).message);
      } finally {
        setBusy(false);
      }
    },
    [api],
  );

  const loadNotes = useCallback(async () => {
    if (!book) return;
    const path = selected ? `/books/${book.id}/notes?node_id=${selected.id}` : `/books/${book.id}/notes`;
    try {
      const data = await api<{ notes: BookNote[] }>("GET", path);
      setNotes(data.notes || []);
    } catch {}
  }, [api, book, selected]);

  useEffect(() => {
    loadBooks();
  }, []);

  useEffect(() => {
    if (book) loadBook(book.id);
  }, [book?.id]);

  useEffect(() => {
    loadNotes();
  }, [loadNotes]);

  const progress = book?.target_word_count ? Math.min(100, Math.round((book.actual_word_count / book.target_word_count) * 100)) : 0;

  async function createBook() {
    const title = newBookTitle.trim();
    if (!title) return;
    const data = await api<{ book: Book }>("POST", "/books", { title, kind: "other", language: "en", create_starter: true });
    setNewBookTitle("");
    await loadBooks();
    setBook(data.book);
  }

  async function createNode(type: string) {
    if (!book) return;
    const title = type === "section" ? "New Section" : type === "part" ? "New Part" : "New Chapter";
    const parent_id = type === "section" && selected ? selected.id : undefined;
    const data = await api<{ node: BookNode }>("POST", `/books/${book.id}/nodes`, { type, title, parent_id });
    await loadBook(book.id);
    setSelected(data.node);
    setDraft(setDraftFromNode(data.node));
  }

  async function saveNode() {
    if (!selected) return;
    await api("PATCH", `/nodes/${selected.id}`, draft);
    if (book) await loadBook(book.id);
    setStatus("Saved");
  }

  async function deleteNode() {
    if (!selected || !book) return;
    await api("DELETE", `/nodes/${selected.id}`);
    await loadBook(book.id);
  }

  async function addNote() {
    if (!book || !newNote.trim()) return;
    await api("POST", `/books/${book.id}/notes`, {
      node_id: selected?.id,
      type: "note",
      title: newNote.trim().slice(0, 80),
      body: newNote.trim(),
    });
    setNewNote("");
    await loadNotes();
  }

  async function exportMarkdown(store: boolean) {
    if (!book) return;
    const data = await api<{ content?: string; export: { filename: string }; url?: string }>("POST", `/books/${book.id}/export`, { store });
    if (data.content) downloadText(data.export.filename || "book.md", data.content);
    setStatus(data.url ? `Stored export: ${data.url}` : "Markdown exported");
  }

  return (
    <div className="h-full min-h-0 grid grid-cols-[260px_minmax(360px,1fr)_300px] bg-bg text-text">
      <aside className="min-h-0 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border">
          <div className="text-sm font-medium">Books</div>
          <div className="mt-2 flex gap-2">
            <input
              value={newBookTitle}
              onChange={(e) => setNewBookTitle(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") createBook(); }}
              placeholder="New book title"
              className="min-w-0 flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <button onClick={createBook} className="w-8 h-8 border border-border rounded text-sm hover:bg-bg-input" title="Create book">+</button>
          </div>
        </div>
        <div className="max-h-48 overflow-auto border-b border-border">
          {books.map((b) => (
            <button
              key={b.id}
              onClick={() => setBook(b)}
              className={`block w-full text-left px-3 py-2 text-sm border-b border-border/60 hover:bg-bg-input ${book?.id === b.id ? "bg-bg-input" : ""}`}
            >
              <div className="truncate font-medium">{b.title}</div>
              <div className="text-xs text-text-muted">{b.actual_word_count || 0} words</div>
            </button>
          ))}
        </div>
        <div className="p-3 border-b border-border flex items-center gap-2">
          <button onClick={() => createNode("part")} disabled={!book} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Part</button>
          <button onClick={() => createNode("chapter")} disabled={!book} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Chapter</button>
          <button onClick={() => createNode("section")} disabled={!book || !selected} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Section</button>
        </div>
        <div className="min-h-0 overflow-auto p-2">
          <NodeTree nodes={nodes} selectedId={selected?.id} onSelect={(n) => { setSelected(n); setDraft(setDraftFromNode(n)); }} />
        </div>
      </aside>

      <main className="min-h-0 flex flex-col">
        <div className="h-14 border-b border-border px-4 flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium">{book?.title || "No book selected"}</div>
            <div className="text-xs text-text-muted">{book ? `${book.actual_word_count || 0} words${book.target_word_count ? ` · ${progress}% target` : ""}` : "Create a book to start writing"}</div>
          </div>
          <button onClick={saveNode} disabled={!selected} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Save</button>
          <button onClick={deleteNode} disabled={!selected} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Delete</button>
        </div>
        {selected ? (
          <div className="min-h-0 flex-1 grid grid-rows-[auto_1fr]">
            <div className="p-4 grid grid-cols-[1fr_140px_140px] gap-3 border-b border-border">
              <input value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} className="bg-bg-input border border-border rounded px-3 py-2 text-sm" />
              <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-2 text-sm">
                <option value="part">Part</option>
                <option value="chapter">Chapter</option>
                <option value="section">Section</option>
                <option value="front_matter">Front matter</option>
                <option value="back_matter">Back matter</option>
                <option value="appendix">Appendix</option>
              </select>
              <select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-2 text-sm">
                <option value="idea">Idea</option>
                <option value="draft">Draft</option>
                <option value="revised">Revised</option>
                <option value="final">Final</option>
              </select>
            </div>
            <textarea
              value={draft.body_markdown}
              onChange={(e) => setDraft({ ...draft, body_markdown: e.target.value })}
              spellCheck
              className="min-h-0 w-full resize-none bg-bg px-5 py-4 font-mono text-sm leading-6 outline-none"
              placeholder="Write the manuscript in Markdown..."
            />
          </div>
        ) : (
          <div className="p-6 text-sm text-text-muted">Select or create a chapter to start writing.</div>
        )}
      </main>

      <aside className="min-h-0 border-l border-border flex flex-col">
        <div className="p-3 border-b border-border">
          <div className="text-sm font-medium">Inspector</div>
          <div className="mt-2 text-xs text-text-muted">{busy ? "Working..." : status}</div>
        </div>
        <div className="p-3 border-b border-border">
          <label className="text-xs text-text-muted">Summary</label>
          <textarea value={draft.summary} onChange={(e) => setDraft({ ...draft, summary: e.target.value })} className="mt-1 w-full h-20 resize-none bg-bg-input border border-border rounded p-2 text-sm" />
          <label className="mt-3 block text-xs text-text-muted">Target words</label>
          <input type="number" value={draft.target_word_count || 0} onChange={(e) => setDraft({ ...draft, target_word_count: Number(e.target.value) || 0 })} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" />
          <div className="mt-2 text-xs text-text-muted">Current: {selected?.actual_word_count || 0}</div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-3 border-b border-border">
          <div className="text-sm font-medium">Notes</div>
          <div className="mt-2 flex gap-2">
            <input value={newNote} onChange={(e) => setNewNote(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") addNote(); }} placeholder="Add note" className="min-w-0 flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm" />
            <button onClick={addNote} disabled={!book} className="w-8 h-8 border border-border rounded text-sm hover:bg-bg-input disabled:opacity-40">+</button>
          </div>
          <div className="mt-3 space-y-2">
            {notes.map((n) => (
              <div key={n.id} className="border border-border rounded p-2">
                <div className="text-sm font-medium truncate">{n.title}</div>
                {n.body && <div className="mt-1 text-xs text-text-muted whitespace-pre-wrap">{n.body}</div>}
              </div>
            ))}
          </div>
        </div>
        <div className="p-3">
          <div className="text-sm font-medium">Exports</div>
          <div className="mt-2 flex gap-2">
            <button onClick={() => exportMarkdown(false)} disabled={!book} className="flex-1 px-2 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Download MD</button>
            <button onClick={() => exportMarkdown(true)} disabled={!book} className="flex-1 px-2 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Store MD</button>
          </div>
        </div>
      </aside>
    </div>
  );
}

function NodeTree({ nodes, selectedId, onSelect, depth = 0 }: { nodes: BookNode[]; selectedId?: number; onSelect: (n: BookNode) => void; depth?: number }) {
  return (
    <div className="space-y-1">
      {nodes.map((n) => (
        <div key={n.id}>
          <button
            onClick={() => onSelect(n)}
            className={`w-full text-left px-2 py-1.5 rounded text-sm hover:bg-bg-input ${selectedId === n.id ? "bg-bg-input" : ""}`}
            style={{ paddingLeft: 8 + depth * 14 }}
          >
            <div className="truncate">{n.title}</div>
            <div className="text-[11px] text-text-muted">{n.type} · {n.actual_word_count || 0}</div>
          </button>
          {n.children?.length ? <NodeTree nodes={n.children} selectedId={selectedId} onSelect={onSelect} depth={depth + 1} /> : null}
        </div>
      ))}
    </div>
  );
}

function flatten(nodes: BookNode[]): BookNode[] {
  const out: BookNode[] = [];
  const walk = (list: BookNode[]) => {
    for (const n of list) {
      out.push(n);
      if (n.children?.length) walk(n.children);
    }
  };
  walk(nodes);
  return out;
}

function setDraftFromNode(n: BookNode | null) {
  return {
    title: n?.title || "",
    body_markdown: n?.body_markdown || "",
    summary: n?.summary || "",
    type: n?.type || "chapter",
    status: n?.status || "draft",
    target_word_count: n?.target_word_count || 0,
  };
}

function downloadText(filename: string, content: string) {
  const blob = new Blob([content], { type: "text/markdown;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
