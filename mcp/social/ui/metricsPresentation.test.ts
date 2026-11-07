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

test("trend entries exclude duplicate totals, heartbeats, single-point series but retain confirmed zero series", () => {
  const entries = metricTrendEntries({
    followers: [{ value: 10 }, { value: 11 }],
    comments: [{ value: 0 }, { value: 0 }],
    impressions_total: [{ value: 139 }],
    total_likes: [{ value: 2 }, { value: 3 }],
    _refresh: [{ value: 1 }, { value: 1 }],
    reach: [{ value: 82 }],
    likes: [{ value: 0 }, { value: 2 }],
  });
  expect(entries.map((entry) => entry.name)).toEqual(["followers", "comments", "likes"]);
});

test("pending and missing chart values remain gaps while ready zero stays zero", async () => {
  const {metricChartValue,metricDateRanges}=await import("./metricsPresentation");
  expect(metricChartValue({value:0,data_status:"ready"})).toBe(0);
  expect(metricChartValue({value:0,data_status:"pending"})).toBeNull();
  expect(metricChartValue({value:null})).toBeNull();
  expect(metricChartValue({value:0,data_status:"unavailable"})).toBeNull();
  expect(metricChartValue({value:4,data_status:"estimated"})).toBe(4);
  expect(metricDateRanges(["2026-10-25","2026-10-26","2026-10-29"])).toContain("2026");
  expect(metricTrendEntries({clicks:[{time:"2026-10-25",value:null,data_status:"pending"},{time:"2026-10-26",value:null,data_status:"pending"}]})).toHaveLength(1);
});
