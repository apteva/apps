// World map: everything the app knows, on one map.
//
// Countries are shaded by how much located data they hold. Located
// entities are points (filled = observed, hollow = inferred, dashed =
// synthetic), relationships between located entities are arcs styled by
// status, and evidence that carries its own attributed coordinates shows as
// small squares. Rings mark quality issues (red conflict, amber stale,
// dashed missing). Entities with no place are listed under the map with the
// reason, and the side panel shows provenance for whatever is selected.
//
// SVG colours come from dashboard CSS variables via attributes (panel
// sources are not scanned by the dashboard's Tailwind JIT).

import { useEffect, useMemo, useRef, useState } from "react";
import { geoNaturalEarth1, geoPath } from "d3-geo";
import { feature } from "topojson-client";
import world from "world-atlas/countries-110m.json";

type Api = <T>(path: string, params?: Record<string, string>) => Promise<T>;
type Post = <T>(path: string, body: unknown) => Promise<T>;

const W = 960, H = 500;
const countries = feature(world as any, (world as any).objects.countries) as any;
const projection = geoNaturalEarth1().fitExtent([[8, 8], [W - 8, H - 8]], { type: "Sphere" } as any);
const path = geoPath(projection);
const countryName: Record<string, string> = Object.fromEntries(countries.features.map((f: any) => [String(f.id), f.properties.name]));

interface Loc { lat: number; lon: number; label?: string; country_id?: string; precision: string; provenance: string; source: string; evidence_id?: number; basis: string }
interface Latest { variable: string; value: number; unit: string; valid_at: string; source: string; status: string; cadence_days: number; age_days: number; basis: string }
interface Ent {
  id: number; name: string; kind: string; basis: string; description: string; flags: string[]; country_id?: string; reason?: string;
  counts: { relationships: number; claims: number; state: number; evidence: number; questions: number };
  latest?: Latest[] | null; location?: Loc; locations?: Loc[];
}
interface Rel { id: number; from: number; to: number; type: string; status: string; basis: string; confidence?: number; evidence_ids: number[]; note: string }
interface Pt { evidence_id: number; title: string; source: string; source_ref: string; kind: string; basis: string; access: string; event_time?: string; observed_at?: string;
  lat: number; lon: number; label: string; relationship: string; precision: string; confidence?: number; provenance: string; country_id: string }
interface Issue { type: string; severity: string; subject: string; code: string; title: string; detail: string; entity_ids?: number[]; refs?: Record<string, unknown>; since?: string }
interface MapData {
  generated_at: string; entities: Ent[]; relationships: Rel[]; evidence_points: Pt[]; unmapped: Ent[];
  countries: Record<string, Record<string, number>>;
  quality: { summary: Record<string, number>; issues: Issue[]; by_entity: Record<string, string[]>; sources: { slug: string; status: string; access: string; last_error: string }[] };
  basis: Record<string, Record<string, number>>; access: Record<string, number>; hidden_evidence: number;
}
type Sel = { kind: "entity"; id: number } | { kind: "point"; index: number } | { kind: "country"; id: string } | null;

const C = {
  observed: "var(--color-green)", inferred: "var(--accent)", synthetic: "var(--color-purple, #9b87f5)",
  conflict: "var(--color-error)", stale: "var(--color-warn)", missing: "var(--text-muted)",
  land: "var(--bg-input)", border: "var(--border)", text: "var(--text)", dim: "var(--text-dim)", bg: "var(--bg)",
};
const statusStroke: Record<string, { color: string; dash: string }> = {
  observed: { color: C.observed, dash: "" }, reported: { color: C.inferred, dash: "" },
  inferred: { color: C.missing, dash: "5 3" }, assumed: { color: C.stale, dash: "2 3" },
};
const control = "bg-bg-input text-text border border-border rounded px-2 py-1 text-sm";
const chip = (on: boolean) => `text-xs px-2 py-1 rounded border ${on ? "border-accent text-text" : "border-border text-text-dim"}`;
const date = (s?: string) => (s ? s.slice(0, 10) : "—");
const num = (v: number) => (Math.abs(v) >= 1000 ? v.toLocaleString(undefined, { maximumFractionDigits: 0 }) : String(Math.round(v * 100) / 100));
const safeURL = (s?: string) => { try { const u = new URL(s || ""); return ["http:", "https:"].includes(u.protocol) ? u.href : null; } catch { return null; } };

