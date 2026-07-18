import { expect, test } from "bun:test";
import { finalizedAccountError, mcpEnvelopeError } from "./accountFlow";

test("extracts an MCP error returned with HTTP 200", () => {
  const response = {
    isError: true,
    content: [{ type: "text", text: "pending account is not ready" }],
  };
  expect(mcpEnvelopeError(response)).toBe("pending account is not ready");
  expect(finalizedAccountError(response)).toBe("pending account is not ready");
});

test("requires a finalized social account id", () => {
  expect(finalizedAccountError({ status: "ok" })).toBe("Account was not finalized.");
  expect(finalizedAccountError({ social_account_id: 17 })).toBe("");
});
