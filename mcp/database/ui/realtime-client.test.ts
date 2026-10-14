import { expect, test } from "bun:test";
import { sseDecoder } from "./realtime-client";
test("SSE survives every split, CRLF, comments, and unicode", () => {
 const text = ': heartbeat\r\n\r\nevent: snapshot\r\ndata: {"result":"España"}\r\n\r\nid: abc\nevent: invalidate\ndata: {"cursor":"abc","collections":["orders"]}\n\n';
 for(let i=0;i<=text.length;i++) { const events: any[]=[];const feed=sseDecoder(e=>events.push(e));feed(text.slice(0,i));feed(text.slice(i));expect(events).toEqual([{type:"snapshot",data:{result:"España"}},{type:"invalidate",data:{cursor:"abc",collections:["orders"]}}]); }
});
test("incomplete frames cannot advance the cursor",()=>{const events:any[]=[];const feed=sseDecoder(e=>events.push(e));feed('event: invalidate\ndata: {"cursor":"new"}\n');expect(events).toEqual([]);feed('\n');expect(events[0].data.cursor).toBe("new");});
test("unbounded frames fail",()=>{const feed=sseDecoder(()=>{});expect(()=>feed("x".repeat(6*1024*1024))).toThrow();});

import { watchCollection } from "./realtime-client";
test("reconnect uses only complete delivered boundaries; expired history resnapshots", async () => {
 const originalFetch = globalThis.fetch;
 const calls: any[] = [], statuses: string[] = [], snapshots: any[] = [];
 const controller = new AbortController(); let changed = 0;
 globalThis.fetch = (async (_url: any, init: any) => {
   calls.push(JSON.parse(init.body));
   if (calls.length === 1) return new Response('event: snapshot\ndata: {"result":{"records":[]}}\n\nevent: ready\ndata: {"cursor":"ready"}\n\nevent: invalidate\ndata: {"cursor":"committed"}\n\nevent: invalidate\ndata: {"cursor":"incomplete"}\n', { headers: {"Content-Type":"text/event-stream"} });
   if (calls.length === 2) return new Response(JSON.stringify({error:{code:"history_expired"}}),{status:410});
   controller.abort();return new Response('');
 }) as typeof fetch;
 try {
  await watchCollection({url:"/watch", database:"shop", collection:"orders",signal:controller.signal,snapshot:v=>snapshots.push(v),changed:()=>changed++,status:v=>statuses.push(v)});
  expect(calls[0].initial.op).toBe("find");expect(calls[1].after).toBe("committed");expect(calls[2].after).toBeUndefined();expect(calls[2].initial.op).toBe("find");expect(changed).toBe(1);expect(snapshots.length).toBe(1);expect(statuses).toContain("Refreshing");
 } finally {globalThis.fetch=originalFetch;}
});
