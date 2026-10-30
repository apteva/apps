// Village: a cosy farming-town look. Warm greens, timber and brick, roofs
// coloured by what each app does, lit windows and lamps after dusk.
import { TILE, disc, makeCanvas, px, rect, rng, shadow, stamp, text, textWidth, type Ctx } from "../core";
import { ICONS, type Category } from "../categories";
import { GROUND, groundOf, inPond, type Layout, type Place, type Pond, type Prop } from "../layout";
import { PLACE_SPRITE, placeOrigin, type Glow, type PixelTheme, type PlaceState } from "../theme";

const C = {
  outline: "#2b2233",
  grass: ["#5f9e3c", "#548f35", "#6fb24a", "#4a8030", "#67a843"],
  wild: ["#4f8a33", "#457a2d", "#5a963a"],
  dirt: ["#c9a26b", "#b48a56", "#dcbb86", "#a07849", "#9d7a4e"],
  stone: ["#b8b1a2", "#8f897b", "#cdc6b6", "#a29b8c"],
  water: ["#3f8fd0", "#3479bd", "#5ea9e3", "#cfe9f7", "#e3d29a"],
  soil: ["#8a5a36", "#6e4529", "#a0704a"],
  wood: ["#c08a55", "#a87443", "#d9a46b", "#7d5330"],
  trunk: ["#7a4f2e", "#5a3a22", "#946139"],
};

type Roof = [string, string, string]; // light, mid, dark
const ROOFS: Record<string, Roof> = {
  red: ["#e0735a", "#c2523d", "#8e3529"], blue: ["#6f9ad6", "#4a76b8", "#2f5288"],
  green: ["#79b56a", "#559447", "#386b31"], teal: ["#5cbcb0", "#3a998f", "#246e67"],
  purple: ["#a684c6", "#8360a8", "#5c4078"], orange: ["#eba45a", "#d07e33", "#99561d"],
  slate: ["#8a91a3", "#666d80", "#454b5a"], brown: ["#b38251", "#8f6237", "#663f20"],
  gold: ["#efc864", "#d4a43a", "#9c7520"], rose: ["#e48aa2", "#c86584", "#93445e"],
};
type Material = "wood" | "brick" | "stone" | "plaster" | "barn";
const STYLE: Record<Category, { roof: keyof typeof ROOFS; wall: Material; ink: string }> = {
  storage: { roof: "brown", wall: "barn", ink: "#8e3529" },
  people: { roof: "teal", wall: "plaster", ink: "#246e67" },
  mail: { roof: "blue", wall: "brick", ink: "#2f5288" },
  tasks: { roof: "green", wall: "wood", ink: "#386b31" },
  build: { roof: "slate", wall: "stone", ink: "#454b5a" },
  money: { roof: "gold", wall: "plaster", ink: "#9c7520" },
  media: { roof: "purple", wall: "plaster", ink: "#5c4078" },
  data: { roof: "slate", wall: "brick", ink: "#2f5288" },
  talk: { roof: "orange", wall: "wood", ink: "#99561d" },
  web: { roof: "teal", wall: "stone", ink: "#246e67" },
  time: { roof: "red", wall: "stone", ink: "#8e3529" },
  link: { roof: "rose", wall: "wood", ink: "#93445e" },
  other: { roof: "brown", wall: "wood", ink: "#663f20" },
};

const noise = (x: number, y: number, salt = 0) => {
  let n = Math.imul(x * 374761393 + y * 668265263 + salt * 2246822519, 1274126177);
  n ^= n >>> 15;
  return ((n >>> 0) % 10000) / 10000;
};
const smooth = (x: number, y: number) => {
  const xi = Math.floor(x), yi = Math.floor(y), fx = x - xi, fy = y - yi;
  const a = noise(xi, yi, 9), b = noise(xi + 1, yi, 9), c = noise(xi, yi + 1, 9), d = noise(xi + 1, yi + 1, 9);
  return a * (1 - fx) * (1 - fy) + b * fx * (1 - fy) + c * (1 - fx) * fy + d * fx * fy;
};

function paintGrass(ctx: Ctx, x0: number, y0: number, tx: number, ty: number, palette: string[], lush = true) {
  const patch = smooth(tx / 5, ty / 5);
  rect(ctx, x0, y0, TILE, TILE, patch > 0.62 && palette[4] ? palette[4] : palette[0]);
  const random = rng(tx * 7919 + ty * 104729 + 3);
  for (let i = 0; i < 9; i++) px(ctx, x0 + Math.floor(random() * 16), y0 + Math.floor(random() * 16), palette[1]);
  for (let i = 0; i < 5; i++) px(ctx, x0 + Math.floor(random() * 16), y0 + Math.floor(random() * 16), palette[2]);
  if (!lush) return;
  for (let i = 0; i < 3; i++) {
    const bx = x0 + 1 + Math.floor(random() * 14), by = y0 + 2 + Math.floor(random() * 12);
    px(ctx, bx, by, palette[3]); px(ctx, bx, by - 1, palette[3]); px(ctx, bx + 1, by - 2, palette[2]);
  }
  if (random() < 0.07) {
    const petals = [["#f4f1e6", "#f2cf4a"], ["#e88fb0", "#fbe3a0"], ["#f2cf4a", "#c7782e"], ["#9fb8f0", "#fbf6ea"]][Math.floor(random() * 4)];
    for (let i = 0; i < 3; i++) {
      const fx = x0 + 2 + Math.floor(random() * 11), fy = y0 + 2 + Math.floor(random() * 11);
      px(ctx, fx - 1, fy, petals[0]); px(ctx, fx + 1, fy, petals[0]); px(ctx, fx, fy - 1, petals[0]); px(ctx, fx, fy + 1, petals[0]); px(ctx, fx, fy, petals[1]);
    }
  }
}

