// Per-frame composition for the iso village: the baked static scene, then
// agents and ducks in depth order. After each moving thing, the static sprites
// in front of it that overlap it are drawn again, which is the painter's
// algorithm without re-sorting the whole village every frame.
import { makeCanvas, rect, seedFor, shadow, type Ctx, type Point } from "../core";
import { drawEmote, type Emote, type Look } from "../characters";
import type { Hit, SceneEvent } from "../render";
import { FZ } from "./archetypes";
import { G, groundAt, type IsoLayout } from "./layout";
import type { IsoPose } from "./motion";
import { depthKey, type Scene } from "./paint";
import { INK, TH, TW, hash, lighten, outline } from "./raster";

export type IsoResident = { id: string; name: string; status: string; region: number; look: Look; at: (time: number) => IsoPose };
export type IsoFrameInput = {
  layout: IsoLayout<any>; scene: Scene; residents: IsoResident[]; events: SceneEvent[]; now: number;
  hover: string; selectedAgent: string; selectedPlace: string; reducedMotion: boolean;
};
type Dynamic = { key: number; x: number; y: number; w: number; h: number; draw: () => void };

const project = (scene: Scene, x: number, y: number, z = 0): Point => ({ x: scene.ox + (x - y) * TW, y: scene.oy + (x + y) * TH - z });

export function drawIsoFrame(ctx: Ctx, input: IsoFrameInput) {
  const { layout, scene, now } = input;
  const time = input.reducedMotion ? 0 : now;
  ctx.imageSmoothingEnabled = false;
  ctx.drawImage(scene.baked, 0, 0);
  drawGlints(ctx, input, time);

  const hits: Hit[] = [];
  const poses = new Map(input.residents.map(resident => [resident.id, resident.at(now)]));
  const dynamics: Dynamic[] = [];
  for (const resident of input.residents) {
    const pose = poses.get(resident.id)!;
    const at = project(scene, pose.x, pose.y, pose.inside ? FZ : 0);
    const x = Math.round(at.x), y = Math.round(at.y);
    dynamics.push({ key: depthKey(layout, pose.x, pose.y, pose.x + pose.y + 0.3), x: x - 7, y: y - 31, w: 14, h: 33, draw: () => drawResident(ctx, resident, pose, x, y, input) });
    hits.push({ id: resident.id, kind: "agent", x: x - 7, y: y - 21, w: 14, h: 23, label: resident.name, anchor: { x, y: y - 24 } });
  }
  for (const region of layout.regions) {
    if (!region.pond) continue;
    for (let i = 0; i < 3; i++) {
      const a = time / 4000 + i * 2.1 + region.cx, x = region.pond.cx + Math.cos(a) * (region.pond.rx - 0.9 - i * 0.3), y = region.pond.cy + Math.sin(a) * (region.pond.ry - 0.8 - i * 0.18);
      const at = project(scene, x, y, -1), sx = Math.round(at.x), sy = Math.round(at.y), facing = Math.cos(a) > 0 ? -1 : 1;
      dynamics.push({ key: x + y - 3, x: sx - 6, y: sy - 7, w: 12, h: 9, draw: () => drawDuck(ctx, sx, sy, facing) });
    }
  }
  dynamics.sort((a, b) => a.key - b.key);
  for (const item of dynamics) {
    item.draw();
    for (const front of scene.statics) {
      if (front.key <= item.key) continue;
      if (front.x < item.x + item.w && item.x < front.x + front.canvas.width && front.y < item.y + item.h && item.y < front.y + front.canvas.height) ctx.drawImage(front.canvas, front.x, front.y);
    }
  }

  drawSmoke(ctx, input, time);
  drawHandoffs(ctx, input, poses);
  const recent = latestByAgent(input.events, now, 7000);
  for (const resident of input.residents) {
    const pose = poses.get(resident.id)!;
    const emote = emoteFor(recent.get(resident.id), pose, resident.status);
    if (!emote) continue;
    const at = project(scene, pose.x, pose.y, pose.inside ? FZ : 0);
    const bob = input.reducedMotion ? 0 : Math.round(Math.sin(now / 300 + seedFor(resident.id)) * 0.6);
    drawEmote(ctx, emote, at.x, at.y - 24 + bob, !input.reducedMotion && Math.floor(now / 400) % 3 === 0);
  }

  for (const place of layout.places) {
    const house = scene.houses.get(place.id)!;
    hits.push({ id: place.id, kind: "place", x: house.x, y: house.y, w: house.w, h: house.h, label: place.name, anchor: { x: house.card[0], y: house.card[1] } });
  }
  for (const island of scene.islands) {
    const region = layout.regions.find(item => item.id === island.id)!;
    hits.push({ id: island.id, kind: "region", x: island.x, y: island.y, w: island.w, h: island.h, label: region.label, anchor: { x: island.top[0], y: island.top[1] } });
  }
  return { hits, poses };
}

