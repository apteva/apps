import { expect, test } from "bun:test";
import { liveRefresh, mergeDraft, subscribeTickets, type EventHost, type TicketEvent } from "./live";
const pause = (ms=15) => new Promise(resolve => setTimeout(resolve,ms));

test("shared subscription scopes ticket events and unsubscribes", () => {
  let receive!: (event: TicketEvent) => void, calls=0, closed=false;
  const host={__aptevaAppEvents:{subscribe(app:string,project:string,fn:typeof receive){expect([app,project]).toEqual(["tickets","project-a"]);receive=fn;return()=>{closed=true}}},EventSource:class{constructor(){throw new Error("must share the dashboard stream")}}} as unknown as EventHost;
  const stop=subscribeTickets(host,"project-a",7,()=>calls++);
  for(const topic of ["ticket.created","ticket.updated","ticket.commented","ticket.internal_note.added","ticket.attachment.added","ticket.status.changed","ticket.resolved","ticket.reopened","ticket.link.added"]){receive({seq:1,topic,project_id:"project-a",install_id:7})}
  receive({seq:2,topic:"file.added"});
  receive({seq:3,topic:"ticket.created",project_id:"other"});
  receive({seq:4,topic:"ticket.created",install_id:8});
  expect(calls).toBe(9);stop();expect(closed).toBe(true);
});

test("fallback uses credentials, refreshes on connect, deduplicates and closes",()=>{
  let stream:any,calls=0;
  class FakeSource{closed=false;onopen:any;onmessage:any;onerror:any;constructor(url:string,options:any){expect(url).toContain("project_id=a%20b");expect(options.withCredentials).toBe(true);stream=this}close(){this.closed=true}}
  const stop=subscribeTickets({EventSource:FakeSource} as unknown as EventHost,"a b",0,()=>calls++);
  stream.onopen();stream.onmessage({data:JSON.stringify({seq:1,topic:"ticket.created"})});stream.onmessage({data:JSON.stringify({seq:1,topic:"ticket.created"})});stream.onmessage({data:"bad json"});
  expect(calls).toBe(2);stop();expect(stream.closed).toBe(true);
});

test("bursts wait for mutations and events during an in-flight refresh are retained",async()=>{
  let busy=true,calls=0,release!:()=>void;
  const queue=liveRefresh(async()=>{calls++;if(calls===1)await new Promise<void>(resolve=>release=resolve)},()=>busy,2);
  queue.request();queue.request();await pause();expect(calls).toBe(0);
  busy=false;await pause();expect(calls).toBe(1);
  queue.request();queue.request();await pause();expect(calls).toBe(1);
  release();await pause();expect(calls).toBe(2);
  queue.request();queue.stop();await pause();expect(calls).toBe(2);
});

test("live updates keep dirty fields while updating untouched fields",()=>{
  expect(mergeDraft({title:"My unsaved title",status:"new"},{title:"Original",status:"new"},{title:"Agent title",status:"resolved"})).toEqual({title:"My unsaved title",status:"resolved"});
});
