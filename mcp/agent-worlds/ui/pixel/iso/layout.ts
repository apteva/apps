// Island layout for the isometric village. Each region (project) is one
// island: a green with a well in the middle, buildings spread around it with
// room to breathe, footpaths that wander from every door to the green, then a
// pond, a garden and trees. Pure function of the input, so maps never shuffle.
import { rng, seedFor, type Point } from "../core";
import { categoryFor, type Category } from "../categories";
import type { PlaceInput, RegionInput } from "../layout";
import { ARCHETYPES, ROOF, archetypeFor, interiorFor, type ArchetypeId, type FloorKind, type Interior, type WallKind } from "./archetypes";
import { hash, lighten } from "./raster";

export const G = { sea: 0, grass: 1, path: 2, plaza: 3, pond: 4, soil: 5, floor: 6 } as const;

export type IsoPlace = PlaceInput & {
  category: Category; archetype: ArchetypeId; region: number; seed: number;
  x: number; y: number; w: number; d: number;
  door: { side: "x" | "y"; at: number }; doorOut: Point; doorIn: Point; gable: "x" | "y";
  wall: WallKind; wallColor: string; roof: string; floor: FloorKind; floorColor: string; rug?: [number, number, number, number];
  interior: Interior;
};
export type IsoPropKind = "tree" | "pine" | "bush" | "rock" | "lamp" | "bench" | "well" | "stall" | "fence" | "barrel" | "crate" | "sign";
export type IsoProp = { kind: IsoPropKind; x: number; y: number; variant: number; region: number; axis?: "x" | "y"; place?: string };
export type IsoRegion<F = unknown> = {
  id: string; label: string; focus?: F;
  x: number; y: number; size: number; cx: number; cy: number; radius: number;
  spots: Point[]; residents: string[]; places: string[];
  pond?: { cx: number; cy: number; rx: number; ry: number };
};
export type IsoLayout<F = unknown> = {
  cols: number; rows: number;
  ground: Uint8Array; house: Int16Array; blocked: Uint8Array;
  places: IsoPlace[]; props: IsoProp[]; regions: IsoRegion<F>[];
  route: (from: Point, to: Point) => Point[];
};

const GAP = 3; // clear tiles between buildings
const GREEN = 2.6;

type Lot = { input: PlaceInput; category: Category; archetype: ArchetypeId; w: number; d: number; x: number; y: number };

// Place buildings around the origin (the green) in local tile coordinates,
// growing the island until everything fits with the required gaps.
function placeLots(input: RegionInput<unknown>) {
  const lots: Lot[] = input.places.map(place => {
    const category = categoryFor(place.name, place.kind), archetype = archetypeFor(category);
    return { input: place, category, archetype: archetype.id, w: archetype.w, d: archetype.d, x: 0, y: 0 };
  }).sort((a, b) => b.w * b.d - a.w * a.d || a.input.id.localeCompare(b.input.id));
  const rectDistance = (x: number, y: number, w: number, d: number) => Math.hypot(Math.max(x - 0, 0, 0 - (x + w)), Math.max(y - 0, 0, 0 - (y + d)));
  for (let radius = 8 + lots.length * 1.25; ; radius += 2) {
    const random = rng(seedFor(input.id) ^ 0x150);
    const placed: Lot[] = [];
    let fits = true;
    for (const lot of lots) {
      let best: { x: number; y: number; score: number } | null = null;
      const span = Math.ceil(radius);
      for (let y = -span; y <= span; y++) for (let x = -span; x <= span; x++) {
        const jitter = random();
        if (rectDistance(x, y, lot.w, lot.d) < GREEN + 2.2) continue;
        const far = Math.max(Math.hypot(x, y), Math.hypot(x + lot.w, y), Math.hypot(x, y + lot.d), Math.hypot(x + lot.w, y + lot.d));
        if (far > radius - 1.5) continue;
        if (placed.some(q => x < q.x + q.w + GAP && q.x < x + lot.w + GAP && y < q.y + q.d + GAP && q.y < y + lot.d + GAP)) continue;
        const score = Math.hypot(x + lot.w / 2, y + lot.d / 2) + jitter * 3;
        if (!best || score < best.score) best = { x, y, score };
      }
      if (!best) { fits = false; break; }
      placed.push({ ...lot, x: best.x, y: best.y });
    }
    if (fits || radius > 80) {
      const reach = Math.max(6, ...placed.map(l => Math.max(Math.hypot(l.x, l.y), Math.hypot(l.x + l.w, l.y), Math.hypot(l.x, l.y + l.d), Math.hypot(l.x + l.w, l.y + l.d))));
      return { lots: placed, reach };
    }
  }
}

