import {afterEach, expect, mock, test} from "bun:test";
import React from "react";
import {act,create} from "react-test-renderer";
const Container=({children,...props}:any)=><div {...props}>{children}</div>;
mock.module("@apteva/ui-kit",()=>({Card:Container,AppCardHeader:Container,CardHeader:Container,StatusDot:Container,StatusPill:Container,DataList:Container}));
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
test("history is requested only when opened and pages on demand",async()=>{
 const requests:string[]=[];
 install("",async(url)=>{
  const view=url.searchParams.get("view") ?? "";
  requests.push(view);
  if(view==="active")return Response.json({sessions:[row("live")]});
  if(view==="history"){
   const offset=Number(url.searchParams.get("offset"));
   return Response.json({sessions:[{...row(`past-${offset}`),status:"closed"}],next_offset:offset+1,has_more:offset===0});
  }
  throw new Error(`unexpected sessions view ${view}`);
 });
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 expect(requests).toEqual(["active"]);
 const button=(label:string)=>root!.root.findAll(n=>n.type==="button"&&n.props.children?.[0]?.props?.children===label)[0];
 expect(root!.root.findAll(n=>n.type==="select"&&n.props.title==="Default provider")).toHaveLength(0);
 await act(async()=>button("Browser settings").props.onClick());
 expect(root!.root.findAll(n=>n.type==="select"&&n.props.title==="Default provider")).toHaveLength(1);
 await act(async()=>button("Session history").props.onClick());
 expect(requests).toEqual(["active","history"]);
 expect(root!.root.findAll(n=>n.props.row?.session_id==="past-0").length).toBeGreaterThan(0);
 await act(async()=>root!.root.findAll(n=>n.type==="button"&&n.props.children==="Load more")[0].props.onClick());
 expect(requests).toEqual(["active","history","history"]);
 expect(root!.root.findAll(n=>n.props.row?.session_id==="past-1").length).toBeGreaterThan(0);
});

test("mobile capabilities expose Home and disable unsupported keyboard controls",async()=>{
 const phone={...row("phone"),backend:"mobile",current_url:"",width:400,height:800,
  target:{kind:"mobile",device_id:"iphone-test"},
  capabilities:{target_kind:"mobile",actions:["screenshot","click","type","key","scroll","wait","home","launch_app","stop_app"],keys:["home","enter"],observations:["screenshot"],context:{reuse:true,persistence:true,isolation:false,snapshot_restore:false}}};
 install("",async()=>Response.json({sessions:[phone]}));
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 const byTitle=(title:string)=>root!.root.findAll(n=>n.type==="button"&&n.props.title===title)[0];
 expect(byTitle("Home")).toBeDefined();
 expect(byTitle("Backspace").props.disabled).toBe(true);
 expect(byTitle("Delete").props.disabled).toBe(true);
 expect(root!.root.findAll(n=>n.type==="input"&&n.props["aria-label"]==="App ID")).toHaveLength(1);
});

test("opening a Mobile context sends a device binding without browser defaults",async()=>{
 install("",async()=>Response.json({sessions:[]}));
 let submitted:any;
 const baseFetch=globalThis.fetch;
 globalThis.fetch=(async(input:any,init:any)=>{
  if(init?.method==="POST"&&String(input).split("?")[0].endsWith("/sessions")){
   submitted=JSON.parse(init.body);return Response.json({session_id:"phone"});
  }
  return baseFetch(input,init);
 }) as typeof fetch;
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 const open=root!.root.findAll(n=>n.type==="button"&&n.props.title==="Open browser")[0];
 // Use the sidebar's open action; its visible title remains compatible.
 const opener=open??root!.root.findAll(n=>n.type==="button"&&n.props.onClick&&String(n.props.children).includes("Open"))[0];
 expect(opener).toBeDefined();
 await act(async()=>opener.props.onClick());
 const modal=root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="OpenSessionModal")[0];
 expect(modal).toBeDefined();
 const backend=modal.findAll(n=>n.type==="select")[0];
 await act(async()=>backend.props.onChange({target:{value:"mobile"}}));
 const device=modal.findAll(n=>n.type==="input"&&n.props["aria-label"]==="Device ID")[0];
 await act(async()=>device.props.onChange({target:{value:"iphone-test"}}));
 await act(async()=>modal.findAll(n=>n.type==="button"&&n.props.title==="Open session")[0].props.onClick());
 expect(submitted.target).toEqual({kind:"mobile",device_id:"iphone-test"});
 expect(submitted.viewport).toBeUndefined();
 expect(submitted.persist).toBeUndefined();
 expect(submitted.proxy_mode).toBeUndefined();
});

