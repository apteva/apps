import { expect, test } from "bun:test";
import { eventTitle } from "./ProjectAssignments";

test("eventTitle turns topics into readable names", () => {
  expect(eventTitle("conversation.message.received")).toBe("Conversation message received");
  expect(eventTitle("contact.channel.deliverability_changed")).toBe("Contact channel deliverability changed");
  expect(eventTitle("opportunity.*")).toBe("Any opportunity event");
});