export function buildIsoLayout<F>(inputs: RegionInput<F>[], portrait: boolean): IsoLayout<F> {
  const regions = inputs.length ? inputs : [{ id: "empty", label: "", places: [], residents: [] } as RegionInput<F>];
  const plans = regions.map(region => placeLots(region as RegionInput<unknown>));
  const plot = Math.max(...plans.map(plan => 2 * Math.ceil(plan.reach + 4.5) + 2));
  const columns = regions.length === 1 ? 1 : portrait ? Math.min(2, regions.length) : Math.ceil(Math.sqrt(regions.length));
  const rowsOfPlots = Math.ceil(regions.length / columns);
  const cols = plot * columns, rows = plot * rowsOfPlots;
  const layout: IsoLayout<F> = {
    cols, rows,
    ground: new Uint8Array(cols * rows), house: new Int16Array(cols * rows), blocked: new Uint8Array(cols * rows),
    places: [], props: [], regions: [], route: () => [],
  };
  regions.forEach((input, index) => carve(layout, input, plans[index], index, (index % columns) * plot, Math.floor(index / columns) * plot, plot));
  const cache = new Map<string, Point[]>();
  layout.route = (from, to) => {
    const key = `${from.x},${from.y}>${to.x},${to.y}`;
    let path = cache.get(key);
    if (!path) {
      path = astar(layout, from, to, walkCost(layout)) || [from];
      if (cache.size > 4000) cache.clear();
      cache.set(key, path);
    }
    return path;
  };
  return layout;
}

const index = (layout: IsoLayout<any>, x: number, y: number) => y * layout.cols + x;
const inside = (layout: IsoLayout<any>, x: number, y: number) => x >= 0 && y >= 0 && x < layout.cols && y < layout.rows;
export function groundAt(layout: IsoLayout<any>, x: number, y: number) {
  return inside(layout, x, y) ? layout.ground[index(layout, x, y)] : G.sea;
}
export function placeAt(layout: IsoLayout<any>, x: number, y: number): IsoPlace | undefined {
  if (!inside(layout, Math.floor(x), Math.floor(y))) return undefined;
  const n = layout.house[index(layout, Math.floor(x), Math.floor(y))];
  return n ? layout.places[n - 1] : undefined;
}

