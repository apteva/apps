// Building types. A place's category picks one; the type fixes the footprint,
// wall material, furniture and the work spots agents stand at. Spots always
// sit in front of their furniture so a working agent is never hidden.
import type { Category } from "../categories";
import { Painter, hash, lighten, tone, type Fill, type Tones } from "./raster";
import type { Spot } from "./stations";

export type WallKind = "stone" | "brick" | "timber" | "planks" | "logs";
export type FloorKind = "planks" | "tiles" | "flags";
export type ArchetypeId = "hall" | "office" | "archive" | "post" | "workshop" | "bank" | "studio" | "planner" | "records" | "market" | "cottage";
export type Archetype = { id: ArchetypeId; w: number; d: number; walls: WallKind[]; wallColors: string[]; floor: FloorKind; floorColor: string; rug?: [number, number, number, number] };

export const FZ = 2, WALL = 24, GABLE = 14, T = 0.25;

export const ARCHETYPES: Record<ArchetypeId, Archetype> = {
  hall: { id: "hall", w: 9, d: 6, walls: ["stone"], wallColors: ["#e3dccb", "#d9d4e4"], floor: "tiles", floorColor: "#eef1f8", rug: [2, 1.6, 4, 2.2] },
  office: { id: "office", w: 8, d: 6, walls: ["timber", "brick"], wallColors: ["#fff1d6", "#ffe6e6"], floor: "planks", floorColor: "#f0c48d", rug: [2.3, 2.9, 2.8, 2.1] },
  archive: { id: "archive", w: 7, d: 6, walls: ["planks"], wallColors: ["#e0594a", "#d9734e"], floor: "planks", floorColor: "#e8b77c" },
  post: { id: "post", w: 7, d: 5, walls: ["planks", "timber"], wallColors: ["#7cc0ec", "#fff4dc"], floor: "planks", floorColor: "#f3d2a6" },
  workshop: { id: "workshop", w: 6, d: 5, walls: ["stone"], wallColors: ["#b4b8c2", "#c7bfb3"], floor: "flags", floorColor: "#d6d8de" },
  bank: { id: "bank", w: 6, d: 5, walls: ["brick"], wallColors: ["#dc7a52", "#c9694f"], floor: "flags", floorColor: "#efe6d6" },
  studio: { id: "studio", w: 6, d: 5, walls: ["logs", "planks"], wallColors: ["#c98f5b", "#f2a7c3"], floor: "planks", floorColor: "#f5d3b0", rug: [1.4, 1.4, 2.4, 2] },
  planner: { id: "planner", w: 7, d: 5, walls: ["timber"], wallColors: ["#f1e6ff", "#e6f4ff"], floor: "planks", floorColor: "#eec08a", rug: [1.1, 1.1, 2.4, 2.4] },
  records: { id: "records", w: 7, d: 5, walls: ["stone", "brick"], wallColors: ["#d8e6dc", "#e3dccb"], floor: "tiles", floorColor: "#f2f2ea", rug: [3.2, 2.2, 2.4, 1.8] },
  market: { id: "market", w: 5, d: 4, walls: ["planks"], wallColors: ["#f6e7c8", "#cde8c4"], floor: "planks", floorColor: "#e9c08f" },
  cottage: { id: "cottage", w: 5, d: 4, walls: ["timber", "logs"], wallColors: ["#fff1d6", "#c98f5b"], floor: "planks", floorColor: "#f0c48d", rug: [1, 0.9, 2, 1.8] },
};

const BY_CATEGORY: Record<Category, ArchetypeId> = {
  storage: "archive", people: "office", mail: "post", talk: "post", tasks: "hall", build: "workshop", money: "bank",
  media: "studio", web: "studio", time: "planner", data: "records", link: "market", other: "cottage",
};
// Roofs carry the category colour, so the same kind of app always reads the same.
export const ROOF: Record<Category, string> = {
  storage: "#2ec4b6", people: "#ef476f", mail: "#ff7b54", talk: "#ff9f1c", tasks: "#4361ee", build: "#5c6475", money: "#f4a100",
  media: "#f15bb5", web: "#3a86ff", time: "#9b5de5", data: "#06a77d", link: "#e76f51", other: "#8d99ae",
};
export const archetypeFor = (category: Category) => ARCHETYPES[BY_CATEGORY[category]];

// The parts of a place a building needs; layout supplies these.
export type Lot = { x: number; y: number; w: number; d: number; archetype: ArchetypeId; roof: string; seed: number };
export type Footprint = { x0: number; y0: number; x1: number; y1: number };
export type Piece = { foot: Footprint; corners: [number, number, number][]; draw: (p: Painter) => void };
// `used` marks back-wall stretches taken by decor or shelves, so windows go elsewhere.
export type Interior = { pieces: Piece[]; decorX: ((p: Painter) => void)[]; decorY: ((p: Painter) => void)[]; spots: Spot[]; used: { axis: "x" | "y"; a0: number; length: number }[] };

