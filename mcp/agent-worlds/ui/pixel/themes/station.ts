// Space station: the same map as the village, painted as a station deck.
// Paths are corridors, the plaza is the hub, apps are modules, the forest
// is open space beyond the hull. Proves themes swap looks, not behaviour.
import { TILE, disc, makeCanvas, px, rect, rng, shadow, stamp, text, textWidth, type Ctx } from "../core";
import { ICONS, type Category } from "../categories";
import { GROUND, groundOf, inPond, type Layout, type Place, type Pond, type Prop } from "../layout";
import { PLACE_SPRITE, placeOrigin, type Glow, type PixelTheme, type PlaceState } from "../theme";

const S = {
  outline: "#141826",
  floor: ["#3b4352", "#2f3644", "#4a5366", "#687389"],
  corridor: ["#2c333f", "#252b36", "#3a4250"],
  hub: ["#4a5468", "#3c4558", "#5c6880"],
  hull: ["#5b6478", "#2a3040", "#7a849a"],
  space: ["#0b0f1e", "#141a30", "#f4f1e6", "#9fb8f0"],
  hazard: ["#d9b33b", "#1d1f27"],
  cyan: ["#5ee6ff", "#2fb7d4", "#c6f6ff"],
  metal: ["#6b7488", "#4c5466", "#8a94a8", "#a9b2c4"],
};
const ACCENT: Record<Category, [string, string]> = {
  storage: ["#c98a3a", "#94631f"], people: ["#3fb6a8", "#278479"], mail: ["#4f86d9", "#335fa3"],
  tasks: ["#5fb760", "#3d8640"], build: ["#9aa3b8", "#6b7488"], money: ["#e0b84a", "#a9862a"],
  media: ["#a77ad6", "#7a52a8"], data: ["#4fb3e0", "#2f84ab"], talk: ["#e48a4a", "#ad5f28"],
  web: ["#43c9b8", "#2a968a"], time: ["#e06a5a", "#a8473a"], link: ["#e07fa8", "#a8577a"], other: ["#8a94a8", "#5b6478"],
};

const noise = (x: number, y: number, salt = 0) => {
  let n = Math.imul(x * 374761393 + y * 668265263 + salt * 2246822519, 1274126177);
  n ^= n >>> 15;
  return ((n >>> 0) % 10000) / 10000;
};

function plateTile(ctx: Ctx, x0: number, y0: number, colors: string[], tx: number, ty: number) {
  rect(ctx, x0, y0, TILE, TILE, colors[0]);
  rect(ctx, x0, y0, TILE, 1, colors[2]);
  rect(ctx, x0, y0, 1, TILE, colors[2]);
  rect(ctx, x0, y0 + 15, TILE, 1, colors[1]);
  rect(ctx, x0 + 15, y0, 1, TILE, colors[1]);
  for (const [rx, ry] of [[2, 2], [13, 2], [2, 13], [13, 13]]) px(ctx, x0 + rx, y0 + ry, S.floor[3]);
  if (noise(tx, ty, 3) < 0.18) { rect(ctx, x0 + 4, y0 + 6, 8, 4, colors[1]); for (let i = 0; i < 4; i++) rect(ctx, x0 + 5 + i * 2, y0 + 7, 1, 2, colors[2]); }
}

