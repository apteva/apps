import { expect, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../src/client";

test("headless contract uses one explicit scope and current bearer credential for all operations", async () => {
  const requests: Array<{url:URL;init:RequestInit}> = [];
  const client = new AptevaClient({baseURL:"https://platform.example",accessToken:"visitor-a",fetch:async(url,init)=>{
    requests.push({url:new URL(String(url)),init:init!});return Response.json({messages:[],conversations:[],items:[]});
  }});
  const c=client.use(conversationsExtension(),{projectId:"project-a",installId:31});
  await c.list();await c.history("private",{before:200});await c.changes("private",201);
  await c.send("private",{content:"hello",client_message_id:"retry-1"});
  client.setAccessToken("renewed-a");await c.markSeen("private",202);await c.act(203,"approve");
  expect(requests).toHaveLength(6);
  for(const {url,init} of requests){expect(url.searchParams.get("project_id")).toBe("project-a");expect(url.searchParams.get("install_id")).toBe("31");expect(init.credentials).toBe("omit");expect(url.search).not.toContain("visitor-a");}
  expect(new Headers(requests[0].init.headers).get("Authorization")).toBe("Bearer visitor-a");
  expect(new Headers(requests.at(-1)!.init.headers).get("Authorization")).toBe("Bearer renewed-a");
  expect(JSON.parse(String(requests[3].init.body))).toEqual({content:"hello",client_message_id:"retry-1"});
  expect(()=>c.apiGet("/chats","other-project")).toThrow("scope");
});

test("bearer SSE distinguishes stream frames, edits and resync without deduplicating message IDs", async()=>{
  let controller!: ReadableStreamDefaultController<Uint8Array>;let resolveOpen!:()=>void;
  const opened=new Promise<void>(r=>resolveOpen=r);let initSeen:RequestInit|undefined;
  const client=new AptevaClient({baseURL:"https://platform.example",accessToken:"visitor-a",fetch:async(_url,init)=>{
    initSeen=init;return new Response(new ReadableStream({start(c){controller=c}}),{headers:{"Content-Type":"text/event-stream"}});
  }});
  const c=client.use(conversationsExtension(),{projectId:"p",installId:4});const messages:any[]=[],frames:any[]=[];let resync=0;
  const sub=c.subscribe("chat",{onOpen:resolveOpen,onMessage:m=>messages.push(m),onFrame:f=>frames.push(f),onResync:()=>resync++});
  await opened;
  controller.enqueue(new TextEncoder().encode('id: 1\ndata: {"id":1,"conversation_id":"chat","revision":1}\n\nid: 1\ndata: {"id":1,"conversation_id":"chat","revision":2}\n\nevent: stream\ndata: {"chat_id":"chat","call_id":"c","text":"Working"}\n\nevent: resync\ndata: {}\n\ndata: {"conversation_id":"other","content":"private"}\n\n'));
  await new Promise(r=>setTimeout(r,10));sub.close();
  expect(messages.map(m=>m.revision)).toEqual([1,2]);expect(frames).toHaveLength(1);expect(resync).toBe(1);
  expect(initSeen?.credentials).toBe("omit");expect(initSeen?.signal?.aborted).toBe(true);
});
