import {geometryFor,rememberGeometry,inheritCache,meshFor} from "./geometry-cache.mjs";
import { loadAsset, inspect, topology, count } from "./inspection.mjs";
import initOpenCascade from "replicad-opencascadejs/src/replicad_single.js";
import {
  Transformation,
  cast,
  BoundingBox,
  getOC,
  draw,
  drawCircle,
  drawRectangle,
  exportSTEP,
  makeBox,
  makeCompound,
  makeCylinder,
  makeSphere,
  measureArea,
  measureShapeVolumeProperties,
  measureVolume,
  setOC,
} from "replicad";
import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

const SCHEMA = "apteva-design/v1";
const importedGeometry=new WeakSet();
const ENGINE_VERSION="replicad-0.23.1-ocjs-0.23.0-ds0.5";

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
    const profile=(definition.print_profiles||[]).find(p=>p.id===definition.fit_profile_id);
    const fitValue=spec.fit?profile?.fits_mm?.[spec.fit]:undefined;
    const candidate = Object.hasOwn(supplied || {}, name) ? supplied[name] : fitValue??spec.default;
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

function polygonSketch(points, params, label, plane = "XY", origin = [0, 0, 0]) {
  if (!Array.isArray(points) || points.length < 3) fail(`${label} needs at least three [x,y] points`);
  const resolved = points.map((point, index) => {
    if (!Array.isArray(point) || point.length !== 2) fail(`${label}[${index}] must be [x,y]`);
    return [resolve(point[0], params), resolve(point[1], params)];
  });
  let pen = draw(resolved[0]);
  for (const point of resolved.slice(1)) pen = pen.lineTo(point);
  return pen.close().sketchOnPlane(plane, vector(origin, params, `${label}.origin`));
}