function paintGround(ctx: Ctx, layout: Layout<any>) {
  for (let ty = 0; ty < layout.rows; ty++) for (let tx = 0; tx < layout.cols; tx++) {
    const g = groundOf(layout, tx, ty), x0 = tx * TILE, y0 = ty * TILE;
    if (g === GROUND.wild) {
      rect(ctx, x0, y0, TILE, TILE, S.space[0]);
      if (noise(tx, ty, 5) < 0.35) px(ctx, x0 + Math.floor(noise(tx, ty, 6) * 16), y0 + Math.floor(noise(tx, ty, 7) * 16), noise(tx, ty, 8) < 0.5 ? S.space[1] : S.space[3]);
      if (noise(tx, ty, 9) < 0.08) px(ctx, x0 + 8, y0 + 8, S.space[2]);
    } else if (g === GROUND.path) {
      rect(ctx, x0, y0, TILE, TILE, S.corridor[0]);
      for (let y = 1; y < TILE; y += 3) rect(ctx, x0, y0 + y, TILE, 1, S.corridor[1]);
      rect(ctx, x0 + 7, y0 + 7, 2, 2, "#245a66");
    } else if (g === GROUND.plaza) plateTile(ctx, x0, y0, S.hub, tx, ty);
    else if (g === GROUND.soil) {
      rect(ctx, x0, y0, TILE, TILE, "#1e2a24");
      for (let y = 3; y < TILE; y += 5) { rect(ctx, x0 + 1, y0 + y, 14, 2, "#2c4a34"); rect(ctx, x0 + 1, y0 + y, 14, 1, "#3d6a46"); }
    } else plateTile(ctx, x0, y0, S.floor, tx, ty);
  }
  // Hazard edging on corridors and a thick hull rim where the deck meets space.
  for (let ty = 0; ty < layout.rows; ty++) for (let tx = 0; tx < layout.cols; tx++) {
    const g = groundOf(layout, tx, ty), x0 = tx * TILE, y0 = ty * TILE;
    const sides: [number, number][] = [[0, -1], [0, 1], [-1, 0], [1, 0]];
    if (g === GROUND.path) {
      for (const [dx, dy] of sides) {
        const n = groundOf(layout, tx + dx, ty + dy);
        if (n === GROUND.path || n === GROUND.plaza || n === GROUND.wild) continue;
        for (let i = 0; i < 16; i++) {
          const color = Math.floor((i + tx * 16 + ty * 16) / 3) % 2 ? S.hazard[0] : S.hazard[1];
          if (dx) px(ctx, dx < 0 ? x0 : x0 + 15, y0 + i, color); else px(ctx, x0 + i, dy < 0 ? y0 : y0 + 15, color);
        }
      }
    }
    if (g !== GROUND.wild) {
      for (const [dx, dy] of sides) {
        if (groundOf(layout, tx + dx, ty + dy) !== GROUND.wild) continue;
        for (let d = 0; d < 4; d++) {
          const color = d === 0 ? S.hull[1] : d === 1 ? S.hull[2] : S.hull[0];
          if (dx) rect(ctx, dx < 0 ? x0 + d : x0 + 15 - d, y0, 1, TILE, color); else rect(ctx, x0, dy < 0 ? y0 + d : y0 + 15 - d, TILE, 1, color);
        }
      }
    }
  }
  for (const pond of layout.ponds) paintPool(ctx, pond);
}

// The pond becomes a glowing coolant pool behind a metal rim.
function paintPool(ctx: Ctx, pond: Pond) {
  for (let y = Math.floor(pond.cy - pond.ry - 6); y <= pond.cy + pond.ry + 6; y++) {
    for (let x = Math.floor(pond.cx - pond.rx - 6); x <= pond.cx + pond.rx + 6; x++) {
      if (!inPond(pond, x, y, -2)) continue;
      if (!inPond(pond, x, y, 0)) px(ctx, x, y, S.outline);
      else if (!inPond(pond, x, y, 2)) px(ctx, x, y, S.metal[2]);
      else px(ctx, x, y, (x + y) % 8 === 0 || (x - y) % 8 === 0 ? "#1b8fa3" : inPond(pond, x, y, 7) ? "#15a3ba" : "#1fb5c8");
    }
  }
}

function animateGround(ctx: Ctx, layout: Layout<any>, now: number) {
  // Distant stars twinkle; pools ripple.
  for (let i = 0; i < 40; i++) {
    const tx = Math.floor(noise(i, 1, 11) * layout.cols), ty = Math.floor(noise(i, 2, 11) * layout.rows);
    if (groundOf(layout, tx, ty) !== GROUND.wild) continue;
    if ((now / 600 + noise(i, 3, 11) * 7) % 3 < 1.2) px(ctx, tx * TILE + 4 + (i % 8), ty * TILE + 3 + (i % 9), S.space[2]);
  }
  for (const pond of layout.ponds) {
    const t = (now / 1600) % 1;
    for (let a = 0; a < 40; a++) {
      const angle = (a / 40) * Math.PI * 2;
      const x = Math.round(pond.cx + Math.cos(angle) * pond.rx * t * 0.8), y = Math.round(pond.cy + Math.sin(angle) * pond.ry * t * 0.8);
      if (inPond(pond, x, y, 3)) { ctx.globalAlpha = 1 - t; px(ctx, x, y, S.cyan[2]); }
    }
    ctx.globalAlpha = 1;
  }
}

