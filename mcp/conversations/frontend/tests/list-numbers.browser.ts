import { expect, test } from "@playwright/test";

// Live prod replies: "- 01. Define" showed "• 1." and "1. 01. Define" showed
// "1. 1.": CommonMark reads the item's own number as a nested list.
for (const host of ["dashboard", "panel", "external", "package"]) {
  test(`${host}: list items keep their own numbers`, async ({ page, request }) => {
    await request.post("/reset");
    const chat = host === "dashboard" || host === "panel" ? "chat-operator" : "chat-visitor-a";
    if (host === "panel") await request.post("/seed-panel", { data: [{ id: chat, project_id: "project", lead_agent_id: 41, title: "Support chat", kind: "direct", audience: "operator", origin: "web", created_at: "", updated_at: "" }] });
    const at = (second: number) => new Date(Date.UTC(2026, 10, 23, 7, 38, second)).toISOString();
    await request.post("/append-message", { data: { id: 1, conversation_id: chat, role: "agent", agent_id: 41, components: [], created_at: at(0),
      content: "Created. The list is in the SaaS group:\n\n- 01. Define the concept, audience and rules (Dec 2)\n- 02. Research market, competitors and platform/ad policy risks (Dec 4)" } });
    await request.post("/append-message", { data: { id: 2, conversation_id: chat, role: "agent", agent_id: 41, components: [], created_at: at(1),
      content: "**Todos:**\n1. 01. Define the goal and scope (Nov 27)\n2. 02. Legal and platform rules check (Nov 30)" } });
    await page.goto(host === "panel" ? "/?host=dashboard&surface=panel&theme=terminal" : `/?host=${host}&theme=terminal`);
    await expect(page.getByTitle("Live")).toBeVisible();
    const items = page.locator(".chat-md li");
    await expect(items).toHaveCount(4);
    await expect(page.locator(".chat-md li ol, .chat-md li ul")).toHaveCount(0);
    expect(await items.allInnerTexts()).toEqual([
      "01. Define the concept, audience and rules (Dec 2)",
      "02. Research market, competitors and platform/ad policy risks (Dec 4)",
      "01. Define the goal and scope (Nov 27)",
      "02. Legal and platform rules check (Nov 30)",
    ]);
    await page.screenshot({ path: test.info().outputPath("list-numbers.png") });
  });
}