function buildOperation(operation, shapes, params, assets, importedVisuals) {
  if (!operation || typeof operation !== "object") fail("every operation must be an object");
  if (!operation.id || typeof operation.id !== "string") fail("every operation requires a string id");
  if (shapes.has(operation.id)) fail(`duplicate operation id ${operation.id}`);

  let shape;
  switch (operation.type) {
    case "import_step": {
      const loaded=loadAsset(assets?.[operation.asset_id],operation.component_id);
      importedGeometry.add(loaded.shape);for(const v of loaded.visuals)importedGeometry.add(v.shape);
      shape=applyAssemblyTransform(loaded.shape,operation.transform||{},params);
      importedVisuals.set(operation.id,loaded.visuals.map(v=>({...v,shape:applyAssemblyTransform(v.shape,operation.transform||{},params)})));
      break;
    }
    case "swept_box": {
      const origin=vector(operation.origin,params,"swept_box.origin"),size=vector(operation.size,params,"swept_box.size"),travel=vector(operation.travel,params,"swept_box.travel");
      if(size.some(x=>x<=0))fail("swept_box size must be positive");
      const min=origin.map((x,k)=>Math.min(x,x+travel[k])),max=origin.map((x,k)=>Math.max(x+size[k],x+size[k]+travel[k]));
      shape=makeBox(min,max);break;
    }
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
    case "revolve_profile": {
      const sketch = polygonSketch(operation.points, params, `${operation.id}.points`, operation.plane || "XZ", operation.origin);
      shape = sketch.revolve(
        vector(operation.axis, params, `${operation.id}.axis`, [0, 0, 1]),
        { origin: vector(operation.axis_origin, params, `${operation.id}.axis_origin`), angle: resolve(operation.angle ?? 360, params, `${operation.id}.angle`) },
      );
      break;
    }
    case "sweep_circle": {
      if (!Array.isArray(operation.path) || operation.path.length < 2) fail(`${operation.id}.path needs at least two [x,y] points`);
      const path = operation.path.map((point, index) => {
        if (!Array.isArray(point) || point.length !== 2) fail(`${operation.id}.path[${index}] must be [x,y]`);
        return [resolve(point[0], params), resolve(point[1], params)];
      });
      let pen = draw(path[0]);
      for (const point of path.slice(1)) pen = pen.lineTo(point);
      const spine = pen.done().sketchOnPlane(operation.plane || "XY", vector(operation.origin, params, `${operation.id}.origin`));
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0) fail(`${operation.id}.radius must be positive`);
      shape = spine.sweepSketch((profilePlane, profileOrigin) => drawCircle(radius).sketchOnPlane(profilePlane, profileOrigin));
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
    case "mirror": {
      shape = shapeFor(operation.input, shapes).clone().mirror(
        operation.plane || "YZ",
        vector(operation.origin, params, `${operation.id}.origin`),
      );
      break;
    }
    case "linear_pattern": {
      const count = Math.trunc(resolve(operation.count, params, `${operation.id}.count`));
      if (count < 1 || count > 128) fail(`${operation.id}.count must be between 1 and 128`);
      const step = vector(operation.step, params, `${operation.id}.step`);
      const copies = [];
      for (let index = 0; index < count; index += 1) {
        copies.push(shapeFor(operation.input, shapes).clone().translate(step.map((value) => value * index)));
      }
      shape = copies[0];
      for (const copy of copies.slice(1)) shape = operation.fuse ? shape.fuse(copy) : null;
      if (!operation.fuse) shape = makeCompound(copies);
      break;
    }
    case "circular_pattern": {
      const count = Math.trunc(resolve(operation.count, params, `${operation.id}.count`));
      if (count < 1 || count > 128) fail(`${operation.id}.count must be between 1 and 128`);
      const totalAngle = resolve(operation.angle ?? 360, params, `${operation.id}.angle`);
      const center = vector(operation.center, params, `${operation.id}.center`);
      const direction = vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]);
      const copies = [];
      for (let index = 0; index < count; index += 1) {
        copies.push(shapeFor(operation.input, shapes).clone().rotate(totalAngle * index / count, center, direction));
      }
      shape = copies[0];
      for (const copy of copies.slice(1)) shape = operation.fuse ? shape.fuse(copy) : null;
      if (!operation.fuse) shape = makeCompound(copies);
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

function exactAssemblyTransform(shape,transform,params){
 let output=shape.clone();
 if(transform?.scale!==undefined)output=output.scale(resolve(transform.scale,params,"assembly scale"),[0,0,0]);
 for(const r of transform?.rotate||[])output=output.rotate(resolve(r.angle,params,"assembly angle"),vector(r.center,params,"rotation center"),vector(r.direction,params,"rotation axis",[0,0,1]));
 if(transform?.translate)output=output.translate(vector(transform.translate,params,"assembly translation"));
 return output;
}
function applyAssemblyTransform(shape, transform, params) {
  if(!importedGeometry.has(shape)||(transform?.scale!==undefined&&resolve(transform.scale,params)!==1))return exactAssemblyTransform(shape,transform,params);
  let output=shape.clone();
  if(transform?.scale!==undefined)output=output.scale(resolve(transform.scale,params,"assembly scale"),[0,0,0]);
  // Rigid placement uses OCCT locations instead of copying all B-rep geometry.
  // This matters for manufacturer assemblies containing tens of thousands of faces.
  function placed(t){const oc=getOC(),location=new oc.TopLoc_Location_2(t.wrapped);try{const next=cast(output.wrapped.Moved(location,true));output.delete();output=next;}finally{location.delete();t.delete();}}
  for(const r of transform?.rotate||[])placed(new Transformation().rotate(resolve(r.angle,params,"assembly rotation angle"),vector(r.center,params,"rotation center"),vector(r.direction,params,"rotation axis",[0,0,1])));
  if(transform?.translate)placed(new Transformation().translate(vector(transform.translate,params,"assembly translation")));
  importedGeometry.add(output);inheritCache(shape,output,transform||{},params);
  const cached=geometryFor(shape);
  if(cached){const physical=transformPhysical({mass:0,center:cached.center_of_mass||[0,0,0]},transform||{},params);rememberGeometry(output,{...cached,center_of_mass:physical.center});}
  return output;
}

function rotatePoint(point, angleDegrees, center, axis) {
  const radians = angleDegrees * Math.PI / 180;
  const length = Math.hypot(...axis);
  if (length === 0) fail("assembly rotation direction must not be zero");
  const [ux, uy, uz] = axis.map((value) => value / length);
  const [x, y, z] = point.map((value, index) => value - center[index]);
  const cosine = Math.cos(radians), sine = Math.sin(radians), dot = ux * x + uy * y + uz * z;
  return [
    center[0] + x * cosine + (uy * z - uz * y) * sine + ux * dot * (1 - cosine),
    center[1] + y * cosine + (uz * x - ux * z) * sine + uy * dot * (1 - cosine),
    center[2] + z * cosine + (ux * y - uy * x) * sine + uz * dot * (1 - cosine),
  ];
}

function transformPhysical(physical, transform, params) {
  if (!physical) return null;
  let center = [...physical.center];
  let mass = physical.mass;
  if (transform?.scale !== undefined) {
    const factor = resolve(transform.scale, params, "assembly scale");
    center = center.map((value) => value * factor);
    mass *= Math.abs(factor ** 3);
  }
  for (const rotation of transform?.rotate || []) {
    center = rotatePoint(
      center,
      resolve(rotation.angle, params, "assembly rotation angle"),
      vector(rotation.center, params, "assembly rotation center"),
      vector(rotation.direction, params, "assembly rotation direction", [0, 0, 1]),
    );
  }
  if (transform?.translate) {
    const translation = vector(transform.translate, params, "assembly translation");
    center = center.map((value, index) => value + translation[index]);
  }
  return { mass, center };
}

function hexColor(value, fallback = "#64748b") {
  return typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value) ? value : fallback;
}

