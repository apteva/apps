import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { uploadResumable } from "../../storage/ui/uploadResumable";

const API = "/api/apps/content-catalog";
type Brand = { id: string; slug: string; name: string; storage_root: string; host_provider: string; host_connection_id: number; host_library_id: string; host_collection_id: string };
type Session = { id: string; brand_id: string; title: string; session_date: string; status: string; notes: string };
type Publication = { id: string; asset_id: string; asset_ids: string[]; title: string; destination: string; account_ref: string; audience: string; status: string; planned_at: string; actual_at: string; external_post_id: string; external_url: string; evidence_source: string; failure_details: string; legacy_target_id: string };
type Asset = { id: string; session_id: string; storage_install_id: number; storage_file_id: string; name: string; kind: string; content_type: string; size_bytes: number; review_status: string; media_status: string; media_rating: string; publications: Publication[] };
type Hosting = { id: string; provider: string; remote_id: string; status: string; embed_url: string; error: string };
type AssetSource = { asset_id: string; relation: string; source_order: number; media_render_id: number };
type GigLink = { gigs_install_id: number; gig_id: number; role: string };
type MediaDetails = { title?: string; description?: string; duration_ms?: number; width?: number; height?: number; audience_rating?: string; transcript_status?: string; probe_status?: string };
type Host = { connection_id: number; provider: string; default: boolean };
type ImportCandidate = { storage_file_id: string; storage_install_id: number; name: string; folder: string; content_type: string; linked_session_ids: string[]; needs_review: boolean };
type SearchUse = { publication_id: string; destination: string; account_ref: string; status: string; external_url: string; external_post_id: string; actual_at: string };
type AssetHit = Asset & { brand_id: string; session_title: string; session_date: string; is_derivative: boolean; uses: SearchUse[]; match_reason: string };
type SessionHit = Session & { brand_name: string; asset_count: number };
type SearchPage<T> = { items: T[]; next_cursor?: string };
type SearchResults = { assets: SearchPage<AssetHit>; sessions: SearchPage<SessionHit> };
const emptySearch = (): SearchResults => ({ assets: { items: [] }, sessions: { items: [] } });

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

