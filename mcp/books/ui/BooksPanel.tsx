import { useCallback, useEffect, useState, type ReactNode } from "react";

interface NativePanelProps { appName: string; installId: number; projectId: string }
interface Contributor { name: string; role: string }
interface BookPrice { marketplace: string; currency: string; amount: number }
interface PrintSettings {
  trim_width_in: number; trim_height_in: number; inner_margin_in: number; outer_margin_in: number;
  top_margin_in: number; bottom_margin_in: number; font_size_pt: number; line_height_pt: number;
  bleed: boolean; paper: string; ink: string; binding: string;
}
interface PublicationMetadata {
  publisher: string; imprint: string; series_name: string; series_number: string; edition: string;
  publication_date: string; ebook_isbn: string; print_isbn: string; contributors: Contributor[];
  categories: string[]; keywords: string[]; audience: string; min_age: number; max_age: number;
  rights_statement: string; territories: string[]; ai_disclosure: string; reading_direction: string;
  copyright_year: number; accessibility_summary: string; accessibility_features: string[];
  accessibility_hazards: string[]; prices: BookPrice[]; print: PrintSettings;
}
interface Book {
  id: number; title: string; subtitle?: string; author_name?: string; description?: string; kind: string;
  language: string; target_word_count: number; actual_word_count: number; node_count: number; status: string;
  publication: PublicationMetadata;
}
interface BookNode {
  id: number; book_id: number; parent_id?: number; type: string; title: string; body_markdown?: string;
  summary?: string; position: number; status: string; target_word_count: number; actual_word_count: number;
  children?: BookNode[];
}
interface BookNote { id: number; book_id: number; node_id?: number; type: string; title: string; body?: string; url?: string; tags?: string[] }
interface BookAsset {
  id: number; book_id: number; node_id?: number; kind: string; filename: string; content_type: string;
  alt_text?: string; caption?: string; size_bytes: number;
}
interface ValidationReport { valid: boolean; validator: string; checks: string[]; warnings: string[]; errors: string[] }
interface ChecklistItem { id: string; label: string; status: string; detail?: string }
interface Checklist { platform: string; ready: boolean; items: ChecklistItem[] }

const API = "/api/apps/books";
const currentYear = new Date().getFullYear();
const defaultPublication: PublicationMetadata = {
  publisher: "", imprint: "", series_name: "", series_number: "", edition: "", publication_date: "",
  ebook_isbn: "", print_isbn: "", contributors: [], categories: [], keywords: [], audience: "adult",
  min_age: 0, max_age: 0, rights_statement: "All rights reserved.", territories: ["WORLD"],
  ai_disclosure: "", reading_direction: "ltr", copyright_year: currentYear, prices: [],
  accessibility_summary: "", accessibility_features: ["tableOfContents", "structuralNavigation", "readingOrder"], accessibility_hazards: ["none"],
  print: { trim_width_in: 6, trim_height_in: 9, inner_margin_in: 0.75, outer_margin_in: 0.5,
    top_margin_in: 0.65, bottom_margin_in: 0.65, font_size_pt: 11, line_height_pt: 15,
    bleed: false, paper: "white", ink: "black", binding: "paperback" },
};

