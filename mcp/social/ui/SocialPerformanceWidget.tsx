import { useState } from "react";
import { BarChart3 } from "lucide-react";
import { platformPresentation } from "./platformPresentation";
import {
  previewPerformanceSummary,
  socialPageLink,
  useWidgetData,
  widgetPreferences,
  widgetURL,
  trendPath,
  type HomeWidgetProps,
  type PerformanceSummary,
  type WidgetMetric,
} from "./homeWidgetData";
import {
  completePerformanceDates,
  performanceDateError,
  performanceDefaults,
  performanceURL,
  sortPerformanceAccounts,
  type PerformanceFilters,
  type PerformanceOptions,
  type PerformanceSort,
} from "./performanceFilters";

const number = (value: number | null | undefined) =>
  value == null
    ? "—"
    : new Intl.NumberFormat(undefined, {
        notation: "compact",
        maximumFractionDigits: 1,
      }).format(value);
const labels: Record<string, string> = {
  followers: "Followers",
  views: "Views",
  impressions: "Impressions",
  interactions: "Interactions",
};
const control =
  "min-h-8 max-w-full rounded border border-border bg-bg-input px-2 py-1 text-xs text-text";
const emptyOptions: PerformanceOptions = {
  profiles: [],
  accounts: [],
  platforms: [],
};

export default function SocialPerformanceWidget(props: HomeWidgetProps) {
  // Reset filters and cached responses when switching installation, project or
  // configured defaults.
  return (
    <PerformanceView
      key={`${props.installId}:${props.projectId}:${JSON.stringify(props.widgetSettings || {})}`}
      {...props}
    />
  );
}

