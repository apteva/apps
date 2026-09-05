import { expect, test } from "bun:test";
import React, { StrictMode } from "react";
import { act, create } from "react-test-renderer";
import { createRefreshQueue, usePollingRefresh } from "./refresh";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>((r) => { resolve = r; }); return { promise, resolve }; };
test("refresh bursts coalesce and never overlap", async () => {
  const gate = deferred(); let calls = 0, running = 0, maximum = 0;
  const queue = createRefreshQueue(async () => { calls++; maximum = Math.max(maximum, ++running); if(calls === 1) await gate.promise; running--; });
  const first = queue.refresh(); for (let i=0;i<10;i++) void queue.refresh();
  expect(calls).toBe(1); gate.resolve(); await first;
  expect(calls).toBe(2); expect(maximum).toBe(1); queue.dispose();
});
test("disposing aborts old work and prevents later refreshes", async () => {
  let signal!: AbortSignal, calls=0; const gate=deferred();
  const queue=createRefreshQueue(async (s) => {signal=s;calls++;await gate.promise;});
  const active=queue.refresh(); queue.dispose(); expect(signal.aborted).toBe(true);
  gate.resolve(); await active; await queue.refresh(); expect(calls).toBe(1);
});
test("polling survives strict effects, interval changes and identity changes", async () => {
  const observed: {key:string;signal:AbortSignal}[]=[];
  let refresh!: () => Promise<void>;
  function Probe({id,interval}:{id:string;interval:number}) {
    refresh=usePollingRefresh(async(signal)=>{observed.push({key:id,signal});},id,interval);return null;
  }
  let root!: ReturnType<typeof create>;
  await act(async()=>{root=create(<StrictMode><Probe id="a" interval={0}/></StrictMode>);});
  await act(async()=>{await refresh();}); expect(observed.at(-1)?.key).toBe("a");
  const old=observed.at(-1)!.signal;
  await act(async()=>{root.update(<StrictMode><Probe id="b" interval={10000}/></StrictMode>);});
  expect(old.aborted).toBe(true); expect(observed.at(-1)?.key).toBe("b");
  await act(async()=>{root.update(<StrictMode><Probe id="b" interval={0}/></StrictMode>);});
  await act(async()=>{await refresh();}); expect(observed.at(-1)!.signal.aborted).toBe(false);
  await act(async()=>{root.unmount();}); expect(observed.at(-1)!.signal.aborted).toBe(true);
});