function paintGround(ctx: Ctx, layout: Layout<any>) {
  for (let ty = 0; ty < layout.rows; ty++) for (let tx = 0; tx < layout.cols; tx++) {
    const g = groundOf(layout, tx, ty), x0 = tx * TILE, y0 = ty * TILE;
    const random = rng(tx * 31337 + ty * 7331 + 11);
    if (g === GROUND.grass) paintGrass(ctx, x0, y0, tx, ty, C.grass);
    else if (g === GROUND.wild) paintGrass(ctx, x0, y0, tx, ty, C.wild, false);
    else if (g === GROUND.path) {
      rect(ctx, x0, y0, TILE, TILE, C.dirt[0]);
      for (let i = 0; i < 10; i++) px(ctx, x0 + Math.floor(random() * 16), y0 + Math.floor(random() * 16), random() < 0.5 ? C.dirt[1] : C.dirt[2]);
      for (let i = 0; i < 2; i++) { const sx = x0 + 2 + Math.floor(random() * 12), sy = y0 + 2 + Math.floor(random() * 12); rect(ctx, sx, sy, 2, 1, C.dirt[4]); px(ctx, sx, sy - 1, C.dirt[2]); }
    } else if (g === GROUND.plaza) {
      rect(ctx, x0, y0, TILE, TILE, C.stone[1]);
      for (let sy = 0; sy < 2; sy++) for (let sx = 0; sx < 2; sx++) {
        const shift = (ty + sy) % 2 ? 4 : 0;
        const bx = x0 + ((sx * 8 + shift) % 16), by = y0 + sy * 8;
        const tone = random() < 0.3 ? C.stone[3] : C.stone[0];
        rect(ctx, bx + 1, by + 1, 6, 6, tone);
        rect(ctx, bx + 1, by + 1, 6, 1, C.stone[2]);
        px(ctx, bx + 1, by + 2, C.stone[2]);
        if (bx + 8 > x0 + 16) rect(ctx, x0 + 1, by + 1, bx + 7 - x0 - 16, 6, tone);
      }
    } else if (g === GROUND.water) {
      paintGrass(ctx, x0, y0, tx, ty, C.grass, false);
    } else if (g === GROUND.soil) {
      rect(ctx, x0, y0, TILE, TILE, C.soil[0]);
      for (let y = 2; y < TILE; y += 4) { rect(ctx, x0, y0 + y, TILE, 1, C.soil[1]); rect(ctx, x0, y0 + y - 1, TILE, 1, C.soil[2]); }
    }
  }
  // Soft edges: grass tufts over dirt and stone, sand rims along the water.
  for (let ty = 0; ty < layout.rows; ty++) for (let tx = 0; tx < layout.cols; tx++) {
    const g = groundOf(layout, tx, ty), x0 = tx * TILE, y0 = ty * TILE;
    const random = rng(tx * 4241 + ty * 911 + 5);
    const isGreen = (gx: number, gy: number) => { const n = groundOf(layout, gx, gy); return n === GROUND.grass || n === GROUND.wild; };
    if (g === GROUND.path || g === GROUND.plaza || g === GROUND.soil) {
      const rim = g === GROUND.path ? C.dirt[3] : g === GROUND.plaza ? C.stone[1] : C.soil[1];
      const sides: [number, number, (i: number) => [number, number], (i: number, d: number) => [number, number]][] = [
        [0, -1, i => [x0 + i, y0], (i, d) => [x0 + i, y0 + d]],
        [0, 1, i => [x0 + i, y0 + 15], (i, d) => [x0 + i, y0 + 15 - d]],
        [-1, 0, i => [x0, y0 + i], (i, d) => [x0 + d, y0 + i]],
        [1, 0, i => [x0 + 15, y0 + i], (i, d) => [x0 + 15 - d, y0 + i]],
      ];
      for (const [dx, dy, , inward] of sides) {
        if (!isGreen(tx + dx, ty + dy)) continue;
        for (let i = 0; i < 16; i++) {
          const depth = random() < 0.35 ? 2 : random() < 0.7 ? 1 : 0;
          for (let d = 0; d < depth; d++) { const [px0, py0] = inward(i, d); px(ctx, px0, py0, d ? C.grass[2] : C.grass[0]); }
          const [rx, ry] = inward(i, depth);
          px(ctx, rx, ry, rim);
        }
      }
    }
  }
  for (const pond of layout.ponds) paintPond(ctx, pond);
}

// Ponds are painted per pixel from their shape: muddy bank, sand, foam, water.
function paintPond(ctx: Ctx, pond: Pond) {
  const random = rng(Math.floor(pond.seed * 1000));
  for (let y = Math.floor(pond.cy - pond.ry - 6); y <= pond.cy + pond.ry + 6; y++) {
    for (let x = Math.floor(pond.cx - pond.rx - 6); x <= pond.cx + pond.rx + 6; x++) {
      if (!inPond(pond, x, y, -2)) continue;
      if (!inPond(pond, x, y, 0)) px(ctx, x, y, "#7d6a40");
      else if (!inPond(pond, x, y, 1)) px(ctx, x, y, C.water[4]);
      else if (!inPond(pond, x, y, 2)) px(ctx, x, y, C.water[3]);
      else px(ctx, x, y, inPond(pond, x, y, 7) ? C.water[1] : C.water[0]);
    }
  }
  for (let i = 0; i < 4; i++) {
    const x = pond.cx + (random() - 0.5) * pond.rx * 1.2, y = pond.cy + (random() - 0.5) * pond.ry * 1.1;
    disc(ctx, x, y, 3, "#4f9a46", 0.8); px(ctx, x, y - 2, C.water[0]); if (random() < 0.6) px(ctx, x + 1, y - 1, "#f2a8c2");
  }
  // Reeds along the far bank.
  for (let i = 0; i < 7; i++) {
    const angle = Math.PI * (1.1 + random() * 0.8), x = Math.round(pond.cx + Math.cos(angle) * pond.rx), y = Math.round(pond.cy + Math.sin(angle) * pond.ry);
    rect(ctx, x, y - 6, 1, 6, "#3a7f43"); rect(ctx, x + 2, y - 4, 1, 4, "#4f9a46"); px(ctx, x, y - 7, "#7a4f2e");
  }
}

