import { expect, test, type APIRequestContext } from "@playwright/test";

// A spawn row becomes a card for the helper it started: what the helper is
// doing now, its steps on demand, its own helpers and its result.
const chat = "chat-operator";
const thread = "chat-chat-operator";
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

function spawnRow(chatId = chat, id = 501, call = "spawn-1", startedAt = ago(31_000)) {
  return { id, chat_id: chatId, agent_id: 41, thread_id: thread, call_id: call, name: "spawn", reason: "Start a helper",
    status: "completed", started_at: startedAt, ended_at: startedAt, duration_ms: 12, revision: 2 };
}

function worker(over: Record<string, unknown> = {}) {
  return { id: 1, chat_id: chat, agent_id: 41, thread_id: "researcher", parent_thread_id: thread, spawn_call_id: "spawn-1", depth: 1,
    name: "researcher", purpose: "Find the three cheapest suppliers.", state: "working", model: "large",
    current: { kind: "tool", tool: "web_search", since: ago(2000) }, started_at: ago(30_000), revision: 1, ...over };
}

const emit = (request: APIRequestContext, data: Record<string, unknown>, chatId = chat) =>
  request.post("/emit", { data: { chat_id: chatId, agent_id: 41, ...data } });

for (const width of [1280, 390]) {
  test(`worker card at ${width}px: live state, steps, nested helper, result`, async ({ page, request }) => {
    await request.post("/reset");
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/?host=dashboard&theme=terminal");
    await expect(page.getByTitle("Live")).toBeVisible();

    await emit(request, { tool_activity: spawnRow() });
    // Until the helper is linked, the spawn is an ordinary tool row.
    await expect(page.locator(".chat-tool-activity")).toHaveCount(1);
    await emit(request, { worker: worker() });
    const card = page.locator('[data-worker-thread="researcher"]');
    await expect(card).toBeVisible();
    await expect(page.locator(".chat-tool-activity")).toHaveCount(0);
    await expect(card.getByText("researcher", { exact: true })).toBeVisible();
    await expect(card.getByText("Using web search")).toBeVisible();
    await expect(card.getByText(/^3\ds$/)).toBeVisible();
    await expect(card.locator(".chat-tool-copy-running")).toHaveCount(1);

    // Its own helper is counted on the collapsed card.
    await emit(request, { worker: worker({ id: 2, thread_id: "fetcher", parent_thread_id: "researcher", spawn_call_id: "", depth: 2,
      name: "fetcher", purpose: "Fetch the price pages.", state: "sleeping", current: undefined, started_at: ago(10_000) }) });
    await expect(card.getByText("+1 helper")).toBeVisible();

    await request.post("/seed-worker-steps", { data: { thread_id: "researcher", steps: [
      { kind: "model", started_at: ago(29_000), ended_at: ago(26_900), duration_ms: 2100, status: "ok", model: "large" },
      { kind: "tool", tool: "web_search", reason: "Searching suppliers", started_at: ago(26_000), ended_at: ago(20_000), duration_ms: 6000,
        status: "ok", arguments: '{"q":"steel suppliers"}', result: "3 results" },
      { kind: "message", to: "main", text: "Found three suppliers", started_at: ago(19_000), status: "ok" },
      { kind: "tool", tool: "browser_open", reason: "Opening the price page", started_at: ago(18_000), status: "ended" },
    ] } });
    await card.locator("button[aria-expanded]").first().click();
    await expect(card.locator("button[aria-expanded]").first()).toHaveAttribute("aria-expanded", "true");
    await expect(card.getByText("Find the three cheapest suppliers.").first()).toBeVisible();
    await expect(card.getByText(/Model: large · Started /)).toBeVisible();
    await expect(card.getByText("Thinking · 2.1s")).toBeVisible();
    await expect(card.getByText("Searching suppliers")).toBeVisible();
    await expect(card.getByText("Reported to main: Found three suppliers")).toBeVisible();
    await expect(card.getByText("Ended with the helper")).toBeVisible();
    // Arguments and results stay folded until asked for.
    await expect(card.getByText('{"q":"steel suppliers"}')).toBeHidden();
    await card.getByText("Details").first().click();
    await expect(card.getByText('{"q":"steel suppliers"}')).toBeVisible();
    await expect(card.getByText("3 results")).toBeVisible();
    // The nested helper is a card inside the parent card.
    const nested = card.locator('[data-worker-thread="fetcher"]');
    await expect(nested).toBeVisible();
    await expect(nested.getByText("Sleeping")).toBeVisible();
    await page.screenshot({ path: test.info().outputPath(`worker-expanded-${width}.png`) });

    // It finishes with a long result: a preview, then the whole text.
    const result = "Supplier A is the cheapest. ".repeat(40).trim();
    await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: new Date().toISOString(), outcome: "completed", result, revision: 2 }) });
    await expect(card.locator("> button").getByText("Done")).toBeVisible();
    await expect(card.locator(".sr-only[role=status]").first()).toHaveText("researcher finished");
    await expect(card.getByText("Result", { exact: true })).toBeVisible();
    await card.getByRole("button", { name: "Show full" }).click();
    await expect(card.getByRole("button", { name: "Show less" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`worker-done-${width}.png`) });

    // The card stays as a marker after a reload.
    await page.reload();
    await expect(page.locator('[data-worker-thread="researcher"]').getByText("Done")).toBeVisible();
  });
}

