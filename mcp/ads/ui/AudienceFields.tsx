import { useState } from "react";

export type AudienceDraft = { keywords: string; urls: string; apps: string; mode: string; seeds: number[]; segments: number[]; interests: string[]; countries: string; expansion: string };
export const freshAudienceDraft = (): AudienceDraft => ({ keywords: "", urls: "", apps: "", mode: "interests", seeds: [], segments: [], interests: [], countries: "US", expansion: "balanced" });
export function audienceDraftFromMetadata(metadata: Record<string, any>, audiences: {id:number; native_id:string; type:string}[]): AudienceDraft {
  const d = freshAudienceDraft();
  d.mode = metadata.native_type === "SEARCH" ? "search_terms" : "interests";
  for (const m of metadata.members || []) { if (m.keyword) d.keywords += m.keyword + "\n"; if (m.url) d.urls += m.url + "\n"; if (m.app) d.apps += m.app + "\n"; }
  for (const dim of metadata.dimensions || []) for (const segment of (dim.audienceSegments || dim.audience_segments)?.segments || []) {
    const interest = (segment.userInterest || segment.user_interest)?.userInterestCategory || segment.user_interest?.user_interest_category;
    if (interest) d.interests.push(String(interest).split("/").pop()!);
    const user = (segment.userList || segment.user_list)?.userList || segment.user_list?.user_list;
    const custom = (segment.customAudience || segment.custom_audience)?.customAudience || segment.custom_audience?.custom_audience;
    const row = audiences.find(a => a.native_id === String(user || custom).split("/").pop() && (custom ? a.type === "custom_segment" : a.type !== "custom_segment" && a.type !== "grouped"));
    if (row) d.segments.push(row.id);
  }
  return d;
}
const lines = (text: string) => text.split("\n").map(v => v.trim()).filter(Boolean);
export function audienceDefinition(kind: string, draft: AudienceDraft): Record<string, unknown> {
  if (kind === "custom_segment") return { keywords: lines(draft.keywords), urls: lines(draft.urls), apps: lines(draft.apps), segment_mode: draft.mode };
  if (kind === "lookalike") return { source_audience_ids: draft.seeds, country_codes: draft.countries.split(/[,\s]+/).filter(Boolean).map(v => v.toUpperCase()), expansion_level: draft.expansion };
  if (kind === "grouped") return { segment_audience_ids: draft.segments, user_interest_ids: draft.interests };
  return {};
}
export function audienceDefinitionValid(kind: string, draft: AudienceDraft): boolean {
  if (kind === "custom_segment") return [draft.keywords, draft.urls, draft.apps].some(v => lines(v).length > 0);
  if (kind === "lookalike") return draft.seeds.length > 0 && /^[a-z]{2}(?:[,\s]+[a-z]{2})*$/i.test(draft.countries.trim());
  if (kind === "grouped") return draft.segments.length + draft.interests.length > 0;
  return true;
}
const field = "min-w-0 w-full rounded border border-border bg-bg-input px-3 py-2 text-sm text-text";
type Audience = { id: number; name: string; type: string; status: string };
export default function AudienceFields({ kind, draft, onChange, audiences, adAccountId, callTool }: { kind: string; draft: AudienceDraft; onChange: (draft: AudienceDraft) => void; audiences: Audience[]; adAccountId: number; callTool: (name: string, args: Record<string, unknown>) => Promise<any> }) {
  const [query, setQuery] = useState(""), [results, setResults] = useState<any[]>([]), [error, setError] = useState(""), [busy, setBusy] = useState(false);
  const change = <K extends keyof AudienceDraft>(key: K, value: AudienceDraft[K]) => onChange({ ...draft, [key]: value });
  const toggle = (key: "seeds" | "segments", id: number, checked: boolean) => change(key, checked ? [...draft[key], id] : draft[key].filter(v => v !== id));
  if (kind === "custom_segment") return <div className="grid min-w-0 gap-3 md:col-span-2">
    <label className="grid gap-1 text-xs">Keyword interpretation<select className={field} value={draft.mode} onChange={e => change("mode", e.target.value)}><option value="interests">Interests or purchase intentions</option><option value="search_terms">People who searched these terms on Google</option></select></label>
    {([['keywords', 'Keywords or phrases'], ['urls', 'Website URLs'], ['apps', 'Android app package names']] as const).map(([key, label]) => <label key={key} className="grid gap-1 text-xs">{label}<textarea className={field} rows={3} placeholder="One per line" value={draft[key]} onChange={e => change(key, e.target.value)} /></label>)}
    <p className="text-xs text-text-muted">Members match any supplied signal. URLs describe interests; they do not restrict ads to those websites. Search behavior is used on supported Google properties.</p>
  </div>;
  if (kind === "lookalike") return <div className="grid min-w-0 gap-3 md:col-span-2"><p className="text-xs text-text-muted">Google requires at least 100 active matched people across eligible seeds. Eligibility is checked by Google; a new segment can take 2–3 days to become usable.</p>
    <fieldset className="grid max-h-44 gap-2 overflow-auto rounded border border-border p-3"><legend className="text-xs">First-party seed audiences</legend>{audiences.filter(a => a.status === "active" && ["customer_list", "website", "app_activity", "remarketing"].includes(a.type)).map(a => <label key={a.id} className="flex items-center gap-2 text-xs"><input type="checkbox" checked={draft.seeds.includes(a.id)} onChange={e => toggle("seeds", a.id, e.target.checked)} />{a.name}</label>)}</fieldset>
    <label className="grid gap-1 text-xs">Country codes (comma separated)<input required className={field} value={draft.countries} onChange={e => change("countries", e.target.value)} /></label>
    <label className="grid gap-1 text-xs">Expansion<select className={field} value={draft.expansion} onChange={e => change("expansion", e.target.value)}>{["narrow", "balanced", "broad"].map(v => <option key={v} value={v}>{v}</option>)}</select></label>
    <p className="text-xs text-text-muted">YouTube viewer/subscriber seeds must first be created in Google Ads Audience Manager, then refreshed here. Google does not expose their exact channel/action details.</p>
  </div>;
  if (kind !== "grouped") return null;
  return <div className="grid min-w-0 gap-3 md:col-span-2"><fieldset className="grid max-h-44 gap-2 overflow-auto rounded border border-border p-3"><legend className="text-xs">Audience segments</legend>{audiences.filter(a => a.status === "active" && a.type !== "grouped").map(a => <label key={a.id} className="flex items-center gap-2 text-xs"><input type="checkbox" checked={draft.segments.includes(a.id)} onChange={e => toggle("segments", a.id, e.target.checked)} />{a.name}</label>)}</fieldset>
    <label className="grid gap-1 text-xs">Find Google interest segments<input className={field} value={query} onChange={e => setQuery(e.target.value)} /></label>
    <button type="button" disabled={busy} className="rounded border border-border px-3 py-2 text-xs" onClick={async () => { setError(""); setBusy(true); try { const result = await callTool("targeting_catalog_search", { ad_account_id: adAccountId, type: "interest", query, limit: 50 }); setResults(result.data || []); } catch(e) { setError((e as Error).message); } finally { setBusy(false); } }}>Search interests</button>
    {error && <p role="alert" className="text-xs text-red">{error}</p>}
    <div className="grid max-h-44 gap-2 overflow-auto">{results.map(r => <label key={r.id} className="flex items-center gap-2 text-xs"><input type="checkbox" checked={draft.interests.includes(r.id)} onChange={e => change("interests", e.target.checked ? [...draft.interests, r.id] : draft.interests.filter(v => v !== r.id))} />{r.name}</label>)}</div>
    {draft.interests.length > 0 && <p className="text-xs text-text-muted">Selected interest IDs: {draft.interests.join(", ")}</p>}
    <p className="text-xs text-text-muted">Use this grouped audience in a Demand Gen ad group. Optimized targeting remains a separate ad-group control.</p>
  </div>;
}
