import {
  geometryFor,
  rememberGeometry,
  rememberMesh,
} from "./geometry-cache.mjs";
import {
  getOC,
  deserializeShape,
  makeCompound,
  makeVertex,
  measureVolume,
} from "replicad";
import { readFileSync } from "node:fs";

export function loadAsset(asset, componentID) {
  if (!asset) throw new Error("Source asset was not resolved by Design Studio");
  const oc = getOC(),
    shapes = [];
  for (const c of asset.metadata.components) {
    if (componentID && componentID !== c.id) continue;
    let shape;
    try {
      shape = deserializeShape(readFileSync(asset.files[c.id], "utf8"));
    } catch (e) {
      throw new Error(
        `Cannot read B-rep component ${c.id} (${c.name}): ${String(e)}`,
      );
    }
    rememberMesh(shape, asset.meshes?.[c.id]);
    shapes.push({ id: c.id, name: c.name, shape });
  }
  if (!shapes.length) throw new Error("Imported component ID not found");
  const combined = makeCompound(shapes.map((c) => c.shape.clone()));
  if (
    !componentID &&
    asset.metadata.engine_version === "replicad-0.23.1-ocjs-0.23.0-ds0.5"
  )
    rememberGeometry(combined, asset.metadata.geometry);
  return { shape: combined, visuals: shapes };
}
export function count(shape, kind) {
  const oc = getOC(),
    it = new oc.TopExp_Explorer_2(
      shape.wrapped,
      oc.TopAbs_ShapeEnum[kind],
      oc.TopAbs_ShapeEnum.TopAbs_SHAPE,
    );
  let n = 0;
  try {
    for (; it.More(); it.Next()) n++;
  } finally {
    it.delete();
  }
  return n;
}
export function topology(shape, instance_id) {
  const cached = geometryFor(shape)?.topology;
  if (cached?.length) {
    return { ...cached[0], instance_id };
  }

  const oc = getOC();
  let valid = false,
    solid_count = 0,
    shell_count = 0,
    status = "indeterminate";
  try {
    const a = new oc.BRepCheck_Analyzer(shape.wrapped, true, false);
    try {
      valid = a.IsValid_2();
    } finally {
      a.delete();
    }
    solid_count = count(shape, "TopAbs_SOLID");
    shell_count = count(shape, "TopAbs_SHELL");
    status =
      valid && solid_count > 0 && measureVolume(shape) > 0
        ? "pass"
        : "indeterminate";
  } catch {}
  return { instance_id, valid, solid_count, shell_count, status };
}
function exactPair(
  a,
  b,
  tolerance = 0.001,
  volumeTolerance = 1e-6,
  measureOnly = false,
) {
  const oc = getOC(),
    tool = new oc.BRepExtrema_DistShapeShape_1(),
    progress = new oc.Message_ProgressRange_1();
  try {
    tool.SetDeflection(tolerance);
    tool.LoadS1(a.wrapped);
    tool.LoadS2(b.wrapped);
    if (!tool.Perform(progress) || !tool.IsDone() || !tool.NbSolution())
      throw new Error("Distance solver did not converge");
    const distance = tool.Value();
    if (!Number.isFinite(distance))
      throw new Error("Distance solver returned no finite result");
    const point = (p) => {
      const x = [p.X(), p.Y(), p.Z()];
      p.delete();
      return x;
    };
    let volume = 0;
    if (!measureOnly && (distance <= tolerance || tool.InnerSolution())) {
      const op = new oc.BRepAlgoAPI_Common_3(a.wrapped, b.wrapped, progress);
      try {
        op.Build(progress);
        if (!op.IsDone())
          throw new Error("Intersection solver did not complete");
        const props = new oc.GProp_GProps_1();
        try {
          oc.BRepGProp.VolumeProperties_1(
            op.Shape(),
            props,
            false,
            false,
            false,
          );
          volume = Math.abs(props.Mass());
        } finally {
          props.delete();
        }
      } finally {
        op.delete();
      }
    }
    return {
      distance_mm: distance,
      intersection_volume_mm3: volume,
      intersects: volume > volumeTolerance,
      contact: volume <= volumeTolerance && distance <= tolerance,
      closest_points: [
        point(tool.PointOnShape1(1)),
        point(tool.PointOnShape2(1)),
      ],
      tolerance_mm: tolerance,
      volume_tolerance_mm3: volumeTolerance,
      method: "exact_brep",
    };
  } finally {
    tool.delete();
    progress.delete();
  }
}
const bounds = (shape) => shape.boundingBox.bounds;
function boxDistance(a, b) {
  const [amin, amax] = bounds(a),
    [bmin, bmax] = bounds(b);
  return Math.hypot(
    ...[0, 1, 2].map((k) => Math.max(amin[k] - bmax[k], bmin[k] - amax[k], 0)),
  );
}
export function inspect(def, built, transform, exactTransform) {
  const instances = built.assembly.instances.length
    ? built.assembly.instances
    : [
        {
          id: def.parts?.[0]?.id || "output",
          part: def.parts?.[0] || {},
          shape: built.assembly.shape,
        },
      ];
  const tops = instances.map((i) => topology(i.shape, i.id)),
    byID = new Map(instances.map((i) => [i.id, i]));
  const results = [];
  for (const p of def.parts || [])
    if (
      p._exclude_print &&
      !["purchased", "reference"].includes(p.manufacturing?.classification)
    )
      results.push({
        id: "export_scope_" + p.id,
        type: "export_scope",
        status: "indeterminate",
        message: `Linked document ${p.name} contains purchased references. Select source.part_id for manufacturing or classify this wrapper as reference.`,
      });

  const find = (id) => {
    const exact = byID.get(id);
    if (exact) return exact;
    const matches = instances.filter((i) => i.part.id === id);
    if (matches.length !== 1)
      throw new Error(`Select an unambiguous instance ID: ${id}`);
    return matches[0];
  };
  function pair(a, b, c) {
    if (
      topology(a.shape, a.id).status !== "pass" ||
      topology(b.shape, b.id).status !== "pass"
    )
      throw new Error("One or both components are not validated solids");
    const t = Number(c.tolerance_mm ?? 0.001),
      vt = Number(c.volume_tolerance_mm3 ?? 1e-6);
    if (
      !Number.isFinite(t) ||
      t <= 0 ||
      t > 0.1 ||
      !Number.isFinite(vt) ||
      vt < 0
    )
      throw new Error("Invalid numerical tolerances");
    const left = a.sourceShape
      ? exactTransform(a.sourceShape, a.resolvedTransform || {}, built.params)
      : a.shape;
    const right = b.sourceShape
      ? exactTransform(b.sourceShape, b.resolvedTransform || {}, built.params)
      : b.shape;
    try {
      return exactPair(left, right, t, vt);
    } finally {
      if (left !== a.shape) left.delete();
      if (right !== b.shape) right.delete();
    }
  }
  for (const [index, c] of (def.checks || []).entries()) {
    if (
      ![
        "part_clearance",
        "assembly_collision",
        "measurement",
        "print_geometry",
      ].includes(c.type)
    )
      continue;
    const result = {
      id: c.id || `check_${index}`,
      type: c.type,
      status: "indeterminate",
      message: "Check could not be completed",
      instances: [],
      details: {},
    };
    try {
      if (c.type === "print_geometry") {
        const i = find(c.part);
        Object.assign(result, analyzePrint(i, c));
      } else if (c.type === "assembly_collision") {
        const selected = c.instances?.map(find) || instances;
        if (selected.length < 2)
          throw new Error("Collision check needs at least two instances");
        if (selected.length > 128)
          throw new Error(
            "Select at most 128 instances for one collision check",
          );
        const pairs = [];
        let uncertain = false;
        for (let l = 0; l < selected.length; l++)
          for (let r = l + 1; r < selected.length; r++) {
            const a = selected[l],
              b = selected[r],
              match = (p) =>
                p.length === 2 &&
                ((p[0] === a.id && p[1] === b.id) ||
                  (p[1] === a.id && p[0] === b.id));
            if ((c.ignore_pairs || []).some(match)) {
              pairs.push({ a: a.id, b: b.id, status: "ignored" });
              continue;
            }
            const required =
              Number(c.min ?? 0) +
              Number(a.part.evidence?.uncertainty_mm || 0) +
              Number(b.part.evidence?.uncertainty_mm || 0);
            if (
              boxDistance(a.shape, b.shape) >
              required + Number(c.tolerance_mm ?? 0.001)
            ) {
              pairs.push({
                a: a.id,
                b: b.id,
                status: "pass",
                method: "aabb_lower_bound",
              });
              continue;
            }
            try {
              const p = pair(a, b, c);
              const allowed = (c.allowed_contact_pairs || []).some(match);
              const pass =
                !p.intersects &&
                (!p.contact || allowed) &&
                (allowed ||
                  p.distance_mm + Number(c.tolerance_mm ?? 0.001) >= required);
              pairs.push({
                a: a.id,
                b: b.id,
                status: pass ? "pass" : "fail",
                ...p,
              });
            } catch (e) {
              uncertain = true;
              pairs.push({
                a: a.id,
                b: b.id,
                status: "indeterminate",
                error: String(e),
              });
            }
          }
        const failed = pairs.filter((p) => p.status === "fail");
        result.status = failed.length
          ? "fail"
          : uncertain
            ? "indeterminate"
            : "pass";
        result.instances = [...new Set(failed.flatMap((p) => [p.a, p.b]))];
        result.details = { pairs };
        result.message = `Solid collision check: ${failed.length} failed, ${pairs.filter((p) => p.status === "indeterminate").length} indeterminate pairs`;
      } else {
        let a = find(c.a),
          b = find(c.b);
        result.instances = [a.id, b.id];
        if (c.type === "measurement") {
          function target(i, face, point, visual) {
            if (!visual && i.sourceShape)
              i = {
                ...i,
                shape: exactTransform(
                  i.sourceShape,
                  i.resolvedTransform || {},
                  built.params,
                ),
              };
            if (visual) {
              const v = built.assembly.visuals.find((v) => v.id === visual);
              if (
                !v ||
                !(
                  visual === i.id ||
                  visual.startsWith(i.id + "/") ||
                  !built.assembly.instances.length
                )
              )
                throw new Error("Visual does not belong to selected instance");
              i = {
                ...i,
                shape: v.sourceShape
                  ? exactTransform(
                      v.sourceShape,
                      v.resolvedTransform || {},
                      built.params,
                    )
                  : v.shape,
              };
            }
            if (point) {
              if (point.length !== 3 || !point.every(Number.isFinite))
                throw new Error(
                  "Point must contain three finite world coordinates",
                );
              return makeVertex(point);
            }
            if (face !== undefined) {
              if (
                !Number.isInteger(face) ||
                face < 0 ||
                face >= i.shape.faces.length
              )
                throw new Error("Face index is outside the current build");
              return i.shape.faces[face];
            }
            return i.shape;
          }
          const p = exactPair(
            target(a, c.face_a, c.point_a, c.visual_a),
            target(b, c.face_b, c.point_b, c.visual_b),
            Number(c.tolerance_mm ?? 0.001),
            1e-6,
            true,
          );
          result.status = "pass";
          result.actual = p.distance_mm;
          result.details = p;
          result.message = `Exact CAD distance: ${p.distance_mm.toFixed(4)} mm`;
        } else {
          const p = pair(a, b, c);
          const required =
            Number(c.min ?? 0) +
            Number(a.part.evidence?.uncertainty_mm || 0) +
            Number(b.part.evidence?.uncertainty_mm || 0);
          p.required_with_uncertainty_mm = required;
          result.actual = p.distance_mm;
          result.expected = { min: required };
          result.details = p;
          result.status =
            !p.intersects &&
            p.distance_mm + Number(c.tolerance_mm ?? 0.001) >= required
              ? "pass"
              : "fail";
          result.message = p.intersects
            ? `Solid interference: ${p.intersection_volume_mm3.toFixed(4)} mm³`
            : `Clearance ${p.distance_mm.toFixed(4)} mm; required ${required} mm (including declared uncertainty)`;
        }
      }
    } catch (e) {
      result.message = e instanceof Error ? e.message : String(e);
    }
    results.push(result);
  }
  const keepouts = [];
  for (const k of def.assembly?.keepouts || []) {
    const owner = find(k.owner_instance);
    const local = built.shapes.get(k.output);
    if (!local) throw new Error(`Keepout ${k.id} has no operation geometry`);
    const shape = transform(
      local,
      owner.resolvedTransform || owner.transform || {},
      built.params,
    );
    keepouts.push({
      id: k.id,
      name: k.purpose,
      owner_instance: owner.id,
      shape,
      kind: "keepout",
      color: "#eab308",
    });
    for (const target of k.against?.length
      ? k.against.map(find)
      : instances.filter((i) => i.id !== owner.id)) {
      const r = {
        id: `keepout_${k.id}_${target.id}`,
        type: "keepout",
        status: "indeterminate",
        instances: [owner.id, target.id],
        message: "Keepout could not be checked",
      };
      try {
        const p = pair({ id: k.id, shape }, target, {});
        r.details = { ...p, source: k.source, purpose: k.purpose };
        r.status =
          !p.intersects && p.distance_mm + 0.001 >= k.clearance_mm
            ? "pass"
            : "fail";
        r.message = `${k.purpose}: ${target.id}, ${p.intersects ? "interference" : p.distance_mm.toFixed(3) + " mm clearance"}`;
      } catch (e) {
        r.message = String(e);
      }
      results.push(r);
    }
  }
  function evidence(d, prefix = "") {
    for (const p of d.parts || [])
      if (
        ["purchased", "reference"].includes(p.manufacturing?.classification)
      ) {
        const e = p.evidence,
          complete =
            e?.complete &&
            !e.omissions?.length &&
            !e.measurements_needed?.length;
        if (!complete)
          results.push({
            id: "evidence_" + prefix + p.id,
            type: "component_evidence",
            status: "indeterminate",
            message: `Component ${prefix + p.name} has incomplete fit evidence`,
            details: e || { missing: true },
          });
      }
    for (const c of d._resolved_components || [])
      evidence(c.definition, prefix + c.part_id + "/");
  }
  evidence(def);
  return { topology: tops, exact_checks: results, keepouts };
}
// Sampling detects violations; a clean sample is explicitly indeterminate, never certification.
function analyzePrint(instance, c) {
  const mesh = instance.shape.mesh({ tolerance: 0.05, angularTolerance: 0.1 }),
    verts = mesh.vertices,
    idx = mesh.triangles;
  if (idx.length / 3 > 60000)
    return {
      status: "indeterminate",
      message:
        "Print analysis requires a simplified mesh (maximum 60000 triangles)",
    };
  const triangles = [];
  const sub = (a, b) => a.map((x, k) => x - b[k]),
    cross = (a, b) => [
      a[1] * b[2] - a[2] * b[1],
      a[2] * b[0] - a[0] * b[2],
      a[0] * b[1] - a[1] * b[0],
    ],
    dot = (a, b) => a.reduce((s, x, k) => s + x * b[k], 0);
  for (let t = 0; t < idx.length; t += 3) {
    const p = [0, 1, 2].map((k) =>
      verts.slice(idx[t + k] * 3, idx[t + k] * 3 + 3),
    );
    const n = cross(sub(p[1], p[0]), sub(p[2], p[0]));
    const len = Math.hypot(...n);
    if (len > 1e-10)
      triangles.push({ p, n: n.map((x) => x / len), index: t / 3 });
  }
  function ray(o, d, tri) {
    const [a, b, cc] = tri.p,
      e1 = sub(b, a),
      e2 = sub(cc, a),
      h = cross(d, e2),
      det = dot(e1, h);
    if (Math.abs(det) < 1e-9) return Infinity;
    const f = 1 / det,
      s = sub(o, a),
      u = f * dot(s, h);
    if (u < 0 || u > 1) return Infinity;
    const q = cross(s, e1),
      v = f * dot(d, q);
    if (v < 0 || u + v > 1) return Infinity;
    const t = f * dot(e2, q);
    return t > 1e-4 ? t : Infinity;
  }
  let min = Infinity;
  const regions = [],
    featureRegions = [],
    overhangRegions = [];
  const zmin = Math.min(
    ...triangles.map((t) => Math.min(...t.p.map((p) => p[2]))),
  );
  for (const tri of triangles) {
    const center = [0, 1, 2].map(
      (k) => tri.p.reduce((s, p) => s + p[k], 0) / 3,
    );
    const angle = (Math.asin(Math.max(0, -tri.n[2])) * 180) / Math.PI;
    if (
      c.max_overhang_deg !== undefined &&
      center[2] > zmin + 0.1 &&
      angle > Number(c.max_overhang_deg)
    )
      overhangRegions.push({
        point: center,
        triangle: tri.index,
        overhang_deg: angle,
      });
  }
  const stride = Math.max(1, Math.ceil(triangles.length / 512));
  for (let t = 0; t < triangles.length; t += stride) {
    const tri = triangles[t],
      center = [0, 1, 2].map((k) => tri.p.reduce((s, p) => s + p[k], 0) / 3),
      d = tri.n.map((x) => -x),
      o = center.map((x, k) => x + d[k] * 1e-4);
    let distance = Infinity;
    for (const other of triangles)
      if (other !== tri) distance = Math.min(distance, ray(o, d, other));
    if (Number.isFinite(distance)) {
      min = Math.min(min, distance + 1e-4);
      if (distance + 1e-4 < Number(c.min_wall_mm ?? 0.8))
        regions.push({
          point: center,
          triangle: tri.index,
          thickness_mm: distance + 1e-4,
        });
      if (
        c.min_feature_mm !== undefined &&
        distance + 1e-4 < Number(c.min_feature_mm)
      )
        featureRegions.push({
          point: center,
          triangle: tri.index,
          feature_span_mm: distance + 1e-4,
        });
    }
  }
  return {
    instances: [instance.id],
    status:
      regions.length || featureRegions.length || overhangRegions.length
        ? "fail"
        : "indeterminate",
    actual: Number.isFinite(min) ? min : null,
    details: {
      method: "sampled_mesh_rays",
      mesh_tolerance_mm: 0.05,
      sample_limit: 512,
      regions,
      feature_regions: featureRegions,
      overhang_regions: overhangRegions,
      print_orientation:
        "current part coordinates; apply manufacturing orientation before interpreting overhangs",
      coverage:
        "sampled; small features and overhangs require inspection/slicing",
    },
    message:
      regions.length || featureRegions.length || overhangRegions.length
        ? `Sampled print geometry: ${regions.length} thin walls, ${featureRegions.length} small spans, ${overhangRegions.length} overhang triangles`
        : "No violation detected in samples; full geometry printability is not established",
  };
}
