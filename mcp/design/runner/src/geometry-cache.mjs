import { readFileSync } from "node:fs";
// Caches are runner-private, populated only from server-verified immutable assets.
const geometry = new WeakMap(),
  meshes = new WeakMap();
export const geometryFor = (shape) => geometry.get(shape);
export function rememberGeometry(shape, value) {
  if (value) geometry.set(shape, value);
}
export function rememberMesh(shape, path) {
  if (path) meshes.set(shape, { path, transforms: [] });
}
export function inheritCache(source, target, transform = {}, params = {}) {
  const g = geometry.get(source);
  if (g) geometry.set(target, g);
  const m = meshes.get(source);
  if (m)
    meshes.set(target, {
      ...m,
      transforms: [...m.transforms, { transform, params }],
    });
}
export function meshFor(shape, resolveVector, resolveNumber, rotatePoint) {
  const cached = meshes.get(shape);
  if (!cached) return null;
  const document = JSON.parse(readFileSync(cached.path, "utf8"));
  const mesh = {
    vertices: document.vertices,
    triangles: document.triangles,
    normals: document.normals,
    faceGroups: document.faceGroups,
  };
  for (const { transform: t, params: p } of cached.transforms) {
    const scale =
      t.scale === undefined ? 1 : resolveNumber(t.scale, p, "scale");
    const shift = resolveVector(t.translate, p, "translation");
    const rotations = (t.rotate || []).map((r) => ({
      angle: resolveNumber(r.angle, p, "angle"),
      center: resolveVector(r.center, p, "rotation center"),
      direction: resolveVector(r.direction, p, "rotation axis", [0, 0, 1]),
    }));
    for (let i = 0; i < mesh.vertices.length; i += 3) {
      let v = mesh.vertices.slice(i, i + 3).map((x) => x * scale);
      for (const r of rotations)
        v = rotatePoint(v, r.angle, r.center, r.direction);
      for (let k = 0; k < 3; k++) mesh.vertices[i + k] = v[k] + shift[k];
    }
    for (let i = 0; i < (mesh.normals?.length || 0); i += 3) {
      let v = mesh.normals.slice(i, i + 3);
      for (const r of rotations)
        v = rotatePoint(v, r.angle, [0, 0, 0], r.direction);
      for (let k = 0; k < 3; k++) mesh.normals[i + k] = v[k];
    }
  }
  return mesh;
}
