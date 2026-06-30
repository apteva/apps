import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface MetricCurrency {
  currency: string;
  mrr_cents: number;
  recurring_amount_cents: number;
  subscriptions: number;
}

interface Metrics {
  project_id: string;
  source?: string;
  statuses: string[];
  currencies: MetricCurrency[];
  subscriptions: number;
  generated_at: string;
}

interface SubItem {
  title: string;
  quantity: number;
  unit_amount_cents: number;
  currency: string;
}

interface Subscription {
  id: number;
  customer_email?: string;
  customer_name?: string;
  status: string;
  source: string;
  source_ref?: string;
  currency: string;
  interval: string;
  interval_count: number;
  trial_end?: string;
  items?: SubItem[];
}

const API = "/api/apps/subscriptions";

function money(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: (currency || "USD").toUpperCase(),
      currencyDisplay: "narrowSymbol",
    }).format((cents || 0) / 100);
  } catch {
    return `${((cents || 0) / 100).toFixed(2)} ${currency}`;
  }
}

function intervalLabel(sub: Subscription): string {
  const interval = sub.interval || "month";
  const count = sub.interval_count || 1;
  return count > 1 ? `every ${count} ${interval}s` : `/${interval}`;
}

function statusTone(status: string): string {
  if (status === "active") return "border-green-500/30 text-green-500 bg-green-500/10";
  if (status === "trialing") return "border-blue-500/30 text-blue-500 bg-blue-500/10";
  if (status === "past_due" || status === "paused") return "border-yellow-500/30 text-yellow-500 bg-yellow-500/10";
  return "border-border text-text-muted bg-bg-input";
}

