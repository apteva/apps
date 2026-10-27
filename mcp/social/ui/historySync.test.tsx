import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { syncAccountHistory, historySyncMessage } from "./historySync";
import { NativePostObservation } from "./NativePostObservation";
const browser = new Window({ url: "http://localhost:3000" });
const originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;
function setup() {
  Object.assign(globalThis, {
    window: browser,
    document: browser.document,
    navigator: browser.navigator,
    HTMLElement: browser.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
}
afterEach(async () => {
  setup();
  if (root) await act(async () => root!.unmount());
  root = undefined;
  browser.document.body.innerHTML = "";
  browser.sessionStorage.clear();
  globalThis.fetch = originalFetch;
  await browser.happyDOM.abort();
});
test("explicit account history follows Social cursors and keeps requests project scoped", async () => {
  setup();
  const calls: { url: URL; body: any }[] = [];
  globalThis.fetch = (async (url, init) => {
    calls.push({
      url: new URL(String(url), "http://localhost"),
      body: JSON.parse(String(init!.body)),
    });
    return Response.json(
      calls.length === 1
        ? {
            status: "partial",
            unique_posts: 100,
            continuation_cursor: "opaque-social-cursor",
          }
        : {
            status: "complete",
            imported: 299,
            updated: 28,
            observed_at: "2026-10-27T08:00:00Z",
            coverage: { complete: true },
            continuation_cursor: null,
          },
    );
  }) as typeof fetch;
  const r = await syncAccountHistory(42, "project");
  expect(calls).toHaveLength(2);
  expect(calls[1].body).toEqual({
    mode: "full",
    max_pages: 2,
    cursor: "opaque-social-cursor",
  });
  expect(
    calls.every(
      (c) =>
        c.url.pathname.endsWith("/accounts/42/history-sync") &&
        c.url.searchParams.get("project_id") === "project",
    ),
  ).toBe(true);
  expect(historySyncMessage(r)).toContain("Accessible history synced");
});
test("bounded UI batches retain continuation for retry without restarting at the head", async () => {
  setup();
  let call = 0;
  let lastBody: any;
  globalThis.fetch = (async (_url, init) => {
    lastBody = JSON.parse(String(init!.body));
    call++;
    return Response.json({
      status: "partial",
      continuation_cursor: `cursor-${call}`,
      imported: call,
      unique_posts: call,
    });
  }) as typeof fetch;
  const partial = await syncAccountHistory(42, "a");
  expect(call).toBe(5);
  expect(historySyncMessage(partial)).toContain("again to continue");
  globalThis.fetch = (async (_url, init) => {
    lastBody = JSON.parse(String(init!.body));
    return Response.json({ status: "complete", continuation_cursor: null });
  }) as typeof fetch;
  await syncAccountHistory(42, "a");
  expect(lastBody.cursor).toBe("cursor-5");
  await syncAccountHistory(42, "b");
  expect(lastBody.cursor).toBeUndefined();
});
test("private metadata stays distinct from membership and unavailable fields stay unknown", async () => {
  setup();
  const host = browser.document.createElement("div");
  browser.document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <NativePostObservation
        metadata={{
          native_post_id: "v",
          observed_at: "2026-10-27T09:00:00Z",
          published_at: null,
          title: "Private video",
          body: null,
          visibility: "private",
          audience: null,
          processing_state: "processing",
          original_filename: null,
          bytes: 0,
          duration_seconds: null,
        }}
      />,
    ),
  );
  const text = browser.document.body.textContent!;
  expect(text).toContain("private");
  expect(text).toContain("Membership audienceUnknown");
  expect(text).toContain("Original filenameUnknown");
  expect(text).toContain("Original bytes0");
  expect(text).toContain("Provider publication timeUnknown");
  expect(text).toContain("not independent public access verification");
  expect(text).not.toContain("members-only");
});

test("history summary distinguishes first traversal from reconciled/provider totals and preserves limited coverage", () => {
  const receipt: any = {
    status: "complete",
    imported: 271,
    updated: 28,
    observed_at: "2026-10-27T10:00:00Z",
    continuation_cursor: null,
    coverage: {
      complete: true,
      traversal_unique_posts: 296,
      reconciled_unique_posts: 299,
      provider_total: 299,
    },
  };
  expect(historySyncMessage(receipt)).toContain(
    "299 distinct posts / 299 reported by provider; first traversal 296",
  );
  expect(
    historySyncMessage({
      ...receipt,
      status: "limited",
      coverage: {
        ...receipt.coverage,
        complete: false,
        reconciled_unique_posts: 296,
      },
      limitations: ["Unexplained provider count discrepancy"],
    }),
  ).toContain("Coverage is limited. Unexplained provider count discrepancy");
});
