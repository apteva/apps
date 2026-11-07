import { useEffect, useRef, useState } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";

type MeshData = {
  id?: string;
  instance_id?: string;
  part_id?: string;
  name?: string;
  color?: string;
  visible?: boolean;
  vertices: number[];
  triangles: number[];
  normals?: number[];
  faceGroups?: { start: number; count: number; faceId: number }[];
  parts?: MeshData[];
  keepouts?: MeshData[];
};
type Pick = {
  visual: string;
  instance: string;
  face?: number;
  point: number[];
};
type Props = {
  projectId: string;
  artifactId?: number;
  designId?: number;
  revisionId?: number;
  focus?: string[];
  issue?: any;
  onSelect?: (id: string) => void;
  onCapture?: (data: string) => void;
};
const endpoint = (p: string, project: string) =>
  `/api/apps/design/api${p}${p.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(project)}`;

export default function AssemblyViewer({
  projectId,
  artifactId,
  designId,
  revisionId,
  focus,
  issue,
  onSelect,
  onCapture,
}: Props) {
  const host = useRef<HTMLDivElement>(null),
    state = useRef<any>(null);
  const [parts, setParts] = useState<MeshData[]>([]),
    [selected, setSelected] = useState(""),
    [hidden, setHidden] = useState<Set<string>>(new Set()),
    [alpha, setAlpha] = useState(0.35),
    [transparent, setTransparent] = useState(false),
    [showKeepouts, setShowKeepouts] = useState(true),
    [axis, setAxis] = useState(""),
    [section, setSection] = useState(0),
    [range, setRange] = useState<[number, number]>([-100, 100]),
    [measuring, setMeasuring] = useState(false),
    [measureMode, setMeasureMode] = useState("point"),
    [picks, setPicks] = useState<Pick[]>([]),
    [measurement, setMeasurement] = useState(""),
    [error, setError] = useState("");
  const pickMode = useRef({ measuring, measureMode });
  pickMode.current = { measuring, measureMode };
  const choose = (id: string, visual?: string) => {
    setSelected(visual || id);
    onSelect?.(id);
  };
  useEffect(() => {
    if (!artifactId) {
      setParts([]);
      return;
    }
    let cancelled = false,
      dispose: (() => void) | undefined;
    setError("");
    setPicks([]);
    setMeasurement("");
    setHidden(new Set());
    fetch(endpoint(`/artifacts/${artifactId}/content`, projectId), {
      credentials: "same-origin",
    })
      .then((r) => {
        if (!r.ok) throw new Error("Unable to load assembly preview");
        return r.json();
      })
      .then((data: MeshData) => {
        if (cancelled || !host.current) return;
        const renderer = new THREE.WebGLRenderer({
          antialias: true,
          alpha: true,
          preserveDrawingBuffer: true,
        });
        renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
        renderer.localClippingEnabled = true;
        const el = renderer.domElement;
        el.className = "design-canvas";
        host.current.appendChild(el);
        const scene = new THREE.Scene(),
          camera = new THREE.PerspectiveCamera(35, 1, 0.01, 100000),
          controls = new OrbitControls(camera, el);
        controls.enableDamping = true;
        scene.add(new THREE.HemisphereLight(0xffffff, 0x64748b, 2));
        const light = new THREE.DirectionalLight(0xffffff, 2);
        light.position.set(1, -2, 4);
        scene.add(light);
        const root = new THREE.Group();
        scene.add(root);
        const objects: THREE.Mesh[] = [];
        const sources = data.parts?.length
          ? data.parts
          : [{ ...data, id: "output", name: "Output" }];
        for (const [index, part] of [
          ...sources,
          ...(data.keepouts || []),
        ].entries()) {
          const geometry = new THREE.BufferGeometry();
          geometry.setAttribute(
            "position",
            new THREE.Float32BufferAttribute(part.vertices, 3),
          );
          geometry.setIndex(part.triangles);
          if (part.normals?.length === part.vertices.length)
            geometry.setAttribute(
              "normal",
              new THREE.Float32BufferAttribute(part.normals, 3),
            );
          else geometry.computeVertexNormals();
          const isKeepout = index >= sources.length;
          const material = new THREE.MeshStandardMaterial({
            color: part.color || "#64748b",
            side: THREE.DoubleSide,
            roughness: 0.6,
            transparent: isKeepout,
            opacity: isKeepout ? 0.18 : 1,
            depthWrite: !isKeepout,
          });
          const object = new THREE.Mesh(geometry, material);
          object.userData = {
            part,
            id: part.id || `part_${index}`,
            instance: part.instance_id || part.id || "output",
            keepout: isKeepout,
          };
          object.visible = part.visible !== false;
          root.add(object);
          objects.push(object);
        }
        const box = new THREE.Box3().setFromObject(root),
          center = box.getCenter(new THREE.Vector3()),
          size = box.getSize(new THREE.Vector3()),
          diagonal = Math.max(size.length(), 1);
        controls.target.copy(center);
        camera.up.set(0, 0, 1);
        camera.position
          .copy(center)
          .add(new THREE.Vector3(1, -1, 1).multiplyScalar(diagonal * 1.6));
        camera.near = Math.max(diagonal / 10000, 0.001);
        camera.far = diagonal * 100;
        camera.updateProjectionMatrix();
        controls.update();
        const helpers = new THREE.Group();
        scene.add(helpers);
        const clip = new THREE.Plane();
        state.current = {
          renderer,
          scene,
          camera,
          controls,
          objects,
          box,
          center,
          diagonal,
          clip,
          helpers,
        };
        setHidden(
          new Set(
            sources.filter((p) => p.visible === false).map((p) => p.id || ""),
          ),
        );
        setParts(sources);
        setRange([box.min.z, box.max.z]);
        setSection(center.z);
        function resize() {
          if (!host.current) return;
          const { width, height } = host.current.getBoundingClientRect();
          renderer.setSize(width, Math.max(height, 1), false);
          camera.aspect = width / Math.max(height, 1);
          camera.updateProjectionMatrix();
        }
        resize();
        const observer = new ResizeObserver(resize);
        observer.observe(host.current);
        const raycaster = new THREE.Raycaster();
        let down = [0, 0];
        function pointerDown(e: PointerEvent) {
          down = [e.clientX, e.clientY];
        }
        function click(e: PointerEvent) {
          if (Math.hypot(e.clientX - down[0], e.clientY - down[1]) > 4) return;
          const rect = el.getBoundingClientRect();
          raycaster.setFromCamera(
            new THREE.Vector2(
              ((e.clientX - rect.left) / rect.width) * 2 - 1,
              (-(e.clientY - rect.top) / rect.height) * 2 + 1,
            ),
            camera,
          );
          const hits = raycaster
            .intersectObjects(
              objects.filter((o) => o.visible && !o.userData.keepout),
            )
            .filter(
              (hit) =>
                !renderer.clippingPlanes.some(
                  (p) => p.distanceToPoint(hit.point) < 0,
                ),
            );
          if (!hits.length) return;
          const hit = hits[0],
            o = hit.object as THREE.Mesh;
          choose(o.userData.instance, o.userData.id);
          if (pickMode.current.measuring) {
            const source = o.userData.part as MeshData;
            const group = source.faceGroups?.find(
              (g) =>
                (hit.faceIndex ?? 0) * 3 >= g.start &&
                (hit.faceIndex ?? 0) * 3 < g.start + g.count,
            );
            const face =
              pickMode.current.measureMode === "face"
                ? group?.faceId
                : undefined;
            if (pickMode.current.measureMode === "face" && face === undefined) {
              setError(
                "Exact face selection is unavailable for this nested preview; use instance distance or point measurement.",
              );
              return;
            }
            const p = {
              visual: o.userData.id,
              instance: o.userData.instance,
              point: hit.point.toArray(),
              face,
            };
            setPicks((old) => (old.length >= 2 ? [p] : [...old, p]));
          }
        }
        el.addEventListener("pointerdown", pointerDown);
        el.addEventListener("pointerup", click);
        let frame = 0;
        function animate() {
          frame = requestAnimationFrame(animate);
          controls.update();
          renderer.render(scene, camera);
        }
        animate();
        dispose = () => {
          cancelAnimationFrame(frame);
          observer.disconnect();
          el.removeEventListener("pointerdown", pointerDown);
          el.removeEventListener("pointerup", click);
          controls.dispose();
          for (const o of objects) {
            o.geometry.dispose();
            (o.material as THREE.Material).dispose();
          }
          renderer.dispose();
          el.remove();
          if (state.current?.renderer === renderer) state.current = null;
        };
      })
      .catch((e) => setError(e.message));
    return () => {
      cancelled = true;
      dispose?.();
    };
  }, [artifactId, projectId]);
  useEffect(() => {
    const s = state.current;
    if (!s) return;
    for (const o of s.objects) {
      o.visible = o.userData.keepout
        ? showKeepouts
        : !hidden.has(o.userData.id) && !hidden.has(o.userData.instance);
      const m = o.material as THREE.MeshStandardMaterial;
      const isSelected =
        o.userData.instance === selected || o.userData.id === selected;
      m.emissive.set(isSelected ? "#5f3200" : "#000000");
      m.transparent = o.userData.keepout || transparent;
      m.opacity = o.userData.keepout ? 0.18 : transparent ? alpha : 1;
      m.depthWrite = !m.transparent;
    }
    if (axis) {
      const index = Number(axis);
      s.clip.normal.set(
        index === 0 ? 1 : 0,
        index === 1 ? 1 : 0,
        index === 2 ? 1 : 0,
      );
      s.clip.constant = -section;
      s.renderer.clippingPlanes = [s.clip];
    } else s.renderer.clippingPlanes = [];
  }, [
    parts,
    hidden,
    selected,
    transparent,
    alpha,
    showKeepouts,
    axis,
    section,
  ]);
  function fit(ids?: string[]) {
    const s = state.current;
    if (!s) return;
    const box = new THREE.Box3();
    for (const o of s.objects)
      if (
        !o.userData.keepout &&
        (!ids?.length ||
          ids.includes(o.userData.instance) ||
          ids.includes(o.userData.id))
      )
        box.expandByObject(o);
    if (box.isEmpty()) return;
    const center = box.getCenter(new THREE.Vector3()),
      d = Math.max(box.getSize(new THREE.Vector3()).length(), 1),
      direction = s.camera.position.clone().sub(s.controls.target).normalize();
    s.controls.target.copy(center);
    s.camera.position.copy(center).addScaledVector(direction, d * 1.6);
    s.controls.update();
  }
  useEffect(() => {
    if (focus?.length) {
      setHidden(new Set());
      setSelected(focus[0]);
      fit(focus);
    }
    const s = state.current;
    if (!s) return;
    s.helpers.clear();
    const pairs = issue?.details?.pairs || [issue?.details];
    for (const p of pairs) {
      if (!p?.closest_points) continue;
      const pts = p.closest_points.map(
        (v: number[]) => new THREE.Vector3(...v),
      );
      const g = new THREE.BufferGeometry().setFromPoints(pts);
      s.helpers.add(
        new THREE.Line(
          g,
          new THREE.LineBasicMaterial({ color: "#ef4444", depthTest: false }),
        ),
      );
      for (const v of pts) {
        const marker = new THREE.Mesh(
          new THREE.SphereGeometry(s.diagonal / 200, 10, 8),
          new THREE.MeshBasicMaterial({ color: "#ef4444", depthTest: false }),
        );
        marker.position.copy(v);
        s.helpers.add(marker);
      }
    }
    for (const r of [
      ...(issue?.details?.regions || []),
      ...(issue?.details?.feature_regions || []),
      ...(issue?.details?.overhang_regions || []),
    ].slice(0, 512)) {
      const marker = new THREE.Mesh(
        new THREE.SphereGeometry(s.diagonal / 150, 8, 6),
        new THREE.MeshBasicMaterial({ color: "#ef4444", depthTest: false }),
      );
      marker.position.fromArray(r.point);
      s.helpers.add(marker);
    }
  }, [focus, issue, parts]);
  useEffect(() => {
    if (picks.length !== 2) return;
    if (measureMode === "point") {
      const d = Math.hypot(
        ...picks[0].point.map((v, k) => v - picks[1].point[k]),
      );
      setMeasurement(`Preview mesh points: ${d.toFixed(3)} mm (approximate)`);
      return;
    }
    if (!designId || !revisionId) return;
    setMeasurement("Measuring exact CAD…");
    let cancelled = false;
    const q: any = {
      a: picks[0].instance,
      b: picks[1].instance,
      visual_a: picks[0].visual,
      visual_b: picks[1].visual,
    };
    if (measureMode === "face") {
      q.face_a = picks[0].face;
      q.face_b = picks[1].face;
    }
    fetch(endpoint("/measure", projectId), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        design_id: designId,
        revision_id: revisionId,
        query: q,
      }),
    })
      .then((r) => r.json())
      .then((b) => {
        if (!cancelled)
          setMeasurement(
            b.error || b.measurement?.message || "No measurement result",
          );
      })
      .catch((e) => {
        if (!cancelled) setMeasurement(e.message);
      });
    return () => {
      cancelled = true;
    };
  }, [picks, measureMode, designId, revisionId, projectId]);
  function standard(view: string) {
    const s = state.current;
    if (!s) return;
    const directions: any = {
      front: [0, -1, 0],
      back: [0, 1, 0],
      top: [0, 0, 1],
      side: [1, 0, 0],
      iso: [1, -1, 1],
    };
    s.camera.up.set(0, 0, 1);
    if (view === "top") s.camera.up.set(0, 1, 0);
    s.camera.position
      .copy(s.controls.target)
      .add(
        new THREE.Vector3(...directions[view])
          .normalize()
          .multiplyScalar(s.diagonal * 1.6),
      );
    s.controls.update();
  }
  const btn = {
    fontSize: 11,
    padding: "4px 7px",
    border: "1px solid #475569",
    borderRadius: 4,
    background: "#111827",
    color: "#e2e8f0",
    cursor: "pointer",
  };
  return (
    <div
      style={{
        position: "absolute",
        inset: 0,
        display: "flex",
        flexDirection: "column",
      }}
    >
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          gap: 4,
          padding: 8,
          zIndex: 2,
          background: "#0b101acc",
        }}
      >
        {["front", "top", "side", "iso"].map((v) => (
          <button key={v} style={btn} onClick={() => standard(v)}>
            {v}
          </button>
        ))}
        <button style={btn} onClick={() => fit()}>
          Fit all
        </button>
        <button
          style={btn}
          disabled={!selected}
          onClick={() => fit([selected])}
        >
          Fit selection
        </button>
        <button
          style={btn}
          onClick={() => {
            setHidden(new Set());
            setTransparent(false);
          }}
        >
          Show all
        </button>
        <button style={btn} onClick={() => setTransparent(!transparent)}>
          Transparency
        </button>
        {transparent && (
          <input
            aria-label="Opacity"
            type="range"
            min=".1"
            max="1"
            step=".05"
            value={alpha}
            onChange={(e) => setAlpha(Number(e.target.value))}
          />
        )}
        <select
          aria-label="Section plane"
          style={btn}
          value={axis}
          onChange={(e) => {
            const a = e.target.value;
            setAxis(a);
            if (a) {
              const s = state.current,
                k = Number(a);
              if (s) {
                setRange([
                  s.box.min.getComponent(k),
                  s.box.max.getComponent(k),
                ]);
                setSection(s.center.getComponent(k));
              }
            }
          }}
        >
          <option value="">No section</option>
          <option value="0">Section X</option>
          <option value="1">Section Y</option>
          <option value="2">Section Z</option>
        </select>
        {axis && (
          <>
            <input
              aria-label="Section position"
              type="range"
              min={range[0]}
              max={range[1]}
              step=".1"
              value={section}
              onChange={(e) => setSection(Number(e.target.value))}
            />
            <span>{section.toFixed(1)} mm</span>
          </>
        )}
        <label style={{ fontSize: 11 }}>
          <input
            type="checkbox"
            checked={showKeepouts}
            onChange={(e) => setShowKeepouts(e.target.checked)}
          />
          Keepouts
        </label>
        <button
          style={btn}
          onClick={() => {
            setMeasuring(!measuring);
            setPicks([]);
            setMeasurement("");
          }}
        >
          {measuring ? "Stop measuring" : "Measure"}
        </button>
        {measuring && (
          <select
            aria-label="Measurement mode"
            style={btn}
            value={measureMode}
            onChange={(e) => {
              setMeasureMode(e.target.value);
              setPicks([]);
            }}
          >
            <option value="point">Mesh points (approximate)</option>
            <option value="instance">Exact instance distance</option>
            <option value="face">Exact CAD faces</option>
          </select>
        )}
        <button
          style={btn}
          onClick={() => {
            const s = state.current;
            if (!s) return;
            s.renderer.render(s.scene, s.camera);
            const data = s.renderer.domElement.toDataURL("image/png");
            onCapture?.(data);
            const a = document.createElement("a");
            a.download = `design-${designId}-r${revisionId}-${Date.now()}.png`;
            a.href = data;
            a.click();
          }}
        >
          Save inspection view
        </button>
      </div>
      <div style={{ position: "relative", flex: 1, minHeight: 180 }}>
        <div ref={host} style={{ position: "absolute", inset: 0 }} />
        {parts.length > 0 && (
          <div
            style={{
              position: "absolute",
              top: 8,
              left: 8,
              maxHeight: "65%",
              width: 200,
              overflow: "auto",
              background: "#0b101add",
              borderRadius: 6,
              padding: 8,
              fontSize: 11,
            }}
          >
            <strong>Assembly instances</strong>
            {parts.map((p, i) => (
              <div
                key={p.id || i}
                style={{
                  display: "flex",
                  gap: 5,
                  marginTop: 5,
                  alignItems: "center",
                }}
              >
                <input
                  aria-label={`Show ${p.name}`}
                  type="checkbox"
                  checked={
                    !hidden.has(p.id || "") &&
                    !hidden.has(p.instance_id || p.id || "")
                  }
                  onChange={(e) =>
                    setHidden((old) => {
                      const n = new Set(old);
                      e.target.checked
                        ? n.delete(p.id || "")
                        : n.add(p.id || "");
                      return n;
                    })
                  }
                />
                <button
                  style={{
                    ...btn,
                    flex: 1,
                    textAlign: "left",
                    borderColor:
                      selected === p.id || selected === p.instance_id
                        ? "#f97316"
                        : "transparent",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                  }}
                  title={p.id}
                  onClick={() => choose(p.instance_id || p.id || "", p.id)}
                >
                  {p.name || p.id}
                </button>
                <button
                  title="Isolate"
                  style={btn}
                  onClick={() => {
                    setHidden(
                      new Set(
                        parts
                          .filter((x) => x.id !== p.id)
                          .map((x) => x.id || ""),
                      ),
                    );
                    choose(p.instance_id || p.id || "", p.id);
                    fit([p.id || ""]);
                  }}
                >
                  ◎
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
      {(error || measurement || measuring) && (
        <div
          style={{
            padding: 8,
            fontSize: 12,
            background: "#0b101add",
            zIndex: 2,
          }}
        >
          {error ||
            measurement ||
            `Select two ${measureMode === "face" ? "faces" : measureMode === "instance" ? "instances" : "mesh points"} (${picks.length}/2)`}
        </div>
      )}
    </div>
  );
}
