// FleetPanel — dashboard surface for the fleet app's tenant control
// plane. Two-pane layout: tenant list on the left, detail + event
// timeline on the right.
//
// Wire protocol:
//   GET  /api/apps/fleet/tenants[/<id>]   — registry reads (cheap, no envelope)
//   POST /api/apps/fleet/mcp              — JSON-RPC for everything mutating
//                                           (create / start / stop / delete /
//                                           support_login)
//
// fleet doesn't publish app-events today, so this panel polls every
// 8s instead of subscribing. If/when fleet starts emitting events
// (e.g. tenant.spawned, tenant.status_changed) drop in the standard
// useAppEvents hook from tables/crm and remove the timer.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

// Local theme-token StatusPill — mirrors CRM/storage's vocabulary so
// fleet visually sits with the rest of the panels. The dashboard's JIT
// only emits utilities it actually sees referenced in its own source +
// these panels' bundled output; raw `text-blue-700`/`text-amber-700`
// from the ui-kit's StatusPill fall outside that scan and render as
// no-ops, which is what made fleet look different.
type PillVariant = "success" | "info" | "warn" | "error" | "neutral";

const PILL_CLASSES: Record<PillVariant, string> = {
  success: "bg-green/15 text-green",
  info: "bg-accent/15 text-accent",
  warn: "bg-warn/15 text-warn",
  error: "bg-red/15 text-red",
  neutral: "bg-bg-input text-text-dim",
};

