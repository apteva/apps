// AuthPanel — single-pane dashboard surface for the auth app. Four
// internal tabs: Overview (stats + recent audit), Users (search/filter
// table + per-user drawer), Clients (registered OAuth clients + create
// + rotate/disable), Endpoints (OIDC discovery URLs + signing keys +
// effective config).
//
// Calls the sidecar's /admin/* HTTP routes; never imports the SDK.
// React + jsx-runtime are externalised to the dashboard's importmap.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/auth";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface User {
  id: number;
  email: string;
  email_verified_at?: string;
  display_name?: string;
  avatar_url?: string;
  status: string;
  has_password: boolean;
  mfa_enabled: boolean;
  last_login_at?: string;
  locked_until?: string;
  created_at?: string;
  updated_at?: string;
}

interface Client {
  id: number;
  client_id: string;
  name: string;
  type: "spa" | "web" | "native" | "m2m";
  redirect_uris?: string[];
  allowed_origins?: string[];
  allowed_grant_types?: string[];
  token_endpoint_auth_method: string;
  require_pkce: boolean;
  require_mfa: boolean;
  jwt_audience?: string;
  access_token_ttl_seconds?: number;
  refresh_token_ttl_seconds?: number;
  refresh_rotation: boolean;
  disabled_at?: string;
  created_at?: string;
}

interface Session {
  id: number;
  user_id: number;
  client_id: string;
  user_agent?: string;
  ip?: string;
  created_at?: string;
  last_seen_at?: string;
  expires_at?: string;
  revoked_at?: string;
}

interface AuditEvent {
  id: number;
  user_id?: number;
  client_id?: string;
  event: string;
  ip?: string;
  user_agent?: string;
  metadata?: string;
  occurred_at: string;
}

interface Stats {
  active: number;
  disabled: number;
  locked: number;
  signups_7d: number;
  logins_24h: number;
}

interface SigningKey {
  kid: string;
  alg: string;
  created_at: string;
  retired_at?: string;
}

interface OIDCInfo {
  issuer: string;
  jwks_uri: string;
  openid_configuration: string;
  authorization_endpoint: string;
  token_endpoint: string;
  userinfo_endpoint: string;
  signing_keys: SigningKey[];
  app_url_configured: boolean;
  verification_required: boolean;
  magic_link_enabled: boolean;
  access_ttl_seconds: number;
  refresh_ttl_days: number;
  password_min_length: number;
  password_classes: number;
  lockout_threshold: number;
  lockout_initial_minutes: number;
}

type Tab = "overview" | "users" | "clients" | "endpoints";

// ─── Panel root ──────────────────────────────────────────────────────

