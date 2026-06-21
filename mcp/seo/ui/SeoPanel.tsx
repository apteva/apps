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

interface PageRankingSummary {
  url: string;
  keywordIds: number[];
  bestRank?: number;
  latestTs?: number;
  rows: Ranking[];
}

type View = "seed" | "domains" | "keywords" | "locations";

const API = "/api/apps/seo";
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
    const existing =
      map.get(url) ||
      ({
        url,
        keywordIds: [],
        rows: [],
      } satisfies PageRankingSummary);
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
  const [view, setView] = useState<View>("seed");
  const [locations, setLocations] = useState<SEOLocation[]>([]);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [keywords, setKeywords] = useState<Keyword[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<Domain | null>(null);
  const [selectedKeyword, setSelectedKeyword] = useState<Keyword | null>(null);
  const [domainMetrics, setDomainMetrics] = useState<DomainMetrics | null>(null);
  const [keywordMetrics, setKeywordMetrics] = useState<KeywordMetrics | null>(null);
  const [domainRankings, setDomainRankings] = useState<Ranking[]>([]);
  const [selectedRankURL, setSelectedRankURL] = useState<string | null>(null);
  const [pageSerpRows, setPageSerpRows] = useState<Ranking[]>([]);
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
    const rows = await callTool<Keyword[]>("keywords_list", { limit: 300 });
    setKeywords(rows || []);
    setSelectedKeyword((cur) => cur || rows?.[0] || null);
  }, [callTool]);

  const reloadAll = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      await Promise.all([reloadLocations(), reloadDomains(), reloadKeywords()]);
      setStatus("Updated");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [reloadDomains, reloadKeywords, reloadLocations]);

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
    Promise.all(ids.map((id) => callTool<Ranking[]>("rankings_for_keyword", { keyword_id: id, limit: 100 })))
      .then((groups) => setPageSerpRows(groups.flat()))
      .catch((e) => setErr((e as Error).message));
  }, [callTool, domainRankings, selectedRankURL]);

  useEffect(() => {
    if (!selectedKeyword) {
      setKeywordMetrics(null);
      return;
    }
    callTool<{ keyword: Keyword; metrics: KeywordMetrics | null }>("keywords_get", { id: selectedKeyword.id })
      .then((r) => setKeywordMetrics(r.metrics || null))
      .catch((e) => setErr((e as Error).message));
  }, [callTool, selectedKeyword]);

  const locationById = useMemo(() => {
    const m = new Map<number, SEOLocation>();
    for (const l of locations) m.set(l.id, l);
    return m;
  }, [locations]);

  const defaultLocation = locations[0];

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
      await api<Record<string, unknown>>("POST", backlinks ? `/domains/${domain.id}/backlinks/refresh` : `/domains/${domain.id}/refresh`, params);
      await reloadDomains();
      setSelectedDomain(domain);
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
      setStatus("Keyword refreshed");
      pushActivity(`Keyword refreshed: ${keyword.text}`);
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
            {(["seed", "domains", "keywords", "locations"] as View[]).map((v) => (
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

      {view === "seed" && (
        <SeedView
          locations={locations}
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
                });
              }
              const keywordRows: Keyword[] = [];
              for (const text of payload.keywords) {
                keywordRows.push(await callTool<Keyword>("keywords_add", {
                  text,
                  location_id: payload.locationId,
                }));
              }
              await Promise.all([reloadDomains(), reloadKeywords()]);
              if (domain) setSelectedDomain(domain);
              if (keywordRows[0]) setSelectedKeyword(keywordRows[0]);
              if (payload.refresh && domain) {
                await refreshDomain(domain);
              }
              if (payload.refresh) {
                for (const keyword of keywordRows) {
                  await refreshKeyword(keyword);
                }
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

      {view === "domains" && (
        <DomainsView
          domains={domains}
          locations={locations}
          selected={selectedDomain}
          metrics={domainMetrics}
          rankings={domainRankings}
          pageSerpRows={pageSerpRows}
          selectedRankURL={selectedRankURL}
          onSelectRankURL={setSelectedRankURL}
          keywords={keywords}
          allDomains={domains}
          onSelect={setSelectedDomain}
          onAdd={async (host, label, locationId) => {
            const d = await callTool<Domain>("domains_add", { host, label, location_id: locationId });
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

      {view === "keywords" && (
        <KeywordsView
          keywords={keywords}
          locations={locations}
          selected={selectedKeyword}
          metrics={keywordMetrics}
          onSelect={setSelectedKeyword}
          onAdd={async (text, locationId) => {
            const k = await callTool<Keyword>("keywords_add", { text, location_id: locationId });
            await reloadKeywords();
            setSelectedKeyword(k);
          }}
          onRemove={async (id) => {
            await callTool("keywords_remove", { id });
            setSelectedKeyword(null);
            await reloadKeywords();
          }}
          onRefresh={refreshKeyword}
          locationById={locationById}
          busy={busy}
        />
      )}

      {view === "locations" && (
        <LocationsView locations={locations} activity={activity} onSync={syncLocations} busy={busy} />
      )}
    </div>
  );
}

function DomainsView(props: {
  domains: Domain[];
  allDomains: Domain[];
  keywords: Keyword[];
  locations: SEOLocation[];
  selected: Domain | null;
  metrics: DomainMetrics | null;
  rankings: Ranking[];
  pageSerpRows: Ranking[];
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
  const domainById = useMemo(() => new Map(props.allDomains.map((d) => [d.id, d])), [props.allDomains]);
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
              domainById={domainById}
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
  pageSerpRows: Ranking[];
  onSelectURL(url: string): void;
  keywordById: Map<number, Keyword>;
  domainById: Map<number, Domain>;
}) {
  const pages = useMemo(() => pageSummaries(props.rankings), [props.rankings]);
  const selected = pages.find((p) => p.url === props.selectedURL) || pages[0];
  const selectedKeywordIds = new Set(selected?.keywordIds || []);
  const selectedRows = selected?.rows || [];
  const competingRows = props.pageSerpRows
    .filter((r) => selectedKeywordIds.has(r.keyword_id))
    .sort((a, b) => (a.keyword_id - b.keyword_id) || ((a.rank || 9999) - (b.rank || 9999)));

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
                                <div key={r.id} className={r.rank_url === row.rank_url ? "text-text" : "text-text-dim"}>
                                  <span className="tabular-nums">#{r.rank || "-"}</span>{" "}
                                  <span>{props.domainById.get(r.domain_id)?.host || hostFromURL(r.rank_url) || `Domain #${r.domain_id}`}</span>{" "}
                                  <span className="truncate inline-block max-w-[360px] align-bottom">{pagePath(r.rank_url)}</span>
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
  const keywords = keywordText
    .split(/\n|,/)
    .map((s) => s.trim())
    .filter(Boolean);
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
              await props.onSeed({
                host,
                label,
                locationId: Number(locationId),
                keywords,
                refresh,
              });
            }}
          >
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <label className="block">
                <span className="block text-xs text-text-dim mb-1">Locale</span>
                <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
                  <option value="">Select locale</option>
                  {props.locations.map((l) => (
                    <option key={l.id} value={l.id}>{localeLabel(l)}</option>
                  ))}
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
              <textarea
                className={`${inputCls} min-h-40 resize-y`}
                value={keywordText}
                onChange={(e) => setKeywordText(e.target.value)}
                placeholder="one keyword per line"
              />
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
              <button
                type="submit"
                className={primaryBtn}
                disabled={props.busy || locationId === "" || (!host.trim() && keywords.length === 0)}
              >
                Seed
              </button>
              <button
                type="button"
                className={buttonCls}
                disabled={props.busy}
                onClick={() => {
                  setHost("");
                  setLabel("");
                  setKeywordText("");
                }}
              >
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
  onSelect(k: Keyword): void;
  onAdd(text: string, locationId: number): Promise<void>;
  onRemove(id: number): Promise<void>;
  onRefresh(k: Keyword): Promise<void>;
  locationById: Map<number, SEOLocation>;
  busy: boolean;
}) {
  const [text, setText] = useState("");
  const [locationId, setLocationId] = useState<number | "">("");
  const selectedLoc = props.selected ? props.locationById.get(props.selected.location_id) : undefined;
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
          <input className={inputCls} value={text} onChange={(e) => setText(e.target.value)} placeholder="keyword" />
          <select className={inputCls} value={locationId} onChange={(e) => setLocationId(e.target.value ? Number(e.target.value) : "")}>
            <option value="">Locale</option>
            {props.locations.map((l) => (
              <option key={l.id} value={l.id}>{localeLabel(l)}</option>
            ))}
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
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRefresh(props.selected!)}>Refresh Metrics</button>
                <button className={buttonCls} disabled={props.busy} onClick={() => props.onRemove(props.selected!.id)}>Remove</button>
              </div>
            </div>
            <MetricGrid
              rows={[
                ["Volume", fmt(props.metrics?.volume)],
                ["Difficulty", fmt(props.metrics?.difficulty)],
                ["CPC", props.metrics?.cpc_usd === undefined ? "-" : `$${props.metrics.cpc_usd.toFixed(2)}`],
                ["Provider", props.metrics?.provider || "-"],
              ]}
            />
            <div className="text-xs text-text-dim">Last refresh: {date(props.metrics?.ts)}</div>
          </div>
        ) : (
          <div className="text-sm text-text-dim">No keyword selected.</div>
        )}
      </div>
    </div>
  );
}

function LocationsView({ locations, activity, onSync, busy }: { locations: SEOLocation[]; activity: string[]; onSync(): Promise<void>; busy: boolean }) {
  const [filter, setFilter] = useState("");
  const rows = locations.filter((l) => localeLabel(l).toLowerCase().includes(filter.toLowerCase())).slice(0, 500);
  return (
    <div className="flex-1 min-h-0 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_420px]">
      <div className="min-h-0 flex flex-col">
        <div className="px-6 py-4 border-b border-border flex items-center gap-3">
          <input className={`${inputCls} max-w-sm`} value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter locales" />
          <button type="button" className={primaryBtn} onClick={onSync} disabled={busy}>Sync DataForSEO</button>
          <span className="text-xs text-text-dim">{fmt(locations.length)} locales</span>
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