const spriteCache = new Map<string, HTMLCanvasElement>();
function cached(key: string, w: number, h: number, paint: (ctx: Ctx) => void) {
  let canvas = spriteCache.get(key);
  if (!canvas) {
    const [created, ctx] = makeCanvas(w, h);
    paint(ctx);
    spriteCache.set(key, (canvas = created));
  }
  return canvas;
}

function drawProp(ctx: Ctx, prop: Prop, now: number) {
  const x = Math.round(prop.x), y = Math.round(prop.y);
  switch (prop.kind) {
    case "tree": {
      if (prop.variant >= 4) {
        // Beyond the hull: the odd drifting rock or satellite.
        if (prop.variant === 6 && noise(x, y, 1) < 0.25) { disc(ctx, x, y - 8, 4, "#4c5466", 0.9); disc(ctx, x - 1, y - 9, 2, "#6b7488", 0.9); }
        if (prop.variant === 7 && noise(x, y, 2) < 0.1) { rect(ctx, x - 6, y - 10, 4, 3, "#2f84ab"); rect(ctx, x + 2, y - 10, 4, 3, "#2f84ab"); rect(ctx, x - 2, y - 11, 4, 5, S.metal[2]); if (Math.floor(now / 500) % 2) px(ctx, x, y - 12, "#ff5a4a"); }
        break;
      }
      shadow(ctx, x, y - 1, 9);
      if (prop.variant % 2) {
        // Server tower with blinking status lights.
        rect(ctx, x - 7, y - 30, 14, 30, S.outline); rect(ctx, x - 6, y - 29, 12, 28, S.metal[1]); rect(ctx, x - 6, y - 29, 12, 2, S.metal[2]);
        for (let row = 0; row < 6; row++) {
          rect(ctx, x - 5, y - 25 + row * 4, 10, 3, "#262c3a");
          const on = Math.floor(now / 300 + row * 3 + prop.x) % 5;
          px(ctx, x - 4 + on, y - 24 + row * 4, row % 3 ? "#5ee08a" : S.cyan[0]);
        }
      } else {
        // Plant pod: glass dome over greenery.
        rect(ctx, x - 7, y - 6, 14, 6, S.outline); rect(ctx, x - 6, y - 5, 12, 4, S.metal[0]);
        disc(ctx, x, y - 13, 8, S.outline, 1); disc(ctx, x, y - 13, 7, "#1f3a33", 1);
        disc(ctx, x, y - 12, 5, "#3f8a41"); disc(ctx, x - 2, y - 14, 3, "#5aa94f");
        px(ctx, x - 4, y - 17, "#c6f6ff"); px(ctx, x - 3, y - 18, "#c6f6ff");
      }
      break;
    }
    case "bush": {
      if (prop.variant >= 4) break;
      shadow(ctx, x, y, 6);
      const color = ["#c98a3a", "#4f86d9", "#5fb760", "#9aa3b8"][prop.variant % 4];
      rect(ctx, x - 6, y - 10, 12, 10, S.outline); rect(ctx, x - 5, y - 9, 10, 8, color); rect(ctx, x - 5, y - 9, 10, 1, "#ffffff55");
      rect(ctx, x - 5, y - 5, 10, 1, S.outline);
      break;
    }
    case "rock":
      rect(ctx, x - 5, y - 6, 10, 6, S.outline); rect(ctx, x - 4, y - 5, 8, 4, S.metal[1]);
      for (let i = 0; i < 3; i++) rect(ctx, x - 3 + i * 3, y - 4, 1, 2, "#262c3a");
      break;
    case "lamp":
      shadow(ctx, x, y, 3);
      rect(ctx, x - 1, y - 22, 3, 22, S.outline); rect(ctx, x, y - 21, 1, 21, S.metal[2]);
      rect(ctx, x - 3, y - 26, 7, 5, S.outline); rect(ctx, x - 2, y - 25, 5, 3, S.cyan[Math.floor(now / 900 + prop.x) % 7 ? 0 : 2]);
      break;
    case "bench":
      shadow(ctx, x, y, 8);
      rect(ctx, x - 9, y - 7, 18, 5, S.outline); rect(ctx, x - 8, y - 6, 16, 3, S.metal[0]); rect(ctx, x - 8, y - 6, 16, 1, S.metal[3]);
      rect(ctx, x - 7, y - 2, 2, 2, S.outline); rect(ctx, x + 5, y - 2, 2, 2, S.outline);
      break;
    case "fountain": {
      // Reactor core: a pulsing orb inside a ring.
      const pulse = Math.sin(now / 500) * 0.5 + 0.5;
      disc(ctx, x, y - 10, 15, S.outline, 0.55); disc(ctx, x, y - 10, 14, S.metal[1], 0.55); disc(ctx, x, y - 11, 11, "#1d2433", 0.55);
      disc(ctx, x, y - 16, 7, S.outline); disc(ctx, x, y - 16, 6, S.cyan[1]); disc(ctx, x, y - 16, 4 + Math.round(pulse), S.cyan[0]); disc(ctx, x - 1, y - 18, 2, S.cyan[2]);
      for (let i = 0; i < 4; i++) { const a = now / 700 + (i * Math.PI) / 2; px(ctx, x + Math.round(Math.cos(a) * 10), y - 16 + Math.round(Math.sin(a) * 4), S.cyan[2]); }
      break;
    }
    case "crate":
      shadow(ctx, x, y, 6);
      rect(ctx, x - 6, y - 11, 12, 11, S.outline); rect(ctx, x - 5, y - 10, 10, 9, prop.variant ? "#c98a3a" : S.metal[0]);
      rect(ctx, x - 5, y - 10, 10, 1, "#ffffff40"); rect(ctx, x - 1, y - 10, 2, 9, S.outline);
      break;
    case "crops":
      rect(ctx, x - 1, y - 6, 2, 5, "#3f8a41"); rect(ctx, x - 3, y - 8, 2, 2, "#5aa94f"); rect(ctx, x + 1, y - 9, 2, 2, "#5aa94f");
      if (Math.floor(now / 1200 + prop.x) % 4 === 0) px(ctx, x, y - 11, "#c78bff");
      break;
    case "fence":
      rect(ctx, x - 8, y - 8, 16, 2, S.outline); rect(ctx, x - 8, y - 8, 16, 1, S.metal[3]);
      rect(ctx, x - 7, y - 9, 2, 9, S.outline); rect(ctx, x - 6, y - 9, 1, 8, S.metal[2]);
      break;
    case "sign": {
      const label = (prop.label || "").slice(0, 26);
      const w = Math.max(22, textWidth(label) + 8);
      rect(ctx, x - 1, y - 6, 2, 6, S.metal[1]);
      rect(ctx, x - w / 2, y - 17, w, 11, S.outline); rect(ctx, x - w / 2 + 1, y - 16, w - 2, 9, "#10202a");
      text(ctx, label, x - w / 2 + 4, y - 14, S.cyan[0]);
      break;
    }
  }
}

