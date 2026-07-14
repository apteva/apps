import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface PlanFeature {
  feature_key: string;
  grant_type: string;
}

interface PlanLimit {
  feature_key: string;
  limit_value: number;
  reset_interval: string;
}

interface UsageSource {
  app_name: string;
  tool_name: string;
  feature_prefix?: string;
}

interface Plan {
  key: string;
  name: string;
  billing_mode: string;
  subscription_required: boolean;
  catalog_discount_id?: number;
  features?: PlanFeature[];
  limits?: PlanLimit[];
  usage_sources?: UsageSource[];
}

interface Account {
  id: string;
  customer_id: number;
  auth_org_id?: number;
  auth_user_id?: number;
  subscription_id?: number;
  slug: string;
  owner_email: string;
  plan_key: string;
  status: string;
  last_usage_sync_at?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

interface Checkout {
  id: string;
  status: string;
  payment_url?: string;
  account_id?: string;
  trial_ends_at?: string;
  last_error?: string;
}

interface CheckoutResponse {
  checkout: Checkout;
  account?: Account;
  status: string;
  requires_payment: boolean;
  url?: string;
  discount?: {
    status: string;
    discount_code?: string;
    catalog_discount_id: number;
  };
  pricing?: {
    currency: string;
    subtotal_cents: number;
    discount_cents: number;
    total_cents: number;
  };
}

interface UsageTotal {
  account_id: string;
  customer_id: number;
  feature_key: string;
  quantity: number;
  limit_value?: number;
  over_limit: boolean;
  observed_at?: string;
  source_count: number;
}

interface AccessResult {
  allowed: boolean;
  entitled: boolean;
  status: string;
  over_limit: boolean;
  usage: UsageTotal[];
}

const API = "/api/apps/saas";
const CONTROL = "w-full bg-bg-card border border-border rounded px-2 py-1.5 text-sm text-text";

function tone(status: string): string {
  switch (status) {
    case "active":
    case "allowed":
      return "bg-green/15 text-green";
    case "provisioning":
      return "bg-accent/15 text-accent";
    case "past_due":
    case "suspended":
      return "bg-warn/15 text-warn";
    case "cancelled":
    case "failed":
    case "denied":
      return "bg-red/15 text-red";
    default:
      return "bg-bg-input text-text-dim";
  }
}

function fmtDate(s?: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (!Number.isFinite(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function compactNumber(n: number | undefined): string {
  if (n === undefined || n === null) return "-";
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(n);
}

function fmtMoney(cents: number, currency: string): string {
  return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD" }).format(cents / 100);
}

function featureOptions(plan: Plan | null, usage: UsageTotal[]): string[] {
  const out = new Set<string>();
  for (const f of plan?.features || []) out.add(f.feature_key);
  for (const l of plan?.limits || []) out.add(l.feature_key);
  for (const u of usage) out.add(u.feature_key);
  return Array.from(out).sort();
}

export default function SaaSPanel({ projectId }: NativePanelProps) {
  const [plans, setPlans] = useState<Plan[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [usage, setUsage] = useState<UsageTotal[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState("");
  const [busy, setBusy] = useState("");
  const [status, setStatus] = useState("Loading SaaS...");
  const [error, setError] = useState("");
  const [accessFeature, setAccessFeature] = useState("");
  const [accessResult, setAccessResult] = useState<AccessResult | null>(null);
  const [checkout, setCheckout] = useState<CheckoutResponse | null>(null);
  const [form, setForm] = useState({
    owner_email: "",
    customer_name: "",
    slug: "",
    plan_key: "free",
    discount_code: "",
    create_owner_user: true,
  });

  const query = useCallback((extra: Record<string, string> = {}) => {
    const q = new URLSearchParams();
    if (projectId) q.set("project_id", projectId);
    for (const [k, v] of Object.entries(extra)) if (v) q.set(k, v);
    const s = q.toString();
    return s ? `?${s}` : "";
  }, [projectId]);

  const getJSON = useCallback(async <T,>(path: string, params: Record<string, string> = {}): Promise<T> => {
    const res = await fetch(`${API}${path}${query(params)}`, { credentials: "same-origin" });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return await res.json() as T;
  }, [query]);

  const callTool = useCallback(async <T,>(tool: string, args: Record<string, unknown>): Promise<T> => {
    const res = await fetch(`${API}/mcp`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: { name: tool, arguments: { _project_id: projectId, ...args } },
      }),
    });
    if (!res.ok) throw new Error(`${tool}: ${res.status} ${await res.text().catch(() => "")}`);
    const body = await res.json();
    if (body.error) throw new Error(body.error.message || tool);
    const text = body.result?.content?.[0]?.text;
    return text ? JSON.parse(text) as T : body.result as T;
  }, [projectId]);

  const selected = useMemo(
    () => accounts.find((a) => a.id === selectedId) || accounts[0] || null,
    [accounts, selectedId],
  );

  const selectedPlan = useMemo(
    () => plans.find((p) => p.key === selected?.plan_key) || null,
    [plans, selected?.plan_key],
  );

  const createPlan = useMemo(
    () => plans.find((p) => p.key === form.plan_key) || plans[0] || null,
    [plans, form.plan_key],
  );

  const features = useMemo(() => featureOptions(selectedPlan, usage), [selectedPlan, usage]);

  const summary = useMemo(() => {
    const active = accounts.filter((a) => a.status === "active").length;
    const pastDue = accounts.filter((a) => a.status === "past_due").length;
    const over = usage.filter((u) => u.over_limit).length;
    return { active, pastDue, over };
  }, [accounts, usage]);

  const load = useCallback(async () => {
    try {
      const [p, a] = await Promise.all([
        getJSON<{ plans: Plan[] }>("/plans"),
        getJSON<{ accounts: Account[] }>("/accounts", statusFilter ? { status: statusFilter } : {}),
      ]);
      const planRows = p.plans || [];
      const accountRows = a.accounts || [];
      setPlans(planRows);
      setAccounts(accountRows);
      if (!form.plan_key && planRows[0]) setForm((f) => ({ ...f, plan_key: planRows[0].key }));
      if (!selectedId && accountRows[0]) setSelectedId(accountRows[0].id);
      setStatus(`${accountRows.length} account${accountRows.length === 1 ? "" : "s"}`);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setStatus("Load failed");
    }
  }, [form.plan_key, getJSON, selectedId, statusFilter]);

  const loadUsage = useCallback(async (account: Account | null) => {
    if (!account) {
      setUsage([]);
      return;
    }
    try {
      const out = await getJSON<{ usage: UsageTotal[] }>("/usage", { account_id: account.id });
      setUsage(out.usage || []);
    } catch {
      setUsage([]);
    }
  }, [getJSON]);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 8000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    loadUsage(selected);
    setAccessResult(null);
  }, [loadUsage, selected]);

  useEffect(() => {
    if (!accessFeature && features[0]) setAccessFeature(features[0]);
  }, [accessFeature, features]);

  useEffect(() => {
    if (!checkout?.checkout.id || !["processing", "awaiting_payment", "awaiting_payment_method"].includes(checkout.status)) return;
    const timer = window.setInterval(async () => {
      try {
        const next = await callTool<CheckoutResponse>("saas_checkout_get", { checkout_id: checkout.checkout.id });
        setCheckout(next);
        if (next.account?.id) setSelectedId(next.account.id);
        if (["active", "trialing"].includes(next.status)) await load();
      } catch (e) {
        setError((e as Error).message);
      }
    }, 3000);
    return () => window.clearInterval(timer);
  }, [callTool, checkout?.checkout.id, checkout?.status, load]);

  const createAccount = useCallback(async () => {
    setBusy("create");
    setError("");
    try {
      const email = form.owner_email.trim();
      const slug = form.slug.trim();
      if (!email || !slug) throw new Error("owner email and slug are required");
      const out = await callTool<CheckoutResponse>("saas_checkout_create", {
        owner_email: email,
        customer_name: form.customer_name.trim(),
        slug,
        plan_key: form.plan_key,
        discount_code: form.discount_code.trim(),
        create_owner_user: form.create_owner_user,
        idempotency_key: `panel:${slug}`,
      });
      setCheckout(out);
      if (out.account?.id) setSelectedId(out.account.id);
      setForm((f) => ({ ...f, slug: "", discount_code: "" }));
      setStatus(out.status);
      await load();
    } catch (e) {
      setError((e as Error).message);
      setStatus("Create failed");
    } finally {
      setBusy("");
    }
  }, [callTool, form, load]);

  const accountAction = useCallback(async (account: Account, action: "suspend" | "resume" | "cancel" | "sync") => {
    setBusy(`${action}:${account.id}`);
    setError("");
    try {
      if (action === "sync") {
        await callTool("saas_usage_sync", { account_id: account.id });
        await loadUsage(account);
      } else {
        await callTool(`saas_account_${action}`, { account_id: account.id });
        await load();
      }
      setStatus(`${action} complete`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [callTool, load, loadUsage]);

  const checkAccess = useCallback(async () => {
    if (!selected || !accessFeature) return;
    setBusy(`access:${selected.id}`);
    setError("");
    try {
      const out = await callTool<AccessResult>("saas_access_check", {
        account_id: selected.id,
        feature_key: accessFeature,
      });
      setAccessResult(out);
    } catch (e) {
      setError((e as Error).message);
      setAccessResult(null);
    } finally {
      setBusy("");
    }
  }, [accessFeature, callTool, selected]);

  return (
    <div className="h-full min-h-0 bg-bg text-text grid grid-cols-[340px_minmax(0,1fr)]">
      <aside className="min-h-0 border-r border-border flex flex-col">
        <header className="px-4 py-3 border-b border-border flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <h1 className="text-sm font-semibold">SaaS</h1>
            <p className="text-xs text-text-dim truncate">{status}</p>
          </div>
          <button type="button" onClick={load} className="px-2 py-1 rounded bg-bg-input text-xs hover:bg-bg-hover">
            Refresh
          </button>
        </header>

        <section className="p-3 border-b border-border space-y-2">
          <div className="grid grid-cols-2 gap-2">
            <Field label="Owner email" className="col-span-2">
              <input
                value={form.owner_email}
                onChange={(e) => setForm((f) => ({ ...f, owner_email: e.target.value }))}
                className={CONTROL}
                placeholder="owner@example.com"
              />
            </Field>
            <Field label="Customer" className="col-span-2">
              <input
                value={form.customer_name}
                onChange={(e) => setForm((f) => ({ ...f, customer_name: e.target.value }))}
                className={CONTROL}
                placeholder="Acme Inc"
              />
            </Field>
            <Field label="Slug">
              <input
                value={form.slug}
                onChange={(e) => setForm((f) => ({ ...f, slug: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-") }))}
                className={`${CONTROL} font-mono`}
                placeholder="acme"
              />
            </Field>
            <Field label="Plan">
              <select
                value={form.plan_key}
                onChange={(e) => setForm((f) => ({ ...f, plan_key: e.target.value, discount_code: "" }))}
                className={CONTROL}
              >
                {plans.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
              </select>
            </Field>
            <Field label="Discount code" className="col-span-2">
              <input
                value={form.discount_code}
                onChange={(e) => setForm((f) => ({ ...f, discount_code: e.target.value.toUpperCase().replace(/\s/g, "") }))}
                className={`${CONTROL} font-mono`}
                placeholder={createPlan?.catalog_discount_id ? "Automatic plan discount" : "PROMO"}
                disabled={Boolean(createPlan?.catalog_discount_id)}
              />
            </Field>
          </div>
          <label className="flex items-center gap-2 text-xs text-text-dim">
            <input
              type="checkbox"
              checked={form.create_owner_user}
              onChange={(e) => setForm((f) => ({ ...f, create_owner_user: e.target.checked }))}
              className="h-3.5 w-3.5"
            />
            Create owner user
          </label>
          {createPlan && (
            <div className="grid grid-cols-3 gap-1 text-[11px] text-text-dim">
              <span>{createPlan.billing_mode}</span>
              <span>{createPlan.features?.length || 0} grants</span>
              <span>{createPlan.limits?.length || 0} limits</span>
            </div>
          )}
          <button
            type="button"
            disabled={busy === "create"}
            onClick={createAccount}
            className="w-full px-3 py-1.5 rounded bg-accent text-bg text-sm font-semibold disabled:opacity-50"
          >
            {busy === "create" ? "Starting..." : "Start Checkout"}
          </button>
          {checkout && (
            <div className="border-t border-border pt-2 space-y-2">
              <div className="flex items-center gap-2">
                <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${tone(checkout.status)}`}>{checkout.status}</span>
                {checkout.discount && <span className="text-[11px] font-mono text-text-dim">{checkout.discount.discount_code || `discount #${checkout.discount.catalog_discount_id}`}</span>}
                {checkout.url && (
                  <button type="button" onClick={() => window.open(checkout.url, "_blank", "noopener,noreferrer")} className="ml-auto px-2 py-1 rounded bg-bg-input text-xs hover:bg-bg-hover">
                    Continue
                  </button>
                )}
              </div>
              {checkout.pricing && (
                <div className="grid grid-cols-3 gap-2 text-[11px]">
                  <Metric label="Subtotal" value={fmtMoney(checkout.pricing.subtotal_cents, checkout.pricing.currency)} />
                  <Metric label="Discount" value={`-${fmtMoney(checkout.pricing.discount_cents, checkout.pricing.currency)}`} />
                  <Metric label="Total" value={fmtMoney(checkout.pricing.total_cents, checkout.pricing.currency)} />
                </div>
              )}
            </div>
          )}
        </section>

        <section className="p-3 border-b border-border">
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className={`${CONTROL} text-xs`}
          >
            <option value="">all statuses</option>
            <option value="active">active</option>
            <option value="past_due">past_due</option>
            <option value="suspended">suspended</option>
            <option value="cancelled">cancelled</option>
            <option value="failed">failed</option>
          </select>
        </section>

        <div className="flex-1 min-h-0 overflow-auto">
          {accounts.length === 0 ? (
            <div className="p-4 text-sm text-text-dim">No accounts match this view.</div>
          ) : accounts.map((account) => (
            <button
              key={account.id}
              type="button"
              onClick={() => setSelectedId(account.id)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-input ${selected?.id === account.id ? "bg-bg-input" : ""}`}
            >
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm truncate flex-1">{account.slug}</span>
                <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${tone(account.status)}`}>{account.status}</span>
              </div>
              <div className="text-xs text-text-dim truncate mt-0.5">{account.owner_email}</div>
              <div className="text-[11px] text-text-dim mt-1">{account.plan_key} · {fmtDate(account.created_at)}</div>
            </button>
          ))}
        </div>
      </aside>

      <main className="min-w-0 min-h-0 flex flex-col">
        {error && <div className="px-4 py-2 border-b border-red/30 bg-red/10 text-red text-xs">{error}</div>}
        {!selected ? (
          <div className="p-6 text-sm text-text-dim">Create or select a SaaS account.</div>
        ) : (
          <>
            <header className="px-5 py-4 border-b border-border flex items-start gap-4">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <h2 className="text-lg font-semibold font-mono truncate">{selected.slug}</h2>
                  <span className={`px-2 py-0.5 rounded text-xs font-medium ${tone(selected.status)}`}>{selected.status}</span>
                </div>
                <div className="text-sm text-text-dim truncate">{selected.owner_email}</div>
                <div className="text-xs text-text-dim mt-1">
                  customer #{selected.customer_id} · account {selected.id} · plan {selected.plan_key}
                </div>
              </div>
              <div className="flex items-center justify-end gap-2 flex-wrap">
                <Action label="Sync Usage" busy={busy === `sync:${selected.id}`} onClick={() => accountAction(selected, "sync")} />
                {selected.status === "active" || selected.status === "past_due" ? (
                  <Action label="Suspend" busy={busy === `suspend:${selected.id}`} onClick={() => accountAction(selected, "suspend")} />
                ) : selected.status === "suspended" ? (
                  <Action label="Resume" busy={busy === `resume:${selected.id}`} onClick={() => accountAction(selected, "resume")} />
                ) : null}
                {selected.status !== "cancelled" && (
                  <Action label="Cancel" tone="danger" busy={busy === `cancel:${selected.id}`} onClick={() => accountAction(selected, "cancel")} />
                )}
              </div>
            </header>

            <section className="grid grid-cols-4 gap-3 p-4 border-b border-border">
              <Metric label="Active" value={String(summary.active)} />
              <Metric label="Past due" value={String(summary.pastDue)} />
              <Metric label="Plans" value={String(plans.length)} />
              <Metric label="Over limit" value={String(summary.over)} tone={summary.over > 0 ? "warn" : "normal"} />
            </section>

            <section className="p-4 border-b border-border grid grid-cols-[minmax(0,1fr)_240px_auto] gap-2 items-end">
              <Field label="Feature">
                {features.length > 0 ? (
                  <select value={accessFeature} onChange={(e) => setAccessFeature(e.target.value)} className={CONTROL}>
                    {features.map((f) => <option key={f} value={f}>{f}</option>)}
                  </select>
                ) : (
                  <input value={accessFeature} onChange={(e) => setAccessFeature(e.target.value)} className={`${CONTROL} font-mono`} placeholder="crm:contacts" />
                )}
              </Field>
              <div>
                {accessResult && (
                  <span className={`inline-flex px-2 py-1 rounded text-xs font-medium ${tone(accessResult.allowed ? "allowed" : "denied")}`}>
                    {accessResult.allowed ? "allowed" : "denied"}
                  </span>
                )}
              </div>
              <button
                type="button"
                disabled={!accessFeature || busy === `access:${selected.id}`}
                onClick={checkAccess}
                className="px-3 py-1.5 rounded bg-bg-input text-sm hover:bg-bg-hover disabled:opacity-50"
              >
                {busy === `access:${selected.id}` ? "Checking..." : "Check Access"}
              </button>
            </section>

            <section className="p-4 grid grid-cols-[minmax(0,1fr)_380px] gap-4 min-h-0 flex-1">
              <div className="border border-border rounded-md overflow-hidden min-h-0 flex flex-col">
                <div className="px-3 py-2 border-b border-border text-xs uppercase text-text-dim font-semibold">Live Usage</div>
                <div className="overflow-auto">
                  {usage.length === 0 ? (
                    <div className="p-4 text-sm text-text-dim">No usage snapshots yet.</div>
                  ) : usage.map((u) => (
                    <div key={`${u.account_id}-${u.feature_key}`} className="px-3 py-2 border-b border-border grid grid-cols-[minmax(0,1fr)_100px_100px_80px] gap-3 items-center">
                      <span className="font-mono text-sm truncate">{u.feature_key}</span>
                      <span className="font-mono text-sm text-right">{compactNumber(u.quantity)}</span>
                      <span className="font-mono text-sm text-right text-text-dim">{u.limit_value ? compactNumber(u.limit_value) : "-"}</span>
                      <span className={`justify-self-end px-1.5 py-0.5 rounded text-[10px] font-medium ${u.over_limit ? tone("denied") : tone("allowed")}`}>
                        {u.over_limit ? "over" : "ok"}
                      </span>
                    </div>
                  ))}
                </div>
              </div>

              <div className="min-h-0 grid grid-rows-[minmax(0,1fr)_minmax(0,1fr)] gap-4">
                <div className="border border-border rounded-md overflow-hidden min-h-0 flex flex-col">
                  <div className="px-3 py-2 border-b border-border text-xs uppercase text-text-dim font-semibold">Plan Access</div>
                  <div className="overflow-auto">
                    {(selectedPlan?.features || []).map((f) => (
                      <div key={f.feature_key} className="px-3 py-2 border-b border-border">
                        <div className="font-mono text-xs text-text">{f.feature_key}</div>
                        <div className="text-xs text-text-dim">{f.grant_type || "access"}</div>
                      </div>
                    ))}
                    {(selectedPlan?.features || []).length === 0 && <div className="p-3 text-sm text-text-dim">No grants configured.</div>}
                  </div>
                </div>

                <div className="border border-border rounded-md overflow-hidden min-h-0 flex flex-col">
                  <div className="px-3 py-2 border-b border-border text-xs uppercase text-text-dim font-semibold">Limits and Sources</div>
                  <div className="overflow-auto">
                    {(selectedPlan?.limits || []).map((l) => (
                      <div key={l.feature_key} className="px-3 py-2 border-b border-border">
                        <div className="font-mono text-xs text-text">{l.feature_key}</div>
                        <div className="text-xs text-text-dim">{compactNumber(l.limit_value)} · {l.reset_interval}</div>
                      </div>
                    ))}
                    {(selectedPlan?.usage_sources || []).map((s) => (
                      <div key={`${s.app_name}-${s.tool_name}`} className="px-3 py-2 border-b border-border">
                        <div className="font-mono text-xs text-text">{s.app_name}.{s.tool_name}</div>
                        <div className="text-xs text-text-dim">{s.feature_prefix || "all features"}</div>
                      </div>
                    ))}
                    {(selectedPlan?.limits || []).length === 0 && (selectedPlan?.usage_sources || []).length === 0 && (
                      <div className="p-3 text-sm text-text-dim">No limits or usage sources configured.</div>
                    )}
                  </div>
                </div>
              </div>
            </section>
          </>
        )}
      </main>
    </div>
  );
}

function Field({ label, children, className = "" }: { label: string; children: ReactNode; className?: string }) {
  return (
    <label className={`block min-w-0 ${className}`}>
      <span className="block text-[10px] uppercase text-text-dim font-semibold mb-1">{label}</span>
      {children}
    </label>
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

function Metric({ label, value, tone: t = "normal" }: { label: string; value: string; tone?: "normal" | "warn" }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] uppercase text-text-dim font-semibold mb-1">{label}</div>
      <div className={`text-sm truncate ${t === "warn" ? "text-warn" : "text-text"}`}>{value || "-"}</div>
    </div>
  );
}
