import { test, expect } from "@playwright/test";

const rows = [
  { bucket: "2026-10-01", ts: Date.UTC(2026, 9, 1), count: 0 },
  { bucket: "2026-10-02", ts: Date.UTC(2026, 9, 2), count: 120 },
  { bucket: "2026-10-03", ts: Date.UTC(2026, 9, 3), count: 99 },
  { bucket: "2026-10-04", ts: Date.UTC(2026, 9, 4), value: null },
];

test("trend tooltips show each bucket, zero and missing values at any width", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  const queries: any[] = [];
  await page.route("**/api/apps/analytics/query-widget?**", route => {
    queries.push(route.request().postDataJSON());
    return route.fulfill({ json: { type: "timeseries", series: rows } });
  });
  await page.goto("/?trend=1");
  await page.evaluate(() => (window as any).renderAnalytics({
    trend: true, projectId: "p1", widgetId: "hover-check", widgetSettings: {
      title: "Traffic trend", app: "content", topic: "page_view", aggregation: "count",
      filter_field: "props.host", filter_value: "marcoschwartz.com", filter_label: "Site",
      filter_options: "marcoschwartz.com,makecademy.com", event_options: "page_view,affiliate_cta_click",
      window: "30d", interval: "day",
    },
  }));
  const chart = page.getByRole("group", { name: /Analytics trend/ });
  const tooltip = page.getByRole("tooltip");
  for (const width of [1400, 360]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(chart).toBeVisible();
    await page.evaluate(() => { document.getElementById("root")!.style.height = "500px"; });
    const shortHeight = (await chart.boundingBox())!.height;
    await page.evaluate(() => { document.getElementById("root")!.style.height = "740px"; });
    await expect.poll(async () => (await chart.boundingBox())!.height).toBeCloseTo(shortHeight + 240, 0);
    const card = (await page.locator("section").boundingBox())!;
    const footer = (await page.getByText("Hover for details").boundingBox())!;
    expect(card.y + card.height - footer.y - footer.height).toBeLessThanOrEqual(24);
    const box = (await chart.boundingBox())!;
    for (const [i, expected] of [[0, "0"], [1, "120"], [2, "99"], [3, "No observation"]] as const) {
      await page.mouse.move(box.x + box.width * (8 + i / 3 * 284) / 300, box.y + box.height / 2);
      await expect(tooltip).toContainText(expected);
      await expect(tooltip).toContainText(`Oct ${i + 1}, 2026`);
      const tipBox = (await tooltip.boundingBox())!;
      expect(tipBox.x).toBeGreaterThanOrEqual(box.x - 1);
      expect(tipBox.x + tipBox.width).toBeLessThanOrEqual(box.x + box.width + 1);
    }
    const gridHeights = await chart.evaluate(svg => Array.from(svg.parentElement!.children)
      .filter(el => el.tagName === "DIV" && (el as HTMLElement).style.height === "1px")
      .map(el => el.getBoundingClientRect().height));
    expect(gridHeights).toEqual([1, 1, 1, 1]);
    await page.mouse.move(0, 0);
    await expect(tooltip).toHaveCount(0);
    await chart.focus();
    await chart.press("Home");
    await expect(tooltip).toContainText("Oct 1, 2026");
    await chart.press("ArrowRight");
    await expect(tooltip).toContainText("120");
    await chart.press("End");
    await expect(tooltip).toContainText("No observation");
    await chart.press("Escape");
    await expect(tooltip).toHaveCount(0);
  }
  await page.getByLabel("Site", { exact: true }).selectOption("");
  await expect.poll(() => queries.at(-1).widget.config.where["props.host"]).toEqual(["marcoschwartz.com", "makecademy.com"]);
  expect(errors).toEqual([]);
});
