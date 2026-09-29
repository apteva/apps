import { expect, test } from "@playwright/test";

test("agent Overview chat frames the existing header and keeps a narrow composer visible", async ({ page, request }) => {
  await request.post("/reset");
  await request.post("/seed", { data: {} });
  for (let index = 0; index < 20; index++) {
    await request.post("/append-message", {
      data: {
        id: index + 3,
        conversation_id: "chat-operator",
        role: "agent",
        agent_id: 41,
        content: `Earlier update ${index}: ${"details ".repeat(12)}`,
        components: [],
        created_at: new Date().toISOString(),
      },
    });
  }
  await page.setViewportSize({ width: 390, height: 720 });
  await page.goto("/?host=dashboard&theme=clean&slot=dashboard.agent_detail&size=half");

  const frame = page.locator(".agent-conversations-widget-frame");
  await expect(frame).toBeVisible();
  await expect(frame.getByRole("heading", { name: "Support chat" })).toHaveCount(1);
  await expect(frame.getByRole("button", { name: "History" })).toBeVisible();
  expect(await frame.evaluate((element) => {
    const style = getComputedStyle(element);
    return { border: style.borderTopWidth, radius: style.borderTopLeftRadius, background: style.backgroundColor };
  })).toEqual({ border: "1px", radius: "10px", background: "rgb(255, 255, 255)" });

  const geometry = await frame.evaluate((element) => {
    const frameBox = element.getBoundingClientRect();
    const actions = element.querySelector("[data-chat-header-actions]")!.getBoundingClientRect();
    const transcript = element.querySelector(".overflow-auto")!;
    const composer = element.querySelector(".chat-composer-safe")!.getBoundingClientRect();
    return {
      actionsInside: actions.left >= frameBox.left && actions.right <= frameBox.right,
      transcriptScrolls: transcript.scrollHeight > transcript.clientHeight,
      composerInside: composer.top >= frameBox.top && composer.bottom <= frameBox.bottom,
    };
  });
  expect(geometry).toEqual({ actionsInside: true, transcriptScrolls: true, composerInside: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test("Build chat does not inherit the agent Overview widget frame", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard&slot=dashboard.build");
  await expect(page.locator(".agent-conversations-widget-frame")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Support chat" })).toBeVisible();
});
