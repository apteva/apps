// Theme-independent map layout. It decides where places, paths, water and
// props go; themes only decide what those look like. Everything is a pure
// function of the input and a seed, so the same project keeps the same map.
import { TILE, rng, seedFor, type Point } from "./core";
import { categoryFor, type Category } from "./categories";

export const GROUND = { grass: 0, path: 1, plaza: 2, water: 3, wild: 4, soil: 5 } as const;
export type Ground = (typeof GROUND)[keyof typeof GROUND];

export type PlaceInput = { id: string; name: string; kind: string; status: string; calls: number };
export type RegionInput<F = unknown> = { id: string; label: string; focus?: F; places: PlaceInput[]; residents: string[] };

export type Place = PlaceInput & {
  category: Category; region: number;
  x: number; y: number; w: number; h: number; // footprint in tiles
  door: Point; // tile in front of the door
};
export type PropKind = "tree" | "bush" | "rock" | "lamp" | "bench" | "fountain" | "crate" | "crops" | "fence" | "sign";
export type Prop = { kind: PropKind; x: number; y: number; variant: number; region: number; label?: string };
export type Region<F = unknown> = {
  id: string; label: string; focus?: F;
  x: number; y: number; w: number; h: number;
  plaza: { x: number; y: number; w: number; h: number };
  spots: Point[]; residents: string[]; places: string[];
};
export type Layout<F = unknown> = {
  cols: number; rows: number; width: number; height: number;
  ground: Uint8Array; cost: Uint8Array;
  places: Place[]; props: Prop[]; regions: Region<F>[];
  ponds: Pond[];
  route: (from: Point, to: Point) => Point[];
};
// Ponds are kept as shapes (px) so themes can paint smooth, round banks.
export type Pond = { cx: number; cy: number; rx: number; ry: number; seed: number };
export function inPond(pond: Pond, x: number, y: number, inset = 0) {
  const angle = Math.atan2(y - pond.cy, x - pond.cx);
  const wobble = 1 + 0.07 * Math.sin(angle * 3 + pond.seed) + 0.05 * Math.sin(angle * 5 + pond.seed * 2);
  const nx = (x - pond.cx) / (pond.rx * wobble - inset), ny = (y - pond.cy) / (pond.ry * wobble - inset);
  return nx * nx + ny * ny <= 1;
}

const BUILDING = { w: 5, h: 3 };
const MARGIN = 2;

export function buildLayout<F>(inputs: RegionInput<F>[], portrait: boolean): Layout<F> {
  const single = inputs.length <= 1;
  const most = Math.max(0, ...inputs.map(region => region.places.length));
  let plotW: number, plotH: number, columns: number;
  if (single) {
    plotW = portrait ? 30 : 46 + Math.max(0, most - 8) * 3;
    plotH = portrait ? 46 + Math.max(0, most - 6) * 3 : 30;
    columns = 1;
  } else {
    plotW = portrait ? 28 : 34;
    plotH = portrait ? 28 : 26;
    columns = portrait ? Math.min(2, inputs.length) : inputs.length <= 4 ? Math.min(2, inputs.length) : Math.ceil(Math.sqrt(inputs.length * 1.5));
  }
  const count = Math.max(1, inputs.length);
  const rowsOfPlots = Math.ceil(count / columns);
  const cols = plotW * Math.min(columns, count), rows = plotH * rowsOfPlots;
  const layout: Layout<F> = {
    cols, rows, width: cols * TILE, height: rows * TILE,
    ground: new Uint8Array(cols * rows).fill(GROUND.wild),
    cost: new Uint8Array(cols * rows),
    places: [], props: [], regions: [], ponds: [],
    route: () => [],
  };
  (inputs.length ? inputs : [{ id: "empty", label: "", places: [], residents: [] }]).forEach((input, index) => {
    const rx = (index % columns) * plotW, ry = Math.floor(index / columns) * plotH;
    carveRegion(layout, input, index, rx, ry, plotW, plotH);
  });
  scatterWild(layout);
  computeCost(layout);
  const cache = new Map<string, Point[]>();
  layout.route = (from, to) => {
    const key = `${from.x},${from.y}>${to.x},${to.y}`;
    let path = cache.get(key);
    if (!path) {
      path = astar(layout, from, to, false) || [from];
      if (cache.size > 4000) cache.clear();
      cache.set(key, path);
    }
    return path;
  };
  return layout;
}