const W = PLACE_SPRITE.w, H = PLACE_SPRITE.h, DECK = H - 30;

function moduleSprite(category: Category, kind: string, lit: boolean, open: boolean) {
  return cached(`m|${category}|${kind}|${lit}|${open}`, W, H, ctx => {
    const [accent, accentDark] = ACCENT[category];
    if (kind === "integration") {
      // Docking pod: rounded capsule with a dish.
      disc(ctx, W / 2, H - 20, 26, S.outline, 0.62); disc(ctx, W / 2, H - 20, 25, S.metal[0], 0.6); disc(ctx, W / 2 - 4, H - 24, 16, S.metal[2], 0.55);
      rect(ctx, W / 2 - 18, H - 22, 36, 5, S.outline); rect(ctx, W / 2 - 17, H - 21, 34, 3, lit ? S.cyan[0] : "#2a5a70");
      rect(ctx, W / 2 - 1, 8, 2, 16, S.metal[2]); disc(ctx, W / 2, 10, 6, S.outline, 0.6); disc(ctx, W / 2, 10, 5, S.metal[3], 0.6); px(ctx, W / 2, 9, accent);
      stamp(ctx, ICONS[category], { x: accent }, W / 2 - 3.5, H - 36);
      return;
    }
    rect(ctx, 3, DECK - 1, W - 6, 31, S.outline);
    rect(ctx, 4, DECK, W - 8, 26, S.metal[0]);
    for (let x = 14; x < W - 4; x += 12) rect(ctx, x, DECK, 1, 26, S.metal[1]);
    rect(ctx, 4, DECK + 12, W - 8, 1, S.metal[1]);
    rect(ctx, 4, DECK, W - 8, 1, S.metal[3]);
    rect(ctx, 4, H - 4, W - 8, 3, S.metal[1]);
    // Window band.
    rect(ctx, 8, DECK + 4, 22, 6, S.outline); rect(ctx, W - 30, DECK + 4, 22, 6, S.outline);
    rect(ctx, 9, DECK + 5, 20, 4, lit ? S.cyan[0] : "#2a5a70"); rect(ctx, W - 29, DECK + 5, 20, 4, lit ? S.cyan[0] : "#2a5a70");
    rect(ctx, 9, DECK + 5, 20, 1, lit ? S.cyan[2] : "#3f7a90"); rect(ctx, W - 29, DECK + 5, 20, 1, lit ? S.cyan[2] : "#3f7a90");
    // Sliding door with hazard frame.
    const dx = W / 2 - 7;
    for (let i = 0; i < 16; i++) { px(ctx, dx - 1, DECK + 8 + i, i % 4 < 2 ? S.hazard[0] : S.hazard[1]); px(ctx, dx + 14, DECK + 8 + i, i % 4 < 2 ? S.hazard[0] : S.hazard[1]); }
    rect(ctx, dx, DECK + 8, 14, 18, S.outline);
    if (open) { rect(ctx, dx + 1, DECK + 9, 2, 17, S.metal[1]); rect(ctx, dx + 11, DECK + 9, 2, 17, S.metal[1]); rect(ctx, dx + 3, DECK + 9, 8, 17, lit ? "#1d3a48" : "#141826"); }
    else { rect(ctx, dx + 1, DECK + 9, 6, 17, S.metal[2]); rect(ctx, dx + 7, DECK + 9, 6, 17, S.metal[2]); rect(ctx, dx + 7, DECK + 9, 1, 17, S.metal[1]); }
    // Roof deck with the category stripe, vents and an icon panel.
    for (let row = 0; row < DECK + 2; row++) {
      const inset = Math.round((1 - row / (DECK + 2)) * 5);
      rect(ctx, inset, row + 2, W - inset * 2, 1, S.outline);
      rect(ctx, inset + 1, row + 2, W - inset * 2 - 2, 1, row < 3 ? S.metal[3] : row % 8 === 7 ? S.metal[1] : S.metal[2]);
    }
    rect(ctx, 8, 10, W - 16, 4, accentDark); rect(ctx, 8, 10, W - 16, 2, accent);
    for (let i = 0; i < 3; i++) { rect(ctx, 12 + i * 8, 20, 6, 5, S.outline); rect(ctx, 13 + i * 8, 21, 4, 3, S.metal[1]); }
    rect(ctx, W / 2 - 7, DECK - 12, 14, 11, S.outline); rect(ctx, W / 2 - 6, DECK - 11, 12, 9, "#10202a");
    stamp(ctx, ICONS[category], { x: accent }, W / 2 - 3.5, DECK - 10);
    rect(ctx, W - 20, 4, 2, 10, S.metal[1]);
    rect(ctx, 0, DECK + 1, W, 2, S.outline);
  });
}

