// Agent movement is a pure function of the scene clock: polling never restarts
// it, pausing freezes it and replay seeks reproduce the same positions.
import { TILE, seedFor, type Point } from "./core";
import { isWalkable, type Layout, type Place } from "./layout";

export type Facing = "down" | "up" | "left" | "right";
export type Pose = Point & { facing: Facing; walking: boolean; inside?: string };
export type TrackEvent = { id: string; agent_id: string; kind: string; target_id?: string; time: string };

type Leg = { from: Point; to: Point; start: number; end: number };
type Visit = { start: number; entered: number; leaving: number; end: number; place: Place; inbound: Point[]; outbound: Point[] };

const fraction = (seed: number, index: number) => {
  let n = Math.imul(seed ^ Math.imul(index + 1, 374761393), 668265263);
  n = Math.imul(n ^ (n >>> 13), 1274126177);
  return ((n ^ (n >>> 16)) >>> 0) / 4294967296;
};
const distance = (a: Point, b: Point) => Math.abs(a.x - b.x) + Math.abs(a.y - b.y);
const length = (points: Point[]) => points.slice(1).reduce((sum, point, i) => sum + distance(points[i], point), 0);
const feet = (tile: Point): Point => ({ x: tile.x * TILE + TILE / 2, y: tile.y * TILE + TILE - 3 });
const facingOf = (from: Point, to: Point, fallback: Facing): Facing => {
  const dx = to.x - from.x, dy = to.y - from.y;
  if (!dx && !dy) return fallback;
  return Math.abs(dx) > Math.abs(dy) ? (dx > 0 ? "right" : "left") : (dy > 0 ? "down" : "up");
};

export function sampleRoute(points: Point[], progress: number): Pose {
  let remaining = Math.max(0, Math.min(1, progress)) * length(points);
  for (let i = 1; i < points.length; i++) {
    const d = distance(points[i - 1], points[i]);
    if (remaining <= d || i === points.length - 1) {
      const t = d ? Math.min(1, remaining / d) : 0;
      return {
        x: points[i - 1].x + (points[i].x - points[i - 1].x) * t,
        y: points[i - 1].y + (points[i].y - points[i - 1].y) * t,
        facing: facingOf(points[i - 1], points[i], "down"),
        walking: progress > 0 && progress < 1 && d > 0,
      };
    }
    remaining -= d;
  }
  return { ...(points[0] || { x: 0, y: 0 }), facing: "down", walking: false };
}

export function workSpot(place: Place, agentId: string): Point {
  const seed = seedFor(agentId + place.id);
  return { x: place.x * TILE + 18 + Math.round(fraction(seed, 0) * (place.w * TILE - 36)), y: place.y * TILE + 30 + Math.round(fraction(seed, 1) * 8) };
}

