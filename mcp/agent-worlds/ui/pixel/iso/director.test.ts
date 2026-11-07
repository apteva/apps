import { expect, test } from "bun:test";
import type { RegionInput } from "../layout";
import { buildIsoLayout } from "./layout";
import { DASH_LAG, isoTrack } from "./motion";

const region: RegionInput = {
  id: "growth", label: "Growth", residents: ["ada"],
  places: ["crm", "storage", "tasks", "billing"].map((name, i) => ({ id: name, name, kind: "app", status: "running", calls: i })),
};
const layout = buildIsoLayout([region], false);
const t0 = Date.parse("2026-10-30T10:00:00Z");
const call = (n: number, place: string, seconds: number, target = "contacts_search") => ({ id: `e${String(n).padStart(4, "0")}`, agent_id: "ada", kind: "tool", target, target_id: place, time: new Date(t0 + seconds * 1000).toISOString() });

test("a burst of calls at one station is one visit", () => {
  const events = Array.from({ length: 12 }, (_, i) => call(i, "crm", i * 0.25));
  const track = isoTrack("ada", events, layout, 0);
  expect(track.visits.length).toBe(1);
  expect(track.visits[0].calls).toBe(12);
  // The agent stays for the whole burst.
  expect(track.visits[0].leaving - track.visits[0].entered).toBeGreaterThan(2750);
});

test("a one-off call elsewhere goes by envelope instead of a walk", () => {
  const events = [call(0, "crm", 0), call(1, "crm", 0.4), call(2, "crm", 0.8), call(3, "storage", 1.2, "files_get"), call(4, "crm", 2.0), call(5, "crm", 2.4)];
  const track = isoTrack("ada", events, layout, 0);
  expect(track.visits.map(visit => visit.place.id)).toEqual(["crm"]);
  expect(track.visits[0].calls).toBe(5);
  expect(track.couriers.map(courier => courier.place.id)).toEqual(["storage"]);
  expect(track.couriers[0].time).toBeGreaterThanOrEqual(track.visits[0].entered);
});

test("an agent that falls behind sprints, dashes, skips stale visits, and never teleports", () => {
  const order = ["crm", "storage", "tasks", "billing"];
  const events = Array.from({ length: 40 }, (_, i) => call(i, order[i % 4], i * 3, ["contacts_search", "files_get", "tasks_update", "invoice_create"][i % 4]));
  const track = isoTrack("ada", events, layout, 0), again = isoTrack("ada", events, layout, 0);
  const paces = new Set(track.visits.map(visit => visit.pace));
  expect(paces.has("sprint") || paces.has("dash")).toBe(true);
  expect(track.visits.some(visit => visit.skipped > 0)).toBe(true);
  // Skipping keeps the village close to live instead of drifting further behind.
  const last = track.visits.at(-1)!;
  expect(last.start - Date.parse(events.at(-1)!.time)).toBeLessThan(DASH_LAG + 12000);
  let previous = track.at(t0);
  for (let time = t0; time < last.end + 1000; time += 50) {
    const pose = track.at(time);
    expect(again.at(time)).toEqual(pose);
    expect(Math.abs(pose.x - previous.x) + Math.abs(pose.y - previous.y)).toBeLessThan(1.5);
    previous = pose;
  }
});

test("calls spread out are walked at an easy pace", () => {
  const events = [call(0, "crm", 0), call(1, "storage", 60, "files_get"), call(2, "tasks", 120, "tasks_update")];
  const track = isoTrack("ada", events, layout, 0);
  expect(track.visits.map(visit => visit.pace)).toEqual(["walk", "walk", "walk"]);
  expect(track.couriers.length).toBe(0);
});

test("furniture inside a building always draws after that building's back walls", async () => {
  const { depthKey } = await import("./paint");
  for (const size of [6, 8, 12]) {
    const many = buildIsoLayout([{ ...region, places: Array.from({ length: size }, (_, i) => ({ id: `p${i}`, name: ["storage", "crm", "tasks", "deploy", "billing", "social"][i % 6], kind: "app", status: "running", calls: i })) }], false);
    for (const place of many.places) for (const piece of place.interior.pieces) {
      expect(depthKey(many, piece.foot.x0 + 0.01, piece.foot.y0 + 0.01, piece.foot.x1 + piece.foot.y1 - 0.01)).toBeGreaterThan(place.x + place.y);
    }
  }
});