export default function BooksPanel({ projectId }: NativePanelProps) {
  const [books, setBooks] = useState<Book[]>([]);
  const [book, setBook] = useState<Book | null>(null);
  const [nodes, setNodes] = useState<BookNode[]>([]);
  const [selected, setSelected] = useState<BookNode | null>(null);
  const [notes, setNotes] = useState<BookNote[]>([]);
  const [assets, setAssets] = useState<BookAsset[]>([]);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [rightTab, setRightTab] = useState<"write" | "publish">("write");
  const [platform, setPlatform] = useState("kindle");
  const [checklist, setChecklist] = useState<Checklist | null>(null);
  const [validation, setValidation] = useState<ValidationReport | null>(null);
  const [publication, setPublication] = useState<PublicationMetadata>(defaultPublication);
  const [bookDraft, setBookDraft] = useState({ title: "", subtitle: "", author_name: "", description: "", language: "en" });
  const [listDrafts, setListDrafts] = useState({ contributors: "", categories: "", keywords: "", territories: "WORLD", price: "" });
  const [draft, setDraft] = useState({ title: "", body_markdown: "", summary: "", type: "chapter", status: "draft", target_word_count: 0 });
  const [newBookTitle, setNewBookTitle] = useState("");
  const [newNote, setNewNote] = useState("");

  const qs = useCallback((extra: Record<string, string> = {}) => new URLSearchParams({ project_id: projectId, ...extra }).toString(), [projectId]);
  const api = useCallback(async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const init: RequestInit = { method, credentials: "same-origin" };
    if (body !== undefined) { init.headers = { "Content-Type": "application/json" }; init.body = JSON.stringify(body); }
    const sep = path.includes("?") ? "&" : "?";
    const res = await fetch(`${API}${path}${sep}${qs()}`, init);
    if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
    return (await res.json()) as T;
  }, [qs]);

  const loadBooks = useCallback(async () => {
    setBusy(true);
    try {
      const data = await api<{ books: Book[] }>("GET", "/books");
      setBooks(data.books || []);
      if (!book && data.books?.[0]) setBook(data.books[0]);
      setStatus(`${data.books?.length || 0} books`);
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }, [api, book]);

  const loadBook = useCallback(async (id: number) => {
    setBusy(true);
    try {
      const data = await api<{ book: Book; nodes: BookNode[] }>("GET", `/books/${id}?include_tree=1`);
      setBook(data.book); setNodes(data.nodes || []);
      const nextPublication = mergePublication(data.book.publication);
      setPublication(nextPublication);
      setListDrafts({ contributors: contributorsText(nextPublication.contributors), categories: nextPublication.categories.join(", "), keywords: nextPublication.keywords.join(", "), territories: nextPublication.territories.join(", "), price: priceText(nextPublication.prices) });
      setBookDraft({ title: data.book.title, subtitle: data.book.subtitle || "", author_name: data.book.author_name || "", description: data.book.description || "", language: data.book.language || "en" });
      const flat = flatten(data.nodes || []);
      const current = flat.find((node) => node.id === selected?.id) || flat[0] || null;
      setSelected(current); setDraft(setDraftFromNode(current)); setStatus(data.book.title);
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }, [api, selected?.id]);

  const loadAssets = useCallback(async () => {
    if (!book) return;
    try { const data = await api<{ assets: BookAsset[] }>("GET", `/books/${book.id}/assets`); setAssets(data.assets || []); } catch (e) { setStatus((e as Error).message); }
  }, [api, book?.id]);
  const loadNotes = useCallback(async () => {
    if (!book) return;
    const path = selected ? `/books/${book.id}/notes?node_id=${selected.id}` : `/books/${book.id}/notes`;
    try { const data = await api<{ notes: BookNote[] }>("GET", path); setNotes(data.notes || []); } catch {}
  }, [api, book?.id, selected?.id]);

  useEffect(() => { loadBooks(); }, []);
  useEffect(() => { if (book) loadBook(book.id); }, [book?.id]);
  useEffect(() => { loadNotes(); }, [loadNotes]);
  useEffect(() => { loadAssets(); }, [loadAssets]);

  const progress = book?.target_word_count ? Math.min(100, Math.round((book.actual_word_count / book.target_word_count) * 100)) : 0;

  async function createBook() {
    const title = newBookTitle.trim(); if (!title) return;
    try {
      const data = await api<{ book: Book }>("POST", "/books", { title, kind: "other", language: "en", create_starter: true, publication: defaultPublication });
      setNewBookTitle(""); await loadBooks(); setBook(data.book);
    } catch (e) { setStatus((e as Error).message); }
  }
  async function createNode(type: string) {
    if (!book) return;
    const title = type === "section" ? "New Section" : type === "part" ? "New Part" : "New Chapter";
    const parent_id = type === "section" && selected ? selected.id : undefined;
    try {
      const data = await api<{ node: BookNode }>("POST", `/books/${book.id}/nodes`, { type, title, parent_id });
      await loadBook(book.id); setSelected(data.node); setDraft(setDraftFromNode(data.node));
    } catch (e) { setStatus((e as Error).message); }
  }
  async function saveNode() {
    if (!selected) return;
    try { await api("PATCH", `/nodes/${selected.id}`, draft); if (book) await loadBook(book.id); setStatus("Chapter saved"); }
    catch (e) { setStatus((e as Error).message); }
  }
  async function moveSelected(delta: number) {
    if (!selected || !book) return;
    try { await api("POST", `/nodes/${selected.id}/move`, { parent_id: selected.parent_id || null, position: Math.max(0, selected.position + delta) }); await loadBook(book.id); }
    catch (e) { setStatus((e as Error).message); }
  }
  async function deleteNode() {
    if (!selected || !book) return;
    try { await api("DELETE", `/nodes/${selected.id}`); await loadBook(book.id); } catch (e) { setStatus((e as Error).message); }
  }
  async function addNote() {
    if (!book || !newNote.trim()) return;
    try {
      await api("POST", `/books/${book.id}/notes`, { node_id: selected?.id, type: "note", title: newNote.trim().slice(0, 80), body: newNote.trim() });
      setNewNote(""); await loadNotes();
    } catch (e) { setStatus((e as Error).message); }
  }
  async function savePublication() {
    if (!book) return;
    setBusy(true);
    try {
      const prices = parsePrice(listDrafts.price);
      if (listDrafts.price.trim() && prices.length === 0) throw new Error("List price must look like USD 4.99");
      const nextPublication = { ...publication, contributors: parseContributors(listDrafts.contributors), categories: csv(listDrafts.categories), keywords: csv(listDrafts.keywords), territories: csv(listDrafts.territories), prices };
      await api("PATCH", `/books/${book.id}`, { ...bookDraft, publication: nextPublication });
      setPublication(nextPublication);
      await loadBook(book.id); await loadBooks(); setStatus("Publication metadata saved");
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }
  async function uploadAsset(kind: string, file: File) {
    if (!book) return;
    setBusy(true);
    try {
      const content_base64 = await fileToBase64(file);
      const data = await api<{ asset: BookAsset }>("POST", `/books/${book.id}/assets`, {
        kind, filename: file.name, content_type: file.type, content_base64,
        node_id: kind === "interior_image" ? selected?.id : undefined,
        alt_text: kind === "cover" ? `Cover of ${book.title}` : file.name.replace(/\.[^.]+$/, ""),
      });
      await loadAssets();
      if (kind === "interior_image" && selected) {
        const insertion = `\n\n![${data.asset.alt_text || "Image"}](asset:${data.asset.id})\n\n`;
        setDraft((current) => ({ ...current, body_markdown: current.body_markdown + insertion }));
        setStatus("Image uploaded and inserted; save the chapter to keep the reference");
      } else setStatus("Cover uploaded");
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }
  async function deleteAsset(id: number) {
    try { await api("DELETE", `/assets/${id}`); await loadAssets(); } catch (e) { setStatus((e as Error).message); }
  }
  async function runPublicationCheck() {
    if (!book) return;
    setBusy(true);
    try {
      const data = await api<{ checklist: Checklist; validation: ValidationReport }>("POST", `/books/${book.id}/publication-check`, { platform, include_notes: true });
      setChecklist(data.checklist); setValidation(data.validation); setStatus(data.checklist.ready ? "Ready for final store preview" : "Publication checklist needs attention");
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }
  async function exportBook(format: "markdown" | "epub" | "pdf" | "package", store = false) {
    if (!book) return;
    setBusy(true);
    try {
      const data = await api<{ content?: string; content_base64?: string; content_type: string; export: { filename: string }; url?: string; validation?: ValidationReport; checklist?: Checklist }>("POST", `/books/${book.id}/export`, { format, platform, include_notes: true, store });
      if (data.content) downloadBlob(data.export.filename, new Blob([data.content], { type: data.content_type }));
      if (data.content_base64) downloadBlob(data.export.filename, base64ToBlob(data.content_base64, data.content_type));
      if (data.validation) setValidation(data.validation);
      if (data.checklist) setChecklist(data.checklist);
      setStatus(data.url ? `Stored export: ${data.url}` : `${format.toUpperCase()} exported`);
    } catch (e) { setStatus((e as Error).message); } finally { setBusy(false); }
  }

  return (
    <div className="h-full min-h-0 grid grid-cols-[270px_minmax(360px,1fr)_360px] bg-bg text-text">
      <aside className="min-h-0 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border"><div className="text-sm font-medium">Books</div><div className="mt-2 flex gap-2"><input value={newBookTitle} onChange={(e) => setNewBookTitle(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") createBook(); }} placeholder="New book title" className="min-w-0 flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm" /><button onClick={createBook} className="w-8 h-8 border border-border rounded text-sm hover:bg-bg-input" title="Create book">+</button></div></div>
        <div className="max-h-48 overflow-auto border-b border-border">{books.map((item) => <button key={item.id} onClick={() => setBook(item)} className={`block w-full text-left px-3 py-2 text-sm border-b border-border/60 hover:bg-bg-input ${book?.id === item.id ? "bg-bg-input" : ""}`}><div className="truncate font-medium">{item.title}</div><div className="text-xs text-text-muted">{item.actual_word_count || 0} words</div></button>)}</div>
        <div className="p-3 border-b border-border flex items-center gap-2"><button onClick={() => createNode("part")} disabled={!book} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Part</button><button onClick={() => createNode("chapter")} disabled={!book} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Chapter</button><button onClick={() => createNode("section")} disabled={!book || !selected} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-40">Section</button></div>
        <div className="min-h-0 overflow-auto p-2"><NodeTree nodes={nodes} selectedId={selected?.id} onSelect={(node) => { setSelected(node); setDraft(setDraftFromNode(node)); }} /></div>
      </aside>

      <main className="min-h-0 flex flex-col">
        <div className="h-14 border-b border-border px-4 flex items-center gap-2"><div className="min-w-0 flex-1"><div className="truncate font-medium">{book?.title || "No book selected"}</div><div className="text-xs text-text-muted">{book ? `${book.actual_word_count || 0} words${book.target_word_count ? ` · ${progress}% target` : ""}` : "Create a book to start writing"}</div></div><button onClick={() => moveSelected(-1)} disabled={!selected || selected.position === 0} className="w-8 h-8 text-sm border border-border rounded disabled:opacity-40" title="Move up">↑</button><button onClick={() => moveSelected(1)} disabled={!selected} className="w-8 h-8 text-sm border border-border rounded disabled:opacity-40" title="Move down">↓</button><button onClick={saveNode} disabled={!selected} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Save</button><button onClick={deleteNode} disabled={!selected} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Delete</button></div>
        {selected ? <div className="min-h-0 flex-1 grid grid-rows-[auto_1fr]"><div className="p-4 grid grid-cols-[1fr_140px_140px] gap-3 border-b border-border"><input value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} className="bg-bg-input border border-border rounded px-3 py-2 text-sm" /><select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-2 text-sm"><option value="part">Part</option><option value="chapter">Chapter</option><option value="section">Section</option><option value="front_matter">Front matter</option><option value="back_matter">Back matter</option><option value="appendix">Appendix</option></select><select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-2 text-sm"><option value="idea">Idea</option><option value="draft">Draft</option><option value="revised">Revised</option><option value="final">Final</option></select></div><textarea value={draft.body_markdown} onChange={(e) => setDraft({ ...draft, body_markdown: e.target.value })} spellCheck className="min-h-0 w-full resize-none bg-bg px-5 py-4 font-mono text-sm leading-6 outline-none" placeholder="Write the manuscript in Markdown..." /></div> : <div className="p-6 text-sm text-text-muted">Select or create a chapter to start writing.</div>}
      </main>

      <aside className="min-h-0 border-l border-border flex flex-col">
        <div className="border-b border-border"><div className="px-3 pt-3 flex items-center justify-between"><div className="text-sm font-medium">Inspector</div><div className="text-xs text-text-muted truncate max-w-48">{busy ? "Working…" : status}</div></div><div className="mt-2 grid grid-cols-2"><button onClick={() => setRightTab("write")} className={`py-2 text-xs border-b-2 ${rightTab === "write" ? "border-text" : "border-transparent text-text-muted"}`}>Writing</button><button onClick={() => setRightTab("publish")} className={`py-2 text-xs border-b-2 ${rightTab === "publish" ? "border-text" : "border-transparent text-text-muted"}`}>Publish</button></div></div>
        {rightTab === "write" ? <WritingInspector draft={draft} setDraft={setDraft} selected={selected} book={book} notes={notes} newNote={newNote} setNewNote={setNewNote} addNote={addNote} uploadAsset={uploadAsset} /> : <div className="min-h-0 flex-1 overflow-auto">
          <section className="p-3 border-b border-border space-y-2"><div className="text-sm font-medium">Book details</div>
            <Field label="Title"><input className="books-field" value={bookDraft.title} onChange={(e) => setBookDraft({ ...bookDraft, title: e.target.value })} /></Field><Field label="Subtitle"><input className="books-field" value={bookDraft.subtitle} onChange={(e) => setBookDraft({ ...bookDraft, subtitle: e.target.value })} /></Field>
            <div className="grid grid-cols-2 gap-2"><Field label="Author"><input className="books-field" value={bookDraft.author_name} onChange={(e) => setBookDraft({ ...bookDraft, author_name: e.target.value })} /></Field><Field label="Language"><input className="books-field" value={bookDraft.language} onChange={(e) => setBookDraft({ ...bookDraft, language: e.target.value })} /></Field></div>
            <Field label="Store description"><textarea className="books-field h-20 resize-none" value={bookDraft.description} onChange={(e) => setBookDraft({ ...bookDraft, description: e.target.value })} /></Field>
            <div className="grid grid-cols-2 gap-2"><Field label="Publisher"><input className="books-field" value={publication.publisher} onChange={(e) => setPublication({ ...publication, publisher: e.target.value })} /></Field><Field label="Imprint"><input className="books-field" value={publication.imprint} onChange={(e) => setPublication({ ...publication, imprint: e.target.value })} /></Field><Field label="Series"><input className="books-field" value={publication.series_name} onChange={(e) => setPublication({ ...publication, series_name: e.target.value })} /></Field><Field label="Series number"><input className="books-field" value={publication.series_number} onChange={(e) => setPublication({ ...publication, series_number: e.target.value })} /></Field><Field label="Edition"><input className="books-field" value={publication.edition} onChange={(e) => setPublication({ ...publication, edition: e.target.value })} /></Field><Field label="Publication date"><input type="date" className="books-field" value={publication.publication_date} onChange={(e) => setPublication({ ...publication, publication_date: e.target.value })} /></Field><Field label="Ebook ISBN"><input className="books-field" value={publication.ebook_isbn} onChange={(e) => setPublication({ ...publication, ebook_isbn: e.target.value })} /></Field><Field label="Print ISBN"><input className="books-field" value={publication.print_isbn} onChange={(e) => setPublication({ ...publication, print_isbn: e.target.value })} /></Field></div>
            <Field label="Contributors (Name | role, …)"><input className="books-field" value={listDrafts.contributors} onChange={(e) => setListDrafts({ ...listDrafts, contributors: e.target.value })} /></Field>
            <Field label="Categories (comma-separated)"><input className="books-field" value={listDrafts.categories} onChange={(e) => setListDrafts({ ...listDrafts, categories: e.target.value })} /></Field><Field label="Keywords (comma-separated)"><input className="books-field" value={listDrafts.keywords} onChange={(e) => setListDrafts({ ...listDrafts, keywords: e.target.value })} /></Field><Field label="Rights statement"><input className="books-field" value={publication.rights_statement} onChange={(e) => setPublication({ ...publication, rights_statement: e.target.value })} /></Field>
            <div className="grid grid-cols-2 gap-2"><Field label="Territories"><input className="books-field" value={listDrafts.territories} onChange={(e) => setListDrafts({ ...listDrafts, territories: e.target.value })} /></Field><Field label="List price"><input className="books-field" placeholder="USD 4.99" value={listDrafts.price} onChange={(e) => setListDrafts({ ...listDrafts, price: e.target.value })} /></Field><Field label="Audience"><select className="books-field" value={publication.audience} onChange={(e) => setPublication({ ...publication, audience: e.target.value })}><option value="adult">Adult</option><option value="young_adult">Young adult</option><option value="children">Children</option></select></Field><Field label="Reading direction"><select className="books-field" value={publication.reading_direction} onChange={(e) => setPublication({ ...publication, reading_direction: e.target.value })}><option value="ltr">Left to right</option><option value="rtl">Right to left</option></select></Field><Field label="AI disclosure"><select className="books-field" value={publication.ai_disclosure} onChange={(e) => setPublication({ ...publication, ai_disclosure: e.target.value })}><option value="">Review…</option><option value="none">None</option><option value="assisted">AI-assisted</option><option value="generated">AI-generated</option></select></Field><Field label="Copyright year"><input type="number" className="books-field" value={publication.copyright_year} onChange={(e) => setPublication({ ...publication, copyright_year: Number(e.target.value) || currentYear })} /></Field></div>
            <Field label="Accessibility summary"><textarea className="books-field h-16 resize-none" placeholder="Describe navigation, image alternatives, and reading order" value={publication.accessibility_summary} onChange={(e) => setPublication({ ...publication, accessibility_summary: e.target.value })} /></Field>
            <button onClick={savePublication} disabled={!book || busy} className="w-full px-3 py-2 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Save publication metadata</button>
          </section>
          <section className="p-3 border-b border-border space-y-2"><div className="text-sm font-medium">Covers and assets</div><div className="grid grid-cols-2 gap-2"><AssetUpload label="Ebook cover" accept="image/jpeg,image/png" disabled={!book} onFile={(file) => uploadAsset("cover", file)} /><AssetUpload label="Print cover PDF" accept="application/pdf" disabled={!book} onFile={(file) => uploadAsset("print_cover", file)} /></div><div className="space-y-2">{assets.map((asset) => <div key={asset.id} className="flex gap-2 border border-border rounded p-2 items-center">{asset.content_type.startsWith("image/") ? <img src={`${API}/assets/${asset.id}/content?${qs()}`} className="w-10 h-14 object-cover rounded" alt={asset.alt_text || ""} /> : <div className="w-10 h-14 grid place-items-center bg-bg-input rounded text-[10px]">PDF</div>}<div className="min-w-0 flex-1"><div className="truncate text-xs font-medium">{asset.filename}</div><div className="text-[11px] text-text-muted">{asset.kind} · {formatBytes(asset.size_bytes)}</div></div><button onClick={() => deleteAsset(asset.id)} className="text-xs text-text-muted hover:text-text">Remove</button></div>)}</div></section>
          <section className="p-3 border-b border-border space-y-2"><div className="text-sm font-medium">Print layout</div><div className="grid grid-cols-2 gap-2"><Field label="Trim width (in)"><input type="number" step="0.125" className="books-field" value={publication.print.trim_width_in} onChange={(e) => setPrint(publication, setPublication, "trim_width_in", e.target.value)} /></Field><Field label="Trim height (in)"><input type="number" step="0.125" className="books-field" value={publication.print.trim_height_in} onChange={(e) => setPrint(publication, setPublication, "trim_height_in", e.target.value)} /></Field><Field label="Inner margin"><input type="number" step="0.05" className="books-field" value={publication.print.inner_margin_in} onChange={(e) => setPrint(publication, setPublication, "inner_margin_in", e.target.value)} /></Field><Field label="Outer margin"><input type="number" step="0.05" className="books-field" value={publication.print.outer_margin_in} onChange={(e) => setPrint(publication, setPublication, "outer_margin_in", e.target.value)} /></Field><Field label="Top margin"><input type="number" step="0.05" className="books-field" value={publication.print.top_margin_in} onChange={(e) => setPrint(publication, setPublication, "top_margin_in", e.target.value)} /></Field><Field label="Bottom margin"><input type="number" step="0.05" className="books-field" value={publication.print.bottom_margin_in} onChange={(e) => setPrint(publication, setPublication, "bottom_margin_in", e.target.value)} /></Field><Field label="Font size (pt)"><input type="number" step="0.5" className="books-field" value={publication.print.font_size_pt} onChange={(e) => setPrint(publication, setPublication, "font_size_pt", e.target.value)} /></Field><Field label="Line height (pt)"><input type="number" step="0.5" className="books-field" value={publication.print.line_height_pt} onChange={(e) => setPrint(publication, setPublication, "line_height_pt", e.target.value)} /></Field><Field label="Paper"><select className="books-field" value={publication.print.paper} onChange={(e) => setPublication({ ...publication, print: { ...publication.print, paper: e.target.value } })}><option value="white">White</option><option value="cream">Cream</option></select></Field><Field label="Interior"><select className="books-field" value={publication.print.ink} onChange={(e) => setPublication({ ...publication, print: { ...publication.print, ink: e.target.value } })}><option value="black">Black & white</option><option value="color">Color</option></select></Field></div><label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={publication.print.bleed} onChange={(e) => setPublication({ ...publication, print: { ...publication.print, bleed: e.target.checked } })} /> Interior contains bleed</label></section>
          <section className="p-3 space-y-2"><div className="text-sm font-medium">Validate and export</div><select className="books-field" value={platform} onChange={(e) => { setPlatform(e.target.value); setChecklist(null); }}><option value="kindle">Kindle / KDP</option><option value="apple_books">Apple Books</option><option value="kobo">Kobo</option><option value="google_play">Google Play Books</option><option value="print">Print</option><option value="generic">Universal package</option></select><button onClick={runPublicationCheck} disabled={!book || busy} className="w-full px-3 py-2 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Run publication check</button>
            {checklist && <div className={`rounded border p-2 ${checklist.ready ? "border-emerald-500/50" : "border-amber-500/50"}`}><div className="text-xs font-medium">{checklist.ready ? "Ready for final preview" : "Action required"}</div><div className="mt-2 space-y-1">{checklist.items.map((item) => <div key={item.id} className="text-[11px]"><span className={item.status === "complete" ? "text-emerald-500" : "text-amber-500"}>{item.status === "complete" ? "✓" : "○"}</span> {item.label}</div>)}</div>{validation && <div className="mt-2 text-[11px] text-text-muted">{validation.validator}: {validation.valid ? "passed" : `${validation.errors.length} errors`}{validation.warnings.length ? ` · ${validation.warnings.length} warning` : ""}</div>}</div>}
            <div className="grid grid-cols-2 gap-2"><button onClick={() => exportBook("epub")} disabled={!book || busy} className="books-export-button">Download EPUB</button><button onClick={() => exportBook("pdf")} disabled={!book || busy} className="books-export-button">Download PDF</button><button onClick={() => exportBook("package")} disabled={!book || busy} className="books-export-button col-span-2">Download platform package</button><button onClick={() => exportBook("markdown")} disabled={!book || busy} className="books-export-button">Download source</button><button onClick={() => exportBook("package", true)} disabled={!book || busy} className="books-export-button">Store package</button></div>
          </section>
        </div>}
      </aside>
      <style>{`.books-field{width:100%;background:var(--color-bg-input);border:1px solid var(--color-border);border-radius:.375rem;padding:.4rem .5rem;font-size:.75rem;outline:none}.books-export-button{padding:.5rem;font-size:.75rem;border:1px solid var(--color-border);border-radius:.375rem}.books-export-button:hover{background:var(--color-bg-input)}.books-export-button:disabled{opacity:.4}`}</style>
    </div>
  );
}

function WritingInspector({ draft, setDraft, selected, book, notes, newNote, setNewNote, addNote, uploadAsset }: any) { return <div className="min-h-0 flex-1 overflow-auto"><div className="p-3 border-b border-border"><Field label="Summary"><textarea value={draft.summary} onChange={(e) => setDraft({ ...draft, summary: e.target.value })} className="books-field h-20 resize-none" /></Field><Field label="Target words"><input type="number" value={draft.target_word_count || 0} onChange={(e) => setDraft({ ...draft, target_word_count: Number(e.target.value) || 0 })} className="books-field" /></Field><div className="text-xs text-text-muted">Current: {selected?.actual_word_count || 0}</div></div><div className="p-3 border-b border-border"><div className="text-sm font-medium">Notes</div><div className="mt-2 flex gap-2"><input value={newNote} onChange={(e) => setNewNote(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") addNote(); }} placeholder="Add note" className="min-w-0 flex-1 books-field" /><button onClick={addNote} disabled={!book} className="w-8 h-8 border border-border rounded text-sm disabled:opacity-40">+</button></div><div className="mt-3 space-y-2">{notes.map((note: BookNote) => <div key={note.id} className="border border-border rounded p-2"><div className="text-sm font-medium truncate">{note.title}</div>{note.body && <div className="mt-1 text-xs text-text-muted whitespace-pre-wrap">{note.body}</div>}</div>)}</div></div><div className="p-3"><AssetUpload label="Insert image" accept="image/jpeg,image/png" disabled={!book || !selected} onFile={(file) => uploadAsset("interior_image", file)} /></div></div>; }
function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="block"><span className="block mb-1 text-[11px] text-text-muted">{label}</span>{children}</label>; }
function AssetUpload({ label, accept, disabled, onFile }: { label: string; accept: string; disabled?: boolean; onFile: (file: File) => void }) { return <label className={`block text-center px-2 py-2 text-xs border border-border rounded hover:bg-bg-input ${disabled ? "opacity-40 pointer-events-none" : "cursor-pointer"}`}>{label}<input type="file" accept={accept} disabled={disabled} className="hidden" onChange={(event) => { const file = event.target.files?.[0]; if (file) onFile(file); event.currentTarget.value = ""; }} /></label>; }
function NodeTree({ nodes, selectedId, onSelect, depth = 0 }: { nodes: BookNode[]; selectedId?: number; onSelect: (node: BookNode) => void; depth?: number }) { return <div className="space-y-1">{nodes.map((node) => <div key={node.id}><button onClick={() => onSelect(node)} className={`w-full text-left px-2 py-1.5 rounded text-sm hover:bg-bg-input ${selectedId === node.id ? "bg-bg-input" : ""}`} style={{ paddingLeft: 8 + depth * 14 }}><div className="truncate">{node.title}</div><div className="text-[11px] text-text-muted">{node.type} · {node.actual_word_count || 0}</div></button>{node.children?.length ? <NodeTree nodes={node.children} selectedId={selectedId} onSelect={onSelect} depth={depth + 1} /> : null}</div>)}</div>; }
function flatten(nodes: BookNode[]): BookNode[] { const out: BookNode[] = []; const walk = (items: BookNode[]) => { for (const node of items) { out.push(node); if (node.children?.length) walk(node.children); } }; walk(nodes); return out; }
function setDraftFromNode(node: BookNode | null) { return { title: node?.title || "", body_markdown: node?.body_markdown || "", summary: node?.summary || "", type: node?.type || "chapter", status: node?.status || "draft", target_word_count: node?.target_word_count || 0 }; }
function mergePublication(value?: Partial<PublicationMetadata>): PublicationMetadata { return { ...defaultPublication, ...(value || {}), print: { ...defaultPublication.print, ...(value?.print || {}) }, contributors: value?.contributors || [], categories: value?.categories || [], keywords: value?.keywords || [], territories: value?.territories || ["WORLD"], prices: value?.prices || [] }; }
function csv(value: string): string[] { return value.split(",").map((item) => item.trim()).filter(Boolean); }
function contributorsText(values: Contributor[]): string { return values.map((item) => `${item.name} | ${item.role || "contributor"}`).join(", "); }
function parseContributors(value: string): Contributor[] { return csv(value).map((item) => { const [name, role] = item.split("|").map((part) => part.trim()); return { name, role: role || "contributor" }; }).filter((item) => item.name); }
function priceText(values: BookPrice[]): string { const price = values[0]; return price ? `${price.currency} ${price.amount}` : ""; }
function parsePrice(value: string): BookPrice[] { const match = value.trim().match(/^([A-Za-z]{3})\s+([0-9]+(?:\.[0-9]{1,2})?)$/); return match ? [{ marketplace: "primary", currency: match[1].toUpperCase(), amount: Number(match[2]) }] : []; }
function setPrint(publication: PublicationMetadata, setter: (value: PublicationMetadata) => void, key: keyof PrintSettings, value: string) { setter({ ...publication, print: { ...publication.print, [key]: Number(value) || 0 } }); }
function formatBytes(size: number): string { if (size < 1024) return `${size} B`; if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`; return `${(size / 1024 / 1024).toFixed(1)} MB`; }
function fileToBase64(file: File): Promise<string> { return new Promise((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result).split(",")[1] || ""); reader.onerror = () => reject(reader.error); reader.readAsDataURL(file); }); }
function base64ToBlob(value: string, type: string): Blob { const binary = atob(value); const bytes = new Uint8Array(binary.length); for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i); return new Blob([bytes], { type }); }
function downloadBlob(filename: string, blob: Blob) { const url = URL.createObjectURL(blob); const anchor = document.createElement("a"); anchor.href = url; anchor.download = filename; anchor.click(); URL.revokeObjectURL(url); }
