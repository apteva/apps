import { BarChart3 } from "lucide-react";
import { platformPresentation } from "./platformPresentation";
import { previewPerformanceSummary, socialPageLink, trendPath, useWidgetData, widgetPreferences, widgetURL, type HomeWidgetProps, type PerformanceSummary, type WidgetMetric } from "./homeWidgetData";

const number = (value: number | null | undefined) => value == null ? "—" : new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value);
const labels: Record<string, string> = { followers: "Followers", views: "Views", impressions: "Impressions", interactions: "Interactions" };

export default function SocialPerformanceWidget(props: HomeWidgetProps) {
  const prefs = widgetPreferences(props);
  const { data, error, retry } = useWidgetData<PerformanceSummary>(widgetURL(props, "/widgets/performance", { days: prefs.days }), props, () => previewPerformanceSummary(prefs.days));
  const keys = ["followers", prefs.visibilityMetric, "interactions"];
  const full = props.widgetSize === "full";
  const metric = data?.metrics[prefs.visibilityMetric];
  return <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
    <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
      <div><h2 className="flex items-center gap-2 text-sm font-bold text-text"><BarChart3 size={16} aria-hidden />Social · Performance</h2><p className="mt-1 text-[11px] text-text-dim">Last {prefs.days} complete days · UTC</p></div>
      <a href={socialPageLink(props, { tab: "metrics", range: `${prefs.days}d` })} className="shrink-0 text-[11px] text-text-muted hover:text-accent">Open analytics →</a>
    </header>
    {error && <div role="alert" className="flex items-center gap-2 px-4 py-2 text-xs text-error">{error}<button className="underline" onClick={retry}>Retry</button></div>}
    {!data ? <p className="min-h-28 px-4 py-6 text-xs text-text-dim">{error ? "" : "Loading performance…"}</p> : data.accounts.length === 0 ? (
      <div className="flex min-h-28 flex-1 flex-col justify-center px-4 py-5"><p className="text-xs font-semibold text-text">No connected accounts in this scope</p><a href={socialPageLink(props, { tab: "accounts" })} className="mt-2 text-[11px] text-accent">Connect an account →</a></div>
    ) : <div className="min-h-0 flex-1 overflow-auto p-4">
      <div className="grid grid-cols-3 gap-3">{keys.map(key => <Headline key={key} name={key} metric={data.metrics[key]} totalAccounts={data.accounts.length} days={data.days} showTrend={prefs.showTrends && !full} />)}</div>
      <p className="mt-3 text-[10px] text-text-dim">Followers are a current total across accounts, not unique people. Interactions = likes + comments + shares.</p>
      {keys.every(key => data.metrics[key]?.value == null) && <p className="mt-3 text-xs text-text-muted">Analytics aren't available yet. Social collects them in the background; open analytics to refresh supported accounts.</p>}
      {full && prefs.showTrends && <div className="mt-4 rounded border border-border p-3"><div className="mb-2 flex justify-between text-[11px] text-text-muted"><span>{labels[prefs.visibilityMetric]} per day</span><span>{data.start_date} – {data.end_date}</span></div><Trend metric={metric} label={labels[prefs.visibilityMetric]} tall /></div>}
      {full && <div className="mt-4 overflow-x-auto"><table className="w-full text-left text-[11px]"><caption className="sr-only">Social performance by account</caption><thead className="text-text-dim"><tr><th className="pb-2 font-medium">Account</th>{keys.map(key => <th key={key} className="pb-2 pl-3 text-right font-medium">{labels[key]}</th>)}</tr></thead><tbody>{data.accounts.map(account => { const platform = platformPresentation(account.platform); return <tr key={account.id} className="border-t border-border"><td className="py-2 text-text"><a href={socialPageLink(props, { tab: "metrics", account_ids: account.id, range: `${prefs.days}d` })} className="flex items-center gap-2 hover:text-accent"><span className="font-bold" style={{ color: platform.color }} title={platform.label}>{platform.mark}</span><span>{account.name || platform.label}</span></a></td>{keys.map(key => <td key={key} className="py-2 pl-3 text-right tabular-nums text-text-muted" title={account.metrics[key]?.value == null ? "Unavailable" : `${account.metrics[key]?.value?.toLocaleString()}${key === "followers" ? "" : ` · ${account.metrics[key]?.days || 0}/${data.days} complete days`}`}>{number(account.metrics[key]?.value)}</td>)}</tr>; })}</tbody></table></div>}
    </div>}
    <footer className="border-t border-border px-4 py-2 text-[10px] text-text-dim">{data?.updated_at ? `Oldest available refresh: ${new Date(data.updated_at).toLocaleString()}` : "No cached analytics yet"} · Supported accounts only</footer>
  </section>;
}

function Headline({ name, metric, totalAccounts, days, showTrend }: { name: string; metric?: WidgetMetric; totalAccounts: number; days: number; showTrend: boolean }) {
  const change = metric?.change_percent;
  return <div className="min-w-0"><p className="text-[11px] text-text-muted">{labels[name]}</p><p className="mt-1 text-xl font-bold tabular-nums text-text" title={metric?.value == null ? "Unavailable" : metric.value.toLocaleString()}>{number(metric?.value)}</p>
    <p className="mt-1 text-[10px] text-text-dim">{metric?.value == null ? "Unavailable" : `${metric.accounts}/${totalAccounts} accounts${name === "followers" ? "" : ` · ${metric.days || 0}/${days} complete days`}`}</p>
    {change != null && <p className={`mt-1 text-[10px] ${change >= 0 ? "text-success" : "text-error"}`} title="Compared with the previous equal period, using the same accounts and complete daily data">{change > 0 ? "+" : ""}{change.toFixed(1)}% vs prior period</p>}
    {showTrend && name !== "followers" && <div className="mt-2"><Trend metric={metric} label={labels[name]} /></div>}
  </div>;
}

function Trend({ metric, label, tall = false }: { metric?: WidgetMetric; label: string; tall?: boolean }) {
  const path = trendPath(metric?.trend || []);
  if (!path) return <p className="py-2 text-[10px] text-text-dim">More daily history needed</p>;
  return <svg viewBox="0 0 280 64" className={`w-full text-accent ${tall ? "h-28" : "h-12"}`} role="img" aria-label={`${label} daily trend; gaps indicate missing data`} preserveAspectRatio="none"><path d={path} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" /></svg>;
}