function PerformanceView(props: HomeWidgetProps) {
  const prefs = widgetPreferences(props);
  const [filters, setFilters] = useState<PerformanceFilters>(() =>
    performanceDefaults(props),
  );
  const [sort, setSort] = useState<{
    key: PerformanceSort;
    descending: boolean;
  }>({ key: "name", descending: false });
  const invalid = performanceDateError(filters);
  const days =
    filters.period === "custom"
      ? Math.max(
          1,
          Math.round(
            (Date.parse(filters.endDate) - Date.parse(filters.startDate)) /
              86400000,
          ) + 1,
        )
      : Number(filters.period);
  const result = useWidgetData<PerformanceSummary>(
    performanceURL(props, filters),
    props,
    () => previewPerformanceSummary(days),
    !invalid,
  );
  const optionsResult = useWidgetData<PerformanceOptions>(
    widgetURL(
      { ...props, widgetSettings: undefined },
      "/widgets/performance/options",
      {},
    ),
    props,
    () => emptyOptions,
  );
  const options = optionsResult.data || emptyOptions;
  const data = result.data;
  const rows = sortPerformanceAccounts(
    data?.accounts || [],
    sort.key,
    sort.descending,
    prefs.visibilityMetric,
  );
  const accountChoices = (options.accounts || []).filter(
    (account) =>
      (!filters.profileID || account.profile_id === filters.profileID) &&
      (filters.platform === "all" || account.platform === filters.platform),
  );
  const selectedIDs = filters.accountIDs.split(",").filter(Boolean);
  const navigation = {
    ...props,
    widgetSettings: {
      ...props.widgetSettings,
      profile_id: filters.profileID,
      account_ids: filters.accountIDs,
    },
  };
  const range =
    data?.start_date && data?.end_date
      ? `${data.start_date} – ${data.end_date}`
      : filters.period === "custom"
        ? `${filters.startDate} – ${filters.endDate}`
        : `Last ${days} complete days`;
  const keys = ["followers", prefs.visibilityMetric, "interactions"];
  const full = props.widgetSize === "full";
  const changeFilter = (patch: Partial<PerformanceFilters>) =>
    setFilters((previous) => ({ ...previous, ...patch }));
  const toggleAccount = (id: number) =>
    changeFilter({
      accountIDs: (selectedIDs.includes(String(id))
        ? selectedIDs.filter((value) => value !== String(id))
        : [...selectedIDs, String(id)]
      ).join(","),
    });
  const heading = (key: PerformanceSort, label: string) => (
    <th
      className="px-2 py-2 text-left font-medium whitespace-nowrap"
      aria-sort={
        sort.key === key
          ? sort.descending
            ? "descending"
            : "ascending"
          : "none"
      }
    >
      <button
        type="button"
        onClick={() =>
          setSort((previous) => ({
            key,
            descending:
              previous.key === key
                ? !previous.descending
                : !["name", "profile_name", "platform"].includes(key),
          }))
        }
      >
        {label}
        {sort.key === key ? (sort.descending ? " ↓" : " ↑") : ""}
      </button>
    </th>
  );
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-bold text-text">
            <BarChart3 size={16} aria-hidden />
            Social · Performance
          </h2>
          <p className="mt-1 text-[11px] text-text-dim">{range} · UTC</p>
        </div>
        <a
          href={socialPageLink(navigation, {
            tab: "metrics",
            ...(filters.period !== "custom" ? { range: `${days}d` } : {}),
          })}
          className="shrink-0 text-[11px] text-text-muted hover:text-accent"
        >
          Open analytics →
        </a>
      </header>
      <div
        className="flex flex-wrap items-end gap-2 border-b border-border px-4 py-2"
        aria-label="Performance filters"
      >
        <label className="min-w-0 text-[11px] text-text-dim">
          Profile
          <select
            aria-label="Profile"
            className={`block ${control}`}
            value={filters.profileID}
            onChange={(event) =>
              changeFilter({
                profileID: Number(event.target.value),
                accountIDs: "",
              })
            }
          >
            <option value={0}>All profiles</option>
            {(options.profiles || []).map((profile) => (
              <option key={profile.id} value={profile.id}>
                {profile.name}
              </option>
            ))}
          </select>
        </label>
        <label className="text-[11px] text-text-dim">
          Platform
          <select
            aria-label="Platform"
            className={`block ${control}`}
            value={filters.platform}
            onChange={(event) =>
              changeFilter({ platform: event.target.value, accountIDs: "" })
            }
          >
            <option value="all">All platforms</option>
            {(options.platforms || []).map((platform) => (
              <option key={platform} value={platform}>
                {platformPresentation(platform).label}
              </option>
            ))}
          </select>
        </label>
        <label className="text-[11px] text-text-dim">
          Date range
          <select
            aria-label="Date range"
            className={`block ${control}`}
            value={filters.period}
            onChange={(event) => changeFilter({ period: event.target.value })}
          >
            <option value="7">Last 7 days</option>
            <option value="28">Last 28 days</option>
            <option value="90">Last 90 days</option>
            <option value="custom">Custom dates</option>
          </select>
        </label>
        {filters.period === "custom" && (
          <>
            <label className="text-[11px] text-text-dim">
              From
              <input
                aria-label="Start date"
                type="date"
                max={completePerformanceDates().endDate}
                className={`block ${control}`}
                value={filters.startDate}
                onChange={(event) =>
                  changeFilter({ startDate: event.target.value })
                }
              />
            </label>
            <label className="text-[11px] text-text-dim">
              To
              <input
                aria-label="End date"
                type="date"
                max={completePerformanceDates().endDate}
                className={`block ${control}`}
                value={filters.endDate}
                onChange={(event) =>
                  changeFilter({ endDate: event.target.value })
                }
              />
            </label>
          </>
        )}
        <details className="w-full text-xs">
          <summary className="cursor-pointer text-text-muted">
            Accounts ·{" "}
            {selectedIDs.length
              ? `${selectedIDs.length} selected`
              : "All matching accounts"}
          </summary>
          <div className="mt-2 max-h-32 overflow-auto rounded border border-border p-2">
            <button
              type="button"
              className="mb-2 text-accent"
              onClick={() => changeFilter({ accountIDs: "" })}
            >
              Use all matching accounts
            </button>
            {accountChoices.map((account) => (
              <label key={account.id} className="flex items-center gap-2 py-1">
                <input
                  type="checkbox"
                  aria-label={`Include ${account.name}`}
                  checked={selectedIDs.includes(String(account.id))}
                  onChange={() => toggleAccount(account.id)}
                />
                {account.name} · {platformPresentation(account.platform).label}
              </label>
            ))}
            {accountChoices.length === 0 && (
              <p>No accounts match these filters.</p>
            )}
          </div>
        </details>
      </div>
      {(invalid || result.error || optionsResult.error) && (
        <div
          role="alert"
          className="flex items-center gap-2 px-4 py-2 text-xs text-error"
        >
          {invalid || result.error || optionsResult.error}
          {!invalid && (
            <button
              className="underline"
              onClick={() => {
                result.retry();
                optionsResult.retry();
              }}
            >
              Retry
            </button>
          )}
        </div>
      )}
      {!data ? (
        <p className="min-h-28 px-4 py-6 text-xs text-text-dim">
          {invalid || result.error ? "" : "Loading performance…"}
        </p>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <div className="grid grid-cols-3 gap-3">
            {keys.map((key) => (
              <Headline
                key={key}
                name={key}
                metric={data.metrics[key]}
                totalAccounts={
                  data.accounts.filter(
                    (account) => !account.aggregate_exclusion_reason,
                  ).length
                }
                days={data.days}
                showTrend={prefs.showTrends && !full}
              />
            ))}
          </div>
          <p className="mt-3 text-[10px] text-text-dim">
            Followers are current totals, not unique people or historical
            totals. Views, impressions and interactions use the selected dates.
            Interactions = likes + comments + shares.
          </p>
          {full && prefs.showTrends && (
            <div className="mt-3 rounded border border-border p-2">
              <Trend
                metric={data.metrics[prefs.visibilityMetric]}
                label={labels[prefs.visibilityMetric]}
              />
            </div>
          )}
          <p className="mt-3 text-[11px] text-text-muted">
            {rows.length} {rows.length === 1 ? "account" : "accounts"} · Change
            compares {prefs.visibilityMetric} with the previous equal period
          </p>
          {rows.length === 0 ? (
            <p className="py-4 text-xs text-text-dim">
              No connected accounts match these filters.
            </p>
          ) : (
            <div className="mt-2 overflow-x-auto">
              <table className="w-full text-left text-[11px]">
                <caption className="sr-only">
                  Social performance by account
                </caption>
                <thead className="text-text-dim">
                  <tr>
                    {heading("name", "Account")}
                    {heading("profile_name", "Profile")}
                    {heading("platform", "Platform")}
                    {heading("followers", "Followers (current)")}
                    {heading("views", "Views")}
                    {heading("impressions", "Impressions")}
                    {heading("interactions", "Interactions")}
                    {heading("change", "Change %")}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((account) => (
                    <tr key={account.id} className="border-t border-border">
                      <td className="px-2 py-2 text-text whitespace-nowrap">
                        <a
                          className="hover:text-accent"
                          href={socialPageLink(navigation, {
                            tab: "metrics",
                            account_ids: account.id,
                            ...(filters.period !== "custom"
                              ? { range: `${days}d` }
                              : {}),
                          })}
                        >
                          {account.name}
                        </a>
                        {account.status && account.status !== "active" && (
                          <span className="block text-[10px] text-text-dim">
                            {account.status.replace(/_/g, " ")} · cached metrics
                          </span>
                        )}
                        {account.aggregate_exclusion_reason && (
                          <span className="block max-w-56 whitespace-normal text-text-dim">
                            {account.aggregate_exclusion_reason}
                          </span>
                        )}
                      </td>
                      <td className="px-2 py-2 whitespace-nowrap">
                        {account.profile_name || "Unassigned"}
                      </td>
                      <td className="px-2 py-2 whitespace-nowrap">
                        {platformPresentation(account.platform).label}
                      </td>
                      {[
                        "followers",
                        "views",
                        "impressions",
                        "interactions",
                      ].map((key) => {
                        const metric = account.metrics[key];
                        return (
                          <td
                            key={key}
                            className="px-2 py-2 tabular-nums"
                            title={
                              metric?.value == null
                                ? "Unavailable"
                                : key === "followers"
                                  ? "Current follower snapshot"
                                  : `${metric.days || 0}/${data.days} days of data`
                            }
                          >
                            {number(metric?.value)}
                            {key !== "followers" &&
                              metric?.value != null &&
                              (metric.days || 0) < data.days && (
                                <span className="block text-[10px] text-text-dim">
                                  Partial · {metric.days || 0}/{data.days} days
                                </span>
                              )}
                          </td>
                        );
                      })}
                      <td className="px-2 py-2 tabular-nums">
                        {account.metrics[prefs.visibilityMetric]
                          ?.change_percent == null
                          ? "—"
                          : `${account.metrics[prefs.visibilityMetric].change_percent! > 0 ? "+" : ""}${account.metrics[prefs.visibilityMetric].change_percent!.toFixed(1)}%`}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
      <footer className="border-t border-border px-4 py-2 text-[10px] text-text-dim">
        {data?.updated_at
          ? `Oldest available refresh: ${new Date(data.updated_at).toLocaleString()}`
          : "No cached analytics yet"}{" "}
        · Missing metrics shown as —
      </footer>
    </section>
  );
}
function Headline({
  name,
  metric,
  totalAccounts,
  days,
  showTrend,
}: {
  name: string;
  metric?: WidgetMetric;
  totalAccounts: number;
  days: number;
  showTrend: boolean;
}) {
  const change = metric?.change_percent;
  return (
    <div className="min-w-0">
      <p className="text-[11px] text-text-muted">{labels[name]}</p>
      <p
        className="mt-1 text-xl font-bold tabular-nums text-text"
        title={
          metric?.value == null ? "Unavailable" : metric.value.toLocaleString()
        }
      >
        {number(metric?.value)}
      </p>
      <p className="mt-1 text-[10px] text-text-dim">
        {metric?.value == null
          ? "Unavailable"
          : `${metric.accounts}/${totalAccounts} accounts${name === "followers" ? "" : ` · ${metric.days || 0}/${days} complete days`}`}
      </p>
      {change != null && (
        <p
          className={`mt-1 text-[10px] ${change >= 0 ? "text-success" : "text-error"}`}
          title="Compared with the previous equal period, using the same accounts and complete daily data"
        >
          {change > 0 ? "+" : ""}
          {change.toFixed(1)}% vs prior period
        </p>
      )}
      {showTrend && name !== "followers" && (
        <div className="mt-2">
          <Trend metric={metric} label={labels[name]} />
        </div>
      )}
    </div>
  );
}

function Trend({
  metric,
  label,
  tall = false,
}: {
  metric?: WidgetMetric;
  label: string;
  tall?: boolean;
}) {
  const path = trendPath(metric?.trend || []);
  if (!path)
    return (
      <p className="py-2 text-[10px] text-text-dim">
        More daily history needed
      </p>
    );
  return (
    <svg
      viewBox="0 0 280 64"
      className={`w-full text-accent ${tall ? "h-28" : "h-12"}`}
      role="img"
      aria-label={`${label} daily trend; gaps indicate missing data`}
      preserveAspectRatio="none"
    >
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        vectorEffect="non-scaling-stroke"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}
