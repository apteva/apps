// Everything in the village that never moves, painted once per layout: the
// ground (sea, cliffs, grass, paths, pond, floors) into one buffer, and every
// standing thing (walls, furniture, trees, props) as an outlined sprite with a
// depth key. The static scene is then baked; only agents are drawn per frame.
import { makeCanvas } from "../core";
import { FZ, GABLE, T, WALL, barrel, crate, floorTexture, wallTexture } from "./archetypes";
import { G, groundAt, type IsoLayout, type IsoPlace, type IsoProp } from "./layout";
import { BufferPainter, Painter, TW, TH, boxCorners, hash, lighten, sprite, tone, type Fill, type Sprite } from "./raster";

export type Static = Sprite & { key: number; gx: number; gy: number };
export type Scene = {
  width: number; height: number; ox: number; oy: number;
  ground: HTMLCanvasElement; baked: HTMLCanvasElement; statics: Static[];
  houses: Map<string, { x: number; y: number; w: number; h: number; card: [number, number]; chimney: [number, number] }>;
  islands: { id: string; x: number; y: number; w: number; h: number; top: [number, number] }[];
};

export const SEA = "#5cc6ef";
const GRASS = ["#9be564", "#8fdb5a", "#83cf50"];
const TIMBER = "#7a4b2e", WOOD = "#c98f5b";
const LEAVES = [["#c1f07a", "#7fd04f", "#4aab45", "#2f7d3c"], ["#d6f57a", "#9ad94a", "#5cb33a", "#3a8a33"], ["#9ef0c0", "#4fcf8a", "#2aa070", "#1d7556"]];
const FLOWERS = ["#ff8fab", "#ffffff", "#ffd166"];
const MARGIN = { side: 40, top: 96, bottom: 40 };

const XV = (n: number): [number, number] => [TW * n, TH * n];
const YV = (n: number): [number, number] => [-TW * n, TH * n];
const ZV = (h: number): [number, number] => [0, -h];
const isLand = (layout: IsoLayout<any>, x: number, y: number) => groundAt(layout, x, y) !== G.sea;

export function paintScene(layout: IsoLayout<any>): Scene {
  // Frame the land (plus headroom for roofs and trees), not the whole grid.
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (let y = 0; y < layout.rows; y++) for (let x = 0; x < layout.cols; x++) {
    if (!isLand(layout, x, y)) continue;
    for (const [px, py] of [[x, y], [x + 1, y], [x, y + 1], [x + 1, y + 1]]) {
      const sx = (px - py) * TW, sy = (px + py) * TH;
      minX = Math.min(minX, sx); maxX = Math.max(maxX, sx); minY = Math.min(minY, sy); maxY = Math.max(maxY, sy + 12);
    }
  }
  if (!Number.isFinite(minX)) { minX = 0; minY = 0; maxX = 64; maxY = 32; }
  const ox = -minX + MARGIN.side, oy = -minY + MARGIN.top;
  const width = Math.ceil(maxX - minX + MARGIN.side * 2), height = Math.ceil(maxY - minY + MARGIN.top + MARGIN.bottom);
  const origin = { ox, oy };
  const statics: Static[] = [];
  const shadows: [number, number, number][] = [];
  const add = (corners: [number, number, number][], draw: (p: Painter) => void, key: number, gx = 0, gy = 0) => statics.push({ ...sprite(origin, corners, draw), key, gx, gy });

  const houses: Scene["houses"] = new Map();
  for (const place of layout.places) {
    paintHouse(place, add);
    const probe = new Painter(null as any, ox, oy);
    const top = probe.P(place.x - 0.2, place.y - 0.2, FZ + WALL + GABLE + 18), left = probe.P(place.x - 0.2, place.y + place.d + 0.2), right = probe.P(place.x + place.w + 0.2, place.y - 0.2), bottom = probe.P(place.x + place.w, place.y + place.d);
    const card = place.gable === "y" ? probe.P(place.x + 0.2, place.y + place.d / 2, FZ + WALL + GABLE + 10) : probe.P(place.x + place.w / 2, place.y + 0.2, FZ + WALL + GABLE + 10);
    const chimney = place.gable === "y" ? probe.P(place.x + place.w - 1.5 + 0.27, place.y - 0.15 + 0.27, FZ + WALL + 17) : probe.P(place.x - 0.15 + 0.27, place.y + place.d - 1.5 + 0.27, FZ + WALL + 17);
    houses.set(place.id, { x: left[0], y: top[1], w: right[0] - left[0], h: bottom[1] - top[1], card, chimney });
  }
  const placeById = new Map(layout.places.map(place => [place.id, place]));
  for (const prop of layout.props) paintProp(prop, add, shadows, placeById);

  // Things standing just behind a house draw before its back walls.
  for (const item of statics) if (item.gx || item.gy) item.key = depthKey(layout, item.gx + 0.01, item.gy + 0.01, item.key);
  statics.sort((a, b) => a.key - b.key);

  const ground = paintGround(layout, width, height, ox, oy, shadows);
  const [baked, bctx] = makeCanvas(width, height);
  bctx.drawImage(ground, 0, 0);
  for (const item of statics) bctx.drawImage(item.canvas, item.x, item.y);

  const islands = layout.regions.map(region => {
    const r = region.radius + 2, probe = (x: number, y: number) => [ox + (x - y) * TW, oy + (x + y) * TH];
    const top = probe(region.cx - r * 0.7, region.cy - r * 0.7), left = probe(region.cx - r, region.cy + r), right = probe(region.cx + r, region.cy - r), bottom = probe(region.cx + r * 0.7, region.cy + r * 0.7);
    return { id: region.id, x: left[0], y: top[1] - 40, w: right[0] - left[0], h: bottom[1] - top[1] + 40, top: [top[0], top[1] - 44] as [number, number] };
  });
  return { width, height, ox, oy, ground, baked, statics, houses, islands };
}

