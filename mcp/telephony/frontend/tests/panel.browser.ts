import { test, expect } from "@playwright/test";
test("Calls panel uses shared controller and preserves a call across navigation", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/panel");
  await page.getByRole("button", { name: "Answer", exact: true }).first().click();
  const controls = page.getByLabel("Active call controls");
  await expect(controls).toBeVisible();
  await expect(controls).toContainText("Audio connected");
  await expect(page.getByText("Browser ↔ app RTT", { exact: false })).toBeVisible();
  // Do not mute before the separate carrier-side assertion receives microphone audio.
  await expect.poll(async () => (await page.request.get(process.env.TELEPHONY_TEST_GATEWAY + "/fixture/audio-ready")).json()).toEqual({ ready: true });
  await controls.getByRole("button", { name: "Mute", exact: true }).click();
  await expect(controls.getByRole("button", { name: "Unmute" })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Numbers", exact: true }).click();
  await expect(controls).toContainText("Audio connected");
  await controls.getByRole("button", { name: "Keypad", exact: true }).click();
  await controls.getByRole("button", { name: "1", exact: true }).click();
  await expect(controls).toContainText("Keypad tone sent");
  await controls.getByRole("button", { name: "Hang up", exact: true }).click();
  await expect(controls).toHaveCount(0);
  expect(errors).toEqual([]);
});