function paintGrassPixel(ctx: Ctx, x: number, y: number, tx: number, ty: number) {
  const n = noise(x, y, 2);
  px(ctx, x, y, n < 0.1 ? C.grass[1] : n > 0.94 ? C.grass[2] : smooth(tx / 5, ty / 5) > 0.62 ? C.grass[4] : C.grass[0]);
}

function animateGround(ctx: Ctx, layout: Layout<any>, now: number) {
  for (const pond of layout.ponds) {
    for (let i = 0; i < 9; i++) {
      const seed = noise(i, Math.floor(pond.seed * 100), 4);
      const phase = (now / 900 + seed * 10) % 4;
      if (phase > 2.6) continue;
      const x = Math.round(pond.cx + (seed - 0.5) * pond.rx * 1.4 + ((now / 260 + seed * 50) % 6) - 3), y = Math.round(pond.cy + (noise(i, 3, 7) - 0.5) * pond.ry * 1.3);
      if (!inPond(pond, x - 1, y, 4) || !inPond(pond, x + 3, y, 4)) continue;
      rect(ctx, x, y, 3, 1, C.water[2]);
      if (phase < 1.2) px(ctx, x + 1, y - 1, C.water[3]);
    }
  }
}

// ---- Props -----------------------------------------------------------------

const treeCache = new Map<string, HTMLCanvasElement>();
const TREE_PALETTES = [
  ["#24432a", "#2f6b34", "#3f8a41", "#5aa94f", "#7cc46a"],
  ["#24432a", "#2c6437", "#3a7f43", "#4f9a4c", "#6db35d"],
  ["#5a2e3e", "#c9658a", "#e58cab", "#f4b6c9", "#fde0e8"],
  ["#5a3417", "#b5612a", "#d98a3a", "#efb35a", "#f7d68a"],
];

function treeSprite(variant: number) {
  const key = String(variant);
  const hit = treeCache.get(key);
  if (hit) return hit;
  const [canvas, ctx] = makeCanvas(34, 46);
  const random = rng(variant * 977 + 1);
  const pine = variant === 1 || variant === 5 || variant === 7;
  const palette = TREE_PALETTES[variant === 2 ? 2 : variant === 3 ? 3 : variant % 2];
  const cx = 17;
  rect(ctx, cx - 3, 30, 6, 14, C.outline);
  rect(ctx, cx - 2, 30, 4, 14, C.trunk[0]);
  rect(ctx, cx + 1, 30, 1, 14, C.trunk[1]);
  px(ctx, cx - 2, 35, C.trunk[2]); px(ctx, cx - 1, 39, C.trunk[2]);
  rect(ctx, cx - 4, 43, 8, 2, C.outline); rect(ctx, cx - 3, 43, 6, 1, C.trunk[1]);
  if (pine) {
    const tiers = [[34, 13], [26, 11], [18, 8], [11, 5]];
    for (const [y, half] of tiers) {
      for (let row = 0; row < 9; row++) {
        const w = Math.round(half * (row / 9)) + 1;
        rect(ctx, cx - w - 1, y - 9 + row, w * 2 + 2, 1, palette[0]);
      }
      for (let row = 1; row < 8; row++) {
        const w = Math.round(half * (row / 9));
        rect(ctx, cx - w, y - 9 + row, w * 2 + 1, 1, palette[2]);
        rect(ctx, cx - w, y - 9 + row, Math.max(1, Math.round(w * 0.6)), 1, palette[3]);
        rect(ctx, cx + Math.round(w * 0.4), y - 9 + row, Math.max(1, Math.round(w * 0.6)), 1, palette[1]);
      }
    }
    px(ctx, cx, 1, palette[4]);
  } else {
    const blobs: [number, number, number][] = [[cx, 20, 13], [cx - 7, 23, 8], [cx + 7, 23, 8], [cx - 4, 13, 9], [cx + 5, 14, 8]];
    for (const [x, y, r] of blobs) disc(ctx, x, y, r + 1, palette[0]);
    for (const [x, y, r] of blobs) disc(ctx, x, y, r, palette[1]);
    for (const [x, y, r] of blobs) disc(ctx, x - 1, y - 2, r - 2, palette[2]);
    disc(ctx, cx - 4, 12, 5, palette[3]); disc(ctx, cx + 4, 14, 4, palette[3]); disc(ctx, cx - 8, 20, 3, palette[3]);
    disc(ctx, cx - 6, 10, 2, palette[4]); disc(ctx, cx + 2, 10, 1, palette[4]);
    for (let i = 0; i < 26; i++) {
      const a = random() * Math.PI * 2, r = random() * 11;
      px(ctx, cx + Math.cos(a) * r, 20 + Math.sin(a) * r * 0.8, random() < 0.6 ? palette[1] : palette[3]);
    }
    if (variant === 2) for (let i = 0; i < 8; i++) px(ctx, cx - 10 + random() * 20, 10 + random() * 18, "#fff3f6");
  }
  treeCache.set(key, canvas);
  return canvas;
}

