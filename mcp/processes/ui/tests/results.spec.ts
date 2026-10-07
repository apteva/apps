import { expect, test } from "@playwright/test";
const step = (state = "completed") => ({ id: "result-step", run_id: "result-run", key: "load_weather", definition: {key: "load_weather", name: "Load weather", role: "worker", instructions: "Retrieve weather.", expected_output: "Weather receipt", depends_on: []}, executor: {kind: "agent", agent_id: 7}, state, progress: state === "completed" ? 100 : 35, output: '{"conditions":"**Overcast**, 19.8°C","receipt_id":9007199254740993}', error: "" });
const entry = (state = "completed") => ({ id: "result-run", process_id: "weather", version: 3, workflow: true, backend: "agent", state, progress: state === "completed" ? 100 : 45, created_at: new Date().toISOString(), result: state === "completed" ? JSON.stringify({load_weather: "**Barcelona**\n\n- Overcast\n- 19.8°C", send_pushover: {status: "Delivered", request_id: "f31d0d58-45e8-4024-8628-1f9505b0e638"}, evidence: ["portrait_3.png", "portrait_4.png"], unsafe: '<img src=x onerror="window.resultInjected=true"><script>window.resultInjected=true</script>[bad](javascript:alert(1))'}) : "", steps: [step(state === "completed" ? "completed" : "running")] });
async function openRun(page: any) {
  await page.goto("/?live&activity");
  await page.getByRole("button", {name: "Hourly weather alerts", exact: true}).click();
  await page.getByRole("button", {name: "Runs", exact: true}).click();
  await page.locator(".run-list > button").first().click();
}
for (const width of [1440, 375]) {
  test(`formatted result modal and compact labelled progress at ${width}px`, async ({page, request}) => {
    await page.setViewportSize({width, height: 1000});
    await request.post("/fixture/runs", {data: [entry()]});
    await openRun(page);
    const current = page.locator(".run-current-step");
    await expect(current).toContainText("Step completed");
    await expect(current).not.toContainText("request_id");
    const progress = page.getByRole("progressbar", {name: "Run progress", exact: true});
    await expect(progress).toHaveAttribute("value", "100");
    const bar = (await progress.boundingBox())!;
    expect(bar.width).toBeLessThanOrEqual(300);
    expect(bar.height).toBeGreaterThanOrEqual(12);
    const button = page.getByRole("button", {name: "View result", exact: true});
    await button.click();
    const modal = page.getByRole("dialog", {name: "Process result", exact: true});
    await expect(modal).toBeVisible();
    await expect(modal.locator("strong")).toContainText(["Barcelona"]);
    await expect(modal.locator("li")).toContainText(["Overcast", "19.8°C", "portrait_3.png", "portrait_4.png"]);
    await expect(modal).toContainText("f31d0d58-45e8-4024-8628-1f9505b0e638");
    await expect(modal.locator("script,img,[onerror],a[href^='javascript:']")).toHaveCount(0);
    expect(await page.evaluate(() => (window as any).resultInjected)).toBeUndefined();
    await modal.screenshot({path: `/private/tmp/processes-result-modal-${width}.png`});
    await page.keyboard.press("Escape");
    await expect(modal).toHaveCount(0);
    await expect(button).toBeFocused();
    expect(await page.evaluate(() => document.body.style.overflow)).not.toBe("hidden");
    await current.getByRole("button", {name: "View step details", exact: true}).click();
    const details = page.getByRole("dialog", {name: "Step details", exact: true});
    await expect(details).toContainText("9007199254740993");
    await expect(details.locator(".result-content strong")).toHaveText("Overcast");
    await details.getByRole("button", {name: "Close", exact: true}).click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}
test("current step renders saved output and shows separate step progress", async ({page, request}) => {
  await request.post("/fixture/runs", {data: [entry("running")]});
  await openRun(page);
  await expect(page.getByRole("button", {name: "View result", exact: true})).toHaveCount(0);
  const current = page.locator(".run-current-step");
  await expect(current.getByRole("progressbar", {name: "Run progress", exact: true})).toHaveAttribute("value", "45");
  await expect(current.locator(".step-output")).toHaveCount(0);
  await current.getByRole("button", {name: "View step details", exact: true}).click();
  const details = page.getByRole("dialog", {name: "Step details", exact: true});
  await expect(details.getByRole("progressbar", {name: "Step progress", exact: true})).toHaveAttribute("value", "35");
  await expect(details.locator(".step-output strong")).toHaveText("Overcast");
  await expect(details.locator(".step-output")).toContainText("9007199254740993");
  await page.screenshot({path: "/private/tmp/processes-current-step-result.png", fullPage: true});
});
