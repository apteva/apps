import { expect, test } from "@playwright/test";
import { overviewFixture } from "./overview-fixture";
test("equal-height clear rows order running, scheduled, then history and reveal live steps on selection", async ({
  page,
}) => {
  await page.clock.setFixedTime(new Date("2026-09-13T17:10:00Z"));
  await page.setViewportSize({ width: 1100, height: 700 });
  const data = structuredClone(overviewFixture);
  data.active.unshift({
    ...data.active[0],
    id: "blocked",
    state: "blocked",
    created_at: "2026-09-13T17:05:00Z",
  });
  await page.route("**/api/apps/processes/processes/overview?**", (route) =>
    route.fulfill({ json: data }),
  );
  await page.goto("/?widget&full");
  const rows = page.locator(".po-row");
  await expect(rows).toHaveCount(4);
  expect(
    await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getAttribute("data-state")),
    ),
  ).toEqual(["running", "scheduled", "blocked", "completed"]);
  expect(
    await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getBoundingClientRect().height),
    ),
  ).toEqual([80, 80, 80, 80]);
  await expect(
    page.locator(".po-detail, .po-stats, progress, .react-flow"),
  ).toHaveCount(0);
  await page
    .getByRole("region", { name: "Processes overview", exact: true })
    .screenshot({
      path: "/private/tmp/processes-clear-desktop.png",
    });
  await rows.first().click();
  await expect(
    page.getByRole("region", { name: "Selected process details" }),
  ).toContainText("1/3 steps complete");
  await expect(
    page.locator(".po-detail li").filter({ hasText: "Send notification" }),
  ).toBeVisible();
  data.active[1].steps_completed = 2;
  await page.locator("#host-revision").click();
  await expect(
    page.getByRole("region", { name: "Selected process details" }),
  ).toContainText("2/3 steps complete");
  await page.getByRole("button", { name: "Close details" }).click();
  await page.setViewportSize({ width: 375, height: 900 });
  expect(
    await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getBoundingClientRect().height),
    ),
  ).toEqual([80, 80, 80, 80]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page
    .getByRole("region", { name: "Processes overview", exact: true })
    .screenshot({
      path: "/private/tmp/processes-clear-mobile.png",
    });
  await page.getByRole("button", { name: "Scheduled", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows).toContainText("Hourly forecast");
});
test("selected run links into Processes history", async ({ page }) => {
  await page.route("**/api/apps/processes/processes/overview?**", (route) =>
    route.fulfill({ json: overviewFixture }),
  );
  await page.goto("/?widget");
  await page.locator(".po-row").first().click();
  await page.getByRole("link", { name: "Open run" }).click();
  await expect(page).toHaveURL(/\/apps\/processes\/page.*run_id=weather-run/);
  await expect(
    page.getByRole("button", { name: "Refresh history" }),
  ).toBeVisible();
});
