import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatToolActivity } from "../frontend/src/ToolActivity";
import { buildToolVisualRegistry } from "../frontend/src/toolVisuals";
import type { ToolActivity } from "../frontend/src/toolActivityModel";

const registry = buildToolVisualRegistry([], [], []);
const completed: ToolActivity = {
  id: "1", callId: "call-1", agentId: 1, threadId: "thread-1",
  name: "computer_get_visible_app", reason: "Checking visible app",
  state: "done", success: true, startedAt: 1000, finishedAt: 2500, durationMs: 1500,
};

test("tool activity defaults to source icon and reason without completion or duration", () => {
  const markup = renderToStaticMarkup(<ChatToolActivity tools={[completed]} registry={registry} />);
  expect(markup).toContain("Checking visible app");
  expect(markup).toContain("chat-tool-icon");
  expect(markup).not.toContain("text-green");
  expect(markup).not.toContain("1.5s");
});

test("completion checkmarks and duration can be enabled independently", () => {
  const completion = renderToStaticMarkup(<ChatToolActivity tools={[completed]} registry={registry} showCompletion />);
  expect(completion).toContain("text-green");
  expect(completion).not.toContain("1.5s");

  const duration = renderToStaticMarkup(<ChatToolActivity tools={[completed]} registry={registry} showDuration />);
  expect(duration).not.toContain("text-green");
  expect(duration).toContain("1.5s");
});

test("failed activity remains visible in the compact layout", () => {
  const markup = renderToStaticMarkup(<ChatToolActivity tools={[{ ...completed, success: false }]} registry={registry} />);
  expect(markup).toContain("chat-tool-failed-text");
  expect(markup).toContain("failed");
});
