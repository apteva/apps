import { afterAll, afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { InboxTab } from "./CrmPanel";
import type { InboxItem } from "./inbox";

const previousGlobals = {
  window: globalThis.window, document: globalThis.document,
  HTMLElement: globalThis.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: (globalThis as any).IS_REACT_ACT_ENVIRONMENT,
};
const browser = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window: browser, document: browser.document, HTMLElement: browser.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root | undefined;
afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.innerHTML = "";
});
afterAll(() => Object.assign(globalThis, previousGlobals));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const row = (id: number): InboxItem => ({
  id, contact_id: id, contact_name: `Person ${id}`, channel: "email",
  subject: `Subject ${id}`, status: "open", priority: "normal",
  last_activity_at: "2026-10-08T12:00:00Z",
});
const activity = (id: number, body = `Message ${id}`) => ({
  id: String(id), kind: "email_received", body, occurred_at: "2026-10-08T12:00:00Z",
  messaging_id: id, message_status: { id, status: "received" },
  message_addresses: { from: `person${id}@example.com`, to: ["team@example.com"] },
});
const props = { lists: [], senders: [], projectId: "alpha", draftRevision: 0,
  onOpenContact: () => {}, onOpenDraft: () => {} };

async function mount(history?: ReturnType<typeof activity>[], channel = "email") {
  let rows = [row(1), row(2)].map(item => ({ ...item, channel }));
  let inboxError = false;
  let inboxTotal = 2;
  let inboxHold: ReturnType<typeof deferred<any>> | undefined;
  const statuses = new Map<number, string>();
  const activities = new Map<number, ReturnType<typeof activity>[]>();
  if (history) activities.set(1, history);
  const activityTotals = new Map<number, number>();
  const holds = new Map<number, ReturnType<typeof deferred<any>>>();
  const calls: { method: string; path: string; params?: Record<string, string>; signal?: AbortSignal }[] = [];
  const listeners = new Map<string, (event: any) => void>();
  let afterSend: (() => void | Promise<void>) | undefined;
  (browser as any).__aptevaAppEvents = {
    subscribe: (app: string, _project: string, listener: (event: any) => void) => {
      listeners.set(app, listener);
      return () => { listeners.delete(app); };
    },
  };
  const thread = (id: number, params?: Record<string, string>) => {
    const all = activities.get(id) || [activity(id)];
    const offset = Number(params?.activity_offset || 0);
    const limit = Math.min(500, Number(params?.activity_limit || 500));
    return {
    conversation: { id, contact_id: id, channel, subject: `Subject ${id}`,
      status: statuses.get(id) || "open", priority: "normal",
      started_at: "2026-10-08T12:00:00Z", last_activity_at: "2026-10-08T12:00:00Z" },
    activities: params ? all.slice(Math.max(0, all.length - offset - limit), all.length - offset) : all,
    activity_total: activityTotals.get(id) ?? all.length,
  }; };
  const api: Parameters<typeof InboxTab>[0]["api"] = async (method, path, body, params, signal) => {
    calls.push({ method, path, params, signal });
    if (path === "/inbox") {
      if (inboxError) throw new Error("Inbox offline");
      if (inboxHold) {
        const held = inboxHold;
        inboxHold = undefined;
        return await held.promise;
      }
      return { inbox: rows.map(it => ({ ...it })), total: inboxTotal } as any;
    }
    if (path === "/drafts") return { drafts: [], total: 0 } as any;
    if (path === "/messaging/reply-route") return { from: "+15550000001", to: "+15550000002" } as any;
    if (path === "/messaging/whatsapp-session") return {
      active: true, checked_at: "2026-10-08T12:00:00Z", expires_at: "2026-10-08T13:00:00Z",
    } as any;
    if (path.endsWith("/unsubscribe")) return { address: "person@example.com", outbound_blocked: false } as any;
    if (path.endsWith("/status")) {
      const id = Number(path.split("/")[4]);
      statuses.set(id, body.status);
      rows = rows.filter(it => it.id !== id);
      inboxTotal = rows.length;
      return {} as any;
    }
    const match = path.match(/^\/contacts\/(\d+)\/conversations\/(\d+)$/);
    if (match) {
      const id = Number(match[2]);
      const held = holds.get(id);
      holds.delete(id);
      // Intentionally ignore cancellation to verify response identity guards.
      return (held ? await held.promise : thread(id, params)) as any;
    }
    const contact = path.match(/^\/contacts\/(\d+)$/);
    if (contact) return { contact: { id: contact[1], display_name: `Person ${contact[1]}`, channels: [{ kind: "email", value: `person${contact[1]}@example.com` }] } } as any;
    throw new Error(`Unexpected API: ${path}`);
  };
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<InboxTab {...props} api={api} onReply={(_contact, _activity, _conversation, callback) => { afterSend = callback; }} />);
  });
  return {
    host, calls, activities, activityTotals, thread, statuses, listeners,
    setRows: (next: InboxItem[], total = next.length) => { rows = next; inboxTotal = total; },
    failInbox: () => { inboxError = true; },
    holdInbox: () => { const held = deferred<any>(); inboxHold = held; return held; },
    holdThread: (id = 1) => { const held = deferred<any>(); holds.set(id, held); return held; },
    threadCalls: () => calls.filter(call => /\/conversations\/\d+$/.test(call.path)),
    emit: (app: string, topic: string, data: object) => listeners.get(app)?.({ topic, data }),
    send: () => afterSend?.(),
  };
}

