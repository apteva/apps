import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { AccountConnectionChooser } from "./AccountConnectionChooser";
import { AddAccountDialog } from "./SocialPanel";
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
    (item) =>
      item.textContent?.trim() === text || item.textContent?.includes(text),
  )!;
  expect(button).toBeDefined();
  await act(async () => button.click());
}
const choices = [
  {
    id: 11,
    name: "Pinterest",
    accounts: [
      {
        id: 1,
        name: "outfitencore",
        profile_id: 27,
        profile_name: "Outfit Encore",
      },
    ],
  },
  {
    id: 12,
    name: "Second Pinterest",
    accounts: [
      {
        id: 2,
        name: "secondbrand",
        profile_id: 28,
        profile_name: "Second Brand",
      },
    ],
  },
];
const platform = {
  platform: "pinterest",
  display_name: "Pinterest",
  available: true,
  requires_picker: false,
  option_fields: [],
  integration_slug: "pinterest",
} as any;
const profile = { id: 27, name: "Outfit Encore" } as any;
test("existing connection selection forwards account and profile without opening OAuth", async () => {
  const requests: { url: URL; body: any }[] = [];
  let popups = 0;
  let reused = 0;
  browser.open = (() => {
    popups++;
    return null;
  }) as typeof browser.open;
  globalThis.fetch = (async (input, init) => {
    const url = new URL(String(input), "http://localhost");
    requests.push({
      url,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return Response.json(
      url.pathname.endsWith("/start")
        ? { pending_account_id: 7, reused_connection: 12 }
        : { connections: choices },
    );
  }) as typeof fetch;
  await render(
    <AddAccountDialog
      platforms={[platform]}
      activeProfile={profile}
      projectId="content-sites"
      onClose={() => {}}
      setStatus={() => {}}
      onReuseExisting={(_, id) => (reused = id)}
      onOAuthStarted={() => {
        throw new Error("unexpected OAuth");
      }}
    />,
  );
  await click("Direct");
  expect(browser.document.body.textContent).toContain(
    "Linked to Outfit Encore",
  );
  expect(popups).toBe(0);
  await click("secondbrand");
  expect(reused).toBe(12);
  expect(popups).toBe(0);
  expect(requests[0].url.searchParams.get("platform")).toBe("pinterest");
  expect(
    requests.every(
      (request) =>
        request.url.searchParams.get("project_id") === "content-sites",
    ),
  ).toBe(true);
  expect(requests.at(-1)?.body).toEqual({
    platform: "pinterest",
    connection_id: 12,
    profile_id: 27,
  });
});
test("connect another account forces fresh OAuth, keeps selected profile and navigates the popup", async () => {
  const popup = { location: { href: "" }, close() {}, closed: false };
  let popups = 0;
  let body: any;
  let oauth = 0;
  let closed = 0;
  browser.open = (() => {
    popups++;
    return popup;
  }) as any;
  globalThis.fetch = (async (input, init) => {
    if (String(input).includes("/start")) {
      body = JSON.parse(String(init?.body));
      return Response.json({
        authorize_url: "https://pinterest.example/authorize",
        pending_account_id: 9,
      });
    }
    return Response.json({ connections: choices });
  }) as typeof fetch;
  await render(
    <AddAccountDialog
      platforms={[platform]}
      activeProfile={profile}
      projectId="content-sites"
      onClose={() => closed++}
      setStatus={() => {}}
      onReuseExisting={() => {
        throw new Error("unexpected reuse");
      }}
      onOAuthStarted={(id) => (oauth = id)}
    />,
  );
  await click("Direct");
  await click("Connect another account");
  expect(body).toEqual({
    platform: "pinterest",
    force_new: true,
    profile_id: 27,
  });
  expect(popups).toBe(1);
  expect(oauth).toBe(9);
  expect(closed).toBe(1);
  expect(popup.location.href).toBe("https://pinterest.example/authorize");
});
test("YouTube uses the same chooser and fresh-login controls", async () => {
  globalThis.fetch = (async () =>
    Response.json({ connections: [] })) as typeof fetch;
  await render(
    <AddAccountDialog
      platforms={[
        { ...platform, platform: "youtube", display_name: "YouTube" },
      ]}
      activeProfile={null}
      projectId="project"
      onClose={() => {}}
      setStatus={() => {}}
      onReuseExisting={() => {}}
      onOAuthStarted={() => {}}
    />,
  );
  await click("Direct");
  expect(
    browser.document.querySelector(
      '[aria-label="YouTube account connections"]',
    ),
  ).not.toBeNull();
  expect(browser.document.body.textContent).toContain(
    "Connect another account",
  );
});
test("connection lookup failure still allows a fresh login", async () => {
  globalThis.fetch = (async () =>
    Response.json({
      isError: true,
      content: [{ type: "text", text: "Connections unavailable" }],
    })) as typeof fetch;
  let fresh = 0;
  await render(
    <AccountConnectionChooser
      platform="pinterest"
      label="Pinterest"
      projectId="project"
      busy={false}
      onExisting={() => {}}
      onNew={() => fresh++}
      onBack={() => {}}
    />,
  );
  expect(
    browser.document.querySelector('[role="alert"]')?.textContent,
  ).toContain("Connections unavailable");
  await click("Connect another account");
  expect(fresh).toBe(1);
});
test("late choices from another project are ignored", async () => {
  let finish: (value: Response) => void = () => {};
  globalThis.fetch = (async (input) =>
    String(input).includes("project_id=old")
      ? await new Promise<Response>((resolve) => (finish = resolve))
      : Response.json({ connections: [choices[1]] })) as typeof fetch;
  const props = {
    platform: "pinterest",
    label: "Pinterest",
    busy: false,
    onExisting: () => {},
    onNew: () => {},
    onBack: () => {},
  };
  await render(<AccountConnectionChooser {...props} projectId="old" />);
  await render(<AccountConnectionChooser {...props} projectId="new" />);
  await act(async () => finish(Response.json({ connections: [choices[0]] })));
  expect(browser.document.body.textContent).toContain("secondbrand");
  expect(browser.document.body.textContent).not.toContain("outfitencore");
});