const at = (layout: Layout<any>, x: number, y: number) => y * layout.cols + x;
const inside = (layout: Layout<any>, x: number, y: number) => x >= 0 && y >= 0 && x < layout.cols && y < layout.rows;
function setGround(layout: Layout<any>, x: number, y: number, value: Ground) {
  if (inside(layout, x, y)) layout.ground[at(layout, x, y)] = value;
}
function groundAt(layout: Layout<any>, x: number, y: number) {
  return inside(layout, x, y) ? layout.ground[at(layout, x, y)] : GROUND.wild;
}

function carveRegion<F>(layout: Layout<F>, input: RegionInput<F>, index: number, rx: number, ry: number, rw: number, rh: number) {
  const random = rng(seedFor(input.id) ^ 0x51ed);
  const ix = rx + MARGIN, iy = ry + MARGIN, iw = rw - MARGIN * 2, ih = rh - MARGIN * 2;
  for (let y = iy; y < iy + ih; y++) for (let x = ix; x < ix + iw; x++) setGround(layout, x, y, GROUND.grass);

  const pw = Math.min(10, Math.max(6, Math.round(iw * 0.24) & ~1)), ph = 6;
  const plaza = { x: rx + Math.floor((rw - pw) / 2), y: ry + Math.floor((rh - ph) / 2) + (rh > 30 ? 0 : 1), w: pw, h: ph };
  for (let y = plaza.y; y < plaza.y + ph; y++) for (let x = plaza.x; x < plaza.x + pw; x++) setGround(layout, x, y, GROUND.plaza);
  for (let x = plaza.x - 1; x <= plaza.x + pw; x++) { setGround(layout, x, plaza.y - 1, GROUND.path); setGround(layout, x, plaza.y + ph, GROUND.path); }
  for (let y = plaza.y; y < plaza.y + ph; y++) { setGround(layout, plaza.x - 1, y, GROUND.path); setGround(layout, plaza.x + pw, y, GROUND.path); }

  const region: Region<F> = { id: input.id, label: input.label, focus: input.focus, x: rx, y: ry, w: rw, h: rh, plaza, spots: [], residents: input.residents, places: [] };
  layout.regions.push(region);
  const cx = plaza.x + pw / 2, cy = plaza.y + ph / 2;
  layout.props.push({ kind: "fountain", x: cx * TILE, y: (cy + 1) * TILE, variant: 0, region: index });

  // Main road to the region's lower edge, two tiles wide.
  const roadX = Math.floor(cx) - 1;
  for (let y = plaza.y + ph; y < ry + rh; y++) { setGround(layout, roadX, y, GROUND.path); setGround(layout, roadX + 1, y, GROUND.path); }
  if (input.label) layout.props.push({ kind: "sign", x: (roadX + 3) * TILE, y: (ry + rh - MARGIN) * TILE - 2, variant: 0, region: index, label: input.label });

  // Building slots: closest free footprints around the plaza, keeping alleys.
  const taken: { x: number; y: number; w: number; h: number }[] = [{ x: plaza.x - 2, y: plaza.y - 2, w: pw + 4, h: ph + 4 }, { x: roadX - 1, y: plaza.y + ph, w: 4, h: ry + rh - plaza.y - ph }];
  const overlaps = (a: { x: number; y: number; w: number; h: number }) => taken.some(b => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h);
  for (const input2 of input.places) {
    let best: { x: number; y: number; score: number } | null = null;
    for (let y = iy + 1; y + BUILDING.h + 1 < iy + ih; y++) {
      for (let x = ix + 1; x + BUILDING.w < ix + iw; x++) {
        const box = { x: x - 1, y: y - 1, w: BUILDING.w + 2, h: BUILDING.h + 3 };
        if (overlaps(box)) continue;
        const dx = x + BUILDING.w / 2 - cx, dy = y + BUILDING.h - cy;
        // Prefer a row facing the plaza from above; spread around evenly.
        const score = Math.abs(dx) * 0.9 + Math.abs(dy) * 1.25 + (dy > 0 ? 1.5 : 0) + (x % 2) * 0.3;
        if (!best || score < best.score) best = { x, y, score };
      }
    }
    if (!best) break;
    taken.push({ x: best.x - 1, y: best.y - 1, w: BUILDING.w + 2, h: BUILDING.h + 3 });
    const place: Place = {
      ...input2, category: categoryFor(input2.name, input2.kind), region: index,
      x: best.x, y: best.y, w: BUILDING.w, h: BUILDING.h,
      door: { x: best.x + 2, y: best.y + BUILDING.h },
    };
    layout.places.push(place);
    region.places.push(place.id);
  }

  // Doors connect to the plaza ring along the cheapest grass route.
  computeCost(layout);
  for (const place of layout.places.filter(item => item.region === index)) {
    setGround(layout, place.door.x, place.door.y, GROUND.path);
    const target = { x: Math.floor(cx), y: place.door.y < plaza.y ? plaza.y - 1 : plaza.y + ph };
    const path = astar(layout, place.door, target, true) || [];
    for (const point of path) if (groundAt(layout, point.x, point.y) === GROUND.grass) setGround(layout, point.x, point.y, GROUND.path);
  }

  const free = (x: number, y: number, w: number, h: number, pad = 1) => {
    for (let ty = y - pad; ty < y + h + pad; ty++) for (let tx = x - pad; tx < x + w + pad; tx++) {
      if (groundAt(layout, tx, ty) !== GROUND.grass) return false;
    }
    return !layout.places.some(p => x - pad < p.x + p.w && p.x < x + w + pad && y - pad < p.y + p.h + 1 && p.y < y + h + pad);
  };
  const corners = [[ix + 2, iy + ih - 7], [ix + iw - 10, iy + 1], [ix + 2, iy + 1], [ix + iw - 10, iy + ih - 7]].sort(() => random() - 0.5);

  // A pond in a free corner, with an irregular edge.
  for (const [px0, py0] of corners) {
    const w = 8, h = 5;
    if (!free(px0, py0, w, h)) continue;
    const pond: Pond = { cx: (px0 + w / 2) * TILE, cy: (py0 + h / 2) * TILE, rx: (w / 2) * TILE - 4, ry: (h / 2) * TILE - 3, seed: random() * 6 };
    layout.ponds.push(pond);
    // A tile is water (unwalkable) when most of it lies inside the pond.
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
      if (inPond(pond, (px0 + x + 0.5) * TILE, (py0 + y + 0.5) * TILE, -3)) setGround(layout, px0 + x, py0 + y, GROUND.water);
    }
    break;
  }
  // A fenced garden patch in another free corner.
  for (const [gx, gy] of corners) {
    const w = 6, h = 3;
    if (!free(gx, gy + 1, w + 2, h + 2)) continue;
    const x0 = gx + 1, y0 = gy + 2;
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
      setGround(layout, x0 + x, y0 + y, GROUND.soil);
      layout.props.push({ kind: "crops", x: (x0 + x + 0.5) * TILE, y: (y0 + y + 1) * TILE - 2, variant: Math.floor(random() * 3), region: index });
    }
    for (let x = -1; x <= w; x++) if (x !== Math.floor(w / 2)) layout.props.push({ kind: "fence", x: (x0 + x + 0.5) * TILE, y: (y0 + h + 1) * TILE - 4, variant: 0, region: index });
    for (let x = -1; x <= w; x++) layout.props.push({ kind: "fence", x: (x0 + x + 0.5) * TILE, y: y0 * TILE - 4, variant: 0, region: index });
    region.spots.push({ x: x0 + Math.floor(w / 2), y: y0 + h + 1 });
    break;
  }

  // Street furniture: lamps at the plaza corners and down the road, benches.
  for (const [lx, ly] of [[plaza.x - 2, plaza.y - 2], [plaza.x + pw + 1, plaza.y - 2], [plaza.x - 2, plaza.y + ph + 1], [plaza.x + pw + 1, plaza.y + ph + 1]]) {
    if (groundAt(layout, lx, ly) === GROUND.grass) layout.props.push({ kind: "lamp", x: (lx + 0.5) * TILE, y: (ly + 1) * TILE - 3, variant: 0, region: index });
  }
  for (let y = plaza.y + ph + 4; y < ry + rh - MARGIN - 1; y += 5) {
    const side = (y / 5) % 2 ? roadX - 1 : roadX + 2;
    if (groundAt(layout, side, y) === GROUND.grass) layout.props.push({ kind: "lamp", x: (side + 0.5) * TILE, y: (y + 1) * TILE - 3, variant: 0, region: index });
  }
  for (const [bx, by] of [[plaza.x + 1, plaza.y + 1], [plaza.x + pw - 2, plaza.y + ph - 2]]) {
    layout.props.push({ kind: "bench", x: (bx + 0.5) * TILE, y: (by + 1) * TILE - 4, variant: bx > cx ? 1 : 0, region: index });
    region.spots.push({ x: bx, y: by + 1 });
  }
  for (const place of layout.places.filter(item => item.region === index)) {
    if (random() < 0.5 && groundAt(layout, place.x + place.w, place.y + place.h - 1) === GROUND.grass) {
      layout.props.push({ kind: "crate", x: (place.x + place.w + 0.5) * TILE, y: (place.y + place.h) * TILE - 2, variant: Math.floor(random() * 2), region: index });
    }
  }

  // Trees, bushes and rocks on open grass, never crowding paths or doors.
  const near = (x: number, y: number, kinds: number[]) => {
    for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) if (kinds.includes(groundAt(layout, x + dx, y + dy))) return true;
    return false;
  };
  const occupied = new Set(layout.props.map(prop => `${Math.floor(prop.x / TILE)},${Math.floor((prop.y - 1) / TILE)}`));
  for (let attempt = 0; attempt < iw * ih * 0.5; attempt++) {
    const x = ix + Math.floor(random() * iw), y = iy + Math.floor(random() * ih);
    if (groundAt(layout, x, y) !== GROUND.grass || near(x, y, [GROUND.path, GROUND.plaza, GROUND.soil, GROUND.water]) || occupied.has(`${x},${y}`)) continue;
    if (layout.places.some(p => x >= p.x - 1 && x <= p.x + p.w && y >= p.y - 2 && y <= p.y + p.h + 1)) continue;
    let crowded = false;
    for (const key of occupied) { const [ox, oy] = key.split(",").map(Number); if (Math.abs(ox - x) + Math.abs(oy - y) < 3) { crowded = true; break; } }
    if (crowded) continue;
    const roll = random();
    const kind: PropKind = roll < 0.55 ? "tree" : roll < 0.85 ? "bush" : "rock";
    // Mostly green oaks and pines, with the occasional blossom or autumn tree.
    const tint = random();
    const variant = kind === "tree" ? (tint < 0.08 ? 2 : tint < 0.14 ? 3 : tint < 0.5 ? 1 : 0) : Math.floor(random() * 4);
    layout.props.push({ kind, x: (x + 0.5) * TILE, y: (y + 1) * TILE - 2, variant, region: index });
    occupied.add(`${x},${y}`);
  }

  // Places to stroll to: plaza corners, the pond shore and quiet path tiles.
  for (let i = 0; i < 6; i++) region.spots.push({ x: plaza.x + Math.floor(random() * pw), y: plaza.y + Math.floor(random() * ph) });
  for (let y = iy; y < iy + ih; y++) for (let x = ix; x < ix + iw; x++) {
    const ground = groundAt(layout, x, y);
    if (ground === GROUND.grass && near(x, y, [GROUND.water]) && random() < 0.35) region.spots.push({ x, y });
    if (ground === GROUND.path && random() < 0.04) region.spots.push({ x, y });
  }
}

