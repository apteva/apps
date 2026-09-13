import { expect, test } from "@playwright/test";
import { overviewFixture } from "./overview-fixture";
test("compact widget shows live steps and refreshes from host events at mobile and desktop widths", async ({
  page,
}) => {
  let data = structuredClone(overviewFixture);
  await page.route("**/api/apps/processes/processes/overview?**", (route) =>
    route.fulfill({ json: data }),
  );
  await page.goto("/?widget");
  await expect(
    page.getByRole("region", { name: "Processes overview" }),
  ).toBeVisible();
  await expect(page.getByText("Weather agent").first()).toBeVisible();
  await page.getByText("1/3 steps complete · View steps").click();
  await expect(page.getByText("Fetch weather")).toBeVisible();
  await expect(page.locator(".react-flow")).toHaveCount(0);
  data.active[0].steps_completed = 2;
  data.active[0].steps[1].state = "completed";
  await page.locator("#host-revision").click();
  await expect(page.getByText("2/3 steps complete · View steps")).toBeVisible();
  await page.setViewportSize({ width: 375, height: 900 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: "/private/tmp/processes-overview-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1200, height: 1000 });
  await page.goto("/?widget&full");
  await page.getByRole("button", { name: /Scheduled/ }).click();
  await expect(page.getByText("Hourly forecast")).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/processes-overview-desktop.png",
    fullPage: true,
  });
});
test("run link opens the run history in the Processes page", async ({
  page,
}) => {
  await page.route("**/api/apps/processes/processes/overview?**", (route) =>
    route.fulfill({ json: overviewFixture }),
  );
  await page.goto("/?widget");
  await page.getByRole("link", { name: "Open run" }).first().click();
  await expect(page).toHaveURL(/\/apps\/processes\/page.*run_id=weather-run/);
  await expect(
    page.getByRole("button", { name: "Refresh history" }),
  ).toBeVisible();
});
