import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface PanelProps { projectId: string }
interface Artifact { id: number; format: string; name: string; size_bytes: number; kind: string }
interface Revision { id: number; revision_number: number; definition: Record<string, unknown>; parameters: Record<string, number>; note: string; source_sha256: string }
interface Build { id: number; status: string; report?: GeometryReport; checks?: Check[]; duration_ms?: number }
interface Design { id: number; name: string; description: string; kind: string; status: string; tags: string[]; current_revision_id: number; current_revision?: Revision; latest_build?: Build; artifacts?: Artifact[]; updated_at: string }
interface GeometryReport { valid: boolean; bounds: { size: [number, number, number] }; volume_mm3: number; surface_area_mm2: number; face_count: number; triangle_count: number }
interface Check { type: string; status: string; message: string }
interface Mesh { vertices: number[]; triangles: number[]; normals?: number[] }

const API = "/api/apps/design/api";
const FALLBACK = {
  schema: "apteva-design/v1", units: "mm",
  parameters: {
    width: { type: "number", default: 80, min: 20, max: 300 },
    depth: { type: "number", default: 50, min: 20, max: 300 },
    thickness: { type: "number", default: 4, min: 1, max: 20 },
    hole: { type: "number", default: 4.2, min: 1, max: 20 },
    margin: { type: "number", default: 7, min: 2, max: 40 },
  },
  operations: [
    { id: "plate", type: "box", size: ["width", "depth", "thickness"] },
    { id: "h1", type: "cylinder", radius: "hole/2", height: "thickness+2", origin: ["margin", "margin", -1] },
    { id: "h2", type: "cylinder", radius: "hole/2", height: "thickness+2", origin: ["width-margin", "margin", -1] },
    { id: "h3", type: "cylinder", radius: "hole/2", height: "thickness+2", origin: ["width-margin", "depth-margin", -1] },
    { id: "h4", type: "cylinder", radius: "hole/2", height: "thickness+2", origin: ["margin", "depth-margin", -1] },
    { id: "drilled", type: "cut", inputs: ["plate", "h1", "h2", "h3", "h4"] },
    { id: "finished", type: "fillet", input: "drilled", radius: 1.5 },
  ], output: "finished",
  checks: [{ type: "bounding_box", max: [300, 300, 20] }, { type: "body_count", equals: 1 }],
};

function url(path: string, projectId: string) {
  const join = path.includes("?") ? "&" : "?";
  return `${API}${path}${join}project_id=${encodeURIComponent(projectId)}`;
}

async function request(path: string, projectId: string, init?: RequestInit) {
  const response = await fetch(url(path, projectId), { credentials: "same-origin", ...init, headers: { "Content-Type": "application/json", ...(init?.headers || {}) } });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || response.statusText);
  return body;
}

function fmt(value: number, digits = 1) {
  return Number.isFinite(value) ? value.toLocaleString(undefined, { maximumFractionDigits: digits }) : "—";
}

