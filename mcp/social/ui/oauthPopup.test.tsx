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
    if (path.endsWith("/accounts/connections")) return json({connections:[]});
    if (path.endsWith("/accounts/start")) {
      expect(JSON.parse(String(init?.body)).force_new).toBe(true);
      return json({ pending_account_id: 42, authorize_url: "https://pinterest.example/authorize" });
    }
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
  await click("Connect new account");
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

test("existing account reconnect separates profile health from verified inbox access", async () => {
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  const popup = { closed: false, location: { href: "about:blank" }, close() { this.closed = true; } };
  (browser as any).open = () => popup;
  let accepted = false;
  let polls = 0;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), browser.location.origin);
    if (url.pathname.endsWith("/accounts")) return json({ accounts: [{
      id: 72, connection_id: 130, platform: "youtube", display_name: "Black Iron", status: "active",
      last_check_status: "ok", inbox_coverage: accepted ? "complete" : "needs_reconnect", reconnect_supported: true,
    }] });
    if (url.pathname.endsWith("/accounts/72/reconnect")) {
      expect(init?.method).toBe("POST");
      expect(url.searchParams.get("project_id")).toBe("test-proj");
      return json({ reconnect_id: "same-account", status: "pending", authorize_url: "https://accounts.example/authorize" });
    }
    if (url.pathname.endsWith("/accounts/72/reconnect/status")) {
      expect(JSON.parse(String(init?.body)).reconnect_id).toBe("same-account");
      polls++;
      return json({ reconnect_id: "same-account", status: accepted ? "comments_connected" : "pending" });
    }
    if (url.pathname.includes("/accounts/")) throw new Error(`Unexpected account request ${url.pathname}`);
    if (url.pathname.endsWith("/posts")) return json({ posts: [] });
    if (url.pathname.endsWith("/profiles")) return json({ profiles: [] });
    return json({});
  }) as typeof fetch;

  const host = browser.document.createElement("div");
  browser.document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<SocialPanel appName="social" installId={1} projectId="test-proj" />));
  await click("Accounts");
  expect(host.textContent).toContain("Profile check passed");
  expect(host.textContent).toContain("Comments need permission");
  await click("Reconnect");
  expect(popup.location.href).toBe("https://accounts.example/authorize");
  expect(host.textContent).not.toContain("Comment access verified");
  expect(polls).toBeGreaterThan(0);
  accepted = true;
  await act(async () => { await Bun.sleep(1100); });
  expect(host.textContent).toContain("Comment access verified");
  expect(host.textContent).not.toContain("Comments need permission");
});
