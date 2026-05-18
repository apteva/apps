// ContentPanel — dashboard surface for the content app.
//
// Two views: a list of posts/pages, and a block editor for one post.
// Talks to /api/apps/content/admin/* through the platform proxy
// (the proxy strips /api/apps/content, the sidecar mounts the REST
// surface under /admin/* so it doesn't collide with the public
// render namespace at / and /posts/:slug).
//
// Bundled to ContentPanel.mjs by apps/scripts/build-panels.ts, which
// externalizes `react` + `react/jsx-runtime` against the dashboard's
// importmap. The dashboard host imports the default export and mounts
// it — the panel must NOT self-mount.

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Block {
  id: string;
  type: string;
  attrs?: Record<string, any>;
  inner?: Block[];
}

interface Document {
  version: number;
  blocks: Block[];
}

interface Post {
  id: number;
  kind: string;
  slug: string;
  status: string;
  title: string;
  excerpt?: string;
  body_blocks?: Document;
  updated_at?: string;
}

interface BlockTypeInfo {
  name: string;
  display_name: string;
  category: string;
  description?: string;
  container?: boolean;
}

type Kind = "post" | "page";

// ── inline SVG icons (no emojis in app UIs) ─────────────────────
function Icon({ name }: { name: string }) {
  const d: Record<string, string> = {
    plus: "M12 5v14M5 12h14",
    edit: "M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z",
    eye: "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8Zm11 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
    arrowLeft: "M19 12H5M12 19l-7-7 7-7",
    arrowUp: "M12 19V5M5 12l7-7 7 7",
    arrowDown: "M12 5v14M19 12l-7 7-7-7",
    trash: "M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6",
    save: "M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2zM17 21v-8H7v8M7 3v5h8",
  };
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d={d[name] ?? ""} />
    </svg>
  );
}

// ── shared api helper (scoped via closure to the panel's project) ─
// makeAPI returns a fetcher pre-bound to a project and (optionally) a
// site slug. The site slug is appended as ?site=<slug> on every URL so
// every admin REST call lands on the right site.
function makeAPI(projectId: string, siteSlug?: string | null) {
  return async function api<T = any>(path: string, opts: RequestInit = {}): Promise<T> {
    const sep = path.includes("?") ? "&" : "?";
    let url = `/api/apps/content${path}${sep}project_id=${encodeURIComponent(projectId)}`;
    if (siteSlug) {
      url += `&site=${encodeURIComponent(siteSlug)}`;
    }
    const r = await fetch(url, {
      headers: { "Content-Type": "application/json" },
      ...opts,
    });
    if (!r.ok) {
      const body = await r.text();
      throw new Error(`${r.status}: ${body.slice(0, 200)}`);
    }
    return r.json();
  };
}

// ── default attrs per block type for newly-inserted blocks ────────
function defaultAttrs(type: string): Record<string, any> {
  switch (type) {
    case "core/heading":   return { level: 2, text: "Heading" };
    case "core/paragraph": return { text_md: "" };
    case "core/list":      return { style: "bullet", items: ["Item"] };
    case "core/quote":     return { citation: "" };
    case "core/code":      return { language: "", source: "" };
    case "core/embed":     return { url: "" };
    case "core/separator": return { style: "plain" };
    case "core/html":      return { source: "" };
    case "core/markdown":  return { source: "" };
    case "core/table":     return { header: [], rows: [] };
    case "core/button":    return { label: "Click", url: "#", style: "primary" };
    case "core/cta":       return { heading: "", body: "", button_label: "Learn more", button_url: "#" };
    case "core/image":     return { media_id: 0, alt: "", size: "inline" };
    case "core/gallery":   return { media_ids: [], columns: 3 };
    case "core/columns":   return {};
    case "core/group":     return {};
    default:               return {};
  }
}

// ── top-level panel ──────────────────────────────────────────────
//
// Three views:
//   - "list"      → the post/page index (default)
//   - "templates" → site-kit catalog (Finance Blog, SaaS Marketing, …)
//   - "editor"    → block editor for a specific post
//
// Switching between list and templates is via the top tab bar;
// opening the editor replaces the view entirely (own back button).

type View = "list" | "templates";

interface SiteSummary {
  id: number;
  slug: string;
  name: string;
  hostname?: string;
  is_default: boolean;
}

