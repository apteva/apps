import { expect, test } from "bun:test";
import { messageAddressLines, messageRecipientSummary } from "./message_addresses";

test("shows every recipient and separate delivery identity without guessing", () => {
  const addresses = { from: "sender@example.test", to: ["sales@example.test", "support@example.test"], cc: ["team@example.test"], bcc: ["archive@example.test"], received_at: "alias@example.test" };
  expect(messageRecipientSummary(addresses)).toBe("To: sales@example.test, support@example.test");
  expect(messageAddressLines(addresses)).toEqual([
    ["From", "sender@example.test"], ["To", "sales@example.test, support@example.test"],
    ["CC", "team@example.test"], ["BCC", "archive@example.test"], ["Received at", "alias@example.test"],
  ]);
});

test("missing historical headers remain unknown", () => {
  expect(messageRecipientSummary()).toBe("To: Unknown");
  expect(messageAddressLines()).toEqual([["From", "Unknown"], ["To", "Unknown"]]);
  expect(messageRecipientSummary({ received_at: "hidden@example.test" })).toBe("Received at: hidden@example.test");
  expect(messageAddressLines({ received_at: "hidden@example.test" })[1]).toEqual(["To", "Unknown"]);
});

test("phone transports display the recorded destination", () => {
  expect(messageRecipientSummary({ to: ["+15551230000"] })).toBe("To: +15551230000");
});
