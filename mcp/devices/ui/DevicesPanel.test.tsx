import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
const initialBrowser=new Window({url:"http://localhost"});
Object.assign(globalThis,{window:initialBrowser,document:initialBrowser.document});
const {createRoot}=await import("react-dom/client");
import DevicesPanel from "./DevicesPanel";
let browser:Window,root:Root,container:HTMLElement;
const originalFetch=globalThis.fetch;
let calls:{url:URL;init?:RequestInit}[];
let failProvision=false;
const device={id:"lamp",name:"Lamp",protocol:"apteva.devices/v2",enabled:true,status:"online",manifest:{operations:[{name:"echo",input_schema:{type:"object"}}]},state:[]};
const json=(value:any,status=200)=>new Response(JSON.stringify(value),{status,headers:{"Content-Type":"application/json"}});
function button(text:string){const match=[...container.querySelectorAll("button")].find(b=>b.textContent?.toLowerCase().includes(text.toLowerCase()));expect(match).toBeDefined();return match!}
async function click(text:string){await act(async()=>button(text).click())}
async function input(label:string,value:string){const field=[...container.querySelectorAll("input")].find(i=>i.closest("label")?.textContent?.startsWith(label));expect(field).toBeDefined();await act(async()=>{Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype,"value")!.set!.call(field,value);field!.dispatchEvent(new browser.Event("input",{bubbles:true}) as any)})}
beforeEach(()=>{
 browser=new Window({url:"http://localhost"});Object.assign(globalThis,{window:browser,document:browser.document,HTMLElement:browser.HTMLElement,Event:browser.Event,IS_REACT_ACT_ENVIRONMENT:true});container=browser.document.createElement("div") as any;browser.document.body.appendChild(container as any);root=createRoot(container);calls=[];failProvision=false;
 globalThis.fetch=(async(input:any,init?:RequestInit)=>{
  const url=new URL(String(input));calls.push({url,init});const path=url.pathname.replace("/api/apps/devices/_install/7","");
  if(path==="/summary")return json({devices:1,online:1,offline:0,disabled:0,commands_24h:0,failed_commands_24h:0});
  if(path==="/devices" && init?.method==="POST"){
   if(failProvision){failProvision=false;throw new Error("response lost")}
   return json({status:"completed",device,mqtt:{username:"scoped-user",password:"test-password",endpoint:"mqtt://host:1883"},topics:{state:"scopes/token/devices/lamp/state"}})
  }
  if(path==="/devices")return json([device]);
  if(path==="/devices/lamp")return json(device);
  if(path==="/devices/lamp/commands"){if(init?.method==="POST")return json({command_id:"cmd_one",command:{status:"queued"}},202);return json([])}
  if(path==="/data")return json({items:[{message_id:url.searchParams.get("cursor")?"second":"first"}],next_cursor:url.searchParams.get("cursor")?undefined:"next"});
  if(path==="/history/events")return json({items:[]});
  return json({items:[]});
 }) as typeof fetch;
});
afterEach(async()=>{await act(async()=>root.unmount());globalThis.fetch=originalFetch;await browser.happyDOM.close()});
async function render(projectId="project-a"){await act(async()=>root.render(<DevicesPanel installId={7} projectId={projectId}/>))}
test("install and ownership scope are attached to all API requests",async()=>{
 await render();expect(container.textContent).toContain("Lamp");expect(calls.length).toBeGreaterThan(0);
 expect(calls.every(c=>c.url.pathname.startsWith("/api/apps/devices/_install/7/") && c.url.searchParams.get("project_id")==="project-a")).toBe(true);
 await click("history");expect(container.textContent).toContain("first");await click("Load more");expect(container.textContent).toContain("second");expect(calls.some(c=>c.url.pathname.endsWith("/data") && c.url.searchParams.get("cursor")==="next")).toBe(true);
});
test("generic commands are asynchronous and carry a retry identity",async()=>{
 await render();await click("controls");const select=container.querySelector("select")!;
 await act(async()=>{select.value="echo";select.dispatchEvent(new browser.Event("change",{bubbles:true}) as any)});
 await click("Submit command");const request=calls.find(c=>c.init?.method==="POST" && c.url.pathname.endsWith("/commands"))!;expect(request).toBeDefined();const body=JSON.parse(request.init!.body as string);expect(body.operation).toBe("echo");expect(body.wait).toBe(false);expect(body.idempotency_key).toBeTruthy();expect((request.init!.headers as any)["Idempotency-Key"]).toBe(body.idempotency_key);
});
test("lost enrollment responses reuse the same provisioning key",async()=>{
 await render();await click("Provision device");await input("Name","New lamp");failProvision=true;await click("Create credentials");await click("Create credentials");const posts=calls.filter(c=>c.init?.method==="POST" && c.url.pathname.endsWith("/devices"));expect(posts.length).toBe(2);expect((posts[0].init!.headers as any)["Idempotency-Key"]).toBe((posts[1].init!.headers as any)["Idempotency-Key"]);expect(JSON.parse(posts[0].init!.body as string).protocol).toBe("apteva.devices/v2");expect(container.textContent).toContain("scopes/token/devices/lamp/state");expect(container.textContent).toContain("scoped-user");
});
