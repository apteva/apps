import { expect, test } from "@playwright/test";

for (const pinnedRole of [false, true]) {
  test(`replace a deleted coordinator${pinnedRole ? " and explicitly assigned role" : " with inherited roles"}`, async ({ page }) => {
    await page.goto(`/?missing_agent${pinnedRole ? "&pinned_role" : ""}`);
    await page.getByRole("button", { name: "Hourly weather alerts", exact: true }).click();
    await page.getByRole("button", { name: "Assignments", exact: true }).click();
    await page.getByRole("button", { name: "Edit assignment", exact: true }).click();
    const owner = page.getByLabel("Responsible agent / coordinator");
    const role = page.getByLabel("weather_agent", { exact: true });
    const save = page.getByRole("button", { name: "Save assignment", exact: true });
    await expect(owner).toHaveValue("1104");
    await expect(owner.locator("option:checked")).toHaveText(/Agent 1104 unavailable/);
    await expect(role.locator("option:checked")).toHaveText(/Agent 1104 unavailable/);
    await expect(save).toBeDisabled();
    await owner.selectOption("1105");
    if (pinnedRole) {
      await expect(role).toHaveValue("1104");
      await expect(save).toBeDisabled();
      await role.selectOption("1105");
    }
    await expect(role).toHaveValue("1105");
    await expect(save).toBeEnabled();
    await save.click();
    await expect(page.getByRole("button", { name: "Edit assignment", exact: true })).toBeVisible();
    const saved = await page.evaluate(() => JSON.parse(sessionStorage.getItem("submitted-assignment")!));
    expect(saved.expected_revision).toBe(4);
    expect(saved.assignment.owner_agent_id).toBe(1105);
    expect(saved.assignment.roles).toEqual(pinnedRole ? { weather_agent: { kind: "agent", agent_id: 1105 } } : {});
    expect(saved.assignment.parameters).toEqual({ city: "Barcelona" });
    expect(saved.assignment.schedule).toEqual({ kind: "cron", cron: "0 * * * *", timezone: "Europe/Madrid" });
    expect(saved.assignment.status).toBe("paused");
    await page.getByRole("button", { name: "Edit assignment", exact: true }).click();
    await expect(owner).toHaveValue("1105");
    await expect(role).toHaveValue("1105");
  });
}
