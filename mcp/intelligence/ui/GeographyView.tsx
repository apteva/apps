import { useEffect, useMemo, useRef, useState } from "react";
import { geoNaturalEarth1, geoPath } from "d3-geo";
import { feature } from "topojson-client";
import world from "world-atlas/countries-110m.json";

// Bundled Natural Earth geometry: no tile service, API key or third-party
// runtime requests. Rendering is independent of the evidence contract.
const countries = feature(world as any, world.objects.countries as any) as any;
const projection = geoNaturalEarth1().fitExtent([[12, 12], [948, 468]], { type: "Sphere" });
const path = geoPath(projection);
type Location = { lat: number; lon: number; label?: string; country?: string; relationship: string; precision: string; confidence?: number; provenance: string };
type Row = { id: number; title: string; body: string; source: string; source_ref?: string; kind: string; event_time?: string; observed_at?: string; locations?: Location[]; payload?: unknown };
type API = <T>(path: string, params?: Record<string, string>) => Promise<T>;
const control = "bg-bg-input text-text border border-border rounded px-2 py-1.5 text-sm";
const dateOf = (r: Row) => r.event_time ? Date.parse(r.event_time) : NaN;
const safeURL = (s?: string) => { try { const u = new URL(s || ""); return ["http:", "https:"].includes(u.protocol) ? u.href : null; } catch { return null; } };

