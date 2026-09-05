import {afterEach, beforeAll, expect, test} from "bun:test";
import {Window} from "happy-dom";
import React, {act} from "react";
import {createRoot, type Root} from "react-dom/client";
import {CreateDialog, InstanceCard} from "./InstancesPanel";
import {normalizeArchitecture} from "./catalog-state";

const win = new Window({url:"http://localhost"});
let root: Root | undefined;
let container: HTMLDivElement;
const originalFetch = globalThis.fetch;
beforeAll(() => {
  Object.assign(globalThis, {window:win, document:win.document, navigator:win.navigator, HTMLElement:win.HTMLElement, IS_REACT_ACT_ENVIRONMENT:true});
});
afterEach(async () => {if(root) await act(async()=>root!.unmount()); root=undefined;container?.remove();globalThis.fetch=originalFetch;});
async function render(element: React.ReactNode) {container=document.createElement("div");document.body.append(container);root=createRoot(container);await act(async()=>root!.render(element));}
const response = (body: unknown) => new Response(JSON.stringify(body),{headers:{"Content-Type":"application/json"}});
const params = () => "install_id=1&project_id=test";

test("architecture aliases match across provider catalogs",()=>{expect(normalizeArchitecture("aarch64")).toBe(normalizeArchitecture("arm"));expect(normalizeArchitecture("x86_64")).toBe(normalizeArchitecture("amd64"));expect(normalizeArchitecture("arm")).not.toBe(normalizeArchitecture("x86"));});

test("null disk metrics render without a panel crash",async()=>{
 globalThis.fetch=(async()=>response({metrics:{cpu:{total_pct:0,cores:2},mem:{used_bytes:0,total_bytes:0},disk:null,load:{l1:0,l5:0,l15:0},uptime_s:1,process_count:1}})) as unknown as typeof fetch;
 await render(<InstanceCard inst={{id:1,name:"Null disks",provider:"hetzner",status:"ready",monthly_cost_cents:0} as any} withParams={params} busy={false} onUpgrade={()=>{}} onVolumes={()=>{}} onDestroy={()=>{}}/>);
 expect(container.textContent).toContain("Null disks");
});

test("late catalog responses cannot replace the selected account",async()=>{
 const pending:Array<()=>void>=[];
 globalThis.fetch=(async(input:any)=>{
  const url=new URL(String(input),"http://localhost");
  if(url.pathname.endsWith("instances-providers"))return response({providers:[{provider:"hetzner",connection_id:7,default:true},{provider:"digitalocean",connection_id:8}]});
  const name=url.searchParams.get("provider_connection_id")==="7"?"old-account":"new-account";
  const body=url.pathname.endsWith("server-types")?{server_types:[{name,cores:2,memory_gb:4,disk_gb:40}]}:url.pathname.endsWith("locations")?{locations:[{name:"zone"}]}:url.pathname.endsWith("images")?{images:[{name:"ubuntu"}]}:{capabilities:{storage_classes:[],tiers:[]}};
  if(name==="old-account")return new Promise<Response>(resolve=>pending.push(()=>resolve(response(body))));
  return response(body);
 }) as unknown as typeof fetch;
 await render(<CreateDialog withParams={params} setError={()=>{}} onCreated={()=>{}} onClose={()=>{}}/>);
 const provider=container.querySelector("select")!;
 await act(async()=>{provider.value="8";provider.dispatchEvent(new win.Event("change",{bubbles:true}) as any);});
 expect(container.textContent).toContain("new-account");
 await act(async()=>{pending.forEach(resolve=>resolve());});
 expect(container.textContent).not.toContain("old-account");
 expect(container.textContent).toContain("new-account");
});
