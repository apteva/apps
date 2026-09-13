import { useCallback, useEffect, useRef, useState } from "react";
import {
  renderer,
  pick,
  type Camera,
  type Highlight,
  type Model,
  type Triangle,
  type V,
} from "./viewport";

type Props = { projectId: string; apiBase?: string; uiBase?: string };
type Selection = {
  id: string;
  revision_id: number;
  selection: { node_id: string; kind: "face" | "vertex"; ids: string[] };
};
const empty: Model = { nodes: [] };
const colors = [
  "#bd3646",
  "#238b82",
  "#4478ba",
  "#dfae52",
  "#cfdae6",
  "#252e3c",
];
export default function StudioPanel({
  projectId,
  apiBase = "/api/apps/3d-studio/api",
  uiBase = "/api/apps/3d-studio/ui",
}: Props) {
  const [assets, setAssets] = useState<any[]>([]),
    [current, setCurrent] = useState<any>(null),
    [candidate, setCandidate] = useState<any>(null),
    [model, setModel] = useState<Model>(empty),
    [triangles, setTriangles] = useState<Triangle[]>([]),
    [nodeID, setNodeID] = useState(""),
    [selection, setSelection] = useState<Selection | null>(null),
    [kind, setKind] = useState<"face" | "vertex">("face"),
    [busy, setBusy] = useState(""),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [history, setHistory] = useState<any[]>([]),
    [showHistory, setShowHistory] = useState(false),
    [wire, setWire] = useState(false),
    [operation, setOperation] = useState("transform"),
    [delta, setDelta] = useState<V>([0, 0.1, 0]),
    [scale, setScale] = useState(1),
    [radius, setRadius] = useState(0),
    [distance, setDistance] = useState(0.2),
    [inset, setInset] = useState(0.2),
    [axis, setAxis] = useState("x"),
    [engineStatus, setEngineStatus] = useState("Connecting preview engine"),
    [name, setName] = useState("Untitled model"),
    [viewName, setViewName] = useState("perspective");
  const canvas = useRef<HTMLCanvasElement>(null),
    camera = useRef<Camera>({ yaw: 0.7, pitch: 0.4, zoom: 1 }),
    draw = useRef<(() => void) | null>(null),
    latest = useRef<any>(null),
    worker = useRef<Worker | null>(null),
    pending = useRef(
      new Map<
        string,
        { resolve: (v: any) => void; reject: (e: Error) => void; timer: any }
      >(),
    ),
    loadSequence = useRef(0),
    drag = useRef<any>(null);
  const request = useCallback(
    async (tool: string, args: any = {}) => {
      const response = await fetch(
        `${apiBase}/tools/${tool}?project_id=${encodeURIComponent(projectId)}`,
        {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(args),
        },
      );
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || "Request failed");
      return body;
    },
    [apiBase, projectId],
  );
  const artifactURL = (path: string) =>
    `${apiBase}${path.replace(/^\/api/, "")}?project_id=${encodeURIComponent(projectId)}`;
  const update = (body: any) => {
    setCurrent(body);
    setModel(body.revision.document);
    setTriangles(body.render_mesh || []);
    setCandidate(null);
    setSelection(null);
    setNodeID((old) =>
      body.revision.document.nodes.some((n: any) => n.id === old)
        ? old
        : body.revision.document.nodes[0]?.id || "",
    );
  };
  const refreshList = useCallback(async () => {
    const body = await request("assets_list");
    setAssets(body.assets);
    return body.assets;
  }, [request]);
  async function load(id: number) {
    const seq = ++loadSequence.current;
    setBusy("Opening model");
    setError("");
    try {
      const body = await request("assets_get", { asset_id: id });
      if (seq === loadSequence.current) {
        update(body);
        setShowHistory(false);
        setNotice("");
      }
    } catch (e) {
      if (seq === loadSequence.current) setError((e as Error).message);
    } finally {
      if (seq === loadSequence.current) setBusy("");
    }
  }
  useEffect(() => {
    let alive = true;
    setCurrent(null);
    setModel(empty);
    setTriangles([]);
    setSelection(null);
    setCandidate(null);
    refreshList()
      .then((list) => {
        if (alive && list.length) load(list[0].id);
      })
      .catch((e) => alive && setError(e.message));
    return () => {
      alive = false;
      loadSequence.current++;
    };
  }, [refreshList]);
  useEffect(() => {
    let w: Worker;
    try {
      w = new Worker(`${uiBase}/engine.worker.js`);
      worker.current = w;
      w.onmessage = ({ data }) => {
        if (data.ready) {
          setEngineStatus("Go / WebAssembly ready");
          return;
        }
        if (data.error && !data.id) {
          setEngineStatus("Server preview available");
          return;
        }
        const task = pending.current.get(data.id);
        if (!task) return;
        clearTimeout(task.timer);
        pending.current.delete(data.id);
        data.error ? task.reject(new Error(data.error)) : task.resolve(data);
      };
      w.onerror = () => setEngineStatus("Server preview available");
      setEngineStatus("Loading Go preview engine");
    } catch {
      setEngineStatus("Server preview available");
    }
    return () => {
      w?.terminate();
      worker.current = null;
      for (const task of pending.current.values()) {
        clearTimeout(task.timer);
        task.reject(new Error("Preview closed"));
      }
      pending.current.clear();
    };
  }, [uiBase]);
  const evaluateLocal = (req: any) =>
    new Promise<any>((resolve, reject) => {
      if (!worker.current) {
        reject(new Error("No local worker"));
        return;
      }
      const id = crypto.randomUUID();
      const timer = setTimeout(() => {
        pending.current.delete(id);
        reject(new Error("Local preview timed out"));
      }, 5000);
      pending.current.set(id, { resolve, reject, timer });
      worker.current.postMessage({ id, request: req });
    });
  const highlighted: Highlight = selection
    ? {
        node: selection.selection.node_id,
        kind: selection.selection.kind,
        ids: selection.selection.ids,
      }
    : null;
  latest.current = { model, triangles, nodeID, highlighted, wire, kind };
  useEffect(() => {
    if (!canvas.current) return;
    let viewport: ReturnType<typeof renderer>;
    try {
      viewport = renderer(canvas.current);
    } catch (e) {
      setError((e as Error).message);
      return;
    }
    const repaint = () => {
      const v = latest.current;
      viewport.draw(
        v.model,
        v.triangles,
        camera.current,
        v.highlighted,
        v.nodeID,
        v.wire,
        v.kind === "vertex",
      );
    };
    draw.current = repaint;
    const observer = new ResizeObserver(repaint);
    observer.observe(canvas.current);
    repaint();
    return () => {
      observer.disconnect();
      viewport.dispose();
      draw.current = null;
    };
  }, []);
  useEffect(() => {
    draw.current?.();
  }, [model, triangles, nodeID, selection, wire, kind]);
  const locked = !!busy || !!candidate;
  const report = candidate?.result.report || current?.report;
  const selectedNode = model.nodes.find((n) => n.id === nodeID);
  async function run(label: string, fn: () => Promise<void>) {
    setBusy(label);
    setError("");
    setNotice("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  async function create(template: string) {
    await run("Creating model", async () => {
      const body = await request("assets_create", {
        name: template === "car" ? "Little roadster" : name,
        template,
      });
      update(body);
      await refreshList();
    });
  }
  async function selectGeometry(query: any) {
    if (locked || !current) return;
    await run("Selecting", async () => {
      const s = await request("mesh_select", {
        asset_id: current.asset.id,
        revision_id: current.revision.id,
        name: "Editor selection",
        query,
      });
      setSelection(s);
      setNodeID(s.selection.node_id);
      setKind(s.selection.kind);
    });
  }
  async function expand() {
    if (!selection) return;
    await run("Growing selection", async () => {
      setSelection(
        await request("selection_expand", {
          asset_id: current.asset.id,
          selection_id: selection.id,
          steps: 1,
          name: "Editor selection",
        }),
      );
    });
  }
  function command() {
    const selected = selection ? { selection: "active" } : {};
    switch (operation) {
      case "transform":
        return {
          op: "transform",
          node_id: nodeID,
          ...selected,
          translation: delta,
          scale: [scale, scale, scale],
          radius,
          falloff: "smooth",
          connected_only: true,
        };
      case "extrude":
        return {
          op: "extrude",
          node_id: nodeID,
          ...selected,
          direction: delta,
          distance,
          result_selection: "edited",
        };
      case "inset":
        return {
          op: "inset",
          node_id: nodeID,
          ...selected,
          amount: inset,
          result_selection: "edited",
        };
      case "flatten":
        return {
          op: "flatten",
          node_id: nodeID,
          ...selected,
          axis,
          offset: distance,
        };
      default:
        return {};
    }
  }
  async function preview(commands: any[]) {
    if (!current || locked) return;
    await run("Evaluating edit", async () => {
      try {
        const local = await evaluateLocal({
          document: current.revision.document,
          commands,
          selections: selection ? { active: selection.selection } : {},
        });
        setModel(local.result.document);
        setTriangles(local.render_mesh);
        setEngineStatus("Go / WebAssembly");
      } catch {
        setEngineStatus("Go / server preview");
      }
      try {
        const result = await request("mesh_edit", {
          asset_id: current.asset.id,
          expected_revision_id: current.revision.id,
          commands,
          selections: selection ? { active: selection.id } : {},
          mode: "preview",
          note: "Editor modeling edit",
        });
        setCandidate(result);
        setModel(result.result.document);
        setTriangles(result.render_mesh);
        setSelection(null);
      } catch (e) {
        setModel(current.revision.document);
        setTriangles(current.render_mesh);
        throw e;
      }
    });
  }
  async function save() {
    if (!candidate) return;
    await run("Saving revision", async () => {
      const result = await request("mesh_edit_commit", {
        asset_id: current.asset.id,
        candidate_id: candidate.id,
        request_key: `editor-${candidate.id}`,
        note: "Editor: " + candidate.result.changes.join(", "),
      });
      update(result);
      setNotice("Revision saved");
      await refreshList();
    });
  }
  async function discard() {
    if (!candidate) return;
    await run("Discarding preview", async () => {
      await request("mesh_edit_discard", {
        asset_id: current.asset.id,
        candidate_id: candidate.id,
      });
      setCandidate(null);
      setModel(current.revision.document);
      setTriangles(current.render_mesh);
      setSelection(null);
    });
  }
  async function exportGLB() {
    await run("Exporting GLB", async () => {
      const body = await request("assets_export", {
        asset_id: current.asset.id,
        revision_id: current.revision.id,
        format: "glb",
      });
      const a = document.createElement("a");
      a.href = artifactURL(body.artifact.url);
      a.download = `${current.asset.name}.glb`;
      a.click();
      setNotice("GLB exported with separate objects and materials");
    });
  }
  async function openHistory() {
    if (!current) return;
    await run("Loading history", async () => {
      const body = await request("revisions_list", {
        asset_id: current.asset.id,
      });
      setHistory(body.revisions);
      setShowHistory(!showHistory);
    });
  }
  async function restore(id: number) {
    await run("Restoring revision", async () => {
      const result = await request("revisions_restore", {
        asset_id: current.asset.id,
        revision_id: id,
        expected_revision_id: current.revision.id,
        request_key: crypto.randomUUID(),
        note: `Restored revision ${id}`,
      });
      update(result);
      setShowHistory(false);
      await refreshList();
    });
  }
  function view(value: string) {
    setViewName(value);
    const pairs: Record<string, [number, number]> = {
      perspective: [0.7, 0.4],
      front: [0, 0],
      side: [Math.PI / 2, 0],
      top: [0, Math.PI / 2],
    };
    [camera.current.yaw, camera.current.pitch] = pairs[value];
    draw.current?.();
  }
  function pointerDown(e: any) {
    if (e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = {
      x: e.clientX,
      y: e.clientY,
      startX: e.clientX,
      startY: e.clientY,
      moved: false,
    };
  }
  function pointerMove(e: any) {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x,
      dy = e.clientY - d.y;
    if (Math.hypot(e.clientX - d.startX, e.clientY - d.startY) > 4)
      d.moved = true;
    if (d.moved) {
      camera.current.yaw += dx * 0.008;
      camera.current.pitch = Math.max(
        -1.55,
        Math.min(1.55, camera.current.pitch + dy * 0.008),
      );
      setViewName("orbit");
      draw.current?.();
    }
    d.x = e.clientX;
    d.y = e.clientY;
  }
  function pointerUp(e: any) {
    const d = drag.current;
    drag.current = null;
    if (!d || d.moved || locked || !canvas.current) return;
    const rect = canvas.current.getBoundingClientRect();
    const p = pick(
      model,
      triangles,
      camera.current,
      rect.width,
      rect.height,
      e.clientX - rect.left,
      e.clientY - rect.top,
      kind,
      nodeID,
    );
    if (p) {
      const ids =
        e.shiftKey &&
        selection?.selection.node_id === p.node &&
        selection.selection.kind === p.kind
          ? Array.from(new Set([...selection.selection.ids, p.id]))
          : [p.id];
      selectGeometry({ node_id: p.node, kind: p.kind, ids });
    }
  }
  const input = (
    label: string,
    value: number,
    set: (v: number) => void,
    step = 0.1,
  ) => (
    <label className="s3-field">
      <span>{label}</span>
      <input
        type="number"
        step={step}
        value={value}
        disabled={locked}
        onChange={(e) => set(Number(e.target.value))}
      />
    </label>
  );
  return (
    <div className="s3">
      <style>{styles}</style>
      <header className="s3-header">
        <div className="s3-brand">
          <span className="s3-logo" role="img" aria-label="3D Studio" style={{ maskImage: `url(${uiBase}/icon.svg)`, WebkitMaskImage: `url(${uiBase}/icon.svg)` }} />
          <div>
            3D STUDIO<small>Game asset workshop</small>
          </div>
        </div>
        <div className="s3-document">
          {current?.asset.name || "Your next world starts here"}
          {current && (
            <span>
              r{current.revision.id}
              {candidate ? " · Unsaved preview" : ""}
            </span>
          )}
        </div>
        <div className="s3-actions">
          <button disabled={!current || locked} onClick={openHistory}>
            History
          </button>
          <button disabled={!current || locked} onClick={exportGLB}>
            Export GLB ↗
          </button>
          <button
            className="s3-primary"
            disabled={!candidate || !!busy}
            onClick={save}
          >
            Save revision
          </button>
        </div>
      </header>
      {(error || notice) && (
        <div
          role={error ? "alert" : "status"}
          className={error ? "s3-message s3-error" : "s3-message"}
        >
          {error || notice}
          <button
            onClick={() => {
              setError("");
              setNotice("");
            }}
          >
            ×
          </button>
        </div>
      )}
      <div className="s3-workspace">
        <aside className="s3-left">
          <section>
            <div className="s3-label">
              ASSET LIBRARY <span>{assets.length}</span>
            </div>
            <select
              aria-label="Asset"
              disabled={locked}
              value={current?.asset.id || ""}
              onChange={(e) => load(Number(e.target.value))}
            >
              <option value="" disabled>
                Choose a model
              </option>
              {assets.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
            <div className="s3-new">
              <input
                aria-label="New model name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={locked}
              />
              <button disabled={locked} onClick={() => create("empty")}>
                ＋
              </button>
            </div>
            <button
              className="s3-example"
              disabled={locked}
              onClick={() => create("car")}
            >
              <span>▰</span> Little roadster{" "}
              <small>Start from an editable car ↗</small>
            </button>
          </section>
          <section className="s3-scene">
            <div className="s3-label">
              SCENE <span>{model.nodes.length} objects</span>
            </div>
            {model.nodes.map((n) => (
              <button
                key={n.id}
                className={`s3-node ${nodeID === n.id ? "active" : ""}`}
                disabled={locked}
                onClick={() => {
                  setNodeID(n.id);
                  setSelection(null);
                }}
              >
                <span
                  style={{
                    color: `rgb(${n.color.map((c) => Math.round(Math.pow(c, 1 / 2.2) * 255)).join(",")})`,
                  }}
                >
                  ◇
                </span>
                <span>{n.name}</span>
                <small>{n.mesh.faces.length}</small>
              </button>
            ))}
            {!model.nodes.length && (
              <p className="s3-muted">Add a mesh to begin.</p>
            )}
          </section>
          <section>
            <div className="s3-label">ADD MESH</div>
            <div className="s3-two">
              <button
                disabled={!current || locked}
                onClick={() =>
                  preview([
                    {
                      op: "primitive.add",
                      node_id: `box_${Date.now()}`,
                      shape: "box",
                      size: [1, 1, 1],
                    },
                  ])
                }
              >
                ＋ Box
              </button>
              <button
                disabled={!current || locked}
                onClick={() =>
                  preview([
                    {
                      op: "primitive.add",
                      node_id: `wheel_${Date.now()}`,
                      shape: "cylinder",
                      segments: 12,
                      size: [0.7, 0.2, 0.7],
                    },
                  ])
                }
              >
                ＋ Cylinder
              </button>
            </div>
          </section>
          <div className="s3-engine">
            <span /> {engineStatus}
            <small>Native polygon engine · meters</small>
          </div>
        </aside>
        <main className="s3-center">
          <div className="s3-toolbar">
            <div className="s3-tabs">
              {(["face", "vertex"] as const).map((k) => (
                <button
                  key={k}
                  disabled={locked}
                  aria-pressed={kind === k}
                  className={kind === k ? "active" : ""}
                  onClick={() => {
                    setKind(k);
                    setSelection(null);
                  }}
                >
                  {k === "face" ? "▱ Faces" : "⁙ Vertices"}
                </button>
              ))}
            </div>
            <button
              aria-pressed={wire}
              className={wire ? "active" : ""}
              onClick={() => setWire(!wire)}
            >
              Wireframe
            </button>
            <div className="s3-view-tabs">
              {["perspective", "front", "side", "top"].map((v) => (
                <button
                  key={v}
                  aria-pressed={viewName === v}
                  className={viewName === v ? "active" : ""}
                  onClick={() => view(v)}
                >
                  {v === "perspective" ? "3D" : v}
                </button>
              ))}
            </div>
          </div>
          <div className="s3-canvas-wrap">
            <canvas
              ref={canvas}
              aria-label="3D model viewport"
              onPointerDown={pointerDown}
              onPointerMove={pointerMove}
              onPointerUp={pointerUp}
              onPointerCancel={() => {
                drag.current = null;
              }}
              onLostPointerCapture={() => {
                drag.current = null;
              }}
              onWheel={(e) => {
                camera.current.zoom = Math.max(
                  0.15,
                  Math.min(
                    8,
                    camera.current.zoom * Math.exp(-e.deltaY * 0.001),
                  ),
                );
                draw.current?.();
              }}
            />
            {!current && (
              <div className="s3-empty">
                <span>◇</span>
                <h2>Shape something new.</h2>
                <p>
                  Create a model, select its geometry,
                  <br />
                  and make it your own.
                </p>
                <button
                  className="s3-primary"
                  disabled={!!busy}
                  onClick={() => create("box")}
                >
                  Create your first mesh
                </button>
              </div>
            )}
            <div className="s3-view-label">
              {candidate ? "PREVIEW" : viewName.toUpperCase()}
              <span>Y ↑ &nbsp; meters</span>
            </div>
            <div className="s3-hint">
              Drag to orbit · Scroll to zoom · Click to select · Shift-click to
              add
            </div>
            {candidate && (
              <div className="s3-preview-banner">
                <span>
                  Preview ready{" "}
                  <small>
                    {candidate.result.changes.length} operation(s) · Review
                    before saving
                  </small>
                </span>
                <button disabled={!!busy} onClick={discard}>
                  Discard
                </button>
                <button className="s3-primary" disabled={!!busy} onClick={save}>
                  Save revision
                </button>
              </div>
            )}
            {busy && (
              <div className="s3-busy">
                <span />
                {busy}
              </div>
            )}
            {showHistory && (
              <div className="s3-history">
                <div className="s3-label">
                  REVISION HISTORY
                  <button onClick={() => setShowHistory(false)}>×</button>
                </div>
                {history.map((h) => (
                  <div key={h.id}>
                    <span>
                      r{h.id}
                      <small>{h.note || "Mesh edit"}</small>
                    </span>
                    <button
                      disabled={h.id === current.revision.id || locked}
                      onClick={() => restore(h.id)}
                    >
                      {h.id === current.revision.id ? "Current" : "Restore"}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
          <footer className="s3-status">
            <span>
              <b>{report?.vertices || 0}</b> vertices
            </span>
            <span>
              <b>{report?.faces || 0}</b> faces
            </span>
            <span>
              <b>{report?.triangles || 0}</b> triangles
            </span>
            <span className="s3-health">
              {report?.boundary_edges
                ? `${report.boundary_edges} open edges`
                : current
                  ? "● Mesh validated"
                  : "Ready to model"}
            </span>
          </footer>
        </main>
        <aside className="s3-right">
          <section>
            <div className="s3-label">INSPECTOR</div>
            <h3>{selectedNode?.name || "No object selected"}</h3>
            <div className="s3-selection">
              <span>
                {selection
                  ? `${selection.selection.ids.length} ${selection.selection.kind}${selection.selection.ids.length === 1 ? "" : "s"} selected`
                  : selectedNode
                    ? "Whole object"
                    : "Select an object to edit"}
              </span>
              {selection && (
                <button disabled={locked} onClick={() => setSelection(null)}>
                  Clear
                </button>
              )}
            </div>
            <div className="s3-two">
              <button
                disabled={!nodeID || locked}
                onClick={() => selectGeometry({ node_id: nodeID, kind })}
              >
                Select all
              </button>
              <button disabled={!selection || locked} onClick={expand}>
                Grow selection
              </button>
            </div>
            <button
              className="s3-wide"
              disabled={!nodeID || locked}
              onClick={() =>
                selectGeometry({
                  node_id: nodeID,
                  kind: "face",
                  normal: [0, 1, 0],
                  min_dot: 0.8,
                })
              }
            >
              Select upward faces ↑
            </button>
          </section>
          <section>
            <div className="s3-label">MODEL</div>
            <div className="s3-operation-grid">
              {["transform", "extrude", "inset", "flatten"].map((op) => (
                <button
                  key={op}
                  disabled={locked}
                  aria-pressed={operation === op}
                  className={operation === op ? "active" : ""}
                  onClick={() => {
                    setOperation(op);
                    if (op === "extrude") setDelta([0, 1, 0]);
                  }}
                >
                  {op}
                </button>
              ))}
            </div>
            {(operation === "transform" || operation === "extrude") && (
              <>
                <div className="s3-subtitle">
                  {operation === "transform"
                    ? "Move (meters)"
                    : "Extrusion direction"}
                </div>
                <div className="s3-vectors">
                  {["X", "Y", "Z"].map((k, i) =>
                    input(k, delta[i], (v) =>
                      setDelta(
                        (old) => old.map((x, j) => (i === j ? v : x)) as V,
                      ),
                    ),
                  )}
                </div>
              </>
            )}
            {operation === "transform" && (
              <>
                <div className="s3-two">
                  {input("Scale", scale, setScale, 0.05)}
                  {input("Falloff radius", radius, setRadius)}
                </div>
                <p className="s3-muted">
                  A radius above zero gently moves nearby connected vertices.
                </p>
              </>
            )}
            {operation === "extrude" &&
              input("Distance (m)", distance, setDistance)}
            {operation === "inset" && (
              <>
                {input("Inset fraction", inset, setInset, 0.05)}
                <p className="s3-muted">
                  Inset each selected face toward its center. Use a value
                  between 0 and 1.
                </p>
              </>
            )}
            {operation === "flatten" && (
              <>
                <label className="s3-field">
                  <span>Plane axis</span>
                  <select
                    value={axis}
                    disabled={locked}
                    onChange={(e) => setAxis(e.target.value)}
                  >
                    {["x", "y", "z"].map((a) => (
                      <option key={a}>{a}</option>
                    ))}
                  </select>
                </label>
                {input("Plane position (m)", distance, setDistance)}
              </>
            )}
            <button
              className="s3-primary s3-wide"
              disabled={
                !nodeID ||
                locked ||
                (["extrude", "inset"].includes(operation) &&
                  selection?.selection.kind !== "face")
              }
              onClick={() => preview([command()])}
            >
              Preview {operation} ↗
            </button>
            {["extrude", "inset"].includes(operation) &&
              !candidate &&
              selection?.selection.kind !== "face" && (
                <p className="s3-muted">Select faces in the viewport first.</p>
              )}
          </section>
          <section>
            <div className="s3-label">SURFACE COLOR</div>
            <div className="s3-colors">
              {colors.map((hex) => (
                <button
                  key={hex}
                  title={hex}
                  aria-label={`Set color ${hex}`}
                  style={{ background: hex }}
                  disabled={!nodeID || locked}
                  onClick={() =>
                    preview([
                      {
                        op: "node.color",
                        node_id: nodeID,
                        color: [1, 3, 5].map((i) =>
                          Math.pow(
                            parseInt(hex.slice(i, i + 2), 16) / 255,
                            2.2,
                          ),
                        ),
                      },
                    ])
                  }
                />
              ))}
            </div>
          </section>
          <section>
            <div className="s3-label">OBJECT ACTIONS</div>
            <div className="s3-two">
              <button
                disabled={!nodeID || locked}
                onClick={() =>
                  preview([
                    {
                      op: "node.duplicate",
                      node_id: nodeID,
                      target_id: `${nodeID.slice(0, 40)}_${Date.now()}`,
                      translation: [0, 0, 1.5],
                    },
                  ])
                }
              >
                Duplicate
              </button>
              <button
                disabled={!nodeID || locked}
                onClick={() =>
                  preview([
                    {
                      op: "mesh.mirror",
                      node_id: nodeID,
                      target_id: `mirror_${Date.now()}`,
                      axis: "z",
                      offset: 0,
                    },
                  ])
                }
              >
                Mirror across Z
              </button>
            </div>
            <p className="s3-muted">
              Mirror creates an editable reflected copy.
            </p>
          </section>
        </aside>
      </div>
    </div>
  );
}
const styles = `
.s3{--bg:var(--color-bg,#101216);--panel:var(--color-bg-card,#181b21);--input:var(--color-bg-input,#15181e);--line:var(--color-border,#30343e);--text:var(--color-text,#eceef2);--muted:var(--color-text-muted,#969eac);--accent:var(--color-accent,#f97316);color:var(--text);background:var(--bg);font:13px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;height:100%;min-height:620px;display:flex;flex-direction:column;overflow:hidden}.s3 *{box-sizing:border-box}.s3 button,.s3 input,.s3 select{font:inherit}.s3 button{border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--text);padding:8px 11px;cursor:pointer}.s3 button:hover:not(:disabled){border-color:var(--accent);background:color-mix(in srgb,var(--accent) 8%,var(--panel))}.s3 button:disabled{opacity:.42;cursor:default}.s3 button:focus-visible,.s3 input:focus-visible,.s3 select:focus-visible{outline:2px solid var(--accent);outline-offset:2px}.s3 .s3-primary{background:var(--accent);border-color:var(--accent);color:var(--bg);font-weight:650}.s3-header{height:64px;min-height:64px;padding:0 20px;display:flex;align-items:center;gap:20px;border-bottom:1px solid var(--line);background:var(--panel)}.s3-brand{display:flex;align-items:center;gap:10px;font-size:13px;font-weight:650;min-width:190px}.s3-logo{width:28px;height:28px;display:block;background:var(--accent);-webkit-mask-position:center;-webkit-mask-size:contain;-webkit-mask-repeat:no-repeat;mask-position:center;mask-size:contain;mask-repeat:no-repeat;flex:none}.s3-brand small{display:block;font-size:11px;font-weight:400;color:var(--muted);margin-top:1px}.s3-document{font-size:14px;flex:1}.s3-document span{font:11px ui-monospace,monospace;color:var(--muted);margin-left:10px}.s3-actions{display:flex;gap:8px}.s3-workspace{display:grid;grid-template-columns:240px minmax(360px,1fr) 280px;flex:1;min-height:0}.s3-left,.s3-right{background:var(--panel);overflow-y:auto;min-height:0}.s3-left{border-right:1px solid var(--line);display:flex;flex-direction:column}.s3-right{border-left:1px solid var(--line)}.s3 section{padding:16px;border-bottom:1px solid var(--line)}.s3-label{font-size:11px;font-weight:600;color:var(--muted);display:flex;align-items:center;justify-content:space-between;margin-bottom:10px}.s3-label span{font-weight:400}.s3 input,.s3 select{border:1px solid var(--line);border-radius:8px;background:var(--input);color:var(--text);padding:8px 10px;min-width:0;width:100%}.s3-new{display:flex;gap:7px;margin-top:8px}.s3-new button{padding:7px 11px}.s3 .s3-example{background:color-mix(in srgb,var(--accent) 8%,var(--panel));border-color:color-mix(in srgb,var(--accent) 35%,var(--line));width:100%;text-align:left;margin-top:12px;padding:11px;font-size:12px}.s3-example>span{color:var(--accent);margin-right:5px}.s3-example small{display:block;color:var(--muted);font-size:11px;margin-top:3px}.s3-scene{flex:1}.s3 .s3-node{display:flex;align-items:center;gap:8px;width:100%;border:1px solid transparent;background:transparent;text-align:left;padding:8px 7px;font-size:12px}.s3-node span:nth-child(2){overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.s3-node small{margin-left:auto;color:var(--muted);font-size:10px}.s3 .s3-node.active{background:color-mix(in srgb,var(--accent) 10%,var(--panel));border-color:color-mix(in srgb,var(--accent) 35%,var(--line))}.s3-two{display:grid;grid-template-columns:1fr 1fr;gap:7px}.s3-two button{font-size:12px;padding:8px 5px}.s3-engine{padding:13px 16px;color:var(--muted);font-size:11px}.s3-engine>span{display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--accent);margin-right:5px}.s3-engine small{display:block;padding-left:11px;margin-top:2px;font-size:10px}.s3-center{display:flex;flex-direction:column;min-width:0;min-height:0;position:relative}.s3-toolbar{height:48px;min-height:48px;padding:8px 12px;display:flex;align-items:center;gap:8px;background:var(--panel);border-bottom:1px solid var(--line)}.s3-toolbar button{font-size:11px;padding:6px 9px}.s3-tabs,.s3-view-tabs{display:flex;gap:5px}.s3-view-tabs{margin-left:auto}.s3 .active{background:color-mix(in srgb,var(--accent) 15%,var(--panel));color:var(--accent);border-color:color-mix(in srgb,var(--accent) 50%,var(--line))}.s3-canvas-wrap{flex:1;min-height:0;position:relative;overflow:hidden;background:var(--bg)}.s3 canvas{width:100%;height:100%;display:block;touch-action:none}.s3-view-label{position:absolute;left:18px;top:16px;pointer-events:none;font:11px ui-monospace,monospace;color:var(--muted)}.s3-view-label span{display:block;font-size:10px;margin-top:4px;color:var(--muted)}.s3-hint{position:absolute;bottom:14px;left:0;right:0;text-align:center;font-size:11px;color:var(--muted);pointer-events:none}.s3-status{height:34px;min-height:34px;display:flex;align-items:center;gap:16px;padding:0 14px;background:var(--panel);border-top:1px solid var(--line);font-size:11px;color:var(--muted)}.s3-status b{color:var(--text);font-weight:500;font-family:ui-monospace,monospace}.s3-health{margin-left:auto;color:var(--accent)}.s3 h3{font-size:17px;font-weight:600;margin:0 0 8px}.s3-selection{font-size:12px;color:var(--muted);display:flex;justify-content:space-between;align-items:center;min-height:28px;margin-bottom:8px}.s3-selection button{font-size:11px;padding:3px 7px}.s3-wide{width:100%;margin-top:10px}.s3-operation-grid{display:grid;grid-template-columns:1fr 1fr;gap:7px}.s3-operation-grid button{font-size:12px}.s3-subtitle{font-size:11px;color:var(--muted);margin:15px 0 7px}.s3-vectors{display:grid;grid-template-columns:repeat(3,1fr);gap:7px}.s3-field{display:block;margin-top:9px}.s3-field>span{display:block;font-size:11px;color:var(--muted);margin-bottom:4px}.s3-field input{font:12px ui-monospace,monospace;padding:8px}.s3-muted{font-size:11px;line-height:1.5;color:var(--muted);margin:8px 0 0}.s3-colors{display:flex;gap:8px}.s3 .s3-colors button{height:26px;width:26px;border-radius:50%;padding:0;border:2px solid var(--line)}.s3-preview-banner{position:absolute;left:16px;right:16px;bottom:42px;background:var(--panel);border:1px solid var(--accent);border-radius:8px;padding:11px;display:flex;align-items:center;gap:8px}.s3-preview-banner>span{flex:1;font-size:12px;color:var(--text)}.s3-preview-banner small{display:block;font-size:11px;color:var(--muted)}.s3-busy{position:absolute;top:14px;right:14px;border:1px solid var(--line);background:var(--panel);padding:7px 11px;border-radius:999px;font-size:11px;color:var(--text)}.s3-busy span{display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--accent);margin-right:6px}.s3-message{padding:9px 16px;color:var(--text);background:color-mix(in srgb,var(--accent) 12%,var(--panel));font-size:12px;display:flex;align-items:center;justify-content:space-between}.s3-error{background:color-mix(in srgb,#ef4444 12%,var(--panel));color:var(--text)}.s3-empty{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;background:color-mix(in srgb,var(--bg) 92%,transparent)}.s3-empty>span{color:var(--accent);font-size:52px;line-height:1}.s3-empty h2{font-size:23px;font-weight:600;margin:14px 0 0}.s3-empty p{color:var(--muted);font-size:13px;text-align:center;margin:9px 0 18px}.s3-history{position:absolute;right:12px;top:12px;max-width:380px;width:90%;max-height:80%;overflow:auto;background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:13px}.s3-history>div:not(:first-child){display:flex;gap:10px;align-items:center;justify-content:space-between;border-top:1px solid var(--line);padding:10px 0;font:12px ui-monospace,monospace}.s3-history small{display:block;font:11px/1.5 sans-serif;color:var(--muted);max-width:230px;overflow-wrap:anywhere;margin-top:3px}@media(max-width:1100px){.s3-workspace{grid-template-columns:210px minmax(260px,1fr) 250px}.s3-header{padding:0 14px;gap:12px}.s3-brand{min-width:150px}}@media(max-width:820px){.s3-workspace{grid-template-columns:minmax(240px,1fr) 230px}.s3-left{display:none}.s3-brand small{display:none}.s3-brand{min-width:0}.s3-document{font-size:12px}.s3-actions button{font-size:11px;padding:7px}.s3-status{gap:8px}.s3-health{display:none}}
`;
