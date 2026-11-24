// Phase-2 modelling features: edge/face selectors, selective fillet/chamfer,
// shell, 2D profiles (rounded rectangles, slots, arcs), holes, and loft.
// Helpers that need the expression resolver are passed in so this module
// stays independent of runner.mjs internals.
import { EdgeFinder, FaceFinder, draw, drawCircle, drawPolysides, drawRoundedRectangle } from "replicad";

function fail(message) {
  throw new Error(message);
}

const PLANES = new Set(["XY", "XZ", "YZ"]);
const CURVES = { line: "LINE", circle: "CIRCLE", ellipse: "ELLIPSE", bspline: "BSPLINE_CURVE", bezier: "BEZIER_CURVE" };
const SURFACES = { plane: "PLANE", cylinder: "CYLINDRE", cone: "CONE", sphere: "SPHERE", torus: "TORUS", bspline: "BSPLINE_SURFACE", revolution: "REVOLUTION_SURFACE", extrusion: "EXTRUSION_SURFACE" };
const SIDES = { top: ["XY", 2, "max"], bottom: ["XY", 2, "min"], "+z": ["XY", 2, "max"], "-z": ["XY", 2, "min"], "+x": ["YZ", 0, "max"], "-x": ["YZ", 0, "min"], "+y": ["XZ", 1, "max"], "-y": ["XZ", 1, "min"] };
const EDGE_KEYS = new Set(["side", "direction", "curve", "parallel_to", "in_plane", "in_box", "near", "contains_point", "length", "not", "any"]);
const FACE_KEYS = new Set(["side", "surface", "parallel_to", "in_plane", "in_box", "near", "contains_point", "not", "any"]);

