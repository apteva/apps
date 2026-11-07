import AssemblyViewer from "./AssemblyViewer";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface PanelProps { projectId: string }
interface Artifact { id: number; format: string; name: string; size_bytes: number; kind: string }
interface Revision { id: number; revision_number: number; definition: Record<string, unknown>; parameters: Record<string, number>; note: string; source_sha256: string }
interface Build { id: number; status: string; report?: GeometryReport; checks?: Check[]; duration_ms?: number }
interface Design { id: number; name: string; description: string; kind: string; status: string; tags: string[]; current_revision_id: number; current_revision?: Revision; latest_build?: Build; artifacts?: Artifact[]; updated_at: string }
interface GeometryReport { valid: boolean; bounds: { size: [number, number, number] }; volume_mm3: number; surface_area_mm2: number; face_count: number; triangle_count: number; body_count: number; mass_g?: number; center_of_mass?: [number, number, number]; parts?: unknown[] }
interface Check { id?:string;type: string; status: string; message: string;instances?:string[];details?:any }
interface MeshPart extends Mesh { id: string; part_id: string; name: string; color?: string }
interface Mesh { vertices: number[]; triangles: number[]; normals?: number[]; parts?: MeshPart[] }

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
  const [pcbDesignId, setPCBDesignId] = useState("");
  const [pcbRevisionId, setPCBRevisionId] = useState("");
  const [pcbBound, setPCBBound] = useState<boolean | null>(null);
  const [note, setNote] = useState("");
  const [mode, setMode] = useState<"model" | "source">("model");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ report?: GeometryReport; checks?: Check[]; artifacts?: Artifact[] } | null>(null);
  const [openRover, setOpenRover] = useState<any>(null);
  const [focus,setFocus]=useState<string[]>([]),[issue,setIssue]=useState<Check|null>(null),[instance,setInstance]=useState(''),[importing,setImporting]=useState(false),[model,setModel]=useState(''),[manufacturer,setManufacturer]=useState(''),[sourceURL,setSourceURL]=useState(''),[hardwareRevision,setHardwareRevision]=useState(''),[omissions,setOmissions]=useState(''),[geometryStatus,setGeometryStatus]=useState('manufacturer_cad'),[uncertainty,setUncertainty]=useState('0'),[complete,setComplete]=useState(false),[capture,setCapture]=useState<string|null>(null);
  async function importSTEP(file:File) {
    setBusy('Importing STEP');setError('');
    try{const data=new FormData();data.append('file',file);data.append('evidence',JSON.stringify({model,manufacturer,hardware_revision:hardwareRevision,source:sourceURL,date:new Date().toISOString().slice(0,10),geometry_status:geometryStatus,complete,uncertainty_mm:Number(uncertainty),omissions:omissions.split('\n').filter(Boolean)}));const r=await fetch(url('/sources',projectId),{method:'POST',credentials:'same-origin',body:data});const b=await r.json();if(!r.ok)throw new Error(b.error||'Import failed');await loadDesigns();setSelectedId(b.design.id);setImporting(false);await loadDesign(b.design.id);setMode('model');}catch(e){setError((e as Error).message)}finally{setBusy('')}
  }
  async function createAssemblyEnclosure(){if(!design)return;setBusy('Generating enclosure');setError('');try{const b=await request('/assembly-enclosures',projectId,{method:'POST',body:JSON.stringify({assembly_design_id:design.id,assembly_revision_id:design.current_revision_id,options:{}})});await loadDesigns();setSelectedId(b.design.id);await loadDesign(b.design.id)}catch(e){setError((e as Error).message)}finally{setBusy('')}}
  function changePlacement(axis:number,value:string,rotation=false){if(!parsed)return;const d=structuredClone(parsed),i=d.assembly?.instances.find((i:any)=>i.id===instance);if(!i)return;const n=Number(value);if(!Number.isFinite(n))return;i.transform ||= {};if(rotation){i.transform.rotate=[{angle:n,direction:[0,0,1]}]}else{i.transform.translate ||= [0,0,0];i.transform.translate[axis]=n}setDefinitionText(JSON.stringify(d,null,2));}


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
  useEffect(() => { request("/pcb-source", projectId).then((body) => setPCBBound(!!body.bound)).catch(() => setPCBBound(false)); }, [projectId]);
  useEffect(() => { request("/examples", projectId).then((body) => setOpenRover(body.featured?.open_rover?.definition || null)).catch(() => setOpenRover(null)); }, [projectId]);

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

  async function createPCBEnclosure() {
    const sourceId = Number(pcbDesignId), revisionId = Number(pcbRevisionId || 0);
    if (!Number.isInteger(sourceId) || sourceId <= 0) return setError("Enter a valid PCB Studio design ID.");
    setBusy("Creating PCB enclosure"); setError("");
    try {
      const body = await request("/pcb-enclosures", projectId, { method: "POST", body: JSON.stringify({ pcb_design_id: sourceId, pcb_revision_id: revisionId, name: `PCB ${sourceId} enclosure` }) });
      await loadDesigns(); setSelectedId(body.design.id); setMode("model");
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function createOpenRover() {
    if (!openRover) return setError("The Open Rover reference design is unavailable.");
    setBusy("Creating Open Rover"); setError("");
    try {
      const created = await request("/designs", projectId, { method: "POST", body: JSON.stringify({
        name: "Apteva Open Rover", description: "Fully printable modular open-hardware robot chassis.", kind: "parametric",
        tags: ["robot", "open-hardware", "assembly", "3d-printing"], definition: openRover, parameters: {}, note: "Open Rover reference design",
      }) });
      await request(`/designs/${created.design.id}/build`, projectId, { method: "POST", body: JSON.stringify({ formats: ["mesh-json", "glb"] }) });
      await loadDesigns(); setSelectedId(created.design.id); await loadDesign(created.design.id); setMode("model");
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function createLinkedAssembly() {
    if (!design?.current_revision) return setError("Select a component design first.");
    setBusy("Creating linked assembly"); setError("");
    try {
      const body = await request("/assemblies", projectId, { method: "POST", body: JSON.stringify({
        name: `${design.name} Assembly`, description: `Assembly linked to ${design.name} r${design.current_revision.revision_number}.`,
        components: [{ design_id: design.id, revision_id: design.current_revision.id }],
      }) });
      await loadDesigns(); setSelectedId(body.design.id); await loadDesign(body.design.id); setMode("source");
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function refreshPCBEnclosure() {
    if (!design || !parsed?.provenance || parsed.provenance.kind !== "pcb-mechanical-envelope") return;
    setBusy("Refreshing PCB enclosure"); setError("");
    try {
      await request(`/designs/${design.id}/refresh-pcb`, projectId, { method: "POST", body: JSON.stringify({ expected_parent_id: design.current_revision_id, pcb_revision_id: 0 }) });
      await loadDesign(design.id); await loadDesigns();
    } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }

  async function refreshLinkedComponents() {
    if (!design) return;
    setBusy("Checking linked components"); setError("");
    try {
      await request(`/designs/${design.id}/refresh-components`, projectId, { method: "POST", body: JSON.stringify({ expected_parent_id: design.current_revision_id }) });
      await loadDesign(design.id); await loadDesigns();
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
  const mesh = artifacts.filter((item) => item.format === "mesh-json" && !(item as any).metadata?.part_id).sort((a, b) => b.id - a.id)[0];
  const report = result?.report || design?.latest_build?.report;
  const checks = result?.checks || design?.latest_build?.checks || [];

  return <div className="design-studio h-full flex flex-col bg-bg text-text overflow-hidden">
    <header className="design-header h-14 px-4 border-b border-border flex items-center gap-3 shrink-0">
      <div className="w-8 h-8 rounded-lg bg-accent/15 text-accent grid place-items-center text-lg">◇</div>
      <div><h1 className="text-sm font-semibold leading-tight">Design Studio</h1><p className="text-[11px] text-text-muted">Open hardware · assemblies · parametric CAD</p></div>
      <span className="flex-1" />
      <button className="design-button" disabled={!!busy} onClick={()=>setImporting(!importing)}>Import STEP</button>
      <button className="design-button" disabled={!!busy || !design} onClick={createAssemblyEnclosure}>Enclosure template</button>
      <button className="design-button" disabled={!!busy || !design} onClick={() => build(["mesh-json", "glb"])}>Build preview</button>
      <button className="design-button" disabled={!!busy || !design} onClick={() => build(["step", "stl", "3mf", "glb"])}>Export CAD</button>
      <button className="design-button design-button-primary" disabled={!!busy || !design} onClick={makePackage}>Manufacturing ZIP</button>
    </header>
    {importing&&<div className="p-4 border-b border-border text-xs" style={{display:'grid',gridTemplateColumns:'repeat(4,minmax(0,1fr))',gap:8}}>
      <input className="field" placeholder="Exact manufacturer model (required)" value={model} onChange={e=>setModel(e.target.value)}/><input className="field" placeholder="Manufacturer" value={manufacturer} onChange={e=>setManufacturer(e.target.value)}/><input className="field" placeholder="Hardware revision" value={hardwareRevision} onChange={e=>setHardwareRevision(e.target.value)}/><input className="field" placeholder="CAD source URL" value={sourceURL} onChange={e=>setSourceURL(e.target.value)}/>
      <select className="field" value={geometryStatus} onChange={e=>setGeometryStatus(e.target.value)}><option value="manufacturer_cad">Manufacturer CAD</option><option value="drawing_derived">Drawing derived</option><option value="measured">Physically measured</option><option value="estimated">Estimated</option></select><input className="field" type="number" min="0" step=".01" aria-label="Dimensional uncertainty in mm" value={uncertainty} onChange={e=>setUncertainty(e.target.value)}/><textarea className="field" placeholder="Known omissions, one per line" value={omissions} onChange={e=>setOmissions(e.target.value)}/><label><input type="checkbox" checked={complete} onChange={e=>setComplete(e.target.checked)}/>Complete geometry evidence</label>
      <label>STEP file (up to 96 MiB)<input type="file" accept=".step,.stp" disabled={!!busy||!model.trim()} onChange={e=>{if(e.target.files?.[0])importSTEP(e.target.files[0])}}/></label><span>Original file, source hash and assembly placements are retained. Missing features require measured proxy solids.</span>
    </div>}
    {error && <div className="px-4 py-2 text-xs border-b border-red/30 bg-red/10 text-red">{error}</div>}
    {busy && <div className="h-0.5 bg-accent animate-pulse" />}
    <div className="design-workspace">
      <aside className="design-library">
        <div className="text-[10px] uppercase tracking-widest text-text-muted mb-2">Designs</div>
        <div className="space-y-1">
          {designs.map((item) => <button key={item.id} onClick={() => setSelectedId(item.id)} className="design-list-item" data-selected={selectedId === item.id}>
            <div className="design-list-name">{item.name}</div><div className="design-list-meta">{item.kind}{item.id === design?.id && design.current_revision ? ` · r${design.current_revision.revision_number}` : ""}</div>
          </button>)}
          {!designs.length && <p className="text-xs text-text-muted p-2">No designs yet. Start with the template.</p>}
        </div>
        <div className="mt-5 pt-4 border-t border-border space-y-2">
          <label className="text-[10px] uppercase tracking-widest text-text-muted">New design</label>
          <input className="field" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Design name" />
          <button className="design-button design-button-primary design-full" disabled={!!busy || !newName.trim() || !parsed} onClick={createDesign}>Create from editor</button>
          <button className="design-button design-full" disabled={!!busy || !design?.current_revision} onClick={createLinkedAssembly}>Start assembly with selected</button>
        </div>
        <div className="mt-5 pt-4 border-t border-border space-y-2">
          <label className="text-[10px] uppercase tracking-widest text-text-muted">Featured open hardware</label>
          <button className="design-button design-button-rover design-full" disabled={!!busy || !openRover} onClick={createOpenRover}><span>◈</span> Create Open Rover</button>
          <p className="text-[10px] leading-4 text-text-muted">Printable chassis, TPU tires, hubs, modular deck, interfaces, joints and release BOM.</p>
        </div>
        <div className="pcb-import mt-5 pt-4 border-t border-border space-y-2" data-bound={pcbBound === true}>
          <div className="flex items-center justify-between gap-2"><label className="text-[10px] uppercase tracking-widest text-text-muted">PCB enclosure</label><span className="pcb-binding-dot" title={pcbBound ? "PCB Studio bound" : "PCB Studio not bound"} /></div>
          <input className="field" inputMode="numeric" value={pcbDesignId} onChange={(e) => setPCBDesignId(e.target.value)} placeholder="PCB design ID" />
          <input className="field" inputMode="numeric" value={pcbRevisionId} onChange={(e) => setPCBRevisionId(e.target.value)} placeholder="Revision ID · current if empty" />
          <button className="design-button design-full" disabled={!!busy || !pcbBound || !pcbDesignId.trim()} onClick={createPCBEnclosure}>Generate linked enclosure</button>
          {pcbBound === false && <p className="text-[10px] leading-4 text-text-muted">Bind PCB Studio as the optional PCB source to enable this workflow.</p>}
        </div>
      </aside>

      <main className="design-main">
        <div className="h-11 px-3 border-b border-border flex items-center gap-2 bg-bg">
          <button className={mode === "model" ? "tab-active" : "tab"} onClick={() => setMode("model")}>3D model</button>
          <button className={mode === "source" ? "tab-active" : "tab"} onClick={() => setMode("source")}>Geometry source</button>
          <span className="flex-1" />
          {design && <span className="text-[10px] text-text-muted font-mono">r{design.current_revision?.revision_number} · {design.current_revision?.source_sha256?.slice(0, 10)}</span>}
        </div>
        {mode === "model" ? <div className="design-viewport">
          <AssemblyViewer projectId={projectId} artifactId={mesh?.id} designId={design?.id} revisionId={design?.current_revision_id} focus={focus} issue={issue} onSelect={setInstance} onCapture={setCapture}/>
          {!mesh && <div className="absolute inset-0 grid place-items-center pointer-events-none"><div className="text-center"><div className="text-4xl text-text-muted/30 mb-3">◇</div><p className="text-sm text-text-muted">Build a preview to inspect the model</p><p className="text-[11px] text-text-muted/60 mt-1">Drag to orbit · wheel to zoom</p></div></div>}
          {report && <div className="absolute left-3 bottom-3 flex gap-1.5">
            <Metric label="X" value={`${fmt(report.bounds.size[0])} mm`} /><Metric label="Y" value={`${fmt(report.bounds.size[1])} mm`} /><Metric label="Z" value={`${fmt(report.bounds.size[2])} mm`} />
          </div>}
        </div> : <textarea className="flex-1 min-h-0 resize-none bg-[#0b1016] p-5 font-mono text-xs leading-5 outline-none text-[#cbd5e1]" spellCheck={false} value={definitionText} onChange={(e) => setDefinitionText(e.target.value)} />}
      </main>

      <aside className="design-inspector">
        <Section title="Parameters">{parsed?.print_profiles?.some((p:any)=>p.fits_mm)&&<label className="text-xs">Fit profile<select className="field" value={parsed.fit_profile_id||''} onChange={e=>{const d=structuredClone(parsed);d.fit_profile_id=e.target.value||undefined;setDefinitionText(JSON.stringify(d,null,2))}}><option value="">Parameter defaults</option>{parsed.print_profiles.filter((p:any)=>p.fits_mm).map((p:any)=><option key={p.id} value={p.id}>{p.name}</option>)}</select><p className="text-[10px] mt-1">Save a revision to apply. Explicit parameter values override profile allowances.</p></label>}
          {Object.entries(specs).map(([name, spec]: [string, any]) => <label key={name} className="design-param">
            <span className="design-param-head"><span>{name}</span><span className="design-param-value">{parameters[name] ?? spec.default} mm</span></span>
            <input className="design-range" type="range" min={spec.min ?? 0} max={spec.max ?? Math.max((spec.default || 1) * 3, 10)} step={spec.step ?? 0.1} value={parameters[name] ?? spec.default} onChange={(e) => setParameters((p) => ({ ...p, [name]: Number(e.target.value) }))} />
          </label>)}
          {!Object.keys(specs).length && <p className="text-xs text-text-muted">No exposed parameters.</p>}
        </Section>
        <Section title="Revision">
          <textarea className="field min-h-16 resize-y" value={note} onChange={(e) => setNote(e.target.value)} placeholder="What changed?" />
          <button className="design-button design-full design-revision-button" disabled={!!busy || !design || !parsed} onClick={saveRevision}>Save immutable revision</button>
        </Section>
        {parsed?.provenance?.kind === "pcb-mechanical-envelope" && <Section title="PCB source">
          <Row label="PCB design" value={String(parsed.provenance.source_design_id)} /><Row label="PCB revision" value={String(parsed.provenance.source_revision_id)} /><Row label="Source hash" value={String(parsed.provenance.source_sha256 || "").slice(0, 12)} />
          <button className="design-button design-full design-revision-button" disabled={!!busy || !pcbBound || !design} onClick={refreshPCBEnclosure}>Refresh from current PCB revision</button>
        </Section>}
        {!!parsed?.parts?.some((part: any) => part.source) && <Section title="Linked components">
          <div className="space-y-2">{parsed.parts.filter((part: any) => part.source).map((part: any) => <div key={part.id} className="rounded border border-border p-2 text-[11px]">
            <div className="font-medium">{part.name}</div><div className="text-text-muted mt-1">Design {part.source.design_id} · revision {part.source.revision_id}</div><div className="font-mono text-text-muted">{String(part.source.source_sha256).slice(0, 12)}</div>
          </div>)}</div>
          <button className="design-button design-full design-revision-button" disabled={!!busy || !design} onClick={refreshLinkedComponents}>Update to current source revisions</button>
          <p className="text-[10px] leading-4 text-text-muted mt-2">Updates create a new assembly revision. Existing revisions stay pinned and reproducible.</p>
        </Section>}
        {instance&&parsed?.assembly&&<Section title="Instance placement">
          <p className="text-xs mb-2">{instance}</p>{[0,1,2].map((axis)=><label key={axis} className="text-xs">{['X','Y','Z'][axis]} mm<input className="field" type="number" step=".1" value={parsed.assembly.instances.find((i:any)=>i.id===instance)?.transform?.translate?.[axis]??0} onChange={e=>changePlacement(axis,e.target.value)}/></label>)}<label className="text-xs">Rotation about Z (degrees)<input className="field" type="number" value={parsed.assembly.instances.find((i:any)=>i.id===instance)?.transform?.rotate?.[0]?.angle??0} onChange={e=>changePlacement(2,e.target.value,true)}/></label><button className="design-button" disabled={!!busy} onClick={saveRevision}>Save placement revision</button><p className="text-[10px] mt-2">Save, then build to validate the new placement. Arbitrary rotations and datum alignment are available in Source.</p>
        </Section>}
        {capture&&<Section title="Inspection capture"><img src={capture} alt="Saved assembly inspection" style={{width:'100%'}}/><p className="text-[10px]">Downloaded PNG. Attach it to revision-bound test evidence for release.</p></Section>}
        {report && <Section title="Measurements">
          <Row label="Bodies" value={String(report.body_count || 1)} /><Row label="Mass" value={report.mass_g ? `${fmt(report.mass_g)} g` : "—"} /><Row label="Volume" value={`${fmt(report.volume_mm3)} mm³`} /><Row label="Surface" value={`${fmt(report.surface_area_mm2)} mm²`} /><Row label="Faces" value={String(report.face_count)} /><Row label="Triangles" value={String(report.triangle_count)} />
        </Section>}
        {parsed?.open_hardware && <Section title="Open hardware">
          <Row label="Project" value={parsed.open_hardware.project_name} /><Row label="Version" value={parsed.open_hardware.version} /><Row label="License" value={parsed.open_hardware.license} />
          <Row label="Parts" value={String(parsed.parts?.length || 0)} /><Row label="Instances" value={String(parsed.assembly?.instances?.length || 0)} /><Row label="Joints" value={String(parsed.assembly?.joints?.length || 0)} />
        </Section>}
        {!!checks.length && <Section title="Validation">
          <div className="space-y-2">{checks.map((check, i) => <button key={i} onClick={()=>{setFocus(check.instances||[]);setIssue(check)}} className="design-validation-issue flex gap-2 text-xs text-left w-full" title="Focus affected instances"><span className={check.status === "pass" ? "text-green" : "text-red"}>{check.status === "pass" ? "●" : "×"}</span><span className="text-text-muted">{check.status}: {check.message}</span></button>)}</div>
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

function Section({ title, children }: { title: string; children: any }) { return <section className="design-section"><h2 className="design-section-title">{title}</h2>{children}</section> }
function Row({ label, value }: { label: string; value: string }) { return <div className="design-row"><span className="text-text-muted">{label}</span><span className="font-mono">{value}</span></div> }
function Metric({ label, value }: { label: string; value: string }) { return <div className="rounded bg-black/50 backdrop-blur px-2 py-1 text-[10px]"><span className="text-text-muted mr-1">{label}</span><span className="font-mono">{value}</span></div> }

const styles = `
.design-studio,.design-studio *{box-sizing:border-box}.design-studio{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--color-bg,#090b0e);color:var(--color-text,#e8edf2)}
.design-header{display:flex;align-items:center;gap:12px;height:56px;min-height:56px;padding:0 16px;border-bottom:1px solid var(--color-border,#252a31);overflow-x:auto}
.design-workspace{display:grid;grid-template-columns:220px minmax(360px,1fr) 320px;flex:1;min-width:0;min-height:0;overflow:hidden}
.design-library{min-width:0;min-height:0;padding:12px;border-right:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-main{display:flex;flex-direction:column;min-width:0;min-height:0;background:#0c1118}
.design-viewport{position:relative;flex:1;min-width:0;min-height:0;overflow:hidden;background:radial-gradient(circle at 50% 38%,#18202a 0,#0a0f15 48%,#05080c 100%)}
.design-viewport:after{content:"";position:absolute;inset:58% -20% -30%;pointer-events:none;opacity:.25;transform:perspective(500px) rotateX(62deg);transform-origin:top;background-image:linear-gradient(rgba(249,115,22,.18) 1px,transparent 1px),linear-gradient(90deg,rgba(249,115,22,.18) 1px,transparent 1px);background-size:32px 32px}
.design-inspector{min-width:0;min-height:0;border-left:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-list-item{display:block;width:100%;padding:10px;text-align:left;color:inherit;background:transparent;border:1px solid transparent;border-radius:8px;cursor:pointer;transition:background .15s,border-color .15s}
.design-list-item:hover{background:var(--color-bg-input,#151a21)}.design-list-item[data-selected="true"]{background:color-mix(in srgb,var(--color-accent,#f97316) 12%,transparent);border-color:color-mix(in srgb,var(--color-accent,#f97316) 48%,transparent)}
.design-list-name{font-size:12px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.design-list-meta{margin-top:4px;font-size:10px;color:var(--color-text-muted,#8b949e)}
.design-button{flex:none;font-size:11px;line-height:1.2;padding:7px 10px;color:var(--color-text,#e8edf2);background:transparent;border:1px solid var(--color-border,#30363d);border-radius:6px;cursor:pointer;transition:background .15s,border-color .15s}.design-button:hover{background:var(--color-bg-input,#151a21);border-color:var(--color-text-muted,#6e7681)}.design-button-primary{background:var(--color-accent,#f97316);border-color:var(--color-accent,#f97316);color:var(--color-bg,#090b0e);font-weight:700}.design-button:disabled{opacity:.4;cursor:not-allowed}.design-full{width:100%}.design-revision-button{margin-top:8px}
.design-button-rover{display:flex;align-items:center;justify-content:center;gap:8px;border-color:color-mix(in srgb,var(--color-accent,#f97316) 55%,var(--color-border,#30363d));background:color-mix(in srgb,var(--color-accent,#f97316) 9%,transparent)}
.design-studio .field{display:block;width:100%;border:1px solid var(--color-border,#30363d);background:var(--color-bg-input,#151a21);color:inherit;border-radius:6px;padding:7px 8px;font-size:12px;outline:none}.design-studio .field:focus{border-color:var(--color-accent,#f97316)}
.design-studio .tab,.design-studio .tab-active{font-size:11px;padding:6px 9px;border:0;border-radius:6px;background:transparent;color:var(--color-text-muted,#8b949e);cursor:pointer}.design-studio .tab-active{background:var(--color-bg-input,#151a21);color:var(--color-text,#e8edf2)}
.design-validation-issue{background:transparent;border:0;color:inherit;cursor:pointer;padding:4px 0;font-size:11px;text-align:left}.design-validation-issue:hover{color:#f97316}
.design-canvas{display:block;width:100%;height:100%;cursor:grab;touch-action:none}.design-canvas:active{cursor:grabbing}
.design-section{padding:16px;border-bottom:1px solid var(--color-border,#252a31)}.design-section-title{margin:0 0 12px;font-size:10px;font-weight:500;text-transform:uppercase;letter-spacing:.12em;color:var(--color-text-muted,#8b949e)}
.design-param{display:block;margin-bottom:14px}.design-param-head{display:flex;justify-content:space-between;gap:8px;margin-bottom:5px;font-size:12px}.design-param-value{color:var(--color-accent,#f97316);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.design-range{display:block;width:100%;accent-color:var(--color-accent,#f97316)}
.design-row{display:flex;justify-content:space-between;gap:12px;padding:4px 0;font-size:12px}
.pcb-binding-dot{width:7px;height:7px;border-radius:50%;background:#64748b;box-shadow:0 0 0 3px rgba(100,116,139,.12)}.pcb-import[data-bound="true"] .pcb-binding-dot{background:#22c55e;box-shadow:0 0 0 3px rgba(34,197,94,.12)}
@media(max-width:1100px){.design-workspace{grid-template-columns:190px minmax(320px,1fr) 280px}}
@media(max-width:820px){.design-workspace{grid-template-columns:170px minmax(280px,1fr)}.design-inspector{display:none}}
`;