// Fill the wild margins with forest so regions read as clearings.
function scatterWild(layout: Layout<any>) {
  const random = rng(0xf0e5);
  for (let y = 0; y < layout.rows; y++) for (let x = 0; x < layout.cols; x++) {
    if (groundAt(layout, x, y) !== GROUND.wild) continue;
    if ((x + y * 3) % 2 && random() < 0.75) {
      const region = layout.regions.findIndex(r => x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.h);
      layout.props.push({ kind: random() < 0.85 ? "tree" : "bush", x: (x + 0.5) * TILE + Math.round(random() * 6 - 3), y: (y + 1) * TILE - 1, variant: random() < 0.06 ? 3 : [4, 5, 6, 7][Math.floor(random() * 4)], region: Math.max(0, region) });
    }
  }
}

const SOLID_PROPS = new Set<PropKind>(["tree", "rock", "lamp", "bench", "crate", "fence", "sign", "bush"]);

function computeCost(layout: Layout<any>) {
  const weights: Record<number, number> = { [GROUND.grass]: 3, [GROUND.path]: 1, [GROUND.plaza]: 1, [GROUND.soil]: 0, [GROUND.water]: 0, [GROUND.wild]: 0 };
  for (let i = 0; i < layout.ground.length; i++) layout.cost[i] = weights[layout.ground[i]];
  for (const place of layout.places) for (let y = place.y; y < place.y + place.h; y++) for (let x = place.x; x < place.x + place.w; x++) layout.cost[at(layout, x, y)] = 0;
  for (const prop of layout.props) {
    const x = Math.floor(prop.x / TILE), y = Math.floor((prop.y - 1) / TILE);
    if (prop.kind === "fountain") {
      for (let dy = -2; dy <= 0; dy++) for (let dx = -1; dx <= 0; dx++) if (inside(layout, x + dx, y + dy)) layout.cost[at(layout, x + dx, y + dy)] = 0;
    } else if (SOLID_PROPS.has(prop.kind) && inside(layout, x, y)) layout.cost[at(layout, x, y)] = 0;
  }
}

