import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import ConversionsWorkspace, {
  createdId,
  metricDisplay,
  mobileCampaignInput,
  storeIdentifier,
} from "./ConversionsWorkspace";
const browser = new Window();
Object.assign(globalThis, {
  window: browser,
  document: browser.document,
  HTMLElement: browser.HTMLElement,
  Event: browser.Event,
  sessionStorage: browser.sessionStorage,
  IS_REACT_ACT_ENVIRONMENT: true,
});
const { createRoot } = await import("react-dom/client");
const roots: ReturnType<typeof createRoot>[] = [];
afterEach(async () => {
  for (const root of roots.splice(0)) await act(async () => root.unmount());
  document.body.innerHTML = "";
  sessionStorage.clear();
});
async function render(callTool: any, platform = "google") {
  const node = document.createElement("div");
  document.body.append(node);
  const root = createRoot(node);
  roots.push(root);
  await act(async () =>
    root.render(
      <ConversionsWorkspace
        scopeKey="install:project"
        account={{ id: 1, platform, currency: "USD" }}
        dateFrom="2026-10-01"
        dateTo="2026-10-02"
        callTool={callTool}
      />,
    ),
  );
  return { node, root };
}
async function click(node: HTMLElement, text: string) {
  await act(async () => {
    const button = [...node.querySelectorAll("button")].find(
      (b) => b.textContent === text,
    );
    if (!button) throw new Error(`Missing ${text}`);
    button.click();
  });
}
function draft(platform = "google") {
  return {
    appId: 2,
    sourceId: 3,
    eventId: 0,
    goal: "installs",
    campaignName: "Example installs",
    budget: "10",
    country: "US",
    locationId: "2840",
    headline: "Install Example",
    description: "Use Example today",
    asset: platform === "meta" ? "hash123" : "",
    build: { key: "stable-draft" },
  };
}
function fixture(platform = "google") {
  const calls: { tool: string; args: any }[] = [];
  const callTool = async (tool: string, args: any) => {
    calls.push({ tool, args });
    switch (tool) {
      case "conversion_capabilities_get":
        return {
          goals: ["installs", "in_app_event"],
          measurement_sources: [
            platform === "google" ? "firebase" : "meta_sdk",
          ],
        };
      case "resource_list":
        return {
          data:
            args.kind === "mobile_app"
              ? [
                  {
                    id: 2,
                    name: "Example",
                    status: "active",
                    metadata: { os: "android" },
                  },
                ]
              : [
                  {
                    id: 3,
                    name: "Measurement",
                    status: "active",
                    metadata: { mobile_app_resource_id: 2 },
                  },
                ],
        };
      case "conversion_performance_get":
        return {
          events: [
            {
              event_id: "install",
              event: "install",
              measurement_source: "firebase",
              attribution_window: "provider_default",
              conversions: 5,
              cpi_micros: 2000000,
              value_micros: null,
            },
          ],
          freshness: { fetched_at: "2026-10-03T00:00:00Z" },
        };
      case "campaign_create":
        return { id: "11" };
      case "adset_create":
        return { id: "12" };
      case "ad_create":
        return { id: "13" };
      case "creative_create":
        return { id: "14" };
      case "conversion_event_list":
        return { data: [] };
      case "conversion_readiness_get":
        return {
          configured: true,
          app_access: "unknown",
          eligible_for_optimization: "unknown",
          ready: false,
          event_observed: "unknown",
        };
      default:
        throw new Error(`Unexpected tool ${tool}`);
    }
  };
  return { calls, callTool };
}

test("validates store app identifiers and preserves missing metrics", () => {
  expect(
    storeIdentifier(
      "android",
      "https://play.google.com/store/apps/details?id=com.example.app",
    ),
  ).toBe("com.example.app");
  expect(
    storeIdentifier("ios", "https://apps.apple.com/us/app/example/id12345"),
  ).toBe("12345");
  expect(
    storeIdentifier(
      "android",
      "https://play.google.com.evil.test/store/apps/details?id=com.example.app",
    ),
  ).toBe("");
  expect(metricDisplay(null, "USD", true)).toBe("Unavailable");
  expect(metricDisplay(0, "USD")).toBe("0");
  expect(() =>
    mobileCampaignInput(1, 2, 3, 0, "value", "Value", "10", "key"),
  ).toThrow("eligible");
  expect(() => createdId({})).toThrow("Reconcile");
});

