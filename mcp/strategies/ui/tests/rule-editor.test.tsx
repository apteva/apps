import { afterEach, expect, test } from "bun:test";
import React, { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RuleProgramEditor } from "../../RuleProgramEditor.tsx";

let root: Root;
let container: HTMLElement;
afterEach(async () => { if (root) await act(async () => root.unmount()); container?.remove(); });

test("rule builder edits indicators, rearming and monetary risk in the portable definition", async () => {
 let latest: any;
 function Harness() {
  const [program, setProgram] = useState<any>({version:"trading-rules/1",symbol:"XAUUSD",timeframe:"1m",timezone:"UTC",rules:[]});
  latest = program;
  return <RuleProgramEditor program={program} onChange={setProgram} />;
 }
 container = document.createElement("div"); document.body.append(container); root = createRoot(container);
 await act(async () => root.render(<Harness />));
 const click = async (text: string) => act(async () => { Array.from(container.querySelectorAll("button")).find(b => b.textContent === text)!.click(); });
 const change = async (label: string, value: string) => act(async () => {
  const element = container.querySelector(`[aria-label="${label}"]`) as HTMLInputElement;
  const proto = element.tagName === "SELECT" ? window.HTMLSelectElement.prototype : window.HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto,"value")!.set!.call(element, value);
  element.dispatchEvent(new window.Event(element.tagName === "SELECT" ? "change" : "input", {bubbles:true}));
 });
 await click("Add rule");
 await change("Operator", "crosses_above");
 await change("Metric", "ema"); await change("Indicator period", "50"); await change("Metric timeframe", "1h");
 await change("Repeat policy", "until_reset");
 await change("Sizing amount", "250"); await change("stop percent", "0.5");
 await change("Trail activation", "0.5"); await change("Trail distance", "0.1");
 expect(latest.rules[0].when).toEqual({op:"crosses_above",args:[{metric:"ema",period:50,timeframe:"1h"},{value:0}]});
 expect(latest.rules[0].reset).toBeDefined();
 expect(latest.rules[0].actions[0]).toMatchObject({stop_pct:.005,trail_activation_pct:.005,trail_distance_pct:.001,sizing:{mode:"fixed_risk",amount:250}});
 await click("Add action");
 expect(latest.rules[0].actions).toHaveLength(2);
 await click("Add session window"); await click("Add clock schedule");
 expect(latest.windows.window_1).toEqual({start:"08:00",end:"11:00"});
 expect(latest.schedules.clock_1).toBe("18:00");
});
