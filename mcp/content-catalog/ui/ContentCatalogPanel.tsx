import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { buildAssetFamilies, assetInScope, lineageBadge, type AssetFamily } from "./lineage";
import { hostingNotice, hostingPending, hostingStageLabel, transcodingMessages } from "./hosting";
import { uploadResumable } from "../../storage/ui/uploadResumable";

const API = "/api/apps/content-catalog";
type Brand = { id: string; slug: string; name: string; storage_root: string; host_provider: string; host_connection_id: number; host_library_id: string; host_collection_id: string };
type Lifecycle = { lifecycle?: "active" | "archived"; revision?: number; archive_reason?: string; archived_at?: string };
type Session = Lifecycle & { id: string; brand_id: string; title: string; session_date: string; status: string; notes: string };
type Publication = { id: string; asset_id: string; asset_ids: string[]; title: string; destination: string; account_ref: string; audience: string; status: string; planned_at: string; actual_at: string; external_post_id: string; external_url: string; evidence_source: string; failure_details: string; legacy_target_id: string };
type HostingSummary = { id: string; provider: string; connection_id: number; status: string; remote_id: string; last_checked_at: string; encode_progress?: number | null; provider_stage?: string };
type Asset = Lifecycle & { role?: string; output_type?: string; ancestors?: Asset[]; original_session_id?: string; session_lifecycle?: string; session_revision?: number; eligible?: boolean; id: string; session_id: string; storage_install_id: number; storage_file_id: string; name: string; kind: string; content_type: string; size_bytes: number; review_status: string; media_status: string; media_rating: string; favorite?: boolean; patreon_intent?: "unset" | "free" | "paid"; tags?: string[]; description?: string; description_source?: string; description_updated_at?: string; duration_ms?: number; media_error?: string; publications: Publication[]; hostings?: HostingSummary[]; hosting_intents?: HostingIntent[]; sources?: AssetSource[] };
type Hosting = HostingSummary & { asset_id: string; embed_url: string; error: string; provider_status?: number | null; transcoding_messages?: unknown; check_error?: string; next_check_at?: string };
type HostingIntent = { id: string; asset_id: string; title: string; status: string; checksum_status: string; error: string; hosting_id?: string };
type HostingResponse = { hostings: Hosting[]; hosting_intents: HostingIntent[] };
type AssetSource = { asset_id: string; relation: string; source_order: number; media_render_id: number; name?: string; session_id?: string; kind?: string; content_type?: string };
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
function recordingDate(date: string): string { return date || "Recording date unknown"; }
function storageFileURL(projectId: string, installId: number, fileId: string): string { return `/api/apps/storage/files/${encodeURIComponent(fileId)}/content?${new URLSearchParams({ project_id: projectId, install_id: String(installId) })}`; }
function storageContentURL(projectId: string, asset: Asset): string { return storageFileURL(projectId, asset.storage_install_id, asset.storage_file_id); }
function previewURL(projectId: string, kind: "sessions" | "assets", id: string): string { return `${API}/${kind}/${encodeURIComponent(id)}/preview?${new URLSearchParams({ project_id: projectId })}`; }
function mediaGlyph(kind: string): string { return kind === "video" ? "▶" : kind === "audio" ? "♫" : kind === "image" ? "▣" : "▤"; }
function AssetDescription({ asset }: { asset: Asset }) {
  return <div style={{ flexShrink: 0 }}><p className="text-xs text-text-muted line-clamp-2" style={{ height: 32, lineHeight: "16px", marginTop: 6 }} title={asset.description || ""}>{asset.description || "No Media description available"}</p><p className="text-xs text-text-muted truncate" style={{ height: 20 }}>{asset.media_status === "completed" ? "Media ready" : `Media ${asset.media_status || "unavailable"}`}{asset.media_rating ? ` · ${asset.media_rating}` : ""}</p></div>;
}
function assetMediaKind(asset: Asset): string { return asset.content_type.startsWith("image/") ? "image" : asset.content_type.startsWith("video/") ? "video" : asset.content_type.startsWith("audio/") ? "audio" : asset.kind; }
function durationLabel(ms: number): string { const seconds = Math.round(ms / 1000); const minutes = Math.floor(seconds / 60); return minutes >= 60 ? `${Math.floor(minutes / 60)}:${String(minutes % 60).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}` : `${minutes}:${String(seconds % 60).padStart(2, "0")}`; }
type LengthMode = "any" | "range" | "unknown";
function matchesDuration(ms: number | undefined, mode: LengthMode, minimum: number, maximum: number): boolean {
  if (mode === "any") return true;
  if (mode === "unknown") return !ms;
  return !!ms && ms >= minimum * 1000 && ms <= maximum * 1000;
}
function DurationFilter({ mode, minimum, maximum, limit, loading, onMode, onRange }: { mode: LengthMode; minimum: number; maximum: number; limit: number; loading: boolean; onMode: (mode: LengthMode) => void; onRange: (minimum: number, maximum: number) => void }) {
  const percent = (seconds: number) => 100 * seconds / limit;
  const setMinimum = (value: number) => { if (Number.isFinite(value)) { const seconds = Math.max(0, Math.round(value)); onRange(seconds, Math.max(seconds, maximum)); } };
  const setMaximum = (value: number) => { if (Number.isFinite(value)) { const seconds = Math.max(0, Math.round(value)); onRange(Math.min(minimum, seconds), seconds); } };
  return <div className="rounded-lg border border-border p-3 space-y-2" aria-label="Video or audio length filter">
    <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
      <div className="flex flex-wrap items-center gap-2"><strong>Video / audio length</strong><span className="text-text-muted" role="status">{mode === "range" ? `${durationLabel(minimum * 1000)} – ${durationLabel(maximum * 1000)}` : mode === "unknown" ? "Length unknown" : "Any length"}</span></div>
      <div className="flex gap-2">{([["any", "Any length"], ["range", "Select range"], ["unknown", "Unknown only"]] as const).map(([value, label]) => <button key={value} type="button" aria-pressed={mode === value} className={`rounded-full border px-2 py-1 ${mode === value ? "border-accent text-accent bg-accent/10" : "border-border text-text-muted"}`} onClick={() => onMode(value)}>{label}</button>)}</div>
    </div>
    <style>{`input.catalog-duration-slider[type="range"] { position:absolute; inset:0; width:100%; height:32px; margin:0; background:transparent; appearance:none; -webkit-appearance:none; pointer-events:none; }
input.catalog-duration-slider[type="range"]::-webkit-slider-runnable-track { height:4px; background:transparent; }
input.catalog-duration-slider[type="range"]::-moz-range-track { height:4px; background:transparent; }
input.catalog-duration-slider[type="range"]::-webkit-slider-thumb { appearance:none; -webkit-appearance:none; width:18px; height:18px; margin-top:-7px; border-radius:50%; border:2px solid var(--bg, var(--color-bg, #12141c)); background:var(--accent, var(--color-accent, #93b3ff)); pointer-events:auto; cursor:ew-resize; }
input.catalog-duration-slider[type="range"]::-moz-range-thumb { width:14px; height:14px; border-radius:50%; border:2px solid var(--bg, var(--color-bg, #12141c)); background:var(--accent, var(--color-accent, #93b3ff)); pointer-events:auto; cursor:ew-resize; }
input.catalog-duration-slider[type="range"]:focus-visible { outline:2px solid var(--accent, var(--color-accent, #93b3ff)); outline-offset:2px; border-radius:4px; }
input.catalog-duration-slider[type="range"]:disabled { opacity:.4; }
input.catalog-duration-slider[type="range"]:disabled::-webkit-slider-thumb { cursor:default; }
input.catalog-duration-slider[type="range"]:disabled::-moz-range-thumb { cursor:default; }`}</style>
    <div className="flex flex-wrap items-center gap-4">
      <div className="min-w-0" style={{ flex: "1 1 260px" }}>
        <div style={{ position: "relative", height: 32, marginInline: 9 }}>
          <div className="bg-border rounded-full" style={{ position: "absolute", top: 14, left: 0, right: 0, height: 4 }} />
          <div className="bg-accent rounded-full" style={{ position: "absolute", top: 14, left: `${percent(minimum)}%`, width: `${percent(maximum) - percent(minimum)}%`, height: 4, opacity: mode === "range" ? 1 : .35 }} />
          <input className="catalog-duration-slider" type="range" min={0} max={limit} step={1} value={minimum} disabled={loading} style={{ zIndex: minimum > limit / 2 ? 3 : 2 }} aria-label="Minimum length slider" aria-valuetext={durationLabel(minimum * 1000)} onChange={e => onRange(Math.min(Number(e.target.value), maximum), maximum)} />
          <input className="catalog-duration-slider" type="range" min={0} max={limit} step={1} value={maximum} disabled={loading} style={{ zIndex: 2 }} aria-label="Maximum length slider" aria-valuetext={durationLabel(maximum * 1000)} onChange={e => onRange(minimum, Math.max(minimum, Number(e.target.value)))} />
        </div>
        <div className="flex justify-between text-xs text-text-muted"><span>0:00</span><span>{durationLabel(limit * 1000)}</span></div>
      </div>
      <div className="flex gap-3 text-xs"><label>From (seconds)<input className={inputClass} style={{ width: 100, marginTop: 4 }} type="number" min={0} step={1} value={minimum} disabled={loading} onChange={e => setMinimum(e.target.valueAsNumber)} /></label><label>To (seconds)<input className={inputClass} style={{ width: 100, marginTop: 4 }} type="number" min={0} step={1} value={maximum} disabled={loading} onChange={e => setMaximum(e.target.valueAsNumber)} /></label></div>
    </div>
    <p className="text-xs text-text-muted">{loading ? "Loading lengths from Media…" : "Drag either handle or enter exact seconds. Range includes both endpoints; files with unknown length are excluded."}</p>
  </div>;
}
function matchesSharing(posts: Publication[], state: string): boolean {
  if (state === "any") return true;
  if (state === "none") return posts.length === 0;
  if (state === "not_verified") return !posts.some(post => post.status === "verified_published");
  if (state === "verified") return posts.some(post => post.status === "verified_published");
  if (state === "reported") return posts.some(post => post.status === "provider_reported_published");
  if (state === "scheduled") return posts.some(post => ["scheduled", "submitted"].includes(post.status));
  return posts.some(post => post.status === "failed");
}
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
function AssetLabels({ asset, compact = false }: { asset: Asset; compact?: boolean }) {
  const max = compact ? 3 : 8;
  return <div className="flex flex-wrap items-center gap-1" aria-label="Catalog labels">
    {asset.favorite && <span className="rounded bg-yellow-400/15 px-1.5 py-0.5 text-xs text-yellow-300" title="Favorite">⭐ Favorite</span>}
    {asset.output_type && <span className="rounded bg-accent/10 px-1.5 py-0.5 text-xs text-accent">{asset.output_type}</span>}
    {asset.patreon_intent && asset.patreon_intent !== "unset" && <span className={`rounded px-1.5 py-0.5 text-xs ${asset.patreon_intent === "paid" ? "bg-fuchsia-400/15 text-fuchsia-300" : "bg-sky-400/15 text-sky-300"}`}>Patreon {asset.patreon_intent}</span>}
    {(asset.tags || []).slice(0, max).map(tag => <span key={tag} className="rounded bg-accent/10 px-1.5 py-0.5 text-xs text-accent">{tag}</span>)}
    {(asset.tags || []).length > max && <span className="text-xs text-text-muted">+{asset.tags!.length - max}</span>}
  </div>;
}
function PublicationIcons({ items }: { items: Array<{ destination: string; status: string; account_ref?: string }> }) {
  if (!items.length) return <span className="text-xs text-text-muted">No platforms recorded</span>;
  return <div className="flex flex-wrap gap-1.5" aria-label="Platform publication status">{items.map((item, i) => <span key={`${item.destination}:${item.account_ref || ""}:${i}`} className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-xs" style={{ color: publicationColor(item.status) }} title={`${item.destination}${item.account_ref ? ` · ${item.account_ref}` : ""}: ${publicationLabel(item.status)}`} aria-label={`${item.destination}: ${publicationLabel(item.status)}`}><span aria-hidden="true" className="font-bold">{platformGlyph(item.destination)}</span><span aria-hidden="true">{statusGlyph(item.status)}</span></span>)}</div>;
}

function Modal({ title, onClose, children, wide = false }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  const dialog = useRef<HTMLElement>(null);
  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.current?.querySelector<HTMLButtonElement>("button")?.focus();
    return () => { if (previousFocus?.isConnected) previousFocus.focus(); };
  }, []);
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); onClose(); }
      if (event.key !== "Tab" || !dialog.current) return;
      const controls = Array.from(dialog.current.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), video[controls], audio[controls], [tabindex="0"]')).filter(element => element.getClientRects().length > 0);
      const first = controls[0]; const last = controls[controls.length - 1];
      if (!first || !last) return;
      if (!dialog.current.contains(document.activeElement) || (!event.shiftKey && document.activeElement === last)) { event.preventDefault(); first.focus(); }
      else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);
  return <div role="presentation" style={{ position: "fixed", inset: 0, zIndex: 1000, display: "flex", alignItems: "center", justifyContent: "center", padding: 20, background: "rgba(0, 0, 0, .72)" }} onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
    <section ref={dialog} role="dialog" aria-modal="true" aria-label={title} className="rounded-xl border border-border bg-bg p-4 shadow-2xl" style={{ width: wide ? "min(100%, 1120px)" : "min(100%, 560px)", maxHeight: "90vh", overflowY: "auto", overscrollBehavior: "contain" }}>
      <div className="mb-3 flex items-start justify-between gap-4 bg-bg" style={wide ? { position: "sticky", top: -16, zIndex: 1, paddingTop: 16, paddingBottom: 12 } : undefined}>{wide && <h3 className="min-w-0 break-all text-lg font-semibold">{title}</h3>}<button type="button" className="ml-auto shrink-0 rounded border border-border px-2 py-1 text-sm" onClick={onClose} aria-label="Close dialog">Close</button></div>
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

