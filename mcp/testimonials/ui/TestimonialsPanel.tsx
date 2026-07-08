import { useCallback, useEffect, useMemo, useState } from "react";

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

type Status = "draft" | "submitted" | "approved" | "rejected" | "published" | "archived";
type Kind = "text" | "review" | "video" | "audio" | "image";
type ConsentStatus = "unknown" | "granted" | "denied" | "revoked";
type PermissionScope = "private" | "internal" | "public" | "marketing";

const API = "/api/apps/testimonials";
const statuses: Status[] = ["draft", "submitted", "approved", "rejected", "published", "archived"];
const kinds: Kind[] = ["text", "review", "video", "audio", "image"];
const consents: ConsentStatus[] = ["unknown", "granted", "denied", "revoked"];
const scopes: PermissionScope[] = ["private", "internal", "public", "marketing"];

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
  const [selected, setSelected] = useState<Testimonial | null>(null);
  const [draft, setDraft] = useState<Testimonial>(blank);
  const [statusFilter, setStatusFilter] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [tagText, setTagText] = useState("");
  const selectedId = selected?.id || 0;

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

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const params: Record<string, string> = {};
      if (statusFilter) params.status = statusFilter;
      if (kindFilter) params.kind = kindFilter;
      if (query.trim()) params.q = query.trim();
      const data = await api<{ testimonials: Testimonial[] }>(
        "GET",
        `/testimonials?${new URLSearchParams(params).toString()}`,
      );
      const list = data.testimonials || [];
      setItems(list);
      if (selectedId) {
        const fresh = list.find((x) => x.id === selectedId);
        if (fresh) {
          setSelected(fresh);
          setDraft(copyTestimonial(fresh));
          setTagText((fresh.tags || []).join(", "));
        }
      } else if (list[0]) {
        setSelected(list[0]);
        setDraft(copyTestimonial(list[0]));
        setTagText((list[0].tags || []).join(", "));
      }
      setStatus(`${list.length} testimonial${list.length === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [api, kindFilter, query, selectedId, statusFilter]);

  useEffect(() => {
    load();
  }, [load]);

  const visibleTitle = useMemo(() => labelFor(draft), [draft]);

  function startNew() {
    setSelected(null);
    setDraft(copyTestimonial(blank));
    setTagText("");
  }

  function selectItem(item: Testimonial) {
    setSelected(item);
    setDraft(copyTestimonial(item));
    setTagText((item.tags || []).join(", "));
  }

  async function save() {
    const payload = toPayload(draft, tagText);
    setBusy(true);
    try {
      if (draft.id) {
        const data = await api<{ testimonial: Testimonial }>("PATCH", `/testimonials/${draft.id}`, payload);
        setSelected(data.testimonial);
        setDraft(copyTestimonial(data.testimonial));
        setTagText((data.testimonial.tags || []).join(", "));
        setStatus("Saved");
      } else {
        const data = await api<{ testimonial: Testimonial }>("POST", "/testimonials", payload);
        setSelected(data.testimonial);
        setDraft(copyTestimonial(data.testimonial));
        setTagText((data.testimonial.tags || []).join(", "));
        setStatus("Created");
      }
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function setLifecycle(next: Status) {
    if (!draft.id) {
      setDraft({ ...draft, status: next });
      return;
    }
    setBusy(true);
    try {
      const data = await api<{ testimonial: Testimonial }>("POST", `/testimonials/${draft.id}/status`, { status: next });
      setSelected(data.testimonial);
      setDraft(copyTestimonial(data.testimonial));
      setStatus(`Status: ${next}`);
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function archive() {
    if (!draft.id) return;
    setBusy(true);
    try {
      await api("DELETE", `/testimonials/${draft.id}`);
      setSelected(null);
      setDraft(copyTestimonial(blank));
      setTagText("");
      await load();
      setStatus("Archived");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="h-full min-h-0 grid grid-cols-[340px_minmax(460px,1fr)] bg-bg text-text">
      <aside className="min-h-0 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border">
          <div className="flex items-center gap-2">
            <div className="text-sm font-medium flex-1">Testimonials</div>
            <button onClick={startNew} className="w-8 h-8 border border-border rounded text-sm hover:bg-bg-input" title="New testimonial">
              +
            </button>
          </div>
          <div className="mt-3 grid grid-cols-2 gap-2">
            <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
              <option value="">All statuses</option>
              {statuses.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <select value={kindFilter} onChange={(e) => setKindFilter(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
              <option value="">All kinds</option>
              {kinds.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
          </div>
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search"
            className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {items.length === 0 ? (
            <div className="p-4 text-sm text-text-muted">No testimonials.</div>
          ) : (
            items.map((item) => (
              <button
                key={item.id}
                onClick={() => selectItem(item)}
                className={`block w-full text-left px-3 py-3 border-b border-border/60 hover:bg-bg-input ${selected?.id === item.id ? "bg-bg-input" : ""}`}
              >
                <div className="flex items-center gap-2">
                  <div className="min-w-0 flex-1 truncate text-sm font-medium">{labelFor(item)}</div>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusTone(item.status)}`}>{item.status}</span>
                </div>
                <div className="mt-1 text-xs text-text-muted truncate">
                  {authorLine(item) || item.kind}
                  {item.rating ? ` · ${item.rating}/5` : ""}
                </div>
                <div className="mt-1 text-xs text-text-dim line-clamp-2">{item.quote || item.body || item.media_url || item.media_file_id}</div>
              </button>
            ))
          )}
        </div>
        <div className="p-2 border-t border-border text-xs text-text-dim">{busy ? "Working..." : status}</div>
      </aside>

      <main className="min-h-0 flex flex-col">
        <header className="h-14 border-b border-border px-4 flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium">{visibleTitle || "New testimonial"}</div>
            <div className="text-xs text-text-muted">
              {draft.id ? `#${draft.id} · ${draft.kind}` : "Unsaved"}
              {draft.updated_at ? ` · updated ${formatDate(draft.updated_at)}` : ""}
            </div>
          </div>
          <button onClick={save} disabled={busy} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Save</button>
          <button onClick={archive} disabled={!draft.id || busy} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-40">Archive</button>
        </header>

        <div className="min-h-0 flex-1 overflow-auto p-4">
          <div className="grid grid-cols-[1fr_160px_160px_160px] gap-3">
            <input value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} placeholder="Title" className="bg-bg-input border border-border rounded px-3 py-2 text-sm" />
            <select value={draft.status} onChange={(e) => setLifecycle(e.target.value as Status)} className="bg-bg-input border border-border rounded px-2 py-2 text-sm">
              {statuses.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
            <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as Kind })} className="bg-bg-input border border-border rounded px-2 py-2 text-sm">
              {kinds.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
            <input value={draft.source} onChange={(e) => setDraft({ ...draft, source: e.target.value })} placeholder="Source" className="bg-bg-input border border-border rounded px-3 py-2 text-sm" />
          </div>

          <div className="mt-4 grid grid-cols-[minmax(0,1fr)_280px] gap-4">
            <section className="min-w-0">
              <label className="text-xs text-text-muted">Quote</label>
              <textarea value={draft.quote} onChange={(e) => setDraft({ ...draft, quote: e.target.value })} className="mt-1 w-full h-28 resize-none bg-bg-input border border-border rounded p-3 text-sm leading-5" />

              <label className="mt-4 block text-xs text-text-muted">Body</label>
              <textarea value={draft.body} onChange={(e) => setDraft({ ...draft, body: e.target.value })} className="mt-1 w-full h-44 resize-none bg-bg-input border border-border rounded p-3 text-sm leading-5" />

              <div className="mt-4 grid grid-cols-2 gap-3">
                <div>
                  <label className="text-xs text-text-muted">Media file ID</label>
                  <input value={draft.media_file_id} onChange={(e) => setDraft({ ...draft, media_file_id: e.target.value })} className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
                </div>
                <div>
                  <label className="text-xs text-text-muted">Media URL</label>
                  <input value={draft.media_url} onChange={(e) => setDraft({ ...draft, media_url: e.target.value })} className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
                </div>
              </div>
            </section>

            <aside className="min-w-0">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="text-xs text-text-muted">Rating</label>
                  <input type="number" min={1} max={5} value={draft.rating || ""} onChange={(e) => setDraft({ ...draft, rating: e.target.value ? Number(e.target.value) : undefined })} className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
                </div>
                <div>
                  <label className="text-xs text-text-muted">Consent</label>
                  <select value={draft.consent_status} onChange={(e) => setDraft({ ...draft, consent_status: e.target.value as ConsentStatus })} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">
                    {consents.map((c) => <option key={c} value={c}>{c}</option>)}
                  </select>
                </div>
              </div>

              <label className="mt-3 block text-xs text-text-muted">Permission</label>
              <select value={draft.permission_scope} onChange={(e) => setDraft({ ...draft, permission_scope: e.target.value as PermissionScope })} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">
                {scopes.map((s) => <option key={s} value={s}>{s}</option>)}
              </select>

              <label className="mt-3 block text-xs text-text-muted">Author</label>
              <input value={draft.author_name} onChange={(e) => setDraft({ ...draft, author_name: e.target.value })} placeholder="Name" className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
              <input value={draft.author_role} onChange={(e) => setDraft({ ...draft, author_role: e.target.value })} placeholder="Role" className="mt-2 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
              <input value={draft.author_company} onChange={(e) => setDraft({ ...draft, author_company: e.target.value })} placeholder="Company" className="mt-2 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
              <input value={draft.author_email} onChange={(e) => setDraft({ ...draft, author_email: e.target.value })} placeholder="Email" className="mt-2 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />

              <label className="mt-3 block text-xs text-text-muted">Tags</label>
              <input value={tagText} onChange={(e) => setTagText(e.target.value)} placeholder="homepage, video" className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm" />
            </aside>
          </div>
        </div>
      </main>
    </div>
  );
}

function copyTestimonial(t: Testimonial): Testimonial {
  return { ...t, tags: [...(t.tags || [])], metadata: { ...(t.metadata || {}) } };
}

function toPayload(t: Testimonial, tagText: string) {
  return {
    status: t.status,
    kind: t.kind,
    source: t.source || "manual",
    title: t.title,
    quote: t.quote,
    body: t.body,
    rating: t.rating || null,
    author_name: t.author_name,
    author_role: t.author_role,
    author_company: t.author_company,
    author_email: t.author_email,
    media_file_id: t.media_file_id,
    media_url: t.media_url,
    consent_status: t.consent_status,
    permission_scope: t.permission_scope,
    tags: tagText.split(",").map((x) => x.trim()).filter(Boolean),
    metadata: t.metadata || {},
  };
}

function labelFor(t: Testimonial): string {
  return t.title || t.quote || authorLine(t) || t.body || t.media_url || "Untitled testimonial";
}

function authorLine(t: Testimonial): string {
  return [t.author_name, t.author_role, t.author_company].filter(Boolean).join(" · ");
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

function formatDate(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString();
}
