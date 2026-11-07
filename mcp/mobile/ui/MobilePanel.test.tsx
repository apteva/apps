import {afterEach,beforeEach,expect,mock,test} from "bun:test";
import {Window} from "happy-dom";
import {act} from "react";
import type {Root} from "react-dom/client";
mock.module("@apteva/ui-kit",()=>({StatusDot:({children}:any)=><span>{children}</span>}));
const initial=new Window(); Object.assign(globalThis,{window:initial,document:initial.document});
const {createRoot}=await import("react-dom/client");
const {default:MobilePanel}=await import("./MobilePanel");
let browser:Window,root:Root,container:HTMLElement;
const oldFetch=globalThis.fetch;
let requests:{tool:string,args:any}[], setup:any, currentSession:any, attempts:number, preparing:boolean;
const phone={id:"ios:PHONE",name:"Spare iPhone",platform:"ios",physical:true,driver:"xctest",state:"ready",capabilities:["screen"]};
const json=(v:unknown,status=200)=>new Response(JSON.stringify(v),{status,headers:{"Content-Type":"application/json"}});
const button=(label:string)=>[...container.querySelectorAll("button")].find(b=>b.textContent===label)!;
const click=async(label:string)=>{expect(button(label)).toBeDefined();await act(async()=>{button(label).click();});};
beforeEach(()=>{
 browser=new Window();Object.assign(globalThis,{window:browser,document:browser.document,HTMLElement:browser.HTMLElement,Event:browser.Event,IS_REACT_ACT_ENVIRONMENT:true});
 container=browser.document.createElement("div") as any;browser.document.body.appendChild(container as any);root=createRoot(container);
 requests=[];currentSession=null;attempts=0;preparing=false;
 setup={state:"ready",message:"Signing configured automatically",team_id:"AAAAAAAAAA",teams:[{id:"AAAAAAAAAA",name:"Personal Team",personal:true}]};
 globalThis.fetch=(async(_input:any,init:any)=>{
  const req=JSON.parse(init.body);requests.push(req);const a=req.args;
  switch(req.tool){
   case "mobile_devices":return json({devices:[phone],diagnostics:[]});
   case "mobile_session":
    if(a.action==="list")return json({sessions:currentSession?[currentSession]:[]});
    if(a.action==="open"){
     attempts++;if(preparing&&attempts===1)return json({code:"setup_in_progress",error:"Building runner"},502);
     currentSession={id:"session-1",device_id:phone.id,device:phone,status:"active",mode:"screen",expires_at:Date.now()/1000+900};
     return json({session:currentSession});
    }
    break;
   case "mobile_setup":
    if(a.action==="select_team")setup={...setup,state:"ready",team_id:a.team_id,message:"Signing configured automatically"};
    return json(setup);
   case "mobile_test_get":return json({runs:[]});
   case "mobile_record":return json({recordings:[],presets:[]});
   case "mobile_observe":return json({mime_type:"image/png",image_base64:"",width:100,height:200});
  }
  throw new Error("Unexpected request: "+JSON.stringify(req));
 }) as typeof fetch;
});
afterEach(async()=>{await act(async()=>root.unmount());globalThis.fetch=oldFetch;await browser.happyDOM.close();});
const render=()=>act(async()=>root.render(<MobilePanel projectId="p"/>));

test("first iPhone session uses automatic signing without certificate instructions",async()=>{
 await render();await click("Open session");expect(attempts).toBe(1);expect(container.textContent).toContain("Screen session");
 await click("Setup");expect(container.textContent).toContain("Signing configured automatically");expect(container.textContent).toContain("no manual certificate creation");
});
test("team selection resumes the requested iPhone connection",async()=>{
 setup={state:"needs_team",message:"Choose your Apple signing team",teams:[{id:"AAAAAAAAAA",name:"Personal",personal:true},{id:"BBBBBBBBBB",name:"Company",personal:false}]};
 await render();await click("Open session");expect(attempts).toBe(0);expect(container.textContent).toContain("Choose your Apple signing team");
 const select=[...container.querySelectorAll("select")].find(s=>s.closest("label")?.textContent?.includes("Apple signing team"))!;
 await act(async()=>{select.value="BBBBBBBBBB";select.dispatchEvent(new browser.Event("change",{bubbles:true}) as any);});
 expect(requests).toContainEqual({tool:"mobile_setup",args:{action:"select_team",team_id:"BBBBBBBBBB"}});expect(attempts).toBe(1);
});
test("account setup opens Xcode and reports the Apple sign-in step",async()=>{
 setup={state:"needs_account",message:"Sign in under Xcode > Settings > Accounts",teams:[]};
 await render();await click("Open session");await click("Open Xcode to sign in");
 expect(requests).toContainEqual({tool:"mobile_setup",args:{action:"open_xcode"}});expect(attempts).toBe(0);
});
test("connection resumes after the operator completes Apple sign-in",async()=>{
 setup={state:"needs_account",message:"Sign in to Xcode",teams:[]};
 await render();await click("Open session");expect(attempts).toBe(0);
 setup={state:"ready",message:"Signing configured automatically",team_id:"AAAAAAAAAA",teams:[{id:"AAAAAAAAAA",name:"Personal",personal:true}]};
 await act(async()=>{await new Promise(r=>setTimeout(r,5200));});
 expect(attempts).toBe(1);expect(container.textContent).toContain("Screen session");
},10000);
test("first build retries automatically and shows preparation progress",async()=>{
 preparing=true;await render();await click("Open session");
 expect(container.textContent).toContain("Preparing your iPhone");
 await act(async()=>{await new Promise(r=>setTimeout(r,3200));});
 expect(attempts).toBe(2);expect(container.textContent).toContain("Screen session");
});