export function agentTrack(agentId: string, events: TrackEvent[], layout: Layout<any>, regionIndex: number) {
  const region = layout.regions[regionIndex];
  const seed = seedFor(agentId);
  const wander = 20 + fraction(seed, 1) * 9, hurry = 40 + fraction(seed, 0) * 10;
  const spots = region.spots.filter(spot => isWalkable(layout, spot));
  if (!spots.length) spots.push({ x: region.plaza.x + 1, y: region.plaza.y + 1 });
  const route = (from: Point, to: Point) => {
    const tiles = layout.route({ x: Math.floor(from.x / TILE), y: Math.floor(from.y / TILE) }, { x: Math.floor(to.x / TILE), y: Math.floor(to.y / TILE) });
    return [from, ...tiles.slice(1, -1).map(feet), to];
  };

  // A personal loop of five resting points with varied pauses.
  const chosen = Array.from({ length: 5 }, (_, i) => feet(spots[Math.floor(fraction(seed, i + 3) * spots.length)]));
  const legs: Leg[] = [];
  let duration = 0;
  for (let i = 0; i < chosen.length; i++) {
    const pause = 2200 + fraction(seed, i + 10) * 6000;
    legs.push({ from: chosen[i], to: chosen[i], start: duration, end: duration + pause });
    duration += pause;
    const path = route(chosen[i], chosen[(i + 1) % chosen.length]);
    for (let j = 1; j < path.length; j++) {
      const ms = (distance(path[j - 1], path[j]) / wander) * 1000;
      if (ms <= 0) continue;
      legs.push({ from: path[j - 1], to: path[j], start: duration, end: duration + ms });
      duration += ms;
    }
  }
  const offset = fraction(seed, 2) * duration;
  const stroll = (time: number): Pose => {
    const t = (((time + offset) % duration) + duration) % duration;
    const index = legs.findIndex(leg => t < leg.end);
    const leg = legs[Math.max(0, index)];
    const previous = legs[(Math.max(0, index) - 1 + legs.length) % legs.length];
    const pose = sampleRoute([leg.from, leg.to], (t - leg.start) / (leg.end - leg.start));
    const moving = distance(leg.from, leg.to) > 0;
    return { ...pose, walking: moving, facing: moving ? pose.facing : facingOf(previous.from, previous.to, "down") };
  };

  const visits: Visit[] = [];
  const at = (time: number): Pose => {
    const visit = [...visits].reverse().find(v => v.start <= time);
    if (!visit || time >= visit.end) return stroll(time);
    const place = visit.place;
    if (time >= visit.entered && time < visit.leaving) {
      return { ...visit.inbound[visit.inbound.length - 1], facing: "up", walking: false, inside: place.id };
    }
    const going = time < visit.entered;
    const pose = going
      ? sampleRoute(visit.inbound, (time - visit.start) / (visit.entered - visit.start))
      : sampleRoute(visit.outbound, (time - visit.leaving) / (visit.end - visit.leaving));
    const within = pose.x >= place.x * TILE && pose.x < (place.x + place.w) * TILE && pose.y >= place.y * TILE && pose.y < (place.y + place.h) * TILE;
    return { ...pose, inside: within ? place.id : undefined };
  };

  const places = new Map(layout.places.filter(place => place.region === regionIndex).map(place => [place.id, place]));
  const calls = events
    .filter(event => event.agent_id === agentId && event.kind === "tool" && event.target_id && places.has(event.target_id))
    .sort((a, b) => Date.parse(a.time) - Date.parse(b.time) || a.id.localeCompare(b.id));
  for (const event of calls) {
    const start = Date.parse(event.time);
    if (!Number.isFinite(start)) continue;
    const place = places.get(event.target_id!)!;
    // Bursts of calls to the same place share one visit.
    const previous = visits.at(-1);
    if (previous?.place.id === place.id && start < previous.leaving) continue;
    const origin = at(start);
    const door = feet(place.door), threshold = { x: door.x, y: (place.y + place.h) * TILE - 4 };
    const work = workSpot(place, agentId);
    // An agent still inside another place leaves through that door first.
    let exit: Point[] = [], from: Point = origin;
    if (origin.inside && previous) {
      const previousDoor = feet(previous.place.door), previousThreshold = { x: previousDoor.x, y: (previous.place.y + previous.place.h) * TILE - 4 };
      exit = [origin, { x: origin.x, y: previousThreshold.y }, previousThreshold];
      from = previousDoor;
    }
    const inbound = [...exit, ...route(from, door), threshold, { x: work.x, y: threshold.y }, work];
    const entered = start + (length(inbound) / hurry) * 1000;
    const leaving = entered + 4500 + fraction(seed, 21) * 3000;
    // Walk back out and converge on the stroll without teleporting.
    let end = leaving + 10000, outbound: Point[] = [];
    for (let i = 0; i < 24; i++) {
      outbound = [work, { x: work.x, y: threshold.y }, threshold, ...route(door, stroll(end))];
      const next = leaving + (length(outbound) / hurry) * 1000;
      if (Math.abs(next - end) < 0.01) { end = next; break; }
      end = next;
    }
    visits.push({ start, entered, leaving, end, place, inbound, outbound });
  }
  return { at, visits, stroll };
}
