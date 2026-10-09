import "./testDom";
import { beforeEach,afterEach,test,expect } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot,type Root } from "react-dom/client";
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "../frontend/src/client";
import { ConversationsProvider } from "../frontend/src/context";
import { MessageComponents,ConversationComponentProvider } from "../frontend/src/messageComponents";
import type { Message } from "../frontend/src/types";
let win:Window,root:Root,element:HTMLElement,posts:Array<{url:string;body:any}>,fail:boolean;
const NativeResponse=globalThis.Response;
const fixture=(blocking=false):Message=>({id:9,conversation_id:"chat",role:"agent",content:"I will use concise and continue.",component_kind:"interactive",created_at:"2026-10-09T12:00:00Z",components:[{id:"tone",version:1,app:"conversations",name:"question-card",props:{title:"Which tone?"},interaction:{blocking,status:"pending",fields:[{id:"tone",label:"Tone",type:"choice",required:true,allow_custom:true,options:[{id:"concise",label:"Concise"},{id:"detailed",label:"Detailed"}]}],actions:[{id:"submit",label:"Submit",style:"primary"},{id:"skip",label:"Skip",cancel:true}]}}]});
let message:Message;
beforeEach(()=>{win=new Window({url:"http://localhost/"});Object.assign(globalThis,{window:win,document:win.document,navigator:win.navigator,HTMLElement:win.HTMLElement,HTMLInputElement:win.HTMLInputElement,HTMLSelectElement:win.HTMLSelectElement,Event:win.Event,sessionStorage:win.sessionStorage,IS_REACT_ACT_ENVIRONMENT:true});element=win.document.createElement("div") as any;win.document.body.append(element as any);root=createRoot(element);posts=[];fail=false;message=fixture();globalThis.fetch=(async(url:any,init?:RequestInit)=>{const body=JSON.parse(String(init?.body??"{}"));posts.push({url:String(url),body});if(fail)return new NativeResponse("Temporarily unavailable",{status:503});const updated=structuredClone(message);const c=updated.components[0];if(c.interaction){c.interaction.status=body.action_id==="skip"?"cancelled":"answered";c.interaction.values=body.values;c.interaction.action_id=body.action_id}else c.props.status=body.action_id;return new NativeResponse(JSON.stringify({message:updated}),{headers:{"Content-Type":"application/json"}})}) as any;});
afterEach(async()=>{await act(async()=>root.unmount());await win.happyDOM.abort()});
const client=(global=false)=>new AptevaClient({baseURL:"http://localhost",accessToken:"fixture-key"}).use(conversationsExtension({global}),global?{installId:7}:{projectId:"project",installId:7});
const render=async(custom?:React.ReactNode,projectId?:string)=>{await act(async()=>root.render(<ConversationsProvider conversations={client(Boolean(projectId))}>{custom??<MessageComponents message={message} projectId={projectId}/>}</ConversationsProvider>))};
const click=async(text:string)=>{await act(async()=>{([...element.querySelectorAll("button")].find(b=>b.textContent===text) as HTMLElement).click();await new Promise(r=>setTimeout(r,5))})};
const select=async(value:string)=>{await act(async()=>{const s=element.querySelector("select")!;s.value=`option:${value}`;s.dispatchEvent(new win.Event("change",{bubbles:true}) as any)})};
test("optional question never sends on selection; submit saves an answer in the common action contract",async()=>{await render();expect(element.textContent).toContain("Optional — work can continue");expect(element.querySelector(".chat-thinking-dots")).toBeNull();await select("detailed");expect(posts).toHaveLength(0);await click("Submit");expect(posts).toHaveLength(1);expect(posts[0].body).toEqual({message_id:9,component_id:"tone",action_id:"submit",values:{tone:"detailed"}});expect(element.textContent).toContain("Answered");expect(element.textContent).toContain("Tone: Detailed");expect(element.querySelector("select")).toBeNull()});
test("blocking widget shows a waiting state; skip does not need required answers",async()=>{message=fixture(true);await render();expect(element.textContent).toContain("Waiting for your answer");await click("Skip");expect(element.textContent).toContain("Skipped");expect(posts[0].body.values).toEqual({})});
test("failed submit retains the selection and stays actionable",async()=>{await render();await select("concise");fail=true;await click("Submit");expect(element.querySelector('[role="alert"]')).not.toBeNull();expect(element.querySelector("select")!.value).toBe("option:concise");fail=false;await click("Submit");expect(element.textContent).toContain("Answered")});
test("the same renderer submits in the inbox item's project scope",async()=>{await render(undefined,"other-project");await select("concise");await click("Submit");expect(posts[0].url).toContain("project_id=other-project");expect(posts[0].url).toContain("install_id=7")});
test("legacy approvals use their existing verdict endpoint; reports and alerts render through the shared host",async()=>{message={...fixture(),component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approval",status:"pending",actions:[{id:"approve",label:"Approve"}]}},{app:"conversations",name:"report-card",props:{title:"Report",summary:"Evidence"}},{app:"conversations",name:"alert-card",props:{text:"Alert",severity:"warn"}}]};await render();expect(element.textContent).toContain("Evidence");expect(element.textContent).toContain("Alert");await click("Approve");expect(posts[0].body).toEqual({message_id:9,action_id:"approve",note:""})});
test("host-provided widget renderer reuses generic actions and future versions stay read-only",async()=>{message.components[0].app="example";message.components[0].name="picker";const Picker=({submit}:any)=><button onClick={()=>void submit("submit",{tone:"detailed"})}>Pick</button>;await render(<ConversationComponentProvider renderers={{"example/picker":Picker}}><MessageComponents message={message}/></ConversationComponentProvider>);await click("Pick");expect(posts[0].body.component_id).toBe("tone");message.components[0].version=2;await render();expect(element.textContent).toContain("This widget version is unavailable");expect(element.querySelector("button")).toBeNull()});

test("pending form draft survives a pane remount and clears after resolution",async()=>{
 const scopedClient=client();
 const mount=async()=>{await act(async()=>root.render(<ConversationsProvider conversations={scopedClient}><MessageComponents message={message}/></ConversationsProvider>))};
 await mount();await select("detailed");await act(async()=>root.render(null));await mount();
 expect(element.querySelector("select")!.value).toBe("option:detailed");
 await click("Submit");
 expect([...Array(sessionStorage.length)].map((_,i)=>sessionStorage.key(i)).filter(key=>key?.includes("component-draft"))).toHaveLength(0);
});

test("messages without components add no empty wrapper or transcript spacing",async()=>{message.components=[];await render();expect(element.children).toHaveLength(0)});
