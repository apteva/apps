import { test, expect } from "bun:test";
import {
  createSelectionGate,
  decodeToolResult,
  requestedUpdateVersion,
} from "./fleet-state";
test("late tenant A response cannot replace tenant B", () => {
  const gate = createSelectionGate();
  gate.select("a");
  const a = gate.request("a");
  gate.select("b");
  const b = gate.request("b");
  expect(b()).toBe(true);
  expect(a()).toBe(false);
});
test("new refresh invalidates older response for the same tenant", () => {
  const gate = createSelectionGate();
  gate.select("a");
  const first = gate.request("a");
  const second = gate.request("a");
  expect(first()).toBe(false);
  expect(second()).toBe(true);
  gate.select(null);
  expect(second()).toBe(false);
});
test("pending update uses the displayed exact target", () => {
  expect(
    requestedUpdateVersion(
      { current_version: "0.41.0", target_version: "0.41.1" },
      "0.99.0",
    ),
  ).toBe("0.41.1");
});
test("MCP tool failures remain failures", () => {
  expect(() =>
    decodeToolResult({
      result: { isError: true, content: [{ text: "provider failed" }] },
    }),
  ).toThrow("provider failed");
  expect(() => decodeToolResult({ error: { message: "RPC failure" } })).toThrow(
    "RPC failure",
  );
});
test("successful MCP JSON is decoded", () =>
  expect(
    decodeToolResult<{ ok: boolean }>({
      result: { content: [{ text: '{"ok":true}' }] },
    }),
  ).toEqual({ ok: true }));