function button(host: Element, label: string) {
  const found = [...host.querySelectorAll("button")].find(node => node.textContent === label);
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}
async function click(node: Element) { await act(async () => (node as HTMLElement).click()); }
async function flushEvents() { await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); }); }
const messageNode = (host: Element) => host.querySelector("main .whitespace-pre-wrap");

test("slow background refresh preserves message DOM, scroll and collapsed drafts", async () => {
  const h = await mount();
  const message = messageNode(h.host);
  const main = h.host.querySelector("main")!;
  expect(main.querySelector('[data-message-direction="incoming"] header')!.textContent).toContain("Contact · Person 1");
  main.scrollTop = 120;
  await click(button(main, "▾ Saved reply drafts (0)"));
  const drafts = button(main, "▸ Saved reply drafts");
  const held = h.holdThread();
  await click(button(main, "Refresh"));
  expect(messageNode(h.host)).toBe(message);
  expect(main.textContent).not.toContain("Loading thread");
  expect(main.scrollTop).toBe(120);
  await act(async () => { held.resolve(h.thread(1)); });
  expect(messageNode(h.host)).toBe(message);
  expect(button(main, "▸ Saved reply drafts")).toBe(drafts);
  expect(main.scrollTop).toBe(120);
});

test("same-ID inbox metadata refresh does not fetch or unmount the thread", async () => {
  const h = await mount();
  const message = messageNode(h.host);
  h.setRows([{ ...row(1), subject: "Updated subject" }, row(2)]);
  await click(button(h.host, "Refresh")); // Queue refresh, not thread Refresh.
  expect(h.threadCalls()).toHaveLength(1);
  expect(messageNode(h.host)).toBe(message);
  expect(h.host.querySelector("aside")!.textContent).toContain("Updated subject");
});

test("reply keeps Pending conversation visible outside filters, navigation removes it", async () => {
  const h = await mount();
  const message = messageNode(h.host);
  await click(button(h.host.querySelector("main")!, "Reply"));
  h.statuses.set(1, "pending");
  h.activities.set(1, [activity(1), { ...activity(3, "Sent reply"), kind: "email_sent" }]);
  h.activityTotals.set(1, 2);
  h.setRows([row(2)]);
  await act(async () => { await h.send(); });
  expect(messageNode(h.host)).toBe(message);
  const current = h.host.querySelector("aside [aria-current=true]")!;
  expect(current.textContent).toContain("Person 1");
  expect(current.textContent).toContain("pending");
  expect(current.textContent).toContain("Currently viewing");
  expect(h.host.querySelector("main")!.textContent).toContain("Sent reply");
  expect(h.host.textContent).toContain("1 of 1");
  const other = [...h.host.querySelectorAll("aside li")].find(node => node.textContent?.includes("Person 2"))!;
  await click(other);
  expect(h.host.querySelector("aside")!.textContent).not.toContain("Person 1");
  expect(h.host.querySelector("main")!.textContent).toContain("Message 2");
});

test("empty Open queue retains current thread after status update", async () => {
  const h = await mount();
  h.setRows([row(1)]);
  await click(button(h.host.querySelector("main")!, "Pending"));
  expect(h.host.querySelector("aside [aria-current=true]")!.textContent).toContain("pending");
  expect(h.host.querySelector("main")!.textContent).toContain("Message 1");
  expect(h.host.querySelector("main")!.textContent).not.toContain("Loading thread");
  expect(h.host.querySelector("aside")!.textContent).not.toContain("Nothing here");
});