function drawProp(ctx: Ctx, prop: Prop, now: number, night: number) {
  const x = Math.round(prop.x), y = Math.round(prop.y);
  switch (prop.kind) {
    case "tree": {
      shadow(ctx, x, y - 1, 11);
      // Gentle sway on the canopy only; trunks stay planted.
      const sway = Math.round(Math.sin(now / 1400 + prop.x * 0.07) * 0.6);
      const sprite = treeSprite(prop.variant);
      ctx.drawImage(sprite, 0, 30, 34, 16, x - 17, y - 16, 34, 16);
      ctx.drawImage(sprite, 0, 0, 34, 30, x - 17 + sway, y - 46, 34, 30);
      break;
    }
    case "bush": {
      shadow(ctx, x, y - 1, 7);
      const greens = prop.variant % 2 ? ["#24432a", "#3a7f43", "#5aa94f"] : ["#24432a", "#2f6b34", "#4f9a4c"];
      disc(ctx, x, y - 6, 7, greens[0], 0.75); disc(ctx, x, y - 6, 6, greens[1], 0.72); disc(ctx, x - 2, y - 8, 3, greens[2], 0.8);
      if (prop.variant === 2) { px(ctx, x + 2, y - 7, "#d0412f"); px(ctx, x - 3, y - 5, "#d0412f"); px(ctx, x + 4, y - 4, "#d0412f"); }
      if (prop.variant === 3) { px(ctx, x + 1, y - 8, "#fbf6ea"); px(ctx, x - 3, y - 5, "#fbf6ea"); }
      break;
    }
    case "rock":
      shadow(ctx, x, y - 1, 6);
      disc(ctx, x, y - 4, 6, C.outline, 0.65); disc(ctx, x, y - 4, 5, "#9b968c", 0.6); disc(ctx, x - 1, y - 5, 3, "#c2bdb2", 0.6); px(ctx, x + 2, y - 3, "#7d786f");
      break;
    case "lamp": {
      shadow(ctx, x, y, 4);
      rect(ctx, x - 2, y - 2, 4, 2, C.outline);
      rect(ctx, x - 1, y - 20, 2, 18, "#3a3440");
      rect(ctx, x - 3, y - 27, 7, 7, C.outline);
      rect(ctx, x - 2, y - 26, 5, 5, night > 0.15 ? "#ffd36b" : "#efe6c8");
      px(ctx, x - 1, y - 25, night > 0.15 ? "#fff3c4" : "#ffffff");
      rect(ctx, x - 3, y - 28, 7, 1, "#3a3440");
      break;
    }
    case "bench": {
      shadow(ctx, x, y, 9);
      rect(ctx, x - 9, y - 9, 18, 3, C.outline); rect(ctx, x - 8, y - 9, 16, 2, C.wood[0]);
      rect(ctx, x - 9, y - 5, 18, 3, C.outline); rect(ctx, x - 8, y - 5, 16, 2, C.wood[2]);
      rect(ctx, x - 8, y - 2, 2, 2, C.wood[3]); rect(ctx, x + 6, y - 2, 2, 2, C.wood[3]);
      break;
    }
    case "fountain": {
      shadow(ctx, x, y - 2, 15, 0.22);
      disc(ctx, x, y - 10, 15, C.outline, 0.55); disc(ctx, x, y - 10, 14, "#a9a294", 0.55); disc(ctx, x, y - 11, 12, "#cdc6b6", 0.5);
      disc(ctx, x, y - 11, 10, C.water[1], 0.5); disc(ctx, x, y - 12, 8, C.water[0], 0.5);
      rect(ctx, x - 2, y - 24, 4, 13, "#cdc6b6"); rect(ctx, x + 1, y - 24, 1, 13, "#a9a294"); rect(ctx, x - 3, y - 25, 6, 2, C.outline);
      for (let i = 0; i < 6; i++) {
        const t = ((now / 700 + i / 6) % 1), side = i % 2 ? 1 : -1;
        px(ctx, x + side * Math.round(2 + t * 6), y - 26 + Math.round(t * t * 14), i % 3 ? C.water[3] : C.water[2]);
      }
      break;
    }
    case "crate":
      shadow(ctx, x, y, 6);
      if (prop.variant) {
        rect(ctx, x - 5, y - 12, 10, 12, C.outline); rect(ctx, x - 4, y - 11, 8, 10, C.wood[1]); rect(ctx, x - 4, y - 9, 8, 1, "#5b5560"); rect(ctx, x - 4, y - 4, 8, 1, "#5b5560"); rect(ctx, x - 3, y - 11, 2, 10, C.wood[2]);
      } else {
        rect(ctx, x - 6, y - 11, 12, 11, C.outline); rect(ctx, x - 5, y - 10, 10, 9, C.wood[0]);
        for (let i = 0; i < 9; i++) { px(ctx, x - 5 + i, y - 10 + i, C.wood[3]); px(ctx, x + 4 - i, y - 10 + i, C.wood[3]); }
        rect(ctx, x - 5, y - 10, 10, 1, C.wood[2]);
      }
      break;
    case "crops": {
      const sway = Math.sin(now / 900 + prop.x) > 0.6 ? 1 : 0;
      rect(ctx, x - 1, y - 6, 2, 6, "#3a7f43");
      rect(ctx, x - 4 + sway, y - 8, 3, 2, "#5aa94f"); rect(ctx, x + 1 + sway, y - 9, 3, 2, "#4f9a4c");
      if (prop.variant === 1) disc(ctx, x, y - 2, 3, "#e0802f", 0.8);
      if (prop.variant === 2) { px(ctx, x - 2, y - 5, "#d0412f"); px(ctx, x + 2, y - 6, "#d0412f"); }
      break;
    }
    case "fence":
      rect(ctx, x - 8, y - 9, 16, 2, C.outline); rect(ctx, x - 8, y - 9, 16, 1, C.wood[2]);
      rect(ctx, x - 8, y - 5, 16, 2, C.outline); rect(ctx, x - 8, y - 5, 16, 1, C.wood[0]);
      rect(ctx, x - 7, y - 11, 3, 11, C.outline); rect(ctx, x - 6, y - 11, 1, 10, C.wood[2]);
      break;
    case "sign": {
      const label = (prop.label || "").slice(0, 26);
      const w = Math.max(22, textWidth(label) + 8);
      shadow(ctx, x, y, w / 2);
      rect(ctx, x - w / 2 + 3, y - 6, 2, 6, C.wood[3]); rect(ctx, x + w / 2 - 5, y - 6, 2, 6, C.wood[3]);
      rect(ctx, x - w / 2, y - 17, w, 11, C.outline); rect(ctx, x - w / 2 + 1, y - 16, w - 2, 9, C.wood[0]); rect(ctx, x - w / 2 + 1, y - 16, w - 2, 1, C.wood[2]);
      text(ctx, label, x - w / 2 + 4, y - 14, "#3a2416");
      break;
    }
  }
}

