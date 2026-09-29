import "./testDom";
import { expect, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../frontend/src/client";
import { ConversationsProvider } from "../frontend/src/context";
import { applyConversationActivityFrame, ConversationActivityIndicator, useConversationActivity } from "../frontend/src/conversationActivity";

test("thread activity follows the existing scoped SSE stream and settles without polling", async () => {
  const requests: URL[] = [];
  let stream!: ReadableStreamDefaultController<Uint8Array>;
  let streamOpened!: () => void;
  const opened = new Promise<void>(resolve => { streamOpened = resolve; });
  const client = new AptevaClient({
    baseURL: "https://platform.example",
    accessToken: "operator",
    fetch: async (url) => {
      requests.push(new URL(String(url)));
      streamOpened();
      return new Response(new ReadableStream({ start(controller) { stream = controller; } }), { headers: { "Content-Type": "text/event-stream" } });
    },
  }).use(conversationsExtension(), { projectId: "project", installId: 7 });
  const node = document.createElement("div");
  document.body.appendChild(node);
  const root = createRoot(node);
  const originalHidden = Object.getOwnPropertyDescriptor(document, "hidden");
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  function Probe() {
    const active = useConversationActivity("project", 41);
    return <>
      <ConversationActivityIndicator active={active.has("first")} />
      <ConversationActivityIndicator active={active.has("second")} />
    </>;
  }
  try {
    await act(async () => root.render(<ConversationsProvider conversations={client}><Probe /></ConversationsProvider>));
    await opened;
    expect(requests).toHaveLength(1);
    expect(requests[0].pathname).toEndWith("/stream");
    expect(requests[0].searchParams.get("scope")).toBe("user");
    expect(requests[0].searchParams.get("agent_id")).toBe("41");
    await act(async () => {
      stream.enqueue(new TextEncoder().encode('event: stream\ndata: {"snapshot":true,"chat_id":"","frames":[{"chat_id":"first","response_progress":{"phase":"thinking"}}]}\n\n'));
      await new Promise(resolve => setTimeout(resolve, 10));
    });
    expect(node.querySelectorAll('[role="img"][aria-label="Working"]')).toHaveLength(1);

    await act(async () => {
      stream.enqueue(new TextEncoder().encode('event: stream\ndata: {"snapshot":true,"chat_id":"first","frames":[]}\n\n'));
      await new Promise(resolve => setTimeout(resolve, 10));
    });
    expect(node.querySelectorAll(".chat-thread-working-dot")).toHaveLength(0);
    expect(requests).toHaveLength(1);
  } finally {
    await act(async () => root.unmount());
    if (originalHidden) Object.defineProperty(document, "hidden", originalHidden);
    else Reflect.deleteProperty(document, "hidden");
    node.remove();
  }
});

test("reconnect snapshots replace stale list activity and per-chat settlements preserve other chats", () => {
  const thinking = { phase: "thinking", run_id: "ack", revision: 1, after_message_id: 1, started_at: "" } as const;
  let active = applyConversationActivityFrame(new Set(["stale"]), {
    snapshot: true, chat_id: "", frames: [{ chat_id: "first", response_progress: thinking }],
  } as any);
  expect([...active]).toEqual(["first"]);
  active = applyConversationActivityFrame(active, { snapshot: true, chat_id: "second", frames: [{ chat_id: "second", response_progress: thinking }] } as any);
  expect([...active]).toEqual(["first", "second"]);
  active = applyConversationActivityFrame(active, { snapshot: true, chat_id: "first", frames: [] } as any);
  expect([...active]).toEqual(["second"]);
});
