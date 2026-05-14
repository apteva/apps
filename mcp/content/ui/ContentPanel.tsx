// ContentPanel — v1 dashboard surface for the content app.
//
// Lists posts/pages, creates drafts, and publishes/archives. The full
// block editor is a follow-up PR; this panel exercises the data +
// render path while v1 ships. Talks to /api/apps/content/* through
// the platform proxy.
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

interface Post {
  id: number;
  kind: string;
  slug: string;
  status: string;
  title: string;
  updated_at?: string;
}

type Kind = "post" | "page";

// Small inline SVG icon set — no emojis in app UIs (proper icons only).
function Icon({ name }: { name: "plus" | "edit" | "eye" }) {
  const d: Record<string, string> = {
    plus: "M12 5v14M5 12h14",
    edit: "M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z",
    eye: "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8Zm11 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
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
      <path d={d[name]} />
    </svg>
  );
}

export default function ContentPanel({ projectId }: NativePanelProps) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [kind, setKind] = useState<Kind>("post");
  const [status, setStatus] = useState<string>("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draftTitle, setDraftTitle] = useState("");

  // api builds a proxied URL with the project scope threaded on.
  const api = useCallback(
    (path: string, opts: RequestInit = {}) => {
      const sep = path.includes("?") ? "&" : "?";
      const url = `/api/apps/content${path}${sep}project_id=${encodeURIComponent(projectId)}`;
      return fetch(url, {
        headers: { "Content-Type": "application/json" },
        ...opts,
      }).then(async (r) => {
        if (!r.ok) {
          const body = await r.text();
          throw new Error(`${r.status}: ${body.slice(0, 200)}`);
        }
        return r.json();
      });
    },
    [projectId],
  );

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    api(`/posts?kind=${kind}${status ? `&status=${status}` : ""}`)
      .then((r) => setPosts(r.posts ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, kind, status]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const createDraft = useCallback(() => {
    const title = draftTitle.trim();
    if (!title) return;
    api("/posts", {
      method: "POST",
      body: JSON.stringify({
        kind,
        title,
        blocks: [{ type: "core/paragraph", attrs: { text_md: "" } }],
      }),
    })
      .then(() => {
        setDraftTitle("");
        refresh();
      })
      .catch((e) => setError(String(e)));
  }, [api, draftTitle, kind, refresh]);

  const act = useCallback(
    (id: number, action: "publish" | "archive") => {
      api(`/posts/${id}/${action}`, { method: "POST" })
        .then(refresh)
        .catch((e) => setError(String(e)));
    },
    [api, refresh],
  );

  const empty = useMemo(() => !loading && posts.length === 0, [loading, posts]);

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
          <Icon name="plus" /> Create draft
        </button>
      </section>

      {error && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
      )}
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
                disabled
                title="Block editor — coming in v1.1"
                className="flex items-center gap-1 px-2 py-1 text-xs rounded border border-border opacity-50"
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
                href={`/api/apps/content/posts/${p.slug}?project_id=${encodeURIComponent(projectId)}`}
                target="_blank"
                rel="noreferrer"
                className="flex items-center px-2 py-1 text-xs rounded border border-border"
                title="View JSON"
              >
                <Icon name="eye" />
              </a>
            </div>
          </li>
        ))}
        {empty && (
          <li className="text-fg-muted py-8 text-center">
            No {kind}s yet — create one above.
          </li>
        )}
      </ul>

      <footer className="text-fg-muted text-xs pt-4 mt-4 border-t border-border">
        The block editor lands in v1.1. For now, post bodies are built through
        the MCP tools (blocks_insert, blocks_update, blocks_move) — the agent is
        the intended primary author.
      </footer>
    </div>
  );
}
