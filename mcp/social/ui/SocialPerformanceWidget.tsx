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
  performanceDisplay,
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
  const display = performanceDisplay(props);
  const [accountsOpen, setAccountsOpen] = useState(false);
  const [filters, setFilters] = useState<PerformanceFilters>(() =>
    performanceDefaults(props),
  );
  const [sort, setSort] = useState<{
    key: PerformanceSort;
    descending: boolean;
  }>({ key: display.ranking, descending: display.ranking !== "name" });
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
    display.showFilters,
  );
  const options = optionsResult.data || emptyOptions;
  const data = result.data;
  const sortedRows = sortPerformanceAccounts(
    data?.accounts || [],
    sort.key,
    sort.descending,
  );
  const rows = sortedRows.slice(0, display.maxAccounts);
  const sortedMetric = display.columns.find((metric) => metric === sort.key);
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
      key={key}
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
      <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
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
      {display.showFilters && (
        <div
          className="flex shrink-0 flex-nowrap items-end gap-2 overflow-x-auto border-b border-border px-4 py-2"
          aria-label="Performance filters"
        >
          <label className="shrink-0 text-[11px] text-text-dim">
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
          <label className="shrink-0 text-[11px] text-text-dim">
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
          <label className="shrink-0 text-[11px] text-text-dim">
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
              <label className="shrink-0 text-[11px] text-text-dim">
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
              <label className="shrink-0 text-[11px] text-text-dim">
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
          <div className="shrink-0 text-[11px] text-text-dim">
            <p>Accounts</p>
            <button
              type="button"
              className={control}
              aria-label="Select accounts"
              aria-expanded={accountsOpen}
              onClick={() => setAccountsOpen((open) => !open)}
            >
              {selectedIDs.length
                ? `${selectedIDs.length} selected`
                : "All matching accounts"}{" "}
              ▾
            </button>
          </div>
        </div>
      )}
      {display.showFilters && accountsOpen && (
        <div
          role="group"
          aria-label="Account selection"
          className="max-h-40 shrink-0 overflow-auto border-b border-border px-4 py-2 text-xs"
        >
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
      )}
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
          {display.showSummary && display.columns.length > 0 && (
            <div
              aria-label="Performance totals"
              className="grid gap-3"
              style={{
                gridTemplateColumns: `repeat(${Math.min(display.columns.length, full ? 4 : 2)}, minmax(0, 1fr))`,
              }}
            >
              {display.columns.map((key) => (
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
                />
              ))}
            </div>
          )}
          {prefs.showTrends && (
            <div
              aria-label="Performance graph"
              className="mt-3 rounded border border-border p-2"
            >
              <Trend
                metric={data.metrics[prefs.visibilityMetric]}
                label={labels[prefs.visibilityMetric]}
              />
            </div>
          )}
          {display.showAccounts && (
            <>
              <p className="mt-3 text-[11px] text-text-muted">
                {rows.length < sortedRows.length
                  ? `${sort.descending && sortedMetric ? "Top" : "Showing"} ${rows.length} of ${sortedRows.length} accounts`
                  : `${rows.length} ${rows.length === 1 ? "account" : "accounts"}`}
                {sortedMetric &&
                  ` · ${labels[sortedMetric]} ${sort.descending ? "↓" : "↑"}${sortedMetric === "followers" ? ` · Growth during ${data.start_date} – ${data.end_date}` : ` · % vs previous ${data.days} days`}`}
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
                        {display.showProfile &&
                          heading("profile_name", "Profile")}
                        {display.showPlatform &&
                          heading("platform", "Platform")}
                        {display.columns.map((metric) =>
                          heading(
                            metric,
                            metric === "followers"
                              ? "Followers (current)"
                              : labels[metric],
                          ),
                        )}
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
                                {account.status.replace(/_/g, " ")} · cached
                                metrics
                              </span>
                            )}
                            {account.aggregate_exclusion_reason && (
                              <span className="block max-w-56 whitespace-normal text-text-dim">
                                {account.aggregate_exclusion_reason}
                              </span>
                            )}
                          </td>
                          {display.showProfile && (
                            <td className="px-2 py-2 whitespace-nowrap">
                              {account.profile_name || "Unassigned"}
                            </td>
                          )}
                          {display.showPlatform && (
                            <td className="px-2 py-2 whitespace-nowrap">
                              {platformPresentation(account.platform).label}
                            </td>
                          )}
                          {display.columns.map((key) => {
                            const metric = account.metrics[key];
                            return (
                              <td
                                key={key}
                                className="px-2 py-2 tabular-nums"
                                title={
                                  metric?.value == null
                                    ? "Unavailable"
                                    : key === "followers"
                                      ? `Latest follower observation${metric.observed_at ? `: ${metric.observed_at}` : ""}`
                                      : `${metric.days || 0}/${data.days} days of data`
                                }
                              >
                                <span className="inline-flex items-baseline gap-1.5 whitespace-nowrap">
                                  {number(metric?.value)}
                                  {sort.key === key &&
                                    metric?.value != null && (
                                      <MetricChange
                                        change={metric.change_percent}
                                        label={labels[key]}
                                        days={data.days}
                                        growth={metric.growth}
                                      />
                                    )}
                                  {sort.key === "followers" &&
                                    key === "followers" &&
                                    metric?.value != null &&
                                    metric?.change_percent == null && (
                                      <span
                                        className="text-[10px] text-text-dim"
                                        aria-label="Followers growth unavailable"
                                        title={followerGrowthUnavailable(
                                          metric?.growth?.reason,
                                        )}
                                      >
                                        —
                                      </span>
                                    )}
                                </span>
                                {key !== "followers" &&
                                  metric?.value != null &&
                                  (metric.days || 0) < data.days && (
                                    <span className="block text-[10px] text-text-dim">
                                      Partial · {metric.days || 0}/{data.days}{" "}
                                      days
                                    </span>
                                  )}
                              </td>
                            );
                          })}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </div>
      )}
      <footer className="shrink-0 border-t border-border px-4 py-2 text-[10px] text-text-dim">
        {data?.updated_at
          ? `Oldest available refresh: ${new Date(data.updated_at).toLocaleString()}`
          : "No cached analytics yet"}{" "}
        · Followers are current; other metrics use selected dates · Missing
        metrics —
      </footer>
    </section>
  );
}
function Headline({
  name,
  metric,
  totalAccounts,
  days,
}: {
  name: string;
  metric?: WidgetMetric;
  totalAccounts: number;
  days: number;
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
      {change != null && Number.isFinite(change) && (
        <p
          className={`mt-1 text-[10px] ${change > 0 ? "text-success" : change < 0 ? "text-error" : "text-text-dim"}`}
          title={
            name === "followers"
              ? "Follower balance change during the selected dates, using matched boundary observations"
              : "Compared with the previous equal period, using the same accounts and complete daily data"
          }
        >
          {change > 0 ? "+" : ""}
          {change.toFixed(1)}%{" "}
          {name === "followers" ? "during selected dates" : "vs prior period"}
        </p>
      )}
      {name === "followers" && metric?.value != null && change == null && (
        <p
          className="mt-1 text-[10px] text-text-dim"
          title={followerGrowthUnavailable(metric.growth?.reason)}
        >
          Growth unavailable
        </p>
      )}
    </div>
  );
}

