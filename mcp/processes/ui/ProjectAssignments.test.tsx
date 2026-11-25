import { expect, test } from "bun:test";
import { eventTitle, scheduleTitle } from "./ProjectAssignments";

test("eventTitle turns topics into readable names", () => {
  expect(eventTitle("conversation.message.received")).toBe("Conversation message received");
  expect(eventTitle("contact.channel.deliverability_changed")).toBe("Contact channel deliverability changed");
  expect(eventTitle("opportunity.*")).toBe("Any opportunity event");
});

test("scheduleTitle describes common cron and interval schedules", () => {
  const cron = (c: string) => scheduleTitle({ kind: "cron", cron: c, timezone: "Europe/Madrid" });
  expect(cron("0 9 * * 1")).toBe("Every Monday at 09:00");
  expect(cron("30 9 * * *")).toBe("Every day at 09:30");
  expect(cron("0 18 * * 1-5")).toBe("Every weekday at 18:00");
  expect(cron("15 9-23 * * *")).toBe("Hourly at :15, 09:15–23:15");
  expect(cron("0 8 1 * *")).toBe("Monthly on day 1 at 08:00");
  expect(cron("*/5 * * * *")).toBe("*/5 * * * *");
  expect(scheduleTitle({ kind: "interval", every: "2h" })).toBe("Every 2 hours");
  expect(scheduleTitle({ kind: "interval", every: "1d" })).toBe("Every day");
});
