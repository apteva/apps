import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  agentConversationWidgetLayout,
  conversationDisplayMode,
  fixedAgentConversationInput,
  scopedConversationListPath,
  selectedConversationSeenInput,
  showNewConversation,
  singleConversationListPath,
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

  test("single mode uses a lead-owned, server-limited projection", () => {
    expect(singleConversationListPath(42)).toBe("/chats?lead_agent_id=42&limit=1");
    expect(singleConversationListPath(42, 50)).toBe("/chats?lead_agent_id=42&limit=50");
    expect(() => singleConversationListPath(0)).toThrow("target agent");
    expect(() => singleConversationListPath(42, 201)).toThrow("between 1 and 200");
  });

  test("single mode marks only the selected unread conversation through its latest message", () => {
    const entries = [
      { conversation_id: "older", latest_id: 17, unread: 3 },
      { conversation_id: "selected", latest_id: 29, unread: 2 },
      { conversation_id: "read", latest_id: 31, unread: 0 },
    ];
    expect(selectedConversationSeenInput(entries, "selected")).toEqual({
      chat_id: "selected",
      last_seen_id: 29,
    });
    expect(selectedConversationSeenInput(entries, "read")).toBeNull();
    expect(selectedConversationSeenInput(entries, "missing")).toBeNull();

    const widget = readFileSync(new URL("./AgentConversationsWidget.tsx", import.meta.url), "utf8");
    expect(widget).toContain('appendAgentScope("/unread-summary", instanceId)');
    expect(widget).toContain('apiPost("/seen", seen, projectId)');
  });

  test("browser remains the default and creation visibility is configurable", () => {
    expect(conversationDisplayMode()).toBe("browser");
    expect(conversationDisplayMode({ display_mode: "browser" })).toBe("browser");
    expect(conversationDisplayMode({ display_mode: "single" })).toBe("single");
    expect(showNewConversation()).toBe(true);
    expect(showNewConversation({ show_new_conversation: false })).toBe(false);
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

  test("single mode is focused, refreshes only while empty, lazy-loads history, and guards agent switches", () => {
    const widget = readFileSync(new URL("./AgentConversationsWidget.tsx", import.meta.url), "utf8");
    expect(widget).toContain("function SingleConversation");
    expect(widget).toContain("singleConversationListPath(instanceId)");
    expect(widget).toContain("singleConversationListPath(instanceId, 50)");
    expect(widget).toContain("const openHistory = async");
    expect(widget).toContain("previousEventRevision.current !== eventRevision");
    expect(widget).toContain("EMPTY_CONVERSATION_REFRESH_MS");
    expect(widget).toContain("if (selected || loading || !projectId || !validAgent) return");
    expect(widget).toContain("await loadLatest(true)");
    expect(widget).toContain("generation !== requestGeneration.current");
    expect(widget).toContain('key={`${props.projectId}:${props.instanceId}`}');
    expect(widget).toContain("headerActions={");
    expect(widget).not.toContain("/inbox");
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
