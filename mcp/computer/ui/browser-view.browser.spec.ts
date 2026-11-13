import { expect, test } from "@playwright/test";

test("the mounted browser fits panel/floating bounds, keeps its iframe, and retains the closed frame", async ({ page }) => {
  let closed = false, iframeLoads = 0;
  const calls: { url: URL; method: string }[] = [];
  await page.route("**/api/apps/computer/**", async route => {
    const request = route.request(), url = new URL(request.url());
    calls.push({ url, method: request.method() });
    expect(url.searchParams.get("install_id")).toBe("55");
    expect(url.searchParams.get("project_id")).toBe("p1");
    if (url.pathname.endsWith("presentation")) return route.fulfill({ json: { session: { status: closed ? "closed" : "active", current_url: "https://example.com", width: 1600, height: 800 }, screenshot_url: "/api/apps/computer/sessions/br_one/screenshot" } });
    if (url.pathname.endsWith("stream")) return route.fulfill({ json: { kind: "iframe", url: "/api/apps/computer/sessions/br_one/live", status: closed ? "closed" : "active" } });
    if (url.pathname.endsWith("live")) { iframeLoads++; return route.fulfill({ contentType: "text/html", body: '<input aria-label="Retained input" value="initial">' }); }
    return route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="800"><rect width="1600" height="800" fill="green"/></svg>' });
  });
  await page.goto("/");
  const frame = page.frameLocator("iframe").getByLabel("Retained input");
  await frame.fill("keep this state");
  const viewport = page.locator("[data-browser-viewport]");
  await expect(viewport).toHaveCSS("height", "360px");
  const move = async (mode: string, width: number, height: number, active = true) => {
    await page.evaluate(args => (window as any).renderView({ session_id: "br_one", projectId: "p1", installId: 55, presentation: args }), { mode, width, height, active });
  };
  for (const [mode, width, height] of [["side-panel", 402, 602], ["floating", 1002, 302], ["floating", 222, 162]] as const) {
    await move(mode, width, height);
    await expect(viewport).toHaveCSS("height", `${height - 2}px`);
    const box = await page.locator("iframe").boundingBox();
    expect(box).not.toBeNull(); expect(box!.width / box!.height).toBeCloseTo(2);
    const bounds = await page.locator("#root").boundingBox();
    expect(box!.x + box!.width).toBeLessThanOrEqual(bounds!.x + bounds!.width);
    expect(box!.y + box!.height).toBeLessThanOrEqual(bounds!.y + bounds!.height);
    await expect(frame).toHaveValue("keep this state");
  }
  expect(iframeLoads).toBe(1);
  await move("floating", 402, 602, false);
  const before = calls.length;
  // Longer than Computer's polling interval, to prove hidden views pause.
  await page.waitForTimeout(3200);
  expect(calls).toHaveLength(before);
  closed = true;
  await move("floating", 402, 602, true);
  const image = page.getByAltText("Browser page");
  await expect(image).toBeVisible();
  await expect(page.locator("iframe")).toHaveCount(0);
  expect(await image.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(1600);
  expect(calls.every(call => call.method === "GET")).toBe(true);
});
