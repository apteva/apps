import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";

const browser = new Window({ url: "http://localhost:3000" });
Object.assign(globalThis, {
  window: browser,
  document: browser.document,
  navigator: browser.navigator,
  localStorage: browser.localStorage,
  HTMLElement: browser.HTMLElement,
  Event: browser.Event,
  MouseEvent: browser.MouseEvent,
  IS_REACT_ACT_ENVIRONMENT: true,
});
const { createRoot } = await import("react-dom/client");
const { default: SocialPanel } = await import("./SocialPanel");

const originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  browser.document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
  await browser.happyDOM.abort();
});

const json = (value: unknown) => new Response(JSON.stringify(value), {
  headers: { "Content-Type": "application/json" },
});

async function click(label: string) {
  const button = [...browser.document.querySelectorAll("button")].find((item) => item.textContent?.includes(label));
  expect(button).toBeTruthy();
  await act(async () => button!.click());
}

test("Pinterest popup completes across origins through authenticated status polling", async () => {
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  const popup = { closed: false, location: { href: "about:blank" }, close() { this.closed = true; } };
  (browser as any).open = () => popup;
  let ready = false;
  let finalized = false;
  const requests: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), browser.location.origin).pathname;
    requests.push(path);
    if (path.endsWith("/profiles")) return json({ profiles: [] });
    if (path.endsWith("/platforms")) return json({ platforms: [{
      platform: "pinterest", display_name: "Pinterest", available: true, requires_picker: false,
    }] });
    if (path.endsWith("/accounts/start")) return json({ pending_account_id: 42, authorize_url: "https://pinterest.example/authorize" });
    if (path.endsWith("/accounts/42/oauth_status")) return json({ status: ready ? "ready" : "pending_oauth" });
    if (path.endsWith("/accounts/42/pages")) return json({ pages: [], requires_picker: false });
    if (path.endsWith("/accounts/finalize") && init?.method === "POST") {
      finalized = true;
      return json({ social_account_id: 7 });
    }
    if (path.endsWith("/accounts")) return json({ accounts: [] });
    if (path.endsWith("/posts")) return json({ posts: [] });
    if (path.endsWith("/inbox")) return json({ items: [] });
    return json({});
  }) as typeof fetch;

  const host = browser.document.createElement("div");
  browser.document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<SocialPanel appName="social" installId={1} projectId="test-proj" />));
  await click("Accounts");
  await click("+ Add account");
  await click("Direct");
  expect(popup.location.href).toBe("https://pinterest.example/authorize");
  expect(finalized).toBe(false);

  // No postMessage arrives from the other origin; closing the popup triggers
  // an authenticated request to the private pending-account status route.
  ready = true;
  popup.closed = true;
  await act(async () => { await Bun.sleep(1200); });
  expect(requests).toContain("/api/apps/social/accounts/42/oauth_status");
  expect(finalized).toBe(true);
});
