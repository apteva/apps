// Content panel — v1 minimal stub.
//
// Lists posts/pages, lets the operator create a draft, and links to
// the (not yet built) block editor. The full block editor is a
// follow-up PR — this panel ensures the manifest's project.page slot
// has something useful to render while v1 ships the data + render
// path.
//
// The dashboard host loads this panel as a fallback iframe (per the
// manifest's `ui_panels[].entry`), and passes auth + project context
// through props. For the v1 stub we use the REST surface directly.

import React, { useEffect, useMemo, useState } from "https://esm.sh/react@19";
import { createRoot } from "https://esm.sh/react-dom@19/client";

function api(path, opts = {}) {
  const url = `/api/apps/content${path}${path.includes("?") ? "&" : "?"}project_id=${
    window.__APTEVA_PROJECT_ID__ || ""
  }`;
  return fetch(url, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  }).then((r) => {
    if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
    return r.json();
  });
}

function Icon({ name }) {
  const paths = {
    plus: "M12 5v14M5 12h14",
    edit: "M12 20h9M16.5 3.5a2.12 2.12 0 013 3L7 19l-4 1 1-4L16.5 3.5z",
    eye: "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z M12 9a3 3 0 100 6 3 3 0 000-6z",
  };
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none"
         stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d={paths[name] || ""} />
    </svg>
  );
}

function Panel() {
  const [posts, setPosts] = useState([]);
  const [kind, setKind] = useState("post");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);
  const [draftTitle, setDraftTitle] = useState("");

  const refresh = () => {
    setLoading(true);
    setError(null);
    api(`/posts?kind=${kind}${status ? `&status=${status}` : ""}`)
      .then((r) => setPosts(r.posts || []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  };

  useEffect(refresh, [kind, status]);

  const createDraft = () => {
    if (!draftTitle.trim()) return;
    api("/posts", {
      method: "POST",
      body: JSON.stringify({
        kind,
        title: draftTitle,
        blocks: [
          { type: "core/paragraph", attrs: { text_md: "" } },
        ],
      }),
    })
      .then(() => {
        setDraftTitle("");
        refresh();
      })
      .catch((e) => setError(String(e)));
  };

  const publish = (id) => {
    api(`/posts/${id}/publish`, { method: "POST" }).then(refresh).catch((e) => setError(String(e)));
  };
  const archive = (id) => {
    api(`/posts/${id}/archive`, { method: "POST" }).then(refresh).catch((e) => setError(String(e)));
  };

  return (
    <div className="content-panel">
      <header className="content-panel__header">
        <h2>Content</h2>
        <div className="content-panel__filters">
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="post">Posts</option>
            <option value="page">Pages</option>
          </select>
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All statuses</option>
            <option value="draft">Draft</option>
            <option value="scheduled">Scheduled</option>
            <option value="published">Published</option>
            <option value="archived">Archived</option>
          </select>
        </div>
      </header>

      <section className="content-panel__create">
        <input
          type="text"
          value={draftTitle}
          placeholder={`New ${kind} title…`}
          onChange={(e) => setDraftTitle(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && createDraft()}
        />
        <button onClick={createDraft}>
          <Icon name="plus" /> Create draft
        </button>
      </section>

      {error && <div className="content-panel__error">{error}</div>}
      {loading && <div className="content-panel__loading">Loading…</div>}

      <ul className="content-panel__list">
        {posts.map((p) => (
          <li key={p.id}>
            <div className="row">
              <div className="meta">
                <strong>{p.title || <em>(untitled)</em>}</strong>
                <span className="status status-{p.status}">{p.status}</span>
                <span className="slug">/{p.kind === "post" ? "posts/" : ""}{p.slug}</span>
              </div>
              <div className="actions">
                <button title="Block editor — coming in v1.1" disabled>
                  <Icon name="edit" /> Edit
                </button>
                {p.status !== "published" && (
                  <button onClick={() => publish(p.id)}>Publish</button>
                )}
                {p.status !== "archived" && (
                  <button onClick={() => archive(p.id)}>Archive</button>
                )}
                <a href={`/api/apps/content/posts/${p.slug}`} target="_blank" rel="noreferrer">
                  <Icon name="eye" />
                </a>
              </div>
            </div>
          </li>
        ))}
        {!loading && posts.length === 0 && (
          <li className="empty">No {kind}s yet — create one above.</li>
        )}
      </ul>

      <footer className="content-panel__footer">
        Block editor lands in v1.1. For now, use the MCP tools (blocks_insert,
        blocks_update, blocks_move) to build post bodies; the agent's the
        intended primary author.
      </footer>
    </div>
  );
}

const style = document.createElement("style");
style.textContent = `
.content-panel { padding: 1rem; font: 14px/1.5 system-ui, sans-serif; color: var(--fg, #1c1c1c); }
.content-panel__header { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.content-panel__filters { display: flex; gap: 0.5rem; }
.content-panel__create { display: flex; gap: 0.5rem; margin: 1rem 0; }
.content-panel__create input { flex: 1; padding: 0.5rem; border: 1px solid #ddd; border-radius: 4px; }
.content-panel__create button { padding: 0.5rem 1rem; }
.content-panel__list { list-style: none; padding: 0; margin: 0; }
.content-panel__list .row { display: flex; justify-content: space-between; align-items: center; padding: 0.75rem 0; border-bottom: 1px solid #eee; }
.content-panel__list .meta { display: flex; gap: 0.5rem; align-items: baseline; }
.content-panel__list .status { font-size: 0.8rem; color: #888; }
.content-panel__list .slug { font-size: 0.8rem; color: #888; }
.content-panel__list .actions { display: flex; gap: 0.25rem; }
.content-panel__list .actions button { font-size: 0.85rem; padding: 0.25rem 0.5rem; }
.content-panel__list .empty { color: #888; padding: 2rem 0; text-align: center; }
.content-panel__error { background: #fee; padding: 0.5rem 1rem; border-radius: 4px; margin: 0.5rem 0; }
.content-panel__loading { color: #888; padding: 1rem; }
.content-panel__footer { color: #888; font-size: 0.85rem; padding-top: 1rem; border-top: 1px solid #eee; margin-top: 1rem; }
`;
document.head.appendChild(style);

const root = createRoot(document.getElementById("root") || document.body);
root.render(<Panel />);
