import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface SEOLocation {
  id: number;
  provider: string;
  search_engine: string;
  location_code?: number;
  location_name: string;
  country_iso?: string;
  language_code: string;
  language_name?: string;
  synced_at?: number;
}

interface Domain {
  id: number;
  project_id: string;
  host: string;
  label?: string;
  default_location_id?: number;
  created_at: string;
}

interface Keyword {
  id: number;
  project_id: string;
  search_engine?: string;
  text: string;
  location_id: number;
  country_iso: string;
  language_iso: string;
  created_at: string;
}

interface DomainMetrics {
  id: number;
  location_id: number;
  provider: string;
  ts: number;
  country_iso?: string;
  organic_traffic?: number;
  organic_keywords?: number;
  paid_traffic?: number;
  paid_keywords?: number;
  backlinks_count?: number;
  referring_domains_count?: number;
}

interface Backlink {
  id: number;
  provider: string;
  source_url: string;
  dest_url: string;
  anchor: string;
  is_dofollow?: number;
  is_nofollow?: number;
  is_ugc?: number;
  is_sponsored?: number;
  source_authority?: number;
  first_seen?: number;
  last_seen?: number;
  is_lost: number;
}

type BacklinkStatus = "all" | "active" | "lost";
type BacklinkFollowFilter = "all" | "follow" | "nofollow";

interface BacklinkBrowseArgs {
  status: BacklinkStatus;
  query?: string;
  dofollow?: boolean;
  limit: number;
  offset: number;
}

interface BacklinkPage {
  rows: Backlink[];
  total: number;
  limit: number;
  offset: number;
  status: BacklinkStatus;
  provider: string;
  query?: string;
}

interface BacklinkMovementPoint {
  date: string;
  gained: number;
  lost: number;
}

interface BacklinkMovement {
  domain_id: number;
  provider: string;
  days: number;
  from_date: string;
  to_date: string;
  active_links: number;
  lost_links: number;
  gained_in_range: number;
  lost_in_range: number;
  net_change: number;
  known_first_seen: number;
  known_lost_date: number;
  points: BacklinkMovementPoint[];
  data_basis: string;
}

interface KeywordMetrics {
  id: number;
  location_id: number;
  provider: string;
  ts: number;
  volume?: number;
  difficulty?: number;
  cpc_usd?: number;
}

interface KeywordMetricJob {
  id: number;
  project_id: string;
  provider: string;
  search_engine: string;
  location_id: number;
  status: "pending" | "running" | "partial" | "failed" | "completed";
  phase: string;
  total_keywords: number;
  completed_keywords: number;
  incomplete_keywords: number;
  volume_completed: number;
  difficulty_completed: number;
  last_error?: string;
  created_at: number;
  updated_at: number;
}

interface Ranking {
  id: number;
  domain_id: number;
  keyword_id: number;
  location_id: number;
  provider: string;
  ts: number;
  rank?: number;
  rank_url?: string;
  device: string;
  serp_features_json: string;
}

interface SearchEntity {
  id: number;
  project_id: string;
  search_engine: string;
  entity_type: string;
  identifier: string;
  label?: string;
  url?: string;
  default_location_id?: number;
  created_at: string;
  updated_at: string;
}

interface SearchRanking {
  id: number;
  snapshot_id: number;
  entity_id?: number;
  search_engine: string;
  keyword_id?: number;
  keyword_text: string;
  location_id?: number;
  provider: string;
  ts: number;
  rank?: number;
  result_type?: string;
  title?: string;
  url?: string;
  identifier?: string;
  channel_identifier?: string;
  channel_title?: string;
  snippet?: string;
  published_at?: string;
}

interface RankTracker {
  id: number;
  keyword_id: number;
  entity_id: number;
  entity_type: string;
  entity_identifier: string;
  entity_label?: string;
  provider: string;
  device: string;
  frequency: RefreshFrequency;
  enabled: boolean;
  daily_depth: number;
  weekly_depth: number;
  next_run_at: number;
  last_success_at?: number;
  last_error?: string;
}

interface RankObservation {
  id: number;
  tracker_id: number;
  observed_date: string;
  ts: number;
  found: boolean;
  rank?: number;
  rank_url?: string;
  checked_depth: number;
  provider: string;
}

interface RankTrackingSettings {
  enabled: boolean;
  monthly_budget_usd: number;
  daily_depth: number;
  weekly_depth: number;
}

interface KeywordIdea {
  keyword: string;
  source_keyword?: string;
  opportunity_score?: number;
  reason?: string;
  example_titles?: string[];
  top_channels?: string[];
}

interface ContentOpportunity {
  search_engine: string;
  keyword: string;
  opportunity_score?: number;
  result_count?: number;
  top10_count?: number;
  latest_ts?: number;
  example_titles?: string[];
  reason?: string;
  volume?: number | null;
  difficulty?: number | null;
}

interface PageRankingSummary {
  url: string;
  keywordIds: number[];
  bestRank?: number;
  latestTs?: number;
  rows: Ranking[];
}

type SearchEngine = "google" | "youtube";
type SEOProvider = "dataforseo" | "yepapi";
type RefreshFrequency = "daily" | "weekly" | "monthly";
type View = "overview" | "domains" | "keywords" | "explorer" | "settings";
type ExplorerMode = "research" | "entities";

interface ProviderStatus {
  default: string;
  providers: string[];
}

const API = "/api/apps/seo";
const engines: { id: SearchEngine; label: string }[] = [
  { id: "google", label: "Google" },
  { id: "youtube", label: "YouTube" },
];
const navigation: { id: View; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "domains", label: "Domains" },
  { id: "keywords", label: "Keywords" },
  { id: "explorer", label: "Explorer" },
  { id: "settings", label: "Settings" },
];
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";
const buttonCls = "px-3 py-1.5 rounded border border-border hover:bg-surface-2 disabled:opacity-50";
const primaryBtn = "px-3 py-1.5 rounded bg-accent text-bg hover:opacity-90 disabled:opacity-50";

function fmt(n?: number | null): string {
  if (n === null || n === undefined) return "-";
  return n.toLocaleString();
}

function date(ts?: number): string {
  if (!ts) return "-";
  return new Date(ts * 1000).toLocaleString();
}

function localeLabel(l?: SEOLocation): string {
  if (!l) return "No locale";
  const code = l.country_iso ? `${l.country_iso}/${l.language_code}` : l.language_code;
  return `${l.location_name} - ${code} - ${l.search_engine}`;
}

function cleanURL(raw?: string): string {
  const value = (raw || "").trim();
  if (!value) return "";
  try {
    const url = new URL(value);
    url.hash = "";
    return url.toString().replace(/\/$/, "");
  } catch {
    return value.replace(/\/$/, "");
  }
}

function pagePath(raw?: string): string {
  const value = cleanURL(raw);
  if (!value) return "-";
  try {
    const url = new URL(value);
    return `${url.pathname || "/"}${url.search || ""}`;
  } catch {
    return value;
  }
}

function hostFromURL(raw?: string): string {
  try {
    return new URL(raw || "").host;
  } catch {
    return "";
  }
}

function pageSummaries(rankings: Ranking[]): PageRankingSummary[] {
  const map = new Map<string, PageRankingSummary>();
  for (const row of rankings) {
    const url = cleanURL(row.rank_url);
    if (!url) continue;
    const existing = map.get(url) || { url, keywordIds: [], rows: [] };
    existing.rows.push(row);
    if (!existing.keywordIds.includes(row.keyword_id)) existing.keywordIds.push(row.keyword_id);
    if (row.rank !== undefined && row.rank !== null) {
      existing.bestRank = existing.bestRank === undefined ? row.rank : Math.min(existing.bestRank, row.rank);
    }
    existing.latestTs = existing.latestTs === undefined ? row.ts : Math.max(existing.latestTs, row.ts);
    map.set(url, existing);
  }
  return Array.from(map.values()).sort((a, b) => (a.bestRank || 9999) - (b.bestRank || 9999));
}

