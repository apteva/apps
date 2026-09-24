import { useCallback, useEffect, useState, type ReactNode } from "react";

const API = "/api/apps/content-catalog";
type Brand = { id: string; slug: string; name: string; storage_root: string; host_provider: string; host_connection_id: number; host_library_id: string; host_collection_id: string };
type Session = { id: string; brand_id: string; title: string; session_date: string; status: string; notes: string };
type Asset = { id: string; session_id: string; storage_install_id: number; storage_file_id: string; name: string; kind: string; content_type: string; size_bytes: number; review_status: string; media_status: string; media_rating: string };
type Target = { id: string; destination: string; account_ref: string; current_status: string; planned_at: string; asset_ids: string[] };
type Release = { id: string; brand_id: string; title: string; phase: string; audience: string; planned_at: string; approval_status: string; targets: Target[] };
type PublicationObservation = { id: string; status: string; external_post_id: string; external_url: string; actual_at: string; evidence_source: string; failure_details: string; observed_at: string };
type Hosting = { id: string; provider: string; remote_id: string; status: string; embed_url: string; error: string };
type AssetUse = { release_id: string; release_title: string; target_id: string; destination: string; status: string };
type AssetSource = { asset_id: string; relation: string; source_order: number; media_render_id: number };
type GigLink = { gigs_install_id: number; gig_id: number; role: string };
type MediaDetails = { title?: string; description?: string; duration_ms?: number; width?: number; height?: number; audience_rating?: string; transcript_status?: string; probe_status?: string };
type Host = { connection_id: number; provider: string; default: boolean };
type ImportCandidate = { storage_file_id: string; storage_install_id: number; name: string; folder: string; content_type: string; linked_session_ids: string[]; needs_review: boolean };
type SearchUse = AssetUse & { account_ref: string; external_url: string; external_post_id: string; actual_at: string };
type AssetHit = Asset & { brand_id: string; session_title: string; session_date: string; is_derivative: boolean; uses: SearchUse[]; match_reason: string };
type SessionHit = Session & { brand_name: string; asset_count: number };
type ReleaseHit = Release & { brand_name: string; target_count: number; sort_date: string };
type SearchPage<T> = { items: T[]; next_cursor?: string };
type SearchResults = { assets: SearchPage<AssetHit>; sessions: SearchPage<SessionHit>; releases: SearchPage<ReleaseHit> };
const emptySearch = (): SearchResults => ({ assets: { items: [] }, sessions: { items: [] }, releases: { items: [] } });

function errorText(err: unknown): string { return err instanceof Error ? err.message : String(err); }
function short(id: string): string { return id.slice(0, 8); }
function storageFileURL(projectId: string, installId: number, fileId: string): string { return `/api/apps/storage/files/${encodeURIComponent(fileId)}/content?${new URLSearchParams({ project_id: projectId, install_id: String(installId) })}`; }
function storageContentURL(projectId: string, asset: Asset): string { return storageFileURL(projectId, asset.storage_install_id, asset.storage_file_id); }
function previewURL(projectId: string, kind: "sessions" | "assets", id: string): string { return `${API}/${kind}/${encodeURIComponent(id)}/preview?${new URLSearchParams({ project_id: projectId })}`; }
function mediaGlyph(kind: string): string { return kind === "video" ? "▶" : kind === "audio" ? "♫" : kind === "image" ? "▣" : "▤"; }
function assetMediaKind(asset: Asset): string { return asset.content_type.startsWith("image/") ? "image" : asset.content_type.startsWith("video/") ? "video" : asset.content_type.startsWith("audio/") ? "audio" : asset.kind; }
function publicationLabel(status: string): string {
  return ({ verified_published: "Verified live", provider_reported_published: "Reported live", scheduled: "Scheduled", submitted: "Submitted", failed: "Failed", removed: "Removed", unknown: "Unverified", planned: "Planned" } as Record<string, string>)[status] || status.replaceAll("_", " ");
}
function publicationColor(status: string): string {
  if (status === "verified_published") return "#22c55e";
  if (status === "provider_reported_published") return "#86efac";
  if (status === "failed" || status === "removed") return "#f87171";
  if (status === "scheduled" || status === "submitted") return "#fbbf24";
  return "#a1a1aa";
}

function ReleaseUseList({ uses }: { uses: AssetUse[] }) {
  if (!uses.length) return <p className="text-xs text-text-muted">No release yet</p>;
  return <div className="space-y-2" aria-label="Release destinations">{uses.map(use => <div key={use.target_id} className="text-xs"><div className="flex flex-wrap items-center justify-between gap-x-2"><span className="min-w-0 truncate">{use.destination}</span><span style={{ color: publicationColor(use.status) }}>{publicationLabel(use.status)}</span></div><div className="truncate text-text-muted" title={use.release_title}>{use.release_title}</div></div>)}</div>;
}

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);
  return <div role="presentation" style={{ position: "fixed", inset: 0, zIndex: 1000, display: "flex", alignItems: "center", justifyContent: "center", padding: 20, background: "rgba(0, 0, 0, .72)" }} onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
    <section role="dialog" aria-modal="true" aria-label={title} className="rounded-xl border border-border bg-bg p-4 shadow-2xl" style={{ width: "min(100%, 560px)", maxHeight: "90vh", overflowY: "auto" }}>
      <div className="mb-2 flex justify-end"><button type="button" className="rounded border border-border px-2 py-1 text-sm" onClick={onClose} aria-label="Close dialog">Close</button></div>
      {children}
    </section>
  </div>;
}

function PreviewImage({ src, alt, fallback, className = "", style }: { src: string; alt: string; fallback: string; className?: string; style?: { width?: number; height?: number; flexShrink?: number } }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => { setFailed(false); }, [src]);
  return <div className={`relative overflow-hidden bg-gradient-to-br from-accent/15 via-bg-input to-bg ${className}`} style={style}>{failed ? <div className="absolute inset-0 flex items-center justify-center text-3xl text-text-muted" aria-label="No preview available">{fallback}</div> : <img src={src} alt={alt} loading="lazy" className="absolute inset-0 h-full w-full object-cover" onError={() => setFailed(true)} />}</div>;
}