function BasisMark({ basis }: { basis: string }) {
  const color = basis === "observed" ? C.observed : basis === "inferred" ? C.inferred : C.synthetic;
  return <span className="text-xs px-1.5 py-0.5 rounded border" style={{ color, borderColor: color }}>{basis}</span>;
}
function FlagMark({ flag }: { flag: string }) {
  const color = flag === "conflict" ? C.conflict : flag === "stale" ? C.stale : C.missing;
  return <span className="text-xs px-1.5 py-0.5 rounded border" style={{ color, borderColor: color }}>{flag}</span>;
}

export default function WorldMap({ api, post }: { api: Api; post: Post }) {
  const [data, setData] = useState<MapData | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [bases, setBases] = useState<Record<string, boolean>>({ observed: true, inferred: true, synthetic: true });
  const [layers, setLayers] = useState({ countries: true, links: true, entities: true, evidence: true });
  const [flagFilter, setFlagFilter] = useState(""), [kind, setKind] = useState(""), [asOf, setAsOf] = useState("");
  const [sel, setSel] = useState<Sel>(null);
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const drag = useRef<{ x: number; y: number; vx: number; vy: number } | null>(null);
  const svgRef = useRef<SVGSVGElement | null>(null);
  const gen = useRef(0);

  async function load() {
    const id = ++gen.current; setBusy(true); setError("");
    try {
      const params: Record<string, string> = {};
      if (asOf) params.as_of = `${asOf}T23:59:59Z`;
      const d = await api<MapData>("/world-map", params);
      if (id === gen.current) setData(d);
    } catch (e) { if (id === gen.current) setError((e as Error).message); }
    finally { if (id === gen.current) setBusy(false); }
  }
  useEffect(() => { load(); return () => { gen.current++; }; }, [api, asOf]); // eslint-disable-line react-hooks/exhaustive-deps

  async function synthetic(action: "seed" | "clear") {
    setBusy(true); setError("");
    try { await post(action === "seed" ? "/synthetic-seed" : "/synthetic-clear", {}); setSel(null); await load(); }
    catch (e) { setError((e as Error).message); setBusy(false); }
  }

  const proj = (lon: number, lat: number) => projection([lon, lat]) || [0, 0];
  const visibleEnt = (e: Ent) => bases[e.basis] && (!kind || e.kind === kind) && (!flagFilter || e.flags.includes(flagFilter));
  const ents = useMemo(() => (data?.entities || []).filter(visibleEnt), [data, bases, kind, flagFilter]); // eslint-disable-line react-hooks/exhaustive-deps
  const byId = useMemo(() => new Map((data?.entities || []).concat(data?.unmapped || []).map(e => [e.id, e])), [data]);
  const located = useMemo(() => new Map(ents.map(e => [e.id, e])), [ents]);
  const rels = (data?.relationships || []).filter(r => bases[r.basis]);
  const arcs = rels.filter(r => located.has(r.from) && located.has(r.to) && r.from !== r.to);
  const points = (data?.evidence_points || []).filter(p => bases[p.basis] && !flagFilter);
  const kinds = [...new Set((data?.entities || []).concat(data?.unmapped || []).map(e => e.kind))].sort();
  const maxCountry = Math.max(1, ...Object.values(data?.countries || {}).map(c => (c.entities || 0) + (c.evidence || 0)));
  const unmapped = (data?.unmapped || []).filter(visibleEnt);
  const issues = (data?.quality.issues || []).filter(i => !flagFilter || i.type === flagFilter);
  const syntheticRows = Object.values(data?.basis || {}).reduce((n, m) => n + (m.synthetic || 0), 0);
  const r = (e: Ent) => Math.min(11, 3.5 + Math.sqrt(e.counts.relationships + e.counts.evidence + e.counts.state / 4));

  // Pan and zoom by changing the transform of the data layer. With a
  // located selection, zooming centres on it.
  const zoom = (f: number) => setView(v => {
    const k = Math.min(12, Math.max(1, v.k * f));
    if (k === 1) return { k: 1, x: 0, y: 0 };
    const focus = sel?.kind === "entity" ? byId.get(sel.id)?.location : sel?.kind === "point" ? points[sel.index] : undefined;
    if (focus) {
      const [fx, fy] = proj(focus.lon, focus.lat);
      return { k, x: W / 2 - fx * k, y: H / 2 - fy * k };
    }
    const cx = W / 2, cy = H / 2;
    return { k, x: cx - ((cx - v.x) / v.k) * k, y: cy - ((cy - v.y) / v.k) * k };
  });
  const toSvg = (e: React.PointerEvent) => {
    const rect = svgRef.current!.getBoundingClientRect();
    return { x: ((e.clientX - rect.left) / rect.width) * W, y: ((e.clientY - rect.top) / rect.height) * H };
  };
  const s = 1 / view.k; // keep marks the same screen size while zoomed

  const selected = sel?.kind === "entity" ? byId.get(sel.id) : undefined;
  const selPoint = sel?.kind === "point" ? points[sel.index] : undefined;

  return <section className="space-y-3">
    <div className="flex flex-wrap items-end gap-2">
      <div className="mr-auto">
        <h2 className="font-semibold">World map</h2>
        <p className="text-xs text-text-muted">Every located entity, link and evidence point, with where each location came from. Nothing is placed from a publisher's country or a guess.</p>
      </div>
      <label className="text-xs text-text-muted">Known as of<input type="date" className={`${control} block`} value={asOf} onChange={e => setAsOf(e.target.value)} /></label>
      <label className="text-xs text-text-muted">Kind<select className={`${control} block`} value={kind} onChange={e => setKind(e.target.value)}><option value="">All kinds</option>{kinds.map(k => <option key={k}>{k}</option>)}</select></label>
      <button className={control} onClick={load} disabled={busy}>{busy ? "Loading…" : "Refresh"}</button>
    </div>

    <div className="flex flex-wrap gap-2 items-center">
      <span className="text-xs text-text-dim">Basis</span>
      {(["observed", "inferred", "synthetic"] as const).map(b => {
        const n = (data?.basis?.entities?.[b] || 0) + (data?.basis?.evidence?.[b] || 0);
        return <button key={b} className={chip(bases[b])} aria-pressed={bases[b]} onClick={() => setBases({ ...bases, [b]: !bases[b] })}>
          <svg width="10" height="10" className="inline mr-1" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill={b === "observed" ? C.observed : "none"} stroke={C[b]} strokeWidth="1.5" strokeDasharray={b === "synthetic" ? "2 1.5" : ""} /></svg>
          {b} <span className="text-text-dim">{n}</span></button>;
      })}
      <span className="text-xs text-text-dim ml-3">Show</span>
      {(Object.keys(layers) as (keyof typeof layers)[]).map(l => <button key={l} className={chip(layers[l])} aria-pressed={layers[l]} onClick={() => setLayers({ ...layers, [l]: !layers[l] })}>{l}</button>)}
      <span className="text-xs text-text-dim ml-3">Quality</span>
      {(["conflict", "stale", "missing"] as const).map(f => <button key={f} className={chip(flagFilter === f)} aria-pressed={flagFilter === f} onClick={() => setFlagFilter(flagFilter === f ? "" : f)}>
        {f} <span className="text-text-dim">{data?.quality.summary?.[f] ?? 0}</span></button>)}
    </div>

    {error && <p role="alert" className="text-error text-sm">{error}</p>}

    <div className="flex flex-wrap gap-3 items-start">
      <div className="border border-border rounded bg-bg-card overflow-hidden flex-1 min-w-0" style={{ flexBasis: 560 }}>
        <div className="relative">
          <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} style={{ width: "100%", display: "block", touchAction: "none", cursor: drag.current ? "grabbing" : "grab" }}
            role="img" aria-label="World map of located entities, links and evidence"
            onWheel={e => { if (e.ctrlKey || e.metaKey) { e.preventDefault(); zoom(e.deltaY < 0 ? 1.25 : 0.8); } }}
            onPointerDown={e => { const p = toSvg(e); drag.current = { x: p.x, y: p.y, vx: view.x, vy: view.y }; }}
            onPointerMove={e => { if (!drag.current || view.k === 1) return; const p = toSvg(e); setView(v => ({ ...v, x: drag.current!.vx + p.x - drag.current!.x, y: drag.current!.vy + p.y - drag.current!.y })); }}
            onPointerUp={() => { drag.current = null; }} onPointerLeave={() => { drag.current = null; }}>
            <defs>
              <marker id="wm-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill={C.dim} /></marker>
            </defs>
            <rect x="0" y="0" width={W} height={H} fill={C.bg} />
            <g transform={`translate(${view.x},${view.y}) scale(${view.k})`}>
              <path d={path({ type: "Sphere" } as any) || ""} fill={C.bg} stroke={C.border} strokeWidth={s} />
              {countries.features.map((c: any) => {
                const stats = data?.countries?.[String(c.id)];
                const amount = stats ? (stats.entities || 0) + (stats.evidence || 0) : 0;
                const on = layers.countries && amount > 0;
                const isSel = sel?.kind === "country" && sel.id === String(c.id);
                // Neutral shading, so it never reads as a basis colour.
                return <path key={c.id} d={path(c) || ""} fill={on ? C.text : C.land} fillOpacity={on ? 0.1 + 0.3 * Math.sqrt(amount / maxCountry) : 1}
                  stroke={isSel ? C.text : C.border} strokeWidth={(isSel ? 1.5 : 0.5) * s}
                  onClick={() => stats && setSel({ kind: "country", id: String(c.id) })} style={{ cursor: stats ? "pointer" : "default" }}>
                  <title>{c.properties.name}{stats ? ` · ${stats.entities || 0} entities · ${stats.evidence || 0} evidence` : ""}</title>
                </path>;
              })}
              {layers.links && arcs.map(rel => {
                const a = proj(located.get(rel.from)!.location!.lon, located.get(rel.from)!.location!.lat);
                const b = proj(located.get(rel.to)!.location!.lon, located.get(rel.to)!.location!.lat);
                const mx = (a[0] + b[0]) / 2, my = (a[1] + b[1]) / 2, dx = b[0] - a[0], dy = b[1] - a[1];
                const bend = 0.18; // curve so overlapping links stay apart
                const st = rel.basis === "synthetic" ? { color: C.synthetic, dash: statusStroke[rel.status]?.dash || "" } : statusStroke[rel.status] || statusStroke.inferred;
                const hot = selected && (rel.from === selected.id || rel.to === selected.id);
                return <path key={rel.id} d={`M${a[0]},${a[1]} Q${mx - dy * bend},${my + dx * bend} ${b[0]},${b[1]}`} fill="none" stroke={st.color}
                  strokeWidth={(hot ? 2.2 : 1.2) * s} strokeDasharray={st.dash ? st.dash.split(" ").map(n => Number(n) * s).join(" ") : undefined}
                  strokeOpacity={selected && !hot ? 0.25 : 0.85} markerEnd="url(#wm-arrow)">
                  <title>{byId.get(rel.from)?.name} {rel.type} {byId.get(rel.to)?.name} · {rel.status} · {rel.basis}</title>
                </path>;
              })}
              {layers.evidence && points.map((p, i) => {
                const [x, y] = proj(p.lon, p.lat); const isSel = sel?.kind === "point" && sel.index === i; const sz = 3.2 * s;
                return <rect key={`${p.evidence_id}-${i}`} x={x - sz} y={y - sz} width={sz * 2} height={sz * 2} rx={0.6 * s} transform={`rotate(45 ${x} ${y})`}
                  fill={p.basis === "synthetic" ? C.synthetic : C.stale} fillOpacity={0.85} stroke={isSel ? C.text : C.bg} strokeWidth={(isSel ? 1.5 : 0.6) * s}
                  onClick={e => { e.stopPropagation(); setSel({ kind: "point", index: i }); }} style={{ cursor: "pointer" }}>
                  <title>{p.title} · {p.source} · {p.relationship}</title>
                </rect>;
              })}
              {layers.entities && ents.map(e => {
                const [x, y] = proj(e.location!.lon, e.location!.lat); const rad = r(e) * s; const isSel = selected?.id === e.id;
                const color = C[e.basis as "observed" | "inferred" | "synthetic"] || C.missing;
                return <g key={e.id} onClick={ev => { ev.stopPropagation(); setSel({ kind: "entity", id: e.id }); }} style={{ cursor: "pointer" }}
                  role="button" tabIndex={0} aria-label={`${e.name}, ${e.kind}, ${e.basis}${e.flags.length ? ", " + e.flags.join(", ") : ""}`}
                  onKeyDown={ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSel({ kind: "entity", id: e.id }); } }}>
                  {e.flags.includes("conflict") && <circle cx={x} cy={y} r={rad + 3.5 * s} fill="none" stroke={C.conflict} strokeWidth={1.6 * s} />}
                  {e.flags.includes("stale") && <circle cx={x} cy={y} r={rad + (e.flags.includes("conflict") ? 6 : 3.5) * s} fill="none" stroke={C.stale} strokeWidth={1.4 * s} />}
                  {e.flags.includes("missing") && <circle cx={x} cy={y} r={rad + 2.5 * s} fill="none" stroke={C.missing} strokeWidth={1 * s} strokeDasharray={`${2 * s} ${2 * s}`} />}
                  <circle cx={x} cy={y} r={rad} fill={e.basis === "observed" ? color : C.bg} fillOpacity={e.basis === "observed" ? 0.85 : 1}
                    stroke={isSel ? C.text : color} strokeWidth={(isSel ? 2.4 : 1.6) * s} strokeDasharray={e.basis === "synthetic" ? `${2.5 * s} ${1.5 * s}` : undefined} />
                  {(view.k >= 2.5 || isSel) && <text x={x + rad + 3 * s} y={y + 3 * s} fontSize={10 * s} fill={C.text} stroke={C.bg} strokeWidth={2.5 * s} paintOrder="stroke">{e.name}</text>}
                  <title>{e.name} · {e.kind} · {e.basis} · {e.location!.precision} via {e.location!.source}</title>
                </g>;
              })}
            </g>
          </svg>
          <div className="absolute top-2 right-2 flex flex-col gap-1">
            <button className={control} aria-label="Zoom in" onClick={() => zoom(1.5)}>+</button>
            <button className={control} aria-label="Zoom out" onClick={() => zoom(1 / 1.5)}>−</button>
            <button className={control} aria-label="Reset view" onClick={() => setView({ x: 0, y: 0, k: 1 })}>⟲</button>
          </div>
        </div>
        <Legend />
        <p className="px-3 pb-2 text-xs text-text-dim">
          {ents.length} entities · {arcs.length} links · {points.length} evidence points · {unmapped.length} not on the map
          {data?.hidden_evidence ? ` · ${data.hidden_evidence} restricted records hidden` : ""}. Boundaries: Natural Earth (public domain). Ctrl/⌘ + scroll or the buttons to zoom; drag to pan.
        </p>
      </div>

      <aside className="border border-border rounded bg-bg-card p-3 flex-1 min-w-0 text-sm" style={{ flexBasis: 300, maxWidth: 440, overflowWrap: "anywhere" }}>
        {selected ? <EntityDetail e={selected} rels={rels} byId={byId} issues={(data?.quality.issues || []).filter(i => i.entity_ids?.includes(selected.id))} select={id => setSel({ kind: "entity", id })} />
          : selPoint ? <PointDetail p={selPoint} />
          : sel?.kind === "country" ? <CountryDetail id={sel.id} data={data!} ents={ents} rels={rels} byId={byId} select={id => setSel({ kind: "entity", id })} />
          : <Health data={data} syntheticRows={syntheticRows} busy={busy} synthetic={synthetic} />}
        {sel && <button className={`${control} mt-3`} onClick={() => setSel(null)}>Back to data health</button>}
      </aside>
    </div>

    <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit,minmax(320px,1fr))" }}>
      <section className="border border-border rounded bg-bg-card">
        <h3 className="px-3 py-2 border-b border-border font-semibold text-sm">Not on the map · {unmapped.length}</h3>
        {unmapped.length === 0 ? <p className="p-3 text-text-muted text-sm">Every entity in view has a location.</p> :
          <div style={{ maxHeight: 320, overflowY: "auto" }}>{unmapped.map(e => <button key={e.id} onClick={() => setSel({ kind: "entity", id: e.id })}
            className="block w-full text-left px-3 py-1.5 border-b border-border hover:bg-bg-hover text-sm">
            <span className="flex gap-2 items-center"><span className="flex-1">{e.name} <span className="text-text-dim text-xs">{e.kind}</span></span><BasisMark basis={e.basis} />{e.flags.map(f => <FlagMark key={f} flag={f} />)}</span>
            <span className="text-xs text-text-dim">{e.reason}</span></button>)}</div>}
      </section>
      <section className="border border-border rounded bg-bg-card">
        <h3 className="px-3 py-2 border-b border-border font-semibold text-sm">Quality issues · {issues.length}</h3>
        {issues.length === 0 ? <p className="p-3 text-text-muted text-sm">No {flagFilter || ""} issues.</p> :
          <div style={{ maxHeight: 320, overflowY: "auto" }}>{issues.map((i, n) => <IssueRow key={n} i={i} select={id => setSel({ kind: "entity", id })} />)}</div>}
      </section>
    </div>
  </section>;
}