function followerGrowthUnavailable(reason?: string) {
  const reasons: Record<string, string> = {
    missing_baseline:
      "No follower observation at the start of the selected dates.",
    missing_end_count:
      "No follower observation at the end of the selected dates.",
    stale_baseline:
      "The starting follower observation is more than 24 hours old.",
    stale_end_count:
      "The ending follower observation is more than 24 hours old.",
    no_distinct_observations:
      "Two distinct follower observations are required.",
    zero_baseline: "Percentage growth is undefined from zero followers.",
    incomplete_account_coverage: "Some accounts lack valid follower history.",
  };
  return (
    reasons[reason || ""] ||
    "Not enough follower history for the selected dates."
  );
}

function MetricChange({
  change,
  label,
  days,
  growth,
}: {
  change?: number;
  label: string;
  days: number;
  growth?: WidgetMetric["growth"];
}) {
  if (change == null || !Number.isFinite(change)) return null;
  const text = `${change > 0 ? "+" : ""}${change.toFixed(1)}%`;
  return (
    <span
      className={`text-[10px] font-normal ${change > 0 ? "text-success" : change < 0 ? "text-error" : "text-text-dim"}`}
      aria-label={`${label} change: ${text}`}
      title={
        growth
          ? `Follower growth during ${growth.start_date} – ${growth.end_date}; observed ${growth.baseline_observed_at || "at the start"} to ${growth.end_observed_at || "the end"}`
          : `Compared with the previous ${days} days, using complete data`
      }
    >
      {text}
    </span>
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