const BOOKS = ["#ef476f", "#4361ee", "#06d6a0", "#ffd166", "#9b5de5", "#ff7b54", "#4cc9f0"];
const LEAVES = ["#b8f27c", "#6fcf4a", "#3fa34d", "#2a7a3b"];
const WHITE: Tones = { top: "#ffffff", left: "#e6e2ea", right: "#ccc6d4" };
const WOOD = "#c98f5b", DESK = "#d9a066", DARK = "#8a5a3c", NAVY = "#3d405b";

type RowFill = (row: number, length: number) => Fill;
const books: RowFill = (row, length) => (u, v) => {
  const i = Math.floor((u * length * 18) / 2), top = 0.45 + hash(i, row) * 0.5;
  return v > top ? null : BOOKS[Math.floor(hash(i + 3, row) * BOOKS.length)];
};
const boxes: RowFill = (row, length) => (u, v) => {
  const s = u * length * 18;
  if (s % 5 < 1 || v > 0.85) return null;
  return ["#e8b46a", "#bde0fe", "#ffc8dd", "#caffbf"][Math.floor(hash(Math.floor(s / 5), row) * 4)];
};
const folders: RowFill = (row, length) => (u, v) => {
  const s = u * length * 18;
  if (s % 3 < 0.8) return v < 0.8 ? "#c9b182" : null;
  return v > 0.8 ? null : ["#f6e7c8", "#ffe6a7", "#d8f3dc"][Math.floor(hash(Math.floor(s / 3), row) * 3)];
};
const letters: RowFill = (row, length) => (u, v) => {
  const s = u * length * 18;
  if (s % 4 < 1) return null;
  return v < 0.6 && hash(Math.floor(s / 4), row) < 0.7 ? ["#ffffff", "#fff1c1", "#ffd6e0"][Math.floor(hash(row, Math.floor(s / 4)) * 3)] : null;
};
const framed = (fill: Fill): Fill => (u, v, x, y) => (u < 0.08 || u > 0.92 || v < 0.1 || v > 0.9 ? DARK : typeof fill === "string" ? fill : fill(u, v, x, y));

// Collects furniture pieces and wall decor in absolute tile coordinates.
class Kit {
  readonly out: Interior = { pieces: [], decorX: [], decorY: [], spots: [], used: [] };
  constructor(readonly lot: Lot) {}
  get X() { return this.lot.x; }
  get Y() { return this.lot.y; }

