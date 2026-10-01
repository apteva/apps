import { expect, test } from "@playwright/test";

for (const [severity, rank, color] of [["info", 1, "text-info"], ["warn", 3, "text-warn"], ["error", 4, "text-error"]] as const) {
  test(`${severity} alert uses its card icon and clears after viewing`, async ({ page, request }) => {
    await page.setViewportSize({ width: 390, height: 900 });
    await request.post("/reset");
    const chat = "chat-operator";
    const conversation = { id: chat, project_id: "project", lead_agent_id: 41, title: "Alert conversation", kind: "direct", audience: "operator", origin: "web", created_at: "", updated_at: "" };
    await request.post("/seed-panel", { data: [conversation, { ...conversation, id: "approval-chat", title: "Needs approval" }] });
    const alert = { id: 301, conversation_id: chat, agent_id: 41, role: "agent", content: "Test alert", component_kind: "alert", severity, components: [{ app: "conversations", name: "alert-card", props: { text: "Test alert", severity } }], created_at: "2026-10-01T09:10:00Z" };
    const approval = { ...alert, id: 302, conversation_id: "approval-chat", component_kind: "approval", components: [{ app: "conversations", name: "approval-card", props: { title: "Continue?", status: "pending", actions: [] } }] };
    await request.post("/append-message", { data: alert });
    let seen = false;
    // The Go test exercises the real read-mark query; these responses verify
    // the panel's visible-read callback refreshes both the icon and inbox count.
    await page.route("**/api/apps/conversations/inbox?*", route => route.fulfill({ json: { items: [{ message: approval, priority: 0 }, ...(seen ? [] : [{ message: alert, priority: 3 }])], total: seen ? 1 : 2, next_cursor: "", attention: { "approval-chat": 2, ...(seen ? {} : { [chat]: rank }) } } }));
    await page.route("**/api/apps/conversations/seen?*", route => {
      const body = route.request().postDataJSON();
      if (body.chat_id === chat && body.last_seen_id >= alert.id) seen = true;
      return route.fulfill({ json: { ok: true } });
    });
    await page.goto("/?host=dashboard&surface=panel");
    const row = page.locator(`[data-conversation-id="${chat}"]`);
    const icon = row.getByRole("img", { name: "Unread alert" });
    await expect(icon).toBeVisible();
    await expect(icon).toHaveClass(new RegExp(color));
    expect(seen).toBe(false);
    const alertPath = await icon.locator("svg path").getAttribute("d");
    await row.click();
    await expect(page.getByText("Test alert", { exact: true })).toBeVisible();
    await expect.poll(() => seen).toBe(true);
    const card = page.getByText("Test alert", { exact: true }).locator("..");
    expect(await card.locator("svg path").getAttribute("d")).toBe(alertPath);
    await page.getByRole("button", { name: "Back to conversations" }).click();
    await expect(row.getByRole("img", { name: "Unread alert" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Inbox 1" })).toBeVisible();
    await expect(page.getByRole("img", { name: "Review approval" })).toBeVisible();
    await page.reload();
    await expect(row).toBeVisible();
    await expect(row.getByRole("img", { name: "Unread alert" })).toHaveCount(0);
    await row.click();
    await expect(page.getByText("Test alert", { exact: true })).toBeVisible();
  });
}
