import { describe, expect, test } from "bun:test";
import { aggregatePerformance, percentageChange, previousDateRange } from "./AdsPanel";

describe("ads analytics helpers", () => {
  test("aggregates additive metrics and derives campaign KPIs", () => {
    const summary = aggregatePerformance([
      {
        spend_micros: 10_000_000,
        impressions: 1_000,
        reach: 800,
        clicks: 50,
        link_clicks: 40,
        conversions: 5,
        conversion_value_micros: 30_000_000,
        video_views: 100,
        currency: "EUR",
        timezone: "Europe/Madrid",
      },
      {
        spend_micros: 5_000_000,
        impressions: 500,
        reach: 400,
        clicks: 25,
        link_clicks: 20,
        conversions: 2.5,
        conversion_value_micros: 15_000_000,
        video_views: 50,
        currency: "EUR",
        timezone: "Europe/Madrid",
      },
    ] as any);

    expect(summary.spend_micros).toBe(15_000_000);
    expect(summary.clicks).toBe(75);
    expect(summary.ctr).toBe(5);
    expect(summary.cpc_micros).toBe(200_000);
    expect(summary.cpm_micros).toBe(10_000_000);
    expect(summary.cpa_micros).toBe(2_000_000);
    expect(summary.roas).toBe(3);
  });

  test("builds an equal previous comparison window", () => {
    expect(previousDateRange("2026-07-08", "2026-07-14")).toEqual({
      from: "2026-07-01",
      to: "2026-07-07",
    });
  });

  test("handles comparison baselines without infinities", () => {
    expect(percentageChange(120, 100)).toBe(20);
    expect(percentageChange(0, 0)).toBe(0);
    expect(percentageChange(10, 0)).toBeNull();
  });
});
