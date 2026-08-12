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

  return <div className="design-studio h-full flex flex-col bg-bg text-text overflow-hidden">
    <header className="design-header h-14 px-4 border-b border-border flex items-center gap-3 shrink-0">
      <div className="w-8 h-8 rounded-lg bg-accent/15 text-accent grid place-items-center text-lg">◇</div>
      <div><h1 className="text-sm font-semibold leading-tight">Design Studio</h1><p className="text-[11px] text-text-muted">Parametric CAD · B-rep · manufacturing</p></div>
      <span className="flex-1" />
      <button className="design-button" disabled={!!busy || !design} onClick={() => build(["mesh-json", "glb"])}>Build preview</button>
      <button className="design-button" disabled={!!busy || !design} onClick={() => build(["step", "stl", "3mf", "glb"])}>Export CAD</button>
      <button className="design-button design-button-primary" disabled={!!busy || !design} onClick={makePackage}>Manufacturing ZIP</button>
    </header>
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
          <MeshViewer projectId={projectId} artifactId={mesh?.id} />
          {!mesh && <div className="absolute inset-0 grid place-items-center pointer-events-none"><div className="text-center"><div className="text-4xl text-text-muted/30 mb-3">◇</div><p className="text-sm text-text-muted">Build a preview to inspect the model</p><p className="text-[11px] text-text-muted/60 mt-1">Drag to orbit · wheel to zoom</p></div></div>}
          {report && <div className="absolute left-3 bottom-3 flex gap-1.5">
            <Metric label="X" value={`${fmt(report.bounds.size[0])} mm`} /><Metric label="Y" value={`${fmt(report.bounds.size[1])} mm`} /><Metric label="Z" value={`${fmt(report.bounds.size[2])} mm`} />
          </div>}
        </div> : <textarea className="flex-1 min-h-0 resize-none bg-[#0b1016] p-5 font-mono text-xs leading-5 outline-none text-[#cbd5e1]" spellCheck={false} value={definitionText} onChange={(e) => setDefinitionText(e.target.value)} />}
      </main>

      <aside className="design-inspector">
        <Section title="Parameters">
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
    const target = canvas.current;
    if (!target || !mesh) return;
    renderMeshWebGL(target, mesh, view.current);
  }, [mesh]);
  useEffect(() => {
    draw();
    const resize = new ResizeObserver(draw);
    if (canvas.current) resize.observe(canvas.current);
    return () => resize.disconnect();
  }, [draw]);
  return <canvas ref={canvas} className="design-canvas" onPointerDown={(e)=>{view.current.drag=true;view.current.x=e.clientX;view.current.y=e.clientY;(e.target as Element).setPointerCapture(e.pointerId)}} onPointerMove={(e)=>{if(!view.current.drag)return;view.current.ry+=(e.clientX-view.current.x)*.01;view.current.rx+=(e.clientY-view.current.y)*.01;view.current.x=e.clientX;view.current.y=e.clientY;draw()}} onPointerUp={()=>view.current.drag=false} onPointerCancel={()=>view.current.drag=false} onWheel={(e)=>{e.preventDefault();view.current.zoom=Math.max(.25,Math.min(5,view.current.zoom*(e.deltaY>0?.9:1.1)));draw()}} />;
}

type MeshView = { rx: number; ry: number; zoom: number };

const vertexShaderSource = `
attribute vec3 a_position;
attribute vec3 a_normal;
uniform vec3 u_center;
uniform vec2 u_rotation;
uniform float u_scale;
uniform float u_aspect;
varying float v_light;
vec3 rotateModel(vec3 p) {
  float cy = cos(u_rotation.y), sy = sin(u_rotation.y);
  float cx = cos(u_rotation.x), sx = sin(u_rotation.x);
  vec3 y = vec3(p.x * cy + p.z * sy, p.y, -p.x * sy + p.z * cy);
  return vec3(y.x, y.y * cx - y.z * sx, y.y * sx + y.z * cx);
}
void main() {
  vec3 position = rotateModel(a_position - u_center);
  vec3 normal = normalize(rotateModel(a_normal));
  vec3 lightDirection = normalize(vec3(-0.35, 0.55, -0.75));
  v_light = 0.28 + 0.72 * max(0.0, abs(dot(normal, lightDirection)));
  gl_Position = vec4(position.x * u_scale / u_aspect, position.y * u_scale, position.z * u_scale, 1.0);
}`;

const fragmentShaderSource = `
precision mediump float;
varying float v_light;
void main() {
  vec3 blue = vec3(0.12, 0.55, 0.88);
  gl_FragColor = vec4(blue * v_light + vec3(0.025, 0.035, 0.05), 1.0);
}`;

type GLResources = {
  gl: WebGLRenderingContext;
  program: WebGLProgram;
  position: WebGLBuffer;
  normal: WebGLBuffer;
  vertexCount: number;
  center: [number, number, number];
  maxSize: number;
};

