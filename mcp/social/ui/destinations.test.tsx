import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { DestinationPicker } from "./destinations";
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
async function click(text: string) {
  const button = [...browser.document.querySelectorAll("button")].find(
    (item) => item.textContent === text,
  )!;
  expect(button).toBeDefined();
  await act(async () => button.click());
}
const capabilities = {
  kind: "board",
  label: "Board",
  list: true,
  create: true,
  set_default: true,
  privacy: ["PUBLIC", "SECRET"],
};
test("shared destination picker paginates, creates, selects and sets an account default", async () => {
  const calls: { url: URL; init?: RequestInit }[] = [];
  let value = "";
  let changed = 0;
  globalThis.fetch = (async (input, init) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({ url, init });
    if (init?.method === "POST")
      return Response.json({
        status: "ok",
        destination: { id: "new", name: "New" },
      });
    if (init?.method === "PUT") return Response.json({ status: "ok" });
    return Response.json({
      status: "ok",
      items: url.searchParams.has("cursor")
        ? [
            { id: "second", name: "Second" },
            { id: "new", name: "New" },
          ]
        : [{ id: "first", name: "First" }],
      cursor: url.searchParams.has("cursor") ? "" : "next",
      default_destination_id: "first",
    });
  }) as typeof fetch;
  await render(
    <DestinationPicker
      accountId={7}
      projectId="project"
      capabilities={capabilities}
      manage
      onChange={(id) => (value = id)}
      onDefaultChanged={() => changed++}
    />,
  );
  expect(calls.length).toBe(2);
  expect(
    calls.every(
      (call) =>
        call.url.pathname === "/api/apps/social/destinations/accounts/7" &&
        call.url.searchParams.get("project_id") === "project",
    ),
  ).toBe(true);
  const select = browser.document.querySelector(
    'select[aria-label="Select Board"]',
  )! as HTMLSelectElement;
  await act(async () => {
    select.value = "second";
    select.dispatchEvent(new browser.Event("change", { bubbles: true }));
  });
  expect(value).toBe("second");
  await click("Set as default");
  expect(changed).toBe(1);
  expect(
    JSON.parse(
      String(calls.find((call) => call.init?.method === "PUT")?.init?.body),
    ),
  ).toEqual({ destination_id: "second" });
  await click("Create board");
  const name = browser.document.querySelector(
    'input[aria-label="Destination name"]',
  )! as HTMLInputElement;
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(
      browser.HTMLInputElement.prototype,
      "value",
    )!.set!;
    setter.call(name, "New");
    name.dispatchEvent(new browser.Event("input", { bubbles: true }));
    name.dispatchEvent(new browser.Event("change", { bubbles: true }));
  });
  await click("Create");
  expect(value).toBe("new");
  expect(
    JSON.parse(
      String(calls.find((call) => call.init?.method === "POST")?.init?.body),
    ),
  ).toEqual({ name: "New", description: "", privacy: "PUBLIC" });
});
test("late destination response cannot leak across accounts or projects", async () => {
  let finish: (value: Response) => void = () => {};
  globalThis.fetch = (async (input) =>
    String(input).includes("accounts/1?")
      ? await new Promise<Response>((resolve) => (finish = resolve))
      : Response.json({
          status: "ok",
          items: [{ id: "fresh", name: "Fresh" }],
        })) as typeof fetch;
  await render(
    <DestinationPicker
      accountId={1}
      projectId="old"
      capabilities={capabilities}
    />,
  );
  await render(
    <DestinationPicker
      accountId={2}
      projectId="new"
      capabilities={capabilities}
    />,
  );
  await act(async () =>
    finish(
      Response.json({
        status: "ok",
        items: [{ id: "secret", name: "Secret old board" }],
      }),
    ),
  );
  expect(browser.document.body.textContent).toContain("Fresh");
  expect(browser.document.body.textContent).not.toContain("Secret old board");
});
test("unsupported accounts have no extra UI or requests; catalog failures are actionable", async () => {
  let calls = 0;
  globalThis.fetch = (async () => {
    calls++;
    return Response.json({
      status: "failed",
      error: "Update the installed integration catalog",
    });
  }) as typeof fetch;
  await render(<DestinationPicker accountId={1} capabilities={{}} />);
  expect(calls).toBe(0);
  expect(browser.document.body.textContent).toBe("");
  await render(<DestinationPicker accountId={2} capabilities={capabilities} />);
  expect(
    browser.document.querySelector('[role="alert"]')?.textContent,
  ).toContain("installed integration catalog");
});
