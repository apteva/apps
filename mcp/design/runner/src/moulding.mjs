// Moulding checks on a tessellated solid: draft angle per B-rep face and
// undercuts (surfaces that cannot leave a two-part mould along the pull
// direction because the part itself blocks them).

const sub = (a, b) => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

function rayHit(origin, direction, tri) {
  const [a, b, c] = tri.p, e1 = sub(b, a), e2 = sub(c, a), h = cross(direction, e2), det = dot(e1, h);
  if (Math.abs(det) < 1e-12) return Infinity;
  const f = 1 / det, s = sub(origin, a), u = f * dot(s, h);
  if (u < 0 || u > 1) return Infinity;
  const q = cross(s, e1), v = f * dot(direction, q);
  if (v < 0 || u + v > 1) return Infinity;
  const t = f * dot(e2, q);
  return t > 1e-4 ? t : Infinity;
}

export function analyzeMoulding(shape, check) {
  const pullInput = Array.isArray(check.pull) ? check.pull.map(Number) : [0, 0, 1];
  const length = Math.hypot(...pullInput);
  if (!(length > 1e-9)) throw new Error("pull direction must not be zero");
  const pull = pullInput.map((v) => v / length);
  const minDraft = Number(check.min_draft_deg ?? 1);
  if (!(minDraft >= 0 && minDraft < 45)) throw new Error("min_draft_deg must be between 0 and 45");
  const mesh = shape.mesh({ tolerance: 0.05, angularTolerance: 0.1 });
  const vertices = mesh.vertices, indices = mesh.triangles;
  if (indices.length / 3 > 60000) {
    return { status: "indeterminate", message: "Moulding analysis needs a simpler mesh (maximum 60000 triangles)" };
  }
  const faceOf = new Int32Array(indices.length / 3).fill(-1);
  for (const group of mesh.faceGroups || []) {
    for (let t = group.start / 3; t < (group.start + group.count) / 3; t++) faceOf[t] = group.faceId;
  }
  const triangles = [];
  for (let t = 0; t < indices.length; t += 3) {
    const p = [0, 1, 2].map((k) => [vertices[indices[t + k] * 3], vertices[indices[t + k] * 3 + 1], vertices[indices[t + k] * 3 + 2]]);
    const n = cross(sub(p[1], p[0]), sub(p[2], p[0]));
    const area2 = Math.hypot(...n);
    if (area2 < 1e-12) continue;
    triangles.push({ p, n: n.map((v) => v / area2), area: area2 / 2, face: faceOf[t / 3], index: t / 3 });
  }
  // Draft: angle between the surface and the pull direction. Surfaces
  // perpendicular to the pull (draft 90°) part cleanly; walls parallel to it
  // (draft 0°) drag.
  const faces = new Map();
  for (const tri of triangles) {
    const draft = (Math.asin(Math.min(1, Math.abs(dot(tri.n, pull)))) * 180) / Math.PI;
    const key = tri.face >= 0 ? tri.face : `t${tri.index}`;
    const entry = faces.get(key) || { face: tri.face, min_draft_deg: 90, area_mm2: 0 };
    entry.min_draft_deg = Math.min(entry.min_draft_deg, draft);
    entry.area_mm2 += tri.area;
    faces.set(key, entry);
  }
  const lowDraft = [...faces.values()].filter((face) => face.min_draft_deg + 1e-6 < minDraft && face.area_mm2 > 1e-3);
  // Undercuts: from each surface, look along the direction it releases
  // (+pull for faces turned toward it, -pull for faces turned away). If the
  // part is in the way, that surface is trapped in a straight-pull mould.
  const undercuts = [];
  let sampling = null;
  if (check.undercuts !== false) {
    const candidates = triangles.filter((tri) => Math.abs(dot(tri.n, pull)) > Math.sin((Math.max(minDraft, 0.05) * Math.PI) / 180) * 0.5);
    const samples = Math.round(Number(check.undercut_samples ?? 1024));
    if (!(samples >= 16 && samples <= 8192)) throw new Error("undercut_samples must be between 16 and 8192");
    const stride = Math.max(1, Math.ceil(candidates.length / samples));
    sampling = { candidates: candidates.length, sampled: Math.ceil(candidates.length / stride), stride };
    const trapped = new Map();
    for (let i = 0; i < candidates.length; i += stride) {
      const tri = candidates[i];
      const direction = dot(tri.n, pull) > 0 ? pull : pull.map((v) => -v);
      const center = [0, 1, 2].map((k) => (tri.p[0][k] + tri.p[1][k] + tri.p[2][k]) / 3);
      const origin = center.map((v, k) => v + tri.n[k] * 1e-3);
      for (const other of triangles) {
        if (other === tri) continue;
        if (Number.isFinite(rayHit(origin, direction, other))) {
          const key = tri.face >= 0 ? tri.face : `t${tri.index}`;
          const entry = trapped.get(key) || { face: tri.face, point: center, side: dot(tri.n, pull) > 0 ? "pull" : "against_pull", samples: 0 };
          entry.samples += 1;
          trapped.set(key, entry);
          break;
        }
      }
    }
    undercuts.push(...trapped.values());
  }
  const failed = lowDraft.length > 0 || undercuts.length > 0;
  const parts = [];
  if (lowDraft.length) parts.push(`${lowDraft.length} face(s) under ${minDraft}° draft`);
  if (undercuts.length) parts.push(`${undercuts.length} undercut face(s)`);
  const sampledNote = sampling && sampling.stride > 1 ? ` (undercuts sampled on ${sampling.sampled} of ${sampling.candidates} triangles; raise undercut_samples for a denser search)` : "";
  return {
    status: failed ? "fail" : "pass",
    message: (failed ? `Not mouldable along [${pull.map((v) => +v.toFixed(3)).join(", ")}]: ${parts.join(", ")}` : `Mouldable along [${pull.map((v) => +v.toFixed(3)).join(", ")}] with at least ${minDraft}° draft and no undercuts`) + sampledNote,
    expected: minDraft,
    actual: Math.min(...[...faces.values()].map((face) => face.min_draft_deg)),
    details: {
      pull,
      min_draft_deg: minDraft,
      low_draft_faces: lowDraft.sort((a, b) => a.min_draft_deg - b.min_draft_deg).slice(0, 64),
      undercut_faces: undercuts.slice(0, 64),
      method: "mesh_normals_and_ray_casting",
      undercut_sampling: sampling,
    },
  };
}
