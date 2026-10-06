import { expect, test } from "bun:test";
import { hasLogErrors, telemetryQuery } from "./telemetry";

test("partial GraphQL errors are errors even with HTTP 200, including legacy logs", () => {
  expect(hasLogErrors({ status_code: 200, error: "Source failed", error_codes: [] })).toBe(true);
  expect(hasLogErrors({ status_code: 200, error_codes: ["execution_timeout"] })).toBe(true);
  expect(hasLogErrors({ status_code: 200, errors: [{ message: "Field failed" }] })).toBe(true);
  expect(hasLogErrors({ status_code: 403 })).toBe(true);
  expect(hasLogErrors({ status_code: 200, error: "", errors: [], error_codes: [] })).toBe(false);
});

test("rolling window advances on refresh and preserves success-only and scoped filters", () => {
  const filters = { range: "120", has_errors: "false", environment: "production", search: "req-42", limit: "10", sort_by: "duration_ms", sort_order: "desc" };
  const query = telemetryQuery(filters, 500, new Date("2026-10-06T15:00:00+02:00"));
  expect(query.get("since")).toBe("2026-10-06T11:00:00.000Z");
  expect(query.get("has_errors")).toBe("false");
  expect(query.get("environment")).toBe("production");
  expect(query.get("search")).toBe("req-42");
  expect(query.get("slow_threshold_ms")).toBe("500");
  expect(query.has("range")).toBe(false);
  expect(telemetryQuery(filters, 500, new Date("2026-10-06T14:00:00Z")).get("since")).toBe("2026-10-06T12:00:00.000Z");
});

test("all-time does not send hidden custom dates; custom bounds use UTC", () => {
  expect(telemetryQuery({ range: "all", since: "2026-10-01T12:00" }, 1000).has("since")).toBe(false);
  const query = telemetryQuery({ range: "custom", since: "2026-10-06T14:00:00+02:00", until: "2026-10-06T16:00:00+02:00" }, 1000);
  expect(query.get("since")).toBe("2026-10-06T12:00:00.000Z");
  expect(query.get("until")).toBe("2026-10-06T14:00:00.000Z");
});

test("runtime filters retain false sharing values and queue/backend sorts", () => {
  const query = telemetryQuery({ range: "all", coalesced: "false", min_queue_ms: "10", min_backend_reads: "2", sort_by: "queue_ms" }, 1000);
  expect(query.get("coalesced")).toBe("false");
  expect(query.get("min_queue_ms")).toBe("10");
  expect(query.get("min_backend_reads")).toBe("2");
  expect(query.get("sort_by")).toBe("queue_ms");
});