function StatusPill({ variant = "neutral", children }: { variant?: PillVariant; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium whitespace-nowrap ${PILL_CLASSES[variant]}`}
    >
      {children}
    </span>
  );
}

// Local shape for a card surface — matches CRM/storage's two-pane
// rounded panel style. Drops ui-kit's Card (which carried `text-blue-700`
// / `text-amber-700` in its descendants and dropped fleet off-theme).
function Card({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={`flex flex-col overflow-hidden rounded-md border border-border bg-bg-card ${className ?? ""}`}>
      {children}
    </div>
  );
}

function CardHeader({
  title,
  subtitle,
  status,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  status?: { label: string; variant?: PillVariant };
}) {
  return (
    <header className="flex items-center gap-3 px-4 py-3 border-b border-border">
      <div className="min-w-0 flex-1 flex flex-col">
        <div className="text-text text-sm font-semibold truncate leading-tight">{title}</div>
        {subtitle && (
          <div className="text-text-dim text-xs truncate mt-0.5 leading-tight">{subtitle}</div>
        )}
      </div>
      {status && <StatusPill variant={status.variant}>{status.label}</StatusPill>}
    </header>
  );
}

function Row({
  leading,
  title,
  subtitle,
  trailing,
  onClick,
  flush,
}: {
  leading?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  trailing?: ReactNode;
  onClick?: () => void;
  flush?: boolean;
}) {
  const cls = `flex items-center gap-3 w-full text-left px-4 py-2.5 ${flush ? "" : "border-t border-border"} ${onClick ? "hover:bg-bg-input" : ""}`;
  const inner = (
    <>
      {leading !== undefined && (
        <span className="flex-shrink-0 flex items-center justify-center">{leading}</span>
      )}
      <div className="min-w-0 flex-1 flex flex-col">
        <div className="text-text text-sm font-medium truncate leading-tight">{title}</div>
        {subtitle !== undefined && (
          <div className="text-text-dim text-xs truncate mt-0.5 leading-tight">{subtitle}</div>
        )}
      </div>
      {trailing !== undefined && (
        <span className="flex-shrink-0 text-xs text-text-dim tabular-nums">{trailing}</span>
      )}
    </>
  );
  if (onClick) {
    return (
      <button type="button" className={cls} onClick={onClick}>
        {inner}
      </button>
    );
  }
  return <div className={cls}>{inner}</div>;
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type TenantStatus =
  | "starting"
  | "setup_pending"
  | "active"
  | "suspended"
  | "stopped"
  | "disconnected"
  | "failed"
  | "deleted";

interface Tenant {
  id: string;
  slug: string;
  kind: "local" | "remote";
  base_url: string;
  config_dir?: string;
  owner_email: string;
  current_version?: string;
  target_version?: string;
  status: TenantStatus;
  last_seen_at?: string;
  last_health?: unknown;
  created_at: string;
  updated_at: string;
  // v0.3 — domain link (populated by tenant_attach_domain orchestration)
  domain?: string;
  domain_record_id?: string;
  domain_attached_at?: string;
  // v0.3 — auto-respawn bookkeeping
  respawn_attempts?: number;
  last_respawn_at?: string;
  // v0.6+ — host placement. instance_id 0 = local parent host;
  // >0 = a row in the Instances app (tenant runs on that VPS).
  instance_id?: number;
}

interface InstanceOption {
  id: number;
  name: string;
  public_ipv4: string;
  status: string;
}

// Fleet's /api/_meta response. The panel calls this once per refresh
// so it knows whether the optional Domains/Certs/Routes integrations
// are bound, what apexes are picker-eligible, and the cert state for
// every tenant FQDN. Also returns the npm latest apteva version for
// the version-drift indicator.
interface MetaResp {
  domains_available: boolean;
  certs_available: boolean;
  routes_available: boolean;
  public_host: string;
  domains: Array<{ name: string }>;
  certs: Record<
    string,
    { status: string; expires_at?: string; error?: string }
  >;
  apteva_latest?: string;
  // v0.8 — Instances integration. host_provider_available gates the
  // "Run on" picker; instances is the ready remote-host list (parent /
  // local instance already excluded server-side).
  host_provider_available?: boolean;
  instances?: InstanceOption[];
}

interface FleetEvent {
  id: number;
  tenant_id: string;
  kind: string;
  actor?: string;
  payload?: unknown;
  created_at: string;
}

interface ListResp {
  tenants: Tenant[] | null;
  count: number;
}

// Auto-setup response shape from tenant_create when the orchestrator
// succeeds. status==="active" and the three reveal fields are
// populated. On orchestrator failure the response degrades to the
// setup_pending shape (no admin_password/api_key) and we surface the
// auto_setup_error to the operator instead.
interface CreateResp {
  tenant_id: string;
  slug: string;
  base_url: string;
  status: TenantStatus;
  admin_email?: string;
  admin_password?: string;
  api_key?: string;
  setup_url?: string;
  setup_token?: string;
  auto_setup_error?: string;
}

interface CredentialsReveal {
  slug: string;
  base_url: string;
  admin_email: string;
  admin_password: string;
  api_key: string;
}

interface GetResp {
  tenant: Tenant;
  events: FleetEvent[] | null;
  // Only populated while tenant.status === "setup_pending"; surfaced
  // by handlers.go's decorateView so the operator can recover the
  // info on refresh without re-running tenant_create.
  setup_token?: string;
  setup_url?: string;
}

const API = "/api/apps/fleet";
const REFRESH_MS = 8000;

// Status → pill variant. Same five-color semantic palette CRM/storage
// use, so a list of mixed statuses reads as a coherent group.
const STATUS_VARIANT: Record<TenantStatus, PillVariant> = {
  starting: "info",
  setup_pending: "warn",
  active: "success",
  suspended: "warn",
  stopped: "neutral",
  disconnected: "warn",
  failed: "error",
  deleted: "neutral",
};

export default function FleetPanel({ projectId, installId }: NativePanelProps) {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<GetResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [status, setStatus] = useState<string>("");
  const [showCreate, setShowCreate] = useState(false);
  const [showConnect, setShowConnect] = useState(false);
  // Credentials returned by a successful auto-setup. Held in panel
  // state because tenant_create is the only chance to see the admin
  // password and api_key — fleet doesn't store the plaintext anywhere
  // after this. Cleared when the operator clicks "I've saved them".
  const [credentialsReveal, setCredentialsReveal] = useState<CredentialsReveal | null>(null);
  // /api/_meta snapshot. Populated on mount + every refresh tick. The
  // panel uses it to decide whether to render attach/update controls,
  // and to seed picker lists. Null until first fetch — the detail pane
  // hides those controls in that brief window rather than disabling
  // them, which is less jumpy.
  const [meta, setMeta] = useState<MetaResp | null>(null);
  const [showAttachDomain, setShowAttachDomain] = useState<Tenant | null>(null);
  const [showUpdate, setShowUpdate] = useState<Tenant | null>(null);
  const [showMigrate, setShowMigrate] = useState<Tenant | null>(null);
  const [showClone, setShowClone] = useState<Tenant | null>(null);
  // Held in panel state because both fleet endpoints return sensitive
  // material the operator gets to see once and copy — same pattern as
  // the post-create credentialsReveal.
  const [revealedAPIKey, setRevealedAPIKey] = useState<{
    slug: string;
    base_url: string;
    api_key: string;
  } | null>(null);
  const [resetPassword, setResetPassword] = useState<{
    slug: string;
    base_url: string;
    admin_email: string;
    admin_password: string;
  } | null>(null);

  // Two query params travel on every call. The platform proxy uses
  // these to scope per-install state — same convention as tables/crm.
  const withParams = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const httpGet = useCallback(
    async <T,>(path: string): Promise<T> => {
      const res = await fetch(`${API}${path}?${withParams()}`, {
        credentials: "same-origin",
      });
      if (!res.ok) {
        throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      }
      return res.json();
    },
    [withParams],
  );

  // callTool — POST /api/apps/fleet/mcp with the JSON-RPC envelope
  // and unwrap result.content[0].text. Same shape every panel uses;
  // see apps/mcp/ads/ui/AdsPanel.tsx for the canonical version.
  const callTool = useCallback(
    async <T,>(tool: string, args: Record<string, unknown>): Promise<T> => {
      const res = await fetch(`${API}/mcp?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          method: "tools/call",
          params: { name: tool, arguments: args },
        }),
      });
      if (!res.ok) {
        throw new Error(`${tool}: ${res.status} ${await res.text().catch(() => "")}`);
      }
      const j = await res.json();
      if (j.error) throw new Error(j.error.message || tool);
      const text = j.result?.content?.[0]?.text;
      if (!text) return j.result as T;
      return JSON.parse(text) as T;
    },
    [withParams],
  );

  const refreshList = useCallback(
    async (opts: { quiet?: boolean } = {}) => {
      if (!opts.quiet) setLoading(true);
      try {
        const r = await httpGet<ListResp>("/tenants");
        const list = r.tenants || [];
        setTenants(list);
        setStatus(`${list.length} tenant${list.length !== 1 ? "s" : ""}`);
      } catch (e) {
        setStatus(`Error: ${(e as Error).message}`);
      } finally {
        if (!opts.quiet) setLoading(false);
      }
    },
    [httpGet],
  );

  const refreshDetail = useCallback(
    async (id: string) => {
      try {
        const r = await httpGet<GetResp>(`/tenants/${encodeURIComponent(id)}`);
        setDetail(r);
      } catch (e) {
        setStatus(`Error: ${(e as Error).message}`);
        setDetail(null);
      }
    },
    [httpGet],
  );

  const refreshMeta = useCallback(async () => {
    try {
      const r = await httpGet<MetaResp>("/_meta");
      setMeta(r);
    } catch {
      // _meta is opportunistic — a transient failure shouldn't toast
      // an error in the list status bar. The UI degrades to "no
      // integrations visible" until the next tick succeeds.
    }
  }, [httpGet]);

  useEffect(() => {
    refreshList();
    refreshMeta();
  }, [refreshList, refreshMeta]);

  useEffect(() => {
    if (!selectedId) {
      setDetail(null);
      return;
    }
    refreshDetail(selectedId);
  }, [selectedId, refreshDetail]);

  // Background polling. Quiet refresh so the spinner doesn't blink
  // every 8s — the list updates in place. Detail + meta refresh in
  // lockstep so the version-drift / cert badges don't lag the row data.
  useEffect(() => {
    const t = window.setInterval(() => {
      refreshList({ quiet: true });
      refreshMeta();
      if (selectedId) refreshDetail(selectedId);
    }, REFRESH_MS);
    return () => window.clearInterval(t);
  }, [refreshList, refreshDetail, refreshMeta, selectedId]);

  const selected = useMemo(
    () => tenants.find((t) => t.id === selectedId) || detail?.tenant || null,
    [tenants, selectedId, detail],
  );

  return (
    <div className="grid grid-cols-[340px_1fr] gap-3 h-full p-3 bg-bg">
      <TenantList
        tenants={tenants}
        selectedId={selectedId}
        loading={loading}
        status={status}
        latest={meta?.apteva_latest}
        onSelect={setSelectedId}
        onCreate={() => setShowCreate(true)}
        onConnect={() => setShowConnect(true)}
        onRefresh={() => refreshList()}
      />
      <TenantDetail
        tenant={selected}
        events={detail?.events ?? null}
        setupToken={detail?.setup_token ?? null}
        setupURL={detail?.setup_url ?? null}
        meta={meta}
        callTool={callTool}
        onOpenAttachDomain={(t) => setShowAttachDomain(t)}
        onOpenUpdate={(t) => setShowUpdate(t)}
        onOpenMigrate={(t) => setShowMigrate(t)}
        onOpenClone={(t) => setShowClone(t)}
        onRevealAPIKey={setRevealedAPIKey}
        onResetPassword={setResetPassword}
        onAfterAction={async (after) => {
          await refreshList({ quiet: true });
          await refreshMeta();
          if (after === "deselect") {
            setSelectedId(null);
          } else if (selectedId) {
            await refreshDetail(selectedId);
          }
        }}
      />
      {showCreate && (
        <CreateTenantDialog
          meta={meta}
          onClose={() => setShowCreate(false)}
          onSubmit={async ({ slug, owner_email, instance_id, port }) => {
            try {
              const r = await callTool<CreateResp>("tenant_create", {
                slug,
                owner_email,
                ...(instance_id ? { instance_id } : {}),
                ...(port ? { port } : {}),
              });
              await refreshList({ quiet: true });
              if (r.tenant_id) setSelectedId(r.tenant_id);
              setShowCreate(false);
              // Auto-setup happy path returns admin_password + api_key.
              // Surface them in a one-shot modal — fleet stores the
              // api_key sealed and never the plaintext password, so
              // this is the operator's only chance to capture them.
              if (r.admin_password && r.api_key && r.admin_email) {
                setCredentialsReveal({
                  slug: r.slug,
                  base_url: r.base_url,
                  admin_email: r.admin_email,
                  admin_password: r.admin_password,
                  api_key: r.api_key,
                });
              }
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {credentialsReveal && (
        <CredentialsRevealDialog
          creds={credentialsReveal}
          onClose={() => setCredentialsReveal(null)}
        />
      )}
      {showConnect && (
        <ConnectTenantDialog
          onClose={() => setShowConnect(false)}
          onSubmit={async (args) => {
            try {
              const r = await callTool<{ tenant_id: string }>("tenant_connect", args);
              await refreshList({ quiet: true });
              if (r.tenant_id) setSelectedId(r.tenant_id);
              setShowConnect(false);
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {showAttachDomain && meta && (
        <AttachDomainDialog
          tenant={showAttachDomain}
          meta={meta}
          onClose={() => setShowAttachDomain(null)}
          onSubmit={async (args) => {
            try {
              await callTool("tenant_attach_domain", {
                tenant_id: showAttachDomain.id,
                ...args,
              });
              await refreshList({ quiet: true });
              await refreshMeta();
              if (selectedId) await refreshDetail(selectedId);
              setShowAttachDomain(null);
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {showUpdate && (
        <UpdateVersionDialog
          tenant={showUpdate}
          latest={meta?.apteva_latest}
          onClose={() => setShowUpdate(null)}
          onSubmit={async (version) => {
            try {
              await callTool("tenant_update", {
                tenant_id: showUpdate.id,
                ...(version ? { version } : {}),
              });
              await refreshList({ quiet: true });
              if (selectedId) await refreshDetail(selectedId);
              setShowUpdate(null);
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {showMigrate && meta && (
        <MoveTenantDialog
          tenant={showMigrate}
          meta={meta}
          onClose={() => setShowMigrate(null)}
          onSubmit={async ({ instance_id, port }) => {
            try {
              await callTool("tenant_migrate", {
                tenant_id: showMigrate.id,
                instance_id,
                ...(port ? { port } : {}),
              });
              await refreshList({ quiet: true });
              if (selectedId) await refreshDetail(selectedId);
              setShowMigrate(null);
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {showClone && (
        <CloneTenantDialog
          tenant={showClone}
          meta={meta}
          onClose={() => setShowClone(null)}
          onSubmit={async ({ slug, owner_email, instance_id, port, start }) => {
            try {
              const r = await callTool<{ tenant_id: string }>("tenant_clone", {
                source_tenant_id: showClone.id,
                slug,
                ...(owner_email ? { owner_email } : {}),
                instance_id,
                ...(port ? { port } : {}),
                start,
              });
              await refreshList({ quiet: true });
              if (r.tenant_id) setSelectedId(r.tenant_id);
              setShowClone(null);
              return { ok: true };
            } catch (e) {
              return { ok: false, error: (e as Error).message };
            }
          }}
        />
      )}
      {revealedAPIKey && (
        <RevealAPIKeyDialog
          data={revealedAPIKey}
          onClose={() => setRevealedAPIKey(null)}
        />
      )}
      {resetPassword && (
        <ResetPasswordDialog
          data={resetPassword}
          onClose={() => setResetPassword(null)}
        />
      )}
    </div>
  );
}

// ─── List pane ──────────────────────────────────────────────────────

function TenantList({
  tenants,
  selectedId,
  loading,
  status,
  latest,
  onSelect,
  onCreate,
  onConnect,
  onRefresh,
}: {
  tenants: Tenant[];
  selectedId: string | null;
  loading: boolean;
  status: string;
  latest?: string;
  onSelect: (id: string) => void;
  onCreate: () => void;
  onConnect: () => void;
  onRefresh: () => void;
}) {
  return (
    <Card className="h-full">
      <CardHeader
        title="Tenants"
        subtitle={loading ? "Loading…" : status}
        status={{ label: String(tenants.length), variant: "info" }}
      />
      <div className="flex items-center gap-2 px-4 py-2 border-b border-border">
        <button
          type="button"
          onClick={onCreate}
          className="px-2.5 py-1 rounded-md text-xs font-medium bg-accent/10 text-accent hover:bg-accent/15"
        >
          + Create local
        </button>
        <button
          type="button"
          onClick={onConnect}
          className="px-2.5 py-1 rounded-md text-xs font-medium bg-bg-input hover:bg-bg-hover text-text"
        >
          Connect remote
        </button>
        <span className="flex-1" />
        <button
          type="button"
          onClick={onRefresh}
          title="Refresh"
          className="px-2 py-1 rounded-md text-xs text-text-dim hover:text-text hover:bg-bg-input"
        >
          ↻
        </button>
      </div>
      <div className="flex-1 overflow-y-auto">
        {tenants.length === 0 && !loading && (
          <p className="text-xs text-text-dim px-4 py-6 text-center">
            No tenants yet. Create a local tenant or connect an existing apteva.
          </p>
        )}
        {tenants.map((t, i) => (
          <Row
            key={t.id}
            flush={i === 0}
            onClick={() => onSelect(t.id)}
            leading={<KindGlyph kind={t.kind} />}
            title={
              <span
                className={t.id === selectedId ? "text-accent font-semibold" : ""}
              >
                {t.slug}
              </span>
            }
            subtitle={
              <span className="font-mono text-[10px] flex items-center gap-1.5">
                <span className="truncate">
                  {t.domain ? t.domain : shortBaseURL(t.base_url)}
                </span>
                <span className="text-text-dim">·</span>
                <span
                  className={
                    t.current_version
                      ? "text-text-dim"
                      : "text-text-dim italic"
                  }
                >
                  {t.current_version ? `v${t.current_version}` : "v—"}
                </span>
                {latest &&
                  t.current_version &&
                  t.current_version !== latest && (
                    <span
                      title={`npm latest: ${latest}`}
                      className="text-warn"
                    >
                      ↑
                    </span>
                  )}
              </span>
            }
            trailing={
              <div className="flex items-center gap-1.5">
                {(t.respawn_attempts ?? 0) > 0 && (
                  <span
                    title={`${t.respawn_attempts} auto-respawn${t.respawn_attempts === 1 ? "" : "s"}`}
                    className="text-[10px] font-medium text-warn px-1.5 py-0.5 rounded bg-warn/10"
                  >
                    ↻{t.respawn_attempts}
                  </span>
                )}
                <StatusPill variant={STATUS_VARIANT[t.status]}>{t.status}</StatusPill>
              </div>
            }
          />
        ))}
      </div>
    </Card>
  );
}

function KindGlyph({ kind }: { kind: Tenant["kind"] }) {
  // Inline SVG instead of an emoji per project convention (memory:
  // feedback_no_emojis_in_app_ui). Local = server stack icon, remote
  // = cloud-ish icon. Both use currentColor and inherit text-text-dim
  // so they sit visually with the row text.
  if (kind === "local") {
    return (
      <svg
        width="14"
        height="14"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="text-text-dim"
        aria-label="local tenant"
      >
        <rect x="2" y="3" width="12" height="4" rx="1" />
        <rect x="2" y="9" width="12" height="4" rx="1" />
        <circle cx="4.5" cy="5" r="0.5" fill="currentColor" />
        <circle cx="4.5" cy="11" r="0.5" fill="currentColor" />
      </svg>
    );
  }
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="text-text-dim"
      aria-label="remote tenant"
    >
      <path d="M4.5 11.5a3 3 0 1 1 .8-5.9 3.5 3.5 0 0 1 6.7 1.1 2.5 2.5 0 0 1-.5 4.8H4.5z" />
    </svg>
  );
}

// ─── Detail pane ────────────────────────────────────────────────────

function TenantDetail({
  tenant,
  events,
  setupToken,
  setupURL,
  meta,
  callTool,
  onOpenAttachDomain,
  onOpenUpdate,
  onOpenMigrate,
  onOpenClone,
  onRevealAPIKey,
  onResetPassword,
  onAfterAction,
}: {
  tenant: Tenant | null;
  events: FleetEvent[] | null;
  setupToken: string | null;
  setupURL: string | null;
  meta: MetaResp | null;
  callTool: <T>(tool: string, args: Record<string, unknown>) => Promise<T>;
  onOpenAttachDomain: (t: Tenant) => void;
  onOpenUpdate: (t: Tenant) => void;
  onOpenMigrate: (t: Tenant) => void;
  onOpenClone: (t: Tenant) => void;
  onRevealAPIKey: (r: { slug: string; base_url: string; api_key: string }) => void;
  onResetPassword: (r: { slug: string; base_url: string; admin_email: string; admin_password: string }) => void;
  onAfterAction: (after?: "deselect") => Promise<void>;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [supportURL, setSupportURL] = useState<{ url: string; expires_at?: string } | null>(null);

  // Reset transient detail state whenever the tenant changes.
  useEffect(() => {
    setBusy(null);
    setErr(null);
    setConfirmDelete(false);
    setSupportURL(null);
  }, [tenant?.id]);

  if (!tenant) {
    return (
      <Card className="h-full items-center justify-center text-text-dim text-sm">
        <p>Select a tenant to see details, events, and actions.</p>
      </Card>
    );
  }

  const run = async (
    label: string,
    tool: string,
    args: Record<string, unknown>,
    after?: "deselect",
  ) => {
    setBusy(label);
    setErr(null);
    try {
      await callTool(tool, args);
      await onAfterAction(after);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const isLocal = tenant.kind === "local";
  const isRunning = tenant.status === "active" || tenant.status === "starting";
  const isSetupPending = tenant.status === "setup_pending";
  // Host placement: instance_id 0/undefined = parent host; >0 = a VPS.
  const onParentHost = (tenant.instance_id ?? 0) === 0;
  const hostInstance = meta?.instances?.find((i) => i.id === tenant.instance_id);
  const canMigrate = isLocal && !isSetupPending;
  const canClone = isLocal;

  return (
    <Card className="h-full">
      <CardHeader
        title={tenant.slug}
        subtitle={
          <span className="font-mono text-[11px]">
            {tenant.base_url}
            {" · "}
            {onParentHost
              ? "local"
              : hostInstance
                ? `on ${hostInstance.name}`
                : `on instance ${tenant.instance_id}`}
            {tenant.current_version ? ` · v${tenant.current_version}` : ""}
          </span>
        }
        status={{ label: tenant.status, variant: STATUS_VARIANT[tenant.status] }}
      />

      {/* Action bar. When setup_pending we hide the normal lifecycle
          buttons — the operator still needs to complete admin
          registration before Start/Stop/Support make sense. */}
      <div className="flex flex-wrap items-center gap-2 px-4 py-2 border-b border-border">
        {!isSetupPending && isLocal && !isRunning && (
          <ActionButton
            label="Start"
            busy={busy === "start"}
            onClick={() => run("start", "tenant_start", { tenant_id: tenant.id })}
          />
        )}
        {!isSetupPending && isLocal && isRunning && (
          <ActionButton
            label="Stop"
            busy={busy === "stop"}
            onClick={() => run("stop", "tenant_stop", { tenant_id: tenant.id })}
          />
        )}
        {!isSetupPending && !isLocal && tenant.status !== "suspended" && (
          <ActionButton
            label="Suspend"
            busy={busy === "stop"}
            onClick={() => run("stop", "tenant_stop", { tenant_id: tenant.id })}
          />
        )}
        {!isSetupPending && (
          <ActionButton
            label={supportURL ? "Open support URL" : "Support login"}
            busy={busy === "support"}
            onClick={async () => {
              if (supportURL) {
                window.open(supportURL.url, "_blank", "noopener");
                return;
              }
              const reason = window.prompt("Reason for support login (audit trail):");
              if (!reason) return;
              setBusy("support");
              setErr(null);
              try {
                const r = await callTool<{ url: string; expires_at?: string }>(
                  "tenant_support_login",
                  { tenant_id: tenant.id, reason },
                );
                setSupportURL(r);
                if (r?.url) window.open(r.url, "_blank", "noopener");
                await onAfterAction();
              } catch (e) {
                setErr((e as Error).message);
              } finally {
                setBusy(null);
              }
            }}
          />
        )}
        {canMigrate && (
          <ActionButton
            label="Move…"
            onClick={() => onOpenMigrate(tenant)}
          />
        )}
        {canClone && (
          <ActionButton
            label="Clone…"
            onClick={() => onOpenClone(tenant)}
          />
        )}
        <span className="flex-1" />
        {confirmDelete ? (
          <>
            <span className="text-xs text-error">
              {isLocal
                ? "Stop process and wipe data dir?"
                : "Remove from registry?"}
            </span>
            <ActionButton
              label="Confirm delete"
              tone="danger"
              busy={busy === "delete"}
              onClick={() =>
                run(
                  "delete",
                  "tenant_delete",
                  { tenant_id: tenant.id, confirm: true },
                  "deselect",
                )
              }
            />
            <ActionButton label="Cancel" onClick={() => setConfirmDelete(false)} />
          </>
        ) : (
          <ActionButton
            label="Delete"
            tone="danger"
            onClick={() => setConfirmDelete(true)}
          />
        )}
      </div>

      {err && (
        <div className="px-4 py-2 text-xs text-error border-b border-border bg-error/5">
          {err}
        </div>
      )}

      {supportURL && (
        <div className="px-4 py-2 text-xs border-b border-border bg-accent/5 flex items-center gap-2">
          <span className="text-text-dim">Support session active</span>
          <a
            href={supportURL.url}
            target="_blank"
            rel="noopener"
            className="font-mono truncate text-accent underline"
          >
            {supportURL.url}
          </a>
          {supportURL.expires_at && (
            <span className="text-text-dim ml-auto">expires {formatTime(supportURL.expires_at)}</span>
          )}
        </div>
      )}

      {isSetupPending && (
        <SetupPendingBanner
          tenantId={tenant.id}
          baseURL={tenant.base_url}
          setupToken={setupToken}
          setupURL={setupURL}
          busy={busy === "attach"}
          onAttach={async (apiKey) => {
            setBusy("attach");
            setErr(null);
            try {
              await callTool("tenant_attach_key", {
                tenant_id: tenant.id,
                api_key: apiKey,
              });
              await onAfterAction();
            } catch (e) {
              setErr((e as Error).message);
              throw e;
            } finally {
              setBusy(null);
            }
          }}
        />
      )}

      {!isSetupPending && (
        <DomainBlock
          tenant={tenant}
          meta={meta}
          busy={busy === "detach-domain"}
          onAttach={() => onOpenAttachDomain(tenant)}
          onDetach={() =>
            run("detach-domain", "tenant_detach_domain", { tenant_id: tenant.id })
          }
        />
      )}

      {!isSetupPending && isLocal && (
        <VersionBlock
          tenant={tenant}
          latest={meta?.apteva_latest}
          onUpdate={() => onOpenUpdate(tenant)}
        />
      )}

      {!isSetupPending && (
        <CredentialsBlock
          tenantId={tenant.id}
          busyReveal={busy === "reveal-api-key"}
          busyReset={busy === "reset-password"}
          onReveal={async () => {
            setBusy("reveal-api-key");
            setErr(null);
            try {
              const r = await callTool<{
                slug: string;
                base_url: string;
                api_key: string;
              }>("tenant_reveal_api_key", { tenant_id: tenant.id });
              onRevealAPIKey(r);
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(null);
            }
          }}
          onReset={async () => {
            // Reset is destructive (revokes sessions) — confirm first.
            if (!window.confirm(
              "Rotate the admin password and revoke every active session for this tenant?\n\n" +
              "The new password is shown only once — you'll need to copy it before closing the dialog.",
            )) return;
            setBusy("reset-password");
            setErr(null);
            try {
              const r = await callTool<{
                slug: string;
                base_url: string;
                admin_email: string;
                admin_password: string;
              }>("tenant_reset_admin_password", { tenant_id: tenant.id });
              onResetPassword(r);
              await onAfterAction();
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(null);
            }
          }}
        />
      )}

      <div className="grid grid-cols-2 gap-0 border-b border-border">
        <Field label="Owner">{tenant.owner_email}</Field>
        <Field label="Kind">{tenant.kind}</Field>
        <Field label="Status">
          <StatusPill variant={STATUS_VARIANT[tenant.status]}>{tenant.status}</StatusPill>
        </Field>
        <Field label="Base URL" mono>
          <a
            href={tenant.base_url}
            target="_blank"
            rel="noopener"
            className="text-accent hover:underline"
          >
            {tenant.base_url}
          </a>
        </Field>
        <Field label="Last seen">{formatTime(tenant.last_seen_at)}</Field>
        {tenant.config_dir && (
          <Field label="Data dir" mono>
            {tenant.config_dir}
          </Field>
        )}
        <Field label="Created">{formatTime(tenant.created_at)}</Field>
        {(tenant.respawn_attempts ?? 0) > 0 && (
          <Field label="Auto-respawns">
            <span className="text-warn font-medium">
              {tenant.respawn_attempts}
            </span>
            {tenant.last_respawn_at && (
              <span className="text-text-dim"> · last {formatTime(tenant.last_respawn_at)}</span>
            )}
          </Field>
        )}
      </div>

      <div className="flex-1 overflow-y-auto">
        <div className="px-4 pt-3 pb-1 text-[11px] uppercase tracking-wider text-text-dim font-medium">
          Recent events
        </div>
        {!events || events.length === 0 ? (
          <p className="text-xs text-text-dim px-4 py-3">No events yet.</p>
        ) : (
          <ul className="px-4 pb-4">
            {events.map((e) => (
              <EventRow key={e.id} ev={e} />
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

// ─── Setup-pending banner ───────────────────────────────────────────

function SetupPendingBanner({
  tenantId,
  baseURL,
  setupToken,
  setupURL,
  busy,
  onAttach,
}: {
  tenantId: string;
  baseURL: string;
  setupToken: string | null;
  setupURL: string | null;
  busy: boolean;
  onAttach: (apiKey: string) => Promise<void>;
}) {
  // Reset the api-key field when the operator switches between
  // setup_pending tenants so a half-typed key doesn't leak across.
  const [apiKey, setApiKey] = useState("");
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    setApiKey("");
    setCopied(false);
  }, [tenantId]);

  const url = setupURL || `${baseURL}/?setup=1`;

  return (
    <div className="px-4 py-3 border-b border-border bg-warn/5 space-y-2">
      <div className="flex items-start gap-2">
        <span className="mt-0.5 text-[10px] uppercase tracking-wider font-semibold text-warn">
          Setup pending
        </span>
        <span className="text-xs text-text-dim flex-1">
          Register an admin in the tenant dashboard, generate an API key, then
          paste it below to finish linking.
        </span>
      </div>

      <div className="flex items-center gap-2 text-xs">
        <span className="text-text-dim w-20 flex-shrink-0">Setup URL</span>
        <a
          href={url}
          target="_blank"
          rel="noopener"
          className="font-mono text-accent hover:underline truncate flex-1"
        >
          {url}
        </a>
        <ActionButton label="Open ↗" onClick={() => window.open(url, "_blank", "noopener")} />
      </div>

      <div className="flex items-center gap-2 text-xs">
        <span className="text-text-dim w-20 flex-shrink-0">Setup token</span>
        <code className="font-mono text-text bg-bg-hover px-2 py-0.5 rounded truncate flex-1">
          {setupToken || <span className="text-text-dim italic">unavailable — recreate the tenant</span>}
        </code>
        {setupToken && (
          <ActionButton
            label={copied ? "Copied" : "Copy"}
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(setupToken);
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1500);
              } catch {
                // Clipboard blocked (insecure context / permission) — fall
                // back to selecting. The token's still visible on-screen.
              }
            }}
          />
        )}
      </div>

      <form
        className="flex items-center gap-2 text-xs"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!apiKey.trim() || busy) return;
          try {
            await onAttach(apiKey.trim());
            setApiKey("");
          } catch {
            // Error surface handled by parent's err state.
          }
        }}
      >
        <span className="text-text-dim w-20 flex-shrink-0">API key</span>
        <input
          type="password"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder="sk-…"
          className="flex-1 px-2 py-1.5 rounded-md border border-border bg-bg-card text-text font-mono"
          disabled={busy}
        />
        <button
          type="submit"
          disabled={busy || !apiKey.trim()}
          className="px-2.5 py-1 rounded-md text-xs font-medium bg-accent/10 text-accent hover:bg-accent/15 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {busy ? "Attaching…" : "Attach key"}
        </button>
      </form>
    </div>
  );
}

function Field({
  label,
  children,
  mono,
}: {
  label: string;
  children: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="px-4 py-2 border-t border-border first:border-t-0 odd:border-r odd:border-r-border">
      <div className="text-[10px] uppercase tracking-wider text-text-dim font-medium mb-0.5">
        {label}
      </div>
      <div className={`text-xs text-text ${mono ? "font-mono truncate" : ""}`}>
        {children}
      </div>
    </div>
  );
}

function EventRow({ ev }: { ev: FleetEvent }) {
  return (
    <li className="flex items-start gap-3 py-1.5 border-b border-border last:border-b-0">
      <span className="text-[10px] uppercase tracking-wider text-text-dim w-28 flex-shrink-0 pt-0.5">
        {formatTime(ev.created_at)}
      </span>
      <span className="text-xs flex-shrink-0">
        <EventKindPill kind={ev.kind} />
      </span>
      <span className="text-xs text-text-dim min-w-0 break-all">
        {ev.payload ? JSON.stringify(ev.payload) : ev.actor || ""}
      </span>
    </li>
  );
}

function EventKindPill({ kind }: { kind: string }) {
  // Bucketed for visual scanning. New v0.3 kinds: domain.*, route.*,
  // updated, auto_respawn_*. Anything unknown falls through to neutral.
  const success = [
    "spawned",
    "started",
    "connected",
    "domain.attached",
    "domain.detached",
    "route.registered",
    "updated",
    "auto_respawn_ok",
  ];
  const error = [
    "spawn_failed",
    "health_failed",
    "auto_respawn_failed",
    "auto_respawn_gave_up",
    "update_failed",
    "route.register_failed",
  ];
  const warn = [
    "domain.cert_kickoff_failed",
    "route.register_skipped",
    "admin_password_reset",
    "api_key_revealed",
  ];
  const info = ["support_login", "remote_call"];
  const variant: PillVariant = success.includes(kind)
    ? "success"
    : error.includes(kind)
      ? "error"
      : warn.includes(kind)
        ? "warn"
        : info.includes(kind)
          ? "info"
          : kind === "stopped" || kind === "status_changed"
            ? "neutral"
            : "neutral";
  return <StatusPill variant={variant}>{kind}</StatusPill>;
}

// ─── Action button ──────────────────────────────────────────────────

function ActionButton({
  label,
  onClick,
  busy,
  tone,
}: {
  label: string;
  onClick: () => void;
  busy?: boolean;
  tone?: "danger";
}) {
  const cls =
    tone === "danger"
      ? "bg-red/10 text-red hover:bg-red/15"
      : "bg-bg-input hover:bg-bg-hover text-text";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      className={`px-2.5 py-1 rounded-md text-xs font-medium ${cls} disabled:opacity-50 disabled:cursor-not-allowed`}
    >
      {busy ? "…" : label}
    </button>
  );
}

// ─── Dialogs ────────────────────────────────────────────────────────

// ─── Credentials reveal (one-shot) ──────────────────────────────────

function CredentialsRevealDialog({
  creds,
  onClose,
}: {
  creds: CredentialsReveal;
  onClose: () => void;
}) {
  return (
    <DialogFrame title={`Tenant ${creds.slug} ready — save these credentials`} onClose={onClose}>
      <div className="bg-warn/10 border border-warn/30 rounded-md px-3 py-2 mb-3 text-xs text-warn">
        These are shown only once. The admin password and API key are not
        recoverable from the fleet registry — copy them somewhere safe
        before dismissing.
      </div>

      <CredentialRow label="Tenant URL" value={creds.base_url} />
      <CredentialRow label="Admin email" value={creds.admin_email} />
      <CredentialRow label="Admin password" value={creds.admin_password} sensitive />
      <CredentialRow label="API key (fleet)" value={creds.api_key} sensitive />

      <DialogActions>
        <button
          type="button"
          onClick={onClose}
          className="px-2.5 py-1 rounded-md text-xs font-medium bg-accent/10 text-accent hover:bg-accent/15"
        >
          I've saved them
        </button>
      </DialogActions>
    </DialogFrame>
  );
}

function CredentialRow({
  label,
  value,
  sensitive,
}: {
  label: string;
  value: string;
  sensitive?: boolean;
}) {
  const [revealed, setRevealed] = useState(!sensitive);
  const [copied, setCopied] = useState(false);

  return (
    <div className="mb-2">
      <div className="text-[10px] uppercase tracking-wider text-text-dim font-medium mb-1">
        {label}
      </div>
      <div className="flex items-center gap-2">
        <code className="flex-1 font-mono text-xs px-2 py-1.5 rounded-md bg-bg-hover text-text truncate">
          {revealed ? value : "•".repeat(Math.min(value.length, 32))}
        </code>
        {sensitive && (
          <button
            type="button"
            onClick={() => setRevealed((v) => !v)}
            className="px-2 py-1 rounded-md text-xs text-text-dim hover:text-text hover:bg-bg-hover"
          >
            {revealed ? "Hide" : "Show"}
          </button>
        )}
        <button
          type="button"
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(value);
              setCopied(true);
              window.setTimeout(() => setCopied(false), 1500);
            } catch {
              // Clipboard blocked — value is still on-screen if revealed.
            }
          }}
          className="px-2 py-1 rounded-md text-xs font-medium bg-bg-input hover:bg-bg-hover text-text"
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}

function hostPickerOptions(meta: MetaResp | null, currentID: number) {
  const out = [
    { id: 0, name: "Local server", public_ipv4: "", status: "ready" },
    ...(meta?.instances ?? []),
  ];
  if (currentID > 0 && !out.some((i) => i.id === currentID)) {
    out.push({
      id: currentID,
      name: `Instance ${currentID}`,
      public_ipv4: "",
      status: "ready",
    });
  }
  return out;
}

function CreateTenantDialog({
  onClose,
  onSubmit,
  meta,
}: {
  onClose: () => void;
  onSubmit: (v: {
    slug: string;
    owner_email: string;
    instance_id?: number;
    port?: number;
  }) => Promise<{ ok: boolean; error?: string }>;
  meta: MetaResp | null;
}) {
  const [slug, setSlug] = useState("");
  const [email, setEmail] = useState("");
  // "Run on": 0 = local parent host; >0 = an Instances row id.
  const [runOn, setRunOn] = useState(0);
  const [port, setPort] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const instances = meta?.instances ?? [];
  const canHost = !!meta?.host_provider_available && instances.length > 0;
  const hosted = runOn > 0;
  const target = instances.find((i) => i.id === runOn);

  return (
    <DialogFrame title="Create tenant" onClose={onClose}>
      <p className="text-xs text-text-dim mb-3">
        Spawns a fresh apteva process with its own data dir and port,
        registers an admin using <code className="font-mono">owner_email</code>,
        and returns a one-shot password + api_key. Slug must be{" "}
        <code className="font-mono">[a-z0-9_-]</code>. May take 15-45s
        the first time (server + core boot).
      </p>
      <Label text="Slug">
        <input
          type="text"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="acme"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
          autoFocus
        />
      </Label>
      <Label text="Owner email">
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="ops@acme.com"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        />
      </Label>
      <Label text="Run on">
        <select
          value={runOn}
          onChange={(e) => setRunOn(Number(e.target.value))}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        >
          <option value={0}>Local (this server)</option>
          {instances.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name} — {i.public_ipv4}
              {i.status !== "ready" ? ` (${i.status})` : ""}
            </option>
          ))}
        </select>
      </Label>
      {!canHost && (
        <p className="text-xs text-text-dim -mt-1">
          {meta?.host_provider_available
            ? "No ready remote instances. Provision one in the Instances app to host elsewhere."
            : "Install + bind the Instances app to host tenants on remote VPSes."}
        </p>
      )}
      {hosted && (
        <Label text="Port (optional)">
          <input
            type="number"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            placeholder="auto (7100 + tenants on this instance)"
            className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
          />
        </Label>
      )}
      {hosted && target && (
        <div className="text-xs text-text-dim font-mono">
          Will spawn on <span className="text-text">{target.name}</span> (
          {target.public_ipv4})
        </div>
      )}
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Spawning…" : "Spawn"}
          busy={busy}
          onClick={async () => {
            if (!slug || !email) {
              setErr("slug and owner_email are required");
              return;
            }
            setBusy(true);
            setErr(null);
            const r = await onSubmit({
              slug,
              owner_email: email,
              ...(hosted ? { instance_id: runOn } : {}),
              ...(hosted && port.trim() ? { port: Number(port) } : {}),
            });
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

function CloneTenantDialog({
  tenant,
  meta,
  onClose,
  onSubmit,
}: {
  tenant: Tenant;
  meta: MetaResp | null;
  onClose: () => void;
  onSubmit: (v: {
    slug: string;
    owner_email?: string;
    instance_id: number;
    port?: number;
    start: boolean;
  }) => Promise<{ ok: boolean; error?: string }>;
}) {
  const [slug, setSlug] = useState(`${tenant.slug}-copy`);
  const [email, setEmail] = useState(tenant.owner_email);
  const hostOptions = useMemo(
    () => hostPickerOptions(meta, tenant.instance_id ?? 0),
    [meta, tenant.instance_id],
  );
  const [instanceID, setInstanceID] = useState(tenant.instance_id ?? 0);
  const [port, setPort] = useState("");
  const [start, setStart] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const target = hostOptions.find((i) => i.id === instanceID);

  return (
    <DialogFrame title={`Clone ${tenant.slug}`} onClose={onClose}>
      <p className="text-xs text-text-dim mb-3">
        Creates a new tenant from the current data dir. The source tenant
        is not stopped, restarted, or changed. Domains are not copied to
        the clone.
      </p>
      <Label text="New slug">
        <input
          type="text"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="acme-copy"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
          autoFocus
        />
      </Label>
      <Label text="Owner email">
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder={tenant.owner_email}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        />
      </Label>
      <Label text="Target host">
        <select
          value={instanceID}
          onChange={(e) => setInstanceID(Number(e.target.value))}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        >
          {hostOptions.map((i) => (
            <option key={i.id} value={i.id}>
              {i.id === 0 ? "Local server" : `${i.name} — ${i.public_ipv4}`}
              {i.status !== "ready" ? ` (${i.status})` : ""}
            </option>
          ))}
        </select>
      </Label>
      <Label text="Port (optional)">
        <input
          type="number"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          placeholder="auto"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
        />
      </Label>
      <label className="flex items-center gap-2 text-xs text-text">
        <input
          type="checkbox"
          checked={start}
          onChange={(e) => setStart(e.target.checked)}
          className="h-3.5 w-3.5"
        />
        Start clone after copy
      </label>
      {tenant.domain && (
        <div className="text-xs text-text-dim font-mono">
          Domain stays on source: {tenant.domain}
        </div>
      )}
      {target && (
        <div className="text-xs text-text-dim font-mono">
          Clone target: {target.id === 0 ? "local server" : `${target.name} (${target.public_ipv4})`}
        </div>
      )}
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Cloning…" : "Clone"}
          busy={busy}
          onClick={async () => {
            if (!slug.trim()) {
              setErr("slug is required");
              return;
            }
            setBusy(true);
            setErr(null);
            const r = await onSubmit({
              slug: slug.trim(),
              ...(email.trim() && email.trim() !== tenant.owner_email ? { owner_email: email.trim() } : {}),
              instance_id: instanceID,
              ...(port.trim() ? { port: Number(port) } : {}),
              start,
            });
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

// ─── Move-tenant dialog ─────────────────────────────────────────────
//
// Migrates a Fleet-managed tenant between local and Instances hosts.
// Cold move — the tenant is briefly down while fleet stops it,
// transfers the data dir, starts the target, and re-points routes.

function MoveTenantDialog({
  tenant,
  meta,
  onClose,
  onSubmit,
}: {
  tenant: Tenant;
  meta: MetaResp;
  onClose: () => void;
  onSubmit: (args: { instance_id: number; port?: number }) => Promise<{ ok: boolean; error?: string }>;
}) {
  const currentID = tenant.instance_id ?? 0;
  const hostOptions = hostPickerOptions(meta, currentID);
  const defaultTarget = hostOptions.find((i) => i.id !== currentID)?.id ?? currentID;
  const [instanceID, setInstanceID] = useState(defaultTarget);
  const [port, setPort] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const target = hostOptions.find((i) => i.id === instanceID);
  const sameHost = instanceID === currentID;

  return (
    <DialogFrame title={`Move ${tenant.slug}`} onClose={onClose}>
      <p className="text-xs text-text-dim mb-3">
        Relocates this tenant to another host. Fleet stops the source,
        transfers its data dir, starts the target, and re-points routes.
        The tenant is briefly <span className="text-text">down</span>.
        If anything fails before commit, the source is restarted.
      </p>
      <Label text="Target host">
        <select
          value={instanceID}
          onChange={(e) => setInstanceID(Number(e.target.value))}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        >
          {hostOptions.map((i) => (
            <option key={i.id} value={i.id}>
              {i.id === 0 ? "Local server" : `${i.name} — ${i.public_ipv4}`}
              {i.status !== "ready" ? ` (${i.status})` : ""}
            </option>
          ))}
        </select>
      </Label>
      <Label text="Port (optional)">
        <input
          type="number"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          placeholder={instanceID === 0 ? "auto" : "auto (7100 + tenants on this instance)"}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
        />
      </Label>
      {target && (
        <div className="text-xs text-text-dim font-mono">
          {tenant.base_url} → {target.id === 0 ? "local server" : `${target.name} (${target.public_ipv4})`}
        </div>
      )}
      {sameHost && !port.trim() && (
        <p className="text-xs text-warn mt-2">
          Pick a different host or specify a new port.
        </p>
      )}
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Moving…" : "Move"}
          busy={busy}
          onClick={async () => {
            if (sameHost && !port.trim()) {
              setErr("pick a different host or specify a new port");
              return;
            }
            setBusy(true);
            setErr(null);
            const r = await onSubmit({
              instance_id: instanceID,
              ...(port.trim() ? { port: Number(port) } : {}),
            });
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

function ConnectTenantDialog({
  onClose,
  onSubmit,
}: {
  onClose: () => void;
  onSubmit: (v: {
    base_url: string;
    api_key: string;
    owner_email: string;
    slug?: string;
  }) => Promise<{ ok: boolean; error?: string }>;
}) {
  const [baseURL, setBaseURL] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [email, setEmail] = useState("");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  return (
    <DialogFrame title="Connect existing tenant" onClose={onClose}>
      <p className="text-xs text-text-dim mb-3">
        Registers an apteva-server that's already running. fleet probes
        <code className="font-mono"> /api/health </code>
        with the supplied bearer before persisting.
      </p>
      <Label text="Base URL">
        <input
          type="url"
          value={baseURL}
          onChange={(e) => setBaseURL(e.target.value)}
          placeholder="https://tenant.example.com"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
          autoFocus
        />
      </Label>
      <Label text="API key">
        <input
          type="password"
          value={apiKey}
          onChange={(e) => setAPIKey(e.target.value)}
          placeholder="sk-…"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
        />
      </Label>
      <Label text="Owner email">
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="ops@acme.com"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        />
      </Label>
      <Label text="Slug (optional)">
        <input
          type="text"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="derived from hostname"
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text"
        />
      </Label>
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Connecting…" : "Connect"}
          busy={busy}
          onClick={async () => {
            if (!baseURL || !apiKey || !email) {
              setErr("base_url, api_key, owner_email are required");
              return;
            }
            setBusy(true);
            setErr(null);
            const r = await onSubmit({
              base_url: baseURL,
              api_key: apiKey,
              owner_email: email,
              ...(slug ? { slug } : {}),
            });
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

function DialogFrame({
  title,
  children,
  onClose,
}: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-xl border border-border bg-bg-card shadow-xl">
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-border">
          <h2 className="text-sm font-semibold text-text">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="text-text-dim hover:text-text text-lg leading-none"
            aria-label="Close"
          >
            ×
          </button>
        </div>
        <div className="p-4 space-y-3">{children}</div>
      </div>
    </div>
  );
}

function DialogActions({ children }: { children: React.ReactNode }) {
  return <div className="flex items-center justify-end gap-2 pt-2">{children}</div>;
}

function Label({ text, children }: { text: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-[10px] uppercase tracking-wider text-text-dim font-medium mb-1">
        {text}
      </span>
      {children}
    </label>
  );
}

// ─── Domain block ───────────────────────────────────────────────────

function DomainBlock({
  tenant,
  meta,
  busy,
  onAttach,
  onDetach,
}: {
  tenant: Tenant;
  meta: MetaResp | null;
  busy: boolean;
  onAttach: () => void;
  onDetach: () => void;
}) {
  const hasIntegrations =
    !!meta && (meta.domains_available || meta.certs_available || meta.routes_available);
  // No integration AND no domain attached → render a quiet hint so the
  // pane doesn't go silent on what's actually a configurable feature.
  if (!tenant.domain && !hasIntegrations) {
    return (
      <div className="px-4 py-2 border-b border-border text-[11px] text-text-dim">
        <span className="font-medium">Public hostname</span> ·{" "}
        Install + bind the Domains / Certs / Routes apps to attach custom hostnames.
      </div>
    );
  }
  if (!tenant.domain) {
    return (
      <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-accent/5">
        <span className="text-[11px] uppercase tracking-wider text-text-dim font-semibold">
          Domain
        </span>
        <span className="text-xs text-text-dim flex-1">Not attached</span>
        <ActionButton label="Attach domain" onClick={onAttach} />
      </div>
    );
  }
  const clientManagedDNS = !tenant.domain_record_id;
  const cert = clientManagedDNS ? undefined : meta?.certs?.[tenant.domain];
  const certVariant: PillVariant = !cert
    ? "neutral"
    : cert.status === "live"
      ? "success"
      : cert.status === "pending" || cert.status === "issuing"
        ? "info"
        : cert.status === "error" || cert.status === "failed"
          ? "error"
          : "warn";
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-accent/5 flex-wrap">
      <span className="text-[11px] uppercase tracking-wider text-text-dim font-semibold">
        Domain
      </span>
      <a
        href={`https://${tenant.domain}`}
        target="_blank"
        rel="noopener"
        className="font-mono text-xs text-accent hover:underline"
      >
        {tenant.domain}
      </a>
      {cert && (
        <span title={cert.error || cert.expires_at || ""}>
          <StatusPill variant={certVariant}>{`cert · ${cert.status}`}</StatusPill>
        </span>
      )}
      {clientManagedDNS && (
        <StatusPill variant="success">edge HTTPS</StatusPill>
      )}
      {!cert && tenant.domain && !clientManagedDNS && (
        <StatusPill variant="neutral">no cert</StatusPill>
      )}
      {meta?.routes_available && (
        <StatusPill variant="info">routed</StatusPill>
      )}
      <span className="flex-1" />
      <ActionButton
        label={busy ? "Detaching…" : "Detach"}
        busy={busy}
        tone="danger"
        onClick={onDetach}
      />
    </div>
  );
}

// ─── Version block ──────────────────────────────────────────────────

function VersionBlock({
  tenant,
  latest,
  onUpdate,
}: {
  tenant: Tenant;
  latest?: string;
  onUpdate: () => void;
}) {
  const current = tenant.current_version || "";
  const target = tenant.target_version || "";
  // Three states drive the action surface:
  //   pendingApply   — operator pinned a target that hasn't taken effect
  //                    yet → "Apply <target>"
  //   updateAvailable — current is older than npm latest, no pin → "Update to <latest>"
  //   unknownCurrent  — health poller hasn't observed a version yet
  //                     (fresh spawn, or /api/health doesn't expose it) →
  //                     "Set version" so the operator can still pin one
  //   otherwise       — current matches latest (or no latest known) and
  //                     nothing pending → hide the button entirely;
  //                     "up to date" pill carries the message
  const pendingApply = !!target && !!current && target !== current;
  const updateAvailable =
    !pendingApply && !!latest && !!current && current !== latest;
  const unknownCurrent = !current;
  const showButton = pendingApply || updateAvailable || unknownCurrent;
  const buttonLabel = pendingApply
    ? `Apply ${target}`
    : updateAvailable
      ? `Update to ${latest}`
      : "Set version";
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-border flex-wrap">
      <span className="text-[11px] uppercase tracking-wider text-text-dim font-semibold">
        Version
      </span>
      <span className="font-mono text-xs text-text">
        {current || <span className="text-text-dim italic">unknown</span>}
      </span>
      {pendingApply && (
        <span className="font-mono text-[11px] text-warn">
          → target {target}
        </span>
      )}
      {updateAvailable && (
        <StatusPill variant="warn">{`update available · ${latest}`}</StatusPill>
      )}
      {!pendingApply && !updateAvailable && !unknownCurrent && (
        <StatusPill variant="success">up to date</StatusPill>
      )}
      <span className="flex-1" />
      {latest && (
        <span className="text-[11px] text-text-dim font-mono">npm: {latest}</span>
      )}
      {showButton && <ActionButton label={buttonLabel} onClick={onUpdate} />}
    </div>
  );
}

// ─── Attach-domain dialog ───────────────────────────────────────────

function AttachDomainDialog({
  tenant,
  meta,
  onClose,
  onSubmit,
}: {
  tenant: Tenant;
  meta: MetaResp;
  onClose: () => void;
  onSubmit: (args: {
    fqdn: string;
    target?: string;
    type?: string;
    manage_dns?: boolean;
  }) => Promise<{ ok: boolean; error?: string }>;
}) {
  // Two attach modes:
  //   "managed"  — apex is in our Domains catalog; Domains app writes
  //                the DNS record. Original 0.6.x behavior.
  //   "external" — client already pointed their DNS at this machine;
  //                we skip registrar + Certs-app issuance and only
  //                register the route. The parent edge proxy handles HTTPS.
  type Mode = "managed" | "external";
  const noDomains = meta.domains.length === 0;
  const [mode, setMode] = useState<Mode>(noDomains ? "external" : "managed");
  const [fqdn, setFqdn] = useState("");
  const [externalFqdn, setExternalFqdn] = useState("");
  const [apex, setApex] = useState(meta.domains[0]?.name || "");
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const composedFqdn = useMemo(() => {
    if (mode === "external") {
      return externalFqdn.trim().replace(/\.$/, "").toLowerCase();
    }
    const sub = fqdn.trim().replace(/\.$/, "");
    if (!apex) return sub;
    if (!sub) return apex;
    return `${sub}.${apex}`;
  }, [mode, fqdn, externalFqdn, apex]);

  const helpText =
    mode === "external"
      ? `The client already pointed DNS at this machine (${meta.public_host || "the parent host"}). Fleet skips DNS and Certs-app issuance; Routes proxies to the tenant and the parent edge proxy handles HTTPS.`
      : noDomains
        ? "No domains registered in the Domains app yet. Add one there first, or switch to “Client-managed DNS”."
        : "Pick the apex you've registered with the Domains app, then a subdomain (leave empty to attach the apex itself). target defaults to the parent host's public IP.";

  return (
    <DialogFrame title={`Attach domain to ${tenant.slug}`} onClose={onClose}>
      {/* Mode toggle — primary UX affordance for the two attach paths. */}
      <div className="flex items-center gap-1 mb-3 p-1 bg-bg-input rounded-md">
        <button
          type="button"
          onClick={() => setMode("managed")}
          disabled={noDomains}
          className={`flex-1 px-2 py-1 text-xs rounded ${
            mode === "managed"
              ? "bg-bg-card text-text font-medium"
              : "text-text-dim hover:text-text disabled:opacity-40"
          }`}
        >
          We manage DNS
        </button>
        <button
          type="button"
          onClick={() => setMode("external")}
          className={`flex-1 px-2 py-1 text-xs rounded ${
            mode === "external"
              ? "bg-bg-card text-text font-medium"
              : "text-text-dim hover:text-text"
          }`}
        >
          Client-managed DNS
        </button>
      </div>
      <p className="text-xs text-text-dim mb-3">{helpText}</p>

      {mode === "managed" ? (
        <>
          <Label text="Apex">
            <select
              value={apex}
              onChange={(e) => setApex(e.target.value)}
              className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
              disabled={noDomains}
            >
              {meta.domains.map((d) => (
                <option key={d.name} value={d.name}>
                  {d.name}
                </option>
              ))}
            </select>
          </Label>
          <Label text="Subdomain (optional)">
            <input
              type="text"
              value={fqdn}
              onChange={(e) => setFqdn(e.target.value)}
              placeholder={`e.g. ${tenant.slug}`}
              className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
            />
          </Label>
          <Label text="Target (optional)">
            <input
              type="text"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder={meta.public_host || "default: parent host's public IP"}
              className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
            />
          </Label>
        </>
      ) : (
        <>
          <Label text="Hostname (FQDN)">
            <input
              type="text"
              value={externalFqdn}
              onChange={(e) => setExternalFqdn(e.target.value)}
              placeholder="app.client.com"
              className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
              autoFocus
            />
          </Label>
          <div className="text-xs text-text-dim border border-border rounded-md px-3 py-2 bg-bg-input/50 space-y-1">
            <div className="font-medium text-text">Before submitting, verify:</div>
            <div>
              · client's DNS for{" "}
              <span className="font-mono text-text">{externalFqdn || "…"}</span>{" "}
              resolves to{" "}
              <span className="font-mono text-text">
                {meta.public_host || "this machine"}
              </span>
            </div>
            <div>· HTTPS traffic is served by the parent edge proxy</div>
          </div>
        </>
      )}

      <div className="text-xs text-text-dim font-mono mt-2">
        Will attach: <span className="text-text">{composedFqdn || "—"}</span>
      </div>
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Attaching…" : "Attach"}
          busy={busy}
          onClick={async () => {
            if (!composedFqdn) {
              setErr(
                mode === "external"
                  ? "enter the FQDN (e.g. app.client.com)"
                  : "pick an apex (subdomain optional)",
              );
              return;
            }
            setBusy(true);
            setErr(null);
            const r = await onSubmit({
              fqdn: composedFqdn,
              ...(mode === "managed" && target ? { target } : {}),
              ...(mode === "external" ? { manage_dns: false } : {}),
            });
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

// ─── Update-version dialog ──────────────────────────────────────────

function UpdateVersionDialog({
  tenant,
  latest,
  onClose,
  onSubmit,
}: {
  tenant: Tenant;
  latest?: string;
  onClose: () => void;
  onSubmit: (version: string) => Promise<{ ok: boolean; error?: string }>;
}) {
  const [version, setVersion] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const resolved = version.trim() || latest || "(latest)";

  return (
    <DialogFrame title={`Update ${tenant.slug}`} onClose={onClose}>
      <p className="text-xs text-text-dim mb-3">
        Installs the requested apteva version into a fleet-owned npm prefix
        at <code className="font-mono">~/.apteva-fleet/versions/&lt;v&gt;/</code>,
        stops the tenant, and respawns it. Other tenants are unaffected.
        Leave empty to track npm latest.
      </p>
      <div className="text-xs text-text-dim font-mono mb-2 space-y-0.5">
        <div>
          current: <span className="text-text">{tenant.current_version || "—"}</span>
        </div>
        {tenant.target_version && (
          <div>
            target: <span className="text-text">{tenant.target_version}</span>
          </div>
        )}
        {latest && (
          <div>
            npm latest: <span className="text-text">{latest}</span>
          </div>
        )}
      </div>
      <Label text="Version (empty = latest)">
        <input
          type="text"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          placeholder={latest || "0.10.0"}
          className="w-full px-2 py-1.5 text-sm rounded-md border border-border bg-bg-card text-text font-mono"
          autoFocus
        />
      </Label>
      <div className="text-[11px] text-text-dim font-mono">
        Will install + respawn at: <span className="text-text">{resolved}</span>
      </div>
      {err && <p className="text-xs text-error mt-2">{err}</p>}
      <DialogActions>
        <ActionButton label="Cancel" onClick={onClose} />
        <ActionButton
          label={busy ? "Updating…" : "Update"}
          busy={busy}
          onClick={async () => {
            setBusy(true);
            setErr(null);
            const r = await onSubmit(version.trim());
            setBusy(false);
            if (!r.ok) setErr(r.error || "failed");
          }}
        />
      </DialogActions>
    </DialogFrame>
  );
}

// ─── Credentials block + reveal dialogs ─────────────────────────────

function CredentialsBlock({
  tenantId: _tenantId,
  busyReveal,
  busyReset,
  onReveal,
  onReset,
}: {
  tenantId: string;
  busyReveal: boolean;
  busyReset: boolean;
  onReveal: () => void;
  onReset: () => void;
}) {
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-border flex-wrap">
      <span className="text-[11px] uppercase tracking-wider text-text-dim font-semibold">
        Credentials
      </span>
      <span className="text-xs text-text-dim flex-1">
        API key revealed on demand; admin password is rotated (not stored).
      </span>
      <ActionButton
        label={busyReveal ? "Revealing…" : "Reveal API key"}
        busy={busyReveal}
        onClick={onReveal}
      />
      <ActionButton
        label={busyReset ? "Rotating…" : "Reset admin password"}
        busy={busyReset}
        onClick={onReset}
      />
    </div>
  );
}

function RevealAPIKeyDialog({
  data,
  onClose,
}: {
  data: { slug: string; base_url: string; api_key: string };
  onClose: () => void;
}) {
  return (
    <DialogFrame title={`API key — ${data.slug}`} onClose={onClose}>
      <div className="bg-accent/5 border border-border rounded-md px-3 py-2 mb-3 text-xs text-text-dim">
        The api_key authenticates machine-to-machine calls against this
        tenant (Bearer auth). Fleet keeps it sealed in its keyring; revealing
        records an event but doesn't rotate it. Use{" "}
        <span className="font-medium text-text">Reset admin password</span> to
        get a fresh human-login credential.
      </div>
      <CredentialRow label="Tenant URL" value={data.base_url} />
      <CredentialRow label="API key" value={data.api_key} sensitive />
      <DialogActions>
        <ActionButton label="Done" onClick={onClose} />
      </DialogActions>
    </DialogFrame>
  );
}

function ResetPasswordDialog({
  data,
  onClose,
}: {
  data: {
    slug: string;
    base_url: string;
    admin_email: string;
    admin_password: string;
  };
  onClose: () => void;
}) {
  return (
    <DialogFrame title={`New admin password — ${data.slug}`} onClose={onClose}>
      <div className="bg-warn/10 border border-warn/30 rounded-md px-3 py-2 mb-3 text-xs text-warn">
        Shown only once. Every existing session for this admin has been
        revoked — give the operator the new password before dismissing.
      </div>
      <CredentialRow label="Tenant URL" value={data.base_url} />
      <CredentialRow label="Admin email" value={data.admin_email} />
      <CredentialRow label="New password" value={data.admin_password} sensitive />
      <DialogActions>
        <ActionButton label="I've saved it" onClick={onClose} />
      </DialogActions>
    </DialogFrame>
  );
}

// ─── Helpers ────────────────────────────────────────────────────────

function shortBaseURL(url: string): string {
  // Strip protocol for compactness, keep host:port intact.
  return url.replace(/^https?:\/\//, "");
}

function formatTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const now = Date.now();
  const diff = (now - d.getTime()) / 1000;
  if (diff < 60) return `${Math.floor(diff)}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return d.toLocaleString();
}