test("Google mobile flow creates paused entities and resumes its draft on remount", async () => {
  sessionStorage.setItem(
    "ads:mobile:v1:install:project:1",
    JSON.stringify(draft()),
  );
  const { calls, callTool } = fixture();
  const { node, root } = await render(callTool);
  await click(node, "Create paused campaign");
  await click(node, "Create paused ad group");
  await click(node, "Create paused ad");
  const campaign = calls.find((c) => c.tool === "campaign_create")!.args;
  expect(campaign).toMatchObject({
    mobile_app_resource_id: 2,
    measurement_source_resource_id: 3,
    status: "PAUSED",
    daily_budget_cents: 1000,
    idempotency_key: "stable-draft:campaign",
    locations: ["2840"],
  });
  const ad = calls.find((c) => c.tool === "ad_create")!.args;
  expect(ad).toMatchObject({
    ad_format: "app",
    adset_id: "12",
    headlines: ["Install Example"],
    idempotency_key: "stable-draft:ad",
  });
  expect(
    [...node.querySelectorAll("button")].find(
      (b) => b.textContent === "Activate campaign and ad",
    )?.disabled,
  ).toBe(true);
  expect(node.textContent).toContain("Unavailable");
  expect(node.textContent).toContain("provider_default");
  await act(async () => root.unmount());
  roots.splice(roots.indexOf(root), 1);
  const restored = await render(callTool);
  expect(restored.node.textContent).toContain(
    "Campaign 11 · Ad group 12 · Ad 13",
  );
  expect(calls.filter((c) => c.tool === "campaign_create")).toHaveLength(1);
});

test("Meta inherits campaign budget and freezes measurement after creation", async () => {
  sessionStorage.setItem(
    "ads:mobile:v1:install:project:1",
    JSON.stringify(draft("meta")),
  );
  const { calls, callTool } = fixture("meta");
  const { node } = await render(callTool, "meta");
  await click(node, "Create paused campaign");
  await click(node, "Create paused ad group");
  const group = calls.find((c) => c.tool === "adset_create")!.args;
  expect(group.daily_budget_cents).toBeUndefined();
  expect(group.targeting.geo_locations.countries).toEqual(["US"]);
  const sources = [...node.querySelectorAll("select")].find((select) =>
    select.parentElement?.textContent?.startsWith("Measurement source"),
  );
  expect(sources?.disabled).toBe(true);
  await click(node, "Create app creative");
  await click(node, "Create paused ad");
  expect(calls.find((c) => c.tool === "ad_create")!.args.creative_id).toBe(
    "14",
  );
});

test("unknown provider outcomes retain the same request key", async () => {
  sessionStorage.setItem(
    "ads:mobile:v1:install:project:1",
    JSON.stringify(draft()),
  );
  const f = fixture();
  const callTool = async (tool: string, args: any) =>
    tool === "campaign_create"
      ? (f.calls.push({ tool, args }), {})
      : f.callTool(tool, args);
  const { node } = await render(callTool);
  await click(node, "Create paused campaign");
  await click(node, "Create paused campaign");
  expect(node.querySelector('[role="alert"]')?.textContent).toContain(
    "Reconcile",
  );
  const attempts = f.calls.filter((c) => c.tool === "campaign_create");
  expect(attempts[0].args.idempotency_key).toBe(
    attempts[1].args.idempotency_key,
  );
  expect(node.textContent).not.toContain("Campaign undefined");
});

test("cache notifications read the event cache without another live provider sync", async () => {
  sessionStorage.setItem(
    "ads:mobile:v1:install:project:1",
    JSON.stringify(draft()),
  );
  const { calls, callTool } = fixture();
  const { root } = await render(callTool);
  await act(async () =>
    root.render(
      <ConversionsWorkspace
        scopeKey="install:project"
        account={{ id: 1, platform: "google", currency: "USD" }}
        dateFrom="2026-10-01"
        dateTo="2026-10-02"
        callTool={callTool}
        cacheRefreshKey={1}
      />,
    ),
  );
  const reports = calls.filter((c) => c.tool === "conversion_performance_get");
  expect(reports).toHaveLength(2);
  expect(reports[0].args.refresh).toBe(true);
  expect(reports[1].args.refresh).toBe(false);
});
