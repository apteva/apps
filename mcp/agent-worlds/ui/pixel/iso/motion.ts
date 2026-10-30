// Agent movement in tile units, a pure function of the scene clock like the
// top-down engine: a personal stroll between village spots, interrupted by
// visits. A visit walks to the building's door, in to a work spot, waits, then
// walks back out and rejoins the stroll without teleporting.
//
// A director turns raw tool calls into visits an eye can follow: calls close
// together at one station become one visit, a one-off call elsewhere goes by
// envelope instead of a walk, and an agent that falls behind sprints, then
// dashes, works shorter, and finally skips visits that are already stale.
import { seedFor, type Point } from "../core";
import type { TrackEvent } from "../motion";
import { isWalkableTile, placeAt, type IsoLayout, type IsoPlace } from "./layout";
import { stationSpots } from "./stations";

export type Pace = "walk" | "sprint" | "dash";
export type IsoPose = Point & { back: boolean; flip: boolean; walking: boolean; inside?: string; pace?: Pace };
type Leg = { from: Point; to: Point; start: number; end: number };
export type IsoVisit = { start: number; entered: number; leaving: number; end: number; place: IsoPlace; work: Point; inbound: Point[]; outbound: Point[]; pace: Pace; calls: number; skipped: number };
// An envelope sent from wherever the agent is to a place it calls only briefly.
export type Courier = { time: number; place: IsoPlace; tool?: string };

type Call = { time: number; place: IsoPlace; work: Point; tool?: string };
type Activity = { place: IsoPlace; work: Point; start: number; end: number; calls: Call[]; couriers: Call[] };

export const MERGE_GAP = 2500, SPRINT_LAG = 2000, DASH_LAG = 6000;
const PACE = { walk: 1, sprint: 2.2, dash: 5.8 };
const same = (a: Point, b: Point) => a.x === b.x && a.y === b.y;

// Calls to one station less than MERGE_GAP apart are one activity. A blip
// elsewhere (at most two calls within a second, right after an activity) is
// not worth a walk when the agent comes straight back or moves on at once.
export function groupCalls(calls: Call[]): Activity[] {
  const activities: Activity[] = [];
  for (const call of calls) {
    const last = activities.at(-1);
    if (last && last.place === call.place && same(last.work, call.work) && call.time - last.end < MERGE_GAP) { last.calls.push(call); last.end = call.time; }
    else activities.push({ place: call.place, work: call.work, start: call.time, end: call.time, calls: [call], couriers: [] });
  }
  for (let i = 1; i < activities.length; i++) {
    const blip = activities[i], previous = activities[i - 1], next = activities[i + 1];
    if (blip.place === previous.place || blip.calls.length > 2 || blip.end - blip.start >= 1000 || blip.start - previous.end >= MERGE_GAP) continue;
    if (next && next.place !== previous.place && next.start - blip.end >= 1500) continue;
    previous.couriers.push(...blip.calls);
    previous.end = Math.max(previous.end, blip.end);
    activities.splice(i, 1);
    if (next && next.place === previous.place && same(next.work, previous.work) && next.start - previous.end < MERGE_GAP) {
      previous.calls.push(...next.calls); previous.couriers.push(...next.couriers); previous.end = next.end;
      activities.splice(i, 1);
    }
    i--;
  }
  return activities;
}

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

// The furniture spot an agent uses for a tool: the building's station that
// matches the tool name, then a stable pick per agent among its spots.
export function isoWorkSpot(place: IsoPlace, agentId: string, tool?: string): Point {
  const spots = stationSpots(place.interior.spots, tool);
  if (!spots.length) return center(place.doorIn);
  const spot = spots[seedFor(agentId + place.id) % spots.length];
  return { x: spot.x, y: spot.y };
}

export function isoTrack(agentId: string, events: (TrackEvent & { target?: string })[], layout: IsoLayout<any>, regionIndex: number) {
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
  const couriers: Courier[] = [];
  const inside = (pose: IsoPose): IsoPose => ({ ...pose, inside: placeAt(layout, pose.x, pose.y)?.id });
  const at = (time: number): IsoPose => {
    const visit = [...visits].reverse().find(v => v.start <= time);
    if (!visit || time >= visit.end) return inside(stroll(time));
    if (time >= visit.entered && time < visit.leaving) return { ...visit.work, back: true, flip: false, walking: false, inside: visit.place.id };
    if (time < visit.entered) return { ...inside(sampleIsoRoute(visit.inbound, (time - visit.start) / (visit.entered - visit.start))), pace: visit.pace };
    return inside(sampleIsoRoute(visit.outbound, (time - visit.leaving) / (visit.end - visit.leaving)));
  };

  const places = new Map(layout.places.filter(place => place.region === regionIndex).map(place => [place.id, place]));
  const calls: Call[] = events
    .filter(event => event.agent_id === agentId && event.kind === "tool" && event.target_id && places.has(event.target_id) && Number.isFinite(Date.parse(event.time)))
    .sort((a, b) => Date.parse(a.time) - Date.parse(b.time) || a.id.localeCompare(b.id))
    .map(event => { const place = places.get(event.target_id!)!; return { time: Date.parse(event.time), place, work: isoWorkSpot(place, agentId, event.target), tool: event.target }; });
  const activities = groupCalls(calls);
  let free = -Infinity, skipped = 0;
  for (const [i, activity] of activities.entries()) {
    const begin = Math.max(activity.start, free), lag = begin - activity.start, next = activities[i + 1];
    // Far behind and the next activity has already started: let this one go.
    if (lag > DASH_LAG && next && next.start <= begin) { skipped++; continue; }
    const pace: Pace = lag > DASH_LAG ? "dash" : lag > SPRINT_LAG ? "sprint" : "walk";
    const inbound = route(at(begin), activity.work);
    const entered = begin + (length(inbound) / (hurry * PACE[pace])) * 1000;
    const dwell = pace === "walk" ? Math.max(3500, activity.end - activity.start + 1500) + fraction(seed, 21 + i) * 2000 : pace === "sprint" ? 2500 : 1500;
    const leaving = entered + dwell;
    for (const call of activity.couriers) couriers.push({ time: Math.max(entered, call.time + lag), place: call.place, tool: call.tool });
    // Walk back out and converge on the stroll without teleporting.
    let end = leaving + 10000, outbound: Point[] = [];
    for (let k = 0; k < 24; k++) {
      outbound = route(activity.work, stroll(end));
      const value = leaving + (length(outbound) / hurry) * 1000;
      if (Math.abs(value - end) < 0.01) { end = value; break; }
      end = value;
    }
    // Aim at exactly where the stroll is at `end`, even if the estimate has not settled.
    outbound = route(activity.work, stroll(end));
    visits.push({ start: begin, entered, leaving, end, place: activity.place, work: activity.work, inbound, outbound, pace, calls: activity.calls.length, skipped });
    skipped = 0;
    free = leaving;
  }
  return { at, visits, stroll, couriers };
}
