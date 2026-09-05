import { expect, test } from "bun:test";
import { RequestGate } from "./requestGate";

test("late detail and secondary requests cannot overwrite a new selection", async () => {
  const gate = new RequestGate(); let resolveA!: (value: string) => void; let shown = "";
  const a = gate.begin();
  const late = new Promise<string>(resolve => { resolveA = resolve; }).then(value => { if (a.current()) shown = value; });
  const b = gate.begin(); if (b.current()) shown = "B";
  resolveA("A"); await late;
  expect(a.signal.aborted).toBe(true); expect(shown).toBe("B");
});
test("scope changes invalidate all pending updates", () => {
  const gate = new RequestGate(); const old = gate.begin(); gate.invalidate();
  expect(old.current()).toBe(false);
});
