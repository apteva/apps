export type V = [number, number, number];
export type Triangle = {
  A: V;
  B: V;
  C: V;
  Normal: V;
  Color: V;
  NodeID: string;
  FaceID: string;
};
export type Model = {
  nodes: {
    id: string;
    name: string;
    color: V;
    mesh: {
      vertices: { id: string; position: V }[];
      faces: { id: string; vertices: string[] }[];
    };
  }[];
};
export type Pick = { node: string; kind: "face" | "vertex"; id: string };
export type Camera = { yaw: number; pitch: number; zoom: number };
export type Highlight = { node: string; kind: string; ids: string[] } | null;
const vertex = `attribute vec3 position;attribute vec3 normal;attribute vec3 color;uniform vec3 center;uniform vec2 angles;uniform float scale;uniform float aspect;uniform float pointSize;varying vec3 shade;
void main(){vec3 p=position-center;float cy=cos(angles.x),sy=sin(angles.x),cx=cos(angles.y),sx=sin(angles.y);vec3 q=vec3(p.x*cy+p.z*sy,p.y,-p.x*sy+p.z*cy);q=vec3(q.x,q.y*cx-q.z*sx,q.y*sx+q.z*cx);gl_Position=vec4(q.x*scale/aspect,q.y*scale,-q.z*scale*.08,1.);gl_PointSize=pointSize;float light=length(normal)<.1?1.:.45+.55*abs(dot(normal,normalize(vec3(-.4,.8,1.))));shade=pow(color*light,vec3(1./2.2));}`;
const fragment = `precision mediump float;varying vec3 shade;void main(){gl_FragColor=vec4(shade,1.);}`;
export function frame(model: Model) {
  let min: V = [Infinity, Infinity, Infinity],
    max: V = [-Infinity, -Infinity, -Infinity];
  for (const n of model.nodes)
    for (const v of n.mesh.vertices)
      for (let i = 0; i < 3; i++) {
        min[i] = Math.min(min[i], v.position[i]);
        max[i] = Math.max(max[i], v.position[i]);
      }
  if (!Number.isFinite(min[0])) return { center: [0, 0, 0] as V, size: 3 };
  return {
    center: min.map((v, i) => (v + max[i]) / 2) as V,
    size: Math.max(0.1, Math.hypot(...min.map((v, i) => max[i] - v))),
  };
}
export function project(
  p: V,
  model: Model,
  camera: Camera,
  width: number,
  height: number,
) {
  const { center, size } = frame(model);
  const a = p.map((v, i) => v - center[i]);
  const x = a[0] * Math.cos(camera.yaw) + a[2] * Math.sin(camera.yaw),
    z = -a[0] * Math.sin(camera.yaw) + a[2] * Math.cos(camera.yaw),
    y = a[1] * Math.cos(camera.pitch) - z * Math.sin(camera.pitch),
    depth = a[1] * Math.sin(camera.pitch) + z * Math.cos(camera.pitch);
  const scale = (height * 0.8 * camera.zoom) / size;
  return [width / 2 + x * scale, height / 2 - y * scale, depth] as V;
}
export function pick(
  model: Model,
  triangles: Triangle[],
  camera: Camera,
  width: number,
  height: number,
  x: number,
  y: number,
  kind: "face" | "vertex",
  node: string,
): Pick | null {
  const f = frame(model);
  const scale = (height * 0.8 * camera.zoom) / f.size;
  const proj = (p: V) => {
    const a = p.map((v, i) => v - f.center[i]);
    const xx = a[0] * Math.cos(camera.yaw) + a[2] * Math.sin(camera.yaw),
      zz = -a[0] * Math.sin(camera.yaw) + a[2] * Math.cos(camera.yaw);
    return [
      width / 2 + xx * scale,
      height / 2 -
        (a[1] * Math.cos(camera.pitch) - zz * Math.sin(camera.pitch)) * scale,
      a[1] * Math.sin(camera.pitch) + zz * Math.cos(camera.pitch),
    ];
  };
  let hit: Pick | null = null,
    depth = -Infinity;
  for (const t of triangles) {
    const a = proj(t.A),
      b = proj(t.B),
      c = proj(t.C),
      area = (b[1] - c[1]) * (a[0] - c[0]) + (c[0] - b[0]) * (a[1] - c[1]);
    if (Math.abs(area) < 1e-8) continue;
    const u = ((b[1] - c[1]) * (x - c[0]) + (c[0] - b[0]) * (y - c[1])) / area,
      v = ((c[1] - a[1]) * (x - c[0]) + (a[0] - c[0]) * (y - c[1])) / area,
      w = 1 - u - v;
    if (u >= 0 && v >= 0 && w >= 0) {
      const z = u * a[2] + v * b[2] + w * c[2];
      if (z > depth) {
        depth = z;
        hit = { node: t.NodeID, kind: "face", id: t.FaceID };
      }
    }
  }
  if (kind === "face") return hit;
  let nearest = 13,
    result: Pick | null = null;
  for (const n of model.nodes) {
    if (node && n.id !== node) continue;
    for (const v of n.mesh.vertices) {
      const p = proj(v.position),
        dist = Math.hypot(p[0] - x, p[1] - y);
      if (dist < nearest && p[2] >= depth - f.size * 0.015) {
        nearest = dist;
        result = { node: n.id, kind: "vertex", id: v.id };
      }
    }
  }
  return result;
}
export function renderer(canvas: HTMLCanvasElement) {
  const gl = canvas.getContext("webgl", { antialias: true, alpha: false });
  if (!gl) throw new Error("This browser does not support WebGL.");
  const shader = (type: number, source: string) => {
    const s = gl.createShader(type)!;
    gl.shaderSource(s, source);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS))
      throw new Error(gl.getShaderInfoLog(s) || "Shader error");
    return s;
  };
  const program = gl.createProgram()!,
    vs = shader(gl.VERTEX_SHADER, vertex),
    fs = shader(gl.FRAGMENT_SHADER, fragment);
  gl.attachShader(program, vs);
  gl.attachShader(program, fs);
  gl.linkProgram(program);
  gl.deleteShader(vs);
  gl.deleteShader(fs);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS))
    throw new Error("Could not link viewport shaders");
  const buffer = gl.createBuffer()!;
  const draw = (data: number[], mode: number, pointSize: number) => {
    if (!data.length) return;
    gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(data), gl.DYNAMIC_DRAW);
    for (const [i, key] of ["position", "normal", "color"].entries()) {
      const loc = gl.getAttribLocation(program, key);
      gl.enableVertexAttribArray(loc);
      gl.vertexAttribPointer(loc, 3, gl.FLOAT, false, 36, i * 12);
    }
    gl.uniform1f(gl.getUniformLocation(program, "pointSize"), pointSize);
    gl.drawArrays(mode, 0, data.length / 9);
  };
  return {
    draw(
      model: Model,
      triangles: Triangle[],
      camera: Camera,
      highlight: Highlight,
      nodeID: string,
      wire: boolean,
      vertexMode: boolean,
    ) {
      const rect = canvas.getBoundingClientRect(),
        dpr = Math.min(devicePixelRatio || 1, 2);
      const width = Math.max(1, Math.round(rect.width * dpr)),
        height = Math.max(1, Math.round(rect.height * dpr));
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width;
        canvas.height = height;
      }
      const { center, size } = frame(model);
      gl.viewport(0, 0, width, height);
      gl.clearColor(0.055, 0.071, 0.093, 1);
      gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
      gl.enable(gl.DEPTH_TEST);
      gl.depthFunc(gl.LEQUAL);
      gl.useProgram(program);
      gl.uniform3fv(gl.getUniformLocation(program, "center"), center);
      gl.uniform2f(
        gl.getUniformLocation(program, "angles"),
        camera.yaw,
        camera.pitch,
      );
      gl.uniform1f(
        gl.getUniformLocation(program, "scale"),
        (1.6 * camera.zoom) / size,
      );
      gl.uniform1f(gl.getUniformLocation(program, "aspect"), width / height);
      const grid: number[] = [];
      const step = Math.pow(10, Math.floor(Math.log10(size / 5))),
        extent = step * 15;
      for (let i = -15; i <= 15; i++) {
        const col = i === 0 ? [0.15, 0.23, 0.29] : [0.065, 0.085, 0.11];
        grid.push(
          -extent,
          0,
          i * step,
          0,
          0,
          0,
          ...col,
          extent,
          0,
          i * step,
          0,
          0,
          0,
          ...col,
          i * step,
          0,
          -extent,
          0,
          0,
          0,
          ...col,
          i * step,
          0,
          extent,
          0,
          0,
          0,
          ...col,
        );
      }
      draw(grid, gl.LINES, 1);
      const ids = new Set(highlight?.ids || []),
        data: number[] = [];
      for (const t of triangles) {
        const selected =
          highlight?.kind === "face" &&
          highlight.node === t.NodeID &&
          ids.has(t.FaceID);
        const col = selected ? [1, 0.48, 0.06] : t.Color;
        for (const p of [t.A, t.B, t.C]) data.push(...p, ...t.Normal, ...col);
      }
      gl.enable(gl.POLYGON_OFFSET_FILL);
      gl.polygonOffset(1, 1);
      draw(data, gl.TRIANGLES, 1);
      gl.disable(gl.POLYGON_OFFSET_FILL);
      const lines: number[] = [],
        points: number[] = [];
      for (const n of model.nodes) {
        const p = new Map(n.mesh.vertices.map((v) => [v.id, v.position]));
        if (wire || n.id === nodeID) {
          const color =
            n.id === nodeID ? [0.15, 0.62, 0.55] : [0.08, 0.11, 0.15];
          for (const f of n.mesh.faces)
            for (let i = 0; i < f.vertices.length; i++)
              lines.push(
                ...p.get(f.vertices[i])!,
                0,
                0,
                0,
                ...color,
                ...p.get(f.vertices[(i + 1) % f.vertices.length])!,
                0,
                0,
                0,
                ...color,
              );
        }
        if (vertexMode && n.id === nodeID)
          for (const v of n.mesh.vertices) {
            const selected =
              highlight?.kind === "vertex" &&
              highlight.node === n.id &&
              ids.has(v.id);
            points.push(
              ...v.position,
              0,
              0,
              0,
              ...(selected ? [1, 0.62, 0.1] : [0.35, 0.75, 0.66]),
            );
          }
      }
      draw(lines, gl.LINES, 1);
      draw(points, gl.POINTS, 6 * dpr);
    },
    dispose() {
      gl.deleteBuffer(buffer);
      gl.deleteProgram(program);
    },
  };
}