function platformGlyph(destination: string): string {
  const platform = destination.trim().toLowerCase().split(/[\s·:]/)[0];
  return ({ instagram: "◎", youtube: "▶", patreon: "P", tiktok: "♫", facebook: "f", x: "𝕏", twitter: "𝕏", linkedin: "in", threads: "@", pinterest: "P", reddit: "●" } as Record<string, string>)[platform] || destination.slice(0, 2).toUpperCase();
}
function statusGlyph(status: string): string {
  return ({ verified_published: "✓", provider_reported_published: "◌", scheduled: "◷", submitted: "↗", failed: "!", removed: "×", planned: "·", unknown: "?" } as Record<string, string>)[status] || "?";
}
function PublicationIcons({ items }: { items: Array<{ destination: string; status: string; account_ref?: string }> }) {
  if (!items.length) return <span className="text-xs text-text-muted">No platforms recorded</span>;
  return <div className="flex flex-wrap gap-1.5" aria-label="Platform publication status">{items.map((item, i) => <span key={`${item.destination}:${item.account_ref || ""}:${i}`} className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-xs" style={{ color: publicationColor(item.status) }} title={`${item.destination}${item.account_ref ? ` · ${item.account_ref}` : ""}: ${publicationLabel(item.status)}`} aria-label={`${item.destination}: ${publicationLabel(item.status)}`}><span aria-hidden="true" className="font-bold">{platformGlyph(item.destination)}</span><span aria-hidden="true">{statusGlyph(item.status)}</span></span>)}</div>;
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

function PreviewImage({ src, alt, fallback, className = "", style }: { src: string; alt: string; fallback: string; className?: string; style?: CSSProperties }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => { setFailed(false); }, [src]);
  return <div className={`bg-gradient-to-br from-accent/15 via-bg-input to-bg ${className}`} style={{ position: "relative", width: "100%", aspectRatio: "16 / 9", flexShrink: 0, overflow: "hidden", ...style }}>{failed ? <div className="flex items-center justify-center text-3xl text-text-muted" style={{ position: "absolute", inset: 0 }} aria-label="No preview available">{fallback}</div> : <img src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} style={{ position: "absolute", inset: 0, display: "block", width: "100%", height: "100%", objectFit: "cover", objectPosition: "center" }} />}</div>;
}

const previewCardStyle: CSSProperties = { display: "flex", flexDirection: "column", height: "100%", minWidth: 0 };

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
  const [tab, setTab] = useState<"brands" | "sessions" | "search">("sessions");
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
  const [hosts, setHosts] = useState<Host[]>([]);
  const [selectedSession, setSelectedSession] = useState<Session | null>(null);
  const [assets, setAssets] = useState<Asset[]>([]);
  const [gigs, setGigs] = useState<GigLink[]>([]);
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);
  const [assetSources, setAssetSources] = useState<AssetSource[]>([]);
  const [hostings, setHostings] = useState<Hosting[]>([]);
  const [assetMedia, setAssetMedia] = useState<MediaDetails | null>(null);
  const [candidates, setCandidates] = useState<ImportCandidate[]>([]);
  const [importLimitReached, setImportLimitReached] = useState(false);
  const [editingPublication, setEditingPublication] = useState<Publication | null>(null);
  const [modal, setModal] = useState<"new-session" | "add-file" | "link-gig" | "publication" | null>(null);
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
    const [o, b, s, h] = await Promise.all([
      get<Record<string, number>>("/overview"), get<{ brands: Brand[] }>("/brands"),
      get<{ sessions: Session[] }>("/sessions"),
      get<{ hosts: Host[] }>("/video-hosts"),
    ]);
    setOverview(o); setBrands(b.brands || []); setSessions(s.sessions || []); setHosts(h.hosts || []); setSearchReload(n => n + 1);
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
    setSelectedAsset(asset); setAssetMedia(null); setAssetSources([]); setHostings([]); setError("");
    try { const result = await get<{ asset: Asset; hostings: Hosting[]; sources: AssetSource[]; publications: Publication[]; media?: MediaDetails; media_error?: string }>(`/assets/${asset.id}`); setSelectedAsset({ ...result.asset, publications: result.publications || [] }); setHostings(result.hostings || []); setAssetSources(result.sources || []); setAssetMedia(result.media || null); if (result.media_error) setError(`Media unavailable: ${result.media_error}`); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const brandName = (id: string) => brands.find(b => b.id === id)?.name || short(id);
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
  const loadMore = useCallback(async (kind: "assets" | "sessions", cursor: string) => {
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
    <div className="flex flex-wrap gap-2">{["brands", "sessions", "assets", "publications", "hostings"].map(k => <span key={k} className="rounded border border-border px-3 py-1 text-sm"><strong>{overview[k] ?? 0}</strong> {k}</span>)}</div>
    {error && <div className="rounded border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-400">{error}</div>}
    {notice && <div className="rounded border border-green-500/50 bg-green-500/10 p-3 text-sm">{notice}</div>}
    <nav className="flex gap-1 border-b border-border">{(["sessions", "search", "brands"] as const).map(t => <button key={t} onClick={() => { setSelectedSession(null); setTab(t); }} className={`px-3 py-2 text-sm capitalize ${tab === t ? "border-b-2 border-accent text-accent" : "text-text-muted"}`}>{t}</button>)}</nav>

    {tab === "search" && <section className="space-y-4">
      <div><h2 className="text-lg font-semibold">Search Catalog</h2><p className="text-sm text-text-muted">Searches sessions and linked files. Storage folders are never scanned automatically.</p></div>
      <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-4">
        <input className={`${inputClass} md:col-span-2`} type="search" value={searchText} onChange={e => setSearchText(e.target.value)} placeholder="Search titles, file names, notes…" aria-label="Search Catalog" />
        <select className={inputClass} value={searchType} onChange={e => { setSearchType(e.target.value); if (e.target.value === "sessions") { setSearchKind(""); setSearchLineage(""); setSearchReview(""); setSearchAvailability("any"); } }} aria-label="Result type"><option value="all">All records</option><option value="assets">Files</option><option value="sessions">Sessions</option></select>
        <select className={inputClass} value={searchBrand} onChange={e => setSearchBrand(e.target.value)} aria-label="Brand"><option value="">All brands</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select>
        <select className={inputClass} value={searchKind} disabled={searchType === "sessions"} onChange={e => setSearchKind(e.target.value)} aria-label="File type"><option value="">All file types</option>{["video", "image", "audio", "other"].map(kind => <option key={kind} value={kind}>{kind}</option>)}</select>
        <select className={inputClass} value={searchLineage} disabled={searchType === "sessions"} onChange={e => setSearchLineage(e.target.value)} aria-label="File lineage"><option value="">Sources and derivatives</option><option value="source">Source files</option><option value="derivative">Derivatives</option></select>
        <select className={inputClass} value={searchReview} disabled={searchType === "sessions"} onChange={e => setSearchReview(e.target.value)} aria-label="Review status"><option value="">Any review state</option>{["pending", "approved", "rejected"].map(state => <option key={state} value={state}>{state}</option>)}</select>
        <select className={inputClass} value={searchSort} disabled={searchType === "sessions"} onChange={e => setSearchSort(e.target.value)} aria-label="Sort files"><option value="session_newest">Newest session first</option><option value="asset_newest">Recently linked first</option></select>
        <label className="text-xs text-text-muted">From session date<input className={inputClass} type="date" value={searchDateFrom} onChange={e => setSearchDateFrom(e.target.value)} /></label>
        <label className="text-xs text-text-muted">Through session date<input className={inputClass} type="date" value={searchDateTo} onChange={e => setSearchDateTo(e.target.value)} /></label>
        <input className={inputClass} value={searchDestination} onChange={e => { setSearchDestination(e.target.value); if (!e.target.value) { setSearchAccount(""); if (searchAvailability !== "never_used") setSearchAvailability("any"); } }} placeholder="Destination, e.g. instagram" aria-label="Destination" />
        <input className={inputClass} value={searchAccount} disabled={!searchDestination} onChange={e => setSearchAccount(e.target.value)} placeholder="Account reference (optional)" aria-label="Account reference" />
        <select className={inputClass} value={searchAvailability} disabled={searchType === "sessions"} onChange={e => setSearchAvailability(e.target.value)} aria-label="Availability"><option value="any">Any use state</option><option value="never_used">Never used</option><option value="not_published" disabled={!searchDestination}>Not published here</option><option value="ready_to_publish" disabled={!searchDestination}>Ready to publish here</option><option value="scheduled" disabled={!searchDestination}>Scheduled here</option><option value="published" disabled={!searchDestination}>Published here</option><option value="failed" disabled={!searchDestination}>Failed here</option></select>
      </div>
      <p className="text-xs text-text-muted">Ready to publish means approved and no active plan or observed post for the selected destination{searchAccount ? " and account" : ""}. Results reflect evidence recorded in Catalog.</p>
      {searchError && <p className="rounded border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-400">{searchError}</p>}
      {searchLoading && <p className="text-sm text-text-muted">Searching…</p>}
      {(searchType === "all" || searchType === "assets") && <div className="space-y-2"><h3 className="font-semibold">Files <span className="text-text-muted font-normal">{searchResults.assets.items.length}</span></h3>{searchResults.assets.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching linked files.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(230px, 1fr))", gap: 12, alignItems: "stretch" }}>{searchResults.assets.items.map(hit => <button key={hit.id} type="button" style={previewCardStyle} className="overflow-hidden rounded-xl border border-border text-left hover:border-accent/60" onClick={() => openSearchAsset(hit).catch(e => setSearchError(errorText(e)))}><PreviewImage src={previewURL(projectId, "assets", hit.id)} alt={`Preview of ${hit.name}`} fallback={mediaGlyph(assetMediaKind(hit))} /><div className="p-3 text-sm" style={{ display: "flex", flexDirection: "column", flex: 1, minWidth: 0 }}><div className="truncate font-medium" style={{ height: 24, flexShrink: 0 }} title={hit.name}>{hit.name}</div><div className="text-xs text-text-muted truncate" style={{ height: 20, flexShrink: 0 }}>{hit.session_title} · {hit.session_date}</div><div className="text-xs text-text-muted truncate" style={{ height: 20, flexShrink: 0 }}>{brandName(hit.brand_id)} · {hit.review_status} · {hit.is_derivative ? "derivative" : "source"}</div><div className="border-t border-border pt-2" style={{ marginTop: 8, flex: 1 }}><PublicationIcons items={hit.uses} /></div><p className="text-xs text-text-muted line-clamp-2" style={{ marginTop: 8 }}>{hit.match_reason}</p></div></button>)}</div>{searchResults.assets.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("assets", searchResults.assets.next_cursor!)}>More files</button>}</div>}
      {!assetSearchOnly && !searchDestination && (searchType === "all" || searchType === "sessions") && <div className="space-y-2"><h3 className="font-semibold">Sessions <span className="text-text-muted font-normal">{searchResults.sessions.items.length}</span></h3>{searchResults.sessions.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching sessions.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12, alignItems: "stretch" }}>{searchResults.sessions.items.map(hit => <button key={hit.id} type="button" style={previewCardStyle} className="overflow-hidden rounded-xl border border-border text-left hover:border-accent/60" onClick={() => showSession(hit, "search")}><PreviewImage src={previewURL(projectId, "sessions", hit.id)} alt={`Preview of ${hit.title}`} fallback="▣" /><div className="p-3 text-sm" style={{ flex: 1 }}><div className="font-medium line-clamp-2" style={{ minHeight: 40 }}>{hit.title}</div><div className="mt-1 text-xs text-text-muted">{hit.brand_name} · {hit.session_date} · {hit.asset_count} files</div></div></button>)}</div>{searchResults.sessions.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("sessions", searchResults.sessions.next_cursor!)}>More sessions</button>}</div>}

    </section>}

    {tab === "brands" && <div className="grid gap-5 lg:grid-cols-2">
      <section className="space-y-3"><h2 className="font-semibold">Brands</h2>{brands.map(b => <div key={b.id} className="rounded border border-border p-3 space-y-2 text-sm"><div className="font-medium">{b.name}</div><div className="text-text-muted">{b.storage_root}</div><div>{b.host_provider ? `${b.host_provider} · connection ${b.host_connection_id} · library ${b.host_library_id}` : "No cloud host configured"}</div><EditBrand key={`${b.id}:${b.host_connection_id}:${b.host_library_id}`} brand={b} hosts={hosts} busy={busy} onSubmit={input => run(async () => { await action("content_catalog_brands_update", { id: b.id, ...input }); }, "Brand updated")} /></div>)}</section>
      <CreateBrand busy={busy} hosts={hosts} onSubmit={input => run(async () => { await action("content_catalog_brands_create", input); }, "Brand created")} />
    </div>}

    {tab === "sessions" && !selectedSession && <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="text-lg font-semibold">Sessions</h2><p className="text-sm text-text-muted">Select a session to see its files and publication status.</p></div><button type="button" className={buttonClass} onClick={() => setModal("new-session")}>+ New session</button></div>
      {sessions.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No sessions yet.</p>}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12, alignItems: "stretch" }}>{sessions.map(s => <button key={s.id} style={previewCardStyle} className="overflow-hidden rounded-xl border border-border text-left text-sm transition-colors hover:border-accent/60" onClick={() => showSession(s)}><PreviewImage src={previewURL(projectId, "sessions", s.id)} alt={`Preview of ${s.title}`} fallback="▣" /><div className="p-3" style={{ flex: 1 }}><div className="font-medium line-clamp-2" style={{ minHeight: 40 }}>{s.title}</div><div className="mt-1 text-xs text-text-muted">{brandName(s.brand_id)} · {s.session_date}</div></div></button>)}</div>
    </div>}

    {tab === "sessions" && selectedSession && <div className="space-y-5">
      <div className="border-b border-border pb-4">
        <button type="button" className="mb-3 text-sm text-accent underline" onClick={backToSessions}>← All sessions</button>
        <div className="flex flex-wrap items-start justify-between gap-4"><div className="min-w-0"><h2 className="text-2xl font-semibold">{selectedSession.title}</h2><p className="mt-1 text-sm text-text-muted">{brandName(selectedSession.brand_id)} · {selectedSession.session_date} · {selectedSession.status}</p></div><div className="flex flex-wrap gap-2"><button type="button" className={buttonClass} onClick={() => setModal("add-file")}>+ Add file</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" disabled={!assets.length} onClick={() => { setSelectedAsset(null); setEditingPublication(null); setModal("publication"); }}>+ Record post</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" onClick={() => setModal("link-gig")}>Link Gig</button></div></div>
      </div>
      <section className="space-y-3"><div className="flex items-center justify-between gap-3"><h3 className="text-lg font-semibold">Assets</h3><span className="text-xs text-text-muted">{assets.length} file{assets.length === 1 ? "" : "s"}</span></div><p className="text-xs text-text-muted">Each file shows its platforms. A green check means verified live; a hollow circle means reported live but unverified.</p>
        {assets.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No files linked yet. Use Add file to upload into this session or link one from Storage.</p>}
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))", gap: 12, alignItems: "stretch" }}>{assets.map(asset =>
          <button key={asset.id} onClick={() => openAsset(asset)} style={previewCardStyle} className={`overflow-hidden rounded-xl border text-left text-sm transition-colors hover:border-accent/60 ${selectedAsset?.id === asset.id ? "border-accent" : "border-border"}`}>
            <PreviewImage src={previewURL(projectId, "assets", asset.id)} alt={`Preview of ${asset.name}`} fallback={mediaGlyph(assetMediaKind(asset))} />
            <div className="p-3" style={{ display: "flex", flexDirection: "column", flex: 1, minWidth: 0 }}>
              <div className="font-medium truncate" style={{ height: 24, flexShrink: 0 }} title={asset.name}>{asset.name}</div>
              <div className="text-xs text-text-muted truncate" style={{ height: 20, flexShrink: 0 }}>{asset.kind} · #{asset.storage_file_id} · {asset.review_status}</div>
              <div className="border-t border-border pt-2" style={{ marginTop: 8, flex: 1 }}><PublicationIcons items={asset.publications || []} /></div>
            </div>
          </button>)}</div>
      </section>
      {assets.some(a => a.publications?.length) && <section className="rounded-xl border border-border p-4 space-y-2 text-sm"><h3 className="font-semibold">Posts for this session</h3>{Array.from(new Map(assets.flatMap(a => a.publications || []).map(p => [p.id, p])).values()).map(p => <div key={p.id} className="flex flex-wrap items-center gap-2 border-t border-border pt-2 text-xs"><PublicationIcons items={[p]} /><strong>{p.title || p.destination}</strong><span>{publicationLabel(p.status)}</span><span className="text-text-muted">{p.asset_ids?.length || 1} files</span>{p.external_url && <a href={p.external_url} target="_blank" rel="noreferrer" className="text-accent underline">View post</a>}<button type="button" className="ml-auto text-accent underline" onClick={() => { setEditingPublication(p); setSelectedAsset(null); setModal("publication"); }}>Edit post</button></div>)}</section>}
      {selectedAsset && <section className="rounded-xl border border-border p-4 space-y-3"><div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold break-all">{selectedAsset.name}</h3><button type="button" className="text-sm text-accent underline" onClick={() => setSelectedAsset(null)}>Close file</button></div><AssetViewer asset={selectedAsset} projectId={projectId} />
        {assetMedia && <div className="text-sm space-y-1"><div>{assetMedia.title || "Media metadata"} · {assetMedia.probe_status || "unknown"} · {assetMedia.audience_rating || "unrated"}</div>{assetMedia.description && <p className="text-text-muted">{assetMedia.description}</p>}<div className="text-xs text-text-muted">{assetMedia.duration_ms ? `${Math.round(assetMedia.duration_ms / 1000)}s · ` : ""}{assetMedia.width && assetMedia.height ? `${assetMedia.width} × ${assetMedia.height} · ` : ""}Transcript: {assetMedia.transcript_status || "not available"}</div></div>}
        <div className="text-xs text-text-muted">Sources: {assetSources.length ? assetSources.map(s => `${short(s.asset_id)} (${s.relation})`).join(", ") : "original / none linked"}</div>
        <div className="flex flex-wrap gap-2 text-sm">{(["pending", "approved", "rejected"] as const).map(state => <button disabled={busy || selectedAsset.review_status === state} key={state} className="border border-border rounded px-2 py-1 disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_assets_review", { asset_id: selectedAsset.id, review_status: state }); await openSession(selectedSession); setSelectedAsset({ ...selectedAsset, review_status: state }); }, `Review set to ${state}`)}>{state}</button>)}</div>
        <button disabled={busy || selectedAsset.review_status !== "approved"} className="rounded border border-accent px-3 py-1.5 text-sm disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_hosting_request", { asset_id: selectedAsset.id }); await openAsset(selectedAsset); }, "Hosting request recorded")}>Host approved asset</button>
        <div className="space-y-2">{hostings.map(h => <div key={h.id} className="rounded border border-border p-2 text-sm"><span className="font-medium">{h.provider}: {h.status}</span>{h.remote_id && <span className="ml-2 text-text-muted">{short(h.remote_id)}</span>}{h.embed_url && <a className="ml-2 text-accent underline" href={h.embed_url} target="_blank" rel="noreferrer">Open host</a>}{h.error && <p className="text-red-400">{h.error}</p>}{h.remote_id && h.status !== "ready" && <button className="text-accent underline" onClick={() => run(async () => { await action("content_catalog_hosting_check", { id: h.id }); await openAsset(selectedAsset); }, "Host checked")}>Check readiness</button>}</div>)}</div>
        <div className="space-y-2 text-sm"><div className="flex items-center justify-between gap-2"><h4 className="font-medium">Platforms and posts</h4><button type="button" className="rounded border border-accent px-2 py-1 text-xs text-accent" onClick={() => { setEditingPublication(null); setModal("publication"); }}>+ Add platform</button></div>{!selectedAsset.publications?.length && <p className="text-xs text-text-muted">Nothing recorded for this file yet.</p>}{(selectedAsset.publications || []).map(p => <div key={p.id} className="flex flex-wrap items-center gap-2 rounded border border-border p-2 text-xs"><PublicationIcons items={[p]} /><span className="font-medium">{publicationLabel(p.status)}</span><span className="text-text-muted">{p.asset_ids?.length || 1} file{p.asset_ids?.length === 1 ? "" : "s"}</span>{p.account_ref && <span className="text-text-muted">{p.account_ref}</span>}{p.audience && <span className="text-text-muted">{p.audience}</span>}{p.actual_at && <span className="text-text-muted">{p.actual_at.slice(0, 10)}</span>}{p.planned_at && !p.actual_at && <span className="text-text-muted">Planned {p.planned_at.slice(0, 10)}</span>}{p.external_url && <a className="text-accent underline" href={p.external_url} target="_blank" rel="noreferrer">View post</a>}{p.external_post_id && <span className="text-text-muted">ID {p.external_post_id}</span>}{p.evidence_source && <span className="text-text-muted">via {p.evidence_source}</span>}{p.failure_details && <span className="text-red-400">{p.failure_details}</span>}<button type="button" className="ml-auto text-accent underline" onClick={() => { setEditingPublication(p); setModal("publication"); }}>Update status</button></div>)}</div>
      </section>}
      <section className="rounded-xl border border-border p-4 space-y-2 text-sm"><h3 className="font-semibold">Session info</h3>{selectedSession.notes && <p className="text-text-muted">{selectedSession.notes}</p>}<p className="text-text-muted">Gigs: {gigs.length ? gigs.map(g => `#${g.gig_id}${g.role ? ` (${g.role})` : ""}`).join(", ") : "none linked"}</p></section>
    </div>}

    {modal === "new-session" && <Modal title="New session" onClose={() => setModal(null)}><CreateSession busy={busy} brands={brands} onSubmit={input => run(async () => { const result = await action<{ session: Session }>("content_catalog_sessions_create", input); setModal(null); await showSession(result.session); }, "Session created")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
    {modal === "add-file" && selectedSession && <Modal title="Add file to session" onClose={() => setModal(null)}><div className="space-y-4"><UploadFiles projectId={projectId} session={selectedSession} action={action} onUploaded={() => openSession(selectedSession)} /><AttachAsset busy={busy} onSubmit={input => run(async () => { await action("content_catalog_assets_attach", { session_id: selectedSession.id, ...input }); setModal(null); await openSession(selectedSession); }, "File linked to session")} /><div className="rounded border border-border p-3 text-sm"><button type="button" className="text-accent underline" onClick={() => previewImport(selectedSession)}>Browse Storage candidates</button><p className="mt-1 text-xs text-text-muted">Lists files only when clicked. Check each file before linking it.</p>{importLimitReached && <p className="mt-1 text-xs text-yellow-400">Showing the first 200 files.</p>}{candidates.map(candidate => <div key={candidate.storage_file_id} className="flex flex-wrap items-center gap-2 border-t border-border py-2"><span className="min-w-0 flex-1 break-all">{candidate.folder}{candidate.name}</span><button type="button" disabled={busy || candidate.linked_session_ids.includes(selectedSession.id)} className="text-accent underline disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_assets_attach", { session_id: selectedSession.id, storage_file_id: candidate.storage_file_id }); setModal(null); await openSession(selectedSession); }, "File linked to session")}>Link</button></div>)}</div>{error && <p className="text-sm text-red-400">{error}</p>}</div></Modal>}
    {modal === "link-gig" && selectedSession && <Modal title="Link Gig to session" onClose={() => setModal(null)}><LinkGig busy={busy} onSubmit={input => run(async () => { await action("content_catalog_sessions_link_gig", { session_id: selectedSession.id, ...input }); setModal(null); await openSession(selectedSession); }, "Gig linked to session")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
    {modal === "publication" && selectedSession && <Modal title={editingPublication ? "Edit post" : "Record post"} onClose={() => setModal(null)}><PublicationForm key={editingPublication?.id || selectedAsset?.id || "session"} busy={busy} initial={editingPublication} assets={assets} initialAssetId={selectedAsset?.id} onSubmit={input => run(async () => { const asset = selectedAsset; await action("content_catalog_posts_record", input); setModal(null); await openSession(selectedSession); if (asset) await openAsset(asset); }, "Post saved")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
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
function UploadFiles({ projectId, session, get, action, onUploaded }: { projectId: string; session: Session; action: <T>(tool: string, args: Record<string, unknown>) => Promise<T>; onUploaded: () => Promise<void> }) {
  type Row = { key: string; file: File; progress: number; status: string; storageId?: number; error?: string };
  const [rows, setRows] = useState<Row[]>([]);
  const [folder, setFolder] = useState("");
  const [dragging, setDragging] = useState(false);
  const controllers = useRef(new Map<string, AbortController>());
  useEffect(() => () => { controllers.current.forEach(c => c.abort()); }, []);
  const update = (key: string, patch: Partial<Row>) => setRows(prev => prev.map(r => r.key === key ? { ...r, ...patch } : r));
  const select = (files: FileList | File[]) => setRows(prev => [...prev, ...Array.from(files).map(file => ({ key: `${Date.now()}-${Math.random()}`, file, progress: 0, status: "waiting" }))]);
  const upload = async (row: Row) => {
    const controller = new AbortController(); controllers.current.set(row.key, controller);
    try {
      const target = await action<{ folder: string; storage_install_id: number; project_id: string }>("content_catalog_session_upload_target", { session_id: session.id });
      setFolder(target.folder);
      update(row.key, { status: "uploading", error: "" });
      const file = await uploadResumable(row.file, { folder: target.folder, projectId, installId: target.storage_install_id, signal: controller.signal, onProgress: (done, total) => update(row.key, { progress: Math.round(100 * done / total) }) });
      update(row.key, { status: "linking", storageId: file.id, progress: 100 });
      await action("content_catalog_assets_attach_uploaded", { session_id: session.id, storage_file_id: String(file.id) });
      update(row.key, { status: "linked" });
      await onUploaded();
    } catch (e) { update(row.key, { status: controller.signal.aborted ? "cancelled" : "error", error: errorText(e) }); }
    finally { controllers.current.delete(row.key); }
  };
  const retryLink = async (row: Row) => {
    if (!row.storageId) return;
    update(row.key, { status: "linking", error: "" });
    try { await action("content_catalog_assets_attach_uploaded", { session_id: session.id, storage_file_id: String(row.storageId) }); update(row.key, { status: "linked" }); await onUploaded(); }
    catch (e) { update(row.key, { status: "error", error: errorText(e) }); }
  };
  return <div className="space-y-3 rounded border border-border p-3 text-sm"><h3 className="font-semibold">Upload into session</h3><p className="text-xs text-text-muted">Selected files go to this session's Storage folder. No folders are scanned.</p><div className={`rounded-lg border-2 border-dashed p-5 text-center ${dragging ? "border-accent" : "border-border"}`} onDragOver={e => { e.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={e => { e.preventDefault(); setDragging(false); select(e.dataTransfer.files); }}>Drop files here or <label className="cursor-pointer text-accent underline">choose files<input type="file" multiple className="sr-only" onChange={e => { if (e.target.files) select(e.target.files); e.target.value = ""; }} /></label></div>{folder && <p className="break-all text-xs text-text-muted">Storage folder: {folder}</p>}{rows.map(row => <div key={row.key} className="rounded border border-border p-2"><div className="flex items-center gap-2"><span className="min-w-0 flex-1 truncate" title={row.file.name}>{row.file.name}</span><span className="text-xs text-text-muted">{row.status}{row.status === "uploading" ? ` ${row.progress}%` : ""}</span>{row.status === "waiting" && <button type="button" className="text-accent underline" onClick={() => upload(row)}>Upload</button>}{row.status === "uploading" && <button type="button" className="text-accent underline" onClick={() => controllers.current.get(row.key)?.abort()}>Cancel</button>}{row.status === "error" && <button type="button" className="text-accent underline" onClick={() => row.storageId ? retryLink(row) : upload(row)}>Retry {row.storageId ? "link" : "upload"}</button>}</div>{row.storageId && <p className="text-xs text-text-muted">Storage file #{row.storageId}</p>}{row.error && <p className="text-xs text-red-400">{row.error}</p>}{row.status === "uploading" && <div className="mt-1 h-1 rounded bg-bg-input"><div className="h-full rounded bg-accent" style={{ width: `${row.progress}%` }} /></div>}</div>)}{rows.some(r => r.status === "waiting") && <button type="button" className={buttonClass} onClick={() => rows.filter(r => r.status === "waiting").forEach(upload)}>Upload selected</button>}</div>;
}
function PublicationForm({ busy, initial, assets, initialAssetId, onSubmit }: { busy: boolean; initial: Publication | null; assets: Asset[]; initialAssetId?: string; onSubmit: (x: Record<string, unknown>) => void }) {
  const [selected, setSelected] = useState<string[]>(initial?.asset_ids || (initialAssetId ? [initialAssetId] : []));
  const [title, setTitle] = useState(initial?.title || "");
  const [destination, setDestination] = useState(initial?.destination || "");
  const [account, setAccount] = useState(initial?.account_ref || "");
  const [audience, setAudience] = useState(initial?.audience || "");
  const [status, setStatus] = useState(initial?.status || "planned");
  const [planned, setPlanned] = useState(initial?.planned_at?.slice(0, 16) || "");
  const [actual, setActual] = useState(initial?.actual_at?.slice(0, 16) || "");
  const [url, setURL] = useState(initial?.external_url || "");
  const [postID, setPostID] = useState(initial?.external_post_id || "");
  const [source, setSource] = useState(initial?.evidence_source || "");
  const [failure, setFailure] = useState(initial?.failure_details || "");
  const published = status === "verified_published" || status === "provider_reported_published";
  return <form className="space-y-3" onSubmit={e => { e.preventDefault(); if (!selected.length) return; onSubmit({ ...(initial ? { post_id: initial.id } : { destination: destination.trim().toLowerCase() }), asset_ids: selected, title: title.trim(), account_ref: account.trim(), audience: audience.trim(), planned_at: planned ? new Date(planned).toISOString() : "", status, actual_at: actual ? new Date(actual).toISOString() : "", external_url: url.trim(), external_post_id: postID.trim(), evidence_source: source.trim(), failure_details: failure.trim() }); }}>
    <h3 className="font-semibold">{initial ? `Edit ${initial.destination} post` : "Record a platform post"}</h3>
    <p className="text-xs text-text-muted">One post can include several files. This records Catalog evidence only; it does not publish externally.</p>
    <label className="block text-xs">Post title (optional)<input className={inputClass} value={title} onChange={e => setTitle(e.target.value)} /></label>
    <fieldset className="space-y-1"><legend className="text-xs font-medium">Files in this post</legend>{assets.map(a => <label key={a.id} className="flex items-center gap-2 rounded border border-border px-2 py-1 text-xs"><input type="checkbox" checked={selected.includes(a.id)} onChange={e => setSelected(prev => e.target.checked ? [...prev, a.id] : prev.filter(x => x !== a.id))} /><span className="min-w-0 truncate" title={a.name}>{a.name}</span></label>)}{!selected.length && <p className="text-red-400 text-xs">Choose at least one file.</p>}</fieldset>
    <label className="block text-xs">Platform<input className={inputClass} list="catalog-platforms" required disabled={!!initial} value={destination} onChange={e => setDestination(e.target.value)} placeholder="Instagram, YouTube, Patreon…" /></label>
    <datalist id="catalog-platforms">{["instagram", "youtube", "patreon", "tiktok", "facebook", "x", "linkedin", "threads", "pinterest", "reddit"].map(x => <option key={x} value={x} />)}</datalist>
    <label className="block text-xs">Account or channel (optional)<input className={inputClass} value={account} onChange={e => setAccount(e.target.value)} /></label><label className="block text-xs">Audience or tier (optional)<input className={inputClass} value={audience} onChange={e => setAudience(e.target.value)} /></label><label className="block text-xs">Planned time (optional)<input className={inputClass} type="datetime-local" value={planned} onChange={e => setPlanned(e.target.value)} /></label>
    <label className="block text-xs">Status<select className={inputClass} value={status} onChange={e => setStatus(e.target.value)}>{["planned", "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown"].map(x => <option key={x} value={x}>{publicationLabel(x)}</option>)}</select></label>
    {published && <label className="block text-xs">Actual publication time<input className={inputClass} type="datetime-local" required value={actual} onChange={e => setActual(e.target.value)} /></label>}
    <label className="block text-xs">Post URL<input className={inputClass} type="url" required={status === "verified_published" && !postID} value={url} onChange={e => setURL(e.target.value)} placeholder="https://…" /></label>
    <label className="block text-xs">External post ID (optional)<input className={inputClass} value={postID} onChange={e => setPostID(e.target.value)} /></label>
    {status !== "planned" && <label className="block text-xs">How was this confirmed?<input className={inputClass} required value={source} onChange={e => setSource(e.target.value)} placeholder="Manual creator-page check or Social post result" /></label>}
    {status === "failed" && <label className="block text-xs">Failure details<input className={inputClass} value={failure} onChange={e => setFailure(e.target.value)} /></label>}
    <button className={buttonClass} disabled={busy || !selected.length}>Save post</button>
  </form>;
}