test("worker card shows the last eight steps, then earlier ones on request", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { tool_activity: spawnRow() });
  await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: ago(1000), outcome: "completed" }) });
  await request.post("/seed-worker-steps", { data: { thread_id: "researcher", steps: Array.from({ length: 12 }, (_, i) => ({
    kind: "tool", tool: "web_search", reason: `Search ${i + 1}`, started_at: ago(60_000 - i * 1000), ended_at: ago(59_500 - i * 1000), duration_ms: 500, status: "ok" })) } });
  const card = page.locator('[data-worker-thread="researcher"]');
  await card.locator("button[aria-expanded]").first().click();
  await expect(card.getByText("Search 12", { exact: true })).toBeVisible();
  await expect(card.getByText("Search 5", { exact: true })).toBeVisible();
  await expect(card.getByText("Search 4", { exact: true })).toHaveCount(0);
  await card.getByRole("button", { name: "Show earlier" }).click();
  await expect(card.getByText("Search 1", { exact: true })).toBeVisible();
});

test("an open response waiting on a helper says so instead of Thinking", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  const started = ago(40_000);
  await emit(request, { thread_id: thread, response_progress: { phase: "thinking", run_id: "run-1", revision: 1, after_message_id: 0, started_at: started } });
  await expect(page.getByText("Thinking…")).toBeVisible();
  await emit(request, { tool_activity: spawnRow(chat, 501, "spawn-1", ago(30_000)) });
  await emit(request, { worker: worker() });
  await expect(page.getByText("Waiting for 1 helper…")).toBeVisible();
  await expect(page.getByText("Thinking…")).toHaveCount(0);
  await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: new Date().toISOString(), outcome: "completed", revision: 2 }) });
  await expect(page.getByText("Waiting for 1 helper…")).toHaveCount(0);
  await expect(page.getByText("Thinking…")).toBeVisible();
});

test("worker card speaks the conversation's language", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard&locale=fr");
  await expect(page.getByTitle("En direct")).toBeVisible();
  await emit(request, { tool_activity: spawnRow() });
  await emit(request, { worker: worker({ state: "failed", current: undefined, ended_at: ago(1000), outcome: "failed" }) });
  const card = page.locator('[data-worker-thread="researcher"]');
  await expect(card.getByText("Échec")).toBeVisible();
  await expect(card).toHaveAttribute("aria-label", "Assistant : researcher, Échec");
});

const mk = (id: string, title: string) => ({ id, project_id: "project", lead_agent_id: 41, title, kind: "direct", origin: "web", audience: "operator", created_at: "", updated_at: "", archived_at: null });

