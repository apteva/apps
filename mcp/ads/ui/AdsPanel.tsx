import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

const API = "/api/apps/ads";
const PANEL_PROJECT_ID = new URL(import.meta.url).searchParams.get("project_id") || "";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface AdAccount {
  id: number;
  platform: string;
  connection_id: number;
  native_account_id: string;
  display_name: string;
  currency: string;
  timezone: string;
  status: string;
  created_at: string;
}

interface PendingAccountPage {
  id: string;
  name: string;
  currency: string;
  timezone: string;
  test_account?: boolean;
}

interface ConnectionOption {
  id: number;
  name: string;
}

interface Campaign {
  id: string;
  name: string;
  objective?: string;
  status?: string;
  effective_status?: string;
  daily_budget?: string;
}

interface AdSet {
  id: string;
  name: string;
  campaign_id?: string;
  status?: string;
  effective_status?: string;
  daily_budget?: string;
  lifetime_budget?: string;
  optimization_goal?: string;
  billing_event?: string;
  targeting?: Record<string, any>;
  promoted_object?: Record<string, any>;
  start_time?: string;
  end_time?: string;
}

interface AdCreative {
  id?: string;
  name?: string;
  thumbnail_url?: string;
  image_url?: string;
  object_story_spec?: Record<string, any>;
}

interface Ad {
  id: string;
  name: string;
  adset_id?: string;
  campaign_id?: string;
  status?: string;
  effective_status?: string;
  creative?: AdCreative;
  tracking_specs?: unknown[];
}

interface PerformancePoint {
  platform: string;
  ad_account_id: number;
  level: "account" | "campaign" | "ad_group" | "ad";
  entity_id: string;
  entity_name: string;
  campaign_id: string;
  campaign_name: string;
  date: string;
  currency: string;
  timezone: string;
  spend_micros: number;
  impressions: number;
  reach: number;
  clicks: number;
  link_clicks: number;
  conversions: number;
  conversion_value_micros: number;
  video_views: number;
  ctr: number;
  cpc_micros: number;
  cpm_micros: number;
  cpa_micros: number;
  roas: number;
  fetched_at: string;
}

interface PerformanceSummary {
  spend_micros: number;
  impressions: number;
  reach: number;
  clicks: number;
  link_clicks: number;
  conversions: number;
  conversion_value_micros: number;
  video_views: number;
  ctr: number;
  cpc_micros: number;
  cpm_micros: number;
  cpa_micros: number;
  roas: number;
  currency: string;
  timezone: string;
}

interface PerformanceResponse {
  data: PerformancePoint[];
  summary: PerformanceSummary;
  source: "live" | "cache";
  freshness: { fetched_at?: string; row_count: number };
}

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}

interface AdsEventData {
  ad_account_id?: number;
  level?: string;
  levels?: string[];
  fetched_at?: string;
  next_attempt_at?: string;
  message?: string;
}

type PerformanceSyncState = {
  status: "live" | "updating" | "updated" | "delayed";
  at?: string;
  message?: string;
};

interface CampaignPerformance extends PerformanceSummary {
  campaign_id: string;
  campaign_name: string;
  points: PerformancePoint[];
}

interface PlatformInfo {
  platform: string;
  display_name: string;
  integration_slug: string;
  supported: boolean;
  configured: boolean;
  available: boolean;
  state: "setup_required" | "ready" | "connected" | "unsupported" | "unavailable";
  can_add: boolean;
  setup_url: string;
  connection_count: number;
  connections: ConnectionOption[];
  active_account: boolean;
  unavailable_reason?: string;
}

interface AdResource {
  id: number;
  ad_account_id: number;
  kind: string;
  provider_type: string;
  name: string;
  status: string;
  capabilities: string[];
  metadata: Record<string, unknown>;
  parent_resource_id?: number | null;
  managed_by_app: boolean;
  refreshed_at?: string;
}

interface AccountContext {
  ad_account_id: number;
  platform: string;
  resource_kinds: string[];
  resources: AdResource[];
  defaults: Record<string, AdResource>;
  refresh_errors: Record<string, string>;
}

interface PendingPicker {
  pendingId: number;
  platform?: string;
  pages: PendingAccountPage[];
}

function mcpErrorText(data: any): string | null {
  if (!data?.isError) return null;
  return data.content?.find((item: any) => item.type === "text")?.text || "Request returned an error";
}

function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (event: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (event: AppEventEnvelope<T>) => handlerRef.current(event);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          appName: string,
          scopedProjectId: string,
          listener: (event: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler);

    let lastSeq = 0;
    let source: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url = `/api/app-events/${encodeURIComponent(app)}?project_id=${encodeURIComponent(projectId)}`
        + (lastSeq > 0 ? `&since=${lastSeq}` : "");
      source = new EventSource(url, { withCredentials: true });
      source.onmessage = (message) => {
        try {
          const event = JSON.parse(message.data) as AppEventEnvelope<T>;
          if (event.seq <= lastSeq) return;
          lastSeq = event.seq;
          handlerRef.current(event);
        } catch {}
      };
      source.onerror = () => {
        if (source?.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      source?.close();
    };
  }, [app, projectId]);
}

function ProviderMark({ platform, size = "md" }: { platform: string; size?: "sm" | "md" }) {
	const provider = providerInfo(platform);
	return (
		<span
			aria-hidden="true"
			className={`${size === "sm" ? "h-7 w-7 text-xs" : "h-9 w-9 text-sm"} ${provider.markClass} grid shrink-0 place-items-center rounded border border-border font-semibold shadow-sm`}
		>
			{provider.mark}
		</span>
	);
}

const PROVIDERS: Record<string, { name: string; integration: string; mark: string; markClass: string }> = {
  meta: { name: "Meta Ads", integration: "Facebook & Instagram", mark: "M", markClass: "bg-blue text-white" },
  google: { name: "Google Ads", integration: "Google Ads", mark: "G", markClass: "bg-white text-blue" },
  x: { name: "X Ads", integration: "X Ads", mark: "X", markClass: "bg-text text-bg" },
  reddit: { name: "Reddit Ads", integration: "Reddit Ads", mark: "R", markClass: "bg-[#ff4500] text-white" },
};

function providerInfo(platform: string) {
  return PROVIDERS[platform] || { name: platform || "Ads", integration: platform || "Ads", mark: "A", markClass: "bg-border text-text" };
}

function providerName(platform: string): string {
  return providerInfo(platform).name;
}

function statusStyle(status?: string): string {
  if (status === "ACTIVE") return "bg-green/15 text-green";
  if (status === "PAUSED") return "bg-yellow/15 text-yellow";
  return "bg-border text-text-muted";
}

function displayStatus(campaign: Campaign): string {
  return campaign.status || campaign.effective_status || "UNKNOWN";
}

function entityStatus(entity: { status?: string; effective_status?: string }): string {
  return entity.status || entity.effective_status || "UNKNOWN";
}

function formatBudget(value: string | undefined, currency: string): string {
  if (!value) return "-";
  const cents = Number(value);
  if (!Number.isFinite(cents)) return value;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: 2,
    }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency || "USD"}`;
  }
}

function formatNumber(value: number, maximumFractionDigits = 0): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits }).format(Number(value) || 0);
}

function formatMoneyMicros(value: number, currency: string): string {
  const amount = (Number(value) || 0) / 1_000_000;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      minimumFractionDigits: amount > 0 && amount < 1 ? 2 : 0,
      maximumFractionDigits: 2,
    }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${currency || "USD"}`;
  }
}

