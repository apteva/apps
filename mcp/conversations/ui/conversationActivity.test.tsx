import "./testDom";
import { expect, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../frontend/src/client";
import { ConversationsProvider } from "../frontend/src/context";
import { ConversationActivityIndicator, useConversationActivity } from "../frontend/src/conversationActivity";

test("thread activity follows one scoped summary poll and settles", async () => {
  const requests: URL[] = [];
  let activeIDs = ["first"];
  const client = new AptevaClient({
    baseURL: "https://platform.example",
    accessToken: "operator",
    fetch: async (url) => {
      requests.push(new URL(String(url)));
      return Response.json({ active_conversation_ids: activeIDs });
    },
  }).use(conversationsExtension(), { projectId: "project", installId: 7 });
  const node = document.createElement("div");
  document.body.appendChild(node);
  const root = createRoot(node);
  const originalInterval = window.setInterval;
  const originalClear = window.clearInterval;
  const originalHidden = Object.getOwnPropertyDescriptor(document, "hidden");
  let tick: (() => Promise<void>) | undefined;
  window.setInterval = ((callback: () => Promise<void>) => { tick = callback; return 1; }) as typeof window.setInterval;
  window.clearInterval = (() => {}) as typeof window.clearInterval;
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
    expect(requests).toHaveLength(1);
    expect(requests[0].pathname).toEndWith("/activity-summary");
    expect(requests[0].searchParams.get("agent_id")).toBe("41");
    expect(node.querySelectorAll('[role="img"][aria-label="Working"]')).toHaveLength(1);

    activeIDs = [];
    await act(async () => { await tick?.(); });
    expect(node.querySelectorAll(".chat-thread-working-dot")).toHaveLength(0);
  } finally {
    await act(async () => root.unmount());
    window.setInterval = originalInterval;
    window.clearInterval = originalClear;
    if (originalHidden) Object.defineProperty(document, "hidden", originalHidden);
    else Reflect.deleteProperty(document, "hidden");
    node.remove();
  }
});
