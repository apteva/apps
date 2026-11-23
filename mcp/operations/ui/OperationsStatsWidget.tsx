import type { ReactNode } from "react";
import { NoProject, WidgetFrame } from "./WidgetFrame";
import { brandSetting, callTool, human, minutes, money, pageLink, projectCurrency, setting, useWidgetData, type WidgetProps } from "./widget";

interface Metrics {
  created: number; closed: number; cancelled: number; waiting_on_owner: number; open_by_phase?: Record<string, number>;
  quote_acceptance_pct?: number; invoiced_cents: number; margin_pct?: number; median_minutes_to_first_slot?: number;
  gmv_cents?: number; revenue_cents?: number;
  commissions?: { accrued_cents: number; invoiced_cents: number; paid_cents: number; total_cents: number };
  utilisation?: { resource_id: number; name: string; utilisation_pct: number }[];
  by_brand?: { brand: string; name: string; created: number; closed: number; open: number; invoiced_cents: number; gmv_cents?: number; commission_cents?: number; revenue_cents?: number }[];
  by_contractor?: { resource_id: number; name: string; mode: string; jobs_invoiced: number; gmv_cents: number; commission_accrued_cents: number; commission_invoiced_cents: number; commission_paid_cents: number }[];
}

const PERIODS = [7, 30, 90] as const;

function Tile({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: ReactNode; tone?: string }) {
  return (
    <div className="min-w-0 rounded border border-border px-3 py-2">
      <div className="truncate text-xs text-text-muted">{label}</div>
      <div className={`mt-0.5 truncate text-lg font-semibold ${tone ?? ""}`}>{value}</div>
      {hint && <div className="truncate text-xs text-text-dim">{hint}</div>}
    </div>
  );
}