function AssetViewer({ asset, projectId }: { asset: Asset; projectId: string }) {
  const src = storageContentURL(projectId, asset);
  const poster = previewURL(projectId, "assets", asset.id);
  const kind = assetMediaKind(asset);
  return <div className="rounded-xl border border-border bg-black/90 overflow-hidden">
    {kind === "image" ? <a href={src} target="_blank" rel="noreferrer" title="Open full image"><img src={src} alt={asset.name} className="max-h-[560px] w-full object-contain" /></a>
      : kind === "video" ? <video key={src} src={src} poster={poster} controls preload="metadata" playsInline className="max-h-[560px] w-full bg-black" aria-label={`Play ${asset.name}`} />
      : kind === "audio" ? <div className="p-6 space-y-4"><PreviewImage src={poster} alt={`Waveform for ${asset.name}`} fallback="♫" className="h-28 rounded-lg" /><audio key={src} src={src} controls preload="metadata" className="w-full" aria-label={`Play ${asset.name}`} /></div>
      : asset.content_type === "application/pdf" ? <iframe src={src} title={`Preview ${asset.name}`} className="h-[560px] w-full bg-white" />
      : <div className="flex h-40 items-center justify-center text-5xl text-text-muted" aria-label="No inline preview">{mediaGlyph(kind)}</div>}
    <div className="flex items-center justify-between gap-3 border-t border-border bg-bg p-3 text-xs"><span className="min-w-0 truncate text-text-muted">{asset.content_type || asset.kind}{asset.size_bytes ? ` · ${(asset.size_bytes / 1024 / 1024).toFixed(1)} MB` : ""}</span><a href={src} target="_blank" rel="noreferrer" className="shrink-0 text-accent underline">Open original ↗</a></div>
  </div>;
}

function CandidateViewer({ candidate, projectId }: { candidate: ImportCandidate; projectId: string }) {
  const src = storageFileURL(projectId, candidate.storage_install_id, candidate.storage_file_id);
  return <div className="space-y-2 rounded-lg bg-bg-input/40 p-3">{candidate.content_type.startsWith("image/") ? <img src={src} alt={candidate.name} loading="lazy" className="max-h-64 w-full rounded object-contain" /> : candidate.content_type.startsWith("video/") ? <video src={src} controls preload="metadata" playsInline className="max-h-64 w-full rounded bg-black" /> : candidate.content_type.startsWith("audio/") ? <audio src={src} controls preload="metadata" className="w-full" /> : candidate.content_type === "application/pdf" ? <iframe src={src} title={`Preview ${candidate.name}`} className="h-64 w-full bg-white" /> : <p className="text-text-muted">No inline preview for this file type.</p>}<a href={src} target="_blank" rel="noreferrer" className="text-accent underline">Open original ↗</a></div>;
}

