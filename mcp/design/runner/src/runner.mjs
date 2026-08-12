import initOpenCascade from "replicad-opencascadejs/src/replicad_single.js";
import {
  draw,
  drawCircle,
  drawRectangle,
  exportSTEP,
  makeBox,
  makeCompound,
  makeCylinder,
  makeSphere,
  measureArea,
  measureVolume,
  setOC,
} from "replicad";
import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

const SCHEMA = "apteva-design/v1";

function fail(message, details = undefined) {
  const err = new Error(message);
  if (details !== undefined) err.details = details;
  throw err;
}

function finite(value, label) {
  const number = Number(value);
  if (!Number.isFinite(number)) fail(`${label} must resolve to a finite number`);
  return number;
}

class ExpressionParser {
  constructor(source, params) {
    this.source = source;
    this.params = params;
    this.index = 0;
  }

  parse() {
    const result = this.expression();
    this.space();
    if (this.index !== this.source.length) {
      fail(`unexpected token in expression ${JSON.stringify(this.source)} at ${this.index}`);
    }
    return finite(result, `expression ${JSON.stringify(this.source)}`);
  }

  expression() {
    let value = this.term();
    for (;;) {
      this.space();
      if (this.take("+")) value += this.term();
      else if (this.take("-")) value -= this.term();
      else return value;
    }
  }

  term() {
    let value = this.unary();
    for (;;) {
      this.space();
      if (this.take("*")) value *= this.unary();
      else if (this.take("/")) {
        const divisor = this.unary();
        if (divisor === 0) fail(`division by zero in expression ${JSON.stringify(this.source)}`);
        value /= divisor;
      } else return value;
    }
  }

  unary() {
    this.space();
    if (this.take("+")) return this.unary();
    if (this.take("-")) return -this.unary();
    return this.primary();
  }

  primary() {
    this.space();
    if (this.take("(")) {
      const value = this.expression();
      this.space();
      if (!this.take(")")) fail(`missing ')' in expression ${JSON.stringify(this.source)}`);
      return value;
    }
    const number = this.match(/^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/);
    if (number) return Number(number);
    const identifier = this.match(/^[A-Za-z_][A-Za-z0-9_]*/);
    if (identifier) {
      if (!Object.hasOwn(this.params, identifier)) fail(`unknown parameter ${identifier}`);
      return finite(this.params[identifier], `parameter ${identifier}`);
    }
    fail(`expected number, parameter, or '(' in expression ${JSON.stringify(this.source)} at ${this.index}`);
  }

  match(pattern) {
    const found = this.source.slice(this.index).match(pattern);
    if (!found) return "";
    this.index += found[0].length;
    return found[0];
  }

  take(value) {
    if (!this.source.startsWith(value, this.index)) return false;
    this.index += value.length;
    return true;
  }

  space() {
    while (/\s/.test(this.source[this.index] || "")) this.index += 1;
  }
}

function resolve(value, params, label = "value") {
  if (typeof value === "number") return finite(value, label);
  if (typeof value === "string") return new ExpressionParser(value, params).parse();
  fail(`${label} must be a number or arithmetic parameter expression`);
}

function vector(value, params, label, fallback = [0, 0, 0]) {
  const source = value ?? fallback;
  if (!Array.isArray(source) || source.length !== 3) fail(`${label} must contain three values`);
  return source.map((part, index) => resolve(part, params, `${label}[${index}]`));
}

function parameterValues(definition, supplied) {
  const output = {};
  for (const [name, spec] of Object.entries(definition.parameters || {})) {
    const candidate = Object.hasOwn(supplied || {}, name) ? supplied[name] : spec.default;
    const value = finite(candidate, `parameter ${name}`);
    if (spec.min !== undefined && value < finite(spec.min, `${name}.min`)) {
      fail(`parameter ${name} is below its minimum`);
    }
    if (spec.max !== undefined && value > finite(spec.max, `${name}.max`)) {
      fail(`parameter ${name} is above its maximum`);
    }
    output[name] = value;
  }
  for (const name of Object.keys(supplied || {})) {
    if (!Object.hasOwn(output, name)) fail(`unknown parameter ${name}`);
  }
  return output;
}

function shapeFor(id, shapes) {
  const shape = shapes.get(id);
  if (!shape) fail(`operation references unknown shape ${id}`);
  return shape;
}

