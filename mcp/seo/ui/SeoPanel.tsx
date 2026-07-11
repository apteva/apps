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

interface KeywordMetrics {
  id: number;
  location_id: number;
  provider: string;
  ts: number;
  volume?: number;
  difficulty?: number;
  cpc_usd?: number;
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
type View = "seed" | "domains" | "keywords" | "discover" | "entities" | "locations";

const API = "/api/apps/seo";
const engines: { id: SearchEngine; label: string }[] = [
  { id: "google", label: "Google" },
  { id: "youtube", label: "YouTube" },
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

function engineViews(engine: SearchEngine): View[] {
  return engine === "google"
    ? ["seed", "domains", "keywords", "discover", "entities", "locations"]
    : ["discover", "entities", "keywords", "locations"];
}

export default function SeoPanel({ projectId, installId }: NativePanelProps) {
  const [searchEngine, setSearchEngine] = useState<SearchEngine>("google");
  const [view, setView] = useState<View>("seed");
  const [locations, setLocations] = useState<SEOLocation[]>([]);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [keywords, setKeywords] = useState<Keyword[]>([]);
  const [entities, setEntities] = useState<SearchEntity[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<Domain | null>(null);
  const [selectedKeyword, setSelectedKeyword] = useState<Keyword | null>(null);
  const [selectedEntity, setSelectedEntity] = useState<SearchEntity | null>(null);
  const [domainMetrics, setDomainMetrics] = useState<DomainMetrics | null>(null);
  const [keywordMetrics, setKeywordMetrics] = useState<KeywordMetrics | null>(null);
  const [domainRankings, setDomainRankings] = useState<Ranking[]>([]);
  const [entityRankings, setEntityRankings] = useState<SearchRanking[]>([]);
  const [selectedRankURL, setSelectedRankURL] = useState<string | null>(null);
  const [pageSerpRows, setPageSerpRows] = useState<SearchRanking[]>([]);
  const [serpResults, setSerpResults] = useState<SearchRanking[]>([]);
  const [keywordIdeas, setKeywordIdeas] = useState<KeywordIdea[]>([]);
  const [opportunities, setOpportunities] = useState<ContentOpportunity[]>([]);
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
    async <T,>(method: string, path: string, params: Record<string, string> = {}, body?: unknown): Promise<T> => {
      const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
      if (body !== undefined) {
        (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
        opts.body = JSON.stringify(body);
      }
      const res = await fetch(`${API}${path}?${withParams(params)}`, opts);
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json();
    },
    [withParams],
  );

  const callTool = useCallback(
    async <T,>(tool: string, args: Record<string, unknown> = {}): Promise<T> => {
      return api<T>("POST", "/tools/call", {}, { tool, args: { ...args, _project_id: projectId } });
    },
    [api, projectId],
  );

  const reloadLocations = useCallback(async () => {
    const resp = await api<{ locations: SEOLocation[] }>("GET", "/locations", { limit: "500" });
    setLocations(resp.locations || []);
  }, [api]);

  const reloadDomains = useCallback(async () => {
    const rows = await callTool<Domain[]>("domains_list");
    setDomains(rows || []);
    setSelectedDomain((cur) => cur || rows?.[0] || null);
  }, [callTool]);

  const reloadKeywords = useCallback(async () => {
    const rows = await callTool<Keyword[]>("keywords_list", { search_engine: searchEngine, limit: 300 });
    setKeywords(rows || []);
    setSelectedKeyword((cur) => (cur && rows?.some((r) => r.id === cur.id) ? cur : rows?.[0] || null));
  }, [callTool, searchEngine]);

  const reloadEntities = useCallback(async () => {
    const rows = await callTool<SearchEntity[]>("entities_list", { search_engine: searchEngine, limit: 300 });
    setEntities(rows || []);
    setSelectedEntity((cur) => (cur && rows?.some((r) => r.id === cur.id) ? cur : rows?.[0] || null));
  }, [callTool, searchEngine]);

  const reloadOpportunities = useCallback(async () => {
    const resp = await callTool<{ items: ContentOpportunity[] }>("content_opportunities", {
      search_engine: searchEngine,
      limit: 25,
    });
    setOpportunities(resp.items || []);
  }, [callTool, searchEngine]);

  const reloadAll = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      await Promise.all([reloadLocations(), reloadDomains(), reloadKeywords(), reloadEntities(), reloadOpportunities()]);
      setStatus("Updated");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [reloadDomains, reloadEntities, reloadKeywords, reloadLocations, reloadOpportunities]);

  useEffect(() => {
    if (!engineViews(searchEngine).includes(view)) {
      setView(searchEngine === "youtube" ? "discover" : "seed");
    }
    setSelectedKeyword(null);
    setSelectedEntity(null);
    setSerpResults([]);
    setKeywordIdeas([]);
  }, [searchEngine, view]);

  useEffect(() => {
    reloadAll();
  }, [reloadAll]);

  useEffect(() => {
    if (!selectedDomain) {
      setDomainMetrics(null);
      setDomainRankings([]);
      setSelectedRankURL(null);
      return;
    }
    callTool<{ domain: Domain; metrics: DomainMetrics | null }>("domains_get", { id: selectedDomain.id })
      .then((r) => setDomainMetrics(r.metrics || null))
      .catch((e) => setErr((e as Error).message));
    callTool<Ranking[]>("rankings_for_domain", { domain_id: selectedDomain.id, limit: 500 })
      .then((rows) => {
        setDomainRankings(rows || []);
        setSelectedRankURL(null);
      })
      .catch((e) => setErr((e as Error).message));
  }, [callTool, selectedDomain]);

  useEffect(() => {
    if (!selectedRankURL && domainRankings.length > 0) {
      const first = pageSummaries(domainRankings)[0];
      if (first) setSelectedRankURL(first.url);
    }
  }, [domainRankings, selectedRankURL]);

  useEffect(() => {
    if (!selectedRankURL) {
      setPageSerpRows([]);
      return;
    }
    const ids = Array.from(
      new Set(domainRankings.filter((r) => cleanURL(r.rank_url) === selectedRankURL).map((r) => r.keyword_id)),
    );
    if (ids.length === 0) {
      setPageSerpRows([]);
      return;
    }
    callTool<SearchRanking[]>("rankings_for_keywords", { keyword_ids: ids, limit: 1000 })
      .then((rows) => setPageSerpRows(rows || []))
      .catch((e) => setErr((e as Error).message));
  }, [callTool, domainRankings, selectedRankURL]);

  useEffect(() => {
    if (!selectedKeyword) {
      setKeywordMetrics(null);
      setSerpResults([]);
      return;
    }
    callTool<{ keyword: Keyword; metrics: KeywordMetrics | null }>("keywords_get", { id: selectedKeyword.id })
      .then((r) => setKeywordMetrics(r.metrics || null))
      .catch((e) => setErr((e as Error).message));
    callTool<SearchRanking[]>("rankings_for_keyword", { keyword_id: selectedKeyword.id, limit: 100 })
      .then((rows) => setSerpResults(rows || []))
      .catch((e) => setErr((e as Error).message));
  }, [callTool, searchEngine, selectedKeyword]);

  useEffect(() => {
    if (!selectedEntity) {
      setEntityRankings([]);
      return;
    }
    callTool<SearchRanking[]>("rankings_for_entity", { entity_id: selectedEntity.id, limit: 200 })
      .then((rows) => setEntityRankings(rows || []))
      .catch((e) => setErr((e as Error).message));
  }, [callTool, selectedEntity]);

  const filteredLocations = useMemo(
    () => locations.filter((l) => l.search_engine === searchEngine),
    [locations, searchEngine],
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
      const r = await api<Record<string, unknown>>("POST", "/locations/sync");
      await reloadLocations();
      setStatus(`Synced ${fmt(Number(r.rows_upserted || 0))} locales`);
      pushActivity(`Synced ${fmt(Number(r.rows_upserted || 0))} locales`);
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
      const loc = domain.default_location_id || defaultLocation?.id;
      const params = loc ? { location_id: String(loc) } : {};
      await api<Record<string, unknown>>(
        "POST",
        backlinks ? `/domains/${domain.id}/backlinks/refresh` : `/domains/${domain.id}/refresh`,
        params,
      );
      await reloadDomains();
      setSelectedDomain(domain);
      if (!backlinks) {
        const [detail, rows] = await Promise.all([
          callTool<{ domain: Domain; metrics: DomainMetrics | null }>("domains_get", { id: domain.id }),
          callTool<Ranking[]>("rankings_for_domain", { domain_id: domain.id, limit: 500 }),
        ]);
        setDomainMetrics(detail.metrics || null);
        setDomainRankings(rows || []);
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
      });
      await reloadKeywords();
      setSelectedKeyword(keyword);
      const detail = await callTool<{ keyword: Keyword; metrics: KeywordMetrics | null }>("keywords_get", { id: keyword.id });
      setKeywordMetrics(detail.metrics || null);
      setStatus("Keyword refreshed");
      pushActivity(`Keyword refreshed: ${keyword.text}`);
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

  async function runSerp(keyword: string, locationId: number, depth: number) {
    setBusy(true);
    setErr("");
    try {
      const resp = await callTool<{ results: SearchRanking[]; count: number }>("serp_search", {
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
      <div className="px-6 pt-5 pb-3 border-b border-border flex items-center justify-between gap-4">
        <div className="flex items-center gap-4 min-w-0">
          <h1 className="text-lg font-semibold shrink-0">SEO</h1>
          <div className="flex rounded border border-border overflow-hidden text-xs">
            {engines.map((engine) => (
              <button
                key={engine.id}
                type="button"
                onClick={() => setSearchEngine(engine.id)}
                className={`px-3 py-1.5 ${searchEngine === engine.id ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
              >
                {engine.label}
              </button>
            ))}
          </div>
          <div className="flex rounded border border-border overflow-hidden text-xs">
            {engineViews(searchEngine).map((v) => (
              <button
                key={v}
                type="button"
                onClick={() => setView(v)}
                className={`px-3 py-1.5 capitalize ${view === v ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
              >
                {v}
              </button>
            ))}
          </div>
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading...</span>}
          {status && !busy && <span>{status}</span>}
          <button type="button" className={buttonCls} onClick={reloadAll} disabled={busy}>
            Refresh
          </button>
        </div>
      </div>

      {err && (
        <div className="mx-6 mt-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">
          {err}
        </div>
      )}

      {view === "seed" && searchEngine === "google" && (
        <SeedView
          locations={filteredLocations}
          activity={activity}
          busy={busy}
          onSync={syncLocations}
          onSeed={async (payload) => {
            setBusy(true);
            setErr("");
            try {
              let domain: Domain | null = null;
              if (payload.host.trim()) {
                domain = await callTool<Domain>("domains_add", {
                  host: payload.host,
                  label: payload.label,
                  location_id: payload.locationId,
                  search_engine: "google",
                });
              }
              const keywordRows: Keyword[] = [];
              for (const text of payload.keywords) {
                keywordRows.push(await callTool<Keyword>("keywords_add", {
                  text,
                  location_id: payload.locationId,
                  search_engine: "google",
                }));
              }
              await Promise.all([reloadDomains(), reloadKeywords()]);
              if (domain) setSelectedDomain(domain);
              if (keywordRows[0]) setSelectedKeyword(keywordRows[0]);
              if (payload.refresh && domain) await refreshDomain(domain);
              if (payload.refresh) {
                for (const keyword of keywordRows) await refreshKeyword(keyword);
              }
              setStatus(`Seeded ${domain ? 1 : 0} domain, ${keywordRows.length} keywords`);
              pushActivity(`Seeded ${domain ? 1 : 0} domain and ${keywordRows.length} keywords`);
            } catch (e) {
              setErr((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        />
      )}

      {view === "domains" && searchEngine === "google" && (
        <DomainsView
          domains={domains}
          locations={filteredLocations}
          selected={selectedDomain}
          metrics={domainMetrics}
          rankings={domainRankings}
          pageSerpRows={pageSerpRows}
          selectedRankURL={selectedRankURL}
          onSelectRankURL={setSelectedRankURL}
          keywords={keywords}
          onSelect={setSelectedDomain}
          onAdd={async (host, label, locationId) => {
            const d = await callTool<Domain>("domains_add", { host, label, location_id: locationId, search_engine: "google" });
            await reloadDomains();
            setSelectedDomain(d);
          }}
          onRemove={async (id) => {
            await callTool("domains_remove", { id });
            setSelectedDomain(null);
            await reloadDomains();
          }}
          onRefresh={refreshDomain}
          locationById={locationById}
          busy={busy}
        />
      )}

      {view === "discover" && (
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
      )}

      {view === "entities" && (
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

      {view === "keywords" && (
        <KeywordsView
          keywords={keywords}
          locations={filteredLocations}
          selected={selectedKeyword}
          metrics={keywordMetrics}
          serpResults={serpResults}
          searchEngine={searchEngine}
          onSelect={setSelectedKeyword}
          onAdd={async (text, locationId) => {
            const k = await callTool<Keyword>("keywords_add", { text, location_id: locationId, search_engine: searchEngine });
            await reloadKeywords();
            setSelectedKeyword(k);
          }}
          onRemove={async (id) => {
            await callTool("keywords_remove", { id });
            setSelectedKeyword(null);
            await reloadKeywords();
          }}
          onRefresh={refreshKeyword}
          onRefreshSERP={refreshKeywordSERP}
          locationById={locationById}
          busy={busy}
        />
      )}

      {view === "locations" && (
        <LocationsView
          locations={locations}
          searchEngine={searchEngine}
          activity={activity}
          onSync={syncLocations}
          busy={busy}
        />
      )}
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
              Sync DataForSEO
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
  rankings: Ranking[];
  pageSerpRows: SearchRanking[];
  selectedRankURL: string | null;
  onSelectRankURL(url: string): void;
  onSelect(d: Domain): void;
  onAdd(host: string, label: string, locationId?: number): Promise<void>;
  onRemove(id: number): Promise<void>;
  onRefresh(d: Domain, backlinks?: boolean): Promise<void>;
  locationById: Map<number, SEOLocation>;
  busy: boolean;
}) {
  const [host, setHost] = useState("");
  const [label, setLabel] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const selectedLoc = props.selected?.default_location_id
    ? props.locationById.get(props.selected.default_location_id)
    : undefined;
  const keywordById = useMemo(() => new Map(props.keywords.map((k) => [k.id, k])), [props.keywords]);
  return (
    <div className="flex-1 min-h-0 flex">
      <div className="w-80 border-r border-border flex flex-col min-h-0">
        <form
          className="p-4 border-b border-border space-y-2"
          onSubmit={async (e) => {
            e.preventDefault();
            await props.onAdd(host, label, locationId === "" ? undefined : Number(locationId));
            setHost("");
            setLabel("");
          }}
        >
          <input className={inputCls} value={host} onChange={(e) => setHost(e.target.value)} placeholder="domain.com" />
          <input className={inputCls} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Label" />
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">No default locale</option>
            {props.locations.map((l) => (
              <option key={l.id} value={l.id}>{localeLabel(l)}</option>
            ))}
          </select>
          <button type="submit" className={primaryBtn} disabled={props.busy || !host.trim()}>Add Domain</button>
        </form>
        <div className="flex-1 overflow-auto">
          {props.domains.map((d) => (
            <button
              key={d.id}
              type="button"
              onClick={() => props.onSelect(d)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${props.selected?.id === d.id ? "bg-surface-2" : ""}`}
            >
              <div className="font-medium truncate">{d.host}</div>
              <div className="text-xs text-text-dim truncate">{d.label || localeLabel(d.default_location_id ? props.locationById.get(d.default_location_id) : undefined)}</div>
            </button>
          ))}
        </div>
      </div>
      <div className="flex-1 min-w-0 p-6 overflow-auto">
        {props.selected ? (
          <div className="space-y-6">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h2 className="text-xl font-semibold">{props.selected.host}</h2>
                <div className="text-sm text-text-dim">{localeLabel(selectedLoc)}</div>
              </div>
              <div className="flex gap-2">
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!)}>Refresh Metrics</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!, true)}>Refresh Links</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRemove(props.selected!.id)}>Remove</button>
              </div>
            </div>
            <MetricGrid
              rows={[
                ["Organic traffic", fmt(props.metrics?.organic_traffic)],
                ["Organic keywords", fmt(props.metrics?.organic_keywords)],
                ["Paid traffic", fmt(props.metrics?.paid_traffic)],
                ["Paid keywords", fmt(props.metrics?.paid_keywords)],
                ["Backlinks", fmt(props.metrics?.backlinks_count)],
                ["Referring domains", fmt(props.metrics?.referring_domains_count)],
              ]}
            />
            <div className="text-xs text-text-dim">Last refresh: {date(props.metrics?.ts)}</div>
            <RankingExplorer
              rankings={props.rankings}
              selectedURL={props.selectedRankURL}
              pageSerpRows={props.pageSerpRows}
              onSelectURL={props.onSelectRankURL}
              keywordById={keywordById}
            />
          </div>
        ) : (
          <div className="text-sm text-text-dim">No domain selected.</div>
        )}
      </div>
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
  busy: boolean;
  onSync(): Promise<void>;
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
              Sync DataForSEO
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
  selected: Keyword | null;
  metrics: KeywordMetrics | null;
  serpResults: SearchRanking[];
  searchEngine: SearchEngine;
  onSelect(k: Keyword): void;
  onAdd(text: string, locationId: number): Promise<void>;
  onRemove(id: number): Promise<void>;
  onRefresh(k: Keyword): Promise<void>;
  onRefreshSERP(k: Keyword): Promise<void>;
  locationById: Map<number, SEOLocation>;
  busy: boolean;
}) {
  const [text, setText] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const selectedLoc = props.selected ? props.locationById.get(props.selected.location_id) : undefined;
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
      <div className="w-80 border-r border-border flex flex-col min-h-0">
        <form
          className="p-4 border-b border-border space-y-2"
          onSubmit={async (e) => {
            e.preventDefault();
            if (locationId === "") return;
            await props.onAdd(text, Number(locationId));
            setText("");
          }}
        >
          <input className={inputCls} value={text} onChange={(e) => setText(e.target.value)} placeholder={`${props.searchEngine} keyword`} />
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">Locale</option>
            {props.locations.map((l) => <option key={l.id} value={l.id}>{localeLabel(l)}</option>)}
          </select>
          <button type="submit" className={primaryBtn} disabled={props.busy || !text.trim() || locationId === ""}>Add Keyword</button>
        </form>
        <div className="flex-1 overflow-auto">
          {props.keywords.map((k) => (
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
                    ["Provider", props.metrics?.provider || "-"],
                  ]}
                />
                <div className="text-xs text-text-dim">Last metrics refresh: {date(props.metrics?.ts)}</div>
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
          <button type="button" className={primaryBtn} onClick={onSync} disabled={busy}>Sync DataForSEO</button>
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