// selector: object (all keys must match) or array (any entry matches).
//   side: top|bottom|+x|-x|+y|-y|+z|-z   lies in the shape's outer plane on that side
//   direction: X|Y|Z|[x,y,z]   (edges) runs along that direction
//   curve: line|circle|...     (edges)  surface: plane|cylinder|... (faces)
//   parallel_to: XY|XZ|YZ      in_plane: {plane, offset}
//   in_box: [[x,y,z],[x,y,z]]  near: {point:[x,y,z], distance}
//   contains_point: [x,y,z]    length: number (edges)
//   not: selector              any: [selectors]
export function applySelector(finder, selector, ctx, kind, label) {
  if (Array.isArray(selector)) {
    if (!selector.length) fail(`${label} must not be an empty list`);
    return finder.either(selector.map((entry, index) => (f) => applySelector(f, entry, ctx, kind, `${label}[${index}]`)));
  }
  if (!selector || typeof selector !== "object") fail(`${label} must be a selector object or list`);
  const allowed = kind === "edge" ? EDGE_KEYS : FACE_KEYS;
  for (const key of Object.keys(selector)) {
    if (!allowed.has(key)) fail(`${label}.${key} is not a ${kind} selector (use ${[...allowed].join(", ")})`);
  }
  const point = (value, name) => ctx.vector(value, `${label}.${name}`);
  let f = finder;
  if (selector.side !== undefined) {
    const side = SIDES[String(selector.side).toLowerCase()];
    if (!side) fail(`${label}.side must be one of ${Object.keys(SIDES).join(", ")}`);
    const [plane, axis, end] = side;
    f = f.inPlane(plane, ctx.bounds()[end][axis]);
  }
  if (selector.direction !== undefined) {
    const direction = typeof selector.direction === "string" ? selector.direction.toUpperCase() : point(selector.direction, "direction");
    if (typeof direction === "string" && !["X", "Y", "Z"].includes(direction)) fail(`${label}.direction must be X, Y, Z or [x,y,z]`);
    f = f.inDirection(direction);
  }
  if (selector.curve !== undefined) {
    const curve = CURVES[String(selector.curve).toLowerCase()];
    if (!curve) fail(`${label}.curve must be one of ${Object.keys(CURVES).join(", ")}`);
    f = f.ofCurveType(curve);
  }
  if (selector.surface !== undefined) {
    const surface = SURFACES[String(selector.surface).toLowerCase()];
    if (!surface) fail(`${label}.surface must be one of ${Object.keys(SURFACES).join(", ")}`);
    f = f.ofSurfaceType(surface);
  }
  if (selector.parallel_to !== undefined) {
    if (!PLANES.has(selector.parallel_to)) fail(`${label}.parallel_to must be XY, XZ or YZ`);
    f = f.parallelTo(selector.parallel_to);
  }
  if (selector.in_plane !== undefined) {
    const spec = typeof selector.in_plane === "string" ? { plane: selector.in_plane } : selector.in_plane;
    if (!PLANES.has(spec?.plane)) fail(`${label}.in_plane.plane must be XY, XZ or YZ`);
    f = f.inPlane(spec.plane, ctx.resolve(spec.offset ?? 0, `${label}.in_plane.offset`));
  }
  if (selector.in_box !== undefined) {
    if (!Array.isArray(selector.in_box) || selector.in_box.length !== 2) fail(`${label}.in_box must be [[x,y,z],[x,y,z]]`);
    f = f.inBox(point(selector.in_box[0], "in_box[0]"), point(selector.in_box[1], "in_box[1]"));
  }
  if (selector.near !== undefined) {
    const distance = ctx.resolve(selector.near.distance ?? 0.01, `${label}.near.distance`);
    if (distance <= 0) fail(`${label}.near.distance must be positive`);
    f = f.withinDistance(distance, point(selector.near.point, "near.point"));
  }
  if (selector.contains_point !== undefined) f = f.containsPoint(point(selector.contains_point, "contains_point"));
  if (selector.length !== undefined) {
    const length = ctx.resolve(selector.length, `${label}.length`);
    f = f.ofLength((value) => Math.abs(value - length) <= 1e-4);
  }
  if (selector.any !== undefined) f = applySelector(f, Array.isArray(selector.any) ? selector.any : [selector.any], ctx, kind, `${label}.any`);
  if (selector.not !== undefined) f = f.not((inner) => applySelector(inner, selector.not, ctx, kind, `${label}.not`));
  return f;
}

function selectionCount(shape, selector, ctx, kind, label) {
  const finder = applySelector(kind === "edge" ? new EdgeFinder() : new FaceFinder(), selector, ctx, kind, label);
  const found = finder.find(shape);
  const count = Array.isArray(found) ? found.length : found ? 1 : 0;
  if (count === 0) fail(`${label} selected no ${kind}s on the input shape`);
  return count;
}

export function edgeFeature(shape, operation, kind, amount, ctx) {
  if (operation.edges === undefined || operation.edges === "all") {
    return kind === "fillet" ? shape.fillet(amount) : shape.chamfer(amount);
  }
  selectionCount(shape, operation.edges, ctx, "edge", `${operation.id}.edges`);
  const filter = (e) => applySelector(e, operation.edges, ctx, "edge", `${operation.id}.edges`);
  try {
    return kind === "fillet" ? shape.fillet(amount, filter) : shape.chamfer(amount, filter);
  } catch (error) {
    fail(`${operation.id}: ${kind} of ${amount} mm failed on the selected edges (radius too large for adjacent faces?): ${error?.message || error}`);
  }
}

export function shellFeature(shape, operation, ctx) {
  const thickness = ctx.resolve(operation.thickness, `${operation.id}.thickness`);
  if (thickness <= 0) fail(`${operation.id}.thickness must be positive`);
  if (operation.open_faces === undefined) fail(`${operation.id}.open_faces is required (for example {"side":"top"})`);
  selectionCount(shape, operation.open_faces, ctx, "face", `${operation.id}.open_faces`);
  // replicad offsets by -thickness: walls grow inward and the outside is kept.
  try {
    return shape.shell(thickness, (f) => applySelector(f, operation.open_faces, ctx, "face", `${operation.id}.open_faces`));
  } catch (error) {
    fail(`${operation.id}: shell failed (wall thicker than a feature, or fillets smaller than the wall?): ${error?.message || error}`);
  }
}

