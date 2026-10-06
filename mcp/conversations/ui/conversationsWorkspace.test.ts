import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

describe("Conversations project workspace", () => {
  test("only the full panel opts into the host workspace rail", () => {
    const panel = readFileSync(new URL("../frontend/src/ConversationsPanel.tsx", import.meta.url), "utf8");
    const exportedWidget = readFileSync(new URL("../frontend/src/AgentConversationsWidget.tsx", import.meta.url), "utf8");
    const inboxWidget = readFileSync(new URL("../frontend/src/InboxWidget.tsx", import.meta.url), "utf8");

    expect(panel).toContain("workspaceRail: WorkspaceRail");
    expect(panel).toContain("<WorkspaceRail");
    expect(panel).toContain('context={{ app: "conversations", kind: "conversation", id: selected.id }}');
    expect(panel).toContain("threadId={selected.thread_id}");
    expect(panel).toContain("setWorkspaceOpen(true)");
    expect(panel).toContain('aria-label={t("panel.workspace")}');
    expect(exportedWidget).not.toContain("workspaceRail");
    expect(inboxWidget).not.toContain("workspaceRail");
  });

  test("retains the standalone Details column for older hosts", () => {
    const panel = readFileSync(new URL("../frontend/src/ConversationsPanel.tsx", import.meta.url), "utf8");
    expect(panel).toContain("{WorkspaceRail ? (");
    expect(panel).toContain("embedded");
    expect(panel).toContain('t("common.details")');
  });
});
