import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Testimonial {
  id: number;
  status: Status;
  kind: Kind;
  source: string;
  title: string;
  quote: string;
  body: string;
  rating?: number;
  author_name: string;
  author_role: string;
  author_company: string;
  author_email: string;
  media_file_id: string;
  media_url: string;
  consent_status: ConsentStatus;
  permission_scope: PermissionScope;
  tags: string[];
  metadata: Record<string, unknown>;
  submitted_at?: string;
  approved_at?: string;
  published_at?: string;
  created_at?: string;
  updated_at?: string;
}

interface ListResponse {
  testimonials: Testimonial[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
}

type Status = "draft" | "submitted" | "approved" | "rejected" | "published" | "archived";
type Kind = "text" | "review" | "video" | "audio" | "image";
type ConsentStatus = "unknown" | "granted" | "denied" | "revoked";
type PermissionScope = "private" | "internal" | "public" | "marketing";
type MobileView = "list" | "editor";
type Notice = { tone: "info" | "error" | "success"; text: string } | null;

const API = "/api/apps/testimonials";
const PAGE_SIZE = 25;
const statuses: Status[] = ["draft", "submitted", "approved", "rejected", "published", "archived"];
const kinds: Kind[] = ["text", "review", "video", "audio", "image"];
const consents: ConsentStatus[] = ["unknown", "granted", "denied", "revoked"];
const scopes: PermissionScope[] = ["private", "internal", "public", "marketing"];
const inputClass = "w-full min-w-0 rounded border border-border bg-bg-input px-3 py-2 text-sm text-text";

const blank: Testimonial = {
  id: 0,
  status: "draft",
  kind: "text",
  source: "manual",
  title: "",
  quote: "",
  body: "",
  author_name: "",
  author_role: "",
  author_company: "",
  author_email: "",
  media_file_id: "",
  media_url: "",
  consent_status: "unknown",
  permission_scope: "internal",
  tags: [],
  metadata: {},
};

export default function TestimonialsPanel({ projectId }: NativePanelProps) {
  const [items, setItems] = useState<Testimonial[]>([]);
  const [selectedId, setSelectedId] = useState<number | null | undefined>(undefined);
  const [draft, setDraft] = useState<Testimonial>(() => copyTestimonial(blank));
  const [tagText, setTagText] = useState("");
  const [baseline, setBaseline] = useState(() => fingerprint(blank, ""));
  const [statusFilter, setStatusFilter] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [mobileView, setMobileView] = useState<MobileView>("list");
  const [showMedia, setShowMedia] = useState(false);
  const initializedRef = useRef(false);
  const requestSeqRef = useRef(0);
  const selectedIdRef = useRef<number | null | undefined>(selectedId);
  const dirtyRef = useRef(false);

  const payload = useMemo(() => toPayload(draft, tagText), [draft, tagText]);
  const dirty = useMemo(() => JSON.stringify(payload) !== baseline, [baseline, payload]);
  const publishBlocked = draft.status === "published" &&
    (draft.consent_status !== "granted" || !["public", "marketing"].includes(draft.permission_scope));
  const busy = loading || saving;
  const pageStart = total === 0 ? 0 : page * PAGE_SIZE + 1;
  const pageEnd = Math.min((page + 1) * PAGE_SIZE, total);

  useEffect(() => {
    selectedIdRef.current = selectedId;
  }, [selectedId]);

  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setQuery(queryInput.trim());
      setPage(0);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [queryInput]);

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
      const url = new URL(`${API}${path}`, window.location.origin);
      url.searchParams.set("project_id", projectId);
      const init: RequestInit = { method, credentials: "same-origin" };
      if (body !== undefined) {
        init.headers = { "Content-Type": "application/json" };
        init.body = JSON.stringify(body);
      }
      const res = await fetch(`${url.pathname}${url.search}`, init);
      if (!res.ok) {
        const data = await res.json().catch(() => null) as { error?: string } | null;
        throw new Error(data?.error || `${res.status} ${res.statusText}`);
      }
      return (await res.json()) as T;
    },
    [projectId],
  );

  const load = useCallback(async (preferredId?: number, requestedPage = page) => {
    const seq = ++requestSeqRef.current;
    setLoading(true);
    try {
      const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(requestedPage * PAGE_SIZE) });
      if (statusFilter) params.set("status", statusFilter);
      if (kindFilter) params.set("kind", kindFilter);
      if (query) params.set("q", query);
      const data = await api<ListResponse>("GET", `/testimonials?${params}`);
      if (seq !== requestSeqRef.current) return;
      const list = data.testimonials || [];
      setItems(list);
      setTotal(data.total || 0);
      setHasMore(Boolean(data.has_more));

      const currentId = preferredId ?? selectedIdRef.current;
      const fresh = typeof currentId === "number" ? list.find((item) => item.id === currentId) : undefined;
      if (!initializedRef.current) {
        initializedRef.current = true;
        if (list[0]) openLoadedItem(list[0], false);
      } else if (fresh && (!dirtyRef.current || preferredId !== undefined)) {
        openLoadedItem(fresh, false);
      } else if (
        typeof currentId === "number" &&
        !fresh &&
        !dirtyRef.current &&
        preferredId === undefined &&
        !statusFilter &&
        !kindFilter &&
        !query
      ) {
        if (list[0]) openLoadedItem(list[0], false);
        else resetToNew(false);
      }
    } catch (error) {
      if (seq === requestSeqRef.current) setNotice({ tone: "error", text: (error as Error).message });
    } finally {
      if (seq === requestSeqRef.current) setLoading(false);
    }
  }, [api, kindFilter, page, query, statusFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  function openLoadedItem(item: Testimonial, showEditor = true) {
    const copy = copyTestimonial(item);
    const tags = (copy.tags || []).join(", ");
    selectedIdRef.current = item.id;
    dirtyRef.current = false;
    setSelectedId(item.id);
    setDraft(copy);
    setTagText(tags);
    setBaseline(fingerprint(copy, tags));
    setShowMedia(false);
    if (showEditor) setMobileView("editor");
  }

  function resetToNew(showEditor = true) {
    const next = copyTestimonial(blank);
    selectedIdRef.current = null;
    dirtyRef.current = false;
    setSelectedId(null);
    setDraft(next);
    setTagText("");
    setBaseline(fingerprint(next, ""));
    setShowMedia(false);
    if (showEditor) setMobileView("editor");
  }

  function confirmDiscard(): boolean {
    return !dirtyRef.current || window.confirm("Discard unsaved changes?");
  }

  function startNew() {
    if (!confirmDiscard()) return;
    resetToNew();
    setNotice(null);
  }

  function selectItem(item: Testimonial) {
    if (selectedIdRef.current === item.id) {
      setMobileView("editor");
      return;
    }
    if (!confirmDiscard()) return;
    openLoadedItem(item);
    setNotice(null);
  }

  function backToList() {
    if (!confirmDiscard()) return;
    if (dirtyRef.current) {
      if (typeof selectedIdRef.current === "number") {
        const original = items.find((item) => item.id === selectedIdRef.current);
        if (original) openLoadedItem(original, false);
      } else {
        resetToNew(false);
      }
    }
    setMobileView("list");
  }

  async function save() {
    if (!hasContent(payload)) {
      setNotice({ tone: "error", text: "Add text or a media reference before saving." });
      return;
    }
    if (publishBlocked) {
      setNotice({ tone: "error", text: "Publishing requires granted consent and public or marketing permission." });
      return;
    }
    setSaving(true);
    try {
      const data = draft.id
        ? await api<{ testimonial: Testimonial }>("PATCH", `/testimonials/${draft.id}`, payload)
        : await api<{ testimonial: Testimonial }>("POST", "/testimonials", payload);
      openLoadedItem(data.testimonial);
      setNotice({ tone: "success", text: draft.id ? "Changes saved." : "Testimonial created." });
      setPage(0);
      await load(data.testimonial.id, 0);
    } catch (error) {
      setNotice({ tone: "error", text: (error as Error).message });
    } finally {
      setSaving(false);
    }
  }

  async function archive() {
    if (!draft.id || !window.confirm(`Archive “${labelFor(draft)}”?`)) return;
    setSaving(true);
    try {
      await api("DELETE", `/testimonials/${draft.id}`);
      initializedRef.current = false;
      resetToNew(false);
      setPage(0);
      setNotice({ tone: "success", text: "Testimonial archived." });
      await load(undefined, 0);
      setMobileView("list");
    } catch (error) {
      setNotice({ tone: "error", text: (error as Error).message });
    } finally {
      setSaving(false);
    }
  }

  function setFilter(setter: (value: string) => void, value: string) {
    setter(value);
    setPage(0);
  }

  return (
    <div className="testimonials-shell h-full min-h-0 bg-bg text-text">
      <style>{`
        .testimonials-shell { display: block; }
        .testimonials-editor-frame { width: 100%; max-width: 1400px; margin-inline: auto; }
        .testimonials-meta-grid, .testimonials-content-grid { display: grid; }
        @media (min-width: 1024px) {
          .testimonials-shell { display: grid; grid-template-columns: 300px minmax(0, 1fr); }
        }
        @media (min-width: 1280px) {
          .testimonials-meta-grid { grid-template-columns: minmax(280px, 1fr) 150px 150px 170px; }
          .testimonials-content-grid { grid-template-columns: minmax(0, 1fr) 320px; }
        }
      `}</style>
      <aside className={`${mobileView === "editor" ? "hidden lg:flex" : "flex"} h-full min-h-0 flex-col border-r border-border`}>
        <div className="border-b border-border p-3">
          <div className="flex items-center gap-2">
            <h1 className="min-w-0 flex-1 text-sm font-semibold">Testimonials</h1>
            <button
              type="button"
              onClick={startNew}
              disabled={busy}
              className="flex h-8 w-8 items-center justify-center rounded border border-border text-lg hover:bg-bg-input disabled:opacity-40"
              aria-label="New testimonial"
              title="New testimonial"
            >+</button>
          </div>
          <div className="mt-3 grid grid-cols-2 gap-2">
            <label className="sr-only" htmlFor="testimonials-status-filter">Status filter</label>
            <select id="testimonials-status-filter" value={statusFilter} onChange={(event) => setFilter(setStatusFilter, event.target.value)} className="min-w-0 rounded border border-border bg-bg-input px-2 py-1.5 text-sm">
              <option value="">Active statuses</option>
              {statuses.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
            </select>
            <label className="sr-only" htmlFor="testimonials-kind-filter">Type filter</label>
            <select id="testimonials-kind-filter" value={kindFilter} onChange={(event) => setFilter(setKindFilter, event.target.value)} className="min-w-0 rounded border border-border bg-bg-input px-2 py-1.5 text-sm">
              <option value="">All types</option>
              {kinds.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
            </select>
          </div>
          <label className="sr-only" htmlFor="testimonials-search">Search testimonials</label>
          <input id="testimonials-search" value={queryInput} onChange={(event) => setQueryInput(event.target.value)} placeholder="Search testimonials" className="mt-2 w-full rounded border border-border bg-bg-input px-2 py-1.5 text-sm" />
        </div>

        <div className="min-h-0 flex-1 overflow-auto">
          {loading && items.length === 0 ? (
            <div className="p-4 text-sm text-text-muted">Loading…</div>
          ) : items.length === 0 ? (
            <div className="p-4 text-sm text-text-muted">No testimonials match these filters.</div>
          ) : items.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => selectItem(item)}
              className={`block w-full border-b border-border/60 px-3 py-3 text-left hover:bg-bg-input ${selectedId === item.id ? "bg-bg-input" : ""}`}
            >
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1 truncate text-sm font-medium">{labelFor(item)}</div>
                <span className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] ${statusTone(item.status)}`}>{capitalize(item.status)}</span>
              </div>
              <div className="mt-1 truncate text-xs text-text-muted">
                {authorLine(item) || capitalize(item.kind)}{item.rating ? ` · ${item.rating}/5` : ""}
              </div>
              <div className="mt-1 line-clamp-2 text-xs text-text-dim">{item.quote || item.body || item.media_url || item.media_file_id}</div>
            </button>
          ))}
        </div>

        <div className="border-t border-border p-2">
          <div className="flex items-center justify-between gap-2 text-xs text-text-dim">
            <span>{pageStart}–{pageEnd} of {total}</span>
            <div className="flex gap-1">
              <button type="button" onClick={() => setPage((value) => Math.max(0, value - 1))} disabled={page === 0 || loading} className="flex h-7 w-7 items-center justify-center rounded border border-border hover:bg-bg-input disabled:opacity-30" aria-label="Previous page" title="Previous page">←</button>
              <button type="button" onClick={() => setPage((value) => value + 1)} disabled={!hasMore || loading} className="flex h-7 w-7 items-center justify-center rounded border border-border hover:bg-bg-input disabled:opacity-30" aria-label="Next page" title="Next page">→</button>
            </div>
          </div>
        </div>
      </aside>

      <main className={`${mobileView === "list" ? "hidden lg:flex" : "flex"} h-full min-h-0 flex-col`}>
        <header className="min-h-14 border-b border-border px-3 py-2 sm:px-5">
          <div className="testimonials-editor-frame flex min-w-0 items-center gap-2">
            <button type="button" onClick={backToList} className="flex h-8 w-8 shrink-0 items-center justify-center rounded border border-border hover:bg-bg-input lg:hidden" aria-label="Back to testimonials" title="Back to testimonials">←</button>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <h2 className="truncate text-sm font-semibold sm:text-base">{draft.id ? labelFor(draft) : "New testimonial"}</h2>
                {dirty && <span className="shrink-0 rounded bg-yellow/15 px-1.5 py-0.5 text-[10px] text-yellow">Unsaved</span>}
              </div>
              <div className="truncate text-xs text-text-muted">
                {draft.id ? `#${draft.id} · ${capitalize(draft.kind)}` : "New testimonial"}
                {draft.updated_at ? ` · Updated ${formatDate(draft.updated_at)}` : ""}
              </div>
            </div>
            <button type="button" onClick={save} disabled={saving || !dirty} className="rounded border border-border px-3 py-1.5 text-sm font-medium hover:bg-bg-input disabled:opacity-40">Save</button>
            <button type="button" onClick={archive} disabled={!draft.id || saving} className="hidden rounded border border-red/50 px-3 py-1.5 text-sm text-red hover:bg-red/10 disabled:opacity-40 sm:block">Archive</button>
          </div>
          {notice && <div className={`mt-2 text-xs ${notice.tone === "error" ? "text-red" : notice.tone === "success" ? "text-green" : "text-text-muted"}`} role={notice.tone === "error" ? "alert" : "status"}>{notice.text}</div>}
        </header>

        <div className="min-h-0 flex-1 overflow-auto px-3 py-4 sm:px-5 sm:py-5">
          <div className="testimonials-editor-frame">
          <div className="testimonials-meta-grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Title" id="testimonial-title">
              <input id="testimonial-title" value={draft.title} maxLength={500} onChange={(event) => setDraft({ ...draft, title: event.target.value })} className={inputClass} />
            </Field>
            <Field label="Status" id="testimonial-status">
              <select id="testimonial-status" value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value as Status })} className={inputClass}>
                {statuses.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
              </select>
            </Field>
            <Field label="Type" id="testimonial-kind">
              <select id="testimonial-kind" value={draft.kind} onChange={(event) => setDraft({ ...draft, kind: event.target.value as Kind })} className={inputClass}>
                {kinds.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
              </select>
            </Field>
            <Field label="Source" id="testimonial-source">
              <input id="testimonial-source" value={draft.source} maxLength={100} onChange={(event) => setDraft({ ...draft, source: event.target.value })} className={inputClass} />
            </Field>
          </div>

          {publishBlocked && <div className="mt-3 rounded border border-yellow/40 bg-yellow/10 px-3 py-2 text-sm text-yellow" role="alert">Publishing requires granted consent and public or marketing permission.</div>}

          <div className="testimonials-content-grid mt-5 grid-cols-1 gap-6">
            <section className="min-w-0">
              <Field label="Quote" id="testimonial-quote">
                <textarea id="testimonial-quote" value={draft.quote} maxLength={5000} onChange={(event) => setDraft({ ...draft, quote: event.target.value })} className={`${inputClass} h-28 resize-y p-3 leading-5`} />
              </Field>
              <div className="mt-4">
                <Field label="Full review" id="testimonial-body">
                  <textarea id="testimonial-body" value={draft.body} maxLength={100000} onChange={(event) => setDraft({ ...draft, body: event.target.value })} className={`${inputClass} h-44 resize-y p-3 leading-5`} />
                </Field>
              </div>

              <div className="mt-4 border-t border-border pt-4">
                <div className="flex items-center justify-between gap-3">
                  <h3 className="text-sm font-medium">Media</h3>
                  <a href="/apps/storage/page" className="text-xs text-accent hover:underline">Open Storage</a>
                </div>
                <div className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label="Storage file ID" id="testimonial-media-file-id">
                    <input id="testimonial-media-file-id" value={draft.media_file_id} maxLength={500} onChange={(event) => setDraft({ ...draft, media_file_id: event.target.value })} className={inputClass} />
                  </Field>
                  <Field label="Media URL" id="testimonial-media-url">
                    <input id="testimonial-media-url" type="url" value={draft.media_url} maxLength={4096} onChange={(event) => { setDraft({ ...draft, media_url: event.target.value }); setShowMedia(false); }} className={inputClass} />
                  </Field>
                </div>
                {draft.media_url && isPreviewableURL(draft.media_url) && (
                  <div className="mt-3">
                    <button type="button" onClick={() => setShowMedia((value) => !value)} className="rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-input">{showMedia ? "Hide preview" : "Preview media"}</button>
                    {showMedia && <MediaPreview kind={draft.kind} url={draft.media_url} />}
                  </div>
                )}
              </div>
            </section>

            <aside className="min-w-0 border-t border-border pt-5 xl:border-l xl:border-t-0 xl:pl-6 xl:pt-0">
              <div>
                <span className="block text-xs text-text-muted">Rating</span>
                <div className="mt-1 flex items-center gap-1" role="group" aria-label="Rating">
                  {[1, 2, 3, 4, 5].map((value) => (
                    <button key={value} type="button" onClick={() => setDraft({ ...draft, rating: value })} className={`flex h-8 w-8 items-center justify-center rounded border text-base ${draft.rating && value <= draft.rating ? "border-yellow/60 bg-yellow/10 text-yellow" : "border-border text-text-dim hover:bg-bg-input"}`} aria-label={`${value} star${value === 1 ? "" : "s"}`} aria-pressed={draft.rating === value}>★</button>
                  ))}
                  {draft.rating && <button type="button" onClick={() => setDraft({ ...draft, rating: undefined })} className="ml-1 text-xs text-text-muted hover:text-text">Clear</button>}
                </div>
              </div>

              <div className="mt-4 grid grid-cols-2 gap-3">
                <Field label="Consent" id="testimonial-consent">
                  <select id="testimonial-consent" value={draft.consent_status} onChange={(event) => setDraft({ ...draft, consent_status: event.target.value as ConsentStatus })} className={inputClass}>
                    {consents.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
                  </select>
                </Field>
                <Field label="Permission" id="testimonial-permission">
                  <select id="testimonial-permission" value={draft.permission_scope} onChange={(event) => setDraft({ ...draft, permission_scope: event.target.value as PermissionScope })} className={inputClass}>
                    {scopes.map((value) => <option key={value} value={value}>{capitalize(value)}</option>)}
                  </select>
                </Field>
              </div>

              <div className="mt-4 space-y-3">
                <Field label="Author name" id="testimonial-author-name"><input id="testimonial-author-name" value={draft.author_name} maxLength={500} onChange={(event) => setDraft({ ...draft, author_name: event.target.value })} className={inputClass} /></Field>
                <Field label="Role" id="testimonial-author-role"><input id="testimonial-author-role" value={draft.author_role} maxLength={500} onChange={(event) => setDraft({ ...draft, author_role: event.target.value })} className={inputClass} /></Field>
                <Field label="Company" id="testimonial-author-company"><input id="testimonial-author-company" value={draft.author_company} maxLength={500} onChange={(event) => setDraft({ ...draft, author_company: event.target.value })} className={inputClass} /></Field>
                <Field label="Email" id="testimonial-author-email"><input id="testimonial-author-email" type="email" value={draft.author_email} maxLength={500} onChange={(event) => setDraft({ ...draft, author_email: event.target.value })} className={inputClass} /></Field>
                <Field label="Tags" id="testimonial-tags"><input id="testimonial-tags" value={tagText} onChange={(event) => setTagText(event.target.value)} placeholder="homepage, video" className={inputClass} /></Field>
              </div>
              {parseTags(tagText).length > 0 && <div className="mt-2 flex flex-wrap gap-1">{parseTags(tagText).map((tag) => <span key={tag} className="rounded bg-bg-card px-1.5 py-0.5 text-[11px] text-text-muted">{tag}</span>)}</div>}

              <button type="button" onClick={archive} disabled={!draft.id || saving} className="mt-5 w-full rounded border border-red/50 px-3 py-2 text-sm text-red hover:bg-red/10 disabled:opacity-40 sm:hidden">Archive</button>
            </aside>
          </div>
          </div>
        </div>
      </main>
    </div>
  );
}

function Field({ label, id, children }: { label: string; id: string; children: ReactNode }) {
  return <div className="min-w-0"><label htmlFor={id} className="mb-1 block text-xs text-text-muted">{label}</label>{children}</div>;
}

function MediaPreview({ kind, url }: { kind: Kind; url: string }) {
  if (kind === "video") return <video className="mt-3 max-h-80 w-full rounded border border-border bg-black" src={url} controls preload="metadata" />;
  if (kind === "audio") return <audio className="mt-3 w-full" src={url} controls preload="metadata" />;
  if (kind === "image") return <img className="mt-3 max-h-80 max-w-full rounded border border-border object-contain" src={url} alt="Testimonial media preview" />;
  return <a href={url} target="_blank" rel="noreferrer" className="mt-3 inline-block text-sm text-accent hover:underline">Open media</a>;
}

function copyTestimonial(testimonial: Testimonial): Testimonial {
  return { ...testimonial, tags: [...(testimonial.tags || [])], metadata: { ...(testimonial.metadata || {}) } };
}

function toPayload(testimonial: Testimonial, tagText: string) {
  return {
    status: testimonial.status,
    kind: testimonial.kind,
    source: testimonial.source || "manual",
    title: testimonial.title,
    quote: testimonial.quote,
    body: testimonial.body,
    rating: testimonial.rating || null,
    author_name: testimonial.author_name,
    author_role: testimonial.author_role,
    author_company: testimonial.author_company,
    author_email: testimonial.author_email,
    media_file_id: testimonial.media_file_id,
    media_url: testimonial.media_url,
    consent_status: testimonial.consent_status,
    permission_scope: testimonial.permission_scope,
    tags: parseTags(tagText),
    metadata: testimonial.metadata || {},
  };
}

function fingerprint(testimonial: Testimonial, tagText: string): string {
  return JSON.stringify(toPayload(testimonial, tagText));
}

function parseTags(value: string): string[] {
  return [...new Set(value.split(",").map((tag) => tag.trim()).filter(Boolean))].slice(0, 50);
}

function hasContent(payload: ReturnType<typeof toPayload>): boolean {
  return Boolean(payload.title.trim() || payload.quote.trim() || payload.body.trim() || payload.media_file_id.trim() || payload.media_url.trim());
}

function isPreviewableURL(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

function labelFor(testimonial: Testimonial): string {
  return testimonial.title || testimonial.quote || authorLine(testimonial) || testimonial.body || testimonial.media_url || "Untitled testimonial";
}

function authorLine(testimonial: Testimonial): string {
  return [testimonial.author_name, testimonial.author_role, testimonial.author_company].filter(Boolean).join(" · ");
}

function statusTone(status: Status): string {
  switch (status) {
    case "published": return "bg-green/15 text-green";
    case "approved": return "bg-accent/15 text-accent";
    case "rejected": return "bg-red/15 text-red";
    case "archived": return "bg-bg-card text-text-dim";
    case "submitted": return "bg-yellow/15 text-yellow";
    default: return "bg-bg-card text-text-muted";
  }
}

function capitalize(value: string): string {
  return value ? value[0].toUpperCase() + value.slice(1) : value;
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString();
}
