import { expect, test } from "@playwright/test";

for (const host of ["dashboard", "panel", "external", "package"]) {
  test(`${host}: transcript spacing stays even around text, tools and cards`, async ({ page, request }) => {
    await request.post("/reset");
    const chat = host === "dashboard" || host === "panel" ? "chat-operator" : "chat-visitor-a";
    if (host === "panel") await request.post("/seed-panel", { data: [{ id: chat, project_id: "project", lead_agent_id: 41, title: "Support chat", kind: "direct", audience: "operator", origin: "web", created_at: "", updated_at: "" }] });
    const at = (second: number) => new Date(Date.UTC(2026, 9, 1, 9, 0, second)).toISOString();
    for (const [id, content, second] of [
      [1, "I’ll add this todo to Nimbus Tasks.", 0],
      [2, "Added to **Nimbus Tasks**:\n\n- Review the market intelligence report\n- [Read the details](https://example.com/report)", 2],
      [3, "I’ll move that todo’s due date to today.", 3],
    ] as const) {
      await request.post("/append-message", { data: { id, conversation_id: chat, role: "agent", agent_id: 41, content, components: [], created_at: at(second) } });
    }
    for (const [id, reason, second] of [[11, "Creating requested todo", 1], [12, "Moving todo to today", 4]] as const) {
      await request.post("/emit", { data: { chat_id: chat, tool_activity: { id, chat_id: chat, agent_id: 41, thread_id: chat, call_id: `spacing-${id}`, name: "todos_create", reason, status: "completed", started_at: at(second), ended_at: at(second), revision: 1 } } });
    }
    await request.post("/append-message", { data: { id: 4, conversation_id: chat, role: "agent", agent_id: 41, content: "", component_kind: "approval", components: [{ app: "conversations", name: "approval-card", props: { title: "Confirm the next step", status: "pending", actions: [{ id: "approve", label: "Continue", style: "primary" }] } }], created_at: at(5) } });
    await page.goto(host === "panel" ? "/?host=dashboard&surface=panel&theme=terminal" : `/?host=${host}&theme=terminal`);
    await expect(page.getByTitle("Live")).toBeVisible();
    await expect(page.locator(".chat-tool-activity")).toHaveCount(2);
    for (const width of [960, 390]) {
      await page.setViewportSize({ width, height: 1000 });
      if (host === "panel") {
        // The mobile panel opens on its conversation list after navigation.
        await page.reload();
        if (width === 390) await page.getByRole("button", { name: "Support chat agent 41" }).click();
      }
      await expect(page.locator(".chat-transcript")).toBeVisible();
      await expect(page.locator(".chat-tool-activity")).toHaveCount(2);
      const measure = () => page.locator(".chat-transcript").evaluate(container => {
        const blocks = Array.from(container.children).filter(el => el.textContent?.trim());
        const rects = blocks.map(el => el.getBoundingClientRect());
        return {
          gaps: rects.slice(1).map((rect, index) => rect.top - rects[index]!.bottom),
          // Short and list-ending replies must not hide extra space in their wrappers.
          textInsets: blocks.filter(el => el.querySelector(".chat-md")).map(el => {
            const outer = el.getBoundingClientRect();
            const inner = el.querySelector(".chat-md")!.getBoundingClientRect();
            return [inner.top - outer.top, outer.bottom - inner.bottom];
          }),
        };
      });
      const before = await measure();
      expect(before.gaps).toHaveLength(5);
      for (const gap of before.gaps) expect(gap).toBeCloseTo(16, 0);
      for (const insets of before.textInsets) expect(insets).toEqual([0, 0]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: test.info().outputPath(`spacing-${width}.png`) });
      await page.reload();
      if (host === "panel" && width === 390) await page.getByRole("button", { name: "Support chat agent 41" }).click();
      await expect(page.locator(".chat-tool-activity")).toHaveCount(2);
      expect(await measure()).toEqual(before);
    }
    const frame = { chat_id: chat, agent_id: 41, thread_id: chat, call_id: "spacing-stream", run_id: "spacing-run", text: "The update is ready.", created_at: at(6), after_message_id: 4 };
    await request.post("/emit", { data: frame });
    const reply = page.locator(".chat-md").filter({ hasText: frame.text });
    await expect(reply).toHaveCount(1);
    const textHeight = (await reply.boundingBox())!.height;
    await request.post("/emit", { data: { ...frame, text: "", done: true } });
    await request.post("/append-message", { data: { id: 5, conversation_id: chat, agent_id: 41, role: "agent", components: [], content: frame.text, created_at: at(6) } });
    await expect(reply).toHaveCount(1);
    expect((await reply.boundingBox())!.height).toBe(textHeight);
  });
}
