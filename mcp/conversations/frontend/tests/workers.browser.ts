import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

// Helpers in the chat: the helpers one reply started read as one line, then
// one row per helper with only what it does now. Everything a helper did is
// in its detail view.
const chat = "chat-operator";
const thread = "chat-chat-operator";
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

function spawnRow(chatId = chat, id = 501, call = "spawn-1", startedAt = ago(31_000), reason = "Start a helper") {
  return { id, chat_id: chatId, agent_id: 41, thread_id: thread, call_id: call, name: "spawn", reason,
    status: "completed", started_at: startedAt, ended_at: startedAt, duration_ms: 12, revision: 2 };
}

const longPurpose = "You are a read-only research helper for the H Sites project. Find the three cheapest steel suppliers. ".repeat(6).trim();

function worker(over: Record<string, unknown> = {}) {
  return { id: 1, chat_id: chat, agent_id: 41, thread_id: "researcher", parent_thread_id: thread, spawn_call_id: "spawn-1", depth: 1,
    name: "researcher", label: "Compare supplier prices", purpose: longPurpose, state: "working", model: "large",
    current: { kind: "tool", tool: "web_search", since: ago(2000), reason: "Searching steel suppliers" }, started_at: ago(30_000), revision: 1, ...over };
}

function writer(over: Record<string, unknown> = {}) {
  return worker({ id: 3, thread_id: "writer", spawn_call_id: "spawn-2", label: "Draft the summary", name: "writer", purpose: "Write the summary.",
    current: { kind: "model", since: ago(1000) }, ...over });
}

const emit = (request: APIRequestContext, data: Record<string, unknown>, chatId = chat) =>
  request.post("/emit", { data: { chat_id: chatId, agent_id: 41, ...data } });

async function twoHelpers(page: Page, request: APIRequestContext) {
  const started = ago(31_000);
  await emit(request, { tool_activity: spawnRow(chat, 501, "spawn-1", started, "Compare supplier prices") });
  await emit(request, { tool_activity: spawnRow(chat, 502, "spawn-2", started, "Draft the summary") });
  await emit(request, { worker: worker() });
  await emit(request, { worker: writer() });
  return page.locator("[data-helper-group]");
}

