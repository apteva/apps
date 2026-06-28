import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface PlanLimit {
  feature_key: string;
  limit_value: number;
  reset_interval: string;
}

interface Plan {
  key: string;
  name: string;
  billing_mode: "free" | "paid";
  image: string;
  cpu: number;
  memory_mb: number;
  storage_mb: number;
  limits?: PlanLimit[];
}

interface Tenant {
  id: string;
  customer_id: number;
  subscription_id?: number;
  workload_id: string;
  slug: string;
  default_hostname: string;
  owner_email: string;
  plan_key: string;
  status: string;
  image: string;
  last_health_status: string;
  last_error: string;
  created_at: string;
  updated_at: string;
}

interface Customer {
  id: number;
  email: string;
  name: string;
}

interface UsageTotal {
  customer_id: number;
  tenant_id?: string;
  feature_key: string;
  quantity: number;
}

const API = "/api/apps/hosting";

function tone(status: string): string {
  switch (status) {
    case "active":
    case "ok":
    case "healthy":
      return "bg-green/15 text-green";
    case "provisioning":
    case "running":
      return "bg-accent/15 text-accent";
    case "suspended":
    case "stopped":
    case "past_due":
      return "bg-warn/15 text-warn";
    case "failed":
    case "error":
    case "deleted":
      return "bg-red/15 text-red";
    default:
      return "bg-bg-input text-text-dim";
  }
}