  piece(u: number, v: number, w: number, d: number, h: number, draw: (p: Painter) => void) {
    const x = this.X + u, y = this.Y + v;
    this.out.pieces.push({
      foot: { x0: x, y0: y, x1: x + w, y1: y + d },
      corners: [[x - 0.3, y - 0.3, FZ + h + 8], [x + w + 0.3, y - 0.3, FZ + h + 8], [x - 0.3, y + d + 0.3, FZ + h + 8], [x + w + 0.3, y + d + 0.3, 0], [x + w + 0.3, y - 0.3, 0], [x - 0.3, y + d + 0.3, 0]],
      draw,
    });
  }
  block(u: number, v: number, w: number, d: number, h: number, tones: Tones, extra?: (p: Painter, x: number, y: number) => void) {
    this.piece(u, v, w, d, h, p => { p.box(this.X + u, this.Y + v, FZ, w, d, h, tones); extra?.(p, this.X + u, this.Y + v); });
  }
  // Against the back wall that runs along x (front faces down-left) or along y.
  shelf(axis: "x" | "y", a0: number, length: number, h: number, fill: RowFill = books, rows = 3, color = WOOD) {
    const depth = 0.55, u = axis === "x" ? a0 : 0.3, v = axis === "x" ? 0.3 : a0;
    const w = axis === "x" ? length : depth, d = axis === "x" ? depth : length;
    this.out.used.push({ axis, a0, length });
    this.block(u, v, w, d, h, tone(color), (p, x, y) => {
      const rowH = (h - 3) / rows;
      for (let r = 0; r < rows; r++) {
        const z = FZ + 2 + r * rowH;
        if (axis === "x") { p.wall("x", x + 0.08, y + d, z, length - 0.16, rowH - 1.2, lighten(color, -45)); p.wall("x", x + 0.1, y + d, z, length - 0.2, rowH - 1.8, fill(r * 7 + a0, length - 0.2)); }
        else { p.wall("y", x + w, y + 0.08, z, length - 0.16, rowH - 1.2, lighten(color, -45)); p.wall("y", x + w, y + 0.1, z, length - 0.2, rowH - 1.8, fill(r * 7 + a0, length - 0.2)); }
      }
    });
  }
  table(u: number, v: number, w: number, d: number, h = 7, color = DESK, items?: (p: Painter, x: number, y: number) => void) {
    this.piece(u, v, w, d, h + 6, p => {
      const x = this.X + u, y = this.Y + v, leg = tone(lighten(color, -30));
      for (const [a, b] of [[0.05, 0.05], [w - 0.17, 0.05], [0.05, d - 0.17], [w - 0.17, d - 0.17]]) p.box(x + a, y + b, FZ, 0.12, 0.12, h - 2, leg);
      p.box(x, y, FZ + h - 2, w, d, 2, tone(color));
      items?.(p, x, y);
    });
  }
  chair(u: number, v: number, color: string, back: "x" | "y" = "x") {
    this.piece(u, v, 0.45, 0.45, 10, p => {
      const x = this.X + u, y = this.Y + v;
      p.box(x, y, FZ, 0.45, 0.45, 4, tone(color));
      if (back === "x") p.box(x, y, FZ + 4, 0.45, 0.08, 6, tone(color)); else p.box(x, y, FZ + 4, 0.08, 0.45, 6, tone(color));
    });
  }
  plant(u: number, v: number, size = 1, pot = "#e07a5f") {
    this.piece(u, v, 0.35, 0.35, 4 + 12 * size, p => {
      const x = this.X + u, y = this.Y + v;
      p.box(x, y, FZ, 0.35, 0.35, 4, tone(pot));
      const [sx, sy] = p.P(x + 0.175, y + 0.175, FZ + 4 + 5 * size);
      p.ball(sx, sy, Math.round(5 * size), LEAVES, x + y);
    });
  }
  sofa(u: number, v: number, color: string, axis: "x" | "y" = "x") {
    const w = axis === "x" ? 1.6 : 0.7, d = axis === "x" ? 0.7 : 1.6;
    this.piece(u, v, w, d, 10, p => {
      const x = this.X + u, y = this.Y + v, c = tone(color);
      p.box(x, y, FZ, w, d, 4, c);
      if (axis === "x") { p.box(x, y, FZ + 4, w, 0.22, 5, c); p.box(x, y + 0.2, FZ + 4, 0.2, d - 0.2, 2, c); p.box(x + w - 0.2, y + 0.2, FZ + 4, 0.2, d - 0.2, 2, c); p.box(x + 0.4, y + 0.25, FZ + 4, 0.35, 0.3, 2, WHITE); }
      else { p.box(x, y, FZ + 4, 0.22, d, 5, c); p.box(x + 0.2, y, FZ + 4, w - 0.2, 0.2, 2, c); p.box(x + 0.2, y + d - 0.2, FZ + 4, w - 0.2, 0.2, 2, c); p.box(x + 0.25, y + 0.4, FZ + 4, 0.3, 0.35, 2, WHITE); }
    });
  }
  crate(u: number, v: number, size = 0.6, z = 0) {
    this.piece(u, v, size, size, z + size * 14, p => crate(p, this.X + u, this.Y + v, FZ + z, size));
  }
  barrel(u: number, v: number) {
    this.piece(u, v, 0.55, 0.55, 9, p => barrel(p, this.X + u, this.Y + v, FZ));
  }
  sack(u: number, v: number, tie: string) {
    this.piece(u, v, 0.6, 0.6, 12, p => {
      const [sx, sy] = p.P(this.X + u + 0.3, this.Y + v + 0.3, FZ + 5);
      p.ball(sx, sy, 6, ["#f6e7c8", "#e3cfa4", "#c9b182", "#a8905f"], u);
      p.px(tie, sx - 2, sy - 7, 4, 2);
    });
  }
  onX(a0: number, length: number, z: number, h: number, fill: Fill) { this.out.used.push({ axis: "x", a0, length }); this.out.decorX.push(p => p.wall("x", this.X + a0, this.Y + T, FZ + z, length, h, fill)); }
  onY(a0: number, length: number, z: number, h: number, fill: Fill) { this.out.used.push({ axis: "y", a0, length }); this.out.decorY.push(p => p.wall("y", this.X + T, this.Y + a0, FZ + z, length, h, fill)); }
  spot(u: number, v: number, tags?: string[]) { this.out.spots.push({ x: this.X + u, y: this.Y + v, ...(tags ? { tags } : {}) }); }
}

export function crate(p: Painter, x: number, y: number, z: number, size: number) {
  p.box(x, y, z, size, size, size * 14, {
    top: "#e8b46a",
    left: (u, v) => (u < 0.1 || u > 0.9 || v < 0.12 || v > 0.88 || Math.abs(u - v) < 0.08 ? "#a8693a" : "#d39b55"),
    right: (u, v) => (u < 0.1 || u > 0.9 || v < 0.12 || v > 0.88 ? "#8d5730" : "#b9813f"),
  });
}
export function barrel(p: Painter, x: number, y: number, z: number) {
  const band = (v: number) => Math.abs(v - 0.25) < 0.08 || Math.abs(v - 0.75) < 0.08;
  p.box(x, y, z, 0.55, 0.55, 9, { top: WOOD, left: (_u, v) => (band(v) ? "#6d6875" : "#b0723f"), right: (_u, v) => (band(v) ? "#56525e" : "#91592f") });
}
const paper = (p: Painter, x: number, y: number, z: number, color = "#ffffff") => p.box(x, y, z, 0.28, 0.2, 1, { top: color, left: "#d8d2de", right: "#c4bccc" });
const lampOn = (p: Painter, x: number, y: number, z: number) => { p.box(x + 0.08, y + 0.08, z, 0.12, 0.12, 6, tone(NAVY)); p.box(x, y, z + 6, 0.3, 0.3, 3, { top: "#ffe08a", left: "#ffd166", right: "#f4a100" }); };
const coins = (p: Painter, x: number, y: number, z: number, n: number) => { for (let k = 0; k < n; k++) p.box(x + k * 0.5, y, z, 0.22, 0.22, 2 + (k % 3) * 2, tone("#ffd166")); };

