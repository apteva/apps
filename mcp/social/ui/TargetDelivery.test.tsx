import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { TargetDelivery } from "./TargetDelivery";
const browser = new Window({ url: "http://localhost:3000" });
const original = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;
function globals() {
  Object.assign(globalThis, {
    window: browser,
    document: browser.document,
    navigator: browser.navigator,
    HTMLElement: browser.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
}
afterEach(async () => {
  globals();
  if (root) await act(async () => root!.unmount());
  root = undefined;
  browser.document.body.innerHTML = "";
  globalThis.fetch = original;
  await browser.happyDOM.abort();
});
async function render(node: React.ReactNode) {
  globals();
  if (!root) {
    const host = browser.document.createElement("div");
    browser.document.body.append(host);
    root = createRoot(host);
  }
  await act(async () => root!.render(node));
}
async function click(label: string) {
  const button = [...browser.document.querySelectorAll("button")].find(
    (b) => b.textContent === label,
  )!;
  expect(button).toBeDefined();
  await act(async () => button.click());
}
const diagnostics = {
  verified_source_bytes: 25565810,
  attempted_upload_bytes: 25565810,
  acknowledged_upload_bytes: 0,
  provider_status: "PROCESSING_UPLOAD",
  provider_observation: {
    observed_at: "2026-10-26T08:11:44Z",
    uploaded_bytes: 0,
    downloaded_bytes: null,
    query_succeeded: true,
  },
  latest_upload_error: {
    http_status: 404,
    response_excerpt: "No valid upload task",
    request_id: "upload-request",
  },
  local_diagnostic: "stalled_upload",
  automatic_polling_stopped: true,
};
test("delivery UI distinguishes acknowledged zero, unknown provider bytes and native processing", async () => {
  await render(
    <TargetDelivery
      postId={908}
      targetId={1022}
      projectId="project"
      diagnostics={diagnostics}
    />,
  );
  const text = browser.document.body.textContent!;
  expect(text).toContain("0 bytes");
  expect(text).toContain("Unknown");
  expect(text).toContain("PROCESSING_UPLOAD");
  expect(text).toContain("Upload stalled");
  expect(text).toContain(
    "Status retrieved; this alone does not confirm publication",
  );
  expect(text).toContain("HTTP 404");
  expect(text).toContain("Automatic checks paused");
  expect(text).not.toContain("Recover this target");
});
test("ambiguous recovery never offers resubmission and only addresses its target", async () => {
  const calls: string[] = [];
  globalThis.fetch = (async (input) => {
    calls.push(String(input));
    return Response.json({
      decision: "pending",
      reason: "existing operation is processing; never resubmit",
      recovery_id: "blocked",
    });
  }) as typeof fetch;
  await render(
    <TargetDelivery
      postId={908}
      targetId={1022}
      projectId="project"
      diagnostics={diagnostics}
    />,
  );
  await click("Check recovery eligibility");
  expect(calls).toEqual([
    "/api/apps/social/posts/908/delivery/1022/revalidate?project_id=project",
  ]);
  expect(browser.document.body.textContent).not.toContain(
    "Recover this target",
  );
});
test("validated recovery is a distinct confirmed action scoped to one target", async () => {
  const calls: { url: string; body: any }[] = [];
  let changed = 0;
  globalThis.fetch = (async (input, init) => {
    calls.push({
      url: String(input),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return Response.json(
      String(input).includes("revalidate")
        ? {
            decision: "retry",
            reason: "provider confirms FAILED",
            recovery_id: "proof",
          }
        : { decision: "retry", targets: [] },
    );
  }) as typeof fetch;
  await render(
    <TargetDelivery
      postId={908}
      targetId={1022}
      projectId="project"
      diagnostics={diagnostics}
      onChanged={() => changed++}
    />,
  );
  await click("Check recovery eligibility");
  expect(calls.length).toBe(1);
  await click("Recover this target");
  expect(calls[1]).toEqual({
    url: "/api/apps/social/posts/908/delivery/1022/recover?project_id=project",
    body: { recovery_id: "proof", confirm: true },
  });
  expect(changed).toBe(1);
});
test("late inspection cannot leak into another project/target", async () => {
  let finish: (response: Response) => void = () => {};
  globalThis.fetch = (async () =>
    await new Promise<Response>(
      (resolve) => (finish = resolve),
    )) as typeof fetch;
  await render(<TargetDelivery postId={1} targetId={2} projectId="old" />);
  await click("Check status");
  await render(<TargetDelivery postId={3} targetId={4} projectId="new" />);
  await act(async () =>
    finish(
      Response.json({
        delivery_diagnostics: { provider_status: "OLD_SECRET_ACCOUNT" },
      }),
    ),
  );
  expect(browser.document.body.textContent).not.toContain("OLD_SECRET_ACCOUNT");
  const button = [...browser.document.querySelectorAll("button")].find(
    (b) => b.textContent === "Check status",
  )!;
  expect(button.disabled).toBe(false);
});

test("a normalized successful acknowledgement is resolved evidence without a resend action", async () => {
  await render(
    <TargetDelivery
      postId={913}
      targetId={1034}
      projectId="project"
      diagnostics={{
        ...diagnostics,
        local_diagnostic: undefined,
        acknowledged_upload_bytes: 15864632,
        latest_upload_error: {
          http_status: 201,
          result: "invalid_acknowledgement",
        },
        upload_acknowledgement_resolution: {
          http_status: 201,
          result: "acknowledged",
        },
      }}
    />,
  );
  const text = browser.document.body.textContent!;
  expect(text).toContain("acknowledgement reconciled");
  expect(text).toContain("Previous acknowledgement warning (resolved)");
  expect(text).not.toContain("Upload stalled");
  expect(text).not.toContain("Recover this target");
});

test("expired upload review is separate from a confirmed, target-scoped recovery", async () => {
  const calls: { url: string; body: any }[] = [];
  globalThis.fetch = (async (input, init) => {
    calls.push({
      url: String(input),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return Response.json(
      String(input).includes("revalidate")
        ? {
            decision: "retry",
            recovery_kind: "expired_zero_byte_upload",
            recovery_id: "expiry-proof",
            reason: "Reviewed expiry evidence",
            expired_upload_review: {
              reason: "Stopped uploader and two fresh zero-byte observations",
            },
          }
        : { targets: [] },
    );
  }) as typeof fetch;
  await render(
    <TargetDelivery
      postId={908}
      targetId={1022}
      projectId="project"
      diagnostics={diagnostics}
    />,
  );
  await click("Review expired upload");
  expect(calls).toEqual([
    {
      url: "/api/apps/social/posts/908/delivery/1022/revalidate?project_id=project",
      body: { review_expired_upload: true },
    },
  ]);
  expect(browser.document.body.textContent).toContain(
    "Stopped uploader and two fresh zero-byte observations",
  );
  await click("Retire expired upload and recover this target");
  expect(calls[1]).toEqual({
    url: "/api/apps/social/posts/908/delivery/1022/recover?project_id=project",
    body: { recovery_id: "expiry-proof", confirm: true },
  });
});

test("unknown acknowledgement evidence never offers expiry recovery", async () => {
  await render(
    <TargetDelivery
      postId={908}
      targetId={1022}
      diagnostics={{ ...diagnostics, acknowledged_upload_bytes: null }}
    />,
  );
  expect(browser.document.body.textContent).not.toContain(
    "Review expired upload",
  );
  expect(browser.document.body.textContent).not.toContain(
    "Retire expired upload and recover this target",
  );
});
