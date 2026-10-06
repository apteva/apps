import { expect, test } from "bun:test";
import { channelPresentation, conversationChannels, sessionFromResponse, whatsappSessionRequiresTemplate, whatsappWindowLabel } from "./channels";

test("transport colors are distinct and always accompanied by labels/icons", () => {
  expect(new Set(Object.values(channelPresentation).map(item => item.color)).size).toBe(3);
  for (const item of Object.values(channelPresentation)) { expect(item.label).toBeTruthy(); expect(item.icon).toBeTruthy(); }
  expect(conversationChannels("whatsapp", ["sms_sent", "sms_received", "whatsapp_received", "email_send_failed", "note"])).toEqual(["whatsapp", "sms"]);
});

test("server-relative window survives clock skew and expires without a refresh", () => {
  const localNow = Date.parse("2030-01-01T00:00:00Z");
  const session = sessionFromResponse({ active: true, checked_at: "2026-10-03T10:00:00Z", expires_at: "2026-10-03T13:00:00Z" }, localNow);
  expect(whatsappSessionRequiresTemplate(session, localNow)).toBe(false);
  expect(whatsappWindowLabel(session, localNow)).toContain("3h 0m");
  expect(whatsappSessionRequiresTemplate(session, localNow + 3 * 3600000)).toBe(true);
  expect(whatsappWindowLabel(session, localNow + 3 * 3600000)).toContain("window closed");
});

test("unknown, checking, errors and malformed expiry cannot allow free-form sends", () => {
  for (const state of ["idle", "checking", "error"] as const) expect(whatsappSessionRequiresTemplate({ state })).toBe(true);
  expect(whatsappSessionRequiresTemplate(sessionFromResponse({ active: true }))).toBe(true);
  expect(whatsappWindowLabel({ state: "error" })).toContain("Unable to check");
  expect(whatsappWindowLabel({ state: "closed" })).toContain("window closed");
});
