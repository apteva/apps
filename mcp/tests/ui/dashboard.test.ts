import { describe, expect, test } from "bun:test";
import { dashboardURL, duration, relativeTime, testsPageLink, testsPageRoute, widgetPreferences } from "./dashboard";
describe("Tests dashboard host contract", () => {
 test("uses scoped install routing, settings and size without losing filters", () => {
  const props={projectId:"project /a",installId:6,appName:"tests",widgetSize:"half" as const};
  const prefs=widgetPreferences({window:"7d",suite_id:42,environment:"prod & stage",max_results:8,group_repeated:false});
  const url=new URL(dashboardURL(props,"failures",prefs),"http://localhost");
  expect(Object.fromEntries(url.searchParams)).toEqual({project_id:"project /a",install_id:"6",hours:"168",limit:"3",group:"false",suite_id:"42",environment:"prod & stage"});
  expect(new URL(dashboardURL({...props,widgetSize:"full"},"overview",prefs),"http://localhost").searchParams.get("limit")).toBe("8");
 });
 test("links directly to the saved run and ignores foreign routing scope", () => {
  const link=testsPageLink({projectId:"one",installId:7},{id:123,suite_id:9});
  expect(link).toBe("/apps/tests/page?project_id=one&install_id=7&suite_id=9&run_id=123");
  expect(testsPageRoute(link.split("?")[1])).toEqual({suite:9,run:123});
  expect(testsPageRoute("?suite_id=-1&run_id=not-a-number&project_id=foreign")).toEqual({suite:0,run:0});
 });
 test("normalizes settings and missing values without claiming success", () => {
  expect(widgetPreferences()).toEqual({hours:24,suite:0,environment:"",limit:8,group:true});
  expect(widgetPreferences({window:"unknown",suite_id:NaN,max_results:500}).limit).toBe(10);
  expect(widgetPreferences({max_results:0}).limit).toBe(3);
  expect(relativeTime("invalid")).toBe("Unknown time");
  expect(relativeTime("2026-11-13T12:00:00Z",Date.parse("2026-11-13T11:00:00Z"))).toBe("just now");
  expect(duration(null)).toBe("");expect(duration(1250)).toBe("1.3s");expect(duration(61000)).toBe("1m 1s");
 });
});