function assemblyGeometry(definition, shapes, partShapes, partPhysical, partVisuals, params, importedVisuals) {
  const parts = new Map((definition.parts || []).map((part) => [part.id, part]));
  const materials = new Map((definition.materials || []).map((material) => [material.id, material]));
  if (!definition.assembly?.instances?.length) {
    const shape = shapeFor(definition.output, shapes);
    const outputPart = (definition.parts || []).find((part) => part.output === definition.output);
    const visuals = outputPart ? partVisuals.get(outputPart.id) : importedVisuals.get(definition.output) || [{ id: definition.output, part_id: definition.output, name: definition.output, color: "#64748b", shape }];
    return { shape, instances: [], visuals, parts, materials };
  }
  const instances = [];
  const pending=[...definition.assembly.instances];
  let attempts=0;
  while(pending.length) {
    if(++attempts>definition.assembly.instances.length**2+1)fail("Datum alignment dependency cycle");
    const instance=pending.shift();
    if(instance.alignment && !instances.some(i=>i.id===instance.alignment.target_instance)){pending.push(instance);continue;}

    const part = parts.get(instance.part_id);
    if (!part) fail(`assembly instance ${instance.id} references unknown part ${instance.part_id}`);
    const sourceShape = partShapes.get(part.id);
    if (!sourceShape) fail(`assembly part ${part.id} has no resolved geometry`);
    let resolvedTransform=instance.transform||{};
    if(instance.alignment) {
      const a=instance.alignment,target=instances.find(i=>i.id===a.target_instance);
      const point=(p,t)=>transformPhysical({mass:0,center:vector(p,params,"datum")},t,params).center;
      const local=point(a.local_point,resolvedTransform),world=point(a.target_point,target.resolvedTransform),offset=vector(a.offset,params,"datum offset");
      const delta=world.map((v,k)=>v+offset[k]-local[k]);
      const old=vector(resolvedTransform.translate,params,"translation");resolvedTransform={...resolvedTransform,translate:old.map((v,k)=>v+delta[k])};
    }
    if(part.manufacturing?.classification==='purchased' && resolvedTransform.scale!==undefined && resolve(resolvedTransform.scale,params)!==1)fail('Purchased components require rigid placement; normalize import units separately');
    const shape = applyAssemblyTransform(sourceShape, resolvedTransform, params);
    const material = materials.get(part.material_id);
    const visuals = (partVisuals.get(part.id) || [{ id: part.id, part_id: part.id, name: part.name, color: hexColor(part.color || material?.color), shape: sourceShape }]).map((visual) => ({
      ...visual,
      id: visual.id === part.id ? instance.id : `${instance.id}/${visual.id}`,
      shape: visual.shape === sourceShape ? shape : applyAssemblyTransform(visual.shape, resolvedTransform, params),
      sourceShape:visual.shape,resolvedTransform,
    }));
    instances.push({
      id: instance.id,
      visible: instance.visible !== false, resolvedTransform, sourceShape,
      part,
      material,
      shape,
      color: hexColor(part.color || material?.color),
      physical: transformPhysical(partPhysical.get(part.id), resolvedTransform, params),
      visuals,
    });
  }
  if (!instances.length) fail("assembly has no visible instances");
  const assemblyShape=makeCompound(instances.map((item)=>item.shape.clone()));if(instances.every(i=>importedGeometry.has(i.shape)))importedGeometry.add(assemblyShape);
  if(instances.every(i=>geometryFor(i.shape))){
    const gs=instances.map(i=>geometryFor(i.shape)),volume=gs.reduce((s,g)=>s+g.volume_mm3,0);
    const solids=gs.reduce((s,g)=>s+(g.topology?.[0]?.solid_count||0),0),valid=gs.every(g=>g.topology?.[0]?.status==='pass');
    rememberGeometry(assemblyShape,{volume_mm3:volume,surface_area_mm2:gs.reduce((s,g)=>s+g.surface_area_mm2,0),center_of_mass:[0,1,2].map(k=>gs.reduce((s,g)=>s+g.volume_mm3*g.center_of_mass[k],0)/volume),face_count:gs.reduce((s,g)=>s+g.face_count,0),edge_count:gs.reduce((s,g)=>s+g.edge_count,0),topology:[{valid,solid_count:solids,shell_count:gs.reduce((s,g)=>s+(g.topology?.[0]?.shell_count||0),0),status:valid?'pass':'indeterminate'}]});
  }

  return { shape: assemblyShape, instances, visuals: instances.flatMap((item) => item.visuals), parts, materials };
}

