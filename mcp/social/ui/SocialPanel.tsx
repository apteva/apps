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
  profile_id?: number;
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
  profile_id?: number;
}

// Profile = brand/client/site container (see profiles.go). One
// project, one social install, many profiles, each grouping a
// set of social_accounts. profile_id=0 means "unassigned" (legacy
// rows pre-003_profiles migration).
interface Profile {
  id: number;
  project_id: string;
  name: string;
  slug: string;
  description: string;
  color: string;
  is_default: boolean;
  account_count?: number;
  post_count?: number;
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
  // option_fields — per-platform overrides the compose dialog can
  // surface as inputs. Empty when the platform has nothing to
  // customise (Twitter / FB / IG / LinkedIn / TikTok in v1; only
  // YouTube exposes title / visibility / category / tags today).
  option_fields?: OptionField[];
}

interface OptionField {
  name: string;
  label: string;
  type: "text" | "textarea" | "select" | "tags";
  options?: string[];
  help?: string;
}

// Each multi-destination platform exposes a different concept in its
// picker — FB shows pages, IG shows business accounts (linked via FB
// pages), YouTube shows channels. Used to label the picker title,
// counter, search placeholder, and AddAccount hint without scattering
// platform checks all over the UI.
const PICKER_KIND: Record<string, { singular: string; plural: string }> = {
  facebook: { singular: "page", plural: "pages" },
  instagram: { singular: "account", plural: "accounts" },
  youtube: { singular: "channel", plural: "channels" },
};