export default function ContentCatalogPanel({ projectId, installId }: { projectId: string; installId: number }) {
  const [tab, setTab] = useState<"brands" | "sessions" | "search" | "releases">("sessions");
  const [returnTab, setReturnTab] = useState<"sessions" | "search">("sessions");
  const [searchText, setSearchText] = useState("");
  const [searchType, setSearchType] = useState("all");
  const [searchBrand, setSearchBrand] = useState("");
  const [searchDateFrom, setSearchDateFrom] = useState("");
  const [searchDateTo, setSearchDateTo] = useState("");
  const [searchKind, setSearchKind] = useState("");
  const [searchLineage, setSearchLineage] = useState("");
  const [searchSort, setSearchSort] = useState("session_newest");
  const [searchReview, setSearchReview] = useState("");
  const [searchDestination, setSearchDestination] = useState("");
  const [searchAccount, setSearchAccount] = useState("");
  const [searchAvailability, setSearchAvailability] = useState("any");
  const [searchResults, setSearchResults] = useState<SearchResults>(emptySearch);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [searchReload, setSearchReload] = useState(0);
  const [overview, setOverview] = useState<Record<string, number>>({});
  const [brands, setBrands] = useState<Brand[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [releases, setReleases] = useState<Release[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [selectedSession, setSelectedSession] = useState<Session | null>(null);
  const [assets, setAssets] = useState<Asset[]>([]);
  const [gigs, setGigs] = useState<GigLink[]>([]);
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);
  const [assetSources, setAssetSources] = useState<AssetSource[]>([]);
  const [hostings, setHostings] = useState<Hosting[]>([]);
  const [assetUses, setAssetUses] = useState<AssetUse[]>([]);
  const [assetMedia, setAssetMedia] = useState<MediaDetails | null>(null);
  const [candidates, setCandidates] = useState<ImportCandidate[]>([]);
  const [importLimitReached, setImportLimitReached] = useState(false);
  const [selectedRelease, setSelectedRelease] = useState<Release | null>(null);
  const [observations, setObservations] = useState<Record<string, PublicationObservation[]>>({});
  const [modal, setModal] = useState<"new-session" | "add-file" | "link-gig" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const query = useCallback((extra: Record<string, string> = {}) => new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(), [projectId, installId]);
  const get = useCallback(async <T,>(path: string, extra: Record<string, string> = {}): Promise<T> => {
    const res = await fetch(`${API}${path}?${query(extra)}`, { credentials: "same-origin" });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }, [query]);
  const action = useCallback(async <T,>(tool: string, args: Record<string, unknown>): Promise<T> => {
    const res = await fetch(`${API}/action?${query()}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tool, args }) });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }, [query]);
  const refresh = useCallback(async () => {
    const [o, b, s, r, h] = await Promise.all([
      get<Record<string, number>>("/overview"), get<{ brands: Brand[] }>("/brands"),
      get<{ sessions: Session[] }>("/sessions"), get<{ releases: Release[] }>("/releases"),
      get<{ hosts: Host[] }>("/video-hosts"),
    ]);
    setOverview(o); setBrands(b.brands || []); setSessions(s.sessions || []); setReleases(r.releases || []); setHosts(h.hosts || []); setSearchReload(n => n + 1);
  }, [get]);
  useEffect(() => { refresh().catch(e => setError(errorText(e))); }, [refresh]);
  const run = useCallback(async (task: () => Promise<void>, message: string) => {
    setBusy(true); setError(""); setNotice("");
    try { await task(); await refresh(); setNotice(message); } catch (e) { setError(errorText(e)); } finally { setBusy(false); }
  }, [refresh]);
  const openSession = useCallback(async (s: Session) => {
    setSelectedSession(s); setSelectedAsset(null); setCandidates([]); setError("");
    try { const result = await get<{ assets: Asset[]; gigs: GigLink[] }>(`/sessions/${s.id}`); setAssets(result.assets || []); setGigs(result.gigs || []); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const showSession = useCallback(async (s: Session, origin: "sessions" | "search" = "sessions") => {
    const url = new URL(window.location.href);
    url.searchParams.set("catalog_session", s.id);
    window.history.pushState(null, "", url);
    setReturnTab(origin); setTab("sessions");
    setModal(null);
    await openSession(s);
  }, [openSession]);
  const backToSessions = useCallback(() => {
    const url = new URL(window.location.href);
    url.searchParams.delete("catalog_session");
    window.history.replaceState(null, "", url);
    setSelectedSession(null); setSelectedAsset(null); setModal(null); setTab(returnTab);
  }, [returnTab]);
  useEffect(() => {
    const syncFromURL = () => {
      const id = new URLSearchParams(window.location.search).get("catalog_session");
      if (!id) { setSelectedSession(null); setSelectedAsset(null); return; }
      const session = sessions.find(s => s.id === id);
      if (session) openSession(session);
    };
    window.addEventListener("popstate", syncFromURL);
    syncFromURL();
    return () => window.removeEventListener("popstate", syncFromURL);
  }, [sessions, openSession]);
  const previewImport = useCallback(async (s: Session) => {
    try { const result = await get<{ candidates: ImportCandidate[]; limit_reached: boolean }>("/import-preview", { session_id: s.id }); setCandidates(result.candidates || []); setImportLimitReached(result.limit_reached); setError(""); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const openAsset = useCallback(async (asset: Asset) => {
    setSelectedAsset(asset); setAssetMedia(null); setAssetSources([]); setAssetUses([]); setHostings([]); setError("");
    try { const result = await get<{ hostings: Hosting[]; sources: AssetSource[]; uses: AssetUse[]; media?: MediaDetails; media_error?: string }>(`/assets/${asset.id}`); setHostings(result.hostings || []); setAssetSources(result.sources || []); setAssetUses(result.uses || []); setAssetMedia(result.media || null); if (result.media_error) setError(`Media unavailable: ${result.media_error}`); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const openRelease = useCallback(async (release: Release) => {
    try { const result = await get<{ release: Release; observations: Record<string, PublicationObservation[]> }>(`/releases/${release.id}`); setSelectedRelease(result.release); setObservations(result.observations || {}); setError(""); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const brandName = (id: string) => brands.find(b => b.id === id)?.name || short(id);
  const usesForAsset = (id: string): AssetUse[] => releases.flatMap(release => (release.targets || []).filter(target => target.asset_ids?.includes(id)).map(target => ({ release_id: release.id, release_title: release.title, target_id: target.id, destination: target.destination, status: target.current_status })));
  const searchParams = useCallback(() => ({ entity_type: searchType, query: searchText, brand_id: searchBrand, date_from: searchDateFrom, date_to: searchDateTo, kind: searchKind, lineage: searchLineage, sort: searchSort, review_status: searchReview, destination: searchDestination, account_ref: searchAccount, availability: searchAvailability, limit: "24" }), [searchType, searchText, searchBrand, searchDateFrom, searchDateTo, searchKind, searchLineage, searchSort, searchReview, searchDestination, searchAccount, searchAvailability]);
  useEffect(() => {
    if (tab !== "search") return;
    let active = true;
    const timer = window.setTimeout(() => {
      setSearchLoading(true); setSearchError("");
      get<SearchResults>("/search", searchParams()).then(result => { if (active) setSearchResults(result); }).catch(err => { if (active) setSearchError(errorText(err)); }).finally(() => { if (active) setSearchLoading(false); });
    }, 180);
    return () => { active = false; window.clearTimeout(timer); };
  }, [tab, get, searchParams, searchReload]);
  const loadMore = useCallback(async (kind: "assets" | "sessions" | "releases", cursor: string) => {
    setSearchLoading(true); setSearchError("");
    try {
      const result = await get<SearchResults>("/search", { ...searchParams(), entity_type: kind, [`${kind}_cursor`]: cursor });
      setSearchResults(prev => ({ ...prev, [kind]: { items: [...prev[kind].items, ...result[kind].items], next_cursor: result[kind].next_cursor } }));
    } catch (err) { setSearchError(errorText(err)); } finally { setSearchLoading(false); }
  }, [get, searchParams]);
  const openSearchAsset = useCallback(async (hit: AssetHit) => {
    const result = await get<{ session: Session }>(`/sessions/${hit.session_id}`);
    await showSession(result.session, "search");
    await openAsset(hit);
  }, [get, showSession, openAsset]);
  const assetSearchOnly = !!searchKind || !!searchLineage || !!searchReview || searchAvailability !== "any";

  return <div className="h-full overflow-y-auto bg-bg text-text p-5 space-y-5">
    <header className="flex flex-wrap justify-between gap-3 items-center">
      <div><h1 className="text-xl font-semibold">Content Catalog</h1><p className="text-sm text-text-muted">Production relationships and verified publication evidence</p></div>
      <button className="border border-border rounded px-3 py-1.5 text-sm hover:bg-bg-hover" onClick={() => refresh().catch(e => setError(errorText(e)))}>Refresh</button>
    </header>
    <div className="flex flex-wrap gap-2">{["brands", "sessions", "assets", "releases", "hostings"].map(k => <span key={k} className="rounded border border-border px-3 py-1 text-sm"><strong>{overview[k] ?? 0}</strong> {k}</span>)}</div>
    {error && <div className="rounded border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-400">{error}</div>}
    {notice && <div className="rounded border border-green-500/50 bg-green-500/10 p-3 text-sm">{notice}</div>}
    <nav className="flex gap-1 border-b border-border">{(["sessions", "search", "releases", "brands"] as const).map(t => <button key={t} onClick={() => { setSelectedSession(null); setTab(t); }} className={`px-3 py-2 text-sm capitalize ${tab === t ? "border-b-2 border-accent text-accent" : "text-text-muted"}`}>{t}</button>)}</nav>

    {tab === "search" && <section className="space-y-4">
      <div><h2 className="text-lg font-semibold">Search Catalog</h2><p className="text-sm text-text-muted">Searches sessions, linked files, and releases. Storage folders are never scanned automatically.</p></div>
      <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-4">
        <input className={`${inputClass} md:col-span-2`} type="search" value={searchText} onChange={e => setSearchText(e.target.value)} placeholder="Search titles, file names, notes…" aria-label="Search Catalog" />
        <select className={inputClass} value={searchType} onChange={e => { setSearchType(e.target.value); if (e.target.value === "sessions" || e.target.value === "releases") { setSearchKind(""); setSearchLineage(""); setSearchReview(""); setSearchAvailability("any"); } }} aria-label="Result type"><option value="all">All records</option><option value="assets">Files</option><option value="sessions">Sessions</option><option value="releases">Releases</option></select>
        <select className={inputClass} value={searchBrand} onChange={e => setSearchBrand(e.target.value)} aria-label="Brand"><option value="">All brands</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select>
        <select className={inputClass} value={searchKind} disabled={searchType === "sessions" || searchType === "releases"} onChange={e => setSearchKind(e.target.value)} aria-label="File type"><option value="">All file types</option>{["video", "image", "audio", "other"].map(kind => <option key={kind} value={kind}>{kind}</option>)}</select>
        <select className={inputClass} value={searchLineage} disabled={searchType === "sessions" || searchType === "releases"} onChange={e => setSearchLineage(e.target.value)} aria-label="File lineage"><option value="">Sources and derivatives</option><option value="source">Source files</option><option value="derivative">Derivatives</option></select>
        <select className={inputClass} value={searchReview} disabled={searchType === "sessions" || searchType === "releases"} onChange={e => setSearchReview(e.target.value)} aria-label="Review status"><option value="">Any review state</option>{["pending", "approved", "rejected"].map(state => <option key={state} value={state}>{state}</option>)}</select>
        <select className={inputClass} value={searchSort} disabled={searchType === "sessions" || searchType === "releases"} onChange={e => setSearchSort(e.target.value)} aria-label="Sort files"><option value="session_newest">Newest session first</option><option value="asset_newest">Recently linked first</option></select>
        <label className="text-xs text-text-muted">From session date<input className={inputClass} type="date" value={searchDateFrom} onChange={e => setSearchDateFrom(e.target.value)} /></label>
        <label className="text-xs text-text-muted">Through session date<input className={inputClass} type="date" value={searchDateTo} onChange={e => setSearchDateTo(e.target.value)} /></label>
        <input className={inputClass} value={searchDestination} onChange={e => { setSearchDestination(e.target.value); if (!e.target.value) { setSearchAccount(""); if (searchAvailability !== "never_used") setSearchAvailability("any"); } }} placeholder="Destination, e.g. instagram" aria-label="Destination" />
        <input className={inputClass} value={searchAccount} disabled={!searchDestination} onChange={e => setSearchAccount(e.target.value)} placeholder="Account reference (optional)" aria-label="Account reference" />
        <select className={inputClass} value={searchAvailability} disabled={searchType === "sessions" || searchType === "releases"} onChange={e => setSearchAvailability(e.target.value)} aria-label="Availability"><option value="any">Any use state</option><option value="never_used">Never used</option><option value="not_published" disabled={!searchDestination}>Not published here</option><option value="ready_to_publish" disabled={!searchDestination}>Ready to publish here</option><option value="scheduled" disabled={!searchDestination}>Scheduled here</option><option value="published" disabled={!searchDestination}>Published here</option><option value="failed" disabled={!searchDestination}>Failed here</option></select>
      </div>
      <p className="text-xs text-text-muted">Ready to publish means approved and no active plan or observed post for the selected destination{searchAccount ? " and account" : ""}. Results reflect evidence recorded in Catalog.</p>
      {searchError && <p className="rounded border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-400">{searchError}</p>}
      {searchLoading && <p className="text-sm text-text-muted">Searching…</p>}
      {(searchType === "all" || searchType === "assets") && <div className="space-y-2"><h3 className="font-semibold">Files <span className="text-text-muted font-normal">{searchResults.assets.items.length}</span></h3>{searchResults.assets.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching linked files.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(230px, 1fr))", gap: 12 }}>{searchResults.assets.items.map(hit => <button key={hit.id} type="button" className="min-w-0 overflow-hidden rounded-xl border border-border text-left hover:border-accent/60" onClick={() => openSearchAsset(hit).catch(e => setSearchError(errorText(e)))}><PreviewImage src={previewURL(projectId, "assets", hit.id)} alt={`Preview of ${hit.name}`} fallback={mediaGlyph(assetMediaKind(hit))} className="aspect-video" /><div className="space-y-2 p-3 text-sm"><div className="truncate font-medium" title={hit.name}>{hit.name}</div><div className="text-xs text-text-muted">{hit.session_title} · {hit.session_date}</div><div className="text-xs text-text-muted">{brandName(hit.brand_id)} · {hit.review_status} · {hit.is_derivative ? "derivative" : "source"}</div><div className="border-t border-border pt-2"><ReleaseUseList uses={hit.uses} /></div><p className="text-xs text-text-muted">{hit.match_reason}</p></div></button>)}</div>{searchResults.assets.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("assets", searchResults.assets.next_cursor!)}>More files</button>}</div>}
      {!assetSearchOnly && !searchDestination && (searchType === "all" || searchType === "sessions") && <div className="space-y-2"><h3 className="font-semibold">Sessions <span className="text-text-muted font-normal">{searchResults.sessions.items.length}</span></h3>{searchResults.sessions.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching sessions.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12 }}>{searchResults.sessions.items.map(hit => <button key={hit.id} type="button" className="min-w-0 overflow-hidden rounded-xl border border-border text-left hover:border-accent/60" onClick={() => showSession(hit, "search")}><PreviewImage src={previewURL(projectId, "sessions", hit.id)} alt={`Preview of ${hit.title}`} fallback="▣" className="aspect-video" /><div className="p-3 text-sm"><div className="font-medium line-clamp-2">{hit.title}</div><div className="mt-1 text-xs text-text-muted">{hit.brand_name} · {hit.session_date} · {hit.asset_count} files</div></div></button>)}</div>{searchResults.sessions.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("sessions", searchResults.sessions.next_cursor!)}>More sessions</button>}</div>}
      {!assetSearchOnly && (searchType === "all" || searchType === "releases") && <div className="space-y-2"><h3 className="font-semibold">Releases <span className="text-text-muted font-normal">{searchResults.releases.items.length}</span></h3>{searchResults.releases.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching releases.</p>}<div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">{searchResults.releases.items.map(hit => <button key={hit.id} type="button" className="rounded-xl border border-border p-3 text-left text-sm hover:border-accent/60" onClick={() => { setTab("releases"); openRelease(hit).catch(e => setError(errorText(e))); }}><div className="font-medium">{hit.title}</div><div className="mt-1 text-xs text-text-muted">{hit.brand_name} · {hit.planned_at || "Unscheduled"} · {hit.target_count} destinations</div></button>)}</div>{searchResults.releases.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("releases", searchResults.releases.next_cursor!)}>More releases</button>}</div>}
    </section>}

    {tab === "brands" && <div className="grid gap-5 lg:grid-cols-2">
      <section className="space-y-3"><h2 className="font-semibold">Brands</h2>{brands.map(b => <div key={b.id} className="rounded border border-border p-3 space-y-2 text-sm"><div className="font-medium">{b.name}</div><div className="text-text-muted">{b.storage_root}</div><div>{b.host_provider ? `${b.host_provider} · connection ${b.host_connection_id} · library ${b.host_library_id}` : "No cloud host configured"}</div><EditBrand key={`${b.id}:${b.host_connection_id}:${b.host_library_id}`} brand={b} hosts={hosts} busy={busy} onSubmit={input => run(async () => { await action("content_catalog_brands_update", { id: b.id, ...input }); }, "Brand updated")} /></div>)}</section>
      <CreateBrand busy={busy} hosts={hosts} onSubmit={input => run(async () => { await action("content_catalog_brands_create", input); }, "Brand created")} />
    </div>}

    {tab === "sessions" && !selectedSession && <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="text-lg font-semibold">Sessions</h2><p className="text-sm text-text-muted">Select a session to see its assets and release history.</p></div><button type="button" className={buttonClass} onClick={() => setModal("new-session")}>+ New session</button></div>
      {sessions.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No sessions yet.</p>}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12 }}>{sessions.map(s => <button key={s.id} className="min-w-0 overflow-hidden rounded-xl border border-border text-left text-sm transition-colors hover:border-accent/60" onClick={() => showSession(s)}><PreviewImage src={previewURL(projectId, "sessions", s.id)} alt={`Preview of ${s.title}`} fallback="▣" className="aspect-video" /><div className="p-3"><div className="font-medium line-clamp-2">{s.title}</div><div className="mt-1 text-xs text-text-muted">{brandName(s.brand_id)} · {s.session_date}</div></div></button>)}</div>
    </div>}

    {tab === "sessions" && selectedSession && <div className="space-y-5">
      <div className="border-b border-border pb-4">
        <button type="button" className="mb-3 text-sm text-accent underline" onClick={backToSessions}>← All sessions</button>
        <div className="flex flex-wrap items-start justify-between gap-4"><div className="min-w-0"><h2 className="text-2xl font-semibold">{selectedSession.title}</h2><p className="mt-1 text-sm text-text-muted">{brandName(selectedSession.brand_id)} · {selectedSession.session_date} · {selectedSession.status}</p></div><div className="flex flex-wrap gap-2"><button type="button" className={buttonClass} onClick={() => setModal("add-file")}>+ Add file</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" onClick={() => setModal("link-gig")}>Link Gig</button></div></div>
      </div>
      <section className="space-y-3"><div className="flex items-center justify-between gap-3"><h3 className="text-lg font-semibold">Assets</h3><span className="text-xs text-text-muted">{assets.length} file{assets.length === 1 ? "" : "s"}</span></div><p className="text-xs text-text-muted">Each file shows its release destinations. Only “Verified live” confirms a published post.</p>
        {assets.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No files linked yet. Use Add file to link one from Storage.</p>}
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))", gap: 12 }}>{assets.map(asset => <button key={asset.id} onClick={() => openAsset(asset)} className={`min-w-0 overflow-hidden rounded-xl border text-left text-sm transition-colors hover:border-accent/60 ${selectedAsset?.id === asset.id ? "border-accent" : "border-border"}`}><PreviewImage src={previewURL(projectId, "assets", asset.id)} alt={`Preview of ${asset.name}`} fallback={mediaGlyph(assetMediaKind(asset))} className="aspect-video" /><div className="space-y-2 p-3"><div className="font-medium truncate" title={asset.name}>{asset.name}</div><div className="text-xs text-text-muted">{asset.kind} · #{asset.storage_file_id} · {asset.review_status}</div><div className="border-t border-border pt-2"><ReleaseUseList uses={usesForAsset(asset.id)} /></div></div></button>)}</div>
      </section>
      {selectedAsset && <section className="rounded-xl border border-border p-4 space-y-3"><div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold break-all">{selectedAsset.name}</h3><button type="button" className="text-sm text-accent underline" onClick={() => setSelectedAsset(null)}>Close file</button></div><AssetViewer asset={selectedAsset} projectId={projectId} />
        {assetMedia && <div className="text-sm space-y-1"><div>{assetMedia.title || "Media metadata"} · {assetMedia.probe_status || "unknown"} · {assetMedia.audience_rating || "unrated"}</div>{assetMedia.description && <p className="text-text-muted">{assetMedia.description}</p>}<div className="text-xs text-text-muted">{assetMedia.duration_ms ? `${Math.round(assetMedia.duration_ms / 1000)}s · ` : ""}{assetMedia.width && assetMedia.height ? `${assetMedia.width} × ${assetMedia.height} · ` : ""}Transcript: {assetMedia.transcript_status || "not available"}</div></div>}
        <div className="text-xs text-text-muted">Sources: {assetSources.length ? assetSources.map(s => `${short(s.asset_id)} (${s.relation})`).join(", ") : "original / none linked"}</div>
        <div className="flex flex-wrap gap-2 text-sm">{(["pending", "approved", "rejected"] as const).map(state => <button disabled={busy || selectedAsset.review_status === state} key={state} className="border border-border rounded px-2 py-1 disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_assets_review", { asset_id: selectedAsset.id, review_status: state }); await openSession(selectedSession); setSelectedAsset({ ...selectedAsset, review_status: state }); }, `Review set to ${state}`)}>{state}</button>)}</div>
        <button disabled={busy || selectedAsset.review_status !== "approved"} className="rounded border border-accent px-3 py-1.5 text-sm disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_hosting_request", { asset_id: selectedAsset.id }); await openAsset(selectedAsset); }, "Hosting request recorded")}>Host approved asset</button>
        <div className="space-y-2">{hostings.map(h => <div key={h.id} className="rounded border border-border p-2 text-sm"><span className="font-medium">{h.provider}: {h.status}</span>{h.remote_id && <span className="ml-2 text-text-muted">{short(h.remote_id)}</span>}{h.embed_url && <a className="ml-2 text-accent underline" href={h.embed_url} target="_blank" rel="noreferrer">Open host</a>}{h.error && <p className="text-red-400">{h.error}</p>}{h.remote_id && h.status !== "ready" && <button className="text-accent underline" onClick={() => run(async () => { await action("content_catalog_hosting_check", { id: h.id }); await openAsset(selectedAsset); }, "Host checked")}>Check readiness</button>}</div>)}</div>
        <div className="text-sm"><h4 className="font-medium">Release destinations</h4><ReleaseUseList uses={assetUses.length ? assetUses : usesForAsset(selectedAsset.id)} /></div>
      </section>}
      <section className="rounded-xl border border-border p-4 space-y-2 text-sm"><h3 className="font-semibold">Session info</h3>{selectedSession.notes && <p className="text-text-muted">{selectedSession.notes}</p>}<p className="text-text-muted">Gigs: {gigs.length ? gigs.map(g => `#${g.gig_id}${g.role ? ` (${g.role})` : ""}`).join(", ") : "none linked"}</p></section>
    </div>}

    {tab === "releases" && <div className="grid gap-5 xl:grid-cols-[300px_minmax(0,1fr)]"><aside className="space-y-3"><CreateRelease busy={busy} brands={brands} onSubmit={input => run(async () => { await action("content_catalog_releases_create", input); }, "Release planned")} /><h2 className="font-semibold">Release plans</h2>{releases.map(r => <button key={r.id} onClick={() => openRelease(r)} className={`block w-full text-left rounded border p-3 text-sm ${selectedRelease?.id === r.id ? "border-accent" : "border-border"}`}><div className="font-medium">{r.title}</div><div className="text-text-muted">{brandName(r.brand_id)} · {r.planned_at || "Unscheduled"}</div></button>)}</aside>
      <section className="space-y-4">{selectedRelease ? <><div className="rounded border border-border p-4"><h2 className="font-semibold">{selectedRelease.title}</h2><p className="text-sm text-text-muted">{brandName(selectedRelease.brand_id)} · {selectedRelease.phase || "No phase"} · {selectedRelease.audience || "No audience"}</p></div><AddTarget key={selectedRelease.id} busy={busy} sessions={sessions.filter(s => s.brand_id === selectedRelease.brand_id)} loadAssets={async id => (await get<{ assets: Asset[] }>("/assets", { session_id: id })).assets} onSubmit={input => run(async () => { await action("content_catalog_release_targets_add", { release_id: selectedRelease.id, ...input }); await openRelease(selectedRelease); }, "Destination added")} /><h3 className="font-semibold">Destinations and evidence</h3>{selectedRelease.targets?.map(t => <div key={t.id} className="rounded border border-border p-4 space-y-2"><div className="font-medium">{t.destination} <span className="text-text-muted font-normal">{t.account_ref}</span></div><div className="text-sm">Planned: {t.planned_at || "—"} · Observed: {t.current_status}</div><div className="text-xs text-text-muted">{t.asset_ids?.length || 0} assets</div>{(observations[t.id] || []).map(o => <div key={o.id} className="border-l-2 border-border pl-2 text-xs text-text-muted"><span className="font-medium text-text">{o.status.replaceAll("_", " ")}</span> · {o.evidence_source} · {o.actual_at || o.observed_at}{o.external_url && <a className="ml-2 text-accent underline" href={o.external_url} target="_blank" rel="noreferrer">View post</a>}{o.external_post_id && <span className="ml-2">ID {o.external_post_id}</span>}{o.failure_details && <p className="text-red-400">{o.failure_details}</p>}</div>)}<RecordObservation busy={busy} onSubmit={input => run(async () => { await action("content_catalog_publications_record", { target_id: t.id, ...input }); await openRelease(selectedRelease); }, "Publication evidence recorded")} /></div>)}</> : <p className="text-sm text-text-muted">Select a release to inspect each destination.</p>}</section>
    </div>}
    {modal === "new-session" && <Modal title="New session" onClose={() => setModal(null)}><CreateSession busy={busy} brands={brands} onSubmit={input => run(async () => { const result = await action<{ session: Session }>("content_catalog_sessions_create", input); setModal(null); await showSession(result.session); }, "Session created")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
    {modal === "add-file" && selectedSession && <Modal title="Add file to session" onClose={() => setModal(null)}><div className="space-y-4"><AttachAsset busy={busy} onSubmit={input => run(async () => { await action("content_catalog_assets_attach", { session_id: selectedSession.id, ...input }); setModal(null); await openSession(selectedSession); }, "File linked to session")} /><div className="rounded border border-border p-3 text-sm"><button type="button" className="text-accent underline" onClick={() => previewImport(selectedSession)}>Browse Storage candidates</button><p className="mt-1 text-xs text-text-muted">Lists files only when clicked. Check each file before linking it.</p>{importLimitReached && <p className="mt-1 text-xs text-yellow-400">Showing the first 200 files.</p>}{candidates.map(candidate => <div key={candidate.storage_file_id} className="flex flex-wrap items-center gap-2 border-t border-border py-2"><span className="min-w-0 flex-1 break-all">{candidate.folder}{candidate.name}</span><button type="button" disabled={busy || candidate.linked_session_ids.includes(selectedSession.id)} className="text-accent underline disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_assets_attach", { session_id: selectedSession.id, storage_file_id: candidate.storage_file_id }); setModal(null); await openSession(selectedSession); }, "File linked to session")}>Link</button></div>)}</div>{error && <p className="text-sm text-red-400">{error}</p>}</div></Modal>}
    {modal === "link-gig" && selectedSession && <Modal title="Link Gig to session" onClose={() => setModal(null)}><LinkGig busy={busy} onSubmit={input => run(async () => { await action("content_catalog_sessions_link_gig", { session_id: selectedSession.id, ...input }); setModal(null); await openSession(selectedSession); }, "Gig linked to session")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
  </div>;
}

const inputClass = "w-full rounded border border-border bg-bg-input px-2 py-1.5 text-sm";
const buttonClass = "rounded bg-accent px-3 py-1.5 text-sm text-bg disabled:opacity-50";
function CreateBrand({ busy, hosts, onSubmit }: { busy: boolean; hosts: Host[]; onSubmit: (x: Record<string, unknown>) => void }) {
  const [slug, setSlug] = useState(""); const [name, setName] = useState(""); const [root, setRoot] = useState("/productions/"); const [connection, setConnection] = useState(""); const [library, setLibrary] = useState(""); const [collection, setCollection] = useState("");
  return <form className="rounded border border-border p-4 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ slug, name, storage_root: root, ...(connection ? { host_provider: "bunny", host_connection_id: Number(connection), host_library_id: library, host_collection_id: collection } : {}) }); }}><h2 className="font-semibold">New brand</h2><input className={inputClass} placeholder="Brand name" required value={name} onChange={e => setName(e.target.value)} /><input className={inputClass} placeholder="brand-slug" required value={slug} onChange={e => setSlug(e.target.value)} /><label className="block text-xs text-text-muted">Storage folder root</label><input className={inputClass} required value={root} onChange={e => setRoot(e.target.value)} /><label className="block text-xs text-text-muted">Video host (optional)</label><select className={inputClass} value={connection} onChange={e => setConnection(e.target.value)}><option value="">None</option>{hosts.map(h => <option key={h.connection_id} value={h.connection_id}>{h.provider} · connection {h.connection_id}</option>)}</select>{connection && <><input className={inputClass} placeholder="Bunny library ID" required value={library} onChange={e => setLibrary(e.target.value)} /><input className={inputClass} placeholder="Bunny collection ID (optional)" value={collection} onChange={e => setCollection(e.target.value)} /></>}<button disabled={busy} className={buttonClass}>Create brand</button></form>;
}
function EditBrand({ brand, busy, hosts, onSubmit }: { brand: Brand; busy: boolean; hosts: Host[]; onSubmit: (x: Record<string, unknown>) => void }) {
  const [open, setOpen] = useState(false); const [name, setName] = useState(brand.name); const [root, setRoot] = useState(brand.storage_root); const [connection, setConnection] = useState(String(brand.host_connection_id || "")); const [library, setLibrary] = useState(brand.host_library_id); const [collection, setCollection] = useState(brand.host_collection_id);
  return <div><button type="button" className="text-accent underline" onClick={() => setOpen(!open)}>{open ? "Close settings" : "Edit settings"}</button>{open && <form className="space-y-2 mt-2" onSubmit={e => { e.preventDefault(); onSubmit({ name, storage_root: root, host_provider: connection ? "bunny" : "", host_connection_id: connection ? Number(connection) : 0, host_library_id: connection ? library : "", host_collection_id: connection ? collection : "" }); }}><input className={inputClass} aria-label="Brand name" required value={name} onChange={e => setName(e.target.value)} /><input className={inputClass} aria-label="Storage root" required value={root} onChange={e => setRoot(e.target.value)} /><select className={inputClass} aria-label="Video host" value={connection} onChange={e => setConnection(e.target.value)}><option value="">No video host</option>{hosts.map(h => <option key={h.connection_id} value={h.connection_id}>{h.provider} · connection {h.connection_id}</option>)}</select>{connection && <><input className={inputClass} placeholder="Bunny library ID" required value={library} onChange={e => setLibrary(e.target.value)} /><input className={inputClass} placeholder="Bunny collection ID (optional)" value={collection} onChange={e => setCollection(e.target.value)} /></>}<button className={buttonClass} disabled={busy}>Save brand</button></form>}</div>;
}
function CreateSession({ busy, brands, onSubmit }: { busy: boolean; brands: Brand[]; onSubmit: (x: Record<string, unknown>) => void }) {
  const [brand, setBrand] = useState(""); const [title, setTitle] = useState(""); const [date, setDate] = useState(new Date().toISOString().slice(0, 10));
  return <form className="rounded border border-border p-3 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ brand_id: brand, title, session_date: date }); }}><h2 className="font-semibold text-sm">New session</h2><select className={inputClass} required value={brand} onChange={e => setBrand(e.target.value)}><option value="">Choose brand</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select><input className={inputClass} placeholder="Session title" required value={title} onChange={e => setTitle(e.target.value)} /><input className={inputClass} type="date" required value={date} onChange={e => setDate(e.target.value)} /><button className={buttonClass} disabled={busy}>Create session</button></form>;
}
function AttachAsset({ busy, onSubmit }: { busy: boolean; onSubmit: (x: Record<string, unknown>) => void }) {
  const [file, setFile] = useState(""); return <form className="rounded border border-border p-3 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ storage_file_id: file }); }}><h3 className="font-semibold text-sm">Link Storage file</h3><p className="text-xs text-text-muted">Uses an existing file ID; no bytes are copied.</p><input className={inputClass} inputMode="numeric" placeholder="Storage file ID" required value={file} onChange={e => setFile(e.target.value)} /><button className={buttonClass} disabled={busy}>Attach file</button></form>;
}
function LinkGig({ busy, onSubmit }: { busy: boolean; onSubmit: (x: Record<string, unknown>) => void }) {
  const [gig, setGig] = useState(""); const [role, setRole] = useState(""); return <form className="rounded border border-border p-3 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ gig_id: Number(gig), role }); }}><h3 className="font-semibold text-sm">Link Gig</h3><p className="text-xs text-text-muted">Reads the assignment; Gigs stays authoritative.</p><input className={inputClass} inputMode="numeric" placeholder="Gig ID" required value={gig} onChange={e => setGig(e.target.value)} /><input className={inputClass} placeholder="Role (optional)" value={role} onChange={e => setRole(e.target.value)} /><button className={buttonClass} disabled={busy}>Link Gig</button></form>;
}
function CreateRelease({ busy, brands, onSubmit }: { busy: boolean; brands: Brand[]; onSubmit: (x: Record<string, unknown>) => void }) {
  const [brand, setBrand] = useState(""); const [title, setTitle] = useState(""); const [phase, setPhase] = useState(""); const [audience, setAudience] = useState(""); const [planned, setPlanned] = useState("");
  return <form className="rounded border border-border p-3 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ brand_id: brand, title, phase, audience, planned_at: planned ? new Date(planned).toISOString() : "" }); }}><h2 className="font-semibold text-sm">New release</h2><select className={inputClass} required value={brand} onChange={e => setBrand(e.target.value)}><option value="">Choose brand</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select><input className={inputClass} placeholder="Release title" required value={title} onChange={e => setTitle(e.target.value)} /><input className={inputClass} placeholder="Phase (optional)" value={phase} onChange={e => setPhase(e.target.value)} /><input className={inputClass} placeholder="Audience or tier" value={audience} onChange={e => setAudience(e.target.value)} /><input className={inputClass} type="datetime-local" value={planned} onChange={e => setPlanned(e.target.value)} /><button className={buttonClass} disabled={busy}>Plan release</button></form>;
}
function AddTarget({ busy, sessions, loadAssets, onSubmit }: { busy: boolean; sessions: Session[]; loadAssets: (id: string) => Promise<Asset[]>; onSubmit: (x: Record<string, unknown>) => void }) {
  const [destination, setDestination] = useState(""); const [account, setAccount] = useState(""); const [session, setSession] = useState(""); const [assetsBySession, setAssetsBySession] = useState<Record<string, Asset[]>>({}); const [selected, setSelected] = useState<string[]>([]);
  const chooseSession = (id: string) => { setSession(id); if (id && !assetsBySession[id]) loadAssets(id).then(rows => setAssetsBySession(prev => ({ ...prev, [id]: rows }))).catch(() => setAssetsBySession(prev => ({ ...prev, [id]: [] }))); };
  return <form className="rounded border border-border p-4 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ destination, account_ref: account, asset_ids: selected }); }}><h3 className="font-semibold text-sm">Add destination</h3><input className={inputClass} required placeholder="Destination, e.g. instagram or patreon" value={destination} onChange={e => setDestination(e.target.value)} /><input className={inputClass} placeholder="Account or tier reference" value={account} onChange={e => setAccount(e.target.value)} /><select className={inputClass} value={session} onChange={e => chooseSession(e.target.value)}><option value="">Choose asset session</option>{sessions.map(s => <option key={s.id} value={s.id}>{s.title}</option>)}</select><p className="text-xs text-text-muted">Selected across sessions: {selected.length}</p><div className="space-y-1">{(assetsBySession[session] || []).map(a => <label key={a.id} className="flex gap-2 text-sm"><input type="checkbox" checked={selected.includes(a.id)} onChange={e => setSelected(prev => e.target.checked ? [...prev, a.id] : prev.filter(x => x !== a.id))} />{a.name}</label>)}</div><button className={buttonClass} disabled={busy || selected.length === 0}>Add target</button></form>;
}
function RecordObservation({ busy, onSubmit }: { busy: boolean; onSubmit: (x: Record<string, unknown>) => void }) {
  const [status, setStatus] = useState(""); const [source, setSource] = useState(""); const [postId, setPostId] = useState(""); const [url, setURL] = useState(""); const [actual, setActual] = useState(""); const [failure, setFailure] = useState("");
  return <form className="grid gap-2 md:grid-cols-2" onSubmit={e => { e.preventDefault(); onSubmit({ status, evidence_source: source, external_post_id: postId, external_url: url, actual_at: actual ? new Date(actual).toISOString() : "", failure_details: failure }); }}><select className={inputClass} required value={status} onChange={e => setStatus(e.target.value)}><option value="">Choose observed status</option>{["scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown"].map(x => <option key={x} value={x}>{x.replaceAll("_", " ")}</option>)}</select><input className={inputClass} required placeholder="Evidence source, e.g. creator_page_manual" value={source} onChange={e => setSource(e.target.value)} /><input className={inputClass} placeholder="External post ID" value={postId} onChange={e => setPostId(e.target.value)} /><input className={inputClass} type="url" placeholder="External URL" value={url} onChange={e => setURL(e.target.value)} /><label className="text-xs text-text-muted">Actual time, if observed<input className={inputClass} type="datetime-local" required={status === "verified_published" || status === "provider_reported_published"} value={actual} onChange={e => setActual(e.target.value)} /></label><input className={inputClass} placeholder="Failure details (if applicable)" value={failure} onChange={e => setFailure(e.target.value)} /><button className={buttonClass} disabled={busy || !status}>Record observation</button></form>;
}
