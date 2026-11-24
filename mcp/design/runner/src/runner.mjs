import {geometryFor,rememberGeometry,inheritCache,meshFor} from "./geometry-cache.mjs";
import { ExpressionParser } from "./expressions.mjs";
import { IDENTITY, applyPose, applyR, compose, interfaceFrame, matePose, poseFromTransform, rotationAbout, transformFromPose, unit } from "./kinematics.mjs";
import { edgeFeature, shellFeature, extrudeProfile, loftFeature, holeFeature, FONTS, loadedFonts } from "./features.mjs";
import { flattenPath } from "./svgpath.mjs";
import { analyzeMoulding } from "./moulding.mjs";
import { exactPair, boxDistance, loadAsset, inspect, topology, count } from "./inspection.mjs";
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
  setOC, loadFont, importSTL, drawProjection } from "replicad";
import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

const SCHEMA = "apteva-design/v1";
const importedGeometry=new WeakSet();
const ENGINE_VERSION="replicad-0.23.1-ocjs-0.23.0-ds0.10";

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

function resolve(value, params, label = "value") {
  if (typeof value === "number") return finite(value, label);
  if (typeof value === "boolean") return value ? 1 : 0;
  if (typeof value === "string") return new ExpressionParser(value, params).parse();
  fail(`${label} must be a number or parameter expression`);
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
    const value = typeof candidate === "boolean" ? (candidate ? 1 : 0) : finite(candidate, `parameter ${name}`);
    if (spec.type === "boolean" && value !== 0 && value !== 1) fail(`parameter ${name} must be true/false (1/0)`);
    if (spec.type === "integer" && !Number.isInteger(value)) fail(`parameter ${name} must be an integer`);
    if (Array.isArray(spec.options) && spec.options.length && !spec.options.some((option) => Math.abs(Number(option) - value) <= 1e-9)) {
      fail(`parameter ${name} must be one of ${spec.options.join(", ")}`);
    }
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

const SKIPPED = Symbol("skipped");
const stlShapes = new Map();

function shapeFor(id, shapes) {
  const shape = shapes.get(id);
  if (shape === SKIPPED) fail(`operation references ${id}, which is skipped because its when condition is false`);
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

function featureContext(params, shape) {
  let bounds;
  return {
    resolve: (value, label) => resolve(value, params, label),
    vector: (value, label) => vector(value, params, label),
    bounds: () => {
      if (!shape) fail("side selectors need an input shape");
      bounds ??= shapeBounds(shape);
      return bounds;
    },
  };
}

function activeIds(ids, shapes) {
  return ids.filter((id) => shapes.get(id) !== SKIPPED);
}

function componentParameters(component, params, label) {
  const merged = { ...(component.parameters || {}) };
  for (const [name, expression] of Object.entries(component.parameter_expressions || {})) {
    merged[name] = resolve(expression, params, `${label}.parameters.${name}`);
  }
  return merged;
}

function buildComponentShape(component, params, label) {
  const child = buildDefinitionGeometry(component.definition, componentParameters(component, params, label));
  const selected = component.source_part_id ? child.partShapes.get(component.source_part_id) : child.assembly.shape;
  if (!selected) fail(`${label} source part ${component.source_part_id} has no geometry`);
  const shape = selected.clone();
  if (importedGeometry.has(selected)) importedGeometry.add(shape);
  inheritCache(selected, shape);
  return { child, selected: shape };
}

function buildOperation(operation, shapes, params, assets, importedVisuals, components = {}) {
  if (!operation || typeof operation !== "object") fail("every operation must be an object");
  if (!operation.id || typeof operation.id !== "string") fail("every operation requires a string id");
  if (shapes.has(operation.id)) fail(`duplicate operation id ${operation.id}`);

  let shape;
  switch (operation.type) {
    case "component": {
      const component = components?.[operation.id];
      if (!component) fail(`component operation ${operation.id} was not resolved by Design Studio`);
      shape = applyAssemblyTransform(buildComponentShape(component, params, operation.id).selected, operation.transform || {}, params);
      break;
    }
    case "import_stl": {
      const loaded = stlShapes.get(operation.asset_id);
      if (!loaded) fail(`STL asset ${operation.asset_id} was not resolved by Design Studio`);
      const units = { mm: 1, cm: 10, m: 1000, in: 25.4, inch: 25.4 }[operation.units || "mm"];
      if (!units) fail(`${operation.id}.units must be mm, cm, m or in`);
      let mesh = loaded.clone();
      if (units !== 1) mesh = mesh.scale(units, [0, 0, 0]);
      shape = applyAssemblyTransform(mesh, operation.transform || {}, params);
      break;
    }
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
      // Skipped inputs drop out. A cut keeps its base (a skipped base is an
      // error); a fuse/intersect needs at least one remaining input.
      const active = operation.type === "cut" ? [ids[0], ...activeIds(ids.slice(1), shapes)] : activeIds(ids, shapes);
      if (active.length === 0) fail(`${operation.id}: every input is skipped by its when condition`);
      shape = shapeFor(active[0], shapes).clone();
      for (const id of active.slice(1)) {
        const tool = shapeFor(id, shapes).clone();
        shape = operation.type === "fuse" ? shape.fuse(tool) : operation.type === "cut" ? shape.cut(tool) : shape.intersect(tool);
      }
      break;
    }
    case "compound": {
      const ids = operation.inputs;
      if (!Array.isArray(ids) || ids.length === 0) fail(`${operation.id}.inputs is required`);
      const active = activeIds(ids, shapes);
      if (active.length === 0) fail(`${operation.id}: every input is skipped by its when condition`);
      shape = makeCompound(active.map((id) => shapeFor(id, shapes).clone()));
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
      const input = shapeFor(operation.input, shapes).clone();
      shape = edgeFeature(input, operation, "fillet", radius, featureContext(params, input));
      break;
    }
    case "chamfer": {
      const distance = resolve(operation.distance, params, `${operation.id}.distance`);
      if (distance <= 0) fail(`${operation.id}.distance must be positive`);
      const input = shapeFor(operation.input, shapes).clone();
      shape = edgeFeature(input, operation, "chamfer", distance, featureContext(params, input));
      break;
    }
    case "shell": {
      const input = shapeFor(operation.input, shapes).clone();
      shape = shellFeature(input, operation, featureContext(params, input));
      break;
    }
    case "hole": {
      const input = shapeFor(operation.input, shapes).clone();
      shape = holeFeature(input, operation, featureContext(params, input), polygonSketch);
      break;
    }
    case "extrude_profile": {
      shape = extrudeProfile(operation, featureContext(params));
      break;
    }
    case "loft": {
      shape = loftFeature(operation, featureContext(params));
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

function assemblyGeometry(definition, shapes, partShapes, partPhysical, partVisuals, params, importedVisuals, options = {}) {
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
  const interfaces = new Map((definition.assembly.interfaces || []).map((item) => [item.id, item]));
  const jointByChild = new Map((definition.assembly.joints || []).map((joint) => [joint.child_instance, joint]));
  const placed = (id) => instances.find((item) => item.id === id);
  const frameOf = (id, partID, label) => {
    const item = interfaces.get(id);
    if (!item) fail(`${label} references unknown interface ${id}`);
    if (item.part_id !== partID) fail(`${label}: interface ${id} belongs to part ${item.part_id}, not ${partID}`);
    return interfaceFrame(vector(item.position, params, `interface ${id}.position`), item.direction ? vector(item.direction, params, `interface ${id}.direction`) : undefined, item.up ? vector(item.up, params, `interface ${id}.up`) : undefined, `interface ${id}`);
  };
  const usedTargets = new Map();
  let attempts=0;
  while(pending.length) {
    if(++attempts>definition.assembly.instances.length**2+1)fail("Assembly placement cycle: alignment, mate and joint references must form a tree");
    const instance=pending.shift();
    const joint = jointByChild.get(instance.id);
    const dependsOn = [instance.alignment?.target_instance, instance.mate?.to_instance, joint?.parent_instance].filter(Boolean);
    for (const id of dependsOn) if (id === instance.id) fail(`assembly instance ${instance.id} cannot be placed relative to itself`);
    if(dependsOn.some((id) => !placed(id))){pending.push(instance);continue;}

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
    let drivenBy = new Set();
    for (const id of dependsOn) for (const j of placed(id).drivenBy) drivenBy.add(j);
    if (instance.mate || joint) {
      const label = `assembly instance ${instance.id}`;
      if (resolvedTransform.scale !== undefined && resolve(resolvedTransform.scale, params) !== 1) fail(`${label}: mated or jointed instances cannot be scaled`);
      let pose = poseFromTransform(resolvedTransform, (v, l) => resolve(v, params, l), (v, l, f) => vector(v, params, l, f));
      if (instance.mate) {
        const mate = instance.mate;
        if (instance.alignment) fail(`${label} has both alignment and mate; use one`);
        if (resolvedTransform.rotate?.length || resolvedTransform.translate) fail(`${label} has both a transform and a mate; the mate decides placement (use mate.gap and mate.angle)`);
        const target = placed(mate.to_instance);
        if (!target.pose) fail(`${label} mates to scaled instance ${target.id}`);
        const key = `${mate.to_instance}/${mate.to_interface}`;
        if (usedTargets.has(key)) fail(`${label} and ${usedTargets.get(key)} both mate to ${key}`);
        usedTargets.set(key, instance.id);
        pose = matePose(target.pose, frameOf(mate.to_interface, target.part.id, `${label}.mate.to_interface`), frameOf(mate.interface, part.id, `${label}.mate.interface`),
          resolve(mate.gap ?? 0, params, `${label}.mate.gap`), resolve(mate.angle ?? 0, params, `${label}.mate.angle`), mate.flip !== false);
      }
      if (joint && joint.type !== "fixed") {
        const parent = placed(joint.parent_instance);
        if (!parent.pose) fail(`joint ${joint.id} parent ${parent.id} is scaled`);
        const override = options.jointStates && Object.hasOwn(options.jointStates, joint.id) ? options.jointStates[joint.id] : undefined;
        const state = override !== undefined ? override : resolve(joint.state ?? 0, params, `joint ${joint.id}.state`);
        if (Array.isArray(joint.limits) && joint.limits.length === 2 && (state < joint.limits[0] - 1e-9 || state > joint.limits[1] + 1e-9)) {
          fail(`joint ${joint.id} state ${state} is outside its limits [${joint.limits.join(", ")}]`);
        }
        const origin = applyPose(parent.pose, vector(joint.origin, params, `joint ${joint.id}.origin`));
        const axis = unit(applyR(parent.pose.R, vector(joint.axis, params, `joint ${joint.id}.axis`, [0, 0, 1])), `joint ${joint.id}.axis`);
        const motion = joint.type === "revolute" ? rotationAbout(origin, axis, state) : { R: IDENTITY().R, t: axis.map((v) => v * state) };
        pose = compose(motion, pose);
        drivenBy.add(joint.id);
      }
      resolvedTransform = transformFromPose(pose);
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
      drivenBy,
      pose: resolvedTransform.scale !== undefined ? null : poseFromTransform(resolvedTransform, (v, l) => resolve(v, params, l), (v, l, f) => vector(v, params, l, f)),
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
  for (const operation of definition.operations) {
    if (operation && operation.when !== undefined && operation.when !== null) {
      if (shapes.has(operation.id)) fail(`duplicate operation id ${operation.id}`);
      if (resolve(operation.when, params, `${operation.id}.when`) === 0) {
        shapes.set(operation.id, SKIPPED);
        continue;
      }
    }
    buildOperation(operation, shapes, params, definition._assets, importedVisuals, definition._resolved_operation_components || {});
  }
  const resolved = new Map((definition._resolved_components || []).map((component) => [component.part_id, component]));
  const partShapes = new Map();
  const partPhysical = new Map();
  const partVisuals = new Map();
  const materials = new Map((definition.materials || []).map((material) => [material.id, material]));
  for (const part of definition.parts || []) {
    if (part.source) {
      const component = resolved.get(part.id);
      if (!component) fail(`linked part ${part.id} was not resolved by Design Studio`);
      const { child, selected: selectedShape } = buildComponentShape(component, params, `part ${part.id}`);
      const sourcePart=component.source_part_id?(component.definition.parts||[]).find(p=>p.id===component.source_part_id):null;
      if(sourcePart && !part.manufacturing) part.manufacturing=sourcePart.manufacturing;
      if(!component.source_part_id && (component.definition.parts||[]).some(p=>['purchased','reference'].includes(p.manufacturing?.classification)||p._exclude_print))part._exclude_print=true;

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
  return { params, shapes, partShapes, partPhysical, partVisuals, assembly, physical, importedVisuals, definition };
}

function mouldingChecks(definition, built) {
  const results = [];
  for (const [index, c] of (definition.checks || []).entries()) {
    if (c.type !== "moulding") continue;
    const result = { id: c.id || `check_${index}`, type: "moulding", status: "indeterminate", message: "Moulding check could not be completed", instances: [], details: {} };
    results.push(result);
    try {
      let shape = built.assembly.shape;
      if (c.part !== undefined) {
        const instance = built.assembly.instances.find((item) => item.id === c.part) || built.assembly.instances.find((item) => item.part.id === c.part);
        shape = instance ? instance.shape : built.partShapes.get(c.part);
        if (!shape) throw new Error(`unknown part or instance ${c.part}`);
        result.instances = [c.part];
      }
      Object.assign(result, analyzeMoulding(shape, c));
    } catch (error) {
      result.message = String(error?.message || error);
    }
  }
  return results;
}

// Orthographic drawing views as flattened polylines (mm, y up). Design
// Studio turns these into DXF and PDF sheets.
function drawingViews(shape) {
  const views = [];
  for (const [name, plane] of [["front", "front"], ["top", "top"], ["right", "right"]]) {
    const projection = drawProjection(shape, plane);
    const lines = (drawing) => {
      if (!drawing) return [];
      let paths;
      try { paths = drawing.toSVGPaths(); } catch { return []; }
      return paths.flat(Infinity).flatMap((d) => flattenPath(d, 12).map((item) => item.points));
    };
    views.push({ name, visible: lines(projection.visible), hidden: lines(projection.hidden) });
  }
  return views;
}

// joint_sweep: move one joint through its range and check every instance it
// moves against every instance it does not, at each sampled state.
function jointSweeps(definition, built) {
  const results = [];
  for (const [index, c] of (definition.checks || []).entries()) {
    if (c.type !== "joint_sweep") continue;
    const result = { id: c.id || `check_${index}`, type: "joint_sweep", status: "indeterminate", message: "Joint sweep could not be completed", instances: [], details: {} };
    results.push(result);
    try {
      const joint = (definition.assembly?.joints || []).find((item) => item.id === c.joint);
      if (!joint) throw new Error(`unknown joint ${c.joint}`);
      if (!["revolute", "prismatic"].includes(joint.type)) throw new Error(`joint ${joint.id} is ${joint.type}; only revolute and prismatic joints move`);
      const from = c.from !== undefined ? resolve(c.from, built.params, `${result.id}.from`) : joint.limits?.[0];
      const to = c.to !== undefined ? resolve(c.to, built.params, `${result.id}.to`) : joint.limits?.[1];
      if (!Number.isFinite(from) || !Number.isFinite(to)) throw new Error("set from/to or give the joint limits");
      const steps = Math.trunc(Number(c.steps ?? 8));
      if (!(steps >= 1 && steps <= 64)) throw new Error("steps must be between 1 and 64");
      const min = Number(c.min ?? 0), tolerance = Number(c.tolerance_mm ?? 0.001);
      const match = (pairs, a, b) => (pairs || []).some((p) => p.length === 2 && ((p[0] === a && p[1] === b) || (p[1] === a && p[0] === b)));
      const allowedContact = [...(c.allowed_contact_pairs || []), [joint.parent_instance, joint.child_instance]];
      const samples = [];
      let worst = Infinity, failures = 0;
      for (let step = 0; step <= steps; step += 1) {
        const value = from + ((to - from) * step) / steps;
        const assembly = assemblyGeometry(definition, built.shapes, built.partShapes, built.partPhysical, built.partVisuals, built.params, built.importedVisuals, { jointStates: { [joint.id]: value } });
        const moving = assembly.instances.filter((item) => item.drivenBy.has(joint.id));
        const fixed = assembly.instances.filter((item) => !item.drivenBy.has(joint.id));
        const sample = { state: value, pairs: [] };
        for (const a of moving) for (const b of fixed) {
          if (match(c.ignore_pairs, a.id, b.id)) continue;
          if (boxDistance(a.shape, b.shape) > min + tolerance) continue;
          const left = exactAssemblyTransform(a.sourceShape, a.resolvedTransform, built.params);
          const right = exactAssemblyTransform(b.sourceShape, b.resolvedTransform, built.params);
          try {
            const p = exactPair(left, right, tolerance, Number(c.volume_tolerance_mm3 ?? 1e-6));
            const allowed = match(allowedContact, a.id, b.id);
            const pass = !p.intersects && (!p.contact || allowed) && (allowed || p.distance_mm + tolerance >= min);
            worst = Math.min(worst, p.distance_mm - (p.intersects ? p.intersection_volume_mm3 : 0));
            if (!pass) { failures += 1; sample.pairs.push({ a: a.id, b: b.id, distance_mm: p.distance_mm, intersection_volume_mm3: p.intersection_volume_mm3 }); }
          } finally { left.delete(); right.delete(); }
        }
        samples.push(sample);
      }
      const failing = samples.filter((sample) => sample.pairs.length);
      result.status = failing.length ? "fail" : "pass";
      result.instances = [joint.child_instance];
      result.expected = min;
      result.actual = Number.isFinite(worst) ? worst : null;
      result.message = failing.length
        ? `Joint ${joint.id} collides at ${failing.map((sample) => +sample.state.toFixed(3)).join(", ")} (${failures} pair contact(s))`
        : `Joint ${joint.id} moves from ${from} to ${to} without collisions (${steps + 1} states)`;
      result.details = { joint: joint.id, from, to, steps, failing_states: failing, method: "exact_brep_sampled" };
    } catch (error) {
      result.message = String(error?.message || error);
    }
  }
  return results;
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
    return {id:item.id,part_id:item.part_id,name:item.name,color:item.color,material_id:assembly.parts?.get?.(item.part_id)?.material_id||undefined,visible:!assembly.instances.some(i=>i.visible===false&&(item.id===i.id||item.id.startsWith(i.id+'/'))),instance_id:assembly.instances.find(i=>item.id===i.id||item.id.startsWith(i.id+'/'))?.id||(!request.definition.assembly?(request.definition.parts?.find(p=>p.output===request.definition.output)?.id||'output'):undefined),...visualMesh};
  });
  const mesh={vertices:[],triangles:[],normals:[]};
  for(const part of partMeshes){const offset=mesh.vertices.length/3;for(const v of part.vertices)mesh.vertices.push(v);for(const n of part.normals||[])mesh.normals.push(n);for(const i of part.triangles)mesh.triangles.push(i+offset);}

  let report;try{report=geometryReport(shape, mesh, assembly.instances);}catch(e){fail(`Geometry measurements failed: ${String(e)}`);}
  const inspection=inspect(request.definition,built,applyAssemblyTransform,exactAssemblyTransform);
  report.topology=inspection.topology; report.exact_checks=[...inspection.exact_checks, ...jointSweeps(request.definition, built), ...mouldingChecks(request.definition, built)];
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
  if (formats.has("drawing-json")) {
    const path = join(outputDir, "drawing.json");
    const target = assembly.instances.length ? makeCompound(assembly.instances.filter((item) => item.visible).map((item) => item.shape.clone())) : shape;
    const views = drawingViews(target);
    await Bun.write(path, JSON.stringify({ name: request.name || "Design", units: "mm", bounds: shapeBounds(target), views }));
    artifacts.push({ format: "drawing-json", path, content_type: "application/json" });
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

// Async resources (fonts, STL meshes) are loaded before the synchronous
// build, for the definition and every resolved component inside it.
async function preload(definition) {
  const text = JSON.stringify(definition || {});
  if (text.includes('"text"')) {
    const files = { sans: "font-sans.ttf", "sans-bold": "font-sans-bold.ttf", mono: "font-mono.ttf" };
    for (const [key, file] of Object.entries(files)) {
      if (!text.includes(key === "sans" ? '"text"' : `"${key}"`)) continue;
      const body = await Bun.file(join(import.meta.dir, file)).arrayBuffer();
      await loadFont(body, FONTS[key]);
      loadedFonts.add(key);
    }
  }
  const visit = async (def) => {
    for (const [id, asset] of Object.entries(def?._stl_assets || {})) {
      if (stlShapes.has(id)) continue;
      const body = await Bun.file(asset.file).arrayBuffer();
      const shape = await importSTL(new Blob([body]));
      stlShapes.set(id, shape);
    }
    for (const component of def?._resolved_components || []) await visit(component.definition);
    for (const component of Object.values(def?._resolved_operation_components || {})) await visit(component.definition);
  };
  await visit(definition);
}

async function main() {
  const wasmPath = join(import.meta.dir, "replicad_single.wasm");
  const oc = await initOpenCascade({ locateFile: () => wasmPath });
  setOC(oc);
  const request = await Bun.stdin.json();
  await preload(request.definition);
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