export default function SubscriptionsPanel({ projectId, installId }: NativePanelProps) {
  const [source, setSource] = useState("");
  const [includeTrialing, setIncludeTrialing] = useState(false);
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const qs = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const metricQuery: Record<string, string> = {};
      if (source) metricQuery.source = source;
      if (includeTrialing) metricQuery.include_trialing = "true";
      const [metricRes, activeRes, trialRes] = await Promise.all([
        fetch(`${API}/metrics?${qs(metricQuery)}`, { credentials: "include", cache: "no-store" }),
        fetch(`${API}/subscriptions?${qs({ status: "active", limit: "100" })}`, {
          credentials: "include",
          cache: "no-store",
        }),
        includeTrialing
          ? fetch(`${API}/subscriptions?${qs({ status: "trialing", limit: "100" })}`, {
              credentials: "include",
              cache: "no-store",
            })
          : Promise.resolve(null),
      ]);
      if (!metricRes.ok) throw new Error(await metricRes.text());
      if (!activeRes.ok) throw new Error(await activeRes.text());
      if (trialRes && !trialRes.ok) throw new Error(await trialRes.text());
      const metricBody = (await metricRes.json()) as { metrics: Metrics };
      const activeBody = (await activeRes.json()) as { subscriptions: Subscription[] };
      const trialBody = trialRes
        ? ((await trialRes.json()) as { subscriptions: Subscription[] })
        : { subscriptions: [] };
      setMetrics(metricBody.metrics);
      const rows = [...(activeBody.subscriptions || []), ...(trialBody.subscriptions || [])];
      setSubscriptions(source ? rows.filter((s) => s.source === source) : rows);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  }, [includeTrialing, qs, source]);

  useEffect(() => {
    load();
  }, [load]);

  const sources = useMemo(() => {
    const values = new Set<string>();
    for (const sub of subscriptions) {
      if (sub.source) values.add(sub.source);
    }
    if (source) values.add(source);
    return Array.from(values).sort();
  }, [source, subscriptions]);

  const countByStatus = useMemo(() => {
    const out: Record<string, number> = {};
    for (const sub of subscriptions) out[sub.status] = (out[sub.status] || 0) + 1;
    return out;
  }, [subscriptions]);

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="border-b border-border px-4 py-3 flex items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold">Subscriptions</h1>
          <div className="text-xs text-text-muted">Recurring revenue and lifecycle status</div>
        </div>
        <button
          type="button"
          onClick={load}
          disabled={loading}
          className="px-3 py-1.5 rounded border border-border text-sm hover:bg-bg-input disabled:opacity-50"
        >
          {loading ? "Loading..." : "Refresh"}
        </button>
      </header>

      <div className="border-b border-border px-4 py-2 flex flex-wrap items-center gap-2 text-sm">
        <select
          value={source}
          onChange={(e) => setSource(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1"
        >
          <option value="">All sources</option>
          <option value="hosting">Hosting</option>
          {sources.filter((s) => s !== "hosting").map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <label className="inline-flex items-center gap-2 text-text-muted">
          <input
            type="checkbox"
            checked={includeTrialing}
            onChange={(e) => setIncludeTrialing(e.target.checked)}
          />
          Include trials
        </label>
      </div>

      {error && (
        <div className="m-4 rounded border border-red-500/30 bg-red-500/10 p-2 text-sm text-red-500">
          {error}
        </div>
      )}

      <main className="min-h-0 flex-1 overflow-auto p-4 space-y-4">
        <section className="grid grid-cols-1 md:grid-cols-3 gap-3">
          {(metrics?.currencies || []).map((cur) => (
            <div key={cur.currency} className="border border-border rounded p-3">
              <div className="text-xs uppercase text-text-dim">MRR · {cur.currency}</div>
              <div className="mt-1 text-2xl font-semibold tabular-nums">
                {money(cur.mrr_cents, cur.currency)}
              </div>
              <div className="mt-1 text-xs text-text-muted">
                {cur.subscriptions} subscription{cur.subscriptions === 1 ? "" : "s"}
              </div>
            </div>
          ))}
          {(!metrics || metrics.currencies.length === 0) && (
            <div className="border border-border rounded p-3">
              <div className="text-xs uppercase text-text-dim">MRR</div>
              <div className="mt-1 text-2xl font-semibold tabular-nums">$0.00</div>
              <div className="mt-1 text-xs text-text-muted">No active recurring subscriptions</div>
            </div>
          )}
          <div className="border border-border rounded p-3">
            <div className="text-xs uppercase text-text-dim">Statuses</div>
            <div className="mt-2 flex flex-wrap gap-1">
              {Object.entries(countByStatus).map(([status, count]) => (
                <span key={status} className={`inline-flex rounded border px-2 py-0.5 text-xs ${statusTone(status)}`}>
                  {status.replaceAll("_", " ")} · {count}
                </span>
              ))}
              {Object.keys(countByStatus).length === 0 && (
                <span className="text-sm text-text-muted">No subscriptions</span>
              )}
            </div>
          </div>
        </section>

        <section className="border border-border rounded overflow-hidden">
          <div className="px-3 py-2 border-b border-border text-sm font-medium">Subscriptions</div>
          <div className="overflow-auto">
            <table className="w-full text-sm">
              <thead className="text-xs text-text-dim border-b border-border">
                <tr>
                  <th className="text-left font-normal px-3 py-2">Customer</th>
                  <th className="text-left font-normal px-3 py-2">Source</th>
                  <th className="text-left font-normal px-3 py-2">Status</th>
                  <th className="text-right font-normal px-3 py-2">Amount</th>
                </tr>
              </thead>
              <tbody>
                {subscriptions.map((sub) => {
                  const item = sub.items?.[0];
                  return (
                    <tr key={sub.id} className="border-b border-border last:border-b-0">
                      <td className="px-3 py-2">
                        <div className="font-medium truncate max-w-[260px]">
                          {sub.customer_name || sub.customer_email || `Subscription ${sub.id}`}
                        </div>
                        <div className="text-xs text-text-muted truncate max-w-[260px]">
                          {item?.title || sub.customer_email || ""}
                        </div>
                      </td>
                      <td className="px-3 py-2 text-text-muted">
                        {sub.source || "manual"}
                        {sub.source_ref ? <span className="text-text-dim"> / {sub.source_ref}</span> : null}
                      </td>
                      <td className="px-3 py-2">
                        <span className={`inline-flex rounded border px-2 py-0.5 text-xs ${statusTone(sub.status)}`}>
                          {sub.status.replaceAll("_", " ")}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">
                        {money(item?.unit_amount_cents || 0, item?.currency || sub.currency)}
                        <span className="text-xs text-text-muted ml-1">{intervalLabel(sub)}</span>
                      </td>
                    </tr>
                  );
                })}
                {subscriptions.length === 0 && (
                  <tr>
                    <td className="px-3 py-6 text-sm text-text-muted" colSpan={4}>
                      No subscriptions match this view.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      </main>
    </div>
  );
}