for (const width of [1280, 390]) {
  test(`helpers at ${width}px: one line, one row each, detail on demand`, async ({ page, request }) => {
    await request.post("/reset");
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/?host=dashboard&theme=terminal");
    await expect(page.getByTitle("Live")).toBeVisible();

    await emit(request, { tool_activity: spawnRow() });
    // Until the helper is linked, the spawn is an ordinary tool row.
    await expect(page.locator(".chat-tool-activity")).toHaveCount(1);
    await request.post("/reset");
    await page.reload();
    await expect(page.getByTitle("Live")).toBeVisible();

    const group = await twoHelpers(page, request);
    await expect(group).toHaveCount(1);
    await expect(page.locator(".chat-tool-activity")).toHaveCount(0);
    await expect(group.locator("[data-helper-summary]")).toHaveText("2 helpers working");
    // Live helpers show their rows: the title is the spawn reason, never the
    // instructions, and the second line is only what it does now.
    const researcher = group.locator('[data-worker-thread="researcher"]');
    await expect(researcher.locator("[data-helper-title]")).toHaveText("Compare supplier prices");
    await expect(researcher.locator("[data-helper-activity]")).toHaveText("Searching steel suppliers");
    await expect(group.locator('[data-worker-thread="writer"] [data-helper-activity]')).toHaveText("Thinking");
    await expect(group.getByText(longPurpose)).toHaveCount(0);
    await expect(researcher.getByText(/^3\ds$/)).toBeVisible();

    // A helper's own helper is counted on its row.
    await emit(request, { worker: worker({ id: 2, thread_id: "fetcher", parent_thread_id: "researcher", spawn_call_id: "", depth: 2,
      name: "fetcher", label: "Fetch the price pages", purpose: "Fetch the price pages.", state: "sleeping", current: undefined, started_at: ago(10_000) }) });
    await expect(researcher.getByText("+1 helper")).toBeVisible();
    await expect(page.locator('[data-helper-group] > button, [data-helper-group] li')).toHaveCount(3);

    await request.post("/seed-worker-steps", { data: { thread_id: "researcher", steps: [
      { kind: "model", started_at: ago(29_000), ended_at: ago(26_900), duration_ms: 2100, status: "ok", model: "large" },
      { kind: "tool", tool: "web_search", reason: "Searching suppliers", started_at: ago(26_000), ended_at: ago(20_000), duration_ms: 6000,
        status: "ok", arguments: '{"q":"steel suppliers"}', result: "3 results" },
      { kind: "message", to: "main", text: "Found three suppliers", started_at: ago(19_000), status: "ok" },
      { kind: "tool", tool: "browser_open", reason: "Opening the price page", started_at: ago(18_000), status: "ended" },
    ] } });
    await researcher.click();
    const detail = page.locator('[data-helper-detail="researcher"]');
    await expect(detail).toBeVisible();
    await expect(detail.getByRole("heading", { name: "Compare supplier prices" })).toBeVisible();
    // The instructions are there, folded when long.
    await expect(detail.getByText(/^You are a read-only research helper/)).toBeVisible();
    await detail.getByRole("button", { name: "Show full" }).first().click();
    await expect(detail.getByText(longPurpose)).toBeVisible();
    await expect(detail.getByText(/Model: large · Started /)).toBeVisible();
    await expect(detail.getByText("Thought for 2.1s")).toBeVisible();
    await expect(detail.getByText("Searching suppliers")).toBeVisible();
    await expect(detail.getByText("Reported to main: Found three suppliers")).toBeVisible();
    await expect(detail.getByText('{"q":"steel suppliers"}')).toBeHidden();
    await detail.getByText("Details").first().click();
    await expect(detail.getByText('{"q":"steel suppliers"}')).toBeVisible();
    // Its own helper opens from the detail, and leads back to its parent.
    await expect(detail.locator('[data-worker-thread="fetcher"]')).toContainText("Fetch the price pages");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`helpers-detail-${width}.png`) });
    await detail.locator('[data-worker-thread="fetcher"]').click();
    const nested = page.locator('[data-helper-detail="fetcher"]');
    await expect(nested.getByRole("button", { name: "Started by Compare supplier prices" })).toBeVisible();
    await nested.getByRole("button", { name: "Started by Compare supplier prices" }).click();
    await expect(detail).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(detail).toHaveCount(0);

    // It finishes: its row says how it ended, the line counts it.
    const result = "Supplier A is the cheapest at 610 EUR per tonne.\n\n" + "Supplier B follows. ".repeat(40).trim();
    await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: new Date().toISOString(), outcome: "completed", result, revision: 2, tokens: 48_200, tool_steps: 7 }) });
    await expect(group.locator("[data-helper-summary]")).toHaveText("2 helpers · 1 finished, 1 working");
    await expect(researcher.locator("[data-helper-activity]")).toHaveText("Supplier A is the cheapest at 610 EUR per tonne.");
    await researcher.click();
    await expect(detail.getByText("Result", { exact: true })).toBeVisible();
    await expect(detail.getByText("7 tool calls · 48.2K tokens")).toBeVisible();
    await detail.getByRole("button", { name: "Show full" }).last().click();
    await expect(detail.getByRole("button", { name: "Show less" }).last()).toBeVisible();
    await page.keyboard.press("Escape");

    await emit(request, { worker: writer({ state: "done", current: undefined, ended_at: new Date().toISOString(), outcome: "completed", result: "Summary drafted.", revision: 2 }) });
    await expect(group.locator("[data-helper-summary]")).toHaveText("2 helpers finished");
    await expect(group.locator(".sr-only[role=status]")).toHaveText("2 helpers finished");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`helpers-done-${width}.png`) });

    // After a reload a finished group is one quiet line.
    await page.reload();
    const reloaded = page.locator("[data-helper-group]");
    await expect(reloaded.locator("[data-helper-summary]")).toHaveText("2 helpers finished");
    await expect(reloaded.locator("[data-worker-thread]")).toHaveCount(0);
    await reloaded.locator("button[aria-expanded]").click();
    await expect(reloaded.locator("[data-worker-thread]")).toHaveCount(2);
  });
}

test("a single helper is one row", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { tool_activity: spawnRow() });
  // Rows saved before titles were kept: the spawn reason names it.
  await emit(request, { worker: worker({ label: undefined, current: { kind: "tool", tool: "files__read_file", since: ago(1000) } }) });
  const group = page.locator("[data-helper-group]");
  await expect(group).toHaveAttribute("data-helper-count", "1");
  await expect(group.locator("[data-helper-title]")).toHaveText("Start a helper");
  await expect(group.locator("[data-helper-activity]")).toHaveText("Using read file");
  await expect(group.locator("button[aria-expanded]")).toHaveCount(0);
});

test("a helper with no title is named from its thread id, not its instructions", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { tool_activity: { ...spawnRow(), reason: "" } });
  await emit(request, { worker: worker({ label: undefined, name: "fan-care_review", state: "sleeping", current: undefined }) });
  await expect(page.locator("[data-helper-title]")).toHaveText("Fan care review");
});