function validateDefinition(definition) {
  if (!definition || typeof definition !== "object") fail("definition must be an object");
  if (definition.schema !== SCHEMA) fail(`schema must be ${SCHEMA}`);
  if ((definition.units || "mm") !== "mm") fail("v0.1 supports millimetres only");
  if (!Array.isArray(definition.operations)) fail("definition.operations must be an array");
  if (definition.operations.length === 0 && !definition.assembly?.instances?.length) {
    fail("definition requires operations or an assembly with instances");
  }
  if ((!definition.output || typeof definition.output !== "string") && !definition.assembly) fail("definition.output is required outside an assembly");
  if (definition.operations.length > 256) fail("definition exceeds the 256-operation limit");
}

function buildDefinitionGeometry(definition, supplied) {
  validateDefinition(definition);
  const params = parameterValues(definition, supplied || {});
  const shapes = new Map(), importedVisuals=new Map();
  for (const operation of definition.operations) buildOperation(operation, shapes, params, definition._assets, importedVisuals);
  const resolved = new Map((definition._resolved_components || []).map((component) => [component.part_id, component]));
  const partShapes = new Map();
  const partPhysical = new Map();
  const partVisuals = new Map();
  const materials = new Map((definition.materials || []).map((material) => [material.id, material]));
  for (const part of definition.parts || []) {
    if (part.source) {
      const component = resolved.get(part.id);
      if (!component) fail(`linked part ${part.id} was not resolved by Design Studio`);
      const child = buildDefinitionGeometry(component.definition, component.parameters || {});
      const sourcePart=component.source_part_id?(component.definition.parts||[]).find(p=>p.id===component.source_part_id):null;
      if(sourcePart && !part.manufacturing) part.manufacturing=sourcePart.manufacturing;
      if(!component.source_part_id && (component.definition.parts||[]).some(p=>['purchased','reference'].includes(p.manufacturing?.classification)||p._exclude_print))part._exclude_print=true;

      const selected = component.source_part_id ? child.partShapes.get(component.source_part_id) : child.assembly.shape;
      if (!selected) fail(`linked part ${part.id} source part ${component.source_part_id} has no geometry`);
      const selectedShape=selected.clone();if(importedGeometry.has(selected))importedGeometry.add(selectedShape);
      inheritCache(selected,selectedShape);
      partShapes.set(part.id, selectedShape);
      const physical = component.source_part_id ? child.partPhysical.get(component.source_part_id) : child.physical;
      if (physical) partPhysical.set(part.id, physical);
      const visuals = component.source_part_id ? child.partVisuals.get(component.source_part_id) : child.assembly.visuals;
      if (visuals?.length) partVisuals.set(part.id, visuals);
    } else {
      const partShape = shapeFor(part.output, shapes);
      partShapes.set(part.id, partShape);
      const density = Number(materials.get(part.material_id)?.density_g_cm3 || 0);
      partPhysical.set(part.id, { mass: density > 0 ? cachedVolume(partShape) * density / 1000 : 0, center: exactCenterOfMass(partShape) });
      partVisuals.set(part.id, importedVisuals.get(part.output)?.map(v=>({...v,part_id:part.id,color:hexColor(part.color)})) || [{ id: part.id, part_id: part.id, name: part.name, color: hexColor(part.color || materials.get(part.material_id)?.color), shape: partShape }]);
    }
  }
  const assembly = assemblyGeometry(definition, shapes, partShapes, partPhysical, partVisuals, params, importedVisuals);
  let physical;
  if (assembly.instances.length) {
    const mass = assembly.instances.reduce((sum, item) => sum + (item.physical?.mass || 0), 0);
    const center = mass > 0 ? [0, 1, 2].map((axis) => assembly.instances.reduce((sum, item) => sum + (item.physical?.center[axis] || 0) * (item.physical?.mass || 0), 0) / mass) : exactCenterOfMass(assembly.shape);
    physical = { mass, center };
  } else {
    const matching = (definition.parts || []).find((part) => part.output === definition.output);
    physical = matching ? partPhysical.get(matching.id) : { mass: 0, center: exactCenterOfMass(assembly.shape) };
  }
  return { params, shapes, partShapes, partPhysical, partVisuals, assembly, physical };
}

