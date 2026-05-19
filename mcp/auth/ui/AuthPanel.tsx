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
  organization_id: number;
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
  organization_id: number;
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

type Tab = "overview" | "organizations" | "users" | "clients" | "endpoints";

interface Organization {
  id: number;
  slug: string;
  name: string;
  color?: string;
  status: string;
  policy_overrides?: string;
  created_at?: string;
  updated_at?: string;
}

// orgQS — builds `?organization_slug=<slug>` (or appends to an existing
// query). When orgSlug is null we're in the "All organizations" rollup
// view and the server returns project-wide data.
function orgQS(slug: string | null, extra?: string): string {
  const params = new URLSearchParams();
  if (slug) params.set("organization_slug", slug);
  if (extra) {
    for (const [k, v] of new URLSearchParams(extra)) params.set(k, v);
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

// ─── Panel root ──────────────────────────────────────────────────────

export default function AuthPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("overview");
  const [stats, setStats] = useState<Stats | null>(null);
  const [status, setStatus] = useState("");
  const [orgs, setOrgs] = useState<Organization[]>([]);
  const [activeOrgSlug, setActiveOrgSlug] = useState<string | null>(() => {
    try {
      return localStorage.getItem(`auth.activeOrg.${projectId || ""}`);
    } catch {
      return null;
    }
  });

  // Persist active-org selection per project.
  useEffect(() => {
    try {
      if (activeOrgSlug == null) {
        localStorage.removeItem(`auth.activeOrg.${projectId || ""}`);
      } else {
        localStorage.setItem(`auth.activeOrg.${projectId || ""}`, activeOrgSlug);
      }
    } catch {}
  }, [activeOrgSlug, projectId]);

  const loadOrgs = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/organizations`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`orgs ${r.status}`);
      const data = await r.json();
      setOrgs(data.organizations || []);
    } catch (e) {
      setStatus(`orgs: ${(e as Error).message}`);
    }
  }, []);

  const loadStats = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/stats${orgQS(activeOrgSlug)}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`stats ${r.status}`);
      setStats(await r.json());
    } catch (e) {
      setStatus(`stats: ${(e as Error).message}`);
    }
  }, [activeOrgSlug]);

  useEffect(() => { loadOrgs(); }, [loadOrgs, projectId]);
  useEffect(() => { loadStats(); }, [loadStats]);

  // Users tab needs a specific org for create/edit. When user picks
  // "All" we still show a read-only flat list, but disable mutations.

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-1 border-b border-border px-4 py-2">
        <div className="flex items-center gap-2 mr-2">
          <KeyIcon />
          <span className="font-semibold text-text">Auth</span>
        </div>
        <OrgSwitcher
          orgs={orgs}
          activeSlug={activeOrgSlug}
          onSelect={setActiveOrgSlug}
          onManage={() => setTab("organizations")}
        />
        <span className="w-px h-5 bg-border mx-2" />
        <NavTab label="Overview" value="overview" current={tab} onClick={setTab} />
        <NavTab label="Organizations" value="organizations" current={tab} onClick={setTab} count={orgs.length} />
        <NavTab label="Users" value="users" current={tab} onClick={setTab} count={stats ? stats.active + stats.disabled : undefined} />
        <NavTab label="Clients" value="clients" current={tab} onClick={setTab} />
        <NavTab label="Endpoints" value="endpoints" current={tab} onClick={setTab} />
        <span className="ml-auto text-text-dim text-xs truncate max-w-xs" title={status}>{status}</span>
      </header>

      <div className="flex-1 overflow-auto">
        {tab === "overview" && (
          <OverviewTab
            stats={stats}
            orgs={orgs}
            activeOrgSlug={activeOrgSlug}
            projectId={projectId}
            setStatus={setStatus}
          />
        )}
        {tab === "organizations" && (
          <OrganizationsTab
            orgs={orgs}
            activeOrgSlug={activeOrgSlug}
            onSelect={setActiveOrgSlug}
            onChanged={() => { loadOrgs(); loadStats(); }}
            setStatus={setStatus}
          />
        )}
        {tab === "users" && (
          <UsersTab
            activeOrgSlug={activeOrgSlug}
            orgs={orgs}
            projectId={projectId}
            setStatus={setStatus}
            onUsersChanged={loadStats}
          />
        )}
        {tab === "clients" && (
          <ClientsTab
            activeOrgSlug={activeOrgSlug}
            orgs={orgs}
            projectId={projectId}
            setStatus={setStatus}
          />
        )}
        {tab === "endpoints" && (
          <EndpointsTab
            activeOrgSlug={activeOrgSlug}
            onSelectOrg={setActiveOrgSlug}
            orgs={orgs}
            projectId={projectId}
            setStatus={setStatus}
          />
        )}
      </div>
    </div>
  );
}

// ─── OrgSwitcher (header dropdown) ───────────────────────────────────

function OrgSwitcher({ orgs, activeSlug, onSelect, onManage }: {
  orgs: Organization[];
  activeSlug: string | null;
  onSelect: (s: string | null) => void;
  onManage: () => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", onDoc);
    return () => window.removeEventListener("mousedown", onDoc);
  }, [open]);
  const active = orgs.find((o) => o.slug === activeSlug) || null;
  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 px-2 py-1 text-sm rounded hover:bg-bg-card transition-colors"
        title={active ? `Organization: ${active.name}` : "All organizations"}
      >
        <span
          className="w-2 h-2 rounded-full"
          style={{ backgroundColor: active?.color || "#94a3b8" }}
        />
        <span className="text-text font-medium">{active ? active.name : "All organizations"}</span>
        <span className="text-text-dim text-xs">▾</span>
      </button>
      {open && (
        <div className="absolute left-0 top-full mt-1 w-64 z-40 bg-bg-card border border-border rounded shadow-lg py-1">
          <button
            onClick={() => { onSelect(null); setOpen(false); }}
            className={
              "w-full text-left flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-bg-input/50 " +
              (activeSlug == null ? "text-accent" : "text-text")
            }
          >
            <span className="w-2 h-2 rounded-full bg-text-dim" />
            <span className="flex-1">All organizations</span>
            <span className="text-text-dim text-xs">project rollup</span>
          </button>
          {orgs.length > 0 && <div className="border-t border-border my-1" />}
          {orgs.map((o) => (
            <button
              key={o.id}
              onClick={() => { onSelect(o.slug); setOpen(false); }}
              className={
                "w-full text-left flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-bg-input/50 " +
                (o.slug === activeSlug ? "text-accent" : "text-text")
              }
              title={o.slug}
            >
              <span
                className="w-2 h-2 rounded-full flex-shrink-0"
                style={{ backgroundColor: o.color || "#94a3b8" }}
              />
              <span className="flex-1 truncate">{o.name}</span>
              {o.status === "archived" && <span className="text-text-dim text-xs">archived</span>}
              {o.slug === "default" && <span className="text-text-dim text-xs">default</span>}
            </button>
          ))}
          <div className="border-t border-border my-1" />
          <button
            onClick={() => { onManage(); setOpen(false); }}
            className="w-full text-left px-3 py-1.5 text-sm text-text-muted hover:bg-bg-input/50 hover:text-text"
          >
            Manage organizations…
          </button>
        </div>
      )}
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

function OverviewTab({ stats, orgs, activeOrgSlug, projectId, setStatus }: {
  stats: Stats | null;
  orgs: Organization[];
  activeOrgSlug: string | null;
  projectId: string;
  setStatus: (s: string) => void;
}) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const loadAudit = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/audit${orgQS(activeOrgSlug, "limit=25")}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`audit ${r.status}`);
      const data = await r.json();
      setEvents(data.events || []);
    } catch (e) {
      setStatus(`audit: ${(e as Error).message}`);
    }
  }, [activeOrgSlug, setStatus]);
  useEffect(() => { loadAudit(); }, [loadAudit, projectId]);
  const active = orgs.find((o) => o.slug === activeOrgSlug) || null;

  return (
    <div className="p-4 space-y-4">
      <div className="text-text-dim text-xs">
        {active
          ? <>Scope: <span className="text-text">{active.name}</span></>
          : <>Scope: <span className="text-text">All organizations</span> (project-wide rollup)</>}
      </div>
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

// ─── Organizations tab ───────────────────────────────────────────────

function OrganizationsTab({ orgs, activeOrgSlug, onSelect, onChanged, setStatus }: {
  orgs: Organization[];
  activeOrgSlug: string | null;
  onSelect: (s: string | null) => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
}) {
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Organization | null>(null);

  const archive = async (o: Organization) => {
    if (o.slug === "default") return;
    if (!window.confirm(`Archive organization "${o.name}"? Users and clients remain but new signups via that org's client_ids fail.`)) return;
    try {
      const r = await fetch(`${API}/admin/organizations/${o.id}/archive`, {
        method: "POST", credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`archive ${r.status}`);
      onChanged();
      if (activeOrgSlug === o.slug) onSelect(null);
    } catch (e) {
      setStatus(`archive: ${(e as Error).message}`);
    }
  };

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center gap-3">
        <button
          onClick={() => setCreateOpen(true)}
          className="px-3 py-1 text-sm bg-accent text-bg rounded font-medium"
        >+ New organization</button>
        <span className="text-text-dim text-sm">
          Each organization is its own user pool — separate users, OAuth clients, signing keys, and JWKS.
        </span>
      </div>

      {orgs.length === 0 ? (
        <EmptyState
          icon={<BuildingIcon />}
          title="No organizations yet"
          hint="Create one to start partitioning users by SaaS product."
        />
      ) : (
        <ul className="space-y-2">
          {orgs.map((o) => (
            <li
              key={o.id}
              className={
                "border rounded bg-bg-card px-3 py-2 flex items-center gap-3 " +
                (o.slug === activeOrgSlug ? "border-accent" : "border-border")
              }
            >
              <span className="w-2.5 h-2.5 rounded-full flex-shrink-0" style={{ backgroundColor: o.color || "#94a3b8" }} />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-text font-medium">{o.name}</span>
                  <span className="text-text-dim text-xs font-mono">{o.slug}</span>
                  {o.slug === "default" && <Pill tone="muted">default</Pill>}
                  {o.status === "archived" && <Pill tone="warn">archived</Pill>}
                  {o.slug === activeOrgSlug && <Pill tone="ok">selected</Pill>}
                </div>
                <div className="text-text-dim text-xs">
                  created {o.created_at ? relTime(o.created_at) : "—"}
                </div>
              </div>
              <div className="flex items-center gap-1 flex-shrink-0">
                {o.slug !== activeOrgSlug && (
                  <button
                    onClick={() => onSelect(o.slug)}
                    className="px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg"
                  >Select</button>
                )}
                <button
                  onClick={() => setEditing(o)}
                  className="px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg"
                >Edit</button>
                {o.status === "active" && o.slug !== "default" && (
                  <button
                    onClick={() => archive(o)}
                    className="px-2 py-1 text-xs border border-border rounded text-text-muted hover:text-text hover:bg-bg"
                  >Archive</button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      {createOpen && (
        <CreateOrgModal
          existingSlugs={orgs.map((o) => o.slug)}
          onClose={() => setCreateOpen(false)}
          onCreated={(slug) => {
            setCreateOpen(false);
            onChanged();
            onSelect(slug);
          }}
          setStatus={setStatus}
        />
      )}
      {editing && (
        <EditOrgModal
          org={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            onChanged();
          }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function CreateOrgModal({ existingSlugs, onClose, onCreated, setStatus }: {
  existingSlugs: string[];
  onClose: () => void;
  onCreated: (slug: string) => void;
  setStatus: (s: string) => void;
}) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [color, setColor] = useState("#3b82f6");
  const [busy, setBusy] = useState(false);

  // Auto-derive slug from name as the user types — common UX.
  const onNameChange = (v: string) => {
    setName(v);
    if (!slug || slug === slugify(name)) setSlug(slugify(v));
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const s = slug.trim().toLowerCase();
    if (!s || !name.trim()) return;
    if (existingSlugs.includes(s)) {
      setStatus(`slug "${s}" already in use`);
      return;
    }
    setBusy(true);
    try {
      const r = await fetch(`${API}/admin/organizations`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug: s, name: name.trim(), color }),
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || `create ${r.status}`);
      onCreated(data.organization.slug);
    } catch (err) {
      setStatus(`create org: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose} title="New organization">
      <form onSubmit={submit} className="space-y-3">
        <Field label="Name">
          <input
            value={name} onChange={(e) => onNameChange(e.target.value)}
            autoFocus required
            placeholder="e.g. Acme Corp"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          />
        </Field>
        <Field label="Slug" hint="Used in URLs, JWT iss, and tools. Lowercase letters/digits/hyphens, 3–32 chars.">
          <input
            value={slug} onChange={(e) => setSlug(e.target.value)}
            required minLength={3} maxLength={32}
            pattern="^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$"
            placeholder="acme"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
          />
        </Field>
        <Field label="Color">
          <input
            type="color" value={color} onChange={(e) => setColor(e.target.value)}
            style={{ width: 48, height: 32 }}
            className="bg-bg-input border border-border rounded cursor-pointer"
          />
        </Field>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted hover:text-text">Cancel</button>
          <button
            type="submit" disabled={busy || !slug.trim() || !name.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
          >{busy ? "Creating…" : "Create organization"}</button>
        </div>
      </form>
    </Modal>
  );
}

function EditOrgModal({ org, onClose, onSaved, setStatus }: {
  org: Organization;
  onClose: () => void;
  onSaved: () => void;
  setStatus: (s: string) => void;
}) {
  const [name, setName] = useState(org.name);
  const [color, setColor] = useState(org.color || "#94a3b8");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await fetch(`${API}/admin/organizations/${org.id}`, {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), color }),
      });
      if (!r.ok) {
        const data = await r.json().catch(() => ({}));
        throw new Error(data.error || `update ${r.status}`);
      }
      onSaved();
    } catch (err) {
      setStatus(`update org: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose} title={`Edit ${org.slug}`}>
      <form onSubmit={submit} className="space-y-3">
        <Field label="Name">
          <input
            value={name} onChange={(e) => setName(e.target.value)} required
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          />
        </Field>
        <Field label="Slug">
          <input
            value={org.slug} disabled
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim font-mono opacity-60"
          />
          <div className="text-text-dim text-xs mt-1">Slug is immutable — it's baked into issued JWTs.</div>
        </Field>
        <Field label="Color">
          <input
            type="color" value={color} onChange={(e) => setColor(e.target.value)}
            style={{ width: 48, height: 32 }}
            className="bg-bg-input border border-border rounded cursor-pointer"
          />
        </Field>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted hover:text-text">Cancel</button>
          <button
            type="submit" disabled={busy || !name.trim()}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
          >{busy ? "Saving…" : "Save"}</button>
        </div>
      </form>
    </Modal>
  );
}

function slugify(s: string): string {
  return s.toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}

// ─── Users tab ───────────────────────────────────────────────────────

function UsersTab({ activeOrgSlug, orgs, projectId, setStatus, onUsersChanged }: {
  activeOrgSlug: string | null;
  orgs: Organization[];
  projectId: string;
  setStatus: (s: string) => void;
  onUsersChanged: () => void;
}) {
  const [users, setUsers] = useState<User[]>([]);
  const [q, setQ] = useState("");
  const [statusFilter, setStatusFilter] = useState<"" | "active" | "disabled">("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (activeOrgSlug) params.set("organization_slug", activeOrgSlug);
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
  }, [activeOrgSlug, q, statusFilter, setStatus]);

  useEffect(() => { load(); }, [load, projectId]);

  // When user clicks an "All" row, the drawer needs the user's own org —
  // it lives on the user row. The drawer takes orgSlug as a prop and
  // uses it for all its own queries.
  const orgByID = new Map(orgs.map((o) => [o.id, o]));
  const selectedUser = users.find((u) => u.id === selectedId) || null;
  const selectedOrg = selectedUser
    ? orgByID.get(selectedUser.organization_id) || orgs.find((o) => o.slug === activeOrgSlug) || null
    : null;

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
          <button
            onClick={() => setCreateOpen(true)}
            disabled={!activeOrgSlug}
            title={activeOrgSlug ? "Create user" : "Pick an organization first to create a user"}
            className="ml-auto inline-flex items-center gap-1.5 px-3 py-1 text-sm bg-accent text-bg rounded font-medium disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <UserPlusIcon /> New user
          </button>
          <span className="text-text-dim text-xs">{users.length} user{users.length === 1 ? "" : "s"}</span>
        </div>

        {users.length === 0 ? (
          <EmptyState
            icon={<UsersIcon />}
            title={q || statusFilter ? "No matching users" : "No users yet"}
            hint={q || statusFilter
              ? "Try clearing the filters."
              : activeOrgSlug
                ? "Users appear once your SaaS frontend has signups against /signup, or once you click \"+ New user\"."
                : "No users in any organization yet. Pick an organization above to add one."}
          />
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide">
              <tr className="border-b border-border">
                <th className="text-left px-3 py-2 font-normal">Email</th>
                {!activeOrgSlug && <th className="text-left px-3 py-2 font-normal">Org</th>}
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
                  {!activeOrgSlug && (
                    <td className="px-3 py-2">
                      {(() => {
                        const o = orgByID.get(u.organization_id);
                        if (!o) return <span className="text-text-dim text-xs">—</span>;
                        return (
                          <span className="inline-flex items-center gap-1.5 text-xs text-text-muted">
                            <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: o.color || "#94a3b8" }} />
                            {o.name}
                          </span>
                        );
                      })()}
                    </td>
                  )}
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

      {selectedId != null && selectedOrg != null && (
        <UserDrawer
          userId={selectedId}
          orgSlug={selectedOrg.slug}
          projectId={projectId}
          onClose={() => setSelectedId(null)}
          onChanged={() => { load(); onUsersChanged(); }}
          setStatus={setStatus}
        />
      )}

      {createOpen && activeOrgSlug && (
        <CreateUserModal
          orgSlug={activeOrgSlug}
          onClose={() => setCreateOpen(false)}
          onCreated={(userId, opened) => {
            setCreateOpen(false);
            load();
            onUsersChanged();
            if (opened) setSelectedId(userId);
          }}
          setStatus={setStatus}
        />
      )}
    </div>
  );
}

function CreateUserModal({ orgSlug, onClose, onCreated, setStatus }: {
  orgSlug: string;
  onClose: () => void;
  onCreated: (userId: number, openDrawer: boolean) => void;
  setStatus: (s: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [mode, setMode] = useState<"invite" | "password">("invite");
  const [password, setPassword] = useState("");
  const [emailVerified, setEmailVerified] = useState(true);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim()) return;
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        email_verified: emailVerified,
      };
      if (mode === "password") {
        body.password = password;
      }
      const r = await fetch(`${API}/admin/users${orgQS(orgSlug)}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || `create ${r.status}`);
      onCreated(data.user.id, true);
    } catch (err) {
      setStatus(`create: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose} title="New user">
      <form onSubmit={submit} className="space-y-3">
        <Field label="Email">
          <input
            type="email" value={email} onChange={(e) => setEmail(e.target.value)}
            autoFocus required
            placeholder="alice@example.com"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          />
        </Field>
        <Field label="Display name (optional)">
          <input
            value={displayName} onChange={(e) => setDisplayName(e.target.value)}
            placeholder="Alice"
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
          />
        </Field>
        <Field label="Initial credential" hint={
          mode === "invite"
            ? "Issues a one-time password-reset link the user follows to set their own password."
            : "Sets a password directly. Use for service accounts or test fixtures."
        }>
          <div className="flex gap-1">
            <ModeButton active={mode === "invite"} onClick={() => setMode("invite")}>Send reset link</ModeButton>
            <ModeButton active={mode === "password"} onClick={() => setMode("password")}>Set password</ModeButton>
          </div>
        </Field>
        {mode === "password" && (
          <Field label="Password" hint="Must satisfy the install's password policy.">
            <input
              type="text" value={password} onChange={(e) => setPassword(e.target.value)}
              required minLength={8}
              placeholder="at least 12 chars by default"
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text font-mono"
            />
          </Field>
        )}
        <label className="flex items-center gap-2 text-sm text-text">
          <input type="checkbox" checked={emailVerified} onChange={(e) => setEmailVerified(e.target.checked)} />
          Mark email as already verified
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted hover:text-text">
            Cancel
          </button>
          <button
            type="submit" disabled={busy || !email.trim() || (mode === "password" && !password)}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
          >{busy ? "Creating…" : "Create user"}</button>
        </div>
      </form>
    </Modal>
  );
}

function ModeButton({ active, onClick, children }: {
  active: boolean; onClick: () => void; children: React.ReactNode;
}) {
  return (
    <button
      type="button" onClick={onClick}
      className={
        "flex-1 px-2 py-1.5 text-sm rounded border " +
        (active
          ? "border-accent bg-accent/15 text-accent"
          : "border-border bg-bg-input text-text-muted hover:text-text")
      }
    >{children}</button>
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

function UserDrawer({ userId, orgSlug, projectId, onClose, onChanged, setStatus }: {
  userId: number;
  orgSlug: string;
  projectId: string;
  onClose: () => void;
  onChanged: () => void;
  setStatus: (s: string) => void;
}) {
  const [data, setData] = useState<{ user: User; sessions: Session[]; audit_log: AuditEvent[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [editingName, setEditingName] = useState(false);
  const [nameDraft, setNameDraft] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/admin/users/${userId}/context${orgQS(orgSlug)}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`user ${r.status}`);
      setData(await r.json());
    } catch (e) {
      setStatus(`user: ${(e as Error).message}`);
    }
  }, [userId, orgSlug, setStatus]);
  useEffect(() => { load(); }, [load, projectId]);

  const act = async (path: string, body?: unknown, method: "POST" | "PATCH" = "POST") => {
    setBusy(true);
    try {
      const url = path
        ? `${API}/admin/users/${userId}/${path}${orgQS(orgSlug)}`
        : `${API}/admin/users/${userId}${orgQS(orgSlug)}`;
      const r = await fetch(url, {
        method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body ?? {}),
      });
      if (!r.ok) {
        const errBody = await r.json().catch(() => ({}));
        throw new Error(errBody.error || `${path || "patch"} ${r.status}`);
      }
      await load();
      onChanged();
    } catch (e) {
      setStatus(`${path || "patch"}: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const saveName = async () => {
    await act("", { display_name: nameDraft }, "PATCH");
    setEditingName(false);
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
          <button
            onClick={() => act("send_password_reset")}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded text-text hover:bg-bg-card disabled:opacity-50"
            title="Issues a fresh reset token; link is emailed (when messaging is installed) or written to the audit log."
          >Send password reset</button>
          {!u.email_verified_at && (
            <button
              onClick={() => act("", { email_verified: true }, "PATCH")}
              disabled={busy}
              className="px-3 py-1 text-sm border border-border rounded text-text hover:bg-bg-card disabled:opacity-50"
            >Mark verified</button>
          )}
        </div>

        <DetailGrid>
          <div className="flex justify-between gap-3 py-1.5 px-3 border-b border-border text-sm items-center">
            <span className="text-text-dim">Display name</span>
            {editingName ? (
              <span className="flex items-center gap-1">
                <input
                  value={nameDraft}
                  onChange={(e) => setNameDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") saveName();
                    if (e.key === "Escape") setEditingName(false);
                  }}
                  autoFocus
                  className="bg-bg-input border border-border rounded px-2 py-0.5 text-sm text-text"
                  style={{ width: 180 }}
                />
                <button
                  onClick={saveName} disabled={busy}
                  className="text-accent text-xs px-1.5 hover:underline disabled:opacity-50"
                >Save</button>
                <button
                  onClick={() => setEditingName(false)}
                  className="text-text-dim text-xs px-1.5 hover:text-text"
                >Cancel</button>
              </span>
            ) : (
              <span className="flex items-center gap-2">
                <span className="text-text text-right">{u.display_name || "—"}</span>
                <button
                  onClick={() => { setNameDraft(u.display_name || ""); setEditingName(true); }}
                  className="text-text-dim hover:text-text text-xs"
                >Edit</button>
              </span>
            )}
          </div>
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

function ClientsTab({ activeOrgSlug, orgs, projectId, setStatus }: {
  activeOrgSlug: string | null;
  orgs: Organization[];
  projectId: string;
  setStatus: (s: string) => void;
}) {
  const [clients, setClients] = useState<Client[]>([]);
  const [includeDisabled, setIncludeDisabled] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [revealed, setRevealed] = useState<{ client_id: string; secret: string } | null>(null);

  const load = useCallback(async () => {
    try {
      const extra = includeDisabled ? "include_disabled=true" : "";
      const r = await fetch(`${API}/admin/clients${orgQS(activeOrgSlug, extra)}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`clients ${r.status}`);
      const data = await r.json();
      setClients(data.clients || []);
    } catch (e) {
      setStatus(`clients: ${(e as Error).message}`);
    }
  }, [activeOrgSlug, includeDisabled, setStatus]);
  useEffect(() => { load(); }, [load, projectId]);
  const orgByID = new Map(orgs.map((o) => [o.id, o]));

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
          title="Register a new OAuth client"
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
          hint="Create one for each frontend or service that consumes auth — SPA, web, mobile, or M2M. Bind to one organization, or leave multi-organization to serve many."
        />
      ) : (
        <ul className="space-y-2">
          {clients.map((c) => (
            <ClientCard
              key={c.id}
              client={c}
              org={c.organization_id ? orgByID.get(c.organization_id) : null}
              showOrg={!activeOrgSlug || !c.organization_id}
              onRotate={() => rotate(c)}
              onDisable={() => disable(c)}
            />
          ))}
        </ul>
      )}

      {createOpen && (
        <CreateClientModal
          defaultOrgSlug={activeOrgSlug}
          orgs={orgs}
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

function ClientCard({ client, org, showOrg, onRotate, onDisable }: {
  client: Client;
  org: Organization | null | undefined;
  showOrg: boolean;
  onRotate: () => void;
  onDisable: () => void;
}) {
  const isPublic = client.type === "spa" || client.type === "native";
  const isMultiOrg = !client.organization_id;
  return (
    <li className={
      "border border-border rounded bg-bg-card p-3 " +
      (client.disabled_at ? "opacity-60" : "")
    }>
      <div className="flex items-start gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-text font-medium">{client.name}</span>
            <ClientTypePill type={client.type} />
            {isMultiOrg ? (
              <Pill tone="ok">multi-organization</Pill>
            ) : (
              showOrg && org && (
                <span className="inline-flex items-center gap-1.5 text-xs text-text-muted">
                  <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: org.color || "#94a3b8" }} />
                  {org.name}
                </span>
              )
            )}
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

function CreateClientModal({ defaultOrgSlug, orgs, onClose, onCreated, setStatus }: {
  defaultOrgSlug: string | null;
  orgs: Organization[];
  onClose: () => void;
  onCreated: (clientId: string, secret?: string) => void;
  setStatus: (s: string) => void;
}) {
  const [name, setName] = useState("");
  const [type, setType] = useState<Client["type"]>("web");
  // scope: "single" → bound to one org (the v0.4.0 default).
  //        "multi"  → no org binding; SaaS sends organization_slug on
  //                   every public call (Auth0 Organizations pattern).
  const [scope, setScope] = useState<"single" | "multi">(defaultOrgSlug ? "single" : "multi");
  const [orgSlug, setOrgSlug] = useState<string>(defaultOrgSlug || (orgs[0]?.slug ?? ""));
  const [redirects, setRedirects] = useState("");
  const [audience, setAudience] = useState("");
  const [requireMFA, setRequireMFA] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    if (scope === "single" && !orgSlug) return;
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        type,
        redirect_uris: redirects.split(/\s+|,/).map((s) => s.trim()).filter(Boolean),
        require_mfa: requireMFA,
        jwt_audience: audience.trim() || undefined,
      };
      // scope=single → ?organization_slug=… binds the client to that org.
      // scope=multi  → no org query → server creates a multi-org client.
      const qs = scope === "single" ? orgQS(orgSlug) : "";
      const r = await fetch(`${API}/admin/clients${qs}`, {
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
        <Field label="Scope" hint={
          scope === "single"
            ? "Bind this client to one organization. The SaaS frontend sends only client_id; the server derives the org. Default."
            : "One client serves all organizations. The SaaS frontend MUST send organization_slug on every /signup, /login, /refresh call. Use when one deployment hosts many customer orgs."
        }>
          <div className="flex gap-1">
            <ModeButton active={scope === "single"} onClick={() => setScope("single")}>One organization</ModeButton>
            <ModeButton active={scope === "multi"} onClick={() => setScope("multi")}>Multi-organization</ModeButton>
          </div>
        </Field>
        {scope === "single" && (
          <Field label="Organization">
            <select
              value={orgSlug} onChange={(e) => setOrgSlug(e.target.value)}
              required
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text"
            >
              {orgs.filter((o) => o.status === "active").map((o) => (
                <option key={o.id} value={o.slug}>{o.name} ({o.slug})</option>
              ))}
            </select>
          </Field>
        )}
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

function EndpointsTab({ activeOrgSlug, onSelectOrg, orgs, projectId, setStatus }: {
  activeOrgSlug: string | null;
  onSelectOrg: (s: string | null) => void;
  orgs: Organization[];
  projectId: string;
  setStatus: (s: string) => void;
}) {
  const [info, setInfo] = useState<OIDCInfo | null>(null);
  const load = useCallback(async () => {
    if (!activeOrgSlug) return;
    try {
      const r = await fetch(`${API}/admin/oidc${orgQS(activeOrgSlug)}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`oidc ${r.status}`);
      setInfo(await r.json());
    } catch (e) {
      setStatus(`oidc: ${(e as Error).message}`);
    }
  }, [activeOrgSlug, setStatus]);
  useEffect(() => { load(); }, [load, projectId]);

  if (!activeOrgSlug) {
    return (
      <div className="p-4">
        <div className="border border-border rounded bg-bg-card px-4 py-6">
          <div className="text-text font-medium mb-2">Pick an organization to see its endpoints</div>
          <div className="text-text-dim text-sm mb-4">
            Discovery URLs and signing keys are per-organization in v0.4.0.
            Select one below or via the switcher in the header.
          </div>
          <div className="flex flex-wrap gap-2">
            {orgs.map((o) => (
              <button
                key={o.id}
                onClick={() => onSelectOrg(o.slug)}
                className="inline-flex items-center gap-2 px-3 py-1.5 text-sm border border-border rounded text-text hover:bg-bg"
              >
                <span className="w-2 h-2 rounded-full" style={{ backgroundColor: o.color || "#94a3b8" }} />
                {o.name}
              </button>
            ))}
          </div>
        </div>
      </div>
    );
  }

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

function KeyIcon({ size = 18 }: { size?: number }) {
  // Lucide "key-round" — horizontal bow + shaft + tooth. Much more
  // legible at 16-18px than the FA-style diagonal key I had before.
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M2 18a4 4 0 0 1 4-4h.5" />
      <path d="M14 14h.5a4 4 0 0 1 0 8H6a4 4 0 0 1 0-8" />
      <circle cx="16.5" cy="7.5" r="5.5" />
      <circle cx="16.5" cy="7.5" r="1.5" fill="currentColor" stroke="none" />
    </svg>
  );
}

function UserPlusIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M16 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" />
      <circle cx="8.5" cy="7" r="4" />
      <line x1="20" y1="8" x2="20" y2="14" />
      <line x1="23" y1="11" x2="17" y2="11" />
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

function BuildingIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="2" width="16" height="20" rx="2" />
      <line x1="9" y1="6" x2="9.01" y2="6" />
      <line x1="15" y1="6" x2="15.01" y2="6" />
      <line x1="9" y1="10" x2="9.01" y2="10" />
      <line x1="15" y1="10" x2="15.01" y2="10" />
      <line x1="9" y1="14" x2="9.01" y2="14" />
      <line x1="15" y1="14" x2="15.01" y2="14" />
      <path d="M10 22V18a2 2 0 0 1 4 0v4" />
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