const webglResources = new WeakMap<HTMLCanvasElement, { mesh: Mesh; resources: GLResources }>();

function shader(gl: WebGLRenderingContext, type: number, source: string) {
  const output = gl.createShader(type);
  if (!output) throw new Error("WebGL shader allocation failed");
  gl.shaderSource(output, source);
  gl.compileShader(output);
  if (!gl.getShaderParameter(output, gl.COMPILE_STATUS)) {
    const message = gl.getShaderInfoLog(output) || "WebGL shader compilation failed";
    gl.deleteShader(output);
    throw new Error(message);
  }
  return output;
}

function createWebGLResources(canvas: HTMLCanvasElement, mesh: Mesh): GLResources | null {
  const gl = canvas.getContext("webgl", { alpha: false, antialias: true });
  if (!gl) return null;
  const program = gl.createProgram();
  if (!program) return null;
  const vertex = shader(gl, gl.VERTEX_SHADER, vertexShaderSource);
  const fragment = shader(gl, gl.FRAGMENT_SHADER, fragmentShaderSource);
  gl.attachShader(program, vertex); gl.attachShader(program, fragment); gl.linkProgram(program);
  gl.deleteShader(vertex); gl.deleteShader(fragment);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) return null;

  const positions: number[] = [], normals: number[] = [];
  let min = [Infinity, Infinity, Infinity], max = [-Infinity, -Infinity, -Infinity];
  for (let i = 0; i < mesh.vertices.length; i += 3) {
    for (let axis = 0; axis < 3; axis++) {
      min[axis] = Math.min(min[axis], mesh.vertices[i + axis]);
      max[axis] = Math.max(max[axis], mesh.vertices[i + axis]);
    }
  }
  for (const index of mesh.triangles) {
    const offset = index * 3;
    positions.push(mesh.vertices[offset], mesh.vertices[offset + 1], mesh.vertices[offset + 2]);
    if (mesh.normals?.length === mesh.vertices.length) {
      normals.push(mesh.normals[offset], mesh.normals[offset + 1], mesh.normals[offset + 2]);
    } else {
      normals.push(0, 0, 1);
    }
  }
  const position = gl.createBuffer(), normal = gl.createBuffer();
  if (!position || !normal) return null;
  gl.bindBuffer(gl.ARRAY_BUFFER, position); gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(positions), gl.STATIC_DRAW);
  gl.bindBuffer(gl.ARRAY_BUFFER, normal); gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(normals), gl.STATIC_DRAW);
  return {
    gl, program, position, normal, vertexCount: positions.length / 3,
    center: [(min[0] + max[0]) / 2, (min[1] + max[1]) / 2, (min[2] + max[2]) / 2],
    maxSize: Math.max(Math.hypot(max[0] - min[0], max[1] - min[1], max[2] - min[2]), 1),
  };
}

function renderMeshWebGL(canvas: HTMLCanvasElement, mesh: Mesh, view: MeshView) {
  let cached = webglResources.get(canvas);
  if (!cached || cached.mesh !== mesh) {
    if (cached) {
      cached.resources.gl.deleteBuffer(cached.resources.position);
      cached.resources.gl.deleteBuffer(cached.resources.normal);
      cached.resources.gl.deleteProgram(cached.resources.program);
    }
    const resources = createWebGLResources(canvas, mesh);
    if (!resources) return;
    cached = { mesh, resources };
    webglResources.set(canvas, cached);
  }
  const { gl, program, position, normal, vertexCount, center, maxSize } = cached.resources;
  const rect = canvas.getBoundingClientRect(), dpr = Math.min(devicePixelRatio || 1, 2);
  const width = Math.max(1, Math.round(rect.width * dpr)), height = Math.max(1, Math.round(rect.height * dpr));
  if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
  gl.viewport(0, 0, width, height);
  gl.clearColor(0.035, 0.05, 0.07, 1); gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
  gl.enable(gl.DEPTH_TEST); gl.depthFunc(gl.LEQUAL); gl.disable(gl.BLEND);
  gl.useProgram(program);
  const positionLocation = gl.getAttribLocation(program, "a_position"), normalLocation = gl.getAttribLocation(program, "a_normal");
  gl.bindBuffer(gl.ARRAY_BUFFER, position); gl.enableVertexAttribArray(positionLocation); gl.vertexAttribPointer(positionLocation, 3, gl.FLOAT, false, 0, 0);
  gl.bindBuffer(gl.ARRAY_BUFFER, normal); gl.enableVertexAttribArray(normalLocation); gl.vertexAttribPointer(normalLocation, 3, gl.FLOAT, false, 0, 0);
  gl.uniform3fv(gl.getUniformLocation(program, "u_center"), center);
  gl.uniform2f(gl.getUniformLocation(program, "u_rotation"), view.rx, view.ry);
  gl.uniform1f(gl.getUniformLocation(program, "u_scale"), 1.55 * view.zoom / maxSize);
  gl.uniform1f(gl.getUniformLocation(program, "u_aspect"), width / height);
  gl.drawArrays(gl.TRIANGLES, 0, vertexCount);
}

