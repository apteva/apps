import { expect, test } from "@playwright/test";
test("view, edit, connect and persist the semantic weather flow", async ({
  page,
}, testInfo) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await expect(page.locator(".pf-step")).toHaveCount(3);
  const initialBoxes = await page.locator(".pf-step").evaluateAll((nodes) =>
    nodes.map((node) => {
      const box = node.getBoundingClientRect();
      return { x: box.x, y: box.y };
    }),
  );
  expect(new Set(initialBoxes.map(({ x, y }) => `${x}:${y}`)).size).toBe(3);
  await expect(page.locator(".react-flow__edge")).toHaveCount(5);
  await page.screenshot({
    path: testInfo.outputPath("weather-flow.png"),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Step 1: Fetch current weather", exact: true })
    .click();
  await expect(
    page.getByRole("complementary", { name: "Step details" }),
  ).toContainText("Timestamped report");
  await page.getByRole("button", { name: "Close step details" }).click();
  await page
    .getByRole("button", { name: "Edit procedure", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Step 3: Send Pushover notification",
      exact: true,
    })
    .click();
  await page
    .getByLabel("Step name", { exact: true })
    .fill("Send weather notification");
  await page
    .getByLabel("Post alert in Conversations", { exact: true })
    .uncheck();
  await expect(page.locator(".react-flow__edge")).toHaveCount(5); // now two terminal steps
  await page.getByRole("button", { name: "Close step details" }).click();
  await page.getByRole("button", { name: "Fit flow", exact: true }).click();
  const source = page.locator(
    '[data-id="post_conversations"] .react-flow__handle-right',
  );
  const target = page.locator(
    '[data-id="send_pushover"] .react-flow__handle-left',
  );
  await page.waitForTimeout(400);
  await source.click();
  await target.click();
  await expect(
    page.locator('[data-testid="rf__edge-post_conversations:send_pushover"]'),
  ).toHaveCount(1);
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  const saved = await page.evaluate(() =>
    JSON.parse(sessionStorage.getItem("process")!),
  );
  expect(saved.steps[2].name).toBe("Send weather notification");
  expect(saved.steps[2].depends_on).toEqual([
    "fetch_weather",
    "post_conversations",
  ]);
  expect(saved.steps.every((step: Record<string, unknown>) => !("position" in step))).toBe(true);
  await page.reload();
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Step 3: Send weather notification",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Edit procedure", exact: true })
    .click();
  await page
    .getByRole("button", { name: "+ Add step", exact: true })
    .click();
  await expect(page.getByLabel("Role", { exact: true })).toBeVisible();
  await page
    .getByLabel("Step instructions")
    .fill("Review the report before completion.");
  await page
    .getByLabel("Required output", { exact: true })
    .fill("Review result and evidence.");
  await page.waitForTimeout(400);
  const card = (await page
    .getByRole("button", { name: "Step 4: New step", exact: true })
    .boundingBox())!;
  const canvas = (await page.locator(".pf-canvas").boundingBox())!;
  expect(card.x + card.width).toBeLessThanOrEqual(canvas.x + canvas.width);
  await page.screenshot({
    path: testInfo.outputPath("flow-editor.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Remove step", exact: true }).click();
  await expect(page.locator(".pf-step")).toHaveCount(3);
  expect(errors).toEqual([]);
});
test("mobile editor fits the page and opens step details below the canvas", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?no_agents=1");
  await page
    .getByRole("button", { name: "+ New process", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Add first step", exact: true })
    .click();
  await page
    .getByLabel("Process name", { exact: true })
    .fill("Unassigned weather process");
  await expect(
    page.getByLabel("Responsible agent", { exact: true }),
  ).toHaveCount(0);
  await page.getByLabel("Step name", { exact: true }).fill("Fetch weather");
  await page
    .getByLabel("Step instructions")
    .fill("Fetch fresh conditions for the configured city.");
  await page
    .getByLabel("Required output", { exact: true })
    .fill("Weather report with source and timestamp.");
  expect(await page.evaluate(() => document.body.scrollWidth)).toBe(390);
  const canvas = (await page.locator(".pf-canvas").boundingBox())!;
  const inspector = (await page.locator(".pf-inspector").boundingBox())!;
  expect(inspector.y).toBeGreaterThanOrEqual(canvas.y + canvas.height);
  await page.screenshot({
    path: testInfo.outputPath("flow-mobile.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect(
    page.getByRole("heading", {
      name: "Unassigned weather process",
      exact: true,
    }),
  ).toBeVisible();
  const submitted = await page.evaluate(() =>
    JSON.parse(sessionStorage.getItem("submitted-definition")!),
  );
  expect(submitted.owner_agent_id).toBeUndefined();
  expect(submitted.schedule).toBeUndefined();
  expect(submitted.execution_mode).toBeUndefined();
  expect(submitted.steps).toHaveLength(1);
  await page.getByRole("button", { name: "Assignments", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "No assignments yet" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Add assignment", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Save assignment", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page
    .getByRole("button", { name: "← All processes", exact: true })
    .click();
  const row = page
    .getByRole("row")
    .filter({ hasText: "Unassigned weather process" });
  await expect(row.getByRole("cell").nth(1)).toHaveText("Unassigned");
  await expect(row.getByRole("cell").nth(2)).toHaveText("0");
});