// Depth key for anything at tile (x, y): roughly x + y, pulled in front of
// nothing but pushed behind the back walls of a house it stands behind.
export function depthKey(layout: IsoLayout<any>, x: number, y: number, base: number) {
  // Inside a building its own walls frame it; neighbours must not pull it behind them.
  if (layout.places.some(r => x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.d)) return base;
  for (const r of layout.places) {
    if ((x < r.x + 0.2 && x > r.x - 5 && y > r.y - 5 && y < r.y + r.d + 0.3) || (y < r.y + 0.2 && y > r.y - 5 && x > r.x - 5 && x < r.x + r.w + 0.3)) base = Math.min(base, r.x + r.y - 0.2);
  }
  return base;
}

function paintGround(layout: IsoLayout<any>, width: number, height: number, ox: number, oy: number, shadows: [number, number, number][]) {
  const [canvas, ctx] = makeCanvas(width, height);
  const image = ctx.createImageData(canvas.width, canvas.height);
  const p = new BufferPainter(image.data, canvas.width, canvas.height, ox, oy);
  p.px(SEA, 0, 0, canvas.width, canvas.height);
  for (let i = 0; i < width * height / 2400; i++) p.px(hash(i, 3) < 0.5 ? "#7fd6f5" : "#8ee0fa", hash(i, 1) * width, hash(i, 2) * height, 3 + Math.floor(hash(i, 4) * 4), 1);
  const land = (x: number, y: number) => isLand(layout, x, y);
  const is = (x: number, y: number, ...kinds: number[]) => kinds.includes(groundAt(layout, x, y));

  for (let y = -1; y <= layout.rows; y++) for (let x = -1; x <= layout.cols; x++) {
    if (land(x, y)) continue;
    let near = false;
    for (let dy = -1; dy <= 1 && !near; dy++) for (let dx = -1; dx <= 1; dx++) if (land(x + dx, y + dy)) { near = true; break; }
    if (near) p.para(p.P(x, y, -10), XV(1), YV(1), (_u, _v, X, Y) => ((X * 3 + Y * 5) % 23 === 0 ? "#b6efff" : "#7ad6f4"));
  }
  for (let y = 0; y < layout.rows; y++) for (let x = 0; x < layout.cols; x++) {
    const g = groundAt(layout, x, y);
    if (g === G.sea || g === G.floor) continue;
    let fill: Fill;
    if (g === G.plaza) fill = (u, v) => {
      const a = u * 16, b = v * 16;
      if (a % 8 < 1 || (b + (Math.floor(a / 8) % 2) * 4) % 8 < 1) return "#cfc6b4";
      return hash(Math.floor(a / 8) + x * 3, Math.floor(b / 8) + y * 5) < 0.3 ? "#ece4d2" : "#e2d9c5";
    };
    else if (g === G.path) fill = (u, v, X, Y) => {
      const e = 0.18 + hash(X, Y) * 0.12, path = (a: number, b: number) => is(a, b, G.path, G.plaza, G.floor);
      if ((u < e && !path(x - 1, y)) || (u > 1 - e && !path(x + 1, y)) || (v < e && !path(x, y - 1)) || (v > 1 - e && !path(x, y + 1))) return GRASS[(x + y) % 2];
      return hash(X, Y) < 0.06 ? "#d9b77f" : hash(X + 1, Y) < 0.04 ? "#fff1cf" : "#f0d39c";
    };
    else if (g === G.pond) fill = (u, v, X, Y) => {
      const edge = ([[-1, 0, u], [1, 0, 1 - u], [0, -1, v], [0, 1, 1 - v]] as const).some(([dx, dy, t]) => !is(x + dx, y + dy, G.pond) && t < 0.18);
      return edge ? "#e9d7a6" : hash(X, Y) < 0.02 ? "#c4f3ff" : (X + Y) % 11 === 0 && Y % 3 === 0 ? "#6fd0f0" : "#4fb8e6";
    };
    else if (g === G.soil) fill = (u, v) => { const b = v * 16; return b % 4 < 1.6 ? "#9c6a3e" : Math.floor(u * 16) % 3 === 1 && b % 4 > 2.4 ? (y % 2 ? "#5fbf3a" : "#ffb703") : "#b47d4b"; };
    else fill = (_u, _v, X, Y) => {
      const h = hash(X, Y);
      if (h < 0.012) return ["#ffffff", "#ffd166", "#ff8fab", "#a0c4ff"][Math.floor(hash(Y, X) * 4)];
      if (h < 0.05) return "#b4f07c";
      if (h > 0.985) return "#6dbb42";
      return GRASS[Math.floor(hash(x, y) * 3)];
    };
    p.para(p.P(x, y, 0), XV(1), YV(1), fill);
  }
  // Cliff faces where land meets sea, with a grass lip and foam at the waterline.
  const cliff: Fill = (_u, v, X, Y) => (v > 0.85 ? "#6cbd3e" : v < 0.15 ? "#c9f2ff" : Y % 4 === 0 && hash(X, Y) < 0.7 ? "#9a6a40" : "#b97f4b");
  const cliffSide: Fill = (u, v, X, Y) => { const c = (cliff as Function)(u, v, X, Y); return c === "#b97f4b" ? "#a26c3d" : c === "#9a6a40" ? "#86572f" : c; };
  for (let y = 0; y < layout.rows; y++) for (let x = 0; x < layout.cols; x++) {
    if (!land(x, y)) continue;
    if (!land(x, y + 1)) p.para(p.P(x, y + 1, -10), XV(1), ZV(10), cliff);
    if (!land(x + 1, y)) p.para(p.P(x + 1, y, -10), YV(1), ZV(10), cliffSide);
  }
  for (const r of layout.places) {
    p.box(r.x, r.y, 0, r.w, r.d, FZ, { top: floorTexture(r.floor, r.floorColor, r.w, r.d), left: lighten(r.wallColor, -50), right: lighten(r.wallColor, -70) });
    if (r.rug) {
      const [a, b, w, d] = r.rug;
      p.para(p.P(r.x + a, r.y + b, FZ), XV(w), YV(d), (u, v) => {
        const e = Math.min(u * w, (1 - u) * w, v * d, (1 - v) * d);
        return e < 0.12 ? lighten(r.roof, -20) : e < 0.22 ? "#fff6e0" : (Math.floor(u * w * 6) + Math.floor(v * d * 6)) % 2 ? r.roof : lighten(r.roof, 22);
      });
    }
    p.para(p.P(r.doorOut.x + 0.15, r.doorOut.y + 0.15, 0.5), XV(0.7), YV(0.7), "#cfc6b4");
  }
  for (const [x, y, R] of shadows) {
    const [sx, sy] = p.P(x, y, 0);
    for (let dy = -R; dy <= R; dy++) for (let dx = -2 * R; dx <= 2 * R; dx++) if ((dx / (2 * R)) ** 2 + (dy / R) ** 2 < 1) p.shade(Math.round(sx + dx + 3), Math.round(sy + dy + 2), 0.82, 0.86, 0.8);
  }
  ctx.putImageData(image, 0, 0);
  return canvas;
}