test("the detail shows the last eight steps, then earlier ones on request", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { tool_activity: spawnRow() });
  await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: ago(1000), outcome: "completed" }) });
  await request.post("/seed-worker-steps", { data: { thread_id: "researcher", steps: Array.from({ length: 12 }, (_, i) => ({
    kind: "tool", tool: "web_search", reason: `Search ${i + 1}`, started_at: ago(60_000 - i * 1000), ended_at: ago(59_500 - i * 1000), duration_ms: 500, status: "ok" })) } });
  await page.locator('[data-worker-thread="researcher"]').click();
  const detail = page.locator('[data-helper-detail="researcher"]');
  await expect(detail.getByText("Search 12", { exact: true })).toBeVisible();
  await expect(detail.getByText("Search 5", { exact: true })).toBeVisible();
  await expect(detail.getByText("Search 4", { exact: true })).toHaveCount(0);
  await detail.getByRole("button", { name: "Show earlier" }).click();
  await expect(detail.getByText("Search 1", { exact: true })).toBeVisible();
});

test("an open response waiting on its helpers shows the group, not Thinking", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await emit(request, { thread_id: thread, response_progress: { phase: "thinking", run_id: "run-1", revision: 1, after_message_id: 0, started_at: ago(40_000) } });
  await expect(page.getByText("Thinking…")).toBeVisible();
  await emit(request, { tool_activity: spawnRow(chat, 501, "spawn-1", ago(30_000)) });
  await emit(request, { worker: worker() });
  await expect(page.locator("[data-helper-group]")).toBeVisible();
  await expect(page.getByText("Thinking…")).toHaveCount(0);
  await expect(page.getByText("Waiting for 1 helper…")).toHaveCount(0);
  await emit(request, { worker: worker({ state: "done", current: undefined, ended_at: new Date().toISOString(), outcome: "completed", revision: 2 }) });
  await expect(page.getByText("Thinking…")).toBeVisible();
});

test("helpers speak the conversation's language", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard&locale=fr");
  await expect(page.getByTitle("En direct")).toBeVisible();
  await twoHelpers(page, request);
  await emit(request, { worker: worker({ state: "failed", current: undefined, ended_at: ago(1000), outcome: "failed", revision: 2 }) });
  const group = page.locator("[data-helper-group]");
  await expect(group.locator("[data-helper-summary]")).toHaveText("2 assistants · 1 en cours, 1 en échec");
  await expect(group.locator('[data-worker-thread="researcher"]')).toHaveAttribute("aria-label", "Assistant : Compare supplier prices, Échec");
});

const mk = (id: string, title: string) => ({ id, project_id: "project", lead_agent_id: 41, title, kind: "direct", origin: "web", audience: "operator", created_at: "", updated_at: "", archived_at: null });

