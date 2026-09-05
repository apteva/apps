import {afterEach, expect, mock, test} from "bun:test";
import React from "react";
import {act,create} from "react-test-renderer";
const Container=({children,...props}:any)=><div {...props}>{children}</div>;
mock.module("@apteva/ui-kit",()=>({Card:Container,CardHeader:Container,StatusDot:Container,StatusPill:Container,DataList:Container}));
const {default:ComputerPanel}=await import("./ComputerPanel");
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;
const originalFetch=globalThis.fetch, originalWindow=(globalThis as any).window;
let root:ReturnType<typeof create>|undefined;
afterEach(async()=>{if(root)await act(async()=>root!.unmount());root=undefined;globalThis.fetch=originalFetch;(globalThis as any).window=originalWindow;});
const row=(id:string)=>({session_id:id,backend:"local",status:"active",current_url:`https://${id}.example/`,opened_at:new Date().toISOString()});
function install(search:string, sessions:(url:URL)=>Promise<Response>){
 (globalThis as any).window={location:{search,origin:"http://localhost"},__aptevaAppEvents:{subscribe:()=>()=>{}}};
 globalThis.fetch=(async(input:any)=>{const url=new URL(String(input),"http://localhost");if(url.pathname.endsWith("/sessions"))return sessions(url);if(url.pathname.endsWith("/contexts"))return Response.json({contexts:[]});if(url.pathname.endsWith("/settings"))return Response.json({settings:{default_backend:"local",default_proxy_mode:"auto"}});if(url.pathname.endsWith("/proxy-profiles"))return Response.json({profiles:[],connections:[]});if(url.pathname.endsWith("/presentation"))return Response.json({session:row("requested")});return Response.json({});}) as typeof fetch;
}
const selected=()=>root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="SessionDetail")[0]?.props.session?.session_id;
test("deep link survives initial empty list and resolves outside the history page",async()=>{
 let release!:(r:Response)=>void;
 install("?session_id=requested",()=>new Promise(r=>{release=r}));
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 await act(async()=>{release(Response.json({sessions:[row("first")]}));});
 expect(selected()).toBe("requested");
});
test("an old project response cannot overwrite the newly selected project",async()=>{
 let release!:(r:Response)=>void;
 install("",async(url)=>url.searchParams.get("project_id")==="one"?new Promise(r=>{release=r}):Response.json({sessions:[row("new")]}));
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 await act(async()=>{root!.update(<ComputerPanel projectId="two"/>)});
 expect(selected()).toBe("new");
 await act(async()=>{release(Response.json({sessions:[row("old")]}));});
 expect(selected()).toBe("new");
});
