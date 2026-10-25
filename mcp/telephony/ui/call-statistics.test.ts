import { expect, test } from "bun:test";
import {
  statisticsURL,
  defaultStatisticsFilters,
  percent,
  comparison,
} from "./call-statistics";
const host = { projectId: "p&a", installId: 42 },
  now = Date.parse("2026-10-25T14:00:00Z");
test("statistics URL carries scope, custom period and stable chart selection", () => {
  const f = {
    ...defaultStatisticsFilters,
    range: "custom" as const,
    from: "2026-10-25T00:00:00Z",
    until: "2026-10-26T00:00:00Z",
    provider: "twilio",
    threshold: "1000",
  };
  const u = new URL(
    statisticsURL(
      host,
      f,
      {
        from: "2026-10-25T08:00:00Z",
        until: "2026-10-25T09:00:00Z",
        anchor: now,
      },
      "problems",
      "cursor",
      now,
      "Europe/Madrid",
    ),
    "http://test",
  );
  expect(u.searchParams.get("view")).toBe("statistics");
  expect(u.searchParams.get("project_id")).toBe("p&a");
  expect(u.searchParams.get("threshold_ms")).toBe("1000");
  expect(u.searchParams.get("segment_from")).toBe("2026-10-25T08:00:00Z");
  expect(u.searchParams.get("focus")).toBe("problems");
  expect(u.searchParams.get("timezone")).toBe("Europe/Madrid");
});
test("chart selection freezes relative cohort while other requests advance", () => {
  const sel = {
    from: "2026-10-25T08:00:00Z",
    until: "2026-10-25T09:00:00Z",
    anchor: now,
  };
  expect(
    statisticsURL(host, defaultStatisticsFilters, sel, "", "", now, "UTC"),
  ).toBe(
    statisticsURL(
      host,
      defaultStatisticsFilters,
      sel,
      "",
      "",
      now + 90000,
      "UTC",
    ),
  );
  expect(
    statisticsURL(host, defaultStatisticsFilters, null, "", "", now, "UTC"),
  ).not.toBe(
    statisticsURL(
      host,
      defaultStatisticsFilters,
      null,
      "",
      "",
      now + 90000,
      "UTC",
    ),
  );
});
test("unknown rates and zero baselines never fabricate percentages", () => {
  expect(percent(null)).toBe("—");
  expect(percent(0)).toBe("0.0%");
  expect(comparison(10, null, true)).toBe("No comparable observations");
  expect(comparison(10, 0)).toBe("No previous baseline");
  expect(comparison(10, 20, true)).toBe("-10.0 pp vs previous");
});
test("invalid/reversed/overlong ranges rejected before requesting", () => {
  for (const [from, until] of [
    ["bad", "bad"],
    ["2026-10-25", "2026-10-24"],
    ["2026-01-01", "2026-10-25"],
  ])
    expect(() =>
      statisticsURL(
        host,
        { ...defaultStatisticsFilters, range: "custom", from, until },
        null,
        "",
        "",
        now,
      ),
    ).toThrow();
});