test("refresh failures retain both snapshots and allow inline thread retry", async () => {
  const h = await mount();
  const message = messageNode(h.host);
  const held = h.holdThread();
  await click(button(h.host.querySelector("main")!, "Refresh"));
  await act(async () => { held.reject(new Error("Thread offline")); });
  expect(messageNode(h.host)).toBe(message);
  expect(h.host.querySelector("main [role=alert]")!.textContent).toContain("last loaded messages");
  await click(button(h.host, "Retry"));
  expect(messageNode(h.host)).toBe(message);
  expect(h.host.querySelector("main [role=alert]")).toBeNull();
  h.failInbox();
  await click(button(h.host, "Refresh"));
  expect(h.host.querySelector("aside")!.textContent).toContain("Inbox offline");
  expect(h.host.querySelector("aside")!.querySelectorAll("li")).toHaveLength(2);
  expect(messageNode(h.host)).toBe(message);
});

test("switching threads immediately hides old contact and ignores late responses", async () => {
  const h = await mount();
  const oldRequest = h.holdThread(1);
  await click(button(h.host.querySelector("main")!, "Refresh"));
  const newRequest = h.holdThread(2);
  await click(h.host.querySelectorAll("aside li")[1]!);
  expect(h.host.querySelector("main")!.textContent).toContain("Loading thread");
  expect(h.host.querySelector("main")!.textContent).not.toContain("Message 1");
  expect(h.host.querySelectorAll("aside")[1]!.textContent).not.toContain("Person 1");
  await act(async () => { oldRequest.resolve(h.thread(1)); });
  expect(h.host.querySelector("main")!.textContent).not.toContain("Message 1");
  await act(async () => { newRequest.resolve(h.thread(2)); });
  expect(h.host.querySelector("main")!.textContent).toContain("Message 2");
  expect(h.host.querySelectorAll("aside")[1]!.textContent).toContain("Person 2");
});

test("CRM and Messaging bursts coalesce and never abort a slow live refresh", async () => {
  const h = await mount();
  const message = messageNode(h.host);
  const held = h.holdThread();
  await act(async () => {
    for (let i = 0; i < 6; i++) {
      h.emit("crm", "contact.activity.added", { contact_id: 1, conversation_id: 1 });
      h.emit("messaging", "message.delivered", { message_id: 1 });
    }
  });
  await flushEvents();
  expect(h.threadCalls()).toHaveLength(2);
  const signal = h.threadCalls().at(-1)!.signal!;
  await act(async () => {
    h.emit("crm", "conversation.status.changed", { conversation_id: 1 });
    h.emit("messaging", "message.opened", { message_id: 1 });
  });
  await flushEvents();
  expect(h.threadCalls()).toHaveLength(2);
  expect(signal.aborted).toBe(false);
  expect(messageNode(h.host)).toBe(message);
  await act(async () => { held.resolve(h.thread(1)); });
  expect(h.threadCalls()).toHaveLength(3); // One queued follow-up, not one per event.
  expect(messageNode(h.host)).toBe(message);
});

test("unrelated CRM contact and Messaging message events do not reload the thread", async () => {
  const h = await mount();
  await act(async () => {
    h.emit("crm", "contact.channel.deliverability.changed", { contact_id: 2 });
    h.emit("messaging", "message.delivered", { message_id: 2 });
  });
  await flushEvents();
  expect(h.threadCalls()).toHaveLength(1);
});

test("unmount cancels requests, timer and both event subscriptions", async () => {
  const h = await mount();
  const held = h.holdThread();
  await click(button(h.host.querySelector("main")!, "Refresh"));
  const signal = h.threadCalls().at(-1)!.signal!;
  await act(async () => {
    h.emit("crm", "contact.activity.added", { conversation_id: 1 });
    root!.unmount(); root = undefined;
  });
  expect(signal.aborted).toBe(true);
  expect(h.listeners.size).toBe(0);
  await act(async () => { held.resolve(h.thread(1)); });
  await flushEvents();
  expect(h.threadCalls()).toHaveLength(2);
});

test("background refresh preserves paged history within the API's 500-row cap", async () => {
  const h = await mount(Array.from({ length: 600 }, (_, index) => activity(index + 1)));
  const main = h.host.querySelector("main")!;
  await click(button(main, "Load older messages (200 of 600)"));
  await click(button(main, "Load older messages (400 of 600)"));
  const oldest = messageNode(h.host);
  expect(oldest!.textContent).toBe("Message 1");
  await click(button(main, "Refresh"));
  expect(messageNode(h.host)).toBe(oldest);
  expect(main.querySelectorAll(".whitespace-pre-wrap")).toHaveLength(600);
  const pages = h.threadCalls().slice(-2);
  expect(pages.map(call => call.params?.activity_limit)).toEqual(["500", "100"]);
  expect(pages.map(call => call.params?.activity_offset)).toEqual(["0", "500"]);
  h.activities.set(1, [...h.activities.get(1)!, activity(601)]);
  await click(button(main, "Refresh"));
  expect(messageNode(h.host)).toBe(oldest);
  expect(main.querySelectorAll(".whitespace-pre-wrap")).toHaveLength(601);
  expect(main.textContent).toContain("Message 601");
});

