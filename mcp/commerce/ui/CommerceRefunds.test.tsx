import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SaleRefunds } from "./CommercePanel";

const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, HTMLElement: window.HTMLElement, sessionStorage: window.sessionStorage, IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
  sessionStorage.clear();
});
const sale = { id: 1, store_id: 1, invoice_number: "INV-1", status: "paid", payment_status: "paid", fulfillment_status: "pending", total_cents: 3750, refunded_cents: 0, currency: "USD", customer_email: "buyer@example.com", customer_name: "Buyer", created_at: "2026-10-27" };
const mount = async (api: any) => {
  const host = document.createElement("div"); document.body.append(host);
  await act(async () => { root = createRoot(host); root.render(<SaleRefunds sale={sale} scope="project:1" api={api} onSale={() => {}} />); });
  return host;
};
const button = (host: HTMLElement, text: string) => Array.from(host.querySelectorAll("button")).find((node) => node.textContent === text)!;
const submit = async (host: HTMLElement) => act(async () => { host.querySelector("form")!.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }) as unknown as Event); });

test("lost response and remount preserve the original refund amount and key", async () => {
  const posts: any[] = [];
  const api = async (_: string, init?: RequestInit) => {
    if (!init) return { requests: [] };
    const body = JSON.parse(String(init.body)); posts.push(body);
    if (posts.length === 1) throw new Error("Network response lost");
    return { request: { ...body, status: "succeeded" }, sale: { ...sale, payment_status: "refunded", refunded_cents: 3750 }, reconciled: true };
  };
  let host = await mount(api);
  await submit(host);
  expect(posts).toHaveLength(1);
  expect(button(host, "Issue refund").disabled).toBe(true);
  await act(async () => root.unmount());
  host = await mount(api);
  await act(async () => button(host, "Retry request").click());
  expect(posts).toHaveLength(2);
  expect(posts[1]).toEqual(posts[0]);
  expect(posts[0].amount_cents).toBe(3750);
  expect(host.textContent).toContain("Refund succeeded.");
  expect(sessionStorage.getItem("commerce-refund:project:1:1")).toBeNull();
});

test("pending outcomes block a new refund and are never reported as succeeded", async () => {
  let posts = 0;
  const host = await mount(async (_: string, init?: RequestInit) => {
    if (!init) return { requests: [] };
    posts++;
    return { request: { ...JSON.parse(String(init.body)), status: "submitted" }, sale, reconciled: true };
  });
  await submit(host);
  expect(posts).toBe(1);
  expect(host.textContent).toContain("Refund submitted.");
  expect(host.textContent).not.toContain("Refund succeeded.");
  expect(button(host, "Issue refund").disabled).toBe(true);
  expect(button(host, "Check status")).toBeDefined();
});

test("confirmed execution remains visible when sale reconciliation fails", async () => {
  const host = await mount(async (_: string, init?: RequestInit) => !init ? { requests: [] } : {
    request: { ...JSON.parse(String(init.body)), status: "succeeded" }, sale, reconciled: false, reconciliation_error: "Billing unavailable",
  });
  await submit(host);
  expect(host.textContent).toContain("Refund succeeded.");
  expect(host.textContent).toContain("Sale reconciliation: Billing unavailable");
  expect(button(host, "Refresh sale state")).toBeDefined();
});
