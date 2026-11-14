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
const phone={id:"ios:PHONE",name:"Spare iPhone",platform:"ios",physical:true,driver:"xctest",state:"ready",capabilities:["screen","keep_awake"]};
let targetPhone=phone, power:any, denyPower:boolean, powerReadError:string;
const json=(v:unknown,status=200)=>new Response(JSON.stringify(v),{status,headers:{"Content-Type":"application/json"}});
const button=(label:string)=>[...container.querySelectorAll("button")].find(b=>b.textContent===label)!;
const click=async(label:string)=>{expect(button(label)).toBeDefined();await act(async()=>{button(label).click();});};
beforeEach(()=>{
 browser=new Window();Object.assign(globalThis,{window:browser,document:browser.document,HTMLElement:browser.HTMLElement,Event:browser.Event,IS_REACT_ACT_ENVIRONMENT:true});
 container=browser.document.createElement("div") as any;browser.document.body.appendChild(container as any);root=createRoot(container);
 requests=[];currentSession=null;attempts=0;preparing=false;
 targetPhone=phone;power={keep_awake:false,scope:"always",keep_awake_while_charging:false,charging_sources:[]};denyPower=false;powerReadError="";
 setup={state:"ready",message:"Signing configured automatically",team_id:"AAAAAAAAAA",teams:[{id:"AAAAAAAAAA",name:"Personal Team",personal:true}]};
 globalThis.fetch=(async(_input:any,init:any)=>{
  const req=JSON.parse(init.body);requests.push(req);const a=req.args;
  switch(req.tool){
   case "mobile_devices":return json({devices:[targetPhone],diagnostics:[]});
   case "mobile_session":
    if(a.action==="list")return json({sessions:currentSession?[currentSession]:[]});
    if(a.action==="open"){
     attempts++;if(preparing&&attempts===1)return json({code:"setup_in_progress",error:"Building runner"},502);
     currentSession={id:"session-1",device_id:targetPhone.id,device:targetPhone,status:"active",mode:"screen",expires_at:Date.now()/1000+900};
     return json({session:currentSession});
    }
    if(a.action==="close"){currentSession=null;return json({session:{id:"session-1",status:"closed"}});}
    break;
   case "mobile_power":
    if(a.action==="status"&&powerReadError)return json({error:powerReadError,code:"device_unavailable"},409);
    if(a.action==="set"){
     if(denyPower)return json({error:"Device policy denied the change",code:"device_unavailable"},409);
     power={...power,keep_awake:a.enabled,keep_awake_while_charging:power.scope==="while_charging"&&a.enabled,charging_sources:power.scope==="while_charging"&&a.enabled?["AC","USB","wireless","dock"]:[]};
    }
    return json(power);
   case "mobile_setup":
    if(a.action==="select_team")setup={...setup,state:"ready",team_id:a.team_id,message:"Signing configured automatically"};
    return json(setup);
   case "mobile_test_get":return json({runs:[]});
   case "mobile_record":return json({recordings:[],presets:[]});
   case "mobile_action":return json({ok:true});
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

const android=()=>{targetPhone={...phone,id:"android:PHONE",name:"Spare Android",platform:"android",driver:"adb",capabilities:["screen","keep_awake"]};power={...power,scope:"while_charging",normal_screen_timeout_ms:30000};};
const powerToggle=()=>container.querySelector('input[aria-label="Keep awake"]') as HTMLInputElement;
test("Android power toggle reads the phone and persists on then off explicitly",async()=>{
 android();await render();expect(powerToggle()).toBeNull();await click("Open session");
 expect(powerToggle().checked).toBe(false);expect(container.textContent).toContain("Unplugged, its normal screen timeout applies");
 await act(async()=>{powerToggle().click();});
 expect(requests).toContainEqual({tool:"mobile_power",args:{session_id:"session-1",action:"set",enabled:true}});expect(powerToggle().checked).toBe(true);
 await act(async()=>{powerToggle().click();});
 expect(requests).toContainEqual({tool:"mobile_power",args:{session_id:"session-1",action:"set",enabled:false}});expect(powerToggle().checked).toBe(false);
});
test("Android power state survives closing and reopening the session",async()=>{
 android();power={...power,keep_awake:true,keep_awake_while_charging:true,charging_sources:["USB"]};await render();await click("Open session");
 expect(powerToggle().checked).toBe(true);await click("Close session");await click("Open session");
 expect(powerToggle().checked).toBe(true);expect(requests.filter(r=>r.tool==="mobile_power"&&r.args.action==="set")).toHaveLength(0);
});
test("rejected power changes show the error and retain actual phone state",async()=>{
 android();denyPower=true;await render();await click("Open session");await act(async()=>{powerToggle().click();});
 expect(container.textContent).toContain("Device policy denied the change");expect(powerToggle().checked).toBe(false);
});
test("unsupported devices do not offer power settings",async()=>{
 targetPhone={...phone,capabilities:["screen"]};await render();await click("Open session");expect(powerToggle()).toBeNull();expect(requests.some(r=>r.tool==="mobile_power")).toBe(false);
});

test("iPhone shares the power toggle with its always-on scope and restore explanation",async()=>{
 await render();await click("Open session");
 expect(powerToggle().checked).toBe(false);
 expect(container.textContent).toContain("Auto-Lock to Never, including on battery");
 expect(container.textContent).toContain("restores the saved timeout, or 30 seconds");
 expect(container.textContent).not.toContain("Unplugged, its normal screen timeout applies");
 await act(async()=>{powerToggle().click();});expect(powerToggle().checked).toBe(true);
 expect(requests).toContainEqual({tool:"mobile_power",args:{session_id:"session-1",action:"set",enabled:true}});
 await click("Close session");await click("Open session");expect(powerToggle().checked).toBe(true);
 await act(async()=>{powerToggle().click();});expect(powerToggle().checked).toBe(false);
 expect(requests.filter(r=>r.tool==="mobile_power"&&r.args.action==="set").map(r=>r.args.enabled)).toEqual([true,false]);
});
test("power refresh is explicit and normal actions do not revisit Settings",async()=>{
 await render();await click("Open session");await click("Home");
 expect(requests.filter(r=>r.tool==="mobile_power"&&r.args.action==="status")).toHaveLength(1);
 power={...power,keep_awake:true};await click("Refresh power");expect(powerToggle().checked).toBe(true);
 expect(requests.filter(r=>r.tool==="mobile_power"&&r.args.action==="status")).toHaveLength(2);
});
test("a blocked iPhone read can be retried without automatically changing its preference",async()=>{
 powerReadError="Turn off Low Power Mode";await render();await click("Open session");
 expect(container.textContent).toContain("Turn off Low Power Mode");expect(powerToggle().disabled).toBe(true);
 powerReadError="";await click("Refresh power");expect(powerToggle().disabled).toBe(false);
 expect(container.textContent).not.toContain("Turn off Low Power Mode");
 expect(requests.filter(r=>r.tool==="mobile_power"&&r.args.action==="set")).toHaveLength(0);
});
