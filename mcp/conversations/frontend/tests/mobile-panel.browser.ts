import { expect, test } from "@playwright/test";

const panelRows = [
  { id: "mobile-one", project_id: "project", lead_agent_id: 41, lead_agent_name: "Assistant", title: "Client onboarding", kind: "direct", origin: "web", audience: "operator", created_at: "2026-09-27T09:00:00Z", updated_at: "2026-09-28T07:00:00Z" },
  { id: "mobile-two", project_id: "project", lead_agent_id: 41, lead_agent_name: "Assistant", title: "Weather via Pushover", kind: "direct", origin: "web", audience: "operator", created_at: "2026-09-26T09:00:00Z", updated_at: "2026-09-27T07:00:00Z" },
];

test.beforeEach(async ({ page, request }) => {
  await page.setViewportSize({ width: 390, height: 780 });
  await request.post("/reset");
  await request.post("/seed-panel", { data: panelRows });
});

test("mobile panel shows a list or a conversation, never both", async ({ page, request }) => {
  await page.goto("/?host=dashboard&surface=panel");
  const first = page.getByRole("button", { name: /Client onboarding/ });
  await expect(first).toBeVisible();
  await expect(page.getByText("Load earlier conversations")).toHaveCount(0);
  await expect(page.getByText("Chat, inbox, and optional Telegram delivery in one durable system.")).toBeHidden();
  await expect(page.getByRole("button", { name: "New conversation" })).toBeVisible();
  await expect(page.getByRole("button", { name: "More options" })).toBeVisible();
  expect(await page.locator("main aside ul").evaluate(element => ({ style: getComputedStyle(element).listStyleType, padding: getComputedStyle(element).paddingLeft }))).toEqual({ style: "none", padding: "0px" });
  const before = await (await request.get("/requests")).json();
  expect(before.some((call: any) => ["/messages", "/changes", "/seen"].some(path => call.path.endsWith(path)) || (call.path.endsWith("/stream") && call.query.scope !== "user"))).toBe(false);

  await first.click();
  await expect(page.getByRole("button", { name: "Back to conversations" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Back to conversations" })).toBeFocused();
  await expect(page.getByRole("heading", { name: "Client onboarding" })).toBeVisible();
  await expect(first).toBeHidden();
  await expect(page.getByRole("button", { name: "Conversation options" })).toBeVisible();
  expect(await page.locator(".chat-composer-safe").evaluate(element => window.innerHeight - element.getBoundingClientRect().bottom)).toBeLessThan(32);
  await page.getByRole("button", { name: "Back to conversations" }).click();
  await expect(first).toBeVisible();
  await expect(first).toBeFocused();
  await expect(page.getByRole("heading", { name: "Client onboarding" })).toHaveCount(0);
});

test("a reply to a chat left on mobile becomes unread immediately from the list SSE", async ({ page, request }) => {
  await page.goto("/?host=dashboard&surface=panel");
  const row = page.locator('button[data-conversation-id="mobile-one"]');
  await row.click();
  await page.getByRole("button", { name: "Back to conversations" }).click();
  await expect(row).toBeVisible();
  await request.post("/seed-unread", { data: { chat_id: "mobile-one", count: 1 } });
  await expect(row.getByText("1", { exact: true })).toBeVisible({ timeout: 3_000 });
});

test("the agent widget marks an unselected chat unread from the same SSE", async ({ page, request }) => {
  await page.goto("/?host=dashboard&surface=widget-browser");
  const row = page.getByRole("button", { name: /Weather via Pushover/ });
  await expect(row).toBeVisible();
  await request.post("/seed-unread", { data: { chat_id: "mobile-two", count: 1 } });
  await expect(row.getByText("1", { exact: true })).toBeVisible({ timeout: 3_000 });
});

for (const surface of ["panel", "widget-browser"] as const) {
  test(`${surface} thread list pulses only while that conversation is active`, async ({ page, request }) => {
    await request.post("/seed-activity", { data: ["mobile-two"] });
    await page.goto(`/?host=dashboard&surface=${surface}`);
    const working = page.getByRole("img", { name: "Working" });
    await expect(working).toHaveCount(1);
    const calls = await (await request.get("/requests")).json();
    expect(calls.some((call: any) => call.path.endsWith("/stream") && call.query.scope === "user")).toBe(true);
    expect(calls.some((call: any) => call.path.endsWith("/activity-summary"))).toBe(false);
    await expect(working.locator("xpath=..")).toContainText("Weather via Pushover");
    expect(await working.evaluate(element => getComputedStyle(element).animationName)).toBe("chat-thread-working");
    await page.emulateMedia({ reducedMotion: "reduce" });
    expect(await working.evaluate(element => getComputedStyle(element).animationName)).toBe("none");
    await request.post("/seed-activity", { data: [] });
    await expect(working).toHaveCount(0, { timeout: 6_000 });
  });
}

test("mobile back preserves the list position and narrow screens do not overflow", async ({ page, request }) => {
  await request.post("/seed-panel", { data: Array.from({ length: 30 }, (_, index) => ({ ...panelRows[0], id: `mobile-${index}`, title: `Conversation ${index + 1}` })) });
  await page.setViewportSize({ width: 320, height: 650 });
  await page.goto("/?host=dashboard&surface=panel");
  const scroller = page.locator("aside .overflow-auto");
  await expect(page.getByRole("button", { name: /Conversation 30/ })).toBeVisible();
  await scroller.evaluate(element => { element.scrollTop = 360; });
  const position = await scroller.evaluate(element => element.scrollTop);
  expect(position).toBeGreaterThan(0);
  await page.getByRole("button", { name: /Conversation 9/ }).click();
  await page.getByRole("button", { name: "Back to conversations" }).click();
  expect(await scroller.evaluate(element => element.scrollTop)).toBe(position);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
});

test("the mobile list offers older conversations only while another page exists", async ({ page, request }) => {
  const current = Array.from({ length: 100 }, (_, index) => ({ ...panelRows[0], id: `current-${index}`, title: `Current ${index + 1}` }));
  await request.post("/seed-panel", { data: { current, older: [{ ...panelRows[1], id: "older-chat", title: "Older conversation" }] } });
  await page.goto("/?host=dashboard&surface=panel");
  const more = page.getByRole("button", { name: "Load earlier conversations" });
  await expect(more).toBeVisible();
  await more.click();
  await expect(page.getByRole("button", { name: /Older conversation/ })).toBeVisible();
  await expect(more).toHaveCount(0);
});

test("desktop retains the list and selected chat together", async ({ page }) => {
  await page.setViewportSize({ width: 1200, height: 800 });
  await page.goto("/?host=dashboard&surface=panel");
  await expect(page.getByRole("button", { name: /Client onboarding/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Client onboarding" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Back to conversations" })).toHaveCount(0);
});

test("mobile new conversation opens its detail, and inbox returns to inbox", async ({ page }) => {
  await page.goto("/?host=dashboard&surface=panel");
  await page.getByRole("button", { name: "New conversation" }).click();
  const dialog = page.getByRole("dialog", { name: "New conversation" });
  await dialog.getByRole("checkbox", { name: "Assistant", exact: true }).check();
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "New conversation" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Back to conversations" })).toBeVisible();
  await page.getByRole("button", { name: "Back to conversations" }).click();
  await expect(page.getByRole("button", { name: /New conversation Assistant/ })).toBeVisible();

  await page.getByRole("button", { name: /Inbox/ }).click();
  await page.getByRole("button", { name: "Open the conversation" }).first().click();
  await expect(page.getByRole("button", { name: "Back to inbox" })).toBeVisible();
  await page.getByRole("button", { name: "Back to inbox" }).click();
  await expect(page.getByText("Daily report")).toBeVisible();
});

test("mobile overflow keeps Telegram and archived chats off the primary tabs", async ({ page }) => {
  await page.goto("/?host=dashboard&surface=panel");
  await expect(page.getByRole("navigation", { name: "Conversations" }).getByRole("button")).toHaveCount(2);
  await page.getByRole("button", { name: "More options" }).click();
  await page.getByRole("button", { name: "Telegram" }).click();
  await expect(page.getByRole("button", { name: "Back to conversations" })).toBeVisible();
  await page.getByRole("button", { name: "Back to conversations" }).click();
  await expect(page.getByRole("button", { name: /Client onboarding/ })).toBeVisible();
  await page.getByRole("button", { name: "More options" }).click();
  await page.getByRole("button", { name: "Archived conversations" }).click();
  await expect(page.getByText("No archived conversations")).toBeVisible();
  await page.getByRole("button", { name: "More options" }).click();
  await page.getByRole("button", { name: "Back to active conversations" }).click();
  await expect(page.getByRole("button", { name: /Client onboarding/ })).toBeVisible();
});

test("the exported chat widget does not acquire panel navigation", async ({ page }) => {
  await page.goto("/?host=dashboard");
  await expect(page.getByRole("button", { name: "Back to conversations" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "More options" })).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Conversations" })).toHaveCount(0);
});