function fmtDate(s: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (!Number.isFinite(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function gb(mb: number): string {
  if (!mb) return "0 GB";
  return `${Math.round((mb / 1024) * 10) / 10} GB`;
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API}${path}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
  return (await res.json()) as T;
}

export default function HostingPanel(_props: NativePanelProps) {
  const [plans, setPlans] = useState<Plan[]>([]);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [usage, setUsage] = useState<UsageTotal[]>([]);
  const [busy, setBusy] = useState("");
  const [status, setStatus] = useState("Loading hosting...");
  const [error, setError] = useState("");
  const [logs, setLogs] = useState<{ tenant: string; body: string } | null>(null);
  const [createdCustomer, setCreatedCustomer] = useState<Customer | null>(null);
  const [form, setForm] = useState({
    owner_email: "",
    customer_name: "",
    slug: "",
    plan_key: "free",
  });

  const selected = useMemo(
    () => tenants.find((t) => t.id === selectedId) || tenants[0] || null,
    [tenants, selectedId],
  );

  const selectedPlan = useMemo(
    () => plans.find((p) => p.key === form.plan_key) || plans[0] || null,
    [plans, form.plan_key],
  );

  const load = useCallback(async () => {
    try {
      const [p, t] = await Promise.all([
        getJSON<{ plans: Plan[] }>("/plans"),
        getJSON<{ tenants: Tenant[] }>("/tenants"),
      ]);
      setPlans(p.plans || []);
      const rows = t.tenants || [];
      setTenants(rows);
      if (!selectedId && rows[0]) setSelectedId(rows[0].id);
      setStatus(`${rows.length} tenant${rows.length === 1 ? "" : "s"}`);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setStatus("Load failed");
    }
  }, [selectedId]);

  const loadUsage = useCallback(async (tenant: Tenant | null) => {
    if (!tenant) {
      setUsage([]);
      return;
    }
    try {
      const u = await getJSON<{ usage: UsageTotal[] }>(
        `/usage?tenant_id=${encodeURIComponent(tenant.id)}`,
      );
      setUsage(u.usage || []);
    } catch {
      setUsage([]);
    }
  }, []);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 8000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    loadUsage(selected);
  }, [selected, loadUsage]);

  const callTool = useCallback(async <T,>(tool: string, args: Record<string, unknown>): Promise<T> => {
    const res = await fetch(`${API}/mcp`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: { name: tool, arguments: args },
      }),
    });
    if (!res.ok) throw new Error(`${tool}: ${res.status} ${await res.text().catch(() => "")}`);
    const body = await res.json();
    if (body.error) throw new Error(body.error.message || tool);
    const text = body.result?.content?.[0]?.text;
    return text ? JSON.parse(text) as T : body.result as T;
  }, []);

  const createTenant = useCallback(async () => {
    setBusy("create");
    setError("");
    try {
      const email = form.owner_email.trim();
      const slug = form.slug.trim();
      if (!email || !slug) throw new Error("owner email and slug are required");
      const created = await callTool<{ customer: Customer }>("hosting_customer_create", {
        email,
        name: form.customer_name.trim(),
      });
      setCreatedCustomer(created.customer);
      const out = await callTool<{ tenant: Tenant }>("hosting_tenant_create", {
        customer_id: created.customer.id,
        owner_email: email,
        slug,
        plan_key: form.plan_key,
      });
      setSelectedId(out.tenant.id);
      setStatus(`Created ${out.tenant.slug}`);
      setForm((f) => ({ ...f, slug: "" }));
      await load();
    } catch (e) {
      setError((e as Error).message);
      setStatus("Create failed");
    } finally {
      setBusy("");
    }
  }, [callTool, form, load]);

  const tenantAction = useCallback(async (tenant: Tenant, action: "suspend" | "resume" | "restart" | "health" | "delete") => {
    setBusy(`${action}:${tenant.id}`);
    setError("");
    try {
      const tool = {
        suspend: "hosting_tenant_suspend",
        resume: "hosting_tenant_resume",
        restart: "hosting_tenant_restart",
        health: "hosting_tenant_health",
        delete: "hosting_tenant_delete",
      }[action];
      await callTool(tool, { tenant_id: tenant.id });
      await load();
      setStatus(`${action} complete`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [callTool, load]);

  const showLogs = useCallback(async (tenant: Tenant) => {
    setBusy(`logs:${tenant.id}`);
    setError("");
    try {
      const out = await callTool<{ logs: string }>("hosting_tenant_logs", { tenant_id: tenant.id, tail: 300 });
      setLogs({ tenant: tenant.slug, body: out.logs || "" });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [callTool]);

  return (
    <div className="grid grid-cols-[340px_1fr] h-full bg-bg text-text">
      <aside className="border-r border-border flex flex-col min-h-0">
        <header className="px-4 py-3 border-b border-border flex items-center justify-between">
          <div>
            <h2 className="text-sm font-semibold">Hosting</h2>
            <p className="text-xs text-text-dim">{status}</p>
          </div>
          <button type="button" onClick={load} className="px-2 py-1 rounded bg-bg-input text-xs hover:bg-bg-hover">
            Refresh
          </button>
        </header>

        <section className="p-3 border-b border-border space-y-2">
          <div className="grid grid-cols-2 gap-2">
            <label className="col-span-2">
              <span className="block text-[10px] uppercase text-text-dim mb-1">Owner email</span>
              <input
                value={form.owner_email}
                onChange={(e) => setForm((f) => ({ ...f, owner_email: e.target.value }))}
                className="w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm"
                placeholder="owner@example.com"
              />
            </label>
            <label className="col-span-2">
              <span className="block text-[10px] uppercase text-text-dim mb-1">Customer name</span>
              <input
                value={form.customer_name}
                onChange={(e) => setForm((f) => ({ ...f, customer_name: e.target.value }))}
                className="w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm"
                placeholder="Acme Inc"
              />
            </label>
            <label>
              <span className="block text-[10px] uppercase text-text-dim mb-1">Slug</span>
              <input
                value={form.slug}
                onChange={(e) => setForm((f) => ({ ...f, slug: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-") }))}
                className="w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm font-mono"
                placeholder="acme"
              />
            </label>
            <label>
              <span className="block text-[10px] uppercase text-text-dim mb-1">Plan</span>
              <select
                value={form.plan_key}
                onChange={(e) => setForm((f) => ({ ...f, plan_key: e.target.value }))}
                className="w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm"
              >
                {plans.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
              </select>
            </label>
          </div>
          {selectedPlan && (
            <div className="grid grid-cols-3 gap-1 text-[11px] text-text-dim">
              <span>{selectedPlan.cpu} CPU</span>
              <span>{selectedPlan.memory_mb} MB</span>
              <span>{gb(selectedPlan.storage_mb)}</span>
            </div>
          )}
          <button
            type="button"
            disabled={busy === "create"}
            onClick={createTenant}
            className="w-full px-3 py-1.5 rounded bg-accent text-bg text-sm font-semibold disabled:opacity-50"
          >
            {busy === "create" ? "Creating..." : "Create Tenant"}
          </button>
          {createdCustomer && (
            <p className="text-[11px] text-text-dim">Customer #{createdCustomer.id}: {createdCustomer.email}</p>
          )}
        </section>

        <div className="flex-1 overflow-auto">
          {tenants.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setSelectedId(t.id)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-input ${selected?.id === t.id ? "bg-bg-input" : ""}`}
            >
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm truncate flex-1">{t.slug}</span>
                <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${tone(t.status)}`}>{t.status}</span>
              </div>
              <div className="text-xs text-text-dim truncate mt-0.5">{t.default_hostname}</div>
              <div className="text-[11px] text-text-dim mt-1">{t.plan_key} · {fmtDate(t.created_at)}</div>
            </button>
          ))}
        </div>
      </aside>

      <main className="min-w-0 min-h-0 flex flex-col">
        {error && <div className="px-4 py-2 border-b border-red/30 bg-red/10 text-red text-xs">{error}</div>}
        {!selected ? (
          <div className="p-6 text-sm text-text-dim">No hosted tenants yet.</div>
        ) : (
          <>
            <header className="px-5 py-4 border-b border-border flex items-start gap-4">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <h1 className="text-lg font-semibold font-mono truncate">{selected.slug}</h1>
                  <span className={`px-2 py-0.5 rounded text-xs font-medium ${tone(selected.status)}`}>{selected.status}</span>
                  <span className={`px-2 py-0.5 rounded text-xs font-medium ${tone(selected.last_health_status)}`}>health · {selected.last_health_status || "unknown"}</span>
                </div>
                <a href={`https://${selected.default_hostname}`} target="_blank" rel="noreferrer" className="text-sm text-accent hover:underline font-mono">
                  {selected.default_hostname}
                </a>
                <div className="text-xs text-text-dim mt-1">
                  customer #{selected.customer_id} · workload {selected.workload_id || "pending"} · {selected.image}
                </div>
              </div>
              <div className="flex items-center gap-2 flex-wrap justify-end">
                <Action label="Health" busy={busy === `health:${selected.id}`} onClick={() => tenantAction(selected, "health")} />
                <Action label="Logs" busy={busy === `logs:${selected.id}`} onClick={() => showLogs(selected)} />
                <Action label="Restart" busy={busy === `restart:${selected.id}`} onClick={() => tenantAction(selected, "restart")} />
                {selected.status === "active" ? (
                  <Action label="Suspend" busy={busy === `suspend:${selected.id}`} onClick={() => tenantAction(selected, "suspend")} />
                ) : (
                  <Action label="Resume" busy={busy === `resume:${selected.id}`} onClick={() => tenantAction(selected, "resume")} />
                )}
                <Action label="Delete" tone="danger" busy={busy === `delete:${selected.id}`} onClick={() => {
                  if (window.confirm(`Delete ${selected.slug}?`)) tenantAction(selected, "delete");
                }} />
              </div>
            </header>

            <section className="grid grid-cols-4 gap-3 p-4 border-b border-border">
              <Metric label="Plan" value={selected.plan_key} />
              <Metric label="Owner" value={selected.owner_email} />
              <Metric label="Created" value={fmtDate(selected.created_at)} />
              <Metric label="Updated" value={fmtDate(selected.updated_at)} />
            </section>

            <section className="p-4 grid grid-cols-[1fr_360px] gap-4 min-h-0 flex-1">
              <div className="border border-border rounded-md overflow-hidden min-h-0 flex flex-col">
                <div className="px-3 py-2 border-b border-border text-xs uppercase text-text-dim font-semibold">Usage</div>
                <div className="overflow-auto">
                  {usage.length === 0 ? (
                    <div className="p-4 text-sm text-text-dim">No usage recorded yet.</div>
                  ) : (
                    usage.map((u) => (
                      <div key={`${u.customer_id}-${u.tenant_id}-${u.feature_key}`} className="px-3 py-2 border-b border-border flex items-center gap-3">
                        <span className="font-mono text-sm flex-1">{u.feature_key}</span>
                        <span className="font-mono text-sm text-text">{u.quantity}</span>
                      </div>
                    ))
                  )}
                </div>
              </div>

              <div className="border border-border rounded-md overflow-hidden min-h-0 flex flex-col">
                <div className="px-3 py-2 border-b border-border text-xs uppercase text-text-dim font-semibold">Plan Limits</div>
                <div className="overflow-auto">
                  {(plans.find((p) => p.key === selected.plan_key)?.limits || []).map((l) => (
                    <div key={l.feature_key} className="px-3 py-2 border-b border-border">
                      <div className="font-mono text-xs text-text">{l.feature_key}</div>
                      <div className="text-xs text-text-dim">{l.limit_value} · {l.reset_interval}</div>
                    </div>
                  ))}
                </div>
              </div>
            </section>
          </>
        )}
      </main>

      {logs && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
          <div className="w-full max-w-4xl max-h-[80vh] rounded-md border border-border bg-bg-card flex flex-col">
            <header className="px-4 py-3 border-b border-border flex items-center justify-between">
              <h2 className="text-sm font-semibold">Logs · {logs.tenant}</h2>
              <button type="button" onClick={() => setLogs(null)} className="text-text-dim hover:text-text text-lg leading-none">×</button>
            </header>
            <pre className="p-4 overflow-auto text-xs font-mono whitespace-pre-wrap text-text-dim">{logs.body || "(empty)"}</pre>
          </div>
        </div>
      )}
    </div>
  );
}

function Action({ label, busy, onClick, tone: t = "normal" }: { label: string; busy?: boolean; onClick: () => void; tone?: "normal" | "danger" }) {
  return (
    <button
      type="button"
      disabled={busy}
      onClick={onClick}
      className={`px-2.5 py-1 rounded text-xs font-medium disabled:opacity-50 ${t === "danger" ? "bg-red/10 text-red hover:bg-red/15" : "bg-bg-input text-text hover:bg-bg-hover"}`}
    >
      {busy ? "..." : label}
    </button>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] uppercase text-text-dim font-semibold mb-1">{label}</div>
      <div className="text-sm text-text truncate">{value || "—"}</div>
    </div>
  );
}