// A* over 4-connected tiles. `carving` walks grass freely to lay new paths.
function astar(layout: Layout<any>, from: Point, to: Point, carving: boolean): Point[] | null {
  const start = at(layout, from.x, from.y), goal = at(layout, to.x, to.y);
  if (!inside(layout, from.x, from.y) || !inside(layout, to.x, to.y)) return null;
  const cost = (i: number) => {
    if (i === goal || i === start) return 1;
    if (carving) {
      const ground = layout.ground[i];
      return layout.cost[i] === 0 && ground !== GROUND.path ? 0 : ground === GROUND.path || ground === GROUND.plaza ? 1 : 2;
    }
    return layout.cost[i];
  };
  const g = new Float64Array(layout.cols * layout.rows).fill(Infinity);
  const previous = new Int32Array(layout.cols * layout.rows).fill(-1);
  const heap: [number, number][] = [];
  const push = (f: number, i: number) => {
    heap.push([f, i]);
    let n = heap.length - 1;
    while (n > 0) { const p = (n - 1) >> 1; if (heap[p][0] <= heap[n][0]) break; [heap[p], heap[n]] = [heap[n], heap[p]]; n = p; }
  };
  const pop = () => {
    const top = heap[0], last = heap.pop()!;
    if (heap.length) {
      heap[0] = last;
      let n = 0;
      for (;;) {
        const l = n * 2 + 1, r = l + 1;
        let m = n;
        if (l < heap.length && heap[l][0] < heap[m][0]) m = l;
        if (r < heap.length && heap[r][0] < heap[m][0]) m = r;
        if (m === n) break;
        [heap[m], heap[n]] = [heap[n], heap[m]];
        n = m;
      }
    }
    return top;
  };
  g[start] = 0;
  push(0, start);
  while (heap.length) {
    const [, current] = pop();
    if (current === goal) break;
    const cx = current % layout.cols, cy = Math.floor(current / layout.cols);
    for (const [dx, dy] of [[1, 0], [-1, 0], [0, 1], [0, -1]]) {
      const nx = cx + dx, ny = cy + dy;
      if (!inside(layout, nx, ny)) continue;
      const next = at(layout, nx, ny), step = cost(next);
      if (!step) continue;
      // A small turn penalty keeps routes straight and readable.
      const turn = previous[current] >= 0 && (previous[current] % layout.cols === cx) !== (dx === 0) ? 0.4 : 0;
      const value = g[current] + step + turn;
      if (value < g[next]) {
        g[next] = value;
        previous[next] = current;
        push(value + Math.abs(nx - to.x) + Math.abs(ny - to.y), next);
      }
    }
  }
  if (!Number.isFinite(g[goal])) return null;
  const path: Point[] = [];
  for (let i = goal; i !== -1; i = previous[i]) path.unshift({ x: i % layout.cols, y: Math.floor(i / layout.cols) });
  return path;
}

export function isWalkable(layout: Layout<any>, point: Point) {
  return inside(layout, point.x, point.y) && layout.cost[at(layout, point.x, point.y)] > 0;
}

export function groundOf(layout: Layout<any>, x: number, y: number) {
  return groundAt(layout, x, y);
}