function syncStateLabel(state: PerformanceSyncState): string {
  if (state.status === "updating") return "Updating";
  if (state.status === "delayed") return "Sync delayed";
  if (state.status === "live") return "Auto refresh";
  if (!state.at) return "Updated";
  const elapsed = Math.max(0, Date.now() - new Date(state.at).getTime());
  if (elapsed < 60_000) return "Updated now";
  if (elapsed < 3_600_000) return `Updated ${Math.floor(elapsed / 60_000)}m ago`;
  return `Updated ${new Date(state.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

function isoDate(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function dateRange(days: number): { from: string; to: string } {
  const to = new Date();
  const from = new Date(to);
  from.setDate(from.getDate() - Math.max(0, days - 1));
  return { from: isoDate(from), to: isoDate(to) };
}

export function previousDateRange(from: string, to: string): { from: string; to: string } {
  const start = new Date(`${from}T12:00:00`);
  const end = new Date(`${to}T12:00:00`);
  const days = Math.round((end.getTime() - start.getTime()) / 86_400_000) + 1;
  const previousTo = new Date(start);
  previousTo.setDate(previousTo.getDate() - 1);
  const previousFrom = new Date(previousTo);
  previousFrom.setDate(previousFrom.getDate() - days + 1);
  return { from: isoDate(previousFrom), to: isoDate(previousTo) };
}

export function percentageChange(current: number, previous?: number): number | null {
  if (previous == null || previous === 0) return current === 0 ? 0 : null;
  return ((current - previous) / Math.abs(previous)) * 100;
}

export function aggregatePerformance(points: PerformancePoint[]): PerformanceSummary {
  const summary: PerformanceSummary = {
    spend_micros: 0, impressions: 0, reach: 0, clicks: 0, link_clicks: 0,
    conversions: 0, conversion_value_micros: 0, video_views: 0,
    ctr: 0, cpc_micros: 0, cpm_micros: 0, cpa_micros: 0, roas: 0,
    currency: points[0]?.currency || "", timezone: points[0]?.timezone || "",
  };
  for (const point of points) {
    summary.spend_micros += Number(point.spend_micros) || 0;
    summary.impressions += Number(point.impressions) || 0;
    summary.reach += Number(point.reach) || 0;
    summary.clicks += Number(point.clicks) || 0;
    summary.link_clicks += Number(point.link_clicks) || 0;
    summary.conversions += Number(point.conversions) || 0;
    summary.conversion_value_micros += Number(point.conversion_value_micros) || 0;
    summary.video_views += Number(point.video_views) || 0;
  }
  summary.ctr = summary.impressions > 0 ? (summary.clicks / summary.impressions) * 100 : 0;
  summary.cpc_micros = summary.clicks > 0 ? summary.spend_micros / summary.clicks : 0;
  summary.cpm_micros = summary.impressions > 0 ? (summary.spend_micros * 1000) / summary.impressions : 0;
  summary.cpa_micros = summary.conversions > 0 ? summary.spend_micros / summary.conversions : 0;
  summary.roas = summary.spend_micros > 0 ? summary.conversion_value_micros / summary.spend_micros : 0;
  return summary;
}

const RESOURCE_KIND_LABELS: Record<string, string> = {
  identity: "Publishing identities",
  tracking_source: "Conversion tracking",
  conversion_action: "Conversion actions",
  lead_form: "Lead forms",
  audience: "Audiences",
  funding_source: "Funding sources",
};

const RESOURCE_TYPE_LABELS: Record<string, string> = {
  facebook_page: "Facebook Page",
  instagram_business: "Instagram account",
  meta_pixel: "Meta Pixel",
  meta_lead_form: "Meta lead form",
  meta_audience: "Meta audience",
  google_conversion_action: "Google conversion action",
  google_lead_form: "Google lead form",
  google_user_list: "Google audience",
  x_promotable_user: "X identity",
  x_funding_instrument: "X funding source",
  x_custom_audience: "X audience",
  reddit_profile: "Reddit profile",
  reddit_pixel: "Reddit Pixel",
  reddit_funding_instrument: "Reddit funding source",
  reddit_lead_form: "Reddit lead form",
  reddit_custom_audience: "Reddit audience",
};

const LEAD_QUESTION_OPTIONS = [
  { type: "full_name", label: "Full name" },
  { type: "email", label: "Email" },
  { type: "phone", label: "Phone" },
  { type: "company_name", label: "Company" },
  { type: "job_title", label: "Job title" },
];

function resourcePurpose(resource: AdResource): string | null {
  if (resource.provider_type === "facebook_page") return "publishing_identity";
  if (resource.provider_type === "instagram_business") return "instagram_identity";
	if (resource.kind === "identity") return "publishing_identity";
  if (resource.kind === "tracking_source" || resource.kind === "conversion_action") return "conversion_source";
  if (resource.kind === "lead_form") return "lead_form";
  if (resource.kind === "audience") return "audience";
	if (resource.kind === "funding_source") return "funding_source";
  return null;
}

function Modal({
  title,
  description,
  actions,
  size = "default",
  onClose,
  children,
  labelledBy,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  size?: "default" | "large" | "workspace";
  onClose: () => void;
  children: React.ReactNode;
  labelledBy: string;
}) {
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-30 grid place-items-center bg-black/50 p-4" onMouseDown={onClose}>
      <section
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        className={`min-w-0 w-full overflow-hidden rounded border border-border bg-bg-card shadow-xl ${size === "workspace" ? "max-w-6xl" : size === "large" ? "max-w-3xl" : "max-w-lg"}`}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <h2 id={labelledBy} className="text-sm font-semibold text-text">{title}</h2>
            {description && <p className="mt-1 text-xs text-text-muted">{description}</p>}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {actions}
            <button
              ref={closeRef}
              type="button"
              onClick={onClose}
              aria-label="Close"
              title="Close"
              className="grid h-8 w-8 place-items-center rounded text-lg text-text-muted hover:bg-bg-input hover:text-text"
            >
              ×
            </button>
          </div>
        </header>
        {children}
      </section>
    </div>
  );
}

function KpiStrip({
  summary,
  previous,
  currency,
}: {
  summary: PerformanceSummary;
  previous?: PerformanceSummary | null;
  currency: string;
}) {
  const metrics = [
    { label: "Spend", value: summary.spend_micros, previous: previous?.spend_micros, display: formatMoneyMicros(summary.spend_micros, currency), direction: "neutral" },
    { label: "Impressions", value: summary.impressions, previous: previous?.impressions, display: formatNumber(summary.impressions), direction: "neutral" },
    { label: "Clicks", value: summary.clicks, previous: previous?.clicks, display: formatNumber(summary.clicks), direction: "up" },
    { label: "CTR", value: summary.ctr, previous: previous?.ctr, display: `${formatNumber(summary.ctr, 2)}%`, direction: "up" },
    { label: "CPC", value: summary.cpc_micros, previous: previous?.cpc_micros, display: formatMoneyMicros(summary.cpc_micros, currency), direction: "down" },
    { label: "Conversions", value: summary.conversions, previous: previous?.conversions, display: formatNumber(summary.conversions, 2), direction: "up" },
    { label: "CPA", value: summary.cpa_micros, previous: previous?.cpa_micros, display: formatMoneyMicros(summary.cpa_micros, currency), direction: "down" },
    { label: "ROAS", value: summary.roas, previous: previous?.roas, display: `${formatNumber(summary.roas, 2)}x`, direction: "up" },
  ];
  return (
    <div className="grid grid-cols-2 divide-x divide-y divide-border border-b border-border sm:grid-cols-4 xl:grid-cols-8 xl:divide-y-0">
      {metrics.map((metric) => {
        const change = percentageChange(metric.value, metric.previous);
        const favorable = change != null && change !== 0 && (metric.direction === "up" ? change > 0 : metric.direction === "down" ? change < 0 : false);
        const unfavorable = change != null && change !== 0 && (metric.direction === "up" ? change < 0 : metric.direction === "down" ? change > 0 : false);
        return (
          <div key={metric.label} className="min-w-0 px-3 py-3">
            <div className="text-[11px] font-medium uppercase text-text-dim">{metric.label}</div>
            <div className="mt-1 truncate text-lg font-semibold tabular-nums text-text" title={metric.display}>{metric.display}</div>
            {previous && (
              <div className={`mt-0.5 text-[11px] tabular-nums ${favorable ? "text-green" : unfavorable ? "text-red" : "text-text-dim"}`}>
                {change == null ? "New" : `${change >= 0 ? "+" : ""}${formatNumber(change, 1)}%`}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function PerformanceChart({ points, currency, compact = false }: { points: PerformancePoint[]; currency: string; compact?: boolean }) {
  const byDate = new Map<string, { date: string; spend: number; clicks: number; conversions: number }>();
  for (const point of points) {
    const item = byDate.get(point.date) || { date: point.date, spend: 0, clicks: 0, conversions: 0 };
    item.spend += (Number(point.spend_micros) || 0) / 1_000_000;
    item.clicks += Number(point.clicks) || 0;
    item.conversions += Number(point.conversions) || 0;
    byDate.set(point.date, item);
  }
  const data = [...byDate.values()].sort((a, b) => a.date.localeCompare(b.date));
  if (data.length === 0) {
    return <div className="grid h-44 place-items-center text-sm text-text-muted">No delivery in this date range.</div>;
  }
  return (
    <div
      className="w-full min-w-0"
      style={{ height: compact ? 208 : 288, minHeight: compact ? 208 : 288 }}
      role="img"
      aria-label="Daily spend and clicks"
    >
      <ResponsiveContainer
        width="100%"
        height="100%"
        minWidth={0}
        minHeight={compact ? 208 : 288}
        initialDimension={{ width: compact ? 720 : 960, height: compact ? 208 : 288 }}
      >
        <AreaChart data={data} margin={{ top: 12, right: 12, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id="adsSpendFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#f97316" stopOpacity={0.3} />
              <stop offset="100%" stopColor="#f97316" stopOpacity={0.02} />
            </linearGradient>
            <linearGradient id="adsClicksFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#3b82f6" stopOpacity={0.2} />
              <stop offset="100%" stopColor="#3b82f6" stopOpacity={0.01} />
            </linearGradient>
          </defs>
          <CartesianGrid vertical={false} stroke="#2a2a2a" strokeDasharray="3 3" />
          <XAxis dataKey="date" tick={{ fill: "#8a8a8a", fontSize: 10 }} axisLine={false} tickLine={false} minTickGap={28} tickFormatter={(value) => String(value).slice(5)} />
          <YAxis yAxisId="money" tick={{ fill: "#8a8a8a", fontSize: 10 }} axisLine={false} tickLine={false} width={50} tickFormatter={(value) => formatNumber(Number(value), 0)} />
          <YAxis yAxisId="count" orientation="right" hide />
          <Tooltip
            contentStyle={{ background: "#111", border: "1px solid #333", borderRadius: 4, color: "#e5e5e5", fontSize: 12 }}
            formatter={(value, name) => name === "Spend" ? [formatMoneyMicros(Number(value) * 1_000_000, currency), name] : [formatNumber(Number(value), 2), name]}
            labelFormatter={(label) => new Date(`${label}T12:00:00`).toLocaleDateString()}
          />
          <Area yAxisId="money" type="monotone" dataKey="spend" name="Spend" stroke="#f97316" strokeWidth={2} fill="url(#adsSpendFill)" dot={false} activeDot={{ r: 3 }} isAnimationActive={false} />
          <Area yAxisId="count" type="monotone" dataKey="clicks" name="Clicks" stroke="#3b82f6" strokeWidth={1.5} fill="url(#adsClicksFill)" dot={false} activeDot={{ r: 3 }} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

function emptyPerformance(currency: string, timezone = ""): PerformanceSummary {
  return {
    spend_micros: 0, impressions: 0, reach: 0, clicks: 0, link_clicks: 0,
    conversions: 0, conversion_value_micros: 0, video_views: 0,
    ctr: 0, cpc_micros: 0, cpm_micros: 0, cpa_micros: 0, roas: 0,
    currency, timezone,
  };
}

function entityPerformance(response: PerformanceResponse | null, id: string, currency: string): CampaignPerformance {
  const points = (response?.data || []).filter((point) => point.entity_id === id || point.campaign_id === id);
  const summary = points.length > 0 ? aggregatePerformance(points) : emptyPerformance(currency);
  return {
    campaign_id: id,
    campaign_name: points.find((point) => point.entity_name || point.campaign_name)?.entity_name
      || points.find((point) => point.campaign_name)?.campaign_name
      || id,
    points,
    ...summary,
  };
}

function storyField(ad: Ad | null, key: string): string {
  const story = ad?.creative?.object_story_spec || {};
  const data = story.link_data || story.video_data || {};
  if (key === "image") return ad?.creative?.image_url || data.picture || data.image_url || ad?.creative?.thumbnail_url || "";
  if (key === "destination") return data.link || data.call_to_action?.value?.link || "";
  if (key === "cta") return data.call_to_action?.type || "";
  return data[key] || "";
}

function StatusPill({ status }: { status: string }) {
  return <span className={`rounded px-2 py-1 text-xs font-medium ${statusStyle(status)}`}>{status}</span>;
}

function CampaignWorkspace({
  account,
  campaign,
  initialPerformance,
  dateFrom,
  dateTo,
  callTool,
  eventRefreshKey,
  onCampaignChanged,
  onClose,
}: {
  account: AdAccount;
  campaign: Campaign;
  initialPerformance: CampaignPerformance | null;
  dateFrom: string;
  dateTo: string;
  callTool: (tool: string, args: Record<string, unknown>) => Promise<any>;
  eventRefreshKey: number;
  onCampaignChanged: (campaignID: string, status: string) => void;
  onClose: () => void;
}) {
  const [level, setLevel] = useState<"campaign" | "ad_group" | "ad">("campaign");
  const [adSets, setAdSets] = useState<AdSet[]>([]);
  const [ads, setAds] = useState<Ad[]>([]);
  const [selectedAdSetID, setSelectedAdSetID] = useState<string | null>(null);
  const [selectedAdID, setSelectedAdID] = useState<string | null>(null);
  const [campaignMetrics, setCampaignMetrics] = useState<PerformanceResponse | null>(() => initialPerformance ? {
    data: initialPerformance.points,
    summary: initialPerformance,
    source: "cache",
    freshness: { row_count: initialPerformance.points.length },
  } : null);
  const [adSetMetrics, setAdSetMetrics] = useState<PerformanceResponse | null>(null);
  const [adMetrics, setAdMetrics] = useState<PerformanceResponse | null>(null);
  const [loadingHierarchy, setLoadingHierarchy] = useState(true);
  const [loadingMetrics, setLoadingMetrics] = useState(false);
  const [updating, setUpdating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const request = useRef(0);
  const eventRefreshSeen = useRef(eventRefreshKey);

  const selectedAdSet = adSets.find((item) => item.id === selectedAdSetID) || null;
  const selectedAd = ads.find((item) => item.id === selectedAdID) || null;
  const currentID = level === "campaign" ? campaign.id : level === "ad_group" ? selectedAdSetID || "" : selectedAdID || "";
  const currentMetrics = level === "campaign" ? campaignMetrics : level === "ad_group" ? adSetMetrics : adMetrics;
  const performance = entityPerformance(currentMetrics, currentID, account.currency);
  const currentEntity = level === "campaign" ? campaign : level === "ad_group" ? selectedAdSet : selectedAd;
  const currentStatus = currentEntity ? entityStatus(currentEntity) : "UNKNOWN";

  const performanceArgs = useCallback((metricLevel: "campaign" | "ad_group" | "ad", entityIDs: string[], refresh: boolean) => ({
    ad_account_id: account.id,
    level: metricLevel,
    date_from: dateFrom,
    date_to: dateTo,
    granularity: "day",
    entity_ids: entityIDs,
    refresh,
  }), [account.id, dateFrom, dateTo]);

  const loadAds = useCallback(async (adSetID: string, refresh: boolean, requestID: number) => {
    const list = await callTool("ad_list", { ad_account_id: account.id, adset_id: adSetID, limit: 100 });
    if (request.current !== requestID) return;
    const nextAds = (list.data || []) as Ad[];
    setAds(nextAds);
    setSelectedAdID((current) => current && nextAds.some((ad) => ad.id === current) ? current : nextAds[0]?.id || null);
    if (nextAds.length === 0) {
      setAdMetrics(null);
      return;
    }
    const metrics = await callTool("performance_get", performanceArgs("ad", nextAds.map((ad) => ad.id), refresh));
    if (request.current === requestID) setAdMetrics(metrics as PerformanceResponse);
  }, [account.id, callTool, performanceArgs]);

  const loadWorkspace = useCallback(async (refresh: boolean) => {
    const requestID = ++request.current;
    setLoadingHierarchy(true);
    setLoadingMetrics(true);
    setError(null);
    try {
      const [campaignResult, adSetList] = await Promise.all([
        callTool("performance_get", performanceArgs("campaign", [campaign.id], refresh)),
        callTool("adset_list", { ad_account_id: account.id, campaign_id: campaign.id, limit: 100 }),
      ]);
      if (request.current !== requestID) return;
      setCampaignMetrics(campaignResult as PerformanceResponse);
      const nextAdSets = (adSetList.data || []) as AdSet[];
      setAdSets(nextAdSets);
      const nextAdSetID = selectedAdSetID && nextAdSets.some((item) => item.id === selectedAdSetID)
        ? selectedAdSetID
        : nextAdSets[0]?.id || null;
      setSelectedAdSetID(nextAdSetID);
      if (nextAdSets.length === 0) {
        setAdSetMetrics(null);
        setAds([]);
        setAdMetrics(null);
        return;
      }
      const adSetResult = await callTool("performance_get", performanceArgs("ad_group", nextAdSets.map((item) => item.id), refresh));
      if (request.current !== requestID) return;
      setAdSetMetrics(adSetResult as PerformanceResponse);
      if (nextAdSetID) await loadAds(nextAdSetID, refresh, requestID);
    } catch (err) {
      if (request.current === requestID) setError((err as Error).message);
    } finally {
      if (request.current === requestID) {
        setLoadingHierarchy(false);
        setLoadingMetrics(false);
      }
    }
  }, [account.id, callTool, campaign.id, loadAds, performanceArgs, selectedAdSetID]);

  useEffect(() => {
    loadWorkspace(false);
    return () => { request.current++; };
  }, [campaign.id]);

  useEffect(() => {
    if (eventRefreshSeen.current === eventRefreshKey) return;
    eventRefreshSeen.current = eventRefreshKey;
    loadWorkspace(false);
  }, [eventRefreshKey]);

  const chooseAdSet = async (adSetID: string) => {
    setSelectedAdSetID(adSetID);
    setSelectedAdID(null);
    setAds([]);
    setAdMetrics(null);
    setLevel("ad_group");
    setLoadingHierarchy(true);
    setError(null);
    const requestID = ++request.current;
    try {
      await loadAds(adSetID, false, requestID);
    } catch (err) {
      if (request.current === requestID) setError((err as Error).message);
    } finally {
      if (request.current === requestID) setLoadingHierarchy(false);
    }
  };

  const updateStatus = async () => {
    if (!currentEntity || updating) return;
    const target = currentStatus === "ACTIVE" ? "PAUSED" : "ACTIVE";
    setUpdating(true);
    setError(null);
    try {
      if (level === "campaign") {
        await callTool(target === "ACTIVE" ? "campaign_resume" : "campaign_pause", {
          ad_account_id: account.id,
          campaign_id: campaign.id,
        });
        onCampaignChanged(campaign.id, target);
      } else if (level === "ad_group" && selectedAdSet) {
        await callTool("adset_update", { ad_account_id: account.id, adset_id: selectedAdSet.id, status: target });
        setAdSets((current) => current.map((item) => item.id === selectedAdSet.id ? { ...item, status: target } : item));
      } else if (level === "ad" && selectedAd) {
        await callTool("ad_update", {
          ad_account_id: account.id,
          adset_id: selectedAd.adset_id || selectedAdSetID,
          ad_id: selectedAd.id,
          status: target,
        });
        setAds((current) => current.map((item) => item.id === selectedAd.id ? { ...item, status: target } : item));
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setUpdating(false);
    }
  };

  const target = selectedAdSet?.targeting || {};
  const countries = target.geo_locations?.countries || [];
  const platforms = target.publisher_platforms || [];
  const creativeImage = storyField(selectedAd, "image");
  const destination = storyField(selectedAd, "destination");

  return (
    <Modal
      title={campaign.name || campaign.id}
		description={`${providerName(account.platform)} · ${dateFrom} to ${dateTo}`}
      actions={(
        <button type="button" onClick={() => loadWorkspace(true)} disabled={loadingMetrics} aria-label="Refresh campaign workspace" title="Refresh" className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50">↻</button>
      )}
      size="workspace"
      onClose={onClose}
      labelledBy="ads-campaign-workspace-title"
    >
      <style>{`
        .ads-campaign-workspace {
          display: grid;
          grid-template-columns: minmax(0, 1fr);
          grid-template-rows: minmax(10rem, 42%) minmax(0, 1fr);
        }
        .ads-performance-layout,
        .ads-creative-layout {
          display: grid;
          grid-template-columns: minmax(0, 1fr);
        }
        @media (min-width: 768px) {
          .ads-campaign-workspace {
            grid-template-columns: 18rem minmax(0, 1fr);
            grid-template-rows: minmax(0, 1fr);
          }
          .ads-creative-layout {
            grid-template-columns: 12rem minmax(0, 1fr);
          }
        }
        @media (min-width: 1024px) {
          .ads-performance-layout {
            grid-template-columns: minmax(0, 1fr) 18rem;
          }
        }
      `}</style>
      <div className="ads-campaign-workspace min-h-0" style={{ height: "min(76vh, 760px)" }}>
        <aside className="min-h-0 overflow-y-auto border-b border-border md:border-b-0 md:border-r">
          <div className="border-b border-border px-3 py-2 text-[11px] font-medium uppercase text-text-dim">Delivery hierarchy</div>
          <button type="button" onClick={() => setLevel("campaign")} className={`flex w-full items-center gap-3 border-b border-border px-3 py-3 text-left ${level === "campaign" ? "bg-bg-input" : "hover:bg-bg-input/50"}`}>
            <span className="grid h-7 w-7 shrink-0 place-items-center rounded border border-border text-xs font-semibold text-text-muted">C</span>
            <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium" title={campaign.name || campaign.id}>{campaign.name || campaign.id}</span><span className="block truncate text-xs text-text-dim">Campaign</span></span>
            <StatusPill status={entityStatus(campaign)} />
          </button>
          <div className="border-b border-border bg-bg-input/40 px-3 py-2 text-[11px] font-medium uppercase text-text-dim">Ad sets · {adSets.length}</div>
          {loadingHierarchy && adSets.length === 0 ? <p className="px-3 py-4 text-xs text-text-muted">Loading hierarchy...</p> : adSets.length === 0 ? <p className="px-3 py-4 text-xs text-text-muted">No ad sets</p> : adSets.map((item) => (
            <button key={item.id} type="button" onClick={() => chooseAdSet(item.id)} className={`flex w-full items-center gap-3 border-b border-border px-3 py-3 text-left ${level === "ad_group" && selectedAdSetID === item.id ? "bg-bg-input" : "hover:bg-bg-input/50"}`}>
              <span className="grid h-7 w-7 shrink-0 place-items-center rounded border border-border text-xs font-semibold text-text-muted">S</span>
              <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium" title={item.name || item.id}>{item.name || item.id}</span><span className="block truncate text-xs text-text-dim">{item.optimization_goal || "Ad set"}</span></span>
              <StatusPill status={entityStatus(item)} />
            </button>
          ))}
          {selectedAdSetID && <div className="border-b border-border bg-bg-input/40 px-3 py-2 text-[11px] font-medium uppercase text-text-dim">Ads · {ads.length}</div>}
          {selectedAdSetID && (loadingHierarchy && ads.length === 0 ? <p className="px-3 py-4 text-xs text-text-muted">Loading ads...</p> : ads.length === 0 ? <p className="px-3 py-4 text-xs text-text-muted">No ads</p> : ads.map((ad) => (
            <button key={ad.id} type="button" onClick={() => { setSelectedAdID(ad.id); setLevel("ad"); }} className={`flex w-full items-center gap-3 border-b border-border px-3 py-3 text-left ${level === "ad" && selectedAdID === ad.id ? "bg-bg-input" : "hover:bg-bg-input/50"}`}>
              {storyField(ad, "image") ? <img src={storyField(ad, "image")} alt="" className="h-9 w-9 shrink-0 rounded border border-border object-cover" /> : <span className="grid h-9 w-9 shrink-0 place-items-center rounded border border-border text-xs font-semibold text-text-muted">A</span>}
              <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium" title={ad.name || ad.id}>{ad.name || ad.id}</span><span className="block truncate text-xs text-text-dim">Ad</span></span>
              <StatusPill status={entityStatus(ad)} />
            </button>
          )))}
        </aside>

        <section className="min-h-0 overflow-y-auto">
          {error && <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-3 text-sm text-red">{error}</div>}
          <header className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
            <div className="min-w-0">
              <div className="mb-1 flex items-center gap-2 text-xs text-text-dim"><span>Campaign</span>{level !== "campaign" && <><span>/</span><span>Ad set</span></>}{level === "ad" && <><span>/</span><span>Ad</span></>}</div>
              <h3 className="truncate text-base font-semibold text-text">{level === "campaign" ? campaign.name || campaign.id : level === "ad_group" ? selectedAdSet?.name || selectedAdSetID : selectedAd?.name || selectedAdID}</h3>
              <p className="mt-0.5 truncate text-xs text-text-dim">{currentID}</p>
            </div>
            <div className="flex items-center gap-2">
              <StatusPill status={currentStatus} />
              <button type="button" onClick={updateStatus} disabled={updating || !currentEntity} className="h-8 rounded border border-border px-3 text-xs font-medium text-text hover:bg-bg-input disabled:opacity-50">{updating ? "Updating..." : currentStatus === "ACTIVE" ? "Pause" : "Activate"}</button>
            </div>
          </header>

          <KpiStrip summary={performance} currency={account.currency} />

          <div className="ads-performance-layout border-b border-border">
            <div className="min-w-0 border-b border-border px-4 py-4 lg:border-b-0 lg:border-r">
              <div className="mb-3 flex items-center justify-between gap-3"><h4 className="text-xs font-medium uppercase text-text-dim">Daily performance</h4><span className="text-xs text-text-muted">{performance.points.length} data point{performance.points.length === 1 ? "" : "s"}</span></div>
              <PerformanceChart points={performance.points} currency={account.currency} compact />
            </div>
            <div className="divide-y divide-border text-sm">
              {level === "campaign" && <>
                <DetailRow label="Objective" value={campaign.objective || "-"} />
                <DetailRow label="Daily budget" value={formatBudget(campaign.daily_budget, account.currency)} />
                <DetailRow label="Ad sets" value={formatNumber(adSets.length)} />
              </>}
              {level === "ad_group" && selectedAdSet && <>
                <DetailRow label="Optimization" value={selectedAdSet.optimization_goal || "-"} />
                <DetailRow label="Billing" value={selectedAdSet.billing_event || "-"} />
                <DetailRow label="Daily budget" value={formatBudget(selectedAdSet.daily_budget, account.currency)} />
                <DetailRow label="Age" value={target.age_min || target.age_max ? `${target.age_min || "-"}–${target.age_max || "+"}` : "-"} />
                <DetailRow label="Countries" value={countries.length > 0 ? countries.join(", ") : "-"} />
                <DetailRow label="Placements" value={platforms.length > 0 ? platforms.join(", ") : "Automatic"} />
                <DetailRow label="Ads" value={formatNumber(ads.length)} />
              </>}
              {level === "ad" && selectedAd && <>
                <DetailRow label="Creative" value={selectedAd.creative?.name || selectedAd.creative?.id || "-"} />
                <DetailRow label="Call to action" value={storyField(selectedAd, "cta") || "-"} />
                <DetailRow label="Tracking rules" value={formatNumber(selectedAd.tracking_specs?.length || 0)} />
              </>}
              <DetailRow label="Provider status" value={currentEntity?.effective_status || currentStatus} />
            </div>
          </div>

          {level === "ad" && selectedAd && (
            <div className="ads-creative-layout gap-4 px-4 py-4">
              <div className="aspect-[4/5] overflow-hidden rounded border border-border bg-bg-input">
                {creativeImage ? <img src={creativeImage} alt="Ad creative" className="h-full w-full object-cover" /> : <div className="grid h-full place-items-center px-3 text-center text-xs text-text-muted">No creative preview</div>}
              </div>
              <div className="min-w-0 space-y-3">
                <div><div className="text-[11px] font-medium uppercase text-text-dim">Headline</div><div className="mt-1 text-sm font-medium text-text">{storyField(selectedAd, "name") || selectedAd.name}</div></div>
                <div><div className="text-[11px] font-medium uppercase text-text-dim">Primary text</div><p className="mt-1 whitespace-pre-wrap text-sm leading-5 text-text-muted">{storyField(selectedAd, "message") || "-"}</p></div>
                {storyField(selectedAd, "description") && <div><div className="text-[11px] font-medium uppercase text-text-dim">Description</div><p className="mt-1 text-sm text-text-muted">{storyField(selectedAd, "description")}</p></div>}
                {destination && <div><div className="text-[11px] font-medium uppercase text-text-dim">Destination</div><a href={destination} target="_blank" rel="noreferrer" className="mt-1 block truncate text-sm text-accent hover:underline">{destination}</a></div>}
              </div>
            </div>
          )}
        </section>
      </div>
    </Modal>
  );
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return <div className="flex items-start justify-between gap-3 px-4 py-2.5"><span className="text-xs text-text-dim">{label}</span><span className="min-w-0 truncate text-right text-xs font-medium text-text" style={{ maxWidth: "65%" }} title={value}>{value}</span></div>;
}

export default function AdsPanel({ projectId, installId }: NativePanelProps) {
  const initialRange = useMemo(() => dateRange(30), []);
  const scopedProject = projectId || PANEL_PROJECT_ID;
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  const [platforms, setPlatforms] = useState<PlatformInfo[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [activeView, setActiveView] = useState<"overview" | "campaigns">("overview");
  const [performance, setPerformance] = useState<PerformanceResponse | null>(null);
  const [comparison, setComparison] = useState<PerformanceResponse | null>(null);
  const [loadingPerformance, setLoadingPerformance] = useState(false);
  const [performanceError, setPerformanceError] = useState<string | null>(null);
  const [performanceSync, setPerformanceSync] = useState<PerformanceSyncState>({ status: "live" });
  const [workspaceRefreshKey, setWorkspaceRefreshKey] = useState(0);
  const [dateFrom, setDateFrom] = useState(initialRange.from);
  const [dateTo, setDateTo] = useState(initialRange.to);
  const [compareEnabled, setCompareEnabled] = useState(false);
  const [selectedCampaignID, setSelectedCampaignID] = useState<string | null>(null);
  const [loadingAccounts, setLoadingAccounts] = useState(true);
  const [loadingCampaigns, setLoadingCampaigns] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [startingPlatform, setStartingPlatform] = useState<string | null>(null);
  const [copyingPlatform, setCopyingPlatform] = useState<string | null>(null);
  const [copiedPlatform, setCopiedPlatform] = useState<string | null>(null);
  const [connectionPicker, setConnectionPicker] = useState<PlatformInfo | null>(null);
  const [connectionPickerMode, setConnectionPickerMode] = useState<"account" | "link">("account");
  const [pendingPicker, setPendingPicker] = useState<PendingPicker | null>(null);
  const [accountFilter, setAccountFilter] = useState("");
  const [disconnectTarget, setDisconnectTarget] = useState<AdAccount | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [setupAccount, setSetupAccount] = useState<AdAccount | null>(null);
  const [accountContext, setAccountContext] = useState<AccountContext | null>(null);
  const [loadingResources, setLoadingResources] = useState(false);
  const [savingResourceId, setSavingResourceId] = useState<number | null>(null);
  const [resourceError, setResourceError] = useState<string | null>(null);
  const [leadFormOpen, setLeadFormOpen] = useState(false);
  const [creatingLeadForm, setCreatingLeadForm] = useState(false);
  const [leadFormName, setLeadFormName] = useState("");
  const [leadFormPrivacyURL, setLeadFormPrivacyURL] = useState("");
  const [leadFormFollowUpURL, setLeadFormFollowUpURL] = useState("");
  const [leadFormBusinessName, setLeadFormBusinessName] = useState("");
  const [leadFormCampaignID, setLeadFormCampaignID] = useState("");
  const [leadFormHigherIntent, setLeadFormHigherIntent] = useState(true);
  const [leadFormQuestions, setLeadFormQuestions] = useState<string[]>(["full_name", "email", "phone"]);
  const [error, setError] = useState<string | null>(null);
  const campaignRequest = useRef(0);
  const performanceRequest = useRef(0);
  const copyFeedbackTimer = useRef<number | null>(null);
  const resourceRequest = useRef(0);
  const eventRefreshTimer = useRef<number | null>(null);

  const selected = useMemo(
    () => accounts.find((account) => account.id === selectedId) || null,
    [accounts, selectedId],
  );
  const filteredPendingPages = useMemo(() => {
    if (!pendingPicker) return [];
    const query = accountFilter.trim().toLowerCase();
    if (!query) return pendingPicker.pages;
    return pendingPicker.pages.filter((page) =>
      `${page.name} ${page.id} ${page.currency} ${page.timezone}`.toLowerCase().includes(query),
    );
  }, [accountFilter, pendingPicker]);
  const groupedResources = useMemo(() => {
    if (!accountContext) return [];
    return accountContext.resource_kinds.map((kind) => ({
      kind,
      resources: accountContext.resources.filter((resource) => resource.kind === kind),
      error: accountContext.refresh_errors?.[kind],
    }));
  }, [accountContext]);
  const campaignPerformance = useMemo(() => {
    const grouped = new Map<string, PerformancePoint[]>();
    for (const point of performance?.data || []) {
      const id = point.campaign_id || point.entity_id;
      if (!id) continue;
      grouped.set(id, [...(grouped.get(id) || []), point]);
    }
    const result = new Map<string, CampaignPerformance>();
    for (const [campaignID, points] of grouped) {
      result.set(campaignID, {
        campaign_id: campaignID,
        campaign_name: points.find((point) => point.campaign_name || point.entity_name)?.campaign_name
          || points.find((point) => point.entity_name)?.entity_name
          || campaignID,
        points,
        ...aggregatePerformance(points),
      });
    }
    return result;
  }, [performance]);
  const rankedCampaigns = useMemo(
    () => [...campaignPerformance.values()].sort((a, b) => b.spend_micros - a.spend_micros),
    [campaignPerformance],
  );
  const selectedCampaign = selectedCampaignID ? campaigns.find((campaign) => campaign.id === selectedCampaignID) || null : null;
  const selectedCampaignPerformance = selectedCampaignID ? campaignPerformance.get(selectedCampaignID) || null : null;

  const appURL = useCallback((path: string) => {
    const url = new URL(`${API}${path}`, window.location.origin);
    if (scopedProject) url.searchParams.set("project_id", scopedProject);
    url.searchParams.set("install_id", String(installId));
    return url.pathname + url.search;
  }, [installId, scopedProject]);

  const apiJSON = useCallback(async (path: string, init?: RequestInit): Promise<any> => {
    const response = await fetch(appURL(path), {
      credentials: "same-origin",
      ...init,
      headers: {
        ...(init?.body ? { "content-type": "application/json" } : {}),
        ...(init?.headers || {}),
      },
    });
    if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
    const data = await response.json();
    const message = mcpErrorText(data);
    if (message) throw new Error(message);
    return data;
  }, [appURL]);

  const callTool = useCallback(async (tool: string, args: Record<string, unknown>): Promise<any> => {
    const response = await fetch(appURL("/mcp"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: {
          name: tool,
          arguments: { ...args, _project_id: scopedProject },
        },
      }),
    });
    if (!response.ok) throw new Error((await response.text()) || `${tool}: HTTP ${response.status}`);
    const envelope = await response.json();
    if (envelope.error) throw new Error(envelope.error.message || tool);
    const text = envelope.result?.content?.find((item: any) => item.type === "text")?.text;
    const data = text ? JSON.parse(text) : envelope.result;
    const message = mcpErrorText(data);
    if (message) throw new Error(message);
    return data;
  }, [appURL, scopedProject]);

  const refreshAccounts = useCallback(async () => {
    setLoadingAccounts(true);
    try {
      const result = await apiJSON("/accounts");
      const next = result.accounts || [];
      setAccounts(next);
      setSelectedId((current) => current && next.some((account: AdAccount) => account.id === current) ? current : null);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoadingAccounts(false);
    }
  }, [apiJSON]);

  const refreshPlatforms = useCallback(async () => {
    try {
      const result = await apiJSON("/platforms");
      setPlatforms(result.platforms || []);
    } catch (err) {
      setError((err as Error).message);
    }
  }, [apiJSON]);

  const refreshCampaigns = useCallback(async (account: AdAccount) => {
    const request = ++campaignRequest.current;
    setLoadingCampaigns(true);
    setCampaigns([]);
    try {
      const result = await callTool("campaign_list", { ad_account_id: account.id, limit: 50 });
      if (request !== campaignRequest.current) return;
      setCampaigns(result.data || result.campaigns || []);
    } catch (err) {
      if (request === campaignRequest.current) setError((err as Error).message);
    } finally {
      if (request === campaignRequest.current) setLoadingCampaigns(false);
    }
  }, [callTool]);

  const refreshPerformance = useCallback(async (account: AdAccount, refresh = true) => {
    if (!dateFrom || !dateTo || dateFrom > dateTo) return;
    const request = ++performanceRequest.current;
    setLoadingPerformance(true);
    setPerformanceError(null);
    if (refresh) setPerformanceSync({ status: "updating" });
    try {
      const currentPromise = callTool("performance_get", {
        ad_account_id: account.id,
        level: "campaign",
        date_from: dateFrom,
        date_to: dateTo,
        granularity: "day",
        refresh,
      });
      const previous = previousDateRange(dateFrom, dateTo);
      const comparisonPromise = compareEnabled
        ? callTool("performance_get", {
          ad_account_id: account.id,
          level: "campaign",
          date_from: previous.from,
          date_to: previous.to,
          granularity: "day",
          refresh,
        })
        : Promise.resolve(null);
      const [currentResult, comparisonResult] = await Promise.all([currentPromise, comparisonPromise]);
      if (request !== performanceRequest.current) return;
      setPerformance(currentResult as PerformanceResponse);
      setComparison(comparisonResult as PerformanceResponse | null);
      setPerformanceSync({
        status: "updated",
        at: (currentResult as PerformanceResponse).freshness?.fetched_at || new Date().toISOString(),
      });
    } catch (err) {
      if (request === performanceRequest.current) {
        setPerformanceError((err as Error).message);
        setPerformanceSync({ status: "delayed", message: (err as Error).message });
      }
    } finally {
      if (request === performanceRequest.current) setLoadingPerformance(false);
    }
  }, [callTool, compareEnabled, dateFrom, dateTo]);

  const loadAccountContext = useCallback(async (account: AdAccount, refresh = true) => {
    const request = ++resourceRequest.current;
    setLoadingResources(true);
    setResourceError(null);
    try {
      const result = await callTool("account_context_get", { ad_account_id: account.id, refresh });
      if (request === resourceRequest.current) setAccountContext(result as AccountContext);
    } catch (err) {
      if (request === resourceRequest.current) setResourceError((err as Error).message);
    } finally {
      if (request === resourceRequest.current) setLoadingResources(false);
    }
  }, [callTool]);

  const openAccountResources = useCallback((account: AdAccount) => {
    setSetupAccount(account);
    setAccountContext(null);
    loadAccountContext(account, true);
  }, [loadAccountContext]);

  const closeAccountResources = useCallback(() => {
    resourceRequest.current++;
    setSetupAccount(null);
    setAccountContext(null);
    setResourceError(null);
    setLoadingResources(false);
    setLeadFormOpen(false);
  }, []);

  const openLeadFormCreate = () => {
    setLeadFormName("");
    setLeadFormPrivacyURL("");
    setLeadFormFollowUpURL("");
    setLeadFormBusinessName("");
    setLeadFormCampaignID("");
    setLeadFormHigherIntent(true);
    setLeadFormQuestions(["full_name", "email", "phone"]);
    setResourceError(null);
    setLeadFormOpen(true);
  };

  const toggleLeadQuestion = (type: string) => {
    setLeadFormQuestions((current) => current.includes(type)
      ? current.filter((item) => item !== type)
      : [...current, type]);
  };

  const createLeadForm = async () => {
    if (!setupAccount || !leadFormName.trim() || !leadFormPrivacyURL.trim() || leadFormQuestions.length === 0) return;
    setCreatingLeadForm(true);
    setResourceError(null);
    try {
      await callTool("lead_form_create", {
        ad_account_id: setupAccount.id,
        name: leadFormName.trim(),
        privacy_policy_url: leadFormPrivacyURL.trim(),
        follow_up_url: leadFormFollowUpURL.trim() || undefined,
        business_name: leadFormBusinessName.trim() || undefined,
        headline: leadFormName.trim(),
        description: "Request more information",
        call_to_action: "get_quote",
        call_to_action_description: "Submit the form to get in touch",
        higher_intent: leadFormHigherIntent,
        campaign_id: leadFormCampaignID || undefined,
        questions: leadFormQuestions.map((type) => ({ type })),
      });
      setLeadFormOpen(false);
      await loadAccountContext(setupAccount, true);
    } catch (err) {
      setResourceError((err as Error).message);
    } finally {
      setCreatingLeadForm(false);
    }
  };

  const setResourceDefault = async (resource: AdResource) => {
    if (!setupAccount) return;
    const purpose = resourcePurpose(resource);
    if (!purpose) return;
    setSavingResourceId(resource.id);
    setResourceError(null);
    try {
      const result = await callTool("resource_set_default", {
        ad_account_id: setupAccount.id,
        purpose,
        resource_id: resource.id,
      });
      setAccountContext((current) => current ? {
        ...current,
        defaults: { ...current.defaults, [purpose]: result.resource },
      } : current);
    } catch (err) {
      setResourceError((err as Error).message);
    } finally {
      setSavingResourceId(null);
    }
  };

  const resumeOAuth = useCallback(async (pendingId: number) => {
    try {
      const result = await apiJSON(`/accounts/${pendingId}/pages`);
      setPendingPicker({
        pendingId,
        platform: result.platform,
        pages: result.pages || [],
      });
      setAccountFilter("");
      setConnectionPicker(null);
      setAddOpen(false);
    } catch (err) {
      setError((err as Error).message);
    }
  }, [apiJSON]);

  useEffect(() => {
    refreshAccounts();
    refreshPlatforms();
  }, [refreshAccounts, refreshPlatforms]);

  useEffect(() => {
    if (selected) refreshPerformance(selected, true);
  }, [refreshPerformance, selected]);

  useAppEvents<AdsEventData>("ads", scopedProject, (event) => {
    if (event.topic === "account.changed" || event.topic === "account.added" || event.topic === "account.removed") {
      refreshAccounts();
      refreshPlatforms();
      return;
    }
    if (!selected || event.data?.ad_account_id !== selected.id) return;
    if (event.topic === "performance.updated") {
      setPerformanceSync({ status: "updated", at: event.data.fetched_at || event.time });
      if (eventRefreshTimer.current) window.clearTimeout(eventRefreshTimer.current);
      eventRefreshTimer.current = window.setTimeout(() => {
        refreshPerformance(selected, false);
        setWorkspaceRefreshKey((current) => current + 1);
      }, 250);
    } else if (event.topic === "performance.sync_failed") {
      setPerformanceSync({ status: "delayed", at: event.data.next_attempt_at, message: event.data.message });
    } else if (event.topic === "entity.changed") {
      refreshCampaigns(selected);
      setWorkspaceRefreshKey((current) => current + 1);
    }
  });

  useEffect(() => {
    const refreshVisibleCache = () => {
      if (document.visibilityState === "visible" && selected) {
        refreshPerformance(selected, false);
        setWorkspaceRefreshKey((current) => current + 1);
      }
    };
    window.addEventListener("focus", refreshVisibleCache);
    document.addEventListener("visibilitychange", refreshVisibleCache);
    return () => {
      window.removeEventListener("focus", refreshVisibleCache);
      document.removeEventListener("visibilitychange", refreshVisibleCache);
      if (eventRefreshTimer.current) window.clearTimeout(eventRefreshTimer.current);
    };
  }, [refreshPerformance, selected]);

  useEffect(() => {
    const pending = Number(new URLSearchParams(window.location.search).get("pending"));
    if (pending > 0) resumeOAuth(pending);
  }, [resumeOAuth]);

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return;
      if (event.data?.type === "ads.oauth_ready" && event.data.pending_account_id) {
        resumeOAuth(Number(event.data.pending_account_id));
      } else if (event.data?.type === "ads.oauth_failed") {
        setError("Authorization failed. Try connecting the account again.");
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [resumeOAuth]);

  useEffect(() => {
    return () => {
      campaignRequest.current++;
      performanceRequest.current++;
      resourceRequest.current++;
      if (copyFeedbackTimer.current !== null) window.clearTimeout(copyFeedbackTimer.current);
    };
  }, []);

  const selectAccount = (account: AdAccount) => {
    setSelectedId(account.id);
    setPerformance(null);
    setComparison(null);
    setPerformanceError(null);
    setSelectedCampaignID(null);
    refreshCampaigns(account);
  };

  const startPlatform = async (platform: PlatformInfo, connectionId?: number) => {
    if (!platform.can_add) {
      setError(platform.unavailable_reason || "Set up this integration before adding an account.");
      return;
    }
    const activeConnections = platform.connections || [];
    if (!connectionId && activeConnections.length > 1) {
      setConnectionPickerMode("account");
      setConnectionPicker(platform);
      setAddOpen(false);
      return;
    }
    if (connectionId) setConnectionPicker(null);
    const reusableConnectionId = connectionId || activeConnections[0]?.id;
    if (reusableConnectionId) {
      setStartingPlatform(platform.platform);
      setError(null);
      try {
        const result = await apiJSON("/accounts/start", {
          method: "POST",
          body: JSON.stringify({
            platform: platform.platform,
            connection_id: reusableConnectionId,
          }),
        });
        if (!result.pending_account_id || !result.reused_connection) {
          throw new Error("The selected integration connection could not be reused.");
        }
        await resumeOAuth(Number(result.pending_account_id));
      } catch (err) {
        setError((err as Error).message);
      } finally {
        setStartingPlatform(null);
      }
      return;
    }

    const popup = window.open("about:blank", "ads_oauth", "width=620,height=760");
    if (!popup) {
      setError("Popup blocked. Allow pop-ups for this site and try again.");
      return;
    }
    setStartingPlatform(platform.platform);
    setError(null);
    try {
      const result = await apiJSON("/accounts/start", {
        method: "POST",
        body: JSON.stringify({ platform: platform.platform }),
      });
      if (result.pending_account_id && result.reused_connection) {
        popup.close();
        await resumeOAuth(Number(result.pending_account_id));
      } else if (result.authorize_url) {
        popup.location.href = result.authorize_url;
        setAddOpen(false);
      } else {
        popup.close();
        throw new Error("The server did not return an authorization URL.");
      }
    } catch (err) {
      popup.close();
      setError((err as Error).message);
    } finally {
      setStartingPlatform(null);
    }
  };

  const copyAccessLink = async (platform: PlatformInfo, connectionId?: number) => {
    const activeConnections = platform.connections || [];
    if (!connectionId && activeConnections.length > 1) {
      setConnectionPickerMode("link");
      setConnectionPicker(platform);
      setAddOpen(false);
      return;
    }
    const templateConnectionId = connectionId || activeConnections[0]?.id;
    if (!templateConnectionId) {
      setError("Set up this integration before creating an access link.");
      return;
    }

    setCopyingPlatform(platform.platform);
    setError(null);
    try {
      const response = await fetch("/api/invites", {
        method: "POST",
        credentials: "same-origin",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          app_slug: platform.integration_slug,
          source: "local",
          project_id: scopedProject,
          template_connection_id: templateConnectionId,
          ttl_seconds: 24 * 60 * 60,
        }),
      });
      if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
      const result = await response.json();
      if (!result.url) throw new Error("The server did not return an access link.");
      await navigator.clipboard.writeText(result.url);
      setConnectionPicker(null);
      setCopiedPlatform(platform.platform);
      if (copyFeedbackTimer.current !== null) window.clearTimeout(copyFeedbackTimer.current);
      copyFeedbackTimer.current = window.setTimeout(() => {
        setCopiedPlatform((current) => current === platform.platform ? null : current);
        copyFeedbackTimer.current = null;
      }, 2200);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setCopyingPlatform(null);
    }
  };

  const finalizeAccount = async (page: PendingAccountPage) => {
    if (!pendingPicker) return;
    try {
      await apiJSON("/accounts/finalize", {
        method: "POST",
        body: JSON.stringify({
          pending_account_id: pendingPicker.pendingId,
          page_id: page.id,
          name: page.name,
        }),
      });
      setPendingPicker(null);
      const cleanURL = new URL(window.location.href);
      cleanURL.searchParams.delete("pending");
      window.history.replaceState({}, "", cleanURL.pathname + cleanURL.search);
      await Promise.all([refreshAccounts(), refreshPlatforms()]);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const toggleCampaign = async (account: AdAccount, campaign: Campaign) => {
    const tool = displayStatus(campaign) === "ACTIVE" ? "campaign_pause" : "campaign_resume";
    try {
      await callTool(tool, { ad_account_id: account.id, campaign_id: campaign.id });
      if (selectedId === account.id) await refreshCampaigns(account);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const disconnectAccount = async () => {
    if (!disconnectTarget) return;
    setDisconnecting(true);
    try {
      await apiJSON(`/accounts/${disconnectTarget.id}`, { method: "DELETE" });
      if (selectedId === disconnectTarget.id) {
        campaignRequest.current++;
        performanceRequest.current++;
        setSelectedId(null);
        setCampaigns([]);
        setPerformance(null);
        setComparison(null);
      }
      setDisconnectTarget(null);
      await Promise.all([refreshAccounts(), refreshPlatforms()]);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setDisconnecting(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-text">
      <header className="flex min-h-16 items-center justify-between gap-4 border-b border-border px-4 py-3 md:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h1 className="text-base font-semibold text-text">Ads</h1>
            <span className="rounded bg-border px-2 py-0.5 text-xs text-text-muted">
              {accounts.length}
            </span>
          </div>
		  <p className="mt-0.5 truncate text-xs text-text-muted">Campaign accounts across Meta, Google, X, and Reddit</p>
        </div>
        <button
          type="button"
          onClick={() => setAddOpen(true)}
          className="inline-flex h-9 items-center gap-2 rounded bg-accent px-3 text-sm font-medium text-bg hover:opacity-90"
        >
          <span aria-hidden="true" className="text-base">+</span>
          <span>Add account</span>
        </button>
      </header>

      {error && (
        <div role="alert" className="mx-4 mt-3 flex items-start gap-3 rounded border border-red/40 bg-red/10 px-3 py-2 text-sm text-red">
          <span className="min-w-0 flex-1">{error}</span>
          <button type="button" onClick={() => setError(null)} aria-label="Dismiss error" title="Dismiss" className="grid h-6 w-6 place-items-center rounded hover:bg-red/10">
            ×
          </button>
        </div>
      )}

      {pendingPicker && (
        <Modal
          title="Choose an ad account"
		  description={`${pendingPicker.pages.length} available from ${providerName(pendingPicker.platform || "")}.`}
          size="large"
          onClose={() => {
            setPendingPicker(null);
            setAccountFilter("");
          }}
          labelledBy="ads-account-picker-title"
        >
          {pendingPicker.pages.length > 5 && (
            <div className="border-b border-border px-4 py-3">
              <input
                type="search"
                value={accountFilter}
                onChange={(event) => setAccountFilter(event.target.value)}
                placeholder="Search accounts"
                aria-label="Search ad accounts"
                className="h-9 w-full rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent"
              />
            </div>
          )}
          <div className="max-h-[60vh] divide-y divide-border overflow-y-auto">
            {pendingPicker.pages.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">No ad accounts were returned by this connection.</p>
            ) : filteredPendingPages.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">No accounts match that search.</p>
            ) : filteredPendingPages.map((page) => (
              <div key={page.id} className="flex items-center gap-3 px-4 py-3">
                <ProviderMark platform={pendingPicker.platform || ""} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{page.name || page.id}</div>
                  <div className="truncate text-xs text-text-muted">
                    {page.id} · {page.currency || "-"} · {page.timezone || "-"}{page.test_account ? " · Test account" : ""}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => finalizeAccount(page)}
                  aria-label={`Select ${page.name || page.id}`}
                  className="h-8 w-20 shrink-0 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input"
                >
                  Select
                </button>
              </div>
            ))}
          </div>
        </Modal>
      )}

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <aside className="flex max-h-56 w-full shrink-0 flex-col border-b border-border md:max-h-none md:w-72 md:border-b-0 md:border-r">
          <div className="flex h-11 items-center justify-between border-b border-border px-3">
            <h2 className="text-xs font-medium uppercase text-text-dim">Accounts</h2>
            <button
              type="button"
              onClick={refreshAccounts}
              disabled={loadingAccounts}
              aria-label="Refresh accounts"
              title="Refresh accounts"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
            >
              ↻
            </button>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">
            {loadingAccounts && accounts.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">Loading accounts...</p>
            ) : accounts.length === 0 ? (
              <div className="px-4 py-8 text-center">
                <p className="text-sm font-medium">No ad accounts</p>
				<p className="mt-1 text-xs text-text-muted">Connect an ads provider to begin.</p>
                <button type="button" onClick={() => setAddOpen(true)} className="mt-4 h-8 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input">
                  Add account
                </button>
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {accounts.map((account) => (
                  <li key={account.id} className={selectedId === account.id ? "bg-accent/10" : ""}>
                    <div className="flex items-center gap-2 px-2 py-2">
                      <button type="button" onClick={() => selectAccount(account)} className="flex min-w-0 flex-1 items-center gap-3 rounded p-1 text-left hover:bg-bg-input/60">
                        <ProviderMark platform={account.platform} size="sm" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium">{account.display_name}</span>
                          <span className="block truncate text-xs text-text-muted">{account.native_account_id} · {account.currency || "-"}</span>
                        </span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setDisconnectTarget(account)}
                        aria-label={`Remove ${account.display_name}`}
                        title="Remove account"
                        className="grid h-8 w-8 shrink-0 place-items-center rounded text-text-muted hover:bg-red/10 hover:text-red"
                      >
                        ×
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {!selected ? (
            <div className="grid min-h-64 flex-1 place-items-center p-6 text-center">
              <div>
                <div className="mx-auto grid h-12 w-12 place-items-center rounded border border-border bg-bg-card text-xl text-text-muted">▥</div>
                <h2 className="mt-4 text-sm font-medium">Select an account</h2>
                <p className="mt-1 text-sm text-text-muted">Campaigns for the selected provider appear here.</p>
              </div>
            </div>
          ) : (
            <>
              <header className="flex min-h-16 items-center justify-between gap-3 border-b border-border px-4 py-3">
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderMark platform={selected.platform} size="sm" />
                  <div className="min-w-0">
                    <h2 className="truncate text-sm font-semibold">{selected.display_name}</h2>
					<p className="truncate text-xs text-text-muted">{providerName(selected.platform)} · {selected.native_account_id} · {selected.currency || "-"}</p>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <button
                    type="button"
                    onClick={() => openAccountResources(selected)}
                    className="h-9 rounded border border-border px-3 text-xs font-medium text-text-muted hover:bg-bg-input hover:text-text"
                  >
                    Resources
                  </button>
                  <button
                    type="button"
                    onClick={() => Promise.all([refreshCampaigns(selected), refreshPerformance(selected, true)])}
                    disabled={loadingCampaigns || loadingPerformance}
                    aria-label="Refresh account data"
                    title="Refresh account data"
                    className="grid h-9 w-9 place-items-center rounded border border-border text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
                  >
                    ↻
                  </button>
                  <span
                    className="inline-flex h-9 items-center gap-2 whitespace-nowrap px-1 text-xs text-text-muted"
                    title={performanceSync.message || syncStateLabel(performanceSync)}
                  >
                    <span className={`h-2 w-2 rounded-full ${performanceSync.status === "delayed" ? "bg-yellow" : performanceSync.status === "updating" ? "bg-accent" : "bg-green"}`} />
                    {syncStateLabel(performanceSync)}
                  </span>
                </div>
              </header>
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2">
                <div className="inline-flex h-8 rounded border border-border bg-bg-input p-0.5">
                  {(["overview", "campaigns"] as const).map((view) => (
                    <button
                      key={view}
                      type="button"
                      onClick={() => setActiveView(view)}
                      className={`min-w-24 rounded px-3 text-xs font-medium capitalize ${activeView === view ? "bg-bg-card text-text shadow-sm" : "text-text-muted hover:text-text"}`}
                    >
                      {view}
                    </button>
                  ))}
                </div>
                <div className="grid w-full grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 sm:flex sm:w-auto sm:flex-wrap sm:justify-end">
                  <div className="col-span-3 inline-flex h-8 justify-self-end rounded border border-border p-0.5 sm:col-auto sm:justify-self-auto">
                    {[7, 30, 90].map((days) => (
                      <button
                        key={days}
                        type="button"
                        onClick={() => {
                          const range = dateRange(days);
                          setDateFrom(range.from);
                          setDateTo(range.to);
                        }}
                        className="w-10 rounded text-xs text-text-muted hover:bg-bg-input hover:text-text"
                      >
                        {days}d
                      </button>
                    ))}
                  </div>
                  <input type="date" value={dateFrom} max={dateTo} onChange={(event) => setDateFrom(event.target.value)} aria-label="Report start date" className="h-8 min-w-0 rounded border border-border bg-bg-input px-2 text-xs text-text outline-none focus:border-accent" />
                  <span className="text-xs text-text-dim">to</span>
                  <input type="date" value={dateTo} min={dateFrom} max={isoDate(new Date())} onChange={(event) => setDateTo(event.target.value)} aria-label="Report end date" className="h-8 min-w-0 rounded border border-border bg-bg-input px-2 text-xs text-text outline-none focus:border-accent" />
                  <label className="col-span-3 flex h-8 items-center justify-self-end gap-2 rounded border border-border px-2.5 text-xs text-text-muted sm:col-auto sm:justify-self-auto">
                    <input type="checkbox" checked={compareEnabled} onChange={(event) => setCompareEnabled(event.target.checked)} className="h-3.5 w-3.5 accent-accent" />
                    Compare
                  </label>
                </div>
              </div>
              {performanceError && (
                <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-2 text-sm text-red">{performanceError}</div>
              )}
              <div className="min-h-0 flex-1 overflow-auto">
                {activeView === "overview" ? (
                  loadingPerformance && !performance ? (
                    <p className="p-4 text-sm text-text-muted">Loading performance...</p>
                  ) : performance ? (
                    <div className={loadingPerformance ? "opacity-70" : ""}>
                      <KpiStrip summary={performance.summary} previous={comparison?.summary} currency={selected.currency} />
                      <section className="border-b border-border px-4 py-4">
                        <div className="mb-2 flex flex-wrap items-center justify-between gap-3">
                          <div>
                            <h3 className="text-xs font-medium uppercase text-text-dim">Daily delivery</h3>
                            <p className="mt-0.5 text-xs text-text-muted">Spend and clicks in {selected.timezone || "the account timezone"}</p>
                          </div>
                          <div className="flex items-center gap-3 text-xs text-text-muted">
                            <span><span className="mr-1 inline-block h-2 w-2 bg-accent" />Spend</span>
                            <span><span className="mr-1 inline-block h-2 w-2 bg-blue" />Clicks</span>
                            {performance.freshness?.fetched_at && <span>Updated {new Date(performance.freshness.fetched_at).toLocaleString()}</span>}
                          </div>
                        </div>
                        <PerformanceChart points={performance.data} currency={selected.currency} />
                      </section>
                      <section>
                        <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-2">
                          <h3 className="text-xs font-medium uppercase text-text-dim">Top campaigns</h3>
                          <button type="button" onClick={() => setActiveView("campaigns")} className="text-xs font-medium text-accent hover:underline">View all</button>
                        </div>
                        {rankedCampaigns.length === 0 ? (
                          <p className="px-4 py-8 text-center text-sm text-text-muted">No campaign delivery in this range.</p>
                        ) : (
                          <table className="w-full table-fixed text-sm" style={{ minWidth: "52rem" }}>
                            <thead className="bg-bg-input text-xs text-text-dim">
                              <tr>
                                <th className="w-2/5 px-4 py-2 text-left font-medium">Campaign</th>
                                <th className="px-3 py-2 text-right font-medium">Spend</th>
                                <th className="px-3 py-2 text-right font-medium">Clicks</th>
                                <th className="px-3 py-2 text-right font-medium">CPC</th>
                                <th className="px-3 py-2 text-right font-medium">Conversions</th>
                                <th className="px-4 py-2 text-right font-medium">CPA</th>
                              </tr>
                            </thead>
                            <tbody className="divide-y divide-border">
                              {rankedCampaigns.slice(0, 8).map((item) => (
                                <tr key={item.campaign_id} className="hover:bg-bg-input/40">
                                  <td className="px-4 py-2.5">
                                    <button type="button" onClick={() => setSelectedCampaignID(item.campaign_id)} className="block max-w-full truncate text-left font-medium text-text hover:text-accent">{item.campaign_name}</button>
                                  </td>
                                  <td className="px-3 py-2.5 text-right tabular-nums">{formatMoneyMicros(item.spend_micros, selected.currency)}</td>
                                  <td className="px-3 py-2.5 text-right tabular-nums">{formatNumber(item.clicks)}</td>
                                  <td className="px-3 py-2.5 text-right tabular-nums">{formatMoneyMicros(item.cpc_micros, selected.currency)}</td>
                                  <td className="px-3 py-2.5 text-right tabular-nums">{formatNumber(item.conversions, 2)}</td>
                                  <td className="px-4 py-2.5 text-right tabular-nums">{formatMoneyMicros(item.cpa_micros, selected.currency)}</td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        )}
                      </section>
                    </div>
                  ) : (
                    <div className="grid min-h-64 place-items-center p-6 text-center">
                      <div><h3 className="text-sm font-medium">Performance unavailable</h3><p className="mt-1 text-sm text-text-muted">Refresh the account to load provider analytics.</p></div>
                    </div>
                  )
                ) : loadingCampaigns ? (
                  <p className="p-4 text-sm text-text-muted">Loading campaigns...</p>
                ) : campaigns.length === 0 ? (
                  <div className="grid min-h-64 place-items-center p-6 text-center">
                    <div><h3 className="text-sm font-medium">No campaigns found</h3><p className="mt-1 text-sm text-text-muted">This account has no campaigns to display.</p></div>
                  </div>
                ) : (
				  <table className="w-full table-fixed text-sm" style={{ minWidth: "69rem" }}>
                    <thead className="sticky top-0 z-10 bg-bg-input text-xs text-text-dim">
                      <tr>
						<th className="w-56 px-4 py-2 text-left font-medium">Campaign</th>
						<th className="w-24 px-3 py-2 text-left font-medium">Status</th>
						<th className="w-24 px-3 py-2 text-right font-medium">Spend</th>
						<th className="w-24 px-3 py-2 text-right font-medium">Impressions</th>
						<th className="w-20 px-3 py-2 text-right font-medium">Clicks</th>
						<th className="w-16 px-3 py-2 text-right font-medium">CTR</th>
						<th className="w-20 px-3 py-2 text-right font-medium">CPC</th>
						<th className="w-24 px-3 py-2 text-right font-medium">Conversions</th>
						<th className="w-20 px-3 py-2 text-right font-medium">CPA</th>
						<th className="w-28 px-3 py-2 text-right font-medium">Daily budget</th>
						<th className="w-20 px-3 py-2 text-right font-medium">Action</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {campaigns.map((campaign) => {
                        const status = displayStatus(campaign);
                        const metrics = campaignPerformance.get(campaign.id);
                        return (
                          <tr key={campaign.id} className="hover:bg-bg-input/40">
                            <td className="px-4 py-3">
                              <button type="button" onClick={() => setSelectedCampaignID(campaign.id)} className="block max-w-full truncate text-left font-medium text-text hover:text-accent" title={campaign.name}>{campaign.name || campaign.id}</button>
                              <div className="truncate text-xs text-text-dim">{campaign.objective || campaign.id}</div>
                            </td>
                            <td className="px-3 py-3"><span className={`rounded px-2 py-1 text-xs font-medium ${statusStyle(status)}`}>{status}</span></td>
                            <td className="px-3 py-3 text-right tabular-nums">{metrics ? formatMoneyMicros(metrics.spend_micros, selected.currency) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? formatNumber(metrics.impressions) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? formatNumber(metrics.clicks) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? `${formatNumber(metrics.ctr, 2)}%` : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? formatMoneyMicros(metrics.cpc_micros, selected.currency) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? formatNumber(metrics.conversions, 2) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{metrics ? formatMoneyMicros(metrics.cpa_micros, selected.currency) : "-"}</td>
                            <td className="px-3 py-3 text-right tabular-nums text-text-muted">{formatBudget(campaign.daily_budget, selected.currency)}</td>
                            <td className="px-3 py-3 text-right"><button type="button" onClick={() => toggleCampaign(selected, campaign)} className="h-8 rounded border border-border px-3 text-xs font-medium hover:bg-bg-input">{status === "ACTIVE" ? "Pause" : "Resume"}</button></td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                )}
              </div>
            </>
          )}
        </main>
      </div>

      {addOpen && (
        <Modal
          title="Add ad account"
          description="Choose a provider to connect an ad account."
          actions={(
            <button
              type="button"
              onClick={refreshPlatforms}
              aria-label="Refresh providers"
              title="Refresh providers"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text"
            >
              ↻
            </button>
          )}
          size="large"
          onClose={() => setAddOpen(false)}
          labelledBy="ads-add-title"
        >
          <div className="divide-y divide-border">
            {platforms.length === 0 ? (
              <p className="px-4 py-8 text-center text-sm text-text-muted">Checking providers...</p>
            ) : platforms.map((platform) => (
              <div key={platform.platform} className="flex items-center gap-3 px-4 py-3">
                <ProviderMark platform={platform.platform} />
                <div className="min-w-0 flex-1">
                  <span className="block text-sm font-medium">
					{providerName(platform.platform)}
                  </span>
                  <span className="mt-0.5 block text-xs text-text-muted">
                    {platform.state === "connected"
                      ? `${platform.connection_count} active connection${platform.connection_count === 1 ? "" : "s"}`
                      : platform.state === "ready"
                        ? "Ready to connect"
                        : platform.state === "setup_required"
						  ? `${providerInfo(platform.platform).integration} integration required`
                          : platform.unavailable_reason || "Integration unavailable"}
                  </span>
                </div>
                {platform.state === "setup_required" ? (
                  <a
                    href={platform.setup_url || "/integrations"}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex h-8 w-28 shrink-0 items-center justify-center whitespace-nowrap rounded border border-accent px-3 text-xs font-medium text-accent hover:bg-accent/10"
                  >
                    Set up
                  </a>
                ) : (
                  <div className="flex shrink-0 items-center gap-2">
                    {platform.configured && (
                      <button
                        type="button"
                        disabled={copyingPlatform === platform.platform}
                        onClick={() => copyAccessLink(platform)}
                        className="h-8 w-20 whitespace-nowrap rounded border border-border px-2 text-xs font-medium text-text-muted hover:bg-bg-input hover:text-text disabled:cursor-wait disabled:opacity-50"
                      >
                        {copyingPlatform === platform.platform
                          ? "Copying..."
                          : copiedPlatform === platform.platform
                            ? "Copied"
                            : "Copy link"}
                      </button>
                    )}
                    <button
                      type="button"
                      disabled={!platform.can_add || startingPlatform === platform.platform}
                      onClick={() => startPlatform(platform)}
                      className="h-8 w-24 whitespace-nowrap rounded border border-accent px-3 text-xs font-medium text-accent hover:bg-accent/10 disabled:cursor-not-allowed disabled:border-border disabled:text-text-muted disabled:opacity-50"
                    >
                      {startingPlatform === platform.platform
                        ? "Starting..."
                        : platform.state === "connected"
                          ? "Choose"
                          : "Connect"}
                    </button>
                  </div>
                )}
              </div>
            ))}
          </div>
        </Modal>
      )}

      {connectionPicker && (
        <Modal
          title={connectionPickerMode === "link"
            ? `${connectionPicker.display_name} access link`
            : `Choose ${connectionPicker.display_name} connection`}
          description={connectionPickerMode === "link"
            ? "Select the connection to use for this access link."
            : "Select the dashboard integration connection whose ad accounts you want to use."}
          size="large"
          onClose={() => setConnectionPicker(null)}
          labelledBy="ads-connection-picker-title"
        >
          <div className="divide-y divide-border">
            {(connectionPicker.connections || []).map((connection) => (
              <button
                key={connection.id}
                type="button"
                disabled={
                  connectionPickerMode === "link"
                    ? copyingPlatform === connectionPicker.platform
                    : startingPlatform === connectionPicker.platform
                }
                onClick={() => connectionPickerMode === "link"
                  ? copyAccessLink(connectionPicker, connection.id)
                  : startPlatform(connectionPicker, connection.id)}
                className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-bg-input disabled:cursor-wait disabled:opacity-50"
              >
                <ProviderMark platform={connectionPicker.platform} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-text">{connection.name || connectionPicker.display_name}</div>
                  <div className="text-xs text-text-muted">Connection #{connection.id}</div>
                </div>
                <span className="text-xs font-medium text-accent">
                  {connectionPickerMode === "link"
                    ? copyingPlatform === connectionPicker.platform ? "Copying..." : "Copy link"
                    : startingPlatform === connectionPicker.platform ? "Loading..." : "Choose"}
                </span>
              </button>
            ))}
          </div>
        </Modal>
      )}

      {setupAccount && !leadFormOpen && (
        <Modal
          title="Account resources"
		  description={`${setupAccount.display_name} · ${providerName(setupAccount.platform)}`}
          actions={(
            <button
              type="button"
              onClick={() => loadAccountContext(setupAccount, true)}
              disabled={loadingResources}
              aria-label="Refresh account resources"
              title="Refresh account resources"
              className="grid h-8 w-8 place-items-center rounded text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"
            >
              ↻
            </button>
          )}
          size="large"
          onClose={closeAccountResources}
          labelledBy="ads-resources-title"
        >
          <div className="max-h-[70vh] overflow-y-auto">
            {resourceError && (
              <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-3 text-sm text-red">
                {resourceError}
              </div>
            )}
            {loadingResources && !accountContext ? (
              <p className="px-4 py-10 text-center text-sm text-text-muted">Discovering account resources...</p>
            ) : groupedResources.length === 0 ? (
              <p className="px-4 py-10 text-center text-sm text-text-muted">No provider resources are available for this account.</p>
            ) : (
              <div className="divide-y divide-border">
                {groupedResources.map((group) => (
                  <section key={group.kind}>
                    <div className="flex items-center justify-between gap-3 bg-bg-input px-4 py-2">
                      <h3 className="text-xs font-medium uppercase text-text-dim">
                        {RESOURCE_KIND_LABELS[group.kind] || group.kind}
                      </h3>
                      <div className="flex items-center gap-3">
                        <span className="text-xs tabular-nums text-text-dim">{group.resources.length}</span>
						{group.kind === "lead_form" && (setupAccount.platform === "meta" || setupAccount.platform === "google") && (
                          <button
                            type="button"
                            onClick={openLeadFormCreate}
                            className="h-7 rounded border border-border px-2.5 text-xs font-medium text-text hover:bg-bg-card"
                          >
                            Create
                          </button>
                        )}
                      </div>
                    </div>
                    {group.error ? (
                      <p className="px-4 py-3 text-xs text-red">{group.error}</p>
                    ) : group.resources.length === 0 ? (
                      <p className="px-4 py-3 text-xs text-text-muted">None available from this provider connection.</p>
                    ) : (
                      <div className="divide-y divide-border">
                        {group.resources.map((resource) => {
                          const purpose = resourcePurpose(resource);
                          const isDefault = purpose ? accountContext?.defaults?.[purpose]?.id === resource.id : false;
                          const isActive = resource.status === "active";
                          return (
                            <div key={resource.id} className="flex items-center gap-3 px-4 py-3">
                              <div className="min-w-0 flex-1">
                                <div className="truncate text-sm font-medium text-text">{resource.name || RESOURCE_TYPE_LABELS[resource.provider_type] || "Unnamed resource"}</div>
                                <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-text-muted">
                                  <span>{RESOURCE_TYPE_LABELS[resource.provider_type] || resource.provider_type}</span>
                                  {!isActive && <span className="text-yellow">{resource.status}</span>}
                                </div>
                              </div>
                              {purpose && (
                                <button
                                  type="button"
                                  onClick={() => setResourceDefault(resource)}
                                  disabled={!isActive || isDefault || savingResourceId !== null}
                                  className={`h-8 w-24 shrink-0 rounded border px-3 text-xs font-medium disabled:cursor-default ${isDefault ? "border-green/40 bg-green/10 text-green" : "border-border text-text-muted hover:bg-bg-input hover:text-text disabled:opacity-50"}`}
                                >
                                  {isDefault ? "Default" : savingResourceId === resource.id ? "Saving..." : "Use"}
                                </button>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </section>
                ))}
              </div>
            )}
          </div>
        </Modal>
      )}

      {setupAccount && leadFormOpen && (
        <Modal
          title="Create lead form"
		  description={`${setupAccount.display_name} · ${providerName(setupAccount.platform)}`}
          size="large"
          onClose={() => !creatingLeadForm && setLeadFormOpen(false)}
          labelledBy="ads-lead-form-title"
        >
          <form onSubmit={(event) => { event.preventDefault(); createLeadForm(); }}>
            {resourceError && (
              <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-3 text-sm text-red">{resourceError}</div>
            )}
            <div className="grid gap-4 px-4 py-4 md:grid-cols-2">
              <label className="grid gap-1.5 text-xs font-medium text-text-muted">
                Form name
                <input required value={leadFormName} onChange={(event) => setLeadFormName(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              {setupAccount.platform === "google" && (
                <label className="grid gap-1.5 text-xs font-medium text-text-muted">
                  Business name
                  <input required value={leadFormBusinessName} onChange={(event) => setLeadFormBusinessName(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
                </label>
              )}
              <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                Privacy policy URL
                <input required type="url" value={leadFormPrivacyURL} onChange={(event) => setLeadFormPrivacyURL(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                Follow-up URL
                <input type="url" value={leadFormFollowUpURL} onChange={(event) => setLeadFormFollowUpURL(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent" />
              </label>
              {setupAccount.platform === "google" && campaigns.length > 0 && selectedId === setupAccount.id && (
                <label className="grid gap-1.5 text-xs font-medium text-text-muted md:col-span-2">
                  Campaign
                  <select value={leadFormCampaignID} onChange={(event) => setLeadFormCampaignID(event.target.value)} className="h-9 rounded border border-border bg-bg-input px-3 text-sm text-text outline-none focus:border-accent">
                    <option value="">Not attached</option>
                    {campaigns.map((campaign) => <option key={campaign.id} value={campaign.id}>{campaign.name || campaign.id}</option>)}
                  </select>
                </label>
              )}
              <fieldset className="grid gap-2 md:col-span-2">
                <legend className="mb-1 text-xs font-medium text-text-muted">Questions</legend>
                <div className="grid gap-2 sm:grid-cols-2 md:grid-cols-3">
                  {LEAD_QUESTION_OPTIONS.map((question) => (
                    <label key={question.type} className="flex h-9 items-center gap-2 rounded border border-border bg-bg-input px-3 text-sm text-text">
                      <input type="checkbox" checked={leadFormQuestions.includes(question.type)} onChange={() => toggleLeadQuestion(question.type)} className="h-4 w-4 accent-accent" />
                      {question.label}
                    </label>
                  ))}
                </div>
              </fieldset>
              <label className="flex h-9 items-center gap-2 text-sm text-text md:col-span-2">
                <input type="checkbox" checked={leadFormHigherIntent} onChange={(event) => setLeadFormHigherIntent(event.target.checked)} className="h-4 w-4 accent-accent" />
                Higher intent
              </label>
            </div>
            <footer className="flex justify-end gap-2 border-t border-border px-4 py-3">
              <button type="button" disabled={creatingLeadForm} onClick={() => setLeadFormOpen(false)} className="h-9 rounded border border-border px-3 text-sm text-text hover:bg-bg-input disabled:opacity-50">Cancel</button>
              <button type="submit" disabled={creatingLeadForm || !leadFormName.trim() || !leadFormPrivacyURL.trim() || leadFormQuestions.length === 0 || (setupAccount.platform === "google" && !leadFormBusinessName.trim())} className="h-9 rounded bg-accent px-3 text-sm font-medium text-black hover:opacity-90 disabled:opacity-50">
                {creatingLeadForm ? "Creating..." : "Create form"}
              </button>
            </footer>
          </form>
        </Modal>
      )}

      {selectedCampaign && selected && (
        <CampaignWorkspace
          account={selected}
          campaign={selectedCampaign}
          initialPerformance={selectedCampaignPerformance}
          dateFrom={dateFrom}
          dateTo={dateTo}
          callTool={callTool}
          eventRefreshKey={workspaceRefreshKey}
          onCampaignChanged={(campaignID, status) => setCampaigns((current) => current.map((campaign) => campaign.id === campaignID ? { ...campaign, status } : campaign))}
          onClose={() => setSelectedCampaignID(null)}
        />
      )}

      {disconnectTarget && (
        <Modal title="Remove ad account?" description={disconnectTarget.display_name} onClose={() => !disconnecting && setDisconnectTarget(null)} labelledBy="ads-remove-title">
          <div className="px-4 py-4 text-sm text-text-muted">
            The account is removed from this project. The shared provider connection remains available to other apps.
          </div>
          <footer className="flex justify-end gap-2 border-t border-border px-4 py-3">
            <button type="button" disabled={disconnecting} onClick={() => setDisconnectTarget(null)} className="h-9 rounded border border-border px-3 text-sm hover:bg-bg-input disabled:opacity-50">Cancel</button>
            <button type="button" disabled={disconnecting} onClick={disconnectAccount} className="h-9 rounded bg-red px-3 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50">
              {disconnecting ? "Removing..." : "Remove"}
            </button>
          </footer>
        </Modal>
      )}
    </div>
  );
}