type Add = (corners: [number, number, number][], draw: (p: Painter) => void, key: number, gx?: number, gy?: number) => void;
const bb = boxCorners;

function windowFill(w: number, h: number, curtain: string, frame = "#ffffff"): Fill {
  return (u, v) => {
    const s = u * w * 18, z = v * h, W = w * 18;
    if (s < 1.5 || s > W - 1.5 || z < 1.5 || z > h - 1.5 || Math.abs(s - W / 2) < 0.8 || Math.abs(z - h * 0.55) < 0.6) return frame;
    if (s < 4 || s > W - 4) return z > h - 4 ? lighten(curtain, 20) : curtain;
    return (s - z * 0.8) % 9 < 2 ? "#d9f4ff" : "#8fd6ff";
  };
}

function paintHouse(r: IsoPlace, add: Add) {
  const sill = r.wall === "logs" || r.wall === "planks" ? "#ffffff" : lighten(r.wallColor, 20);
  const post = tone(r.wall === "stone" || r.wall === "brick" ? lighten(r.wallColor, -30) : TIMBER);
  const used = r.interior.used;
  const free = (axis: "x" | "y", a0: number, length: number) => !used.some(u => u.axis === axis && a0 < u.a0 + u.length + 0.1 && u.a0 < a0 + length + 0.1);
  add(bb(r.x - 0.2, r.y - 0.2, 0, r.w + 0.2, r.d + 0.2, FZ + WALL + GABLE + 18), p => {
    const roof = r.roof, roofDark = lighten(roof, -36);
    // Back-left wall (runs along y) and back-right wall (runs along x), inner faces textured.
    p.box(r.x, r.y, FZ, T, r.d, WALL, { top: roof, left: lighten(r.wallColor, -40), right: wallTexture(r.wall, lighten(r.wallColor, 6), r.d, WALL) });
    p.box(r.x, r.y, FZ, r.w, T, WALL, { top: roof, left: wallTexture(r.wall, r.wallColor, r.w, WALL), right: lighten(r.wallColor, -50) });
    // Gable end above one back wall: same material, roof-coloured verges, a lit attic window.
    const gable = (base: Fill): Fill => (u, v, X, Y) => {
      const hh = v * (GABLE + 3), peak = (1 - Math.abs(2 * u - 1)) * (GABLE + 3);
      if (hh > peak) return null;
      if (hh > peak - 3) return hh > peak - 1.2 ? lighten(roof, 25) : roof;
      if (Math.abs(u - 0.5) < 0.05 && Math.abs(hh - GABLE * 0.42) < 2.5) return "#ffe08a";
      return typeof base === "string" ? base : base(u, v, X, Y);
    };
    if (r.gable === "y") p.wall("y", r.x + T, r.y, FZ + WALL, r.d, GABLE + 3, gable(wallTexture(r.wall, lighten(r.wallColor, 6), r.d, GABLE)));
    else p.wall("x", r.x, r.y + T, FZ + WALL, r.w, GABLE + 3, gable(wallTexture(r.wall, r.wallColor, r.w, GABLE)));
    p.box(r.x - 0.12, r.y - 0.12, FZ + WALL, T + 0.24, r.d + 0.12, 2, { top: roof, left: roofDark, right: roofDark });
    p.box(r.x - 0.12, r.y - 0.12, FZ + WALL, r.w + 0.12, T + 0.24, 2, { top: roof, left: roofDark, right: roofDark });
    // Windows where the inside decor leaves room.
    const windows: ["x" | "y", number][] = [];
    for (const at of [r.w - 1.6, 0.6, r.w / 2 - 0.5]) if (windows.filter(w => w[0] === "x").length < (r.w >= 7 ? 2 : 1) && at > 0.3 && free("x", at, 1) && !windows.some(w => w[0] === "x" && Math.abs(w[1] - at) < 1.6)) windows.push(["x", at]);
    for (const at of [r.d - 1.5, 0.6]) if (windows.filter(w => w[0] === "y").length < 1 && at > 0.3 && free("y", at, 1)) windows.push(["y", at]);
    for (const [axis, at] of windows) {
      if (axis === "x") { p.wall("x", r.x + at, r.y + T, FZ + 10, 1, 11, windowFill(1, 11, r.roof)); p.wall("x", r.x + at - 0.05, r.y + T + 0.02, FZ + 8, 1.1, 2, sill); }
      else { p.wall("y", r.x + T, r.y + at, FZ + 10, 1, 11, windowFill(1, 11, r.roof)); p.wall("y", r.x + T + 0.02, r.y + at - 0.05, FZ + 8, 1.1, 2, sill); }
    }
    for (const decor of r.interior.decorX) decor(p);
    for (const decor of r.interior.decorY) decor(p);
    // Corner posts and a chimney.
    p.box(r.x - 0.05, r.y - 0.05, FZ, T + 0.1, T + 0.1, WALL, post);
    p.box(r.x + r.w - T, r.y - 0.05, FZ, T + 0.05, T + 0.1, WALL, post);
    p.box(r.x - 0.05, r.y + r.d - T, FZ, T + 0.1, T + 0.05, WALL, post);
    const [cx, cy] = r.gable === "y" ? [r.x + r.w - 1.5, r.y - 0.15] : [r.x - 0.15, r.y + r.d - 1.5];
    p.box(cx, cy, FZ + WALL, 0.55, 0.55, 14, { top: "#5a4a48", left: "#a8604a", right: "#8a4c3a" });
    p.box(cx - 0.06, cy - 0.06, FZ + WALL + 14, 0.67, 0.67, 2, { top: "#4a3a38", left: "#c27a60", right: "#9a5a46" });
  }, r.x + r.y);

  for (const piece of r.interior.pieces) add(piece.corners, piece.draw, piece.foot.x1 + piece.foot.y1 - 0.01, piece.foot.x0, piece.foot.y0);

  // Knee-high front walls, cut per tile so things in front and inside sort
  // correctly; posts frame the door.
  const H = 6, c = { top: lighten(r.roof, 10), left: r.wall === "timber" ? r.wallColor : wallTexture(r.wall, r.wallColor, 1, H), right: wallTexture(r.wall, lighten(r.wallColor, -24), 1, H) };
  for (let i = 0; i < r.w; i++) {
    const x = r.x + i, y = r.y + r.d - T, door = r.door.side === "x" && r.door.at === i;
    add(bb(x, y, 0, 1, T, FZ + WALL), p => {
      if (!door) p.box(x, y, FZ, 1, T, H, c);
      else { p.box(x - 0.02, y - 0.05, FZ, 0.18, T + 0.1, 20, post); p.box(x + 0.84, y - 0.05, FZ, 0.18, T + 0.1, 20, post); }
    }, x + 1 + r.y + r.d - 0.45);
  }
  for (let j = 0; j < r.d; j++) {
    const x = r.x + r.w - T, y = r.y + j, door = r.door.side === "y" && r.door.at === j;
    add(bb(x, y, 0, T, 1, FZ + WALL), p => {
      if (!door) p.box(x, y, FZ, T, 1, H, c);
      else { p.box(x - 0.05, y - 0.02, FZ, T + 0.1, 0.18, 20, post); p.box(x - 0.05, y + 0.84, FZ, T + 0.1, 0.18, 20, post); }
    }, r.x + r.w + y + 1 - 0.45);
  }
  const cx = r.x + r.w - T - 0.05, cy = r.y + r.d - T - 0.05;
  add(bb(cx, cy, FZ, T + 0.1, T + 0.1, WALL), p => p.box(cx, cy, FZ, T + 0.1, T + 0.1, WALL - 4, post), r.x + r.w + r.y + r.d - 0.4);
}