// ---- Buildings -------------------------------------------------------------

const buildingCache = new Map<string, HTMLCanvasElement>();
const W = PLACE_SPRITE.w, H = PLACE_SPRITE.h;
const WALL_TOP = H - 30, ROOF_TOP = 2;

function wallFill(ctx: Ctx, material: Material, x: number, y: number, w: number, h: number, seed: number) {
  const random = rng(seed);
  const palettes: Record<Material, [string, string, string]> = {
    wood: ["#c08a55", "#a87443", "#d9a46b"], barn: ["#b5513a", "#93402d", "#cb6a50"],
    brick: ["#b5583f", "#e2cfb0", "#c86d50"], stone: ["#b3afa5", "#8f8b82", "#cbc7bd"], plaster: ["#efe2c4", "#d4c4a0", "#fbf3df"],
  };
  const [base, dark, light] = palettes[material];
  rect(ctx, x, y, w, h, base);
  if (material === "wood" || material === "barn") {
    for (let row = y + 3; row < y + h; row += 4) rect(ctx, x, row, w, 1, dark);
    for (let i = 0; i < 6; i++) px(ctx, x + Math.floor(random() * w), y + Math.floor(random() * h), dark);
    for (let row = y; row < y + h; row += 4) rect(ctx, x, row, w, 1, light);
  } else if (material === "brick") {
    for (let row = 0; row < h; row += 4) {
      rect(ctx, x, y + row + 3, w, 1, dark);
      for (let col = (row / 4) % 2 ? 0 : 4; col < w; col += 8) rect(ctx, x + col, y + row, 1, 3, dark);
      for (let col = 1; col < w; col += 8 + Math.floor(random() * 3)) px(ctx, x + col, y + row, light);
    }
  } else if (material === "stone") {
    for (let row = 0; row < h; row += 6) {
      const offset = (row / 6) % 2 ? 5 : 0;
      for (let col = -offset; col < w; col += 10) {
        const sx = Math.max(x, x + col + 1), ex = Math.min(x + w, x + col + 10);
        if (ex <= sx) continue;
        rect(ctx, sx, y + row + 1, ex - sx - 1, 4, random() < 0.3 ? light : base);
        rect(ctx, sx, y + row + 1, ex - sx - 1, 1, light);
        rect(ctx, sx, y + row + 5, ex - sx, 1, dark);
      }
    }
  } else {
    for (let i = 0; i < 18; i++) px(ctx, x + Math.floor(random() * w), y + Math.floor(random() * h), random() < 0.5 ? dark : light);
    const beam = "#6b4428";
    rect(ctx, x, y, 2, h, beam); rect(ctx, x + w - 2, y, 2, h, beam);
    rect(ctx, x, y + Math.floor(h / 2) - 1, w, 2, beam);
    for (const bx of [x + Math.floor(w / 3), x + Math.floor((2 * w) / 3)]) rect(ctx, bx, y, 2, h, beam);
  }
}

function windowAt(ctx: Ctx, x: number, y: number, lit: boolean, shutters: string) {
  rect(ctx, x - 2, y, 2, 10, shutters); rect(ctx, x + 12, y, 2, 10, shutters);
  rect(ctx, x, y, 12, 10, "#4a2f1d");
  rect(ctx, x + 1, y + 1, 10, 8, lit ? "#ffd36b" : "#8fc6e3");
  rect(ctx, x + 1, y + 1, 10, 1, lit ? "#fff0b8" : "#c9e8f6");
  px(ctx, x + 2, y + 2, lit ? "#fff7d6" : "#e8f6fc"); px(ctx, x + 3, y + 3, lit ? "#fff0b8" : "#c9e8f6");
  rect(ctx, x + 6, y + 1, 1, 8, "#4a2f1d"); rect(ctx, x + 1, y + 5, 10, 1, "#4a2f1d");
  rect(ctx, x - 1, y + 10, 14, 2, "#7d5330");
}

