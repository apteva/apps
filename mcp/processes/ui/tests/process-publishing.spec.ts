import { expect, test } from "@playwright/test";

for (const width of [1440, 375]) {
  test(`publish and return to draft from the header at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/");
    await page.getByRole("button", { name: "Hourly weather alerts", exact: true }).click();
    const controls = page.getByRole("group", { name: "Process publishing" });
    const publish = controls.getByRole("button", { name: "Publish process", exact: true });
    await expect(publish).toBeInViewport();
    await expect(controls).toContainText("Draft:");
    await publish.click();
    await expect(controls).toContainText("Published:");
    await expect(publish).toHaveCount(0);

    await page.getByRole("button", { name: "Procedure", exact: true }).click();
    await expect(controls.getByRole("button", { name: "Return to draft", exact: true })).toBeInViewport();
    await controls.getByRole("button", { name: "Pause process", exact: true }).click();
    await expect(controls).toContainText("Paused:");
    await controls.getByRole("button", { name: "Return to draft", exact: true }).click();
    await expect(controls).toContainText("Draft:");
    await expect(page.getByRole("button", { name: "Run now", exact: true })).toHaveCount(0);
    await expect(controls).toContainText("Version 1");

    await controls.getByRole("button", { name: "Edit procedure", exact: true }).click();
    await page.getByRole("button", { name: "Save draft", exact: true }).click();
    await expect(controls).toContainText("Version 2");
    await publish.click();
    await expect(controls).toContainText("Published:");
    await controls.getByRole("button", { name: "Return to draft", exact: true }).click();
    await expect(controls).toContainText("Draft:");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.locator("header.head").screenshot({ path: `/private/tmp/processes-publishing-${width}.png` });
    await page.reload();
    await page.getByRole("button", { name: "Hourly weather alerts", exact: true }).click();
    await expect(controls).toContainText("Draft:");
    await expect(controls).toContainText("Version 2");
  });
}