async function writeBlob(path, blob) {
  await mkdir(dirname(path), { recursive: true });
  await Bun.write(path, new Uint8Array(await blob.arrayBuffer()));
}

function shapeBounds(shape) {
  const box=new BoundingBox();
  try{getOC().BRepBndLib.AddOptimal(shape.wrapped,box.wrapped,false,false);
    const [min,max]=box.bounds;return {min,max,size:max.map((v,k)=>v-min[k]),center:max.map((v,k)=>(v+min[k])/2)};
  }finally{box.delete();}
}

function cachedVolume(shape){return geometryFor(shape)?.volume_mm3??measureVolume(shape)}
function exactCenterOfMass(shape) {
  if(geometryFor(shape))return geometryFor(shape).center_of_mass;
  const properties = measureShapeVolumeProperties(shape);
  try { return properties.centerOfMass; }
  finally { properties.delete(); }
}

function geometryReport(shape, mesh, instances = []) {
  const bounds=shapeBounds(shape);
  const volume = cachedVolume(shape);
  const area = geometryFor(shape)?.surface_area_mm2??measureArea(shape);
  const partReports = instances.map((item) => {
    const partVolume = cachedVolume(item.shape);
    const bounds = shapeBounds(item.shape);
    const center = item.physical?.center || exactCenterOfMass(item.shape);
    return {
      part_id: item.part.id,
      instance_id: item.id,
      name: item.part.name,
      material_id: item.part.material_id || "",
      volume_mm3: partVolume,
      mass_g: item.physical?.mass || 0,
      center_of_mass: center,
      bounds,
    };
  });
  const mass = partReports.reduce((sum, item) => sum + item.mass_g, 0);
  const centerOfMass = mass > 0 ? [0, 1, 2].map((axis) => partReports.reduce((sum, item) => sum + item.center_of_mass[axis] * item.mass_g, 0) / mass) : exactCenterOfMass(shape);
  return {
    valid: Number.isFinite(volume) && volume > 0 && mesh.triangles.length >= 3,
    representation: "brep",
    units: "mm",
    bounds,
    volume_mm3: volume,
    surface_area_mm2: area,
    body_count: instances.length || 1,
    face_count: geometryFor(shape)?.face_count??count(shape,"TopAbs_FACE"),
    edge_count: geometryFor(shape)?.edge_count??count(shape,"TopAbs_EDGE"),
    vertex_count: mesh.vertices.length / 3,
    triangle_count: mesh.triangles.length / 3,
    mass_g: mass,
    center_of_mass: centerOfMass,
    parts: partReports,
  };
}

