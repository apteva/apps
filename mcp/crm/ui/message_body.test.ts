import { expect, test } from "bun:test";
import { messageDisplayBody } from "./message_body";

test("collapses email layout gaps without losing words or indentation", () => {
  expect(messageDisplayBody("email_received", "First paragraph" + "\n".repeat(104) + "  indented second\nline"))
    .toBe("First paragraph\n\n  indented second\nline");
  expect(messageDisplayBody("email_received", "A\r\n \r\n\t\r\n\u00a0\r\nB")).toBe("A\n\nB");
});

test("keeps normal paragraph spacing and authored notes/outbound text", () => {
  const body = "A\n\nB";
  expect(messageDisplayBody("email_received", body)).toBe(body);
  for (const kind of ["note", "email_sent", "sms_received"]) {
    expect(messageDisplayBody(kind, "A\n\n\n\nB")).toBe("A\n\n\n\nB");
  }
  expect(messageDisplayBody("email_received")).toBe("");
});
