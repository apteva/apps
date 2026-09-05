import { afterEach, test, expect } from "bun:test";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useFetch } from "../src/hooks/useFetch.ts";
import { OrderTicket } from "../src/components/OrderTicket.tsx";
import { RiskObjectives } from "../src/components/RiskObjectives.tsx";
import type { RiskPolicy } from "../src/api/types.ts";

let root: Root;
let container: HTMLElement;
afterEach(async () => { if (root) await act(async () => root.unmount()); container?.remove(); });
function mount() { container = document.createElement("div"); document.body.append(container); root = createRoot(container); }
function deferred<T>() { let resolve!: (v: T) => void; let reject!: (e: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }

test("portfolio identity change clears actionable data and discards late responses", async () => {
  mount(); const one = deferred<{id: number}>(); const two = deferred<{id: number}>();
  function Ticket({ id }: {id: number}) { const state = useFetch(() => id === 1 ? one.promise : two.promise, [id], 0); return <button disabled={!state.data} data-id={state.data?.id ?? ""}>{state.loading ? "loading" : "ready"}</button>; }
  await act(async () => root.render(<Ticket id={1}/>));
  await act(async () => one.resolve({id: 1}));
  expect(container.querySelector("button")!.dataset.id).toBe("1");
  await act(async () => root.render(<Ticket id={2}/>));
  expect(container.querySelector("button")!.disabled).toBe(true);
  expect(container.querySelector("button")!.dataset.id).toBe("");
  await act(async () => two.reject(new Error("new account failed to load")));
  expect(container.querySelector("button")!.disabled).toBe(true);
});

test("a late previous-portfolio response cannot populate the selected portfolio", async () => {
  mount(); const one = deferred<number>(); const two = deferred<number>();
  function Data({id}: {id:number}) { const state = useFetch(() => id === 1 ? one.promise : two.promise, [id], 0); return <span>{state.data ?? "empty"}</span>; }
  await act(async () => root.render(<Data id={1}/>));
  await act(async () => root.render(<Data id={2}/>));
  await act(async () => two.resolve(2));
  await act(async () => one.resolve(1));
  expect(container.textContent).toBe("2");
});

const initialPolicy = { risk_level: "balanced", max_daily_loss_pct: 3, max_drawdown_pct: 15, max_position_pct: 25, max_gross_exposure_pct: 100, max_order_pct: 20 } as RiskPolicy;
test("performance refresh preserves an edited risk-policy draft", async () => {
  mount(); const render = (policy: RiskPolicy) => <RiskObjectives key={1} portfolioId={1} policy={policy} objectives={[]} allowedClasses={["crypto"]} onRefresh={() => {}}/>;
  await act(async () => root.render(render(initialPolicy)));
  const input = container.querySelector<HTMLInputElement>('input[type="number"]')!;
  await act(async () => {
    // The native setter models browser input rather than React's value tracker.
    Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(input, "4");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => root.render(render({ ...initialPolicy, max_daily_loss_pct: 2 })));
  expect(input.value).toBe("4");
});


test("operator retry after a network failure reuses the same order identity", async () => {
  mount(); const originalFetch = globalThis.fetch; const requests: { url: string; body: any }[] = [];
  (globalThis as any).fetch = async (url: string, options: RequestInit) => {
    requests.push({ url: String(url), body: JSON.parse(String(options.body)) });
    if (requests.length === 1) throw new Error("response lost");
    return new Response(JSON.stringify({ order_id: "same-order", status: "working" }));
  };
  try {
    await act(async () => root.render(<OrderTicket symbol="BTC-USD" portfolio={{ id: 1, status: "active", mode: "paper", execution_environment: "simulation", cash: 100000, buying_power: 100000, allowed_classes: ["crypto"] } as any} universe={[{symbol:"BTC-USD",asset_class:"crypto",price:100} as any]} positions={[]}/>));
    const textarea = container.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,"value")!.set!.call(textarea,"Small position for a carefully tested order retry.");
      textarea.dispatchEvent(new Event("input",{bubbles:true}));
    });
    const submit = Array.from(container.querySelectorAll("button")).find(b => b.textContent?.startsWith("Place "))!;
    expect(submit.disabled).toBe(false);
    await act(async () => submit.click());
    await act(async () => submit.click());
    expect(requests).toHaveLength(2);
    expect(requests[0]!.body.idempotency_key).toBeTruthy();
    expect(requests[0]!.body.idempotency_key).toBe(requests[1]!.body.idempotency_key);
    expect(requests[1]!.url).toContain("/portfolios/1/orders");
  } finally { globalThis.fetch = originalFetch; }
});