export default function AuthPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [stats, setStats] = useState<Stats | null>(null);
  const [status, setStatus] = useState("");

  const loadStats = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/stats`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`stats ${r.status}`);
      setStats(await r.json());
    } catch (e) {
      setStatus(`stats: ${(e as Error).message}`);
    }
  }, []);

  useEffect(() => {
    loadStats();
  }, [loadStats]);

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-1 border-b border-border px-4 py-2">
        <div className="flex items-center gap-2 mr-3">
          <KeyIcon />
          <span className="font-semibold text-text">Auth</span>
        </div>
        <NavTab label="Overview" value="overview" current={tab} onClick={setTab} />
        <NavTab label="Users" value="users" current={tab} onClick={setTab} count={stats ? stats.active + stats.disabled : undefined} />
        <NavTab label="Clients" value="clients" current={tab} onClick={setTab} />
        <NavTab label="Endpoints" value="endpoints" current={tab} onClick={setTab} />
        <span className="ml-auto text-text-dim text-xs truncate max-w-xs" title={status}>{status}</span>
      </header>

      <div className="flex-1 overflow-auto">
        {tab === "overview" && <OverviewTab stats={stats} projectId={projectId} setStatus={setStatus} />}
        {tab === "users" && <UsersTab projectId={projectId} setStatus={setStatus} onUsersChanged={loadStats} />}
        {tab === "clients" && <ClientsTab projectId={projectId} setStatus={setStatus} />}
        {tab === "endpoints" && <EndpointsTab projectId={projectId} setStatus={setStatus} />}
      </div>
    </div>
  );
}

function NavTab({ label, value, current, onClick, count }: {
  label: string; value: Tab; current: Tab;
  onClick: (v: Tab) => void; count?: number;
}) {
  const active = current === value;
  return (
    <button
      onClick={() => onClick(value)}
      className={
        "px-3 py-1 rounded text-sm flex items-center gap-1.5 transition-colors " +
        (active ? "bg-bg-card text-text font-medium" : "text-text-muted hover:text-text hover:bg-bg-card/50")
      }
    >
      <span>{label}</span>
      {typeof count === "number" && (
        <span className={"text-xs " + (active ? "text-text-muted" : "text-text-dim")}>{count}</span>
      )}
    </button>
  );
}

// ─── Overview tab ────────────────────────────────────────────────────

function OverviewTab({ stats, projectId, setStatus }: {
  stats: Stats | null; projectId: string; setStatus: (s: string) => void;
}) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const loadAudit = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/audit?limit=25`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`audit ${r.status}`);
      const data = await r.json();
      setEvents(data.events || []);
    } catch (e) {
      setStatus(`audit: ${(e as Error).message}`);
    }
  }, [setStatus]);
  useEffect(() => { loadAudit(); }, [loadAudit, projectId]);

  return (
    <div className="p-4 space-y-4">
      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <StatCard label="Active users" value={stats?.active} accent />
        <StatCard label="Disabled" value={stats?.disabled} />
        <StatCard label="Locked" value={stats?.locked} warn={(stats?.locked ?? 0) > 0} />
        <StatCard label="Signups (7d)" value={stats?.signups_7d} />
        <StatCard label="Logins (24h)" value={stats?.logins_24h} />
      </div>

      <section className="border border-border rounded bg-bg-card">
        <header className="flex items-center justify-between px-3 py-2 border-b border-border">
          <h3 className="text-sm font-semibold text-text">Recent activity</h3>
          <button
            onClick={loadAudit}
            className="text-text-dim hover:text-text text-xs"
            title="Refresh"
          >Refresh</button>
        </header>
        {events.length === 0 ? (
          <EmptyState
            icon={<ListIcon />}
            title="No activity yet"
            hint="Signups, logins, and admin actions will appear here."
          />
        ) : (
          <ul className="divide-y divide-border">
            {events.map((ev) => (
              <li key={ev.id} className="px-3 py-2 flex items-center gap-3 text-sm">
                <EventBadge event={ev.event} />
                <span className="text-text flex-shrink-0">{prettyEvent(ev.event)}</span>
                <span className="text-text-dim text-xs truncate flex-1" title={ev.metadata}>
                  {ev.user_id ? `user #${ev.user_id}` : ""}
                  {ev.client_id ? ` · ${ev.client_id}` : ""}
                  {ev.ip ? ` · ${ev.ip}` : ""}
                </span>
                <span className="text-text-dim text-xs flex-shrink-0">{relTime(ev.occurred_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function StatCard({ label, value, accent, warn }: {
  label: string; value: number | undefined; accent?: boolean; warn?: boolean;
}) {
  return (
    <div className="border border-border rounded bg-bg-card px-3 py-2">
      <div className="text-text-dim text-xs uppercase tracking-wide">{label}</div>
      <div className={
        "text-2xl font-semibold mt-1 " +
        (warn ? "text-warning" : accent ? "text-accent" : "text-text")
      }>
        {value ?? "—"}
      </div>
    </div>
  );
}

// ─── Users tab ───────────────────────────────────────────────────────

function UsersTab({ projectId, setStatus, onUsersChanged }: {
  projectId: string; setStatus: (s: string) => void; onUsersChanged: () => void;
}) {
  const [users, setUsers] = useState<User[]>([]);
  const [q, setQ] = useState("");
  const [statusFilter, setStatusFilter] = useState<"" | "active" | "disabled">("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (q) params.set("q", q);
      if (statusFilter) params.set("status", statusFilter);
      params.set("limit", "200");
      const r = await fetch(`${API}/admin/users?${params.toString()}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`users ${r.status}`);
      const data = await r.json();
      setUsers(data.users || []);
    } catch (e) {
      setStatus(`users: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, [q, statusFilter, setStatus]);

  useEffect(() => { load(); }, [load, projectId]);

  return (
    <div className="flex h-full">
      <div className={selectedId ? "flex-1 border-r border-border" : "flex-1"}>
        <div className="p-3 flex items-center gap-2 border-b border-border">
          <SearchInput value={q} onChange={setQ} placeholder="Search email or name" />
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value as "" | "active" | "disabled")}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm text-text"
          >
            <option value="">All statuses</option>
            <option value="active">Active</option>
            <option value="disabled">Disabled</option>
          </select>
          <button
            onClick={load}
            disabled={loading}
            className="px-3 py-1 text-sm text-text-muted hover:text-text disabled:opacity-50"
          >{loading ? "…" : "Refresh"}</button>
          <span className="ml-auto text-text-dim text-xs">{users.length} user{users.length === 1 ? "" : "s"}</span>
        </div>

        {users.length === 0 ? (
          <EmptyState
            icon={<UsersIcon />}
            title={q || statusFilter ? "No matching users" : "No users yet"}
            hint={q || statusFilter
              ? "Try clearing the filters."
              : "Users appear once your SaaS frontend has signups against /signup."}
          />
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide">
              <tr className="border-b border-border">
                <th className="text-left px-3 py-2 font-normal">Email</th>
                <th className="text-left px-3 py-2 font-normal">Status</th>
                <th className="text-left px-3 py-2 font-normal">MFA</th>
                <th className="text-left px-3 py-2 font-normal">Last login</th>
                <th className="text-left px-3 py-2 font-normal">Created</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr
                  key={u.id}
                  onClick={() => setSelectedId(u.id)}
                  className={
                    "border-b border-border cursor-pointer hover:bg-bg-card/50 " +
                    (selectedId === u.id ? "bg-bg-card" : "")
                  }
                >
                  <td className="px-3 py-2">
                    <div className="text-text">{u.email}</div>
                    {u.display_name && <div className="text-text-dim text-xs">{u.display_name}</div>}
                  </td>
                  <td className="px-3 py-2"><UserStatusPill user={u} /></td>
                  <td className="px-3 py-2">
                    {u.mfa_enabled
                      ? <span className="text-accent text-xs">on</span>
                      : <span className="text-text-dim text-xs">off</span>}
                  </td>
                  <td className="px-3 py-2 text-text-muted text-xs">{u.last_login_at ? relTime(u.last_login_at) : "—"}</td>
                  <td className="px-3 py-2 text-text-muted text-xs">{u.created_at ? relTime(u.created_at) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {selectedId != null && (
        <UserDrawer
          userId={selectedId}
          projectId={projectId}
          onClose={() => setSelectedId(null)}
          onChanged={() => { load(); onUsersChanged(); }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function UserStatusPill({ user }: { user: User }) {
  if (user.locked_until && new Date(user.locked_until) > new Date()) {
    return <Pill tone="warn">locked</Pill>;
  }
  if (user.status === "active") return <Pill tone="ok">active</Pill>;
  if (user.status === "disabled") return <Pill tone="muted">disabled</Pill>;
  return <Pill tone="muted">{user.status}</Pill>;
}

function UserDrawer({ userId, projectId, onClose, onChanged, setStatus }: {
  userId: number; projectId: string; onClose: () => void;
  onChanged: () => void; setStatus: (s: string) => void;
}) {
  const [data, setData] = useState<{ user: User; sessions: Session[]; audit_log: AuditEvent[] } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/users/${userId}/context`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`user ${r.status}`);
      setData(await r.json());
    } catch (e) {
      setStatus(`user: ${(e as Error).message}`);
    }
  }, [userId, setStatus]);
  useEffect(() => { load(); }, [load, projectId]);

  const act = async (path: string, body?: unknown) => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/admin/users/${userId}/${path}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body ?? {}),
      });
      if (!r.ok) throw new Error(`${path} ${r.status}`);
      await load();
      onChanged();
    } catch (e) {
      setStatus(`${path}: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  if (!data) {
    return (
      <aside style={{ width: 420 }} className="flex-shrink-0 p-4 text-text-dim text-sm">Loading…</aside>
    );
  }
  const u = data.user;
  const activeSessions = data.sessions.filter((s) => !s.revoked_at);
  return (
    <aside style={{ width: 420 }} className="flex-shrink-0 overflow-auto">
      <header className="sticky top-0 bg-bg border-b border-border px-4 py-3 flex items-start gap-2">
        <div className="flex-1 min-w-0">
          <div className="text-text font-semibold truncate" title={u.email}>{u.email}</div>
          <div className="text-text-dim text-xs">
            #{u.id} · {u.status}{u.email_verified_at ? " · verified" : " · unverified"}
          </div>
        </div>
        <button onClick={onClose} className="text-text-dim hover:text-text" title="Close">
          <CloseIcon />
        </button>
      </header>

      <div className="p-4 space-y-4">
        <div className="flex flex-wrap gap-2">
          {u.status === "active" ? (
            <button
              onClick={() => {
                const reason = window.prompt("Reason for disabling?", "");
                if (reason !== null) act("disable", { reason });
              }}
              disabled={busy}
              className="px-3 py-1 text-sm border border-border rounded text-text hover:bg-bg-card disabled:opacity-50"
            >Disable</button>
          ) : (
            <button
              onClick={() => act("enable")}
              disabled={busy}
              className="px-3 py-1 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
            >Re-enable</button>
          )}
          <button
            onClick={() => act("revoke_sessions")}
            disabled={busy || activeSessions.length === 0}
            className="px-3 py-1 text-sm border border-border rounded text-text hover:bg-bg-card disabled:opacity-50"
            title={activeSessions.length === 0 ? "No active sessions" : ""}
          >Revoke {activeSessions.length} session{activeSessions.length === 1 ? "" : "s"}</button>
        </div>

        <DetailGrid>
          <DetailRow label="Display name" value={u.display_name || "—"} />
          <DetailRow label="Email verified" value={u.email_verified_at ? rfc(u.email_verified_at) : "no"} />
          <DetailRow label="Password set" value={u.has_password ? "yes" : "no"} />
          <DetailRow label="MFA" value={u.mfa_enabled ? "enabled" : "not enabled"} />
          <DetailRow label="Last login" value={u.last_login_at ? rfc(u.last_login_at) : "never"} />
          <DetailRow label="Created" value={u.created_at ? rfc(u.created_at) : "—"} />
          {u.locked_until && new Date(u.locked_until) > new Date() && (
            <DetailRow label="Locked until" value={rfc(u.locked_until)} tone="warn" />
          )}
        </DetailGrid>

        <section>
          <h4 className="text-text-dim text-xs uppercase tracking-wide mb-2">
            Active sessions ({activeSessions.length})
          </h4>
          {activeSessions.length === 0 ? (
            <div className="text-text-dim text-sm">None.</div>
          ) : (
            <ul className="space-y-1">
              {activeSessions.map((s) => (
                <li key={s.id} className="border border-border rounded px-2 py-1.5 text-xs">
                  <div className="text-text truncate" title={s.user_agent}>
                    {s.user_agent || "unknown UA"}
                  </div>
                  <div className="text-text-dim mt-0.5">
                    {s.ip || "—"} · {s.client_id} · last seen {s.last_seen_at ? relTime(s.last_seen_at) : "—"}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section>
          <h4 className="text-text-dim text-xs uppercase tracking-wide mb-2">
            Recent activity ({data.audit_log.length})
          </h4>
          {data.audit_log.length === 0 ? (
            <div className="text-text-dim text-sm">None.</div>
          ) : (
            <ul className="space-y-1">
              {data.audit_log.map((ev) => (
                <li key={ev.id} className="flex items-center gap-2 text-xs">
                  <EventBadge event={ev.event} />
                  <span className="text-text">{prettyEvent(ev.event)}</span>
                  <span className="text-text-dim ml-auto">{relTime(ev.occurred_at)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </aside>
  );
}

// ─── Clients tab ─────────────────────────────────────────────────────

function ClientsTab({ projectId, setStatus }: {
  projectId: string; setStatus: (s: string) => void;
}) {
  const [clients, setClients] = useState<Client[]>([]);
  const [includeDisabled, setIncludeDisabled] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [revealed, setRevealed] = useState<{ client_id: string; secret: string } | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await fetch(
        `${API}/admin/clients${includeDisabled ? "?include_disabled=true" : ""}`,
        { credentials: "same-origin" },
      );
      if (!r.ok) throw new Error(`clients ${r.status}`);
      const data = await r.json();
      setClients(data.clients || []);
    } catch (e) {
      setStatus(`clients: ${(e as Error).message}`);
    }
  }, [includeDisabled, setStatus]);
  useEffect(() => { load(); }, [load, projectId]);

  const rotate = async (c: Client) => {
    if (!window.confirm(`Rotate secret for "${c.name}"? Existing services will need the new value.`)) return;
    try {
      const r = await fetch(`${API}/admin/clients/${encodeURIComponent(c.client_id)}/rotate`, {
        method: "POST", credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`rotate ${r.status}`);
      const data = await r.json();
      setRevealed({ client_id: c.client_id, secret: data.client_secret });
    } catch (e) {
      setStatus(`rotate: ${(e as Error).message}`);
    }
  };

  const disable = async (c: Client) => {
    if (!window.confirm(`Disable client "${c.name}"? Refresh tokens stop working immediately.`)) return;
    try {
      const r = await fetch(`${API}/admin/clients/${encodeURIComponent(c.client_id)}/disable`, {
        method: "POST", credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`disable ${r.status}`);
      await load();
    } catch (e) {
      setStatus(`disable: ${(e as Error).message}`);
    }
  };

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center gap-3">
        <button
          onClick={() => setCreateOpen(true)}
          className="px-3 py-1 text-sm bg-accent text-bg rounded font-medium"
        >+ New client</button>
        <label className="flex items-center gap-2 text-text-muted text-sm">
          <input
            type="checkbox"
            checked={includeDisabled}
            onChange={(e) => setIncludeDisabled(e.target.checked)}
          />
          Include disabled
        </label>
        <span className="ml-auto text-text-dim text-xs">{clients.length} client{clients.length === 1 ? "" : "s"}</span>
      </div>

      {clients.length === 0 ? (
        <EmptyState
          icon={<KeyIcon />}
          title="No OAuth clients yet"
          hint="Create one for each frontend or service that consumes auth — SPA, web app, mobile, or machine-to-machine."
        />
      ) : (
        <ul className="space-y-2">
          {clients.map((c) => (
            <ClientCard key={c.id} client={c} onRotate={() => rotate(c)} onDisable={() => disable(c)} />
          ))}
        </ul>
      )}

      {createOpen && (
        <CreateClientModal
          onClose={() => setCreateOpen(false)}
          onCreated={(client_id, secret) => {
            setCreateOpen(false);
            load();
            if (secret) setRevealed({ client_id, secret });
          }}
          setStatus={setStatus}
        />
      )}
      {revealed && (
        <SecretRevealModal
          clientId={revealed.client_id}
          secret={revealed.secret}
          onClose={() => setRevealed(null)}
        />
      )}
    </div>
  );
}

function ClientCard({ client, onRotate, onDisable }: {
  client: Client; onRotate: () => void; onDisable: () => void;
}) {
  const isPublic = client.type === "spa" || client.type === "native";
  return (
    <li className={
      "border border-border rounded bg-bg-card p-3 " +
      (client.disabled_at ? "opacity-60" : "")
    }>
      <div className="flex items-start gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-text font-medium">{client.name}</span>
            <ClientTypePill type={client.type} />
            {client.disabled_at && <Pill tone="muted">disabled</Pill>}
            {client.require_mfa && <Pill tone="ok">MFA required</Pill>}
            {client.require_pkce && <Pill tone="muted">PKCE</Pill>}
          </div>
          <div className="text-text-dim text-xs font-mono mt-1 truncate" title={client.client_id}>
            {client.client_id}
          </div>
          {client.redirect_uris && client.redirect_uris.length > 0 && (
            <div className="mt-2 text-xs">
              <span className="text-text-dim">Redirects:</span>{" "}
              <span className="text-text-muted">{client.redirect_uris.join(", ")}</span>
            </div>
          )}
          {client.allowed_grant_types && client.allowed_grant_types.length > 0 && (
            <div className="text-xs">
              <span className="text-text-dim">Grants:</span>{" "}
              <span className="text-text-muted">{client.allowed_grant_types.join(", ")}</span>
            </div>
          )}
          {client.jwt_audience && (
            <div className="text-xs">
              <span className="text-text-dim">Audience:</span>{" "}
              <span className="text-text-muted font-mono">{client.jwt_audience}</span>
            </div>
          )}
        </div>
        {!client.disabled_at && (
          <div className="flex flex-col gap-1 flex-shrink-0">
            {!isPublic && (
              <button
                onClick={onRotate}
                className="px-2 py-1 text-xs border border-border rounded text-text hover:bg-bg"
              >Rotate secret</button>
            )}
            <button
              onClick={onDisable}
              className="px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg"
            >Disable</button>
          </div>
        )}
      </div>
    </li>
  );
}

function ClientTypePill({ type }: { type: Client["type"] }) {
  const label = {
    spa: "SPA",
    web: "Web (confidential)",
    native: "Native",
    m2m: "Machine-to-machine",
  }[type];
  return <Pill tone="muted">{label}</Pill>;
}

function CreateClientModal({ onClose, onCreated, setStatus }: {
  onClose: () => void;
  onCreated: (clientId: string, secret?: string) => void;
  setStatus: (s: string) => void;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState<Client["type"]>("web");
  const [redirects, setRedirects] = useState("");
  const [audience, setAudience] = useState("");
  const [requireMFA, setRequireMFA] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        type,
        redirect_uris: redirects.split(/\s+|,/).map((s) => s.trim()).filter(Boolean),
        require_mfa: requireMFA,
        jwt_audience: audience.trim() || undefined,
      };
      const r = await fetch(`${API}/admin/clients`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const data = await r.json().catch(() => ({}));
        throw new Error(data.error || `create ${r.status}`);
      }
      const data = await r.json();
      onCreated(data.client_id, data.client_secret);
    } catch (err) {
      setStatus(`create: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose} title="Register OAuth client">
      <form onSubmit={submit} className="space-y-3">
        <Field label="Name">
          <input
            value={name} onChange={(e) => setName(e.target.value)}
            autoFocus required
            placeholder="e.g. Marketing Site"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          />
        </Field>
        <Field label="Type" hint={typeHint(type)}>
          <select
            value={type} onChange={(e) => setType(e.target.value as Client["type"])}
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          >
            <option value="web">web — server-rendered (gets client_secret)</option>
            <option value="spa">spa — single-page app (PKCE, no secret)</option>
            <option value="native">native — mobile or desktop (PKCE, no secret)</option>
            <option value="m2m">m2m — service-to-service (client credentials)</option>
          </select>
        </Field>
        {type !== "m2m" && (
          <Field label="Redirect URIs" hint="One per line, or comma-separated.">
            <textarea
              value={redirects} onChange={(e) => setRedirects(e.target.value)}
              rows={3}
              placeholder="https://app.example.com/auth/callback"
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
            />
          </Field>
        )}
        <Field label="JWT audience (optional)" hint="Defaults to client_id when blank.">
          <input
            value={audience} onChange={(e) => setAudience(e.target.value)}
            placeholder="api.example.com"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
          />
        </Field>
        <label className="flex items-center gap-2 text-sm text-text">
          <input type="checkbox" checked={requireMFA} onChange={(e) => setRequireMFA(e.target.checked)} />
          Require MFA for this client
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted hover:text-text">
            Cancel
          </button>
          <button
            type="submit" disabled={busy || !name.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
          >{busy ? "Creating…" : "Create client"}</button>
        </div>
      </form>
    </Modal>
  );
}

function typeHint(t: Client["type"]): string {
  switch (t) {
    case "web": return "Server-rendered app. Stores client_secret server-side.";
    case "spa": return "Browser-only app. Cannot keep a secret; uses PKCE.";
    case "native": return "Mobile or desktop. Cannot keep a secret; uses PKCE.";
    case "m2m": return "Service that authenticates as itself, not on behalf of a user.";
  }
}

function SecretRevealModal({ clientId, secret, onClose }: {
  clientId: string; secret: string; onClose: () => void;
}) {
  return (
    <Modal onClose={onClose} title="Client secret">
      <div className="space-y-3">
        <div className="text-warning text-sm">
          Copy this now — it will not be shown again.
        </div>
        <div>
          <div className="text-text-dim text-xs mb-1">client_id</div>
          <CopyBox value={clientId} />
        </div>
        <div>
          <div className="text-text-dim text-xs mb-1">client_secret</div>
          <CopyBox value={secret} mono />
        </div>
        <div className="flex justify-end">
          <button onClick={onClose} className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium">
            Done
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ─── Endpoints tab ───────────────────────────────────────────────────

function EndpointsTab({ projectId, setStatus }: {
  projectId: string; setStatus: (s: string) => void;
}) {
  const [info, setInfo] = useState<OIDCInfo | null>(null);
  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/oidc`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`oidc ${r.status}`);
      setInfo(await r.json());
    } catch (e) {
      setStatus(`oidc: ${(e as Error).message}`);
    }
  }, [setStatus]);
  useEffect(() => { load(); }, [load, projectId]);

  if (!info) return <div className="p-4 text-text-dim text-sm">Loading…</div>;
  const activeKeys = info.signing_keys.filter((k) => !k.retired_at);

  return (
    <div className="p-4 space-y-4">
      {!info.app_url_configured && (
        <div className="border border-warning rounded bg-warning/10 px-3 py-2 text-sm text-text">
          <strong className="text-warning">app_url not configured.</strong>{" "}
          The URLs below derive from the request host, which may not match
          your production domain. Set <code className="font-mono text-xs">app_url</code> in install settings.
        </div>
      )}

      <section className="border border-border rounded bg-bg-card">
        <header className="px-3 py-2 border-b border-border">
          <h3 className="text-sm font-semibold text-text">Discovery URLs</h3>
        </header>
        <div className="p-3 space-y-3">
          <UrlField label="Issuer" value={info.issuer} />
          <UrlField label="OpenID configuration" value={info.openid_configuration} />
          <UrlField label="JWKS" value={info.jwks_uri} />
          <UrlField label="Token endpoint" value={info.token_endpoint} />
          <UrlField label="Userinfo endpoint" value={info.userinfo_endpoint} />
        </div>
      </section>

      <section className="border border-border rounded bg-bg-card">
        <header className="px-3 py-2 border-b border-border">
          <h3 className="text-sm font-semibold text-text">
            Signing keys ({activeKeys.length} active, {info.signing_keys.length - activeKeys.length} retired)
          </h3>
        </header>
        <ul className="divide-y divide-border">
          {info.signing_keys.map((k) => (
            <li key={k.kid} className="px-3 py-2 flex items-center gap-3 text-sm">
              <span className="font-mono text-xs text-text truncate flex-1" title={k.kid}>{k.kid}</span>
              <span className="text-text-muted text-xs">{k.alg}</span>
              <span className="text-text-dim text-xs">created {relTime(k.created_at)}</span>
              {k.retired_at
                ? <Pill tone="muted">retired</Pill>
                : <Pill tone="ok">active</Pill>}
            </li>
          ))}
        </ul>
      </section>

      <section className="border border-border rounded bg-bg-card">
        <header className="px-3 py-2 border-b border-border">
          <h3 className="text-sm font-semibold text-text">Policy</h3>
        </header>
        <DetailGrid columns={2}>
          <DetailRow label="Email verification" value={info.verification_required ? "required" : "optional"} />
          <DetailRow label="Magic-link login" value={info.magic_link_enabled ? "enabled" : "disabled"} />
          <DetailRow label="Access token TTL" value={`${info.access_ttl_seconds}s (${Math.round(info.access_ttl_seconds / 60)} min)`} />
          <DetailRow label="Refresh token TTL" value={`${info.refresh_ttl_days} days`} />
          <DetailRow label="Password min length" value={`${info.password_min_length} chars`} />
          <DetailRow label="Password classes required" value={`${info.password_classes} of {lower, upper, digit, symbol}`} />
          <DetailRow label="Lockout threshold" value={info.lockout_threshold > 0 ? `${info.lockout_threshold} fails` : "disabled"} />
          <DetailRow label="Initial lockout" value={`${info.lockout_initial_minutes} min (doubles)`} />
        </DetailGrid>
        <div className="px-3 py-2 border-t border-border text-text-dim text-xs">
          Policy values are read from install settings. Change them in the dashboard's Install Settings panel.
        </div>
      </section>
    </div>
  );
}

// ─── Shared UI primitives ────────────────────────────────────────────

function Pill({ tone, children }: { tone: "ok" | "warn" | "muted"; children: React.ReactNode }) {
  const cls = {
    ok: "bg-accent/15 text-accent border-accent/30",
    warn: "bg-warning/15 text-warning border-warning/30",
    muted: "bg-bg text-text-muted border-border",
  }[tone];
  return <span className={`text-xs px-1.5 py-0.5 rounded border ${cls}`}>{children}</span>;
}

function EventBadge({ event }: { event: string }) {
  const tone = eventTone(event);
  return <span
    className={"inline-block w-1.5 h-1.5 rounded-full flex-shrink-0 " + tone}
    aria-hidden="true"
  />;
}

function eventTone(event: string): string {
  if (event.includes("failed") || event.includes("locked") || event.includes("disabled")) return "bg-warning";
  if (event === "login" || event === "signup" || event === "client_created") return "bg-accent";
  return "bg-text-dim";
}

function prettyEvent(e: string): string {
  return e.replace(/_/g, " ");
}

function relTime(iso: string | undefined): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  if (diff < 7 * 86_400_000) return `${Math.floor(diff / 86_400_000)}d ago`;
  return new Date(t).toLocaleDateString();
}

function rfc(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toLocaleString();
}

function EmptyState({ icon, title, hint }: {
  icon: React.ReactNode; title: string; hint: string;
}) {
  return (
    <div className="px-6 py-10 text-center">
      <div className="inline-flex items-center justify-center mb-3 text-text-dim">{icon}</div>
      <div className="text-text font-medium mb-1">{title}</div>
      <div className="text-text-dim text-sm max-w-md mx-auto">{hint}</div>
    </div>
  );
}

function DetailGrid({ children, columns = 1 }: { children: React.ReactNode; columns?: 1 | 2 }) {
  return (
    <div
      className={columns === 2 ? "grid grid-cols-2 gap-x-4" : "grid grid-cols-1"}
    >
      {children}
    </div>
  );
}

function DetailRow({ label, value, tone }: { label: string; value: React.ReactNode; tone?: "warn" }) {
  return (
    <div className="flex justify-between gap-3 py-1.5 px-3 border-b border-border last:border-b-0 text-sm">
      <span className="text-text-dim">{label}</span>
      <span className={tone === "warn" ? "text-warning text-right" : "text-text text-right"}>{value}</span>
    </div>
  );
}

function Field({ label, hint, children }: {
  label: string; hint?: string; children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="text-text text-sm font-medium mb-1">{label}</div>
      {children}
      {hint && <div className="text-text-dim text-xs mt-1">{hint}</div>}
    </label>
  );
}

function SearchInput({ value, onChange, placeholder }: {
  value: string; onChange: (s: string) => void; placeholder?: string;
}) {
  return (
    <div className="flex-1 relative">
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full bg-bg-input border border-border rounded pl-7 pr-2 py-1 text-sm text-text"
      />
      <span className="absolute left-2 top-1/2 -translate-y-1/2 text-text-dim pointer-events-none">
        <SearchIcon />
      </span>
    </div>
  );
}

function CopyBox({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-stretch border border-border rounded overflow-hidden">
      <input
        readOnly
        value={value}
        className={
          "flex-1 bg-bg-input px-2 py-1.5 text-sm text-text truncate " +
          (mono ? "font-mono" : "")
        }
      />
      <button
        type="button"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {}
        }}
        className="px-3 text-xs bg-bg-card text-text-muted hover:text-text border-l border-border"
      >{copied ? "Copied" : "Copy"}</button>
    </div>
  );
}

function UrlField({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-text-dim text-xs mb-1">{label}</div>
      <CopyBox value={value} mono />
    </div>
  );
}

function Modal({ title, children, onClose }: {
  title: string; children: React.ReactNode; onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div
        ref={ref}
        style={{ width: 480, maxWidth: "90vw", maxHeight: "85vh" }}
        className="bg-bg border border-border rounded shadow-lg overflow-auto"
      >
        <header className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h3 className="text-text font-semibold">{title}</h3>
          <button onClick={onClose} className="text-text-dim hover:text-text" title="Close">
            <CloseIcon />
          </button>
        </header>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

// ─── Icons (inline SVG; no Tailwind colour classes — use currentColor) ───

function KeyIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M15 2a7 7 0 0 0-6.93 8L2 16v4h4l1-1v-2h2v-2h2l3.07-3.07A7 7 0 1 0 15 2z" />
      <circle cx="16.5" cy="7.5" r="1.5" fill="currentColor" />
    </svg>
  );
}

function UsersIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M23 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  );
}

function ListIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <line x1="8" y1="6" x2="21" y2="6" />
      <line x1="8" y1="12" x2="21" y2="12" />
      <line x1="8" y1="18" x2="21" y2="18" />
      <line x1="3" y1="6" x2="3.01" y2="6" />
      <line x1="3" y1="12" x2="3.01" y2="12" />
      <line x1="3" y1="18" x2="3.01" y2="18" />
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="11" cy="11" r="7" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <line x1="18" y1="6" x2="6" y2="18" />
      <line x1="6" y1="6" x2="18" y2="18" />
    </svg>
  );
}