function interiorSprite(category: Category) {
  return cached(`i|${category}`, W, H, ctx => {
    const [accent] = ACCENT[category];
    rect(ctx, 3, 13, W - 6, H - 13, S.outline);
    rect(ctx, 4, 14, W - 8, 12, S.metal[1]);
    for (let i = 0; i < 4; i++) { rect(ctx, 8 + i * 17, 16, 14, 8, "#10202a"); rect(ctx, 9 + i * 17, 17, 12, 1, accent); rect(ctx, 9 + i * 17, 19, 8, 1, S.cyan[1]); rect(ctx, 9 + i * 17, 21, 10, 1, S.cyan[1]); }
    rect(ctx, 7, 26, W - 14, H - 31, S.floor[1]);
    for (let y = 26; y < H - 5; y += 8) for (let x = 7; x < W - 7; x += 8) { rect(ctx, x, y, 8, 1, S.floor[0]); rect(ctx, x, y, 1, 8, S.floor[0]); }
    rect(ctx, 4, 14, 3, H - 15, S.metal[0]); rect(ctx, W - 7, 14, 3, H - 15, S.metal[0]);
    disc(ctx, W / 2, 44, 9, S.outline, 0.5); disc(ctx, W / 2, 44, 8, S.metal[2], 0.5);
    stamp(ctx, ICONS[category], { x: accent }, W / 2 - 3.5, 37);
    rect(ctx, 4, H - 5, W / 2 - 12, 4, S.outline); rect(ctx, W / 2 + 8, H - 5, W / 2 - 12, 4, S.outline);
  });
}

