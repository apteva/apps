import { expect, test } from "bun:test";
import {
  ACCOUNT_METRICS_STALE_MS,
  accountMetricsNeedRefresh,
  metricTrendEntries,
} from "./metricsPresentation";

test("account metrics refresh when missing or stale", () => {
  const now = Date.parse("2026-07-17T10:00:00Z");
  expect(accountMetricsNeedRefresh({ status: "ok" }, now)).toBe(true);
  expect(accountMetricsNeedRefresh({ status: "ok", updated_at: new Date(now-ACCOUNT_METRICS_STALE_MS+1).toISOString() }, now)).toBe(false);
  expect(accountMetricsNeedRefresh({ status: "ok", updated_at: new Date(now-ACCOUNT_METRICS_STALE_MS).toISOString() }, now)).toBe(true);
  expect(accountMetricsNeedRefresh({ status: "failed" }, now)).toBe(false);
});

test("trend entries exclude duplicate totals, heartbeats, zero-only and single-point series", () => {
  const entries = metricTrendEntries({
    followers: [{ value: 10 }, { value: 11 }],
    comments: [{ value: 0 }, { value: 0 }],
    impressions_total: [{ value: 139 }],
    total_likes: [{ value: 2 }, { value: 3 }],
    _refresh: [{ value: 1 }, { value: 1 }],
    reach: [{ value: 82 }],
    likes: [{ value: 0 }, { value: 2 }],
  });
  expect(entries.map((entry) => entry.name)).toEqual(["followers", "likes"]);
});
