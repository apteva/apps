import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { SupportersCard, type SupporterSnapshot } from "./supporters";
const browser = new Window({ url: "http://localhost:3000" });
const originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;
function globals() { Object.assign(globalThis, { window: browser, document: browser.document, navigator: browser.navigator, HTMLElement: browser.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true }); }
afterEach(async () => { globals(); if (root) await act(async () => root!.unmount()); root = undefined; browser.document.body.innerHTML = ""; globalThis.fetch = originalFetch; await browser.happyDOM.abort(); });
async function render(node: React.ReactNode) { globals(); if (!root) { const host = browser.document.createElement("div"); browser.document.body.append(host); root = createRoot(host); } await act(async () => root!.render(node)); await act(async () => { await new Promise(r => setTimeout(r, 0)); }); }
const base: SupporterSnapshot = { social_account_id: 7, platform: "youtube", state: "available", total: 42, complete: true, levels: [{ id: "l1", name: "Supporters", count: 30 }, { id: "l2", name: "Gold", count: 12 }], observed_at: "2026-10-30T08:00:00Z" };
const text = () => browser.document.body.textContent || "";

test("available count shows the total and each level for the selected project", async () => {
  const urls: URL[] = [];
  globalThis.fetch = (async (input, init) => { urls.push(new URL(String(input), "http://localhost")); expect(init?.credentials).toBe("same-origin"); return Response.json(base); }) as typeof fetch;
  await render(<SupportersCard accountId={7} projectId="brand" />);
  expect(urls[0].pathname).toBe("/api/apps/social/supporters/accounts/7");
  expect(urls[0].searchParams.get("project_id")).toBe("brand");
  expect(text()).toContain("42");
  expect(text()).toContain("Gold");
  expect(text()).toContain("12");
});

test("missing permission says to reconnect, has no button and never shows zero", async () => {
  globalThis.fetch = (async () => Response.json({ ...base, state: "scope_missing", total: null, levels: [], reason: "Reconnect the YouTube connection in Integrations once to grant it." })) as typeof fetch;
  await render(<SupportersCard accountId={7} />);
  expect(text()).toContain("Reconnect needed");
  expect(text()).toContain("Reconnect the YouTube connection");
  expect([...browser.document.querySelectorAll("button")].map(b => b.textContent)).toEqual(["Refresh"]);
  expect(text()).not.toMatch(/(^|\D)0(\D|$)/);
});

test("access required shows the provider step without an enable button", async () => {
  globalThis.fetch = (async () => Response.json({ ...base, state: "access_required", total: null, levels: [], reason: "Ask your YouTube Partner Manager to allowlist this channel." })) as typeof fetch;
  await render(<SupportersCard accountId={7} />);
  expect(text()).toContain("Access required");
  expect(text()).toContain("Partner Manager");
  expect([...browser.document.querySelectorAll("button")].map(b => b.textContent)).toEqual(["Refresh"]);
});

test("a transient error keeps the last successful count, labelled", async () => {
  globalThis.fetch = (async () => Response.json({ ...base, state: "error", total: 40, reason: "Supporter counts could not be read." })) as typeof fetch;
  await render(<SupportersCard accountId={7} />);
  expect(text()).toContain("Could not read");
  expect(text()).toContain("last successful count");
});

test("platforms without supporters render nothing", async () => {
  globalThis.fetch = (async () => Response.json({ ...base, platform: "facebook", state: "unsupported", total: null, levels: [], reason: "Supporter counts are not available for this account." })) as typeof fetch;
  await render(<SupportersCard accountId={9} />);
  expect(text()).toBe("");
});

test("refresh asks for a new count", async () => {
  const urls: URL[] = [];
  globalThis.fetch = (async (input) => { urls.push(new URL(String(input), "http://localhost")); return Response.json(base); }) as typeof fetch;
  await render(<SupportersCard accountId={7} />);
  const button = [...browser.document.querySelectorAll("button")].find(b => b.textContent?.includes("Refresh"));
  await act(async () => { button!.click(); await new Promise(r => setTimeout(r, 0)); });
  expect(urls.at(-1)?.searchParams.get("refresh")).toBe("true");
});
