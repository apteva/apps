import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
import { StorageView } from "./StorageView";

const initialWindow = new Window();
Object.assign(globalThis, { window: initialWindow, document: initialWindow.document });
const { createRoot } = await import("react-dom/client");
let browser: Window;
let root: Root;
let container: HTMLElement;
const report = {
  database_bytes: 12 * 1024 ** 3, wal_bytes: 2 * 1024 ** 2,
  reusable_bytes: 4 * 1024 ** 3, auto_vacuum: "none",
  oldest_retained: "2026-09-20T00:00:00Z", cleanup_backlog: 1001, backlog_capped: true,
  retention: { summary_days: 30, success_details_days: 7, failure_details_days: 30 },
  alerts: [{ code: "conversion_required", message: "A planned database conversion is required." }],
};
beforeEach(() => {
  browser = new Window();
  Object.assign(globalThis, { window: browser, document: browser.document, HTMLElement: browser.HTMLElement, Event: browser.Event, IS_REACT_ACT_ENVIRONMENT: true });
  container = browser.document.createElement("div") as unknown as HTMLElement;
  browser.document.body.appendChild(container as any);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  await browser.happyDOM.close();
});
async function expand() {
  await act(async () => {
    const details = container.querySelector("details")!;
    details.open = true;
    details.dispatchEvent(new browser.Event("toggle", { bubbles: true }) as any);
  });
}
test("storage loads on expansion and labels shared disk versus project backlog", async () => {
  const paths: string[] = [];
  const api = async <T,>(_method: string, path: string) => { paths.push(path); return report as T; };
  await act(async () => root.render(<StorageView api={api} />));
  expect(paths).toEqual([]);
  await expand();
  expect(paths).toEqual(["/storage"]);
  expect(container.textContent).toContain("12.0 GiB");
  expect(container.textContent).toContain("2.0 MiB");
  expect(container.textContent).toContain("4.0 GiB");
  expect(container.textContent).toContain("Cleanup backlog: 1000+");
  expect(container.textContent).toContain("cover this project");
  expect(container.textContent).toContain("Successful call details: 7 days");
  expect(container.querySelector('[role="status"]')?.textContent).toContain("planned database conversion");
});
test("storage reports unavailable metrics without inventing zero disk usage", async () => {
  const api = async <T,>(): Promise<T> => { throw new Error("storage health unavailable"); };
  await act(async () => root.render(<StorageView api={api} />));
  await expand();
  expect(container.querySelector('[role="alert"]')?.textContent).toBe("storage health unavailable");
  expect(container.textContent).not.toContain("0 B");
});