// ---- 2D profiles ---------------------------------------------------------

export function drawProfile(profile, ctx, label) {
  if (!profile || typeof profile !== "object") fail(`${label} must be a profile object`);
  const num = (value, name) => ctx.resolve(value, `${label}.${name}`);
  const pt = (value, name) => {
    if (!Array.isArray(value) || value.length !== 2) fail(`${label}.${name} must be [x,y]`);
    return [num(value[0], `${name}[0]`), num(value[1], `${name}[1]`)];
  };
  const center = profile.center === undefined ? [0, 0] : pt(profile.center, "center");
  let drawing;
  switch (profile.kind) {
    case "rectangle":
    case "rounded_rectangle": {
      const width = num(profile.width, "width");
      const height = num(profile.height, "height");
      if (width <= 0 || height <= 0) fail(`${label} width and height must be positive`);
      const radius = profile.radius === undefined ? 0 : num(profile.radius, "radius");
      if (radius < 0 || radius * 2 > Math.min(width, height) + 1e-9) fail(`${label}.radius must be between 0 and half the shorter side`);
      drawing = drawRoundedRectangle(width, height, radius > 0 ? Math.min(radius, Math.min(width, height) / 2 - 1e-6) : 0);
      break;
    }
    case "slot": {
      const length = num(profile.length, "length");
      const width = num(profile.width, "width");
      if (width <= 0 || length < width) fail(`${label} needs width > 0 and length >= width (length is end to end)`);
      drawing = length - width < 1e-6 ? drawCircle(width / 2) : drawRoundedRectangle(length, width, width / 2 - 1e-6);
      break;
    }
    case "circle": {
      const radius = profile.diameter !== undefined ? num(profile.diameter, "diameter") / 2 : num(profile.radius, "radius");
      if (radius <= 0) fail(`${label}.radius must be positive`);
      drawing = drawCircle(radius);
      break;
    }
    case "regular_polygon": {
      const radius = num(profile.radius, "radius");
      const sides = Math.round(num(profile.sides, "sides"));
      if (radius <= 0 || sides < 3 || sides > 64) fail(`${label} needs radius > 0 and 3-64 sides`);
      drawing = drawPolysides(radius, sides);
      break;
    }
    case "polygon": {
      if (!Array.isArray(profile.points) || profile.points.length < 3) fail(`${label}.points needs at least three [x,y] points`);
      const points = profile.points.map((p, i) => pt(p, `points[${i}]`));
      let pen = draw(points[0]);
      for (const p of points.slice(1)) pen = pen.lineTo(p);
      drawing = pen.close();
      break;
    }
    case "path": {
      if (!Array.isArray(profile.segments) || !profile.segments.length) fail(`${label}.segments is required`);
      let pen = draw(pt(profile.start, "start"));
      profile.segments.forEach((segment, index) => {
        const name = `segments[${index}]`;
        if (segment.line) pen = pen.lineTo(pt(segment.line, `${name}.line`));
        else if (segment.arc && segment.via) pen = pen.threePointsArcTo(pt(segment.arc, `${name}.arc`), pt(segment.via, `${name}.via`));
        else if (segment.arc && segment.sagitta !== undefined) pen = pen.sagittaArcTo(pt(segment.arc, `${name}.arc`), num(segment.sagitta, `${name}.sagitta`));
        else if (segment.tangent_arc) pen = pen.tangentArcTo(pt(segment.tangent_arc, `${name}.tangent_arc`));
        else fail(`${label}.${name} must be {line}, {arc, via}, {arc, sagitta} or {tangent_arc}`);
      });
      drawing = pen.close();
      break;
    }
    default:
      fail(`${label}.kind must be rectangle, rounded_rectangle, slot, circle, regular_polygon, polygon or path`);
  }
  if (profile.angle !== undefined) drawing = drawing.rotate(num(profile.angle, "angle"), [0, 0]);
  if (center[0] !== 0 || center[1] !== 0) drawing = drawing.translate(center[0], center[1]);
  if (profile.holes !== undefined) {
    if (!Array.isArray(profile.holes)) fail(`${label}.holes must be a list of profiles`);
    profile.holes.forEach((hole, index) => { drawing = drawing.cut(drawProfile(hole, ctx, `${label}.holes[${index}]`)); });
  }
  return drawing;
}

