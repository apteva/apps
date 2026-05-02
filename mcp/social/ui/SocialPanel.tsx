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
import { uploadResumable } from "./uploadResumable";

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
  // available — true when the operator has seeded an integration
  // connection for this platform (Settings → Integrations). Without
  // it, OAuth start would fail with "missing client_id" — we gray
  // out the button instead of letting the user click into an error.
  available: boolean;
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
  const [tab, setTab] = useState<"accounts" | "posts">("posts");
  const [accounts, setAccounts] = useState<SocialAccount[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [status, setStatus] = useState("");
  // Compose used to be its own tab. It's now a centred modal opened
  // from the Posts tab (+ New post button) so creating a post happens
  // in a focused dialog instead of swapping the right pane — same
  // pattern as the jobs panel's "+ New job" → CreateJobDialog flow.
  const [composeOpen, setComposeOpen] = useState(false);

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
        <Tab label="Posts" value="posts" current={tab} onClick={setTab} count={posts.length} />
        <Tab label="Accounts" value="accounts" current={tab} onClick={setTab} count={accounts.length} />
        <button
          onClick={() => setComposeOpen(true)}
          disabled={accounts.length === 0}
          className="ml-auto px-3 py-1 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          title={accounts.length === 0 ? "Connect at least one social account first" : "New post"}
        >
          + New post
        </button>
        <span className="text-text-dim text-xs ml-2">{status}</span>
      </header>

      <div className="flex-1 overflow-auto">
        {tab === "accounts" && (
          <AccountsView
            accounts={accounts}
            platforms={platforms}
            oauthLanding={oauthLanding}
            onClearLanding={() => setOauthLanding(null)}
            onSetLanding={(pendingId, connId) =>
              setOauthLanding({ pendingId, connectionId: connId })
            }
            onChange={loadAccounts}
            setStatus={setStatus}
          />
        )}
        {tab === "posts" && (
          <PostsView posts={posts} onChange={loadPosts} setStatus={setStatus} />
        )}
      </div>

      {composeOpen && (
        <ComposeDialog
          accounts={accounts}
          onClose={() => setComposeOpen(false)}
          onCreated={() => { loadPosts(); setComposeOpen(false); setTab("posts"); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function Tab({
  label, value, current, onClick, count,
}: {
  label: string; value: "accounts" | "posts";
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
  accounts, platforms, oauthLanding, onClearLanding, onSetLanding, onChange, setStatus,
}: {
  accounts: SocialAccount[]; platforms: PlatformInfo[];
  oauthLanding: { pendingId: number; connectionId: number } | null;
  onClearLanding: () => void;
  onSetLanding: (pendingId: number, connectionId: number) => void;
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
          onReuseExisting={(pendingId, connId) => {
            // Backend returned 'reusing existing connection' — skip the
            // OAuth popup entirely, jump straight into the page picker.
            onSetLanding(pendingId, connId);
            setAdding(false);
          }}
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
  platforms, onClose, setStatus, onReuseExisting,
}: {
  platforms: PlatformInfo[];
  onClose: () => void;
  setStatus: (s: string) => void;
  onReuseExisting: (pendingId: number, connectionId: number) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  // Inline error inside the modal. The panel-header status used to
  // be the only failure surface, but the modal's fixed-inset overlay
  // sits on top of the header — so users never saw the message and
  // it looked like 'popup flashed and closed for no reason'.
  const [err, setErr] = useState<string>("");

  const start = (p: PlatformInfo) => {
    if (!p.available) return;
    setErr("");
    // Reuse-existing path: backend skips OAuth when a connection for
    // this platform already exists. Detect that ahead of opening the
    // popup so we don't pop a window only to immediately close it.
    // We always open the popup synchronously below to dodge the
    // popup-blocker; we close it without navigating in the reuse case.
    // Open the popup SYNCHRONOUSLY in the click handler. Browsers
    // block window.open() when called from an async continuation
    // because the user-gesture context is gone by the time the fetch
    // resolves — the popup gets silently dropped, no error surfaces,
    // and from the user's perspective "nothing happens" when they
    // click the platform button.
    //
    // Pattern: open the popup right away pointing at about:blank,
    // then await the /accounts/start call and navigate the existing
    // popup to the authorize URL once we have it. The popup carries
    // the user gesture from the click; redirecting it later is fine.
    const popup = window.open("about:blank", "social_oauth", "width=600,height=700");
    if (!popup) {
      setStatus("Popup blocked. Allow pop-ups for this site and try again.");
      return;
    }
    setBusy(p.platform);
    setStatus("Starting OAuth for " + p.display_name + "…");
    (async () => {
      const fail = (msg: string) => {
        setErr(msg);
        setStatus(msg);
        setBusy(null);
        try { popup.close(); } catch {}
      };
      try {
        const res = await fetch(`${API}/accounts/start`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ platform: p.platform }),
        });
        if (!res.ok) {
          fail(`Start failed (HTTP ${res.status}): ${await res.text()}`);
          return;
        }
        const data = await res.json();
        // The backend tool returns MCP-shaped errors as 200 with
        // {isError: true, content: [{type: "text", text: "..."}]}
        // because the same handler serves agent MCP calls. Detect
        // that envelope and surface the inner message — otherwise
        // the failure looks like 'no authorize_url' and the user
        // has no idea what went wrong.
        if (data?.isError) {
          const inner = (data.content || []).find((c: any) => c.type === "text")?.text;
          fail(inner || "OAuth start returned an error envelope");
          return;
        }
        // Reuse-existing path: backend signals 'no OAuth needed' by
        // omitting authorize_url and including reused_connection. Skip
        // the popup, hand control back to the parent so it opens the
        // page picker against the existing connection.
        if (!data.authorize_url && data.pending_account_id && data.reused_connection) {
          try { popup.close(); } catch {}
          setBusy(null);
          onReuseExisting(data.pending_account_id, data.reused_connection);
          return;
        }
        if (!data.authorize_url) {
          fail("Server didn't return authorize_url");
          return;
        }
        // Navigate the already-open popup to the upstream authorize URL.
        popup.location.href = data.authorize_url;
        onClose();
      } catch (e) {
        fail("Start failed: " + (e as Error).message);
      }
    })();
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
        {err && (
          <div className="mb-3 border border-error rounded p-2 bg-error/10">
            <div className="text-error text-xs font-medium mb-1">Couldn't start OAuth</div>
            <div className="text-text-muted text-xs whitespace-pre-wrap break-words">{err}</div>
            {/missing client_id/i.test(err) && (
              <div className="text-text-dim text-[11px] mt-2">
                This integration requires a per-deployment OAuth app. Register one with the platform (e.g. developers.facebook.com) and either set <code className="text-text">OAUTH_FACEBOOK_API_CLIENT_ID</code> + <code className="text-text">OAUTH_FACEBOOK_API_CLIENT_SECRET</code> as environment variables on apteva-server, or connect the integration once via Settings → Integrations to seed the credentials.
              </div>
            )}
          </div>
        )}
        <div className="flex flex-col gap-1">
          {platforms.map((p) => {
            const disabled = !p.available || busy === p.platform;
            return (
              <button
                key={p.platform}
                onClick={() => start(p)}
                disabled={disabled}
                title={
                  !p.available
                    ? `No ${p.display_name} integration installed. Add one in Settings → Integrations to enable this.`
                    : undefined
                }
                className="text-left px-3 py-2 border border-border rounded hover:border-accent text-sm disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:border-border"
              >
                <span className="text-text">{p.display_name}</span>
                {p.requires_picker && p.available && (
                  <span className="text-text-dim text-xs ml-2">(page picker after auth)</span>
                )}
                {!p.available && (
                  <span className="text-text-dim text-xs ml-2">— integration not installed</span>
                )}
              </button>
            );
          })}
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
  const [filter, setFilter] = useState("");
  const [busyID, setBusyID] = useState<string>("");

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
    setBusyID(page.id);
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
      setBusyID("");
    }
  };

  // Case-insensitive substring match against page name. Plenty for
  // typical fan-page lists; if it ever needs to scale further this is
  // the place to swap in a token-based ranker.
  const filtered = filter.trim()
    ? pages.filter((p) => p.name.toLowerCase().includes(filter.toLowerCase()))
    : pages;

  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded-lg shadow-lg w-[560px] max-w-[92vw] max-h-[80vh] flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <div className="text-text font-medium">Pick which page to connect</div>
            {!loading && pages.length > 0 && (
              <div className="text-text-dim text-xs mt-0.5">
                {filtered.length === pages.length
                  ? `${pages.length} page${pages.length === 1 ? "" : "s"}`
                  : `${filtered.length} of ${pages.length}`}
              </div>
            )}
          </div>
          <button
            onClick={onClose}
            className="text-text-muted hover:text-text px-2 leading-none text-lg"
            aria-label="Close"
          >
            ×
          </button>
        </div>

        {!loading && pages.length > 5 && (
          <div className="px-4 pt-3">
            <input
              type="text"
              autoFocus
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Search pages…"
              className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text focus:outline-none focus:border-accent"
            />
          </div>
        )}

        <div className="flex-1 overflow-y-auto p-3">
          {loading ? (
            <div className="text-text-dim text-sm py-8 text-center">Loading…</div>
          ) : pages.length === 0 ? (
            <div className="text-text-dim text-sm py-8 text-center">No pages found.</div>
          ) : filtered.length === 0 ? (
            <div className="text-text-dim text-sm py-8 text-center">
              No pages match "{filter}".
            </div>
          ) : (
            <div className="flex flex-col gap-1">
              {filtered.map((p) => (
                <button
                  key={p.id}
                  onClick={() => pick(p)}
                  disabled={busyID !== ""}
                  className="flex items-center gap-3 px-3 py-2 border border-border rounded hover:border-accent hover:bg-bg-input/40 text-sm disabled:opacity-50 disabled:cursor-not-allowed text-left transition-colors"
                >
                  {p.avatar_url ? (
                    <img src={p.avatar_url} alt="" className="w-9 h-9 rounded-full flex-shrink-0 object-cover" />
                  ) : (
                    <div className="w-9 h-9 rounded-full bg-bg-input flex-shrink-0 grid place-items-center text-text-dim text-xs font-medium">
                      {p.name.slice(0, 2).toUpperCase()}
                    </div>
                  )}
                  <span className="text-text flex-1 truncate">{p.name}</span>
                  {busyID === p.id && (
                    <span className="text-text-dim text-xs">connecting…</span>
                  )}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// --- ComposeDialog ------------------------------------------------
//
// Centred modal triggered by the Posts tab's "+ New post" button.
// Replaces the old ComposeView tab — same form, same submit handler;
// just shown as an overlay instead of swapping the tab body. Matches
// the pattern apps/mcp/jobs uses for "+ New job" → CreateJobDialog.

function ComposeDialog({
  accounts, onClose, onCreated, setStatus,
}: {
  accounts: SocialAccount[];
  onClose: () => void;
  onCreated: () => void;
  setStatus: (s: string) => void;
}) {
  const [body, setBody] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [scheduleAt, setScheduleAt] = useState("");
  const [busy, setBusy] = useState(false);
  // Media attached to the post. We upload immediately to the storage app
  // (so the post_create call only carries IDs, not bytes) and remember the
  // returned id + a local preview URL. The previewURL is a local
  // ObjectURL — cheap, but we revoke it on remove + unmount so the
  // browser doesn't keep the bytes around forever.
  const [media, setMedia] = useState<{ id: number; name: string; mime: string; previewURL: string }[]>([]);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  // Revoke any object URLs we created when the modal closes.
  useEffect(() => {
    return () => {
      for (const m of media) URL.revokeObjectURL(m.previewURL);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const toggle = (id: number) => {
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const handleAttach = async (ev: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = Array.from(ev.target.files || []);
    if (fileList.length === 0) return;
    setUploading(true);
    try {
      const uploaded: typeof media = [];
      let i = 0;
      for (const file of fileList) {
        i += 1;
        const label = `(${i}/${fileList.length}) ${file.name}`;
        // uploadResumable picks single-shot for ≤25 MB, chunked
        // resumable above that — multi-GB videos no longer crash
        // the storage sidecar, and a network blip mid-upload
        // resumes from the server-known offset.
        const row = await uploadResumable(file, {
          folder: "social/",
          onProgress: (bytes, total) => {
            const pct = total > 0 ? Math.floor((bytes / total) * 100) : 0;
            setStatus(`Uploading ${label} — ${pct}%`);
          },
        });
        if (typeof row.id !== "number") {
          throw new Error("storage didn't return a file id");
        }
        uploaded.push({
          id: row.id,
          name: file.name,
          mime: file.type,
          previewURL: URL.createObjectURL(file),
        });
      }
      setMedia((prev) => [...prev, ...uploaded]);
      setStatus(`Attached ${uploaded.length} file${uploaded.length !== 1 ? "s" : ""}.`);
    } catch (e) {
      setStatus("Upload failed: " + (e as Error).message);
    } finally {
      setUploading(false);
      ev.target.value = "";
    }
  };

  const removeMedia = (id: number) => {
    setMedia((prev) => {
      const dropped = prev.find((m) => m.id === id);
      if (dropped) URL.revokeObjectURL(dropped.previewURL);
      return prev.filter((m) => m.id !== id);
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
          media_storage_ids: media.length > 0 ? media.map((m) => m.id) : undefined,
        }),
      });
      if (!res.ok) {
        setStatus("Post failed: " + (await res.text()));
        return;
      }
      setBody("");
      setSelected(new Set());
      setScheduleAt("");
      // Don't revoke the object URLs here — the cleanup effect handles
      // them on unmount. Just clear the list visually.
      setMedia([]);
      setStatus("Done.");
      onCreated();
    } catch (e) {
      setStatus("Post failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      onClick={onClose}
    >
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-2xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">New post</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>

        <div className="flex flex-col gap-1">
          <label className="text-xs uppercase tracking-wide text-text-dim">Body</label>
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="Compose your post…"
            autoFocus
            className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm min-h-[120px] resize-y"
          />
          <div className="text-text-dim text-xs">{body.length} characters</div>
        </div>

        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <label className="text-xs uppercase tracking-wide text-text-dim">Media (optional)</label>
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={uploading}
              className="text-xs text-accent hover:underline disabled:opacity-50"
            >
              {uploading ? "Uploading…" : "+ Attach image / video"}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              accept="image/*,video/*"
              onChange={handleAttach}
              className="hidden"
            />
          </div>
          {media.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {media.map((m) => (
                <div
                  key={m.id}
                  className="relative w-20 h-20 rounded border border-border overflow-hidden bg-bg-input flex-shrink-0 group"
                  title={m.name}
                >
                  {m.mime.startsWith("video/") ? (
                    <video src={m.previewURL} className="w-full h-full object-cover" muted />
                  ) : (
                    <img src={m.previewURL} alt={m.name} className="w-full h-full object-cover" />
                  )}
                  <button
                    type="button"
                    onClick={() => removeMedia(m.id)}
                    className="absolute top-0.5 right-0.5 w-5 h-5 rounded-full bg-bg/80 text-text hover:bg-bg flex items-center justify-center text-xs leading-none"
                    aria-label={`Remove ${m.name}`}
                  >
                    ×
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="flex flex-col gap-1">
          <label className="text-xs uppercase tracking-wide text-text-dim">Post to</label>
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

        <div className="flex items-end gap-3 mt-1">
          <div className="flex flex-col gap-1">
            <label className="text-xs uppercase tracking-wide text-text-dim">Schedule (optional)</label>
            <input
              type="datetime-local"
              value={scheduleAt}
              onChange={(e) => setScheduleAt(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            />
          </div>
          <div className="ml-auto flex items-center gap-2">
            <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
            <button
              onClick={submit}
              disabled={!body.trim() || selected.size === 0 || busy}
              className="px-4 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {busy ? "…" : scheduleAt ? "Schedule" : "Post now"}
            </button>
          </div>
        </div>
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
          {p.media_storage_ids && p.media_storage_ids.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {p.media_storage_ids.map((id) => (
                <MediaThumb key={id} fileId={id} />
              ))}
            </div>
          )}
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

// MediaThumb renders a single attached-media tile. We don't get the
// MIME from post_list (it'd cost a storage round-trip per id at list
// time), so the component fetches metadata via files_get on mount —
// images render with <img>, videos render with <video preload=
// "metadata"> so the browser fetches just the moov atom + first
// keyframe, not the whole file. Click expands to a full-screen
// modal with playback controls / open-in-new-tab.
//
// Cache is process-wide: the same fileId rendered in five posts
// only triggers one /files/<id> fetch, even before React Query.
const mediaMetaCache = new Map<number, { mime: string; name: string } | "loading" | "error">();
const mediaMetaWaiters = new Map<number, ((m: { mime: string; name: string } | null) => void)[]>();

async function loadMediaMeta(fileId: number): Promise<{ mime: string; name: string } | null> {
  const cached = mediaMetaCache.get(fileId);
  if (cached && cached !== "loading" && cached !== "error") return cached;
  if (cached === "loading") {
    return new Promise((resolve) => {
      const w = mediaMetaWaiters.get(fileId) ?? [];
      w.push(resolve);
      mediaMetaWaiters.set(fileId, w);
    });
  }
  mediaMetaCache.set(fileId, "loading");
  try {
    const res = await fetch(`/api/apps/storage/files/${fileId}`, { credentials: "same-origin" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = (await res.json()) as { file?: { content_type?: string; name?: string } };
    const meta = {
      mime: data?.file?.content_type ?? "",
      name: data?.file?.name ?? "",
    };
    mediaMetaCache.set(fileId, meta);
    const waiters = mediaMetaWaiters.get(fileId) ?? [];
    mediaMetaWaiters.delete(fileId);
    for (const w of waiters) w(meta);
    return meta;
  } catch {
    mediaMetaCache.set(fileId, "error");
    const waiters = mediaMetaWaiters.get(fileId) ?? [];
    mediaMetaWaiters.delete(fileId);
    for (const w of waiters) w(null);
    return null;
  }
}

function MediaThumb({ fileId }: { fileId: number }) {
  const [meta, setMeta] = useState<{ mime: string; name: string } | null>(null);
  const [open, setOpen] = useState(false);
  const url = `/api/apps/storage/files/${fileId}/content`;
  useEffect(() => {
    let alive = true;
    loadMediaMeta(fileId).then((m) => { if (alive) setMeta(m); });
    return () => { alive = false; };
  }, [fileId]);
  const isVideo = meta?.mime.startsWith("video/") ?? false;
  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className="block w-20 h-20 rounded border border-border overflow-hidden bg-bg-input flex-shrink-0 relative group"
        title={meta?.name || `file #${fileId}`}
      >
        {isVideo ? (
          // preload="metadata" → browser pulls just the container
          // header + first keyframe for the still, not the whole
          // file. <video muted> with no controls renders as a
          // single-frame poster in this size.
          <>
            <video
              src={url}
              preload="metadata"
              muted
              playsInline
              className="w-full h-full object-cover"
            />
            <div className="absolute inset-0 flex items-center justify-center bg-black/30 group-hover:bg-black/50 transition-colors">
              <div className="w-8 h-8 rounded-full bg-bg/80 grid place-items-center">
                <span className="text-text text-xs leading-none">▶</span>
              </div>
            </div>
          </>
        ) : (
          <img src={url} alt={meta?.name || ""} className="w-full h-full object-cover" />
        )}
      </button>
      {open && (
        <div
          className="fixed inset-0 z-50 bg-black/80 grid place-items-center p-6"
          onClick={() => setOpen(false)}
        >
          <div className="relative max-w-5xl max-h-full" onClick={(e) => e.stopPropagation()}>
            {isVideo ? (
              <video
                src={url}
                controls
                autoPlay
                className="max-w-full max-h-[85vh] rounded"
              />
            ) : (
              <img
                src={url}
                alt={meta?.name || ""}
                className="max-w-full max-h-[85vh] rounded"
              />
            )}
            <div className="mt-2 flex items-center justify-between gap-3">
              <span className="text-text-dim text-xs truncate">{meta?.name || `file #${fileId}`}</span>
              <a
                href={url}
                target="_blank"
                rel="noopener"
                className="text-accent text-xs hover:underline"
              >
                Open in new tab ↗
              </a>
            </div>
            <button
              onClick={() => setOpen(false)}
              className="absolute -top-3 -right-3 w-8 h-8 rounded-full bg-bg-card border border-border text-text"
              aria-label="Close"
            >
              ×
            </button>
          </div>
        </div>
      )}
    </>
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