export default function GeographyView({ api }: { api: API }) {
 const [rows, setRows] = useState<Row[]>([]), [busy, setBusy] = useState(false), [error, setError] = useState("");
 const [query, setQuery] = useState(""), [source, setSource] = useState(""), [kind, setKind] = useState("");
 const [from, setFrom] = useState(""), [to, setTo] = useState(""), [asOf, setAsOf] = useState("");
 const [relationship, setRelationship] = useState(""), [selected, setSelected] = useState<number | null>(null);
 const [step, setStep] = useState(1000), [playing, setPlaying] = useState(false), [limited, setLimited] = useState(false);
 const [unmappedOnly, setUnmappedOnly] = useState(false), [page, setPage] = useState(0);
 const generation = useRef(0);
 async function load() {
  const id = ++generation.current; setBusy(true); setError(""); setPlaying(false);
  try {
   const params: Record<string,string> = { query, source, kind, limit: "200", as_of: asOf ? `${asOf}T23:59:59.999Z` : new Date().toISOString() };
   if (from) params.event_from = from;
   if (to) params.event_to = to;
   if (from && to && from >= to) throw new Error("Event before must be later than Event from.");
   const loaded: Row[] = []; let last = 0;
   for (let offset = 0; offset < 10000; offset += 200) {
    const result = await api<{results: Row[]}>("/search", { ...params, offset: String(offset) });
    if (id !== generation.current) return;
    last = result.results.length; loaded.push(...result.results);
    if (last < 200) break;
   }
   setRows(loaded); setLimited(loaded.length === 10000 && last === 200); setStep(1000); setSelected(null); setPage(0);
  } catch (e) { if (id === generation.current) setError((e as Error).message); }
  finally { if (id === generation.current) setBusy(false); }
 }
 useEffect(() => { load(); return () => { generation.current++; }; }, [api]);
 useEffect(() => { if (!playing) return; const timer = setInterval(() => setStep(s => Math.min(1000, s + 10)), 100); return () => clearInterval(timer); }, [playing]);
 useEffect(() => { if (step === 1000) setPlaying(false); }, [step]);
 const dates = rows.map(dateOf).filter(Number.isFinite);
 const min = dates.length ? Math.min(...dates) : 0, max = dates.length ? Math.max(...dates) : 0;
 const cutoff = min + (max - min) * step / 1000;
 const temporal = rows.filter(r => step === 1000 || (Number.isFinite(dateOf(r)) && dateOf(r) <= cutoff));
 const locs = (r: Row) => (r.locations || []).filter(l => !relationship || l.relationship === relationship);
 const mapped = temporal.filter(r => locs(r).length > 0);
 const visible = temporal.filter(r => unmappedOnly ? !(r.locations || []).length : !relationship || locs(r).length > 0);
 const groups = useMemo(() => {
  const result = new Map<string, {x:number; y:number; rows:Row[]; labels:Set<string>}>();
  for (const r of mapped) for (const loc of locs(r)) {
   const point = projection([loc.lon,loc.lat]); if (!point) continue;
   // Screen-space clusters keep densely co-located filings selectable.
   const key = `${Math.round(point[0]/14)},${Math.round(point[1]/14)}`;
   const group = result.get(key) || {x:point[0],y:point[1],rows:[],labels:new Set<string>()};
   if (!group.rows.some(v => v.id === r.id)) group.rows.push(r);
   group.labels.add(loc.label || loc.country || `${loc.lat}, ${loc.lon}`); result.set(key,group);
  }
  return [...result.values()];
 }, [rows, step, relationship]);
 const active = visible.find(r => r.id === selected);
 const select = (r: Row) => { setSelected(r.id); const i = visible.findIndex(v => v.id === r.id); if(i>=0)setPage(Math.floor(i/50)); };
 const relations = [...new Set(rows.flatMap(r => (r.locations || []).map(l => l.relationship)))].sort();
 const sourceURL = safeURL(active?.source_ref);
 return <section className="space-y-3">
  <div><h2 className="font-semibold">Apteva · Intelligence atlas</h2><p className="text-text-muted text-xs">Explore geographic relationships with source evidence. Locations are attributed, never inferred from a provider’s country.</p></div>
  <form onSubmit={e => {e.preventDefault(); load();}} className="flex flex-wrap items-end gap-2">
   <label className="text-xs text-text-muted">Search<input className={`${control} block`} value={query} onChange={e=>setQuery(e.target.value)} placeholder="Topic or entity" /></label>
   <label className="text-xs text-text-muted">Source<input className={`${control} block`} value={source} onChange={e=>setSource(e.target.value)} placeholder="Any source" list="atlas-sources" /></label>
   <datalist id="atlas-sources">{[...new Set(rows.map(r=>r.source))].sort().map(s=><option key={s} value={s}/>)}</datalist>
   <label className="text-xs text-text-muted">Kind<input className={`${control} block`} value={kind} onChange={e=>setKind(e.target.value)} placeholder="Any kind" /></label>
   <label className="text-xs text-text-muted">Event from<input type="date" className={`${control} block`} value={from} onChange={e=>setFrom(e.target.value)}/></label>
   <label className="text-xs text-text-muted">Event before<input type="date" className={`${control} block`} value={to} onChange={e=>setTo(e.target.value)}/></label>
   <label className="text-xs text-text-muted">Known as of<input type="date" className={`${control} block`} value={asOf} onChange={e=>setAsOf(e.target.value)}/></label>
   <button disabled={busy} className="bg-accent text-bg rounded px-3 py-2 disabled:opacity-50">{busy ? "Loading…" : "Apply filters"}</button>
  </form>
  {error && <p role="alert" className="text-error">{error}</p>}
  {limited && <p className="text-amber">Showing the first 10,000 matches. Narrow the date range or source to explore the remaining evidence.</p>}
  <div className="flex flex-wrap gap-3 items-center text-xs text-text-muted">
   <span aria-live="polite">{mapped.length.toLocaleString()} mapped · {temporal.filter(r=>!r.locations?.length).length.toLocaleString()} without coordinates · {rows.length.toLocaleString()} loaded</span>
   <label>Relationship <select className={control} value={relationship} onChange={e=>{setRelationship(e.target.value); setPage(0); setSelected(null);}}><option value="">All</option>{relations.map(s=><option key={s}>{s}</option>)}</select></label>
   <label><input type="checkbox" checked={unmappedOnly} onChange={e=>{setUnmappedOnly(e.target.checked);setPage(0);setSelected(null);}}/> Show unmapped evidence only</label>
  </div>
  <div className="border border-border rounded bg-bg-card overflow-hidden">
   <svg viewBox="0 0 960 480" style={{width:"100%", display:"block", maxHeight:520}} aria-label="World map of attributed evidence locations" role="img">
    <path d={path({type:"Sphere"}) || ""} className="fill-bg stroke-border"/>
    {countries.features.map((c:any)=><path key={c.id} d={path(c)||""} className="fill-bg-input stroke-border" strokeWidth="0.5"><title>{c.properties.name}</title></path>)}
    {!unmappedOnly && groups.map((g,i)=><g key={i} role="button" tabIndex={0} aria-label={`${[...g.labels].join(', ')}: ${g.rows.length} evidence records. Activate to cycle records.`} onClick={()=>select(g.rows[(g.rows.findIndex(r=>r.id===selected)+1)%g.rows.length])} onKeyDown={e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();select(g.rows[(g.rows.findIndex(r=>r.id===selected)+1)%g.rows.length]);}}} style={{cursor:"pointer"}}>
     <circle cx={g.x} cy={g.y} r={Math.min(17,5+Math.sqrt(g.rows.length))} className="fill-accent" fillOpacity={g.rows.some(r=>r.id===selected)?1:0.65} stroke="currentColor" strokeWidth={g.rows.some(r=>r.id===selected)?2:0}/>
     <circle cx={g.x} cy={g.y} r="18" fill="transparent"/>
     <title>{[...g.labels].join(', ')} · {g.rows.length} records</title>
    </g>)}
   </svg>
   {!mapped.length && <p className="px-4 pb-4 text-sm text-text-muted">No attributed coordinates in these results. Evidence remains available below; no locations have been invented.</p>}
   <p className="px-3 pb-2 text-xs text-text-dim">Boundaries: Natural Earth / world-atlas (public domain). Dots cluster nearby records; activate a cluster repeatedly to cycle its evidence.</p>
  </div>
  <div className="flex flex-wrap gap-2 items-center border border-border rounded p-3 bg-bg-card">
   <button className={control} disabled={min===max} onClick={()=>{if(step===1000)setStep(0);setPlaying(!playing);}}>{playing?"Pause":"Play"}</button>
   <label className="flex-1 text-xs text-text-muted">Event timeline · {dates.length ? new Date(cutoff).toISOString().slice(0,10) : "No dated events"}<input aria-label="Event timeline cutoff" type="range" min="0" max="1000" value={step} disabled={min===max} onChange={e=>{setStep(Number(e.target.value));setPlaying(false);setPage(0);}} className="block w-full"/></label>
   <button className={control} onClick={()=>{setStep(1000);setPlaying(false);setPage(0);}}>Show all dates</button>
   <p className="w-full text-xs text-text-dim">Playback filters event time, not knowledge time. Use “Known as of” for point-in-time research. Undated records appear only at “Show all dates”.</p>
  </div>
  <div className="flex flex-wrap gap-3 items-start">
   <div className="flex-1 min-w-0" style={{flexBasis:360}}>
    <h3 className="font-semibold mb-2">Evidence · {visible.length}</h3>
    <div className="space-y-1">{visible.slice(page*50,(page+1)*50).map(r=><button key={r.id} onClick={()=>select(r)} className={`block w-full text-left border rounded p-2 ${r.id===selected?'border-accent bg-accent/10':'border-border bg-bg-card'}`}><span className="text-xs text-text-muted">{r.event_time?.slice(0,10)||"Undated"} · {r.source} · {r.kind}</span><span className="block">{r.title||"Untitled evidence"}</span><span className="text-xs text-text-dim">{r.locations?.length ? r.locations.map(l=>`${l.relationship}: ${l.label||l.country||'coordinates'}`).join(' · ') : 'No attributed location'}</span></button>)}</div>
    <div className="flex gap-2 mt-2"><button className={control} disabled={page===0} onClick={()=>setPage(p=>p-1)}>Previous</button><span>{page+1} / {Math.max(1,Math.ceil(visible.length/50))}</span><button className={control} disabled={(page+1)*50>=visible.length} onClick={()=>setPage(p=>p+1)}>Next</button></div>
   </div>
   <aside className="border border-border rounded bg-bg-card p-3 flex-1 min-w-0" style={{flexBasis:320,overflowWrap:"anywhere"}}>
    <h3 className="font-semibold">Evidence & provenance</h3>
    {active ? <><h4 className="font-semibold mt-3">{active.title}</h4><p className="text-xs text-text-muted">{active.source} · {active.kind} · #{active.id}</p><p className="text-xs mt-2">Event: {active.event_time || 'unknown'}<br/>Observed: {active.observed_at || 'unknown'}</p><p className="whitespace-pre-wrap my-3">{active.body}</p>{sourceURL ? <a className="text-accent underline" href={sourceURL} target="_blank" rel="noopener noreferrer">Original source ↗</a> : <p className="text-xs">Source reference: {active.source_ref || 'not provided'}</p>}{(active.locations||[]).map((l,i)=><div key={i} className="border-t border-border mt-3 pt-2 text-xs"><p>{l.relationship} · {l.label||l.country} · {l.lat}, {l.lon}</p><p>Precision: {l.precision} · Confidence: {l.confidence == null ? 'unspecified' : `${Math.round(l.confidence*100)}%`}</p><p>Location provenance: {l.provenance}</p></div>)}<details className="mt-3"><summary>Raw payload</summary><pre className="text-xs whitespace-pre-wrap">{JSON.stringify(active.payload,null,2)}</pre></details></> : <p className="text-text-muted mt-2">Select a map cluster or evidence record to inspect its source and geographic relationship.</p>}
   </aside>
  </div>
 </section>;
}