// Station tags: nouns from tool names plus verb classes (see stations.ts).
const INBOX = ["conversation", "draft", "message", "messaging", "reply", "send", "inbox", "template", "sender", "whatsapp", "email"];
const CATALOG = ["contact", "list", "segment", "attribute", "audience", "lead", "customer", "member", "read"];
const PIPELINE = ["opportunity", "pipeline", "stage", "deal", "routing", "rule", "offer"];
const SHELVES = ["read", "file", "folder", "download", "document", "asset"];
const INTAKE = ["write", "upload", "save", "sign", "share"];

export function interiorFor(lot: Lot): Interior {
  const k = new Kit(lot), accent = lot.roof;
  switch (lot.archetype) {
    case "hall": {
      k.onX(3, 3, 7, 15, framed((u, v) => {
        const col = Math.floor(u * 3), lane = (u * 3) % 1;
        if (lane < 0.06) return "#2b2d42";
        if (v > 0.8) return ["#ffd166", "#4cc9f0", "#06d6a0"][col];
        const row = Math.floor(v * 5);
        return lane > 0.15 && lane < 0.85 && (v * 5) % 1 > 0.25 && row < [3, 2, 4][col] ? (col === 0 && row === 0 ? "#ef476f" : "#ffffff") : NAVY;
      }));
      for (const a of [1.6, 3.6]) k.onY(a, 0.6, 8, 14, (u, v) => (v > 0.85 ? "#ffd166" : v < 0.15 && Math.abs(u - 0.5) > 0.2 ? null : accent));
      k.shelf("y", 0.4, 1.0, 18);
      k.shelf("x", 6.8, 1.8, 18);
      k.table(1.6, 2.1, 5, 1, 8, DESK, (p, x, y) => { for (let i = 0; i < 6; i++) paper(p, x + 0.2 + i * 0.75, y + 0.3 + (i % 2) * 0.2, FZ + 8, i === 1 ? "#ffd6e0" : "#ffffff"); p.box(x + 4.5, y + 0.2, FZ + 8, 0.3, 0.3, 5, tone("#ffd166")); });
      for (let i = 0; i < 4; i++) { k.chair(1.9 + i * 1.2, 1.5, accent); k.chair(1.9 + i * 1.2, 3.25, accent); }
      k.block(7.4, 3.9, 0.6, 0.5, 11, tone("#a8693a"), (p, x, y) => p.box(x - 0.05, y - 0.05, FZ + 11, 0.7, 0.6, 2, tone(WOOD)));
      k.sofa(7.9, 1.7, accent, "y");
      k.plant(8.4, 5.3, 1.3); k.plant(0.5, 5.2, 0.9, accent); k.plant(6.3, 0.5, 1);
      for (let i = 0; i < 4; i++) k.spot(2.1 + i * 1.2, 4.1);
      k.spot(7.7, 4.9);
      break;
    }
    case "office": {
      // A CRM front office: inbox desk, card catalog, pipeline board.
      k.onX(0.5, 2.6, 6, 15, (u, v) => {
        const a = (u * 7) % 1, b = (v * 5) % 1;
        if (a < 0.1 || b < 0.12) return DARK;
        return hash(Math.floor(u * 7), Math.floor(v * 5)) < 0.55 && b < 0.7 ? ["#ffffff", "#fff1c1", "#ffd6e0"][Math.floor(hash(Math.floor(v * 5), Math.floor(u * 7)) * 3)] : "#5c3a24";
      });
      k.block(0.5, 0.35, 2.6, 0.6, 9, tone(WOOD), (p, x, y) => { for (let i = 0; i < 5; i++) paper(p, x + 0.2 + i * 0.45, y + 0.15 + (i % 2) * 0.15, FZ + 9 + (i % 2), ["#ffffff", "#fff1c1", "#ffd6e0"][i % 3]); });
      k.onX(4.2, 3.1, 6, 16, framed((u, v) => {
        const column = Math.floor(u * 4), lane = (u * 4) % 1;
        if (v > 0.82) return ["#4cc9f0", "#ffd166", "#f15bb5", "#06d6a0"][column];
        if (lane < 0.08 || lane > 0.92) return "#2b2d42";
        const row = Math.floor(v * 6);
        return lane > 0.16 && lane < 0.84 && (v * 6) % 1 > 0.25 && row < [4, 3, 2, 1][column] ? (column === 3 ? "#b9f5c9" : "#ffffff") : NAVY;
      }));
      k.block(0.3, 2.1, 0.75, 2.1, 15, tone("#b5793f"), (p, x, y) => {
        for (let r = 0; r < 3; r++) for (let c = 0; c < 3; c++) {
          p.wall("y", x + 0.75, y + 0.1 + c * 0.66, FZ + 1.5 + r * 4.4, 0.56, 3.6, (u, v) => (Math.abs(u - 0.5) < 0.12 && Math.abs(v - 0.55) < 0.18 ? "#ffd166" : v > 0.85 ? "#e3b27a" : "#c98f5b"));
        }
      });
      k.table(2.8, 3.3, 1.5, 0.8, 8, DESK, (p, x, y) => { p.box(x + 0.15, y + 0.15, FZ + 8, 0.5, 0.4, 2, tone(accent)); paper(p, x + 0.85, y + 0.3, FZ + 8); lampOn(p, x + 1.15, y + 0.05, FZ + 8); });
      k.chair(3.3, 4.3, accent);
      k.block(4.6, 3.5, 0.6, 0.6, 5, tone(DESK), (p, x, y) => p.box(x + 0.15, y + 0.1, FZ + 5, 0.3, 0.25, 1, WHITE));
      k.sofa(5.4, 3.4, accent, "y");
      k.plant(7.3, 0.5, 1.2); k.plant(0.4, 5.1, 1, accent); k.plant(3.6, 0.5, 0.9, "#4361ee");
      k.spot(1.2, 1.6, INBOX); k.spot(2.5, 1.6, INBOX); k.spot(3.6, 4.8, INBOX);
      k.spot(1.7, 2.6, CATALOG); k.spot(1.7, 3.8, CATALOG);
      k.spot(4.9, 1.5, PIPELINE); k.spot(6.3, 1.5, PIPELINE);
      break;
    }
    case "archive": {
      k.onX(5.2, 0.8, 16, 6, framed("#bde0fe"));
      k.shelf("x", 0.3, 2.6, 22, boxes, 3, DARK); k.shelf("x", 3.2, 1.6, 22, books, 3, DARK); k.shelf("x", 5.1, 1.6, 22, boxes, 3, DARK);
      k.shelf("y", 1.2, 2.4, 22, boxes, 3, DARK);
      k.block(2.95, 0.95, 0.1, 0.5, 24, tone(WOOD));
      k.table(1.8, 3.6, 1.4, 0.8, 7, DESK, (p, x, y) => { p.box(x + 0.2, y + 0.15, FZ + 7, 0.5, 0.4, 3, tone("#e8b46a")); paper(p, x + 0.9, y + 0.2, FZ + 7); });
      k.crate(5.6, 4.6); k.crate(6.2, 4.7, 0.5); k.crate(5.65, 4.65, 0.5, 9); k.barrel(6.1, 2.6);
      k.spot(1.6, 1.6, SHELVES); k.spot(4.0, 1.6, SHELVES); k.spot(2.5, 4.9, INTAKE); k.spot(5.8, 1.6, SHELVES);
      break;
    }
    case "post": {
      k.onX(2.3, 2.3, 5, 17, (u, v) => {
        const a = (u * 6) % 1, b = (v * 4) % 1;
        if (a < 0.12 || b < 0.14) return DARK;
        return hash(Math.floor(u * 6), Math.floor(v * 4)) < 0.6 && b < 0.7 ? ["#ffffff", "#fff1c1", "#ffd6e0"][Math.floor(hash(Math.floor(v * 4), Math.floor(u * 6)) * 3)] : "#5c3a24";
      });
      k.block(0.8, 1.4, 2.4, 0.7, 9, tone(WOOD), (p, x, y) => { for (let i = 0; i < 6; i++) paper(p, x + 0.15 + i * 0.36, y + 0.15 + (i % 2) * 0.2, FZ + 9 + (i % 3), ["#ffffff", "#fff1c1", "#ffd6e0"][i % 3]); });
      k.chair(1.8, 2.3, accent);
      k.sack(0.4, 3.0, accent); k.sack(0.9, 3.4, "#4361ee"); k.sack(5.9, 2.4, accent);
      k.shelf("x", 4.8, 1.8, 20, letters, 4);
      k.sofa(4.6, 3.9, "#4cc9f0");
      k.plant(3.8, 0.6, 1);
      k.spot(2.0, 2.9, ["send", "write", "reply", "draft", "message", "post"]); k.spot(1.1, 2.5, ["send", "write", "reply", "draft", "message", "post"]); k.spot(5.6, 1.5, ["read", "inbox", "thread", "channel", "history"]);
      break;
    }
    case "workshop": {
      for (let i = 0; i < 5; i++) k.onX(2.7 + i * 0.28, 0.08, 9 + (i % 2) * 2, 9 - (i % 2) * 2, "#6c757d");
      k.onX(2.6, 1.6, 18, 1, DARK);
      k.block(0.3, 0.3, 1.4, 1, 16, { top: "#6c6f7a", left: (u, v) => (u > 0.3 && u < 0.7 && v < 0.5 ? (v < 0.4 && u > 0.36 && u < 0.64 ? "#ff9f1c" : "#3d2b28") : "#8d909b"), right: "#6d707b" },
        (p, x, y) => p.box(x + 0.55, y + 0.3, FZ + 16, 0.4, 0.4, 14, tone("#7d808b")));
      k.block(2.4, 2.0, 0.5, 0.35, 5, tone(NAVY), (p, x, y) => p.box(x - 0.15, y - 0.05, FZ + 5, 0.8, 0.45, 3, tone("#565a6b")));
      k.table(2.6, 0.4, 1.8, 0.7, 8, "#a8693a", (p, x, y) => { p.box(x + 0.2, y + 0.15, FZ + 8, 0.5, 0.15, 2, tone("#adb5bd")); p.box(x + 1, y + 0.1, FZ + 8, 0.12, 0.4, 2, tone("#ef476f")); });
      k.block(4.6, 0.4, 0.9, 0.6, 12, tone("#5c6475"), (p, x, y) => p.wall("x", x + 0.15, y + 0.6, FZ + 4, 0.6, 5, (u, v) => ((Math.floor(u * 4) + Math.floor(v * 2)) % 2 ? "#06d6a0" : "#2b2d42")));
      k.barrel(0.4, 3.2); k.barrel(5.0, 4.1); k.barrel(5.3, 3.4); k.crate(1.6, 4.2);
      k.spot(1.0, 1.8, ["build", "deploy", "run", "release", "function", "job"]); k.spot(3.4, 1.5, ["write", "code", "file", "repo", "commit"]); k.spot(2.9, 2.8, ["build", "deploy", "run"]); k.spot(5.0, 1.5, ["read", "log", "status", "health", "metric"]);
      break;
    }
    case "bank": {
      k.onX(1.45, 0.65, 10, 9, framed((u, v) => (v > 0.6 ? "#06d6a0" : (u * 5) % 1 < 0.5 && v < u * 0.5 + 0.1 ? "#f4a100" : "#fff8e8")));
      k.block(0.3, 0.35, 1, 0.8, 13, tone("#6c757d"), (p, x, y) => { p.wall("x", x + 0.15, y + 0.8, FZ + 2, 0.7, 9, "#858d96"); p.wall("x", x + 0.55, y + 0.8, FZ + 6, 0.15, 2, "#ffd166"); });
      k.block(0.6, 2.6, 2.6, 0.6, 9, tone(accent), (p, x, y) => { coins(p, x + 0.3, y + 0.15, FZ + 9, 4); p.box(x + 2.3, y + 0.1, FZ + 9, 0.1, 0.1, 6, tone(NAVY)); p.box(x + 2.05, y + 0.05, FZ + 15, 0.6, 0.2, 1, tone(NAVY)); });
      k.shelf("x", 2.2, 1.5, 16, books, 2);
      k.table(4, 1, 1.2, 0.8, 7, DESK, (p, x, y) => { p.box(x + 0.15, y + 0.15, FZ + 7, 0.5, 0.4, 2, tone("#06d6a0")); paper(p, x + 0.75, y + 0.25, FZ + 7); });
      k.chair(4.2, 2, accent); k.sofa(3.6, 3.9, "#06d6a0");
      k.plant(5.4, 4.3, 1.1); k.plant(3.8, 0.5, 0.9, "#4361ee");
      k.spot(1.8, 3.6, ["payment", "charge", "invoice", "checkout", "order", "send", "write"]); k.spot(0.8, 1.6, ["balance", "refund", "payout", "account"]); k.spot(4.6, 2.6, ["read", "report", "customer", "subscription"]);
      break;
    }
    case "studio": {
      k.onX(0.5, 1, 9, 10, framed((_u, v) => (v > 0.5 ? accent : lighten(accent, 60))));
      k.onY(1.2, 1.2, 12, 10, framed(u => ((u * 3) % 1 < 0.1 ? "#ffffff" : ["#4cc9f0", "#ffd166", "#06d6a0"][Math.floor(u * 3)])));
      k.sofa(0.35, 1.6, accent, "y");
      k.piece(2.4, 0.6, 0.7, 0.4, 22, p => {
        const x = k.X + 2.4, y = k.Y + 0.6;
        p.box(x + 0.1, y + 0.25, FZ, 0.08, 0.08, 16, tone(DARK)); p.box(x + 0.5, y + 0.25, FZ, 0.08, 0.08, 16, tone(DARK));
        p.box(x, y + 0.2, FZ + 8, 0.7, 0.12, 12, { top: "#ffffff", left: (u, v) => (v > 0.55 ? "#4cc9f0" : u < 0.5 ? "#ffd166" : "#06d6a0"), right: "#d8d2de" });
      });
      k.block(2.8, 2.6, 0.5, 0.5, 10, tone(NAVY), (p, x, y) => p.box(x - 0.05, y - 0.05, FZ + 10, 0.4, 0.3, 3, tone(accent)));
      k.piece(3.6, 1.9, 0.5, 0.5, 20, p => {
        const x = k.X + 3.6, y = k.Y + 1.9;
        for (const [a, b] of [[0, 0], [0.4, 0], [0.2, 0.4]]) p.box(x + a, y + b, FZ, 0.06, 0.06, 13, tone(NAVY));
        p.box(x + 0.1, y + 0.1, FZ + 13, 0.35, 0.3, 4, tone("#2b2d42")); p.box(x + 0.15, y + 0.38, FZ + 14, 0.2, 0.05, 2, tone("#4cc9f0"));
      });
      k.shelf("x", 4, 1.6, 16);
      k.sofa(4, 3.9, "#ffd166");
      k.plant(5.3, 2.6, 1.2, accent);
      k.spot(2.8, 1.5); k.spot(3.0, 3.6); k.spot(1.6, 2.5);
      break;
    }
    case "planner": {
      k.onX(1.3, 1.8, 7, 14, framed((u, v) => {
        if (v > 0.8) return "#ef476f";
        const a = Math.floor(u * 5), b = Math.floor(v * 4);
        if ((u * 5) % 1 < 0.12 || (v * 4) % 1 < 0.14) return "#e2dcea";
        return (a + b * 5) % 7 === 2 ? accent : (a + b * 5) % 5 === 0 ? "#ffd166" : "#ffffff";
      }));
      k.onY(1.2, 0.7, 12, 10, (u, v) => {
        const d = Math.hypot(u - 0.5, (v - 0.5) * 0.9);
        if (d > 0.5) return null;
        if (d > 0.4) return DARK;
        if ((Math.abs(u - 0.5) < 0.05 && v > 0.5 && v < 0.82) || (Math.abs(v - 0.5) < 0.06 && u > 0.5 && u < 0.75)) return "#342e42";
        return "#fffaf0";
      });
      k.piece(1.6, 1.6, 1.4, 1.4, 10, p => {
        const x = k.X + 1.6, y = k.Y + 1.6;
        p.box(x + 0.55, y + 0.55, FZ, 0.3, 0.3, 6, tone("#a8693a"));
        const [sx, sy] = p.P(x + 0.7, y + 0.7, FZ + 7);
        for (let dy = -6; dy <= 6; dy++) for (let dx = -13; dx <= 13; dx++) { const e = (dx / 13) ** 2 + (dy / 6.5) ** 2; if (e < 1) p.px(e > 0.75 && dy > 0 ? "#b5793f" : e > 0.8 ? WOOD : "#e3b27a", sx + dx, sy + dy); }
        p.px("#ffffff", sx - 5, sy - 2, 4, 2); p.px(accent, sx + 2, sy, 4, 2);
      });
      k.chair(1.3, 2.1, accent, "y"); k.chair(3, 2.1, accent, "y"); k.chair(2.1, 1.2, accent);
      k.block(4, 0.35, 0.55, 0.45, 24, tone(DARK), (p, x, y) => { p.wall("x", x + 0.1, y + 0.45, FZ + 16, 0.35, 6, (u, v) => (Math.hypot(u - 0.5, v - 0.5) < 0.45 ? "#fffaf0" : null)); p.wall("x", x + 0.25, y + 0.45, FZ + 5, 0.05, 8, "#ffd166"); });
      k.shelf("x", 5, 1.6, 16);
      k.sofa(4.8, 3.9, accent);
      k.block(6.3, 2.6, 0.5, 0.5, 6, tone(DESK), (p, x, y) => p.box(x + 0.1, y + 0.1, FZ + 6, 0.25, 0.25, 6, tone("#ffd166")));
      k.plant(0.5, 4.2, 1.1);
      k.spot(2.4, 3.6); k.spot(4.3, 1.5); k.spot(5.8, 1.5);
      break;
    }
    case "records": {
      k.onX(1.2, 2.6, 7, 14, framed((u, v) => {
        const bar = Math.floor(u * 6), inBar = (u * 6) % 1;
        if (inBar < 0.25 || inBar > 0.8) return "#ffffff";
        return v < [0.3, 0.5, 0.42, 0.65, 0.58, 0.8][bar] ? ["#06a77d", "#4cc9f0", "#ffd166"][bar % 3] : "#ffffff";
      }));
      k.shelf("y", 0.5, 3.6, 20, folders, 4, "#9aa5b1");
      k.table(1.4, 1.6, 2, 0.8, 8, DESK, (p, x, y) => { p.box(x + 0.2, y + 0.15, FZ + 8, 0.5, 0.4, 3, tone("#06a77d")); paper(p, x + 0.9, y + 0.3, FZ + 8); lampOn(p, x + 1.5, y + 0.1, FZ + 8); });
      k.chair(2.1, 2.6, accent);
      k.piece(5.6, 0.8, 0.6, 0.6, 18, p => {
        const x = k.X + 5.6, y = k.Y + 0.8;
        p.box(x + 0.2, y + 0.2, FZ, 0.2, 0.2, 8, tone(DARK));
        const [sx, sy] = p.P(x + 0.3, y + 0.3, FZ + 14);
        p.ball(sx, sy, 6, ["#bde0fe", "#4cc9f0", "#3a86ff", "#2a5fa8"], 2);
        p.px("#06d6a0", sx - 3, sy - 2, 3, 2); p.px("#06d6a0", sx + 1, sy + 1, 2, 2);
      });
      k.shelf("x", 4, 1.4, 18, folders, 3, "#9aa5b1");
      k.sofa(4.7, 3.9, accent);
      k.plant(6.4, 2.6, 1.1);
      k.spot(2.6, 3.4); k.spot(5.9, 2.0); k.spot(4.6, 1.5);
      break;
    }
    case "market": {
      k.onX(0, 5, 17, 5, (u, v) => (v < 0.35 ? "#ffffff" : Math.floor(u * 14) % 2 ? "#ffffff" : accent));
      k.block(0.8, 1.4, 2.6, 0.7, 8, tone(WOOD), (p, x, y) => {
        for (let i = 0; i < 5; i++) { const [sx, sy] = p.P(x + 0.3 + i * 0.5, y + 0.35, FZ + 10); p.ball(sx, sy, 2, i % 2 ? ["#ffe08a", "#ffd166", "#f4a100", "#c98000"] : ["#ffb3b3", "#ff6b6b", "#e04848", "#b83232"], i); }
      });
      k.crate(0.4, 0.4); k.crate(0.45, 0.45, 0.5, 9); k.barrel(4, 0.5); k.barrel(3.4, 0.4); k.crate(4, 2.6, 0.55);
      k.spot(1.8, 2.6); k.spot(3.0, 2.6);
      break;
    }
    case "cottage": {
      k.onY(1.1, 0.6, 12, 8, framed("#bde0fe"));
      k.table(1.2, 1.2, 1.4, 0.9, 7, DESK, (p, x, y) => { p.box(x + 0.3, y + 0.3, FZ + 7, 0.2, 0.2, 3, tone("#ffffff")); paper(p, x + 0.8, y + 0.4, FZ + 7); });
      k.chair(1.6, 0.6, accent); k.chair(1.6, 2.2, accent);
      k.shelf("x", 3.1, 1.5, 16);
      k.plant(4.4, 3.4, 1.1); k.plant(0.4, 3.3, 0.9, accent);
      k.spot(2.0, 2.9); k.spot(3.8, 1.5);
      break;
    }
  }
  return k.out;
}