function buildOperation(operation, shapes, params) {
  if (!operation || typeof operation !== "object") fail("every operation must be an object");
  if (!operation.id || typeof operation.id !== "string") fail("every operation requires a string id");
  if (shapes.has(operation.id)) fail(`duplicate operation id ${operation.id}`);

  let shape;
  switch (operation.type) {
    case "box": {
      const [width, depth, height] = vector(operation.size, params, `${operation.id}.size`);
      if (width <= 0 || depth <= 0 || height <= 0) fail(`${operation.id}.size must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = makeBox([x, y, z], [x + width, y + depth, z + height]);
      break;
    }
    case "cylinder": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (radius <= 0 || height <= 0) fail(`${operation.id} radius and height must be positive`);
      shape = makeCylinder(
        radius,
        height,
        vector(operation.origin, params, `${operation.id}.origin`),
        vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]),
      );
      break;
    }
    case "sphere": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0) fail(`${operation.id}.radius must be positive`);
      shape = makeSphere(radius).translate(vector(operation.center, params, `${operation.id}.center`));
      break;
    }
    case "extrude_rectangle": {
      const width = resolve(operation.width, params, `${operation.id}.width`);
      const depth = resolve(operation.depth, params, `${operation.id}.depth`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (width <= 0 || depth <= 0 || height <= 0) fail(`${operation.id} dimensions must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = drawRectangle(width, depth).translate(x + width / 2, y + depth / 2).sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "extrude_circle": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (radius <= 0 || height <= 0) fail(`${operation.id} radius and height must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = drawCircle(radius).translate(x, y).sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "extrude_polygon": {
      if (!Array.isArray(operation.points) || operation.points.length < 3) {
        fail(`${operation.id}.points needs at least three [x,y] points`);
      }
      const points = operation.points.map((point, index) => {
        if (!Array.isArray(point) || point.length !== 2) fail(`${operation.id}.points[${index}] must be [x,y]`);
        return [resolve(point[0], params), resolve(point[1], params)];
      });
      let pen = draw(points[0]);
      for (const point of points.slice(1)) pen = pen.lineTo(point);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      const z = resolve(operation.z ?? 0, params, `${operation.id}.z`);
      shape = pen.close().sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "fuse":
    case "cut":
    case "intersect": {
      const ids = operation.inputs || [operation.base, ...(operation.tools || [])];
      if (!Array.isArray(ids) || ids.length < 2 || ids.some((id) => typeof id !== "string")) {
        fail(`${operation.id}.${operation.type} needs at least two input ids`);
      }
      shape = shapeFor(ids[0], shapes).clone();
      for (const id of ids.slice(1)) {
        const tool = shapeFor(id, shapes).clone();
        shape = operation.type === "fuse" ? shape.fuse(tool) : operation.type === "cut" ? shape.cut(tool) : shape.intersect(tool);
      }
      break;
    }
    case "compound": {
      const ids = operation.inputs;
      if (!Array.isArray(ids) || ids.length === 0) fail(`${operation.id}.inputs is required`);
      shape = makeCompound(ids.map((id) => shapeFor(id, shapes).clone()));
      break;
    }
    case "translate": {
      shape = shapeFor(operation.input, shapes).clone().translate(vector(operation.vector, params, `${operation.id}.vector`));
      break;
    }
    case "rotate": {
      shape = shapeFor(operation.input, shapes).clone().rotate(
        resolve(operation.angle, params, `${operation.id}.angle`),
        vector(operation.center, params, `${operation.id}.center`),
        vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]),
      );
      break;
    }
    case "scale": {
      shape = shapeFor(operation.input, shapes).clone().scale(
        resolve(operation.factor, params, `${operation.id}.factor`),
        vector(operation.center, params, `${operation.id}.center`),
      );
      break;
    }
    case "fillet": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0) fail(`${operation.id}.radius must be positive`);
      shape = shapeFor(operation.input, shapes).clone().fillet(radius);
      break;
    }
    case "chamfer": {
      const distance = resolve(operation.distance, params, `${operation.id}.distance`);
      if (distance <= 0) fail(`${operation.id}.distance must be positive`);
      shape = shapeFor(operation.input, shapes).clone().chamfer(distance);
      break;
    }
    default:
      fail(`unsupported operation type ${JSON.stringify(operation.type)}`);
  }
  shapes.set(operation.id, shape);
}

function validateDefinition(definition) {
  if (!definition || typeof definition !== "object") fail("definition must be an object");
  if (definition.schema !== SCHEMA) fail(`schema must be ${SCHEMA}`);
  if ((definition.units || "mm") !== "mm") fail("v0.1 supports millimetres only");
  if (!Array.isArray(definition.operations) || definition.operations.length === 0) {
    fail("definition.operations must be a non-empty array");
  }
  if (!definition.output || typeof definition.output !== "string") fail("definition.output is required");
  if (definition.operations.length > 256) fail("definition exceeds the 256-operation limit");
}

async function writeBlob(path, blob) {
  await mkdir(dirname(path), { recursive: true });
  await Bun.write(path, new Uint8Array(await blob.arrayBuffer()));
}

function geometryReport(shape, mesh) {
  const box = shape.boundingBox;
  const [min, max] = box.bounds;
  const volume = measureVolume(shape);
  const area = measureArea(shape);
  return {
    valid: Number.isFinite(volume) && volume > 0 && mesh.triangles.length >= 3,
    representation: "brep",
    units: "mm",
    bounds: { min, max, size: [box.width, box.height, box.depth], center: box.center },
    volume_mm3: volume,
    surface_area_mm2: area,
    body_count: 1,
    face_count: shape.faces.length,
    edge_count: shape.edges.length,
    vertex_count: mesh.vertices.length / 3,
    triangle_count: mesh.triangles.length / 3,
  };
}

async function run(request) {
  validateDefinition(request.definition);
  const params = parameterValues(request.definition, request.parameters || {});
  const shapes = new Map();
  for (const operation of request.definition.operations) buildOperation(operation, shapes, params);
  const shape = shapeFor(request.definition.output, shapes);
  const tolerance = finite(request.tolerance ?? 0.1, "tolerance");
  const angularTolerance = finite(request.angular_tolerance ?? 0.1, "angular_tolerance");
  const mesh = shape.mesh({ tolerance, angularTolerance });
  const report = geometryReport(shape, mesh);
  if (!report.valid) fail("output geometry is empty or invalid", report);

  const outputDir = request.output_dir;
  if (!outputDir || typeof outputDir !== "string") fail("output_dir is required");
  await mkdir(outputDir, { recursive: true });
  const artifacts = [];
  const formats = new Set(request.formats || ["mesh-json"]);

  if (formats.has("mesh-json")) {
    const path = join(outputDir, "model.mesh.json");
    await Bun.write(path, JSON.stringify({ ...mesh, edges: shape.meshEdges({ tolerance, angularTolerance }) }));
    artifacts.push({ format: "mesh-json", path, content_type: "application/json" });
  }
  if (formats.has("step")) {
    const path = join(outputDir, "model.step");
    await writeBlob(path, exportSTEP([{ shape, name: request.name || "Design" }], { unit: "MM", modelUnit: "MM" }));
    artifacts.push({ format: "step", path, content_type: "model/step" });
  }
  if (formats.has("stl")) {
    const path = join(outputDir, "model.stl");
    await writeBlob(path, shape.blobSTL({ tolerance, angularTolerance, binary: true }));
    artifacts.push({ format: "stl", path, content_type: "model/stl" });
  }

  return { parameters: params, report, artifacts };
}

async function main() {
  const wasmPath = join(import.meta.dir, "replicad_single.wasm");
  const oc = await initOpenCascade({ locateFile: () => wasmPath });
  setOC(oc);
  const request = await Bun.stdin.json();
  return run(request);
}

try {
  const result = await main();
  process.stdout.write(`${JSON.stringify({ ok: true, ...result })}\n`);
  // OpenCascade's embind finalizers can run after the WASM instance begins
  // shutting down and attempt a stale indirect call. The one-shot runner owns
  // the whole process, so exit after the structured result is flushed instead
  // of asking the JS GC to tear down thousands of kernel wrappers.
  process.exit(0);
} catch (error) {
  process.stdout.write(`${JSON.stringify({
    ok: false,
    error: error instanceof Error ? error.message : String(error),
    details: error?.details,
  })}\n`);
  process.exitCode = 1;
}
