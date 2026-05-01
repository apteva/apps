// SocialPanel — accounts + compose + posts in a single panel.
//
// Surface:
//   - Tab "Accounts": connected social accounts grid + Add Account flow
//     (OAuth in popup → page picker if needed → finalize).
//   - Tab "Compose": prompt body + multi-select accounts + media picker
//     (storage app, when bound) + Schedule/Now → post_create.
//   - Tab "Posts": list of recent posts with per-target status pills,
//     retry button on failed/partial.
//
// Lives in the social app's sidecar at /api/apps/social/ui/SocialPanel.mjs.
// The host React (19) + react-dom come from the dashboard's importmap;
// this file uses the same useAppEvents pattern as image-studio.

import { useCallback, useEffect, useRef, useState } from "react";

const API = "/api/apps/social";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface SocialAccount {
  id: number;
  platform: string;
  connection_id: number;
  external_account_id: string;
  display_name: string;
  avatar_url: string;
  status: string;
  created_at: string;
}

interface PostTarget {
  id: number;
  social_account_id: number;
  platform: string;
  display_name: string;
  avatar_url: string;
  status: string;
  platform_post_id: string;
  platform_url: string;
  attempts: number;
  last_error: string;
  published_at: string;
}

interface Post {
  id: number;
  body: string;
  media_storage_ids: number[];
  schedule_at: string;
  status: string;
  created_at: string;
  published_at: string;
  targets: PostTarget[];
}

interface PlatformInfo {
  platform: string;
  display_name: string;
  integration_slug: string;
  requires_picker: boolean;
}

interface PageEntry {
  id: string;
  name: string;
  avatar_url: string;
}

// --- Inlined SDK app-event subscription (same as other panels) ---
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// --- Panel ---------------------------------------------------------

export default function SocialPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<"accounts" | "compose" | "posts">("accounts");
  const [accounts, setAccounts] = useState<SocialAccount[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [status, setStatus] = useState("");

  const loadAccounts = useCallback(async () => {
    try {
      const res = await fetch(`${API}/accounts`, { credentials: "same-origin" });
      const data = await res.json();
      setAccounts(data.accounts || []);
    } catch (e) {
      setStatus("Load accounts: " + (e as Error).message);
    }
  }, []);

  const loadPosts = useCallback(async () => {
    try {
      const res = await fetch(`${API}/posts`, { credentials: "same-origin" });
      const data = await res.json();
      setPosts(data.posts || []);
    } catch (e) {
      setStatus("Load posts: " + (e as Error).message);
    }
  }, []);

  const loadPlatforms = useCallback(async () => {
    try {
      const res = await fetch(`${API}/platforms`, { credentials: "same-origin" });
      const data = await res.json();
      setPlatforms(data.platforms || []);
    } catch {}
  }, []);

  useEffect(() => {
    loadAccounts();
    loadPosts();
    loadPlatforms();
  }, [loadAccounts, loadPosts, loadPlatforms]);

  // Live updates — account adds/removals + per-target publish events.
  useAppEvents("social", projectId, (ev) => {
    if (ev.topic === "account.added" || ev.topic === "account.removed") {
      loadAccounts();
    }
    if (ev.topic === "post.created" || ev.topic === "post.completed" ||
        ev.topic === "target.published" || ev.topic === "target.failed") {
      loadPosts();
    }
  });

  // OAuth-popup → SocialPanel postMessage hook so the picker can come
  // up immediately after the user clicks Allow.
  const [oauthLanding, setOauthLanding] = useState<{
    pendingId: number;
    connectionId: number;
  } | null>(null);
  useEffect(() => {
    const onMsg = (ev: MessageEvent) => {
      if (ev.data?.type === "social.oauth_ready" && ev.data.pending_account_id) {
        setOauthLanding({
          pendingId: ev.data.pending_account_id,
          connectionId: ev.data.connection_id,
        });
        setTab("accounts");
      }
    };
    window.addEventListener("message", onMsg);
    return () => window.removeEventListener("message", onMsg);
  }, []);

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-1 border-b border-border px-4 py-2">
        <Tab label="Accounts" value="accounts" current={tab} onClick={setTab} count={accounts.length} />
        <Tab label="Compose" value="compose" current={tab} onClick={setTab} />
        <Tab label="Posts" value="posts" current={tab} onClick={setTab} count={posts.length} />
        <span className="ml-auto text-text-dim text-xs">{status}</span>
      </header>

      <div className="flex-1 overflow-auto">
        {tab === "accounts" && (
          <AccountsView
            accounts={accounts}
            platforms={platforms}
            oauthLanding={oauthLanding}
            onClearLanding={() => setOauthLanding(null)}
            onChange={loadAccounts}
            setStatus={setStatus}
          />
        )}
        {tab === "compose" && (
          <ComposeView
            accounts={accounts}
            onCreated={() => { loadPosts(); setTab("posts"); }}
            setStatus={setStatus}
          />
        )}
        {tab === "posts" && (
          <PostsView posts={posts} onChange={loadPosts} setStatus={setStatus} />
        )}
      </div>
    </div>
  );
}

