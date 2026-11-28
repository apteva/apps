import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface Access {
  host: string;
  port: number;
  user: string;
  ssh_command: string;
  granted: boolean;
  issue?: string;
}

interface Tenant {
  id: string;
  external_ref: string;
  profile_key: string;
  backend: string;
  machine_id?: number;
  instance_id?: number;
  slug: string;
  default_hostname?: string;
  owner_email: string;
  status: string;
  last_error: string;
  access?: Access;
  device?: { managed_by: string; device_ref: string; session_held: boolean; online: boolean };
  created_at: string;
  released_at?: string;
  due_at?: string;
}

interface OperationLog {
  at: string;
  step: string;
  result: string;
  detail?: string;
}

interface Operation {
  id: string;
  kind: string;
  tenant_id?: string;
  machine_id?: number;
  steps: string[];
  step: number;
  status: string;
  attempts: number;
  error: string;
  log: OperationLog[];
  current_step?: string;
  created_at: string;
  finished_at?: string;
}

interface Pool {
  key: string;
  name: string;
  platform: string;
  managed_by?: string;
  execution_enabled: boolean;
  counts: Record<string, number>;
}

interface Machine {
  id: number;
  pool_key: string;
  instance_id?: number;
  device_ref?: string;
  device_session?: string;
  managed_by?: string;
  name: string;
  state: string;
  state_reason: string;
  online: boolean;
  serial: string;
  public_host: string;
  validated: boolean;
  tenant_id?: string;
  for_sale: boolean;
  not_for_sale?: string;
  connect_mode?: string;
  last_seen_at?: string;
  public_port: number;
  endpoint_source?: string;
  endpoint_issue?: string;
  operation?: Operation;
  tenant?: Tenant;
}

interface Availability {
  for_sale: boolean;
  available?: number;
  machines?: number;
  presale?: number;
  presale_limit?: number;
  presale_note?: string;
  presale_ready_days?: number;
  waiting?: number;
  reason?: string;
}

interface Profile {
  key: string;
  name: string;
  backend: string;
  product_key: string;
  pool_key?: string;
  cpu: number;
  memory_mb: number;
  storage_mb: number;
  settings: Record<string, unknown> | null;
  status: string;
  availability?: Availability;
}

type View = "tenants" | "machines" | "profiles";

const API = "/api/apps/hosting";

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
  return (await res.json()) as T;
}

async function callTool<T>(tool: string, args: Record<string, unknown>): Promise<T> {
  const res = await fetch(`${API}/mcp`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: tool, arguments: args } }),
  });
  if (!res.ok) throw new Error(`${tool}: ${res.status} ${await res.text().catch(() => "")}`);
  const body = await res.json();
  if (body.error) throw new Error(body.error.message || tool);
  const text = body.result?.content?.[0]?.text;
  if (body.result?.isError) throw new Error(text || `${tool} failed`);
  return text ? (JSON.parse(text) as T) : (body.result as T);
}

function tone(status: string): string {
  switch (status) {
    case "active":
    case "available":
    case "assigned":
    case "succeeded":
      return "bg-green/15 text-green";
    case "provisioning":
    case "preparing":
    case "cleaning":
    case "running":
    case "reserved":
    case "resuming":
    case "suspending":
    case "releasing":
      return "bg-accent/15 text-accent";
    case "suspended":
    case "waiting":
    case "waiting_for_machine":
    case "degraded":
    case "maintenance":
    case "queued":
      return "bg-warn/15 text-warn";
    case "failed":
    case "quarantined":
      return "bg-red/15 text-red";
    default:
      return "bg-bg-input text-text-dim";
  }
}

function Badge({ value }: { value: string }) {
  return <span className={`px-1.5 py-0.5 rounded text-xs ${tone(value)}`}>{value.replace(/_/g, " ")}</span>;
}

