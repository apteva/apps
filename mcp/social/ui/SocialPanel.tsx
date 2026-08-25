// SocialPanel — accounts + compose + posts in a single panel.
//
// Surface:
//   - Tab "Accounts": connected social accounts grid + Add Account flow
//     (OAuth in popup → page picker if needed → finalize).
//   - Tab "Compose": prompt body + multi-select accounts + media picker
//     (storage app, when bound) + explicit Draft/Schedule/Publish → post_create.
//   - Tab "Posts": compact list or bounded month/week calendar with
//     shared filters, post details, and lifecycle actions.
//
// Lives in the social app's sidecar at /api/apps/social/ui/SocialPanel.mjs.
// The host React (19) + react-dom come from the dashboard's importmap;
// this file uses the same useAppEvents pattern as media-studio.

import { useCallback, useEffect, useRef, useState } from "react";
import { File, FileAudio, Paperclip, X } from "lucide-react";
import {
  Area,
  AreaChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { uploadResumable } from "./uploadResumable";
import { isTrustedOAuthMessage, parseStoredProfileId, scopedAppURL } from "./panelScope";
import { finalizedAccountError, mcpEnvelopeError } from "./accountFlow";
import {
  ACCOUNT_METRICS_STALE_MS,
  accountMetricsNeedRefresh,
  metricTrendEntries,
} from "./metricsPresentation";
import { platformPresentation } from "./platformPresentation";
import {
  inboxAttachmentCapabilities,
  parseInboxAttachments,
  storageAttachmentKind,
  type InboxAttachment,
  type InboxAttachmentKind,
} from "./inboxAttachments";
import {
  calendarWindow,
  filterCalendarPosts,
  groupPostsByLocalDay,
  listLeadMediaStyle,
  listPostRowStyle,
  localDateKey,
  moveCalendarCursor,
  postLifecycleDate,
  sortPostList,
  stableSquareStyle,
  type CalendarScale,
  type PostViewMode,
} from "./postCalendar";

const API = "/api/apps/social";
const STORAGE_API = "/api/apps/storage";
const MEDIA_API = "/api/apps/media";
const IMPORTABLE_PLATFORMS = new Set(["facebook", "instagram", "tiktok", "twitter", "youtube"]);

function appURL(path: string, projectId?: string | null): string {
  return scopedAppURL(API, path, projectId);
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

function storedProfileId(projectId: string): number | null {
  try {
    return parseStoredProfileId(localStorage.getItem(`social.activeProfile.${projectId || ""}`));
  } catch {
    return null;
  }
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
  provider_slug?: string;
  provider_account_id?: string;
  provider_profile_id?: string;
  capabilities?: Record<string, unknown> | null;
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
  options?: Record<string, any>;
  failure_code?: string;
  retryable?: boolean;
  upstream_status?: number;
  existing_post_id?: string;
  provider_sync_status?: string;
}

interface PostReview {
  revision: number;
  action: "submit" | "approve" | "reject";
  actor: string;
  reason?: string;
  created_at: string;
}

interface Post {
  id: number;
  body: string;
  media_storage_ids: number[];
  external_media_urls?: string[];
  schedule_at: string;
  status: string;
  created_at: string;
  published_at: string;
  targets: PostTarget[];
  profile_id?: number;
  revision: number;
  approval_status: "not_requested" | "pending" | "approved" | "rejected";
  approved_revision: number;
  approval_required: boolean;
  rejection_reason?: string;
  requested_mode: "draft" | "schedule" | "publish" | "";
  provider_sync_mode: "local" | "mirror";
  source: "local" | "provider";
  updated_at?: string;
  reviews?: PostReview[];
}

interface InboxItem {
  id: number;
  project_id: string;
  social_account_id: number;
  platform: string;
  kind: "comment" | "dm" | "mention" | "review";
  external_id: string;
  parent_external_id?: string;
  post_id?: number;
  external_post_id?: string;
  author_external_id?: string;
  author_name?: string;
  author_handle?: string;
  author_avatar_url?: string;
  body: string;
  media?: unknown;
  permalink?: string;
  rating?: number;
  occurred_at: string;
  fetched_at: string;
  status: "unread" | "read" | "replied" | "hidden" | "archived";
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
  zernio_available?: boolean;
  provider_only?: boolean;
  // option_fields — per-platform overrides the compose dialog can
  // surface as inputs. Empty when the platform has nothing to
  // customise (Twitter / FB / IG / LinkedIn in v1; YouTube and
  // TikTok expose a few platform-specific controls).
  option_fields?: OptionField[];
  inbox?: {
    dm_read?: boolean;
    dm_write?: boolean;
    dm_attachment_types?: InboxAttachmentKind[];
    dm_max_attachments?: number;
    comments_read?: boolean;
    comments_write?: boolean;
    comment_attachment_types?: InboxAttachmentKind[];
    comment_max_attachments?: number;
    private_reply?: boolean;
  };
}

interface OptionField {
  name: string;
  label: string;
  type: "text" | "textarea" | "select" | "tags" | "media" | "number" | "boolean";
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

type MainTab = "accounts" | "posts" | "inbox" | "metrics";

export default function SocialPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<MainTab>("posts");
  const [accounts, setAccounts] = useState<SocialAccount[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [inboxCount, setInboxCount] = useState(0);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [status, setStatus] = useState("");
  const [composeOpen, setComposeOpen] = useState(false);
  // Profile filter — null = "All profiles" (project-wide view).
  // Persists per-project so refreshing the page keeps the user's
  // last-selected brand context.
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [profilesLoaded, setProfilesLoaded] = useState(false);
  const [profileSelection, setProfileSelection] = useState(() => ({
    projectId,
    id: storedProfileId(projectId),
  }));
  const activeProfileId = profileSelection.projectId === projectId
    ? profileSelection.id
    : storedProfileId(projectId);
  const projectRef = useRef(projectId);
  projectRef.current = projectId;
  const dataScope = `${projectId}:${activeProfileId ?? "all"}`;
  const dataScopeRef = useRef(dataScope);
  dataScopeRef.current = dataScope;
  const previousProjectRef = useRef(projectId);
  const profileRequestRef = useRef(0);
  const accountRequestRef = useRef(0);
  const postRequestRef = useRef(0);
  const platformRequestRef = useRef(0);
  useEffect(() => {
    if (previousProjectRef.current === projectId) return;
    previousProjectRef.current = projectId;
    const id = storedProfileId(projectId);
    dataScopeRef.current = `${projectId}:${id ?? "all"}`;
    setProfileSelection({ projectId, id });
    setProfiles([]);
    setProfilesLoaded(false);
    setAccounts([]);
    setPosts([]);
  }, [projectId]);
  const [manageOpen, setManageOpen] = useState(false);
  const selectProfile = useCallback((id: number | null) => {
    dataScopeRef.current = `${projectId}:${id ?? "all"}`;
    setProfileSelection({ projectId, id });
    setAccounts([]);
    setPosts([]);
    try {
      if (id == null) {
        localStorage.removeItem(`social.activeProfile.${projectId || ""}`);
      } else {
        localStorage.setItem(`social.activeProfile.${projectId || ""}`, String(id));
      }
    } catch {}
  }, [projectId]);

  const loadProfiles = useCallback(async () => {
    const requestProject = projectId;
    const requestId = ++profileRequestRef.current;
    try {
      const res = await fetch(appURL("/profiles", projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (requestId !== profileRequestRef.current || projectRef.current !== requestProject) return;
      const next = data.profiles || [];
      setProfiles(next);
      setProfilesLoaded(true);
      setProfileSelection((current) => {
        const currentId = current.projectId === requestProject
          ? current.id
          : storedProfileId(requestProject);
        if (currentId != null && !next.some((p: Profile) => p.id === currentId)) {
          try { localStorage.removeItem(`social.activeProfile.${projectId || ""}`); } catch {}
          dataScopeRef.current = `${requestProject}:all`;
          return { projectId: requestProject, id: null };
        }
        if (current.projectId === requestProject && current.id === currentId) return current;
        return { projectId: requestProject, id: currentId };
      });
    } catch (e) {
      if (requestId !== profileRequestRef.current || projectRef.current !== requestProject) return;
      setProfilesLoaded(true);
      setStatus("Load profiles: " + (e as Error).message);
    }
  }, [projectId]);

  // Profile-scoped fetches — when activeProfileId is set, the
  // accounts/posts queries pass profile_id and the panel only sees
  // that brand's rows. activeProfileId=null = project-wide.
  const profileQuery = useCallback(() => {
    if (activeProfileId == null) return "";
    return `?profile_id=${activeProfileId}`;
  }, [activeProfileId]);

  const loadAccounts = useCallback(async () => {
    const requestScope = dataScope;
    const requestId = ++accountRequestRef.current;
    try {
      const res = await fetch(appURL(`/accounts${profileQuery()}`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (requestId !== accountRequestRef.current || dataScopeRef.current !== requestScope) return;
      setAccounts(data.accounts || []);
    } catch (e) {
      if (requestId !== accountRequestRef.current || dataScopeRef.current !== requestScope) return;
      setStatus("Load accounts: " + (e as Error).message);
    }
  }, [dataScope, profileQuery, projectId]);

  const loadPosts = useCallback(async () => {
    const requestScope = dataScope;
    const requestId = ++postRequestRef.current;
    try {
      const res = await fetch(appURL(`/posts${profileQuery()}`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (requestId !== postRequestRef.current || dataScopeRef.current !== requestScope) return;
      setPosts(data.posts || []);
    } catch (e) {
      if (requestId !== postRequestRef.current || dataScopeRef.current !== requestScope) return;
      setStatus("Load posts: " + (e as Error).message);
    }
  }, [dataScope, profileQuery, projectId]);

  const loadPlatforms = useCallback(async () => {
    const requestProject = projectId;
    const requestId = ++platformRequestRef.current;
    try {
      const res = await fetch(appURL("/platforms", projectId), { credentials: "same-origin" });
      const data = await res.json();
      if (requestId !== platformRequestRef.current || projectRef.current !== requestProject) return;
      setPlatforms(data.platforms || []);
    } catch {}
  }, [projectId]);

  useEffect(() => {
    loadProfiles();
    loadPlatforms();
  }, [loadProfiles, loadPlatforms]);

  useEffect(() => {
    loadAccounts();
    loadPosts();
  }, [loadAccounts, loadPosts]);

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
      if (isTrustedOAuthMessage(ev.origin, window.location.origin, ev.data)) {
        setOauthLanding({
          pendingId: ev.data.pending_account_id,
          connectionId: ev.data.connection_id,
        });
        setTab("accounts");
      } else if (ev.origin === window.location.origin && ev.data?.type === "social.oauth_error") {
        setStatus("Authorization failed or expired. Start the account connection again.");
        setTab("accounts");
      }
    };
    window.addEventListener("message", onMsg);
    return () => window.removeEventListener("message", onMsg);
  }, []);

  const activeProfile = profiles.find((p) => p.id === activeProfileId) || null;
  const clearOauthLanding = useCallback(() => setOauthLanding(null), []);
  const setOauthLandingFromReuse = useCallback((pendingId: number, connectionId: number) => {
    setOauthLanding({ pendingId, connectionId });
  }, []);

  return (
    <div className="h-full min-w-0 overflow-hidden flex flex-col">
      <header className="flex flex-shrink-0 flex-wrap items-center gap-1 border-b border-border px-4 py-2">
        <ProfileSwitcher
          profiles={profiles}
          activeId={activeProfileId}
          loaded={profilesLoaded}
          onSelect={selectProfile}
          onManage={() => setManageOpen(true)}
        />
        <span className="w-px h-5 bg-border mx-2" />
        <Tab label="Posts" value="posts" current={tab} onClick={setTab} count={posts.length} />
        <Tab label="Accounts" value="accounts" current={tab} onClick={setTab} count={accounts.length} />
        <Tab label="Inbox" value="inbox" current={tab} onClick={setTab} count={inboxCount} />
        <Tab label="Metrics" value="metrics" current={tab} onClick={setTab} />
        <button
          onClick={() => setComposeOpen(true)}
          className="ml-auto px-3 py-1 text-sm bg-accent text-bg rounded font-bold"
          title="Create a draft, schedule, or publish"
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
            activeProfile={activeProfile}
            projectId={projectId}
            oauthLanding={oauthLanding}
            onClearLanding={clearOauthLanding}
            onSetLanding={setOauthLandingFromReuse}
            onChange={loadAccounts}
            onImported={loadPosts}
            setStatus={setStatus}
          />
        )}
        {tab === "posts" && (
          <PostsView
            posts={posts}
            accounts={accounts}
            activeProfileId={activeProfile?.id || null}
            onChange={loadPosts}
            setStatus={setStatus}
            projectId={projectId}
          />
        )}
        {tab === "inbox" && (
          <InboxView
            accounts={accounts}
            platforms={platforms}
            projectId={projectId}
            setStatus={setStatus}
            onCountChange={setInboxCount}
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
          accounts={accounts}
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
  profiles, activeId, loaded, onSelect, onManage,
}: {
  profiles: Profile[];
  activeId: number | null;
  loaded: boolean;
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
  const unresolved = activeId != null && !active;
  const label = active?.name || (unresolved ? (loaded ? "Profile unavailable" : "Loading profile…") : "All profiles");
  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 px-2 py-1 text-sm rounded hover:bg-bg-card transition-colors"
        style={{ minWidth: "10rem", maxWidth: "16rem" }}
        title={active ? `Profile: ${active.name}` : label}
      >
        <span
          className="w-2 h-2 rounded-full"
          style={{ backgroundColor: active?.color || "#94a3b8" }}
        />
        <span className={`min-w-0 flex-1 truncate text-left font-medium ${unresolved ? "text-text-dim" : "text-text"}`}>
          {label}
        </span>
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
  const [busy, setBusy] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState<Profile | null>(null);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    setBusy(true);
    try {
      const res = await fetch(appURL("/profiles", projectId), {
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
      const res = await fetch(appURL(`/profiles/${id}`, projectId), {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ is_default: true }),
      });
      if (!res.ok) throw new Error(await res.text());
      onChanged();
    } catch (e) {
      setStatus("Promote: " + (e as Error).message);
    }
  };

  const rename = async (id: number, name: string) => {
    try {
      const res = await fetch(appURL(`/profiles/${id}`, projectId), {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      });
      if (!res.ok) throw new Error(await res.text());
      onChanged();
    } catch (e) {
      setStatus("Rename: " + (e as Error).message);
    }
  };

  const recolor = async (id: number, color: string) => {
    try {
      const res = await fetch(appURL(`/profiles/${id}`, projectId), {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ color }),
      });
      if (!res.ok) throw new Error(await res.text());
      onChanged();
    } catch (e) {
      setStatus("Recolor: " + (e as Error).message);
    }
  };

  const removeProfile = async (id: number) => {
    try {
      const res = await fetch(appURL(`/profiles/${id}`, projectId), {
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
      const res = await fetch(appURL(`/profiles/${profileId}/move`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account_ids: [accountId] }),
      });
      if (!res.ok) throw new Error(await res.text());
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
  label: string; value: MainTab;
  current: string; onClick: (v: MainTab) => void; count?: number;
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
  accounts, platforms, activeProfile, projectId, oauthLanding, onClearLanding, onSetLanding, onChange, onImported, setStatus,
}: {
  accounts: SocialAccount[]; platforms: PlatformInfo[];
  activeProfile: Profile | null;
  projectId?: string | null;
  oauthLanding: { pendingId: number; connectionId: number } | null;
  onClearLanding: () => void;
  onSetLanding: (pendingId: number, connectionId: number) => void;
  onChange: () => void;
  onImported: () => void;
  setStatus: (s: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [providerImportOpen, setProviderImportOpen] = useState(false);
  const closePicker = useCallback(() => {
    onClearLanding();
    onChange();
  }, [onChange, onClearLanding]);

  const handleLanded = useCallback(async (pendingId: number) => {
    // After OAuth, fetch the page list. If empty (no picker required),
    // finalize directly. Otherwise, keep oauthLanding set so the picker
    // renders below.
    try {
      const res = await fetch(appURL(`/accounts/${pendingId}/pages`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const pickerError = mcpEnvelopeError(data);
      if (pickerError) {
        setStatus(pickerError);
        return;
      }
      if (!data.requires_picker) {
        const finalizeRes = await fetch(appURL("/accounts/finalize", projectId), {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ pending_account_id: pendingId }),
        });
        if (!finalizeRes.ok) throw new Error(await finalizeRes.text());
        const finalizeData = await finalizeRes.json();
        const finalizeError = finalizedAccountError(finalizeData);
        if (finalizeError) throw new Error(finalizeError);
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
        <div className="flex items-center gap-2">
          <button
            onClick={() => setImportOpen(true)}
            disabled={accounts.length === 0}
            className="px-3 py-1.5 text-sm border border-border rounded text-text hover:border-text-dim disabled:opacity-50"
          >
            Import history
          </button>
          <button
            onClick={() => setProviderImportOpen(true)}
            className="px-3 py-1.5 text-sm border border-border rounded text-text hover:border-accent"
            title="Import accounts from an optional social provider such as Zernio"
          >
            Import provider
          </button>
          <button
            onClick={() => setAdding(true)}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold"
          >
            + Add account
          </button>
        </div>
      </div>

      {accounts.length === 0 ? (
        <div className="py-12 text-center text-text-muted text-sm">
          No social accounts yet. Click <em>Add account</em> to connect your first one.
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
          {accounts.map((a) => (
            <AccountCard key={a.id} account={a} projectId={projectId} onChange={onChange} setStatus={setStatus} />
          ))}
        </div>
      )}

      {adding && (
        <AddAccountDialog
          platforms={platforms}
          activeProfile={activeProfile}
          projectId={projectId}
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

      {importOpen && (
        <ImportHistoryDialog
          accounts={accounts}
          activeProfile={activeProfile}
          projectId={projectId}
          onClose={() => setImportOpen(false)}
          onImported={() => {
            onImported();
            onChange();
          }}
          setStatus={setStatus}
        />
      )}

      {providerImportOpen && (
        <ProviderImportDialog
          activeProfile={activeProfile}
          projectId={projectId}
          onClose={() => setProviderImportOpen(false)}
          onImported={onChange}
          setStatus={setStatus}
        />
      )}

      {oauthLanding && (
        <PagePicker
          pendingId={oauthLanding.pendingId}
          projectId={projectId}
          onClose={closePicker}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

interface ImportRunAccount {
  account_id: number;
  display_name: string;
  platform: string;
  status: "ready" | "ok" | "unsupported" | "failed";
  imported?: number;
  skipped_existing?: number;
  reason?: string;
  error?: string;
}

interface ImportRunResponse {
  status: string;
  dry_run: boolean;
  limit_per_account: number;
  accounts: ImportRunAccount[];
  imported: number;
  skipped_existing: number;
  unsupported: number;
  failed: number;
}

function ImportHistoryDialog({
  accounts, activeProfile, projectId, onClose, onImported, setStatus,
}: {
  accounts: SocialAccount[];
  activeProfile: Profile | null;
  projectId?: string | null;
  onClose: () => void;
  onImported: () => void;
  setStatus: (s: string) => void;
}) {
  const importableAccounts = accounts.filter((a) => IMPORTABLE_PLATFORMS.has(a.platform));
  const allPlatforms = Array.from(new Set(accounts.map((a) => a.platform))).sort();
  const [selectedPlatforms, setSelectedPlatforms] = useState<Set<string>>(
    () => new Set(importableAccounts.map((a) => a.platform))
  );
  const [selectedAccounts, setSelectedAccounts] = useState<Set<number>>(
    () => new Set(importableAccounts.map((a) => a.id))
  );
  const [limit, setLimit] = useState(100);
  const [busy, setBusy] = useState<"preview" | "import" | null>(null);
  const [result, setResult] = useState<ImportRunResponse | null>(null);

  useEffect(() => {
    setSelectedPlatforms(new Set(importableAccounts.map((a) => a.platform)));
    setSelectedAccounts(new Set(importableAccounts.map((a) => a.id)));
    setResult(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts.map((a) => `${a.id}:${a.platform}`).join(",")]);

  const togglePlatform = (platform: string) => {
    setSelectedPlatforms((prev) => {
      const next = new Set(prev);
      if (next.has(platform)) next.delete(platform);
      else next.add(platform);
      setSelectedAccounts((accountPrev) => {
        const accountNext = new Set(accountPrev);
        for (const account of accounts) {
          if (account.platform !== platform) continue;
          if (next.has(platform) && IMPORTABLE_PLATFORMS.has(platform)) accountNext.add(account.id);
          else accountNext.delete(account.id);
        }
        return accountNext;
      });
      return next;
    });
  };

  const toggleAccount = (id: number) => {
    setSelectedAccounts((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const run = async (dryRun: boolean) => {
    setBusy(dryRun ? "preview" : "import");
    try {
      const payload: Record<string, any> = {
        dry_run: dryRun,
        limit_per_account: limit,
        platforms: Array.from(selectedPlatforms),
        social_account_ids: Array.from(selectedAccounts),
      };
      if (activeProfile) payload.profile_id = activeProfile.id;
      const res = await fetch(appURL("/imports/run", projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as ImportRunResponse;
      setResult(data);
      if (dryRun) {
        setStatus(`Import preview: ${data.accounts.length} account${data.accounts.length !== 1 ? "s" : ""}.`);
      } else {
        setStatus(`Imported ${data.imported}; skipped ${data.skipped_existing}; failed ${data.failed}.`);
        onImported();
      }
    } catch (e) {
      setStatus("Import failed: " + (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const selectableCount = selectedAccounts.size;

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/60"
      onClick={(e) => { if (e.target === e.currentTarget && !busy) onClose(); }}
    >
      <div className="bg-bg-card border border-border rounded-lg shadow-lg w-[min(760px,94vw)] max-h-[86vh] flex flex-col">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <div className="text-text font-bold">Import history</div>
            <div className="text-text-dim text-xs">
              {activeProfile ? `Profile: ${activeProfile.name}` : "Scope: all profiles in this project"}
            </div>
          </div>
          <button onClick={onClose} disabled={!!busy} className="text-text-muted hover:text-text text-lg leading-none disabled:opacity-50">×</button>
        </div>

        <div className="flex-1 overflow-y-auto p-4 flex flex-col gap-4">
          <div className="grid grid-cols-1 md:grid-cols-[180px_1fr] gap-4">
            <div className="flex flex-col gap-2">
              <label className="text-xs uppercase tracking-wide text-text-dim">Depth</label>
              <select
                value={limit}
                onChange={(e) => setLimit(Number(e.target.value))}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                <option value={25}>Last 25 / account</option>
                <option value={100}>Last 100 / account</option>
                <option value={200}>Last 200 / account</option>
              </select>
            </div>
            <div className="flex flex-col gap-2">
              <label className="text-xs uppercase tracking-wide text-text-dim">Networks</label>
              <div className="flex flex-wrap gap-2">
                {allPlatforms.map((platform) => {
                  const supported = IMPORTABLE_PLATFORMS.has(platform);
                  return (
                    <button
                      key={platform}
                      type="button"
                      disabled={!supported}
                      onClick={() => togglePlatform(platform)}
                      className={
                        "px-2 py-1 text-xs rounded border " +
                        (!supported
                          ? "border-border text-text-dim opacity-50 cursor-not-allowed"
                          : selectedPlatforms.has(platform)
                            ? "border-accent text-accent"
                            : "border-border text-text-dim hover:text-text")
                      }
                      title={supported ? platform : "Import not wired for this platform yet"}
                    >
                      {platform}
                    </button>
                  );
                })}
              </div>
            </div>
          </div>

          <div className="flex flex-col gap-2">
            <label className="text-xs uppercase tracking-wide text-text-dim">Accounts</label>
            {accounts.length === 0 ? (
              <div className="text-text-dim text-sm py-6 text-center">No accounts in this scope.</div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                {accounts.map((account) => {
                  const supported = IMPORTABLE_PLATFORMS.has(account.platform);
                  const checked = selectedAccounts.has(account.id);
                  return (
                    <label
                      key={account.id}
                      className={
                        "flex items-center gap-3 border border-border rounded px-3 py-2 " +
                        (supported ? "cursor-pointer" : "opacity-50")
                      }
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        disabled={!supported}
                        onChange={() => toggleAccount(account.id)}
                        className="accent-accent"
                      />
                      {account.avatar_url ? (
                        <img src={account.avatar_url} alt="" className="w-7 h-7 rounded-full" />
                      ) : (
                        <div className="w-7 h-7 rounded-full bg-bg-input" />
                      )}
                      <div className="min-w-0">
                        <div className="text-text text-sm truncate">{account.display_name}</div>
                        <div className="text-text-dim text-xs">{account.platform}</div>
                      </div>
                      {!supported && <span className="ml-auto text-text-dim text-xs">unsupported</span>}
                    </label>
                  );
                })}
              </div>
            )}
          </div>

          {result && (
            <div className="border border-border rounded">
              <div className="px-3 py-2 border-b border-border text-xs text-text-dim">
                {result.dry_run
                  ? `${result.accounts.filter((a) => a.status === "ready").length} ready · ${result.unsupported} unsupported`
                  : `${result.imported} imported · ${result.skipped_existing} skipped · ${result.failed} failed`}
              </div>
              <div className="max-h-52 overflow-y-auto divide-y divide-border">
                {result.accounts.map((account) => (
                  <div key={account.account_id} className="px-3 py-2 flex items-center gap-3 text-sm">
                    <div className="flex-1 min-w-0">
                      <div className="text-text truncate">{account.display_name}</div>
                      <div className="text-text-dim text-xs">{account.platform}</div>
                    </div>
                    <div className="text-xs text-text-dim">
                      {account.status === "ready" && "ready"}
                      {account.status === "ok" && `${account.imported || 0} imported · ${account.skipped_existing || 0} skipped`}
                      {account.status === "unsupported" && (account.reason || "unsupported")}
                      {account.status === "failed" && (account.error || "failed")}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        <div className="flex items-center justify-between gap-3 px-4 py-3 border-t border-border">
          <div className="text-xs text-text-dim">
            {selectableCount} account{selectableCount !== 1 ? "s" : ""} selected
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={onClose}
              disabled={!!busy}
              className="px-3 py-1.5 text-sm text-text-muted disabled:opacity-50"
            >
              Close
            </button>
            <button
              onClick={() => run(true)}
              disabled={!!busy || selectableCount === 0}
              className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg disabled:opacity-50"
            >
              {busy === "preview" ? "Previewing…" : "Preview"}
            </button>
            <button
              onClick={() => run(false)}
              disabled={!!busy || selectableCount === 0}
              className="px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {busy === "import" ? "Importing…" : "Import"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// --- InboxView -----------------------------------------------------

function InboxView({
  accounts, platforms, projectId, setStatus, onCountChange,
}: {
  accounts: SocialAccount[];
  platforms: PlatformInfo[];
  projectId?: string | null;
  setStatus: (s: string) => void;
  onCountChange: (n: number) => void;
}) {
  const [items, setItems] = useState<InboxItem[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [selected, setSelected] = useState<InboxItem | null>(null);
  const [thread, setThread] = useState<InboxItem[]>([]);
  const [accountFilter, setAccountFilter] = useState("all");
  const [kindFilter, setKindFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState("open");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [replyBody, setReplyBody] = useState("");
  const [replyMode, setReplyMode] = useState<"public" | "private">("public");
  const [replyAttachments, setReplyAttachments] = useState<StorageFile[]>([]);
  const [attachmentPickerOpen, setAttachmentPickerOpen] = useState(false);

  const accountIDs = accounts.map((a) => a.id);
  const selectedAccountIDs =
    accountFilter === "all"
      ? accountIDs
      : accountFilter ? [Number(accountFilter)] : [];

  const loadItems = useCallback(async () => {
    if (accounts.length === 0) {
      setItems([]);
      setSelectedId(null);
      setSelected(null);
      setThread([]);
      onCountChange(0);
      return;
    }
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (selectedAccountIDs.length > 0) {
        params.set("social_account_ids", selectedAccountIDs.join(","));
      }
      if (kindFilter !== "all") params.set("kinds", kindFilter);
      if (statusFilter !== "open") {
        params.set(
          "status",
          statusFilter === "all" ? "unread,read,replied,hidden,archived" : statusFilter,
        );
      }
      params.set("limit", "100");
      const res = await fetch(appURL(`/inbox?${params.toString()}`, projectId), {
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { items?: InboxItem[]; count?: number };
      const next = data.items || [];
      setItems(next);
      onCountChange(next.filter((it) => it.status === "unread").length || next.length);
      setSelectedId((prev) => {
        if (prev && next.some((it) => it.id === prev)) return prev;
        return next[0]?.id || null;
      });
    } catch (e) {
      setStatus("Load inbox: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [accounts.length, accountFilter, kindFilter, statusFilter, projectId, setStatus, onCountChange, selectedAccountIDs.join(",")]);

  const loadThread = useCallback(async (id: number | null) => {
    if (!id) {
      setSelected(null);
      setThread([]);
      return;
    }
    try {
      const res = await fetch(appURL(`/inbox/${id}?with_thread=true`, projectId), {
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { item?: InboxItem; thread?: InboxItem[] };
      setSelected(data.item || null);
      setThread(data.thread && data.thread.length > 0 ? data.thread : data.item ? [data.item] : []);
    } catch (e) {
      setStatus("Load thread: " + (e as Error).message);
    }
  }, [projectId, setStatus]);

  useEffect(() => {
    loadItems();
  }, [loadItems]);

  useEffect(() => {
    loadThread(selectedId);
  }, [selectedId, loadThread]);

  useEffect(() => {
    setReplyMode("public");
    setReplyAttachments([]);
    setAttachmentPickerOpen(false);
  }, [selectedId]);

  const runAction = async (action: string, id = selectedId, body?: Record<string, unknown>) => {
    if (!id) return;
    setBusy(action);
    try {
      const res = await fetch(appURL(`/inbox/${id}/${action}`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: body ? JSON.stringify(body) : "{}",
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const envelopeError = mcpEnvelopeError(data);
      if (envelopeError) {
        setStatus(envelopeError);
      } else if (data?.status === "unsupported") {
        setStatus(data.reason || "Inbox action unsupported.");
      } else if (data?.status === "failed") {
        setStatus(data.error || "Inbox action failed.");
      } else if (data?.status === "partial") {
        setStatus(data.error || "Some message parts could not be delivered.");
      } else {
        setStatus("");
      }
      if ((action === "reply" || action === "private_reply") && data?.status === "ok") {
        setReplyBody("");
        setReplyAttachments([]);
      } else if ((action === "reply" || action === "private_reply") && data?.status === "partial") {
        const deliveredKinds = new Set(
          (Array.isArray(data?.deliveries) ? data.deliveries : [])
            .filter((delivery: { status?: string }) => delivery?.status === "ok")
            .map((delivery: { kind?: string }) => delivery?.kind || ""),
        );
        if (deliveredKinds.has("text")) setReplyBody("");
        setReplyAttachments((current) =>
          current.filter((attachment) => !deliveredKinds.has(storageAttachmentKind(attachment.content_type || "")))
        );
      }
      await loadItems();
      await loadThread(id);
    } catch (e) {
      setStatus(`Inbox ${action}: ` + (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const syncInbox = async () => {
    setBusy("sync");
    try {
      const res = await fetch(appURL("/inbox/sync", projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ social_account_ids: selectedAccountIDs }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { results?: { status: string }[] };
      const ok = (data.results || []).filter((r) => r.status === "ok").length;
      const failed = (data.results || []).filter((r) => r.status === "failed").length;
      const unsupported = (data.results || []).filter((r) => r.status === "unsupported").length;
      setStatus(`Inbox sync: ${ok} ok · ${unsupported} unsupported · ${failed} failed`);
      await loadItems();
    } catch (e) {
      setStatus("Inbox sync: " + (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const accountByID = new Map(accounts.map((a) => [a.id, a]));
  const activeItem = selected || (selectedId ? items.find((it) => it.id === selectedId) || null : null);
  const activeAccount = activeItem ? accountByID.get(activeItem.social_account_id) : undefined;
  const platformInfo = activeItem ? platforms.find((platform) => platform.platform === activeItem.platform) : undefined;
  const attachmentCaps = inboxAttachmentCapabilities(
    activeAccount?.capabilities,
    platformInfo?.inbox as Record<string, unknown> | undefined,
    activeItem?.kind || "",
  );
  const canAttach = attachmentCaps.max > 0 && replyMode !== "private";
  const canPrivateReply =
    !!activeItem &&
    activeItem.kind === "comment" &&
    (activeItem.platform === "facebook" || activeItem.platform === "instagram");

  return (
    <div className="h-full min-h-[560px] flex flex-col bg-bg">
      <div className="border-b border-border px-4 py-3 flex flex-wrap items-center gap-2">
        <select
          value={accountFilter}
          onChange={(e) => setAccountFilter(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
        >
          <option value="all">All accounts</option>
          {accounts.map((a) => (
            <option key={a.id} value={a.id}>{a.display_name} · {a.platform}</option>
          ))}
        </select>
        <select
          value={kindFilter}
          onChange={(e) => setKindFilter(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
        >
          <option value="all">All kinds</option>
          <option value="dm">Messages</option>
          <option value="comment">Comments</option>
          <option value="mention">Mentions</option>
          <option value="review">Reviews</option>
        </select>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
        >
          <option value="open">Open</option>
          <option value="unread">Unread</option>
          <option value="read">Read</option>
          <option value="replied">Replied</option>
          <option value="hidden">Hidden</option>
          <option value="archived">Archived</option>
          <option value="all">All statuses</option>
        </select>
        <button
          onClick={loadItems}
          disabled={loading}
          className="px-3 py-1.5 text-sm border border-border rounded text-text hover:border-accent disabled:opacity-50"
        >
          {loading ? "Refreshing..." : "Refresh"}
        </button>
        <button
          onClick={syncInbox}
          disabled={busy === "sync" || accounts.length === 0}
          className="ml-auto px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        >
          {busy === "sync" ? "Syncing..." : "Sync inbox"}
        </button>
      </div>

      <div className="flex-1 min-h-0 flex flex-col lg:flex-row">
        <aside className="lg:w-[42%] xl:w-[38%] border-r border-border min-h-0 flex flex-col">
          <div className="px-4 py-2 border-b border-border text-xs uppercase tracking-wide text-text-dim flex items-center justify-between">
            <span>Inbox</span>
            <span>{items.length}</span>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
            {items.length === 0 ? (
              <div className="text-text-dim text-sm text-center py-10">
                {accounts.length === 0 ? "No accounts in this profile." : "No inbox items in this view."}
              </div>
            ) : (
              items.map((item) => {
                const active = item.id === selectedId;
                const account = accountByID.get(item.social_account_id);
                return (
                  <button
                    key={item.id}
                    onClick={() => setSelectedId(item.id)}
                    className={
                      "w-full text-left px-4 py-3 border-b border-border flex gap-3 hover:bg-bg-card/60 " +
                      (active ? "bg-bg-card border-l-2 border-l-accent" : "border-l-2 border-l-transparent")
                    }
                  >
                    <InboxAvatar item={item} account={account} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 min-w-0">
                        <span className={`text-xs uppercase ${item.status === "unread" ? "text-accent" : "text-text-dim"}`}>
                          {item.kind}
                        </span>
                        <span className="text-text-dim text-xs">{item.platform}</span>
                        <span className="ml-auto text-text-dim text-xs flex-shrink-0">{formatInboxDate(item.occurred_at)}</span>
                      </div>
                      <div className={`truncate mt-1 ${item.status === "unread" ? "text-text font-semibold" : "text-text"}`}>
                        {inboxAuthor(item)}
                      </div>
                      <div className="text-text-dim text-sm truncate mt-0.5">
                        {item.body || inboxAttachmentSummary(item.media) || "(no text)"}
                      </div>
                      {account && (
                        <div className="text-text-dim text-xs truncate mt-1">{account.display_name}</div>
                      )}
                    </div>
                  </button>
                );
              })
            )}
          </div>
        </aside>

        <section className="flex-1 min-w-0 min-h-0 flex flex-col">
          {!activeItem ? (
            <div className="flex-1 grid place-items-center text-text-dim text-sm">
              Select an inbox item.
            </div>
          ) : (
            <>
              <div className="border-b border-border px-4 py-3 flex items-start gap-3">
                <InboxAvatar item={activeItem} account={accountByID.get(activeItem.social_account_id)} large />
                <div className="min-w-0">
                  <div className="text-text font-semibold truncate">{inboxAuthor(activeItem)}</div>
                  <div className="text-text-dim text-xs">
                    {activeItem.platform} · {activeItem.kind} · {formatInboxDate(activeItem.occurred_at)}
                  </div>
                </div>
                <div className="ml-auto flex items-center gap-2">
                  {activeItem.permalink && (
                    <a
                      href={activeItem.permalink}
                      target="_blank"
                      rel="noopener"
                      className="px-2 py-1 text-xs border border-border rounded text-text-dim hover:text-text"
                    >
                      Open
                    </a>
                  )}
                  <button
                    onClick={() => runAction(activeItem.status === "read" ? "unread" : "read")}
                    disabled={!!busy}
                    className="px-2 py-1 text-xs border border-border rounded text-text-dim hover:text-text disabled:opacity-50"
                  >
                    {activeItem.status === "read" ? "Unread" : "Read"}
                  </button>
                  <button
                    onClick={() => runAction("archive")}
                    disabled={!!busy}
                    className="px-2 py-1 text-xs border border-border rounded text-text-dim hover:text-text disabled:opacity-50"
                  >
                    Archive
                  </button>
                </div>
              </div>

              <div className="flex-1 min-h-0 overflow-y-auto px-4 py-4 flex flex-col gap-3">
                {thread.map((message) => {
                  const own = message.author_external_id === accountByID.get(message.social_account_id)?.external_account_id;
                  return (
                    <div
                      key={message.id}
                      className={
                        "max-w-[78%] rounded border px-3 py-2 " +
                        (own
                          ? "ml-auto border-accent/40 bg-accent/10"
                          : "border-border bg-bg-card/50")
                      }
                    >
                      <div className="flex items-center gap-2 text-xs text-text-dim mb-1">
                        <span>{inboxAuthor(message)}</span>
                        <span>·</span>
                        <span>{formatInboxDate(message.occurred_at)}</span>
                      </div>
                      <div className="text-text text-sm whitespace-pre-wrap break-words">
                        {message.body || (parseInboxAttachments(message.media).length === 0 ? "(no text)" : "")}
                      </div>
                      <InboxAttachmentList attachments={parseInboxAttachments(message.media)} />
                    </div>
                  );
                })}
              </div>

              <form
                className="border-t border-border p-4 flex flex-col gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (replyBody.trim() || replyAttachments.length > 0) {
                    runAction("reply", activeItem.id, {
                      body: replyBody.trim(),
                      mode: canPrivateReply ? replyMode : "auto",
                      media_storage_ids: replyAttachments.map((attachment) => attachment.id),
                      media_project_id: projectId,
                    });
                  }
                }}
              >
                <textarea
                  value={replyBody}
                  onChange={(e) => setReplyBody(e.target.value)}
                  placeholder={`Reply to ${activeItem.kind === "dm" ? "message" : activeItem.kind}...`}
                  className="min-h-24 bg-bg-input border border-border rounded px-3 py-2 text-sm text-text resize-y"
                />
                <div className="flex items-center gap-2">
                  {canPrivateReply && (
                    <select
                      value={replyMode}
                      onChange={(e) => {
                        const mode = e.target.value as "public" | "private";
                        setReplyMode(mode);
                        if (mode === "private") setReplyAttachments([]);
                      }}
                      className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
                    >
                      <option value="public">Public comment</option>
                      <option value="private">Private message</option>
                    </select>
                  )}
                  {canAttach && (
                    <button
                      type="button"
                      onClick={() => setAttachmentPickerOpen(true)}
                      disabled={!!busy || replyAttachments.length >= attachmentCaps.max}
                      className="w-9 h-9 grid place-items-center border border-border rounded text-text-dim hover:text-text disabled:opacity-50"
                      title={`Attach ${attachmentCaps.types.join(", ")}`}
                      aria-label="Attach media"
                    >
                      <Paperclip size={17} />
                    </button>
                  )}
                  {(activeItem.kind === "comment" || activeItem.kind === "mention") && (
                    <>
                      <button
                        type="button"
                        onClick={() => runAction(activeItem.status === "hidden" ? "unhide" : "hide")}
                        disabled={!!busy}
                        className="px-3 py-1.5 text-sm border border-border rounded text-text-dim hover:text-text disabled:opacity-50"
                      >
                        {activeItem.status === "hidden" ? "Unhide" : "Hide"}
                      </button>
                      <button
                        type="button"
                        onClick={() => runAction("delete")}
                        disabled={!!busy}
                        className="px-3 py-1.5 text-sm border border-error/50 rounded text-error disabled:opacity-50"
                      >
                        Delete
                      </button>
                    </>
                  )}
                  <button
                    type="submit"
                    disabled={(!replyBody.trim() && replyAttachments.length === 0) || !!busy}
                    className="ml-auto px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
                  >
                    {busy === "reply" ? "Sending..." : "Reply"}
                  </button>
                </div>
                {replyAttachments.length > 0 && (
                  <div className="flex flex-wrap gap-2">
                    {replyAttachments.map((attachment) => (
                      <ReplyAttachmentPreview
                        key={attachment.id}
                        attachment={attachment}
                        projectId={projectId}
                        onRemove={() => setReplyAttachments((current) => current.filter((item) => item.id !== attachment.id))}
                      />
                    ))}
                  </div>
                )}
              </form>
              {attachmentPickerOpen && (
                <StoragePickerDialog
                  excludeIds={new Set(replyAttachments.map((attachment) => attachment.id))}
                  projectId={projectId}
                  onClose={() => setAttachmentPickerOpen(false)}
                  onPick={(files) => {
                    setReplyAttachments((current) =>
                      [...current, ...files.filter((file) => !current.some((item) => item.id === file.id))]
                        .slice(0, attachmentCaps.max)
                    );
                    setAttachmentPickerOpen(false);
                  }}
                  initialKind={attachmentCaps.types.length === 1 ? attachmentCaps.types[0] : "all"}
                  allowedKinds={attachmentCaps.types}
                  single={attachmentCaps.max === 1}
                  title="Attach to reply"
                />
              )}
            </>
          )}
        </section>
      </div>
    </div>
  );
}

function inboxAttachmentSummary(media: unknown): string {
  const attachments = parseInboxAttachments(media);
  if (attachments.length === 0) return "";
  const labels = attachments.map((attachment) =>
    attachment.kind === "audio" ? "Audio" :
      attachment.kind === "image" ? "Image" :
        attachment.kind === "video" ? "Video" : "File"
  );
  return labels.join(", ");
}

function InboxAttachmentList({ attachments }: { attachments: InboxAttachment[] }) {
  if (attachments.length === 0) return null;
  return (
    <div className="mt-2 flex flex-col gap-2">
      {attachments.map((attachment, index) => {
        const key = `${attachment.url}-${index}`;
        if (attachment.kind === "image") {
          return (
            <a key={key} href={attachment.url} target="_blank" rel="noopener" className="block">
              <img
                src={attachment.thumbnail_url || attachment.url}
                alt={attachment.name || "Attached image"}
                loading="lazy"
                className="block max-w-full max-h-72 rounded border border-border object-contain bg-bg-input"
              />
            </a>
          );
        }
        if (attachment.kind === "video") {
          return (
            <video
              key={key}
              src={attachment.url}
              poster={attachment.thumbnail_url}
              controls
              preload="metadata"
              className="block max-w-full max-h-72 rounded border border-border bg-black"
            />
          );
        }
        if (attachment.kind === "audio") {
          return (
            <div key={key} className="flex items-center gap-2 min-w-0">
              <FileAudio size={18} className="text-text-dim flex-shrink-0" />
              <audio src={attachment.url} controls preload="metadata" className="max-w-full h-10" />
            </div>
          );
        }
        return (
          <a
            key={key}
            href={attachment.url}
            target="_blank"
            rel="noopener"
            className="inline-flex items-center gap-2 text-sm text-text hover:text-accent"
          >
            <File size={17} />
            <span className="truncate">{attachment.name || "Open attachment"}</span>
          </a>
        );
      })}
    </div>
  );
}

function ReplyAttachmentPreview({
  attachment, projectId, onRemove,
}: {
  attachment: StorageFile;
  projectId?: string | null;
  onRemove: () => void;
}) {
  const kind = storageAttachmentKind(attachment.content_type || "");
  const src = storageURL(`/files/${attachment.id}/content`, projectId);
  return (
    <div className="relative w-40 h-20 rounded border border-border bg-bg-input overflow-hidden">
      {kind === "image" && <img src={src} alt={attachment.name} className="w-full h-full object-cover" />}
      {kind === "video" && <video src={src} muted preload="metadata" className="w-full h-full object-cover" />}
      {(kind === "audio" || kind === "file") && (
        <div className="h-full px-3 flex items-center gap-2 min-w-0">
          {kind === "audio" ? <FileAudio size={20} /> : <File size={20} />}
          <span className="text-xs text-text truncate">{attachment.name}</span>
        </div>
      )}
      <button
        type="button"
        onClick={onRemove}
        title="Remove attachment"
        aria-label="Remove attachment"
        className="absolute top-1 right-1 w-6 h-6 rounded bg-black/70 text-white grid place-items-center"
      >
        <X size={14} />
      </button>
    </div>
  );
}

function InboxAvatar({
  item, account, large = false,
}: {
  item: InboxItem;
  account?: SocialAccount;
  large?: boolean;
}) {
  const src = item.author_avatar_url || account?.avatar_url || "";
  const size = large ? "w-10 h-10" : "w-9 h-9";
  if (src) {
    return <img src={src} alt="" className={`${size} rounded-full object-cover border border-border flex-shrink-0`} />;
  }
  const label = inboxAuthor(item).slice(0, 1).toUpperCase() || "?";
  return (
    <div className={`${size} rounded-full bg-bg-input border border-border grid place-items-center text-text-dim flex-shrink-0`}>
      {label}
    </div>
  );
}

function inboxAuthor(item: InboxItem): string {
  const raw = item.author_name || item.author_handle || "";
  if (raw.trim()) return raw.trim();
  if (item.platform === "facebook" && item.kind === "comment") return "Facebook commenter";
  return "Author unavailable";
}

function formatInboxDate(raw: string): string {
  if (!raw) return "";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return raw;
  const now = Date.now();
  const diff = now - d.getTime();
  if (diff >= 0 && diff < 24 * 60 * 60 * 1000) {
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  return d.toLocaleDateString([], { month: "short", day: "numeric" });
}

interface ProviderImportResponse {
  status: string;
  provider: string;
  imported: number;
  skipped_existing: number;
  failed: number;
  accounts: Array<{
    id?: number;
    provider_account_id: string;
    provider_profile_id?: string;
    provider_profile?: string;
    platform: string;
    display_name: string;
    avatar_url?: string;
    status: string;
    reason?: string;
  }>;
  error?: string;
}

interface ProviderProfileResponse {
  status: string;
  provider: string;
  profiles: Array<{
    provider_profile_id: string;
    name: string;
    description?: string;
    color?: string;
    is_default?: boolean;
    account_usernames?: string[];
  }>;
  error?: string;
}

function ProviderAvatar({ src, label }: { src?: string; label: string }) {
  const [failed, setFailed] = useState(false);
  const initial = (label.trim()[0] || "?").toUpperCase();
  if (src && !failed) {
    return (
      <img
        src={src}
        alt=""
        onError={() => setFailed(true)}
        className="w-8 h-8 rounded-full object-cover border border-border flex-shrink-0"
      />
    );
  }
  return (
    <div className="w-8 h-8 rounded-full bg-bg-input border border-border flex-shrink-0 grid place-items-center text-xs text-text-dim font-medium">
      {initial}
    </div>
  );
}

function ProviderImportDialog({
  activeProfile, projectId, onClose, onImported, setStatus,
}: {
  activeProfile: Profile | null;
  projectId?: string | null;
  onClose: () => void;
  onImported: () => void;
  setStatus: (s: string) => void;
}) {
  const [providerProfileId, setProviderProfileId] = useState("");
  const [platforms, setPlatforms] = useState("");
  const [dryRun, setDryRun] = useState(true);
  const [busy, setBusy] = useState(false);
  const [profilesBusy, setProfilesBusy] = useState(false);
  const [providerProfiles, setProviderProfiles] = useState<ProviderProfileResponse["profiles"]>([]);
  const [result, setResult] = useState<ProviderImportResponse | null>(null);
  const [selectedAccountIds, setSelectedAccountIds] = useState<Set<string>>(new Set());
  const selectedCount = result?.accounts.filter((a) => selectedAccountIds.has(a.provider_account_id)).length || 0;
  const selectableCount = result?.accounts.filter((a) => a.status !== "failed").length || 0;
  const setAllSelected = (accounts: ProviderImportResponse["accounts"]) => {
    setSelectedAccountIds(new Set(accounts.filter((a) => a.status !== "failed").map((a) => a.provider_account_id)));
  };
  const toggleSelected = (id: string) => {
    setSelectedAccountIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const run = async (preview: boolean) => {
    setBusy(true);
    setDryRun(preview);
    setStatus(preview ? "Previewing provider accounts…" : "Importing provider accounts…");
    try {
      const body: Record<string, unknown> = {
        provider: "zernio",
        dry_run: preview,
      };
      if (activeProfile?.id) body.profile_id = activeProfile.id;
      if (providerProfileId.trim()) body.provider_profile_id = providerProfileId.trim();
      const platformList = platforms.split(",").map((s) => s.trim().toLowerCase()).filter(Boolean);
      if (platformList.length) body.platforms = platformList;
      if (!preview) body.provider_account_ids = Array.from(selectedAccountIds);
      const res = await fetch(appURL("/provider-accounts/import", projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as ProviderImportResponse;
      setResult(data);
      if (data.status !== "ok") {
        setStatus("Provider import failed: " + (data.error || "unknown error"));
        return;
      }
      if (preview) {
        setAllSelected(data.accounts);
        setStatus(`Provider preview: ${data.accounts.length} account${data.accounts.length !== 1 ? "s" : ""}.`);
      } else {
        setStatus(`Imported ${data.imported}; skipped ${data.skipped_existing}; failed ${data.failed}.`);
        onImported();
      }
    } catch (e) {
      setStatus("Provider import failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      setProfilesBusy(true);
      try {
        const res = await fetch(appURL("/provider-profiles", projectId), { credentials: "same-origin" });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json() as ProviderProfileResponse;
        if (cancelled) return;
        if (data.status === "ok") {
          setProviderProfiles(data.profiles || []);
        } else {
          setStatus("Provider profiles failed: " + (data.error || "unknown error"));
        }
      } catch (e) {
        if (!cancelled) setStatus("Provider profiles failed: " + (e as Error).message);
      } finally {
        if (!cancelled) setProfilesBusy(false);
      }
    };
    load();
    run(true);
    return () => { cancelled = true; };
  }, []);
  return (
    <div className="fixed inset-0 bg-black/60 grid place-items-center z-50" onClick={onClose}>
      <div
        className="bg-bg-card border border-border rounded p-4 w-[640px] max-w-[92vw] max-h-[86vh] flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-3">
          <div>
            <div className="text-text font-medium">Import provider accounts</div>
            <div className="text-text-dim text-xs">Zernio accounts become normal Social accounts in this profile.</div>
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 mb-3">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-text-dim uppercase">Zernio profile</span>
            <select
              value={providerProfileId}
              onChange={(e) => setProviderProfileId(e.target.value)}
              className="bg-bg-input border border-border rounded px-3 py-2 text-sm text-text"
            >
              <option value="">{profilesBusy ? "Loading profiles..." : "All profiles"}</option>
              {providerProfiles.map((profile) => (
                <option key={profile.provider_profile_id} value={profile.provider_profile_id}>
                  {profile.name}{profile.is_default ? " (default)" : ""}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-text-dim uppercase">Platforms</span>
            <input
              value={platforms}
              onChange={(e) => setPlatforms(e.target.value)}
              placeholder="optional, e.g. linkedin,twitter"
              className="bg-bg-input border border-border rounded px-3 py-2 text-sm text-text"
            />
          </label>
        </div>
        <div className="border border-border rounded overflow-hidden flex-1 min-h-[180px] overflow-y-auto">
          {!result && busy ? (
            <div className="p-6 text-sm text-text-muted text-center">
              Loading Zernio accounts...
            </div>
          ) : !result ? (
            <div className="p-6 text-sm text-text-muted text-center">
              No provider preview loaded yet.
            </div>
          ) : result.accounts.length === 0 ? (
            <div className="p-6 text-sm text-text-muted text-center">
              No provider accounts matched.
            </div>
          ) : (
            result.accounts.map((a) => (
              <label key={`${a.provider_account_id}-${a.platform}`} className="px-3 py-2 border-b border-border last:border-b-0 flex items-center justify-between gap-3 hover:bg-bg/40 cursor-pointer">
                <div className="min-w-0 flex items-center gap-3">
                  <input
                    type="checkbox"
                    checked={selectedAccountIds.has(a.provider_account_id)}
                    disabled={a.status === "failed"}
                    onChange={() => toggleSelected(a.provider_account_id)}
                    className="w-4 h-4 accent-orange-500 flex-shrink-0"
                  />
                  <ProviderAvatar src={a.avatar_url} label={a.display_name || a.provider_account_id || a.platform} />
                  <div className="min-w-0">
                    <div className="text-sm text-text truncate">{a.display_name || a.provider_account_id}</div>
                    <div className="text-xs text-text-dim truncate">
                      {a.platform}
                      {a.provider_profile && ` · ${a.provider_profile}`}
                      {` · ${a.provider_account_id}`}
                    </div>
                  </div>
                </div>
                <div className={
                  "text-xs " +
                  (a.status === "ok" ? "text-success" : a.status === "failed" ? "text-error" : "text-text-dim")
                }>
                  {a.status}
                </div>
              </label>
            ))
          )}
        </div>
        <div className="flex items-center justify-between gap-3 mt-4">
          <div className="text-xs text-text-dim">
            {activeProfile ? `Target profile: ${activeProfile.name}` : "Target profile: default"}
            {dryRun ? " · preview mode" : ""}
            {result && ` · ${selectedCount}/${selectableCount} selected`}
          </div>
          <div className="flex items-center gap-2">
            {result && selectableCount > 0 && (
              <button
                onClick={() => {
                  if (selectedCount === selectableCount) setSelectedAccountIds(new Set());
                  else setAllSelected(result.accounts);
                }}
                disabled={busy}
                className="px-3 py-1.5 text-sm text-text-muted hover:text-text disabled:opacity-50"
              >
                {selectedCount === selectableCount ? "Select none" : "Select all"}
              </button>
            )}
            <button onClick={onClose} disabled={busy} className="px-3 py-1.5 text-sm text-text-muted disabled:opacity-50">
              Close
            </button>
            <button
              onClick={() => run(true)}
              disabled={busy}
              className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg disabled:opacity-50"
            >
              {busy && dryRun ? "Previewing…" : "Preview"}
            </button>
            <button
              onClick={() => run(false)}
              disabled={busy || selectedCount === 0}
              className="px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {busy && !dryRun ? "Importing…" : "Import"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function AccountCard({
  account, projectId, onChange, setStatus,
}: { account: SocialAccount; projectId?: string | null; onChange: () => void; setStatus: (s: string) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [hardDelete, setHardDelete] = useState(false);
  const [importing, setImporting] = useState(false);
  const [checking, setChecking] = useState(false);
  const doRemove = async (hard = false) => {
    try {
      const query = hard ? "?hard_delete=true&delete_posts=true" : "";
      const res = await fetch(appURL(`/accounts/${account.id}${query}`, projectId), { method: "DELETE", credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      setStatus(hard ? "Local account history deleted." : "Disconnected.");
      onChange();
    } catch (e) {
      setStatus("Disconnect failed: " + (e as Error).message);
    }
  };
  const provider = account.provider_slug || "native";
  const importSupported = provider === "zernio" || IMPORTABLE_PLATFORMS.has(account.platform);
  const doImport = async () => {
    setImporting(true);
    setStatus(`Importing recent posts from ${account.display_name}…`);
    try {
      const res = await fetch(appURL(`/accounts/${account.id}/import?limit=25`, projectId), {
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
      const res = await fetch(appURL(`/accounts/${account.id}/check`, projectId), {
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
            {provider !== "native" && (
              <span className="text-accent" title={`Backed by ${provider}`}>
                {provider}
              </span>
            )}
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
          onClick={() => { setHardDelete(false); setConfirming(true); }}
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
            <div className="flex flex-col gap-3">
              <span>
              Historical posts and inbox items stay in Social. The underlying OAuth connection is
              released when no other account uses it. Scheduled posts targeting this account will
              fail locally; reconnect and retry them if they should still publish.
              </span>
              <label className="flex items-start gap-2 text-red cursor-pointer">
                <input
                  type="checkbox"
                  checked={hardDelete}
                  onChange={(e) => setHardDelete(e.target.checked)}
                  className="mt-0.5 accent-red"
                />
                <span>Permanently delete this account and its local post and inbox history. Upstream social posts are not deleted.</span>
              </label>
            </div>
          }
          confirmLabel={hardDelete ? "Delete local history" : "Disconnect"}
          onClose={() => setConfirming(false)}
          onConfirm={async () => {
            await doRemove(hardDelete);
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
  platforms, activeProfile, projectId, onClose, setStatus, onReuseExisting,
}: {
  platforms: PlatformInfo[];
  activeProfile: Profile | null;
  projectId?: string | null;
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
  const zernioConnected = platforms.some((p) => !!p.zernio_available);
  const visiblePlatforms = zernioConnected ? platforms : platforms.filter((p) => !p.provider_only);

  const start = (p: PlatformInfo, provider: "native" | "zernio" = "native") => {
    const canStart = provider === "zernio" ? !!p.zernio_available : p.available;
    if (!canStart) return;
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
    const busyKey = provider === "zernio" ? `${p.platform}:zernio` : p.platform;
    setBusy(busyKey);
    setStatus("Starting OAuth for " + p.display_name + (provider === "zernio" ? " via Zernio…" : "…"));
    (async () => {
      const fail = (msg: string) => {
        setErr(msg);
        setStatus(msg);
        setBusy(null);
        try { popup.close(); } catch {}
      };
      try {
        const payload: Record<string, any> = provider === "zernio"
          ? { platform: p.platform, provider: "zernio" }
          : { platform: p.platform };
        if (activeProfile?.id) payload.profile_id = activeProfile.id;
        const res = await fetch(appURL("/accounts/start", projectId), {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
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
          {visiblePlatforms.map((p) => {
            const directDisabled = !p.available || !!busy;
            const zernioDisabled = !p.zernio_available || !!busy;
            return (
              <div
                key={p.platform}
                className="px-3 py-2 border border-border rounded text-sm flex items-center justify-between gap-3"
              >
                <div className="min-w-0">
                  <div className="text-text truncate">{p.display_name}</div>
                  <div className="text-text-dim text-xs">
                    {p.provider_only ? "Provider-backed" : p.requires_picker ? `${pickerKind(p.platform).singular} picker after auth` : "Single account"}
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {!p.provider_only && (
                    <button
                      onClick={() => start(p)}
                      disabled={directDisabled}
                      title={
                        !p.available
                          ? `No ${p.display_name} integration installed. Add one in Settings → Integrations to enable this.`
                          : undefined
                      }
                      className="px-2.5 py-1 border border-border rounded text-xs text-text hover:border-accent disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:border-border"
                    >
                      Direct
                    </button>
                  )}
                  {zernioConnected && (
                    <button
                      onClick={() => start(p, "zernio")}
                      disabled={zernioDisabled}
                      title={!p.zernio_available ? "Connect Zernio in Settings → Integrations to enable provider-backed accounts." : undefined}
                      className="px-2.5 py-1 border border-border rounded text-xs text-text hover:border-accent disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:border-border"
                    >
                      Zernio
                    </button>
                  )}
                </div>
              </div>
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
    fetch(appURL(`/accounts/${pendingId}/pages`, projectId), { credentials: "same-origin" })
      .then(async (r) => {
        if (!r.ok) throw new Error(await r.text());
        return r.json();
      })
      .then((d) => {
        const pickerError = mcpEnvelopeError(d);
        if (pickerError) {
          setStatus(pickerError);
          setLoading(false);
          return;
        }
        setPages(d.pages || []);
        setPlatform(d.platform || "");
        setLoading(false);
        if (!d.requires_picker) {
          onClose();
        }
      })
      .catch((e) => {
        setStatus("Account picker failed: " + (e as Error).message);
        setLoading(false);
      });
  }, [pendingId, onClose, projectId, setStatus]);

  const pick = async (page: PageEntry) => {
    setBusyID(page.id);
    try {
      const res = await fetch(appURL("/accounts/finalize", projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          pending_account_id: pendingId,
          page_id: page.id,
          name: page.name,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const finalizeError = finalizedAccountError(data);
      if (finalizeError) throw new Error(finalizeError);
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

  if (field.type === "boolean") {
    return (
      <label className="flex items-start gap-2 border border-border rounded px-2 py-2 bg-bg-input/50">
        <input
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
          className="mt-0.5 w-4 h-4 accent-orange-500"
        />
        <span className="min-w-0">
          <span className="block text-xs uppercase tracking-wide text-text-dim">{field.label}</span>
          {field.help && <span className="block text-text-dim text-[10px] leading-snug">{field.help}</span>}
        </span>
      </label>
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
          projectId,
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
          previewURL: storageURL(`/files/${f.id}/content`, projectId),
        }));
      return [...prev, ...adds];
    });
    setStatus(`Added ${picked.length} file${picked.length !== 1 ? "s" : ""} from storage.`);
  };

  const submit = async (mode: "draft" | "schedule" | "publish") => {
    const hasContent = !!body.trim() || media.length > 0;
    if (!hasContent) return;
    if (mode !== "draft" && selected.size === 0) return;
    if (mode === "schedule" && !scheduleAt) return;
    setBusy(true);
    setStatus(mode === "draft" ? "Saving draft…" : mode === "schedule" ? "Scheduling…" : "Publishing…");
    try {
      // Choose between the simple multicast shape (social_account_ids[])
      // and the per-target shape (targets[]). Use targets[] only when
      // at least one selected account has non-empty options — keeps the
      // common case as terse as it was before.
      const selectedIds = Array.from(selected);
      const anyCustomized = selectedIds.some((id) => isCustomized(id));
      const payload: Record<string, any> = {
        mode,
        body,
        schedule_at: mode === "schedule" ? scheduleAt : undefined,
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
      const res = await fetch(appURL("/posts", projectId), {
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
      setStatus(mode === "draft" ? "Draft saved." : mode === "schedule" ? "Post scheduled." : "Post published.");
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
          <div className="ml-auto flex flex-wrap items-center justify-end gap-2">
            <button onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
            <button
              onClick={() => submit("draft")}
              disabled={(!body.trim() && media.length === 0) || busy}
              className="px-3 py-2 text-sm border border-accent text-accent rounded font-medium disabled:opacity-50"
            >
              {busy ? "…" : "Save draft"}
            </button>
            <button
              onClick={() => submit("schedule")}
              disabled={(!body.trim() && media.length === 0) || selected.size === 0 || !scheduleAt || busy}
              className="px-3 py-2 text-sm border border-border text-text rounded font-medium disabled:opacity-50"
            >
              Schedule
            </button>
            <button
              onClick={() => submit("publish")}
              disabled={(!body.trim() && media.length === 0) || selected.size === 0 || busy}
              className="px-4 py-2 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              Publish now
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
}

interface MediaDerivation {
  kind: "thumbnail" | "waveform" | "cover" | "keyframe" | string;
  storage_file_id: string;
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

type PickerTab = "media" | "storage";
type PickerKind = "all" | InboxAttachmentKind;
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
  if (row.has_audio) return "audio/*";
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
  excludeIds, projectId, onClose, onPick, initialKind = "all", lockedKind,
  allowedKinds = ["image", "video"], single = false, title = "Pick media",
}: {
  excludeIds: Set<number>;
  projectId?: string | null;
  onClose: () => void;
  onPick: (files: StorageFile[]) => void;
  initialKind?: PickerKind;
  lockedKind?: PickerKind;
  allowedKinds?: InboxAttachmentKind[];
  single?: boolean;
  title?: string;
}) {
  const [tab, setTab] = useState<PickerTab>(initialKind === "file" ? "storage" : "media");
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
        else if (kind === "audio") params.set("has_audio", "true");
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
          const rowKind: InboxAttachmentKind = row.is_image
            ? "image"
            : row.has_video ? "video" : row.has_audio ? "audio" : "file";
          return allowedKinds.includes(rowKind) && rowKind !== "file" &&
            (kind === "all" || rowKind === kind);
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
  }, [q, kind, rating, length, aspect, folder, recursive, projectId, allowedKinds.join(",")]);

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
        else if (kind === "audio") params.set("content_type", "audio/");
        appendStorageScope(params, projectId);
        const res = await fetch(`${STORAGE_API}/files?${params.toString()}`, {
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json() as { files: StorageFile[] };
        if (cancelled) return;
        const usable = (data.files || []).filter(
          (f) => !isHiddenStorageFile(f) && (
            allowedKinds.includes(storageAttachmentKind(f.content_type || "")) &&
            (kind === "all" || storageAttachmentKind(f.content_type || "") === kind)
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
  }, [q, kind, projectId, tab, allowedKinds.join(",")]);

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
              {lockedKind === "image" ? "Images" :
                lockedKind === "video" ? "Videos" :
                  lockedKind === "audio" ? "Audio" :
                    lockedKind === "file" ? "Files" : "All media"}
            </div>
          ) : (
            <select
              value={kind}
              onChange={(e) => {
                const next = e.target.value as PickerKind;
                setKind(next);
                if (next === "file") setTab("storage");
              }}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            >
              <option value="all">All media</option>
              {allowedKinds.includes("image") && <option value="image">Images</option>}
              {allowedKinds.includes("video") && <option value="video">Videos</option>}
              {allowedKinds.includes("audio") && <option value="audio">Audio</option>}
              {allowedKinds.includes("file") && <option value="file">Files</option>}
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
                const isAudio = !!row.has_audio && !row.has_video && !row.is_image;
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
                    {isAudio ? (
                      <div className="w-full h-full grid place-items-center text-text-dim">
                        <FileAudio size={32} />
                      </div>
                    ) : isVideo && !hasThumb ? (
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
                const isImage = (f.content_type || "").startsWith("image/");
                const isAudio = (f.content_type || "").startsWith("audio/");
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
                    ) : isImage ? (
                      <img src={src} alt={f.name} className="w-full h-full object-cover" loading="lazy" />
                    ) : (
                      <div className="w-full h-full grid place-items-center text-text-dim">
                        {isAudio ? <FileAudio size={32} /> : <File size={32} />}
                      </div>
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

function PostsView({
  posts, accounts, activeProfileId, onChange, setStatus, projectId,
}: {
  posts: Post[];
  accounts: SocialAccount[];
  activeProfileId: number | null;
  onChange: () => void | Promise<void>;
  setStatus: (s: string) => void;
  projectId?: string | null;
}) {
  const preferenceKey = "social.posts.view";
  const [view, setView] = useState<PostViewMode>(() => {
    try {
      return localStorage.getItem(preferenceKey) === "calendar" ? "calendar" : "list";
    } catch {
      return "list";
    }
  });
  const [calendarScale, setCalendarScale] = useState<CalendarScale>("month");
  const [calendarCursor, setCalendarCursor] = useState(() => new Date());
  const [calendarPosts, setCalendarPosts] = useState<Post[]>([]);
  const [calendarLoading, setCalendarLoading] = useState(false);
  const calendarRequestRef = useRef(0);
  const [statusFilter, setStatusFilter] = useState("all");
  const [accountFilter, setAccountFilter] = useState("all");
  const [selectedPost, setSelectedPost] = useState<Post | null>(null);
  const [menuPostID, setMenuPostID] = useState<number | null>(null);
  const [dayDialog, setDayDialog] = useState<{ date: Date; posts: Post[] } | null>(null);
  const [rescheduleFor, setRescheduleFor] = useState<Post | null>(null);
  const [deleteFor, setDeleteFor] = useState<Post | null>(null);
  const [editFor, setEditFor] = useState<Post | null>(null);

  useEffect(() => {
    try { localStorage.setItem(preferenceKey, view); } catch {}
  }, [view]);

  useEffect(() => {
    setSelectedPost(null);
    setMenuPostID(null);
    setDayDialog(null);
  }, [activeProfileId]);

  useEffect(() => {
    if (accountFilter !== "all" && !accounts.some((account) => String(account.id) === accountFilter)) {
      setAccountFilter("all");
    }
  }, [accountFilter, accounts]);

  const range = calendarWindow(calendarCursor, calendarScale);
  const rangeStart = range.start.toISOString();
  const rangeEnd = range.end.toISOString();
  const loadCalendarPosts = useCallback(async () => {
    const requestID = ++calendarRequestRef.current;
    setCalendarLoading(true);
    try {
      const params = new URLSearchParams({ from: rangeStart, to: rangeEnd, limit: "1000" });
      if (activeProfileId) params.set("profile_id", String(activeProfileId));
      const res = await fetch(appURL(`/posts?${params.toString()}`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const envelopeError = mcpEnvelopeError(data);
      if (envelopeError) throw new Error(envelopeError);
      if (calendarRequestRef.current === requestID) setCalendarPosts(data.posts || []);
    } catch (e) {
      if (calendarRequestRef.current === requestID) {
        setCalendarPosts([]);
        setStatus("Load calendar: " + (e as Error).message);
      }
    } finally {
      if (calendarRequestRef.current === requestID) setCalendarLoading(false);
    }
  }, [activeProfileId, projectId, rangeEnd, rangeStart, setStatus]);

  const postsRevision = posts.map((post) => `${post.id}:${post.status}:${post.schedule_at}:${post.published_at}`).join("|");
  useEffect(() => {
    if (view === "calendar") loadCalendarPosts();
  }, [loadCalendarPosts, postsRevision, view]);

  const refreshAll = useCallback(async () => {
    await Promise.resolve(onChange());
    if (view === "calendar") await loadCalendarPosts();
  }, [loadCalendarPosts, onChange, view]);

  const sourcePosts = view === "calendar" ? calendarPosts : posts;
  const filteredPosts = filterCalendarPosts(sourcePosts, statusFilter, accountFilter);
  const sortedListPosts = sortPostList(filteredPosts);

  useEffect(() => {
    if (!selectedPost) return;
    const current = sourcePosts.find((post) => post.id === selectedPost.id);
    if (current) setSelectedPost(current);
  }, [sourcePosts, selectedPost?.id]);

  useEffect(() => {
    if (!selectedPost?.id) return;
    let alive = true;
    fetch(appURL(`/posts/${selectedPost.id}`, projectId), { credentials: "same-origin" })
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then((data) => { if (alive && data.post) setSelectedPost(data.post); })
      .catch(() => {});
    return () => { alive = false; };
  }, [projectId, selectedPost?.id, postsRevision]);

  const retry = async (postId: number) => {
    try {
      const res = await fetch(appURL(`/posts/${postId}/retry`, projectId), { method: "POST", credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      setStatus("Retry triggered.");
      await refreshAll();
    } catch (e) {
      setStatus("Retry failed: " + (e as Error).message);
    }
  };

  const workflow = async (post: Post, action: "submit" | "approve" | "reject" | "publish", extra: Record<string, any> = {}) => {
    try {
      const res = await fetch(appURL(`/posts/${post.id}/${action}`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expected_revision: post.revision, ...extra }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const envelopeError = mcpEnvelopeError(data);
      if (envelopeError) throw new Error(envelopeError);
      setStatus(
        action === "submit" ? "Draft submitted for review." :
        action === "approve" ? "Draft approved." :
        action === "reject" ? "Draft rejected." :
        extra.mode === "schedule" ? "Draft scheduled." : "Draft published."
      );
      setSelectedPost(data as Post);
      await refreshAll();
    } catch (e) {
      setStatus(`Draft ${action} failed: ${(e as Error).message}`);
    }
  };

  const executeDelete = async (post: Post) => {
    try {
      const res = await fetch(appURL(`/posts/${post.id}`, projectId), {
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
      setSelectedPost(null);
      await refreshAll();
    } catch (e) {
      setStatus("Delete failed: " + (e as Error).message);
    }
  };

  return (
    <div className="p-4 flex flex-col gap-3 min-h-full">
      <div className="flex flex-wrap items-center gap-2 border-b border-border pb-3">
        <div className="inline-flex border border-border rounded overflow-hidden" aria-label="Post view">
          <button
            type="button"
            onClick={() => setView("list")}
            className={`px-3 py-1.5 text-sm ${view === "list" ? "bg-bg-card text-text" : "text-text-muted hover:text-text"}`}
          >
            List
          </button>
          <button
            type="button"
            onClick={() => setView("calendar")}
            className={`px-3 py-1.5 text-sm border-l border-border ${view === "calendar" ? "bg-bg-card text-text" : "text-text-muted hover:text-text"}`}
          >
            Calendar
          </button>
        </div>
        <select
          value={statusFilter}
          onChange={(event) => setStatusFilter(event.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          aria-label="Filter posts by status"
        >
          <option value="all">All statuses</option>
          <option value="scheduled">Scheduled</option>
          <option value="published">Published</option>
          <option value="failed">Failed</option>
          <option value="partial">Partial</option>
          <option value="draft">Draft</option>
          <option value="in_review">In review</option>
          <option value="approved">Approved</option>
          <option value="rejected">Rejected</option>
        </select>
        <select
          value={accountFilter}
          onChange={(event) => setAccountFilter(event.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          style={{ minWidth: 170 }}
          aria-label="Filter posts by account"
        >
          <option value="all">All accounts</option>
          {accounts.map((account) => (
            <option key={account.id} value={account.id}>{account.display_name} · {account.platform}</option>
          ))}
        </select>
        <span className="text-text-dim text-xs ml-auto">
          {filteredPosts.length} post{filteredPosts.length !== 1 ? "s" : ""}
        </span>
      </div>

      {view === "list" ? (
        sortedListPosts.length === 0 ? (
          <PostsEmptyState filtered={posts.length > 0} />
        ) : (
          <div className="flex flex-col gap-2">
            {sortedListPosts.map((post) => (
              <PostListRow
                key={post.id}
                post={post}
                projectId={projectId}
                menuOpen={menuPostID === post.id}
                onToggleMenu={() => setMenuPostID((current) => current === post.id ? null : post.id)}
                onOpen={() => setSelectedPost(post)}
                onRetry={() => retry(post.id)}
                onReschedule={() => { setMenuPostID(null); setRescheduleFor(post); }}
                onEdit={() => { setMenuPostID(null); setEditFor(post); }}
                onDelete={() => { setMenuPostID(null); setDeleteFor(post); }}
              />
            ))}
          </div>
        )
      ) : (
        <PostCalendarView
          posts={filteredPosts}
          loading={calendarLoading}
          cursor={calendarCursor}
          scale={calendarScale}
          projectId={projectId}
          onScaleChange={setCalendarScale}
          onCursorChange={setCalendarCursor}
          onOpen={setSelectedPost}
          onMore={(date, dayPosts) => setDayDialog({ date, posts: dayPosts })}
        />
      )}

      {selectedPost && (
        <PostDetailPanel
          post={selectedPost}
          projectId={projectId}
          onClose={() => setSelectedPost(null)}
          onRetry={() => retry(selectedPost.id)}
          onReschedule={() => setRescheduleFor(selectedPost)}
          onEdit={() => setEditFor(selectedPost)}
          onDelete={() => setDeleteFor(selectedPost)}
          onSubmit={() => workflow(selectedPost, "submit")}
          onApprove={() => workflow(selectedPost, "approve")}
          onReject={() => {
            const reason = window.prompt("Why is this draft being rejected?")?.trim();
            if (reason) workflow(selectedPost, "reject", { reason });
          }}
          onPublish={() => workflow(selectedPost, "publish", { mode: "publish" })}
          onSchedule={() => setRescheduleFor(selectedPost)}
        />
      )}
      {dayDialog && (
        <CalendarDayDialog
          date={dayDialog.date}
          posts={dayDialog.posts}
          projectId={projectId}
          onClose={() => setDayDialog(null)}
          onOpen={(post) => { setDayDialog(null); setSelectedPost(post); }}
        />
      )}
      {rescheduleFor && (
        <RescheduleDialog
          post={rescheduleFor}
          projectId={projectId}
          onClose={() => setRescheduleFor(null)}
          onChanged={() => { setRescheduleFor(null); refreshAll(); }}
          setStatus={setStatus}
        />
      )}
      {deleteFor && (
        <DeleteConfirmDialog
          post={deleteFor}
          onClose={() => setDeleteFor(null)}
          onConfirm={async () => {
            const post = deleteFor;
            setDeleteFor(null);
            await executeDelete(post);
          }}
        />
      )}
      {editFor && (
        <EditPostDialog
          post={editFor}
          projectId={projectId}
          onClose={() => setEditFor(null)}
          onSaved={() => { setEditFor(null); refreshAll(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function PostsEmptyState({ filtered }: { filtered: boolean }) {
  return (
    <div className="py-12 text-center text-text-muted text-sm">
      {filtered ? "No posts match these filters." : "No posts yet. Compose your first one."}
    </div>
  );
}

function PlatformBadge({ platform, size = 18 }: { platform: string; size?: number }) {
  const presentation = platformPresentation(platform);
  return (
    <span
      title={presentation.label}
      aria-label={presentation.label}
      style={{
        minWidth: size,
        height: size,
        padding: "0 4px",
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        border: `1px solid ${presentation.color}88`,
        borderRadius: 4,
        backgroundColor: `${presentation.color}1F`,
        color: presentation.color,
        fontSize: size <= 16 ? 8 : size <= 20 ? 9 : 11,
        fontWeight: 700,
        lineHeight: 1,
        flexShrink: 0,
      }}
    >
      {presentation.mark}
    </span>
  );
}

function PlatformBadges({ targets, size = 18, limit = 3 }: { targets: PostTarget[]; size?: number; limit?: number }) {
  const platforms = Array.from(new Set(targets.map((target) => target.platform).filter(Boolean)));
  return (
    <span className="inline-flex items-center gap-1 flex-shrink-0">
      {platforms.slice(0, limit).map((platform) => <PlatformBadge key={platform} platform={platform} size={size} />)}
      {platforms.length > limit && <span className="text-[10px] text-text-dim">+{platforms.length - limit}</span>}
    </span>
  );
}

function PostListRow({
  post, projectId, menuOpen, onToggleMenu, onOpen, onRetry, onReschedule, onEdit, onDelete,
}: {
  post: Post;
  projectId?: string | null;
  menuOpen: boolean;
  onToggleMenu: () => void;
  onOpen: () => void;
  onRetry: () => void;
  onReschedule: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const displayDate = postLifecycleDate(post);
  const targetLabel = post.targets.length > 0
    ? post.targets.map((target) => `${target.platform} · ${target.display_name}`).join("  ·  ")
    : "No targets";
  const editable = (post.status === "published" || post.status === "partial") &&
    post.targets.some((target) => isEditablePlatform(target.platform));
  return (
    <div
      className="relative border border-border rounded bg-bg-card/30 hover:bg-bg-card/50 transition-colors"
    >
      <div className="flex items-stretch gap-3 p-2.5" style={listPostRowStyle()}>
        <button
          type="button"
          onClick={onOpen}
          className="flex flex-1 min-w-0 items-stretch gap-3 text-left"
          style={{ overflow: "hidden" }}
        >
          <PostLeadMedia post={post} projectId={projectId} variant="list" />
          <div className="flex-1 min-w-0 py-1 flex flex-col">
            <div
              className="text-text text-sm whitespace-pre-line leading-5"
              style={{
                display: "-webkit-box",
                WebkitBoxOrient: "vertical",
                WebkitLineClamp: 3,
                overflow: "hidden",
              }}
            >
              {post.body || <span className="text-text-dim italic">No caption</span>}
            </div>
            <div className="mt-auto min-w-0 pt-2">
              <div className="flex items-center gap-2 min-w-0">
                <PlatformBadges targets={post.targets} />
                <div className="text-text-dim text-xs truncate">{targetLabel}</div>
              </div>
              <div className="text-text-muted text-xs mt-0.5">
                {displayDate ? displayDate.toLocaleString() : "No date"}
              </div>
            </div>
          </div>
        </button>
        <div className="flex-shrink-0 flex flex-col items-end gap-2 py-1" style={{ width: 104 }}>
          <StatusPill status={post.status} />
          {(post.status === "failed" || post.status === "partial") && (
            <button
              type="button"
              onClick={(event) => { event.stopPropagation(); onRetry(); }}
              className="text-xs text-accent hover:underline"
            >
              Retry
            </button>
          )}
          <div className="relative mt-auto">
            <button
              type="button"
              onClick={onToggleMenu}
              className="w-8 h-8 grid place-items-center text-text-muted hover:text-text border border-transparent hover:border-border rounded"
              aria-label="Post actions"
              title="Post actions"
            >
              ⋮
            </button>
            {menuOpen && (
              <div className="absolute right-0 bottom-9 z-20 bg-bg-card border border-border rounded shadow-lg py-1" style={{ minWidth: 150 }}>
                {post.status === "scheduled" && <PostMenuButton label="Reschedule" onClick={onReschedule} />}
                {editable && <PostMenuButton label="Edit" onClick={onEdit} />}
                <PostMenuButton label={post.status === "scheduled" ? "Cancel post" : "Delete"} danger onClick={onDelete} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function PostMenuButton({ label, danger = false, onClick }: { label: string; danger?: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`w-full text-left px-3 py-2 text-xs hover:bg-bg-input ${danger ? "text-red" : "text-text"}`}
    >
      {label}
    </button>
  );
}

function PostCalendarView({
  posts, loading, cursor, scale, projectId, onScaleChange, onCursorChange, onOpen, onMore,
}: {
  posts: Post[];
  loading: boolean;
  cursor: Date;
  scale: CalendarScale;
  projectId?: string | null;
  onScaleChange: (scale: CalendarScale) => void;
  onCursorChange: (date: Date) => void;
  onOpen: (post: Post) => void;
  onMore: (date: Date, posts: Post[]) => void;
}) {
  const window = calendarWindow(cursor, scale);
  const grouped = groupPostsByLocalDay(posts);
  const todayKey = localDateKey(new Date());
  const heading = scale === "month"
    ? cursor.toLocaleDateString(undefined, { month: "long", year: "numeric" })
    : `${window.days[0].toLocaleDateString(undefined, { month: "short", day: "numeric" })} – ${window.days[6].toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}`;
  const daysWithPosts = window.days.filter((day) => (grouped.get(localDateKey(day)) || []).length > 0);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => onCursorChange(moveCalendarCursor(cursor, scale, -1))}
          className="w-8 h-8 border border-border rounded text-text hover:bg-bg-card"
          aria-label="Previous period"
          title="Previous"
        >
          ‹
        </button>
        <button
          type="button"
          onClick={() => onCursorChange(new Date())}
          className="px-3 h-8 border border-border rounded text-xs text-text hover:bg-bg-card"
        >
          Today
        </button>
        <button
          type="button"
          onClick={() => onCursorChange(moveCalendarCursor(cursor, scale, 1))}
          className="w-8 h-8 border border-border rounded text-text hover:bg-bg-card"
          aria-label="Next period"
          title="Next"
        >
          ›
        </button>
        <h2 className="text-text font-medium ml-1">{heading}</h2>
        {loading && <span className="text-text-dim text-xs">Loading…</span>}
        <div className="inline-flex ml-auto border border-border rounded overflow-hidden">
          <button
            type="button"
            onClick={() => onScaleChange("month")}
            className={`px-2.5 py-1.5 text-xs ${scale === "month" ? "bg-bg-card text-text" : "text-text-muted"}`}
          >
            Month
          </button>
          <button
            type="button"
            onClick={() => onScaleChange("week")}
            className={`px-2.5 py-1.5 text-xs border-l border-border ${scale === "week" ? "bg-bg-card text-text" : "text-text-muted"}`}
          >
            Week
          </button>
        </div>
      </div>

      <div className="hidden md:block overflow-x-auto border border-border rounded">
        <div
          className="grid border-b border-border bg-bg-card/30"
          style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))", minWidth: 840 }}
        >
          {window.days.slice(0, 7).map((day) => (
            <div key={day.getDay()} className="px-2 py-1.5 text-center text-[11px] uppercase text-text-dim border-r border-border last:border-r-0">
              {day.toLocaleDateString(undefined, { weekday: "short" })}
            </div>
          ))}
        </div>
        <div className="grid" style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))", minWidth: 840 }}>
          {window.days.map((day, index) => {
            const key = localDateKey(day);
            const dayPosts = grouped.get(key) || [];
            const outsideMonth = scale === "month" && day.getMonth() !== cursor.getMonth();
            return (
              <div
                key={key}
                className={`min-h-[150px] p-1.5 border-b border-border ${index % 7 !== 6 ? "border-r" : ""} ${outsideMonth ? "bg-bg/40" : "bg-bg"}`}
              >
                <div className="flex items-center justify-between mb-1">
                  <span
                    className={`text-xs rounded ${key === todayKey ? "bg-accent text-bg font-bold" : outsideMonth ? "text-text-dim" : "text-text"}`}
                    style={stableSquareStyle(24)}
                  >
                    {day.getDate()}
                  </span>
                  {dayPosts.length > 0 && <span className="text-[10px] text-text-dim">{dayPosts.length}</span>}
                </div>
                <div className="flex flex-col gap-1">
                  {dayPosts.slice(0, 3).map((post) => (
                    <CalendarPostEvent key={post.id} post={post} projectId={projectId} onOpen={() => onOpen(post)} />
                  ))}
                  {dayPosts.length > 3 && (
                    <button
                      type="button"
                      onClick={() => onMore(day, dayPosts)}
                      className="text-left px-1 py-0.5 text-[10px] text-accent hover:underline"
                    >
                      +{dayPosts.length - 3} more
                    </button>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      </div>

      <div className="md:hidden flex flex-col gap-3">
        {daysWithPosts.length === 0 && !loading ? (
          <div className="py-10 text-center text-text-muted text-sm">No posts in this period.</div>
        ) : daysWithPosts.map((day) => {
          const dayPosts = grouped.get(localDateKey(day)) || [];
          return (
            <section key={localDateKey(day)} className="flex flex-col gap-1.5">
              <div className="text-xs text-text-dim uppercase">
                {day.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}
              </div>
              {dayPosts.map((post) => (
                <CalendarPostEvent key={post.id} post={post} projectId={projectId} onOpen={() => onOpen(post)} mobile />
              ))}
            </section>
          );
        })}
      </div>
    </div>
  );
}

function CalendarPostEvent({ post, projectId, onOpen, mobile = false }: {
  post: Post;
  projectId?: string | null;
  onOpen: () => void;
  mobile?: boolean;
}) {
  const date = postLifecycleDate(post);
  const platform = Array.from(new Set(post.targets.map((target) => target.platform))).join(", ");
  return (
    <button
      type="button"
      onClick={onOpen}
      className={`w-full text-left flex items-center gap-1.5 border border-border rounded bg-bg-card/60 hover:border-accent overflow-hidden ${mobile ? "p-2" : "p-1"}`}
    >
      <PostLeadMedia post={post} projectId={projectId} variant="calendar" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1 text-[10px]">
          <span className="text-text">{date?.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>
          <StatusPill status={post.status} />
        </div>
        <div className={`${mobile ? "text-xs" : "text-[10px]"} text-text truncate`}>{post.body || "No caption"}</div>
        {mobile && (
          <div className="flex items-center gap-1.5 min-w-0 mt-0.5">
            <PlatformBadges targets={post.targets} size={16} limit={2} />
            <div className="text-[10px] text-text-dim truncate">{platform || "No target"}</div>
          </div>
        )}
      </div>
    </button>
  );
}

function CalendarDayDialog({ date, posts, projectId, onClose, onOpen }: {
  date: Date;
  posts: Post[];
  projectId?: string | null;
  onClose: () => void;
  onOpen: (post: Post) => void;
}) {
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" onClick={onClose}>
      <div
        className="max-h-[80vh] bg-bg-card border border-border rounded-lg shadow-lg flex flex-col"
        style={{ width: "min(560px, 94vw)" }}
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div>
            <div className="text-text font-medium">{date.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" })}</div>
            <div className="text-text-dim text-xs">{posts.length} posts</div>
          </div>
          <button type="button" onClick={onClose} className="w-8 h-8 text-text-muted hover:text-text" aria-label="Close">×</button>
        </div>
        <div className="overflow-y-auto p-3 flex flex-col gap-2">
          {posts.map((post) => (
            <CalendarPostEvent key={post.id} post={post} projectId={projectId} onOpen={() => onOpen(post)} mobile />
          ))}
        </div>
      </div>
    </div>
  );
}

function PostLeadMedia({ post, projectId, variant }: {
  post: Post;
  projectId?: string | null;
  variant: "list" | "calendar";
}) {
  const storageID = post.media_storage_ids?.[0];
  const externalURL = !storageID ? post.external_media_urls?.[0] : "";
  const [meta, setMeta] = useState<{ mime: string; name: string } | null>(null);
  useEffect(() => {
    let alive = true;
    if (storageID) loadMediaMeta(storageID, projectId).then((next) => { if (alive) setMeta(next); });
    else setMeta(null);
    return () => { alive = false; };
  }, [projectId, storageID]);
  const source = storageID ? storageURL(`/files/${storageID}/content`, projectId) : externalURL || "";
  const total = (post.media_storage_ids?.length || 0) + (post.external_media_urls?.length || 0);
  const isVideo = !!storageID && !!meta?.mime.startsWith("video/");
  const primaryPlatform = post.targets[0]?.platform || "";
  const presentation = platformPresentation(primaryPlatform);
  return (
    <div
      className="relative overflow-hidden rounded border border-border bg-bg-input"
      style={variant === "list" ? listLeadMediaStyle() : stableSquareStyle(32)}
      aria-hidden="true"
    >
      {!source || (storageID && !meta) ? (
        <div
          className="text-text-dim text-[10px] uppercase"
          style={{
            width: "100%",
            height: "100%",
            display: "grid",
            placeItems: "center",
            backgroundColor: `${presentation.color}12`,
          }}
        >
          {source ? "..." : <PlatformBadge platform={primaryPlatform} size={variant === "list" ? 36 : 20} />}
        </div>
      ) : isVideo ? (
        <>
          <video
            src={source}
            preload="metadata"
            muted
            playsInline
            style={{ width: "100%", height: "100%", objectFit: "cover", display: "block" }}
          />
          <div
            className="absolute inset-0 bg-black/20"
            style={{ display: "grid", placeItems: "center" }}
          >
            <span className="text-white text-xs">▶</span>
          </div>
        </>
      ) : (
        <img
          src={source}
          alt=""
          loading="lazy"
          style={{ width: "100%", height: "100%", objectFit: "cover", display: "block" }}
        />
      )}
      {total > 1 && (
        <span className="absolute right-1 bottom-1 px-1 py-0.5 rounded bg-black/80 text-white text-[9px]">+{total - 1}</span>
      )}
    </div>
  );
}

function PostDetailPanel({
  post, projectId, onClose, onRetry, onReschedule, onEdit, onDelete,
  onSubmit, onApprove, onReject, onPublish, onSchedule,
}: {
  post: Post;
  projectId?: string | null;
  onClose: () => void;
  onRetry: () => void;
  onReschedule: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onSubmit: () => void;
  onApprove: () => void;
  onReject: () => void;
  onPublish: () => void;
  onSchedule: () => void;
}) {
  const editable = (post.status === "published" || post.status === "partial") &&
    post.targets.some((target) => isEditablePlatform(target.platform));
  return (
    <div className="fixed inset-0 z-40 bg-black/60" onClick={onClose}>
      <aside
        className="ml-auto h-full bg-bg border-l border-border shadow-lg flex flex-col"
        style={{ width: "min(620px, 100vw)" }}
        onClick={(event) => event.stopPropagation()}
        aria-label="Post details"
      >
        <div className="flex items-center gap-3 px-4 py-3 border-b border-border">
          <div className="min-w-0 flex-1">
            <div className="text-text font-medium">Post #{post.id}</div>
            <div className="text-text-dim text-xs">{postLifecycleDate(post)?.toLocaleString()}</div>
          </div>
          <StatusPill status={post.status} />
          <button type="button" onClick={onClose} className="w-8 h-8 text-text-muted hover:text-text" aria-label="Close">×</button>
        </div>
        <div className="flex-1 overflow-y-auto">
          {(post.media_storage_ids?.length > 0 || (post.external_media_urls?.length || 0) > 0) && (
            <section className="p-4 border-b border-border">
              <div className="flex flex-wrap gap-2">
                {post.media_storage_ids.map((id) => <MediaThumb key={id} fileId={id} projectId={projectId} />)}
                {(post.external_media_urls || []).map((url) => <ExternalMediaThumb key={url} url={url} />)}
              </div>
            </section>
          )}
          <section className="p-4 border-b border-border">
            <div className="text-xs uppercase text-text-dim mb-2">Caption</div>
            <div className="text-text text-sm whitespace-pre-wrap leading-6">{post.body || <span className="text-text-dim italic">No caption</span>}</div>
          </section>
          <section className="p-4 border-b border-border grid grid-cols-2 gap-3 text-xs">
            <div><div className="text-text-dim uppercase">Created</div><div className="text-text mt-1">{new Date(post.created_at).toLocaleString()}</div></div>
            <div><div className="text-text-dim uppercase">Scheduled</div><div className="text-text mt-1">{post.schedule_at ? new Date(post.schedule_at).toLocaleString() : "—"}</div></div>
            <div><div className="text-text-dim uppercase">Published</div><div className="text-text mt-1">{post.published_at ? new Date(post.published_at).toLocaleString() : "—"}</div></div>
            <div><div className="text-text-dim uppercase">Targets</div><div className="text-text mt-1">{post.targets.length}</div></div>
            <div><div className="text-text-dim uppercase">Revision</div><div className="text-text mt-1">{post.revision || 1}</div></div>
            <div><div className="text-text-dim uppercase">Approval</div><div className="text-text mt-1">{post.approval_status || "not requested"}</div></div>
          </section>
          {post.rejection_reason && (
            <section className="p-4 border-b border-border">
              <div className="text-xs uppercase text-text-dim mb-1">Rejection reason</div>
              <div className="text-error text-sm whitespace-pre-wrap">{post.rejection_reason}</div>
            </section>
          )}
          {(post.reviews?.length || 0) > 0 && (
            <section className="p-4 border-b border-border">
              <div className="text-xs uppercase text-text-dim mb-2">Review history</div>
              <div className="flex flex-col gap-2">
                {post.reviews!.map((review, index) => (
                  <div key={`${review.action}-${review.revision}-${index}`} className="text-xs text-text">
                    <span className="font-medium capitalize">{review.action}</span> revision {review.revision}
                    <span className="text-text-dim"> · {review.actor} · {new Date(review.created_at).toLocaleString()}</span>
                    {review.reason && <div className="text-text-muted mt-0.5">{review.reason}</div>}
                  </div>
                ))}
              </div>
            </section>
          )}
          <section className="p-4">
            <div className="text-xs uppercase text-text-dim mb-2">Destinations</div>
            <div className="flex flex-col gap-2">
              {post.targets.length === 0 ? <div className="text-text-dim text-sm">No targets.</div> : post.targets.map((target) => (
                <div key={target.id} className="border border-border rounded px-3 py-2">
                  <div className="flex items-center gap-2 text-sm">
                    <span className="text-text">{target.display_name}</span>
                    <span className="text-text-dim">{target.platform}</span>
                    <span className="ml-auto"><StatusPill status={target.status} /></span>
                  </div>
                  {target.last_error && <div className="text-error text-xs mt-1 whitespace-pre-wrap">{target.last_error}</div>}
                  {target.platform_url && <a href={target.platform_url} target="_blank" rel="noreferrer" className="text-accent text-xs hover:underline mt-1 inline-block">Open platform post ↗</a>}
                </div>
              ))}
            </div>
          </section>
        </div>
        <div className="flex flex-wrap justify-end gap-2 px-4 py-3 border-t border-border bg-bg-card/30">
          {(post.status === "failed" || post.status === "partial") && <button type="button" onClick={onRetry} className="px-3 py-1.5 text-sm border border-accent text-accent rounded">Retry</button>}
          {post.status === "scheduled" && <button type="button" onClick={onReschedule} className="px-3 py-1.5 text-sm border border-border text-text rounded">Reschedule</button>}
          {(post.status === "draft" || post.status === "rejected" || post.status === "approved") && <button type="button" onClick={onEdit} className="px-3 py-1.5 text-sm border border-border text-text rounded">Edit draft</button>}
          {(post.status === "draft" || post.status === "rejected") && <button type="button" onClick={onSubmit} className="px-3 py-1.5 text-sm border border-border text-text rounded">Submit for review</button>}
          {post.status === "in_review" && <button type="button" onClick={onReject} className="px-3 py-1.5 text-sm border border-error text-error rounded">Reject</button>}
          {post.status === "in_review" && <button type="button" onClick={onApprove} className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold">Approve</button>}
          {(post.status === "draft" || post.status === "approved") && <button type="button" onClick={onSchedule} className="px-3 py-1.5 text-sm border border-border text-text rounded">Schedule</button>}
          {(post.status === "draft" || post.status === "approved") && <button type="button" onClick={onPublish} className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold">Publish now</button>}
          {editable && <button type="button" onClick={onEdit} className="px-3 py-1.5 text-sm border border-border text-text rounded">Edit</button>}
          <button type="button" onClick={onDelete} className="px-3 py-1.5 text-sm border border-error text-error rounded">{post.status === "scheduled" ? "Cancel post" : "Delete"}</button>
        </div>
      </aside>
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

// EDITABLE_PLATFORMS — kept here (rather than driven by the server's
// /platforms endpoint) because edit-support is more about platform
// API constraints than per-install configuration. If the server gains
// more edit verbs (Reddit, IG caption-only) we widen this set.
const EDITABLE_PLATFORMS: Set<string> = new Set(["facebook", "youtube"]);

function isEditablePlatform(platform: string): boolean {
  return EDITABLE_PLATFORMS.has(platform);
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
  post, projectId, onClose, onSaved, setStatus,
}: {
  post: Post;
  projectId?: string | null;
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const [body, setBody] = useState(post.body || "");
  // Per-target option overrides, keyed by social_account_id. Seeded
  // empty — the server already has the existing options on file and
  // will merge our overrides on top, so the user only types what they
  // want to change.
  const [targetOptions, setTargetOptions] = useState<Record<number, Record<string, any>>>({});
  const [busy, setBusy] = useState(false);
  const draftLike = post.status === "draft" || post.status === "rejected" || post.status === "approved";

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
      if (targets.length > 0) {
        payload.targets = draftLike
          ? post.targets.map((target) => ({
              social_account_id: target.social_account_id,
              ...(target.options || {}),
              ...(targetOptions[target.social_account_id] || {}),
            }))
          : targets;
      }
      if (draftLike) payload.expected_revision = post.revision;
      if (Object.keys(payload).length === 0) {
        setStatus("Nothing to save — body and targets unchanged.");
        setBusy(false);
        return;
      }
      const res = await fetch(appURL(`/posts/${post.id}/${draftLike ? "draft" : "edit"}`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { targets?: TargetEditOutcome[]; isError?: boolean };
      const envelopeError = mcpEnvelopeError(data);
      if (envelopeError) throw new Error(envelopeError);
      if (draftLike) {
        setStatus("Draft updated. Any prior approval was invalidated.");
        onSaved();
        return;
      }
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
            <div className="text-text font-bold">{draftLike ? "Edit draft" : "Edit post"}</div>
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
  post, projectId, onClose, onChanged, setStatus,
}: {
  post: Post;
  projectId?: string | null;
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
  const schedulingDraft = post.status === "draft" || post.status === "approved";

  const submit = async () => {
    if (!when) return;
    setBusy(true);
    try {
      const res = await fetch(appURL(`/posts/${post.id}/${schedulingDraft ? "publish" : "reschedule"}`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(schedulingDraft
          ? { expected_revision: post.revision, mode: "schedule", schedule_at: when }
          : { schedule_at: when }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const envelopeError = mcpEnvelopeError(data);
      if (envelopeError) throw new Error(envelopeError);
      setStatus(schedulingDraft ? "Draft scheduled." : "Rescheduled.");
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
          <div className="text-text font-medium">{schedulingDraft ? "Schedule draft" : "Reschedule post"} #{post.id}</div>
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
            disabled={!when || busy || (!schedulingDraft && when === seed)}
            className="px-4 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {busy ? "…" : schedulingDraft ? "Schedule" : "Reschedule"}
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
const mediaMetaCache = new Map<string, { mime: string; name: string } | "loading" | "error">();
const mediaMetaWaiters = new Map<string, ((m: { mime: string; name: string } | null) => void)[]>();

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

// --- MetricsView --------------------------------------------------
//
// Two sections:
//   1. Account-level totals — one row per connected account, click to
//      load (lazy; the agent flow doesn't need all of them at once).
//   2. Recent published posts — table with normalized metrics columns.
//      Click a row to expand and fetch per-target details (raw blob
//      included for deep-dives).
//
// Account charts read stored social_metric_points first. The explicit
// Refresh metrics action still fetches upstream live data and persists
// new points, matching the MCP-tool refresh semantics.

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
  profile_id?: number;
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
  likes?: number;
  comments?: number;
  shares?: number;
  saves?: number;
  sends?: number;
  clicks?: number;
  engagement_rate?: number;
  insights?: Record<string, { time?: string; value: number }[]>;
  breakdowns?: AnalyticsBreakdown[];
  capabilities?: {
    available?: string[];
    unavailable?: { dimension: string; reason: string }[];
  };
  history_source?: string;
  updated_at?: string;
  raw?: any;
}

interface AnalyticsBreakdownRow {
  dimensions: Record<string, string>;
  metrics: Record<string, number>;
}

interface AnalyticsBreakdown {
  dimension: string;
  status: "ok" | "unsupported" | "failed";
  reason?: string;
  source?: string;
  rows?: AnalyticsBreakdownRow[];
}

const ANALYTICS_BREAKDOWNS = [
  ["device", "Device"],
  ["os", "Operating system"],
  ["country", "Country"],
  ["region", "Region"],
  ["city", "City"],
  ["age", "Age"],
  ["gender", "Gender"],
  ["traffic_source", "Traffic source"],
  ["audience_type", "Audience"],
  ["content_type", "Content type"],
  ["sharing_service", "Sharing service"],
  ["video", "Video"],
] as const;

function MetricsView({
  posts, accounts, projectId, setStatus, onPostsChanged,
}: {
  posts: Post[];
  accounts: SocialAccount[];
  projectId?: string | null;
  setStatus: (s: string) => void;
  onPostsChanged: () => void;
}) {
  const [accountFor, setAccountFor] = useState<Record<number, AccountMetrics | "loading" | { error: string }>>({});
  const [postFor, setPostFor] = useState<Record<number, PostMetrics | "loading" | { error: string }>>({});
  const [expanded, setExpanded] = useState<number | null>(null);
  const [activeAccountId, setActiveAccountId] = useState<number | null>(accounts[0]?.id ?? null);
  const [syncFor, setSyncFor] = useState<Record<number, "loading" | "done" | { error: string }>>({});
  const [refreshingFor, setRefreshingFor] = useState<Record<number, boolean>>({});
  const [analyticsRange, setAnalyticsRange] = useState("28d");
  const [breakdownDimension, setBreakdownDimension] = useState("device");
  const [breakdownFilter, setBreakdownFilter] = useState("all");
  const autoLoadedAccounts = useRef<Set<string>>(new Set());
  const autoRefreshedAccounts = useRef<Set<string>>(new Set());
  const accountIds = accounts.map((a) => a.id).join(",");

  useEffect(() => {
    setAccountFor({});
    setPostFor({});
    setSyncFor({});
    setRefreshingFor({});
    setExpanded(null);
    autoLoadedAccounts.current.clear();
    autoRefreshedAccounts.current.clear();
  }, [projectId]);

  useEffect(() => {
    if (accounts.length === 0) {
      setActiveAccountId(null);
      return;
    }
    if (!activeAccountId || !accounts.some((a) => a.id === activeAccountId)) {
      setActiveAccountId(accounts[0].id);
    }
  }, [accounts, activeAccountId]);

  const loadAccount = async (id: number, refresh = false, background = false) => {
    if (!background) setAccountFor((prev) => ({ ...prev, [id]: "loading" }));
    if (refresh) setRefreshingFor((prev) => ({ ...prev, [id]: true }));
    try {
      const params = new URLSearchParams({
        refresh: refresh ? "1" : "0",
        range: analyticsRange,
        breakdowns: breakdownDimension,
      });
      const res = await fetch(appURL(`/accounts/${id}/metrics?${params}`, projectId), { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as AccountMetrics;
      setAccountFor((prev) => ({ ...prev, [id]: data }));
    } catch (e) {
      if (!background) {
        setAccountFor((prev) => ({ ...prev, [id]: { error: (e as Error).message } }));
      }
    } finally {
      if (refresh) setRefreshingFor((prev) => ({ ...prev, [id]: false }));
    }
  };

  useAppEvents<{ social_account_id?: number }>("social", projectId, (event) => {
    if (event.topic !== "metrics.updated") return;
    const accountID = Number(event.data?.social_account_id || 0);
    if (accountID > 0 && accounts.some((account) => account.id === accountID)) {
      loadAccount(accountID, false, true);
    }
  });

  const syncAccountPosts = async (id: number, quiet = false) => {
    setSyncFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(appURL(`/accounts/${id}/import?limit=100`, projectId), {
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
    autoLoadedAccounts.current.clear();
    for (const account of accounts) {
      const key = `${account.id}:${analyticsRange}:${breakdownDimension}`;
      if (!autoLoadedAccounts.current.has(key)) {
        autoLoadedAccounts.current.add(key);
        loadAccount(account.id);
      }
    }
  }, [accountIds, analyticsRange, breakdownDimension]);

  useEffect(() => {
    setBreakdownFilter("all");
  }, [analyticsRange, breakdownDimension]);

  const loadPost = async (id: number) => {
    setPostFor((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const res = await fetch(appURL(`/posts/${id}/metrics`, projectId), { credentials: "same-origin" });
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

  useEffect(() => {
    if (!activeAccount || !activeMetrics || activeMetrics === "loading" || "error" in activeMetrics) return;
    const refreshIfStale = () => {
      if (!accountMetricsNeedRefresh(activeMetrics)) return;
      const key = `${activeAccount.id}:${activeMetrics.updated_at || "missing"}`;
      if (autoRefreshedAccounts.current.has(key)) return;
      autoRefreshedAccounts.current.add(key);
      loadAccount(activeAccount.id, true, true);
    };
    if (accountMetricsNeedRefresh(activeMetrics)) {
      refreshIfStale();
      return;
    }
    const updatedAt = Date.parse(activeMetrics.updated_at || "");
    if (!Number.isFinite(updatedAt)) return;
    const timer = window.setTimeout(
      refreshIfStale,
      Math.max(1000, updatedAt + ACCOUNT_METRICS_STALE_MS - Date.now()),
    );
    return () => window.clearTimeout(timer);
  }, [activeAccount?.id, activeMetrics]);

  const activeBreakdown = activeMetrics && typeof activeMetrics === "object" && "status" in activeMetrics
    ? (activeMetrics.breakdowns || []).find((item) => item.dimension === breakdownDimension)
    : undefined;
  const breakdownValues = Array.from(new Set(
    (activeBreakdown?.rows || []).map((row) => row.dimensions[breakdownDimension]).filter(Boolean)
  ));
  const published = posts.filter((p) =>
    (p.status === "published" || p.status === "partial") &&
    (!activeAccount || (p.targets || []).some((t) => t.social_account_id === activeAccount.id))
  );

  return (
    <div className="p-4 flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h2 className="text-sm uppercase tracking-wide text-text-dim">Accounts</h2>
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
        <section className="flex flex-col gap-4 border-t border-border pt-4">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex-1 min-w-0">
              <div className="text-text font-medium truncate">{activeAccount.display_name}</div>
              <div className="text-text-dim text-xs">{activeAccount.platform}</div>
              {activeMetrics && activeMetrics !== "loading" && !("error" in activeMetrics) && activeMetrics.updated_at && (
                <div className="text-text-dim text-xs mt-0.5">
                  Updated {new Date(activeMetrics.updated_at).toLocaleString()}
                </div>
              )}
            </div>
            <button
              onClick={() => loadAccount(activeAccount.id, true, true)}
              disabled={refreshingFor[activeAccount.id]}
              className="px-2.5 py-1.5 text-xs border border-border rounded text-accent hover:border-accent disabled:opacity-50"
            >
              {refreshingFor[activeAccount.id] ? "Refreshing…" : "Refresh analytics"}
            </button>
            <button
              onClick={() => syncAccountPosts(activeAccount.id)}
              disabled={syncFor[activeAccount.id] === "loading"}
              className="px-2.5 py-1.5 text-xs border border-border rounded text-accent hover:border-accent disabled:opacity-50"
            >
              {syncFor[activeAccount.id] === "loading" ? "Syncing..." : "Sync posts"}
            </button>
          </div>
          <div className="flex flex-wrap items-end gap-2 border-y border-border py-3">
            <label className="flex flex-col gap-1">
              <span className="text-[10px] uppercase tracking-wide text-text-dim">Range</span>
              <select
                value={analyticsRange}
                onChange={(event) => setAnalyticsRange(event.target.value)}
                className="h-9 min-w-[110px] bg-bg-input border border-border rounded px-2 text-sm"
              >
                <option value="7d">Last 7 days</option>
                <option value="28d">Last 28 days</option>
                <option value="90d">Last 90 days</option>
              </select>
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[10px] uppercase tracking-wide text-text-dim">Breakdown</span>
              <select
                value={breakdownDimension}
                onChange={(event) => setBreakdownDimension(event.target.value)}
                className="h-9 min-w-[180px] bg-bg-input border border-border rounded px-2 text-sm"
              >
                {ANALYTICS_BREAKDOWNS.map(([value, label]) => (
                  <option key={value} value={value}>{label}</option>
                ))}
              </select>
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[10px] uppercase tracking-wide text-text-dim">Show</span>
              <select
                value={breakdownFilter}
                onChange={(event) => setBreakdownFilter(event.target.value)}
                disabled={breakdownValues.length === 0}
                className="h-9 min-w-[160px] bg-bg-input border border-border rounded px-2 text-sm disabled:opacity-50"
              >
                <option value="all">All values</option>
                {breakdownValues.map((value) => (
                  <option key={value} value={value}>{analyticsLabel(value)}</option>
                ))}
              </select>
            </label>
          </div>
          {activeMetrics === "loading" ? (
            <div className="text-text-dim text-sm">Loading account metrics...</div>
          ) : activeMetrics && typeof activeMetrics === "object" && "error" in activeMetrics ? (
            <div className="text-red text-sm">{activeMetrics.error}</div>
          ) : activeMetrics && typeof activeMetrics === "object" && "status" in activeMetrics ? (
            <>
              <div className="text-xs uppercase tracking-wide text-text-dim">Overview</div>
              <AccountMetricsSummary metrics={activeMetrics as AccountMetrics} />
              {(activeMetrics as AccountMetrics).status === "ok" && (
                <>
                  <div className="text-xs uppercase tracking-wide text-text-dim mt-1">Trends</div>
                  <InsightCharts metrics={activeMetrics as AccountMetrics} />
                  <BreakdownView breakdown={activeBreakdown} filter={breakdownFilter} />
                </>
              )}
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
        <AccountBreakdownComparison
          accounts={accounts}
          accountFor={accountFor}
          dimension={breakdownDimension}
          filter={breakdownFilter}
        />
      )}
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h2 className="text-sm uppercase tracking-wide text-text-dim">Post performance</h2>
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
  if (am.posts != null && am.posts > 0) bits.push(`${am.posts} posts`);
  if (am.total_videos != null && am.total_videos > 0) bits.push(`${am.total_videos} videos`);
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
    ["likes", metrics.likes ?? metrics.total_likes],
    ["reach", metrics.reach],
    ["impressions", metrics.impressions],
    ["engagements", metrics.engagements],
    ["views", metrics.views],
    ["comments", metrics.comments],
    ["shares", metrics.shares],
    ["saves", metrics.saves],
    ["sends", metrics.sends],
    ["clicks", metrics.clicks],
  ].filter(([, value]) => value != null && Number(value) > 0) as [string, number][];
  if (metrics.engagement_rate != null && metrics.engagement_rate > 0) {
    stats.push(["engagement rate", metrics.engagement_rate]);
  }
  if (stats.length === 0) {
    return <div className="text-text-dim text-sm">No totals returned by the platform.</div>;
  }
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
      {stats.map(([label, value]) => (
        <div key={label} className="border border-border rounded px-3 py-2 bg-bg">
          <div className="text-text font-medium">
            {label === "engagement rate" ? `${value.toFixed(2)}%` : formatNumber(value)}
          </div>
          <div className="text-text-dim text-[10px] uppercase tracking-wide">{label}</div>
        </div>
      ))}
    </div>
  );
}

function AccountBreakdownComparison({
  accounts,
  accountFor,
  dimension,
  filter,
}: {
  accounts: SocialAccount[];
  accountFor: Record<number, AccountMetrics | "loading" | { error: string }>;
  dimension: string;
  filter: string;
}) {
  const rows = accounts.flatMap((account) => {
    const metrics = accountFor[account.id];
    if (!metrics || metrics === "loading" || !("status" in metrics)) return [];
    const breakdown = (metrics.breakdowns || []).find((item) => item.dimension === dimension && item.status === "ok");
    if (!breakdown) return [];
    const matching = (breakdown.rows || []).filter((row) => filter === "all" || row.dimensions[dimension] === filter);
    const value = matching.reduce((total, row) => total + breakdownMetric(row.metrics).value, 0);
    return [{ account, value }];
  }).sort((a, b) => b.value - a.value);
  if (rows.length < 2) return null;
  const max = Math.max(...rows.map((row) => row.value), 1);
  return (
    <section className="flex flex-col gap-3 border-t border-border pt-4">
      <div>
        <h2 className="text-sm uppercase tracking-wide text-text-dim">Account comparison</h2>
        <div className="text-text-dim text-xs mt-1">
          {filter === "all" ? `All ${analyticsLabel(dimension)} values` : analyticsLabel(filter)}
        </div>
      </div>
      <div className="flex flex-col gap-2">
        {rows.map(({ account, value }) => (
          <div key={account.id} className="grid grid-cols-[minmax(140px,220px)_1fr_auto] items-center gap-3">
            <div className="min-w-0">
              <div className="text-text text-sm truncate">{account.display_name}</div>
              <div className="text-text-dim text-xs">{account.platform}</div>
            </div>
            <div className="h-2 bg-bg-input rounded overflow-hidden">
              <div className="h-full bg-accent" style={{ width: `${Math.max(2, (value / max) * 100)}%` }} />
            </div>
            <div className="text-text text-sm tabular-nums min-w-[64px] text-right">{formatNumber(value)}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

function BreakdownView({ breakdown, filter }: { breakdown?: AnalyticsBreakdown; filter: string }) {
  if (!breakdown) return null;
  if (breakdown.status !== "ok") {
    return (
      <div className="border-t border-border pt-3 text-text-dim text-sm">
        {breakdown.reason || `${analyticsLabel(breakdown.dimension)} analytics are not available from this provider.`}
      </div>
    );
  }
  const rows = (breakdown.rows || [])
    .filter((row) => filter === "all" || row.dimensions[breakdown.dimension] === filter)
    .map((row) => ({ row, metric: breakdownMetric(row.metrics) }))
    .sort((a, b) => b.metric.value - a.metric.value);
  if (rows.length === 0) {
    return <div className="border-t border-border pt-3 text-text-dim text-sm">No breakdown data returned for this range.</div>;
  }
  const max = Math.max(...rows.map(({ metric }) => metric.value), 1);
  return (
    <div className="border-t border-border pt-3 flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm uppercase tracking-wide text-text-dim">{analyticsLabel(breakdown.dimension)}</h3>
        <span className="text-text-dim text-xs">{analyticsLabel(rows[0].metric.name)}</span>
      </div>
      <div className="flex flex-col gap-2">
        {rows.map(({ row, metric }) => {
          const value = row.dimensions[breakdown.dimension] || "unknown";
          return (
            <div key={JSON.stringify(row.dimensions)} className="grid grid-cols-[minmax(120px,200px)_1fr_auto] items-center gap-3">
              <span className="text-text text-sm truncate" title={analyticsLabel(value)}>{analyticsLabel(value)}</span>
              <div className="h-2 bg-bg-input rounded overflow-hidden">
                <div className="h-full bg-accent" style={{ width: `${Math.max(2, (metric.value / max) * 100)}%` }} />
              </div>
              <span className="text-text text-sm tabular-nums min-w-[64px] text-right">{formatNumber(metric.value)}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function breakdownMetric(metrics: Record<string, number>): { name: string; value: number } {
  for (const name of ["views", "audience", "impressions", "reach", "engagements", "watch_time_minutes"]) {
    if (metrics[name] != null) return { name, value: Number(metrics[name]) || 0 };
  }
  const first = Object.entries(metrics)[0];
  return first ? { name: first[0], value: Number(first[1]) || 0 } : { name: "views", value: 0 };
}

function analyticsLabel(value: string): string {
  return value
    .replace(/_/g, " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function InsightCharts({ metrics }: { metrics: AccountMetrics }) {
  const entries = metricTrendEntries(metrics.insights);
  if (entries.length === 0) {
    return <div className="py-6 text-center text-text-dim text-sm">No trend data yet.</div>;
  }
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
      {entries.map(({ name, points }) => {
        const latest = points[points.length - 1];
        return (
          <div key={name} className="border border-border rounded px-3 py-2 bg-bg">
            <div className="flex items-center justify-between gap-3">
              <span className="text-text text-sm">{name.replace(/_/g, " ")}</span>
              <span className="text-text font-medium">{formatNumber(latest.value)}</span>
            </div>
            <Sparkline points={points} gradientId={`socialMetricFill-${name.replace(/[^a-zA-Z0-9_-]/g, "-")}`} />
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

function Sparkline({ points, gradientId }: { points: { time?: string; value: number }[]; gradientId: string }) {
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
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
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
            fill={`url(#${gradientId})`}
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
  const [open, setOpen] = useState(false);
  const url = storageURL(`/files/${fileId}/content`, projectId);
  useEffect(() => {
    let alive = true;
    loadMediaMeta(fileId, projectId).then((m) => { if (alive) setMeta(m); });
    return () => { alive = false; };
  }, [fileId, projectId]);
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

function ExternalMediaThumb({ url }: { url: string }) {
  const [failed, setFailed] = useState(false);
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener"
      className="block w-20 h-20 rounded border border-border overflow-hidden bg-bg-input flex-shrink-0 relative group"
      title="Imported media"
    >
      {!failed ? (
        <img
          src={url}
          alt=""
          loading="lazy"
          onError={() => setFailed(true)}
          className="w-full h-full object-cover"
        />
      ) : (
        <div className="w-full h-full grid place-items-center px-2 text-center text-[10px] text-text-dim">
          Open media
        </div>
      )}
      <div className="absolute left-1 bottom-1 px-1 py-0.5 rounded bg-black/70 text-[9px] uppercase text-white">
        imported
      </div>
    </a>
  );
}

function StatusPill({ status }: { status: string }) {
  const tone =
    status === "published" ? "text-success" :
    status === "failed" ? "text-error" :
    status === "partial" ? "text-warn" :
    status === "scheduled" ? "text-info" :
    status === "approved" ? "text-success" :
    status === "in_review" ? "text-info" :
    status === "rejected" ? "text-error" :
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
