import { expect, test } from "@playwright/test";

test("whole process row signals selection and opens by mouse or keyboard", async ({ page }) => {
  await page.goto("/");
  const row = page.getByRole("row").filter({ hasText: "Hourly weather alerts" });
  await expect(row).toHaveCSS("cursor", "pointer");
  await page.getByRole("heading", { name: "Processes", exact: true }).hover();
  const background = await row.evaluate((node) => getComputedStyle(node).backgroundColor);
  await row.getByRole("cell").nth(3).hover();
  await expect.poll(() => row.evaluate((node) => getComputedStyle(node).backgroundColor)).not.toBe(background);
  await row.getByRole("cell").nth(3).click();
  await expect(page.getByRole("heading", { name: "Hourly weather alerts", exact: true })).toBeVisible();

  for (const key of ["Enter", "Space"]) {
    await page.getByRole("button", { name: "← All processes", exact: true }).click();
    await row.getByRole("button", { name: "Hourly weather alerts", exact: true }).focus();
    await expect(row).toHaveCSS("outline-style", "solid");
    await page.keyboard.press(key);
    await expect(page.getByRole("heading", { name: "Hourly weather alerts", exact: true })).toBeVisible();
  }
});