function Section({ title, children }: { title: string; children: any }) { return <section className="design-section"><h2 className="design-section-title">{title}</h2>{children}</section> }
function Row({ label, value }: { label: string; value: string }) { return <div className="design-row"><span className="text-text-muted">{label}</span><span className="font-mono">{value}</span></div> }
function Metric({ label, value }: { label: string; value: string }) { return <div className="rounded bg-black/50 backdrop-blur px-2 py-1 text-[10px]"><span className="text-text-muted mr-1">{label}</span><span className="font-mono">{value}</span></div> }

const styles = `
.design-studio,.design-studio *{box-sizing:border-box}.design-studio{width:100%;height:100%;min-height:0;background:var(--color-bg,#090b0e);color:var(--color-text,#e8edf2)}
.design-header{display:flex;align-items:center;gap:12px;height:56px;min-height:56px;padding:0 16px;border-bottom:1px solid var(--color-border,#252a31);overflow-x:auto}
.design-workspace{display:grid;grid-template-columns:220px minmax(360px,1fr) 320px;flex:1;min-width:0;min-height:0;overflow:hidden}
.design-library{min-width:0;min-height:0;padding:12px;border-right:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-main{display:flex;flex-direction:column;min-width:0;min-height:0;background:#0c1118}
.design-viewport{position:relative;flex:1;min-width:0;min-height:0;overflow:hidden;background:#091018}
.design-inspector{min-width:0;min-height:0;border-left:1px solid var(--color-border,#252a31);overflow-y:auto;background:var(--color-bg,#090b0e)}
.design-list-item{display:block;width:100%;padding:10px;text-align:left;color:inherit;background:transparent;border:1px solid transparent;border-radius:8px;cursor:pointer;transition:background .15s,border-color .15s}
.design-list-item:hover{background:var(--color-bg-input,#151a21)}.design-list-item[data-selected="true"]{background:color-mix(in srgb,var(--color-accent,#f97316) 12%,transparent);border-color:color-mix(in srgb,var(--color-accent,#f97316) 48%,transparent)}
.design-list-name{font-size:12px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.design-list-meta{margin-top:4px;font-size:10px;color:var(--color-text-muted,#8b949e)}
.design-button{flex:none;font-size:11px;line-height:1.2;padding:7px 10px;color:var(--color-text,#e8edf2);background:transparent;border:1px solid var(--color-border,#30363d);border-radius:6px;cursor:pointer;transition:background .15s,border-color .15s}.design-button:hover{background:var(--color-bg-input,#151a21);border-color:var(--color-text-muted,#6e7681)}.design-button-primary{background:var(--color-accent,#f97316);border-color:var(--color-accent,#f97316);color:var(--color-bg,#090b0e);font-weight:700}.design-button:disabled{opacity:.4;cursor:not-allowed}.design-full{width:100%}.design-revision-button{margin-top:8px}
.design-studio .field{display:block;width:100%;border:1px solid var(--color-border,#30363d);background:var(--color-bg-input,#151a21);color:inherit;border-radius:6px;padding:7px 8px;font-size:12px;outline:none}.design-studio .field:focus{border-color:var(--color-accent,#f97316)}
.design-studio .tab,.design-studio .tab-active{font-size:11px;padding:6px 9px;border:0;border-radius:6px;background:transparent;color:var(--color-text-muted,#8b949e);cursor:pointer}.design-studio .tab-active{background:var(--color-bg-input,#151a21);color:var(--color-text,#e8edf2)}
.design-canvas{display:block;width:100%;height:100%;cursor:grab;touch-action:none}.design-canvas:active{cursor:grabbing}
.design-section{padding:16px;border-bottom:1px solid var(--color-border,#252a31)}.design-section-title{margin:0 0 12px;font-size:10px;font-weight:500;text-transform:uppercase;letter-spacing:.12em;color:var(--color-text-muted,#8b949e)}
.design-param{display:block;margin-bottom:14px}.design-param-head{display:flex;justify-content:space-between;gap:8px;margin-bottom:5px;font-size:12px}.design-param-value{color:var(--color-accent,#f97316);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.design-range{display:block;width:100%;accent-color:var(--color-accent,#f97316)}
.design-row{display:flex;justify-content:space-between;gap:12px;padding:4px 0;font-size:12px}
@media(max-width:1100px){.design-workspace{grid-template-columns:190px minmax(320px,1fr) 280px}}
@media(max-width:820px){.design-workspace{grid-template-columns:170px minmax(280px,1fr)}.design-inspector{display:none}}
`;
