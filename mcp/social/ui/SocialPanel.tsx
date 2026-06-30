// SocialPanel — accounts + compose + posts in a single panel.
//
// Surface:
//   - Tab "Accounts": connected social accounts grid + Add Account flow
//     (OAuth in popup → page picker if needed → finalize).
//   - Tab "Compose": prompt body + multi-select accounts + media picker
//     (storage app, when bound) + Schedule/Now → post_create.
//   - Tab "Posts": list of recent posts with per-target status pills,
//     retry button on failed/partial.
//   - Tab "Inbox": unified comments/messages/mentions/reviews pulled
//     automatically from supported connected accounts.
//
// Lives in the social app's sidecar at /api/apps/social/ui/SocialPanel.mjs.
// The host React (19) + react-dom come from the dashboard's importmap;
// this file uses the same useAppEvents pattern as media-studio.

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Area,
  AreaChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { uploadResumable } from "./uploadResumable";

const API = "/api/apps/social";
const STORAGE_API = "/api/apps/storage";
const MEDIA_API = "/api/apps/media";

function socialURL(path: string, projectId?: string | null, extra?: Record<string, string | number | undefined | null>): string {
  const params = new URLSearchParams();
  if (projectId) params.set("project_id", projectId);
  for (const [key, value] of Object.entries(extra || {})) {
    if (value !== undefined && value !== null && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return `${API}${path}${qs ? `?${qs}` : ""}`;
}

function waitForOAuthPopupResult(popup: Window | null, onDone: (ok: boolean) => void) {
  let settled = false;
  let poll: number | null = null;
  const finish = (ok: boolean) => {
    if (settled) return;
    settled = true;
    window.removeEventListener("message", onMsg);
    if (poll !== null) window.clearInterval(poll);
    onDone(ok);
  };
  const onMsg = (ev: MessageEvent) => {
    if (ev.origin !== window.location.origin) return;
    if (ev.data?.type !== "apteva-oauth-result") return;
    finish(ev.data.ok !== false);
  };
  window.addEventListener("message", onMsg);
  poll = window.setInterval(() => {
    if (popup?.closed) finish(false);
  }, 800);
  window.setTimeout(() => finish(false), 10 * 60 * 1000);
}

function storageURL(path: string, projectId?: string | null): string {
  const params = new URLSearchParams();
  if (projectId) params.set("project_id", projectId);
  const qs = params.toString();
  return `${STORAGE_API}${path}${qs ? `?${qs}` : ""}`;
}

function appendStorageScope(params: URLSearchParams, projectId?: string | null) {
  if (projectId) params.set("project_id", projectId);
}

function appendProjectScope(params: URLSearchParams, projectId?: string | null) {
  if (projectId) params.set("project_id", projectId);
}

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
  last_check_at?: string;
  last_check_status?: "ok" | "failed" | "unsupported" | "";
  last_check_error?: string;
  last_check_details?: Record<string, unknown> | null;
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
  media_project_id?: string;
  schedule_at: string;
  status: string;
  created_at: string;
  published_at: string;
  targets: PostTarget[];
  profile_id?: number;
}

interface InboxItem {
  id: number;
  project_id: string;
  social_account_id: number;
  platform: string;
  kind: "comment" | "dm" | "mention" | "review";
  external_id: string;
  thread_external_id?: string;
  parent_external_id?: string;
  author_external_id?: string;
  author_name?: string;
  author_handle?: string;
  author_avatar_url?: string;
  body?: string;
  permalink?: string;
  occurred_at: string;
  status: "unread" | "read" | "replied" | "hidden" | "archived";
  direction?: "inbound" | "outbound";
}

interface InboxSyncResult {
  social_account_id: number;
  platform: string;
  display_name?: string;
  status: "ok" | "unsupported" | "failed";
  new_items: number;
  comments?: number;
  dms?: number;
  mentions?: number;
  reviews?: number;
  warnings?: string[];
  error?: string;
  last_sync_at?: string;
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
  type: "text" | "textarea" | "select" | "tags" | "media" | "number";
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
  const [tab, setTab] = useState<"accounts" | "posts" | "inbox" | "metrics">("posts");
  const [allAccounts, setAllAccounts] = useState<SocialAccount[]>([]);
  const [allPosts, setAllPosts] = useState<Post[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [status, setStatus] = useState("");
  const [composeOpen, setComposeOpen] = useState(false);
  // Profile filter — null = "All profiles" (project-wide view).
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [activeProfileId, setActiveProfileId] = useState<number | null>(null);
  const [manageOpen, setManageOpen] = useState(false);
  const activeProfile = profiles.find((p) => p.id === activeProfileId) || null;
  const effectiveProfileId = activeProfile?.id ?? null;
  const accounts = effectiveProfileId == null
    ? allAccounts
    : allAccounts.filter((a) => (a.profile_id || 0) === effectiveProfileId);
  const posts = effectiveProfileId == null
    ? allPosts
    : allPosts.filter((p) => (p.profile_id || 0) === effectiveProfileId);

  useEffect(() => {
    setActiveProfileId(null);
    setProfiles([]);
    setAllAccounts([]);
    setAllPosts([]);
  }, [projectId]);

  const loadProfiles = useCallback(async () => {
    try {
      const res = await fetch(socialURL("/profiles", projectId), { credentials: "same-origin" });
      const data = await res.json();
      setProfiles(data.profiles || []);
    } catch (e) {
      setStatus("Load profiles: " + (e as Error).message);
    }
  }, [projectId]);

  // Project-scoped fetches. The panel filters locally by profile_id
  // so switching profiles cannot be overwritten by an older network
  // response from another profile/all-profiles request.
  const loadAccounts = useCallback(async () => {
    try {
      const res = await fetch(socialURL("/accounts", projectId), { credentials: "same-origin" });
      const data = await res.json();
      setAllAccounts(data.accounts || []);
    } catch (e) {
      setStatus("Load accounts: " + (e as Error).message);
    }
  }, [projectId]);

  const loadPosts = useCallback(async () => {
    try {
      const res = await fetch(socialURL("/posts", projectId), { credentials: "same-origin" });
      const data = await res.json();
      setAllPosts(data.posts || []);
    } catch (e) {
      setStatus("Load posts: " + (e as Error).message);
    }
  }, [projectId]);

  const loadPlatforms = useCallback(async () => {
    try {
      const res = await fetch(socialURL("/platforms", projectId), { credentials: "same-origin" });
      const data = await res.json();
      setPlatforms(data.platforms || []);
    } catch {}
  }, [projectId]);

  useEffect(() => {
    loadProfiles();
    loadAccounts();
    loadPosts();
    loadPlatforms();
  }, [loadProfiles, loadAccounts, loadPosts, loadPlatforms]);

  // Live updates — account adds/removals + per-target publish events
  // + profile CRUD + post lifecycle (reschedule/delete from agent).
  useAppEvents("social", projectId, (ev) => {
    if (ev.topic === "account.added" || ev.topic === "account.removed" ||
        ev.topic === "account.disconnected" || ev.topic === "account.deleted") {
      loadAccounts();
    }
    if (ev.topic === "account.checked") {
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

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-1 border-b border-border px-4 py-2">
        <ProfileSwitcher
          profiles={profiles}
          activeId={effectiveProfileId}
          onSelect={setActiveProfileId}
          onManage={() => setManageOpen(true)}
        />
        <span className="w-px h-5 bg-border mx-2" />
        <Tab label="Posts" value="posts" current={tab} onClick={setTab} count={posts.length} />
        <Tab label="Inbox" value="inbox" current={tab} onClick={setTab} />
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

      <div className={"flex-1 min-h-0 " + (tab === "inbox" ? "overflow-hidden" : "overflow-auto")}>
        {tab === "accounts" && (
          <AccountsView
            accounts={accounts}
            platforms={platforms}
            oauthLanding={oauthLanding}
            projectId={projectId}
            activeProfileId={effectiveProfileId}
            onClearLanding={() => setOauthLanding(null)}
            onSetLanding={(pendingId, connId) =>
              setOauthLanding({ pendingId, connectionId: connId })
            }
            onChange={loadAccounts}
            setStatus={setStatus}
          />
        )}
        {tab === "posts" && (
          <PostsView posts={posts} onChange={loadPosts} setStatus={setStatus} projectId={projectId} />
        )}
        {tab === "inbox" && (
          <InboxView
            accounts={accounts}
            projectId={projectId}
            activeProfileId={effectiveProfileId}
            setStatus={setStatus}
          />
        )}
        {tab === "metrics" && (
          <MetricsView posts={posts} accounts={accounts} setStatus={setStatus} onPostsChanged={loadPosts} projectId={projectId} />
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
          projectId={projectId}
        />
      )}
      {manageOpen && (
        <ProfileManageModal
          profiles={profiles}
          accounts={allAccounts}
          projectId={projectId}
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
  profiles, accounts, projectId, onClose, onChanged, setStatus,
}: {
  profiles: Profile[];
  accounts: SocialAccount[];
  projectId?: string | null;
  onClose: () => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
}) {
  const [newName, setNewName] = useState("");
  const [newColor, setNewColor] = useState("#3b82f6");
  const [nameDrafts, setNameDrafts] = useState<Record<number, string>>({});
  const [busy, setBusy] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState<Profile | null>(null);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    setBusy(true);
    try {
      const res = await fetch(socialURL("/profiles", projectId), {
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
      await fetch(socialURL(`/profiles/${id}`, projectId), {
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
      const nextName = name.trim();
      if (!nextName) return;
      const res = await fetch(socialURL(`/profiles/${id}`, projectId), {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: nextName }),
      });
      if (!res.ok) throw new Error(await res.text());
      setNameDrafts((drafts) => {
        const next = { ...drafts };
        delete next[id];
        return next;
      });
      onChanged();
    } catch (e) {
      setStatus("Rename: " + (e as Error).message);
    }
  };

  const recolor = async (id: number, color: string) => {
    try {
      await fetch(socialURL(`/profiles/${id}`, projectId), {
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
      const res = await fetch(socialURL(`/profiles/${id}`, projectId), {
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
      await fetch(socialURL(`/profiles/${profileId}/move`, projectId), {
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
                  {(() => {
                    const draftName = nameDrafts[p.id] ?? p.name;
                    const trimmedDraft = draftName.trim();
                    const nameChanged = trimmedDraft !== "" && trimmedDraft !== p.name;
                    return (
                      <>
                  <input
                    type="color"
                    value={p.color || "#94a3b8"}
                    onChange={(e) => recolor(p.id, e.target.value)}
                    className="w-7 h-7 rounded border border-border cursor-pointer"
                  />
                  <input
                    type="text"
                    value={draftName}
                    onChange={(e) => setNameDrafts((drafts) => ({ ...drafts, [p.id]: e.target.value }))}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && nameChanged) rename(p.id, trimmedDraft);
                    }}
                    className="flex-1 bg-transparent border-b border-transparent hover:border-border focus:border-accent outline-none text-text font-medium"
                  />
                  <button
                    onClick={() => rename(p.id, trimmedDraft)}
                    disabled={!nameChanged}
                    className="text-xs text-accent hover:underline disabled:text-text-dim disabled:no-underline disabled:cursor-not-allowed"
                  >
                    Rename
                  </button>
                      </>
                    );
                  })()}
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
  label: string; value: "accounts" | "posts" | "inbox" | "metrics";
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
  accounts, platforms, oauthLanding, projectId, activeProfileId, onClearLanding, onSetLanding, onChange, setStatus,
}: {
  accounts: SocialAccount[]; platforms: PlatformInfo[];
  oauthLanding: { pendingId: number; connectionId: number } | null;
  projectId?: string | null;
  activeProfileId?: number | null;
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
      const res = await fetch(socialURL(`/accounts/${pendingId}/pages`, projectId), { credentials: "same-origin" });
      const data = await res.json();
      if (!data.requires_picker) {
        await fetch(socialURL("/accounts/finalize", projectId), {
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
  }, [onChange, onClearLanding, projectId, setStatus]);

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
            <AccountCard key={a.id} account={a} onChange={onChange} setStatus={setStatus} projectId={projectId} />
          ))}
        </div>
      )}

      {adding && (
        <AddAccountDialog
          platforms={platforms}
          onClose={() => setAdding(false)}
          setStatus={setStatus}
          projectId={projectId}
          activeProfileId={activeProfileId}
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
          projectId={projectId}
          onClose={() => { onClearLanding(); onChange(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function AccountCard({
  account, onChange, setStatus, projectId,
}: { account: SocialAccount; onChange: () => void; setStatus: (s: string) => void; projectId?: string | null }) {
  const [confirming, setConfirming] = useState(false);
  const [importing, setImporting] = useState(false);
  const [checking, setChecking] = useState(false);
  const [reauthing, setReauthing] = useState(false);
  const doRemove = async () => {
    try {
      const res = await fetch(socialURL(`/accounts/${account.id}`, projectId), { method: "DELETE", credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      setStatus("Disconnected.");
      onChange();
    } catch (e) {
      setStatus("Disconnect failed: " + (e as Error).message);
    }
  };
  const doHardRemove = async () => {
    try {
      const res = await fetch(socialURL(`/accounts/${account.id}`, projectId, {
        hard_delete: "true",
        delete_posts: "true",
      }), { method: "DELETE", credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as {
        posts_deleted?: number;
        post_targets_deleted?: number;
      };
      setStatus(`Hard deleted locally: ${data.posts_deleted || 0} post${data.posts_deleted === 1 ? "" : "s"}, ${data.post_targets_deleted || 0} target${data.post_targets_deleted === 1 ? "" : "s"}.`);
      onChange();
    } catch (e) {
      setStatus("Hard delete failed: " + (e as Error).message);
    }
  };
  const importSupported = isImportablePlatform(account.platform);
  const doImport = async () => {
    setImporting(true);
    setStatus(`Importing recent posts from ${account.display_name}…`);
    try {
      const res = await fetch(socialURL(`/accounts/${account.id}/import`, projectId, { limit: 25 }), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { status: string; imported: number; skipped_existing: number; error?: string; reason?: string };
      if (data.status === "ok") {
        setStatus(`Imported ${data.imported} new post${data.imported !== 1 ? "s" : ""}` +
          (data.skipped_existing > 0 ? ` (${data.skipped_existing} already in)` : "") + ".");
        onChange();
      } else if (data.status === "unsupported") {
        setStatus("Import unsupported: " + (data.reason || "platform not wired yet"));
      } else {
        setStatus("Import failed: " + (data.error || "unknown error"));
      }
    } catch (e) {
      setStatus("Import failed: " + (e as Error).message);
    } finally {
      setImporting(false);
    }
  };
  const doCheck = async () => {
    setChecking(true);
    setStatus(`Checking ${account.display_name}…`);
    try {
      const res = await fetch(socialURL(`/accounts/${account.id}/check`, projectId), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as {
        status: "ok" | "failed" | "unsupported";
        error?: string;
      };
      if (data.status === "ok") {
        setStatus(`${account.display_name} is working.`);
      } else if (data.status === "unsupported") {
        setStatus(`Check unsupported: ${data.error || account.platform}`);
      } else {
        setStatus(`Check failed: ${data.error || "unknown error"}`);
      }
      onChange();
    } catch (e) {
      setStatus("Check failed: " + (e as Error).message);
    } finally {
      setChecking(false);
    }
  };
  const doReauth = () => {
    if (!account.connection_id) {
      setStatus("Reconnect failed: account has no linked connection.");
      return;
    }
    const popup = window.open("about:blank", `social_reauth_${account.id}`, "width=600,height=700");
    if (!popup) {
      setStatus("Popup blocked. Allow pop-ups for this site and try again.");
      return;
    }
    setReauthing(true);
    setStatus(`Starting reconnect for ${account.display_name}…`);
    (async () => {
      const fail = (msg: string) => {
        setStatus(msg);
        setReauthing(false);
        try { popup.close(); } catch {}
      };
      try {
        const res = await fetch(`/api/connections/${account.connection_id}/oauth/reauth`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: "{}",
        });
        if (!res.ok) {
          fail(`Reconnect failed (HTTP ${res.status}): ${await res.text()}`);
          return;
        }
        const data = await res.json() as { redirect_url?: string };
        if (!data.redirect_url) {
          fail("Reconnect failed: server did not return an OAuth URL.");
          return;
        }
        popup.location.href = data.redirect_url;
        waitForOAuthPopupResult(popup, async (ok) => {
          if (!ok) {
            setStatus("Reconnect did not complete.");
            setReauthing(false);
            return;
          }
          setStatus(`Reconnect complete. Refreshing ${account.display_name}…`);
          setReauthing(false);
          await doCheck();
        });
      } catch (e) {
        fail("Reconnect failed: " + (e as Error).message);
      }
    })();
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
          <div className="flex items-center gap-2 text-xs">
            <span className="text-text-dim">{account.platform}</span>
            <HealthPill account={account} />
          </div>
        </div>
        <button
          onClick={doCheck}
          disabled={checking}
          className="text-xs text-text-muted hover:text-text px-2 py-1 border border-border rounded disabled:opacity-50"
          title="Check whether this account still works"
        >
          {checking ? "Checking…" : "Check"}
        </button>
        <button
          onClick={doReauth}
          disabled={reauthing || checking}
          className="text-xs text-text-muted hover:text-text px-2 py-1 border border-border rounded disabled:opacity-50"
          title="Reconnect OAuth and update the linked tokens for this account"
        >
          {reauthing ? "Reconnecting…" : "Reconnect"}
        </button>
        {importSupported && (
          <button
            onClick={doImport}
            disabled={importing}
            className="text-xs text-text-muted hover:text-text px-2 py-1 border border-border rounded disabled:opacity-50"
            title="Import recent posts from this account into the local DB"
          >
            {importing ? "Importing…" : "Import"}
          </button>
        )}
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
	              Disconnect hides this account from posting while preserving local post history.
	              Hard delete removes this account and its local history only; posts already published
	              on social networks are not deleted.
	            </>
	          }
	          confirmLabel="Disconnect"
	          secondaryDangerLabel="Hard delete local history"
	          onSecondaryDanger={async () => {
	            await doHardRemove();
	            setConfirming(false);
	          }}
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

function HealthPill({ account }: { account: SocialAccount }) {
  const status = account.last_check_status || "";
  if (!status) {
    return <span className="text-text-dim">never checked</span>;
  }
  const when = account.last_check_at ? new Date(account.last_check_at).toLocaleString() : "";
  if (status === "ok") {
    return <span className="text-success" title={when}>ok</span>;
  }
  if (status === "failed") {
    return <span className="text-error" title={account.last_check_error || when}>failed</span>;
  }
  return <span className="text-text-dim" title={account.last_check_error || when}>unsupported</span>;
}

function AddAccountDialog({
  platforms, onClose, setStatus, onReuseExisting, projectId, activeProfileId,
}: {
  platforms: PlatformInfo[];
  onClose: () => void;
  setStatus: (s: string) => void;
  onReuseExisting: (pendingId: number, connectionId: number) => void;
  projectId?: string | null;
  activeProfileId?: number | null;
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
        const profileId = activeProfileId ?? undefined;
        const res = await fetch(socialURL("/accounts/start", projectId, { profile_id: profileId }), {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ platform: p.platform, profile_id: profileId }),
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
  pendingId, projectId, onClose, setStatus,
}: { pendingId: number; projectId?: string | null; onClose: () => void; setStatus: (s: string) => void }) {
  const [pages, setPages] = useState<PageEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState("");
  const [busyID, setBusyID] = useState<string>("");
  const [platform, setPlatform] = useState<string>("");
  const kind = pickerKind(platform);

  useEffect(() => {
    fetch(socialURL(`/accounts/${pendingId}/pages`, projectId), { credentials: "same-origin" })
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
  }, [pendingId, onClose, projectId]);

  const pick = async (page: PageEntry) => {
    setBusyID(page.id);
    try {
      await fetch(socialURL("/accounts/finalize", projectId), {
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
                    <img
                      src={p.avatar_url}
                      alt=""
                      className="w-9 h-9 rounded-full flex-shrink-0 object-cover"
                      style={{
                        width: 36,
                        height: 36,
                        minWidth: 36,
                        minHeight: 36,
                        maxWidth: 36,
                        maxHeight: 36,
                        objectFit: "cover",
                        borderRadius: 9999,
                      }}
                    />
                  ) : (
                    <div
                      className="w-9 h-9 rounded-full bg-bg-input flex-shrink-0 grid place-items-center text-text-dim text-xs font-medium"
                      style={{
                        width: 36,
                        height: 36,
                        minWidth: 36,
                        minHeight: 36,
                        maxWidth: 36,
                        maxHeight: 36,
                      }}
                    >
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
  field, value, onChange, projectId,
}: {
  field: OptionField;
  value: any;
  onChange: (v: any) => void;
  projectId?: string | null;
}) {
  const [showPicker, setShowPicker] = useState(false);
  const labelEl = (
    <div className="flex items-baseline gap-2">
      <label className="text-xs uppercase tracking-wide text-text-dim">{field.label}</label>
      {field.help && <span className="text-text-dim text-[10px]">{field.help}</span>}
    </div>
  );

  if (field.type === "media") {
    const numericValue = typeof value === "number" ? value : Number(value || 0);
    const fileId = Number.isFinite(numericValue) && numericValue > 0 ? numericValue : null;
    return (
      <div className="flex flex-col gap-2">
        {labelEl}
        <div className="flex items-center gap-3">
          <div className="border border-border rounded bg-bg-input overflow-hidden" style={{ width: 84, height: 54 }}>
            {fileId ? (
              <img
                src={storageURL(`/files/${fileId}/content`, projectId)}
                alt=""
                className="w-full h-full object-cover"
                loading="lazy"
              />
            ) : (
              <div className="w-full h-full grid place-items-center text-[10px] text-text-dim">No image</div>
            )}
          </div>
          <div className="flex flex-col gap-1 min-w-0">
            <div className="text-xs text-text-dim truncate">
              {fileId ? `Storage file #${fileId}` : "Optional thumbnail image"}
            </div>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setShowPicker(true)}
                className="px-2 py-1 text-xs border border-border rounded text-accent hover:border-accent"
              >
                Browse
              </button>
              {fileId && (
                <button
                  type="button"
                  onClick={() => onChange("")}
                  className="px-2 py-1 text-xs text-text-dim hover:text-text"
                >
                  Clear
                </button>
              )}
            </div>
          </div>
        </div>
        {showPicker && (
          <StoragePickerDialog
            title={`Pick ${field.label.toLowerCase()}`}
            excludeIds={new Set(fileId ? [fileId] : [])}
            projectId={projectId}
            initialKind="image"
            lockedKind="image"
            single
            onClose={() => setShowPicker(false)}
            onPick={(picked) => {
              const first = picked[0];
              if (first) onChange(first.id);
              setShowPicker(false);
            }}
          />
        )}
      </div>
    );
  }

  if (field.type === "number") {
    const textValue = value == null ? "" : String(value);
    return (
      <div className="flex flex-col gap-1">
        {labelEl}
        <input
          type="number"
          min={0}
          step={1}
          value={textValue}
          onChange={(e) => onChange(e.target.value.trim() === "" ? "" : Number(e.target.value))}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
      </div>
    );
  }

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
  accounts, platforms, activeProfile, onClose, onCreated, setStatus, projectId,
}: {
  accounts: SocialAccount[];
  platforms: PlatformInfo[];
  activeProfile: Profile | null;
  onClose: () => void;
  onCreated: () => void;
  setStatus: (s: string) => void;
  projectId?: string | null;
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
  // (so post_create only carries IDs), and keep lightweight preview
  // URLs for the compose modal. Videos prefer Media thumbnails when
  // present; otherwise we generate a small poster from metadata/range
  // loading instead of trying to load the whole file.
  const [media, setMedia] = useState<ComposeMedia[]>([]);
  const mediaRef = useRef<ComposeMedia[]>([]);
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

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);

  // Revoke any object URLs we created when the modal closes.
  useEffect(() => {
    return () => {
      for (const m of mediaRef.current) revokeComposeMedia(m);
    };
  }, []);

  useEffect(() => {
    const pending = media.filter((m) =>
      isVideoMime(m.mime, m.name) && !m.posterURL && !m.posterLoading && !m.posterTried
    );
    if (pending.length === 0) return;
    setMedia((prev) => prev.map((m) =>
      pending.some((p) => p.id === m.id) ? { ...m, posterLoading: true } : m
    ));
    for (const item of pending) {
      buildVideoPoster(item.previewURL).then((posterURL) => {
        setMedia((prev) => {
          let found = false;
          const next = prev.map((m) => {
            if (m.id !== item.id) return m;
            found = true;
            if (m.posterObjectURL && m.posterURL) URL.revokeObjectURL(m.posterURL);
            return {
              ...m,
              posterURL: posterURL || m.posterURL,
              posterObjectURL: posterURL ? true : m.posterObjectURL,
              posterLoading: false,
              posterTried: true,
            };
          });
          if (!found && posterURL) URL.revokeObjectURL(posterURL);
          return next;
        });
      });
    }
  }, [media]);

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
      const uploaded: ComposeMedia[] = [];
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
          projectId,
          onProgress: (bytes, total) => {
            const pct = total > 0 ? Math.floor((bytes / total) * 100) : 0;
            setStatus(`Uploading ${label} — ${pct}%`);
          },
        });
        if (typeof row.id !== "number") {
          throw new Error("storage didn't return a file id");
        }
        const previewURL = URL.createObjectURL(file);
        uploaded.push({
          id: row.id,
          name: file.name,
          mime: file.type,
          previewURL,
          previewObjectURL: true,
          posterTried: !isVideoMime(file.type, file.name),
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
      if (dropped) revokeComposeMedia(dropped);
      return prev.filter((m) => m.id !== id);
    });
  };

  // Add files chosen from the storage browser. Same shape as the
  // upload flow — the only difference is previewURL points at storage's
  // /content endpoint instead of a local ObjectURL. Skip ids already in
  // media so users don't double-attach the same file.
  const addFromStorage = (picked: { id: number; name: string; content_type: string; preview_url?: string }[]) => {
    setMedia((prev) => {
      const existing = new Set(prev.map((m) => m.id));
      const adds = picked
        .filter((f) => !existing.has(f.id))
        .map((f) => {
          const mime = f.content_type || "";
          const isVideo = isVideoMime(mime, f.name);
          return {
            id: f.id,
            name: f.name,
            mime,
            previewURL: storageURL(`/files/${f.id}/content`, projectId),
            posterURL: isVideo ? f.preview_url : undefined,
            posterTried: !isVideo || !!f.preview_url,
          };
        });
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
        _project_id: projectId || undefined,
        schedule_at: scheduleAt || undefined,
        media_storage_ids: media.length > 0 ? media.map((m) => m.id) : undefined,
        media_project_id: media.length > 0 ? projectId : undefined,
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
      const res = await fetch(socialURL("/posts", projectId), {
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
      for (const m of mediaRef.current) revokeComposeMedia(m);
      mediaRef.current = [];
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
                  aria-label={m.name}
                >
                  {isVideoMime(m.mime, m.name) ? (
                    <>
                      {m.posterURL ? (
                        <img src={m.posterURL} alt="" className="w-full h-full object-cover" />
                      ) : (
                        <video
                          src={m.previewURL}
                          className="w-full h-full object-cover"
                          muted
                          playsInline
                          preload="metadata"
                        />
                      )}
                      <PlayBadge />
                    </>
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
                          projectId={projectId}
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
        projectId={projectId}
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
// Stacked on top of ComposeDialog. Use inline zIndex because the
// production CSS build may not include arbitrary Tailwind classes.

interface StorageFile {
  id: number;
  name: string;
  content_type: string;
  folder?: string;
  size_bytes?: number;
  preview_url?: string;
}

interface MediaDerivation {
  kind: "thumbnail" | "waveform" | "cover" | "keyframe" | string;
  storage_file_id: string;
  position_ms?: number;
  status: "ok" | "failed" | "stale" | string;
}

interface MediaLibraryRow {
  file_id: string;
  name?: string;
  folder?: string;
  has_video?: boolean;
  has_audio?: boolean;
  is_image?: boolean;
  duration_ms?: number;
  width?: number;
  height?: number;
  title?: string;
  description?: string;
  audience_rating?: "unrated" | "general" | "mature" | "adult" | "";
  derivations?: MediaDerivation[];
}

interface ComposeMedia {
  id: number;
  name: string;
  mime: string;
  previewURL: string;
  previewObjectURL?: boolean;
  posterURL?: string;
  posterObjectURL?: boolean;
  posterLoading?: boolean;
  posterTried?: boolean;
}

type PickerTab = "media" | "storage";
type PickerKind = "all" | "image" | "video";
type RatingFilter = "all" | "general" | "mature" | "adult" | "unrated";
type LengthFilter = "all" | "short" | "medium" | "long" | "very-long";
type AspectFilter = "all" | "9:16" | "1:1" | "4:5" | "16:9";

function isHiddenStorageFile(file: StorageFile): boolean {
  const folderParts = (file.folder || "/").split("/").filter(Boolean);
  return folderParts.some((part) => part.startsWith("."));
}

function mediaStorageID(row: MediaLibraryRow): number | null {
  const id = Number(row.file_id);
  return Number.isFinite(id) ? id : null;
}

function mediaContentType(row: MediaLibraryRow): string {
  if (row.is_image) return "image/*";
  if (row.has_video) return "video/*";
  return "";
}

function mediaDisplayName(row: MediaLibraryRow): string {
  return row.title?.trim() || row.name?.trim() || `file #${row.file_id}`;
}

function mediaPreviewURL(row: MediaLibraryRow, projectId?: string | null): string {
  const pick = row.derivations?.find((d) => d.kind === "thumbnail" && d.status === "ok");
  if (pick?.storage_file_id) return storageURL(`/files/${pick.storage_file_id}/content`, projectId);
  return storageURL(`/files/${row.file_id}/content`, projectId);
}

function mediaHasThumbnail(row: MediaLibraryRow): boolean {
  return !!row.derivations?.some((d) => d.kind === "thumbnail" && d.status === "ok" && d.storage_file_id);
}

function isVideoMime(mime: string, name = ""): boolean {
  return mime.startsWith("video/") || /\.(mp4|m4v|mov|webm|avi|mkv)$/i.test(name);
}

function revokeComposeMedia(m: ComposeMedia) {
  if (m.previewObjectURL) URL.revokeObjectURL(m.previewURL);
  if (m.posterObjectURL && m.posterURL) URL.revokeObjectURL(m.posterURL);
}

async function buildVideoPoster(src: string): Promise<string | null> {
  if (typeof document === "undefined") return null;
  return new Promise((resolve) => {
    const video = document.createElement("video");
    let done = false;
    let timer: number | undefined;
    const finish = (url: string | null) => {
      if (done) return;
      done = true;
      if (timer != null) window.clearTimeout(timer);
      video.pause();
      video.removeAttribute("src");
      video.load();
      resolve(url);
    };
    const capture = () => {
      if (!video.videoWidth || !video.videoHeight) {
        finish(null);
        return;
      }
      const maxSide = 240;
      const scale = Math.min(1, maxSide / Math.max(video.videoWidth, video.videoHeight));
      const canvas = document.createElement("canvas");
      canvas.width = Math.max(1, Math.round(video.videoWidth * scale));
      canvas.height = Math.max(1, Math.round(video.videoHeight * scale));
      const ctx = canvas.getContext("2d");
      if (!ctx) {
        finish(null);
        return;
      }
      try {
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        canvas.toBlob((blob) => {
          finish(blob ? URL.createObjectURL(blob) : null);
        }, "image/jpeg", 0.72);
      } catch {
        finish(null);
      }
    };
    video.preload = "metadata";
    video.muted = true;
    video.playsInline = true;
    video.addEventListener("error", () => finish(null), { once: true });
    video.addEventListener("loadedmetadata", () => {
      const target = Number.isFinite(video.duration) && video.duration > 0.2 ? 0.1 : 0;
      if (Math.abs(video.currentTime - target) < 0.01) {
        if (video.readyState >= 2) {
          capture();
          return;
        }
        video.addEventListener("loadeddata", capture, { once: true });
        return;
      }
      video.addEventListener("seeked", capture, { once: true });
      try {
        video.currentTime = target;
      } catch {
        video.addEventListener("loadeddata", capture, { once: true });
      }
    }, { once: true });
    timer = window.setTimeout(() => finish(null), 7000);
    video.src = src;
    video.load();
  });
}

function PlayBadge() {
  return (
    <div
      aria-hidden="true"
      className="absolute rounded-full bg-black/60"
      style={{
        width: 34,
        height: 34,
        left: "50%",
        top: "50%",
        transform: "translate(-50%, -50%)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        pointerEvents: "none",
      }}
    >
      <span
        style={{
          width: 0,
          height: 0,
          borderTop: "7px solid transparent",
          borderBottom: "7px solid transparent",
          borderLeft: "10px solid white",
          marginLeft: 3,
          display: "block",
        }}
      />
    </div>
  );
}

function formatDurationMS(ms?: number): string {
  if (!ms || ms <= 0) return "";
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

function aspectMatches(row: MediaLibraryRow, aspect: AspectFilter): boolean {
  if (aspect === "all") return true;
  if (!row.width || !row.height) return false;
  const [w, h] = aspect.split(":").map(Number);
  const target = w / h;
  const actual = row.width / row.height;
  return Math.abs(actual - target) <= 0.08;
}

function lengthMatches(row: MediaLibraryRow, length: LengthFilter): boolean {
  if (length === "all") return true;
  const ms = row.duration_ms || 0;
  if (length === "short") return ms > 0 && ms < 15_000;
  if (length === "medium") return ms >= 15_000 && ms <= 60_000;
  if (length === "long") return ms > 60_000 && ms <= 300_000;
  return ms > 300_000;
}

function StoragePickerDialog({
  excludeIds, projectId, onClose, onPick, initialKind = "all", lockedKind, single = false, title = "Pick media",
}: {
  excludeIds: Set<number>;
  projectId?: string | null;
  onClose: () => void;
  onPick: (files: StorageFile[]) => void;
  initialKind?: PickerKind;
  lockedKind?: PickerKind;
  single?: boolean;
  title?: string;
}) {
  const [tab, setTab] = useState<PickerTab>("media");
  const [mediaRows, setMediaRows] = useState<MediaLibraryRow[]>([]);
  const [mediaLoading, setMediaLoading] = useState(true);
  const [mediaError, setMediaError] = useState<string | null>(null);
  const [files, setFiles] = useState<StorageFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<PickerKind>(lockedKind || initialKind);
  const [rating, setRating] = useState<RatingFilter>("all");
  const [length, setLength] = useState<LengthFilter>("all");
  const [aspect, setAspect] = useState<AspectFilter>("all");
  const [folder, setFolder] = useState("/");
  const [recursive, setRecursive] = useState(true);
  const [picked, setPicked] = useState<Set<number>>(new Set());

  useEffect(() => {
    let cancelled = false;
    const run = async () => {
      setMediaLoading(true);
      setMediaError(null);
      try {
        const params = new URLSearchParams();
        params.set("limit", "300");
        params.set("order_by", "updated_at");
        if (kind === "image") params.set("is_image", "true");
        else if (kind === "video") params.set("has_video", "true");
        if (length === "short") params.set("duration_max_ms", "14999");
        else if (length === "medium") {
          params.set("duration_min_ms", "15000");
          params.set("duration_max_ms", "60000");
        } else if (length === "long") {
          params.set("duration_min_ms", "60001");
          params.set("duration_max_ms", "300000");
        } else if (length === "very-long") {
          params.set("duration_min_ms", "300001");
        }
        const cleanFolder = folder.trim();
        if (cleanFolder && cleanFolder !== "/") {
          params.set("folder", cleanFolder);
          if (recursive) params.set("recursive", "true");
        }
        appendProjectScope(params, projectId);
        const res = await fetch(`${MEDIA_API}/media?${params.toString()}`, {
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json() as { media?: MediaLibraryRow[] };
        if (cancelled) return;
        const needle = q.trim().toLowerCase();
        const usable = (data.media || []).filter((row) => {
          const id = mediaStorageID(row);
          if (id == null) return false;
          const name = mediaDisplayName(row).toLowerCase();
          const description = (row.description || "").toLowerCase();
          const rowFolder = row.folder || "/";
          if (rowFolder.split("/").filter(Boolean).some((part) => part.startsWith("."))) return false;
          if (needle && !name.includes(needle) && !description.includes(needle)) return false;
          if (rating !== "all" && (row.audience_rating || "unrated") !== rating) return false;
          if (!aspectMatches(row, aspect)) return false;
          if (!lengthMatches(row, length)) return false;
          return row.is_image || row.has_video;
        });
        setMediaRows(usable);
      } catch (e) {
        if (!cancelled) {
          setMediaError((e as Error).message);
          setTab("storage");
        }
      } finally {
        if (!cancelled) setMediaLoading(false);
      }
    };
    const t = setTimeout(run, q.trim() ? 200 : 0);
    return () => { cancelled = true; clearTimeout(t); };
  }, [q, kind, rating, length, aspect, folder, recursive, projectId]);

  // Re-fetch on q/kind change. The storage app does prefix-match on
  // content_type via SQL LIKE, so passing "image/" filters server-side;
  // for "all" we fetch unfiltered and drop non-media client-side so
  // there's only ever one round-trip per change.
  useEffect(() => {
    let cancelled = false;
    const run = async () => {
      if (tab !== "storage") return;
      setLoading(true);
      setError(null);
      try {
        const params = new URLSearchParams();
        params.set("limit", "1000");
        if (q.trim()) params.set("q", q.trim());
        if (kind === "image") params.set("content_type", "image/");
        else if (kind === "video") params.set("content_type", "video/");
        appendStorageScope(params, projectId);
        const res = await fetch(`${STORAGE_API}/files?${params.toString()}`, {
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json() as { files: StorageFile[] };
        if (cancelled) return;
        const usable = (data.files || []).filter(
          (f) => !isHiddenStorageFile(f) && (
            kind !== "all" ||
            (f.content_type || "").startsWith("image/") ||
            (f.content_type || "").startsWith("video/")
          )
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
  }, [q, kind, projectId, tab]);

  const toggle = (id: number) => {
    setPicked((s) => {
      if (single) return s.has(id) ? new Set() : new Set([id]);
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const confirm = () => {
    if (tab === "media") {
      const chosen = mediaRows
        .map((row) => {
          const id = mediaStorageID(row);
          if (id == null || !picked.has(id)) return null;
          return {
            id,
            name: mediaDisplayName(row),
            content_type: mediaContentType(row),
            folder: row.folder,
            preview_url: mediaHasThumbnail(row) ? mediaPreviewURL(row, projectId) : undefined,
          } as StorageFile;
        })
        .filter((f): f is StorageFile => !!f);
      if (chosen.length === 0) return;
      onPick(chosen);
      return;
    }
    const chosen = files.filter((f) => picked.has(f.id));
    if (chosen.length === 0) return;
    onPick(chosen);
  };

  return (
    <div
      className="fixed inset-0 grid place-items-center bg-black/60"
      style={{ zIndex: 60 }}
      onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div
        className="bg-bg border border-border rounded-lg flex flex-col"
        style={{ width: "min(720px, 92vw)", maxHeight: "85vh" }}
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div className="text-sm font-bold text-text">{title}</div>
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
          <button
            type="button"
            onClick={() => setTab("media")}
            className={
              "px-2 py-1 text-xs rounded border " +
              (tab === "media" ? "border-accent text-accent" : "border-border text-text-dim hover:text-text")
            }
          >
            Media library
          </button>
          <button
            type="button"
            onClick={() => setTab("storage")}
            className={
              "px-2 py-1 text-xs rounded border " +
              (tab === "storage" ? "border-accent text-accent" : "border-border text-text-dim hover:text-text")
            }
          >
            Storage files
          </button>
        </div>

        <div className="flex flex-col gap-2 px-4 py-2 border-b border-border">
          <div className="flex items-center gap-2">
          <input
            type="text"
            placeholder={tab === "media" ? "Search title, name, description..." : "Search by name..."}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
          />
          {lockedKind ? (
            <div className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim">
              {lockedKind === "image" ? "Images" : lockedKind === "video" ? "Videos" : "All media"}
            </div>
          ) : (
            <select
              value={kind}
              onChange={(e) => setKind(e.target.value as "all" | "image" | "video")}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            >
              <option value="all">All media</option>
              <option value="image">Images</option>
              <option value="video">Videos</option>
            </select>
          )}
          </div>
          {tab === "media" && (
            <div className="flex flex-wrap items-center gap-2">
              <select
                value={rating}
                onChange={(e) => setRating(e.target.value as RatingFilter)}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
              >
                <option value="all">Any rating</option>
                <option value="general">General</option>
                <option value="mature">Mature</option>
                <option value="adult">Adult</option>
                <option value="unrated">Unrated</option>
              </select>
              <select
                value={length}
                onChange={(e) => setLength(e.target.value as LengthFilter)}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
              >
                <option value="all">Any length</option>
                <option value="short">&lt;15s</option>
                <option value="medium">15-60s</option>
                <option value="long">1-5m</option>
                <option value="very-long">5m+</option>
              </select>
              <select
                value={aspect}
                onChange={(e) => setAspect(e.target.value as AspectFilter)}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
              >
                <option value="all">Any aspect</option>
                <option value="9:16">9:16</option>
                <option value="1:1">1:1</option>
                <option value="4:5">4:5</option>
                <option value="16:9">16:9</option>
              </select>
              <input
                type="text"
                value={folder}
                onChange={(e) => setFolder(e.target.value)}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
                style={{ width: 120 }}
                title="Folder"
              />
              <label className="inline-flex items-center gap-1 text-xs text-text-dim">
                <input
                  type="checkbox"
                  checked={recursive}
                  onChange={(e) => setRecursive(e.target.checked)}
                  className="accent-accent"
                />
                recursive
              </label>
            </div>
          )}
        </div>

        <div className="flex-1 overflow-y-auto p-4">
          {tab === "media" && mediaLoading && (
            <div className="py-12 text-center text-text-dim text-sm">Loading…</div>
          )}
          {tab === "media" && mediaError && !mediaLoading && (
            <div className="py-12 text-center text-text-dim text-sm">
              Media library unavailable. Use Storage files.
            </div>
          )}
          {tab === "media" && !mediaLoading && !mediaError && mediaRows.length === 0 && (
            <div className="py-12 text-center text-text-dim text-sm">
              No matching indexed media.
            </div>
          )}
          {tab === "media" && !mediaLoading && !mediaError && mediaRows.length > 0 && (
            <div
              className="grid gap-3"
              style={{ gridTemplateColumns: "repeat(auto-fill, minmax(132px, 1fr))" }}
            >
              {mediaRows.map((row) => {
                const id = mediaStorageID(row);
                if (id == null) return null;
                const already = excludeIds.has(id);
                const sel = picked.has(id);
                const name = mediaDisplayName(row);
                const isVideo = !!row.has_video;
                const preview = mediaPreviewURL(row, projectId);
                const hasThumb = mediaHasThumbnail(row);
                const meta = [
                  row.audience_rating || "unrated",
                  formatDurationMS(row.duration_ms),
                  row.width && row.height ? `${row.width}x${row.height}` : "",
                ].filter(Boolean).join(" · ");
                return (
                  <button
                    key={row.file_id}
                    type="button"
                    disabled={already}
                    onClick={() => toggle(id)}
                    className={
                      "relative aspect-square rounded border overflow-hidden bg-bg-input flex flex-col items-stretch text-left transition-colors " +
                      (already
                        ? "opacity-40 cursor-not-allowed border-border"
                        : sel
                          ? "border-accent ring-2 ring-accent"
                          : "border-border hover:border-text-dim")
                    }
                    aria-label={already ? `${name} already attached` : `Select ${name}`}
                  >
                    {isVideo && !hasThumb ? (
                      <video src={preview} className="w-full h-full object-cover" muted preload="metadata" />
                    ) : (
                      <img src={preview} alt={name} className="w-full h-full object-cover" loading="lazy" />
                    )}
                    {isVideo && <PlayBadge />}
                    {sel && (
                      <div className="absolute top-1 right-1 w-5 h-5 rounded-full bg-accent text-bg text-xs font-bold grid place-items-center">
                        ✓
                      </div>
                    )}
                    <div className="absolute bottom-0 inset-x-0 bg-black/60 text-white px-1.5 py-0.5">
                      <div className="truncate" style={{ fontSize: 10 }}>{name}</div>
                      {meta && <div className="truncate opacity-80" style={{ fontSize: 9 }}>{meta}</div>}
                    </div>
                  </button>
                );
              })}
            </div>
          )}

          {tab === "storage" && loading && (
            <div className="py-12 text-center text-text-dim text-sm">Loading…</div>
          )}
          {tab === "storage" && error && !loading && (
            <div className="py-12 text-center text-red text-sm">Couldn't load files: {error}</div>
          )}
          {tab === "storage" && !loading && !error && files.length === 0 && (
            <div className="py-12 text-center text-text-dim text-sm">
              No matching files in storage. Upload one with "+ Attach image / video".
            </div>
          )}
          {tab === "storage" && !loading && !error && files.length > 0 && (
            <div
              className="grid gap-3"
              style={{ gridTemplateColumns: "repeat(auto-fill, minmax(132px, 1fr))" }}
            >
              {files.map((f) => {
                const already = excludeIds.has(f.id);
                const sel = picked.has(f.id);
                const isVideo = (f.content_type || "").startsWith("video/");
                const src = storageURL(`/files/${f.id}/content`, projectId);
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
                    aria-label={already ? `${f.name} already attached` : `Select ${f.name}`}
                  >
                    {isVideo ? (
                      <video src={src} className="w-full h-full object-cover" muted preload="metadata" />
                    ) : (
                      <img src={src} alt={f.name} className="w-full h-full object-cover" loading="lazy" />
                    )}
                    {isVideo && <PlayBadge />}
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
            {picked.size > 0
              ? `${picked.size} selected`
              : tab === "media"
                ? `${mediaRows.length} media item${mediaRows.length !== 1 ? "s" : ""}`
                : `${files.length} file${files.length !== 1 ? "s" : ""}`}
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

function InboxView({
  accounts, projectId, activeProfileId, setStatus,
}: {
  accounts: SocialAccount[];
  projectId?: string | null;
  activeProfileId: number | null;
  setStatus: (s: string) => void;
}) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [selected, setSelected] = useState<InboxItem | null>(null);
  const [thread, setThread] = useState<InboxItem[]>([]);
  const [accountID, setAccountID] = useState<number | "all">("all");
  const [kind, setKind] = useState<string>("all");
  const [status, setLocalStatus] = useState<string>("all");
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [replyBody, setReplyBody] = useState("");
  const [syncResults, setSyncResults] = useState<InboxSyncResult[]>([]);
  const [search, setSearch] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const extra: Record<string, string | number | undefined> = {
        profile_id: activeProfileId ?? undefined,
        account_id: accountID === "all" ? undefined : accountID,
        status: status === "all" ? undefined : status,
        limit: 100,
      };
      const res = await fetch(socialURL("/inbox", projectId, extra), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const next = data.items || [];
      setItems(next);
      if (!selectedID && next.length > 0) setSelectedID(next[0].id);
    } catch (e) {
      setStatus("Inbox load failed: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [accountID, activeProfileId, projectId, selectedID, setStatus, status]);

  const loadSelected = useCallback(async (id: number | null) => {
    if (!id) {
      setSelected(null);
      setThread([]);
      return;
    }
    try {
      const res = await fetch(socialURL(`/inbox/${id}`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setSelected(data.item || null);
      setThread(data.thread && data.thread.length > 0 ? data.thread : (data.item ? [data.item] : []));
    } catch (e) {
      setStatus("Inbox item failed: " + (e as Error).message);
    }
  }, [projectId, setStatus]);

  const sync = useCallback(async (initial = false) => {
    setSyncing(true);
    try {
      const extra: Record<string, string | number | undefined> = {
        profile_id: activeProfileId ?? undefined,
        account_id: accountID === "all" ? undefined : accountID,
        limit: initial ? 100 : 50,
      };
      const res = await fetch(socialURL("/inbox/sync", projectId, extra), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ initial }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setSyncResults(data.results || []);
      await load();
      setStatus("Inbox synced.");
    } catch (e) {
      setStatus("Inbox sync failed: " + (e as Error).message);
    } finally {
      setSyncing(false);
    }
  }, [accountID, activeProfileId, load, projectId, setStatus]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => { loadSelected(selectedID); }, [loadSelected, selectedID]);
  useEffect(() => {
    if (items.length === 0 && accounts.length > 0 && !syncing) {
      sync(true);
    }
  // run only when the current filter first resolves empty
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length, accounts.length]);

  useAppEvents("social", projectId, (ev) => {
    if (ev.topic === "inbox.synced" || ev.topic === "inbox.item.created") load();
  });

  const act = async (action: string, body?: Record<string, unknown>) => {
    if (!selected) return;
    try {
      const actionBody = { ...(body || {}) };
      if ((action === "read" || action === "unread" || action === "archive") && thread.length > 0) {
        actionBody.ids = thread.map((item) => item.id);
      }
      const res = await fetch(socialURL(`/inbox/${selected.id}/${action}`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionBody),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json().catch(() => ({}));
      if (data.status === "failed") throw new Error(data.error || "failed");
      if (data.status === "unsupported") {
        setStatus(data.reason || "Unsupported action.");
      } else {
        setStatus("Inbox updated.");
      }
      setReplyBody("");
      await load();
      await loadSelected(selected.id);
    } catch (e) {
      setStatus("Inbox action failed: " + (e as Error).message);
    }
  };

  const accountName = (id: number) => accounts.find((a) => a.id === id)?.display_name || `#${id}`;
  const accountFor = (id: number) => accounts.find((a) => a.id === id);
  const inboxAuthor = (item: InboxItem) => {
    if (item.author_name) return item.author_name;
    if (item.author_handle) return item.author_handle;
    if (item.direction === "outbound") return "You";
    if (item.platform === "facebook" && item.kind === "comment") return "Facebook commenter";
    return "Author unavailable";
  };
  const itemAvatar = (item: InboxItem) => item.author_avatar_url || (item.direction === "outbound" ? accountFor(item.social_account_id)?.avatar_url : "");
  const initials = (label: string) => {
    const words = label.trim().split(/\s+/).filter(Boolean);
    if (words.length === 0) return "?";
    return (words[0][0] + (words[1]?.[0] || "")).toUpperCase();
  };
  const kindLabel = (k: string) => k === "dm" ? "Message" : k.charAt(0).toUpperCase() + k.slice(1);
  const counts = items.reduce((acc, item) => {
    acc.all++;
    acc[item.kind] = (acc[item.kind] || 0) + 1;
    return acc;
  }, { all: 0 } as Record<string, number>);
  const filteredItems = items.filter((item) => {
    if (kind !== "all" && item.kind !== kind) return false;
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return [
      inboxAuthor(item),
      item.body || "",
      item.platform,
      item.kind,
      accountName(item.social_account_id),
    ].join(" ").toLowerCase().includes(q);
  });
  const statusText = selected?.status === "unread" ? "Unread" : selected?.status ? selected.status.charAt(0).toUpperCase() + selected.status.slice(1) : "";
  const Avatar = ({ item, size = "md" }: { item: InboxItem; size?: "sm" | "md" | "lg" }) => {
    const label = inboxAuthor(item);
    const src = itemAvatar(item);
    const cls = size === "lg" ? "w-10 h-10 text-sm" : size === "sm" ? "w-7 h-7 text-[10px]" : "w-9 h-9 text-xs";
    if (src) {
      return <img src={src} alt="" className={`${cls} rounded-full object-cover flex-shrink-0 border border-border`} />;
    }
    return (
      <div className={`${cls} rounded-full flex-shrink-0 grid place-items-center border border-border bg-bg-input text-text-dim font-bold`}>
        {initials(label)}
      </div>
    );
  };

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg">
      <div className="border-b border-border px-4 py-3 flex flex-wrap items-center gap-2">
        <button onClick={() => setKind("all")} className={"px-3 py-1.5 rounded text-sm border " + (kind === "all" ? "bg-accent/15 border-accent text-accent" : "bg-bg-card border-border text-text-dim hover:text-text")}>
          All <span className="ml-1 text-xs">{counts.all || 0}</span>
        </button>
        <button onClick={() => setKind("dm")} className={"px-3 py-1.5 rounded text-sm border " + (kind === "dm" ? "bg-accent/15 border-accent text-accent" : "bg-bg-card border-border text-text-dim hover:text-text")}>
          Messages <span className="ml-1 text-xs">{counts.dm || 0}</span>
        </button>
        <button onClick={() => setKind("comment")} className={"px-3 py-1.5 rounded text-sm border " + (kind === "comment" ? "bg-accent/15 border-accent text-accent" : "bg-bg-card border-border text-text-dim hover:text-text")}>
          Comments <span className="ml-1 text-xs">{counts.comment || 0}</span>
        </button>
        <button onClick={() => setKind("mention")} className={"px-3 py-1.5 rounded text-sm border " + (kind === "mention" ? "bg-accent/15 border-accent text-accent" : "bg-bg-card border-border text-text-dim hover:text-text")}>
          Mentions <span className="ml-1 text-xs">{counts.mention || 0}</span>
        </button>
        <button onClick={() => setKind("review")} className={"px-3 py-1.5 rounded text-sm border " + (kind === "review" ? "bg-accent/15 border-accent text-accent" : "bg-bg-card border-border text-text-dim hover:text-text")}>
          Reviews <span className="ml-1 text-xs">{counts.review || 0}</span>
        </button>
        <select value={status} onChange={(e) => setLocalStatus(e.target.value)} className="ml-auto bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text">
          <option value="all">Open</option>
          <option value="unread">Unread</option>
          <option value="read">Read</option>
          <option value="replied">Replied</option>
          <option value="hidden">Hidden</option>
          <option value="archived">Archived</option>
        </select>
        <button onClick={() => sync(false)} disabled={syncing} className="px-3 py-1.5 text-sm border border-border rounded text-accent disabled:opacity-50">
          {syncing ? "Syncing..." : "Refresh"}
        </button>
      </div>
      {syncResults.some((r) => r.status === "failed" || (r.warnings && r.warnings.length > 0)) && (
        <div className="px-4 py-2 border-b border-border text-xs text-text-dim">
          {syncResults.map((r) => (
            <div key={r.social_account_id}>
              {r.display_name || accountName(r.social_account_id)}: {r.status === "failed" ? r.error : (r.warnings || []).join("; ")}
            </div>
          ))}
        </div>
      )}
      <div className="flex-1 min-h-0 overflow-hidden flex">
        <aside
          className="border-r border-border min-h-0 overflow-hidden flex flex-col bg-bg-card/20 shrink-0"
          style={{ width: "42%", minWidth: 300, maxWidth: 460 }}
        >
          <div className="p-3 border-b border-border flex flex-col gap-2">
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search inbox..."
              className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text"
            />
            <select
              value={accountID}
              onChange={(e) => setAccountID(e.target.value === "all" ? "all" : Number(e.target.value))}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
            >
              <option value="all">All accounts</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>{a.display_name} · {a.platform}</option>
              ))}
            </select>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
          {loading ? (
            <div className="p-6 text-sm text-text-dim">Loading...</div>
          ) : filteredItems.length === 0 ? (
            <div className="p-6 text-sm text-text-dim">No inbox items yet.</div>
          ) : filteredItems.map((it) => (
            <button
              key={it.id}
              onClick={() => setSelectedID(it.id)}
              className={
                "w-full text-left px-3 py-3 border-b border-border hover:bg-bg-card transition-colors " +
                (selectedID === it.id ? "bg-bg-card border-l-2 border-l-accent" : "border-l-2 border-l-transparent")
              }
            >
              <div className="flex items-start gap-3">
                <Avatar item={it} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 min-w-0">
                    <div className="text-sm text-text truncate font-medium">{inboxAuthor(it)}</div>
                    {it.status === "unread" && <span className="w-2 h-2 rounded-full bg-accent flex-shrink-0" />}
                    <div className="ml-auto text-[11px] text-text-muted flex-shrink-0">{new Date(it.occurred_at).toLocaleDateString()}</div>
                  </div>
                  <div className="mt-1 flex items-center gap-2">
                    <span className="text-[10px] uppercase text-accent">{kindLabel(it.kind)}</span>
                    <span className="text-[10px] uppercase text-text-dim">{it.platform}</span>
                    <span className="text-[10px] text-text-muted truncate">{accountName(it.social_account_id)}</span>
                  </div>
                  <div className="text-xs text-text-dim truncate mt-1">{it.body || "(no text)"}</div>
                </div>
              </div>
            </button>
          ))}
          </div>
        </aside>
        <main className="min-w-0 min-h-0 overflow-hidden flex flex-col bg-bg flex-1">
          {!selected ? (
            <div className="m-auto text-sm text-text-dim">Select an item</div>
          ) : (
            <>
              <div className="border-b border-border px-5 py-4 flex items-center gap-3 bg-bg-card/30">
                <Avatar item={selected} size="lg" />
                <div className="min-w-0">
                  <div className="text-text font-medium truncate">{inboxAuthor(selected)}</div>
                  <div className="text-xs text-text-dim truncate">
                    {accountName(selected.social_account_id)} · {selected.platform} · {kindLabel(selected.kind)} {statusText && `· ${statusText}`}
                  </div>
                </div>
                <div className="ml-auto flex flex-wrap justify-end items-center gap-2">
                  {selected.permalink && (
                    <a href={selected.permalink} target="_blank" rel="noopener" className="px-3 py-1.5 text-sm border border-border rounded text-accent hover:bg-bg-card">Open</a>
                  )}
                  <button onClick={() => act(selected.status === "unread" ? "read" : "unread")} className="px-3 py-1.5 text-sm border border-border rounded text-text-dim hover:text-text">{selected.status === "unread" ? "Read" : "Unread"}</button>
                  <button onClick={() => act("archive")} className="px-3 py-1.5 text-sm border border-border rounded text-text-dim hover:text-text">Archive</button>
                </div>
              </div>
              <div className="flex-1 min-h-0 overflow-y-auto p-5 flex flex-col gap-3">
                {thread.map((m) => (
                  <div key={m.id} className={"flex gap-2 " + (m.direction === "outbound" ? "justify-end" : "justify-start")}>
                    {m.direction !== "outbound" && <Avatar item={m} size="sm" />}
                    <div className={"max-w-[78%] rounded px-3 py-2 border " + (m.direction === "outbound" ? "bg-accent/15 border-accent/40 text-text" : "bg-bg-card border-border text-text")}>
                      <div className="text-xs text-text-dim mb-1">{inboxAuthor(m)}</div>
                      <div className="text-sm whitespace-pre-wrap">{m.body || "(no text)"}</div>
                      <div className="text-[11px] text-text-muted mt-2">{new Date(m.occurred_at).toLocaleString()}</div>
                    </div>
                  </div>
                ))}
              </div>
              <div className="border-t border-border p-4 flex flex-col gap-2 bg-bg-card/20">
                <textarea
                  value={replyBody}
                  onChange={(e) => setReplyBody(e.target.value)}
                  placeholder="Reply..."
                  className="w-full min-h-[88px] bg-bg-input border border-border rounded px-3 py-2 text-sm text-text resize-y"
                />
                <div className="flex flex-wrap gap-2">
                  <button onClick={() => act("reply", { body: replyBody })} disabled={!replyBody.trim()} className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50">Reply</button>
                  {(selected.platform === "instagram" || selected.platform === "facebook") && selected.kind === "comment" && (
                    <button onClick={() => act("private_reply", { body: replyBody })} disabled={!replyBody.trim()} className="px-3 py-1.5 text-sm border border-border rounded text-accent disabled:opacity-50">Private reply</button>
                  )}
                  {selected.kind === "comment" && (
                    <>
                      <button onClick={() => act("like")} className="px-3 py-1.5 text-sm border border-border rounded text-text-dim hover:text-text">Like</button>
                      <button onClick={() => act(selected.status === "hidden" ? "unhide" : "hide")} className="px-3 py-1.5 text-sm border border-border rounded text-text-dim hover:text-text">{selected.status === "hidden" ? "Unhide" : "Hide"}</button>
                      <button onClick={() => act("delete")} className="px-3 py-1.5 text-sm border border-red text-red rounded">Delete</button>
                    </>
                  )}
                </div>
              </div>
            </>
          )}
        </main>
      </div>
    </div>
  );
}

function PostsView({
  posts, onChange, setStatus, projectId,
}: { posts: Post[]; onChange: () => void; setStatus: (s: string) => void; projectId?: string | null }) {
  // Open reschedule dialog for a specific post (null = closed).
  const [rescheduleFor, setRescheduleFor] = useState<Post | null>(null);
  // Same pattern for the delete-confirm modal: which post (null = closed).
  const [deleteFor, setDeleteFor] = useState<Post | null>(null);
  // And for the edit dialog.
  const [editFor, setEditFor] = useState<Post | null>(null);

  const retry = async (postId: number) => {
    try {
      await fetch(socialURL(`/posts/${postId}/retry`, projectId), { method: "POST", credentials: "same-origin" });
      setStatus("Retry triggered.");
      onChange();
    } catch (e) {
      setStatus("Retry failed: " + (e as Error).message);
    }
  };

  const executeDelete = async (post: Post) => {
    try {
      const res = await fetch(socialURL(`/posts/${post.id}`, projectId), {
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
            {(p.status === "published" || p.status === "partial") &&
              p.targets.some((t) => isEditablePlatform(t.platform)) && (
              <button
                onClick={() => setEditFor(p)}
                className="text-xs text-accent hover:underline"
                title="Edit post body and metadata where the platform allows"
              >
                Edit
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
                <MediaThumb key={id} fileId={id} projectId={p.media_project_id || projectId} />
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
          projectId={projectId}
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
      {editFor && (
        <EditPostDialog
          post={editFor}
          onClose={() => setEditFor(null)}
          onSaved={() => { setEditFor(null); onChange(); }}
          setStatus={setStatus}
          projectId={projectId}
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
  secondaryDangerLabel, onSecondaryDanger, onConfirm, onClose,
}: {
  title: string;
  body?: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  secondaryDangerLabel?: string;
  onSecondaryDanger?: () => void | Promise<void>;
  onConfirm: () => void | Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => void | Promise<void>) => {
    setBusy(true);
    try {
      await fn();
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
	          {secondaryDangerLabel && onSecondaryDanger && (
	            <button
	              type="button"
	              onClick={() => run(onSecondaryDanger)}
	              disabled={busy}
	              className="mr-auto px-3 py-1.5 text-sm border border-red text-red rounded hover:bg-red/10 disabled:opacity-50"
	            >
	              {busy ? "…" : secondaryDangerLabel}
	            </button>
	          )}
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
	            onClick={() => run(onConfirm)}
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

// EDITABLE_PLATFORMS — kept here (rather than driven by the server's
// /platforms endpoint) because edit-support is more about platform
// API constraints than per-install configuration. If the server gains
// more edit verbs (Reddit, IG caption-only) we widen this set.
const EDITABLE_PLATFORMS: Set<string> = new Set(["facebook", "twitter", "youtube"]);
const IMPORTABLE_PLATFORMS: Set<string> = new Set(["facebook", "instagram", "tiktok", "twitter", "youtube"]);

function isEditablePlatform(platform: string): boolean {
  return EDITABLE_PLATFORMS.has(platform);
}

function isImportablePlatform(platform: string): boolean {
  return IMPORTABLE_PLATFORMS.has(platform);
}

// --- EditPostDialog ----------------------------------------------
//
// Pre-fills the post's current body + per-target options, lets the
// user change them, and submits to POST /posts/:id/edit. Targets on
// non-editable platforms render as disabled rows with a "not editable"
// badge — included in the dialog so the user has visibility into the
// full set of targets even though they can't be modified.

interface TargetEditOutcome {
  social_account_id: number;
  platform: string;
  status: "ok" | "unsupported" | "skipped" | "failed";
  reason?: string;
  error?: string;
}

function EditPostDialog({
  post, onClose, onSaved, setStatus, projectId,
}: {
  post: Post;
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
  projectId?: string | null;
}) {
  const [body, setBody] = useState(post.body || "");
  // Per-target option overrides, keyed by social_account_id. Seeded
  // empty — the server already has the existing options on file and
  // will merge our overrides on top, so the user only types what they
  // want to change.
  const [targetOptions, setTargetOptions] = useState<Record<number, Record<string, any>>>({});
  const [busy, setBusy] = useState(false);

  const setOpt = (acctId: number, key: string, value: any) => {
    setTargetOptions((prev) => {
      const next = { ...prev, [acctId]: { ...(prev[acctId] || {}), [key]: value } };
      if (typeof value === "string" && value.trim() === "") {
        const acct = { ...next[acctId] };
        delete acct[key];
        if (Object.keys(acct).length === 0) {
          const { [acctId]: _drop, ...rest } = next;
          return rest;
        }
        next[acctId] = acct;
      }
      return next;
    });
  };

  const submit = async () => {
    setBusy(true);
    try {
      const payload: Record<string, any> = {};
      if (body !== post.body) payload.body = body;
      const targets = Object.entries(targetOptions).map(([id, opts]) => ({
        social_account_id: Number(id), ...opts,
      }));
      if (targets.length > 0) payload.targets = targets;
      if (Object.keys(payload).length === 0) {
        setStatus("Nothing to save — body and targets unchanged.");
        setBusy(false);
        return;
      }
      const res = await fetch(socialURL(`/posts/${post.id}/edit`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { targets?: TargetEditOutcome[] };
      const outcomes = data.targets || [];
      const failed = outcomes.filter((t) => t.status === "failed");
      const unsupported = outcomes.filter((t) => t.status === "unsupported");
      if (failed.length > 0) {
        const names = Array.from(new Set(failed.map((o) => o.platform))).join(", ");
        setStatus(`Saved locally. Edit failed upstream on: ${names}.`);
      } else if (unsupported.length > 0) {
        setStatus(`Saved. ${unsupported.length} target${unsupported.length !== 1 ? "s" : ""} couldn't be edited upstream (platform constraint).`);
      } else {
        setStatus("Edits applied.");
      }
      onSaved();
    } catch (e) {
      setStatus("Edit failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div
        className="fixed inset-0 z-50 grid place-items-center bg-black/60"
        onClick={(e) => { if (e.target === e.currentTarget && !busy) onClose(); }}
      >
        <div className="bg-bg-card border border-border rounded-lg shadow-lg w-[min(640px,92vw)] max-h-[85vh] flex flex-col">
          <div className="flex items-center justify-between px-4 py-3 border-b border-border">
            <div className="text-text font-bold">Edit post</div>
            <button onClick={onClose} disabled={busy} className="text-text-muted hover:text-text text-lg leading-none disabled:opacity-50">×</button>
          </div>
          <div className="flex-1 overflow-y-auto p-4 flex flex-col gap-4">
            <div className="flex flex-col gap-1">
              <label className="text-xs uppercase tracking-wide text-text-dim">Body</label>
              <textarea
                value={body}
                onChange={(e) => setBody(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-3 py-2 text-sm min-h-[100px] resize-y"
              />
              <div className="text-text-dim text-xs">
                {body.length} chars · applies to platforms that don't have a per-target override
              </div>
            </div>
            <div className="flex flex-col gap-2">
              <label className="text-xs uppercase tracking-wide text-text-dim">Targets</label>
              {post.targets.map((t) => {
                const editable = isEditablePlatform(t.platform);
                return (
                  <div
                    key={t.id}
                    className={
                      "border rounded p-3 " +
                      (editable ? "border-border" : "border-border opacity-60")
                    }
                  >
                    <div className="flex items-center gap-2 mb-2">
                      <span className="text-text text-sm font-medium">{t.platform}</span>
                      <span className="text-text-dim text-xs">·</span>
                      <span className="text-text-dim text-xs">{t.display_name}</span>
                      {!editable && (
                        <span className="ml-auto text-xs text-text-dim italic">
                          not editable on this platform
                        </span>
                      )}
                    </div>
                    {editable && t.platform === "facebook" && (
                      <FieldText
                        label="Override message"
                        placeholder="leave blank to use the post body above"
                        value={targetOptions[t.social_account_id]?.body || ""}
                        onChange={(v) => setOpt(t.social_account_id, "body", v)}
                      />
                    )}
                    {editable && t.platform === "youtube" && (
                      <div className="flex flex-col gap-2">
                        <FieldText
                          label="Title"
                          value={targetOptions[t.social_account_id]?.title || ""}
                          onChange={(v) => setOpt(t.social_account_id, "title", v)}
                          placeholder="leave blank to derive from body"
                        />
                        <FieldText
                          label="Description (override)"
                          value={targetOptions[t.social_account_id]?.body || ""}
                          onChange={(v) => setOpt(t.social_account_id, "body", v)}
                          placeholder="leave blank to use the post body above"
                          textarea
                        />
                        <div className="flex flex-col gap-1">
                          <label className="text-xs uppercase tracking-wide text-text-dim">Visibility</label>
                          <select
                            value={targetOptions[t.social_account_id]?.visibility || ""}
                            onChange={(e) => setOpt(t.social_account_id, "visibility", e.target.value)}
                            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                          >
                            <option value="">Keep current</option>
                            <option value="public">public</option>
                            <option value="unlisted">unlisted</option>
                            <option value="private">private</option>
                          </select>
                        </div>
                        <OptionFieldInput
                          field={{
                            name: "thumbnail_storage_id",
                            label: "Thumbnail",
                            type: "media",
                            help: "Pick a Storage image to apply to the existing YouTube video.",
                          }}
                          value={targetOptions[t.social_account_id]?.thumbnail_storage_id}
                          projectId={projectId}
                          onChange={(v) => setOpt(t.social_account_id, "thumbnail_storage_id", v)}
                        />
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
          <div className="flex items-center justify-end gap-2 px-4 py-3 border-t border-border">
            <button
              onClick={onClose}
              disabled={busy}
              className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-card disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              onClick={submit}
              disabled={busy}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {busy ? "…" : "Save changes"}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

function FieldText({
  label, value, onChange, placeholder, textarea,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  textarea?: boolean;
}) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-xs uppercase tracking-wide text-text-dim">{label}</label>
      {textarea ? (
        <textarea
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm min-h-[60px] resize-y"
        />
      ) : (
        <input
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
      )}
    </div>
  );
}

function RescheduleDialog({
  post, onClose, onChanged, setStatus, projectId,
}: {
  post: Post;
  onClose: () => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
  projectId?: string | null;
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
      const res = await fetch(socialURL(`/posts/${post.id}/reschedule`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ schedule_at: when, _project_id: projectId || undefined }),
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
// time), so the component fetches metadata via files_get on mount.
// Images render directly from storage. Videos prefer Media's generated
// thumbnail/keyframe images; browser-side canvas extraction is avoided
// here because S3 redirects make the video drawable cross-origin even
// though normal image/video display still works.
//
// Cache is process-wide: the same fileId rendered in five posts
// only triggers one /files/<id> fetch, even before React Query.
const mediaMetaCache = new Map<string, { mime: string; name: string } | "loading" | "error">();
const mediaMetaWaiters = new Map<string, ((m: { mime: string; name: string } | null) => void)[]>();
const mediaPosterCache = new Map<string, string | "loading" | "none" | "error">();
const mediaPosterWaiters = new Map<string, ((url: string | null) => void)[]>();

async function loadMediaMeta(fileId: number, projectId?: string | null): Promise<{ mime: string; name: string } | null> {
  const key = `${projectId || ""}:${fileId}`;
  const cached = mediaMetaCache.get(key);
  if (cached && cached !== "loading" && cached !== "error") return cached;
  if (cached === "loading") {
    return new Promise((resolve) => {
      const w = mediaMetaWaiters.get(key) ?? [];
      w.push(resolve);
      mediaMetaWaiters.set(key, w);
    });
  }
  mediaMetaCache.set(key, "loading");
  try {
    const res = await fetch(storageURL(`/files/${fileId}`, projectId), { credentials: "same-origin" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = (await res.json()) as { file?: { content_type?: string; name?: string } };
    const meta = {
      mime: data?.file?.content_type ?? "",
      name: data?.file?.name ?? "",
    };
    mediaMetaCache.set(key, meta);
    const waiters = mediaMetaWaiters.get(key) ?? [];
    mediaMetaWaiters.delete(key);
    for (const w of waiters) w(meta);
    return meta;
  } catch {
    mediaMetaCache.set(key, "error");
    const waiters = mediaMetaWaiters.get(key) ?? [];
    mediaMetaWaiters.delete(key);
    for (const w of waiters) w(null);
    return null;
  }
}

async function loadMediaPosterURL(fileId: number, projectId?: string | null): Promise<string | null> {
  const key = `${projectId || ""}:${fileId}`;
  const cached = mediaPosterCache.get(key);
  if (cached && cached !== "loading" && cached !== "none" && cached !== "error") return cached;
  if (cached === "none" || cached === "error") return null;
  if (cached === "loading") {
    return new Promise((resolve) => {
      const w = mediaPosterWaiters.get(key) ?? [];
      w.push(resolve);
      mediaPosterWaiters.set(key, w);
    });
  }
  mediaPosterCache.set(key, "loading");
  try {
    const params = new URLSearchParams();
    appendProjectScope(params, projectId);
    const qs = params.toString();
    const res = await fetch(`${MEDIA_API}/media/${fileId}${qs ? `?${qs}` : ""}`, {
      credentials: "same-origin",
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = (await res.json()) as { derivations?: MediaDerivation[] };
    const derivations = data.derivations || [];
    const pick =
      derivations.find((d) => d.kind === "thumbnail" && d.status === "ok" && d.storage_file_id) ||
      derivations
        .filter((d) => d.kind === "keyframe" && d.status === "ok" && d.storage_file_id)
        .sort((a, b) => (a.position_ms || 0) - (b.position_ms || 0))[0];
    const url = pick?.storage_file_id
      ? storageURL(`/files/${pick.storage_file_id}/content`, projectId)
      : null;
    mediaPosterCache.set(key, url || "none");
    const waiters = mediaPosterWaiters.get(key) ?? [];
    mediaPosterWaiters.delete(key);
    for (const w of waiters) w(url);
    return url;
  } catch {
    mediaPosterCache.set(key, "error");
    const waiters = mediaPosterWaiters.get(key) ?? [];
    mediaPosterWaiters.delete(key);
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
  posts?: number;
  reach?: number;
  impressions?: number;
  engagements?: number;
  views?: number;
  insights?: Record<string, { time?: string; value: number }[]>;
  raw?: any;
}

function MetricsView({
  posts, accounts, setStatus, onPostsChanged, projectId,
}: {
  posts: Post[];
  accounts: SocialAccount[];
  setStatus: (s: string) => void;
  onPostsChanged: () => void;
  projectId?: string | null;
}) {
  const [accountFor, setAccountFor] = useState<Record<number, AccountMetrics | "loading" | { error: string }>>({});
  const [postFor, setPostFor] = useState<Record<number, PostMetrics | "loading" | { error: string }>>({});
  const [expanded, setExpanded] = useState<number | null>(null);
  const [activeAccountId, setActiveAccountId] = useState<number | null>(accounts[0]?.id ?? null);
  const [syncFor, setSyncFor] = useState<Record<number, "loading" | "done" | { error: string }>>({});
  const autoLoadedAccounts = useRef<Set<number>>(new Set());
  const accountIds = accounts.map((a) => a.id).join(",");

  useEffect(() => {
    if (accounts.length === 0) {
      setActiveAccountId(null);
      return;
    }
    if (!activeAccountId || !accounts.some((a) => a.id === activeAccountId)) {
      setActiveAccountId(accounts[0].id);
    }
  }, [accounts, activeAccountId]);

  const loadAccount = async (id: number) => {
    setAccountFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(socialURL(`/accounts/${id}/metrics`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as AccountMetrics;
      setAccountFor((prev) => ({ ...prev, [id]: data }));
    } catch (e) {
      setAccountFor((prev) => ({ ...prev, [id]: { error: (e as Error).message } }));
    }
  };

  const syncAccountPosts = async (id: number, quiet = false) => {
    setSyncFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(socialURL(`/accounts/${id}/import`, projectId, { limit: 100 }), {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { status: string; imported?: number; skipped_existing?: number; reason?: string; error?: string };
      if (data.status === "ok") {
        setSyncFor((prev) => ({ ...prev, [id]: "done" }));
        if (!quiet) setStatus(`Imported ${data.imported || 0}; skipped ${data.skipped_existing || 0}`);
        onPostsChanged();
      } else if (data.status === "unsupported") {
        setSyncFor((prev) => ({ ...prev, [id]: { error: data.reason || "import unsupported" } }));
      } else {
        throw new Error(data.error || "import failed");
      }
    } catch (e) {
      setSyncFor((prev) => ({ ...prev, [id]: { error: (e as Error).message } }));
      if (!quiet) setStatus("Post sync failed: " + (e as Error).message);
    }
  };

  useEffect(() => {
    for (const account of accounts) {
      if (!autoLoadedAccounts.current.has(account.id)) {
        autoLoadedAccounts.current.add(account.id);
        loadAccount(account.id);
      }
    }
  }, [accountIds]);

  const loadPost = async (id: number) => {
    setPostFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(socialURL(`/posts/${id}/metrics`, projectId), { credentials: "same-origin" });
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

  const activeAccount = accounts.find((a) => a.id === activeAccountId) || accounts[0] || null;
  const activeMetrics = activeAccount ? accountFor[activeAccount.id] : null;
  const published = posts.filter((p) =>
    (p.status === "published" || p.status === "partial") &&
    (!activeAccount || (p.targets || []).some((t) => t.social_account_id === activeAccount.id))
  );

  return (
    <div className="p-4 flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="text-sm uppercase tracking-wide text-text-dim">Accounts</h2>
          <span className="text-text-dim text-xs">Auto-loads first account</span>
        </div>
        {accounts.length === 0 ? (
          <div className="text-text-dim text-sm py-6 text-center">No accounts connected.</div>
        ) : (
          <div className="flex gap-2 overflow-x-auto pb-1">
            {accounts.map((a) => {
              const m = accountFor[a.id];
              return (
                <button
                  key={a.id}
                  onClick={() => setActiveAccountId(a.id)}
                  disabled={m === "loading"}
                  className={`text-left flex items-center gap-3 px-3 py-2 border rounded min-w-[220px] disabled:opacity-50 ${
                    activeAccount?.id === a.id ? "border-accent bg-bg-card" : "border-border hover:border-text-dim"
                  }`}
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

      {activeAccount && (
        <section className="flex flex-col gap-3 border border-border rounded p-3 bg-bg-card/30">
          <div className="flex items-center gap-3">
            <div className="flex-1 min-w-0">
              <div className="text-text font-medium truncate">{activeAccount.display_name}</div>
              <div className="text-text-dim text-xs">{activeAccount.platform}</div>
            </div>
            <button
              onClick={() => loadAccount(activeAccount.id)}
              className="text-xs text-accent hover:underline"
            >
              Refresh metrics
            </button>
            <button
              onClick={() => syncAccountPosts(activeAccount.id)}
              disabled={syncFor[activeAccount.id] === "loading"}
              className="text-xs text-accent hover:underline disabled:opacity-50"
            >
              {syncFor[activeAccount.id] === "loading" ? "Syncing..." : "Sync posts"}
            </button>
          </div>
          {activeMetrics === "loading" ? (
            <div className="text-text-dim text-sm">Loading account metrics...</div>
          ) : activeMetrics && typeof activeMetrics === "object" && "error" in activeMetrics ? (
            <div className="text-red text-sm">{activeMetrics.error}</div>
          ) : activeMetrics && typeof activeMetrics === "object" && "status" in activeMetrics ? (
            <>
              <AccountMetricsSummary metrics={activeMetrics as AccountMetrics} />
              <InsightCharts metrics={activeMetrics as AccountMetrics} />
            </>
          ) : (
            <div className="text-text-dim text-sm">Waiting for metrics...</div>
          )}
          {syncFor[activeAccount.id] && typeof syncFor[activeAccount.id] === "object" && "error" in syncFor[activeAccount.id] && (
            <div className="text-text-dim text-xs">{(syncFor[activeAccount.id] as { error: string }).error}</div>
          )}
        </section>
      )}

      {accounts.length > 1 && (
        <AccountHistoryGrid accounts={accounts} accountFor={accountFor} />
      )}

      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="text-sm uppercase tracking-wide text-text-dim">Recent posts</h2>
          {activeAccount && <span className="text-text-dim text-xs">{published.length} loaded</span>}
        </div>
        {published.length === 0 ? (
          <div className="text-text-dim text-sm py-6 text-center">No posts loaded for this account yet.</div>
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
  if (am.posts != null && am.posts > 0) bits.push(`${am.posts} posts`);
  return <span className="text-text text-xs">{bits.join(" · ") || "ok"}</span>;
}

function AccountMetricsSummary({ metrics }: { metrics: AccountMetrics }) {
  if (metrics.status === "unsupported") {
    return <div className="text-text-dim text-sm">{metrics.reason || "Account metrics are not available for this platform yet."}</div>;
  }
  if (metrics.status === "failed") {
    return <div className="text-red text-sm">{metrics.error || "Metrics fetch failed."}</div>;
  }
  const stats = [
    ["followers", metrics.followers],
    ["following", metrics.following],
    ["videos", metrics.total_videos],
    ["posts", metrics.posts],
    ["likes", metrics.total_likes],
    ["reach", metrics.reach],
    ["impressions", metrics.impressions],
    ["engagements", metrics.engagements],
    ["views", metrics.views],
  ].filter(([, value]) => value != null && Number(value) > 0) as [string, number][];
  if (stats.length === 0) {
    return <div className="text-text-dim text-sm">No totals returned by the platform.</div>;
  }
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
      {stats.map(([label, value]) => (
        <div key={label} className="border border-border rounded px-3 py-2 bg-bg">
          <div className="text-text font-medium">{formatNumber(value)}</div>
          <div className="text-text-dim text-[10px] uppercase tracking-wide">{label}</div>
        </div>
      ))}
    </div>
  );
}

function AccountHistoryGrid({
  accounts,
  accountFor,
}: {
  accounts: SocialAccount[];
  accountFor: Record<number, AccountMetrics | "loading" | { error: string }>;
}) {
  const loaded = accounts
    .map((account) => ({ account, metrics: accountFor[account.id] }))
    .filter(({ metrics }) => metrics && metrics !== "loading" && typeof metrics === "object" && "status" in metrics) as {
      account: SocialAccount;
      metrics: AccountMetrics;
    }[];
  if (loaded.length === 0) return null;
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm uppercase tracking-wide text-text-dim">Account history</h2>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-2">
        {loaded.map(({ account, metrics }) => (
          <div key={account.id} className="border border-border rounded p-3 bg-bg-card/30">
            <div className="flex items-center justify-between gap-3 mb-2">
              <div className="min-w-0">
                <div className="text-text text-sm truncate">{account.display_name}</div>
                <div className="text-text-dim text-xs">{account.platform}</div>
              </div>
              <AccountMetricsCell m={metrics} />
            </div>
            <InsightCharts metrics={metrics} compact />
          </div>
        ))}
      </div>
    </section>
  );
}

function InsightCharts({ metrics, compact = false }: { metrics: AccountMetrics; compact?: boolean }) {
  const entries = Object.entries(metrics.insights || {})
    .map(([name, points]) => ({ name, points }))
    .filter((entry) => entry.points.length > 0);
  if (entries.length === 0) {
    if (compact) return <div className="text-text-dim text-xs">Totals only</div>;
    return null;
  }
  return (
    <div className={`grid grid-cols-1 ${compact ? "" : "md:grid-cols-2"} gap-2`}>
      {entries.map(({ name, points }) => {
        const latest = points[points.length - 1];
        return (
          <div key={name} className="border border-border rounded px-3 py-2 bg-bg">
            <div className="flex items-center justify-between gap-3">
              <span className="text-text text-sm">{name.replace(/_/g, " ")}</span>
              <span className="text-text font-medium">{formatNumber(latest.value)}</span>
            </div>
            <Sparkline points={points} />
            <div className="text-text-dim text-xs mt-1">
              {points.length} point{points.length !== 1 ? "s" : ""}
              {latest.time ? ` · latest ${new Date(latest.time).toLocaleDateString()}` : ""}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function Sparkline({ points }: { points: { time?: string; value: number }[] }) {
  const data = points.map((point, index) => ({
    index,
    label: point.time ? new Date(point.time).toLocaleDateString() : String(index + 1),
    value: Number(point.value) || 0,
  }));
  if (data.length === 0) return null;
  return (
    <div className="mt-2 h-16 w-full" role="img" aria-hidden="true">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id="socialMetricFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#f97316" stopOpacity={0.28} />
              <stop offset="100%" stopColor="#f97316" stopOpacity={0.02} />
            </linearGradient>
          </defs>
          <XAxis dataKey="index" hide />
          <YAxis hide domain={["dataMin", "dataMax"]} />
          <Tooltip
            cursor={{ stroke: "#3a3a3a", strokeWidth: 1 }}
            contentStyle={{
              background: "#111",
              border: "1px solid #333",
              borderRadius: 4,
              color: "#e5e5e5",
              fontSize: 12,
            }}
            labelFormatter={(_, payload) => payload?.[0]?.payload?.label || ""}
            formatter={(value) => [formatNumber(Number(value) || 0), "value"]}
          />
          <Area
            type="monotone"
            dataKey="value"
            stroke="#f97316"
            strokeWidth={2}
            fill="url(#socialMetricFill)"
            dot={false}
            activeDot={{ r: 3, stroke: "#f97316", strokeWidth: 1, fill: "#111" }}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
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

function MediaThumb({ fileId, projectId }: { fileId: number; projectId?: string | null }) {
  const [meta, setMeta] = useState<{ mime: string; name: string } | null>(null);
  const [posterURL, setPosterURL] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const url = storageURL(`/files/${fileId}/content`, projectId);
  useEffect(() => {
    let alive = true;
    setMeta(null);
    setPosterURL(null);
    loadMediaMeta(fileId, projectId).then((m) => { if (alive) setMeta(m); });
    return () => { alive = false; };
  }, [fileId, projectId]);
  const isVideo = isVideoMime(meta?.mime || "", meta?.name || "");
  useEffect(() => {
    if (!isVideo || posterURL) return;
    let alive = true;
    loadMediaPosterURL(fileId, projectId).then((poster) => {
      if (!alive) return;
      setPosterURL(poster);
    });
    return () => { alive = false; };
  }, [fileId, isVideo, posterURL, projectId]);
  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className="block w-20 h-20 rounded border border-border overflow-hidden bg-bg-input flex-shrink-0 relative group"
        title={meta?.name || `file #${fileId}`}
      >
        {isVideo ? (
          <>
            {posterURL ? (
              <img src={posterURL} alt={meta?.name || ""} className="w-full h-full object-cover" />
            ) : (
              <video
                src={url}
                preload="metadata"
                muted
                playsInline
                className="w-full h-full object-cover"
              />
            )}
            <PlayBadge />
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
