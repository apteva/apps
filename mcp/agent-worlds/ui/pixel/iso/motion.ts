// Agent movement in tile units, a pure function of the scene clock like the
// top-down engine: a personal stroll between village spots, interrupted by
// visits. A visit walks to the building's door, in to a work spot, waits, then
// walks back out and rejoins the stroll without teleporting.
import { seedFor, type Point } from "../core";
import type { TrackEvent } from "../motion";
import { isWalkableTile, placeAt, type IsoLayout, type IsoPlace } from "./layout";

export type IsoPose = Point & { back: boolean; flip: boolean; walking: boolean; inside?: string };
type Leg = { from: Point; to: Point; start: number; end: number };
export type IsoVisit = { start: number; entered: number; leaving: number; end: number; place: IsoPlace; work: Point; inbound: Point[]; outbound: Point[] };

const fraction = (seed: number, index: number) => {
  let n = Math.imul(seed ^ Math.imul(index + 1, 374761393), 668265263);
  n = Math.imul(n ^ (n >>> 13), 1274126177);
  return ((n ^ (n >>> 16)) >>> 0) / 4294967296;
};
const distance = (a: Point, b: Point) => Math.hypot(a.x - b.x, a.y - b.y);
const length = (points: Point[]) => points.slice(1).reduce((sum, point, i) => sum + distance(points[i], point), 0);
const center = (tile: Point): Point => ({ x: tile.x + 0.5, y: tile.y + 0.5 });
const facing = (from: Point, to: Point) => {
  const dx = to.x - from.x, dy = to.y - from.y;
  return { back: dx < 0 || dy < 0, flip: Math.abs(dy) > Math.abs(dx) ? dy > 0 : dx < 0 };
};

export function sampleIsoRoute(points: Point[], progress: number): IsoPose {
  let remaining = Math.max(0, Math.min(1, progress)) * length(points);
  for (let i = 1; i < points.length; i++) {
    const d = distance(points[i - 1], points[i]);
    if (remaining <= d || i === points.length - 1) {
      const t = d ? Math.min(1, remaining / d) : 0;
      return {
        x: points[i - 1].x + (points[i].x - points[i - 1].x) * t,
        y: points[i - 1].y + (points[i].y - points[i - 1].y) * t,
        ...facing(points[i - 1], points[i]),
        walking: progress > 0 && progress < 1 && d > 0,
      };
    }
    remaining -= d;
  }
  return { ...(points[0] || { x: 0, y: 0 }), back: false, flip: false, walking: false };
}

// The furniture spot an agent uses in a building; stable per agent and place.
export function isoWorkSpot(place: IsoPlace, agentId: string): Point {
  const spots = place.interior.spots;
  if (!spots.length) return center(place.doorIn);
  return spots[seedFor(agentId + place.id) % spots.length];
}

export function isoTrack(agentId: string, events: TrackEvent[], layout: IsoLayout<any>, regionIndex: number) {
  const region = layout.regions[regionIndex];
  const seed = seedFor(agentId);
  const wander = 1.25 + fraction(seed, 1) * 0.55, hurry = 2.5 + fraction(seed, 0) * 0.6;
  const spots = region.spots.filter(spot => isWalkableTile(layout, spot));
  if (!spots.length) spots.push({ x: region.cx + 1, y: region.cy });
  const route = (from: Point, to: Point) => {
    const tiles = layout.route({ x: Math.floor(from.x), y: Math.floor(from.y) }, { x: Math.floor(to.x), y: Math.floor(to.y) });
    return [from, ...tiles.slice(1, -1).map(center), to];
  };

  // A personal loop of five resting points with varied pauses.
  const chosen = Array.from({ length: 5 }, (_, i) => {
    const spot = spots[Math.floor(fraction(seed, i + 3) * spots.length)];
    return { x: spot.x + 0.25 + fraction(seed, i + 30) * 0.5, y: spot.y + 0.25 + fraction(seed, i + 40) * 0.5 };
  });
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
  const stroll = (time: number): IsoPose => {
    const t = (((time + offset) % duration) + duration) % duration;
    const index = Math.max(0, legs.findIndex(leg => t < leg.end));
    const leg = legs[index];
    const moving = distance(leg.from, leg.to) > 0;
    if (moving) return { ...sampleIsoRoute([leg.from, leg.to], (t - leg.start) / (leg.end - leg.start)), walking: true };
    const previous = legs[(index - 1 + legs.length) % legs.length];
    return { ...leg.from, ...facing(previous.from, previous.to), walking: false };
  };

  const visits: IsoVisit[] = [];
  const inside = (pose: IsoPose): IsoPose => ({ ...pose, inside: placeAt(layout, pose.x, pose.y)?.id });
  const at = (time: number): IsoPose => {
    const visit = [...visits].reverse().find(v => v.start <= time);
    if (!visit || time >= visit.end) return inside(stroll(time));
    if (time >= visit.entered && time < visit.leaving) return { ...visit.work, back: true, flip: false, walking: false, inside: visit.place.id };
    const going = time < visit.entered;
    return inside(going
      ? sampleIsoRoute(visit.inbound, (time - visit.start) / (visit.entered - visit.start))
      : sampleIsoRoute(visit.outbound, (time - visit.leaving) / (visit.end - visit.leaving)));
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
    const origin = at(start), work = isoWorkSpot(place, agentId);
    // Routes cross building edges only at doors, so leaving another building first is implicit.
    const inbound = route(origin, work);
    const entered = start + (length(inbound) / hurry) * 1000;
    const leaving = entered + 4500 + fraction(seed, 21) * 3000;
    let end = leaving + 10000, outbound: Point[] = [];
    for (let i = 0; i < 24; i++) {
      outbound = route(work, stroll(end));
      const next = leaving + (length(outbound) / hurry) * 1000;
      if (Math.abs(next - end) < 0.01) { end = next; break; }
      end = next;
    }
    visits.push({ start, entered, leaving, end, place, work, inbound, outbound });
  }
  return { at, visits, stroll };
}