function paintProp(prop: IsoProp, add: Add, shadows: [number, number, number][], places: Map<string, IsoPlace>) {
  const { x, y } = prop;
  const B = (w: number, d: number, h: number, draw: (p: Painter) => void, z = 0) => add(bb(x, y, z, w, d, h + 14), draw, x + w + y + d - 0.01, x, y);
  switch (prop.kind) {
    case "tree": {
      const s = 0.65 + hash(y, x) * 0.5, pal = LEAVES[prop.variant % 3], R = Math.round(10 * s), th = Math.round(9 * s);
      shadows.push([x, y, Math.round(5 * s)]);
      add([[x - 1.2 * s, y - 1.2 * s, th + 2 * R + 6], [x + 1.2 * s, y + 1.2 * s, 0], [x + 1.3 * s, y - 1.3 * s, 0], [x - 1.3 * s, y + 1.3 * s, 0]], p => {
        p.box(x - 0.1, y - 0.1, 0, 0.2, 0.2, th + 4, { top: "#8a5a3c", left: "#9a6440", right: "#7a4b2e" });
        const [sx, sy] = p.P(x, y, th + R);
        p.ball(sx, sy, R, pal, x * 7 + y);
        if (s > 0.8) { p.ball(sx - R * 0.55, sy + R * 0.2, Math.round(R * 0.55), pal, x); p.ball(sx + R * 0.6, sy + R * 0.15, Math.round(R * 0.5), pal, y); p.ball(sx + 1, sy - R * 0.45, Math.round(R * 0.55), pal, x + y); }
        if (hash(x, y) < 0.35) for (let i = 0; i < 4; i++) p.px("#ff6b6b", sx - R * 0.6 + hash(i, x) * R * 1.2, sy - R * 0.4 + hash(y, i) * R * 0.9, 2, 2);
      }, x + y + 0.2, x, y);
      return;
    }
    case "pine": {
      const s = 0.9 + hash(x, y) * 0.3;
      shadows.push([x, y, Math.round(4 * s)]);
      add([[x - 1, y - 1, 40 * s], [x + 1, y + 1, 0], [x + 1, y - 1, 0], [x - 1, y + 1, 0]], p => {
        p.box(x - 0.08, y - 0.08, 0, 0.16, 0.16, 6, tone(TIMBER));
        const [sx, sy] = p.P(x, y, 0);
        for (let t = 0; t < 3; t++) {
          const base = sy - 4 - t * 9 * s, hw = Math.round((10 - t * 2.5) * s), hh = Math.round(14 * s);
          for (let dy = 0; dy < hh; dy++) { const w = Math.round(hw * (dy / hh)); for (let dx = -w; dx <= w; dx++) p.px(dx < -w * 0.3 ? "#4fbf6a" : dx > w * 0.4 ? "#1f7a4a" : "#2f9a58", sx + dx, base - hh + dy); }
        }
      }, x + y + 0.2, x, y);
      return;
    }
    case "bush":
      add([[x - 0.6, y - 0.6, 14], [x + 0.6, y + 0.6, 0], [x + 0.7, y - 0.7, 0], [x - 0.7, y + 0.7, 0]], p => {
        const [sx, sy] = p.P(x, y, 5);
        p.ball(sx, sy, 6, LEAVES[0], x + y);
        if (prop.variant) for (let i = 0; i < 5; i++) p.px(FLOWERS[(prop.variant - 1) % 3], sx - 4 + hash(i, x) * 8, sy - 4 + hash(y, i) * 6, 1, 1);
      }, x + y, x, y);
      return;
    case "rock": {
      const w = 0.5 + hash(x, 1) * 0.4;
      add(bb(x, y, -6, w, 0.45, 7), p => p.box(x, y, -6, w, 0.45, 7, { top: "#d6d3dc", left: "#b3afbc", right: "#95909f" }), x + w + y + 0.45);
      return;
    }
    case "lamp":
      B(0.16, 0.16, 18, p => {
        p.box(x, y, 0, 0.16, 0.16, 18, tone("#3d405b"));
        p.box(x - 0.1, y - 0.1, 18, 0.36, 0.36, 5, { top: "#3d405b", left: "#ffe08a", right: "#ffcf5c" });
        p.box(x - 0.14, y - 0.14, 23, 0.44, 0.44, 2, tone("#3d405b"));
      });
      return;
    case "bench": {
      const along = prop.axis !== "y", w = along ? 1.2 : 0.35, d = along ? 0.35 : 1.2;
      B(w, d, 8, p => { p.box(x, y, 0, w, d, 4, tone("#e3b27a")); p.box(x, y, 4, along ? 1.2 : 0.08, along ? 0.08 : 1.2, 4, tone("#e3b27a")); });
      return;
    }
    case "well":
      add(bb(x, y, 0, 1, 1, 34), p => {
        p.box(x, y, 0, 1, 1, 7, { top: (u, v) => (Math.hypot(u - 0.5, v - 0.5) < 0.32 ? "#3a8fc0" : "#c9c2b4"), left: wallTexture("stone", "#d9d2c3", 1, 7), right: wallTexture("stone", "#b9b2a3", 1, 7) });
        p.box(x + 0.05, y + 0.45, 7, 0.1, 0.1, 16, tone("#8a5a3c")); p.box(x + 0.85, y + 0.45, 7, 0.1, 0.1, 16, tone("#8a5a3c"));
        p.box(x - 0.15, y + 0.15, 23, 1.3, 0.7, 3, tone("#ef476f")); p.box(x + 0.05, y + 0.3, 26, 0.9, 0.4, 3, tone("#d43d5f"));
        p.box(x + 0.45, y + 0.45, 12, 0.12, 0.12, 4, tone(WOOD));
      }, x + y + 2, x, y);
      return;
    case "stall":
      add(bb(x, y, 0, 1.6, 1, 30), p => {
        p.box(x, y, 0, 1.6, 0.8, 8, tone(WOOD));
        for (let k = 0; k < 4; k++) { const [sx, sy] = p.P(x + 0.25 + k * 0.38, y + 0.4, 9); p.ball(sx, sy, 2, k % 2 ? ["#ffe08a", "#ffd166", "#f4a100", "#c98000"] : ["#ffb3b3", "#ff6b6b", "#e04848", "#b83232"], k); }
        p.box(x, y, 8, 0.1, 0.1, 14, tone("#8a5a3c")); p.box(x + 1.5, y, 8, 0.1, 0.1, 14, tone("#8a5a3c"));
        p.box(x - 0.1, y - 0.1, 22, 1.8, 1.1, 2, { top: u => (Math.floor(u * 9) % 2 ? "#ffffff" : "#ef476f"), left: u => (Math.floor(u * 9) % 2 ? "#eeeeee" : "#d43d5f"), right: "#c23356" });
      }, x + y + 2.4, x, y);
      return;
    case "fence": {
      const along = prop.axis !== "y", rail = tone("#f6e7c8");
      add(bb(x, y, 0, along ? 1 : 0.1, along ? 0.1 : 1, 12), p => {
        p.box(x, y, 0, 0.12, 0.12, 9, rail);
        for (const z of [3, 7]) along ? p.box(x, y + 0.02, z, 1, 0.08, 1.5, rail) : p.box(x + 0.02, y, z, 0.08, 1, 1.5, rail);
      }, x + y + 1.1, x, y);
      return;
    }
    case "barrel": B(0.55, 0.55, 9, p => barrel(p, x, y, 0)); return;
    case "crate": B(0.6, 0.6, 8.4, p => crate(p, x, y, 0, 0.6)); return;
    case "sign": {
      const place = prop.place ? places.get(prop.place) : undefined;
      if (!place) return;
      add(bb(x, y, 0, 0.6, 0.6, 26), p => {
        p.box(x, y, 0, 0.12, 0.12, 20, tone("#8a5a3c"));
        const edge = (color: string): Fill => (u, v) => (u < 0.06 || u > 0.94 || v < 0.1 || v > 0.9 ? "#ffffff" : color);
        if (place.door.side === "x") p.para(p.P(x - 0.3, y + 0.06, 11), XV(0.72), ZV(8), edge(place.roof));
        else p.para(p.P(x + 0.06, y - 0.3, 11), YV(0.72), ZV(8), edge(lighten(place.roof, -18)));
      }, x + y + 0.3, x, y);
      return;
    }
  }
}
