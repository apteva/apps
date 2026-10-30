// Frame composition shared by every pixel theme: ground, y-sorted world,
// particles, lighting and emotes. Returns hit regions for interaction.
import { TILE, disc, makeCanvas, rect, seedFor, shadow, type Ctx, type Point } from "./core";
import { CELL, characterFrame, drawEmote, type Emote, type Look } from "./characters";
import type { Layout, Place } from "./layout";
import type { Pose, TrackEvent } from "./motion";
import { PLACE_SPRITE, placeOrigin, type PixelTheme, type PlaceState } from "./theme";

export type Resident = { id: string; name: string; status: string; region: number; look: Look; at: (time: number) => Pose };
export type SceneEvent = TrackEvent & { target_agent_id?: string };
export type Hit = { id: string; kind: "agent" | "place" | "region"; x: number; y: number; w: number; h: number; label: string; anchor: Point };
export type FrameInput = {
  layout: Layout<any>; theme: PixelTheme; residents: Resident[]; events: SceneEvent[]; now: number;
  hover: string; selectedAgent: string; selectedPlace: string; reducedMotion: boolean;
};

const groundCache = new WeakMap<Layout<any>, Map<string, HTMLCanvasElement>>();
const openState = new Map<string, { value: number; at: number }>();

function ground(layout: Layout<any>, theme: PixelTheme) {
  let byTheme = groundCache.get(layout);
  if (!byTheme) groundCache.set(layout, (byTheme = new Map()));
  let canvas = byTheme.get(theme.id);
  if (!canvas) {
    const [created, ctx] = makeCanvas(layout.width, layout.height);
    theme.paintGround(ctx, layout);
    byTheme.set(theme.id, (canvas = created));
  }
  return canvas;
}

// Recent activity decides emotes and whether a place looks busy.
function latestByAgent(events: SceneEvent[], now: number, window: number) {
  const latest = new Map<string, SceneEvent>();
  for (const event of events) {
    const time = Date.parse(event.time);
    if (time <= now && now - time < window) latest.set(event.agent_id, event);
  }
  return latest;
}

function emoteFor(event: SceneEvent | undefined, pose: Pose, status: string): Emote | null {
  if (pose.inside) return "working";
  if (!event) return status === "waiting" ? "waiting" : null;
  switch (event.kind) {
    case "thinking": return "thinking";
    case "error": return "error";
    case "message": return "message";
    case "waiting": return "waiting";
    case "done": case "spawn": return "done";
    default: return null;
  }
}

export function drawFrame(ctx: Ctx, input: FrameInput): Hit[] {
  const { layout, theme, now } = input;
  const hits: Hit[] = [];
  const light = theme.ambient(now);
  ctx.imageSmoothingEnabled = false;
  ctx.drawImage(ground(layout, theme), 0, 0);
  theme.animateGround(ctx, layout, input.reducedMotion ? 0 : now);

  const poses = new Map(input.residents.map(resident => [resident.id, resident.at(now)]));
  const states = new Map<string, PlaceState>();
  for (const place of layout.places) {
    const calls = input.events.filter(event => event.target_id === place.id && Date.parse(event.time) <= now);
    const last = calls.at(-1);
    const active = !!last && now - Date.parse(last.time) < 20000;
    const error = !!last && last.kind === "error" && now - Date.parse(last.time) < 30000;
    const occupied = [...poses.values()].some(pose => pose.inside === place.id);
    const wantOpen = occupied || input.hover === place.id || input.selectedPlace === place.id ? 1 : 0;
    // Ease the roof away over ~250ms; snaps under reduced motion.
    const previous = openState.get(place.id) ?? { value: wantOpen, at: now };
    const step = input.reducedMotion ? 1 : Math.min(1, Math.abs(now - previous.at) / 250);
    const value = previous.value + (wantOpen - previous.value) * step;
    openState.set(place.id, { value, at: now });
    states.set(place.id, { open: value < 0.02 ? 0 : value > 0.98 ? 1 : value, active, error, selected: input.selectedPlace === place.id, night: light.darkness, now });
    const { x, y } = placeOrigin(place);
    hits.push({ id: place.id, kind: "place", x, y: y - PLACE_SPRITE.h, w: PLACE_SPRITE.w, h: PLACE_SPRITE.h, label: place.name, anchor: { x: x + PLACE_SPRITE.w / 2, y: y - PLACE_SPRITE.h } });
  }

  type Drawable = { y: number; draw: () => void };
  const drawables: Drawable[] = [];
  for (const prop of layout.props) drawables.push({ y: prop.y, draw: () => theme.drawProp(ctx, prop, input.reducedMotion ? 0 : now, light.darkness) });
  for (const place of layout.places) {
    const state = states.get(place.id)!;
    drawables.push({ y: placeOrigin(place).y - 0.5, draw: () => theme.drawPlace(ctx, place, state) });
  }
  for (const resident of input.residents) {
    const pose = poses.get(resident.id)!;
    const place = pose.inside ? layout.places.find(item => item.id === pose.inside) : undefined;
    // Residents inside a place render on its floor, above the open interior.
    const sortY = place ? placeOrigin(place).y : pose.y;
    drawables.push({ y: sortY, draw: () => drawResident(ctx, resident, pose, input) });
    hits.push({ id: resident.id, kind: "agent", x: pose.x - 7, y: pose.y - 21, w: 14, h: 23, label: resident.name, anchor: { x: pose.x, y: pose.y - 22 } });
  }
  drawables.sort((a, b) => a.y - b.y);
  for (const item of drawables) item.draw();
  for (const place of layout.places) theme.placeEffects(ctx, place, states.get(place.id)!);
  drawHandoffs(ctx, input, poses);

  // Pixel lighting: an ambient tint, then stepped glows that keep hard edges.
  if (light.warmth > 0) { ctx.globalAlpha = light.warmth; rect(ctx, 0, 0, layout.width, layout.height, "#ff9a3c"); }
  if (light.darkness > 0) { ctx.globalAlpha = light.darkness; rect(ctx, 0, 0, layout.width, layout.height, light.tint); }
  ctx.globalAlpha = 1;
  const glowList = theme.glows(layout, states, light.darkness, now);
  if (glowList.length) {
    ctx.globalCompositeOperation = "lighter";
    for (const glow of glowList) {
      for (const [scale, alpha] of [[1, 0.1], [0.66, 0.12], [0.36, 0.16]]) {
        ctx.globalAlpha = alpha * glow.strength;
        disc(ctx, glow.x, glow.y, glow.radius * scale, glow.color, 0.8);
      }
    }
    ctx.globalCompositeOperation = "source-over";
    ctx.globalAlpha = 1;
  }

  const recent = latestByAgent(input.events, now, 7000);
  for (const resident of input.residents) {
    const pose = poses.get(resident.id)!;
    const emote = emoteFor(recent.get(resident.id), pose, resident.status);
    if (!emote) continue;
    const bob = input.reducedMotion ? 0 : Math.round(Math.sin(now / 300 + seedFor(resident.id)) * 0.6);
    drawEmote(ctx, emote, pose.x, pose.y - 22 + bob, !input.reducedMotion && Math.floor(now / 400) % 3 === 0);
  }
  for (const region of layout.regions) {
    hits.push({ id: region.id, kind: "region", x: region.x * TILE, y: region.y * TILE, w: region.w * TILE, h: region.h * TILE, label: region.label, anchor: { x: (region.x + region.w / 2) * TILE, y: region.y * TILE + 6 } });
  }
  return hits;
}