// Wall textures, sampled per pixel on a wall face of the given length (tiles) and height (px).
export function wallTexture(kind: WallKind, base: string, length: number, h: number): Fill {
  const dark = lighten(base, -28), light = lighten(base, 14);
  return (u, v) => {
    const s = u * length * 18, z = v * h;
    if (kind === "brick") {
      const row = Math.floor(z / 3), off = (row % 2) * 4;
      if (z % 3 < 1 || (s + off) % 8 < 1) return lighten(base, 34);
      return hash(Math.floor((s + off) / 8), row) < 0.22 ? lighten(base, -14) : base;
    }
    if (kind === "stone") {
      const row = Math.floor(z / 5), w = 6 + Math.floor(hash(row, 1) * 5), off = hash(row, 2) * w, col = Math.floor((s + off) / w);
      if (z % 5 < 1 || (s + off) % w < 1) return dark;
      const k = hash(col, row);
      return k < 0.3 ? light : k > 0.8 ? lighten(base, -10) : base;
    }
    if (kind === "planks") {
      if (s % 5 < 1) return dark;
      if (z < 3) return lighten(base, -18);
      return hash(Math.floor(s / 5), 3) < 0.3 ? lighten(base, -7) : base;
    }
    if (kind === "logs") { const t = z % 4; return t < 1 ? dark : t > 3 ? light : base; }
    const m = s % 26;
    if (z < 2 || Math.abs(z - h * 0.5) < 1 || m < 2 || (z < h * 0.5 && Math.abs(m - 2 - z * 1.2) < 1)) return "#7a4b2e";
    return base;
  };
}

export function floorTexture(kind: FloorKind, base: string, w: number, d: number): Fill {
  const dark = lighten(base, -16), light = lighten(base, 10);
  return (u, v) => {
    const a = u * w * 16, b = v * d * 16;
    if (kind === "planks") { const row = Math.floor(b / 4); if (b % 4 < 1) return dark; if ((a + row * 11) % 22 < 1 && row % 3 === 0) return dark; return hash(row, 5) < 0.3 ? light : base; }
    if (kind === "tiles") { if (a % 8 < 1 || b % 8 < 1) return dark; return (Math.floor(a / 8) + Math.floor(b / 8)) % 2 ? base : light; }
    const row = Math.floor(b / 6), off = hash(row, 9) * 8, col = Math.floor((a + off) / 9);
    if (b % 6 < 1 || (a + off) % 9 < 1) return dark;
    return hash(col, row) < 0.35 ? light : base;
  };
}
