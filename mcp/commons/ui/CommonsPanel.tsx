import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/commons/api";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Profile {
  id: number;
  username: string;
  display_name: string;
  summary: string;
  domain: string;
  actor_url: string;
  inbox_url: string;
  outbox_url: string;
  created_at: string;
  updated_at: string;
}

interface Post {
  id: number;
  profile_id: number;
  username: string;
  body: string;
  visibility: string;
  activity_id: string;
  object_id: string;
  published_at: string;
  created_at: string;
}

interface ExportData {
  schema: string;
  profiles: Profile[];
  posts: Post[];
  follows: unknown[];
  blocks: unknown[];
}

interface IngressRoute {
  id: number;
  hostname: string;
  target: string;
  project_id: string;
  owner_kind: string;
  tls_mode: string;
  status: string;
  created_at: string;
  updated_at: string;
}

type Tab = "timeline" | "profiles" | "domain" | "moderation" | "export";

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    credentials: "same-origin",
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(options?.headers || {}),
    },
  });
  if (!res.ok) throw new Error((await res.text()) || `${res.status}`);
  return res.json() as Promise<T>;
}

function relTime(iso: string): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!t) return iso;
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function handleOf(p: Profile): string {
  const host = p.domain.replace(/^https?:\/\//, "").replace(/\/$/, "");
  return `@${p.username}@${host}`;
}

export default function CommonsPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("timeline");
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [posts, setPosts] = useState<Post[]>([]);
  const [routes, setRoutes] = useState<IngressRoute[]>([]);
  const [selectedUser, setSelectedUser] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);

  const [profileDraft, setProfileDraft] = useState({
    username: "",
    domain: "",
    display_name: "",
    summary: "",
  });
  const [postBody, setPostBody] = useState("");
  const [followDraft, setFollowDraft] = useState({
    remote_actor: "",
    remote_inbox: "",
    remote_name: "",
  });
  const [blockDraft, setBlockDraft] = useState({
    target: "",
    kind: "domain",
    reason: "",
  });
  const [domainDraft, setDomainDraft] = useState("");
  const [exportData, setExportData] = useState<ExportData | null>(null);

  const activeProfile = useMemo(
    () => profiles.find((p) => p.username === selectedUser) || profiles[0] || null,
    [profiles, selectedUser],
  );

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const [nextProfiles, nextPosts] = await Promise.all([
        api<Profile[]>("/profiles"),
        api<Post[]>("/timeline?limit=80"),
      ]);
      setProfiles(nextProfiles);
      setPosts(nextPosts);
      try {
        const ingress = await api<{ routes: IngressRoute[] }>("/ingress");
        setRoutes(ingress.routes || []);
      } catch {
        setRoutes([]);
      }
      if (!selectedUser && nextProfiles[0]) setSelectedUser(nextProfiles[0].username);
      setStatus(`updated ${new Date().toLocaleTimeString()}`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [selectedUser]);

  useEffect(() => {
    load();
  }, [load]);

  const createProfile = async () => {
    if (!profileDraft.username.trim() || !profileDraft.domain.trim()) return;
    setBusy(true);
    try {
      const p = await api<Profile>("/profiles", {
        method: "POST",
        body: JSON.stringify(profileDraft),
      });
      setSelectedUser(p.username);
      setProfileDraft({ username: "", domain: "", display_name: "", summary: "" });
      await load();
      setStatus(`profile ${handleOf(p)} saved`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const exposeDomain = async () => {
    if (!domainDraft.trim()) return;
    setBusy(true);
    try {
      const result = await api<{ route: IngressRoute }>("/ingress", {
        method: "POST",
        body: JSON.stringify({ hostname: domainDraft.trim(), project_id: projectId }),
      });
      setDomainDraft("");
      await load();
      setStatus(`exposed ${result.route.hostname}`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const removeDomain = async (hostname: string) => {
    if (!confirm(`Remove public hostname ${hostname}?`)) return;
    setBusy(true);
    try {
      await api("/ingress", {
        method: "DELETE",
        body: JSON.stringify({ hostname }),
      });
      await load();
      setStatus(`removed ${hostname}`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const publishPost = async () => {
    if (!activeProfile || !postBody.trim()) return;
    setBusy(true);
    try {
      await api<Post>("/posts", {
        method: "POST",
        body: JSON.stringify({ username: activeProfile.username, body: postBody }),
      });
      setPostBody("");
      await load();
      setStatus("post published");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const addFollow = async () => {
    if (!activeProfile || !followDraft.remote_actor.trim() || !followDraft.remote_inbox.trim()) return;
    setBusy(true);
    try {
      await api("/follows", {
        method: "POST",
        body: JSON.stringify({ username: activeProfile.username, ...followDraft }),
      });
      setFollowDraft({ remote_actor: "", remote_inbox: "", remote_name: "" });
      setStatus("follower added");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const addBlock = async () => {
    if (!blockDraft.target.trim()) return;
    setBusy(true);
    try {
      await api("/blocks", {
        method: "POST",
        body: JSON.stringify({
          username: activeProfile?.username || "",
          target: blockDraft.target,
          kind: blockDraft.kind,
          reason: blockDraft.reason,
        }),
      });
      setBlockDraft({ target: "", kind: "domain", reason: "" });
      setStatus("block saved");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const loadExport = async () => {
    setBusy(true);
    try {
      const query = activeProfile ? `?username=${encodeURIComponent(activeProfile.username)}` : "";
      const data = await api<ExportData>(`/export${query}`);
      setExportData(data);
      setStatus("export ready");
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="border-b border-border px-4 py-3 flex items-center gap-3">
        <div className="min-w-0">
          <div className="font-medium">Commons</div>
          <div className="text-xs text-text-dim truncate">
            {activeProfile ? handleOf(activeProfile) : "No profile"}
          </div>
        </div>
        <select
          value={activeProfile?.username || ""}
          onChange={(e) => setSelectedUser(e.target.value)}
          className="ml-2 bg-bg-input border border-border rounded px-2 py-1 text-sm max-w-64"
        >
          {profiles.length === 0 && <option value="">No profiles</option>}
          {profiles.map((p) => (
            <option key={p.id} value={p.username}>{handleOf(p)}</option>
          ))}
        </select>
        <button
          type="button"
          onClick={load}
          disabled={busy}
          className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >
          Refresh
        </button>
        <span className="ml-auto text-xs text-text-dim truncate">{status}</span>
      </header>

      <div className="border-b border-border px-4 py-2 flex gap-2">
        {(["timeline", "profiles", "domain", "moderation", "export"] as Tab[]).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={`px-3 py-1 text-sm border rounded capitalize ${
              tab === t ? "border-accent text-accent bg-accent/10" : "border-border text-text-muted hover:text-text"
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      <main className="flex-1 overflow-auto p-4">
        {tab === "timeline" && (
          <div className="grid grid-cols-1 xl:grid-cols-[minmax(320px,420px)_1fr] gap-4">
            <section className="border border-border rounded p-3 flex flex-col gap-3">
              <div className="text-sm font-medium">Publish</div>
              <textarea
                value={postBody}
                onChange={(e) => setPostBody(e.target.value)}
                rows={8}
                placeholder="What's happening on your Commons?"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm resize-none"
              />
              <div className="flex items-center gap-2">
                <span className="text-xs text-text-dim">{postBody.length} chars</span>
                <button
                  type="button"
                  onClick={publishPost}
                  disabled={busy || !activeProfile || !postBody.trim()}
                  className="ml-auto px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
                >
                  Publish
                </button>
              </div>
            </section>

            <section className="border border-border rounded">
              <div className="px-3 py-2 border-b border-border text-sm font-medium">Local Timeline</div>
              {posts.length === 0 ? (
                <div className="p-6 text-sm text-text-muted">No posts yet.</div>
              ) : (
                <ul className="divide-y divide-border">
                  {posts.map((p) => (
                    <li key={p.id} className="p-3">
                      <div className="flex items-center gap-2 text-xs text-text-dim">
                        <span className="text-text-muted">@{p.username}</span>
                        <span>{relTime(p.published_at || p.created_at)}</span>
                        <a className="ml-auto hover:text-accent truncate" href={p.object_id} target="_blank" rel="noreferrer">
                          object
                        </a>
                      </div>
                      <p className="mt-2 whitespace-pre-wrap text-sm leading-6">{p.body}</p>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>
        )}

        {tab === "profiles" && (
          <div className="grid grid-cols-1 xl:grid-cols-[minmax(320px,420px)_1fr] gap-4">
            <section className="border border-border rounded p-3 flex flex-col gap-3">
              <div className="text-sm font-medium">Create Profile</div>
              <input
                value={profileDraft.username}
                onChange={(e) => setProfileDraft((d) => ({ ...d, username: e.target.value }))}
                placeholder="username"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <input
                value={profileDraft.domain}
                onChange={(e) => setProfileDraft((d) => ({ ...d, domain: e.target.value }))}
                placeholder="https://social.example.com"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <input
                value={profileDraft.display_name}
                onChange={(e) => setProfileDraft((d) => ({ ...d, display_name: e.target.value }))}
                placeholder="Display name"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <textarea
                value={profileDraft.summary}
                onChange={(e) => setProfileDraft((d) => ({ ...d, summary: e.target.value }))}
                rows={4}
                placeholder="Profile summary"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm resize-none"
              />
              <button
                type="button"
                onClick={createProfile}
                disabled={busy || !profileDraft.username.trim() || !profileDraft.domain.trim()}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
              >
                Save Profile
              </button>
            </section>

            <section className="border border-border rounded">
              <div className="px-3 py-2 border-b border-border text-sm font-medium">Profiles</div>
              {profiles.length === 0 ? (
                <div className="p-6 text-sm text-text-muted">No local profiles.</div>
              ) : (
                <ul className="divide-y divide-border">
                  {profiles.map((p) => (
                    <li key={p.id} className="p-3">
                      <div className="flex items-center gap-2">
                        <div className="font-medium">{p.display_name || p.username}</div>
                        <code className="text-xs text-text-dim truncate">{handleOf(p)}</code>
                      </div>
                      {p.summary && <p className="mt-1 text-sm text-text-muted">{p.summary}</p>}
                      <div className="mt-2 grid grid-cols-1 lg:grid-cols-2 gap-1 text-xs">
                        <a className="text-accent truncate" href={p.actor_url} target="_blank" rel="noreferrer">{p.actor_url}</a>
                        <a className="text-accent truncate" href={`/.well-known/webfinger?resource=acct:${p.username}@${p.domain.replace(/^https?:\/\//, "")}`} target="_blank" rel="noreferrer">
                          WebFinger
                        </a>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>
        )}

        {tab === "domain" && (
          <div className="grid grid-cols-1 xl:grid-cols-[minmax(320px,420px)_1fr] gap-4">
            <section className="border border-border rounded p-3 flex flex-col gap-3">
              <div className="text-sm font-medium">Expose Public Hostname</div>
              <input
                value={domainDraft}
                onChange={(e) => setDomainDraft(e.target.value)}
                placeholder="social.example.com"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <button
                type="button"
                onClick={exposeDomain}
                disabled={busy || !domainDraft.trim()}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
              >
                Expose Commons
              </button>
              <div className="text-xs text-text-dim leading-5">
                Point DNS for the hostname at this Apteva server first. The server-native ingress layer routes the hostname to this Commons install and manages HTTP-01 TLS when the hostname reaches the server.
              </div>
            </section>

            <section className="border border-border rounded">
              <div className="px-3 py-2 border-b border-border text-sm font-medium">Public Hostnames</div>
              {routes.length === 0 ? (
                <div className="p-6 text-sm text-text-muted">No Commons hostnames exposed.</div>
              ) : (
                <ul className="divide-y divide-border">
                  {routes.map((r) => (
                    <li key={r.id || r.hostname} className="p-3 flex items-center gap-3">
                      <div className="min-w-0 flex-1">
                        <a className="text-accent truncate block" href={`https://${r.hostname}`} target="_blank" rel="noreferrer">
                          {r.hostname}
                        </a>
                        <div className="text-xs text-text-dim truncate">
                          {r.target} · {r.status} · TLS {r.tls_mode}
                        </div>
                      </div>
                      <button
                        type="button"
                        onClick={() => removeDomain(r.hostname)}
                        disabled={busy}
                        className="px-2 py-1 text-xs border border-red text-red rounded hover:bg-red hover:text-bg disabled:opacity-50"
                      >
                        Remove
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>
        )}

        {tab === "moderation" && (
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <section className="border border-border rounded p-3 flex flex-col gap-3">
              <div className="text-sm font-medium">Manual Follower</div>
              <input
                value={followDraft.remote_actor}
                onChange={(e) => setFollowDraft((d) => ({ ...d, remote_actor: e.target.value }))}
                placeholder="https://remote.example/users/alice"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <input
                value={followDraft.remote_inbox}
                onChange={(e) => setFollowDraft((d) => ({ ...d, remote_inbox: e.target.value }))}
                placeholder="https://remote.example/users/alice/inbox"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <input
                value={followDraft.remote_name}
                onChange={(e) => setFollowDraft((d) => ({ ...d, remote_name: e.target.value }))}
                placeholder="Remote name"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <button
                type="button"
                onClick={addFollow}
                disabled={busy || !activeProfile || !followDraft.remote_actor.trim() || !followDraft.remote_inbox.trim()}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
              >
                Add Follower
              </button>
            </section>

            <section className="border border-border rounded p-3 flex flex-col gap-3">
              <div className="text-sm font-medium">Block</div>
              <div className="flex gap-2">
                <select
                  value={blockDraft.kind}
                  onChange={(e) => setBlockDraft((d) => ({ ...d, kind: e.target.value }))}
                  className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
                >
                  <option value="domain">domain</option>
                  <option value="actor">actor</option>
                </select>
                <input
                  value={blockDraft.target}
                  onChange={(e) => setBlockDraft((d) => ({ ...d, target: e.target.value }))}
                  placeholder={blockDraft.kind === "domain" ? "bad.example" : "https://bad.example/users/user"}
                  className="flex-1 min-w-0 bg-bg-input border border-border rounded px-3 py-2 text-sm"
                />
              </div>
              <input
                value={blockDraft.reason}
                onChange={(e) => setBlockDraft((d) => ({ ...d, reason: e.target.value }))}
                placeholder="Reason"
                className="bg-bg-input border border-border rounded px-3 py-2 text-sm"
              />
              <button
                type="button"
                onClick={addBlock}
                disabled={busy || !blockDraft.target.trim()}
                className="px-3 py-1 text-sm border border-red text-red rounded hover:bg-red hover:text-bg disabled:opacity-50"
              >
                Save Block
              </button>
            </section>
          </div>
        )}

        {tab === "export" && (
          <div className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={loadExport}
                disabled={busy}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
              >
                Generate Export
              </button>
              {exportData && (
                <span className="text-xs text-text-dim">
                  {exportData.profiles.length} profiles, {exportData.posts.length} posts, {exportData.follows.length} follows, {exportData.blocks.length} blocks
                </span>
              )}
            </div>
            <pre className="border border-border rounded bg-bg-input p-3 text-xs overflow-auto min-h-96">
              {exportData ? JSON.stringify(exportData, null, 2) : ""}
            </pre>
          </div>
        )}
      </main>
    </div>
  );
}