function sketchProfile(profile, plane, origin, ctx, label) {
  if (!PLANES.has(plane)) fail(`${label}.plane must be XY, XZ or YZ`);
  return drawProfile(profile, ctx, label).sketchOnPlane(plane, origin);
}

export function extrudeProfile(operation, ctx) {
  const height = ctx.resolve(operation.height, `${operation.id}.height`);
  if (height === 0) fail(`${operation.id}.height must not be 0`);
  const origin = ctx.vector(operation.origin, `${operation.id}.origin`);
  return sketchProfile(operation.profile, operation.plane || "XY", origin, ctx, `${operation.id}.profile`).extrude(height);
}

export function loftFeature(operation, ctx) {
  if (!Array.isArray(operation.sections) || operation.sections.length < 2) fail(`${operation.id}.sections needs at least two sections`);
  if (operation.sections.length > 16) fail(`${operation.id}.sections allows at most 16 sections`);
  const plane = operation.plane || "XY";
  const sketches = operation.sections.map((section, index) => {
    const label = `${operation.id}.sections[${index}]`;
    if (section.profile?.holes) fail(`${label}: loft sections cannot have holes`);
    const origin = section.origin !== undefined ? ctx.vector(section.origin, `${label}.origin`) : ctx.resolve(section.offset ?? 0, `${label}.offset`);
    return sketchProfile(section.profile, section.plane || plane, origin, ctx, `${label}.profile`);
  });
  try {
    return sketches[0].loftWith(sketches.slice(1), { ruled: operation.ruled === true });
  } catch (error) {
    fail(`${operation.id}: loft failed (sections must be single closed outlines that do not cross): ${error?.message || error}`);
  }
}

// ---- holes ---------------------------------------------------------------

// Metric sizes: clearance (normal fit), tap drill (for cutting a thread),
// and heat-set insert hole (common brass inserts; check the insert's sheet).
export const THREADS = {
  M2: { clearance: 2.4, tap: 1.6, insert: 3.2, insert_depth: 3.0 },
  "M2.5": { clearance: 2.9, tap: 2.05, insert: 3.6, insert_depth: 4.0 },
  M3: { clearance: 3.4, tap: 2.5, insert: 4.0, insert_depth: 5.7 },
  M4: { clearance: 4.5, tap: 3.3, insert: 5.6, insert_depth: 8.1 },
  M5: { clearance: 5.5, tap: 4.2, insert: 6.4, insert_depth: 9.5 },
  M6: { clearance: 6.6, tap: 5.0, insert: 8.0, insert_depth: 12.7 },
  M8: { clearance: 9.0, tap: 6.8, insert: 10.0, insert_depth: 12.7 },
};

function normalize(v) {
  const length = Math.hypot(...v);
  if (length < 1e-9) fail("hole direction must not be zero");
  return v.map((x) => x / length);
}