function Legend() {
  const item = "flex items-center gap-1";
  return <div className="px-3 pt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-text-muted">
    <span className={item}><svg width="12" height="12"><circle cx="6" cy="6" r="4.5" fill={C.observed} stroke={C.observed} /></svg>observed</span>
    <span className={item}><svg width="12" height="12"><circle cx="6" cy="6" r="4.5" fill={C.bg} stroke={C.inferred} strokeWidth="1.5" /></svg>inferred</span>
    <span className={item}><svg width="12" height="12"><circle cx="6" cy="6" r="4.5" fill={C.bg} stroke={C.synthetic} strokeWidth="1.5" strokeDasharray="2.5 1.5" /></svg>synthetic</span>
    <span className={item}><svg width="12" height="12"><rect x="3" y="3" width="6" height="6" transform="rotate(45 6 6)" fill={C.stale} /></svg>evidence</span>
    <span className={item}><svg width="14" height="14"><circle cx="7" cy="7" r="5.5" fill="none" stroke={C.conflict} strokeWidth="1.6" /></svg>conflict</span>
    <span className={item}><svg width="14" height="14"><circle cx="7" cy="7" r="5.5" fill="none" stroke={C.stale} strokeWidth="1.4" /></svg>stale</span>
    <span className={item}><svg width="14" height="14"><circle cx="7" cy="7" r="5.5" fill="none" stroke={C.missing} strokeDasharray="2 2" /></svg>missing</span>
    {Object.entries(statusStroke).map(([k, v]) => <span key={k} className={item}><svg width="22" height="8"><line x1="1" y1="4" x2="21" y2="4" stroke={v.color} strokeWidth="1.6" strokeDasharray={v.dash} /></svg>{k} link</span>)}
  </div>;
}