export default function OperationsStatsWidget(props: WidgetProps) {
  const brand = brandSetting(props.widgetSettings);
  const period = setting(props.widgetSettings, "period_days", PERIODS, 30);
  const full = props.widgetSize === "full";
  const state = useWidgetData(props, `${props.projectId}|${props.installId}|${brand}|${period}`, async (signal) => {
    const to = new Date();
    const from = new Date(to.getTime() - period * 86400000);
    // to is a little ahead so work updated this second is counted.
    const args = { from: from.toISOString(), to: new Date(to.getTime() + 60000).toISOString(), ...(brand ? { brand } : {}) };
    const [m, currency] = await Promise.all([callTool<Metrics>(props, "metrics", args, signal), projectCurrency(props, brand, signal)]);
    return { m, currency };
  });
  if (!props.projectId) return <NoProject title="Operations stats" />;

  const m = state.data?.m;
  const cur = state.data?.currency ?? "EUR";
  const commission = m?.commissions?.total_cents ?? 0;
  const hasCommission = commission !== 0 || (m?.by_contractor ?? []).some((c) => c.mode === "commission");
  const open = m ? Object.entries(m.open_by_phase ?? {}).filter(([p]) => p !== "closed" && p !== "cancelled").reduce((t, [, n]) => t + n, 0) : 0;

  return (
    <WidgetFrame title="Operations stats" link={pageLink(props)} error={state.error} loading={!m && !state.error}
      sub={`Last ${period} days${brand ? ` · ${brand}` : ""}`}>
      {m && (
        <div className="space-y-4 p-4">
          <div className="grid gap-2" style={{ gridTemplateColumns: `repeat(auto-fill, minmax(${full ? 150 : 128}px, 1fr))` }}>
            <Tile label="Revenue" value={money(m.revenue_cents ?? m.invoiced_cents, cur)}
              hint={hasCommission ? `${money(m.invoiced_cents, cur)} invoiced · ${money(commission, cur)} commission` : m.margin_pct != null ? `${m.margin_pct}% margin` : "invoiced"} />
            {hasCommission && <Tile label="Billed to clients" value={money(m.gmv_cents ?? 0, cur)} hint="incl. contractors' invoices" />}
            <Tile label="Jobs" value={`${m.created} / ${m.closed}`} hint={`new / closed · ${open} open`} />
            <Tile label="Waiting on you" value={m.waiting_on_owner} tone={m.waiting_on_owner > 0 ? "text-red" : undefined} hint="owner decisions" />
            <Tile label="Quotes accepted" value={m.quote_acceptance_pct != null ? `${m.quote_acceptance_pct}%` : "—"} hint="of quotes sent" />
            <Tile label="Time to first slot" value={minutes(m.median_minutes_to_first_slot)} hint="median" />
            {!hasCommission && full && <Tile label="Margin" value={m.margin_pct != null ? `${m.margin_pct}%` : "—"} hint="on invoiced work" />}
            {hasCommission && <Tile label="Commission unpaid" value={money((m.commissions?.accrued_cents ?? 0) + (m.commissions?.invoiced_cents ?? 0), cur)}
              hint={`${money(m.commissions?.paid_cents ?? 0, cur)} paid`} tone={(m.commissions?.invoiced_cents ?? 0) > 0 ? "text-yellow" : undefined} />}
          </div>

          {full && (m.by_brand?.length ?? 0) > 1 && (
            <table className="w-full text-xs">
              <thead className="text-text-muted"><tr>
                <th className="py-1 text-left font-normal">Brand</th><th className="py-1 text-right font-normal">Open</th>
                <th className="py-1 text-right font-normal">New</th><th className="py-1 text-right font-normal">Closed</th>
                <th className="py-1 text-right font-normal">Revenue</th>
              </tr></thead>
              <tbody>{m.by_brand!.map((b) => (
                <tr key={b.brand || "-"} className="border-t border-border">
                  <td className="py-1.5">{b.name}</td><td className="py-1.5 text-right">{b.open}</td><td className="py-1.5 text-right">{b.created}</td>
                  <td className="py-1.5 text-right">{b.closed}</td><td className="py-1.5 text-right">{money(b.revenue_cents ?? b.invoiced_cents, cur)}</td>
                </tr>
              ))}</tbody>
            </table>
          )}

          {full && (m.by_contractor?.length ?? 0) > 0 && (
            <table className="w-full text-xs">
              <thead className="text-text-muted"><tr>
                <th className="py-1 text-left font-normal">Contractor</th><th className="py-1 text-left font-normal">Settlement</th>
                <th className="py-1 text-right font-normal">Jobs</th><th className="py-1 text-right font-normal">Billed</th>
                <th className="py-1 text-right font-normal">Commission</th>
              </tr></thead>
              <tbody>{m.by_contractor!.map((c) => (
                <tr key={`${c.resource_id}-${c.mode}`} className="border-t border-border">
                  <td className="py-1.5">{c.name}</td><td className="py-1.5 text-text-muted">{human(c.mode)}</td>
                  <td className="py-1.5 text-right">{c.jobs_invoiced}</td><td className="py-1.5 text-right">{money(c.gmv_cents, cur)}</td>
                  <td className="py-1.5 text-right">{c.mode === "commission" ? money(c.commission_accrued_cents + c.commission_invoiced_cents + c.commission_paid_cents, cur) : "—"}</td>
                </tr>
              ))}</tbody>
            </table>
          )}

          {(m.utilisation?.length ?? 0) > 0 && (
            <div className="space-y-1.5">
              <div className="text-xs uppercase tracking-wide text-text-dim">Utilisation</div>
              {m.utilisation!.slice(0, full ? 12 : 4).map((u) => (
                <div key={u.resource_id} className="flex items-center gap-2 text-xs">
                  <span className="w-28 truncate">{u.name}</span>
                  <div className="h-1.5 flex-1 overflow-hidden rounded bg-bg-input">
                    <div style={{ width: `${Math.min(100, u.utilisation_pct)}%`, height: "100%", background: "var(--color-accent)" }} />
                  </div>
                  <span className="w-9 text-right text-text-muted">{Math.round(u.utilisation_pct)}%</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </WidgetFrame>
  );
}
