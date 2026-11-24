import { expect, test } from "@playwright/test";

// Core can spend a minute compacting a long conversation before it answers.
// The chat must say so while it happens and keep a quiet marker afterwards,
// instead of a bare "Thinking…" (prod 2026-11-24, 79 s).
for (const width of [1280, 390]) {
  test(`compaction at ${width}px: live row, then a marker with its sizes`, async ({ page, request }) => {
    await request.post("/reset");
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/?host=dashboard&theme=terminal");
    await expect(page.getByTitle("Live")).toBeVisible();
    const chat = "chat-operator";
    const started = new Date(Date.now() - 47_000).toISOString();
    const compaction = { id: 901, chat_id: chat, agent_id: 41, thread_id: "main", call_id: "compaction-1", name: "context_compaction", reason: "", status: "running", started_at: started, ended_at: "", revision: 1 };
    const emit = (item: Record<string, unknown>) => request.post("/emit", { data: { chat_id: chat, agent_id: 41, tool_activity: item } });

    await emit(compaction);
    const row = page.locator(".chat-tool-activity");
    await expect(row).toHaveCount(1);
    await expect(row.getByText("Compacting conversation memory…")).toBeVisible();
    await expect(row).toHaveAttribute("aria-label", /Compacting conversation memory…/);
    // The elapsed time counts up from the start of the compaction.
    await expect(row.getByText(/^4\d\.\ds$/)).toBeVisible();
    await expect(row.locator(".chat-tool-copy-running")).toHaveCount(1);
    await expect(row.locator("svg path[d='M4 9h16M4 15h16']")).toHaveCount(1);
    await page.screenshot({ path: test.info().outputPath(`compaction-running-${width}.png`) });

    await emit({ ...compaction, status: "completed", ended_at: new Date().toISOString(), duration_ms: 79_000, reason: "77k → 24k", revision: 2 });
    await expect(row.getByText("Conversation memory compacted")).toBeVisible();
    await expect(row.getByText("79.0s")).toBeVisible();
    await expect(row.locator(".chat-tool-copy-running")).toHaveCount(0);
    const title = await row.locator(".chat-tool-copy").first().getAttribute("title");
    expect(title).toContain("summarizes older parts");
    expect(title).toContain("Context 77k → 24k tokens");
    // The raw sizes are never the label.
    await expect(row.getByText("77k → 24k", { exact: true })).toHaveCount(0);

    // It survives a reload, like any activity row.
    await page.reload();
    await expect(page.locator(".chat-tool-activity").getByText("Conversation memory compacted")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`compaction-done-${width}.png`) });
  });
}

test("a failed compaction says so, in the conversation's language", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard&theme=terminal&locale=fr");
  await expect(page.getByTitle("En direct")).toBeVisible();
  const chat = "chat-operator";
  await request.post("/emit", { data: { chat_id: chat, agent_id: 41, tool_activity: {
    id: 902, chat_id: chat, agent_id: 41, thread_id: "main", call_id: "compaction-2", name: "context_compaction", reason: "",
    status: "failed", started_at: new Date(Date.now() - 5000).toISOString(), ended_at: new Date().toISOString(), duration_ms: 5000, revision: 2 } } });
  const row = page.locator(".chat-tool-activity");
  await expect(row.getByText("Impossible de compacter la mémoire de la conversation")).toBeVisible();
});
