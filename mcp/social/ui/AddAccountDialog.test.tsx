import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
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
async function render(
  platforms: any[],
  started: (id: number) => void = () => {},
  close = () => {},
  projectId = "content-sites",
  reused: (pendingId: number, connectionId: number) => void = () => {},
) {
  globals();
  if (!root) {
    const host = browser.document.createElement("div");
    browser.document.body.append(host);
    root = createRoot(host);
  }
  await act(async () =>
    root!.render(
      <AddAccountDialog
        platforms={platforms}
        activeProfile={{ id: 27 } as any}
        projectId={projectId}
        onClose={close}
        setStatus={() => {}}
        onReuseExisting={reused}
        onOAuthStarted={started}
      />,
    ),
  );
}
async function click(label: string) {
  const button = [...browser.document.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  expect(button).toBeDefined();
  await act(async () => button!.click());
}
const platform = (name: string, extra = {}) => ({
  platform: name,
  display_name: name,
  integration_slug: name,
  available: true,
  requires_picker: false,
  ...extra,
});
for (const name of [
  "tiktok",
  "youtube",
  "facebook",
  "instagram",
  "pinterest",
  "twitter",
  "linkedin",
  "reddit",
  "threads",
]) {
  test(`${name}: Connect new account starts OAuth directly without listing existing accounts`, async () => {
    const popup = { location: { href: "about:blank" }, close() {} };
    browser.open = (() => popup) as any;
    const requests: { url: URL; body: any }[] = [];
    let started = 0;
    let closed = 0;
    globalThis.fetch = (async (input, init) => {
      const url = new URL(String(input), "http://localhost");
      requests.push({ url, body: JSON.parse(String(init?.body)) });
      expect(url.pathname).toBe("/api/apps/social/accounts/start");
      return Response.json({
        pending_account_id: 9,
        authorize_url: `https://${name}.example/authorize`,
      });
    }) as typeof fetch;
    await render(
      [platform(name)],
      (id) => (started = id),
      () => closed++,
    );
    expect(browser.document.body.textContent).not.toContain(
      "Use an existing connection",
    );
    expect(requests).toHaveLength(0);
    await click("Connect new account");
    expect(requests).toHaveLength(1);
    expect(requests[0].url.searchParams.get("project_id")).toBe(
      "content-sites",
    );
    expect(requests[0].body).toEqual({
      platform: name,
      force_new: true,
      profile_id: 27,
    });
    expect(started).toBe(9);
    expect(closed).toBe(1);
    expect(popup.location.href).toBe(`https://${name}.example/authorize`);
  });
}
test("environment-configured OAuth works without existing connections", async () => {
  let calls = 0;
  browser.open = (() => ({ location: { href: "" }, close() {} })) as any;
  globalThis.fetch = (async () => {
    calls++;
    return Response.json({
      pending_account_id: 9,
      authorize_url: "https://provider.example/authorize",
    });
  }) as typeof fetch;
  await render([platform("tiktok", { available: false })]);
  await click("Connect new account");
  expect(calls).toBe(1);
});
test("OAuth setup error remains visible, closes its popup and allows retry without Facebook instructions", async () => {
  let closed = 0,
    calls = 0;
  browser.open = (() => ({
    location: { href: "" },
    close() {
      closed++;
    },
  })) as any;
  globalThis.fetch = (async () => {
    calls++;
    return Response.json({
      isError: true,
      content: [
        {
          type: "text",
          text: "OAuth start failed: missing client_id for tiktok-api",
        },
      ],
    });
  }) as typeof fetch;
  await render([platform("tiktok")]);
  await click("Connect new account");
  expect(closed).toBe(1);
  expect(browser.document.body.textContent).toContain(
    "missing client_id for tiktok-api",
  );
  expect(browser.document.body.textContent).not.toContain("FACEBOOK");
  expect(browser.document.body.textContent).not.toContain(
    "developers.facebook.com",
  );
  expect(browser.document.body.textContent).toContain(
    "Existing accounts do not need to be disconnected",
  );
  await click("Connect new account");
  expect(calls).toBe(2);
  expect(closed).toBe(2);
});
test("blocked popup starts no OAuth operation", async () => {
  browser.open = (() => null) as any;
  let calls = 0;
  globalThis.fetch = (async () => {
    calls++;
    return Response.json({});
  }) as typeof fetch;
  await render([platform("tiktok")]);
  await click("Connect new account");
  expect(calls).toBe(0);
  expect(browser.document.body.textContent).toContain("Popup blocked");
});
test("incomplete authorization response never navigates or starts callback polling", async () => {
  const popup = { location: { href: "about:blank" }, close() {} };
  browser.open = (() => popup) as any;
  let started = 0;
  globalThis.fetch = (async () =>
    Response.json({
      authorize_url: "https://provider.example/authorize",
    })) as typeof fetch;
  await render([platform("tiktok")], () => started++);
  await click("Connect new account");
  expect(popup.location.href).toBe("about:blank");
  expect(started).toBe(0);
  expect(browser.document.body.textContent).toContain(
    "incomplete authorization request",
  );
});
test("Zernio retains its own connection path while direct starts fresh OAuth", async () => {
  let body: any;
  browser.open = (() => ({ location: { href: "" }, close() {} })) as any;
  globalThis.fetch = (async (_, init) => {
    body = JSON.parse(String(init?.body));
    return Response.json({
      pending_account_id: 9,
      authorize_url: "https://provider.example/connect",
    });
  }) as typeof fetch;
  await render([platform("tiktok", { zernio_available: true })]);
  await click("Zernio");
  expect(body).toEqual({
    platform: "tiktok",
    provider: "zernio",
    profile_id: 27,
  });
});

test("switching projects cancels the old login start and ignores its late response", async () => {
  const popup = {
    location: { href: "about:blank" },
    closed: false,
    close() {
      this.closed = true;
    },
  };
  browser.open = (() => popup) as any;
  let complete: (value: Response) => void = () => {};
  let signal: AbortSignal | undefined;
  globalThis.fetch = (async (_, init) => {
    signal = init?.signal as AbortSignal;
    return await new Promise<Response>((resolve) => (complete = resolve));
  }) as typeof fetch;
  let started = 0;
  await render(
    [platform("tiktok")],
    () => started++,
    () => {},
    "old-project",
  );
  await click("Connect new account");
  await render(
    [platform("tiktok")],
    () => started++,
    () => {},
    "new-project",
  );
  expect(signal?.aborted).toBe(true);
  expect(popup.closed).toBe(true);
  await act(async () =>
    complete(
      Response.json({
        pending_account_id: 9,
        authorize_url: "https://tiktok.example/authorize",
      }),
    ),
  );
  expect(started).toBe(0);
  expect(popup.location.href).toBe("about:blank");
  expect(browser.document.body.textContent).not.toContain(
    "Couldn't connect account",
  );
});

test("repeated clicks cannot create duplicate OAuth operations", async () => {
  let calls = 0,
    opened = 0;
  browser.open = (() => {
    opened++;
    return { location: { href: "" }, close() {} };
  }) as any;
  let complete: (value: Response) => void = () => {};
  globalThis.fetch = (async () => {
    calls++;
    return await new Promise<Response>((resolve) => (complete = resolve));
  }) as typeof fetch;
  await render([platform("tiktok")]);
  const button = [...browser.document.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "Connect new account",
  )!;
  await act(async () => {
    button.click();
    button.click();
  });
  expect(calls).toBe(1);
  expect(opened).toBe(1);
  await act(async () =>
    complete(
      Response.json({
        pending_account_id: 9,
        authorize_url: "https://tiktok.example/authorize",
      }),
    ),
  );
});

for (const name of ["facebook", "instagram"]) {
  test(`${name}: an existing project login is reused without a popup`, async () => {
    let opened = 0;
    browser.open = (() => { opened++; return { location: { href: "" }, close() {} }; }) as any;
    const bodies: any[] = [];
    globalThis.fetch = (async (_input, init) => {
      bodies.push(JSON.parse(String(init?.body)));
      return Response.json({ pending_account_id: 14, platform: name, reused_connection: 5 });
    }) as typeof fetch;
    let landed: number[] = [];
    let started = 0;
    await render(
      [platform(name, { requires_picker: true, reusable_connection: true })],
      () => started++,
      () => {},
      "content-sites",
      (pendingId, connectionId) => (landed = [pendingId, connectionId]),
    );
    expect(browser.document.body.textContent).toContain("Uses this project’s existing login");
    await click(name === "facebook" ? "Add page" : "Add account");
    expect(opened).toBe(0);
    expect(bodies).toEqual([{ platform: name, profile_id: 27 }]);
    expect(landed).toEqual([14, 5]);
    expect(started).toBe(0);
  });
}
test("several saved logins: the newest is reused without asking", async () => {
  browser.open = (() => { throw new Error("no popup expected"); }) as any;
  const bodies: any[] = [];
  globalThis.fetch = (async (_input, init) => {
    const body = JSON.parse(String(init?.body));
    bodies.push(body);
    if (!body.connection_id) {
      return Response.json({ status: "select_connection", connections: [{ id: 3 }, { id: 8 }, { id: 5 }] });
    }
    return Response.json({ pending_account_id: 21, reused_connection: body.connection_id });
  }) as typeof fetch;
  let landed: number[] = [];
  await render(
    [platform("facebook", { requires_picker: true, reusable_connection: true })],
    () => {},
    () => {},
    "content-sites",
    (pendingId, connectionId) => (landed = [pendingId, connectionId]),
  );
  await click("Add page");
  expect(bodies).toEqual([
    { platform: "facebook", profile_id: 27 },
    { platform: "facebook", profile_id: 27, connection_id: 8 },
  ]);
  expect(landed).toEqual([21, 8]);
});
test("a vanished saved login falls back to a fresh sign-in on the next click", async () => {
  let opened = 0;
  const popup = { location: { href: "about:blank" }, close() {} };
  browser.open = (() => { opened++; return popup; }) as any;
  const bodies: any[] = [];
  globalThis.fetch = (async (_input, init) => {
    bodies.push(JSON.parse(String(init?.body)));
    return Response.json({ pending_account_id: 30, authorize_url: "https://facebook.example/authorize" });
  }) as typeof fetch;
  let started = 0;
  await render(
    [platform("facebook", { requires_picker: true, reusable_connection: true })],
    (id) => (started = id),
  );
  await click("Add page");
  expect(opened).toBe(0);
  expect(started).toBe(0);
  expect(browser.document.body.textContent).toContain("no longer available");
  await click("Connect new account");
  expect(opened).toBe(1);
  expect(bodies[1]).toEqual({ platform: "facebook", force_new: true, profile_id: 27 });
  expect(started).toBe(30);
  expect(popup.location.href).toBe("https://facebook.example/authorize");
});