function fmtDate(s?: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (!Number.isFinite(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

const BACKEND_LABEL: Record<string, string> = {
  container: "Container",
  machine_pool: "Pool machine",
  machine_on_demand: "On-demand machine",
};

const CONNECT_LABEL: Record<string, string> = {
  direct: "direct",
  vpn: "VPN",
  reverse_tunnel: "tunnel",
  local: "local",
};

const btn = "px-2 py-1 rounded bg-bg-input text-xs hover:bg-bg-hover disabled:opacity-50";
const btnDanger = "px-2 py-1 rounded bg-red/15 text-red text-xs hover:bg-red/25 disabled:opacity-50";
const input = "w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm";
const label = "block text-xs text-text-dim mb-1";

// Confirm asks for a typed value before a destructive action runs.
function Confirm({ title, expect, detail, onCancel, onConfirm }: {
  title: string;
  expect: string;
  detail: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  return (
    <div className="fixed inset-0 flex items-center justify-center" style={{ background: "rgba(0,0,0,0.45)", zIndex: 50 }}>
      <div className="bg-bg border border-border rounded p-4 space-y-3" style={{ width: 380 }}>
        <h3 className="text-sm font-semibold">{title}</h3>
        <p className="text-xs text-text-dim">{detail}</p>
        <label className="block">
          <span className={label}>Type <span className="font-mono text-text">{expect}</span> to confirm</span>
          <input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} className={`${input} font-mono`} />
        </label>
        <div className="flex justify-end gap-2">
          <button type="button" className={btn} onClick={onCancel}>Cancel</button>
          <button type="button" className={btnDanger} disabled={typed !== expect} onClick={onConfirm}>Confirm</button>
        </div>
      </div>
    </div>
  );
}

function RunLog({ op }: { op: Operation }) {
  return (
    <div className="border border-border rounded p-2 space-y-1">
      <div className="flex items-center gap-2 text-xs">
        <span className="font-semibold">{op.kind}</span>
        <Badge value={op.status} />
        <span className="text-text-dim">{op.step}/{op.steps.length}{op.current_step ? ` · ${op.current_step}` : ""}</span>
        <span className="text-text-dim ml-auto">{fmtDate(op.created_at)}</span>
      </div>
      {op.error && <p className="text-xs text-red break-all">{op.error}</p>}
      {op.log.length > 0 && (
        <ul className="text-xs text-text-dim font-mono space-y-0.5">
          {op.log.slice(-8).map((l, i) => (
            <li key={i} className="break-all">{fmtDate(l.at)} {l.step} {l.result}{l.detail ? ` – ${l.detail}` : ""}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

export default function HostingPanel(_props: NativePanelProps) {
  const [view, setView] = useState<View>("tenants");
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [showReleased, setShowReleased] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detailOps, setDetailOps] = useState<Operation[]>([]);
  const [pools, setPools] = useState<Pool[]>([]);
  const [machines, setMachines] = useState<Machine[]>([]);
  const [failedRuns, setFailedRuns] = useState<Operation[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirm, setConfirm] = useState<null | { title: string; expect: string; detail: string; run: () => Promise<void> }>(null);
  const [createForm, setCreateForm] = useState({ external_ref: "", profile: "", owner_email: "", ssh_keys: "" });
  const [machineForm, setMachineForm] = useState({ pool_key: "", instance_id: "", device_ref: "", serial: "", management_user: "" });
  const [poolForm, setPoolForm] = useState({ key: "", name: "", platform: "macos" });

  const selected = useMemo(() => tenants.find((t) => t.id === selectedId) || null, [tenants, selectedId]);
  const phonePool = !!pools.find((p) => p.key === (machineForm.pool_key || pools[0]?.key))?.managed_by;

  const load = useCallback(async () => {
    try {
      const [t, p, m, f, pr] = await Promise.all([
        getJSON<{ tenants: Tenant[] }>(`/tenants${showReleased ? "?include_released=1" : ""}`),
        getJSON<{ pools: Pool[] }>("/pools"),
        getJSON<{ machines: Machine[] }>("/machines"),
        getJSON<{ operations: Operation[] }>("/operations?status=failed"),
        getJSON<{ profiles: Profile[] }>("/profiles"),
      ]);
      setTenants(t.tenants || []);
      setPools(p.pools || []);
      setMachines(m.machines || []);
      setFailedRuns(f.operations || []);
      setProfiles(pr.profiles || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [showReleased]);

  const loadDetail = useCallback(async (id: string | null) => {
    if (!id) {
      setDetailOps([]);
      return;
    }
    try {
      const d = await getJSON<{ operations: Operation[] }>(`/tenants/${encodeURIComponent(id)}`);
      setDetailOps(d.operations || []);
    } catch {
      setDetailOps([]);
    }
  }, []);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 8000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    loadDetail(selectedId);
  }, [selectedId, loadDetail, tenants]);

  const act = useCallback(async (key: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(key);
    setError("");
    setNotice("");
    try {
      await fn();
      setNotice(done);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [load]);

  const ask = (title: string, expect: string, detail: string, run: () => Promise<void>) =>
    setConfirm({ title, expect, detail, run });

  const createTenant = () =>
    act("create", async () => {
      const keys = createForm.ssh_keys.split("\n").map((k) => k.trim()).filter(Boolean);
      const out = await callTool<{ tenant: Tenant }>("hosting_tenant_create", {
        external_ref: createForm.external_ref.trim(),
        profile: createForm.profile || profiles[0]?.key,
        owner_email: createForm.owner_email.trim(),
        ...(keys.length ? { ssh_keys: keys } : {}),
      });
      setSelectedId(out.tenant.id);
      setCreateForm({ external_ref: "", profile: createForm.profile, owner_email: "", ssh_keys: "" });
    }, "Tenant created");

  const tenantAction = (t: Tenant, tool: string, done: string, extra: Record<string, unknown> = {}) =>
    act(`${tool}:${t.id}`, () => callTool(tool, { tenant_id: t.id, ...extra }), done);

  const stuck = (t: Tenant) => {
    const m = machines.find((x) => x.id === t.machine_id);
    return t.backend !== "container" && t.status !== "released" &&
      (["failed", "suspending", "resuming", "releasing"].includes(t.status) || m?.state === "quarantined");
  };

  const machineAction = (m: Machine, tool: string, args: Record<string, unknown>, done: string) =>
    act(`${tool}:${m.id}`, () => callTool(tool, { machine_id: m.id, ...args }), done);

  const tabs: { key: View; label: string; count?: number }[] = [
    { key: "tenants", label: "Tenants", count: tenants.length },
    { key: "machines", label: "Machines", count: machines.length },
    { key: "profiles", label: "Profiles", count: profiles.length },
  ];

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="px-4 py-3 border-b border-border flex items-center gap-3">
        <h2 className="text-sm font-semibold">Hosting</h2>
        <nav className="flex gap-1">
          {tabs.map((t) => (
            <button
              key={t.key}
              type="button"
              onClick={() => setView(t.key)}
              className={`px-2 py-1 rounded text-xs ${view === t.key ? "bg-bg-input text-text" : "text-text-dim hover:bg-bg-hover"}`}
            >
              {t.label}{t.count != null ? ` ${t.count}` : ""}
            </button>
          ))}
        </nav>
        <span className="text-xs text-text-dim ml-auto">{notice}</span>
        <button type="button" onClick={load} className={btn}>Refresh</button>
      </header>
      {error && <div className="px-4 py-2 text-xs text-red border-b border-border break-all">{error}</div>}

      <div className="flex-1 min-h-0 overflow-auto">
        {view === "tenants" && (
          <div className="flex h-full min-h-0">
            <aside className="border-r border-border flex flex-col min-h-0" style={{ width: 340, flexShrink: 0 }}>
              <section className="p-3 border-b border-border space-y-2">
                <div className="grid grid-cols-2 gap-2">
                  <label>
                    <span className={label}>External ref</span>
                    <input className={`${input} font-mono`} value={createForm.external_ref} placeholder="acct_123"
                      onChange={(e) => setCreateForm((f) => ({ ...f, external_ref: e.target.value }))} />
                  </label>
                  <label>
                    <span className={label}>Profile</span>
                    <select className={input} value={createForm.profile || profiles[0]?.key || ""}
                      onChange={(e) => setCreateForm((f) => ({ ...f, profile: e.target.value }))}>
                      {profiles.map((p) => <option key={p.key} value={p.key}>{p.name || p.key}</option>)}
                    </select>
                  </label>
                  <label className="col-span-2">
                    <span className={label}>Owner email</span>
                    <input className={input} value={createForm.owner_email} placeholder="owner@example.com"
                      onChange={(e) => setCreateForm((f) => ({ ...f, owner_email: e.target.value }))} />
                  </label>
                  {profiles.find((p) => p.key === (createForm.profile || profiles[0]?.key))?.backend !== "container" && (
                    <label className="col-span-2">
                      <span className={label}>SSH public keys, one per line</span>
                      <textarea className={`${input} font-mono text-xs`} rows={2} value={createForm.ssh_keys}
                        onChange={(e) => setCreateForm((f) => ({ ...f, ssh_keys: e.target.value }))} />
                    </label>
                  )}
                </div>
                <button type="button" disabled={busy === "create" || !createForm.external_ref || !createForm.owner_email}
                  onClick={createTenant} className="w-full px-3 py-1.5 rounded bg-accent text-bg text-sm font-semibold disabled:opacity-50">
                  {busy === "create" ? "Creating..." : "Create tenant"}
                </button>
              </section>
              <label className="px-4 py-2 border-b border-border text-xs text-text-dim flex items-center gap-2">
                <input type="checkbox" checked={showReleased} onChange={(e) => setShowReleased(e.target.checked)} />
                Show released
              </label>
              <div className="flex-1 overflow-auto">
                {tenants.length === 0 && <p className="p-4 text-xs text-text-dim">No tenants yet.</p>}
                {tenants.map((t) => (
                  <button key={t.id} type="button" onClick={() => setSelectedId(t.id)}
                    className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-hover ${selectedId === t.id ? "bg-bg-input" : ""}`}>
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium truncate">{t.owner_email}</span>
                      <span className="ml-auto"><Badge value={t.status} /></span>
                    </div>
                    <div className="text-xs text-text-dim truncate">{t.profile_key} · {t.external_ref || t.slug}</div>
                    {t.status === "waiting_for_machine" && t.due_at && (
                      <div className={`text-xs ${new Date(t.due_at.replace(" ", "T") + (t.due_at.endsWith("Z") ? "" : "Z")) < new Date() ? "text-red" : "text-text-dim"}`}>
                        Promised by {t.due_at.slice(0, 10)}
                      </div>
                    )}
                  </button>
                ))}
              </div>
            </aside>

            <section className="flex-1 min-w-0 p-4 space-y-4 overflow-auto">
              {!selected && <p className="text-sm text-text-dim">Select a tenant.</p>}
              {selected && (
                <>
                  <div className="flex items-start gap-3">
                    <div className="min-w-0">
                      <h3 className="text-base font-semibold truncate">{selected.owner_email}</h3>
                      <p className="text-xs text-text-dim font-mono">{selected.id} · ref {selected.external_ref || "none"}</p>
                    </div>
                    <span className="ml-auto"><Badge value={selected.status} /></span>
                  </div>
                  <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
                    <dt className="text-text-dim">Profile</dt><dd>{selected.profile_key} ({BACKEND_LABEL[selected.backend] || selected.backend})</dd>
                    {selected.default_hostname && (<><dt className="text-text-dim">Hostname</dt><dd className="font-mono">{selected.default_hostname}</dd></>)}
                    {selected.machine_id ? (<><dt className="text-text-dim">Machine</dt><dd>#{selected.machine_id}</dd></>) : null}
                    {selected.instance_id ? (<><dt className="text-text-dim">Instance</dt><dd>#{selected.instance_id}</dd></>) : null}
                    {selected.device && (<><dt className="text-text-dim">Phone</dt><dd className="font-mono break-all">{selected.device.device_ref} · {selected.device.online ? "online" : "offline"}{selected.device.session_held ? " · session held" : " · no session"}</dd></>)}
                    {selected.access && (<><dt className="text-text-dim">SSH</dt><dd className="font-mono break-all">{selected.access.issue ? <span className="text-red">{selected.access.issue}</span> : selected.access.granted ? selected.access.ssh_command : "access removed"}</dd></>)}
                    <dt className="text-text-dim">Created</dt><dd>{fmtDate(selected.created_at)}</dd>
                    {selected.released_at && (<><dt className="text-text-dim">Released</dt><dd>{fmtDate(selected.released_at)}</dd></>)}
                  </dl>
                  {selected.last_error && <p className="text-xs text-red break-all">{selected.last_error}</p>}
                  {selected.status !== "released" && (
                    <div className="flex flex-wrap gap-2">
                      {(selected.status === "active" || selected.status === "degraded") && (
                        <button type="button" className={btn} disabled={!!busy} onClick={() => tenantAction(selected, "hosting_tenant_suspend", "Suspending")}>Suspend</button>
                      )}
                      {selected.status === "suspended" && (
                        <button type="button" className={btn} disabled={!!busy} onClick={() => tenantAction(selected, "hosting_tenant_resume", "Resuming")}>Resume</button>
                      )}
                      {selected.status === "active" && !selected.device && (
                        <button type="button" className={btn} disabled={!!busy} onClick={() => tenantAction(selected, "hosting_tenant_restart", "Restarting")}>Restart</button>
                      )}
                      {selected.device && (selected.status === "active" || selected.status === "degraded") && (
                        <button type="button" className={btnDanger} disabled={!!busy}
                          onClick={() => ask("Swap phone", selected.slug, "Moves the tenant to a free phone in the same pool. The customer gets a new session; the old phone is quarantined for cleaning.",
                            () => tenantAction(selected, "hosting_tenant_swap", "Swap started", { confirm: selected.id, reason: "swapped from panel" }))}>Swap phone</button>
                      )}
                      {selected.backend === "machine_pool" && selected.status === "active" && !selected.device && (
                        <button type="button" className={btnDanger} disabled={!!busy}
                          onClick={() => ask("Reinstall machine", selected.slug, "Erases the machine and reinstalls macOS for the same tenant. The customer's data is lost.",
                            () => tenantAction(selected, "hosting_tenant_reinstall", "Reinstall started", { confirm: selected.id }))}>Reinstall</button>
                      )}
                      <button type="button" className={btnDanger} disabled={!!busy}
                        onClick={() => ask("Release tenant", selected.slug,
                          selected.backend === "container" ? "Destroys the container and its hostname." : selected.device ? "Stops the customer's session and cleans the phone back to its baseline." : "Removes access and wipes or destroys the machine.",
                          () => tenantAction(selected, "hosting_tenant_release", "Release started", { confirm: selected.id }))}>Release</button>
                    </div>
                  )}
                  {stuck(selected) && (
                    <div className="border border-border rounded p-3 space-y-2">
                      <p className="text-xs text-text-dim">This tenant was left by a failed run or a quarantined machine. Check the machine and the log, then settle it.</p>
                      <div className="flex flex-wrap gap-2">
                        {(["resume", "release", "force_clean"] as const).map((action) => (
                          <button key={action} type="button" className={action === "resume" ? btn : btnDanger} disabled={!!busy}
                            onClick={() => ask(`Resolve: ${action.replace("_", " ")}`, selected.slug,
                              action === "resume" ? "Restores the customer's access and runs the tenant again."
                                : action === "release" ? "Removes access and resets the machine as a normal release."
                                : "Erases the macOS machine without revoking access first, then puts it back on sale.",
                              () => tenantAction(selected, "hosting_tenant_resolve", "Resolve started", { action, reviewed: true, confirm: selected.id }))}>
                            {action === "force_clean" ? "Force clean" : action === "resume" ? "Resume" : "Release"}
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                  <div className="space-y-2">
                    <h4 className="text-xs font-semibold text-text-dim">Runs</h4>
                    {detailOps.length === 0 && <p className="text-xs text-text-dim">No runs.</p>}
                    {detailOps.map((op) => <RunLog key={op.id} op={op} />)}
                  </div>
                </>
              )}
            </section>
          </div>
        )}

        {view === "machines" && (
          <div className="p-4 space-y-5">
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">Pools</h3>
              {pools.length === 0 && <p className="text-xs text-text-dim">No pools yet.</p>}
              <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))" }}>
                {pools.map((p) => (
                  <div key={p.key} className="border border-border rounded p-3 space-y-2 bg-bg-card">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{p.name || p.key}</span>
                      <span className="text-xs text-text-dim">{p.managed_by ? `${p.platform} phones via ${p.managed_by}` : p.platform}</span>
                      <span className="ml-auto">
                        <Badge value={p.execution_enabled ? "active" : "maintenance"} />
                      </span>
                    </div>
                    <div className="text-xs text-text-dim">
                      {p.counts.for_sale || 0} for sale · {p.counts.assigned || 0} assigned · {p.counts.reserved || 0} reserved · {p.counts.quarantined || 0} quarantined · {p.counts.offline || 0} offline
                    </div>
                    <button type="button" className={p.execution_enabled ? btnDanger : btn} disabled={!!busy}
                      onClick={() => act(`exec:${p.key}`, () => callTool("hosting_pool_set_execution", { key: p.key, enabled: !p.execution_enabled }),
                        p.execution_enabled ? "Execution off" : "Execution on")}>
                      {p.execution_enabled ? "Stop execution" : "Allow execution"}
                    </button>
                  </div>
                ))}
              </div>
              <div className="flex flex-wrap items-end gap-2">
                <label><span className={label}>Key</span><input className={`${input} font-mono`} style={{ width: 120 }} value={poolForm.key} onChange={(e) => setPoolForm((f) => ({ ...f, key: e.target.value }))} /></label>
                <label><span className={label}>Name</span><input className={input} style={{ width: 160 }} value={poolForm.name} onChange={(e) => setPoolForm((f) => ({ ...f, name: e.target.value }))} /></label>
                <label><span className={label}>Platform</span>
                  <select className={input} value={poolForm.platform} onChange={(e) => setPoolForm((f) => ({ ...f, platform: e.target.value }))}>
                    <option value="macos">macOS</option><option value="linux">Linux</option><option value="android">Android phones (Mobile)</option>
                  </select>
                </label>
                <button type="button" className={btn} disabled={!poolForm.key || !!busy}
                  onClick={() => act("pool", () => callTool("hosting_pool_upsert", { ...poolForm, managed_by: poolForm.platform === "android" ? "mobile" : "" }), "Pool saved")}>Save pool</button>
              </div>
            </section>

            <section className="space-y-2">
              <h3 className="text-sm font-semibold">Machines</h3>
              <div className="flex flex-wrap items-end gap-2">
                <label><span className={label}>Pool</span>
                  <select className={input} value={machineForm.pool_key || pools[0]?.key || ""} onChange={(e) => setMachineForm((f) => ({ ...f, pool_key: e.target.value }))}>
                    {pools.map((p) => <option key={p.key} value={p.key}>{p.name || p.key}</option>)}
                  </select>
                </label>
                {phonePool ? (
                  <label><span className={label}>Device id (from Mobile)</span><input className={`${input} font-mono`} style={{ width: 200 }} value={machineForm.device_ref} onChange={(e) => setMachineForm((f) => ({ ...f, device_ref: e.target.value.trim() }))} /></label>
                ) : (
                  <>
                    <label><span className={label}>Instance id</span><input className={input} style={{ width: 100 }} value={machineForm.instance_id} onChange={(e) => setMachineForm((f) => ({ ...f, instance_id: e.target.value.replace(/\D/g, "") }))} /></label>
                    <label><span className={label}>Serial</span><input className={`${input} font-mono`} style={{ width: 140 }} value={machineForm.serial} onChange={(e) => setMachineForm((f) => ({ ...f, serial: e.target.value }))} /></label>
                    <label><span className={label}>Management user</span><input className={`${input} font-mono`} style={{ width: 140 }} value={machineForm.management_user} onChange={(e) => setMachineForm((f) => ({ ...f, management_user: e.target.value }))} /></label>
                  </>
                )}
                <button type="button" className={btn} disabled={(phonePool ? !machineForm.device_ref : !machineForm.instance_id) || pools.length === 0 || !!busy}
                  onClick={() => act("machine", () => callTool("hosting_machine_add", phonePool ? {
                    pool_key: machineForm.pool_key || pools[0]?.key,
                    device_ref: machineForm.device_ref,
                  } : {
                    pool_key: machineForm.pool_key || pools[0]?.key,
                    instance_id: Number(machineForm.instance_id),
                    serial: machineForm.serial,
                    management_user: machineForm.management_user,
                  }), "Machine added (quarantined until inspected and validated)")}>Add machine</button>
              </div>
              {machines.length === 0 ? <p className="text-xs text-text-dim">No machines yet.</p> : (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="text-left text-text-dim border-b border-border">
                      <th className="py-1 pr-2">Machine</th><th className="py-1 pr-2">State</th><th className="py-1 pr-2">Tenant</th><th className="py-1 pr-2">Run</th><th className="py-1">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {machines.map((m) => (
                      <tr key={m.id} className="border-b border-border align-top">
                        <td className="py-2 pr-2">
                          <div className="font-medium">#{m.id} {m.name}</div>
                          {m.device_ref ? (
                            <div className="text-text-dim">{m.pool_key} · phone <span className="font-mono">{m.device_ref}</span> · {m.online ? "online" : "offline"}{m.validated ? " · validated" : " · not validated"}{m.last_seen_at ? ` · seen ${fmtDate(m.last_seen_at)}` : ""}</div>
                          ) : (<>
                          <div className="text-text-dim">{m.pool_key} · instance {m.instance_id} · {CONNECT_LABEL[m.connect_mode || ""] || m.connect_mode || "direct"} · {m.online ? "online" : m.connect_mode === "reverse_tunnel" ? "tunnel down" : "offline"}{m.validated ? " · validated" : " · not validated"}</div>
                          <div className="text-text-dim">
                            {m.public_host ? <span className="font-mono">{m.public_host}:{m.public_port}</span> : "no customer endpoint"}
                            {m.endpoint_source === "manual" ? " (set by hand)" : ""}
                            {m.last_seen_at ? ` · seen ${fmtDate(m.last_seen_at)}` : ""}
                          </div>
                          </>)}
                        </td>
                        <td className="py-2 pr-2">
                          <Badge value={m.state} />
                          {m.state_reason && <div className="text-text-dim break-all mt-1">{m.state_reason}</div>}
                          {!m.for_sale && m.not_for_sale && m.not_for_sale !== "assigned" && m.not_for_sale !== m.state && (
                            <div className="text-warn break-all mt-1" title={m.endpoint_issue || ""}>Not for sale: {m.not_for_sale}</div>
                          )}
                        </td>
                        <td className="py-2 pr-2">{m.tenant ? m.tenant.owner_email : ""}</td>
                        <td className="py-2 pr-2">{m.operation ? <span>{m.operation.kind} <Badge value={m.operation.status} /></span> : ""}</td>
                        <td className="py-2">
                          <div className="flex flex-wrap gap-1">
                            {!m.tenant_id && (
                              <button type="button" className={btn} disabled={!!busy} onClick={() => machineAction(m, "hosting_machine_inspect", {}, "Inspect started")}>Inspect</button>
                            )}
                            {!m.validated && (
                              <button type="button" className={btn} disabled={!!busy}
                                onClick={() => ask("Mark machine validated", String(m.id), m.device_ref ? "Only after inspect recorded the phone's baseline as it should be sold. Validated phones are cleaned and sold automatically." : "Only after its erase and recovery have been tested by hand. Validated machines can be wiped automatically.",
                                  () => machineAction(m, "hosting_machine_update", { validated: true, confirm: String(m.id) }, "Machine validated"))}>Validate</button>
                            )}
                            {m.state === "available" && (
                              <button type="button" className={btn} disabled={!!busy} onClick={() => machineAction(m, "hosting_machine_set_maintenance", { maintenance: true }, "Off sale")}>Maintenance</button>
                            )}
                            {m.state === "maintenance" && (
                              <button type="button" className={btn} disabled={!!busy} onClick={() => machineAction(m, "hosting_machine_set_maintenance", { maintenance: false }, "Back on sale")}>End maintenance</button>
                            )}
                            {m.state !== "quarantined" && (
                              <button type="button" className={btnDanger} disabled={!!busy} onClick={() => machineAction(m, "hosting_machine_quarantine", { reason: "quarantined from panel" }, "Quarantined")}>Quarantine</button>
                            )}
                            {!m.tenant_id && m.validated && (
                              <button type="button" className={btnDanger} disabled={!!busy}
                                onClick={() => ask("Clean machine", String(m.id), m.device_ref ? "Stops any Hosting session, cleans the phone back to its baseline, verifies it and puts it back on sale." : "Erases the machine, verifies it is clean and puts it back on sale.",
                                  () => machineAction(m, "hosting_machine_clean", { confirm: String(m.id) }, "Clean started"))}>Clean</button>
                            )}
                            {!m.tenant_id && (
                              <button type="button" className={btnDanger} disabled={!!busy}
                                onClick={() => ask("Remove machine", String(m.id), "Removes the machine from its pool. The Instances host is not touched.",
                                  () => machineAction(m, "hosting_machine_remove", { confirm: String(m.id) }, "Machine removed"))}>Remove</button>
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </section>

            <section className="space-y-2">
              <h3 className="text-sm font-semibold">Failed runs</h3>
              {failedRuns.length === 0 && <p className="text-xs text-text-dim">None.</p>}
              {failedRuns.map((op) => (
                <div key={op.id} className="space-y-1">
                  <RunLog op={op} />
                  <button type="button" className={btn} disabled={!!busy}
                    onClick={() => ask("Retry run", "reviewed", "Retry only after checking the machine and the log above.",
                      () => act(`retry:${op.id}`, () => callTool("hosting_operation_retry", { operation_id: op.id, reviewed: true }), "Retry started"))}>
                    Retry after review
                  </button>
                </div>
              ))}
            </section>
          </div>
        )}

        {view === "profiles" && (
          <div className="p-4">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-left text-text-dim border-b border-border">
                  <th className="py-1 pr-2">Profile</th><th className="py-1 pr-2">Backend</th><th className="py-1 pr-2">Product</th><th className="py-1 pr-2">Size or source</th><th className="py-1">Availability</th>
                </tr>
              </thead>
              <tbody>
                {profiles.map((p) => {
                  const s = (p.settings || {}) as Record<string, string>;
                  const size = p.backend === "container"
                    ? `${p.cpu} CPU · ${p.memory_mb} MB · ${Math.round(p.storage_mb / 102.4) / 10} GB`
                    : p.backend === "machine_pool" ? `pool ${p.pool_key}` : `${s.provider || ""} ${s.server_type || ""} ${s.region || ""}`;
                  const av = p.availability;
                  return (
                    <tr key={p.key} className="border-b border-border">
                      <td className="py-2 pr-2"><div className="font-medium">{p.name || p.key}</div><div className="text-text-dim font-mono">{p.key}</div></td>
                      <td className="py-2 pr-2">{BACKEND_LABEL[p.backend] || p.backend}</td>
                      <td className="py-2 pr-2">{p.product_key}</td>
                      <td className="py-2 pr-2">
                        {size}
                        {p.backend === "machine_pool" && (p.settings as Record<string, unknown> | null)?.require_unlocked_handover === false && <div className="text-text-dim">Handover lock check off</div>}
                        {p.backend === "machine_pool" && Array.isArray((p.settings as Record<string, unknown> | null)?.clear_system_apps) && <div className="text-text-dim">Clears {((p.settings as Record<string, unknown>).clear_system_apps as unknown[]).length} system app(s)</div>}
                      </td>
                      <td className="py-2">
                        {av && <Badge value={av.for_sale ? "available" : "maintenance"} />}
                        {av?.machines != null && <span className="text-text-dim"> {av.machines} free</span>}
                        {av?.presale_limit != null && <span className="text-text-dim"> · presale {av.presale ?? 0} of {av.presale_limit} left</span>}
                        {av?.presale_note && <div className="text-text-dim">{av.presale_note}</div>}
                        {av?.presale_ready_days != null && <div className="text-text-dim">Promise: ready within {av.presale_ready_days} days</div>}
                        {av?.waiting ? <span className="text-text-dim"> · {av.waiting} waiting</span> : null}
                        {av?.reason && <div className="text-text-dim">{av.reason}</div>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            <p className="text-xs text-text-dim mt-3">Profiles are edited with hosting_profile_upsert. Prices and plans live in the SaaS app.</p>
          </div>
        )}
      </div>

      {confirm && (
        <Confirm title={confirm.title} expect={confirm.expect} detail={confirm.detail}
          onCancel={() => setConfirm(null)}
          onConfirm={() => { const run = confirm.run; setConfirm(null); run(); }} />
      )}
    </div>
  );
}
