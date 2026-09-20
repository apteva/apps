import { expect, test } from "bun:test";
import {
  groupPhoneConversations,
  messagingWidgetPreferences,
  phoneMessagePeer,
  type PhoneMessage,
} from "./MessagingWidget";

function message(overrides: Partial<PhoneMessage>): PhoneMessage {
  return {
    id: 1,
    channel: "sms",
    direction: "in",
    from: "+15550000001",
    to: ["+15550000002"],
    body_text: "Hello",
    status: "received",
    created_at: "2026-09-20T10:00:00Z",
    ...overrides,
  };
}

test("finds the remote peer in inbound and outbound phone messages", () => {
  expect(phoneMessagePeer(message({ from: "whatsapp:+15551110000" }))).toBe("+15551110000");
  expect(phoneMessagePeer(message({ direction: "out", from: "+15550000002", to: ["tel:+15552220000"] }))).toBe("+15552220000");
});

test("groups SMS and WhatsApp independently and sorts newest first", () => {
  const conversations = groupPhoneConversations([
    message({ id: 1, channel: "sms", from: "+15551110000", created_at: "2026-09-20T10:00:00Z" }),
    message({ id: 2, channel: "sms", direction: "out", to: ["+15551110000"], created_at: "2026-09-20T10:01:00Z" }),
    message({ id: 3, channel: "whatsapp", from: "+15552220000", created_at: "2026-09-20T11:00:00Z" }),
    message({ id: 4, channel: "email", from: "person@example.com" }),
  ]);
  expect(conversations.map((conversation) => conversation.key)).toEqual([
    "whatsapp:+15552220000",
    "sms:+15551110000",
  ]);
  expect(conversations[1].messages.map((item) => item.id)).toEqual([1, 2]);
});

test("normalizes widget settings", () => {
  expect(messagingWidgetPreferences({ default_channel: "whatsapp", max_conversations: 100 })).toEqual({
    defaultChannel: "whatsapp",
    maxConversations: 20,
  });
  expect(messagingWidgetPreferences({ default_channel: "email", max_conversations: "nope" })).toEqual({
    defaultChannel: "all",
    maxConversations: 10,
  });
});
