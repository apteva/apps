import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import CampaignBuilder, {
  commaIds,
  demandCampaignInput,
} from "./CampaignBuilder";
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
function seed() {
  return {
    name: "Channel growth",
    goal: "channel_subscriptions",
    budget: "5",
    targetCpa: "",
    targetRoas: "",
    budgetType: "daily",
    start: "",
    end: "",
    locations: "2724",
    languages: "1000",
    audience: "",
    placements: ["youtube_in_feed"],
    format: "responsive_video",
    business: "Example",
    headline: "Explore our channel",
    longHeadline: "Discover more videos on our channel",
    description: "Watch our latest videos",
    finalUrl: "https://www.youtube.com/@example",
    video: "1",
    videoUrl: "",
    logo: "2",
    logoUrl: "",
    image: "",
    square: "",
    cards: "",
    action: "",
    key: "stable",
    campaign: "",
    group: "",
    ad: "",
  };
}
async function render(callTool: any, scope = "install:project") {
  const node = document.createElement("div");
  document.body.append(node);
  const root = createRoot(node);
  roots.push(root);
  await act(async () =>
    root.render(
      <CampaignBuilder
        account={{ id: 1, platform: "google", currency: "EUR" }}
        scopeKey={scope}
        callTool={callTool}
        dateFrom="2026-10-01"
        dateTo="2026-10-02"
        onChanged={() => {}}
      />,
    ),
  );
  return { root, node };
}
async function click(node: HTMLElement, text: string) {
  await act(async () => {
    const b = [...node.querySelectorAll("button")].find(
      (b) => b.textContent === text,
    );
    if (!b) throw new Error("Missing " + text);
    b.click();
  });
}
function fixture() {
  const calls: { tool: string; args: any }[] = [];
  let fail = false;
  const call = async (tool: string, args: any) => {
    calls.push({ tool, args });
    if (tool === "account_context_get")
      return {
        capabilities: {
          demand_gen: {
            supported: true,
            ad_formats: ["responsive_video", "responsive_image", "carousel"],
          },
        },
      };
    if (tool === "conversion_event_list") return { events: [] };
    if (tool === "campaign_create") return { id: "77" };
    if (tool === "adset_create") return { id: "88" };
    if (tool === "ad_create") {
      if (fail) {
        fail = false;
        throw new Error("Provider outcome unknown");
      }
      return { id: "99" };
    }
    if (tool === "conversion_readiness_get")
      return { ready: false, status: "unknown", reason: "Verify channel link" };
    return { events: [] };
  };
  return {
    calls,
    call,
    failNext: () => {
      fail = true;
    },
  };
}
test("Demand Gen validates budgets and uses structured campaign inputs", () => {
  expect(commaIds("2724, 2840\n1000")).toEqual(["2724", "2840", "1000"]);
  const input = demandCampaignInput(1, seed());
  expect(input).toMatchObject({
    channel_type: "demand_gen",
    objective: "engagement",
    optimization_goal: "channel_subscriptions",
    daily_budget_cents: 500,
    status: "PAUSED",
    locations: ["2724"],
  });
  expect("platform_options" in input).toBe(false);
  expect(() => demandCampaignInput(1, { ...seed(), budget: "0" })).toThrow();
  expect(() =>
    demandCampaignInput(1, { ...seed(), budgetType: "lifetime" }),
  ).toThrow();
});
test("Creates the existing hierarchy paused and requires explicit linking acknowledgement", async () => {
  sessionStorage.setItem(
    "ads:campaign:install:project:1",
    JSON.stringify(seed()),
  );
  const f = fixture();
  const { node, root } = await render(f.call);
  await click(node, "Create paused campaign");
  expect(
    f.calls
      .filter((c) =>
        ["campaign_create", "adset_create", "ad_create"].includes(c.tool),
      )
      .map((c) => c.args.status),
  ).toEqual(["PAUSED", "PAUSED", "PAUSED"]);
  const ad = f.calls.find((c) => c.tool === "ad_create")!;
  expect(ad.args).toMatchObject({
    ad_format: "responsive_video",
    video_asset_ids: ["1"],
    logo_asset_ids: ["2"],
  });
  expect(ad.args.platform_options).toBeUndefined();
  const activate = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Activate delivery",
  )!;
  expect(activate.disabled).toBe(true);
  expect(f.calls.some((c) => c.tool === "delivery_activate")).toBe(false);
  await act(async () => root.unmount());
  roots.splice(roots.indexOf(root), 1);
  const remounted = await render(f.call);
  expect(remounted.node.textContent).toContain("Campaign 77");
  expect(
    [...remounted.node.querySelectorAll("button")].find(
      (b) => b.textContent === "Continue paused campaign",
    )!.disabled,
  ).toBe(true);
  expect(f.calls.filter((c) => c.tool === "campaign_create")).toHaveLength(1);
});
test("Retains creation keys after an ambiguous provider response", async () => {
  sessionStorage.setItem(
    "ads:campaign:install:project:1",
    JSON.stringify(seed()),
  );
  const f = fixture();
  f.failNext();
  const { node } = await render(f.call);
  await click(node, "Create paused campaign");
  expect(node.textContent).toContain("Provider outcome unknown");
  await click(node, "Continue paused campaign");
  const creates = f.calls.filter((c) => c.tool === "ad_create");
  expect(creates).toHaveLength(2);
  expect(creates[0].args.idempotency_key).toBe(creates[1].args.idempotency_key);
  expect(f.calls.filter((c) => c.tool === "campaign_create")).toHaveLength(1);
});
test("Drafts remain isolated by install/project/account", async () => {
  sessionStorage.setItem(
    "ads:campaign:install:project:1",
    JSON.stringify({ ...seed(), campaign: "77" }),
  );
  const { node } = await render(fixture().call, "other-install:other-project");
  expect(node.textContent).not.toContain("Campaign 77");
});