function pickerKind(platform: string): { singular: string; plural: string } {
  return PICKER_KIND[platform] || { singular: "destination", plural: "destinations" };
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
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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
  const [tab, setTab] = useState<"accounts" | "posts" | "metrics">("posts");
  const [accounts, setAccounts] = useState<SocialAccount[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [status, setStatus] = useState("");
  const [composeOpen, setComposeOpen] = useState(false);
  // Profile filter — null = "All profiles" (project-wide view).
  // Persists per-project so refreshing the page keeps the user's
  // last-selected brand context.
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [activeProfileId, setActiveProfileId] = useState<number | null>(() => {
    try {
      const raw = localStorage.getItem(`social.activeProfile.${projectId || ""}`);
      return raw ? Number(raw) || null : null;
    } catch {
      return null;
    }
  });
  const [manageOpen, setManageOpen] = useState(false);
  useEffect(() => {
    try {
      if (activeProfileId == null) {
        localStorage.removeItem(`social.activeProfile.${projectId || ""}`);
      } else {
        localStorage.setItem(`social.activeProfile.${projectId || ""}`, String(activeProfileId));
      }
    } catch {}
  }, [activeProfileId, projectId]);

  const loadProfiles = useCallback(async () => {
    try {
      const res = await fetch(`${API}/profiles`, { credentials: "same-origin" });
      const data = await res.json();
      setProfiles(data.profiles || []);
    } catch (e) {
      setStatus("Load profiles: " + (e as Error).message);
    }
  }, []);

  // Profile-scoped fetches — when activeProfileId is set, the
  // accounts/posts queries pass profile_id and the panel only sees
  // that brand's rows. activeProfileId=null = project-wide.
  const profileQuery = useCallback(() => {
    if (activeProfileId == null) return "";
    return `?profile_id=${activeProfileId}`;
  }, [activeProfileId]);

  const loadAccounts = useCallback(async () => {
    try {
      const res = await fetch(`${API}/accounts${profileQuery()}`, { credentials: "same-origin" });
      const data = await res.json();
      setAccounts(data.accounts || []);
    } catch (e) {
      setStatus("Load accounts: " + (e as Error).message);
    }
  }, [profileQuery]);

  const loadPosts = useCallback(async () => {
    try {
      const res = await fetch(`${API}/posts${profileQuery()}`, { credentials: "same-origin" });
      const data = await res.json();
      setPosts(data.posts || []);
    } catch (e) {
      setStatus("Load posts: " + (e as Error).message);
    }
  }, [profileQuery]);

  const loadPlatforms = useCallback(async () => {
    try {
      const res = await fetch(`${API}/platforms`, { credentials: "same-origin" });
      const data = await res.json();
      setPlatforms(data.platforms || []);
    } catch {}
  }, []);

  useEffect(() => {
    loadProfiles();
    loadAccounts();
    loadPosts();
    loadPlatforms();
  }, [loadProfiles, loadAccounts, loadPosts, loadPlatforms]);

  // Live updates — account adds/removals + per-target publish events
  // + profile CRUD + post lifecycle (reschedule/delete from agent).
  useAppEvents("social", projectId, (ev) => {
    if (ev.topic === "account.added" || ev.topic === "account.removed") {
      loadAccounts();
    }
    if (ev.topic === "post.created" || ev.topic === "post.completed" ||
        ev.topic === "post.rescheduled" || ev.topic === "post.deleted" ||
        ev.topic === "target.published" || ev.topic === "target.failed") {
      loadPosts();
    }
    if (ev.topic === "profile.created" || ev.topic === "profile.updated" ||
        ev.topic === "profile.deleted" || ev.topic === "profile.accounts_moved") {
      loadProfiles();
      loadAccounts();
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

  const activeProfile = profiles.find((p) => p.id === activeProfileId) || null;

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-1 border-b border-border px-4 py-2">
        <ProfileSwitcher
          profiles={profiles}
          activeId={activeProfileId}
          onSelect={setActiveProfileId}
          onManage={() => setManageOpen(true)}
        />
        <span className="w-px h-5 bg-border mx-2" />
        <Tab label="Posts" value="posts" current={tab} onClick={setTab} count={posts.length} />
        <Tab label="Accounts" value="accounts" current={tab} onClick={setTab} count={accounts.length} />
        <Tab label="Metrics" value="metrics" current={tab} onClick={setTab} />
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
        {tab === "metrics" && (
          <MetricsView posts={posts} accounts={accounts} setStatus={setStatus} />
        )}
      </div>

      {composeOpen && (
        <ComposeDialog
          accounts={accounts}
          platforms={platforms}
          activeProfile={activeProfile}
          onClose={() => setComposeOpen(false)}
          onCreated={() => { loadPosts(); setComposeOpen(false); setTab("posts"); }}
          setStatus={setStatus}
        />
      )}
      {manageOpen && (
        <ProfileManageModal
          profiles={profiles}
          accounts={accounts}
          onClose={() => setManageOpen(false)}
          onChanged={() => { loadProfiles(); loadAccounts(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

// --- ProfileSwitcher: header dropdown ----------------------------

function ProfileSwitcher({
  profiles, activeId, onSelect, onManage,
}: {
  profiles: Profile[];
  activeId: number | null;
  onSelect: (id: number | null) => void;
  onManage: () => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  // Outside-click close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", onDoc);
    return () => window.removeEventListener("mousedown", onDoc);
  }, [open]);
  const active = profiles.find((p) => p.id === activeId) || null;
  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 px-2 py-1 text-sm rounded hover:bg-bg-card transition-colors"
        title={active ? `Profile: ${active.name}` : "All profiles"}
      >
        <span
          className="w-2 h-2 rounded-full"
          style={{ backgroundColor: active?.color || "#94a3b8" }}
        />
        <span className="text-text font-medium">{active ? active.name : "All profiles"}</span>
        <span className="text-text-dim text-xs">▾</span>
      </button>
      {open && (
        <div className="absolute left-0 top-full mt-1 w-64 z-40 bg-bg-card border border-border rounded shadow-lg py-1">
          <button
            onClick={() => { onSelect(null); setOpen(false); }}
            className={
              "w-full text-left flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-bg-input/50 " +
              (activeId == null ? "text-accent" : "text-text")
            }
          >
            <span className="w-2 h-2 rounded-full bg-text-dim" />
            <span className="flex-1">All profiles</span>
          </button>
          {profiles.length > 0 && <div className="border-t border-border my-1" />}
          {profiles.map((p) => (
            <button
              key={p.id}
              onClick={() => { onSelect(p.id); setOpen(false); }}
              className={
                "w-full text-left flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-bg-input/50 " +
                (p.id === activeId ? "text-accent" : "text-text")
              }
              title={p.description}
            >
              <span
                className="w-2 h-2 rounded-full flex-shrink-0"
                style={{ backgroundColor: p.color || "#94a3b8" }}
              />
              <span className="flex-1 truncate">{p.name}</span>
              {p.is_default && <span className="text-text-dim text-xs">default</span>}
              <span className="text-text-dim text-xs">{p.account_count ?? 0}</span>
            </button>
          ))}
          <div className="border-t border-border my-1" />
          <button
            onClick={() => { onManage(); setOpen(false); }}
            className="w-full text-left px-3 py-1.5 text-sm text-text-muted hover:bg-bg-input/50 hover:text-text"
          >
            Manage profiles…
          </button>
        </div>
      )}
    </div>
  );
}

// --- ProfileManageModal: create / rename / set-default / delete ---

function ProfileManageModal({
  profiles, accounts, onClose, onChanged, setStatus,
}: {
  profiles: Profile[];
  accounts: SocialAccount[];
  onClose: () => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
}) {
  const [newName, setNewName] = useState("");
  const [newColor, setNewColor] = useState("#3b82f6");
  const [busy, setBusy] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState<Profile | null>(null);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    setBusy(true);
    try {
      const res = await fetch(`${API}/profiles`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, color: newColor }),
      });
      if (!res.ok) throw new Error(await res.text());
      setNewName("");
      onChanged();
    } catch (e) {
      setStatus("Create profile: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const promote = async (id: number) => {
    try {
      await fetch(`${API}/profiles/${id}`, {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ is_default: true }),
      });
      onChanged();
    } catch (e) {
      setStatus("Promote: " + (e as Error).message);
    }
  };

  const rename = async (id: number, name: string) => {
    try {
      await fetch(`${API}/profiles/${id}`, {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      });
      onChanged();
    } catch (e) {
      setStatus("Rename: " + (e as Error).message);
    }
  };

  const recolor = async (id: number, color: string) => {
    try {
      await fetch(`${API}/profiles/${id}`, {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ color }),
      });
      onChanged();
    } catch (e) {
      setStatus("Recolor: " + (e as Error).message);
    }
  };

  const removeProfile = async (id: number) => {
    try {
      const res = await fetch(`${API}/profiles/${id}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      onChanged();
    } catch (e) {
      setStatus("Delete: " + (e as Error).message);
    }
  };

  const moveAccount = async (accountId: number, profileId: number) => {
    try {
      await fetch(`${API}/profiles/${profileId}/move`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account_ids: [accountId] }),
      });
      onChanged();
    } catch (e) {
      setStatus("Move: " + (e as Error).message);
    }
  };

  // Unassigned accounts (profile_id=0) get their own group at the
  // bottom — they're a migration relic or a deletion fallback. The
  // user wants to see them so they can re-home each one.
  const unassigned = accounts.filter((a) => !a.profile_id);

  return (
    <>
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded-lg shadow-lg w-[640px] max-w-[92vw] max-h-[85vh] flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-4 py-3 border-b border-border flex items-center justify-between">
          <div className="text-text font-medium">Manage profiles</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <div className="p-4 flex-1 overflow-y-auto space-y-4">
          {/* Create */}
          <div className="border border-border rounded p-3 flex items-center gap-2">
            <input
              type="color"
              value={newColor}
              onChange={(e) => setNewColor(e.target.value)}
              className="w-9 h-9 rounded border border-border cursor-pointer"
            />
            <input
              type="text"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="New profile name (e.g. SocialCast, PaidKit, Personal)"
              className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
              onKeyDown={(e) => { if (e.key === "Enter") create(); }}
            />
            <button
              onClick={create}
              disabled={!newName.trim() || busy}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              Create
            </button>
          </div>

          {/* Existing */}
          <div className="space-y-2">
            {profiles.map((p) => (
              <div key={p.id} className="border border-border rounded p-3 flex flex-col gap-2">
                <div className="flex items-center gap-2">
                  <input
                    type="color"
                    value={p.color || "#94a3b8"}
                    onChange={(e) => recolor(p.id, e.target.value)}
                    className="w-7 h-7 rounded border border-border cursor-pointer"
                  />
                  <input
                    type="text"
                    defaultValue={p.name}
                    onBlur={(e) => {
                      const v = e.target.value.trim();
                      if (v && v !== p.name) rename(p.id, v);
                    }}
                    className="flex-1 bg-transparent border-b border-transparent hover:border-border focus:border-accent outline-none text-text font-medium"
                  />
                  <span className="text-text-dim text-xs">{p.account_count ?? 0} accounts</span>
                  {!p.is_default && (
                    <button
                      onClick={() => promote(p.id)}
                      className="text-xs text-accent hover:underline"
                    >
                      Set default
                    </button>
                  )}
                  {p.is_default && <span className="text-text-dim text-xs">default</span>}
                  <button
                    onClick={() => setConfirmingDelete(p)}
                    className="text-xs text-text-muted hover:text-red"
                  >
                    Delete
                  </button>
                </div>
                <div className="text-text-dim text-xs">
                  slug: <code>{p.slug}</code>
                </div>
              </div>
            ))}
            {profiles.length === 0 && (
              <div className="text-text-dim text-sm py-6 text-center">
                No profiles yet. Create one above.
              </div>
            )}
          </div>

          {/* Unassigned accounts — move them into a profile */}
          {unassigned.length > 0 && profiles.length > 0 && (
            <div className="border border-border rounded p-3">
              <div className="text-text-muted text-xs uppercase tracking-wide mb-2">
                Unassigned accounts ({unassigned.length})
              </div>
              <div className="space-y-1">
                {unassigned.map((a) => (
                  <div key={a.id} className="flex items-center gap-2">
                    {a.avatar_url ? (
                      <img src={a.avatar_url} alt="" className="w-6 h-6 rounded-full" />
                    ) : (
                      <div className="w-6 h-6 rounded-full bg-bg-input" />
                    )}
                    <span className="text-text text-sm flex-1 truncate">{a.display_name}</span>
                    <span className="text-text-dim text-xs">{a.platform}</span>
                    <select
                      defaultValue=""
                      onChange={(e) => {
                        const id = Number(e.target.value);
                        if (id) moveAccount(a.id, id);
                      }}
                      className="text-xs bg-bg-input border border-border rounded px-1 py-0.5"
                    >
                      <option value="">Move to…</option>
                      {profiles.map((p) => (
                        <option key={p.id} value={p.id}>{p.name}</option>
                      ))}
                    </select>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
    {confirmingDelete && (
      <ConfirmDialog
        title={`Delete profile "${confirmingDelete.name}"?`}
        body={
          <>
            Accounts and posts assigned to this profile will become
            unassigned — they won't be deleted. You can re-home them
            from the unassigned bucket below.
          </>
        }
        confirmLabel="Delete profile"
        onClose={() => setConfirmingDelete(null)}
        onConfirm={async () => {
          const p = confirmingDelete;
          setConfirmingDelete(null);
          if (p) await removeProfile(p.id);
        }}
      />
    )}
    </>
  );
}

function Tab({
  label, value, current, onClick, count,
}: {
  label: string; value: "accounts" | "posts" | "metrics";
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
  const [confirming, setConfirming] = useState(false);
  const doRemove = async () => {
    try {
      await fetch(`${API}/accounts/${account.id}`, { method: "DELETE", credentials: "same-origin" });
      setStatus("Disconnected.");
      onChange();
    } catch (e) {
      setStatus("Disconnect failed: " + (e as Error).message);
    }
  };
  return (
    <>
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
          onClick={() => setConfirming(true)}
          className="text-text-muted hover:text-error text-xs"
          title="Disconnect"
        >
          ×
        </button>
      </div>
      {confirming && (
        <ConfirmDialog
          title={`Disconnect ${account.display_name}?`}
          body={
            <>
              The OAuth grant stays valid upstream — you can re-add this {account.platform} account
              later without going through the auth dance again. Scheduled posts targeting this
              account will fail to publish until reconnected.
            </>
          }
          confirmLabel="Disconnect"
          onClose={() => setConfirming(false)}
          onConfirm={async () => {
            await doRemove();
            setConfirming(false);
          }}
        />
      )}
    </>
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
                  <span className="text-text-dim text-xs ml-2">({pickerKind(p.platform).singular} picker after auth)</span>
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
  const [platform, setPlatform] = useState<string>("");
  const kind = pickerKind(platform);

  useEffect(() => {
    fetch(`${API}/accounts/${pendingId}/pages`, { credentials: "same-origin" })
      .then((r) => r.json())
      .then((d) => {
        setPages(d.pages || []);
        setPlatform(d.platform || "");
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
            <div className="text-text font-medium">Pick which {kind.singular} to connect</div>
            {!loading && pages.length > 0 && (
              <div className="text-text-dim text-xs mt-0.5">
                {filtered.length === pages.length
                  ? `${pages.length} ${pages.length === 1 ? kind.singular : kind.plural}`
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
              placeholder={`Search ${kind.plural}…`}
              className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text focus:outline-none focus:border-accent"
            />
          </div>
        )}

        <div className="flex-1 overflow-y-auto p-3">
          {loading ? (
            <div className="text-text-dim text-sm py-8 text-center">Loading…</div>
          ) : pages.length === 0 ? (
            <div className="text-text-dim text-sm py-8 text-center">No {kind.plural} found.</div>
          ) : filtered.length === 0 ? (
            <div className="text-text-dim text-sm py-8 text-center">
              No {kind.plural} match "{filter}".
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

// OptionFieldInput renders one platform-specific override input —
// text, textarea, select dropdown, or comma-separated tags — based
// on the platformDef's declared field type. Server tells us which
// fields exist for which platform via /platforms; this component
// just reflects what the server says without hard-coding any
// platform's schema in the panel.
function OptionFieldInput({
  field, value, onChange,
}: {
  field: OptionField;
  value: any;
  onChange: (v: any) => void;
}) {
  const labelEl = (
    <div className="flex items-baseline gap-2">
      <label className="text-xs uppercase tracking-wide text-text-dim">{field.label}</label>
      {field.help && <span className="text-text-dim text-[10px]">{field.help}</span>}
    </div>
  );

  if (field.type === "select") {
    return (
      <div className="flex flex-col gap-1">
        {labelEl}
        <select
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          <option value="">Use default</option>
          {(field.options || []).map((opt) => (
            <option key={opt} value={opt}>{opt}</option>
          ))}
        </select>
      </div>
    );
  }

  if (field.type === "textarea") {
    return (
      <div className="flex flex-col gap-1">
        {labelEl}
        <textarea
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
          placeholder="Override the default body for this target"
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[80px] resize-y"
        />
      </div>
    );
  }

  if (field.type === "tags") {
    // Comma-separated input → string[]. Empty string clears.
    const asString = Array.isArray(value) ? value.join(", ") : (typeof value === "string" ? value : "");
    return (
      <div className="flex flex-col gap-1">
        {labelEl}
        <input
          type="text"
          value={asString}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw.trim() === "") onChange("");
            else onChange(raw.split(",").map((t) => t.trim()).filter(Boolean));
          }}
          placeholder="comma, separated, tags"
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
      </div>
    );
  }

  // Default: plain text input
  return (
    <div className="flex flex-col gap-1">
      {labelEl}
      <input
        type="text"
        value={typeof value === "string" ? value : ""}
        onChange={(e) => onChange(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      />
    </div>
  );
}

function ComposeDialog({
  accounts, platforms, activeProfile, onClose, onCreated, setStatus,
}: {
  accounts: SocialAccount[];
  platforms: PlatformInfo[];
  activeProfile: Profile | null;
  onClose: () => void;
  onCreated: () => void;
  setStatus: (s: string) => void;
}) {
  const [body, setBody] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [scheduleAt, setScheduleAt] = useState("");
  const [busy, setBusy] = useState(false);
  // Per-account overrides keyed by account id. Sparse — only accounts
  // the user has actually customized appear here. At submit time, if
  // any account has non-empty options the call uses targets[]; otherwise
  // the simple social_account_ids[] form ships.
  const [accountOptions, setAccountOptions] = useState<Record<number, Record<string, any>>>({});
  // Which account's options are currently expanded inline. One at a
  // time keeps the form readable; null = nothing expanded.
  const [expanded, setExpanded] = useState<number | null>(null);
  // Media attached to the post. We upload immediately to the storage app
  // (so the post_create call only carries IDs, not bytes) and remember the
  // returned id + a local preview URL. The previewURL is a local
  // ObjectURL — cheap, but we revoke it on remove + unmount so the
  // browser doesn't keep the bytes around forever.
  const [media, setMedia] = useState<{ id: number; name: string; mime: string; previewURL: string }[]>([]);
  const [uploading, setUploading] = useState(false);
  const [showPicker, setShowPicker] = useState(false);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  // Quick lookup: option_fields by platform name. Empty array when the
  // platform has no per-target customisation today.
  const fieldsByPlatform: Record<string, OptionField[]> = {};
  for (const p of platforms) fieldsByPlatform[p.platform] = p.option_fields || [];

  // Returns true when the given account's stored options have any
  // non-empty value — drives whether we count the account as customized
  // (shows a "•" badge on the expander button + decides targets[] vs
  // social_account_ids[] at submit time).
  const isCustomized = (accountId: number): boolean => {
    const opts = accountOptions[accountId];
    if (!opts) return false;
    return Object.values(opts).some((v) =>
      v != null && (typeof v === "string" ? v.trim() !== "" : true)
    );
  };

  const setAccountOption = (accountId: number, key: string, value: any) => {
    setAccountOptions((prev) => {
      const next = { ...prev, [accountId]: { ...(prev[accountId] || {}), [key]: value } };
      // If the field is being cleared, remove the key entirely so the
      // submit-shape decision doesn't see a phantom empty string.
      if (typeof value === "string" && value.trim() === "") {
        const acct = { ...next[accountId] };
        delete acct[key];
        if (Object.keys(acct).length === 0) {
          const { [accountId]: _drop, ...rest } = next;
          return rest;
        }
        next[accountId] = acct;
      }
      return next;
    });
  };

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

  // Add files chosen from the storage browser. Same shape as the
  // upload flow — the only difference is previewURL points at storage's
  // /content endpoint instead of a local ObjectURL. Skip ids already in
  // media so users don't double-attach the same file.
  const addFromStorage = (picked: { id: number; name: string; content_type: string }[]) => {
    setMedia((prev) => {
      const existing = new Set(prev.map((m) => m.id));
      const adds = picked
        .filter((f) => !existing.has(f.id))
        .map((f) => ({
          id: f.id,
          name: f.name,
          mime: f.content_type || "",
          previewURL: `/api/apps/storage/files/${f.id}/content`,
        }));
      return [...prev, ...adds];
    });
    setStatus(`Added ${picked.length} file${picked.length !== 1 ? "s" : ""} from storage.`);
  };

  const submit = async () => {
    if (!body.trim() || selected.size === 0) return;
    setBusy(true);
    setStatus("Posting…");
    try {
      // Choose between the simple multicast shape (social_account_ids[])
      // and the per-target shape (targets[]). Use targets[] only when
      // at least one selected account has non-empty options — keeps the
      // common case as terse as it was before.
      const selectedIds = Array.from(selected);
      const anyCustomized = selectedIds.some((id) => isCustomized(id));
      const payload: Record<string, any> = {
        body,
        schedule_at: scheduleAt || undefined,
        media_storage_ids: media.length > 0 ? media.map((m) => m.id) : undefined,
        // When the panel is scoped to one profile, tag the post
        // with that profile_id so post_list filtering keeps it
        // visible. Without this, mixed-profile sessions could
        // create "no profile" posts that disappear from filtered
        // views even though their accounts are tagged.
        profile_id: activeProfile?.id,
      };
      if (anyCustomized) {
        payload.targets = selectedIds.map((id) => ({
          social_account_id: id,
          ...(accountOptions[id] || {}),
        }));
      } else {
        payload.social_account_ids = selectedIds;
      }
      const res = await fetch(`${API}/posts`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
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
    <>
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
            <div className="flex items-center gap-3">
              <button
                type="button"
                onClick={() => setShowPicker(true)}
                className="text-xs text-accent hover:underline"
              >
                Browse storage
              </button>
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
          <div className="flex flex-col gap-2">
            {accounts.map((a) => {
              const isSelected = selected.has(a.id);
              const fields = fieldsByPlatform[a.platform] || [];
              const hasCustomization = fields.length > 0;
              const isExpanded = expanded === a.id;
              const customized = isCustomized(a.id);
              return (
                <div
                  key={a.id}
                  className={
                    "border rounded transition-colors " +
                    (isSelected ? "border-accent bg-bg-card" : "border-border")
                  }
                >
                  <div className="flex items-center gap-3 px-3 py-2">
                    <input
                      type="checkbox"
                      checked={isSelected}
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
                    {isSelected && hasCustomization && (
                      <button
                        type="button"
                        onClick={() => setExpanded(isExpanded ? null : a.id)}
                        className={
                          "text-xs px-2 py-1 rounded border transition-colors " +
                          (customized
                            ? "border-accent text-accent"
                            : "border-border text-text-dim hover:text-text hover:border-text-dim")
                        }
                        title={
                          customized
                            ? "This target has custom settings"
                            : "Set per-target overrides for this account"
                        }
                      >
                        {customized ? "Customized" : "Customize"}
                        {customized && <span className="ml-1 text-accent">•</span>}
                      </button>
                    )}
                  </div>
                  {isSelected && hasCustomization && isExpanded && (
                    <div className="border-t border-border px-3 py-3 flex flex-col gap-3 bg-bg/40">
                      {fields.map((f) => (
                        <OptionFieldInput
                          key={f.name}
                          field={f}
                          value={accountOptions[a.id]?.[f.name]}
                          onChange={(v) => setAccountOption(a.id, f.name, v)}
                        />
                      ))}
                    </div>
                  )}
                </div>
              );
            })}
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
    {showPicker && (
      <StoragePickerDialog
        excludeIds={new Set(media.map((m) => m.id))}
        onClose={() => setShowPicker(false)}
        onPick={(picked) => {
          addFromStorage(picked);
          setShowPicker(false);
        }}
      />
    )}
    </>
  );
}

// --- StoragePickerDialog ------------------------------------------
//
// Lets the user pick existing files from the storage app instead of
// uploading new ones. Lists `/api/apps/storage/files` (the same HTTP
// route the storage panel uses), filters client-side to image/video
// MIMEs, and renders a grid of thumbnails sourced from each file's
// `/content` endpoint. Multi-select; "Add" returns the selected rows
// to the caller, who folds them into the compose dialog's `media`
// state with the same shape as a fresh upload.
//
// Stacked on top of ComposeDialog (z-[60] vs ComposeDialog's z-50).

interface StorageFile {
  id: number;
  name: string;
  content_type: string;
  folder?: string;
  size_bytes?: number;
}

function StoragePickerDialog({
  excludeIds, onClose, onPick,
}: {
  excludeIds: Set<number>;
  onClose: () => void;
  onPick: (files: StorageFile[]) => void;
}) {
  const [files, setFiles] = useState<StorageFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<"all" | "image" | "video">("all");
  const [picked, setPicked] = useState<Set<number>>(new Set());

  // Re-fetch on q/kind change. The storage app does prefix-match on
  // content_type via SQL LIKE, so passing "image/" filters server-side;
  // for "all" we fetch unfiltered and drop non-media client-side so
  // there's only ever one round-trip per change.
  useEffect(() => {
    let cancelled = false;
    const run = async () => {
      setLoading(true);
      setError(null);
      try {
        const params = new URLSearchParams();
        params.set("limit", "100");
        if (q.trim()) params.set("q", q.trim());
        if (kind === "image") params.set("content_type", "image/");
        else if (kind === "video") params.set("content_type", "video/");
        const res = await fetch(`/api/apps/storage/files?${params.toString()}`, {
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json() as { files: StorageFile[] };
        if (cancelled) return;
        const usable = (data.files || []).filter(
          (f) => kind !== "all" ||
            (f.content_type || "").startsWith("image/") ||
            (f.content_type || "").startsWith("video/")
        );
        setFiles(usable);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    // Debounce text input so we don't hammer storage on every keystroke.
    const t = setTimeout(run, q.trim() ? 200 : 0);
    return () => { cancelled = true; clearTimeout(t); };
  }, [q, kind]);

  const toggle = (id: number) => {
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const confirm = () => {
    const chosen = files.filter((f) => picked.has(f.id));
    if (chosen.length === 0) return;
    onPick(chosen);
  };

  return (
    <div
      className="fixed inset-0 z-[60] grid place-items-center bg-black/60"
      onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div className="bg-bg border border-border rounded-lg w-[min(720px,92vw)] max-h-[85vh] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div className="text-sm font-bold text-text">Pick from storage</div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-dim hover:text-text text-lg leading-none"
            aria-label="Close"
          >
            ×
          </button>
        </div>

        <div className="flex items-center gap-2 px-4 py-2 border-b border-border">
          <input
            type="text"
            placeholder="Search by name…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
          />
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as "all" | "image" | "video")}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          >
            <option value="all">All media</option>
            <option value="image">Images</option>
            <option value="video">Videos</option>
          </select>
        </div>

        <div className="flex-1 overflow-y-auto p-4">
          {loading && (
            <div className="py-12 text-center text-text-dim text-sm">Loading…</div>
          )}
          {error && !loading && (
            <div className="py-12 text-center text-red text-sm">Couldn't load files: {error}</div>
          )}
          {!loading && !error && files.length === 0 && (
            <div className="py-12 text-center text-text-dim text-sm">
              No matching files in storage. Upload one with "+ Attach image / video".
            </div>
          )}
          {!loading && !error && files.length > 0 && (
            <div className="grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 gap-3">
              {files.map((f) => {
                const already = excludeIds.has(f.id);
                const sel = picked.has(f.id);
                const isVideo = (f.content_type || "").startsWith("video/");
                const src = `/api/apps/storage/files/${f.id}/content`;
                return (
                  <button
                    key={f.id}
                    type="button"
                    disabled={already}
                    onClick={() => toggle(f.id)}
                    className={
                      "relative aspect-square rounded border overflow-hidden bg-bg-input flex flex-col items-stretch text-left transition-colors " +
                      (already
                        ? "opacity-40 cursor-not-allowed border-border"
                        : sel
                          ? "border-accent ring-2 ring-accent"
                          : "border-border hover:border-text-dim")
                    }
                    title={already ? `${f.name} (already attached)` : f.name}
                  >
                    {isVideo ? (
                      <video src={src} className="w-full h-full object-cover" muted preload="metadata" />
                    ) : (
                      <img src={src} alt={f.name} className="w-full h-full object-cover" loading="lazy" />
                    )}
                    {sel && (
                      <div className="absolute top-1 right-1 w-5 h-5 rounded-full bg-accent text-bg text-xs font-bold grid place-items-center">
                        ✓
                      </div>
                    )}
                    <div className="absolute bottom-0 inset-x-0 bg-black/60 text-white text-[10px] px-1.5 py-0.5 truncate">
                      {f.name}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between px-4 py-3 border-t border-border">
          <div className="text-xs text-text-dim">
            {picked.size > 0 ? `${picked.size} selected` : `${files.length} file${files.length !== 1 ? "s" : ""}`}
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-card"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={confirm}
              disabled={picked.size === 0}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              Add {picked.size > 0 ? picked.size : ""}
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
  // Open reschedule dialog for a specific post (null = closed).
  const [rescheduleFor, setRescheduleFor] = useState<Post | null>(null);
  // Same pattern for the delete-confirm modal: which post (null = closed).
  const [deleteFor, setDeleteFor] = useState<Post | null>(null);

  const retry = async (postId: number) => {
    try {
      await fetch(`${API}/posts/${postId}/retry`, { method: "POST", credentials: "same-origin" });
      setStatus("Retry triggered.");
      onChange();
    } catch (e) {
      setStatus("Retry failed: " + (e as Error).message);
    }
  };

  const executeDelete = async (post: Post) => {
    try {
      const res = await fetch(`${API}/posts/${post.id}`, {
        method: "DELETE", credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const body = await res.json().catch(() => null) as { upstream?: { platform: string; status: string; error?: string }[] } | null;
      const upstream = body?.upstream || [];
      const failed = upstream.filter(o => o.status === "failed");
      const unsupported = upstream.filter(o => o.status === "unsupported");
      if (failed.length > 0) {
        const names = Array.from(new Set(failed.map(o => o.platform))).join(", ");
        setStatus(`Deleted locally. Upstream removal failed on: ${names}. The post may still be live there.`);
      } else if (unsupported.length > 0) {
        const names = Array.from(new Set(unsupported.map(o => o.platform))).join(", ");
        setStatus(`Deleted. Note: ${names} doesn't permit programmatic deletion — remove the post in-app if needed.`);
      } else {
        setStatus("Deleted.");
      }
      onChange();
    } catch (e) {
      setStatus("Delete failed: " + (e as Error).message);
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
            {p.status === "scheduled" && (
              <button
                onClick={() => setRescheduleFor(p)}
                className="text-xs text-accent hover:underline"
                title="Pick a new run time"
              >
                Reschedule
              </button>
            )}
            <button
              onClick={() => setDeleteFor(p)}
              className="text-xs text-text-muted hover:text-red"
              title={p.status === "scheduled" ? "Cancel + delete" : "Delete"}
            >
              {p.status === "scheduled" ? "Cancel" : "Delete"}
            </button>
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
      {rescheduleFor && (
        <RescheduleDialog
          post={rescheduleFor}
          onClose={() => setRescheduleFor(null)}
          onChanged={() => { setRescheduleFor(null); onChange(); }}
          setStatus={setStatus}
        />
      )}
      {deleteFor && (
        <DeleteConfirmDialog
          post={deleteFor}
          onClose={() => setDeleteFor(null)}
          onConfirm={async () => {
            const p = deleteFor;
            setDeleteFor(null);
            await executeDelete(p);
          }}
        />
      )}
    </div>
  );
}

// --- DeleteConfirmDialog ------------------------------------------
//
// Replaces the native confirm() prompt for post deletion. Same status-
// aware copy as before but rendered as an in-app modal so it matches
// the rest of the panel and can highlight the upstream-deletion
// behavior more legibly than a single string crammed into a browser
// alert. The actual fetch lives in PostsView.executeDelete; this
// component only collects the user's intent.

function DeleteConfirmDialog({
  post, onClose, onConfirm,
}: {
  post: Post;
  onClose: () => void;
  onConfirm: () => void | Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const isScheduled = post.status === "scheduled";
  const willTouchUpstream = post.status === "published" || post.status === "partial";

  const title =
    isScheduled ? "Cancel scheduled post?" :
    willTouchUpstream ? "Delete post?" :
    "Delete post?";

  const handleConfirm = async () => {
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/60"
      onClick={(e) => { if (e.target === e.currentTarget && !busy) onClose(); }}
    >
      <div className="bg-bg-card border border-border rounded-lg shadow-lg w-[min(480px,92vw)] p-5 flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <div className="text-text font-bold">{title}</div>
          <button
            onClick={onClose}
            disabled={busy}
            className="text-text-muted hover:text-text text-lg leading-none disabled:opacity-50"
            aria-label="Close"
          >
            ×
          </button>
        </div>

        <div className="text-text text-sm whitespace-pre-wrap line-clamp-3 bg-bg-input border border-border rounded px-3 py-2">
          {post.body}
        </div>

        {isScheduled && (
          <div className="text-text-dim text-sm">
            This will cancel the scheduled job and remove the post locally.
            Nothing has been published yet, so no platform is affected.
          </div>
        )}

        {willTouchUpstream && (
          <div className="flex flex-col gap-2 text-sm">
            <div className="text-text-dim">
              The post will be removed locally. Where the platform's API allows it,
              we'll also remove the upstream copy:
            </div>
            <ul className="flex flex-col gap-1.5">
              <li className="flex items-start gap-2">
                <span className="text-green mt-0.5">✓</span>
                <span className="text-text">
                  <span className="font-medium">X, Facebook, YouTube</span>
                  <span className="text-text-dim"> — upstream copy will be deleted</span>
                </span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-text-muted mt-0.5">○</span>
                <span className="text-text">
                  <span className="font-medium">Instagram, TikTok</span>
                  <span className="text-text-dim"> — keeps its copy; delete it in the app</span>
                </span>
              </li>
            </ul>
          </div>
        )}

        {!isScheduled && !willTouchUpstream && (
          <div className="text-text-dim text-sm">
            This post hasn't been published. The local row will be removed.
          </div>
        )}

        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-card disabled:opacity-50"
          >
            {isScheduled ? "Keep scheduled" : "Cancel"}
          </button>
          <button
            type="button"
            onClick={handleConfirm}
            disabled={busy}
            className="px-3 py-1.5 text-sm bg-red text-bg rounded font-bold hover:opacity-90 disabled:opacity-50"
          >
            {busy ? "…" : isScheduled ? "Cancel post" : "Delete"}
          </button>
        </div>
      </div>
    </div>
  );
}

// --- ConfirmDialog -----------------------------------------------
//
// Reusable destructive-action confirmation modal. DeleteConfirmDialog
// is intentionally left separate because it has more bespoke layout
// (post body preview + per-platform upstream breakdown). For simple
// "are you sure?" cases — disconnect account, delete profile — use
// this one. Async onConfirm: the button shows a loading state and the
// backdrop+close are disabled while the request is in flight.

function ConfirmDialog({
  title, body, confirmLabel = "Confirm", cancelLabel = "Cancel",
  onConfirm, onClose,
}: {
  title: string;
  body?: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  onConfirm: () => void | Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const handleConfirm = async () => {
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/60"
      onClick={(e) => { if (e.target === e.currentTarget && !busy) onClose(); }}
    >
      <div className="bg-bg-card border border-border rounded-lg shadow-lg w-[min(440px,92vw)] p-5 flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <div className="text-text font-bold">{title}</div>
          <button
            onClick={onClose}
            disabled={busy}
            className="text-text-muted hover:text-text text-lg leading-none disabled:opacity-50"
            aria-label="Close"
          >
            ×
          </button>
        </div>
        {body && <div className="text-text-dim text-sm">{body}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-card disabled:opacity-50"
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            onClick={handleConfirm}
            disabled={busy}
            className="px-3 py-1.5 text-sm bg-red text-bg rounded font-bold hover:opacity-90 disabled:opacity-50"
          >
            {busy ? "…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

function RescheduleDialog({
  post, onClose, onChanged, setStatus,
}: {
  post: Post;
  onClose: () => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
}) {
  // Seed the input with the post's current schedule_at as a
  // datetime-local value (the input wants "YYYY-MM-DDTHH:MM",
  // sliced from the ISO/RFC3339 string the server stored).
  const seed = (post.schedule_at || "").slice(0, 16);
  const [when, setWhen] = useState(seed);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!when) return;
    setBusy(true);
    try {
      const res = await fetch(`${API}/posts/${post.id}/reschedule`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ schedule_at: when }),
      });
      if (!res.ok) throw new Error(await res.text());
      setStatus("Rescheduled.");
      onChanged();
    } catch (e) {
      setStatus("Reschedule failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded-lg shadow-lg w-[420px] max-w-[92vw] p-4 flex flex-col gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">Reschedule post #{post.id}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <div className="text-text-dim text-xs whitespace-pre-wrap">{post.body}</div>
        <label className="text-xs uppercase tracking-wide text-text-dim">New run time</label>
        <input
          type="datetime-local"
          value={when}
          onChange={(e) => setWhen(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          autoFocus
        />
        <div className="flex items-center justify-end gap-2">
          <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
          <button
            onClick={submit}
            disabled={!when || busy || when === seed}
            className="px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {busy ? "…" : "Reschedule"}
          </button>
        </div>
      </div>
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

// --- MetricsView --------------------------------------------------
//
// Two sections:
//   1. Account-level totals — one row per connected account, click to
//      load (lazy; the agent flow doesn't need all of them at once).
//   2. Recent published posts — table with normalized metrics columns.
//      Click a row to expand and fetch per-target details (raw blob
//      included for deep-dives).
//
// All data is fetched fresh on click — no caching today, matching the
// MCP-tool semantics. Be mindful that scanning many posts will burn
// upstream rate limits.

interface PostMetrics {
  post_id: number;
  body: string;
  status: string;
  targets: TargetMetrics[];
}

interface TargetMetrics {
  target_id: number;
  social_account_id: number;
  platform: string;
  platform_post_id?: string;
  platform_url?: string;
  status: "ok" | "unsupported" | "skipped" | "failed";
  reason?: string;
  error?: string;
  metrics?: {
    views: number;
    likes: number;
    comments: number;
    shares: number;
    raw?: any;
  };
}

interface AccountMetrics {
  social_account_id: number;
  platform: string;
  display_name: string;
  status: "ok" | "unsupported" | "failed";
  reason?: string;
  error?: string;
  followers?: number;
  following?: number;
  total_likes?: number;
  total_videos?: number;
  raw?: any;
}

function MetricsView({
  posts, accounts, setStatus,
}: {
  posts: Post[];
  accounts: SocialAccount[];
  setStatus: (s: string) => void;
}) {
  // accountFor[accountId] = AccountMetrics — fetched lazily.
  const [accountFor, setAccountFor] = useState<Record<number, AccountMetrics | "loading" | { error: string }>>({});
  // postFor[postId] = PostMetrics — fetched on row expand.
  const [postFor, setPostFor] = useState<Record<number, PostMetrics | "loading" | { error: string }>>({});
  const [expanded, setExpanded] = useState<number | null>(null);

  const loadAccount = async (id: number) => {
    setAccountFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(`${API}/accounts/${id}/metrics`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as AccountMetrics;
      setAccountFor((prev) => ({ ...prev, [id]: data }));
    } catch (e) {
      setAccountFor((prev) => ({ ...prev, [id]: { error: (e as Error).message } }));
    }
  };

  const loadPost = async (id: number) => {
    setPostFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(`${API}/posts/${id}/metrics`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as PostMetrics;
      setPostFor((prev) => ({ ...prev, [id]: data }));
    } catch (e) {
      setPostFor((prev) => ({ ...prev, [id]: { error: (e as Error).message } }));
      setStatus("Metrics fetch failed: " + (e as Error).message);
    }
  };

  const togglePost = (id: number) => {
    if (expanded === id) {
      setExpanded(null);
      return;
    }
    setExpanded(id);
    if (!postFor[id] || (typeof postFor[id] === "object" && "error" in (postFor[id] as any))) {
      loadPost(id);
    }
  };

  const published = posts.filter((p) => p.status === "published" || p.status === "partial");

  return (
    <div className="p-4 flex flex-col gap-6">
      {/* ── Accounts section ── */}
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="text-sm uppercase tracking-wide text-text-dim">Accounts</h2>
          <span className="text-text-dim text-xs">Click to load totals</span>
        </div>
        {accounts.length === 0 ? (
          <div className="text-text-dim text-sm py-6 text-center">No accounts connected.</div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {accounts.map((a) => {
              const m = accountFor[a.id];
              return (
                <button
                  key={a.id}
                  onClick={() => loadAccount(a.id)}
                  disabled={m === "loading"}
                  className="text-left flex items-center gap-3 px-3 py-2 border border-border rounded hover:border-text-dim disabled:opacity-50"
                >
                  {a.avatar_url ? (
                    <img src={a.avatar_url} alt="" className="w-8 h-8 rounded-full flex-shrink-0" />
                  ) : (
                    <div className="w-8 h-8 rounded-full bg-bg-input flex-shrink-0" />
                  )}
                  <div className="flex-1 min-w-0">
                    <div className="text-text text-sm truncate">{a.display_name}</div>
                    <div className="text-text-dim text-xs">{a.platform}</div>
                  </div>
                  <AccountMetricsCell m={m} />
                </button>
              );
            })}
          </div>
        )}
      </section>

      {/* ── Posts section ── */}
      <section className="flex flex-col gap-2">
        <h2 className="text-sm uppercase tracking-wide text-text-dim">Recent posts</h2>
        {published.length === 0 ? (
          <div className="text-text-dim text-sm py-6 text-center">No published posts yet.</div>
        ) : (
          <div className="flex flex-col gap-2">
            {published.map((p) => {
              const m = postFor[p.id];
              const isExpanded = expanded === p.id;
              const totals = (typeof m === "object" && m && "targets" in m)
                ? aggregateTotals((m as PostMetrics).targets)
                : null;
              return (
                <div key={p.id} className="border border-border rounded">
                  <button
                    onClick={() => togglePost(p.id)}
                    className="w-full text-left flex items-start gap-3 px-3 py-2 hover:bg-bg-card"
                  >
                    <div className="flex-1 min-w-0">
                      <div className="text-text text-sm whitespace-nowrap overflow-hidden text-ellipsis">
                        {p.body || <span className="text-text-dim italic">no body</span>}
                      </div>
                      <div className="text-text-dim text-xs mt-0.5">
                        {new Date(p.created_at).toLocaleString()}
                        {p.targets && p.targets.length > 0 && (
                          <span className="ml-2">
                            · {p.targets.length} target{p.targets.length !== 1 ? "s" : ""}
                          </span>
                        )}
                      </div>
                    </div>
                    {totals ? (
                      <MetricsRow totals={totals} />
                    ) : m === "loading" ? (
                      <span className="text-text-dim text-xs">Loading…</span>
                    ) : (
                      <span className="text-text-dim text-xs">↓</span>
                    )}
                  </button>
                  {isExpanded && (
                    <div className="border-t border-border px-3 py-2 bg-bg/40 flex flex-col gap-2">
                      {m === "loading" && <div className="text-text-dim text-sm">Loading metrics…</div>}
                      {typeof m === "object" && m && "error" in m && (
                        <div className="text-red text-sm">{(m as any).error}</div>
                      )}
                      {typeof m === "object" && m && "targets" in m && (
                        <div className="flex flex-col gap-2">
                          {(m as PostMetrics).targets.map((t) => (
                            <TargetMetricsBlock key={t.target_id} target={t} />
                          ))}
                          <button
                            onClick={() => loadPost(p.id)}
                            className="self-start text-xs text-accent hover:underline mt-1"
                          >
                            ↻ Refresh
                          </button>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}

function AccountMetricsCell({ m }: { m: any }) {
  if (!m) return <span className="text-text-dim text-xs">→</span>;
  if (m === "loading") return <span className="text-text-dim text-xs">…</span>;
  if (typeof m === "object" && "error" in m) {
    return <span className="text-red text-xs">err</span>;
  }
  const am = m as AccountMetrics;
  if (am.status === "unsupported") {
    return <span className="text-text-dim text-xs italic">unsupported</span>;
  }
  if (am.status === "failed") {
    return <span className="text-red text-xs" title={am.error}>failed</span>;
  }
  const bits: string[] = [];
  if (am.followers != null) bits.push(`${formatNumber(am.followers)} followers`);
  if (am.total_videos != null && am.total_videos > 0) bits.push(`${am.total_videos} videos`);
  return <span className="text-text text-xs">{bits.join(" · ") || "ok"}</span>;
}

function MetricsRow({ totals }: { totals: { views: number; likes: number; comments: number; shares: number } }) {
  return (
    <div className="flex items-center gap-3 text-xs text-text-dim flex-shrink-0">
      <Stat label="views" value={totals.views} />
      <Stat label="likes" value={totals.likes} />
      <Stat label="comments" value={totals.comments} />
      <Stat label="shares" value={totals.shares} />
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-col items-end leading-tight">
      <span className="text-text font-medium text-sm">{formatNumber(value)}</span>
      <span className="text-[10px] uppercase tracking-wider">{label}</span>
    </div>
  );
}

function TargetMetricsBlock({ target }: { target: TargetMetrics }) {
  const status = target.status;
  return (
    <div className="border border-border rounded px-3 py-2 bg-bg-card/40">
      <div className="flex items-center gap-2 text-xs">
        <span className="text-text font-medium">{target.platform}</span>
        {target.platform_url && (
          <a href={target.platform_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
            view post ↗
          </a>
        )}
        <span className="ml-auto text-text-dim">
          {status === "ok" ? "" : status}
          {status === "unsupported" && target.reason && <span className="ml-1">— {target.reason}</span>}
          {status === "failed" && target.error && <span className="ml-1 text-red">— {target.error}</span>}
          {status === "skipped" && target.reason && <span className="ml-1">— {target.reason}</span>}
        </span>
      </div>
      {target.metrics && (
        <div className="mt-2 flex items-center gap-4 text-xs">
          <Stat label="views" value={target.metrics.views} />
          <Stat label="likes" value={target.metrics.likes} />
          <Stat label="comments" value={target.metrics.comments} />
          <Stat label="shares" value={target.metrics.shares} />
        </div>
      )}
    </div>
  );
}

function aggregateTotals(targets: TargetMetrics[]): { views: number; likes: number; comments: number; shares: number } {
  return targets.reduce(
    (acc, t) => {
      if (t.metrics) {
        acc.views += t.metrics.views;
        acc.likes += t.metrics.likes;
        acc.comments += t.metrics.comments;
        acc.shares += t.metrics.shares;
      }
      return acc;
    },
    { views: 0, likes: 0, comments: 0, shares: 0 }
  );
}

function formatNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1).replace(/\.0$/, "") + "K";
  return n.toString();
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
