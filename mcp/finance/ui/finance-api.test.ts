import { afterEach, expect, test } from "bun:test";
import { createFinanceAPI } from "./finance-api";
const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
test("routes reads and payment writes to the mounted installation, preserving query and body", async () => {
  const calls: { url: URL; init?: RequestInit }[] = [];
  globalThis.fetch = (async (url, init) => {
    calls.push({ url: new URL(String(url), "https://example.com"), init });
    return Response.json({ ok: true });
  }) as typeof fetch;
  const first = createFinanceAPI("project one", 17);
  const second = createFinanceAPI("project two", 22);
  await first("/txns?limit=20");
  await second("/settings");
  await first("/banking/payments/submit", { method: "POST", body: '{"id":"reviewed"}' });
  expect(calls.map(c => c.url.searchParams.get("install_id"))).toEqual(["17", "22", "17"]);
  expect(calls.map(c => c.url.searchParams.get("project_id"))).toEqual(["project one", "project two", "project one"]);
  expect(calls[0].url.searchParams.get("limit")).toBe("20");
  expect(calls[2].init?.method).toBe("POST");
  expect(calls[2].init?.body).toBe('{"id":"reviewed"}');
});
test("missing panel context fails before issuing an unscoped request", async () => {
  globalThis.fetch = (() => { throw new Error("unexpected fetch"); }) as typeof fetch;
  await expect(createFinanceAPI("", 0)("/settings")).rejects.toThrow("requires a project or installation");
});