function AssetCardPreview({ asset, projectId }: { asset: Asset; projectId: string }) {
  return <div style={{ position: "relative", width: "100%", flexShrink: 0 }}>
    <PreviewImage src={previewURL(projectId, "assets", asset.id)} alt={`Preview of ${asset.name}`} fallback={mediaGlyph(assetMediaKind(asset))} />
    {(!!asset.hostings?.length || asset.hosting_intents?.some(i=>i.status==="waiting_checksum")) && <div className="flex flex-wrap gap-1" style={{ position: "absolute", top: 8, left: 8, right: 8 }} aria-label="Cloud hosting status">{(asset.hostings||[]).map(hosting => {
      const status = hosting.status === "ready" && !hosting.remote_id ? "uncertain" : hosting.status;
      let label = ({ ready: "Ready", processing: "Processing", reserved: "Queued", uncertain: "Unverified", failed: "Failed" } as Record<string, string>)[status] || "Unverified";
      if(status==="processing"){label=hosting.provider_stage?hostingStageLabel(hosting.provider_stage):label;if(hosting.encode_progress!=null)label+=` ${Math.round(hosting.encode_progress)}%`;}
      const provider = hosting.provider === "bunny" ? "Bunny" : hosting.provider;
      const color = status === "ready" ? "#bbf7d0" : status === "failed" ? "#fecaca" : "#fde68a";
      const background = status === "ready" ? "rgba(5, 46, 22, .94)" : status === "failed" ? "rgba(69, 10, 10, .94)" : "rgba(66, 32, 6, .94)";
      return <span key={hosting.id} className="rounded-md px-2 py-1 text-xs font-medium" style={{ color, background, border: "1px solid currentColor", boxShadow: "0 1px 4px rgba(0,0,0,.35)" }} title={`${provider} hosting: ${label.toLowerCase()} · connection ${hosting.connection_id}${hosting.last_checked_at ? ` · last checked ${hosting.last_checked_at}` : ""}`} aria-label={`${provider} hosting: ${label}`}>{provider} · {label}</span>;
    })}{(asset.hosting_intents||[]).filter(i=>i.status==="waiting_checksum").map(i=><span key={i.id} className="rounded-md px-2 py-1 text-xs font-medium" style={{color:"#fde68a",background:"rgba(66,32,6,.94)",border:"1px solid currentColor"}}>Waiting for checksum</span>)}</div>}
  </div>;
}

function ParentLinks({ asset, projectId, onOpen, compact = false }: { asset: Asset; projectId: string; onOpen: (id: string) => void; compact?: boolean }) {
  const sources = asset.sources || [];
  return <div style={compact ? { height: 32, display: "flex", alignItems: "center", minWidth: 0, gap: 4 } : undefined} className={compact ? "" : "space-y-2"}>
    {(compact ? sources.slice(0, 1) : sources).map(source => <button type="button" key={source.asset_id} className="flex items-center gap-2 text-xs text-accent min-w-0" style={compact ? { flex: 1 } : undefined} onClick={() => onOpen(source.asset_id)} title={`Open ${source.name || short(source.asset_id)} · ${source.relation}`} aria-label={`Open original: ${source.name || short(source.asset_id)}`}>
      <img loading="lazy" src={previewURL(projectId, "assets", source.asset_id)} alt="" style={{ width: 30, height: 24, objectFit: "cover", borderRadius: 4, flexShrink: 0 }} />
      <span className="truncate">{compact ? source.name || short(source.asset_id) : `Open original · ${source.name || short(source.asset_id)}`}</span>
    </button>)}
    {compact && sources.length > 1 && <button type="button" className="shrink-0 text-xs text-accent" title="View all sources" onClick={() => onOpen(asset.id)}>+{sources.length - 1}</button>}
  </div>;
}
function SessionAssetCard({ asset, projectId, duration, context = false, compact = false, onOpen, onToggleFavorite }: { asset: Asset; projectId: string; duration?: number; context?: boolean; compact?: boolean; onOpen: (id: string) => void; onToggleFavorite?: (asset: Asset) => void }) {
  return <article data-asset-id={asset.id} style={{ ...previewCardStyle, position: "relative" }} className="overflow-hidden rounded-xl border border-border text-sm">
    {onToggleFavorite && <button type="button" className="absolute right-2 top-2 z-10 rounded-md border border-border bg-bg/90 px-1.5 py-1 text-sm shadow" aria-label={asset.favorite ? `Remove ${asset.name} from favorites` : `Favorite ${asset.name}`} aria-pressed={!!asset.favorite} title={asset.favorite ? "Remove favorite" : "Favorite"} onClick={event => { event.stopPropagation(); onToggleFavorite(asset); }}>{asset.favorite ? "⭐" : "☆"}</button>}
    <div style={{ display: "flex", flexDirection: compact ? "row" : "column", alignItems: compact ? "center" : undefined }}>
      <button type="button" style={{ display: "block", width: compact ? 92 : "100%", flexShrink: 0, textAlign: "left" }} onClick={() => onOpen(asset.id)} aria-label={`Open asset: ${asset.name}`}>
        <AssetCardPreview asset={asset} projectId={projectId} />
      </button>
      <div className="px-3 pt-2" style={{ display: "flex", flexDirection: "column", flex: 1, minWidth: 0 }}>
        <button type="button" className="font-medium truncate text-left hover:text-accent" style={{ height: 24, flexShrink: 0 }} title={asset.name} onClick={() => onOpen(asset.id)}>{asset.name}</button>
        <div className="text-xs text-text-muted truncate" style={{ height: 20, flexShrink: 0 }} title={`${assetMediaKind(asset)} · #${asset.storage_file_id} · ${asset.review_status}`}>{assetMediaKind(asset)}{duration ? ` · ${durationLabel(duration)}` : ""} · #{asset.storage_file_id} · {asset.review_status}</div>
        <div className="flex items-center gap-1.5 text-xs" style={{ height: 24, flexShrink: 0 }}><span className="rounded bg-accent/10 px-1.5 py-0.5 text-accent">{lineageBadge(asset)}</span>{(asset.lifecycle === "archived" || asset.session_lifecycle === "archived") && <span className="rounded bg-yellow-500/10 px-1.5 text-yellow-400">Archived</span>}{context && <span className="text-text-muted" title="Shown to explain a matching derivative; this asset does not match the filters">Context</span>}</div>
        <AssetLabels asset={asset} compact />
      </div>
    </div>
    <div className="px-3 pb-3" style={{ display: "flex", flexDirection: "column", flex: 1, minWidth: 0 }}>
      <ParentLinks asset={asset} projectId={projectId} onOpen={onOpen} compact />
      <AssetDescription asset={asset} />
      <div className="border-t border-border pt-2" style={{ marginTop: 8, flex: 1 }}><PublicationIcons items={asset.publications || []} /></div>
    </div>
  </article>;
}
function AssetFamilyCard({ node, projectId, durations, filtering, onOpen, onDerived, onToggleFavorite }: { node: AssetFamily<Asset>; projectId: string; durations: Record<string, number>; filtering: boolean; onOpen: (id: string) => void; onDerived: (id: string) => void; onToggleFavorite?: (asset: Asset) => void }) {
  const matchingDerived = node.matchingCount - Number(node.matches);
  const outputSummary = Object.entries(node.outputCounts).map(([type,count])=>`${count} ${type}${count===1?"":"s"}`).join(" · ");
  return <div className="min-w-0 flex flex-col gap-2" data-family-id={node.asset.id}>
    <SessionAssetCard asset={node.asset} projectId={projectId} duration={durations[node.asset.id]} context={!node.matches} onOpen={onOpen} onToggleFavorite={onToggleFavorite} />
    <button type="button" className="flex w-full items-center justify-between rounded-lg border border-border px-3 py-2 text-xs hover:border-accent/60 disabled:opacity-40" style={{ flexShrink: 0 }} aria-haspopup="dialog" disabled={!matchingDerived} onClick={() => onDerived(node.asset.id)}><span title={outputSummary}>{node.derivedCount ? `Selected outputs · ${filtering ? `${matchingDerived} matching` : node.derivedCount}` : "No selected outputs"}</span>{matchingDerived > 0 && <span aria-hidden="true">↗</span>}</button>
    {!!outputSummary && <p className="text-xs text-text-muted px-1" data-output-counts>{outputSummary}</p>}
  </div>;
}

