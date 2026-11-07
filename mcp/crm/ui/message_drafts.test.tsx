import { afterAll, afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import CrmPanel from "./CrmPanel";
import type { SavedReplyDraft } from "./drafts";

const previous = {window:globalThis.window,document:globalThis.document,HTMLElement:globalThis.HTMLElement,fetch:globalThis.fetch,IS_REACT_ACT_ENVIRONMENT:(globalThis as any).IS_REACT_ACT_ENVIRONMENT};
const browser = new Window({url:"http://localhost"});
Object.assign(globalThis,{window:browser,document:browser.document,HTMLElement:browser.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true});
let root:Root | undefined;
afterEach(async()=>{ await act(async()=>root?.unmount()); root=undefined;document.body.innerHTML="";globalThis.fetch=previous.fetch; });
afterAll(()=>Object.assign(globalThis,previous));

const contact = {id:"1",display_name:"Alice",first_name:"Alice",last_name:"",company:"",status:"active",primary_email:"primary@example.test",channels:[{kind:"email",value:"primary@example.test",is_primary:true},{kind:"email",value:"original@example.test"}],tags:[],attributes:[],updated_at:"2026-10-10T10:00:00Z"};
const conversation = {id:"7",contact_id:"1",channel:"email",subject:"Intro",status:"pending",priority:"normal",started_at:"2026-10-09T10:00:00Z",last_activity_at:"2026-10-09T10:00:00Z"};
const activity = {id:"8",contact_id:"1",conversation_id:"7",kind:"email_sent",body:"Original outreach",occurred_at:"2026-10-09T10:00:00Z",message_addresses:{from:"support@example.test",to:["original@example.test"]},attachments:[]};

async function mount(inbox=false) {
 (browser as any).__aptevaAppEvents={subscribe:()=>()=>{}};
 browser.history.replaceState(null,"",inbox?"/?tab=inbox&status=all":"/");
 const calls:{method:string;path:string;body:any;query:URLSearchParams}[]=[];
 let draft:SavedReplyDraft | undefined;
 globalThis.fetch = (async(input:any,init:any)=>{
  const url=new URL(String(input),"http://localhost");const path=url.pathname.replace("/api/apps/crm","");const method=init?.method||"GET";const body=init?.body?JSON.parse(init.body):{};
  calls.push({method,path,body,query:url.searchParams});
  let out:any={};
  if(path==="/contacts") out={contacts:[contact],total:1};
  else if(path==="/contacts/1") out={contact};
  else if(path==="/contacts/1/activities") out={activities:inbox?[activity]:[]};
  else if(path==="/contacts/1/conversations") out={conversations:inbox?[conversation]:[]};
  else if(path==="/contacts/1/conversations/7") out={conversation,activities:[activity],activity_total:1};
  else if(path==="/inbox") out={inbox:[{...conversation,id:7,contact_id:1,contact_name:"Alice"}],total:1};
  else if(path==="/messaging/senders") out={senders:[{channel:"email",address:"support@example.test",is_default:true}]};
  else if(path==="/messaging/reply-route") out={to:"original@example.test",from:"support@example.test"};
  else if(path==="/drafts" && method==="POST") {
   draft={id:20,mode:body.mode,contact_id:body.contact_id,conversation_id:body.conversation_id||0,reply_to_activity_id:0,content:{channel:body.channel,to:body.to,from:body.from,subject:body.subject,body:body.body,body_html:body.body_html,attachments:body.attachments},status:"draft",revision:1,created_by:"human",updated_by:"human",created_at:"2026-10-10T10:00:00Z",updated_at:"2026-10-10T10:00:00Z"};out={draft};
  } else if(path==="/drafts" && method==="GET") out={drafts:draft?[{...draft,...draft.content,preview:draft.content.body}]:[],total:draft?1:0};
  else if(path==="/drafts/20" && method==="GET") out={draft};
  else if(path==="/drafts/20/send") {draft={...draft!,status:"sent",revision:3,conversation_id:draft?.conversation_id||7};out={draft,sent:true};}
  else if(path==="/lists" || path.endsWith("/lists")) out={lists:[]};
  else if(path==="/segments") out={segments:[]};
  else if(path==="/pipelines") out={pipelines:[]};
  else if(path.endsWith("/opportunities")) out={opportunities:[],total:0};
  else if(path==="/attribute-defs") out={attribute_defs:[]};
  else if(path==="/messaging/templates") out={templates:[]};
  else if(path.endsWith("/unsubscribe")) out={address:"original@example.test",outbound_blocked:false};
  else throw new Error(`Unexpected ${method} ${path}`);
  return new Response(JSON.stringify(out),{status:200,headers:{"Content-Type":"application/json"}});
 }) as typeof fetch;
 const host=document.createElement("div");document.body.appendChild(host);
 await act(async()=>{root=createRoot(host);root.render(<CrmPanel appName="crm" projectId="alpha" installId={99}/>);});
 return {host,calls,getDraft:()=>draft,completeDraft:()=>{draft={...draft!,content:{...draft!.content,subject:"Hello",body:"Approved outreach"}};}};
}
function button(host:Element,label:string) {const node=[...host.querySelectorAll("button")].find(n=>n.textContent===label);if(!node) throw new Error(`Missing ${label}`);return node;}
async function click(node:Element){await act(async()=>{(node as HTMLElement).click();});}

test("first outreach Save & close and contact shelf reopen never send or need inbound",async()=>{
 const h=await mount();
 await click(h.host.querySelector("aside li")!);
 await click(button(h.host,"Send message"));
 expect(h.host.textContent).toContain("New message to Alice");
 expect(button(h.host,"Save draft").disabled).toBe(false);
 await click(button(h.host,"Save & close"));
 expect(h.host.textContent).not.toContain("New message to Alice");
 const create=h.calls.find(c=>c.path==="/drafts"&&c.method==="POST")!;
 expect(create.body.mode).toBe("message");expect(create.body.contact_id).toBe(1);expect(create.body.to).toBe("primary@example.test");expect(create.body.conversation_id).toBeUndefined();
 expect(h.calls.some(c=>c.path.endsWith("/send")||c.path.endsWith("/messages"))).toBe(false);
 await click(button(h.host,"▸ Saved message drafts"));
 expect(h.calls.some(c=>c.path==="/drafts"&&c.query.get("contact_id")==="1")).toBe(true);
 const reopen=[...h.host.querySelectorAll("button")].find(n=>n.textContent?.includes("Draft — not sent"))!;
 await click(reopen);
 expect(h.host.textContent).toContain("New message to Alice");expect(h.host.textContent).toContain("primary@example.test");
 expect(h.getDraft()?.revision).toBe(1);
});

test("outbound-only inbox Follow up pins historical route and saves without sending",async()=>{
 const h=await mount(true);
 expect(h.host.textContent).toContain("Original outreach");
 await click(button(h.host,"Follow up"));
 expect(h.host.textContent).toContain("Follow up with Alice");
 const route=h.calls.find(c=>c.path==="/messaging/reply-route")!;
 expect(route.query.get("mode")).toBe("message");
 await click(button(h.host,"Save draft"));
 const create=h.calls.find(c=>c.path==="/drafts"&&c.method==="POST")!;
 expect(create.body.mode).toBe("message");expect(create.body.conversation_id).toBe(7);expect(create.body.reply_to_activity_id).toBeUndefined();expect(create.body.to).toBe("original@example.test");
 expect(h.calls.some(c=>c.path.endsWith("/send"))).toBe(false);
 await click(button(h.host,"Save & close"));
 const reopen=[...h.host.querySelectorAll("button")].find(n=>n.textContent?.includes("Draft — not sent"))!;
 await click(reopen);
 expect(h.host.textContent).toContain("Follow up with Alice");
 expect(h.getDraft()?.conversation_id).toBe(7);
});

test("explicit first outreach Send uses the saved draft endpoint, never a replacement direct send",async()=>{
 const h=await mount();
 await click(h.host.querySelector("aside li")!);
 await click(button(h.host,"Send message"));await click(button(h.host,"Save & close"));
 h.completeDraft();
 await click(button(h.host,"▸ Saved message drafts"));
 const reopen=[...h.host.querySelectorAll("button")].find(n=>n.textContent?.includes("Draft — not sent"))!;
 await click(reopen);
 expect(button(h.host,"Send").disabled).toBe(false);
 await click(button(h.host,"Send"));
 expect(h.calls.filter(c=>c.path==="/drafts/20/send")).toHaveLength(1);
 expect(h.calls.find(c=>c.path==="/drafts/20/send")?.body.expected_revision).toBe(1);
 expect(h.calls.some(c=>c.path.endsWith("/messages")||c.path.endsWith("/reply"))).toBe(false);
 expect(h.getDraft()?.status).toBe("sent");expect(h.host.textContent).not.toContain("New message to Alice");
});