function drawPlace(ctx: Ctx, place: Place, state: PlaceState) {
  const { x, y } = placeOrigin(place);
  shadow(ctx, x + W / 2, y - 1, W / 2 + 2, 0.3);
  const lit = state.active || state.night > 0.1;
  if (state.open < 1 || place.kind === "integration") { ctx.globalAlpha = place.kind === "integration" ? 1 : 1 - state.open; ctx.drawImage(moduleSprite(place.category, place.kind, lit, state.active), x, y - H); }
  if (state.open > 0 && place.kind !== "integration") { ctx.globalAlpha = state.open; ctx.drawImage(interiorSprite(place.category), x, y - H); }
  ctx.globalAlpha = 1;
  if (state.selected) {
    const c = Math.floor(state.now / 300) % 2 ? S.cyan[0] : S.cyan[2];
    rect(ctx, x - 2, y - H - 2, W + 4, 1, c); rect(ctx, x - 2, y + 1, W + 4, 1, c); rect(ctx, x - 2, y - H - 2, 1, H + 4, c); rect(ctx, x + W + 1, y - H - 2, 1, H + 4, c);
  }
}

function placeEffects(ctx: Ctx, place: Place, state: PlaceState) {
  if (place.kind === "integration" || state.open > 0.5) return;
  const { x, y } = placeOrigin(place);
  const blink = Math.floor(state.now / 450) % 2 === 0;
  if (state.error) { if (blink) { rect(ctx, x + W - 21, y - H + 1, 4, 3, "#ff5a4a"); } }
  else if (state.active && blink) rect(ctx, x + W - 21, y - H + 1, 4, 3, "#5ee08a");
}

function glows(layout: Layout<any>, places: Map<string, PlaceState>, night: number, now: number): Glow[] {
  const out: Glow[] = [];
  for (const prop of layout.props) {
    if (prop.kind === "lamp") out.push({ x: prop.x, y: prop.y - 23, radius: 20, color: S.cyan[0], strength: 0.7 });
    if (prop.kind === "fountain") out.push({ x: prop.x, y: prop.y - 16, radius: 34, color: "#4af0d8", strength: 0.75 + Math.sin(now / 500) * 0.2 });
  }
  for (const pond of layout.ponds) out.push({ x: pond.cx, y: pond.cy, radius: pond.rx, color: "#1fb5c8", strength: 0.5 });
  for (const place of layout.places) {
    const { x, y } = placeOrigin(place);
    const state = places.get(place.id);
    if (state?.active) out.push({ x: x + W / 2, y: y - 20, radius: 26, color: S.cyan[0], strength: 0.6 });
  }
  return out;
}

export const station: PixelTheme = {
  id: "station",
  backdrop: S.space[0],
  outline: S.outline,
  jumpsuit: true,
  label: { bg: "#10202a", rim: "#5ee6ff", text: "#d8fbff", dim: "#7fb8c8" },
  paintGround,
  animateGround,
  drawProp,
  drawPlace,
  placeEffects,
  ambient: () => ({ darkness: 0.22, tint: "#060a24", warmth: 0 }),
  glows,
};
