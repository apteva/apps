import { expect, test } from "bun:test";
import type { RegionInput } from "../layout";
import { G, buildIsoLayout, canStep, groundAt, isWalkableTile, type IsoLayout } from "./layout";
import { isoTrack, isoWorkSpot } from "./motion";

const NAMES = ["storage", "crm", "tasks", "deploy", "billing", "social", "calendar", "analytics", "messaging", "website", "slack", "notes"];
const places = (n: number, prefix = "app") => Array.from({ length: n }, (_, i) => ({ id: `${prefix}-${i}`, name: NAMES[i % NAMES.length], kind: i % 3 ? "app" : "integration", status: "running", calls: i }));
const region = (id: string, n = 6): RegionInput => ({ id, label: id, places: places(n, id), residents: ["a1", "a2", "a3"] });
const tileIndex = (layout: IsoLayout, x: number, y: number) => y * layout.cols + x;

function checkRoute(layout: IsoLayout, path: { x: number; y: number }[]) {
  for (let i = 1; i < path.length; i++) {
    const a = path[i - 1], b = path[i];
    expect(Math.abs(a.x - b.x) + Math.abs(a.y - b.y)).toBe(1);
    expect(canStep(layout, tileIndex(layout, a.x, a.y), tileIndex(layout, b.x, b.y))).toBe(true);
  }
}

test("layout is deterministic and spaces buildings out at every size", () => {
  for (const n of [1, 8, 20, 30]) {
    const first = buildIsoLayout([region("growth", n)], false), second = buildIsoLayout([region("growth", n)], false);
    expect(Array.from(second.ground)).toEqual(Array.from(first.ground));
    expect(second.props).toEqual(first.props);
    expect(first.places.length).toBe(n);
    for (const [i, a] of first.places.entries()) {
      for (let y = a.y; y < a.y + a.d; y++) for (let x = a.x; x < a.x + a.w; x++) expect(groundAt(first, x, y)).toBe(G.floor);
      expect([G.path, G.plaza]).toContain(groundAt(first, a.doorOut.x, a.doorOut.y));
      // At least three clear tiles between any two buildings.
      for (const b of first.places.slice(i + 1)) expect(a.x + a.w + 3 <= b.x || b.x + b.w + 3 <= a.x || a.y + a.d + 3 <= b.y || b.y + b.d + 3 <= a.y).toBe(true);
    }
  }
});

test("bigger apps get bigger buildings by category", () => {
  const layout = buildIsoLayout([region("growth", 12)], false);
  const tasks = layout.places.find(place => place.name === "tasks")!, crm = layout.places.find(place => place.name === "crm")!;
  expect(tasks.archetype).toBe("hall");
  expect(tasks.w * tasks.d).toBeGreaterThan(crm.w * crm.d);
  expect(new Set(layout.places.map(place => place.archetype)).size).toBeGreaterThan(5);
});

test("buildings are entered only through their door, and every work spot is reachable", () => {
  const layout = buildIsoLayout([region("growth", 8), region("ops", 3), region("sales", 6)], false);
  for (const place of layout.places) {
    const home = layout.regions[place.region];
    const path = layout.route({ x: home.cx, y: home.cy }, place.doorOut);
    expect(path.at(-1)).toEqual(place.doorOut);
    checkRoute(layout, path);
    expect(place.interior.spots.length).toBeGreaterThan(0);
    for (const spot of place.interior.spots) {
      // Spots sit on the floor, on a free tile, inside the building.
      expect(spot.x > place.x && spot.x < place.x + place.w && spot.y > place.y && spot.y < place.y + place.d).toBe(true);
      const tile = { x: Math.floor(spot.x), y: Math.floor(spot.y) };
      expect(isWalkableTile(layout, tile)).toBe(true);
      const inward = layout.route(place.doorOut, tile);
      expect(inward.at(-1)).toEqual(tile);
      checkRoute(layout, inward);
      expect(inward[1]).toEqual(place.doorIn);
    }
  }
});

test("each project gets its own island", () => {
  const layout = buildIsoLayout([region("growth", 6), region("ops", 6), region("sales", 2), region("hr", 1)], false);
  expect(layout.regions.length).toBe(4);
  for (const r of layout.regions) {
    for (const place of layout.places.filter(p => layout.regions[p.region] === r)) {
      expect(place.x >= r.x && place.y >= r.y && place.x + place.w <= r.x + r.size && place.y + place.d <= r.y + r.size).toBe(true);
    }
    // The plot edge is open sea, so islands never touch.
    for (let i = 0; i < r.size; i++) {
      expect(groundAt(layout, r.x + i, r.y)).toBe(G.sea);
      expect(groundAt(layout, r.x, r.y + i)).toBe(G.sea);
    }
  }
});

test("agents walk in through the door to a work spot and back out without jumping", () => {
  const layout = buildIsoLayout([region("growth")], false);
  const place = layout.places[2];
  const t0 = Date.parse("2026-10-30T10:00:00Z");
  const events = [
    { id: "e1", agent_id: "a1", kind: "tool", target_id: place.id, time: new Date(t0).toISOString() },
    { id: "e2", agent_id: "a1", kind: "tool", target_id: layout.places[4].id, time: new Date(t0 + 40000).toISOString() },
  ];
  const track = isoTrack("a1", events, layout, 0), again = isoTrack("a1", events, layout, 0);
  expect(track.visits.length).toBe(2);
  const visit = track.visits[0];
  const working = track.at((visit.entered + visit.leaving) / 2);
  expect(working.inside).toBe(place.id);
  expect({ x: working.x, y: working.y }).toEqual(isoWorkSpot(place, "a1"));
  expect(track.at(track.visits[1].end + 1).inside).toBeUndefined();
  let previous = track.at(t0 - 1000);
  for (let time = t0 - 1000; time < track.visits[1].end + 2000; time += 50) {
    const pose = track.at(time);
    expect(again.at(time)).toEqual(pose);
    expect(Math.abs(pose.x - previous.x) + Math.abs(pose.y - previous.y)).toBeLessThan(0.5);
    // Never stands in a wall: a pose inside a building has crossed its door.
    if (pose.inside && !previous.inside) {
      const entered = layout.places.find(item => item.id === pose.inside)!;
      expect(Math.floor(pose.x) === entered.doorIn.x && Math.floor(pose.y) === entered.doorIn.y).toBe(true);
    }
    previous = pose;
  }
});