test("panel: Agent work lists main, conversation threads and helpers", async ({ page, request }) => {
  await request.post("/reset");
  await request.post("/seed-panel", { data: [mk("conv-ops", "Ops chat"), mk("conv-billing", "Billing chat")] });
  await request.post("/seed-agent-work", { data: { available: true, agents: [{ agent_id: 41, available: true, threads: [
    { id: "main", kind: "main", state: "sleeping", next_wake_at: new Date(Date.now() + 3_600_000).toISOString() },
    { id: "chat-conv-ops", kind: "chat", parent_id: "main", state: "working", chat_id: "conv-ops", title: "Ops chat", this_conversation: true },
    { id: "chat-conv-billing", kind: "chat", parent_id: "main", state: "waiting", chat_id: "conv-billing", title: "Billing chat" },
    { id: "researcher", kind: "worker", parent_id: "chat-conv-ops", state: "working", purpose: "Find suppliers.", name: "researcher", current: { kind: "tool", tool: "web_search", since: ago(1000) } },
    { id: "old-helper", kind: "worker", parent_id: "main", state: "done", name: "old-helper", ended_at: ago(3_600_000), outcome: "completed" },
  ] }] } });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?host=dashboard&surface=panel");
  await page.getByText("Ops chat", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Ops chat", exact: true })).toBeVisible();
  const section = page.locator("[data-agent-work]");
  await expect(section.getByText("Agent work")).toBeVisible();
  await expect(section.locator('[data-agent-thread="main"]')).toContainText(/Sleeping until/);
  await expect(section.locator('[data-agent-thread="chat-conv-ops"]')).toContainText("This conversation");
  await expect(section.locator('[data-agent-thread="researcher"]')).toContainText("Using web search · Ops chat");
  // Ended threads stay folded.
  await expect(section.getByText("Ended in the last 24 hours (1)")).toBeVisible();
  await expect(section.locator('[data-agent-thread="old-helper"]')).toBeHidden();
  await page.screenshot({ path: test.info().outputPath("agent-work.png") });
  // Another conversation's thread opens that conversation.
  await section.locator('[data-agent-thread="chat-conv-billing"]').click();
  await expect(page.getByRole("heading", { name: "Billing chat", exact: true })).toBeVisible();
});

test("panel: Agent work says when the agent is paused", async ({ page, request }) => {
  await request.post("/reset");
  await request.post("/seed-panel", { data: [mk("conv-ops", "Ops chat")] });
  await request.post("/seed-agent-work", { data: { available: true, agents: [{ agent_id: 41, available: true, threads: [
    { id: "main", kind: "main", state: "paused" },
  ] }] } });
  await page.goto("/?host=dashboard&surface=panel");
  await page.getByText("Ops chat", { exact: true }).first().click();
  await expect(page.locator("[data-agent-work]").getByText("This agent is paused.", { exact: false })).toBeVisible();
});

test("panel: Show helper steps opens worker cards", async ({ page, request }) => {
  await request.post("/reset");
  await request.post("/seed-panel", { data: [mk("conv-ops", "Ops chat")] });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?host=dashboard&surface=panel");
  await page.getByText("Ops chat", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Ops chat", exact: true })).toBeVisible();
  await page.getByText("Tool display").click();
  await page.getByLabel("Show helper steps").check();
  await emit(request, { tool_activity: { ...spawnRow("conv-ops"), thread_id: "chat-conv-ops" } }, "conv-ops");
  await emit(request, { worker: worker({ chat_id: "conv-ops", parent_thread_id: "chat-conv-ops" }) }, "conv-ops");
  const card = page.locator('[data-worker-thread="researcher"]');
  await expect(card.locator("button[aria-expanded]").first()).toHaveAttribute("aria-expanded", "true");
});

// Shape seen live on dev: the result comes as the end step's text, the
// helper's own send/done calls repeat the report and the end, and the list
// only knows the model tier.
test("worker card uses the steps for the model and result, and hides send/done", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { tool_activity: spawnRow() });
  await emit(request, { worker: worker({ name: "", state: "done", current: undefined, ended_at: ago(1000), outcome: "completed", model: "large" }) });
  await request.post("/seed-worker-steps", { data: { thread_id: "researcher", steps: [
    { kind: "tool", tool: "pace", reason: "Waiting 20 seconds", started_at: ago(30_000), ended_at: ago(10_000), duration_ms: 20_000, status: "done" },
    { kind: "model", model: "deepseek-v4-1-flash", started_at: ago(9000), ended_at: ago(8000), duration_ms: 1210, status: "done" },
    { kind: "tool", tool: "send", reason: "Reporting DONE to parent", started_at: ago(7900), ended_at: ago(7900), status: "done" },
    { kind: "message", to: "main", text: "DONE", started_at: ago(7900), status: "done" },
    { kind: "tool", tool: "done", reason: "Finishing with DONE result", started_at: ago(7000), ended_at: ago(7000), status: "done" },
    { kind: "end", text: "DONE", outcome: "completed", started_at: ago(6900), status: "done" },
  ] } });
  const card = page.locator('[data-worker-thread="researcher"]');
  // No thread name: the purpose names the card, and is not repeated below.
  await expect(card.getByText("Find the three cheapest suppliers.")).toHaveCount(1);
  await card.locator("button[aria-expanded]").first().click();
  await expect(card.getByText("Waiting 20 seconds")).toBeVisible();
  await expect(card.getByText("Reported to main: DONE")).toBeVisible();
  await expect(card.getByText("Reporting DONE to parent")).toHaveCount(0);
  await expect(card.getByText("Finishing with DONE result")).toHaveCount(0);
  await expect(card.getByText(/Model: deepseek-v4-1-flash · Started /)).toBeVisible();
  await expect(card.getByText("Result", { exact: true })).toBeVisible();
  await expect(card.getByText("Find the three cheapest suppliers.")).toHaveCount(1);
});
