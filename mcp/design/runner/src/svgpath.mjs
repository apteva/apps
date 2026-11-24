// SVG path data: parse to absolute segments, flatten to polylines, or turn
// into replicad drawings. Supports M L H V C S Q T A Z, absolute and relative,
// with implicit repeated commands.

function fail(message) {
  throw new Error(message);
}

const ARGS = { M: 2, L: 2, H: 1, V: 1, C: 6, S: 4, Q: 4, T: 2, A: 7, Z: 0 };

function tokenize(d) {
  if (typeof d !== "string" || !d.trim()) fail("SVG path data is empty");
  if (d.length > 200000) fail("SVG path data is longer than 200000 characters");
  const tokens = [];
  const raws = [];
  tokens.raws = raws;
  const re = /([MmLlHhVvCcSsQqTtAaZz])|([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)|([\s,]+)|(.)/g;
  let match;
  while ((match = re.exec(d))) {
    if (match[1]) tokens.push(match[1]);
    else if (match[2] !== undefined) { tokens.push(Number(match[2])); raws[tokens.length - 1] = match[2]; }
    else if (match[4]) fail(`unexpected character ${JSON.stringify(match[4])} in SVG path`);
  }
  return tokens;
}

// Returns subpaths: [{ start:[x,y], segments:[{type,...,to:[x,y]}], closed }]
export function parsePath(d) {
  const tokens = tokenize(d);
  const subpaths = [];
  let index = 0, command = null, current = [0, 0], start = [0, 0], path = null;
  let lastControl = null, lastType = null;
  const number = () => {
    const value = tokens[index++];
    if (typeof value !== "number") fail("SVG path is missing a number");
    return value;
  };
  const flag = () => {
    // arc flags may be written without separators ("a1 1 0 011 1")
    let value = tokens[index];
    if (typeof value !== "number") fail("SVG arc flag missing");
    if (value === 0 || value === 1) { index++; return value; }
    const text = tokens.raws[index] ?? String(value);
    if ((text[0] === "0" || text[0] === "1") && text.length > 1) {
      tokens.raws[index] = text.slice(1);
      tokens[index] = Number(text.slice(1));
      return Number(text[0]);
    }
    fail("SVG arc flag must be 0 or 1");
  };
  while (index < tokens.length) {
    if (typeof tokens[index] === "string") command = tokens[index++];
    else if (command === null) fail("SVG path must start with M");
    const upper = command.toUpperCase();
    const relative = command !== upper;
    const base = relative ? current : [0, 0];
    const point = () => [base[0] + number(), base[1] + number()];
    if (upper !== "M" && !path) fail("SVG path must start with M");
    switch (upper) {
      case "M": {
        current = point();
        start = [...current];
        path = { start: [...current], segments: [], closed: false };
        subpaths.push(path);
        command = relative ? "l" : "L";
        lastType = "M";
        continue;
      }
      case "L": { const to = point(); path.segments.push({ type: "L", to }); current = to; lastType = "L"; break; }
      case "H": { const to = [(relative ? current[0] : 0) + number(), current[1]]; path.segments.push({ type: "L", to }); current = to; lastType = "L"; break; }
      case "V": { const to = [current[0], (relative ? current[1] : 0) + number()]; path.segments.push({ type: "L", to }); current = to; lastType = "L"; break; }
      case "C": {
        const c1 = point(), c2 = point(), to = point();
        path.segments.push({ type: "C", c1, c2, to }); lastControl = c2; current = to; lastType = "C"; break;
      }
      case "S": {
        const c1 = lastType === "C" ? [2 * current[0] - lastControl[0], 2 * current[1] - lastControl[1]] : [...current];
        const c2 = point(), to = point();
        path.segments.push({ type: "C", c1, c2, to }); lastControl = c2; current = to; lastType = "C"; break;
      }
      case "Q": {
        const c = point(), to = point();
        path.segments.push({ type: "Q", c, to }); lastControl = c; current = to; lastType = "Q"; break;
      }
      case "T": {
        const c = lastType === "Q" ? [2 * current[0] - lastControl[0], 2 * current[1] - lastControl[1]] : [...current];
        const to = point();
        path.segments.push({ type: "Q", c, to }); lastControl = c; current = to; lastType = "Q"; break;
      }
      case "A": {
        const rx = Math.abs(number()), ry = Math.abs(number()), rotation = number();
        const large = flag(), sweep = flag();
        const to = point();
        if (rx < 1e-12 || ry < 1e-12) path.segments.push({ type: "L", to });
        else path.segments.push({ type: "A", rx, ry, rotation, large: !!large, sweep: !!sweep, from: [...current], to });
        current = to; lastType = "A"; break;
      }
      case "Z": {
        path.closed = true;
        current = [...start];
        lastType = "Z";
        command = null;
        // a new subpath may follow without M: it starts at the closed start
        if (index < tokens.length && typeof tokens[index] === "number") fail("SVG path has numbers after Z");
        if (index < tokens.length && !/[Mm]/.test(tokens[index])) {
          path = { start: [...current], segments: [], closed: false };
          subpaths.push(path);
        }
        continue;
      }
      default:
        fail(`unsupported SVG command ${command}`);
    }
  }
  return subpaths.filter((item) => item.segments.length);
}

// SVG arc endpoint parameterisation -> centre form (SVG 1.1 F.6.5).
function arcCenter(seg) {
  const phi = (seg.rotation * Math.PI) / 180, cos = Math.cos(phi), sin = Math.sin(phi);
  const [x1, y1] = seg.from, [x2, y2] = seg.to;
  const dx = (x1 - x2) / 2, dy = (y1 - y2) / 2;
  const xp = cos * dx + sin * dy, yp = -sin * dx + cos * dy;
  let rx = seg.rx, ry = seg.ry;
  const lambda = (xp * xp) / (rx * rx) + (yp * yp) / (ry * ry);
  if (lambda > 1) { rx *= Math.sqrt(lambda); ry *= Math.sqrt(lambda); }
  const num = rx * rx * ry * ry - rx * rx * yp * yp - ry * ry * xp * xp;
  const den = rx * rx * yp * yp + ry * ry * xp * xp;
  let coef = Math.sqrt(Math.max(0, num / den));
  if (seg.large === seg.sweep) coef = -coef;
  const cxp = (coef * rx * yp) / ry, cyp = (-coef * ry * xp) / rx;
  const cx = cos * cxp - sin * cyp + (x1 + x2) / 2, cy = sin * cxp + cos * cyp + (y1 + y2) / 2;
  const angle = (ux, uy, vx, vy) => Math.atan2(ux * vy - uy * vx, ux * vx + uy * vy);
  const start = angle(1, 0, (xp - cxp) / rx, (yp - cyp) / ry);
  let delta = angle((xp - cxp) / rx, (yp - cyp) / ry, (-xp - cxp) / rx, (-yp - cyp) / ry);
  if (!seg.sweep && delta > 0) delta -= 2 * Math.PI;
  if (seg.sweep && delta < 0) delta += 2 * Math.PI;
  return { cx, cy, rx, ry, cos, sin, start, delta };
}

// Flatten to polylines; `steps` samples per curve.
export function flattenPath(d, steps = 16) {
  return parsePath(d).map((sub) => {
    const points = [sub.start];
    let current = sub.start;
    for (const seg of sub.segments) {
      if (seg.type === "L") points.push(seg.to);
      else if (seg.type === "C") {
        for (let i = 1; i <= steps; i++) {
          const t = i / steps, u = 1 - t;
          points.push([0, 1].map((k) => u * u * u * current[k] + 3 * u * u * t * seg.c1[k] + 3 * u * t * t * seg.c2[k] + t * t * t * seg.to[k]));
        }
      } else if (seg.type === "Q") {
        for (let i = 1; i <= steps; i++) {
          const t = i / steps, u = 1 - t;
          points.push([0, 1].map((k) => u * u * current[k] + 2 * u * t * seg.c[k] + t * t * seg.to[k]));
        }
      } else if (seg.type === "A") {
        const a = arcCenter(seg);
        const n = Math.max(4, Math.ceil((Math.abs(a.delta) / (2 * Math.PI)) * steps * 4));
        for (let i = 1; i <= n; i++) {
          const theta = a.start + (a.delta * i) / n;
          const x = a.rx * Math.cos(theta), y = a.ry * Math.sin(theta);
          points.push([a.cos * x - a.sin * y + a.cx, a.sin * x + a.cos * y + a.cy]);
        }
      }
      current = seg.to;
    }
    if (sub.closed) points.push(sub.start);
    return { points, closed: sub.closed };
  });
}

function polygonArea(points) {
  let area = 0;
  for (let i = 0; i < points.length; i++) {
    const [x1, y1] = points[i], [x2, y2] = points[(i + 1) % points.length];
    area += x1 * y2 - x2 * y1;
  }
  return area / 2;
}

function pointInPolygon([x, y], polygon) {
  let inside = false;
  for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
    const [xi, yi] = polygon[i], [xj, yj] = polygon[j];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

// Build one replicad Drawing from path data. Every subpath is closed; a
// subpath nested inside an odd number of others becomes a hole (even-odd),
// so letters like "O" and "B" come out right. map transforms [x,y].
export function pathDrawing(d, draw, map) {
  const subs = parsePath(d);
  if (!subs.length) fail("SVG path has no drawable segments");
  if (subs.length > 256) fail("SVG path has more than 256 subpaths");
  const flat = flattenPath(d, 8);
  const items = subs.map((sub, index) => {
    const polygon = flat[index].points.map(map);
    return { sub, polygon, area: Math.abs(polygonArea(polygon)) };
  }).filter((item) => item.area > 1e-9);
  if (!items.length) fail("SVG path encloses no area");
  for (const item of items) {
    const probe = item.polygon[0];
    item.depth = items.filter((other) => other !== item && other.area > item.area && pointInPolygon(probe, other.polygon)).length;
  }
  const flipped = (() => { const a = map([0, 0]), b = map([1, 0]), c = map([0, 1]); return (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0]) < 0; })();
  const drawingFor = ({ sub }) => {
    let pen = draw(map(sub.start));
    let current = sub.start;
    for (const seg of sub.segments) {
      const same = Math.hypot(seg.to[0] - current[0], seg.to[1] - current[1]) < 1e-9;
      if (same) continue;
      if (seg.type === "L") pen = pen.lineTo(map(seg.to));
      else if (seg.type === "C") pen = pen.cubicBezierCurveTo(map(seg.to), map(seg.c1), map(seg.c2));
      else if (seg.type === "Q") pen = pen.quadraticBezierCurveTo(map(seg.to), map(seg.c));
      else if (seg.type === "A") {
        const scale = Math.hypot(...[0, 1].map((k) => map([1, 0])[k] - map([0, 0])[k]));
        pen = pen.ellipseTo(map(seg.to), seg.rx * scale, seg.ry * scale, flipped ? -seg.rotation : seg.rotation, seg.large, flipped ? !seg.sweep : seg.sweep);
      }
      current = seg.to;
    }
    return pen.close();
  };
  items.sort((a, b) => a.depth - b.depth || b.area - a.area);
  let result = null;
  for (const item of items) {
    const drawing = drawingFor(item);
    if (!result) result = drawing;
    else result = item.depth % 2 === 1 ? result.cut(drawing) : result.fuse(drawing);
  }
  return result;
}
