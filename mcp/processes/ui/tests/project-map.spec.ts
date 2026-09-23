import {
  flowThemes,
  applyFlowTheme,
  expectBorderContrast,
} from "./flow-themes";
import { expect, test } from "@playwright/test";
const step = (key: string, depends_on: string[] = []) => ({
  key,
  name: key.replaceAll("_", " "),
  role: "coordinator",
  instructions: `Instructions for ${key}`,
  expected_output: "Evidence",
  depends_on,
});
const weatherSteps = [
  step("Get_weather"),
  step("Prepare_report", ["Get_weather"]),
  step("Review_report", ["Prepare_report"]),
];
const processes = [
  {
    id: "weather",
    name: "Hourly weather alerts",
    version: 3,
    status: "active",
    steps: weatherSteps,
    assignments: [{ id: "barcelona", name: "Barcelona" }],
  },
  {
    id: "content",
    name: "Content review and publishing",
    version: 1,
    status: "draft",
    steps: [
      step("Prepare_brief"),
      step("Write_draft", ["Prepare_brief"]),
      step("Check_sources", ["Prepare_brief"]),
      step("Approve_content", ["Write_draft", "Check_sources"]),
    ],
  },
  {
    id: "onboarding",
    name: "Client onboarding",
    version: 1,
    status: "draft",
    steps: [
      step("Collect_requirements"),
      step("Prepare_workspace", ["Collect_requirements"]),
      step("Review_handoff", ["Prepare_workspace"]),
    ],
  },
];
function run(id: string, assignment: string, state: string, version = 3) {
  return {
    id,
    version,
    state,
    backend: "agent",
    assignment: { name: assignment },
    steps: weatherSteps.map((definition, i) => ({
      id: `${id}-${i}`,
      run_id: id,
      key: definition.key,
      definition,
      state: i ? "pending" : state,
      executor: { kind: "agent", agent_id: 7 },
    })),
  };
}
let runData: any;
test.beforeEach(async ({ page }) => {
  runData = {
    direct_runs: [
      run("run-first", "Barcelona", "running"),
      run("run-second", "Madrid", "blocked"),
      run("run-third", "Barcelona", "ready"),
      run("run-old", "Valencia", "waiting", 2),
      {
        id: "run-untracked",
        state: "blocked",
        version: 3,
        assignment: { name: "Lisbon" },
        current_step: "Waiting for a weather source",
      },
    ],
  };
  await page.route("**/fixture/map-processes", (route) =>
    route.fulfill({ json: { processes } }),
  );
  await page.route("**/fixture/map-runs/*", (route) =>
    route.fulfill({
      json: route.request().url().endsWith("weather")
        ? runData
        : { direct_runs: [] },
    }),
  );
  await page.goto("/?map");
  await page.getByRole("button", { name: "Project map", exact: true }).click();
  await expect(page.locator(".pm-summary")).toContainText("3 of 3 SOPs");
});
test("all SOPs and steps share a packed canvas inside the real panel", async ({
  page,
}) => {
  await expect(page.locator(".pm .react-flow")).toHaveCount(1);
  await expect(page.locator(".pm-boundary")).toHaveCount(3);
  await expect(page.locator(".pm-step")).toHaveCount(10);
  const boxes = await page
    .locator(".react-flow__node-sop")
    .evaluateAll((nodes) =>
      nodes.map((n) => {
        const r = n.getBoundingClientRect();
        return { x: r.x, y: r.y, w: r.width, h: r.height };
      }),
    );
  for (let i = 0; i < boxes.length; i++)
    for (let j = i + 1; j < boxes.length; j++) {
      const a = boxes[i],
        b = boxes[j];
      expect(
        a.x + a.w <= b.x ||
          b.x + b.w <= a.x ||
          a.y + a.h <= b.y ||
          b.y + b.h <= a.y,
      ).toBe(true);
    }
  const width =
    Math.max(...boxes.map((b) => b.x + b.w)) -
    Math.min(...boxes.map((b) => b.x));
  const height =
    Math.max(...boxes.map((b) => b.y + b.h)) -
    Math.min(...boxes.map((b) => b.y));
  expect(Math.max(width / height, height / width)).toBeLessThan(2);
  expect(
    await page
      .getByRole("checkbox", { name: "Live only" })
      .evaluate((n) => n.getBoundingClientRect().width),
  ).toBe(15);
  expect(
    await page
      .getByRole("combobox", { name: "Filter SOP status" })
      .evaluate((n) => n.getBoundingClientRect().width),
  ).toBeLessThan(190);
  await page.screenshot({
    path: "/private/tmp/processes-unified-map-desktop.png",
  });
  await page.setViewportSize({ width: 390, height: 900 });
  await page.reload();
  await page.getByRole("button", { name: "Project map", exact: true }).click();
  await expect(page.locator(".pm-step")).toHaveCount(10);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "/private/tmp/processes-unified-map-mobile.png",
  });
});
test("simultaneous runs keep their identity and old versions retain original details", async ({
  page,
}) => {
  await expect(page.locator('.pm-execution[data-run="run-first"]')).toHaveCount(
    3,
  );
  await expect(
    page.locator('.pm-execution[data-run="run-second"]'),
  ).toHaveCount(3);
  await expect(page.locator('.pm-execution[data-run="run-third"]')).toHaveCount(
    3,
  );
  await expect(page.locator('.pm-execution[data-run="run-old"]')).toHaveCount(
    0,
  );
  const represented = await page
    .locator(".pm-execution,.pm-run-card")
    .evaluateAll((nodes) =>
      [...new Set(nodes.map((n) => n.getAttribute("data-run")))].sort(),
    );
  expect(represented).toEqual([
    "run-first",
    "run-old",
    "run-second",
    "run-third",
    "run-untracked",
  ]);
  await expect(page.locator('.pm-run-card[data-run="run-old"]')).toContainText(
    "v2",
  );
  await expect(
    page.locator('.pm-run-card[data-run="run-untracked"]'),
  ).toContainText("Waiting for a weather source");
  await page.locator('.pm-run-card[data-run="run-old"]').click();
  await expect(
    page.getByRole("complementary", { name: "Map details" }),
  ).toContainText("waiting · v2");
  await page.getByRole("button", { name: "Close map details" }).click();
  await page.locator('.pm-execution[data-run="run-second"]').first().click();
  const detail = page.getByRole("complementary", { name: "Map details" });
  await expect(detail).toContainText("Madrid · n-second");
  await expect(detail).toContainText("blocked · Weather agent");
  await expect(detail.getByRole("link", { name: "Open run" })).toHaveAttribute(
    "href",
    /run_id=run-second/,
  );
  await detail.getByRole("button", { name: /Valencia/ }).click();
  await expect(detail).toContainText("waiting · v2");
  await expect(detail.getByRole("link", { name: "Open run" })).toHaveAttribute(
    "href",
    /run_id=run-old/,
  );
});
test("live refresh preserves viewport and exposes partial data failures", async ({
  page,
  request,
}) => {
  await page.locator(".react-flow__controls-zoomin").click();
  const viewport = page.locator(".pm .react-flow__viewport");
  const before = await viewport.getAttribute("style");
  runData.direct_runs[0].steps[0].state = "completed";
  await request.post("/fixture/events", {
    data: {
      app: "processes",
      project_id: "test",
      install_id: 77,
      seq: 900,
      topic: "task.state_changed",
      data: { process_id: "weather", run_id: "run-first" },
    },
  });
  await expect(
    page.locator('.pm-execution[data-run="run-first"]').first(),
  ).toContainText("completed");
  expect(await viewport.getAttribute("style")).toBe(before);
  await page.route("**/fixture/map-runs/content", (route) =>
    route.fulfill({ status: 503, body: "Unavailable" }),
  );
  await page.getByRole("button", { name: "Refresh map", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Content review and publishing: Could not load map (503)",
  );
  await expect(page.locator(".pm-boundary")).toHaveCount(3);
  expect(await viewport.getAttribute("style")).toBe(before);
});

test("a late cancelled refresh cannot overwrite a newer event", async ({
  page,
  request,
}) => {
  let first = true;
  let release: () => void = () => {};
  let markStarted: () => void = () => {};
  const started = new Promise<void>((resolve) => {
    markStarted = resolve;
  });
  await page.route("**/fixture/map-runs/weather", async (route) => {
    if (first) {
      first = false;
      const stale = structuredClone(runData);
      markStarted();
      await new Promise<void>((resolve) => {
        release = resolve;
      });
      await route.fulfill({ json: stale }).catch(() => {});
    } else await route.fulfill({ json: runData });
  });
  await page.getByRole("button", { name: "Refresh map", exact: true }).click();
  await started;
  runData.direct_runs[0].steps[0].state = "completed";
  await request.post("/fixture/events", {
    data: {
      app: "processes",
      project_id: "test",
      install_id: 77,
      seq: 901,
      topic: "task.state_changed",
      data: { process_id: "weather" },
    },
  });
  await expect(
    page.locator('.pm-execution[data-run="run-first"]').first(),
  ).toContainText("completed");
  release();
  await page.waitForTimeout(150);
  await expect(
    page.locator('.pm-execution[data-run="run-first"]').first(),
  ).toContainText("completed");
});

test("project flow follows all host themes with visible card borders", async ({
  page,
}) => {
  for (const theme of flowThemes) {
    await applyFlowTheme(page, theme);
    await expectBorderContrast(page, '.pm-step[data-state="idle"]');
    await expect(page.locator(".pm-step").first()).toHaveCSS(
      "border-top-left-radius",
      theme.radius,
    );
    await expect(page.locator('.pm-step[data-state="running"]')).toHaveCount(1);
    await page
      .getByRole("region", { name: "Project SOP map", exact: true })
      .screenshot({ path: `/private/tmp/processes-map-${theme.name}.png` });
  }
});


test("project map shows timed concurrent work without losing run identity", async ({ page }) => {
  runData.direct_runs[0].state = "scheduled";
  runData.direct_runs[0].steps[0].state = "scheduled";
  runData.direct_runs[0].steps[0].start_at = new Date(Date.now() + 600_000).toISOString();
  await page.getByRole("button", { name: "Refresh map", exact: true }).click();
  const execution = page.locator('.pm-execution[data-run="run-first"]').first();
  await expect(execution).toContainText("Starts in 10 min");
  await expect(execution).toContainText("Barcelona");
});