function Tab({
  label, value, current, onClick, count,
}: {
  label: string; value: "accounts" | "compose" | "posts";
  current: string; onClick: (v: any) => void; count?: number;
}) {
  const active = value === current;
  return (
    <button
      onClick={() => onClick(value)}
      className={
        "px-3 py-1.5 text-sm rounded transition-colors " +
        (active ? "bg-bg-card text-text" : "text-text-muted hover:text-text")
      }
    >
      {label}
      {count !== undefined && count > 0 && (
        <span className="ml-1.5 text-xs text-text-dim">({count})</span>
      )}
    </button>
  );
}

// --- AccountsView -------------------------------------------------

function AccountsView({
  accounts, platforms, oauthLanding, onClearLanding, onChange, setStatus,
}: {
  accounts: SocialAccount[]; platforms: PlatformInfo[];
  oauthLanding: { pendingId: number; connectionId: number } | null;
  onClearLanding: () => void;
  onChange: () => void; setStatus: (s: string) => void;
}) {
  const [adding, setAdding] = useState(false);

  const handleLanded = useCallback(async (pendingId: number) => {
    // After OAuth, fetch the page list. If empty (no picker required),
    // finalize directly. Otherwise, keep oauthLanding set so the picker
    // renders below.
    try {
      const res = await fetch(`${API}/accounts/${pendingId}/pages`, { credentials: "same-origin" });
      const data = await res.json();
      if (!data.requires_picker) {
        await fetch(`${API}/accounts/finalize`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ pending_account_id: pendingId }),
        });
        setStatus("Account added.");
        onClearLanding();
        onChange();
      }
      // else: leave oauthLanding state intact; PagePicker uses it.
    } catch (e) {
      setStatus("Finalize failed: " + (e as Error).message);
    }
  }, [onChange, onClearLanding, setStatus]);

  // When oauthLanding flips, kick the auto-finalize / picker decision.
  useEffect(() => {
    if (oauthLanding) handleLanded(oauthLanding.pendingId);
  }, [oauthLanding, handleLanded]);

  return (
    <div className="p-4 flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <div className="text-text font-medium">Connected accounts</div>
        <button
          onClick={() => setAdding(true)}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold"
        >
          + Add account
        </button>
      </div>

      {accounts.length === 0 ? (
        <div className="py-12 text-center text-text-muted text-sm">
          No social accounts yet. Click <em>Add account</em> to connect your first one.
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
          {accounts.map((a) => (
            <AccountCard key={a.id} account={a} onChange={onChange} setStatus={setStatus} />
          ))}
        </div>
      )}

      {adding && (
        <AddAccountDialog
          platforms={platforms}
          onClose={() => setAdding(false)}
          setStatus={setStatus}
        />
      )}

      {oauthLanding && (
        <PagePicker
          pendingId={oauthLanding.pendingId}
          onClose={() => { onClearLanding(); onChange(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function AccountCard({
  account, onChange, setStatus,
}: { account: SocialAccount; onChange: () => void; setStatus: (s: string) => void }) {
  const remove = async () => {
    if (!confirm(`Disconnect ${account.display_name}?`)) return;
    try {
      await fetch(`${API}/accounts/${account.id}`, { method: "DELETE", credentials: "same-origin" });
      setStatus("Disconnected.");
      onChange();
    } catch (e) {
      setStatus("Disconnect failed: " + (e as Error).message);
    }
  };
  return (
    <div className="border border-border rounded p-3 flex items-center gap-3">
      {account.avatar_url ? (
        <img src={account.avatar_url} alt="" className="w-10 h-10 rounded-full" />
      ) : (
        <div className="w-10 h-10 rounded-full bg-bg-input grid place-items-center text-text-dim text-xs uppercase">
          {account.platform[0]}
        </div>
      )}
      <div className="flex-1 min-w-0">
        <div className="text-text text-sm truncate">{account.display_name}</div>
        <div className="text-text-dim text-xs">{account.platform}</div>
      </div>
      <button
        onClick={remove}
        className="text-text-muted hover:text-error text-xs"
        title="Disconnect"
      >
        ×
      </button>
    </div>
  );
}

function AddAccountDialog({
  platforms, onClose, setStatus,
}: { platforms: PlatformInfo[]; onClose: () => void; setStatus: (s: string) => void }) {
  const [busy, setBusy] = useState<string | null>(null);

  const start = async (p: PlatformInfo) => {
    setBusy(p.platform);
    setStatus("Starting OAuth for " + p.display_name + "…");
    try {
      const res = await fetch(`${API}/accounts/start`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ platform: p.platform }),
      });
      if (!res.ok) {
        setStatus("Start failed: " + (await res.text()));
        setBusy(null);
        return;
      }
      const data = await res.json();
      if (!data.authorize_url) {
        setStatus("Server didn't return authorize_url");
        setBusy(null);
        return;
      }
      // Open the authorize URL in a popup. The OAuth callback will
      // postMessage back to us via the oauth_done landing page.
      window.open(data.authorize_url, "social_oauth", "width=600,height=700");
      onClose();
    } catch (e) {
      setStatus("Start failed: " + (e as Error).message);
      setBusy(null);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded p-4 w-[420px] max-w-[90vw]"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-3">
          <div className="text-text font-medium">Add a social account</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <div className="text-text-dim text-xs mb-3">
          You'll be redirected to authorize. The access token is held by the platform; this app sees only opaque IDs.
        </div>
        <div className="flex flex-col gap-1">
          {platforms.map((p) => (
            <button
              key={p.platform}
              onClick={() => start(p)}
              disabled={busy === p.platform}
              className="text-left px-3 py-2 border border-border rounded hover:border-accent text-sm disabled:opacity-50"
            >
              <span className="text-text">{p.display_name}</span>
              {p.requires_picker && (
                <span className="text-text-dim text-xs ml-2">(page picker after auth)</span>
              )}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

function PagePicker({
  pendingId, onClose, setStatus,
}: { pendingId: number; onClose: () => void; setStatus: (s: string) => void }) {
  const [pages, setPages] = useState<PageEntry[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    fetch(`${API}/accounts/${pendingId}/pages`, { credentials: "same-origin" })
      .then((r) => r.json())
      .then((d) => {
        setPages(d.pages || []);
        setLoading(false);
        if (!d.requires_picker) {
          onClose();
        }
      })
      .catch(() => setLoading(false));
  }, [pendingId, onClose]);

  const pick = async (page: PageEntry) => {
    try {
      await fetch(`${API}/accounts/finalize`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          pending_account_id: pendingId,
          page_id: page.id,
          name: page.name,
        }),
      });
      setStatus("Account added: " + page.name);
      onClose();
    } catch (e) {
      setStatus("Finalize failed: " + (e as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded p-4 w-[420px] max-w-[90vw]"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="text-text font-medium mb-3">Pick which page to connect</div>
        {loading ? (
          <div className="text-text-dim text-sm py-6 text-center">Loading…</div>
        ) : pages.length === 0 ? (
          <div className="text-text-dim text-sm py-6 text-center">No pages found.</div>
        ) : (
          <div className="flex flex-col gap-1">
            {pages.map((p) => (
              <button
                key={p.id}
                onClick={() => pick(p)}
                className="flex items-center gap-3 px-3 py-2 border border-border rounded hover:border-accent text-sm"
              >
                {p.avatar_url ? (
                  <img src={p.avatar_url} alt="" className="w-8 h-8 rounded-full" />
                ) : (
                  <div className="w-8 h-8 rounded-full bg-bg-input" />
                )}
                <span className="text-text flex-1 text-left truncate">{p.name}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// --- ComposeView --------------------------------------------------

function ComposeView({
  accounts, onCreated, setStatus,
}: {
  accounts: SocialAccount[]; onCreated: () => void; setStatus: (s: string) => void;
}) {
  const [body, setBody] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [scheduleAt, setScheduleAt] = useState("");
  const [busy, setBusy] = useState(false);

  const toggle = (id: number) => {
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const submit = async () => {
    if (!body.trim() || selected.size === 0) return;
    setBusy(true);
    setStatus("Posting…");
    try {
      const res = await fetch(`${API}/posts`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          body,
          social_account_ids: Array.from(selected),
          schedule_at: scheduleAt || undefined,
        }),
      });
      if (!res.ok) {
        setStatus("Post failed: " + (await res.text()));
        return;
      }
      setBody("");
      setSelected(new Set());
      setScheduleAt("");
      setStatus("Done.");
      onCreated();
    } catch (e) {
      setStatus("Post failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (accounts.length === 0) {
    return (
      <div className="py-12 px-6 text-center text-text-muted text-sm">
        Connect at least one social account first.
      </div>
    );
  }

  return (
    <div className="p-4 flex flex-col gap-4 max-w-3xl">
      <div>
        <label className="text-text-muted text-xs">What's on your mind?</label>
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Compose your post…"
          className="w-full mt-1 bg-bg-input border border-border rounded px-3 py-2 text-sm min-h-[120px] resize-y"
        />
        <div className="text-text-dim text-xs mt-1">{body.length} characters</div>
      </div>

      <div>
        <div className="text-text-muted text-xs mb-2">Post to:</div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
          {accounts.map((a) => (
            <label
              key={a.id}
              className={
                "flex items-center gap-3 px-3 py-2 border rounded cursor-pointer transition-colors " +
                (selected.has(a.id) ? "border-accent bg-bg-card" : "border-border hover:border-text-dim")
              }
            >
              <input
                type="checkbox"
                checked={selected.has(a.id)}
                onChange={() => toggle(a.id)}
                className="accent-accent"
              />
              {a.avatar_url ? (
                <img src={a.avatar_url} alt="" className="w-6 h-6 rounded-full" />
              ) : (
                <div className="w-6 h-6 rounded-full bg-bg-input" />
              )}
              <div className="flex-1 min-w-0">
                <div className="text-text text-sm truncate">{a.display_name}</div>
                <div className="text-text-dim text-xs">{a.platform}</div>
              </div>
            </label>
          ))}
        </div>
      </div>

      <div className="flex items-end gap-3">
        <div>
          <label className="text-text-muted text-xs block">Schedule (optional)</label>
          <input
            type="datetime-local"
            value={scheduleAt}
            onChange={(e) => setScheduleAt(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
        </div>
        <button
          onClick={submit}
          disabled={!body.trim() || selected.size === 0 || busy}
          className="px-4 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          {busy ? "…" : scheduleAt ? "Schedule" : "Post now"}
        </button>
      </div>
    </div>
  );
}

// --- PostsView ----------------------------------------------------

function PostsView({
  posts, onChange, setStatus,
}: { posts: Post[]; onChange: () => void; setStatus: (s: string) => void }) {
  const retry = async (postId: number) => {
    try {
      await fetch(`${API}/posts/${postId}/retry`, { method: "POST", credentials: "same-origin" });
      setStatus("Retry triggered.");
      onChange();
    } catch (e) {
      setStatus("Retry failed: " + (e as Error).message);
    }
  };

  if (posts.length === 0) {
    return (
      <div className="py-12 text-center text-text-muted text-sm">
        No posts yet. Compose your first one.
      </div>
    );
  }

  return (
    <div className="p-4 flex flex-col gap-3">
      {posts.map((p) => (
        <div key={p.id} className="border border-border rounded p-3">
          <div className="flex items-start gap-3">
            <div className="flex-1 min-w-0">
              <div className="text-text text-sm whitespace-pre-wrap">{p.body}</div>
              <div className="text-text-dim text-xs mt-1">
                {new Date(p.created_at).toLocaleString()}
                {p.schedule_at && ` · scheduled for ${new Date(p.schedule_at).toLocaleString()}`}
              </div>
            </div>
            <StatusPill status={p.status} />
            {(p.status === "failed" || p.status === "partial") && (
              <button
                onClick={() => retry(p.id)}
                className="text-xs text-accent hover:underline"
              >
                Retry
              </button>
            )}
          </div>
          {p.targets.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {p.targets.map((t) => (
                <TargetChip key={t.id} target={t} />
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function StatusPill({ status }: { status: string }) {
  const tone =
    status === "published" ? "text-success" :
    status === "failed" ? "text-error" :
    status === "partial" ? "text-warn" :
    status === "scheduled" ? "text-info" :
    "text-text-dim";
  return <span className={"text-xs uppercase " + tone}>{status}</span>;
}

function TargetChip({ target }: { target: PostTarget }) {
  const ok = target.status === "published";
  const failed = target.status === "failed";
  return (
    <a
      href={target.platform_url || "#"}
      target={target.platform_url ? "_blank" : undefined}
      rel="noopener"
      title={target.last_error || ""}
      className={
        "inline-flex items-center gap-1 px-2 py-1 border rounded text-xs " +
        (ok ? "border-success text-success" :
         failed ? "border-error text-error" :
         "border-border text-text-dim")
      }
    >
      <span>{ok ? "✓" : failed ? "✗" : "…"}</span>
      <span>{target.platform}</span>
      <span className="text-text-dim">· {target.display_name}</span>
    </a>
  );
}