function printOrientation(shape,part,params){
  const angles=part.manufacturing?.orientation;
  if(!angles)return shape;
  if(angles.length!==3)fail('manufacturing.orientation must be Euler X/Y/Z degrees');
  let out=shape.clone();for(let axis=0;axis<3;axis++){const direction=[0,0,0];direction[axis]=1;out=out.rotate(resolve(angles[axis],params),[0,0,0],direction);}
  const min=out.boundingBox.bounds[0];return out.translate(min.map(v=>-v));
}
async function run(request) {
  if(request.expected_engine_version&&request.expected_engine_version!==ENGINE_VERSION)fail(`Package needs engine ${request.expected_engine_version}; this is ${ENGINE_VERSION}`);
  const built = buildDefinitionGeometry(request.definition, request.parameters || {});
  const { params, partShapes, assembly } = built;
  const shape = assembly.shape;
  const tolerance = finite(request.tolerance ?? 0.1, "tolerance");
  const angularTolerance = finite(request.angular_tolerance ?? 0.1, "angular_tolerance");
  // Mesh leaf components independently. Large manufacturer compounds can exceed
  // OCCT's compound tessellator limits even when every leaf meshes successfully.
  const partMeshes=assembly.visuals.map(item=>{
    let visualMesh=meshFor(item.shape,vector,resolve,rotatePoint);
    if(!visualMesh){try{visualMesh=item.shape.mesh({tolerance,angularTolerance});}catch(e){fail(`Preview failed for ${item.name}: ${String(e)}`);}
      const faces=item.shape.faces,faceIndices=new Map(faces.map((f,index)=>[f.hashCode,index]));
      visualMesh.faceGroups=visualMesh.faceGroups.map(g=>({...g,faceId:faceIndices.get(g.faceId)}));for(const f of faces)f.delete();
    }
    return {id:item.id,part_id:item.part_id,name:item.name,color:item.color,visible:!assembly.instances.some(i=>i.visible===false&&(item.id===i.id||item.id.startsWith(i.id+'/'))),instance_id:assembly.instances.find(i=>item.id===i.id||item.id.startsWith(i.id+'/'))?.id,...visualMesh};
  });
  const mesh={vertices:[],triangles:[],normals:[]};
  for(const part of partMeshes){const offset=mesh.vertices.length/3;for(const v of part.vertices)mesh.vertices.push(v);for(const n of part.normals||[])mesh.normals.push(n);for(const i of part.triangles)mesh.triangles.push(i+offset);}

  let report;try{report=geometryReport(shape, mesh, assembly.instances);}catch(e){fail(`Geometry measurements failed: ${String(e)}`);}
  const inspection=inspect(request.definition,built,applyAssemblyTransform,exactAssemblyTransform);
  report.topology=inspection.topology; report.exact_checks=inspection.exact_checks;
  report.body_count=inspection.topology.reduce((s,t)=>s+t.solid_count,0);

  if (!report.valid) fail("output geometry is empty or invalid", report);

  const outputDir = request.output_dir;
  if (!outputDir || typeof outputDir !== "string") fail("output_dir is required");
  await mkdir(outputDir, { recursive: true });
  const artifacts = [];
  const formats = new Set(request.formats || ["mesh-json"]);

  if (formats.has("mesh-json")) {
    const path = join(outputDir, "model.mesh.json");
    await Bun.write(path, JSON.stringify({ ...mesh, parts: partMeshes, keepouts: inspection.keepouts.map(k=>({id:k.id,name:k.name,kind:k.kind,color:k.color,owner_instance:k.owner_instance,...k.shape.mesh({tolerance,angularTolerance})})) }));
    artifacts.push({ format: "mesh-json", path, content_type: "application/json" });
    for (const part of request.definition.parts || []) {
      if (part._exclude_print || ["purchased", "reference"].includes(part.manufacturing?.classification)) continue;
      const partPath = join(outputDir, `${part.id}.mesh.json`);
      const rawPartShape = partShapes.get(part.id);
      const partShape = rawPartShape && printOrientation(rawPartShape,part,params);
	  if (!partShape) fail(`part ${part.id} has no export geometry`);
      const partMesh = partShape.mesh({ tolerance, angularTolerance });
      await Bun.write(partPath, JSON.stringify({ ...partMesh }));
      artifacts.push({ format: "mesh-json", path: partPath, content_type: "application/json", part_id: part.id, part_name: part.name });
    }
  }
  if (formats.has("step")) {
    const path = join(outputDir, "model.step");
    const stepShapes = assembly.instances.length ? assembly.instances.map((item) => ({ shape: item.shape, name: item.id })) : [{ shape, name: request.name || "Design" }];
    await writeBlob(path, exportSTEP(stepShapes, { unit: "MM", modelUnit: "MM" }));
    artifacts.push({ format: "step", path, content_type: "model/step" });
    for (const part of request.definition.parts || []) {
      if (part._exclude_print || ["purchased", "reference"].includes(part.manufacturing?.classification)) continue;
      const partPath = join(outputDir, `${part.id}.step`);
      const rawPartShape = partShapes.get(part.id);
      const partShape = rawPartShape && printOrientation(rawPartShape,part,params);
      if (!partShape) fail(`part ${part.id} has no export geometry`);
      await writeBlob(partPath, exportSTEP([{ shape: partShape, name: part.name }], { unit: "MM", modelUnit: "MM" }));
      artifacts.push({ format: "step", path: partPath, content_type: "model/step", part_id: part.id, part_name: part.name });
    }
  }
  if (formats.has("stl")) {
    const printable=assembly.instances.filter(i=>!['purchased','reference'].includes(i.part.manufacturing?.classification));
    const printShape=assembly.instances.length?(printable.length?makeCompound(printable.map(i=>i.shape.clone())):null):((request.definition.parts||[]).some(p=>['purchased','reference'].includes(p.manufacturing?.classification))?null:shape);
    const path = join(outputDir, "model.stl");
    if(printShape && !request.definition.assembly) await writeBlob(path, printShape.blobSTL({ tolerance, angularTolerance, binary: true }));
    if(printShape && !request.definition.assembly) artifacts.push({ format: "stl", path, content_type: "model/stl" });
    for (const part of request.definition.parts || []) {
      if (part._exclude_print || ["purchased", "reference"].includes(part.manufacturing?.classification)) continue;
      const partPath = join(outputDir, `${part.id}.stl`);
      const rawPartShape = partShapes.get(part.id);
      const partShape = rawPartShape && printOrientation(rawPartShape,part,params);
      if (!partShape) fail(`part ${part.id} has no export geometry`);
      await writeBlob(partPath, partShape.blobSTL({ tolerance, angularTolerance, binary: true }));
      artifacts.push({ format: "stl", path: partPath, content_type: "model/stl", part_id: part.id, part_name: part.name });
    }
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