function IssueRow({ i, select }: { i: Issue; select: (id: number) => void }) {
  const color = i.type === "conflict" ? C.conflict : i.type === "stale" ? C.stale : C.missing;
  return <div className="px-3 py-1.5 border-b border-border text-sm">
    <div className="flex gap-2 items-start"><span className="text-xs px-1.5 rounded border shrink-0" style={{ color, borderColor: color }}>{i.type}</span>
      <span className="flex-1">{i.title}</span>{i.entity_ids?.[0] != null && <button className="text-xs text-accent" onClick={() => select(i.entity_ids![0])}>show</button>}</div>
    <div className="text-xs text-text-dim mt-0.5">{i.detail}</div>
  </div>;
}

function EntityDetail({ e, rels, byId, issues, select }: { e: Ent; rels: Rel[]; byId: Map<number, Ent>; issues: Issue[]; select: (id: number) => void }) {
  const mine = rels.filter(r => r.from === e.id || r.to === e.id);
  return <div className="space-y-3">
    <div><h3 className="font-semibold">{e.name}</h3>
      <div className="flex flex-wrap gap-1 mt-1 items-center"><span className="text-xs text-text-dim">{e.kind}</span><BasisMark basis={e.basis} />{e.flags.map(f => <FlagMark key={f} flag={f} />)}</div>
      {e.description && <p className="text-xs text-text-muted mt-1">{e.description}</p>}</div>
    <div>
      <h4 className="text-xs uppercase tracking-wide text-text-dim">Location</h4>
      {e.locations?.length ? e.locations.map((l, i) => <div key={i} className="text-xs mt-1 border-l-2 pl-2" style={{ borderColor: l === e.location || (l.source === e.location?.source) ? C.inferred : C.border }}>
        <div>{l.label || `${l.lat.toFixed(2)}, ${l.lon.toFixed(2)}`} · {l.precision} · <BasisMark basis={l.basis} /></div>
        <div className="text-text-dim">{l.source}: {l.provenance}{l.evidence_id ? ` (evidence #${l.evidence_id})` : ""}</div></div>)
        : <p className="text-xs text-text-muted mt-1">No location: {e.reason}</p>}
    </div>
    {e.latest && e.latest.length > 0 && <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Latest values</h4>
      {e.latest.map((v, i) => <div key={i} className="flex gap-2 text-xs mt-1 items-center"><span className="flex-1">{v.variable} · {v.source}</span>
        <b className="tabular-nums">{num(v.value)} {v.unit}</b><span className="text-text-dim">{date(v.valid_at)}</span>
        {v.status === "stale" ? <FlagMark flag="stale" /> : v.status === "unknown" && <span className="text-text-dim">cadence unknown</span>}</div>)}</div>}
    {mine.length > 0 && <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Links · {mine.length}</h4>
      {mine.map(r => { const other = byId.get(r.from === e.id ? r.to : r.from); const st = statusStroke[r.status] || statusStroke.inferred;
        return <div key={r.id} className="text-xs mt-1 flex gap-1 items-center flex-wrap">
          <span>{r.from === e.id ? "→" : "←"} {r.type}</span>
          <button className="text-accent" onClick={() => other && select(other.id)}>{other?.name ?? `#${r.from === e.id ? r.to : r.from}`}</button>
          <span style={{ color: st.color }}>{r.status}</span>{r.basis === "synthetic" && <BasisMark basis="synthetic" />}
          <span className="text-text-dim">{r.evidence_ids.length ? `evidence ${r.evidence_ids.join(", ")}` : "no evidence"}</span></div>; })}</div>}
    <div className="text-xs text-text-dim">{e.counts.evidence} evidence · {e.counts.claims} claims · {e.counts.state} values · {e.counts.questions} open questions</div>
    {issues.length > 0 && <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Quality</h4>{issues.map((i, n) => <IssueRow key={n} i={i} select={select} />)}</div>}
  </div>;
}