export default function DesignPanel({ projectId }: PanelProps) {
  const [designs, setDesigns] = useState<Design[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [design, setDesign] = useState<Design | null>(null);
  const [definitionText, setDefinitionText] = useState(JSON.stringify(FALLBACK, null, 2));
  const [parameters, setParameters] = useState<Record<string, number>>({});
  const [newName, setNewName] = useState("Mounting plate");
  const [note, setNote] = useState("");
  const [mode, setMode] = useState<"model" | "source">("model");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ report?: GeometryReport; checks?: Check[]; artifacts?: Artifact[] } | null>(null);

  const loadDesigns = useCallback(async () => {
    const body = await request("/designs?status=all", projectId);
    setDesigns(body.designs || []);
    if (!selectedId && body.designs?.length) setSelectedId(body.designs[0].id);
  }, [projectId, selectedId]);

  const loadDesign = useCallback(async (id: number) => {
    const body = await request(`/designs/${id}`, projectId);
    const next = body.design as Design;
    setDesign(next);
    setDefinitionText(JSON.stringify(next.current_revision?.definition || FALLBACK, null, 2));
    setParameters(next.current_revision?.parameters || {});
    setResult(next.latest_build ? { report: next.latest_build.report, checks: next.latest_build.checks, artifacts: next.artifacts } : { artifacts: next.artifacts });
  }, [projectId]);

  useEffect(() => { loadDesigns().catch((e) => setError(e.message)); }, [loadDesigns]);
  useEffect(() => { if (selectedId) loadDesign(selectedId).catch((e) => setError(e.message)); }, [selectedId, loadDesign]);

  const parsed = useMemo(() => {
    try { return JSON.parse(definitionText) as any; } catch { return null; }
  }, [definitionText]);
  const specs = parsed?.parameters || {};

  async function createDesign() {
    if (!parsed) return setError("The design JSON is not valid.");
    setBusy("Creating"); setError("");
    try {
      const body = await request("/designs", projectId, { method: "POST", body: JSON.stringify({ name: newName, kind: "parametric", definition: parsed, parameters, note: "Initial design" }) });
      await loadDesigns(); setSelectedId(body.design.id);
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function saveRevision() {
    if (!design || !parsed) return setError("Select a design and fix the source JSON first.");
    setBusy("Saving revision"); setError("");
    try {
      await request(`/designs/${design.id}/revisions`, projectId, { method: "POST", body: JSON.stringify({ expected_parent_id: design.current_revision_id, definition: parsed, parameters, note }) });
      setNote(""); await loadDesign(design.id); await loadDesigns();
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function build(formats: string[]) {
    if (!design) return setError("Create or select a design first.");
    setBusy(formats.length > 2 ? "Exporting" : "Building"); setError("");
    try {
      const body = await request(`/designs/${design.id}/build`, projectId, { method: "POST", body: JSON.stringify({ formats }) });
      setResult(body); await loadDesign(design.id);
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function makePackage() {
    if (!design) return;
    setBusy("Packaging"); setError("");
    try { await request(`/designs/${design.id}/package`, projectId, { method: "POST", body: "{}" }); await loadDesign(design.id); }
    catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  const artifacts = result?.artifacts || design?.artifacts || [];
  const mesh = [...artifacts].reverse().find((item) => item.format === "mesh-json");
  const report = result?.report || design?.latest_build?.report;
  const checks = result?.checks || design?.latest_build?.checks || [];

  return <div className="h-full flex flex-col bg-bg text-text overflow-hidden">
    <header className="h-14 px-4 border-b border-border flex items-center gap-3 shrink-0">
      <div className="w-8 h-8 rounded-lg bg-accent/15 text-accent grid place-items-center text-lg">◇</div>
      <div><h1 className="text-sm font-semibold leading-tight">Design Studio</h1><p className="text-[11px] text-text-muted">Parametric CAD · B-rep · manufacturing</p></div>
      <span className="flex-1" />
      <button className="btn" disabled={!!busy || !design} onClick={() => build(["mesh-json", "glb"])}>Build preview</button>
      <button className="btn" disabled={!!busy || !design} onClick={() => build(["step", "stl", "3mf", "glb"])}>Export CAD</button>
      <button className="btn-primary" disabled={!!busy || !design} onClick={makePackage}>Manufacturing ZIP</button>
    </header>
    {error && <div className="px-4 py-2 text-xs border-b border-red/30 bg-red/10 text-red">{error}</div>}
    {busy && <div className="h-0.5 bg-accent animate-pulse" />}
    <div className="flex-1 min-h-0 grid grid-cols-[220px_minmax(360px,1fr)_320px]">
      <aside className="border-r border-border p-3 overflow-y-auto">
        <div className="text-[10px] uppercase tracking-widest text-text-muted mb-2">Designs</div>
        <div className="space-y-1">
          {designs.map((item) => <button key={item.id} onClick={() => setSelectedId(item.id)} className={`w-full text-left p-2.5 rounded-lg border ${selectedId === item.id ? "border-accent/50 bg-accent/10" : "border-transparent hover:bg-bg-input/50"}`}>
            <div className="text-xs font-medium truncate">{item.name}</div><div className="text-[10px] text-text-muted mt-1">{item.kind} · r{item.current_revision?.revision_number || "—"}</div>
          </button>)}
          {!designs.length && <p className="text-xs text-text-muted p-2">No designs yet. Start with the template.</p>}
        </div>
        <div className="mt-5 pt-4 border-t border-border space-y-2">
          <label className="text-[10px] uppercase tracking-widest text-text-muted">New design</label>
          <input className="field" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Design name" />
          <button className="btn-primary w-full" disabled={!!busy || !newName.trim() || !parsed} onClick={createDesign}>Create from editor</button>
        </div>
      </aside>

      <main className="min-w-0 flex flex-col bg-[#0c1118]">
        <div className="h-11 px-3 border-b border-border flex items-center gap-2 bg-bg">
          <button className={mode === "model" ? "tab-active" : "tab"} onClick={() => setMode("model")}>3D model</button>
          <button className={mode === "source" ? "tab-active" : "tab"} onClick={() => setMode("source")}>Geometry source</button>
          <span className="flex-1" />
          {design && <span className="text-[10px] text-text-muted font-mono">r{design.current_revision?.revision_number} · {design.current_revision?.source_sha256?.slice(0, 10)}</span>}
        </div>
        {mode === "model" ? <div className="flex-1 min-h-0 relative">
          <MeshViewer projectId={projectId} artifactId={mesh?.id} />
          {!mesh && <div className="absolute inset-0 grid place-items-center pointer-events-none"><div className="text-center"><div className="text-4xl text-text-muted/30 mb-3">◇</div><p className="text-sm text-text-muted">Build a preview to inspect the model</p><p className="text-[11px] text-text-muted/60 mt-1">Drag to orbit · wheel to zoom</p></div></div>}
          {report && <div className="absolute left-3 bottom-3 flex gap-1.5">
            <Metric label="X" value={`${fmt(report.bounds.size[0])} mm`} /><Metric label="Y" value={`${fmt(report.bounds.size[1])} mm`} /><Metric label="Z" value={`${fmt(report.bounds.size[2])} mm`} />
          </div>}
        </div> : <textarea className="flex-1 min-h-0 resize-none bg-[#0b1016] p-5 font-mono text-xs leading-5 outline-none text-[#cbd5e1]" spellCheck={false} value={definitionText} onChange={(e) => setDefinitionText(e.target.value)} />}
      </main>

      <aside className="border-l border-border overflow-y-auto">
        <Section title="Parameters">
          {Object.entries(specs).map(([name, spec]: [string, any]) => <label key={name} className="block mb-3">
            <span className="flex text-xs mb-1"><span>{name}</span><span className="ml-auto font-mono text-accent">{parameters[name] ?? spec.default} mm</span></span>
            <input className="w-full accent-[var(--color-accent)]" type="range" min={spec.min ?? 0} max={spec.max ?? Math.max((spec.default || 1) * 3, 10)} step={spec.step ?? 0.1} value={parameters[name] ?? spec.default} onChange={(e) => setParameters((p) => ({ ...p, [name]: Number(e.target.value) }))} />
          </label>)}
          {!Object.keys(specs).length && <p className="text-xs text-text-muted">No exposed parameters.</p>}
        </Section>
        <Section title="Revision">
          <textarea className="field min-h-16 resize-y" value={note} onChange={(e) => setNote(e.target.value)} placeholder="What changed?" />
          <button className="btn w-full mt-2" disabled={!!busy || !design || !parsed} onClick={saveRevision}>Save immutable revision</button>
        </Section>
        {report && <Section title="Measurements">
          <Row label="Volume" value={`${fmt(report.volume_mm3)} mm³`} /><Row label="Surface" value={`${fmt(report.surface_area_mm2)} mm²`} /><Row label="Faces" value={String(report.face_count)} /><Row label="Triangles" value={String(report.triangle_count)} />
        </Section>}
        {!!checks.length && <Section title="Validation">
          <div className="space-y-2">{checks.map((check, i) => <div key={i} className="flex gap-2 text-xs"><span className={check.status === "pass" ? "text-green" : "text-red"}>{check.status === "pass" ? "●" : "×"}</span><span className="text-text-muted">{check.message}</span></div>)}</div>
        </Section>}
        <Section title="Artifacts">
          <div className="space-y-1.5">{artifacts.filter((item) => item.format !== "mesh-json").map((item) => <a key={item.id} className="flex items-center gap-2 rounded border border-border p-2 hover:border-accent/50 text-xs" href={url(`/artifacts/${item.id}/content`, projectId)}>
            <span className="font-mono uppercase text-accent w-9">{item.format}</span><span className="truncate flex-1">{item.name}</span><span className="text-text-muted">{fmt(item.size_bytes / 1024)}K</span>
          </a>)}{!artifacts.some((item) => item.format !== "mesh-json") && <p className="text-xs text-text-muted">Exports will appear here.</p>}</div>
        </Section>
      </aside>
    </div>
    <style>{styles}</style>
  </div>;
}

function MeshViewer({ projectId, artifactId }: { projectId: string; artifactId?: number }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [mesh, setMesh] = useState<Mesh | null>(null);
  const view = useRef({ rx: -0.55, ry: 0.7, zoom: 1, drag: false, x: 0, y: 0 });
  useEffect(() => {
    if (!artifactId) { setMesh(null); return; }
    fetch(url(`/artifacts/${artifactId}/content`, projectId), { credentials: "same-origin" }).then((r) => r.json()).then(setMesh).catch(() => setMesh(null));
  }, [artifactId, projectId]);

  const draw = useCallback(() => {
    const c = canvas.current; if (!c || !mesh) return;
    const dpr = devicePixelRatio || 1, rect = c.getBoundingClientRect(); c.width = rect.width * dpr; c.height = rect.height * dpr;
    const g = c.getContext("2d")!; g.scale(dpr, dpr); g.clearRect(0, 0, rect.width, rect.height);
    const v = mesh.vertices, state = view.current; const pts: [number, number, number][] = [];
    let minX=Infinity,maxX=-Infinity,minY=Infinity,maxY=-Infinity;
    for (let i=0;i<v.length;i+=3) { const x=v[i],y=v[i+1],z=v[i+2]; const cy=Math.cos(state.ry),sy=Math.sin(state.ry),cx=Math.cos(state.rx),sx=Math.sin(state.rx); const x1=x*cy+z*sy,z1=-x*sy+z*cy,y1=y*cx-z1*sx,z2=y*sx+z1*cx; pts.push([x1,y1,z2]); minX=Math.min(minX,x1);maxX=Math.max(maxX,x1);minY=Math.min(minY,y1);maxY=Math.max(maxY,y1); }
    const scale=Math.min(rect.width/(maxX-minX||1),rect.height/(maxY-minY||1))*.72*state.zoom, ox=rect.width/2-(minX+maxX)/2*scale, oy=rect.height/2+(minY+maxY)/2*scale;
    const faces=[] as {p:[number,number,number][],z:number,shade:number}[];
    for(let i=0;i<mesh.triangles.length;i+=3){const p=[pts[mesh.triangles[i]],pts[mesh.triangles[i+1]],pts[mesh.triangles[i+2]]] as [number,number,number][];const ax=p[1][0]-p[0][0],ay=p[1][1]-p[0][1],bx=p[2][0]-p[0][0],by=p[2][1]-p[0][1];faces.push({p,z:(p[0][2]+p[1][2]+p[2][2])/3,shade:Math.max(.18,Math.min(.95,.55+(ax*by-ay*bx)/Math.max(1,Math.abs(ax*by-ay*bx))*.18))});}
    faces.sort((a,b)=>a.z-b.z); for(const f of faces){g.beginPath();g.moveTo(ox+f.p[0][0]*scale,oy-f.p[0][1]*scale);g.lineTo(ox+f.p[1][0]*scale,oy-f.p[1][1]*scale);g.lineTo(ox+f.p[2][0]*scale,oy-f.p[2][1]*scale);g.closePath();g.fillStyle=`rgba(74,181,255,${f.shade})`;g.fill();g.strokeStyle="rgba(150,220,255,.12)";g.stroke();}
  }, [mesh]);
  useEffect(() => { draw(); const resize=new ResizeObserver(draw); if(canvas.current)resize.observe(canvas.current); return()=>resize.disconnect(); }, [draw]);
  return <canvas ref={canvas} className="w-full h-full cursor-grab active:cursor-grabbing" onPointerDown={(e)=>{view.current.drag=true;view.current.x=e.clientX;view.current.y=e.clientY;(e.target as Element).setPointerCapture(e.pointerId)}} onPointerMove={(e)=>{if(!view.current.drag)return;view.current.ry+=(e.clientX-view.current.x)*.01;view.current.rx+=(e.clientY-view.current.y)*.01;view.current.x=e.clientX;view.current.y=e.clientY;draw()}} onPointerUp={()=>view.current.drag=false} onWheel={(e)=>{e.preventDefault();view.current.zoom=Math.max(.25,Math.min(5,view.current.zoom*(e.deltaY>0?.9:1.1)));draw()}} />;
}

function Section({ title, children }: { title: string; children: any }) { return <section className="p-4 border-b border-border"><h2 className="text-[10px] uppercase tracking-widest text-text-muted mb-3">{title}</h2>{children}</section> }
function Row({ label, value }: { label: string; value: string }) { return <div className="flex text-xs py-1"><span className="text-text-muted">{label}</span><span className="ml-auto font-mono">{value}</span></div> }
function Metric({ label, value }: { label: string; value: string }) { return <div className="rounded bg-black/50 backdrop-blur px-2 py-1 text-[10px]"><span className="text-text-muted mr-1">{label}</span><span className="font-mono">{value}</span></div> }

const styles = `
.btn,.btn-primary{font-size:11px;padding:6px 10px;border-radius:6px;border:1px solid var(--color-border);transition:.15s}.btn:hover{background:var(--color-bg-input)}.btn-primary{background:var(--color-accent);border-color:var(--color-accent);color:var(--color-bg);font-weight:600}.btn:disabled,.btn-primary:disabled{opacity:.4;cursor:not-allowed}.field{width:100%;border:1px solid var(--color-border);background:var(--color-bg-input);border-radius:6px;padding:7px 8px;font-size:12px;outline:none}.field:focus{border-color:var(--color-accent)}.tab,.tab-active{font-size:11px;padding:6px 9px;border-radius:6px;color:var(--color-text-muted)}.tab-active{background:var(--color-bg-input);color:var(--color-text)}
`;