function closedSprite(category: Category, kind: string, lit: boolean, doorOpen: boolean) {
  const key = `c|${category}|${kind}|${lit}|${doorOpen}`;
  const hit = buildingCache.get(key);
  if (hit) return hit;
  const [canvas, ctx] = makeCanvas(W, H);
  const style = STYLE[category];
  const roof = ROOFS[style.roof];
  const seed = category.length * 131 + kind.length;
  if (kind === "integration") drawStall(ctx, roof, style.ink, category, lit);
  else {
    // Walls and foundation.
    rect(ctx, 3, WALL_TOP - 1, W - 6, 31, C.outline);
    wallFill(ctx, style.wall, 4, WALL_TOP, W - 8, 26, seed);
    rect(ctx, 4, H - 4, W - 8, 3, "#8e8a80"); rect(ctx, 4, H - 4, W - 8, 1, "#b3afa5");
    rect(ctx, 4, WALL_TOP, W - 8, 2, "rgba(30,20,30,0.35)");
    const shutters = ROOFS[style.roof][2];
    windowAt(ctx, 12, WALL_TOP + 6, lit, shutters);
    windowAt(ctx, W - 24, WALL_TOP + 6, lit, shutters);
    // Door with frame and step.
    const dx = W / 2 - 6;
    rect(ctx, dx - 1, WALL_TOP + 8, 14, 19, C.outline);
    if (doorOpen) {
      rect(ctx, dx, WALL_TOP + 9, 12, 18, "#2b2233");
      rect(ctx, dx, WALL_TOP + 9, 12, 18, lit ? "#5a4130" : "#3a2c30");
      rect(ctx, dx + 1, WALL_TOP + 20, 10, 7, lit ? "#7a5536" : "#4a3836");
    } else {
      rect(ctx, dx, WALL_TOP + 9, 12, 18, style.wall === "barn" ? "#f0e6d2" : "#7a4a2a");
      if (style.wall === "barn") {
        for (let i = 0; i < 12; i++) { px(ctx, dx + i, WALL_TOP + 9 + Math.round(i * 1.5), "#93402d"); px(ctx, dx + 11 - i, WALL_TOP + 9 + Math.round(i * 1.5), "#93402d"); }
      } else {
        rect(ctx, dx + 3, WALL_TOP + 9, 1, 18, "#5f3820"); rect(ctx, dx + 8, WALL_TOP + 9, 1, 18, "#5f3820");
        rect(ctx, dx, WALL_TOP + 9, 12, 1, "#9a6238");
        px(ctx, dx + 9, WALL_TOP + 18, "#e7c35a");
      }
    }
    rect(ctx, dx - 2, H - 2, 16, 2, "#a9a294");
    // Roof: a trapezoid of offset shingle rows with ridge and eave.
    const roofH = WALL_TOP - ROOF_TOP + 3;
    for (let row = 0; row < roofH; row++) {
      const inset = Math.round((1 - row / roofH) * 9);
      const y = ROOF_TOP + row;
      rect(ctx, inset, y, W - inset * 2, 1, C.outline);
      const tone = row < 3 ? roof[0] : (row % 5 === 4 ? roof[2] : roof[1]);
      rect(ctx, inset + 1, y, W - inset * 2 - 2, 1, tone);
      if (row % 5 !== 4 && row > 2) {
        const shift = Math.floor(row / 5) % 2 ? 3 : 0;
        for (let col = inset + 2 + shift; col < W - inset - 2; col += 6) px(ctx, col, y, roof[2]);
      }
      if (row % 5 === 3) for (let col = inset + 2; col < W - inset - 2; col += 6) px(ctx, col + 1, y, roof[0]);
    }
    rect(ctx, 9, ROOF_TOP, W - 18, 1, C.outline);
    rect(ctx, 0, WALL_TOP + 1, W, 2, C.outline);
    rect(ctx, 1, WALL_TOP + 1, W - 2, 1, roof[2]);
    // Chimney.
    const chx = W - 22;
    rect(ctx, chx - 1, ROOF_TOP - 2, 10, 12, C.outline);
    rect(ctx, chx, ROOF_TOP - 1, 8, 11, "#a8553f"); rect(ctx, chx, ROOF_TOP + 3, 8, 1, "#7f3b2b"); rect(ctx, chx, ROOF_TOP + 7, 8, 1, "#7f3b2b");
    rect(ctx, chx - 1, ROOF_TOP - 3, 10, 2, "#5b5560");
    // Hanging sign with the category icon.
    const sx = W / 2 - 6, sy = WALL_TOP - 4;
    rect(ctx, sx - 1, sy - 1, 14, 11, C.outline); rect(ctx, sx, sy, 12, 9, "#f3e6c4"); rect(ctx, sx, sy, 12, 1, "#fff7e0");
    stamp(ctx, ICONS[category], { x: style.ink }, sx + 2.5, sy + 1);
  }
  buildingCache.set(key, canvas);
  return canvas;
}

// Integrations are market stalls: an awning, a counter and goods on display.
function drawStall(ctx: Ctx, roof: Roof, ink: string, category: Category, lit: boolean) {
  const top = WALL_TOP - 8;
  rect(ctx, 8, top + 8, 4, H - top - 9, C.outline); rect(ctx, W - 12, top + 8, 4, H - top - 9, C.outline);
  rect(ctx, 9, top + 8, 2, H - top - 9, C.wood[1]); rect(ctx, W - 11, top + 8, 2, H - top - 9, C.wood[1]);
  rect(ctx, 6, H - 16, W - 12, 15, C.outline);
  rect(ctx, 7, H - 15, W - 14, 13, C.wood[0]);
  for (let row = H - 12; row < H - 2; row += 4) rect(ctx, 7, row, W - 14, 1, C.wood[1]);
  rect(ctx, 5, H - 17, W - 10, 3, C.wood[3]); rect(ctx, 5, H - 17, W - 10, 1, C.wood[2]);
  const goods = ["#d0412f", "#f2cf4a", "#4fa35a", "#e0802f", "#9fb8f0"];
  for (let i = 0; i < 7; i++) disc(ctx, 14 + i * 8, H - 20, 3, goods[(i + category.length) % goods.length], 0.9);
  // Striped awning with scalloped edge.
  for (let row = 0; row < 16; row++) {
    const inset = Math.round((1 - row / 16) * 6);
    rect(ctx, inset, top - 8 + row, W - inset * 2, 1, C.outline);
    for (let col = inset + 1; col < W - inset - 1; col++) px(ctx, col, top - 8 + row, Math.floor((col - 1) / 8) % 2 ? "#fbf6ea" : (row < 2 ? roof[0] : roof[1]));
  }
  for (let col = 0; col < W; col += 8) { disc(ctx, col + 4, top + 8, 3, C.outline, 0.7); disc(ctx, col + 4, top + 8, 2, Math.floor(col / 8) % 2 ? "#fbf6ea" : roof[1], 0.7); }
  const sx = W / 2 - 6, sy = top - 18;
  rect(ctx, sx - 1, sy - 1, 14, 11, C.outline); rect(ctx, sx, sy, 12, 9, lit ? "#fff3c4" : "#f3e6c4");
  stamp(ctx, ICONS[category], { x: ink }, sx + 2.5, sy + 1);
  rect(ctx, sx + 5, sy + 10, 2, 4, C.wood[3]);
}

