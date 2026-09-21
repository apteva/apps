import { describe, expect, test } from "bun:test";
import {
  crmInboxAPIURL,
  crmInboxContactLabel,
  crmInboxPageLink,
  crmInboxPreferences,
  crmInboxPreview,
  crmInboxRelativeTime,
  crmPanelInitialRoute,
  type InboxItem,
} from "./inbox";

const item: InboxItem = {
  id: 42,
  contact_id: 7,
  contact_name: "Maria Gomez",
  channel: "email",
  subject: "Renewal",
  status: "open",
  priority: "urgent",
  last_activity_at: "2026-09-21T10:00:00Z",
  snippet: "Can you send the updated proposal?",
};

describe("CRM inbox widget contract", () => {
  test("normalises preferences and bounds the row limit", () => {
    expect(crmInboxPreferences({ default_status: "pending", default_channel: "whatsapp", max_conversations: 99 })).toEqual({
      defaultStatus: "pending",
      defaultChannel: "whatsapp",
      maxConversations: 20,
    });
    expect(crmInboxPreferences({ default_status: "closed", default_channel: "fax", max_conversations: 1 })).toEqual({
      defaultStatus: "open",
      defaultChannel: "all",
      maxConversations: 4,
    });
  });

  test("scopes inbox reads and serialises the channel filter", () => {
    const preferences = crmInboxPreferences({ default_channel: "email", max_conversations: 6 });
    const url = new URL(crmInboxAPIURL({ appName: "crm", projectId: "project-a", installId: 9 }, preferences), "https://example.test");
    expect(url.pathname).toBe("/api/apps/crm/inbox");
    expect(url.searchParams.get("project_id")).toBe("project-a");
    expect(url.searchParams.get("install_id")).toBe("9");
    expect(url.searchParams.get("limit")).toBe("6");
    expect(JSON.parse(url.searchParams.get("filters") || "[]")).toEqual([
      { field: "channel", op: "is", value: "email" },
    ]);
  });

  test("deep-links a row into the selected CRM inbox conversation", () => {
    const href = crmInboxPageLink({ projectId: "project-a", installId: 9 }, item);
    expect(href).toContain("/apps/crm/page?");
    const route = crmPanelInitialRoute(href.split("?")[1] || "");
    expect(route).toEqual({ tab: "inbox", conversationId: 42, status: "open" });
  });

  test("formats row labels, previews, and relative time", () => {
    expect(crmInboxContactLabel(item)).toBe("Maria Gomez");
    expect(crmInboxPreview(item)).toBe("Can you send the updated proposal?");
    expect(crmInboxRelativeTime(item.last_activity_at, Date.parse("2026-09-21T10:04:30Z"))).toBe("4m");
  });
});
