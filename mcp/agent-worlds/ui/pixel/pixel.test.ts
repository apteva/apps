import { expect, test } from "bun:test";
import { categoryFor } from "./categories";
import { TILE, fitText, text, textWidth } from "./core";
import { buildLayout, isWalkable, type RegionInput } from "./layout";
import { agentTrack } from "./motion";
import { MAX_PLACES, placesFor, regionsFor } from "./PixelStage";

const places = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `app-${i}`, name: ["storage", "crm", "tasks", "deploy", "billing", "social"][i % 6], kind: i % 3 ? "app" : "integration", status: "running", calls: i }));
const region = (id: string, n = 6): RegionInput => ({ id, label: id, places: places(n), residents: ["a1", "a2", "a3"] });

test("layout is deterministic and every door is reachable from the plaza", () => {
  for (const inputs of [[region("growth")], [region("growth"), region("ops", 3), region("sales", 6)]]) {
    const first = buildLayout(inputs, false), second = buildLayout(inputs, false);
    expect(Array.from(second.ground)).toEqual(Array.from(first.ground));
    expect(second.places.map(p => [p.id, p.x, p.y])).toEqual(first.places.map(p => [p.id, p.x, p.y]));
    expect(first.places.length).toBe(inputs.reduce((sum, r) => sum + r.places.length, 0));
    for (const place of first.places) {
      const r = first.regions[place.region];
      expect(isWalkable(first, place.door)).toBe(true);
      const path = first.route({ x: r.plaza.x + 1, y: r.plaza.y + 1 }, place.door);
      expect(path.length).toBeGreaterThan(0);
      expect(path.at(-1)).toEqual(place.door);
    }
  }
});

test("places never overlap and stay inside their region", () => {
  const layout = buildLayout([region("growth", 12)], true);
  expect(layout.places.length).toBe(12);
  for (const [i, a] of layout.places.entries()) {
    const r = layout.regions[a.region];
    expect(a.x >= r.x && a.y >= r.y && a.x + a.w <= r.x + r.w && a.y + a.h <= r.y + r.h).toBe(true);
    for (const b of layout.places.slice(i + 1)) expect(a.x + a.w <= b.x || b.x + b.w <= a.x || a.y + a.h <= b.y || b.y + b.h <= a.y).toBe(true);
  }
});

test("agents walk into a place for a tool call and back out without jumping", () => {
  const layout = buildLayout([region("growth")], false);
  const place = layout.places[0];
  const t0 = Date.parse("2026-10-30T10:00:00Z");
  const events = [{ id: "e1", agent_id: "a1", kind: "tool", target_id: place.id, time: new Date(t0).toISOString() }];
  const track = agentTrack("a1", events, layout, 0);
  expect(track.visits.length).toBe(1);
  const visit = track.visits[0];
  expect(track.at((visit.entered + visit.leaving) / 2).inside).toBe(place.id);
  expect(track.at(visit.end + 1).inside).toBeUndefined();
  // Sampled every 50ms the agent never moves more than a few pixels.
  let previous = track.at(t0 - 1000);
  for (let time = t0 - 1000; time < visit.end + 2000; time += 50) {
    const pose = track.at(time);
    expect(Math.abs(pose.x - previous.x) + Math.abs(pose.y - previous.y)).toBeLessThan(TILE);
    previous = pose;
  }
});

test("villages cap and rank places by use, then keep them in stable order", () => {
  const many = Array.from({ length: 20 }, (_, i) => ({ id: `d${String(i).padStart(2, "0")}`, name: `App ${i}`, kind: "app", status: "running", call_count: i, project_id: "p" })) as any[];
  const chosen = placesFor(many, "d00");
  expect(chosen.length).toBe(MAX_PLACES);
  expect(chosen.map(p => p.id)).toContain("d00");
  expect(chosen.map(p => p.id)).toContain("d19");
  expect(chosen.map(p => p.id)).toEqual([...chosen.map(p => p.id)].sort());
  const nodes = [{ kind: "project", id: "p", name: "Growth", focus: { kind: "project", id: "p" }, agents: [{ id: "a" }] }] as any[];
  expect(regionsFor([], nodes, many, "")[0].places.length).toBe(6);
});

test("categories and pixel text", () => {
  expect(categoryFor("Storage", "app")).toBe("storage");
  expect(categoryFor("Gmail", "integration")).toBe("mail");
  expect(categoryFor("Zorblax", "integration")).toBe("link");
  expect(categoryFor("Zorblax", "app")).toBe("other");
  expect(textWidth("ABC")).toBe(11);
  const fitted = fitText("A very long destination name", 40);
  expect(textWidth(fitted)).toBeLessThanOrEqual(40);
  expect(fitted.endsWith(".")).toBe(true);
});

test("label punctuation has glyphs", () => {
  const ctx = { fillStyle: "", calls: 0, fillRect() { this.calls++; } } as any;
  text(ctx, "·", 0, 0, "#fff");
  expect(ctx.calls).toBe(1);
});