// Recent activity decides emotes.
function latestByAgent(events: SceneEvent[], now: number, window: number) {
  const latest = new Map<string, SceneEvent>();
  for (const event of events) {
    const time = Date.parse(event.time);
    if (time <= now && now - time < window) latest.set(event.agent_id, event);
  }
  return latest;
}

function emoteFor(event: SceneEvent | undefined, pose: IsoPose, status: string): Emote | null {
  if (!event) return status === "waiting" ? "waiting" : null;
  switch (event.kind) {
    case "thinking": return "thinking";
    case "error": return "error";
    case "message": return "message";
    case "waiting": return "waiting";
    case "done": case "spawn": return "done";
    default: return pose.inside ? "working" : null;
  }
}

// Little character: 12x20, front or back view, four walking frames.
const characters = new Map<string, HTMLCanvasElement>();
export function isoCharacter(look: Look, back: boolean, frame: number) {
  const key = `${look.skin}|${look.hair}|${look.shirt}|${look.pants}|${look.style}|${back}|${frame}`;
  const hit = characters.get(key);
  if (hit) return hit;
  const [canvas, ctx] = makeCanvas(12, 20);
  const px = (color: string, x: number, y: number, w = 1, h = 1) => rect(ctx, x, y, w, h, color);
  const skin = look.skin[0], hair = look.hair[0], shirt = look.shirt[0], pants = look.pants[0];
  const leg = [[0, 0], [1, 0], [0, 0], [0, 1]][frame % 4];
  px(pants, 3, 13, 2, 5 - leg[0]); px(pants, 7, 13, 2, 5 - leg[1]);
  px(lighten(pants, -40), 3, 18 - leg[0], 2, 1); px(lighten(pants, -40), 7, 18 - leg[1], 2, 1);
  px(shirt, 2, 8, 8, 6); px(look.shirt[1], 8, 8, 2, 6); px(lighten(shirt, 25), 2, 8, 8, 1);
  const swing = frame % 2;
  px(look.shirt[1], 1, 9 + swing, 1, 4); px(look.shirt[1], 10, 9 + (1 - swing), 1, 4);
  px(skin, 1, 13 + swing, 1, 1); px(skin, 10, 13 + (1 - swing), 1, 1);
  px(skin, 3, 2, 6, 6); px(look.skin[1], 8, 2, 1, 6);
  px(hair, 3, 1, 6, 2); px(hair, 2, 2, 1, back ? 6 : 3); px(hair, 9, 2, 1, back ? 6 : 3);
  if (look.style === 1) { px(hair, 2, 5, 1, 4); px(hair, 9, 5, 1, 4); }
  if (back) px(hair, 3, 3, 6, 5);
  else { px("#342e42", 4, 4, 1, 2); px("#342e42", 7, 4, 1, 2); px("#ff8fa3", 3, 6, 1, 1); px("#ff8fa3", 8, 6, 1, 1); }
  if (look.style === 2) { px(look.shirt[1], 2, 0, 8, 2); px(lighten(look.shirt[1], -25), 1, 2, 10, 1); }
  outline(ctx, 12, 20, INK);
  characters.set(key, canvas);
  return canvas;
}

