import { expect, test } from "@playwright/test";

for (const width of [1440, 375]) {
  test(`edit and save a published step directly at ${width}px`, async ({ page }) => {
    await page.setViewportSize({width, height:1000});
    await page.goto("/");
    await page.getByRole("button", {name:"Hourly weather alerts", exact:true}).click();
    await page.getByRole("button", {name:"Publish process", exact:true}).click();
    await page.getByRole("button", {name:"Procedure", exact:true}).click();
    await page.getByRole("button", {name:"Step 2: Post alert in Conversations", exact:true}).click();
    const inspector = page.getByRole("complementary", {name:"Step details"});
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
    await inspector.screenshot({path:`/private/tmp/processes-inline-step-${width}.png`});
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.getByLabel("Procedure version", {exact:true}).selectOption("1");
    await expect(page.getByText("Historical procedure.", {exact:false})).toBeVisible();
    await expect(inspector.getByLabel("Step instructions", {exact:true})).toHaveCount(0);
    await expect(inspector).toContainText("Post alert in Conversations using the configured city");
    await page.getByLabel("Procedure version", {exact:true}).selectOption("2");
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
  const inspector = page.getByRole("complementary", {name:"Step details"});
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