export default function ContentPanel({ projectId }: NativePanelProps) {
  const [sites, setSites] = useState<SiteSummary[]>([]);
  const [activeSite, setActiveSite] = useState<string | null>(null);
  const [sitesError, setSitesError] = useState<string | null>(null);

  // Sites list — fetch once per project. The active site defaults to
  // the default; user can switch via the dropdown. Refreshes whenever
  // a new site is created.
  const refreshSites = useCallback(() => {
    const url = `/api/apps/content/admin/sites?project_id=${encodeURIComponent(projectId)}`;
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`${r.status}`))))
      .then((r: { sites: SiteSummary[] | null }) => {
        const list = r.sites ?? [];
        setSites(list);
        // Pick the default site initially; sticky on user choice
        // through component lifetime.
        setActiveSite((curr) => {
          if (curr && list.find((s) => s.slug === curr)) return curr;
          const def = list.find((s) => s.is_default) ?? list[0];
          return def ? def.slug : null;
        });
        setSitesError(null);
      })
      .catch((e) => setSitesError(String(e)));
  }, [projectId]);
  useEffect(refreshSites, [refreshSites]);

  const api = useMemo(() => makeAPI(projectId, activeSite), [projectId, activeSite]);
  const [editing, setEditing] = useState<number | null>(null);
  const [view, setView] = useState<View>("list");

  if (editing != null) {
    return (
      <Editor
        api={api}
        projectId={projectId}
        postId={editing}
        onExit={() => setEditing(null)}
      />
    );
  }
  return (
    <div>
      <Tabs
        view={view}
        onChange={setView}
        sites={sites}
        activeSite={activeSite}
        onSiteChange={setActiveSite}
        onCreatedSite={refreshSites}
        projectId={projectId}
      />
      {sitesError && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 mx-4 mt-2">{sitesError}</div>
      )}
      {view === "list" && <ListView api={api} projectId={projectId} onOpen={setEditing} />}
      {view === "templates" && (
        <TemplatesView api={api} projectId={projectId} onApplied={() => setView("list")} />
      )}
    </div>
  );
}