const FURNITURE: Record<Category, (ctx: Ctx, floorY: number, ink: string) => void> = {
  storage: (ctx, y) => { for (let i = 0; i < 4; i++) { rect(ctx, 10 + i * 15, y + 2, 11, 9, C.outline); rect(ctx, 11 + i * 15, y + 3, 9, 7, C.wood[i % 2 ? 0 : 1]); rect(ctx, 11 + i * 15, y + 3, 9, 1, C.wood[2]); } },
  people: (ctx, y) => { rect(ctx, 12, y + 4, 22, 9, C.outline); rect(ctx, 13, y + 5, 20, 7, "#3a998f"); rect(ctx, 13, y + 5, 20, 2, "#5cbcb0"); disc(ctx, 60, y + 8, 5, "#3f8a41"); rect(ctx, 58, y + 12, 5, 4, "#a8553f"); },
  mail: (ctx, y) => { rect(ctx, 10, y + 1, 30, 12, C.outline); for (let r = 0; r < 2; r++) for (let c = 0; c < 4; c++) { rect(ctx, 11 + c * 7, y + 2 + r * 5, 6, 4, "#d9a46b"); px(ctx, 13 + c * 7, y + 3 + r * 5, "#fbf6ea"); } rect(ctx, 48, y + 7, 22, 6, C.wood[1]); rect(ctx, 52, y + 5, 6, 3, "#fbf6ea"); },
  tasks: (ctx, y) => { rect(ctx, 14, y, 26, 12, C.outline); rect(ctx, 15, y + 1, 24, 10, "#c9a26b"); for (const [nx, ny, c] of [[16, 2, "#f2cf4a"], [23, 3, "#9fd3a0"], [30, 2, "#e88fb0"], [18, 7, "#9fb8f0"], [27, 7, "#f2cf4a"]] as const) rect(ctx, nx, y + ny, 5, 4, c); rect(ctx, 48, y + 8, 22, 6, C.wood[1]); },
  build: (ctx, y, ink) => { rect(ctx, 10, y + 6, 30, 8, C.outline); rect(ctx, 11, y + 7, 28, 6, C.wood[1]); rect(ctx, 16, y, 12, 8, C.outline); rect(ctx, 17, y + 1, 10, 6, "#2f3a4a"); rect(ctx, 18, y + 2, 6, 1, "#5ee08a"); rect(ctx, 18, y + 4, 4, 1, "#5ee08a"); stamp(ctx, ICONS.build, { x: ink }, 52, y + 3); },
  money: (ctx, y) => { rect(ctx, 10, y + 6, 40, 8, C.outline); rect(ctx, 11, y + 7, 38, 6, C.wood[0]); rect(ctx, 30, y + 2, 8, 6, "#5b5560"); disc(ctx, 60, y + 8, 4, "#d4a43a"); disc(ctx, 66, y + 10, 3, "#efc864"); },
  media: (ctx, y) => { rect(ctx, 14, y - 2, 18, 15, C.outline); rect(ctx, 15, y - 1, 16, 12, "#fbf6ea"); disc(ctx, 21, y + 5, 3, "#8360a8"); rect(ctx, 24, y + 3, 5, 5, "#d07e33"); rect(ctx, 22, y + 13, 2, 3, C.wood[3]); rect(ctx, 48, y + 8, 22, 6, C.wood[1]); },
  data: (ctx, y) => { rect(ctx, 10, y + 6, 30, 8, C.outline); rect(ctx, 11, y + 7, 28, 6, C.wood[1]); rect(ctx, 16, y, 14, 8, C.outline); rect(ctx, 17, y + 1, 12, 6, "#2f3a4a"); for (let i = 0; i < 4; i++) rect(ctx, 18 + i * 3, y + 6 - i, 2, 1 + i, "#7cc4f0"); rect(ctx, 50, y + 1, 18, 13, C.outline); rect(ctx, 51, y + 2, 16, 11, "#a87443"); },
  talk: (ctx, y) => { rect(ctx, 14, y + 4, 40, 9, C.outline); rect(ctx, 15, y + 5, 38, 7, C.wood[0]); for (const cx of [20, 34, 48]) { disc(ctx, cx, y + 2, 3, "#d07e33"); } },
  web: (ctx, y) => { rect(ctx, 10, y + 6, 30, 8, C.outline); rect(ctx, 11, y + 7, 28, 6, C.wood[1]); rect(ctx, 15, y, 14, 8, C.outline); rect(ctx, 16, y + 1, 12, 6, "#2f3a4a"); stamp(ctx, ICONS.web, { x: "#7cc4f0" }, 18.5, y + 0.5); disc(ctx, 60, y + 8, 5, "#3f8a41"); },
  time: (ctx, y) => { disc(ctx, 24, y + 4, 7, C.outline); disc(ctx, 24, y + 4, 6, "#fbf6ea"); rect(ctx, 24, y, 1, 4, "#2b2233"); rect(ctx, 24, y + 4, 3, 1, "#2b2233"); rect(ctx, 44, y + 8, 26, 6, C.wood[1]); },
  link: (ctx, y) => { rect(ctx, 14, y + 6, 46, 8, C.wood[0]); },
  other: (ctx, y) => { rect(ctx, 14, y + 6, 30, 8, C.outline); rect(ctx, 15, y + 7, 28, 6, C.wood[1]); disc(ctx, 60, y + 8, 5, "#3f8a41"); },
};