function PointDetail({ p }: { p: Pt }) {
  const url = safeURL(p.source_ref);
  return <div className="space-y-2">
    <h3 className="font-semibold">{p.title}</h3>
    <div className="flex gap-1 flex-wrap items-center text-xs"><span className="text-text-dim">{p.kind} · {p.source} · #{p.evidence_id}</span><BasisMark basis={p.basis} /><span className="px-1.5 rounded border border-border">{p.access}</span></div>
    <p className="text-xs">Event {date(p.event_time)} · observed {date(p.observed_at)}</p>
    <p className="text-xs">{p.relationship} · {p.label} · {p.precision}{p.confidence != null ? ` · ${Math.round(p.confidence * 100)}% confidence` : ""}</p>
    <p className="text-xs text-text-dim">Location provenance: {p.provenance}</p>
    {url ? <a className="text-accent underline text-xs" href={url} target="_blank" rel="noopener noreferrer">Original source</a> : <p className="text-xs text-text-dim">Source reference: {p.source_ref || "none"}</p>}
  </div>;
}

function CountryDetail({ id, data, ents, rels, byId, select }: { id: string; data: MapData; ents: Ent[]; rels: Rel[]; byId: Map<number, Ent>; select: (id: number) => void }) {
  const here = ents.filter(e => e.country_id === id);
  const hereIds = new Set(here.map(e => e.id));
  // Entities with no place of their own, linked to something here (an
  // indicator that measures this country, a role at an institution here).
  const linked = new Map<number, Rel[]>();
  for (const r of rels) {
    const inside = hereIds.has(r.from) ? r.to : hereIds.has(r.to) ? r.from : 0;
    const other = byId.get(inside);
    if (other && !other.location) linked.set(other.id, [...(linked.get(other.id) || []), r]);
  }
  const pts = data.evidence_points.filter(p => p.country_id === id);
  return <div className="space-y-3">
    <h3 className="font-semibold">{countryName[id] || id}</h3>
    <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Located here · {here.length}</h4>
      {here.map(e => <button key={e.id} className="block text-left text-xs mt-1 text-accent" onClick={() => select(e.id)}>{e.name} <span className="text-text-dim">{e.kind} · {e.location?.precision}</span></button>)}</div>
    {linked.size > 0 && <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Linked without a place · {linked.size}</h4>
      {[...linked.entries()].map(([eid, rs]) => { const e = byId.get(eid)!; return <div key={eid} className="text-xs mt-1">
        <button className="text-accent" onClick={() => select(eid)}>{e.name}</button> <span className="text-text-dim">{rs.map(r => r.type).join(", ")}</span>
        {(e.latest || []).slice(0, 1).map((v, i) => <span key={i}> · {num(v.value)} {v.unit} ({date(v.valid_at)}){v.status === "stale" ? " · stale" : ""}</span>)}</div>; })}</div>}
    {pts.length > 0 && <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Evidence points · {pts.length}</h4>
      {pts.slice(0, 20).map((p, i) => <div key={i} className="text-xs mt-1">{p.title} <span className="text-text-dim">{p.source}</span></div>)}</div>}
  </div>;
}

