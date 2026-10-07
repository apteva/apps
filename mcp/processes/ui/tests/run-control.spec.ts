import { expect, test } from "@playwright/test";
test("step-by-step modal, selected branches, receipts and human approval", async ({
  page,
}, info) => {
  await page.goto("/?step_control");
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await page.getByRole("button", { name: "Assignments", exact: true }).click();
  await page
    .getByRole("button", { name: "Run step by step", exact: true })
    .click();
  await expect(page.getByLabel("Run control")).toHaveValue("step_by_step");
  await page.getByRole("button", { name: "Start run", exact: true }).click();
  await page.locator(".run-list > button").first().click();
  await expect(page.getByLabel("Step-by-step controls")).toContainText(
    "Waiting for you to advance",
  );
  await page.getByLabel("alpha", { exact: true }).check();
  await page.getByLabel("beta", { exact: true }).check();
  await page
    .getByRole("button", { name: "Run selected steps", exact: true })
    .click();
  await page.getByRole("button", {name: "Step 1: alpha", exact: true}).click();
  await expect(page.locator(".run-detail")).toContainText(
    "Exact receipt alpha.png",
  );
  await page.getByRole("button", {name: "Step 2: beta", exact: true}).click();
  await expect(page.locator(".run-detail")).toContainText(
    "Exact receipt beta.png",
  );
  const releases = await page.evaluate(() =>
    JSON.parse(sessionStorage.getItem("releases")!),
  );
  expect(releases.map((r: any) => r.step_id)).toEqual(["alpha", "beta"]);
  expect(new Set(releases.map((r: any) => r.idempotency_key)).size).toBe(2);
  await expect(
    page.getByRole("button", { name: "Complete human step", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", {name: "Follow current step", exact: true}).click();
  await page
    .getByRole("button", { name: "Run next step", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Complete human step", exact: true })
    .click();
  await page
    .getByLabel("Result and evidence")
    .fill("Operator approved exact artifacts");
  await page
    .getByRole("button", { name: "Complete step", exact: true })
    .click();
  await expect(page.locator(".run-detail")).toContainText(
    "Operator approved exact artifacts",
  );
  expect(
    await page.evaluate(
      () => JSON.parse(sessionStorage.getItem("approval")!).output,
    ),
  ).toBe("Operator approved exact artifacts");
  await page.screenshot({
    path: info.outputPath("controlled-run.png"),
    fullPage: true,
  });
});
test("normal Run now retains automatic mode", async ({ page }) => {
  await page.goto("/?step_control");
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await page.getByRole("button", { name: "Assignments", exact: true }).click();
  await page.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(page.getByLabel("Run control")).toHaveValue("automatic");
  await page.getByRole("button", { name: "Start run", exact: true }).click();
  expect(
    await page.evaluate(
      () => JSON.parse(sessionStorage.getItem("submitted-start")!).control_mode,
    ),
  ).toBe("automatic");
});