test("Mobile startup authorization explains the required action without opening a session",async()=>{
 install("",async()=>Response.json({sessions:[]}));
 const baseFetch=globalThis.fetch;
 globalThis.fetch=(async(input:any,init:any)=>{
  if(init?.method==="POST"&&String(input).split("?")[0].endsWith("/sessions"))return Response.json({success:false,status:"needs_user_action",error:"Mobile needs authorization on the iPhone.",next_step:"Complete the XCTest passcode prompt directly on the phone, then retry."});
  return baseFetch(input,init);
 }) as typeof fetch;
 await act(async()=>{root=create(<ComputerPanel projectId="one"/>)});
 const opener=root!.root.findAll(n=>n.type==="button"&&n.props.onClick&&String(n.props.children).includes("Open"))[0];
 await act(async()=>opener.props.onClick());
 const modal=root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="OpenSessionModal")[0];
 await act(async()=>modal.findAll(n=>n.type==="button"&&n.props.title==="Open session")[0].props.onClick());
 expect(modal.findAll(n=>n.type==="div"&&typeof n.props.children==="string"&&n.props.children.includes("Complete the XCTest passcode prompt directly on the phone")).length).toBeGreaterThan(0);
 expect(root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="OpenSessionModal")).toHaveLength(1);
});


test("host install identity scopes panel reads, screenshots and writes", async()=>{
 const requests:{url:URL,method:string}[]=[];
 install("?instance=requested",async()=>Response.json({sessions:[row("requested")]}));
 const baseFetch=globalThis.fetch;
 globalThis.fetch=(async(input:any,init:any)=>{
  requests.push({url:new URL(String(input),"http://localhost"),method:init?.method??"GET"});
  return baseFetch(input,init);
 }) as typeof fetch;
 await act(async()=>{root=create(<ComputerPanel projectId="one" installId={55}/>)});
 expect(requests.length).toBeGreaterThan(3);
 for(const {url} of requests){expect(url.searchParams.get("install_id")).toBe("55");expect(url.searchParams.get("project_id")).toBe("one")}
 const image=root!.root.findAll(n=>n.type==="img")[0];
 expect(new URL(image.props.src,"http://localhost").searchParams.get("install_id")).toBe("55");
 // A session command goes through the same scoped route as every read.
 const detail=root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="SessionDetail")[0];
 await act(async()=>detail.props.onClose("requested"));
 const confirmation=root!.root.findAll(n=>typeof n.type==="function"&&n.type.name==="ConfirmModal")[0];
 await act(async()=>confirmation.props.onConfirm());
 expect(requests.some(r=>r.method==="DELETE"&&r.url.searchParams.get("install_id")==="55")).toBe(true);
});

test("switching installs cannot accept an old install's delayed response",async()=>{
 let release!:(r:Response)=>void;
 install("",async(url)=>url.searchParams.get("install_id")==="55"?new Promise(r=>{release=r}):Response.json({sessions:[row("new-install")]}));
 await act(async()=>{root=create(<ComputerPanel projectId="one" installId={55}/>)});
 await act(async()=>root!.update(<ComputerPanel projectId="one" installId={56}/>));
 expect(selected()).toBe("new-install");
 await act(async()=>release(Response.json({sessions:[row("old-install")]})));
 expect(selected()).toBe("new-install");
});