function Tabs({
  view,
  onChange,
  sites,
  activeSite,
  onSiteChange,
  onCreatedSite,
  projectId,
}: {
  view: View;
  onChange: (v: View) => void;
  sites: SiteSummary[];
  activeSite: string | null;
  onSiteChange: (slug: string) => void;
  onCreatedSite: () => void;
  projectId: string;
}) {
  const [creating, setCreating] = useState(false);
  const tab = (id: View, label: string) => (
    <button
      key={id}
      onClick={() => onChange(id)}
      className={`px-4 py-2 text-sm border-b-2 ${
        view === id ? "border-fg font-semibold" : "border-transparent text-fg-muted"
      }`}
    >
      {label}
    </button>
  );
  return (
    <div className="flex items-center justify-between border-b border-border px-4 pt-2 bg-bg">
      <div className="flex gap-1">
        {tab("list", "Content")}
        {tab("templates", "Templates")}
      </div>
      {/* Site switcher — hidden when only one site exists (single-site UX). */}
      {sites.length >= 2 ? (
        <div className="flex items-center gap-2 pb-2">
          <span className="text-xs text-fg-muted">Site</span>
          <select
            value={activeSite ?? ""}
            onChange={(e) => onSiteChange(e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input text-sm"
          >
            {sites.map((s) => (
              <option key={s.slug} value={s.slug}>
                {s.name}{s.is_default ? " (default)" : ""}
                {s.hostname ? ` · ${s.hostname}` : ""}
              </option>
            ))}
          </select>
          <button
            onClick={() => setCreating(true)}
            className="px-2 py-1 text-xs rounded border border-border"
          >
            + New site
          </button>
        </div>
      ) : (
        // Single-site mode: show a discreet "+ Add second site" button
        // so users can discover multi-site without it being noisy.
        <div className="pb-2">
          <button
            onClick={() => setCreating(true)}
            className="text-xs text-fg-muted hover:text-fg"
          >
            + Add second site
          </button>
        </div>
      )}
      {creating && (
        <NewSiteDialog
          projectId={projectId}
          onClose={() => setCreating(false)}
          onCreated={(slug) => {
            setCreating(false);
            onCreatedSite();
            onSiteChange(slug);
          }}
        />
      )}
    </div>
  );
}

function NewSiteDialog({
  projectId,
  onClose,
  onCreated,
}: {
  projectId: string;
  onClose: () => void;
  onCreated: (slug: string) => void;
}) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [hostname, setHostname] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    if (!slug.trim()) {
      setError("slug required");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const url = `/api/apps/content/admin/sites?project_id=${encodeURIComponent(projectId)}`;
      const r = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug, name: name || slug, hostname }),
      });
      if (!r.ok) {
        const body = await r.text();
        throw new Error(`${r.status}: ${body.slice(0, 200)}`);
      }
      const out: { site: SiteSummary } = await r.json();
      onCreated(out.site.slug);
    } catch (e) {
      setError(String(e));
      setSubmitting(false);
    }
  };

  return (
    <div
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded p-4 w-96"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="font-semibold mb-3">New site</h3>
        <p className="text-xs text-fg-muted mb-3">
          Sites in the same project share templates but have their own posts,
          pages, menus, settings, and theme. Bind a hostname to make this site
          publicly addressable (requires the deploy app).
        </p>
        <label className="block mb-2">
          <span className="text-xs text-fg-muted">Slug (URL-safe id)</span>
          <input
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="e.g. blog"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
            autoFocus
          />
        </label>
        <label className="block mb-2">
          <span className="text-xs text-fg-muted">Display name</span>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Blog"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
          />
        </label>
        <label className="block mb-3">
          <span className="text-xs text-fg-muted">Hostname (optional)</span>
          <input
            type="text"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            placeholder="e.g. blog.example.com"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
          />
        </label>
        {error && (
          <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
        )}
        <div className="flex justify-end gap-2 mt-3">
          <button onClick={onClose} className="px-3 py-1 rounded border border-border">
            Cancel
          </button>
          <button
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1 rounded border border-border font-semibold disabled:opacity-50"
          >
            {submitting ? "Creating…" : "Create site"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── list view ───────────────────────────────────────────────────
function ListView({
  api,
  projectId,
  onOpen,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  onOpen: (id: number) => void;
}) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [kind, setKind] = useState<Kind>("post");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draftTitle, setDraftTitle] = useState("");

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    api<{ posts: Post[] | null }>(
      `/admin/posts?kind=${kind}${status ? `&status=${status}` : ""}`,
    )
      .then((r) => setPosts(r.posts ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, kind, status]);

  useEffect(refresh, [refresh]);

  const createDraft = () => {
    const title = draftTitle.trim();
    if (!title) return;
    api<{ post: Post }>("/admin/posts", {
      method: "POST",
      body: JSON.stringify({
        kind,
        title,
        blocks: [{ type: "core/paragraph", attrs: { text_md: "" } }],
      }),
    })
      .then((r) => {
        setDraftTitle("");
        onOpen(r.post.id);
      })
      .catch((e) => setError(String(e)));
  };

  const act = (id: number, action: "publish" | "archive") => {
    api(`/admin/posts/${id}/${action}`, { method: "POST" })
      .then(refresh)
      .catch((e) => setError(String(e)));
  };

  return (
    <div className="p-4 text-sm">
      <header className="flex items-center justify-between gap-4">
        <h2 className="text-base font-semibold">Content</h2>
        <div className="flex gap-2">
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as Kind)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="post">Posts</option>
            <option value="page">Pages</option>
          </select>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="">All statuses</option>
            <option value="draft">Draft</option>
            <option value="scheduled">Scheduled</option>
            <option value="published">Published</option>
            <option value="archived">Archived</option>
          </select>
        </div>
      </header>

      <section className="flex gap-2 my-4">
        <input
          type="text"
          value={draftTitle}
          placeholder={`New ${kind} title…`}
          onChange={(e) => setDraftTitle(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && createDraft()}
          className="flex-1 border border-border rounded px-2 py-1 bg-bg-input"
        />
        <button
          onClick={createDraft}
          className="flex items-center gap-1 px-3 py-1 rounded border border-border"
        >
          <Icon name="plus" /> New & edit
        </button>
      </section>

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}
      {loading && <div className="text-fg-muted py-4">Loading…</div>}

      <ul className="list-none p-0 m-0">
        {posts.map((p) => (
          <li
            key={p.id}
            className="flex items-center justify-between py-3 border-b border-border"
          >
            <div className="flex items-baseline gap-2">
              <strong>{p.title || <em>(untitled)</em>}</strong>
              <span className="text-xs text-fg-muted">{p.status}</span>
              <span className="text-xs text-fg-muted">
                /{p.kind === "post" ? "posts/" : ""}
                {p.slug}
              </span>
            </div>
            <div className="flex gap-1">
              <button
                onClick={() => onOpen(p.id)}
                className="flex items-center gap-1 px-2 py-1 text-xs rounded border border-border"
              >
                <Icon name="edit" /> Edit
              </button>
              {p.status !== "published" && (
                <button
                  onClick={() => act(p.id, "publish")}
                  className="px-2 py-1 text-xs rounded border border-border"
                >
                  Publish
                </button>
              )}
              {p.status !== "archived" && (
                <button
                  onClick={() => act(p.id, "archive")}
                  className="px-2 py-1 text-xs rounded border border-border"
                >
                  Archive
                </button>
              )}
              <a
                href={
                  p.status === "published"
                    ? `/api/apps/content/${p.kind === "post" ? "posts/" : ""}${p.slug}?project_id=${encodeURIComponent(projectId)}`
                    : `/api/apps/content/admin/posts/${p.id}?project_id=${encodeURIComponent(projectId)}`
                }
                target="_blank"
                rel="noreferrer"
                className="flex items-center px-2 py-1 text-xs rounded border border-border"
                title={p.status === "published" ? "View rendered page" : "View JSON (drafts can't render publicly)"}
              >
                <Icon name="eye" />
              </a>
            </div>
          </li>
        ))}
        {!loading && posts.length === 0 && (
          <li className="text-fg-muted py-8 text-center">
            No {kind}s yet — create one above.
          </li>
        )}
      </ul>
    </div>
  );
}

