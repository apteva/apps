import {afterEach, describe, expect, test} from "bun:test";
import {Window} from "happy-dom";

const source = await Bun.file(new URL("./offer.js", import.meta.url)).text();
const windows: Window[] = [];
const content = {
  sender: "Studio", title: "Software build", currency: "EUR", categories: [
    {name: "Build package", rule: "any", items: [
      {name: "Core", amount_minor: 1200000, required: true},
      {name: "Stripe", amount_minor: 250000}, {name: "CSV", amount_minor: 80000},
    ]},
    {name: "Support", rule: "one_required", items: [
      {name: "Week", amount_minor: 50000}, {name: "Month", amount_minor: 150000},
    ]},
  ],
};
async function open(response?: object, language?: string) {
  const window = new Window({url: "https://example.com/offer/?project_id=demo#private-token"});
  windows.push(window);
  window.document.body.innerHTML = '<main id="app"></main>';
  const calls: any[] = [];
  window.fetch = (async (url: any, options: any) => {
    calls.push({url: String(url), options, body: JSON.parse(options.body)});
    return {ok: true, json: async () => ({content: {...content, language}, response, number: 1, expires_at: 1800000000})};
  }) as any;
  new Function("document", "location", "fetch", "window", source)(window.document, window.location, window.fetch, window);
  await new Promise(resolve => setTimeout(resolve, 0));
  const doc = window.document;
  const group = (label: string) => doc.querySelector(`[aria-label="${label}"]`)!;
  const button = (label: string, decision: string) => group(label).querySelector(`button[value="${decision}"]`)! as any;
  const total = () => doc.querySelector(".total")!.textContent!.replace(/[^0-9]/g, "");
  return {window, doc, calls, group, button, total};
}
afterEach(() => { for (const window of windows.splice(0)) window.happyDOM.abort(); });

describe("public scope buttons", () => {
  test("whole-group actions update every optional item and preserve required pricing", async () => {
    const {button, total, doc} = await open();
    const label = "Group choices for Build package";
    expect(doc.querySelectorAll("select").length).toBe(0);
    button(label, "include").click();
    expect(total()).toBe("1530000");
    expect(button("Your choice for Stripe", "include").getAttribute("aria-pressed")).toBe("true");
    expect(button("Your choice for CSV", "include").getAttribute("aria-pressed")).toBe("true");
    button(label, "later").click();
    expect(total()).toBe("1200000");
    expect(button("Your choice for CSV", "later").getAttribute("aria-pressed")).toBe("true");
    button(label, "decline").click();
    expect(total()).toBe("1200000");
    expect(doc.querySelector(".group-status")!.textContent).toContain("1 of 3 included");
    expect(button("Your choice for Stripe", "decline").getAttribute("aria-pressed")).toBe("true");
  });
  test("individual overrides clear the group state and active choices can be cleared", async () => {
    const {button, total} = await open();
    const label = "Group choices for Build package";
    button(label, "include").click();
    button("Your choice for CSV", "later").click();
    expect(button(label, "include").getAttribute("aria-pressed")).toBe("false");
    expect(total()).toBe("1450000");
    button("Your choice for CSV", "later").click();
    expect(button("Your choice for CSV", "later").getAttribute("aria-pressed")).toBe("false");
    button(label, "include").click();
    button(label, "include").click();
    expect(total()).toBe("1200000");
    expect(button("Your choice for Stripe", "include").getAttribute("aria-pressed")).toBe("false");
  });
  test("exclusive groups have no include-all action and switch between options", async () => {
    const {button, total, doc} = await open();
    expect(doc.querySelector('[aria-label="Group choices for Support"]')).toBeNull();
    button("Your choice for Week", "include").click();
    expect(total()).toBe("1250000");
    button("Your choice for Month", "include").click();
    expect(total()).toBe("1350000");
    expect(button("Your choice for Week", "include").getAttribute("aria-pressed")).toBe("false");
    button("Your choice for Month", "include").click();
    expect(total()).toBe("1200000");
  });
  test("group choices submit the existing item-level selection format", async () => {
    const {button, doc, window, calls} = await open();
    button("Group choices for Build package", "include").click();
    button("Your choice for CSV", "later").click();
    button("Your choice for Month", "include").click();
    doc.querySelector("input")!.value = "Demo Client";
    const form = doc.querySelector("form")!;
    const accept = form.querySelector('button[value="accepted"]')!;
    form.dispatchEvent(new window.SubmitEvent("submit", {bubbles: true, cancelable: true, submitter: accept}));
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(calls[1].body.selection).toEqual({"0:1": "include", "0:2": "later", "1:1": "include"});
    expect(calls[1].options.credentials).toBe("same-origin");
  });
  test("recorded responses show frozen choices and totals without editable buttons", async () => {
    const {doc, total} = await open({outcome: "accepted", name: "Client", created_at: 1794000000,
      total_minor: 1450000, selection: {"0:0": "include", "0:1": "include", "0:2": "later", "1:0": "include"}});
    expect(doc.querySelectorAll(".choice-button").length).toBe(0);
    expect(total()).toBe("1450000");
    expect(doc.body.textContent).toContain("Proposal accepted");
    expect(doc.body.textContent).toContain("For later");
  });
  test("French proposals translate controls, formatting, metadata, and validation", async () => {
    const {doc, button, window, calls} = await open(undefined, "fr");
    expect(doc.documentElement.lang).toBe("fr");
    expect(doc.title).toBe("Proposition");
    expect(doc.body.textContent).toContain("Obligatoire · inclus");
    expect(doc.body.textContent).toContain("Version 1");
    button("Choix du groupe Build package", "include").click();
    expect(doc.querySelector(".total")!.textContent!.replace(/\s/g, "")).toBe("15300,00€");
    expect(doc.body.textContent).toContain("Inclure le groupe");
    expect(doc.body.textContent).toContain("Pour plus tard");
    expect(doc.body.textContent).toContain("Refuser la proposition");
    const form = doc.querySelector("form")!;
    const accept = form.querySelector('button[value="accepted"]')!;
    form.dispatchEvent(new window.SubmitEvent("submit", {cancelable: true, submitter: accept}));
    expect(doc.querySelector('[role="alert"]')!.textContent).toBe("Indiquez votre nom pour accepter.");
    doc.querySelector("input")!.value = "Client";
    form.dispatchEvent(new window.SubmitEvent("submit", {cancelable: true, submitter: accept}));
    expect(doc.querySelector('[role="alert"]')!.textContent).toContain("Choisissez un élément");
    expect(calls.length).toBe(1);
  });

});