test("retained sidebar row never changes queue pagination offsets", async () => {
  const h = await mount();
  h.setRows([row(2)], 3);
  await click(button(h.host, "Refresh"));
  expect(h.host.querySelector("aside")!.textContent).toContain("Currently viewing");
  h.setRows([row(3), row(4)], 3);
  await click(button(h.host, "Load 2 more (1 of 3)"));
  expect(h.calls.filter(call => call.path === "/inbox").at(-1)!.params?.offset).toBe("1");
});

test("deliberate filter changes can clear an out-of-filter selection", async () => {
  const h = await mount();
  h.setRows([]);
  await click(button(h.host, "Refresh"));
  expect(h.host.querySelector("aside")!.textContent).toContain("Currently viewing");
  await act(async () => {
    const filter = h.host.querySelector("select")!;
    filter.value = "closed";
    filter.dispatchEvent(new browser.Event("change", { bubbles: true }) as any);
  });
  expect(h.host.querySelector("main")!.textContent).toContain("Select a conversation");
  expect(h.host.querySelector("aside")!.textContent).not.toContain("Currently viewing");
});

test("failure loading a different thread never exposes the previous snapshot", async () => {
  const h = await mount();
  const held = h.holdThread(2);
  await click(h.host.querySelectorAll("aside li")[1]!);
  await act(async () => { held.reject(new Error("Cannot load this thread")); });
  expect(h.host.querySelector("main [role=alert]")!.textContent).toContain("Cannot load this thread");
  expect(h.host.querySelector("main")!.textContent).not.toContain("Message 1");
  expect(h.host.querySelectorAll("aside")[1]!.textContent).not.toContain("Person 1");
  await click(button(h.host, "Retry"));
  expect(h.host.querySelector("main")!.textContent).toContain("Message 2");
});

test("live updates queue behind a slow sidebar refresh rather than cancelling it", async () => {
  const h = await mount();
  const held = h.holdInbox();
  await click(button(h.host, "Refresh"));
  const inboxCalls = () => h.calls.filter(call => call.path === "/inbox");
  const signal = inboxCalls().at(-1)!.signal!;
  await act(async () => { h.emit("crm", "contact.activity.added", { contact_id: 2 }); });
  await flushEvents();
  expect(inboxCalls()).toHaveLength(2);
  expect(signal.aborted).toBe(false);
  await act(async () => { held.resolve({ inbox: [row(1), row(2)], total: 2 }); });
  expect(inboxCalls()).toHaveLength(3);
  expect(h.threadCalls()).toHaveLength(1);
});

test("an event queued before a filter change refreshes using the new filter", async () => {
  const h = await mount();
  await act(async () => {
    h.emit("crm", "contact.activity.added", { contact_id: 2 });
    const filter = h.host.querySelector("select")!;
    filter.value = "pending";
    filter.dispatchEvent(new browser.Event("change", { bubbles: true }) as any);
  });
  await flushEvents();
  expect(h.calls.filter(call => call.path === "/inbox").slice(1).map(call => call.params?.status)).toEqual(["pending", "pending"]);
  expect(h.threadCalls()).toHaveLength(1);
});

test("WhatsApp live refresh keeps its reply window and messages mounted", async () => {
  const h = await mount([{ ...activity(1), kind: "whatsapp_received" }], "whatsapp");
  const main = h.host.querySelector("main")!;
  const message = messageNode(h.host);
  const notice = main.querySelector("[role=status]");
  expect(notice!.textContent).toContain("WhatsApp free-form reply available");
  const held = h.holdThread();
  await act(async () => { h.emit("messaging", "message.delivered", { message_id: 1 }); });
  await flushEvents();
  expect(messageNode(h.host)).toBe(message);
  expect(main.querySelector("[role=status]")).toBe(notice);
  expect(main.textContent).not.toContain("Loading thread");
  await act(async () => { held.resolve(h.thread(1)); });
  expect(main.querySelector("[role=status]")).toBe(notice);
  expect(notice!.textContent).toContain("WhatsApp free-form reply available");
});