function drawResident(ctx: Ctx, resident: IsoResident, pose: IsoPose, x: number, y: number, input: IsoFrameInput) {
  const frame = pose.walking && !input.reducedMotion ? Math.floor(input.now / 125 + seedFor(resident.id)) % 4 : 0;
  const selected = input.selectedAgent === resident.id, hovered = input.hover === resident.id;
  if (selected || hovered) {
    ctx.globalAlpha = 0.9;
    for (let dy = -3; dy <= 3; dy++) for (let dx = -8; dx <= 8; dx++) {
      const e = (dx / 8) ** 2 + (dy / 3.5) ** 2;
      if (e < 1 && e > 0.45) rect(ctx, x + dx, y + dy, 1, 1, selected ? "#ffd166" : "#ffffff");
    }
    ctx.globalAlpha = 1;
  }
  shadow(ctx, x, y, 5, 0.22);
  const sprite = isoCharacter(resident.look, pose.back, frame);
  ctx.save();
  ctx.translate(x, y - 19);
  if (pose.flip) ctx.scale(-1, 1);
  ctx.drawImage(sprite, -6, 0);
  ctx.restore();
  if (selected) {
    const bounce = input.reducedMotion ? 0 : Math.abs(Math.round(Math.sin(input.now / 220) * 2));
    const ty = y - 30 - bounce;
    rect(ctx, x - 3, ty, 7, 2, INK_HEX); rect(ctx, x - 2, ty + 2, 5, 1, INK_HEX); rect(ctx, x - 1, ty + 3, 3, 1, INK_HEX); rect(ctx, x, ty + 4, 1, 1, INK_HEX);
    rect(ctx, x - 2, ty, 5, 1, "#ffd166"); rect(ctx, x - 1, ty + 1, 3, 1, "#ffd166"); rect(ctx, x, ty + 2, 1, 1, "#ffd166");
  }
}
const INK_HEX = "#342e42";

function drawDuck(ctx: Ctx, x: number, y: number, facing: number) {
  rect(ctx, x - 4, y, 8, 1, "#b6efff");
  rect(ctx, x - 3, y - 3, 6, 3, "#ffffff"); rect(ctx, x + (facing > 0 ? 1 : -3), y - 6, 2, 3, "#ffffff");
  rect(ctx, x + (facing > 0 ? 3 : -5), y - 5, 2, 1, "#ffb703");
  rect(ctx, x + (facing > 0 ? 2 : -3), y - 5, 1, 1, INK_HEX);
}

// Sparkles drifting on open water, never over land.
function drawGlints(ctx: Ctx, input: IsoFrameInput, time: number) {
  const { scene, layout } = input;
  const count = Math.round(scene.width * scene.height / 14000);
  for (let i = 0; i < count; i++) {
    const k = (time / 6000 + hash(i, 7)) % 1;
    if (k > 0.7) continue;
    const wx = Math.round(hash(i, 1) * scene.width + k * 20), wy = Math.round(hash(i, 2) * scene.height);
    // Back to tiles: x - y = (wx - ox) / TW, x + y = (wy - oy) / TH.
    const a = (wx - scene.ox) / TW, b = (wy - scene.oy) / TH, tx = Math.floor((a + b) / 2), ty = Math.floor((b - a) / 2);
    let open = true;
    for (let dy = -2; dy <= 2 && open; dy++) for (let dx = -2; dx <= 2; dx++) if (groundAt(layout, tx + dx, ty + dy) !== G.sea) { open = false; break; }
    if (open) rect(ctx, wx, wy, 3, 1, "#c8f3ff");
  }
}

function drawSmoke(ctx: Ctx, input: IsoFrameInput, time: number) {
  for (const place of input.layout.places) {
    const [bx, by] = input.scene.houses.get(place.id)!.chimney;
    for (let i = 0; i < 4; i++) {
      const k = (time / 2800 + i / 4 + hash(place.x, place.y)) % 1, size = Math.round(2 + k * 3);
      const x = Math.round(bx + Math.sin(k * 5 + i) * 3 + k * 8), y = Math.round(by - k * 26);
      ctx.globalAlpha = 0.9 * (1 - k);
      rect(ctx, x - size / 2, y - size / 2, size, size, "#ffffff");
    }
  }
  ctx.globalAlpha = 1;
}

// A letter flies from sender to receiver for observed handoffs.
function drawHandoffs(ctx: Ctx, input: IsoFrameInput, poses: Map<string, IsoPose>) {
  for (const event of input.events) {
    if (!event.target_agent_id) continue;
    const age = input.now - Date.parse(event.time);
    if (age < 0 || age > 2400) continue;
    const from = poses.get(event.agent_id), to = poses.get(event.target_agent_id);
    if (!from || !to) continue;
    const a = project(input.scene, from.x, from.y), b = project(input.scene, to.x, to.y);
    const t = age / 2400;
    const x = a.x + (b.x - a.x) * t, y = a.y - 24 + (b.y - a.y) * t - Math.sin(t * Math.PI) * 18;
    rect(ctx, x - 4, y - 3, 9, 7, INK_HEX); rect(ctx, x - 3, y - 2, 7, 5, "#ffffff");
    rect(ctx, x - 2, y - 1, 1, 1, "#ef476f"); rect(ctx, x + 2, y - 1, 1, 1, "#ef476f"); rect(ctx, x - 1, y, 3, 1, "#ef476f");
  }
}