test("panel: Agent work lists main, conversation threads and helpers by title", async ({ page, request }) => {
  await request.post("/reset");
  await request.post("/seed-panel", { data: [mk("conv-ops", "Ops chat"), mk("conv-billing", "Billing chat")] });
  await request.post("/seed-agent-work", { data: { available: true, agents: [{ agent_id: 41, available: true, threads: [
    { id: "main", kind: "main", state: "waiting", next_wake_at: new Date(Date.now() + 3_600_000).toISOString() },
    { id: "chat-conv-ops", kind: "chat", parent_id: "main", state: "working", chat_id: "conv-ops", title: "Ops chat", this_conversation: true },
    { id: "chat-conv-billing", kind: "chat", parent_id: "main", state: "waiting", chat_id: "conv-billing", title: "Billing chat" },
    { id: "researcher", kind: "worker", parent_id: "main", state: "working", purpose: longPurpose, name: "researcher", label: "Compare supplier prices",
      current: { kind: "tool", tool: "web_search", since: ago(1000), reason: "Searching steel suppliers" } },
    { id: "old-helper", kind: "worker", parent_id: "main", state: "done", name: "old-helper", ended_at: ago(3_600_000), outcome: "completed" },
  ] }] } });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/?host=dashboard&surface=panel");
  await page.getByText("Ops chat", { exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Ops chat", exact: true })).toBeVisible();
  const section = page.locator("[data-agent-work]");
  await expect(section.getByText("Agent work")).toBeVisible();
  // Main waits on its helper: it says so rather than "Thinking · Next wake".
  await expect(section.locator('[data-agent-thread="main"]')).toContainText("Waiting for 1 helper");
  await expect(section.locator('[data-agent-thread="main"]')).not.toContainText("Next wake");
  await expect(section.locator('[data-agent-thread="chat-conv-ops"]')).toContainText("This conversation");
  await expect(section.locator('[data-agent-thread="researcher"]')).toContainText("Compare supplier prices");
  await expect(section.locator('[data-agent-thread="researcher"]')).toContainText("Searching steel suppliers · Main");
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

for (const width of [1280, 390]) {
  test(`panel at ${width}px: a helper opens in the side column or a drawer, and can be stopped`, async ({ page, request }) => {
    await request.post("/reset");
    await request.post("/seed-panel", { data: [mk("conv-ops", "Ops chat")] });
    await request.post("/seed-agent-work", { data: { available: true, agents: [{ agent_id: 41, available: true, threads: [
      { id: "chat-conv-ops", kind: "chat", parent_id: "main", state: "waiting", chat_id: "conv-ops", title: "Ops chat", this_conversation: true },
      { id: "researcher", kind: "worker", parent_id: "chat-conv-ops", state: "working", name: "researcher", label: "Compare supplier prices", worker_id: 1, chat_id: "conv-ops", this_conversation: true },
    ] }] } });
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/?host=dashboard&surface=panel");
    await page.getByText("Ops chat", { exact: true }).first().click();
    await expect(page.getByRole("heading", { name: "Ops chat", exact: true })).toBeVisible();
    await emit(request, { tool_activity: { ...spawnRow("conv-ops"), thread_id: "chat-conv-ops", reason: "Compare supplier prices" } }, "conv-ops");
    await emit(request, { worker: worker({ chat_id: "conv-ops", parent_thread_id: "chat-conv-ops" }) }, "conv-ops");
    await emit(request, { worker: worker({ id: 2, chat_id: "conv-ops", thread_id: "fetcher", parent_thread_id: "researcher", spawn_call_id: "", depth: 2,
      label: "Fetch the price pages", state: "working", current: { kind: "model", since: ago(1000) } }) }, "conv-ops");
    const row = page.locator('[data-helper-group] [data-worker-thread="researcher"]');
    await row.click();
    const detail = page.locator('[data-helper-detail="researcher"]');
    await expect(detail).toBeVisible();
    if (width >= 1024) {
      // The side column shows the helper; the chat stays as it was.
      await expect(page.locator("[data-helper-detail-slot]")).toBeVisible();
      await expect(page.locator("[data-helper-drawer]")).toHaveCount(0);
      await expect(page.locator("[data-agent-work]")).toHaveCount(0);
    } else {
      await expect(page.locator("[data-helper-drawer]")).toBeVisible();
    }
    await page.screenshot({ path: test.info().outputPath(`panel-helper-${width}.png`) });

    // Stopping asks first, then stops it and the helper it started.
    await detail.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(detail.getByText("Stop this helper and the helpers it started?")).toBeVisible();
    await detail.getByRole("button", { name: "Stop helper" }).click();
    await expect(detail.locator("[data-helper-activity]").first()).toContainText("Stopped from this conversation");
    await expect(detail.locator("[data-helper-stop]")).toHaveCount(0);
    const calls = await (await request.get("/requests")).json();
    expect(calls.filter((call: { path: string }) => call.path.endsWith("/workers/stop")).map((call: { body: unknown }) => call.body))
      .toEqual([{ chat_id: "conv-ops", worker_id: 1 }]);
    await page.getByRole("button", { name: "Close helper" }).click();
    await expect(detail).toHaveCount(0);
    await expect(page.locator("[data-helper-group] [data-helper-title]")).toHaveText("Compare supplier prices");
    await expect(page.locator('[data-helper-group] [data-worker-thread="researcher"] [data-helper-activity]')).toHaveText("Stopped from this conversation");

    if (width >= 1024) {
      // Agent work opens this conversation's helpers too.
      const section = page.locator("[data-agent-work]");
      await expect(section).toBeVisible();
      await section.locator('[data-agent-thread="researcher"]').click();
      await expect(detail).toBeVisible();
    }
  });
}

test("a response that took a while says how long and what it used", async ({ page, request }) => {
  await request.post("/reset");
  await page.goto("/?host=dashboard");
  await expect(page.getByTitle("Live")).toBeVisible();
  await request.post("/append-message", { data: { id: 10, conversation_id: chat, role: "user", content: "Compare the suppliers", components: [], created_at: ago(80_000) } });
  await request.post("/append-message", { data: { id: 11, conversation_id: chat, role: "agent", agent_id: 41, content: "Quick note", components: [], created_at: ago(79_000),
    metadata: { work: { duration_ms: 2400, tokens: 900 } } } });
  await request.post("/append-message", { data: { id: 12, conversation_id: chat, role: "agent", agent_id: 41, content: "Supplier A is cheapest.", components: [], created_at: ago(5000),
    metadata: { work: { duration_ms: 72_000, tokens: 48_200 } } } });
  await expect(page.locator("[data-turn-work]")).toHaveCount(1);
  await expect(page.locator("[data-turn-work]")).toHaveText("Worked for 1m 12s · 48.2K tokens");
});