export default function SeoPanel({ projectId, installId }: NativePanelProps) {
  const [searchEngine, setSearchEngine] = useState<SearchEngine>("google");
  const [provider, setProvider] = useState<SEOProvider | "">("");
  const [providers, setProviders] = useState<string[]>([]);
  const [view, setView] = useState<View>("overview");
  const [explorerMode, setExplorerMode] = useState<ExplorerMode>("research");
  const [locations, setLocations] = useState<SEOLocation[]>([]);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [keywords, setKeywords] = useState<Keyword[]>([]);
  const [entities, setEntities] = useState<SearchEntity[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<Domain | null>(null);
  const [selectedKeyword, setSelectedKeyword] = useState<Keyword | null>(null);
  const [selectedEntity, setSelectedEntity] = useState<SearchEntity | null>(null);
  const [domainMetrics, setDomainMetrics] = useState<DomainMetrics | null>(null);
  const [backlinkMovement, setBacklinkMovement] = useState<BacklinkMovement | null>(null);
  const [activeBacklinks, setActiveBacklinks] = useState<Backlink[]>([]);
  const [lostBacklinks, setLostBacklinks] = useState<Backlink[]>([]);
  const [keywordMetrics, setKeywordMetrics] = useState<KeywordMetrics | null>(null);
  const [domainRankings, setDomainRankings] = useState<Ranking[]>([]);
  const [entityRankings, setEntityRankings] = useState<SearchRanking[]>([]);
  const [selectedRankURL, setSelectedRankURL] = useState<string | null>(null);
  const [pageSerpRows, setPageSerpRows] = useState<SearchRanking[]>([]);
  const [serpResults, setSerpResults] = useState<SearchRanking[]>([]);
  const [keywordIdeas, setKeywordIdeas] = useState<KeywordIdea[]>([]);
  const [opportunities, setOpportunities] = useState<ContentOpportunity[]>([]);
  const [metricJobs, setMetricJobs] = useState<KeywordMetricJob[]>([]);
  const [rankTrackers, setRankTrackers] = useState<RankTracker[]>([]);
  const [rankHistory, setRankHistory] = useState<Record<number, RankObservation[]>>({});
  const [trackingSettings, setTrackingSettings] = useState<RankTrackingSettings>({
    enabled: true,
    monthly_budget_usd: 5,
    daily_depth: 20,
    weekly_depth: 100,
  });
  const [activity, setActivity] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [err, setErr] = useState("");

  const withParams = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(),
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, params: Record<string, string> = {}, body?: unknown, signal?: AbortSignal): Promise<T> => {
      const opts: RequestInit = { method, signal, credentials: "same-origin", headers: {} };
      if (body !== undefined) {
        (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
        opts.body = JSON.stringify(body);
      }
      const res = await fetch(`${API}${path}?${withParams(params)}`, opts);
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      const data = await res.json();
      signal?.throwIfAborted();
      return data;
    },
    [withParams],
  );

  const callTool = useCallback(
    async <T,>(tool: string, args: Record<string, unknown> = {}, signal?: AbortSignal): Promise<T> => {
      return api<T>("POST", "/tools/call", {}, { tool, args: { ...args, _project_id: projectId } }, signal);
    },
    [api, projectId],
  );

  const reloadProviders = useCallback(async (signal?: AbortSignal) => {
    const resp = await api<ProviderStatus>("GET", "/providers", {}, undefined, signal);
    const available = (resp.providers || []).filter((value) => value === "dataforseo" || value === "yepapi");
    setProviders(available);
    setProvider((current) => {
      if (current && available.includes(current)) return current;
      if (resp.default === "dataforseo" || resp.default === "yepapi") return resp.default;
      return (available[0] as SEOProvider | undefined) || "";
    });
  }, [api]);

  const reloadLocations = useCallback(async (signal?: AbortSignal) => {
    const resp = await api<{ locations: SEOLocation[] }>("GET", "/locations", { provider: "all", limit: "500" }, undefined, signal);
    setLocations(resp.locations || []);
  }, [api]);

  const reloadDomains = useCallback(async (signal?: AbortSignal) => {
    const rows = await callTool<Domain[]>("domains_list", {}, signal);
    setDomains(rows || []);
    setSelectedDomain((current) => {
      const updated = rows?.find((domain) => domain.id === current?.id);
      if (updated) return updated;
      return rows?.find((domain) => !isCompetitorDomain(domain)) || rows?.[0] || null;
    });
  }, [callTool]);

  const reloadBacklinkInsights = useCallback(async (domainId: number, signal?: AbortSignal) => {
    const providerArgs = provider ? { provider } : {};
    const [movement, active, lost] = await Promise.all([
      callTool<BacklinkMovement>("backlink_movement", { domain_id: domainId, ...providerArgs, days: 90 }, signal),
      callTool<Backlink[]>("backlinks_list", { domain_id: domainId, ...providerArgs, lost: false, limit: 20 }, signal),
      callTool<Backlink[]>("backlinks_list", { domain_id: domainId, ...providerArgs, lost: true, limit: 20 }, signal),
    ]);
    setBacklinkMovement(movement);
    setActiveBacklinks(active || []);
    setLostBacklinks(lost || []);
  }, [callTool, provider]);

  const browseBacklinks = useCallback(async (domainId: number, args: BacklinkBrowseArgs) => {
    return callTool<BacklinkPage>("backlinks_browse", {
      domain_id: domainId,
      ...(provider ? { provider } : {}),
      ...args,
    });
  }, [callTool, provider]);

  const reloadKeywords = useCallback(async (signal?: AbortSignal) => {
    const rows = await callTool<Keyword[]>("keywords_list", {
      ...(provider ? { provider } : {}),
      search_engine: searchEngine,
      limit: 300,
    }, signal);
    setKeywords(rows || []);
    setSelectedKeyword((cur) => (rows?.find((r) => r.id === cur?.id) || rows?.[0] || null));
  }, [callTool, provider, searchEngine]);

  const reloadEntities = useCallback(async (signal?: AbortSignal) => {
    const rows = await callTool<SearchEntity[]>("entities_list", { search_engine: searchEngine, limit: 300 }, signal);
    setEntities(rows || []);
    setSelectedEntity((cur) => (rows?.find((r) => r.id === cur?.id) || rows?.[0] || null));
  }, [callTool, searchEngine]);

  const reloadOpportunities = useCallback(async (signal?: AbortSignal) => {
    const resp = await callTool<{ items: ContentOpportunity[] }>("content_opportunities", {
      search_engine: searchEngine,
      ...(provider ? { provider } : {}),
      limit: 25,
    }, signal);
    setOpportunities(resp.items || []);
  }, [callTool, provider, searchEngine]);

  const mergeMetricJobs = useCallback((rows: KeywordMetricJob[]) => {
    setMetricJobs((current) => {
      const byId = new Map(current.map((job) => [job.id, job]));
      for (const job of rows) byId.set(job.id, job);
      return Array.from(byId.values()).sort((a, b) => b.id - a.id).slice(0, 20);
    });
  }, []);

  const reloadMetricJobs = useCallback(async (signal?: AbortSignal) => {
    const response = await api<{ jobs: KeywordMetricJob[] }>("GET", "/keyword-metric-jobs", {}, undefined, signal);
    setMetricJobs(response.jobs || []);
  }, [api]);

  const reloadTrackingSettings = useCallback(async (signal?: AbortSignal) => {
    const settings = await api<RankTrackingSettings>("GET", "/rank-tracking/settings", {}, undefined, signal);
    setTrackingSettings(settings);
  }, [api]);

  const reloadRankTracking = useCallback(async (keywordId: number, signal?: AbortSignal) => {
    const response = await api<{ trackers: RankTracker[] }>("GET", "/rank-tracking", { keyword_id: String(keywordId) }, undefined, signal);
    const trackers = response.trackers || [];
    setRankTrackers(trackers);
    const entries = await Promise.all(trackers.map(async (tracker) => {
      const result = await api<{ history: RankObservation[] }>("GET", "/rank-history", {
        tracker_id: String(tracker.id),
        limit: "400",
      }, undefined, signal);
      return [tracker.id, result.history || []] as const;
    }));
    setRankHistory(Object.fromEntries(entries));
  }, [api]);

  const reloadAll = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      await Promise.all([
        reloadProviders(), reloadLocations(), reloadDomains(), reloadKeywords(),
        reloadEntities(), reloadOpportunities(), reloadMetricJobs(), reloadTrackingSettings(),
      ]);
      setStatus("Updated");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [reloadDomains, reloadEntities, reloadKeywords, reloadLocations, reloadMetricJobs, reloadOpportunities, reloadProviders, reloadTrackingSettings]);

  useEffect(() => {
    setSelectedKeyword(null);
    setSelectedEntity(null);
    setSerpResults([]);
    setKeywordIdeas([]);
  }, [provider, searchEngine]);

  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setErr("");
    Promise.all([
      reloadProviders(controller.signal), reloadLocations(controller.signal),
      reloadDomains(controller.signal), reloadMetricJobs(controller.signal),
      reloadTrackingSettings(controller.signal),
    ]).catch((e) => {
      if (!controller.signal.aborted) setErr((e as Error).message);
    }).finally(() => {
      if (!controller.signal.aborted) setBusy(false);
    });
    return () => controller.abort();
  }, [reloadProviders, reloadLocations, reloadDomains, reloadMetricJobs, reloadTrackingSettings]);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      reloadKeywords(controller.signal), reloadEntities(controller.signal),
      reloadOpportunities(controller.signal),
    ]).catch((e) => {
      if (!controller.signal.aborted) setErr((e as Error).message);
    });
    return () => controller.abort();
  }, [reloadKeywords, reloadEntities, reloadOpportunities]);

  useEffect(() => {
    setDomainMetrics(null);
    setDomainRankings([]);
    setBacklinkMovement(null);
    setActiveBacklinks([]);
    setLostBacklinks([]);
    setSelectedRankURL(null);
    if (!selectedDomain || view !== "domains") return;
    const controller = new AbortController();
    const signal = controller.signal;
    Promise.all([
      callTool<{ domain: Domain; metrics: DomainMetrics | null }>("domains_get", { id: selectedDomain.id, ...(provider ? { provider } : {}) }, signal)
        .then((r) => setDomainMetrics(r.metrics || null)),
      callTool<Ranking[]>("rankings_for_domain", { domain_id: selectedDomain.id, ...(provider ? { provider } : {}), limit: 500 }, signal)
        .then((rows) => setDomainRankings(rows || [])),
      reloadBacklinkInsights(selectedDomain.id, signal),
    ]).catch((e) => { if (!signal.aborted) setErr((e as Error).message); });
    return () => controller.abort();
  }, [callTool, provider, reloadBacklinkInsights, selectedDomain, view]);

  useEffect(() => {
    if (!selectedRankURL && domainRankings.length > 0) {
      const first = pageSummaries(domainRankings)[0];
      if (first) setSelectedRankURL(first.url);
    }
  }, [domainRankings, selectedRankURL]);

  useEffect(() => {
    setPageSerpRows([]);
    if (!selectedRankURL || view !== "domains") return;
    const ids = Array.from(new Set(domainRankings.filter((r) => cleanURL(r.rank_url) === selectedRankURL).map((r) => r.keyword_id)));
    if (ids.length === 0) return;
    const controller = new AbortController();
    // The tool accepts at most 200 IDs, even when a page ranks for more keywords.
    const batches = [];
    for (let start = 0; start < ids.length; start += 200) {
      batches.push(callTool<SearchRanking[]>("rankings_for_keywords", {
        keyword_ids: ids.slice(start, start + 200), ...(provider ? { provider } : {}), limit: 1000,
      }, controller.signal));
    }
    Promise.all(batches).then((rows) => setPageSerpRows(rows.flat()))
      .catch((e) => { if (!controller.signal.aborted) setErr((e as Error).message); });
    return () => controller.abort();
  }, [callTool, domainRankings, provider, selectedRankURL, view]);

  useEffect(() => {
    setKeywordMetrics(null);
    setSerpResults([]);
    setRankTrackers([]);
    setRankHistory({});
    if (!selectedKeyword || (view !== "keywords" && view !== "explorer")) return;
    const controller = new AbortController();
    const signal = controller.signal;
    Promise.all([
      callTool<{ keyword: Keyword; metrics: KeywordMetrics | null }>("keywords_get", { id: selectedKeyword.id, ...(provider ? { provider } : {}) }, signal)
        .then((r) => setKeywordMetrics(r.metrics || null)),
      callTool<SearchRanking[]>("rankings_for_keyword", { keyword_id: selectedKeyword.id, ...(provider ? { provider } : {}), limit: 100 }, signal)
        .then((rows) => setSerpResults(rows || [])),
      ...(view === "keywords" ? [reloadRankTracking(selectedKeyword.id, signal)] : []),
    ]).catch((e) => { if (!signal.aborted) setErr((e as Error).message); });
    return () => controller.abort();
  }, [callTool, provider, reloadRankTracking, searchEngine, selectedKeyword, view]);

  useEffect(() => {
    setEntityRankings([]);
    if (!selectedEntity || view !== "explorer") return;
    const controller = new AbortController();
    callTool<SearchRanking[]>("rankings_for_entity", { entity_id: selectedEntity.id, ...(provider ? { provider } : {}), limit: 200 }, controller.signal)
      .then((rows) => setEntityRankings(rows || []))
      .catch((e) => { if (!controller.signal.aborted) setErr((e as Error).message); });
    return () => controller.abort();
  }, [callTool, provider, selectedEntity, view]);

  const filteredLocations = useMemo(
    () => locations.filter((l) => l.search_engine === searchEngine && (!provider || l.provider === provider)),
    [locations, provider, searchEngine],
  );

  const locationById = useMemo(() => {
    const m = new Map<number, SEOLocation>();
    for (const l of locations) m.set(l.id, l);
    return m;
  }, [locations]);

  const defaultLocation = filteredLocations[0] || locations[0];

  const pushActivity = useCallback((message: string) => {
    const stamp = new Date().toLocaleTimeString();
    setActivity((rows) => [`${stamp} - ${message}`, ...rows].slice(0, 20));
  }, []);

  async function syncLocations() {
    setBusy(true);
    setErr("");
    try {
      const r = await api<Record<string, unknown>>("POST", "/locations/sync", provider ? { provider } : {});
      await reloadLocations();
      setStatus(`Synced ${fmt(Number(r.rows_upserted || 0))} locales`);
      pushActivity(`Synced ${fmt(Number(r.rows_upserted || 0))} ${provider || "provider"} locales`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function refreshDomain(domain: Domain, backlinks = false) {
    setBusy(true);
    setErr("");
    try {
      const loc = domain.default_location_id || locations.find((l) => l.search_engine === "google" && (!provider || l.provider === provider))?.id;
      const params = {
        ...(loc ? { location_id: String(loc) } : {}),
        ...(provider ? { provider } : {}),
      };
      await api<Record<string, unknown>>(
        "POST",
        backlinks ? `/domains/${domain.id}/backlinks/refresh` : `/domains/${domain.id}/refresh`,
        params,
      );
      await reloadDomains();
      setSelectedDomain(domain);
      if (!backlinks) {
        const [detail, rows] = await Promise.all([
          callTool<{ domain: Domain; metrics: DomainMetrics | null }>("domains_get", { id: domain.id, ...(provider ? { provider } : {}) }),
          callTool<Ranking[]>("rankings_for_domain", { domain_id: domain.id, ...(provider ? { provider } : {}), limit: 500 }),
        ]);
        setDomainMetrics(detail.metrics || null);
        setDomainRankings(rows || []);
      } else {
        await reloadBacklinkInsights(domain.id);
      }
      setStatus(backlinks ? "Backlinks refreshed" : "Domain metrics refreshed");
      pushActivity(`${backlinks ? "Backlinks" : "Domain metrics"} refreshed for ${domain.host}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function refreshKeyword(keyword: Keyword) {
    setBusy(true);
    setErr("");
    try {
      await api<Record<string, unknown>>("POST", `/keywords/${keyword.id}/refresh`, {
        location_id: String(keyword.location_id),
        ...(provider ? { provider } : {}),
      });
      await reloadKeywords();
      setSelectedKeyword(keyword);
      const detail = await callTool<{ keyword: Keyword; metrics: KeywordMetrics | null }>("keywords_get", { id: keyword.id, ...(provider ? { provider } : {}) });
      setKeywordMetrics(detail.metrics || null);
      setStatus("Keyword refreshed");
      pushActivity(`Keyword refreshed: ${keyword.text}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function waitForMetricJobs(initialJobs: KeywordMetricJob[]): Promise<KeywordMetricJob[]> {
    let jobs = initialJobs;
    mergeMetricJobs(jobs);
    for (let attempt = 0; attempt < 240; attempt += 1) {
      if (jobs.every((job) => job.status !== "pending" && job.status !== "running")) break;
      await new Promise((resolve) => setTimeout(resolve, 1500));
      jobs = await Promise.all(
        jobs.map((job) =>
          job.status === "pending" || job.status === "running"
            ? api<KeywordMetricJob>("GET", `/keyword-metric-jobs/${job.id}`)
            : Promise.resolve(job),
        ),
      );
      mergeMetricJobs(jobs);
    }
    await Promise.all([reloadKeywords(), reloadMetricJobs()]);
    const incomplete = jobs.filter((job) => job.status !== "completed");
    if (incomplete.length > 0) {
      throw new Error(incomplete.map((job) => job.last_error || `Metric job ${job.id} is ${job.status}`).join("\n"));
    }
    return jobs;
  }

  async function refreshKeywordMetricsBulk(rows: Keyword[]): Promise<KeywordMetricJob[]> {
    if (rows.length === 0) return [];
    if (provider === "yepapi") {
      for (let start = 0; start < rows.length; start += 5) {
        await Promise.all(
          rows.slice(start, start + 5).map((keyword) =>
            api<Record<string, unknown>>("POST", `/keywords/${keyword.id}/refresh`, {
              location_id: String(keyword.location_id),
              provider: "yepapi",
            }),
          ),
        );
      }
      await reloadKeywords();
      setStatus(`Refreshed volume and difficulty for ${fmt(rows.length)} keywords`);
      pushActivity(`Refreshed volume and difficulty for ${fmt(rows.length)} keywords with YepAPI`);
      return [];
    }
    const response = await api<{ jobs: KeywordMetricJob[] }>(
      "POST",
      "/keyword-metric-jobs",
      {},
      { keyword_ids: rows.map((keyword) => keyword.id), ...(provider ? { provider } : {}) },
    );
    const jobs = await waitForMetricJobs(response.jobs || []);
    setStatus(`Refreshed volume and difficulty for ${fmt(rows.length)} keywords`);
    pushActivity(`Refreshed volume and difficulty for ${fmt(rows.length)} keywords`);
    return jobs;
  }

  async function refreshAllKeywordMetrics() {
    setBusy(true);
    setErr("");
    try {
      await refreshKeywordMetricsBulk(keywords);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function resumeMetricJob(job: KeywordMetricJob) {
    setBusy(true);
    setErr("");
    try {
      await api<Record<string, unknown>>("POST", `/keyword-metric-jobs/${job.id}/resume`);
      await waitForMetricJobs([{ ...job, status: "pending", phase: "queued", last_error: "" }]);
      setStatus(`Metric job ${job.id} completed`);
      pushActivity(`Resumed metric job ${job.id}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function refreshKeywordSERP(keyword: Keyword) {
    setBusy(true);
    setErr("");
    try {
      const resp = await callTool<{ results: SearchRanking[]; count: number }>("serp_search", {
        ...(provider ? { provider } : {}),
        search_engine: keyword.search_engine || searchEngine,
        keyword_id: keyword.id,
        location_id: keyword.location_id,
        depth: 20,
      });
      setSerpResults(resp.results || []);
      await Promise.all([reloadEntities(), reloadOpportunities()]);
      setStatus(`Cached ${fmt(resp.count || 0)} ${(keyword.search_engine || searchEngine)} results`);
      pushActivity(`Searched ${(keyword.search_engine || searchEngine)}: ${keyword.text}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function enableRankTracking(keyword: Keyword, target: string, frequency: RefreshFrequency) {
    setBusy(true);
    setErr("");
    try {
      const [kind, rawId] = target.split(":");
      const id = Number(rawId);
      await api("POST", "/rank-tracking", {}, {
        keyword_id: keyword.id,
        ...(kind === "domain" ? { domain_id: id } : { entity_id: id }),
        provider: "dataforseo",
        device: "desktop",
        frequency,
        daily_depth: trackingSettings.daily_depth,
        weekly_depth: trackingSettings.weekly_depth,
      });
      await reloadRankTracking(keyword.id);
      setStatus(`${frequency[0].toUpperCase()}${frequency.slice(1)} rank tracking enabled`);
      pushActivity(`Enabled ${frequency} DataForSEO rank tracking: ${keyword.text}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function changeRankTrackingFrequency(keyword: Keyword, trackerId: number, frequency: RefreshFrequency) {
    setBusy(true);
    setErr("");
    try {
      await api("PATCH", `/rank-tracking/${trackerId}`, {}, { frequency });
      await reloadRankTracking(keyword.id);
      setStatus(`Rank tracking changed to ${frequency}`);
      pushActivity(`Changed rank tracking to ${frequency}: ${keyword.text}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function disableRankTracking(keyword: Keyword, trackerId: number) {
    setBusy(true);
    setErr("");
    try {
      await api("DELETE", `/rank-tracking/${trackerId}`);
      await reloadRankTracking(keyword.id);
      setStatus("Rank tracking disabled; history retained");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function saveTrackingSettings(settings: RankTrackingSettings) {
    setBusy(true);
    setErr("");
    try {
      const saved = await api<RankTrackingSettings>("PATCH", "/rank-tracking/settings", {}, settings);
      setTrackingSettings(saved);
      setStatus("Rank tracking budget saved");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function runSerp(keyword: string, locationId: number, depth: number) {
    setBusy(true);
    setErr("");
    try {
      const resp = await callTool<{ results: SearchRanking[]; count: number }>("serp_search", {
        ...(provider ? { provider } : {}),
        search_engine: searchEngine,
        keyword,
        location_id: locationId,
        depth,
      });
      setSerpResults(resp.results || []);
      await Promise.all([reloadEntities(), reloadOpportunities()]);
      setStatus(`Cached ${fmt(resp.count || 0)} ${searchEngine} results`);
      pushActivity(`Searched ${searchEngine}: ${keyword}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function findIdeas(seedKeywords: string[], locationId: number, limit: number, refresh: boolean) {
    setBusy(true);
    setErr("");
    try {
      const resp = await callTool<{ items: KeywordIdea[] }>("keyword_ideas", {
        ...(provider ? { provider } : {}),
        search_engine: searchEngine,
        seed_keywords: seedKeywords,
        location_id: locationId,
        limit,
        refresh,
      });
      setKeywordIdeas(resp.items || []);
      await Promise.all([reloadEntities(), reloadOpportunities()]);
      setStatus(`Found ${fmt((resp.items || []).length)} ideas`);
      pushActivity(`Generated ${searchEngine} ideas from ${seedKeywords.length} seed keyword${seedKeywords.length === 1 ? "" : "s"}`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="h-full min-h-0 flex flex-col text-text bg-bg">
      <div className="px-6 border-b border-border flex flex-wrap items-center justify-between gap-4">
        <div className="flex flex-wrap items-center gap-6 min-w-0">
          <div className="py-4 shrink-0">
            <h1 className="text-lg font-semibold leading-none">SEO</h1>
            <div className="mt-1 text-xs text-text-dim">Search performance</div>
          </div>
          <nav className="flex items-stretch self-stretch" aria-label="SEO workspace">
            {navigation.map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => setView(item.id)}
                className={`px-4 border-b-2 text-sm ${view === item.id ? "border-accent text-text" : "border-transparent text-text-dim hover:text-text"}`}
              >
                {item.label}
              </button>
            ))}
          </nav>
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading...</span>}
          {status && !busy && <span>{status}</span>}
          <button type="button" className={buttonCls} onClick={reloadAll} disabled={busy}>
            Reload cached data
          </button>
        </div>
      </div>

      {err && (
        <div className="mx-6 mt-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      {view === "overview" && (
        <OverviewView
          domains={domains}
          keywords={keywords}
          opportunities={opportunities}
          activity={activity}
          onOpenDomain={(domain) => {
            setSelectedDomain(domain);
            setView("domains");
          }}
          onGoToDomains={() => setView("domains")}
          onGoToKeywords={() => setView("keywords")}
          onGoToExplorer={() => setView("explorer")}
        />
      )}

      {view === "domains" && (
        <DomainsView
          domains={domains}
          locations={filteredLocations}
          selected={selectedDomain}
          metrics={domainMetrics}
          backlinkMovement={backlinkMovement}
          activeBacklinks={activeBacklinks}
          lostBacklinks={lostBacklinks}
          rankings={domainRankings}
          pageSerpRows={pageSerpRows}
          selectedRankURL={selectedRankURL}
          onSelectRankURL={setSelectedRankURL}
          keywords={keywords}
          onSelect={setSelectedDomain}
          onAdd={async (host, label, locationId) => {
            const d = await callTool<Domain>("domains_add", { ...(provider ? { provider } : {}), host, label, location_id: locationId, search_engine: "google" });
            await reloadDomains();
            setSelectedDomain(d);
          }}
          onRemove={async (id) => {
            await callTool("domains_remove", { id });
            setSelectedDomain(null);
            await reloadDomains();
          }}
          onRefresh={refreshDomain}
          onBrowseBacklinks={browseBacklinks}
          locationById={locationById}
          busy={busy}
        />
      )}

      {view === "explorer" && (
        <div className="flex-1 min-h-0 flex flex-col">
          <WorkspaceToolbar
            title="Explorer"
            description="Research live search results, content opportunities, and tracked competitors."
            searchEngine={searchEngine}
            onSearchEngine={setSearchEngine}
            modes={[
              { id: "research", label: "Research" },
              { id: "entities", label: "Tracked results" },
            ]}
            mode={explorerMode}
            onMode={(mode) => setExplorerMode(mode as ExplorerMode)}
          />
          {explorerMode === "research" ? (
            <DiscoverView
              searchEngine={searchEngine}
              locations={filteredLocations}
              busy={busy}
              serpResults={serpResults}
              keywordIdeas={keywordIdeas}
              opportunities={opportunities}
              onSync={syncLocations}
              onSearch={runSerp}
              onIdeas={findIdeas}
            />
          ) : (
            <EntitiesView
              searchEngine={searchEngine}
              entities={entities}
              selected={selectedEntity}
              rankings={entityRankings}
              locations={filteredLocations}
              locationById={locationById}
              busy={busy}
              onSelect={setSelectedEntity}
              onAdd={async (payload) => {
                const entity = await callTool<SearchEntity>("entities_add", {
                  ...(provider ? { provider } : {}),
                  search_engine: searchEngine,
                  entity_type: payload.entityType,
                  identifier: payload.identifier,
                  label: payload.label,
                  url: payload.url,
                  location_id: payload.locationId,
                });
                await reloadEntities();
                setSelectedEntity(entity);
              }}
              onRemove={async (id) => {
                await callTool("entities_remove", { id });
                setSelectedEntity(null);
                await reloadEntities();
              }}
            />
          )}
        </div>
      )}

      {view === "keywords" && (
        <div className="flex-1 min-h-0 flex flex-col">
        <WorkspaceToolbar
          title="Keywords"
          description="Track search demand, rankings, and refresh schedules."
          searchEngine={searchEngine}
          onSearchEngine={setSearchEngine}
        />
        <KeywordsView
          keywords={keywords}
          locations={filteredLocations}
          selected={selectedKeyword}
          metrics={keywordMetrics}
          serpResults={serpResults}
          domains={domains}
          entities={entities}
          rankTrackers={rankTrackers}
          rankHistory={rankHistory}
          trackingSettings={trackingSettings}
          searchEngine={searchEngine}
          onSelect={setSelectedKeyword}
          onAdd={async (text, locationId) => {
            const k = await callTool<Keyword>("keywords_add", { ...(provider ? { provider } : {}), text, location_id: locationId, search_engine: searchEngine });
            await reloadKeywords();
            setSelectedKeyword(k);
          }}
          onRemove={async (id) => {
            await callTool("keywords_remove", { id });
            setSelectedKeyword(null);
            await reloadKeywords();
          }}
          onRefresh={refreshKeyword}
          onRefreshAll={refreshAllKeywordMetrics}
          onRefreshSERP={refreshKeywordSERP}
          onEnableTracking={enableRankTracking}
          onChangeTrackingFrequency={changeRankTrackingFrequency}
          onDisableTracking={disableRankTracking}
          onSaveTrackingSettings={saveTrackingSettings}
          locationById={locationById}
          busy={busy}
        />
        </div>
      )}

      {view === "settings" && (
        <SettingsView
          providers={providers}
          defaultProvider={provider}
          locations={locations}
          activity={activity}
          onSync={syncLocations}
          busy={busy}
        />
      )}
    </div>
  );
}

function isCompetitorDomain(domain: Domain): boolean {
  return (domain.label || "").toLowerCase().includes("competitor");
}

function OverviewView(props: {
  domains: Domain[];
  keywords: Keyword[];
  opportunities: ContentOpportunity[];
  activity: string[];
  onOpenDomain(domain: Domain): void;
  onGoToDomains(): void;
  onGoToKeywords(): void;
  onGoToExplorer(): void;
}) {
  const sites = props.domains.filter((domain) => !isCompetitorDomain(domain));
  const competitors = props.domains.filter(isCompetitorDomain);
  return (
    <div className="flex-1 min-h-0 overflow-auto">
      <div className="max-w-7xl mx-auto p-6 space-y-6">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <h2 className="text-2xl font-semibold">Overview</h2>
            <p className="mt-1 text-sm text-text-dim">Your sites, tracked search demand, and recent SEO work in one place.</p>
          </div>
          <button type="button" className={primaryBtn} onClick={props.onGoToDomains}>Add or find a domain</button>
        </div>

        <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
          {[
            ["Your sites", sites.length, "Domains you added"],
            ["Competitors", competitors.length, "Discovered from search results"],
            ["Keywords", props.keywords.length, "In the selected search engine"],
            ["Opportunities", props.opportunities.length, "From cached search research"],
          ].map(([label, value, hint]) => (
            <div key={String(label)} className="border border-border rounded p-4 bg-surface-1">
              <div className="text-xs text-text-dim">{label}</div>
              <div className="mt-2 text-3xl font-semibold tabular-nums">{fmt(Number(value))}</div>
              <div className="mt-1 text-xs text-text-dim">{hint}</div>
            </div>
          ))}
        </div>

        <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_360px] gap-5">
          <section className="border border-border rounded overflow-hidden">
            <div className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
              <div>
                <h3 className="font-semibold">Your sites</h3>
                <div className="text-xs text-text-dim">Open a site to inspect rankings and backlinks.</div>
              </div>
              <button type="button" className={buttonCls} onClick={props.onGoToDomains}>Browse all domains</button>
            </div>
            {sites.length === 0 ? (
              <div className="p-8 text-center">
                <div className="font-medium">No site added yet</div>
                <div className="mt-1 text-sm text-text-dim">Add your main domain first. Competitors will stay separate.</div>
                <button type="button" className={`${primaryBtn} mt-4`} onClick={props.onGoToDomains}>Add a domain</button>
              </div>
            ) : (
              <div className="divide-y divide-border">
                {sites.slice(0, 8).map((domain) => (
                  <button key={domain.id} type="button" onClick={() => props.onOpenDomain(domain)} className="w-full px-4 py-3 text-left hover:bg-surface-2 flex items-center justify-between gap-4">
                    <div className="min-w-0">
                      <div className="font-medium truncate">{domain.host}</div>
                      <div className="text-xs text-text-dim truncate">{domain.label || "Tracked site"}</div>
                    </div>
                    <span className="text-sm text-text-dim">Open →</span>
                  </button>
                ))}
              </div>
            )}
          </section>

          <div className="space-y-5">
            <section className="border border-border rounded p-4 space-y-3">
              <div>
                <h3 className="font-semibold">Start here</h3>
                <div className="text-xs text-text-dim">Common SEO workflows</div>
              </div>
              <button type="button" className="w-full border border-border rounded p-3 text-left hover:bg-surface-2" onClick={props.onGoToDomains}>
                <div className="text-sm font-medium">Inspect a domain</div>
                <div className="text-xs text-text-dim">Metrics, ranked pages, and backlinks</div>
              </button>
              <button type="button" className="w-full border border-border rounded p-3 text-left hover:bg-surface-2" onClick={props.onGoToKeywords}>
                <div className="text-sm font-medium">Track a keyword</div>
                <div className="text-xs text-text-dim">Volumes, SERPs, and rank history</div>
              </button>
              <button type="button" className="w-full border border-border rounded p-3 text-left hover:bg-surface-2" onClick={props.onGoToExplorer}>
                <div className="text-sm font-medium">Research search results</div>
                <div className="text-xs text-text-dim">Explore Google or YouTube</div>
              </button>
            </section>
            <section className="border border-border rounded overflow-hidden">
              <div className="px-4 py-3 border-b border-border font-semibold">Recent activity</div>
              <div className="max-h-64 overflow-auto">
                <ActivityFeed rows={props.activity.slice(0, 8)} />
              </div>
            </section>
          </div>
        </div>
      </div>
    </div>
  );
}

function WorkspaceToolbar(props: {
  title: string;
  description: string;
  searchEngine: SearchEngine;
  onSearchEngine(engine: SearchEngine): void;
  modes?: { id: string; label: string }[];
  mode?: string;
  onMode?(mode: string): void;
}) {
  return (
    <div className="px-6 py-3 border-b border-border flex flex-wrap items-center justify-between gap-4">
      <div>
        <h2 className="font-semibold">{props.title}</h2>
        <div className="text-xs text-text-dim">{props.description}</div>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {props.modes && props.onMode && (
          <div className="flex rounded border border-border overflow-hidden text-xs">
            {props.modes.map((item) => (
              <button key={item.id} type="button" onClick={() => props.onMode!(item.id)} className={`px-3 py-1.5 ${props.mode === item.id ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}>
                {item.label}
              </button>
            ))}
          </div>
        )}
        <div className="flex rounded border border-border overflow-hidden text-xs" aria-label="Search engine">
          {engines.map((engine) => (
            <button key={engine.id} type="button" onClick={() => props.onSearchEngine(engine.id)} className={`px-3 py-1.5 ${props.searchEngine === engine.id ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}>
              {engine.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

function SettingsView(props: {
  providers: string[];
  defaultProvider: string;
  locations: SEOLocation[];
  activity: string[];
  onSync(): Promise<void>;
  busy: boolean;
}) {
  const [section, setSection] = useState<"sources" | "locations" | "activity">("sources");
  const [filter, setFilter] = useState("");
  const rows = props.locations.filter((location) => localeLabel(location).toLowerCase().includes(filter.toLowerCase())).slice(0, 500);
  const providerName = (value: string) => value === "dataforseo" ? "DataForSEO" : value === "yepapi" ? "YepAPI" : value;
  return (
    <div className="flex-1 min-h-0 flex">
      <aside className="w-64 border-r border-border p-4 space-y-1">
        <div className="px-2 pb-3">
          <h2 className="font-semibold">Settings</h2>
          <div className="text-xs text-text-dim">Data refresh and workspace setup</div>
        </div>
        {([
          ["sources", "Data sources"],
          ["locations", "Search locations"],
          ["activity", "Activity"],
        ] as const).map(([id, label]) => (
          <button key={id} type="button" onClick={() => setSection(id)} className={`w-full text-left px-3 py-2 rounded text-sm ${section === id ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}>{label}</button>
        ))}
      </aside>
      <div className="flex-1 min-w-0 overflow-auto p-6">
        {section === "sources" && (
          <div className="max-w-3xl space-y-5">
            <div>
              <h3 className="text-xl font-semibold">Data sources</h3>
              <p className="mt-1 text-sm text-text-dim">These integrations power explicit refresh actions. Cached browsing never calls them.</p>
            </div>
            <div className="space-y-3">
              {props.providers.length === 0 ? (
                <div className="border border-dashed border-border rounded p-5 text-sm text-text-dim">No SEO data source connected.</div>
              ) : props.providers.map((source) => (
                <div key={source} className="border border-border rounded p-4 flex items-center justify-between gap-4">
                  <div>
                    <div className="font-medium">{providerName(source)}</div>
                    <div className="text-xs text-text-dim">Used for paid domain, keyword, SERP, or backlink refreshes.</div>
                  </div>
                  <span className={`text-xs px-2 py-1 rounded border ${source === props.defaultProvider ? "border-accent/50 text-accent" : "border-border text-text-dim"}`}>
                    {source === props.defaultProvider ? "Default refresh source" : "Connected"}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}
        {section === "locations" && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-end justify-between gap-4">
              <div>
                <h3 className="text-xl font-semibold">Search locations</h3>
                <p className="mt-1 text-sm text-text-dim">Locales available when adding keywords or running research.</p>
              </div>
              <button type="button" className={buttonCls} disabled={props.busy} onClick={props.onSync}>Sync locations</button>
            </div>
            <input className={`${inputCls} max-w-md`} value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Search country or language" />
            <div className="border border-border rounded overflow-hidden">
              <table className="w-full text-sm">
                <thead className="bg-surface-2 text-text-dim"><tr><th className="text-left font-medium px-3 py-2">Location</th><th className="text-left font-medium px-3 py-2">Language</th><th className="text-left font-medium px-3 py-2">Engine</th><th className="text-left font-medium px-3 py-2">Source</th></tr></thead>
                <tbody>{rows.map((location) => <tr key={location.id} className="border-t border-border"><td className="px-3 py-2">{location.location_name} <span className="text-text-dim">{location.country_iso || ""}</span></td><td className="px-3 py-2 text-text-dim">{location.language_name || location.language_code}</td><td className="px-3 py-2 text-text-dim">{location.search_engine}</td><td className="px-3 py-2 text-text-dim">{providerName(location.provider)}</td></tr>)}</tbody>
              </table>
            </div>
          </div>
        )}
        {section === "activity" && <div className="max-w-3xl border border-border rounded overflow-hidden"><ActivityFeed rows={props.activity} /></div>}
      </div>
    </div>
  );
}

function DiscoverView(props: {
  searchEngine: SearchEngine;
  locations: SEOLocation[];
  busy: boolean;
  serpResults: SearchRanking[];
  keywordIdeas: KeywordIdea[];
  opportunities: ContentOpportunity[];
  onSync(): Promise<void>;
  onSearch(keyword: string, locationId: number, depth: number): Promise<void>;
  onIdeas(seedKeywords: string[], locationId: number, limit: number, refresh: boolean): Promise<void>;
}) {
  const [keyword, setKeyword] = useState("");
  const [seedText, setSeedText] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const [depth, setDepth] = useState(20);
  const [refreshIdeas, setRefreshIdeas] = useState(true);
  const selectedLocation = locationId === "" ? undefined : props.locations.find((l) => l.id === Number(locationId));
  const seeds = seedText.split(/\n|,/).map((s) => s.trim()).filter(Boolean);
  const canRun = locationId !== "" && props.locations.length > 0;

  useEffect(() => {
    if (props.locations.length === 0) {
      if (locationId !== "") setLocationId("");
      return;
    }
    if (locationId === "" || !props.locations.some((l) => l.id === Number(locationId))) {
      setLocationId(props.locations[0].id);
    }
  }, [locationId, props.locations]);

  return (
    <div className="flex-1 min-h-0 grid grid-cols-1 xl:grid-cols-[420px_minmax(0,1fr)]">
      <div className="border-r border-border p-5 overflow-auto space-y-5">
        <div className="space-y-2">
          <h2 className="text-lg font-semibold capitalize">{props.searchEngine} Discovery</h2>
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">Select locale</option>
            {props.locations.map((l) => (
              <option key={l.id} value={l.id}>{localeLabel(l)}</option>
            ))}
          </select>
          {props.locations.length === 0 && (
            <button type="button" className={buttonCls} onClick={props.onSync} disabled={props.busy}>
              Sync Locations
            </button>
          )}
        </div>

        <form
          className="space-y-3 border border-border rounded p-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (!canRun || !keyword.trim()) return;
            await props.onSearch(keyword.trim(), Number(locationId), depth);
          }}
        >
          <div className="text-sm font-medium">SERP Search</div>
          <input className={inputCls} value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="keyword to search" />
          <label className="block">
            <span className="block text-xs text-text-dim mb-1">Depth</span>
            <input
              className={inputCls}
              type="number"
              min={1}
              max={100}
              value={depth}
              onChange={(e) => setDepth(Math.max(1, Math.min(100, Number(e.target.value) || 20)))}
            />
          </label>
          <button type="submit" className={primaryBtn} disabled={props.busy || !canRun || !keyword.trim()}>
            Search
          </button>
        </form>

        <form
          className="space-y-3 border border-border rounded p-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (!canRun || seeds.length === 0) return;
            await props.onIdeas(seeds, Number(locationId), 50, refreshIdeas);
          }}
        >
          <div className="text-sm font-medium">Keyword Ideas</div>
          <textarea
            className={`${inputCls} min-h-28 resize-y`}
            value={seedText}
            onChange={(e) => setSeedText(e.target.value)}
            placeholder="seed keywords, one per line"
          />
          <label className="inline-flex items-center gap-2 text-sm">
            <input type="checkbox" checked={refreshIdeas} onChange={(e) => setRefreshIdeas(e.target.checked)} />
            <span>Refresh SERPs first</span>
          </label>
          <button type="submit" className={primaryBtn} disabled={props.busy || !canRun || seeds.length === 0}>
            Find Ideas
          </button>
        </form>

        <div className="text-xs text-text-dim">{selectedLocation ? localeLabel(selectedLocation) : "No locale selected"}</div>
      </div>

      <div className="min-w-0 overflow-auto p-5 space-y-6">
        <IdeasTable ideas={props.keywordIdeas} />
        <OpportunitiesTable rows={props.opportunities} />
        <SERPResultsTable rows={props.serpResults} />
      </div>
    </div>
  );
}

function IdeasTable({ ideas }: { ideas: KeywordIdea[] }) {
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold">Keyword Ideas</h3>
      {ideas.length === 0 ? (
        <div className="border border-dashed border-border rounded p-4 text-sm text-text-dim">No ideas yet.</div>
      ) : (
        <table className="w-full text-sm border border-border">
          <thead className="bg-surface-2 text-text-dim">
            <tr>
              <th className="text-left font-medium px-3 py-2">Keyword</th>
              <th className="text-left font-medium px-3 py-2 w-24">Score</th>
              <th className="text-left font-medium px-3 py-2">Examples</th>
            </tr>
          </thead>
          <tbody>
            {ideas.map((idea) => (
              <tr key={`${idea.source_keyword || ""}-${idea.keyword}`} className="border-t border-border align-top">
                <td className="px-3 py-2 font-medium">{idea.keyword}</td>
                <td className="px-3 py-2 tabular-nums">{fmt(idea.opportunity_score)}</td>
                <td className="px-3 py-2 text-text-dim">{(idea.example_titles || []).slice(0, 2).join(" / ") || idea.reason || "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function OpportunitiesTable({ rows }: { rows: ContentOpportunity[] }) {
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold">Content Opportunities</h3>
      {rows.length === 0 ? (
        <div className="border border-dashed border-border rounded p-4 text-sm text-text-dim">No cached opportunities yet.</div>
      ) : (
        <table className="w-full text-sm border border-border">
          <thead className="bg-surface-2 text-text-dim">
            <tr>
              <th className="text-left font-medium px-3 py-2">Keyword</th>
              <th className="text-left font-medium px-3 py-2 w-24">Score</th>
              <th className="text-left font-medium px-3 py-2 w-28">Top 10</th>
              <th className="text-left font-medium px-3 py-2">Top Titles</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.keyword} className="border-t border-border align-top">
                <td className="px-3 py-2 font-medium">{row.keyword}</td>
                <td className="px-3 py-2 tabular-nums">{fmt(row.opportunity_score)}</td>
                <td className="px-3 py-2 tabular-nums">{fmt(row.top10_count)} / {fmt(row.result_count)}</td>
                <td className="px-3 py-2 text-text-dim">
                  <div>{(row.example_titles || []).slice(0, 3).join(" / ") || "-"}</div>
                  {row.reason && <div className="mt-1 text-xs">{row.reason}</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function SERPResultsTable({ rows }: { rows: SearchRanking[] }) {
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold">Latest SERP Results</h3>
      {rows.length === 0 ? (
        <div className="border border-dashed border-border rounded p-4 text-sm text-text-dim">No SERP search run in this session.</div>
      ) : (
        <table className="w-full text-sm border border-border">
          <thead className="bg-surface-2 text-text-dim">
            <tr>
              <th className="text-left font-medium px-3 py-2 w-20">Rank</th>
              <th className="text-left font-medium px-3 py-2">Title</th>
              <th className="text-left font-medium px-3 py-2">Entity</th>
              <th className="text-left font-medium px-3 py-2">Channel</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className="border-t border-border align-top">
                <td className="px-3 py-2 tabular-nums">#{row.rank || "-"}</td>
                <td className="px-3 py-2">
                  {row.url ? (
                    <a className="hover:underline" href={row.url} target="_blank" rel="noreferrer">{row.title || row.url}</a>
                  ) : (
                    row.title || "-"
                  )}
                </td>
                <td className="px-3 py-2 text-text-dim">{row.identifier || "-"}</td>
                <td className="px-3 py-2 text-text-dim">{row.channel_title || row.channel_identifier || "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function EntitiesView(props: {
  searchEngine: SearchEngine;
  entities: SearchEntity[];
  selected: SearchEntity | null;
  rankings: SearchRanking[];
  locations: SEOLocation[];
  locationById: Map<number, SEOLocation>;
  busy: boolean;
  onSelect(e: SearchEntity): void;
  onAdd(payload: { entityType: string; identifier: string; label: string; url: string; locationId?: number }): Promise<void>;
  onRemove(id: number): Promise<void>;
}) {
  const [entityType, setEntityType] = useState(props.searchEngine === "youtube" ? "channel" : "domain");
  const [identifier, setIdentifier] = useState("");
  const [label, setLabel] = useState("");
  const [url, setURL] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const typeOptions = props.searchEngine === "youtube" ? ["channel", "video"] : ["domain", "page"];
  const selectedLoc = props.selected?.default_location_id ? props.locationById.get(props.selected.default_location_id) : undefined;

  useEffect(() => {
    if (!typeOptions.includes(entityType)) setEntityType(typeOptions[0]);
  }, [entityType, typeOptions]);

  useEffect(() => {
    if (props.locations.length === 0) {
      if (locationId !== "") setLocationId("");
      return;
    }
    if (locationId !== "" && !props.locations.some((l) => l.id === Number(locationId))) {
      setLocationId("");
    }
  }, [locationId, props.locations]);

  return (
    <div className="flex-1 min-h-0 flex">
      <div className="w-96 border-r border-border flex flex-col min-h-0">
        <form
          className="p-4 border-b border-border space-y-2"
          onSubmit={async (e) => {
            e.preventDefault();
            await props.onAdd({
              entityType,
              identifier,
              label,
              url,
              locationId: locationId === "" ? undefined : Number(locationId),
            });
            setIdentifier("");
            setLabel("");
            setURL("");
          }}
        >
          <select className={inputCls} value={entityType} onChange={(e) => setEntityType(e.target.value)}>
            {typeOptions.map((type) => <option key={type} value={type}>{type}</option>)}
          </select>
          <input className={inputCls} value={identifier} onChange={(e) => setIdentifier(e.target.value)} placeholder={props.searchEngine === "youtube" ? "@channel or video id" : "domain or URL"} />
          <input className={inputCls} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Label" />
          <input className={inputCls} value={url} onChange={(e) => setURL(e.target.value)} placeholder="URL optional" />
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">No default locale</option>
            {props.locations.map((l) => <option key={l.id} value={l.id}>{localeLabel(l)}</option>)}
          </select>
          <button type="submit" className={primaryBtn} disabled={props.busy || !identifier.trim()}>Add Entity</button>
        </form>
        <div className="flex-1 overflow-auto">
          {props.entities.map((entity) => (
            <button
              key={entity.id}
              type="button"
              onClick={() => props.onSelect(entity)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${props.selected?.id === entity.id ? "bg-surface-2" : ""}`}
            >
              <div className="font-medium truncate">{entity.label || entity.identifier}</div>
              <div className="text-xs text-text-dim truncate">{entity.entity_type} - {entity.identifier}</div>
            </button>
          ))}
        </div>
      </div>
      <div className="flex-1 min-w-0 p-6 overflow-auto">
        {props.selected ? (
          <div className="space-y-6">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h2 className="text-xl font-semibold">{props.selected.label || props.selected.identifier}</h2>
                <div className="text-sm text-text-dim">{props.selected.search_engine} - {props.selected.entity_type} - {localeLabel(selectedLoc)}</div>
              </div>
              <button className={buttonCls} disabled={props.busy} onClick={() => props.onRemove(props.selected!.id)}>Remove</button>
            </div>
            <SERPResultsTable rows={props.rankings} />
          </div>
        ) : (
          <div className="text-sm text-text-dim">No entity selected.</div>
        )}
      </div>
    </div>
  );
}

function DomainsView(props: {
  domains: Domain[];
  keywords: Keyword[];
  locations: SEOLocation[];
  selected: Domain | null;
  metrics: DomainMetrics | null;
  backlinkMovement: BacklinkMovement | null;
  activeBacklinks: Backlink[];
  lostBacklinks: Backlink[];
  rankings: Ranking[];
  pageSerpRows: SearchRanking[];
  selectedRankURL: string | null;
  onSelectRankURL(url: string): void;
  onSelect(d: Domain): void;
  onAdd(host: string, label: string, locationId?: number): Promise<void>;
  onRemove(id: number): Promise<void>;
  onRefresh(d: Domain, backlinks?: boolean): Promise<void>;
  onBrowseBacklinks(domainId: number, args: BacklinkBrowseArgs): Promise<BacklinkPage>;
  locationById: Map<number, SEOLocation>;
  busy: boolean;
}) {
  const [host, setHost] = useState("");
  const [label, setLabel] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const [query, setQuery] = useState("");
  const [domainFilter, setDomainFilter] = useState<"sites" | "competitors" | "all">("sites");
  const [showAdd, setShowAdd] = useState(false);
  const [detailTab, setDetailTab] = useState<"overview" | "rankings" | "backlinks">("overview");
  const [showAllBacklinks, setShowAllBacklinks] = useState(false);
  const selectedLoc = props.selected?.default_location_id
    ? props.locationById.get(props.selected.default_location_id)
    : undefined;
  const keywordById = useMemo(() => new Map(props.keywords.map((k) => [k.id, k])), [props.keywords]);
  const visibleDomains = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return props.domains
      .filter((domain) => domainFilter === "all" || (domainFilter === "competitors" ? isCompetitorDomain(domain) : !isCompetitorDomain(domain)))
      .filter((domain) => !normalized || domain.host.toLowerCase().includes(normalized) || (domain.label || "").toLowerCase().includes(normalized))
      .sort((a, b) => a.host.localeCompare(b.host));
  }, [domainFilter, props.domains, query]);
  const siteCount = props.domains.filter((domain) => !isCompetitorDomain(domain)).length;
  const competitorCount = props.domains.length - siteCount;
  useEffect(() => {
    setDetailTab("overview");
    setShowAllBacklinks(false);
  }, [props.selected?.id]);
  return (
    <div className="flex-1 min-h-0 flex">
      <aside className="w-96 border-r border-border flex flex-col min-h-0">
        <div className="p-4 border-b border-border space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h2 className="font-semibold">Domain explorer</h2>
              <div className="text-xs text-text-dim">{fmt(props.domains.length)} cached domains</div>
            </div>
            <button type="button" className={primaryBtn} onClick={() => setShowAdd((value) => !value)}>{showAdd ? "Cancel" : "Add domain"}</button>
          </div>
          <input className={inputCls} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Find a domain" autoFocus />
          <div className="grid grid-cols-3 rounded border border-border overflow-hidden text-xs">
            {([
              ["sites", `Sites ${siteCount}`],
              ["competitors", `Competitors ${competitorCount}`],
              ["all", `All ${props.domains.length}`],
            ] as const).map(([value, text]) => (
              <button key={value} type="button" onClick={() => setDomainFilter(value)} className={`px-2 py-2 ${domainFilter === value ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}>{text}</button>
            ))}
          </div>
        </div>
        {showAdd && (
          <form
            className="p-4 border-b border-border space-y-3 bg-surface-1"
            onSubmit={async (event) => {
              event.preventDefault();
              await props.onAdd(host, label, locationId === "" ? undefined : Number(locationId));
              setHost("");
              setLabel("");
              setShowAdd(false);
              setDomainFilter("sites");
            }}
          >
            <div className="text-sm font-medium">Track a site</div>
            <input className={inputCls} value={host} onChange={(event) => setHost(event.target.value)} placeholder="example.com" />
            <input className={inputCls} value={label} onChange={(event) => setLabel(event.target.value)} placeholder="Site name (optional)" />
            <select className={inputCls} value={locationId} onChange={(event) => setLocationId(event.target.value ? Number(event.target.value) : "")}>
              <option value="">Choose location later</option>
              {props.locations.map((location) => <option key={location.id} value={location.id}>{localeLabel(location)}</option>)}
            </select>
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs text-text-dim">Adding does not refresh paid data.</span>
              <button type="submit" className={primaryBtn} disabled={props.busy || !host.trim()}>Add site</button>
            </div>
          </form>
        )}
        <div className="flex-1 overflow-auto">
          {visibleDomains.length === 0 && <div className="p-6 text-center text-sm text-text-dim">No domains match this view.</div>}
          {visibleDomains.map((domain) => (
            <button
              key={domain.id}
              type="button"
              onClick={() => props.onSelect(domain)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${props.selected?.id === domain.id ? "bg-surface-2" : ""}`}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="font-medium truncate">{domain.host}</div>
                <span className="text-[10px] uppercase tracking-wide text-text-dim">{isCompetitorDomain(domain) ? "Competitor" : "Site"}</span>
              </div>
              <div className="mt-1 text-xs text-text-dim truncate">{domain.label || localeLabel(domain.default_location_id ? props.locationById.get(domain.default_location_id) : undefined)}</div>
            </button>
          ))}
        </div>
      </aside>
      <div className="flex-1 min-w-0 p-6 overflow-auto">
        {props.selected ? (
          showAllBacklinks ? (
            <BacklinkDetail
              domain={props.selected}
              movement={props.backlinkMovement}
              busy={props.busy}
              onBack={() => setShowAllBacklinks(false)}
              onRefresh={() => props.onRefresh(props.selected!, true)}
              onLoad={props.onBrowseBacklinks}
            />
          ) : (
          <div className="space-y-6">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <div className="flex items-center gap-2">
                  <h2 className="text-xl font-semibold">{props.selected.host}</h2>
                  <span className="text-[10px] uppercase tracking-wide border border-border rounded px-2 py-0.5 text-text-dim">{isCompetitorDomain(props.selected) ? "Competitor" : "Site"}</span>
                </div>
                <div className="mt-1 text-sm text-text-dim">{props.selected.label || "Tracked domain"} · {localeLabel(selectedLoc)}</div>
              </div>
              <div className="flex flex-wrap gap-2">
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!)}>Refresh metrics</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!, true)}>Refresh backlinks</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRemove(props.selected!.id)}>Remove</button>
              </div>
            </div>
            <div className="border-b border-border flex gap-5">
              {([
                ["overview", "Overview"],
                ["rankings", "Rankings"],
                ["backlinks", "Backlinks"],
              ] as const).map(([value, text]) => (
                <button key={value} type="button" onClick={() => setDetailTab(value)} className={`pb-2 border-b-2 text-sm ${detailTab === value ? "border-accent text-text" : "border-transparent text-text-dim hover:text-text"}`}>{text}</button>
              ))}
            </div>

            {detailTab === "overview" && (
              <div className="space-y-5">
                <div className="grid grid-cols-2 xl:grid-cols-4 gap-px border border-border bg-border">
                  {([
                    ["Organic traffic", fmt(props.metrics?.organic_traffic)],
                    ["Organic keywords", fmt(props.metrics?.organic_keywords)],
                    ["Ranked pages", fmt(pageSummaries(props.rankings).length)],
                    ["Cached backlinks", fmt(props.backlinkMovement?.active_links)],
                  ] as [string, string][]).map(([label, value]) => (
                    <div key={label} className="bg-bg p-4 min-w-0">
                      <div className="text-xs text-text-dim truncate">{label}</div>
                      <div className="mt-1 text-xl font-semibold truncate">{value}</div>
                    </div>
                  ))}
                </div>
                <div className="text-xs text-text-dim">Metrics last refreshed {date(props.metrics?.ts)} · Refresh buttons may use provider credit.</div>
                <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                  <button type="button" onClick={() => setDetailTab("rankings")} className="border border-border rounded p-4 text-left hover:bg-surface-2">
                    <div className="text-sm font-medium">Search visibility</div>
                    <div className="mt-2 text-2xl font-semibold">{fmt(props.rankings.length)}</div>
                    <div className="text-xs text-text-dim">cached ranking rows across {fmt(pageSummaries(props.rankings).length)} pages</div>
                    <div className="mt-3 text-sm">Explore ranked pages →</div>
                  </button>
                  <button type="button" onClick={() => setDetailTab("backlinks")} className="border border-border rounded p-4 text-left hover:bg-surface-2">
                    <div className="text-sm font-medium">Link profile</div>
                    <div className="mt-2 text-2xl font-semibold">{fmt(props.backlinkMovement?.active_links)}</div>
                    <div className="text-xs text-text-dim">active links · {fmt(props.backlinkMovement?.lost_links)} lost · net {props.backlinkMovement ? `${props.backlinkMovement.net_change >= 0 ? "+" : ""}${fmt(props.backlinkMovement.net_change)}` : "-"}</div>
                    <div className="mt-3 text-sm">Explore backlinks →</div>
                  </button>
                </div>
              </div>
            )}
            {detailTab === "rankings" && <RankingExplorer rankings={props.rankings} selectedURL={props.selectedRankURL} pageSerpRows={props.pageSerpRows} onSelectURL={props.onSelectRankURL} keywordById={keywordById} />}
            {detailTab === "backlinks" && <BacklinkInsights movement={props.backlinkMovement} active={props.activeBacklinks} lost={props.lostBacklinks} onViewAll={() => setShowAllBacklinks(true)} />}
          </div>
          )
        ) : (
          <div className="h-full flex items-center justify-center text-center">
            <div>
              <div className="font-medium">Select a domain</div>
              <div className="mt-1 text-sm text-text-dim">Search or filter the domain explorer on the left.</div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function BacklinkInsights(props: { movement: BacklinkMovement | null; active: Backlink[]; lost: Backlink[]; onViewAll(): void }) {
  const movement = props.movement;
  if (!movement) {
    return (
      <section className="border border-border rounded p-4">
        <h3 className="text-sm font-semibold">Backlink Movement</h3>
        <div className="mt-2 text-sm text-text-dim">No cached backlink history yet. Use Refresh Links to import provider history.</div>
      </section>
    );
  }
  const chart = movement.points.slice(-30);
  const maxDaily = Math.max(1, ...chart.flatMap((point) => [point.gained, point.lost]));
  const totalLinks = movement.active_links + movement.lost_links;
  return (
    <section className="border border-border rounded overflow-hidden">
      <div className="px-4 py-3 border-b border-border flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">Backlink Movement</h3>
          <div className="text-xs text-text-dim">Cached provider history · {movement.from_date} to {movement.to_date} · no extra provider request</div>
        </div>
        <div className="flex items-center gap-3">
          <div className="text-xs text-text-dim">{movement.provider}</div>
          <button type="button" className={buttonCls} onClick={props.onViewAll}>View all links</button>
        </div>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-5 gap-px bg-border border-b border-border">
        {[
          ["Cached active", fmt(movement.active_links), "text-text"],
          ["Cached lost", fmt(movement.lost_links), "text-red-300"],
          [`Gained ${movement.days}d`, `+${fmt(movement.gained_in_range)}`, "text-emerald-300"],
          [`Lost ${movement.days}d`, `-${fmt(movement.lost_in_range)}`, "text-red-300"],
          ["Net", `${movement.net_change >= 0 ? "+" : ""}${fmt(movement.net_change)}`, movement.net_change >= 0 ? "text-emerald-300" : "text-red-300"],
        ].map(([label, value, tone]) => (
          <div key={label} className="bg-bg p-3">
            <div className="text-xs text-text-dim">{label}</div>
            <div className={`mt-1 text-lg font-semibold tabular-nums ${tone}`}>{value}</div>
          </div>
        ))}
      </div>
      <div className="p-4 border-b border-border">
        <div className="flex items-center justify-between gap-3 mb-3">
          <div className="text-sm font-medium">Last 30 days</div>
          <div className="flex gap-3 text-xs text-text-dim">
            <span><span className="inline-block w-2 h-2 bg-emerald-400 mr-1" />Gained</span>
            <span><span className="inline-block w-2 h-2 bg-red-400 mr-1" />Lost</span>
          </div>
        </div>
        <div className="h-24 flex items-end gap-1">
          {chart.map((point) => (
            <div key={point.date} className="flex-1 min-w-0 h-full flex items-end gap-px" title={`${point.date}: +${point.gained} / -${point.lost}`}>
              <div className="flex-1 bg-emerald-400/80 min-h-px" style={{ height: `${(point.gained / maxDaily) * 100}%` }} />
              <div className="flex-1 bg-red-400/80 min-h-px" style={{ height: `${(point.lost / maxDaily) * 100}%` }} />
            </div>
          ))}
        </div>
        <div className="mt-2 text-xs text-text-dim">
          Timestamp coverage: first seen {fmt(movement.known_first_seen)}/{fmt(totalLinks)} · lost date {fmt(movement.known_lost_date)}/{fmt(movement.lost_links)}
        </div>
      </div>
      <div className="grid grid-cols-1 xl:grid-cols-2 divide-y xl:divide-y-0 xl:divide-x divide-border">
        <BacklinkRows title="Recent active links" rows={props.active} empty="No cached active links." />
        <BacklinkRows title="Recently lost links" rows={props.lost} empty="No cached lost links." />
      </div>
    </section>
  );
}

function BacklinkDetail(props: {
  domain: Domain;
  movement: BacklinkMovement | null;
  busy: boolean;
  onBack(): void;
  onRefresh(): Promise<void>;
  onLoad(domainId: number, args: BacklinkBrowseArgs): Promise<BacklinkPage>;
}) {
  const pageSize = 25;
  const [status, setStatus] = useState<BacklinkStatus>("all");
  const [follow, setFollow] = useState<BacklinkFollowFilter>("all");
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<BacklinkPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError("");
    const args: BacklinkBrowseArgs = { status, query: query.trim(), limit: pageSize, offset };
    if (follow !== "all") args.dofollow = follow === "follow";
    props.onLoad(props.domain.id, args)
      .then((result) => {
        if (!cancelled) setPage(result);
      })
      .catch((reason) => {
        if (!cancelled) setError((reason as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [follow, offset, props.domain.id, props.onLoad, query, reloadToken, status]);

  const total = page?.total || 0;
  const rows = page?.rows || [];
  const first = total === 0 ? 0 : offset + 1;
  const last = Math.min(offset + rows.length, total);
  const setFilter = (nextStatus: BacklinkStatus) => {
    setStatus(nextStatus);
    setOffset(0);
  };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <button type="button" className="text-sm text-text-dim hover:text-text" onClick={props.onBack}>← Back to domain</button>
          <h2 className="mt-2 text-xl font-semibold">Backlinks for {props.domain.host}</h2>
          <div className="text-sm text-text-dim">Cached links only · browsing does not call the provider</div>
        </div>
        <button
          type="button"
          className={buttonCls}
          disabled={props.busy}
          onClick={async () => {
            await props.onRefresh();
            setOffset(0);
            setReloadToken((value) => value + 1);
          }}
        >
          Refresh Links
        </button>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-px bg-border border border-border rounded overflow-hidden">
        {[
          ["All cached", props.movement ? props.movement.active_links + props.movement.lost_links : total],
          ["Active", props.movement?.active_links],
          ["Lost", props.movement?.lost_links],
          ["Provider", props.movement?.provider || page?.provider || "-"],
        ].map(([label, value]) => (
          <div key={String(label)} className="bg-bg p-3">
            <div className="text-xs text-text-dim">{label}</div>
            <div className="mt-1 text-lg font-semibold tabular-nums">{typeof value === "number" ? fmt(value) : value || "-"}</div>
          </div>
        ))}
      </div>

      <div className="border border-border rounded overflow-hidden">
        <div className="p-3 border-b border-border flex flex-wrap items-center gap-2">
          <div className="flex rounded border border-border overflow-hidden">
            {(["all", "active", "lost"] as BacklinkStatus[]).map((value) => (
              <button
                key={value}
                type="button"
                className={`px-3 py-2 text-sm capitalize ${status === value ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
                onClick={() => setFilter(value)}
              >
                {value}
              </button>
            ))}
          </div>
          <select
            className={`${inputCls} w-auto min-w-36`}
            value={follow}
            onChange={(event) => { setFollow(event.target.value as BacklinkFollowFilter); setOffset(0); }}
          >
            <option value="all">All link types</option>
            <option value="follow">Follow only</option>
            <option value="nofollow">Nofollow only</option>
          </select>
          <input
            className={`${inputCls} min-w-64 flex-1`}
            value={query}
            onChange={(event) => { setQuery(event.target.value); setOffset(0); }}
            placeholder="Search source, target, or anchor"
          />
          <div className="ml-auto text-xs text-text-dim">{first}-{last} of {fmt(total)}</div>
        </div>

        {error ? (
          <div className="p-4 text-sm text-red-300">{error}</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1100px] text-sm">
              <thead className="bg-surface-2 text-text-dim">
                <tr>
                  <th className="text-left font-medium px-3 py-2">Source page</th>
                  <th className="text-left font-medium px-3 py-2">Target</th>
                  <th className="text-left font-medium px-3 py-2">Anchor</th>
                  <th className="text-left font-medium px-3 py-2 w-32">Attributes</th>
                  <th className="text-left font-medium px-3 py-2 w-24">Authority</th>
                  <th className="text-left font-medium px-3 py-2 w-28">First seen</th>
                  <th className="text-left font-medium px-3 py-2 w-28">Last seen</th>
                </tr>
              </thead>
              <tbody>
                {loading && rows.length === 0 ? (
                  <tr><td colSpan={7} className="px-3 py-8 text-center text-text-dim">Loading cached backlinks…</td></tr>
                ) : rows.length === 0 ? (
                  <tr><td colSpan={7} className="px-3 py-8 text-center text-text-dim">No cached backlinks match these filters.</td></tr>
                ) : rows.map((row) => (
                  <tr key={`${row.provider}-${row.id}`} className="border-t border-border align-top">
                    <td className="px-3 py-3 max-w-72">
                      <a href={row.source_url} target="_blank" rel="noreferrer" className="font-medium hover:underline block truncate" title={row.source_url}>
                        {hostFromURL(row.source_url) || row.source_url}
                      </a>
                      <div className="mt-1 text-xs text-text-dim truncate" title={row.source_url}>{row.source_url}</div>
                      <div className="mt-1 text-xs text-text-dim">{row.provider} · {row.is_lost ? <span className="text-red-300">lost</span> : <span className="text-emerald-300">active</span>}</div>
                    </td>
                    <td className="px-3 py-3 max-w-64">
                      <a href={row.dest_url} target="_blank" rel="noreferrer" className="hover:underline block truncate" title={row.dest_url}>{row.dest_url}</a>
                    </td>
                    <td className="px-3 py-3 max-w-80 text-text-dim" title={row.anchor || ""}>{row.anchor || "No anchor"}</td>
                    <td className="px-3 py-3 text-xs">
                      <div>{row.is_dofollow ? "follow" : "nofollow"}</div>
                      {row.is_ugc ? <div className="text-text-dim">ugc</div> : null}
                      {row.is_sponsored ? <div className="text-text-dim">sponsored</div> : null}
                    </td>
                    <td className="px-3 py-3 tabular-nums">{fmt(row.source_authority)}</td>
                    <td className="px-3 py-3 text-text-dim whitespace-nowrap">{date(row.first_seen)}</td>
                    <td className="px-3 py-3 text-text-dim whitespace-nowrap">{date(row.last_seen)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <div className="px-3 py-3 border-t border-border flex items-center justify-between gap-3">
          <div className="text-xs text-text-dim">{loading ? "Loading…" : `${first}-${last} of ${fmt(total)} cached links`}</div>
          <div className="flex gap-2">
            <button type="button" className={buttonCls} disabled={loading || offset === 0} onClick={() => setOffset(Math.max(0, offset - pageSize))}>Previous</button>
            <button type="button" className={buttonCls} disabled={loading || offset + rows.length >= total} onClick={() => setOffset(offset + pageSize)}>Next</button>
          </div>
        </div>
      </div>
    </div>
  );
}

function BacklinkRows(props: { title: string; rows: Backlink[]; empty: string }) {
  return (
    <div className="min-w-0">
      <div className="px-4 py-2 text-sm font-medium border-b border-border">{props.title}</div>
      {props.rows.length === 0 ? (
        <div className="p-4 text-sm text-text-dim">{props.empty}</div>
      ) : (
        <div className="divide-y divide-border">
          {props.rows.slice(0, 10).map((row) => (
            <div key={row.id} className="px-4 py-3 min-w-0">
              <div className="flex items-center justify-between gap-3">
                <a href={row.source_url} target="_blank" rel="noreferrer" className="text-sm font-medium truncate hover:underline">{hostFromURL(row.source_url) || row.source_url}</a>
                <span className="text-xs text-text-dim shrink-0">{row.is_dofollow ? "follow" : "nofollow"}</span>
              </div>
              <div className="mt-1 text-xs text-text-dim truncate">{row.anchor || "No anchor"} · authority {fmt(row.source_authority)}</div>
              <div className="mt-1 text-xs text-text-dim">First {date(row.first_seen)} · Last {date(row.last_seen)}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function RankingExplorer(props: {
  rankings: Ranking[];
  selectedURL: string | null;
  pageSerpRows: SearchRanking[];
  onSelectURL(url: string): void;
  keywordById: Map<number, Keyword>;
}) {
  const pages = useMemo(() => pageSummaries(props.rankings), [props.rankings]);
  const selected = pages.find((p) => p.url === props.selectedURL) || pages[0];
  const selectedKeywordIds = new Set(selected?.keywordIds || []);
  const selectedRows = selected?.rows || [];
  const competingRows = props.pageSerpRows
    .filter((r) => r.keyword_id !== undefined && selectedKeywordIds.has(r.keyword_id))
    .sort((a, b) => ((a.keyword_id || 0) - (b.keyword_id || 0)) || ((a.rank || 9999) - (b.rank || 9999)));

  if (pages.length === 0) {
    return (
      <div className="border border-dashed border-border rounded p-5 text-sm text-text-dim">
        No cached rankings for this domain yet. Once ranked keyword or SERP data is ingested, ranked pages will appear here.
      </div>
    );
  }

  return (
    <div className="border border-border rounded overflow-hidden">
      <div className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Ranked Pages</h3>
          <div className="text-xs text-text-dim">{pages.length} page{pages.length === 1 ? "" : "s"} with cached rankings</div>
        </div>
        {selected && <div className="text-xs text-text-dim">Updated {date(selected.latestTs)}</div>}
      </div>
      <div className="grid grid-cols-1 xl:grid-cols-[360px_minmax(0,1fr)] min-h-[360px]">
        <div className="border-r border-border overflow-auto max-h-[520px]">
          {pages.map((page) => (
            <button
              key={page.url}
              type="button"
              onClick={() => props.onSelectURL(page.url)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${selected?.url === page.url ? "bg-surface-2" : ""}`}
            >
              <div className="text-sm font-medium truncate">{pagePath(page.url)}</div>
              <div className="text-xs text-text-dim mt-1 flex items-center gap-3">
                <span>{page.keywordIds.length} keyword{page.keywordIds.length === 1 ? "" : "s"}</span>
                <span>best #{page.bestRank || "-"}</span>
              </div>
            </button>
          ))}
        </div>
        <div className="min-w-0 overflow-auto max-h-[520px]">
          {selected && (
            <div>
              <div className="px-4 py-3 border-b border-border">
                <div className="text-sm font-medium truncate">{selected.url}</div>
                <div className="text-xs text-text-dim mt-1">Click a page on the left to compare its keyword positions.</div>
              </div>
              <table className="w-full text-sm">
                <thead className="sticky top-0 bg-bg border-b border-border text-text-dim">
                  <tr>
                    <th className="text-left font-medium px-4 py-2 w-20">Rank</th>
                    <th className="text-left font-medium px-3 py-2">Keyword</th>
                    <th className="text-left font-medium px-3 py-2">Page</th>
                    <th className="text-left font-medium px-3 py-2">SERP URLs</th>
                  </tr>
                </thead>
                <tbody>
                  {selectedRows.map((row) => {
                    const keyword = props.keywordById.get(row.keyword_id)?.text || `Keyword #${row.keyword_id}`;
                    const serpRows = competingRows.filter((r) => r.keyword_id === row.keyword_id).slice(0, 8);
                    return (
                      <tr key={row.id} className="border-b border-border/70 align-top">
                        <td className="px-4 py-3 font-medium tabular-nums">#{row.rank || "-"}</td>
                        <td className="px-3 py-3 min-w-44">{keyword}</td>
                        <td className="px-3 py-3 text-text-dim max-w-xs truncate">{pagePath(row.rank_url || selected.url)}</td>
                        <td className="px-3 py-3 min-w-80">
                          {serpRows.length === 0 ? (
                            <span className="text-text-dim">No keyword SERP rows cached.</span>
                          ) : (
                            <div className="space-y-1">
                              {serpRows.map((r) => (
                                <div key={r.id} className={cleanURL(r.url) === cleanURL(row.rank_url) ? "text-text" : "text-text-dim"}>
                                  <span className="tabular-nums">#{r.rank || "-"}</span>{" "}
                                  <span>{hostFromURL(r.url) || r.channel_title || r.identifier || "Result"}</span>{" "}
                                  <span className="truncate inline-block max-w-[360px] align-bottom">{pagePath(r.url)}</span>
                                </div>
                              ))}
                            </div>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function SeedView(props: {
  locations: SEOLocation[];
  activity: string[];
  metricJobs: KeywordMetricJob[];
  busy: boolean;
  onSync(): Promise<void>;
  onResume(job: KeywordMetricJob): Promise<void>;
  onSeed(payload: {
    host: string;
    label: string;
    locationId: number;
    keywords: string[];
    refresh: boolean;
  }): Promise<void>;
}) {
  const [host, setHost] = useState("");
  const [label, setLabel] = useState("");
  const [keywordText, setKeywordText] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const [refresh, setRefresh] = useState(false);
  const selected = locationId === "" ? undefined : props.locations.find((l) => l.id === Number(locationId));
  const keywords = keywordText.split(/\n|,/).map((s) => s.trim()).filter(Boolean);
  return (
    <div className="flex-1 min-h-0 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_420px]">
      <div className="p-6 overflow-auto">
        <div className="max-w-3xl space-y-5">
          <div className="flex items-center justify-between gap-3">
            <h2 className="text-xl font-semibold">Seed</h2>
            <button type="button" className={buttonCls} onClick={props.onSync} disabled={props.busy}>
              Sync Locations
            </button>
          </div>
          <form
            className="space-y-4"
            onSubmit={async (e) => {
              e.preventDefault();
              if (locationId === "") return;
              await props.onSeed({ host, label, locationId: Number(locationId), keywords, refresh });
            }}
          >
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <label className="block">
                <span className="block text-xs text-text-dim mb-1">Locale</span>
                <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
                  <option value="">Select locale</option>
                  {props.locations.map((l) => <option key={l.id} value={l.id}>{localeLabel(l)}</option>)}
                </select>
              </label>
              <label className="block">
                <span className="block text-xs text-text-dim mb-1">Domain</span>
                <input className={inputCls} value={host} onChange={(e) => setHost(e.target.value)} placeholder="domain.com" />
              </label>
            </div>
            <label className="block">
              <span className="block text-xs text-text-dim mb-1">Label</span>
              <input className={inputCls} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Brand or project name" />
            </label>
            <label className="block">
              <span className="block text-xs text-text-dim mb-1">Keywords</span>
              <textarea className={`${inputCls} min-h-40 resize-y`} value={keywordText} onChange={(e) => setKeywordText(e.target.value)} placeholder="one keyword per line" />
            </label>
            <div className="flex flex-wrap items-center justify-between gap-3 border border-border rounded p-3">
              <label className="inline-flex items-center gap-2 text-sm">
                <input type="checkbox" checked={refresh} onChange={(e) => setRefresh(e.target.checked)} />
                <span>Refresh after seeding</span>
              </label>
              <div className="text-xs text-text-dim">
                {selected ? localeLabel(selected) : "No locale"} - {keywords.length} keyword{keywords.length === 1 ? "" : "s"}
              </div>
            </div>
            <div className="flex gap-2">
              <button type="submit" className={primaryBtn} disabled={props.busy || locationId === "" || (!host.trim() && keywords.length === 0)}>
                Seed
              </button>
              <button type="button" className={buttonCls} disabled={props.busy} onClick={() => { setHost(""); setLabel(""); setKeywordText(""); }}>
                Clear
              </button>
            </div>
          </form>
          {props.metricJobs.length > 0 && (
            <div className="border-t border-border pt-4 space-y-3">
              <div className="flex items-center justify-between gap-3">
                <h3 className="text-sm font-medium">Keyword metric refreshes</h3>
                <span className="text-xs text-text-dim">Volume and difficulty</span>
              </div>
              {props.metricJobs.slice(0, 5).map((job) => {
                const fieldsDone = job.volume_completed + job.difficulty_completed;
                const totalFields = Math.max(1, job.total_keywords * 2);
                const progress = Math.min(100, Math.round((fieldsDone / totalFields) * 100));
                const resumable = job.status === "partial" || job.status === "failed";
                return (
                  <div key={job.id} className="border border-border rounded p-3 space-y-2">
                    <div className="flex items-center justify-between gap-3 text-sm">
                      <span>Job {job.id} - {job.status}</span>
                      <span className="text-xs text-text-dim">{progress}%</span>
                    </div>
                    <div className="h-1.5 bg-surface-2 overflow-hidden">
                      <div className="h-full bg-accent" style={{ width: `${progress}%` }} />
                    </div>
                    <div className="flex items-center justify-between gap-3 text-xs text-text-dim">
                      <span>Volume {job.volume_completed}/{job.total_keywords} - Difficulty {job.difficulty_completed}/{job.total_keywords}</span>
                      {resumable && (
                        <button type="button" className={buttonCls} disabled={props.busy} onClick={() => props.onResume(job)}>
                          Resume missing
                        </button>
                      )}
                    </div>
                    {job.last_error && <div className="text-xs text-red-300 whitespace-pre-wrap">{job.last_error}</div>}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>
      <div className="border-l border-border min-h-0 overflow-auto">
        <ActivityFeed rows={props.activity} />
      </div>
    </div>
  );
}

function KeywordsView(props: {
  keywords: Keyword[];
  locations: SEOLocation[];
  domains: Domain[];
  entities: SearchEntity[];
  selected: Keyword | null;
  metrics: KeywordMetrics | null;
  serpResults: SearchRanking[];
  rankTrackers: RankTracker[];
  rankHistory: Record<number, RankObservation[]>;
  trackingSettings: RankTrackingSettings;
  searchEngine: SearchEngine;
  onSelect(k: Keyword): void;
  onAdd(text: string, locationId: number): Promise<void>;
  onRemove(id: number): Promise<void>;
  onRefresh(k: Keyword): Promise<void>;
  onRefreshAll(): Promise<void>;
  onRefreshSERP(k: Keyword): Promise<void>;
  onEnableTracking(k: Keyword, target: string, frequency: RefreshFrequency): Promise<void>;
  onChangeTrackingFrequency(k: Keyword, trackerId: number, frequency: RefreshFrequency): Promise<void>;
  onDisableTracking(k: Keyword, trackerId: number): Promise<void>;
  onSaveTrackingSettings(settings: RankTrackingSettings): Promise<void>;
  locationById: Map<number, SEOLocation>;
  busy: boolean;
}) {
  const [text, setText] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const [filter, setFilter] = useState("");
  const [showAdd, setShowAdd] = useState(false);
  const selectedLoc = props.selected ? props.locationById.get(props.selected.location_id) : undefined;
  const visibleKeywords = props.keywords.filter((keyword) => keyword.text.toLowerCase().includes(filter.trim().toLowerCase()));
  const selectedSerpRows = props.selected
    ? props.serpResults.filter((r) => r.keyword_text === props.selected!.text)
    : [];

  useEffect(() => {
    if (props.locations.length === 0) {
      if (locationId !== "") setLocationId("");
      return;
    }
    if (locationId !== "" && !props.locations.some((l) => l.id === Number(locationId))) {
      setLocationId("");
    }
  }, [locationId, props.locations]);

  return (
    <div className="flex-1 min-h-0 flex">
      <div className="w-96 border-r border-border flex flex-col min-h-0">
        <div className="p-4 border-b border-border space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div><div className="font-medium">Tracked keywords</div><div className="text-xs text-text-dim">{fmt(props.keywords.length)} {props.searchEngine} keywords</div></div>
            <button type="button" className={primaryBtn} onClick={() => setShowAdd((value) => !value)}>{showAdd ? "Cancel" : "Add keyword"}</button>
          </div>
          <input className={inputCls} value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Find a keyword" />
        </div>
        {showAdd && (
        <form
          className="p-4 border-b border-border space-y-2 bg-surface-1"
          onSubmit={async (e) => {
            e.preventDefault();
            if (locationId === "") return;
            await props.onAdd(text, Number(locationId));
            setText("");
            setShowAdd(false);
          }}
        >
          <input className={inputCls} value={text} onChange={(e) => setText(e.target.value)} placeholder={`${props.searchEngine} keyword`} />
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">Locale</option>
            {props.locations.map((l) => <option key={l.id} value={l.id}>{localeLabel(l)}</option>)}
          </select>
          <button type="submit" className={primaryBtn} disabled={props.busy || !text.trim() || locationId === ""}>Add Keyword</button>
        </form>
        )}
        {props.searchEngine === "google" && props.keywords.length > 0 && (
          <div className="px-4 py-2 border-b border-border flex items-center justify-between gap-3">
            <span className="text-xs text-text-dim">Bulk action may use provider credit.</span>
            <button type="button" className={buttonCls} disabled={props.busy} onClick={props.onRefreshAll}>Refresh metrics</button>
          </div>
        )}
        <div className="flex-1 overflow-auto">
          {visibleKeywords.length === 0 && <div className="p-6 text-center text-sm text-text-dim">No keywords match.</div>}
          {visibleKeywords.map((k) => (
            <button
              key={k.id}
              type="button"
              onClick={() => props.onSelect(k)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${props.selected?.id === k.id ? "bg-surface-2" : ""}`}
            >
              <div className="font-medium truncate">{k.text}</div>
              <div className="text-xs text-text-dim truncate">{localeLabel(props.locationById.get(k.location_id))}</div>
            </button>
          ))}
        </div>
      </div>
      <div className="flex-1 min-w-0 p-6 overflow-auto">
        {props.selected ? (
          <div className="space-y-6">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h2 className="text-xl font-semibold">{props.selected.text}</h2>
                <div className="text-sm text-text-dim">{localeLabel(selectedLoc)}</div>
              </div>
              <div className="flex gap-2">
                {props.searchEngine === "google" && (
                  <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!)}>Refresh Metrics</button>
                )}
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefreshSERP(props.selected!)}>Refresh SERP</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRemove(props.selected!.id)}>Remove</button>
              </div>
            </div>
            {props.searchEngine === "google" ? (
              <>
                <MetricGrid
                  rows={[
                    ["Volume", fmt(props.metrics?.volume)],
                    ["Difficulty", fmt(props.metrics?.difficulty)],
                    ["CPC", props.metrics?.cpc_usd === undefined ? "-" : `$${props.metrics.cpc_usd.toFixed(2)}`],
                    ["Rank trackers", fmt(props.rankTrackers.filter((tracker) => tracker.enabled).length)],
                  ]}
                />
                <div className="text-xs text-text-dim">Last metrics refresh: {date(props.metrics?.ts)}</div>
                <RankTrackingPanel
                  keyword={props.selected}
                  domains={props.domains}
                  entities={props.entities.filter((entity) => entity.search_engine === props.searchEngine)}
                  trackers={props.rankTrackers}
                  history={props.rankHistory}
                  settings={props.trackingSettings}
                  busy={props.busy}
                  onEnable={props.onEnableTracking}
                  onChangeFrequency={props.onChangeTrackingFrequency}
                  onDisable={props.onDisableTracking}
                  onSaveSettings={props.onSaveTrackingSettings}
                />
                <SERPResultsTable rows={selectedSerpRows} />
              </>
            ) : (
              <SERPResultsTable rows={selectedSerpRows} />
            )}
          </div>
        ) : (
          <div className="text-sm text-text-dim">No keyword selected.</div>
        )}
      </div>
    </div>
  );
}

function RankTrackingPanel(props: {
  keyword: Keyword;
  domains: Domain[];
  entities: SearchEntity[];
  trackers: RankTracker[];
  history: Record<number, RankObservation[]>;
  settings: RankTrackingSettings;
  busy: boolean;
  onEnable(keyword: Keyword, target: string, frequency: RefreshFrequency): Promise<void>;
  onChangeFrequency(keyword: Keyword, trackerId: number, frequency: RefreshFrequency): Promise<void>;
  onDisable(keyword: Keyword, trackerId: number): Promise<void>;
  onSaveSettings(settings: RankTrackingSettings): Promise<void>;
}) {
  const [target, setTarget] = useState("");
  const [frequency, setFrequency] = useState<RefreshFrequency>("daily");
  const [budget, setBudget] = useState(props.settings.monthly_budget_usd);
  const targets = [
    ...props.domains.map((domain) => ({ value: `domain:${domain.id}`, label: domain.label || domain.host })),
    ...props.entities.map((entity) => ({ value: `entity:${entity.id}`, label: entity.label || entity.identifier })),
  ];

  useEffect(() => {
    setBudget(props.settings.monthly_budget_usd);
  }, [props.settings.monthly_budget_usd]);

  useEffect(() => {
    if (target && !targets.some((item) => item.value === target)) setTarget("");
  }, [target, targets]);

  return (
    <section className="border border-border rounded overflow-hidden">
      <div className="px-4 py-3 border-b border-border flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">Rank History</h3>
          <div className="text-xs text-text-dim">Automatic checks scan the top {props.settings.daily_depth}; daily trackers scan the top {props.settings.weekly_depth} on Sundays.</div>
        </div>
        <div className="flex items-center gap-2">
          <label className="text-xs text-text-dim">Monthly cap $</label>
          <input
            className={`${inputCls} w-24`}
            type="number"
            min="0"
            step="0.5"
            value={budget}
            onChange={(event) => setBudget(Math.max(0, Number(event.target.value) || 0))}
          />
          <button
            type="button"
            className={buttonCls}
            disabled={props.busy || budget === props.settings.monthly_budget_usd}
            onClick={() => props.onSaveSettings({ ...props.settings, monthly_budget_usd: budget })}
          >Save cap</button>
        </div>
      </div>
      <div className="p-4 border-b border-border flex flex-wrap gap-2">
        <select className={`${inputCls} max-w-md`} value={target} onChange={(event) => setTarget(event.target.value)}>
          <option value="">Choose a domain or entity</option>
          {targets.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
        </select>
        <select className={`${inputCls} w-32`} value={frequency} onChange={(event) => setFrequency(event.target.value as RefreshFrequency)}>
          <option value="daily">Daily</option>
          <option value="weekly">Weekly</option>
          <option value="monthly">Monthly</option>
        </select>
        <button
          type="button"
          className={primaryBtn}
          disabled={props.busy || !target}
          onClick={async () => { await props.onEnable(props.keyword, target, frequency); setTarget(""); }}
        >Track</button>
      </div>
      {props.trackers.length === 0 ? (
        <div className="p-4 text-sm text-text-dim">No automatic target tracking for this keyword.</div>
      ) : (
        <div className="divide-y divide-border">
          {props.trackers.map((tracker) => {
            const observations = props.history[tracker.id] || [];
            const latest = observations[0];
            return (
              <div key={tracker.id} className="p-4 space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <div className="text-sm font-medium">{tracker.entity_label || tracker.entity_identifier}</div>
                    <div className="text-xs text-text-dim">
                      {tracker.enabled ? `Next check ${date(tracker.next_run_at)}` : "Disabled · history retained"}
                      {tracker.last_error ? ` · ${tracker.last_error}` : ""}
                    </div>
                  </div>
                  <div className="flex items-center gap-3">
                    <select
                      className={`${inputCls} w-28 text-xs`}
                      value={tracker.frequency}
                      disabled={props.busy || !tracker.enabled}
                      aria-label={`Refresh frequency for ${tracker.entity_label || tracker.entity_identifier}`}
                      onChange={(event) => props.onChangeFrequency(props.keyword, tracker.id, event.target.value as RefreshFrequency)}
                    >
                      <option value="daily">Daily</option>
                      <option value="weekly">Weekly</option>
                      <option value="monthly">Monthly</option>
                    </select>
                    <span className="text-sm font-semibold tabular-nums">
                      {!latest ? "No checks yet" : latest.found ? `#${latest.rank}` : `Not in top ${latest.checked_depth}`}
                    </span>
                    {tracker.enabled && (
                      <button type="button" className={buttonCls} disabled={props.busy} onClick={() => props.onDisable(props.keyword, tracker.id)}>Disable</button>
                    )}
                  </div>
                </div>
                {observations.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {observations.slice(0, 31).reverse().map((observation) => (
                      <span
                        key={observation.id}
                        title={`${observation.observed_date}: ${observation.found ? `#${observation.rank}` : `not found in top ${observation.checked_depth}`}`}
                        className={`px-2 py-1 rounded text-xs tabular-nums ${observation.found ? "bg-surface-2 text-text" : "bg-red-500/10 text-red-300"}`}
                      >{observation.found ? `#${observation.rank}` : "—"}</span>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
      <div className="px-4 py-2 border-t border-border text-xs text-text-dim">
        Trackers are opt-in and keep history when disabled or rescheduled. One provider query is shared by every target using the same keyword, locale, device, and day.
      </div>
    </section>
  );
}

function LocationsView({ locations, searchEngine, activity, onSync, busy }: { locations: SEOLocation[]; searchEngine: SearchEngine; activity: string[]; onSync(): Promise<void>; busy: boolean }) {
  const [filter, setFilter] = useState("");
  const rows = locations
    .filter((l) => localeLabel(l).toLowerCase().includes(filter.toLowerCase()))
    .filter((l) => !searchEngine || l.search_engine === searchEngine)
    .slice(0, 500);
  return (
    <div className="flex-1 min-h-0 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_420px]">
      <div className="min-h-0 flex flex-col">
        <div className="px-6 py-4 border-b border-border flex items-center gap-3">
          <input className={`${inputCls} max-w-sm`} value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter locales" />
          <button type="button" className={primaryBtn} onClick={onSync} disabled={busy}>Sync Locations</button>
          <span className="text-xs text-text-dim">{fmt(rows.length)} {searchEngine} locales</span>
        </div>
        <div className="flex-1 overflow-auto">
          <table className="w-full text-sm">
            <thead className="sticky top-0 bg-bg border-b border-border text-text-dim">
              <tr>
                <th className="text-left font-medium px-6 py-2">Location</th>
                <th className="text-left font-medium px-3 py-2">Country</th>
                <th className="text-left font-medium px-3 py-2">Language</th>
                <th className="text-left font-medium px-3 py-2">Engine</th>
                <th className="text-right font-medium px-6 py-2">Code</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((l) => (
                <tr key={l.id} className="border-b border-border/70 hover:bg-surface-2">
                  <td className="px-6 py-2">{l.location_name}</td>
                  <td className="px-3 py-2 text-text-dim">{l.country_iso || "-"}</td>
                  <td className="px-3 py-2 text-text-dim">{l.language_name || l.language_code}</td>
                  <td className="px-3 py-2 text-text-dim">{l.search_engine}</td>
                  <td className="px-6 py-2 text-right text-text-dim">{l.location_code || "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      <div className="border-l border-border min-h-0 overflow-auto">
        <ActivityFeed rows={activity} />
      </div>
    </div>
  );
}

function ActivityFeed({ rows }: { rows: string[] }) {
  return (
    <div className="p-4">
      <h3 className="text-sm font-semibold mb-3">Session Activity</h3>
      <div className="space-y-2">
        {rows.length === 0 && <div className="text-sm text-text-dim">No actions in this session.</div>}
        {rows.map((row, idx) => (
          <div key={`${idx}-${row}`} className="border border-border rounded p-3 text-sm">
            {row}
          </div>
        ))}
      </div>
    </div>
  );
}

function MetricGrid({ rows }: { rows: [string, string][] }) {
  return (
    <div className="grid grid-cols-2 lg:grid-cols-3 gap-px border border-border bg-border">
      {rows.map(([label, value]) => (
        <div key={label} className="bg-bg p-4 min-w-0">
          <div className="text-xs text-text-dim truncate">{label}</div>
          <div className="mt-1 text-xl font-semibold truncate">{value}</div>
        </div>
      ))}
    </div>
  );
}