function carve<F>(layout: IsoLayout<F>, input: RegionInput<F>, plan: ReturnType<typeof placeLots>, regionIndex: number, rx: number, ry: number, size: number) {
  const random = rng(seedFor(input.id) ^ 0x7a11);
  const cx = rx + Math.floor(size / 2), cy = ry + Math.floor(size / 2);
  const region: IsoRegion<F> = { id: input.id, label: input.label, focus: input.focus, x: rx, y: ry, size, cx, cy, radius: plan.reach + 2.5, spots: [], residents: input.residents, places: [] };
  layout.regions.push(region);
  const set = (x: number, y: number, value: number) => { if (inside(layout, x, y)) layout.ground[index(layout, x, y)] = value; };
  const block = (x: number, y: number) => { if (inside(layout, Math.floor(x), Math.floor(y))) layout.blocked[index(layout, Math.floor(x), Math.floor(y))] = 1; };

  // Buildings, in absolute tiles.
  const places: IsoPlace[] = plan.lots.map(lot => {
    const x = cx + lot.x, y = cy + lot.y, seed = seedFor(lot.input.id), archetype = ARCHETYPES[lot.archetype];
    const toGreenX = cx - (x + lot.w / 2), toGreenY = cy - (y + lot.d / 2);
    const side: "x" | "y" = toGreenY >= toGreenX ? "x" : "y";
    const length = side === "x" ? lot.w : lot.d;
    const at = Math.max(1, Math.min(length - 2, Math.round((length - 1) / 2 + (hash(seed, 1) - 0.5) * 2)));
    const doorOut = side === "x" ? { x: x + at, y: y + lot.d } : { x: x + lot.w, y: y + at };
    const doorIn = side === "x" ? { x: x + at, y: y + lot.d - 1 } : { x: x + lot.w - 1, y: y + at };
    const roof = lighten(ROOF[lot.category], Math.round((hash(seed, 2) - 0.5) * 24));
    const place: IsoPlace = {
      ...lot.input, category: lot.category, archetype: lot.archetype, region: regionIndex, seed,
      x, y, w: lot.w, d: lot.d, door: { side, at }, doorOut, doorIn,
      gable: hash(seed, 3) < 0.5 ? "x" : "y",
      wall: archetype.walls[seed % archetype.walls.length], wallColor: archetype.wallColors[(seed >>> 3) % archetype.wallColors.length],
      roof, floor: archetype.floor, floorColor: archetype.floorColor, rug: archetype.rug,
      interior: interiorFor({ x, y, w: lot.w, d: lot.d, archetype: lot.archetype, roof, seed }),
    };
    layout.places.push(place);
    region.places.push(place.id);
    return place;
  });
  const nearHouse = (x: number, y: number, pad: number) => places.some(p => x >= p.x - pad && x < p.x + p.w + pad && y >= p.y - pad && y < p.y + p.d + pad);

  // Land: a wobbly disc that always contains every building.
  const phase = hash(seedFor(input.id), 4) * 6;
  for (let y = ry; y < ry + size; y++) for (let x = rx; x < rx + size; x++) {
    const a = Math.atan2(y + 0.5 - cy, x + 0.5 - cx), d = Math.hypot(x + 0.5 - cx, y + 0.5 - cy);
    const edge = region.radius + 1.1 * Math.sin(a * 3 + phase) + 0.7 * Math.sin(a * 5 + phase * 2) + 0.4 * Math.sin(a * 9);
    if (d < edge || nearHouse(x, y, 1)) set(x, y, G.grass);
  }
  for (const [n, p] of places.entries()) for (let y = p.y; y < p.y + p.d; y++) for (let x = p.x; x < p.x + p.w; x++) {
    set(x, y, G.floor);
    layout.house[index(layout, x, y)] = layout.places.indexOf(p) + 1 || n + 1;
  }
  // Furniture blocks the tiles it covers, except the threshold and work spots.
  for (const place of places) {
    const keep = new Set([`${place.doorIn.x},${place.doorIn.y}`, ...place.interior.spots.map(s => `${Math.floor(s.x)},${Math.floor(s.y)}`)]);
    for (const piece of place.interior.pieces) for (let y = Math.floor(piece.foot.y0); y < piece.foot.y1; y++) for (let x = Math.floor(piece.foot.x0); x < piece.foot.x1; x++) {
      if (x + 0.5 > piece.foot.x0 && x + 0.5 < piece.foot.x1 && y + 0.5 > piece.foot.y0 && y + 0.5 < piece.foot.y1 && !keep.has(`${x},${y}`)) block(x, y);
    }
  }
  for (let y = cy - 4; y <= cy + 4; y++) for (let x = cx - 4; x <= cx + 4; x++) {
    if (groundAt(layout, x, y) === G.grass && Math.hypot(x - cx, y - cy) < GREEN + hash(x, y) * 0.6) set(x, y, G.plaza);
  }

  // Footpaths: from each door towards the green over noisy grass; later paths
  // reuse earlier ones, which makes them merge into a natural network.
  const byDistance = [...places].sort((a, b) => Math.hypot(a.doorOut.x - cx, a.doorOut.y - cy) - Math.hypot(b.doorOut.x - cx, b.doorOut.y - cy));
  for (const place of byDistance) {
    const path = astar(layout, place.doorOut, { x: cx, y: cy }, i => {
      const ground = layout.ground[i];
      if (ground === G.sea || ground === G.floor) return 0;
      if (ground === G.path || ground === G.plaza) return 0.5;
      return 1.6 + hash(i % layout.cols * 3, Math.floor(i / layout.cols) * 7) * 2.5;
    }) || [];
    for (const point of path) if (groundAt(layout, point.x, point.y) === G.grass) set(point.x, point.y, G.path);
  }

  // Free grass with a margin: nothing but grass around it and no building nearby.
  const open = (x0: number, y0: number, w: number, h: number) => {
    for (let y = y0 - 1; y <= y0 + h; y++) for (let x = x0 - 1; x <= x0 + w; x++) if (groundAt(layout, x, y) !== G.grass) return false;
    return !nearHouse(x0 - 1, y0 - 1, 0) && !places.some(p => x0 - 2 < p.x + p.w && p.x < x0 + w + 2 && y0 - 2 < p.y + p.d && p.y < y0 + h + 2);
  };
  const candidates: Point[] = [];
  for (let y = ry + 2; y < ry + size - 2; y++) for (let x = rx + 2; x < rx + size - 2; x++) {
    const d = Math.hypot(x - cx, y - cy);
    if (d > 4.5 && d < region.radius - 3.5) candidates.push({ x, y });
  }
  candidates.sort((a, b) => hash(a.x * 7 + regionIndex, a.y * 13) - hash(b.x * 7 + regionIndex, b.y * 13));

  const pondAt = candidates.find(c => open(c.x - 3, c.y - 2, 6, 4));
  if (pondAt) {
    const pond = { cx: pondAt.x, cy: pondAt.y, rx: 2.6, ry: 1.9 };
    region.pond = pond;
    for (let y = pondAt.y - 2; y < pondAt.y + 2; y++) for (let x = pondAt.x - 3; x < pondAt.x + 3; x++) {
      if (((x + 0.5 - pond.cx) / pond.rx) ** 2 + ((y + 0.5 - pond.cy) / pond.ry) ** 2 < 1) set(x, y, G.pond);
    }
  }
  const gardenAt = candidates.find(c => open(c.x, c.y, 4, 3));
  if (gardenAt) {
    for (let y = gardenAt.y; y < gardenAt.y + 3; y++) for (let x = gardenAt.x; x < gardenAt.x + 4; x++) set(x, y, G.soil);
    for (let i = 0; i < 4; i++) { layout.props.push({ kind: "fence", x: gardenAt.x + i, y: gardenAt.y - 0.12, variant: 0, region: regionIndex, axis: "x" }); block(gardenAt.x + i, gardenAt.y - 1); }
    for (let i = 0; i < 3; i++) layout.props.push({ kind: "fence", x: gardenAt.x - 0.12, y: gardenAt.y + i, variant: 0, region: regionIndex, axis: "y" });
  }

  // Village furniture on and around the green.
  const prop = (kind: IsoPropKind, x: number, y: number, variant = 0, extra: Partial<IsoProp> = {}, blocks = true) => {
    layout.props.push({ kind, x, y, variant, region: regionIndex, ...extra });
    if (blocks) block(x, y);
  };
  prop("well", cx, cy);
  const greenSpot = (dx: number, dy: number) => { const g = groundAt(layout, cx + dx, cy + dy); return g === G.plaza || g === G.grass; };
  if (greenSpot(1, -3)) prop("bench", cx + 0.6, cy - 2.6, 0, { axis: "x" });
  if (greenSpot(-3, 1)) prop("bench", cx - 2.6, cy + 0.6, 0, { axis: "y" });
  if (greenSpot(2, 2) && greenSpot(3, 2)) { prop("stall", cx + 1.6, cy + 1.6); block(cx + 2.6, cy + 1.6); }
  for (const [dx, dy] of [[-3, -3], [3, -3], [-3, 3], [3, 3], [0, -4], [4, 0]]) {
    const x = cx + dx, y = cy + dy;
    if (groundAt(layout, x, y) === G.grass && (groundAt(layout, x + 1, y) === G.path || groundAt(layout, x, y + 1) === G.path || groundAt(layout, x - 1, y) === G.path || groundAt(layout, x, y - 1) === G.path || Math.abs(dx) + Math.abs(dy) === 4)) prop("lamp", x + 0.4, y + 0.4);
  }
  for (const place of places) {
    const signX = place.door.side === "x" ? place.doorOut.x + 1.05 : place.doorOut.x + 0.25, signY = place.door.side === "x" ? place.doorOut.y + 0.25 : place.doorOut.y + 1.05;
    if (groundAt(layout, Math.floor(signX), Math.floor(signY)) !== G.sea) prop("sign", signX, signY, 0, { place: place.id }, false);
    const sideX = place.x + place.w + 0.3, sideY = place.y + 0.5;
    if (hash(place.seed, 5) < 0.6 && groundAt(layout, Math.floor(sideX), Math.floor(sideY)) === G.grass) prop(hash(place.seed, 6) < 0.5 ? "barrel" : "crate", sideX, sideY, 0);
    const bushX = place.door.side === "x" ? place.doorOut.x - 0.6 : place.doorOut.x + 0.5, bushY = place.door.side === "x" ? place.doorOut.y + 0.5 : place.doorOut.y - 0.6;
    if (groundAt(layout, Math.floor(bushX), Math.floor(bushY)) === G.grass) prop("bush", bushX, bushY, 1 + Math.floor(hash(place.seed, 7) * 3));
  }

  // Trees: a forest ring along the coast, a few inland; never on paths, by doors or crowding houses.
  const near = (x: number, y: number, kinds: number[], reach = 1) => {
    for (let dy = -reach; dy <= reach; dy++) for (let dx = -reach; dx <= reach; dx++) if (kinds.includes(groundAt(layout, x + dx, y + dy))) return true;
    return false;
  };
  for (let y = ry; y < ry + size; y++) for (let x = rx; x < rx + size; x++) {
    if (groundAt(layout, x, y) !== G.grass || layout.blocked[index(layout, x, y)]) continue;
    if (near(x, y, [G.path, G.plaza, G.pond, G.soil, G.floor]) || nearHouse(x, y, 2)) continue;
    const coast = near(x, y, [G.sea], 2), roll = random(), jx = random() * 0.5 + 0.25, jy = random() * 0.5 + 0.25;
    if (roll < (coast ? 0.45 : 0.07)) prop(coast && random() < 0.35 ? "pine" : "tree", x + jx, y + jy, Math.floor(random() * 3));
    else if (roll < (coast ? 0.55 : 0.15)) prop("bush", x + jx, y + jy, random() < 0.5 ? 1 + Math.floor(random() * 3) : 0);
  }
  for (let y = ry; y < ry + size; y++) for (let x = rx; x < rx + size; x++) {
    if (groundAt(layout, x, y) === G.sea && near(x, y, [G.grass], 1) && random() < 0.05) prop("rock", x + 0.3, y + 0.3, Math.floor(random() * 3));
  }

  // Places to stroll to: the green, a few path tiles, the pond shore.
  for (let y = ry; y < ry + size; y++) for (let x = rx; x < rx + size; x++) {
    if (layout.blocked[index(layout, x, y)]) continue;
    const ground = groundAt(layout, x, y);
    if (ground === G.plaza && random() < 0.6) region.spots.push({ x, y });
    else if (ground === G.path && random() < 0.05) region.spots.push({ x, y });
    else if (ground === G.grass && near(x, y, [G.pond]) && random() < 0.35) region.spots.push({ x, y });
  }
  if (!region.spots.length) region.spots.push({ x: cx + 1, y: cy });
}

