import { expect, test } from "bun:test";
import type { RegionInput } from "../layout";
import { buildIsoLayout } from "./layout";
import { isoTrack, isoWorkSpot } from "./motion";
import { describeTool, stationSpots } from "./stations";

test("tool names read as plain actions", () => {
  expect(describeTool("contacts_search")).toBe("searching contacts");
  expect(describeTool("opportunities_update")).toBe("updating opportunities");
  expect(describeTool("contacts_reply")).toBe("replying to contacts");
  expect(describeTool("contacts_set_conversation_status")).toBe("updating conversation status");
  expect(describeTool("conversation_drafts_send")).toBe("sending conversation drafts");
  expect(describeTool("crm_contacts_search", "CRM")).toBe("searching contacts");
  expect(describeTool("conversations_inbox")).toBe("using conversations inbox");
  expect(describeTool("reply")).toBe("replying");
});

const crm: RegionInput = { id: "growth", label: "Growth", residents: ["a1"], places: [{ id: "crm", name: "CRM", kind: "app", status: "running", calls: 1 }] };

test("CRM tools send agents to the matching station", () => {
  const place = buildIsoLayout([crm], false).places[0];
  expect(place.archetype).toBe("office");
  const station = (tool: string) => new Set(stationSpots(place.interior.spots, tool).map(spot => spot.tags![0]));
  for (const tool of ["contacts_search", "contacts_verify_email", "lists_add_contact", "segments_eval"]) expect(station(tool)).toEqual(new Set(["contact"]));
  for (const tool of ["opportunities_search", "opportunities_update", "pipeline_stage_create", "routing_rules_list"]) expect(station(tool)).toEqual(new Set(["opportunity"]));
  for (const tool of ["conversations_inbox", "contacts_reply", "conversation_drafts_send", "messaging_senders_list", "contacts_send_message"]) expect(station(tool)).toEqual(new Set(["conversation"]));
  // Unknown tools may use any spot.
  expect(stationSpots(place.interior.spots, "crm_ping").length).toBe(place.interior.spots.length);
});

test("an agent works at the station for its tool, then moves for a different one", () => {
  const layout = buildIsoLayout([crm], false), place = layout.places[0];
  const t0 = Date.parse("2026-10-30T10:00:00Z");
  const events = [
    { id: "e1", agent_id: "a1", kind: "tool", target: "contacts_search", target_id: "crm", time: new Date(t0).toISOString() },
    { id: "e2", agent_id: "a1", kind: "tool", target: "opportunities_update", target_id: "crm", time: new Date(t0 + 30000).toISOString() },
  ];
  const track = isoTrack("a1", events, layout, 0);
  expect(track.visits.length).toBe(2);
  const [first, second] = track.visits;
  expect(first.work).toEqual(isoWorkSpot(place, "a1", "contacts_search"));
  expect(second.work).toEqual(isoWorkSpot(place, "a1", "opportunities_update"));
  expect(first.work).not.toEqual(second.work);
  expect(track.at((second.entered + second.leaving) / 2).inside).toBe("crm");
});