function openSprite(category: Category, kind: string, lit: boolean) {
  const key = `o|${category}|${kind}|${lit}`;
  const hit = buildingCache.get(key);
  if (hit) return hit;
  const [canvas, ctx] = makeCanvas(W, H);
  const style = STYLE[category];
  const backTop = 14, floorTop = 26;
  rect(ctx, 3, backTop - 1, W - 6, H - backTop + 1, C.outline);
  // Back wall with windows, floor, side walls and a low cut front wall.
  wallFill(ctx, style.wall, 4, backTop, W - 8, floorTop - backTop, 77);
  windowAt(ctx, 14, backTop + 1, lit, ROOFS[style.roof][2]);
  windowAt(ctx, W - 26, backTop + 1, lit, ROOFS[style.roof][2]);
  const floor = style.wall === "stone" || style.wall === "brick" ? ["#cdbfa3", "#b3a588"] : ["#c79a64", "#a77c4c"];
  rect(ctx, 7, floorTop, W - 14, H - floorTop - 5, floor[0]);
  for (let y = floorTop + 3; y < H - 5; y += 4) rect(ctx, 7, y, W - 14, 1, floor[1]);
  for (let y = floorTop; y < H - 5; y += 4) for (let x = 7 + ((y / 4) % 2) * 9; x < W - 7; x += 18) px(ctx, x, y + 1, floor[1]);
  rect(ctx, 4, backTop, 3, H - backTop - 1, ROOFS[style.roof][2]);
  rect(ctx, W - 7, backTop, 3, H - backTop - 1, ROOFS[style.roof][2]);
  rect(ctx, 4, floorTop, W - 8, 2, "rgba(30,20,30,0.25)");
  rect(ctx, 22, floorTop + 16, 36, 12, "#b5513a"); rect(ctx, 23, floorTop + 17, 34, 10, "#cb6a50"); rect(ctx, 25, floorTop + 19, 30, 6, "#e0a93b");
  FURNITURE[category](ctx, floorTop + 2, style.ink);
  rect(ctx, 4, H - 5, W / 2 - 12, 4, C.outline); rect(ctx, W / 2 + 8, H - 5, W / 2 - 12, 4, C.outline);
  rect(ctx, 5, H - 4, W / 2 - 14, 2, "#8e8a80"); rect(ctx, W / 2 + 9, H - 4, W / 2 - 14, 2, "#8e8a80");
  buildingCache.set(key, canvas);
  return canvas;
}

function drawPlace(ctx: Ctx, place: Place, state: PlaceState) {
  const { x, y } = placeOrigin(place);
  const lit = state.night > 0.2 || state.active;
  shadow(ctx, x + W / 2, y - 1, W / 2 + 2, 0.22);
  const top = y - H;
  if (state.open < 1) {
    ctx.globalAlpha = 1 - state.open;
    ctx.drawImage(closedSprite(place.category, place.kind, lit, state.active), x, top);
  }
  if (state.open > 0 && place.kind !== "integration") {
    ctx.globalAlpha = state.open;
    ctx.drawImage(openSprite(place.category, place.kind, lit), x, top);
  } else if (state.open > 0) {
    ctx.globalAlpha = 1;
    ctx.drawImage(closedSprite(place.category, place.kind, lit, true), x, top);
  }
  ctx.globalAlpha = 1;
  if (state.error) {
    rect(ctx, x + W - 12, top - 4, 1, 12, C.outline);
    rect(ctx, x + W - 11, top - 4, 7, 5, "#d0412f"); px(ctx, x + W - 8, top - 3, "#fbf6ea"); px(ctx, x + W - 8, top - 1, "#fbf6ea");
  }
  if (state.selected) {
    const pulse = Math.floor(state.now / 300) % 2;
    const c = "#ffe27a";
    for (const [cx, cy, sx, sy] of [[x - 2, top - 2, 1, 1], [x + W + 1, top - 2, -1, 1], [x - 2, y + 1, 1, -1], [x + W + 1, y + 1, -1, -1]]) {
      rect(ctx, cx - (sx < 0 ? 4 + pulse : 0) + (sx > 0 ? -pulse : 0), cy, 5, 1, c);
      rect(ctx, cx, cy - (sy < 0 ? 4 + pulse : 0) + (sy > 0 ? -pulse : 0), 1, 5, c);
    }
  }
}

function placeEffects(ctx: Ctx, place: Place, state: PlaceState) {
  if (!state.active || place.kind === "integration" || state.open > 0.5) return;
  const { x, y } = placeOrigin(place);
  const cx = x + W - 18, cy = y - H + ROOF_TOP - 4;
  for (let i = 0; i < 4; i++) {
    const t = (state.now / 2200 + i / 4) % 1;
    const r = 1 + Math.round(t * 3);
    ctx.globalAlpha = 0.75 * (1 - t);
    disc(ctx, cx + Math.round(Math.sin(t * 5 + i) * 2 + t * 6), cy - Math.round(t * 22), r, i % 2 ? "#e9e4ea" : "#cfc8d4");
  }
  ctx.globalAlpha = 1;
}

function ambient(now: number) {
  const date = new Date(now);
  const hour = date.getHours() + date.getMinutes() / 60;
  let darkness = 0;
  if (hour >= 17 && hour < 20.5) darkness = ((hour - 17) / 3.5) * 0.58;
  else if (hour >= 20.5 || hour < 5) darkness = 0.58;
  else if (hour >= 5 && hour < 7) darkness = (1 - (hour - 5) / 2) * 0.58;
  const warmth = hour >= 16 && hour < 19.5 ? Math.max(0, 1 - Math.abs(hour - 18) / 1.6) * 0.18 : 0;
  return { darkness, tint: "#141a44", warmth };
}

function glows(layout: Layout<any>, places: Map<string, PlaceState>, night: number): Glow[] {
  const out: Glow[] = [];
  if (night < 0.12) return out;
  for (const prop of layout.props) if (prop.kind === "lamp") out.push({ x: prop.x, y: prop.y - 24, radius: 26, color: "#ffcf70", strength: night });
  for (const place of layout.places) {
    const { x, y } = placeOrigin(place);
    const state = places.get(place.id);
    const strength = night * (state?.active ? 1 : 0.7);
    if (place.kind === "integration") out.push({ x: x + W / 2, y: y - 22, radius: 22, color: "#ffcf70", strength });
    else for (const wx of [x + 18, x + W - 18]) out.push({ x: wx, y: y - 22, radius: 14, color: "#ffcf70", strength });
  }
  return out;
}

export const village: PixelTheme = {
  id: "village",
  backdrop: "#2f5a2a",
  outline: C.outline,
  label: { bg: "#3a2a22", rim: "#e7c88f", text: "#fbf1d8", dim: "#c9b48a" },
  paintGround,
  animateGround,
  drawProp,
  drawPlace,
  placeEffects,
  ambient,
  glows,
};