// Builds the cutting tool along -Z with its top at z=0 (plus a small lead-in
// above the surface so the cut is clean), then turns -Z onto `direction`.
export function holeTool(operation, ctx, polygonSketch) {
  const label = operation.id;
  let diameter = operation.diameter === undefined ? undefined : ctx.resolve(operation.diameter, `${label}.diameter`);
  let depth = operation.depth === undefined ? undefined : ctx.resolve(operation.depth, `${label}.depth`);
  if (operation.thread) {
    const size = THREADS[operation.thread.size];
    if (!size) fail(`${label}.thread.size must be one of ${Object.keys(THREADS).join(", ")}`);
    const mode = operation.thread.mode || "clearance";
    if (!["clearance", "tap", "insert"].includes(mode)) fail(`${label}.thread.mode must be clearance, tap or insert`);
    if (diameter === undefined) diameter = size[mode];
    if (depth === undefined && mode === "insert") depth = size.insert_depth + 1;
  }
  if (!(diameter > 0)) fail(`${label}.diameter (or thread) is required and must be positive`);
  const through = operation.through === true || depth === undefined;
  if (through) depth = 1000;
  if (!(depth > 0)) fail(`${label}.depth must be positive`);
  const r = diameter / 2;
  const lead = 0.5;
  const profile = [[0, lead]];
  if (operation.counterbore) {
    const cb = operation.counterbore;
    const cr = ctx.resolve(cb.diameter, `${label}.counterbore.diameter`) / 2;
    const cd = ctx.resolve(cb.depth, `${label}.counterbore.depth`);
    if (cr <= r || cd <= 0 || cd >= depth) fail(`${label}.counterbore needs diameter > hole diameter and 0 < depth < hole depth`);
    profile.push([cr, lead], [cr, -cd], [r, -cd]);
  } else if (operation.countersink) {
    const cs = operation.countersink;
    const sr = ctx.resolve(cs.diameter, `${label}.countersink.diameter`) / 2;
    const angle = ctx.resolve(cs.angle ?? 90, `${label}.countersink.angle`);
    if (sr <= r || angle <= 0 || angle >= 180) fail(`${label}.countersink needs diameter > hole diameter and 0 < angle < 180`);
    const sinkDepth = (sr - r) / Math.tan((angle / 2) * Math.PI / 180);
    if (sinkDepth >= depth) fail(`${label}.countersink is deeper than the hole`);
    profile.push([sr, lead], [sr, 0], [r, -sinkDepth]);
  } else {
    profile.push([r, lead]);
  }
  profile.push([r, -depth], [0, -depth]);
  // Sketch (radius, z) in the XZ plane and revolve about Z.
  let tool = polygonSketch(profile, {}, `${label}.profile`, "XZ", [0, 0, 0]).revolve([0, 0, 1], { origin: [0, 0, 0], angle: 360 });
  const direction = normalize(operation.direction === undefined ? [0, 0, -1] : ctx.vector(operation.direction, `${label}.direction`));
  const dot = -direction[2];
  if (dot < 1 - 1e-9) {
    if (dot <= -1 + 1e-9) tool = tool.rotate(180, [0, 0, 0], [1, 0, 0]);
    else {
      // rotate -Z onto direction: axis = (-Z) x d, angle = acos(-Z . d)
      const axis = [direction[1], -direction[0], 0];
      tool = tool.rotate((Math.acos(dot) * 180) / Math.PI, [0, 0, 0], axis);
    }
  }
  return { tool, depth: through ? null : depth, diameter };
}

export function holeFeature(shape, operation, ctx, polygonSketch) {
  const positions = operation.positions ?? (operation.position !== undefined ? [operation.position] : undefined);
  if (!Array.isArray(positions) || !positions.length) fail(`${operation.id} needs position [x,y,z] or positions`);
  if (positions.length > 256) fail(`${operation.id}.positions allows at most 256 holes`);
  const { tool } = holeTool(operation, ctx, polygonSketch);
  let result = shape;
  positions.forEach((position, index) => {
    const at = ctx.vector(position, `${operation.id}.positions[${index}]`);
    result = result.cut(tool.clone().translate(at));
  });
  return result;
}