// ── editor view ─────────────────────────────────────────────────
function Editor({
  api,
  projectId,
  postId,
  onExit,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  postId: number;
  onExit: () => void;
}) {
  const [post, setPost] = useState<Post | null>(null);
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [types, setTypes] = useState<BlockTypeInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);

  // Fetch post + block types on mount.
  useEffect(() => {
    setLoading(true);
    Promise.all([
      api<{ post: Post }>(`/admin/posts/${postId}`),
      api<{ types: BlockTypeInfo[] }>(`/admin/block-types`),
    ])
      .then(([p, t]) => {
        setPost(p.post);
        setBlocks(p.post.body_blocks?.blocks ?? []);
        setTypes(t.types);
      })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, postId]);

  const setTitle = (title: string) => {
    if (!post) return;
    setPost({ ...post, title });
    setDirty(true);
  };
  const setExcerpt = (excerpt: string) => {
    if (!post) return;
    setPost({ ...post, excerpt });
    setDirty(true);
  };

  const replaceBlock = (idx: number, next: Block) => {
    setBlocks((prev) => prev.map((b, i) => (i === idx ? next : b)));
    setDirty(true);
  };
  const moveBlock = (idx: number, delta: number) => {
    setBlocks((prev) => {
      const next = [...prev];
      const j = idx + delta;
      if (j < 0 || j >= next.length) return prev;
      [next[idx], next[j]] = [next[j], next[idx]];
      return next;
    });
    setDirty(true);
  };
  const deleteBlock = (idx: number) => {
    setBlocks((prev) => prev.filter((_, i) => i !== idx));
    setDirty(true);
  };
  const insertBlockAt = (idx: number, type: string) => {
    setBlocks((prev) => {
      const next = [...prev];
      next.splice(idx, 0, { id: "", type, attrs: defaultAttrs(type) });
      return next;
    });
    setDirty(true);
  };

  const save = async () => {
    if (!post) return;
    setSaving(true);
    setError(null);
    try {
      await api(`/admin/posts/${postId}`, {
        method: "PATCH",
        body: JSON.stringify({
          title: post.title,
          excerpt: post.excerpt ?? "",
          blocks,
        }),
      });
      setDirty(false);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const publish = async () => {
    if (!post) return;
    if (dirty) {
      await save();
    }
    try {
      const r = await api<{ post: Post }>(`/admin/posts/${postId}/publish`, { method: "POST" });
      setPost(r.post);
    } catch (e) {
      setError(String(e));
    }
  };

  if (loading) return <div className="p-4 text-fg-muted">Loading…</div>;
  if (!post) return <div className="p-4">Post not found.</div>;

  return (
    <div className="p-4 text-sm">
      <header className="flex items-center justify-between gap-2 mb-3">
        <button
          onClick={onExit}
          className="flex items-center gap-1 px-2 py-1 rounded border border-border"
        >
          <Icon name="arrowLeft" /> Back
        </button>
        <div className="flex items-baseline gap-2">
          <span className="text-xs text-fg-muted">{post.status}</span>
          <span className="text-xs text-fg-muted">/{post.kind === "post" ? "posts/" : ""}{post.slug}</span>
        </div>
        <div className="flex gap-2">
          <button
            disabled={!dirty || saving}
            onClick={save}
            className="flex items-center gap-1 px-3 py-1 rounded border border-border disabled:opacity-50"
          >
            <Icon name="save" /> {saving ? "Saving…" : dirty ? "Save" : "Saved"}
          </button>
          <button
            onClick={publish}
            className="px-3 py-1 rounded border border-border"
          >
            {post.status === "published" ? "Republish" : "Publish"}
          </button>
        </div>
      </header>

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}

      <input
        type="text"
        value={post.title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Title"
        className="w-full text-2xl font-bold border-0 bg-transparent py-2 mb-2 focus:outline-none"
      />
      <input
        type="text"
        value={post.excerpt ?? ""}
        onChange={(e) => setExcerpt(e.target.value)}
        placeholder="Excerpt (optional)"
        className="w-full text-fg-muted border-0 bg-transparent py-1 mb-4 focus:outline-none"
      />

      <Insert types={types} onInsert={(t) => insertBlockAt(0, t)} />

      <div className="flex flex-col gap-3">
        {blocks.map((b, i) => (
          <div key={`${i}-${b.id || b.type}`}>
            <BlockCard
              block={b}
              onChange={(nb) => replaceBlock(i, nb)}
              onMoveUp={i > 0 ? () => moveBlock(i, -1) : undefined}
              onMoveDown={i < blocks.length - 1 ? () => moveBlock(i, +1) : undefined}
              onDelete={() => deleteBlock(i)}
            />
            <Insert types={types} onInsert={(t) => insertBlockAt(i + 1, t)} />
          </div>
        ))}
        {blocks.length === 0 && (
          <div className="text-fg-muted text-center py-8">Empty post — add a block above.</div>
        )}
      </div>
    </div>
  );
}

// ── insertion bar ────────────────────────────────────────────
function Insert({
  types,
  onInsert,
}: {
  types: BlockTypeInfo[];
  onInsert: (type: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const grouped = useMemo(() => {
    const out: Record<string, BlockTypeInfo[]> = {};
    for (const t of types) {
      (out[t.category] ??= []).push(t);
    }
    return out;
  }, [types]);

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-1 text-xs text-fg-muted py-1 px-2 my-1 rounded border border-dashed border-border hover:text-fg"
      >
        <Icon name="plus" /> Add block
      </button>
    );
  }
  return (
    <div className="border border-border rounded p-2 my-2 bg-bg-input">
      <div className="flex justify-between items-center mb-2">
        <span className="text-xs text-fg-muted">Insert block</span>
        <button onClick={() => setOpen(false)} className="text-xs text-fg-muted">close</button>
      </div>
      {Object.entries(grouped).map(([cat, ts]) => (
        <div key={cat} className="mb-2">
          <div className="text-xs uppercase text-fg-muted mb-1">{cat}</div>
          <div className="flex flex-wrap gap-1">
            {ts.map((t) => (
              <button
                key={t.name}
                onClick={() => {
                  onInsert(t.name);
                  setOpen(false);
                }}
                title={t.description ?? ""}
                className="px-2 py-1 text-xs rounded border border-border"
              >
                {t.display_name}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// ── per-block card ───────────────────────────────────────────
function BlockCard({
  block,
  onChange,
  onMoveUp,
  onMoveDown,
  onDelete,
}: {
  block: Block;
  onChange: (nb: Block) => void;
  onMoveUp?: () => void;
  onMoveDown?: () => void;
  onDelete: () => void;
}) {
  const setAttr = (key: string, value: any) => {
    onChange({ ...block, attrs: { ...(block.attrs ?? {}), [key]: value } });
  };

  return (
    <div className="border border-border rounded p-3">
      <div className="flex justify-between items-center mb-2">
        <span className="text-xs text-fg-muted">{block.type}</span>
        <div className="flex gap-1">
          {onMoveUp && (
            <button onClick={onMoveUp} title="Move up" className="px-1 py-1 rounded border border-border">
              <Icon name="arrowUp" />
            </button>
          )}
          {onMoveDown && (
            <button onClick={onMoveDown} title="Move down" className="px-1 py-1 rounded border border-border">
              <Icon name="arrowDown" />
            </button>
          )}
          <button onClick={onDelete} title="Delete" className="px-1 py-1 rounded border border-border">
            <Icon name="trash" />
          </button>
        </div>
      </div>
      <BlockEditor block={block} setAttr={setAttr} />
    </div>
  );
}

// ── per-type inline editors ─────────────────────────────────
function BlockEditor({
  block,
  setAttr,
}: {
  block: Block;
  setAttr: (key: string, value: any) => void;
}) {
  const a = block.attrs ?? {};
  const input =
    "w-full border border-border rounded px-2 py-1 bg-bg-input";
  const textarea =
    "w-full border border-border rounded px-2 py-1 bg-bg-input font-mono text-xs";

  switch (block.type) {
    case "core/heading":
      return (
        <div className="flex gap-2">
          <select
            value={a.level ?? 2}
            onChange={(e) => setAttr("level", Number(e.target.value))}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            {[1, 2, 3, 4, 5, 6].map((n) => (
              <option key={n} value={n}>H{n}</option>
            ))}
          </select>
          <input
            type="text"
            value={a.text ?? ""}
            onChange={(e) => setAttr("text", e.target.value)}
            placeholder="Heading text"
            className={input}
          />
        </div>
      );

    case "core/paragraph":
      return (
        <textarea
          rows={3}
          value={a.text_md ?? ""}
          onChange={(e) => setAttr("text_md", e.target.value)}
          placeholder="Paragraph text (markdown — *bold*, _italic_, [link](url))"
          className={textarea}
        />
      );

    case "core/list": {
      const items: string[] = Array.isArray(a.items) ? a.items : [];
      return (
        <div className="flex flex-col gap-1">
          <select
            value={a.style ?? "bullet"}
            onChange={(e) => setAttr("style", e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input self-start"
          >
            <option value="bullet">Bullet</option>
            <option value="number">Numbered</option>
          </select>
          {items.map((it, idx) => (
            <div key={idx} className="flex gap-1">
              <input
                type="text"
                value={it}
                onChange={(e) => {
                  const next = [...items];
                  next[idx] = e.target.value;
                  setAttr("items", next);
                }}
                className={input}
              />
              <button
                onClick={() => setAttr("items", items.filter((_, i) => i !== idx))}
                className="px-2 py-1 rounded border border-border"
              >
                <Icon name="trash" />
              </button>
            </div>
          ))}
          <button
            onClick={() => setAttr("items", [...items, ""])}
            className="self-start px-2 py-1 text-xs rounded border border-border"
          >
            + Item
          </button>
        </div>
      );
    }

    case "core/quote":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.citation ?? ""}
            onChange={(e) => setAttr("citation", e.target.value)}
            placeholder="Citation (optional)"
            className={input}
          />
          <div className="text-xs text-fg-muted">
            Quote body comes from nested blocks (add inside via MCP for now).
          </div>
        </div>
      );

    case "core/code":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.language ?? ""}
            onChange={(e) => setAttr("language", e.target.value)}
            placeholder="Language (e.g. go, js, py)"
            className={input}
          />
          <textarea
            rows={6}
            value={a.source ?? ""}
            onChange={(e) => setAttr("source", e.target.value)}
            placeholder="Code"
            className={textarea}
          />
        </div>
      );

    case "core/embed":
      return (
        <input
          type="text"
          value={a.url ?? ""}
          onChange={(e) => setAttr("url", e.target.value)}
          placeholder="Embed URL (YouTube, Twitter, etc.)"
          className={input}
        />
      );

    case "core/separator":
      return (
        <select
          value={a.style ?? "plain"}
          onChange={(e) => setAttr("style", e.target.value)}
          className="border border-border rounded px-2 py-1 bg-bg-input"
        >
          <option value="plain">Plain</option>
          <option value="wide">Wide</option>
          <option value="dots">Dots</option>
        </select>
      );

    case "core/html":
      return (
        <textarea
          rows={6}
          value={a.source ?? ""}
          onChange={(e) => setAttr("source", e.target.value)}
          placeholder="<div>HTML (sanitized at render)</div>"
          className={textarea}
        />
      );

    case "core/markdown":
      return (
        <textarea
          rows={8}
          value={a.source ?? ""}
          onChange={(e) => setAttr("source", e.target.value)}
          placeholder="# Heading\n\nMulti-paragraph markdown source."
          className={textarea}
        />
      );

    case "core/button":
      return (
        <div className="flex gap-2">
          <input
            type="text"
            value={a.label ?? ""}
            onChange={(e) => setAttr("label", e.target.value)}
            placeholder="Button label"
            className={input}
          />
          <input
            type="text"
            value={a.url ?? ""}
            onChange={(e) => setAttr("url", e.target.value)}
            placeholder="URL"
            className={input}
          />
          <select
            value={a.style ?? "primary"}
            onChange={(e) => setAttr("style", e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="primary">Primary</option>
            <option value="secondary">Secondary</option>
            <option value="ghost">Ghost</option>
          </select>
        </div>
      );

    case "core/cta":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.heading ?? ""}
            onChange={(e) => setAttr("heading", e.target.value)}
            placeholder="CTA heading"
            className={input}
          />
          <textarea
            rows={2}
            value={a.body ?? ""}
            onChange={(e) => setAttr("body", e.target.value)}
            placeholder="CTA body"
            className={textarea}
          />
          <div className="flex gap-2">
            <input
              type="text"
              value={a.button_label ?? ""}
              onChange={(e) => setAttr("button_label", e.target.value)}
              placeholder="Button label"
              className={input}
            />
            <input
              type="text"
              value={a.button_url ?? ""}
              onChange={(e) => setAttr("button_url", e.target.value)}
              placeholder="Button URL"
              className={input}
            />
          </div>
        </div>
      );

    case "core/image":
      return (
        <div className="flex flex-col gap-1">
          <div className="flex gap-2">
            <input
              type="number"
              value={a.media_id ?? 0}
              onChange={(e) => setAttr("media_id", Number(e.target.value))}
              placeholder="media_id"
              className={input}
            />
            <select
              value={a.size ?? "inline"}
              onChange={(e) => setAttr("size", e.target.value)}
              className="border border-border rounded px-2 py-1 bg-bg-input"
            >
              <option value="inline">Inline</option>
              <option value="wide">Wide</option>
              <option value="full">Full</option>
            </select>
          </div>
          <input
            type="text"
            value={a.alt ?? ""}
            onChange={(e) => setAttr("alt", e.target.value)}
            placeholder="Alt text"
            className={input}
          />
          <input
            type="text"
            value={a.caption ?? ""}
            onChange={(e) => setAttr("caption", e.target.value)}
            placeholder="Caption (optional)"
            className={input}
          />
          <div className="text-xs text-fg-muted">
            Upload media via the media library (coming v1.1). For now,
            media_id refers to an already-uploaded row.
          </div>
        </div>
      );

    case "core/columns":
    case "core/group":
      return (
        <div className="text-xs text-fg-muted">
          Container block — nested blocks edited via MCP tools in v1.0.
          {block.inner && block.inner.length > 0 && (
            <span> ({block.inner.length} inside)</span>
          )}
        </div>
      );

    default:
      // Unknown / cross-app block: show the attrs as JSON.
      return (
        <textarea
          rows={4}
          value={JSON.stringify(block.attrs ?? {}, null, 2)}
          onChange={(e) => {
            try {
              const next = JSON.parse(e.target.value);
              if (typeof next === "object" && next != null && !Array.isArray(next)) {
                Object.entries(next as Record<string, any>).forEach(([k, v]) => setAttr(k, v));
              }
            } catch {
              // ignore until valid
            }
          }}
          className={textarea}
        />
      );
  }
}

// ── templates view (site-kit catalog) ─────────────────────────────

interface TemplateListItem {
  name: string;
  display_name: string;
  version: string;
  description: string;
  tags: string[] | null;
  source: string;
}

interface ApplySummary {
  template: string;
  version: string;
  mode: string;
  created: Record<string, number>;
  skipped: Record<string, number>;
  homepage_pinned: boolean;
  warnings?: string[];
  dry_run?: boolean;
  would_refuse?: boolean;
  refuse_reason?: string;
  existing_count?: number;
}

function TemplatesView({
  api,
  projectId: _projectId,
  onApplied,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  onApplied: () => void;
}) {
  const [templates, setTemplates] = useState<TemplateListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [picked, setPicked] = useState<TemplateListItem | null>(null);

  useEffect(() => {
    setLoading(true);
    api<{ templates: TemplateListItem[] | null }>("/admin/templates")
      .then((r) => setTemplates(r.templates ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api]);

  if (picked) {
    return (
      <ApplyTemplateDialog
        api={api}
        template={picked}
        onClose={() => setPicked(null)}
        onApplied={() => {
          setPicked(null);
          onApplied();
        }}
      />
    );
  }

  return (
    <div className="p-4 text-sm">
      <header className="flex items-baseline justify-between mb-3">
        <h2 className="text-base font-semibold">Templates</h2>
        <p className="text-xs text-fg-muted">
          Apply a starter to populate your site with pages, posts, terms, and menus.
        </p>
      </header>

      {error && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
      )}
      {loading && <div className="text-fg-muted py-4">Loading…</div>}

      <ul className="grid grid-cols-1 md:grid-cols-2 gap-3 list-none p-0">
        {templates.map((t) => (
          <li key={t.name} className="border border-border rounded p-3 flex flex-col">
            <div className="flex items-baseline justify-between gap-2">
              <h3 className="font-semibold">{t.display_name}</h3>
              <span className="text-xs text-fg-muted">v{t.version}</span>
            </div>
            <p className="text-fg-muted text-sm flex-1 mt-1">{t.description}</p>
            <div className="flex flex-wrap gap-1 mt-2">
              {(t.tags ?? []).map((tag) => (
                <span key={tag} className="text-xs px-2 py-0.5 rounded bg-bg-input border border-border">
                  {tag}
                </span>
              ))}
              <span className="text-xs px-2 py-0.5 rounded bg-bg-input border border-border ml-auto">
                {t.source}
              </span>
            </div>
            <div className="flex gap-2 mt-3">
              <button
                onClick={() => setPicked(t)}
                className="flex-1 px-3 py-1 rounded border border-border font-medium"
              >
                Apply
              </button>
            </div>
          </li>
        ))}
        {!loading && templates.length === 0 && (
          <li className="text-fg-muted text-center py-8 col-span-full">No templates available.</li>
        )}
      </ul>
    </div>
  );
}

function ApplyTemplateDialog({
  api,
  template,
  onClose,
  onApplied,
}: {
  api: ReturnType<typeof makeAPI>;
  template: TemplateListItem;
  onClose: () => void;
  onApplied: () => void;
}) {
  const [mode, setMode] = useState<"empty_only" | "append" | "overwrite">("empty_only");
  const [preview, setPreview] = useState<ApplySummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ApplySummary | null>(null);

  useEffect(() => {
    setLoading(true);
    setError(null);
    api<{ summary: ApplySummary }>(`/admin/templates/${template.name}/preview?mode=${mode}`)
      .then((r) => setPreview(r.summary))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, template.name, mode]);

  const apply = async () => {
    setApplying(true);
    setError(null);
    try {
      const r = await api<{ summary: ApplySummary }>(`/admin/templates/${template.name}/apply`, {
        method: "POST",
        body: JSON.stringify({ mode }),
      });
      setResult(r.summary);
    } catch (e) {
      setError(String(e));
    } finally {
      setApplying(false);
    }
  };

  if (result) {
    return (
      <div className="p-4 text-sm">
        <h2 className="text-base font-semibold mb-3">Applied — {template.display_name}</h2>
        <SummaryTable s={result} />
        <button onClick={onApplied} className="mt-4 px-3 py-1 rounded border border-border">
          Back to content
        </button>
      </div>
    );
  }

  return (
    <div className="p-4 text-sm">
      <header className="flex items-baseline justify-between mb-3">
        <h2 className="text-base font-semibold">Apply — {template.display_name}</h2>
        <button onClick={onClose} className="text-fg-muted text-xs">close</button>
      </header>

      <p className="text-fg-muted mb-3">{template.description}</p>

      <label className="block mb-3">
        <span className="text-xs text-fg-muted">Mode</span>
        <select
          value={mode}
          onChange={(e) => setMode(e.target.value as typeof mode)}
          className="block mt-1 border border-border rounded px-2 py-1 bg-bg-input"
        >
          <option value="empty_only">Empty only — refuse if site has content</option>
          <option value="append">Append — add only missing slugs</option>
          <option value="overwrite">Overwrite — replace by slug</option>
        </select>
      </label>

      <div className="border border-border rounded p-3 my-3">
        <p className="text-xs text-fg-muted mb-2">Will create:</p>
        {loading && <p className="text-fg-muted">Loading preview…</p>}
        {preview && <SummaryTable s={preview} />}
      </div>

      {preview?.would_refuse && (
        <div className="bg-amber-100 text-amber-900 rounded px-3 py-2 my-2 flex items-center justify-between gap-2">
          <span>{preview.refuse_reason}</span>
          <button
            onClick={() => setMode("append")}
            className="text-xs px-2 py-1 rounded border border-amber-300 bg-white whitespace-nowrap"
          >
            Switch to append
          </button>
        </div>
      )}

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}

      <div className="flex gap-2 mt-3">
        <button onClick={onClose} className="px-3 py-1 rounded border border-border">
          Cancel
        </button>
        <button
          onClick={apply}
          disabled={loading || applying || Boolean(preview?.would_refuse)}
          className="px-3 py-1 rounded border border-border font-semibold disabled:opacity-50"
          title={preview?.would_refuse ? "Change mode to enable Apply" : undefined}
        >
          {applying ? "Applying…" : "Apply"}
        </button>
      </div>
    </div>
  );
}

function SummaryTable({ s }: { s: ApplySummary }) {
  const entries = Object.entries(s.created || {}).filter(([_, n]) => n > 0);
  return (
    <ul className="list-none p-0 m-0">
      {entries.map(([kind, n]) => (
        <li key={kind} className="flex justify-between py-0.5">
          <span>{kind}</span>
          <strong>{n}</strong>
        </li>
      ))}
      {s.homepage_pinned && (
        <li className="flex justify-between py-0.5">
          <span>homepage pinned</span>
          <strong>yes</strong>
        </li>
      )}
      {(s.warnings ?? []).length > 0 && (
        <li className="mt-2">
          <p className="text-xs text-fg-muted">Warnings:</p>
          <ul className="pl-4 text-xs text-fg-muted list-disc">
            {s.warnings!.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </li>
      )}
    </ul>
  );
}
