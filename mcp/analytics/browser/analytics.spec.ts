import { test, expect } from "@playwright/test";
test.beforeAll(async ({ request }) => {
  await request.post("/__test/reset");
});
test("empty project Home widget renders safely", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/?widget=1&projectId=empty");
  await expect(page.getByText("Analytics", { exact: true })).toBeVisible();
  await page.waitForTimeout(400);
  expect(errors).toEqual([]);
});
test("create dashboard and configure a custom widget", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await page.getByLabel("New dashboard name").fill("Browser audit");
  await page
    .getByRole("button", { name: "Create dashboard", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Browser audit", exact: true }),
  ).toBeVisible();
  await page
    .locator("select")
    .filter({ has: page.locator('option[value="stat"]') })
    .selectOption("stat");
  await page.getByRole("button", { name: "Edit widget" }).click();
  await page.getByLabel("Widget title").fill("Audit event count");
  await page.getByLabel("App", { exact: true }).fill("browser");
  await page.getByLabel("Event topic", { exact: true }).fill("visit");
  await page.getByLabel("Window (24h, 7d, all or $filters.window)").fill("all");
  await page.getByRole("button", { name: "Add filter" }).click();
  await page.getByLabel("Filter field 1").fill("props.number");
  await page.getByLabel("Filter type 1").selectOption("number");
  expect(
    await page
      .getByLabel("Filter value 1")
      .evaluate((el: HTMLInputElement) => el.checkValidity()),
  ).toBe(false);
  await page.getByRole("button", { name: "Save widget" }).click();
  await expect(page.getByLabel("Widget title")).toBeVisible();
  await page.getByLabel("Filter type 1").selectOption("array");
  await page.getByLabel("Filter value 1").fill('[1, "1", false]');
  expect(
    await page
      .getByLabel("Filter value 1")
      .evaluate((el: HTMLInputElement) => el.checkValidity()),
  ).toBe(true);
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await page.getByRole("button", { name: "Save widget" }).click();
  await expect(
    page.getByText("Audit event count", { exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});
test("live dashboard refresh works during continuous traffic", async ({
  page,
  request,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await expect(
    page.getByText("Audit event count", { exact: true }),
  ).toBeVisible();
  let refreshes = 0;
  page.on("request", (r) => {
    if (r.url().includes("/query-dashboard")) refreshes++;
  });
  await request.post("/__test/track", {
    data: { app: "browser", event: "visit", ts: 1000, props: { id: 42 } },
  });
  await page.evaluate(() => {
    (window as any).traffic = setInterval(
      () => (window as any).emitAnalytics(),
      100,
    );
  });
  await page.waitForTimeout(3500);
  await page.evaluate(() => clearInterval((window as any).traffic));
  expect(refreshes).toBeGreaterThanOrEqual(2);
});
test("typed and all-time dashboard filters survive browser controls", async ({
  page,
  request,
}) => {
  const d = await (
    await request.post("/api/apps/analytics/dashboards?project_id=p1", {
      data: {
        name: "Typed audit",
        config: {
          filters: [
            {
              key: "window",
              label: "Time range",
              type: "date_window",
              default: "all",
            },
            {
              key: "id",
              label: "Identifier",
              type: "select",
              source: {
                app: "browser",
                topic: "visit",
                value_field: "props.id",
              },
            },
          ],
        },
      },
    })
  ).json();
  await request.post(
    `/api/apps/analytics/dashboards/${d.id}/widgets?project_id=p1`,
    {
      data: {
        type: "stat",
        title: "Typed count",
        config: {
          app: "browser",
          topic: "visit",
          window: "$filters.window",
          where: { "props.id": "$filters.id" },
          aggregation: "count",
        },
      },
    },
  );
  await page.goto("/");
  await page.getByRole("button", { name: "Dashboards", exact: true }).click();
  await page.getByRole("button", { name: "Typed audit", exact: true }).click();
  await page.getByLabel("Time range").selectOption("all");
  const response = page.waitForResponse(
    (r) =>
      r.url().includes("/query-dashboard") &&
      decodeURIComponent(r.url()).includes('"value":42'),
  );
  await page.getByLabel("Identifier").selectOption({ label: "42" });
  const result = await response;
  const body = await result.json();
  expect(body.widgets[0].data.value).toBe(1);
});
test("reference management preserves inactive status and FX creates revisions", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Manage", exact: true }).click();
  await page.getByLabel("Set key").fill("browser-sites");
  await page.getByLabel("Set label").fill("Browser sites");
  await page.getByRole("button", { name: "Save set", exact: true }).click();
  await page.getByLabel("Reference set").selectOption("browser-sites");
  await page.getByLabel("Value", { exact: true }).fill("retired");
  await page.getByLabel("Value label").fill("Retired site");
  await page.getByLabel("Value status").selectOption("inactive");
  await page.getByRole("button", { name: "Save value", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Retired site · retired · inactive" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "FX rates", exact: true }).click();
  await page.getByLabel("Rate", { exact: true }).fill("0.9");
  await page.getByRole("button", { name: "Save rate", exact: true }).click();
  await expect(page.getByText("Rate revision saved")).toBeVisible();
  await expect(
    page.getByRole("cell", { name: "USD/EUR", exact: true }),
  ).toBeVisible();
});
test("blank objective target is rejected; existing targets can be edited", async ({
  page,
  request,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Objectives", exact: true }).click();
  await page.getByRole("button", { name: /new objective/i }).click();
  await page.getByLabel("Objective name", { exact: true }).fill("Browser goal");
  await page.getByLabel("Target name", { exact: true }).fill("Visits");
  await page.getByRole("button", { name: /create objective/i }).click();
  await expect(
    page.getByText(
      "Objective name, target name and target value are required.",
    ),
  ).toBeVisible();
  await page.getByLabel("Target value", { exact: true }).fill("10");
  await page.getByRole("button", { name: /create objective/i }).click();
  await expect(
    page.getByRole("button", { name: "Edit objective" }),
  ).toBeVisible();
  const before = await (
    await request.get("/api/apps/analytics/objectives?project_id=p1")
  ).json();
  await page.getByRole("button", { name: "Edit objective" }).click();
  await page.getByLabel("Target value", { exact: true }).fill("20");
  await page.getByRole("button", { name: "Save objective" }).click();
  await expect(
    page.getByRole("button", { name: "Edit objective" }),
  ).toBeVisible();
  const after = await (
    await request.get("/api/apps/analytics/objectives?project_id=p1")
  ).json();
  expect(after.objectives[0].targets[0].id).toBe(
    before.objectives[0].targets[0].id,
  );
  expect(after.objectives[0].targets[0].target_value).toBe(20);
});
test("Home settings limit queries and manual selection survives refresh", async ({
  page,
}) => {
  await page.goto("/?widget=1");
  await expect(page.getByLabel("Analytics dashboard")).toBeVisible();
  await page
    .getByLabel("Analytics dashboard")
    .selectOption({ label: "Browser audit" });
  await expect(page.getByText("Audit event count")).toBeVisible();
  await page.evaluate(() =>
    (window as any).renderAnalytics({
      widget: true,
      projectId: "p1",
      eventRevision: 1,
      widgetSettings: { show_trends: false, show_goals: false, max_metrics: 1 },
    }),
  );
  await expect(page.getByLabel("Analytics dashboard")).toHaveValue(/\d+/);
  await expect(page.getByText("Audit event count")).toBeVisible();
});
test("switching projects resets dashboard state without old data", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/?widget=1");
  await page.evaluate(() =>
    (window as any).renderAnalytics({ widget: true, projectId: "empty" }),
  );
  await expect(page.getByText("Audit event count")).toHaveCount(0);
  await expect(page.getByLabel("Analytics dashboard")).toHaveCount(0);
  expect(errors).toEqual([]);
});