function drawResident(ctx: Ctx, resident: Resident, pose: Pose, input: FrameInput) {
  const frame = pose.walking && !input.reducedMotion ? Math.floor(input.now / 150 + seedFor(resident.id)) % 4 : 0;
  const x = Math.round(pose.x), y = Math.round(pose.y);
  const selected = input.selectedAgent === resident.id, hovered = input.hover === resident.id;
  if (selected || hovered) {
    ctx.globalAlpha = 0.85;
    disc(ctx, x, y + 1, 7, selected ? "#ffe27a" : "#fbf6ea", 0.4);
    ctx.globalAlpha = 1;
  }
  shadow(ctx, x, y + 1, 5, 0.3);
  ctx.drawImage(characterFrame(resident.look, pose.facing, frame, input.theme.outline), x - CELL.w / 2, y - CELL.h + 3);
  if (selected) {
    const bounce = input.reducedMotion ? 0 : Math.abs(Math.round(Math.sin(input.now / 220) * 2));
    const ty = y - 30 - bounce;
    rect(ctx, x - 3, ty, 7, 2, "#2b2233"); rect(ctx, x - 2, ty + 2, 5, 1, "#2b2233"); rect(ctx, x - 1, ty + 3, 3, 1, "#2b2233"); rect(ctx, x, ty + 4, 1, 1, "#2b2233");
    rect(ctx, x - 2, ty, 5, 1, "#ffe27a"); rect(ctx, x - 1, ty + 1, 3, 1, "#ffe27a"); rect(ctx, x, ty + 2, 1, 1, "#ffe27a");
  }
}

// A letter flies from sender to receiver for observed handoffs.
function drawHandoffs(ctx: Ctx, input: FrameInput, poses: Map<string, Pose>) {
  for (const event of input.events) {
    if (!event.target_agent_id) continue;
    const age = input.now - Date.parse(event.time);
    if (age < 0 || age > 2400) continue;
    const from = poses.get(event.agent_id), to = poses.get(event.target_agent_id);
    if (!from || !to) continue;
    const t = age / 2400;
    const x = from.x + (to.x - from.x) * t, y = from.y - 20 + (to.y - from.y) * t - Math.sin(t * Math.PI) * 18;
    rect(ctx, x - 4, y - 3, 9, 7, "#2b2233"); rect(ctx, x - 3, y - 2, 7, 5, "#fbf6ea");
    rect(ctx, x - 2, y - 1, 1, 1, "#c2523d"); rect(ctx, x + 2, y - 1, 1, 1, "#c2523d"); rect(ctx, x - 1, y, 3, 1, "#c2523d");
  }
}

