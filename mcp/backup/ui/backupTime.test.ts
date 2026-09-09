import { expect, test } from "bun:test";
import { durationOf } from "./backupTime";

test("legacy UTC finish timestamps retain actual duration outside UTC", () => {
  expect(durationOf({ started_at: "2026-09-09T10:00:00Z", finished_at: "2026-09-09 10:00:10", status: "success" })).toBe("10.0 s");
});
test("new timestamps and long durations remain accurate", () => {
  expect(durationOf({ started_at: "2026-09-09T10:00:00Z", finished_at: "2026-09-09T12:00:00.000Z", status: "success" })).toBe("2.0 h");
});