function matchingDescendants(node: AssetFamily<Asset>): AssetFamily<Asset>[] {
  return node.children.flatMap(child => [...(child.matches ? [child] : []), ...matchingDescendants(child)]).sort((a, b) => a.rank - b.rank);
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
  const [tab, setTab] = useState<"brands" | "sessions" | "search">("sessions");
  const [returnTab, setReturnTab] = useState<"sessions" | "search">("sessions");
  const [searchText, setSearchText] = useState("");
  const [searchType, setSearchType] = useState("all");
  const [searchBrand, setSearchBrand] = useState("");
  const [searchDateFrom, setSearchDateFrom] = useState("");
  const [searchDateTo, setSearchDateTo] = useState("");
  const [searchKind, setSearchKind] = useState("");
  const [searchLineage, setSearchLineage] = useState("");
  const [searchRole,setSearchRole]=useState("");
  const [searchIntermediates,setSearchIntermediates]=useState(false);
  const [searchSort, setSearchSort] = useState("session_newest");
  const [searchReview, setSearchReview] = useState("");
  const [searchFavorite, setSearchFavorite] = useState("");
  const [searchPatreon, setSearchPatreon] = useState("");
  const [searchTag, setSearchTag] = useState("");
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
  const [lifecycleScope, setLifecycleScope] = useState("active");
  const [lifecycleTarget, setLifecycleTarget] = useState<{entity:"asset"|"session";operation:"archive"|"move"|"restore";assets?:Asset[];session?:Session}|null>(null);
  const [destinationSessions, setDestinationSessions] = useState<Session[]>([]);
  const [checkedAssets, setCheckedAssets] = useState<string[]>([]);
  const [sessionBrand, setSessionBrand] = useState("");
  const [brandSessions, setBrandSessions] = useState<Session[] | null>(null);
  const [brandSessionsLoading, setBrandSessionsLoading] = useState(false);
  const [brandSessionsError, setBrandSessionsError] = useState("");
  const [hosts, setHosts] = useState<Host[]>([]);
  const [selectedSession, setSelectedSession] = useState<Session | null>(null);
  const [assets, setAssets] = useState<Asset[]>([]);
  const [assetQuery, setAssetQuery] = useState("");
  const [assetKind, setAssetKind] = useState("any");
  const [assetLength, setAssetLength] = useState<LengthMode>("any");
  const [assetLengthMin, setAssetLengthMin] = useState(0);
  const [assetLengthMax, setAssetLengthMax] = useState<number | null>(null);
  const [assetSharing, setAssetSharing] = useState("any");
  const [assetDestination, setAssetDestination] = useState("");
  const [assetSort, setAssetSort] = useState("newest");
  const [assetLineage, setAssetLineage] = useState("any");
  const [assetRole,setAssetRole]=useState("");
  const [showIntermediates,setShowIntermediates]=useState(false);
  const [assetFavorite, setAssetFavorite] = useState("any");
  const [assetPatreon, setAssetPatreon] = useState("any");
  const [assetTag, setAssetTag] = useState("");
  const [assetView, setAssetView] = useState<"grid" | "grouped">(() => { try { return localStorage.getItem("catalog-asset-view") === "grid" ? "grid" : "grouped"; } catch { return "grouped"; } });
  useEffect(() => { try { localStorage.setItem("catalog-asset-view", assetView); } catch {} }, [assetView]);
  const [derivedFamilyId, setDerivedFamilyId] = useState<string | null>(null);
  const [assetDurations, setAssetDurations] = useState<Record<string, number>>({});
  const [durationsLoading, setDurationsLoading] = useState(false);
  const [durationsUnavailable, setDurationsUnavailable] = useState(0);
  const [durationsFailed, setDurationsFailed] = useState(0);
  const [durationsError, setDurationsError] = useState("");
  const durationRequest = useRef(0);
  const sessionRequest = useRef(0);
  const [gigs, setGigs] = useState<GigLink[]>([]);
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);
  const [assetLoading, setAssetLoading] = useState(false);
  const assetRequest = useRef(0);
  const activeSessionId = useRef("");
  const [assetSources, setAssetSources] = useState<AssetSource[]>([]);
  const [hostings, setHostings] = useState<Hosting[]>([]);
  const [hostingIntents,setHostingIntents]=useState<HostingIntent[]>([]);
  const [hostingRefreshError,setHostingRefreshError]=useState("");
  const [assetMedia, setAssetMedia] = useState<MediaDetails | null>(null);
  const [candidates, setCandidates] = useState<ImportCandidate[]>([]);
  const [importLimitReached, setImportLimitReached] = useState(false);
  const [editingPublication, setEditingPublication] = useState<Publication | null>(null);
  const [modal, setModal] = useState<"new-session" | "edit-session" | "add-file" | "link-gig" | "publication" | "filters" | "lifecycle" | "labels" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const query = useCallback((extra: Record<string, string> = {}) => new URLSearchParams({ project_id: projectId, install_id: String(installId), lifecycle: lifecycleScope, include_intermediates: String(showIntermediates), ...extra }).toString(), [projectId, installId, lifecycleScope, showIntermediates]);
  const get = useCallback(async <T,>(path: string, extra: Record<string, string> = {}): Promise<T> => {
    const res = await fetch(`${API}${path}?${query(extra)}`, { credentials: "same-origin" });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }, [query]);
  useEffect(() => {
    let active = true;
    setBrandSessions(null); setBrandSessionsError(""); setBrandSessionsLoading(!!sessionBrand);
    if (sessionBrand) get<{ sessions: Session[] }>("/sessions", { brand_id: sessionBrand }).then(result => {
      if (active) setBrandSessions(result.sessions || []);
    }).catch(err => { if (active) setBrandSessionsError(errorText(err)); }).finally(() => { if (active) setBrandSessionsLoading(false); });
    return () => { active = false; };
  }, [get, sessionBrand, sessions]);
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
  const loadDurations = useCallback((s: Session) => {
    const request = ++durationRequest.current;
    setDurationsLoading(true); setDurationsError(""); setDurationsFailed(0);
    get<{ durations: Record<string, number>; unavailable: number; failed: number }>(`/sessions/${s.id}/durations`).then(result => {
      if (request !== durationRequest.current) return;
      setAssetDurations(result.durations || {}); setDurationsUnavailable(result.unavailable || 0); setDurationsFailed(result.failed || 0);
    }).catch(err => { if (request === durationRequest.current) setDurationsError(errorText(err)); }).finally(() => {
      if (request === durationRequest.current) setDurationsLoading(false);
    });
  }, [get]);
  const openSession = useCallback(async (s: Session) => {
    const request = ++sessionRequest.current;
    ++durationRequest.current;
    ++assetRequest.current; if (activeSessionId.current !== s.id) setDerivedFamilyId(null); activeSessionId.current = s.id;
    setCheckedAssets([]); setSelectedSession(s); setSelectedAsset(null); setCandidates([]); setError("");
    setAssets([]); setAssetDurations({}); setDurationsUnavailable(0); setDurationsFailed(0); setDurationsError(""); setDurationsLoading(false);
    try { const result = await get<{ session: Session; assets: Asset[]; gigs: GigLink[] }>(`/sessions/${s.id}`); if (request !== sessionRequest.current) return; setSelectedSession(result.session || s); setAssets(result.assets || []); setGigs(result.gigs || []); setAssetDurations(Object.fromEntries((result.assets || []).filter(a => a.duration_ms && a.duration_ms > 0).map(a => [a.id, a.duration_ms!]))); setDurationsFailed((result.assets || []).filter(a => ["video", "audio"].includes(assetMediaKind(a)) && a.media_error).length); setDurationsUnavailable((result.assets || []).filter(a => ["video", "audio"].includes(assetMediaKind(a)) && !a.media_error && !a.duration_ms).length); } catch (e) { if (request === sessionRequest.current) setError(errorText(e)); }
  }, [get, loadDurations]);
  const scopeKey = `${lifecycleScope}:${showIntermediates}`;
  const lastScope = useRef(scopeKey);
  useEffect(() => { if (lastScope.current !== scopeKey) { lastScope.current = scopeKey; if (selectedSession) openSession(selectedSession); } }, [scopeKey, selectedSession, openSession]);
  const manageLifecycle = async (entity: "asset" | "session", operation: "archive" | "move" | "restore", files?: Asset[]) => {
    setError("");
    try {
      const sourceSessionId = files?.[0]?.session_id || selectedAsset?.session_id;
      const sourceSession = entity === "session" || !sourceSessionId || sourceSessionId === selectedSession?.id
        ? selectedSession : (await get<{session: Session}>(`/sessions/${sourceSessionId}`)).session;
      const result = await get<{ sessions: Session[] }>("/sessions", { lifecycle: "all", brand_id: sourceSession?.brand_id || "" });
      setDestinationSessions(result.sessions || []); setLifecycleTarget({ entity, operation, assets: files || (selectedAsset ? [selectedAsset] : []), session: selectedSession || undefined }); setModal("lifecycle");
    } catch (err) { setError(errorText(err)); }
  };
  const showSession = useCallback(async (s: Session, origin: "sessions" | "search" = "sessions") => {
    const url = new URL(window.location.href);
    url.searchParams.set("catalog_session", s.id);
    window.history.pushState(null, "", url);
    setReturnTab(origin); setTab("sessions");
    setModal(null);
    setAssetQuery(""); setAssetKind("any"); setAssetLength("any"); setAssetLengthMin(0); setAssetLengthMax(null); setAssetSharing("any"); setAssetDestination(""); setAssetSort("newest"); setAssetLineage("any"); setAssetRole(""); setShowIntermediates(false); setAssetFavorite("any"); setAssetPatreon("any"); setAssetTag("");
    await openSession(s);
  }, [openSession]);
  const backToSessions = useCallback(() => {
    const url = new URL(window.location.href);
    url.searchParams.delete("catalog_session");
    window.history.replaceState(null, "", url);
    ++durationRequest.current; ++sessionRequest.current; ++assetRequest.current; activeSessionId.current = "";
    setDerivedFamilyId(null); setSelectedSession(null); setSelectedAsset(null); setModal(null); setTab(returnTab);
  }, [returnTab]);
  useEffect(() => {
    const syncFromURL = () => {
      const id = new URLSearchParams(window.location.search).get("catalog_session");
      if (!id) { ++durationRequest.current; ++sessionRequest.current; ++assetRequest.current; activeSessionId.current = ""; setDerivedFamilyId(null); setSelectedSession(null); setSelectedAsset(null); return; }
      const session = sessions.find(s => s.id === id);
      if (session) { if (activeSessionId.current !== id) openSession(session); else setSelectedSession(session); }
    };
    window.addEventListener("popstate", syncFromURL);
    syncFromURL();
    return () => window.removeEventListener("popstate", syncFromURL);
  }, [sessions, openSession]);
  const previewImport = useCallback(async (s: Session) => {
    try { const result = await get<{ candidates: ImportCandidate[]; limit_reached: boolean }>("/import-preview", { session_id: s.id }); setCandidates(result.candidates || []); setImportLimitReached(result.limit_reached); setError(""); } catch (e) { setError(errorText(e)); }
  }, [get]);
  const openAsset = useCallback(async (asset: Asset) => {
    const request = ++assetRequest.current;
    setSelectedAsset(asset); setAssetLoading(true); setAssetMedia(null); setAssetSources([]); setHostings([]); setHostingIntents([]);setHostingRefreshError(""); setError(""); setNotice("");
    try { const result = await get<{ asset: Asset; hostings: Hosting[]; hosting_intents: HostingIntent[]; sources: AssetSource[]; publications: Publication[]; media?: MediaDetails; media_error?: string }>(`/assets/${asset.id}`); if (request !== assetRequest.current) return; const loaded = { ...result.asset, sources: result.sources || result.asset.sources || [], publications: result.publications || [] }; setSelectedAsset(loaded); setHostings(result.hostings || []);setHostingIntents(result.hosting_intents || []); setAssets(current => current.map(item => item.id === asset.id ? loaded : item)); setAssetSources(result.sources || []); setAssetMedia(result.media || null); if (result.media_error) setError(`Media unavailable: ${result.media_error}`); } catch (e) { if (request === assetRequest.current) setError(errorText(e)); } finally { if (request === assetRequest.current) setAssetLoading(false); }
  }, [get]);
  const pendingHosting=hostingPending(hostings,hostingIntents);
  useEffect(()=>{
    if(!selectedAsset?.id || !pendingHosting)return;
    let active=true;let timer:number|undefined;
    const assetId=selectedAsset.id;
    const poll=async()=>{
      try{
        const result=await get<HostingResponse>("/hostings",{asset_id:assetId});
        if(!active)return;
        setHostings(result.hostings||[]);setHostingIntents(result.hosting_intents||[]);setHostingRefreshError("");
        setAssets(current=>current.map(a=>a.id===assetId?{...a,hostings:result.hostings||[],hosting_intents:result.hosting_intents||[]}:a));
      }catch(err){if(active)setHostingRefreshError(errorText(err));}
      if(active)timer=window.setTimeout(poll,5000);
    };
    timer=window.setTimeout(poll,5000);
    return()=>{active=false;window.clearTimeout(timer);};
  },[get,selectedAsset?.id,pendingHosting]);
  useEffect(()=>{
    if(!selectedSession?.id)return;
    let active=true;let timer:number|undefined;
    const sessionId=selectedSession.id;
    const poll=async()=>{
      try{
        const result=await get<HostingResponse>("/hostings",{session_id:sessionId});
        if(!active)return;
        setAssets(current=>current.map(a=>({...a,hostings:(result.hostings||[]).filter(h=>h.asset_id===a.id),hosting_intents:(result.hosting_intents||[]).filter(i=>i.asset_id===a.id)})));
      }catch{}
      if(active)timer=window.setTimeout(poll,15000);
    };
    timer=window.setTimeout(poll,15000);
    return()=>{active=false;window.clearTimeout(timer);};
  },[get,selectedSession?.id]);
  const requestHosting=async()=>{
    if(!selectedAsset)return;
    setBusy(true);setError("");setNotice("");
    const asset=selectedAsset;
    try{
      const result=await action<{hosting?:Hosting;hosting_intent?:HostingIntent;pending_checksum?:boolean;checksum_status?:string;warning?:string}>("content_catalog_hosting_request",{asset_id:asset.id});
      await openAsset(asset);setNotice(hostingNotice(result));
    }catch(err){setError(errorText(err));}finally{setBusy(false);}
  };
  const closeAsset = useCallback(() => { ++assetRequest.current; setSelectedAsset(null); setAssetLoading(false); }, []);
  const toggleFavorite = useCallback(async (asset: Asset) => {
    try {
      await action("content_catalog_assets_labels_update", { asset_ids: [asset.id], favorite: !asset.favorite, expected_revisions: { [asset.id]: asset.revision } });
      if (selectedSession) await openSession(selectedSession);
      if (selectedAsset?.id === asset.id) await openAsset({ ...asset, favorite: !asset.favorite });
    } catch (e) { setError(errorText(e)); }
  }, [action, openSession, openAsset, selectedSession, selectedAsset]);
  const filteredSessions = (sessionBrand && brandSessions !== null ? brandSessions : sessions).filter(session => !sessionBrand || session.brand_id === sessionBrand);
  const brandName = (id: string) => brands.find(b => b.id === id)?.name || short(id);
  const searchParams = useCallback(() => ({ entity_type: searchType, query: searchText, brand_id: searchBrand, date_from: searchDateFrom, date_to: searchDateTo, kind: searchKind, lineage: searchLineage, role: searchRole, include_intermediates: String(searchIntermediates || searchRole === "intermediate"), sort: searchSort, review_status: searchReview, tag: searchTag, favorite: searchFavorite, patreon_intent: searchPatreon, destination: searchDestination, account_ref: searchAccount, availability: searchAvailability, limit: "24" }), [searchType, searchText, searchBrand, searchDateFrom, searchDateTo, searchKind, searchLineage, searchRole, searchIntermediates, searchSort, searchReview, searchTag, searchFavorite, searchPatreon, searchDestination, searchAccount, searchAvailability]);
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
  const assetSearchOnly = !!searchRole || searchIntermediates || !!searchKind || !!searchLineage || !!searchReview || !!searchTag || !!searchFavorite || !!searchPatreon || searchAvailability !== "any";
  const assetDestinations = Array.from(new Set(assets.flatMap(asset => (asset.publications || []).map(post => post.destination.trim())).filter(Boolean))).sort((a, b) => a.localeCompare(b));
  const longestDurationSeconds = Math.max(1, ...Object.values(assetDurations).map(ms => Math.ceil(ms / 1000)));
  const lengthMaximum = assetLengthMax ?? longestDurationSeconds;
  const lengthSliderLimit = Math.max(longestDurationSeconds, assetLengthMin, lengthMaximum);
  const activeAssetFilterCount = [!!assetRole,showIntermediates,assetKind !== "any", assetLength !== "any", assetSharing !== "any", !!assetDestination, assetSort !== "newest", assetLineage !== "any", assetFavorite !== "any", assetPatreon !== "any", !!assetTag].filter(Boolean).length;
  const assetFiltersActive = !!assetQuery || activeAssetFilterCount > 0;
  const assetFilterSummary = [
    assetRole ? `Purpose: ${assetRole}` : "", showIntermediates ? "Including intermediates" : "",
    assetLineage === "source" ? "Sources / no parent linked" : assetLineage === "derivative" ? "Derivatives" : "",
    assetKind !== "any" ? ({ video: "Videos", image: "Images", audio: "Audio", other: "Other files" } as Record<string, string>)[assetKind] : "",
    assetLength === "range" ? `${durationLabel(assetLengthMin * 1000)}–${durationLabel(lengthMaximum * 1000)}` : assetLength === "unknown" ? "Length unknown" : "",
    assetSharing !== "any" ? ({ none: "No post recorded", not_verified: "No verified live post", verified: "Verified live", reported: "Reported live", scheduled: "Scheduled or submitted", failed: "Failed" } as Record<string, string>)[assetSharing] : "",
    assetDestination,
    assetFavorite !== "any" ? (assetFavorite === "favorite" ? "Favorites" : "Not favorite") : "",
    assetPatreon !== "any" ? `Patreon ${assetPatreon}` : "",
    assetTag ? `#${assetTag}` : "",
    assetSort !== "newest" ? ({ name: "Name A–Z", shortest: "Shortest first", longest: "Longest first" } as Record<string, string>)[assetSort] : "",
  ].filter(Boolean).join(" · ");
  const resetAssetFilters = (clearSearch = false) => { if (clearSearch) setAssetQuery(""); setAssetKind("any"); setAssetLength("any"); setAssetLengthMin(0); setAssetLengthMax(null); setAssetSharing("any"); setAssetDestination(""); setAssetSort("newest"); setAssetLineage("any"); setAssetRole(""); setShowIntermediates(false); setAssetFavorite("any"); setAssetPatreon("any"); setAssetTag(""); };
  const sharingCount = (state: string) => assets.filter(asset => assetInScope(asset,{lifecycle:lifecycleScope,includeIntermediates:showIntermediates})).filter(asset => {
    const posts = (asset.publications || []).filter(post => !assetDestination || post.destination.toLowerCase() === assetDestination.toLowerCase());
    return (!assetDestination || state !== "any" || posts.length > 0) && matchesSharing(posts, state);
  }).length;
  const scopedAssets = assets.filter(asset => assetInScope(asset,{ lifecycle:lifecycleScope, includeIntermediates:showIntermediates }));
  const filteredAssets = scopedAssets.filter(asset => {
    if (assetRole && asset.role !== assetRole) return false;
    if (assetQuery && !`${asset.name} ${asset.storage_file_id} ${asset.description || ""}`.toLowerCase().includes(assetQuery.trim().toLowerCase())) return false;
    if (assetLineage === "source" && asset.sources?.length) return false;
    if (assetLineage === "derivative" && !asset.sources?.length) return false;
    if (assetFavorite === "favorite" && !asset.favorite) return false;
    if (assetFavorite === "not_favorite" && asset.favorite) return false;
    if (assetPatreon !== "any" && (asset.patreon_intent || "unset") !== assetPatreon) return false;
    if (assetTag && !(asset.tags || []).includes(assetTag.trim().toLowerCase())) return false;
    if (assetKind !== "any" && assetMediaKind(asset) !== assetKind) return false;
    if (assetLength !== "any" && !["video", "audio"].includes(assetMediaKind(asset))) return false;
    if (assetLength !== "any" && (durationsLoading || !matchesDuration(assetDurations[asset.id], assetLength, assetLengthMin, lengthMaximum))) return false;
    const posts = (asset.publications || []).filter(post => !assetDestination || post.destination.toLowerCase() === assetDestination.toLowerCase());
    return (!assetDestination || assetSharing !== "any" || posts.length > 0) && matchesSharing(posts, assetSharing);
  }).sort((a, b) => {
    if (assetSort === "name") return a.name.localeCompare(b.name);
    if (assetSort === "shortest" || assetSort === "longest") {
      const aDuration = assetDurations[a.id]; const bDuration = assetDurations[b.id];
      if (!aDuration) return bDuration ? 1 : 0;
      if (!bDuration) return -1;
      return assetSort === "shortest" ? aDuration - bDuration : bDuration - aDuration;
    }
    return 0;
  });

  const scopedSearchAssets = searchResults.assets.items.filter(a=>assetInScope(a,{lifecycle:lifecycleScope,includeIntermediates:searchIntermediates || searchRole==="intermediate"}));
  const families = buildAssetFamilies(scopedAssets, filteredAssets, {lifecycle:lifecycleScope,includeIntermediates:showIntermediates,mainOnly:assetRole==="main"});
  const derivedFamily = families.find(node => node.asset.id === derivedFamilyId);
  const derivedAssets = derivedFamily ? matchingDescendants(derivedFamily) : [];
  const openLinkedAsset = async (id: string) => {
    const linked = assets.find(asset => asset.id === id);
    if (linked && selectedSession?.id === linked.session_id) { await openAsset(linked); return; }
    try { const result = await get<{ asset: Asset }>(`/assets/${encodeURIComponent(id)}`); if (selectedSession?.id !== result.asset.session_id) { const parentSession = await get<{ session: Session }>(`/sessions/${result.asset.session_id}`); await showSession(parentSession.session, tab === "search" ? "search" : returnTab); } await openAsset(result.asset); } catch (err) { setError(errorText(err)); }
  };

  return <div className="h-full overflow-y-auto bg-bg text-text p-5 space-y-5">
    <header className="flex flex-wrap justify-between gap-3 items-center">
      <div><h1 className="text-xl font-semibold">Content Catalog</h1><p className="text-sm text-text-muted">Production relationships and verified publication evidence</p></div>
      <button className="border border-border rounded px-3 py-1.5 text-sm hover:bg-bg-hover" onClick={() => (async () => { await refresh(); if (selectedSession) { const asset = selectedAsset; await openSession(selectedSession); if (asset) await openAsset(asset); } })().catch(e => setError(errorText(e)))}>Refresh</button>
    </header>
    <div className="flex flex-wrap gap-2">{["brands", "sessions", "assets", "publications", "hostings"].map(k => <span key={k} className="rounded border border-border px-3 py-1 text-sm"><strong>{overview[k] ?? 0}</strong> {k}</span>)}</div>
    {error && <div className="rounded border border-red-500/50 bg-red-500/10 p-3 text-sm text-red-400">{error}</div>}
    {notice && <div className="rounded border border-green-500/50 bg-green-500/10 p-3 text-sm">{notice}</div>}
    <nav className="flex gap-1 border-b border-border">{(["sessions", "search", "brands"] as const).map(t => <button key={t} onClick={() => { const url = new URL(window.location.href); url.searchParams.delete("catalog_session"); window.history.replaceState(null, "", url); ++sessionRequest.current; ++durationRequest.current; activeSessionId.current = ""; closeAsset(); setDerivedFamilyId(null); setSelectedSession(null); setModal(null); setTab(t); }} className={`px-3 py-2 text-sm capitalize ${tab === t ? "border-b-2 border-accent text-accent" : "text-text-muted"}`}>{t}</button>)}</nav>

    {tab === "search" && <section className="space-y-4">
      <div><h2 className="text-lg font-semibold">Search Catalog</h2><LifecycleSelect value={lifecycleScope} onChange={setLifecycleScope} /><p className="text-sm text-text-muted">Searches sessions and linked files. Storage folders are never scanned automatically.</p></div>
      <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-4">
        <input className={`${inputClass} md:col-span-2`} type="search" value={searchText} onChange={e => setSearchText(e.target.value)} placeholder="Search titles, file names, notes…" aria-label="Search Catalog" />
        <select className={inputClass} value={searchType} onChange={e => { setSearchType(e.target.value); if (e.target.value === "sessions") { setSearchKind(""); setSearchLineage(""); setSearchRole(""); setSearchIntermediates(false); setSearchReview(""); setSearchAvailability("any"); } }} aria-label="Result type"><option value="all">All records</option><option value="assets">Files</option><option value="sessions">Sessions</option></select>
        <select className={inputClass} value={searchBrand} onChange={e => setSearchBrand(e.target.value)} aria-label="Brand"><option value="">All brands</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select>
        <select className={inputClass} value={searchKind} disabled={searchType === "sessions"} onChange={e => setSearchKind(e.target.value)} aria-label="File type"><option value="">All file types</option>{["video", "image", "audio", "other"].map(kind => <option key={kind} value={kind}>{kind}</option>)}</select>
        <select className={inputClass} value={searchRole} disabled={searchType==="sessions"} onChange={e=>setSearchRole(e.target.value)} aria-label="Search purpose"><option value="">All content purposes</option><option value="main">Main assets</option><option value="derivative">Derivatives</option><option value="intermediate">Intermediates (inspection)</option><option value="unspecified">Unclassified</option></select>
        <label className="text-xs flex items-center gap-1"><input type="checkbox" checked={searchIntermediates} onChange={e=>setSearchIntermediates(e.target.checked)} disabled={searchType==="sessions"} />Include intermediates</label>
        <select className={inputClass} value={searchLineage} disabled={searchType === "sessions"} onChange={e => setSearchLineage(e.target.value)} aria-label="File lineage"><option value="">Sources and derivatives</option><option value="source">Source files</option><option value="derivative">Derivatives</option></select>
        <select className={inputClass} value={searchReview} disabled={searchType === "sessions"} onChange={e => setSearchReview(e.target.value)} aria-label="Review status"><option value="">Any review state</option>{["pending", "approved", "rejected"].map(state => <option key={state} value={state}>{state}</option>)}</select>
        <select className={inputClass} value={searchFavorite} disabled={searchType === "sessions"} onChange={e => setSearchFavorite(e.target.value)} aria-label="Favorite"><option value="">Any favorite state</option><option value="true">⭐ Favorites</option><option value="false">Not favorite</option></select>
        <select className={inputClass} value={searchPatreon} disabled={searchType === "sessions"} onChange={e => setSearchPatreon(e.target.value)} aria-label="Patreon intent"><option value="">Any Patreon intent</option><option value="unset">Patreon unset</option><option value="free">Patreon free</option><option value="paid">Patreon paid</option></select>
        <input className={inputClass} value={searchTag} disabled={searchType === "sessions"} onChange={e => setSearchTag(e.target.value)} placeholder="Exact tag, e.g. share-next" aria-label="Asset tag" />
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
      {(searchType === "all" || searchType === "assets") && <div className="space-y-2"><h3 className="font-semibold">Files <span className="text-text-muted font-normal">{scopedSearchAssets.length}</span></h3>{scopedSearchAssets.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching linked files.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(230px, 1fr))", gap: 12, alignItems: "stretch" }}>{scopedSearchAssets.map(hit => <div key={hit.id} className="min-w-0 space-y-2"><SessionAssetCard asset={hit} projectId={projectId} duration={hit.duration_ms} onToggleFavorite={toggleFavorite} onOpen={id => { const found = searchResults.assets.items.find(item => item.id === id); (found ? openSearchAsset(found) : openLinkedAsset(id)).catch(e => setSearchError(errorText(e))); }} /><div className="text-xs text-text-muted px-1"><p className="truncate">{hit.session_title} · {recordingDate(hit.session_date)} · {brandName(hit.brand_id)}</p><p className="line-clamp-2">{hit.match_reason}</p></div></div>)}</div>{searchResults.assets.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("assets", searchResults.assets.next_cursor!)}>More files</button>}</div>}
      {!assetSearchOnly && !searchDestination && (searchType === "all" || searchType === "sessions") && <div className="space-y-2"><h3 className="font-semibold">Sessions <span className="text-text-muted font-normal">{searchResults.sessions.items.length}</span></h3>{searchResults.sessions.items.length === 0 && !searchLoading && <p className="text-sm text-text-muted">No matching sessions.</p>}<div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12, alignItems: "stretch" }}>{searchResults.sessions.items.map(hit => <button key={hit.id} type="button" style={previewCardStyle} className="overflow-hidden rounded-xl border border-border text-left hover:border-accent/60" onClick={() => showSession(hit, "search")}><PreviewImage src={previewURL(projectId, "sessions", hit.id)} alt={`Preview of ${hit.title}`} fallback="▣" /><div className="p-3 text-sm" style={{ flex: 1 }}><div className="font-medium line-clamp-2" style={{ minHeight: 40 }}>{hit.title}</div><div className="mt-1 text-xs text-text-muted">{hit.brand_name} · {recordingDate(hit.session_date)} · {hit.asset_count} files</div></div></button>)}</div>{searchResults.sessions.next_cursor && <button disabled={searchLoading} className="text-sm text-accent underline" onClick={() => loadMore("sessions", searchResults.sessions.next_cursor!)}>More sessions</button>}</div>}

    </section>}

    {tab === "brands" && <div className="grid gap-5 lg:grid-cols-2">
      <section className="space-y-3"><h2 className="font-semibold">Brands</h2>{brands.map(b => <div key={b.id} className="rounded border border-border p-3 space-y-2 text-sm"><div className="font-medium">{b.name}</div><div className="text-text-muted">{b.storage_root}</div><div>{b.host_provider ? `${b.host_provider} · connection ${b.host_connection_id} · library ${b.host_library_id}` : "No cloud host configured"}</div><EditBrand key={`${b.id}:${b.host_connection_id}:${b.host_library_id}`} brand={b} hosts={hosts} busy={busy} onSubmit={input => run(async () => { await action("content_catalog_brands_update", { id: b.id, ...input }); }, "Brand updated")} /></div>)}</section>
      <CreateBrand busy={busy} hosts={hosts} onSubmit={input => run(async () => { await action("content_catalog_brands_create", input); }, "Brand created")} />
    </div>}

    {tab === "sessions" && !selectedSession && <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="text-lg font-semibold">Sessions</h2><p className="text-sm text-text-muted">Select a session to see its files and publication status.</p></div><button type="button" className={buttonClass} onClick={() => setModal("new-session")}>+ New session</button></div>
      <div className="flex flex-wrap items-center gap-3" aria-label="Session filters">
        <label className="flex items-center gap-2 text-sm"><span>Brand</span><select className={inputClass} style={{ width: "auto", minWidth: 180, maxWidth: "100%" }} value={sessionBrand} onChange={e => setSessionBrand(e.target.value)} aria-label="Session brand"><option value="">All brands</option>{brands.map(brand => <option key={brand.id} value={brand.id}>{brand.name}</option>)}</select></label>
        <LifecycleSelect value={lifecycleScope} onChange={setLifecycleScope} />
        <span className="text-xs text-text-muted" role="status">{brandSessionsLoading ? "Loading sessions…" : `Showing ${filteredSessions.length} of ${overview.sessions ?? sessions.length} sessions`}</span>
        {sessionBrand && <button type="button" className="text-xs text-accent underline" onClick={() => setSessionBrand("")}>Clear brand filter</button>}
      </div>
      {brandSessionsError && <p className="text-sm text-red-400">Could not load sessions for this brand: {brandSessionsError}</p>}
      {sessions.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No sessions in this lifecycle view.</p>}
      {!brandSessionsLoading && !brandSessionsError && sessions.length > 0 && filteredSessions.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No sessions for this brand in this lifecycle view.</p>}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(190px, 1fr))", gap: 12, alignItems: "stretch" }}>{filteredSessions.map(s => <button key={s.id} data-session-id={s.id} style={previewCardStyle} className="overflow-hidden rounded-xl border border-border text-left text-sm transition-colors hover:border-accent/60" onClick={() => showSession(s)}><PreviewImage src={previewURL(projectId, "sessions", s.id)} alt={`Preview of ${s.title}`} fallback="▣" /><div className="p-3" style={{ flex: 1 }}><div className="font-medium line-clamp-2" style={{ minHeight: 40 }}>{s.title}{s.lifecycle === "archived" && <span className="ml-2 text-xs text-yellow-400">Archived</span>}</div><div className="mt-1 text-xs text-text-muted">{brandName(s.brand_id)} · {recordingDate(s.session_date)}</div></div></button>)}</div>
    </div>}

    {tab === "sessions" && selectedSession && <div className="space-y-5">
      <div className="border-b border-border pb-4">
        <button type="button" className="mb-3 text-sm text-accent underline" onClick={backToSessions}>← All sessions</button>
        <div className="flex flex-wrap items-start justify-between gap-4"><div className="min-w-0"><h2 className="text-2xl font-semibold">{selectedSession.title}</h2>{selectedSession.lifecycle === "archived" && <p className="text-sm text-yellow-400">Archived session · {selectedSession.archive_reason}</p>}<p className="mt-1 text-sm text-text-muted">{brandName(selectedSession.brand_id)} · {recordingDate(selectedSession.session_date)} · {selectedSession.status}</p></div><div className="flex flex-wrap gap-2"><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" onClick={() => manageLifecycle("session", selectedSession.lifecycle === "archived" ? "restore" : "archive")}>{selectedSession.lifecycle === "archived" ? "Restore session" : "Archive session"}</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" onClick={() => setModal("edit-session")}>Edit session</button><button type="button" className={buttonClass} disabled={selectedSession.lifecycle === "archived"} onClick={() => setModal("add-file")}>+ Add file</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" disabled={!assets.length} onClick={() => { setSelectedAsset(null); setEditingPublication(null); setModal("publication"); }}>+ Record post</button><button type="button" className="rounded border border-border px-3 py-1.5 text-sm" onClick={() => setModal("link-gig")}>Link Gig</button></div></div>
      </div>
      <section className="space-y-3"><div className="flex items-center justify-between gap-3"><h3 className="text-lg font-semibold">Assets</h3><span className="text-xs text-text-muted">{assetView === "grouped" ? `${families.length} groups · ${families.reduce((n,g)=>n+g.derivedCount,0)} selected outputs` : `Showing ${filteredAssets.length} of ${scopedAssets.length} files`}</span></div>
        <div className="flex flex-wrap items-center gap-2" aria-label="Asset toolbar">
 <LifecycleSelect value={lifecycleScope} onChange={setLifecycleScope} />
 {checkedAssets.length > 0 && <><span className="text-xs">{checkedAssets.length} selected</span><button type="button" className="text-xs text-accent underline" onClick={() => setModal("labels")}>Edit purpose / labels</button>{(["archive","move","restore"] as const).map(operation => <button key={operation} type="button" className="text-xs text-accent underline" onClick={() => manageLifecycle("asset",operation,assets.filter(asset => checkedAssets.includes(asset.id)))}>{operation}</button>)}</>}
          <div className="inline-flex rounded-lg border border-border p-0.5" role="group" aria-label="Asset display">{([["grid", "Grid"], ["grouped", "Grouped by main"]] as const).map(([view, label]) => <button key={view} type="button" aria-pressed={assetView === view} className={`rounded px-2 py-1 text-xs ${assetView === view ? "bg-accent/15 text-accent" : "text-text-muted"}`} onClick={() => setAssetView(view)}>{label}</button>)}</div>
          <input className={inputClass} style={{ flex: "1 1 180px", maxWidth: 360 }} type="search" value={assetQuery} onChange={e => setAssetQuery(e.target.value)} placeholder="Find name, ID or description" aria-label="Find session file" />
          <button type="button" aria-haspopup="dialog" className={`rounded border px-3 py-1.5 text-sm ${activeAssetFilterCount ? "border-accent text-accent" : "border-border"}`} onClick={() => setModal("filters")}>Filters{activeAssetFilterCount > 0 ? ` (${activeAssetFilterCount})` : ""}</button>
          {assetFilterSummary && <span className="truncate text-xs text-text-muted" style={{ flex: "1 1 120px", minWidth: 0 }} title={assetFilterSummary}>{assetFilterSummary}</span>}
          {assetFiltersActive && <button type="button" className="text-xs text-accent underline" onClick={() => resetAssetFilters(true)}>Clear filters</button>}
        </div>
        {assets.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">No assets in this lifecycle view. Choose All to inspect other linked files.</p>}
        {assets.length > 0 && filteredAssets.length === 0 && <p className="rounded border border-border p-5 text-sm text-text-muted">{durationsLoading && assetLength !== "any" ? "Loading file lengths…" : "No files match these filters."}</p>}
        {assetView === "grid" ? <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))", gap: 12, alignItems: "stretch" }}>{filteredAssets.map(asset => <div key={asset.id} className="min-w-0 flex flex-col gap-1"><label className="text-xs flex items-center gap-2"><input type="checkbox" aria-label={`Select ${asset.name}`} checked={checkedAssets.includes(asset.id)} onChange={e => setCheckedAssets(current => e.target.checked ? [...current,asset.id] : current.filter(id => id !== asset.id))} />Select</label><SessionAssetCard asset={asset} projectId={projectId} duration={assetDurations[asset.id]} onToggleFavorite={toggleFavorite} onOpen={openLinkedAsset} /></div>)}</div>
          : <><p className="text-xs text-text-muted">Grouped using recorded source links, including hidden provenance. Outputs appear once under the nearest visible main asset.{assetFiltersActive ? " Parent context stays visible for matching derivatives." : ""}</p><div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))", gap: 12, alignItems: "stretch" }}>{families.map(node => <AssetFamilyCard key={node.asset.id} node={node} projectId={projectId} durations={assetDurations} filtering={assetFiltersActive} onOpen={openLinkedAsset} onDerived={setDerivedFamilyId} onToggleFavorite={toggleFavorite} />)}</div></>}

      </section>
      {assets.some(a => a.publications?.length) && <section className="rounded-xl border border-border p-4 space-y-2 text-sm"><h3 className="font-semibold">Posts for this session</h3>{Array.from(new Map(assets.flatMap(a => a.publications || []).map(p => [p.id, p])).values()).map(p => <div key={p.id} className="flex flex-wrap items-center gap-2 border-t border-border pt-2 text-xs"><PublicationIcons items={[p]} /><strong>{p.title || p.destination}</strong><span>{publicationLabel(p.status)}</span><span className="text-text-muted">{p.asset_ids?.length || 1} files</span>{p.external_url && <a href={p.external_url} target="_blank" rel="noreferrer" className="text-accent underline">View post</a>}<button type="button" className="ml-auto text-accent underline" onClick={() => { setEditingPublication(p); setSelectedAsset(null); setModal("publication"); }}>Edit post</button></div>)}</section>}
      <section className="rounded-xl border border-border p-4 space-y-2 text-sm"><h3 className="font-semibold">Session info</h3>{selectedSession.notes && <p className="text-text-muted">{selectedSession.notes}</p>}<p className="text-text-muted">Gigs: {gigs.length ? gigs.map(g => `#${g.gig_id}${g.role ? ` (${g.role})` : ""}`).join(", ") : "none linked"}</p></section>
    </div>}

    {modal === "filters" && selectedSession && <Modal title="Filter assets" onClose={() => setModal(null)}>
      <div className="space-y-4">
        <div><h3 className="text-lg font-semibold">Filter assets</h3><p className="text-xs text-text-muted">Showing {filteredAssets.length} of {assets.length} files</p></div>
        <div className="flex flex-wrap gap-2" aria-label="Quick publication filters">{([[
            "any", "All files"], ["none", "No post recorded"], ["not_verified", "No verified live post"], ["verified", "Verified live"]] as const).map(([state, label]) => <button key={state} type="button" onClick={() => setAssetSharing(state)} className={`rounded-full border px-3 py-1 text-xs ${assetSharing === state ? "border-accent bg-accent/10 text-accent" : "border-border text-text-muted hover:text-text"}`}>{label} <strong>{sharingCount(state)}</strong></button>)}</div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 12 }}>
          <label className="block text-xs space-y-1"><span>Purpose</span><select className={inputClass} value={assetRole} onChange={e=>{setAssetRole(e.target.value);if(e.target.value==="intermediate")setShowIntermediates(true);}} aria-label="Asset purpose"><option value="">All content</option><option value="main">Main assets</option><option value="derivative">Derivatives</option><option value="intermediate">Intermediates</option><option value="unspecified">Unclassified</option></select></label>
          <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={showIntermediates} onChange={e=>{setShowIntermediates(e.target.checked);if(!e.target.checked && assetRole==="intermediate")setAssetRole("");}} />Inspect intermediate assets</label>
          <label className="block text-xs space-y-1"><span>Source links</span><select className={inputClass} value={assetLineage} onChange={e => setAssetLineage(e.target.value)} aria-label="Source links"><option value="any">All assets</option><option value="source">Sources / no parent linked</option><option value="derivative">Derivatives only</option></select></label>
          <label className="block text-xs space-y-1"><span>Content type</span><select className={inputClass} value={assetKind} onChange={e => setAssetKind(e.target.value)} aria-label="Content type"><option value="any">All content types</option><option value="video">Videos</option><option value="image">Images</option><option value="audio">Audio</option><option value="other">Other files</option></select></label>
          <label className="block text-xs space-y-1"><span>Publication status</span><select className={inputClass} value={assetSharing} onChange={e => setAssetSharing(e.target.value)} aria-label="Publication status"><option value="any">Any sharing status</option><option value="none">No post recorded</option><option value="not_verified">No verified live post</option><option value="verified">Verified live</option><option value="reported">Reported live, unverified</option><option value="scheduled">Scheduled or submitted</option><option value="failed">Failed</option></select></label>
          <label className="block text-xs space-y-1"><span>Platform</span><select className={inputClass} value={assetDestination} onChange={e => setAssetDestination(e.target.value)} aria-label="Platform"><option value="">All platforms</option>{assetDestinations.map(destination => <option key={destination} value={destination}>{destination}</option>)}</select></label>
          <label className="block text-xs space-y-1"><span>Favorite</span><select className={inputClass} value={assetFavorite} onChange={e => setAssetFavorite(e.target.value)} aria-label="Favorite filter"><option value="any">Any</option><option value="favorite">⭐ Favorites</option><option value="not_favorite">Not favorite</option></select></label>
          <label className="block text-xs space-y-1"><span>Patreon intent</span><select className={inputClass} value={assetPatreon} onChange={e => setAssetPatreon(e.target.value)} aria-label="Patreon intent filter"><option value="any">Any</option><option value="unset">Unset</option><option value="free">Free</option><option value="paid">Paid</option></select></label>
          <label className="block text-xs space-y-1"><span>Exact tag</span><input className={inputClass} value={assetTag} onChange={e => setAssetTag(e.target.value)} placeholder="share-next" aria-label="Exact tag filter" /></label>
          <label className="block text-xs space-y-1"><span>Sort files</span><select className={inputClass} value={assetSort} onChange={e => setAssetSort(e.target.value)} aria-label="Sort files"><option value="newest">Recently added</option><option value="name">Name A–Z</option><option value="shortest">Shortest first</option><option value="longest">Longest first</option></select></label>
        </div>
        <DurationFilter mode={assetLength} minimum={assetLengthMin} maximum={lengthMaximum} limit={lengthSliderLimit} loading={durationsLoading} onMode={mode => { setAssetLength(mode); if (mode === "any") { setAssetLengthMin(0); setAssetLengthMax(null); } }} onRange={(minimum, maximum) => { setAssetLengthMin(minimum); setAssetLengthMax(maximum); setAssetLength("range"); }} />
        <p className="text-xs text-text-muted">Sharing filters use recorded posts; only “Verified live” confirms publication.{durationsUnavailable > 0 ? ` ${durationsUnavailable} file length${durationsUnavailable === 1 ? " is" : "s are"} unknown.` : ""}</p>
        {(durationsError || durationsFailed > 0) && <div className="flex flex-wrap items-center gap-2 text-xs text-yellow-400">Could not read {durationsFailed || "some"} file length{durationsFailed === 1 ? "" : "s"} from Media. <button type="button" className="underline" onClick={() => loadDurations(selectedSession)}>Retry</button></div>}
        <div className="flex items-center justify-between gap-3 border-t border-border pt-3"><button type="button" className="text-sm text-accent underline" onClick={() => resetAssetFilters()}>Reset filters</button><button type="button" className={buttonClass} onClick={() => setModal(null)}>Show {filteredAssets.length} file{filteredAssets.length === 1 ? "" : "s"}</button></div>
      </div>
    </Modal>}

    {derivedFamily && selectedSession && !selectedAsset && !modal && <Modal title={`Derived assets · ${derivedFamily.asset.name}`} wide onClose={() => setDerivedFamilyId(null)}>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2 text-sm">
        <button type="button" className="text-accent underline" onClick={() => openLinkedAsset(derivedFamily.asset.id)}>Open main asset</button>
        <span className="text-xs text-text-muted">{assetFiltersActive ? `${derivedAssets.length} matching of ${derivedFamily.derivedCount}` : derivedAssets.length} derived assets</span>
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(min(100%, 220px), 1fr))", gap: 12, alignItems: "stretch" }} aria-label="Derived asset grid">
        {derivedAssets.map(node => <SessionAssetCard key={node.asset.id} asset={node.asset} projectId={projectId} duration={assetDurations[node.asset.id]} onToggleFavorite={toggleFavorite} onOpen={openLinkedAsset} />)}
      </div>
      {!derivedAssets.length && <p className="text-sm text-text-muted">No selected outputs match the current filters.</p>}
    </Modal>}

    {selectedAsset && !modal && <Modal title={selectedAsset.name} wide onClose={closeAsset}>
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
        <span className={selectedAsset.eligible === false ? "text-yellow-400" : "text-text-muted"}>{selectedAsset.eligible === false ? "Archived / unavailable for processing or new publication" : "Active"}</span>
        <AssetLabels asset={selectedAsset} />
        <button type="button" className="rounded border border-border px-2 py-1" onClick={() => setModal("labels")}>Edit purpose / labels</button>
        {(["archive", "move", "restore"] as const).filter(operation => operation === "move" || (operation === "restore" ? selectedAsset.eligible === false : selectedAsset.lifecycle !== "archived")).map(operation => <button key={operation} type="button" className="rounded border border-border px-2 py-1 capitalize" onClick={() => manageLifecycle("asset",operation)}>{operation}</button>)}
      </div>
      {selectedAsset.archive_reason && <p className="mb-3 text-xs text-text-muted">{selectedAsset.archive_reason} · {selectedAsset.archived_at}{selectedAsset.original_session_id ? ` · Original session ${short(selectedAsset.original_session_id)}` : ""}</p>}
      {derivedFamily && <button type="button" className="mb-3 text-sm text-accent underline" onClick={closeAsset}>← Derived assets</button>}
      {error && <p className="mb-3 rounded border border-red-500/50 p-3 text-sm text-red-400">{error}</p>}
      {notice && <p className="mb-3 rounded border border-green-500/50 p-3 text-sm">{notice}</p>}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 360px), 1fr))", gap: 20 }}>
        <div className="min-w-0 space-y-3">
          <AssetViewer asset={selectedAsset} projectId={projectId} />
          <div className="text-sm space-y-1"><h4 className="font-medium">Description</h4><p className="text-text-muted whitespace-pre-wrap">{selectedAsset.description || "No Media description available"}</p>{selectedAsset.description && <p className="text-xs text-text-muted">From Media{selectedAsset.description_source ? ` · ${selectedAsset.description_source}` : ""}{selectedAsset.description_updated_at ? ` · Updated ${new Date(selectedAsset.description_updated_at).toLocaleString()}` : ""}</p>}<p className="text-xs text-text-muted">Media: {selectedAsset.media_status === "completed" ? "ready" : selectedAsset.media_status}{selectedAsset.media_rating ? ` · ${selectedAsset.media_rating}` : ""}</p>{selectedAsset.media_error && <p className="text-xs text-yellow-400">Media metadata could not be refreshed.</p>}</div>
          {assetMedia && <div className="text-sm space-y-1"><div>{assetMedia.title || "Media metadata"} · {assetMedia.probe_status || "unknown"} · {assetMedia.audience_rating || "unrated"}</div><div className="text-xs text-text-muted">{assetMedia.duration_ms ? `${Math.round(assetMedia.duration_ms / 1000)}s · ` : ""}{assetMedia.width && assetMedia.height ? `${assetMedia.width} × ${assetMedia.height} · ` : ""}Transcript: {assetMedia.transcript_status || "not available"}</div></div>}
          <div className="space-y-2"><h4 className="text-sm font-medium">{lineageBadge(selectedAsset)}</h4>{assetSources.length ? <ParentLinks asset={{ ...selectedAsset, sources: assetSources }} projectId={projectId} onOpen={openLinkedAsset} /> : <p className="text-xs text-text-muted">No source linked. This does not confirm the file is an original.</p>}{!!selectedAsset.ancestors?.length && <details className="text-xs"><summary>Provenance ({selectedAsset.ancestors.length} ancestors)</summary><div className="mt-2 space-y-1">{selectedAsset.ancestors.map(parent=><button key={parent.id} type="button" className="block text-accent underline" onClick={()=>openLinkedAsset(parent.id)}>{parent.name} · {parent.role || "unclassified"}{parent.lifecycle==="archived" || parent.session_lifecycle==="archived" ? " · Archived" : ""}</button>)}</div></details>}</div>
          <div className="flex flex-wrap gap-2 text-sm">{(["pending", "approved", "rejected"] as const).map(state => <button disabled={busy || selectedAsset.review_status === state} key={state} className="border border-border rounded px-2 py-1 disabled:opacity-40" onClick={() => run(async () => { await action("content_catalog_assets_review", { asset_id: selectedAsset.id, review_status: state }); await openAsset(selectedAsset); }, `Review set to ${state}`)}>{state}</button>)}</div>
        </div>
        <div className="min-w-0 space-y-3">
          <LifecycleHistory entity="asset" id={selectedAsset.id} get={get} />
 <h4 className="font-medium">Cloud hosting</h4>
          <button disabled={busy || pendingHosting || selectedAsset.review_status !== "approved" || selectedAsset.eligible === false} className="rounded border border-accent px-3 py-1.5 text-sm disabled:opacity-40" onClick={requestHosting}>Host approved asset</button>
          {pendingHosting && <p className="text-xs text-text-muted" role="status">Updates automatically while this request is pending.</p>}
          {hostingRefreshError && <p className="text-xs text-yellow-400">Could not refresh progress. Retrying… {hostingRefreshError}</p>}
          <div className="space-y-2">{hostingIntents.slice(0,1).filter(i=>i.status!=="submitted").map(i=><div key={i.id} className="rounded border border-border p-3 text-sm" role="status"><p className="font-medium">{i.status==="waiting_checksum"?"Waiting for checksum verification":i.status==="cancelled"?"Hosting request cancelled":i.status==="blocked"?"Hosting request needs attention":"Checksum verification failed"}</p><p className="text-xs text-text-muted">{i.title}</p>{i.status==="waiting_checksum" && <p className="mt-1 text-xs text-text-muted">Storage: {i.checksum_status || "pending"}. Upload resumes automatically after verification and eligibility checks.</p>}{i.error && <p className="mt-1 text-xs text-yellow-400">{i.error}</p>}{i.status==="waiting_checksum" && <button type="button" disabled={busy} className="mt-2 text-xs text-accent underline" onClick={()=>run(async()=>{await action("content_catalog_hosting_cancel",{intent_id:i.id});const result=await get<HostingResponse>("/hostings",{asset_id:selectedAsset.id});setHostingIntents(result.hosting_intents||[]);},"Hosting request cancelled")}>Cancel waiting upload</button>}</div>)}</div>
          <div className="space-y-2">{hostings.map(h => <div key={h.id} className="rounded border border-border p-3 text-sm"><div className="flex flex-wrap items-center gap-2"><span className="font-medium">{h.provider}: {h.status}</span>{h.remote_id && <span className="text-text-muted">{short(h.remote_id)}</span>}{h.embed_url && <a className="text-accent underline" href={h.embed_url} target="_blank" rel="noreferrer">Open host</a>}</div>{h.provider_stage && <p className="mt-1 text-xs text-text-muted">{hostingStageLabel(h.provider_stage)}{h.provider_status!=null?` · Provider status ${h.provider_status}`:""}</p>}{h.encode_progress!=null && <div className="mt-2 space-y-1"><div className="flex justify-between text-xs"><span>Encoding</span><span>{Math.round(h.encode_progress)}%</span></div><progress className="h-2 w-full accent-accent" value={h.encode_progress} max={100} aria-label="Video encoding progress" /></div>}{transcodingMessages(h.transcoding_messages).length>0 && <ul className="mt-2 space-y-1 text-xs text-text-muted">{transcodingMessages(h.transcoding_messages).map((message,index)=><li key={index}>{message}</li>)}</ul>}{h.error && <p className="mt-2 text-red-400">{h.error}</p>}{h.check_error && <p className="mt-2 text-xs text-yellow-400">Last check failed: {h.check_error}. Pending checks will retry automatically.</p>}{h.last_checked_at && <p className="mt-2 text-xs text-text-muted">Last checked {new Date(h.last_checked_at).toLocaleTimeString()}</p>}{h.remote_id && <button disabled={busy} className="mt-2 text-xs text-accent underline" onClick={() => run(async () => { await action("content_catalog_hosting_check", { id: h.id });const result=await get<HostingResponse>("/hostings",{asset_id:selectedAsset.id});setHostings(result.hostings||[]); }, "Host checked")}>Check readiness</button>}</div>)}</div>
          <div className="space-y-2 text-sm"><div className="flex items-center justify-between gap-2"><h4 className="font-medium">Platforms and posts</h4><button type="button" className="rounded border border-accent px-2 py-1 text-xs text-accent" onClick={() => { setEditingPublication(null); setModal("publication"); }}>+ Add platform</button></div>{!selectedAsset.publications?.length && <p className="text-xs text-text-muted">Nothing recorded for this file yet.</p>}{(selectedAsset.publications || []).map(p => <div key={p.id} className="flex flex-wrap items-center gap-2 rounded border border-border p-2 text-xs"><PublicationIcons items={[p]} /><span className="font-medium">{publicationLabel(p.status)}</span><span className="text-text-muted">{p.asset_ids?.length || 1} file{p.asset_ids?.length === 1 ? "" : "s"}</span>{p.account_ref && <span className="text-text-muted">{p.account_ref}</span>}{p.audience && <span className="text-text-muted">{p.audience}</span>}{p.actual_at && <span className="text-text-muted">{p.actual_at.slice(0, 10)}</span>}{p.planned_at && !p.actual_at && <span className="text-text-muted">Planned {p.planned_at.slice(0, 10)}</span>}{p.external_url && <a className="text-accent underline" href={p.external_url} target="_blank" rel="noreferrer">View post</a>}{p.external_post_id && <span className="text-text-muted">ID {p.external_post_id}</span>}{p.evidence_source && <span className="text-text-muted">via {p.evidence_source}</span>}{p.failure_details && <span className="text-red-400">{p.failure_details}</span>}<button type="button" className="ml-auto text-accent underline" onClick={() => { setEditingPublication(p); setModal("publication"); }}>Update status</button></div>)}</div>
        </div>
      </div>
      {assetLoading && <p className="mt-3 text-xs text-text-muted" role="status">Loading file details…</p>}
    </Modal>}

    {modal === "lifecycle" && lifecycleTarget && <Modal title={`${lifecycleTarget.operation} ${lifecycleTarget.entity === "asset" ? `${lifecycleTarget.assets?.length || 0} asset(s)` : "session"}`} onClose={() => setModal(null)}>
      <LifecycleForm key={`${lifecycleTarget.entity}:${lifecycleTarget.operation}`} target={lifecycleTarget} sessions={destinationSessions} busy={busy} onSubmit={input => run(async () => {
        const savedAsset = selectedAsset;
        await action(`content_catalog_${lifecycleTarget.entity === "asset" ? "assets" : "sessions"}_${lifecycleTarget.operation}`, input);
        setModal(null); setCheckedAssets([]);
        if (selectedSession) { const result = await get<{ session: Session }>(`/sessions/${selectedSession.id}`); await openSession(result.session); }
        if (savedAsset) await openAsset(savedAsset);
      }, "Catalog lifecycle updated")} />
      {error && <p className="mt-3 text-sm text-red-400">{error}</p>}
    </Modal>}

    {modal === "labels" && (selectedAsset || checkedAssets.length > 0) && <Modal title="Edit purpose and labels" onClose={() => setModal(null)}>
      <AssetLabelsForm assets={selectedAsset ? [selectedAsset] : assets.filter(asset => checkedAssets.includes(asset.id))} busy={busy} onSubmit={input => run(async () => {
        await action("content_catalog_assets_labels_update", input); setModal(null); setCheckedAssets([]);
        if (selectedSession) { const current = selectedAsset; await openSession(selectedSession); if (current) await openAsset(current); }
      }, "Catalog labels updated")} />
      {error && <p className="mt-3 text-sm text-red-400">{error}</p>}
    </Modal>}

    {modal === "new-session" && <Modal title="New session" onClose={() => setModal(null)}><CreateSession busy={busy} brands={brands} onSubmit={input => run(async () => { const result = await action<{ session: Session }>("content_catalog_sessions_create", input); setModal(null); await showSession(result.session); }, "Session created")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
    {modal === "edit-session" && selectedSession && <Modal title="Edit session" onClose={() => setModal(null)}><EditSession key={selectedSession.id} session={selectedSession} busy={busy} onSubmit={input => run(async () => { const result = await action<{ session: Session }>("content_catalog_sessions_update", { id: selectedSession.id, ...input }); setModal(null); await openSession(result.session); }, "Session updated")} />{error && <p className="mt-2 text-sm text-red-400">{error}</p>}</Modal>}
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
  const [brand, setBrand] = useState(""); const [title, setTitle] = useState(""); const [date, setDate] = useState("");
  return <form className="rounded border border-border p-3 space-y-2" onSubmit={e => { e.preventDefault(); onSubmit({ brand_id: brand, title, session_date: date }); }}><h2 className="font-semibold text-sm">New session</h2><select className={inputClass} required value={brand} onChange={e => setBrand(e.target.value)}><option value="">Choose brand</option>{brands.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}</select><input className={inputClass} placeholder="Session title" required value={title} onChange={e => setTitle(e.target.value)} /><label className="block text-xs text-text-muted">Recording date (optional; leave blank if unknown)<input className={inputClass} type="date" value={date} onChange={e => setDate(e.target.value)} /></label><button className={buttonClass} disabled={busy}>Create session</button></form>;
}
function EditSession({ busy, session, onSubmit }: { busy: boolean; session: Session; onSubmit: (x: Record<string, unknown>) => void }) {
  const [title, setTitle] = useState(session.title); const [date, setDate] = useState(session.session_date); const [notes, setNotes] = useState(session.notes);
  return <form className="space-y-3" onSubmit={e => { e.preventDefault(); onSubmit({ title: title.trim(), session_date: date, notes }); }}><h3 className="font-semibold">Edit session</h3><label className="block text-xs">Title<input className={inputClass} required value={title} onChange={e => setTitle(e.target.value)} /></label><label className="block text-xs">Recording date (optional)<input className={inputClass} type="date" value={date} onChange={e => setDate(e.target.value)} /></label><p className="text-xs text-text-muted">Leave the date empty if it is unknown. Changing it will not move files already uploaded to Storage.</p><label className="block text-xs">Notes<textarea className={inputClass} value={notes} onChange={e => setNotes(e.target.value)} /></label><button className={buttonClass} disabled={busy}>Save session</button></form>;
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

function LifecycleSelect({value,onChange}:{value:string;onChange:(value:string)=>void}) {
 return <label className="text-xs flex items-center gap-2"><span>Lifecycle</span><select className={inputClass} style={{width:"auto"}} aria-label="Lifecycle view" value={value} onChange={e=>onChange(e.target.value)}><option value="active">Active</option><option value="archived">Archive</option><option value="all">All</option></select></label>;
}
function AssetLabelsForm({ assets, busy, onSubmit }: { assets: Asset[]; busy: boolean; onSubmit: (input: Record<string, unknown>) => void }) {
 const single=assets.length===1; const first=assets[0];
 const [role,setRole]=useState(single ? (first?.role || "unspecified") : ""); const [outputType,setOutputType]=useState(first?.output_type || ""); const [replaceOutput,setReplaceOutput]=useState(single);
 const [tags,setTags]=useState((first?.tags||[]).join(", ")); const [replaceTags,setReplaceTags]=useState(single);
 const [favorite,setFavorite]=useState(single ? (first?.favorite ? "on" : "off") : ""); const [intent,setIntent]=useState(single ? (first?.patreon_intent || "unset") : "");
 return <form className="space-y-3" onSubmit={e=>{e.preventDefault();const input:Record<string,unknown>={asset_ids:assets.map(a=>a.id),expected_revisions:Object.fromEntries(assets.map(a=>[a.id,a.revision]))};if(replaceTags)input.tags=tags.split(",").map(x=>x.trim()).filter(Boolean);if(favorite)input.favorite=favorite==="on";if(intent)input.patreon_intent=intent;if(role)input.role=role;if(replaceOutput)input.output_type=outputType;onSubmit(input);}}>
   <div><h3 className="font-semibold">{single ? "Edit Catalog labels" : `Edit labels on ${assets.length} assets`}</h3><p className="text-xs text-text-muted">These labels live in Catalog. They do not alter Media or publish anything.</p></div>
   <label className="block text-xs">Purpose<select className={inputClass} value={role} onChange={e=>setRole(e.target.value)}><option value="">Leave unchanged</option><option value="unspecified">Unclassified</option><option value="main">Main</option><option value="derivative">Derivative</option><option value="intermediate">Intermediate (hidden by default)</option></select></label>
   <label className="block text-xs">Output category<input className={inputClass} value={outputType} onChange={e=>setOutputType(e.target.value)} placeholder="reel, screenshot, portrait, trailer…" /></label>
   <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={replaceOutput} onChange={e=>setReplaceOutput(e.target.checked)} />Set or clear output category</label>
   <label className="block text-xs">Tags, comma separated<input className={inputClass} value={tags} onChange={e=>setTags(e.target.value)} placeholder="share-next, teaser, best-take" /></label>
   <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={replaceTags} onChange={e=>setReplaceTags(e.target.checked)} />Replace the current tag set{!single && <span className="text-text-muted">(leave off to keep each asset’s tags)</span>}</label>
   <label className="block text-xs">Favorite<select className={inputClass} value={favorite} onChange={e=>setFavorite(e.target.value)}><option value="">{single ? "Leave unchanged" : "Leave each as-is"}</option><option value="on">⭐ Favorite</option><option value="off">Not favorite</option></select></label>
   <label className="block text-xs">Patreon intent<select className={inputClass} value={intent} onChange={e=>setIntent(e.target.value)}><option value="">{single ? "Leave unchanged" : "Leave each as-is"}</option><option value="unset">Unset</option><option value="free">Free</option><option value="paid">Paid</option></select></label>
   <button className={buttonClass} disabled={busy || (!replaceTags && !favorite && !intent && !role && !replaceOutput)}>{busy ? "Saving…" : "Save labels"}</button>
 </form>;
}
function LifecycleForm({target,sessions,busy,onSubmit}:{target:{entity:"asset"|"session";operation:"archive"|"move"|"restore";assets?:Asset[];session?:Session};sessions:Session[];busy:boolean;onSubmit:(input:Record<string,unknown>)=>void}) {
 const [reason,setReason]=useState("");const [destination,setDestination]=useState("");const [status,setStatus]=useState("active");
 const [operationId,setOperationId]=useState(()=>crypto.randomUUID());
 const change=()=>setOperationId(crypto.randomUUID());
 const files=target.assets || [];
 const defaultDestination=target.operation === "restore" ? files[0]?.original_session_id || files[0]?.session_id : "";
 const destinations=sessions.filter(session=>target.operation!=="restore" || session.lifecycle!=="archived");
 const mixedOrigins=target.operation === "restore" && new Set(files.map(asset=>asset.original_session_id||asset.session_id)).size>1;
 return <form className="space-y-3" onSubmit={e=>{e.preventDefault();const selected=sessions.find(session=>session.id===(destination||defaultDestination));
 const input:Record<string,unknown>={operation_id:operationId,reason};
 if(target.entity==="session") Object.assign(input,{session_id:target.session?.id,expected_revision:target.session?.revision,status});
 else Object.assign(input,{asset_ids:files.map(asset=>asset.id),expected_revisions:Object.fromEntries(files.map(asset=>[asset.id,asset.revision])),...(destination?{destination_session_id:destination}:{}),...(selected?{destination_revision:selected.revision}:{}),destination_revisions:Object.fromEntries(sessions.map(session=>[session.id,session.revision]))});
 onSubmit(input);}}>
 <h3 className="font-semibold capitalize">{target.operation} {target.entity === "asset" ? `${files.length} asset(s)` : target.session?.title}</h3>
 <p className="text-xs text-text-muted">Updates Catalog membership and lifecycle. Asset IDs, Storage files, source links, hosting and publication history are preserved.</p>
 {target.entity === "asset" && <ul className="max-h-32 overflow-auto text-xs">{files.map(asset=><li key={asset.id}>{asset.name}</li>)}</ul>}
 {target.operation === "archive" && <label className="block text-xs">Archive reason<textarea className={inputClass} required value={reason} onChange={e=>{setReason(e.target.value);change();}} /></label>}
 {target.entity === "asset" && <label className="block text-xs">{target.operation === "archive" ? "Move to session (optional)" : "Destination session"}<select className={inputClass} aria-label="Destination session" required={target.operation==="move"} value={destination} onChange={e=>{setDestination(e.target.value);change();}}><option value="">{target.operation === "restore" ? mixedOrigins ? "Each asset’s original session" : "Original session" : target.operation === "archive" ? "Keep current session" : "Choose session"}</option>{destinations.map(session=><option key={session.id} value={session.id}>{session.title}{session.lifecycle==="archived" ? " · Archived" : ""}</option>)}</select></label>}
 {target.entity === "session" && target.operation === "restore" && target.session?.status === "archived" && <label className="block text-xs">Production status<select className={inputClass} value={status} onChange={e=>{setStatus(e.target.value);change();}}>{["planned","active","completed"].map(value=><option key={value}>{value}</option>)}</select></label>}
 {target.operation === "restore" && <p className="text-xs text-text-muted">The destination session must be active. Restore an archived parent session first, or choose another destination.</p>}
 <button type="submit" className={buttonClass} disabled={busy || !files.length && target.entity === "asset"}>{busy ? "Saving…" : `${target.operation === "archive" ? "Archive" : target.operation === "restore" ? "Restore" : "Move"} ${target.entity === "asset" ? "assets" : "session"}`}</button>
 </form>;
}

function LifecycleHistory({entity,id,get}:{entity:string;id:string;get:<T>(path:string,extra?:Record<string,string>)=>Promise<T>}) {
 const [events,setEvents]=useState<Array<{action:string;reason:string;created_at:string}>>([]);const [error,setError]=useState("");
 useEffect(()=>{let active=true;setEvents([]);setError("");get<{events:typeof events}>("/lifecycle-history",{entity_type:entity,id}).then(result=>{if(active)setEvents(result.events||[]);}).catch(err=>{if(active)setError(errorText(err));});return()=>{active=false;};},[get,entity,id]);
 return <details className="rounded border border-border p-2 text-xs"><summary>Lifecycle history ({events.length})</summary>{error&&<p className="text-yellow-400">{error}</p>}{events.map((event,index)=><p key={index} className="mt-2">{event.action} · {event.created_at} · {event.reason}</p>)}{!events.length&&!error&&<p className="mt-2 text-text-muted">No archive or move history recorded.</p>}</details>;
}
