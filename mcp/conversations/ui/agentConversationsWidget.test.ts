import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  agentConversationWidgetLayout,
  fixedAgentConversationInput,
  scopedConversationListPath,
} from "./agentConversations";
import {
  isSoftBreakMetadata,
  softBreakMessageInput,
} from "./softBreak";

describe("AgentConversationsWidget scope", () => {
  test("always filters active and archived lists by the required participant", () => {
    expect(scopedConversationListPath(false, 42)).toBe("/chats?agent_id=42");
    expect(scopedConversationListPath(true, 42)).toBe("/chats?archived=1&agent_id=42");
    expect(() => scopedConversationListPath(false, 0)).toThrow("target agent");
  });

  test("creates a direct conversation with the fixed agent as participant and lead", () => {
    expect(fixedAgentConversationInput(42, "project-a", " Help me ")).toEqual({
      agent_ids: [42],
      lead_agent_id: 42,
      title: "Help me",
      audience: "operator",
      project_id: "project-a",
    });
  });

  test("owns its responsive geometry instead of depending on host Tailwind output", () => {
    expect(agentConversationWidgetLayout(true)).toEqual({
      gridTemplateColumns: "250px minmax(0,1fr)",
      gridTemplateRows: "minmax(0,1fr)",
    });
    expect(agentConversationWidgetLayout(false)).toEqual({
      gridTemplateColumns: "minmax(0,1fr)",
      gridTemplateRows: "minmax(150px,34%) minmax(0,1fr)",
    });
    const widget = readFileSync(new URL("./AgentConversationsWidget.tsx", import.meta.url), "utf8");
    expect(widget).not.toContain("md:grid-cols-[");
    expect(widget).toContain("window.matchMedia");
  });

  test("both surfaces use the same transport/controller and shared chat view", () => {
    const panel = readFileSync(new URL("./ConversationsPanel.tsx", import.meta.url), "utf8");
    const widget = readFileSync(new URL("./AgentConversationsWidget.tsx", import.meta.url), "utf8");
    const view = readFileSync(new URL("./ConversationChatView.tsx", import.meta.url), "utf8");
    expect(panel).toContain("<ConversationChatView");
    expect(panel).toContain("<ConversationChat");
    expect(widget).toContain("<ConversationChat");
    expect(widget).not.toContain("new EventSource");
    expect(widget).not.toContain("/messages?chat_id");
    expect(view).toContain("Message the agent…");
  });

  test("soft break targets the responding agent and remains an advisory message", () => {
    expect(softBreakMessageInput(41, { callId: "call-7", agentId: 43 }, "break-1")).toEqual({
      content: "Pause here and reconsider before continuing.",
      intent: "soft_break",
      client_message_id: "break-1",
      target_agent_ids: [43],
      target_call_id: "call-7",
    });
    expect(softBreakMessageInput(41, { callId: "ack-1" }, "break-2").target_agent_ids).toEqual([41]);
    expect(isSoftBreakMetadata({ intent: "soft_break" })).toBe(true);
    expect(isSoftBreakMetadata({ intent: "hard_stop" })).toBe(false);
  });

  test("shared chat UI describes a soft request rather than a hard stop", () => {
    const panel = readFileSync(new URL("./ConversationsPanel.tsx", import.meta.url), "utf8");
    const view = readFileSync(new URL("./ConversationChatView.tsx", import.meta.url), "utf8");
    expect(panel).toContain("softBreakMessageInput");
    expect(panel).toContain("Break requested");
    expect(view).toContain("Ask the agent to pause and reconsider");
    expect(view).toContain("it does not stop the agent or cancel running work");
    expect(view).not.toContain("Stop generating");
  });
});
