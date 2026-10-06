import { expect, test } from "@playwright/test";

for (const width of [1440, 375]) {
  test(`edit and save a published step directly at ${width}px`, async ({ page }) => {
    await page.setViewportSize({width, height:1000});
    await page.goto("/");
    await page.getByRole("button", {name:"Hourly weather alerts", exact:true}).click();
    await page.getByRole("button", {name:"Publish process", exact:true}).click();
    await page.getByRole("button", {name:"Procedure", exact:true}).click();
    await page.getByRole("button", {name:"Step 2: Post alert in Conversations", exact:true}).click();
    const inspector = page.getByRole("dialog", {name:"Edit step"});
    await expect(inspector.getByLabel("Step instructions", {exact:true})).toBeVisible();
    await expect(inspector).toContainText("Publish it when ready");
    await expect(inspector.getByRole("button", {name:"Save changes", exact:true})).toBeDisabled();
    await inspector.getByLabel("Step instructions", {exact:true}).fill("Read the exact weather receipt, then prepare the alert.");
    await inspector.getByRole("button", {name:"Save changes", exact:true}).click();
    await expect(inspector.getByRole("status")).toContainText("Changes saved");
    const controls = page.getByRole("group", {name:"Process publishing"});
    await expect(controls).toContainText("Draft:");
    await expect(controls).toContainText("Version 2");
    const definition = await page.evaluate(() => JSON.parse(sessionStorage.getItem("submitted-definition")!));
    expect(definition.steps[1].key).toBe("post_conversations");
    expect(definition.steps[1].instructions).toBe("Read the exact weather receipt, then prepare the alert.");
    expect(definition.steps[1].depends_on).toEqual(["fetch_weather"]);
    expect(definition.steps[0].name).toBe("Fetch current weather");
    expect(definition.steps[2].depends_on).toEqual(["fetch_weather", "post_conversations"]);
    const box = (await inspector.boundingBox())!;
    expect(box.width).toBeGreaterThan(width > 600 ? 800 : width - 32);
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.y).toBeGreaterThanOrEqual(0);
    const save = inspector.getByRole("button", {name:"Save changes", exact:true});
    await inspector.locator(".pf-inspector-content").evaluate(el => { el.scrollTop = el.scrollHeight; });
    await expect(save).toBeInViewport();
    await expect(inspector.getByRole("button", {name:"Close step details"})).toBeInViewport();
    await inspector.locator(".pf-inspector-content").evaluate(el => { el.scrollTop = 0; });
    await inspector.screenshot({path:`/private/tmp/processes-modal-step-${width}.png`});
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await inspector.getByRole("button", {name:"Close step details"}).click();
    await page.getByLabel("Procedure version", {exact:true}).selectOption("1");
    await page.getByRole("button", {name:"Step 2: Post alert in Conversations", exact:true}).click();
    const history = page.getByRole("complementary", {name:"Step details"});
    await expect(page.getByText("Historical procedure.", {exact:false})).toBeVisible();
    await expect(history.getByLabel("Step instructions", {exact:true})).toHaveCount(0);
    await expect(history).toContainText("Post alert in Conversations using the configured city");
    await history.getByRole("button", {name:"Close step details"}).click();
    await page.getByLabel("Procedure version", {exact:true}).selectOption("2");
    await page.getByRole("button", {name:"Step 2: Post alert in Conversations", exact:true}).click();
    await expect(inspector.getByLabel("Step instructions", {exact:true})).toHaveValue("Read the exact weather receipt, then prepare the alert.");
    await page.reload();
    await page.getByRole("button", {name:"Hourly weather alerts", exact:true}).click();
    await page.getByRole("button", {name:"Step 2: Post alert in Conversations", exact:true}).click();
    await expect(inspector.getByLabel("Step instructions", {exact:true})).toHaveValue("Read the exact weather receipt, then prepare the alert.");
  });
}

test("cancel, validate and retry a failed inline save without losing edits", async ({page}) => {
  await page.goto("/?fail_save");
  await page.getByRole("button", {name:"Hourly weather alerts", exact:true}).click();
  await page.getByRole("button", {name:"Step 1: Fetch current weather", exact:true}).click();
  const inspector = page.getByRole("dialog", {name:"Edit step"});
  const instructions = inspector.getByLabel("Step instructions", {exact:true});
  const original = await instructions.inputValue();
  await instructions.fill("Discard this edit");
  await inspector.getByRole("button", {name:"Cancel", exact:true}).click();
  await expect(instructions).toHaveValue(original);
  await instructions.fill("");
  await expect(inspector.getByRole("button", {name:"Save changes", exact:true})).toBeDisabled();
  await instructions.fill("Persist this exact revised step.");
  await inspector.getByRole("button", {name:"Save changes", exact:true}).click();
  await expect(inspector.getByRole("alert")).toContainText("Temporary save failure");
  await expect(instructions).toHaveValue("Persist this exact revised step.");
  await inspector.getByRole("button", {name:"Save changes", exact:true}).click();
  await expect(inspector.getByRole("status")).toContainText("Changes saved");
  await expect(page.getByRole("group", {name:"Process publishing"})).toContainText("Version 2");
});


test("step modal traps focus, closes with Escape and keeps unsaved edits", async ({page}) => {
  await page.goto("/");
  await page.getByRole("button", {name:"Hourly weather alerts", exact:true}).click();
  const step = page.getByRole("button", {name:"Step 1: Fetch current weather", exact:true});
  await step.focus();
  await step.press("Enter");
  const dialog = page.getByRole("dialog", {name:"Edit step"});
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Step instructions", {exact:true}).fill("Keep this pending edit.");
  await dialog.getByRole("button", {name:"Cancel", exact:true}).focus();
  await page.keyboard.press("Tab");
  expect(await dialog.evaluate(el => el.contains(document.activeElement))).toBe(true);
  expect(await page.evaluate(() => document.body.style.overflow)).toBe("hidden");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(step).toBeFocused();
  expect(await page.evaluate(() => document.body.style.overflow)).not.toBe("hidden");
  await step.press("Enter");
  await expect(dialog.getByLabel("Step instructions", {exact:true})).toHaveValue("Keep this pending edit.");
});
