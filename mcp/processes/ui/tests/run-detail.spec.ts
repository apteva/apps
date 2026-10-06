import { expect, test } from "@playwright/test";

for (const width of [1800, 375]) {
  test(`project run opens a detail page using all available width at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1100 });
    await page.goto("/?step_control");
    await page.getByRole("button", { name: "Hourly weather alerts", exact: true }).click();
    await page.getByRole("button", { name: "Assignments", exact: true }).click();
    await page.getByRole("button", { name: "Run step by step", exact: true }).click();
    await page.getByRole("button", { name: "Start run", exact: true }).click();
    await page.getByRole("button", { name: "← All processes", exact: true }).click();
    await page.getByRole("button", { name: "Runs", exact: true }).click();
    await page.locator(".run-list > button").first().click();
    const detail = page.locator(".run-detail-page");
    await expect(detail).toBeVisible();
    await expect(page.locator(".run-list")).toHaveCount(0);
    const bounds = await detail.boundingBox();
    const expectedWidth = width - (width <= 760 ? 36 : 56);
    expect(bounds!.width).toBeCloseTo(expectedWidth, 0);
    await page.getByLabel("Filter project run state").selectOption("completed");
    await expect(detail).toBeVisible();
    await page.screenshot({ path: `/private/tmp/processes-run-detail-${width}.png`, fullPage: true });
    await page.getByRole("button", { name: "← Back to runs", exact: true }).click();
    await expect(detail).toHaveCount(0);
    await page.getByLabel("Filter project run state").selectOption("");
    await expect(page.locator(".run-list > button")).toHaveCount(1);
  });
}
