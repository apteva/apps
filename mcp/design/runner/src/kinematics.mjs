// Rigid placement math for mates and joints. A pose is {R: 3x3 rows, t: [x,y,z]}
// mapping part-local points to world: p' = R p + t.

function fail(message) {
  throw new Error(message);
}

export const IDENTITY = () => ({ R: [[1, 0, 0], [0, 1, 0], [0, 0, 1]], t: [0, 0, 0] });

const dot = (a, b) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const cross = (a, b) => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const scale = (a, k) => a.map((v) => v * k);
const add = (a, b) => a.map((v, i) => v + b[i]);
const sub = (a, b) => a.map((v, i) => v - b[i]);
export function unit(v, label) {
  const length = Math.hypot(...v);
  if (!(length > 1e-9)) fail(`${label} must not be zero`);
  return v.map((x) => x / length);
}

export function applyR(R, v) {
  return [dot(R[0], v), dot(R[1], v), dot(R[2], v)];
}
export function applyPose(pose, p) {
  return add(applyR(pose.R, p), pose.t);
}
function mulR(A, B) {
  return A.map((row) => [0, 1, 2].map((j) => row[0] * B[0][j] + row[1] * B[1][j] + row[2] * B[2][j]));
}
function transposeR(R) {
  return [0, 1, 2].map((i) => [R[0][i], R[1][i], R[2][i]]);
}
// compose(a, b) = a ∘ b (apply b first)
export function compose(a, b) {
  return { R: mulR(a.R, b.R), t: add(applyR(a.R, b.t), a.t) };
}
export function invert(pose) {
  const Rt = transposeR(pose.R);
  return { R: Rt, t: scale(applyR(Rt, pose.t), -1) };
}

export function axisAngle(axis, degrees) {
  const [x, y, z] = unit(axis, "rotation axis");
  const a = (degrees * Math.PI) / 180, c = Math.cos(a), s = Math.sin(a), C = 1 - c;
  return [
    [c + x * x * C, x * y * C - z * s, x * z * C + y * s],
    [y * x * C + z * s, c + y * y * C, y * z * C - x * s],
    [z * x * C - y * s, z * y * C + x * s, c + z * z * C],
  ];
}
// Rotation about a line through `center`.
export function rotationAbout(center, axis, degrees) {
  const R = axisAngle(axis, degrees);
  return { R, t: sub(center, applyR(R, center)) };
}

// Assembly transform ({rotate:[{angle,center,direction}], translate}) <-> pose.
export function poseFromTransform(transform, resolve, vector) {
  let pose = IDENTITY();
  for (const r of transform?.rotate || []) {
    pose = compose(rotationAbout(vector(r.center, "rotation center"), vector(r.direction, "rotation axis", [0, 0, 1]), resolve(r.angle, "rotation angle")), pose);
  }
  if (transform?.translate) pose = compose({ R: IDENTITY().R, t: vector(transform.translate, "translation") }, pose);
  return pose;
}
export function transformFromPose(pose) {
  const R = pose.R;
  const trace = R[0][0] + R[1][1] + R[2][2];
  const cosine = Math.max(-1, Math.min(1, (trace - 1) / 2));
  const angle = Math.acos(cosine);
  const out = { translate: pose.t.map(clean) };
  if (angle < 1e-9) return out;
  let axis;
  if (Math.PI - angle < 1e-6) {
    // 180°: axis from the diagonal of (R + I) / 2
    const xx = (R[0][0] + 1) / 2, yy = (R[1][1] + 1) / 2, zz = (R[2][2] + 1) / 2;
    if (xx >= yy && xx >= zz) { const x = Math.sqrt(xx); axis = [x, R[0][1] / (2 * x), R[0][2] / (2 * x)]; }
    else if (yy >= zz) { const y = Math.sqrt(yy); axis = [R[0][1] / (2 * y), y, R[1][2] / (2 * y)]; }
    else { const z = Math.sqrt(zz); axis = [R[0][2] / (2 * z), R[1][2] / (2 * z), z]; }
  } else {
    axis = [R[2][1] - R[1][2], R[0][2] - R[2][0], R[1][0] - R[0][1]];
  }
  out.rotate = [{ angle: clean((angle * 180) / Math.PI), center: [0, 0, 0], direction: unit(axis, "axis").map(clean) }];
  return out;
}
function clean(v) {
  return Math.abs(v) < 1e-12 ? 0 : v;
}

// Interface frame: origin at position, z along direction (the outward
// normal of the mating face), x along `up` made perpendicular to z.
export function interfaceFrame(position, direction, up, label) {
  const z = unit(direction || [0, 0, 1], `${label}.direction`);
  let x;
  if (up) {
    const u = unit(up, `${label}.up`);
    x = sub(u, scale(z, dot(u, z)));
    if (Math.hypot(...x) < 1e-6) fail(`${label}.up must not be parallel to its direction`);
  } else {
    // Default up: world +Z projected onto the face; world +Y for faces
    // whose normal is (nearly) vertical.
    x = Math.abs(z[2]) < 0.9 ? cross(z, cross([0, 0, 1], z)) : [0, 1, 0];
    x = sub(x, scale(z, dot(x, z)));
  }
  x = unit(x, `${label}.up`);
  const y = cross(z, x);
  // columns x, y, z
  return { R: [[x[0], y[0], z[0]], [x[1], y[1], z[1]], [x[2], y[2], z[2]]], t: position };
}

// Child pose such that child's interface faces the parent's (z opposed,
// x aligned), separated by `gap` along the parent normal and turned by
// `angle` about it. Both frames are given in their parts' local coordinates;
// parentPose places the parent part in the world.
export function matePose(parentPose, parentFrame, childFrame, gap, angle, flip) {
  const target = compose(parentPose, parentFrame);
  let local = { R: IDENTITY().R, t: [0, 0, gap] };
  if (angle) local = compose(local, { R: axisAngle([0, 0, 1], angle), t: [0, 0, 0] });
  if (flip) local = compose(local, { R: [[1, 0, 0], [0, -1, 0], [0, 0, -1]], t: [0, 0, 0] });
  return compose(compose(target, local), invert(childFrame));
}