function Health({ data, syntheticRows, busy, synthetic }: { data: MapData | null; syntheticRows: number; busy: boolean; synthetic: (a: "seed" | "clear") => void }) {
  if (!data) return <p className="text-text-muted">{busy ? "Loading…" : "No data."}</p>;
  const tot = (b: string) => Object.values(data.basis || {}).reduce((n, m) => n + (m[b] || 0), 0);
  const empty = data.entities.length + data.evidence_points.length === 0;
  return <div className="space-y-3">
    <h3 className="font-semibold">Data health</h3>
    <div className="grid grid-cols-3 gap-2 text-center">
      {(["conflict", "stale", "missing"] as const).map(t => <div key={t} className="border border-border rounded p-2">
        <div className="text-lg font-semibold" style={{ color: data.quality.summary[t] ? (t === "conflict" ? C.conflict : t === "stale" ? C.stale : C.text) : C.dim }}>{data.quality.summary[t] || 0}</div>
        <div className="text-xs text-text-dim">{t}</div></div>)}
    </div>
    <div><h4 className="text-xs uppercase tracking-wide text-text-dim">What the data is</h4>
      <div className="flex gap-2 flex-wrap mt-1 text-xs">{(["observed", "inferred", "synthetic"] as const).map(b => <span key={b} className="flex items-center gap-1"><BasisMark basis={b} /> {tot(b)} rows</span>)}</div>
      <p className="text-xs text-text-dim mt-1">Observed: a source said it. Inferred: derived by the app or a model. Synthetic: sample data, never used by forecasts.</p></div>
    <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Evidence access</h4>
      <p className="text-xs mt-1">{data.access.open || 0} open · {data.access.internal || 0} internal · {data.access.restricted || 0} restricted</p>
      <p className="text-xs text-text-dim">Agents get restricted content only with an explicit grant on intelligence.evidence.read; this panel shows everything.</p></div>
    <div><h4 className="text-xs uppercase tracking-wide text-text-dim">Sources</h4>
      {data.quality.sources.map(s => <div key={s.slug} className="text-xs flex gap-2 mt-0.5"><span className="flex-1">{s.slug}</span>
        <span style={{ color: s.status === "fresh" ? C.observed : s.status === "stale" || s.status === "degraded" ? C.stale : s.status === "failing" ? C.conflict : C.dim }}>{s.status}</span>
        <span className="text-text-dim">{s.access}</span></div>)}</div>
    <div className="border-t border-border pt-2">
      {syntheticRows > 0
        ? <><p className="text-xs text-text-muted mb-1">{syntheticRows} synthetic rows are loaded and labelled.</p><button className={control} disabled={busy} onClick={() => synthetic("clear")}>Remove synthetic data</button></>
        : <><p className="text-xs text-text-muted mb-1">{empty ? "Nothing is located yet. " : ""}A small labelled synthetic scenario shows every map feature and quality check.</p><button className={control} disabled={busy} onClick={() => synthetic("seed")}>Load synthetic sample</button></>}
    </div>
  </div>;
}