// Walking rules: water, crops and props block; a building is entered only
// through its door, and its interior floor is free to cross.
function walkCost(layout: IsoLayout<any>) {
  return (i: number) => {
    if (layout.blocked[i]) return 0;
    const ground = layout.ground[i];
    return ground === G.path || ground === G.plaza || ground === G.floor ? 1 : ground === G.grass ? 3 : 0;
  };
}
export function canStep(layout: IsoLayout<any>, a: number, b: number) {
  const ha = layout.house[a], hb = layout.house[b];
  if (ha === hb) return true;
  if (ha && hb) return false;
  const place = layout.places[(ha || hb) - 1];
  const out = index(layout, place.doorOut.x, place.doorOut.y), into = index(layout, place.doorIn.x, place.doorIn.y);
  return (a === into && b === out) || (a === out && b === into);
}

function astar(layout: IsoLayout<any>, from: Point, to: Point, cost: (i: number) => number): Point[] | null {
  if (!inside(layout, from.x, from.y) || !inside(layout, to.x, to.y)) return null;
  const start = index(layout, from.x, from.y), goal = index(layout, to.x, to.y), n = layout.cols * layout.rows;
  const g = new Float64Array(n).fill(Infinity), previous = new Int32Array(n).fill(-1);
  const heap: [number, number][] = [];
  const push = (f: number, i: number) => {
    heap.push([f, i]);
    for (let c = heap.length - 1; c > 0;) { const p = (c - 1) >> 1; if (heap[p][0] <= heap[c][0]) break; [heap[p], heap[c]] = [heap[c], heap[p]]; c = p; }
  };
  const pop = () => {
    const top = heap[0], last = heap.pop()!;
    if (heap.length) {
      heap[0] = last;
      for (let c = 0; ;) {
        const l = c * 2 + 1, r = l + 1;
        let m = c;
        if (l < heap.length && heap[l][0] < heap[m][0]) m = l;
        if (r < heap.length && heap[r][0] < heap[m][0]) m = r;
        if (m === c) break;
        [heap[m], heap[c]] = [heap[c], heap[m]];
        c = m;
      }
    }
    return top;
  };
  g[start] = 0;
  push(0, start);
  while (heap.length) {
    const [f, current] = pop();
    if (current === goal) break;
    const x = current % layout.cols, y = Math.floor(current / layout.cols);
    if (f - Math.abs(x - to.x) - Math.abs(y - to.y) > g[current] + 1e-9) continue;
    for (const [dx, dy] of [[1, 0], [-1, 0], [0, 1], [0, -1]]) {
      const nx = x + dx, ny = y + dy;
      if (!inside(layout, nx, ny)) continue;
      const next = index(layout, nx, ny);
      if (!canStep(layout, current, next)) continue;
      const step = next === goal ? 1 : cost(next);
      if (!step) continue;
      const turn = previous[current] >= 0 && (previous[current] % layout.cols === x) !== (dx === 0) ? 0.4 : 0;
      const value = g[current] + step + turn;
      if (value < g[next]) { g[next] = value; previous[next] = current; push(value + Math.abs(nx - to.x) + Math.abs(ny - to.y), next); }
    }
  }
  if (!Number.isFinite(g[goal])) return null;
  const path: Point[] = [];
  for (let i = goal; i !== -1; i = previous[i]) path.unshift({ x: i % layout.cols, y: Math.floor(i / layout.cols) });
  return path;
}

export function isWalkableTile(layout: IsoLayout<any>, point: Point) {
  if (!inside(layout, point.x, point.y)) return false;
  return walkCost(layout)(index(layout, point.x, point.y)) > 0;
}
