import { type ConversationLocalization } from "../frontend/src/i18n";
import { ConversationThread, ReportCard, AlertCard, ConversationLocalizationProvider } from "../frontend/src/react";
import "./testDom";
import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { createRoot, type Root } from "react-dom/client";
import { act, createRef } from "react";
import { ConversationChat as ChatSource, refreshConversationList, type Conversation } from "../frontend/src/ConversationsPanel";
import { reportSectionsText } from "./messageContent";

import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension, type ConversationsClient } from "../frontend/src/client";
import { ConversationsProvider, PageContextProvider } from "../frontend/src/context";
import type { PageContext } from "../frontend/src/pageContext";
import { showPageContext, type AgentConversationWidgetSettings } from "../frontend/src/agentConversations";
import type { ConversationComposerHandle } from "../frontend/src/composerHost";
import { resizeComposerInput } from "../frontend/src/composer";
import type { ComponentProps } from "react";
import { ConversationUnreadIndicator } from "../frontend/src/conversationActivity";
let conversations: ConversationsClient;
function ConversationChat(props: ComponentProps<typeof ChatSource> & ConversationLocalization & { pageContext?: PageContext; widgetSettings?: AgentConversationWidgetSettings }) {
 const { pageContext, widgetSettings, ...chatProps } = props;
 return <PageContextProvider.Provider value={pageContext}><ConversationsProvider conversations={conversations} locale={props.locale} timeZone={props.timeZone} messages={props.messages}><ChatSource {...chatProps} showPageContext={showPageContext(widgetSettings)}/></ConversationsProvider></PageContextProvider.Provider>;
}
let win: Window, root: Root, element: HTMLElement;
let fetcher: (url:string, init?:RequestInit) => Promise<Response>;
class FakeEvents {
 static instances: FakeEvents[]=[];
 onopen: (()=>void)|null=null; onmessage: ((e:{data:string})=>void)|null=null; onerror: (()=>void)|null=null;
 listeners=new Map<string,(e:{data:string})=>void>();
 constructor(public url:string) {FakeEvents.instances.push(this);}
 addEventListener(name:string,fn:(e:{data:string})=>void){this.listeners.set(name,e=>fn({...e,type:name} as any));}
 removeEventListener(name:string){this.listeners.delete(name);}
 close(){}
 emit(message:unknown){this.listeners.get("message")?.({data:JSON.stringify(message)});}
}
const conv=(id:string):Conversation=>({id,project_id:"project",lead_agent_id:41,title:id,kind:"direct",origin:"web",created_at:"",updated_at:""});
const message=(id:number,conversation_id="a",content=`message ${id}`)=>({id,conversation_id,content,role:"user",components:[],created_at:"2026-09-05T00:00:00Z"});
const NativeResponse=globalThis.Response;
class Response extends NativeResponse {
 constructor(body?:BodyInit|null,init?:ResponseInit){super(body,{...init,headers:{"Content-Type":"application/json",...init?.headers}});}
}
const json=(value:unknown)=>Promise.resolve(new Response(JSON.stringify(value),{headers:{"Content-Type":"application/json"}}));
const settle=async()=>{await act(async()=>{await new Promise(resolve=>setTimeout(resolve,15));});};
const render=async(id="a")=>{await act(async()=>root.render(<ConversationChat key={id} conversation={conv(id)} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));await settle();};
const type=async(value:string)=>{await act(async()=>{const input=element.querySelector("textarea")!;input.focus();Object.getOwnPropertyDescriptor(win.HTMLTextAreaElement.prototype,"value")!.set!.call(input,value);input.dispatchEvent(new win.Event("input",{bubbles:true}) as unknown as Event);input.dispatchEvent(new win.Event("change",{bubbles:true}) as unknown as Event);input.dispatchEvent(new win.KeyboardEvent("keyup",{key:"o",bubbles:true}) as unknown as Event);});};
const send=async()=>{await act(async()=>{element.querySelector("textarea")!.dispatchEvent(new win.KeyboardEvent("keydown",{key:"Enter",bubbles:true}) as unknown as Event);});};
beforeEach(()=>{
 win=new Window({url:"http://localhost/"});
 Object.assign(globalThis,{window:win,document:win.document,navigator:win.navigator,HTMLElement:win.HTMLElement,HTMLTextAreaElement:win.HTMLTextAreaElement,Event:win.Event,KeyboardEvent:win.KeyboardEvent,sessionStorage:win.sessionStorage,EventSource:FakeEvents,IS_REACT_ACT_ENVIRONMENT:true});
 win.HTMLElement.prototype.scrollIntoView=()=>{};
 element=win.document.createElement("div") as unknown as HTMLElement;win.document.body.appendChild(element as any);root=createRoot(element);FakeEvents.instances=[];
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json(url.includes("/changes")?{messages:[],cursor:0,has_more:false}:{messages:[],cursor:0,has_more:false,before:0});
 globalThis.fetch=((url:unknown,init?:RequestInit)=>fetcher(String(url),init)) as typeof fetch;
 conversations=new AptevaClient({baseURL:""}).use(conversationsExtension(),{projectId:"project",installId:7});
});
afterEach(async()=>{await act(async()=>root.unmount());await win.happyDOM.abort();});

test("unread conversations use the shared dot indicator without a numeric badge", async () => {
  await act(async () => root.render(
    <ConversationLocalizationProvider>
      <ConversationUnreadIndicator unread />
    </ConversationLocalizationProvider>,
  ));
  const indicator = element.querySelector('[aria-label="Unread messages"]');
  expect(indicator).not.toBeNull();
  expect(indicator?.className).toContain("chat-thread-unread-dot");
  expect(indicator?.className).not.toContain("chat-thread-working-dot");
  expect(element.textContent).toBe("");
  await act(async () => root.render(
    <ConversationLocalizationProvider>
      <ConversationUnreadIndicator unread={false} />
    </ConversationLocalizationProvider>,
  ));
  expect(element.querySelector('[aria-label="Unread messages"]')).toBeNull();
});

test("composer autosize remeasures after a narrow wrapped draft is cleared", () => {
  const input = win.document.createElement("textarea");
  let contentHeight = 48;
  Object.defineProperty(input, "scrollHeight", {
    configurable: true,
    get: () => input.style.height === "0px" ? contentHeight : 144,
  });
  resizeComposerInput(input);
  expect(input.style.height).toBe("48px");
  contentHeight = 48;
  input.style.height = "144px";
  resizeComposerInput(input);
  expect(input.style.height).toBe("48px");
});

test("voice mic appears only in active direct operator chats",async()=>{
 await render();
 expect(element.querySelector('[aria-label="Start voice in this chat"]')).not.toBeNull();
 await act(async()=>root.render(<ConversationChat conversation={{...conv("a"),audience:"public"}} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));
 expect(element.querySelector('[aria-label="Start voice in this chat"]')).toBeNull();
 await act(async()=>root.render(<ConversationChat conversation={{...conv("a"),kind:"room"}} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));
 expect(element.querySelector('[aria-label="Start voice in this chat"]')).toBeNull();
 await act(async()=>root.render(<ConversationChat conversation={conv("a")} archived={true} onActed={()=>{}} onRemoved={()=>{}}/>));
 expect(element.querySelector('[aria-label="Start voice in this chat"]')).toBeNull();
});

test("offline agents keep the draft but block sending", async () => {
  const posted: string[] = [];
  fetcher = (url, init) => {
    if (init?.method === "POST" && url.includes("/messages")) {
      posted.push(String(init.body));
      return json(message(1, "a", "should not send"));
    }
    return (url.includes("/deliveries") || url.includes("/activity"))
      ? json([])
      : json({ messages: [], cursor: 0, has_more: false, before: 0 });
  };
  await act(async () => root.render(<ConversationChat conversation={conv("a")} agentOnline={false} archived={false} onActed={() => {}} onRemoved={() => {}} />));
  await settle();
  await type("keep this draft");
  const status = element.querySelector('[role="img"][aria-label="Agent offline — sending is unavailable"]');
  expect(status?.className).toContain("bg-error");
  expect(status?.className).not.toContain("bg-success");
  expect(element.textContent).not.toContain("Agent offline");
  expect((element.querySelector("button[type=submit]") as HTMLButtonElement).disabled).toBe(true);
  await send();
  expect(posted).toHaveLength(0);
  expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("keep this draft");
});

test("saved spoken turns are marked as voice in the existing transcript",async()=>{
 const spoken={...message(1,"a","Spoken question"),metadata:{source:"voice"}};
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[spoken],cursor:1,before:1,has_more:false});
 await render();
 expect(element.textContent).toContain("Spoken question");
 expect([...element.querySelectorAll("span")].some(span=>span.textContent==="Voice" && span.title.includes("Voice turns"))).toBe(true);
});

test("dictation updates the existing draft and waits for an explicit send",async()=>{
 const posted:string[]=[];
 class Recognition {
  static current:Recognition;
  lang="";continuous=false;interimResults=false;
  onresult:((event:any)=>void)|null=null;onerror:null=null;onend:(()=>void)|null=null;
  constructor(){Recognition.current=this;}
  start(){} stop(){this.onend?.();} abort(){this.onend?.();}
 }
 Object.assign(win,{SpeechRecognition:Recognition});
 fetcher=(url,init)=>{
  if(url.includes("/voice"))return json({status:"closed",mode:"dictation"});
  if(init?.method==="POST"&&url.includes("/messages")){posted.push(JSON.parse(String(init.body)).content);return json(message(1,"a",posted[0]));}
  return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 };
 await render();await settle();
 await act(async()=>element.querySelector('[aria-label="Dictate a message"]')!.dispatchEvent(new win.MouseEvent("click",{bubbles:true}) as unknown as Event));
 await settle();
 await act(async()=>Recognition.current.onresult?.({resultIndex:0,results:[{isFinal:true,0:{transcript:"Find the onboarding process"}}]}));
 expect(element.querySelector("textarea")!.value).toBe("Find the onboarding process");
 expect(posted).toEqual([]);
 await act(async()=>[...element.querySelectorAll("button")].find(button=>button.textContent==="Stop dictation")!.click());
 await send();await settle();
 expect(posted).toEqual(["Find the onboarding process"]);
});

test("a live row arriving before snapshot never advances durable replay cursor",async()=>{
 let resolveSnapshot!:(value:Response)=>void;const paths:string[]=[];
 fetcher=(url)=>{paths.push(url);if(url.includes("/deliveries")||url.includes("/activity"))return json([]);if(url.includes("page=1"))return new Promise(resolve=>{resolveSnapshot=resolve});return json({messages:[message(201)],cursor:201,has_more:false});};
 await act(async()=>root.render(<ConversationChat conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));
 await act(async()=>FakeEvents.instances[0].emit(message(501)));
 await act(async()=>resolveSnapshot(new Response(JSON.stringify({messages:[message(200)],cursor:200,before:200,has_more:true}))));await settle();
 expect(paths.some(path=>path.includes("cursor=200"))).toBe(true);
 expect(element.textContent).toContain("message 201");expect(element.textContent).toContain("message 501");
});
test("switching conversations keeps drafts and late replies out of the next chat",async()=>{
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json(url.includes("/changes")?{messages:[],cursor:0,has_more:false}:{messages:[message(1,url.includes("chat_id=b")?"b":"a")],cursor:1,before:1,has_more:false});
 await render("a");await type("private draft");await render("b");
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
 await act(async()=>FakeEvents.instances.at(-1)!.emit(message(900,"a","private late reply")));
 expect(element.textContent).not.toContain("private late reply");
});
test("ordinary retry preserves the original submission id",async()=>{
 const bodies:any[]=[];
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){bodies.push(JSON.parse(String(init.body)));return bodies.length===1?Promise.reject(new Error("lost response")):json(message(1,"a",bodies[0].content));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});};
 await render();await type("hello");await send();await settle();await send();await settle();
 expect(bodies.length).toBe(2);expect(bodies[1]).toEqual(bodies[0]);
});
test("page context is included in the posted message snapshot",async()=>{
 const bodies:any[]=[];
 const pageContext:PageContext={version:1,page:"app",project_id:"project",app:"tickets",installation_id:17,panel:"details"};
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){bodies.push(JSON.parse(String(init.body)));return json(message(1,"a","inspect this page"));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});};
 await act(async()=>root.render(<ConversationChat pageContext={pageContext} conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));await settle();
 expect(element.textContent).toContain("Using context: tickets app");
 await type("inspect this page");await send();await settle();
 expect(bodies).toHaveLength(1);expect(bodies[0].page_context).toEqual(pageContext);
});
test("welcome suggestions append to the draft without sending",async()=>{
 const posted:string[]=[];
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){posted.push(String(init.body));return json(message(1,"a","should not send"));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 };
 await act(async()=>root.render(<ConversationChat conversation={conv("a")} archived={false} welcomeText="What would you like to build?" suggestions={[{id:"build",label:"Build something",text:"Help me build a dashboard"}]} onActed={()=>{}} onRemoved={()=>{}}/>));await settle();
 expect(element.textContent).toContain("What would you like to build?");
 await act(async()=>[...element.querySelectorAll("button")].find(button=>button.textContent==="Build something")?.dispatchEvent(new win.MouseEvent("click",{bubbles:true}) as unknown as Event));
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("Help me build a dashboard");
 expect(posted).toHaveLength(0);
});
test("host composer requests are scoped, idempotent, and never send",async()=>{
 const posted:string[]=[];
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){posted.push(String(init.body));return json(message(1,"a","unexpected"));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 };
 const bridge=createRef<ConversationComposerHandle>();
 await act(async()=>root.render(<PageContextProvider.Provider value={undefined}><ConversationsProvider conversations={conversations}><ChatSource ref={bridge} conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/></ConversationsProvider></PageContextProvider.Provider>));await settle();
 let first:any;
 await act(async()=>{first=await bridge.current!.insertText("Add a report",{requestId:"req-1",projectId:"project",agentId:41,conversationId:"a"});});
 expect(first.status).toBe("applied");expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("Add a report");
 let duplicate:any;
 await act(async()=>{duplicate=await bridge.current!.insertText("Do not duplicate",{requestId:"req-1"});});
 expect(duplicate.status).toBe("already_applied");expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("Add a report");expect(posted).toHaveLength(0);
 let wrong:any;
 await act(async()=>{wrong=await bridge.current!.insertText("Wrong scope",{projectId:"other"});});
 expect(wrong.status).toBe("wrong_project");
});
test("hidden page context stays attached to the posted message",async()=>{
 const bodies:any[]=[];
 const pageContext:PageContext={version:1,page:"app",project_id:"project",app:"workspace-setup",panel:"stage=workspace_setup; goal=Launch a shop"};
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){bodies.push(JSON.parse(String(init.body)));return json(message(1,"a","help me configure this"));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});};
 await act(async()=>root.render(<ConversationChat pageContext={pageContext} widgetSettings={{show_page_context:false}} conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));await settle();
 expect(element.textContent).not.toContain("Using context:");
 expect(element.querySelector('[aria-label="Remove page context"]')).toBeNull();
 await type("help me configure this");await send();await settle();
 expect(bodies).toHaveLength(1);expect(bodies[0].page_context).toEqual(pageContext);
});
test("report sections preserve headings, content and additional structured fields",()=>{
 expect(reportSectionsText([{title:"Results",body:"All checks passed",metrics:{count:3}}])).toContain("All checks passed");
 expect(reportSectionsText([{title:"Results",body:"All checks passed",metrics:{count:3}}])).toContain('"count": 3');
});

test("two agents sharing a provider call id keep independent streaming bubbles",async()=>{
 await render();const events=FakeEvents.instances[0];
 const emit=(agent:number,text:string,done=false,run="1")=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:agent,thread_id:"chat-a",call_id:"same",run_id:run,text,done})});
 await act(async()=>{emit(41,"Alpha progress");emit(42,"Beta progress");});
 expect(element.textContent).toContain("Alpha progress");expect(element.textContent).toContain("Beta progress");
 await act(async()=>emit(41,"",true));
 expect(element.textContent).toContain("Alpha progress");expect(element.textContent).toContain("Beta progress");
 await act(async()=>events.emit({...message(1,"a","Alpha progress"),role:"agent",agent_id:41}));
 expect(element.textContent!.match(/Alpha progress/g)).toHaveLength(1);
 expect(element.textContent).toContain("Beta progress");
 await act(async()=>emit(41,"New response",false,"2"));
 expect(element.textContent).toContain("New response");
});
test("send completion preserves text typed while the request was pending",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?new Promise(resolve=>{complete=resolve}):(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render();await type("first");await send();await type("next draft");
 await act(async()=>complete(new Response(JSON.stringify(message(1,"a","first")))));await settle();
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("next draft");
});
test("a send acknowledged after switching clears only its own saved draft",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?new Promise(resolve=>{complete=resolve}):(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render("a");await type("first");await send();await render("b");await type("second draft");
 await act(async()=>complete(new Response(JSON.stringify(message(1,"a","first")))));await settle();
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("second draft");
 await render("a");expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
});
test("read marks require a visible loaded transcript at the bottom",async()=>{
 const marks:any[]=[];
 fetcher=(url,init)=>{if(url.includes("/seen")){marks.push(JSON.parse(String(init?.body)));return json({ok:true});}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json(url.includes("/changes")?{messages:[],cursor:10,has_more:false}:{messages:[message(10)],cursor:10,before:10,has_more:false});};
 Object.defineProperty(win.document,"visibilityState",{configurable:true,value:"hidden"});
 await render();expect(marks.length).toBe(0);
 const scroller=element.querySelector(".overflow-auto")! as HTMLElement;const bottom=scroller.lastElementChild! as HTMLElement;
 Object.defineProperties(scroller,{clientHeight:{configurable:true,value:400},scrollHeight:{configurable:true,value:400}});
 scroller.getBoundingClientRect=()=>({top:0,bottom:400}) as DOMRect;
 bottom.getClientRects=()=>[{}] as unknown as DOMRectList;bottom.getBoundingClientRect=()=>({top:390,bottom:400}) as DOMRect;
 await act(async()=>scroller.dispatchEvent(new win.Event("scroll") as unknown as Event));expect(marks.length).toBe(0);
 Object.defineProperty(win.document,"visibilityState",{configurable:true,value:"visible"});
 await act(async()=>win.document.dispatchEvent(new win.Event("visibilitychange")));await settle();
 expect(marks).toEqual([{chat_id:"a",last_seen_id:10}]);
});

test("list refresh reads every loaded page and drops deleted rows",async()=>{
 const paths:string[]=[];
 fetcher=(url)=>{paths.push(url);return url.includes("cursor=older")?json({conversations:[conv("older-survivor")],next_cursor:""}):json({conversations:Array.from({length:100},(_,i)=>conv(`fresh-${i}`)),next_cursor:"older"});};
 const rows=await refreshConversationList("/chats?agent_id=41",conversations,101);
 expect(paths.length).toBe(2);expect(paths.every(path=>path.includes("agent_id=41"))).toBe(true);expect(rows.length).toBe(101);expect(rows.at(-1)?.id).toBe("older-survivor");
});

test("an older history page cannot overwrite a newer live revision",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):url.includes("before=200")?new Promise(resolve=>{complete=resolve}):url.includes("/changes")?json({messages:[],cursor:200,has_more:false}):json({messages:[{...message(200),revision:200}],cursor:200,before:200,has_more:true});
 await render();const older=[...element.querySelectorAll("button")].find(b=>b.textContent==="Load earlier messages")!;
 await act(async()=>older.click());
 await act(async()=>FakeEvents.instances[0].emit({...message(100,"a","newly edited"),revision:501}));
 await act(async()=>complete(new Response(JSON.stringify({messages:[{...message(100,"a","stale version"),revision:100}],cursor:200,before:100,has_more:false}))));await settle();
 expect(element.textContent).toContain("newly edited");expect(element.textContent).not.toContain("stale version");
});

test("loading historical replies does not settle a current acknowledgement",async()=>{
 await render();const events=FakeEvents.instances[0];
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"ack-current",text:"",phase:"acknowledgement",after_message_id:300})}));
 await act(async()=>events.emit({...message(100,"a","historic reply"),role:"agent",agent_id:41}));
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await act(async()=>events.emit({...message(301,"a","current reply"),role:"agent",agent_id:41}));
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
});


test("dashboard migrates drafts and pending identities into the scoped client", async () => {
 const oldKey="conversations:draft:project:a";
 sessionStorage.setItem(oldKey,"saved draft");
 sessionStorage.setItem(oldKey+":pending",JSON.stringify({content:"saved draft",client_message_id:"original-id"}));
 const bodies:any[]=[];
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){bodies.push(JSON.parse(String(init.body)));return json(message(1,"a","saved draft"));}return (url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});};
 await act(async()=>root.render(<ConversationsProvider legacyDrafts conversations={conversations}><ChatSource conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/></ConversationsProvider>));await settle();
 expect(element.querySelector("textarea")!.value).toBe("saved draft");
 expect(sessionStorage.getItem(oldKey)).toBeNull();
 await send();await settle();
 expect(bodies[0].client_message_id).toBe("original-id");
});

test("changing the SDK client resets displayed history and drafts before the new response", async () => {
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[message(1,"a","first user history")],cursor:1,has_more:false,before:1});
 await render();await type("first user draft");
 expect(element.textContent).toContain("first user history");
 conversations=new AptevaClient({baseURL:""}).use(conversationsExtension(),{projectId:"project",installId:7});
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):new Promise(()=>{});
 await render();
 expect(element.textContent).not.toContain("first user history");
 expect(element.querySelector("textarea")!.value).toBe("");
});

test("exported thread changes locale and copy without replacing the draft, stream, or subscription", async () => {
 const renderThread = async (locale: string, messages?: ConversationLocalization["messages"]) => {
  await act(async () => root.render(<ConversationThread conversations={conversations} conversation={conv("a")} locale={locale} messages={messages}/>));
  await settle();
 };
 await renderThread("en");
 expect(element.textContent).toContain("No messages yet — say something.");
 await act(async () => FakeEvents.instances[0].listeners.get("open")?.({data:""}));
 expect(element.querySelector("textarea")!.placeholder).toBe("Message the agent…");
 await type("Mon brouillon");
 const input = element.querySelector("textarea")!;
 const eventSource = FakeEvents.instances[0];
 await renderThread("fr-FR", { "chat.empty": "Bienvenue <img src=x onerror=alert(1)>" });
 expect(element.querySelector("textarea")).toBe(input);
 expect(input.value).toBe("Mon brouillon");
 expect(input.placeholder).toBe("Écrivez à l’agent…");
 expect(element.querySelector('[aria-label="Envoyer"]')).not.toBeNull();
 expect(element.querySelector('[lang="fr-FR"]')).not.toBeNull();
 expect(element.textContent).toContain("Bienvenue <img src=x onerror=alert(1)>");
 expect(element.querySelector("img")).toBeNull();
 await act(async () => eventSource.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"locale-stream",run_id:"1",text:"Original agent response"})}));
 await renderThread("es-ES");
 expect(element.textContent).toContain("Original agent response");
 expect(element.querySelector("textarea")).toBe(input);
 expect(input.value).toBe("Mon brouillon");
 expect(input.placeholder).toBe("Escribe al agente…");
 expect(FakeEvents.instances).toEqual([eventSource]);
});

test("localization providers and individual surfaces isolate overrides and preserve authored card content", async () => {
 const report = {...message(10), role:"agent", component_kind:"report", components:[{app:"conversations",name:"report-card",props:{title:"Original report title",summary:"Original report content"}}]} as any;
 const alert = {...message(11), role:"agent", component_kind:"alert", components:[{app:"conversations",name:"alert-card",props:{text:"Original alert",severity:"warn"}}]} as any;
 await act(async () => root.render(<>
  <ConversationLocalizationProvider locale="fr" messages={{"card.report":"Compte rendu"}}>
   <section id="french"><ReportCard message={report}/></section>
   <section id="spanish"><ReportCard message={report} locale="es" messages={{"card.report":"Informe propio"}}/></section>
   <section id="alert"><AlertCard message={alert}/></section>
  </ConversationLocalizationProvider>
  <section id="english"><ReportCard message={report}/></section>
 </>));
 expect(element.querySelector("#french")!.textContent).toContain("Compte rendu");
 expect(element.querySelector("#spanish")!.textContent).toContain("Informe propio");
 expect(element.querySelector("#english")!.textContent).toContain("Report");
 expect(element.querySelector("#alert")!.textContent).toContain("avertissement");
 for (const id of ["french","spanish","english"]) expect(element.querySelector(`#${id}`)!.textContent).toContain("Original report content");
});

test("soft break copy describes an advisory request and existing send failures follow locale changes", async () => {
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?Promise.reject(new Error("lost response")):(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render();
 await act(async () => FakeEvents.instances[0].listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"active",run_id:"1",text:"Working"})}));
 const breakButton=element.querySelector('[aria-label="Ask the agent to pause and reconsider"]')!;
 expect(breakButton.getAttribute("title")).toContain("it does not stop the agent or cancel running work");
 await type("Request"); await send(); await settle();
 expect(element.textContent).toContain("Send was not confirmed.");
 await act(async () => root.render(<ConversationChat key="a" conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}} locale="fr"/>));
 expect(element.textContent).toContain("L’envoi n’a pas été confirmé.");
 expect(element.textContent).toContain("lost response");
 expect(element.querySelector("textarea")!.value).toBe("Request");
});

test("stored Conversations work tools remain visible and approval actions use host theme",async()=>{
 const approval={...message(2),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approve deletion",body:"Delete repository?",status:"pending",actions:[{id:"approve",label:"Approve",style:"primary"},{id:"deny",label:"Deny",style:"secondary"}]}}]};
 fetcher=url=> url.includes("/activity") ? json([{id:1,chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"legacy",name:"conversations_request_approval",reason:"Requesting deletion approval",status:"running",started_at:message(1).created_at,ended_at:"",revision:1}]) : url.includes("/deliveries") ? json([]) : json({messages:[approval],cursor:2,before:2,has_more:false});
 await render();
 expect(element.textContent).toContain("Approve deletion");
 expect(element.textContent).not.toContain("Requesting deletion approval");
 const approvalProgress = FakeEvents.instances[0].listeners.get("stream")!;
 await act(async()=>approvalProgress({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"approval-call",text:"",done:false,response_progress:{phase:"running",run_id:"approval-run",revision:1,after_message_id:1,started_at:message(1).created_at,tool_name:"conversations_request_approval",call_id:"approval-call"}})}));
 expect(element.textContent).not.toContain("Thinking");
 expect(element.textContent).not.toContain("Requesting deletion approval");
 const approve=[...element.querySelectorAll("button")].find(b=>b.textContent==="Approve")!;
 const deny=[...element.querySelectorAll("button")].find(b=>b.textContent==="Deny")!;
 expect(approve.className).toContain("bg-accent");
 expect(deny.className).toContain("border-border");
 expect(approve.className+deny.className).not.toMatch(/bg-success|bg-error/);
});


for (const style of ["danger", undefined]) test(`approval choices stay distinct with style=${style ?? "omitted"}`, async () => {
 const approval={...message(2),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Confirm deleting RepeatList",status:"pending",actions:[{id:"approve",label:"Delete RepeatList",style},{id:"deny",label:"Keep RepeatList"}]}}]};
 fetcher=url=>url.includes("/activity")||url.includes("/deliveries")?json([]):json({messages:[approval],cursor:2,before:2,has_more:false});
 await render();
 const buttons=[...element.querySelectorAll("button")];
 const remove=buttons.find(button=>button.textContent==="Delete RepeatList")!;
 const keep=buttons.find(button=>button.textContent==="Keep RepeatList")!;
 expect(remove.className).toContain("border-accent");
 expect(remove.className).toContain("text-accent");
 expect(keep.className).toContain("border-border");
 expect(keep.className).not.toContain("text-accent");
 expect(remove.className+keep.className).not.toMatch(/bg-success|bg-error/);
});

test("approval decisions show the chosen label live and after reopening the chat", async () => {
 const approval={...message(301),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Delete Orbit Plans and todos",status:"pending",actions:[{id:"delete",label:"Delete list and todos"},{id:"keep",label:"Keep list and todos"}]}}]};
 let saved=approval;
 fetcher=url=>url.includes("/activity")||url.includes("/deliveries")?json([]):json({messages:[saved],cursor:301,before:301,has_more:false});
 await render();
 expect(element.textContent).not.toContain("Decision:");
 expect([...element.querySelectorAll("button")].some(button=>button.textContent==="Delete list and todos")).toBe(true);
 saved={...approval,components:[{...approval.components[0],props:{...approval.components[0].props,status:"delete",note:"Remove the seeded data"}}]} as typeof approval;
 await act(async()=>FakeEvents.instances[0].emit({...saved,revision:2}));
 expect(element.textContent).toContain("Decision: Delete list and todos");
 expect(element.textContent).toContain("Note: Remove the seeded data");
 expect([...element.querySelectorAll("button")].some(button=>button.textContent==="Delete list and todos")).toBe(false);
 await act(async()=>root.render(null));
 await render();
 expect(element.textContent).toContain("Decision: Delete list and todos");
 expect(element.textContent).toContain("Note: Remove the seeded data");
 expect(element.textContent).not.toContain("Keep list and todos");
});

test("legacy approval decisions fall back to localized statuses", async () => {
 const approvals=["approve","deny"].map((status,index)=>({...message(301+index),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Legacy approval",status}}]}));
 fetcher=url=>url.includes("/activity")||url.includes("/deliveries")?json([]):json({messages:approvals,cursor:302,before:301,has_more:false});
 await act(async()=>root.render(<ConversationChat conversation={conv("a")} locale="es" archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));
 await settle();
 expect(element.textContent).toContain("Decisión: aprobado");
 expect(element.textContent).toContain("Decisión: denegado");
});

test("alerts show their card without duplicate tool activity live or after reload", async () => {
 const alert={...message(301),role:"agent",agent_id:41,component_kind:"alert",components:[{app:"conversations",name:"alert-card",props:{text:"Test alert: 30 seconds have elapsed.",severity:"info"}}]};
 const activity={id:71,chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"alert-call",name:"conversations_conversations_alert",reason:"Sending scheduled test alert",status:"completed",started_at:message(300).created_at,ended_at:message(301).created_at,revision:2};
 await render();
 const events=FakeEvents.instances[0];
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{phase:"preparing_tool",run_id:"alert-run",revision:1,after_message_id:300,started_at:message(300).created_at,tool_name:activity.name,call_id:activity.call_id}})}));
 expect(element.querySelector(".chat-tool-activity")).toBeNull();
 await act(async()=>{
  events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,tool_activity:activity})});
  events.emit(alert);
 });
 await settle();
 expect(element.textContent).toContain("Test alert: 30 seconds have elapsed.");
 expect(element.textContent).not.toContain(activity.reason);
 expect(element.querySelector(".chat-tool-activity")).toBeNull();
 fetcher=url=>url.includes("/activity")?json([activity]):url.includes("/deliveries")?json([]):json({messages:[alert],cursor:301,before:301,has_more:false});
 await act(async()=>root.render(null));
 await render();
 expect(element.textContent).toContain("Test alert: 30 seconds have elapsed.");
 expect(element.querySelector(".chat-tool-activity")).toBeNull();
});

test("an acknowledgement keeps Thinking during active hidden pacing until idle", async () => {
 const ack={...message(301,"a","I’ll send a test alert in 37 seconds in this conversation."),role:"agent",agent_id:41,phase:"acknowledgement"};
 fetcher=url=>url.includes("/activity")||url.includes("/deliveries")?json([]):json({messages:[ack],cursor:301,before:301,has_more:false});
 await render();
 const events=FakeEvents.instances[0];
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{phase:"thinking",run_id:"pace-run",revision:1,after_message_id:300,started_at:message(300).created_at}})}));
 expect(element.textContent).toContain("I’ll send a test alert in 37 seconds in this conversation.");
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(1);
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{phase:"preparing_tool",run_id:"pace-run",revision:2,after_message_id:300,started_at:message(300).created_at,tool_name:"pace",call_id:"pace-call"}})}));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(1);
 expect(element.querySelectorAll(".chat-tool-activity").length).toBe(0);
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{phase:"idle",run_id:"pace-run",revision:3,after_message_id:300,started_at:message(300).created_at}})}));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(0);
});

test("new approval clears its agent's thinking while historical cards and other agents remain isolated", async () => {
 await render();
 const events=FakeEvents.instances[0];
 const ack=(agent:number)=>({chat_id:"a",agent_id:agent,thread_id:"chat-a",call_id:`ack-${agent}`,text:"",phase:"acknowledgement",after_message_id:300});
 await act(async()=>{for(const agent of [41,42])events.listeners.get("stream")?.({data:JSON.stringify(ack(agent))});});
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(2);
 const approval=(id:number)=>({...message(id),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Confirm deletion",status:"pending",actions:[{id:"approve",label:"Approve"},{id:"deny",label:"Deny"}]}}]});
 await act(async()=>events.emit(approval(200)));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(2);
 await act(async()=>events.emit(approval(301)));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(1);
 // Replayed acknowledgement cannot resurrect the completed agent's indicator.
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify(ack(41))}));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(1);
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify({...ack(42),done:true})}));
 expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(0);
 expect(element.textContent).toContain("Confirm deletion");
});

test("approval verdict starts a fresh thinking indicator that survives the card update and ends on reply", async () => {
 await render();
 const events=FakeEvents.instances[0];
 const approval={...message(301),role:"agent",agent_id:41,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Confirm deletion",status:"pending",actions:[{id:"approve",label:"Approve"}]}}]};
 await act(async()=>events.emit(approval));
 const resumed={chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"ack-verdict",text:"",phase:"acknowledgement",after_message_id:301};
 await act(async()=>events.listeners.get("stream")?.({data:JSON.stringify(resumed)}));
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await act(async()=>events.emit({...approval,revision:2,components:[{...approval.components[0],props:{...approval.components[0].props,status:"approve"}}]}));
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await act(async()=>events.emit({...message(302,"a","Decision received"),role:"agent",agent_id:41}));
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
});


test("optimistic response survives send completion and hands off once to the server",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?new Promise(resolve=>{complete=resolve}):(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render();await type("hello");await send();
 expect(element.querySelectorAll('[role="status"]')).toHaveLength(1);
 expect(element.textContent).toContain("Preparing response");
 await act(async()=>complete(new Response(JSON.stringify(message(1,"a","hello")))));await settle();
 expect(element.querySelectorAll('[role="status"]')).toHaveLength(1);
 const stream=FakeEvents.instances[0].listeners.get("stream")!;
 await act(async()=>stream({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"ack-1",text:"",phase:"acknowledgement",after_message_id:1,done:false})}));
 expect(element.querySelectorAll('[role="status"]')).toHaveLength(1);
 await act(async()=>FakeEvents.instances[0].emit({...message(2,"a","Hello back"),role:"agent",agent_id:41}));
 expect(element.querySelectorAll('[role="status"]')).toHaveLength(0);
});

test("completed tool hands progress to Thinking while the model continues", async () => {
 const user={...message(826,"a","Locate the client onboarding process"),created_at:"2026-09-23T12:12:34.761Z"};
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:url.includes("/messages")?[user]:[],cursor:826,before:826,has_more:false});
 await render();
 const stream=FakeEvents.instances[0].listeners.get("stream")!;
 const tool={id:64,chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"call-lookup",name:"apteva-server_app_tool_call",reason:"Finding onboarding procedure",status:"running",started_at:"2026-09-23T12:12:49.260018Z",ended_at:"",revision:1};
 const progress=(phase:string,revision:number)=>({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,response_progress:{phase,run_id:"run-1",revision,after_message_id:826,started_at:"2026-09-23T12:12:34.761Z"}});
 await act(async()=>{
   stream({data:JSON.stringify(progress("running",1))});
   stream({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"call-lookup",text:"",done:false,tool_activity:tool})});
 });
 await settle();
 expect(element.querySelector(".chat-tool-copy-running")).not.toBeNull();
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 await act(async()=>{
   stream({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"call-lookup",text:"",done:false,tool_activity:{...tool,status:"completed",ended_at:"2026-09-23T12:12:49.306451Z",revision:2,duration_ms:46}})});
   stream({data:JSON.stringify(progress("continuing",2))});
 });
 await settle();
 expect(element.querySelector(".chat-tool-copy-running")).toBeNull();
 expect(element.querySelector(".chat-tool-activity button")?.getAttribute("aria-busy")).toBeNull();
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
});

test("tool preparation stays visible while the running frame waits for activity", async () => {
 const user={...message(827,"a","Create the seed lists"),created_at:"2026-09-23T12:12:34.761Z"};
 fetcher=(url)=>(url.includes("/deliveries")||url.includes("/activity"))?json([]):json({messages:url.includes("/messages")?[user]:[],cursor:827,before:827,has_more:false});
 await render();
 const stream=FakeEvents.instances[0].listeners.get("stream")!;
 const progress=(phase:string,revision:number,call_id="",tool_name="")=>({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,response_progress:{phase,run_id:"run-seed",revision,after_message_id:827,started_at:"2026-09-23T12:12:34.761Z",tool_started_at:"2026-09-23T12:12:35.000Z",call_id,tool_name}});
 await act(async()=>stream({data:JSON.stringify(progress("preparing_tool",1,"call-seed","todo_lists_create"))}));
 expect(element.querySelector(".chat-tool-copy-running")).not.toBeNull();
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 // The server's running progress can cross the stream before the durable
 // tool_activity frame. The tool identity must survive that handoff.
 await act(async()=>stream({data:JSON.stringify(progress("running",2))}));
 await settle();
 expect(element.querySelector(".chat-tool-copy-running")).not.toBeNull();
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
});

test("Processes trace: acknowledgement precedes grouped tools, model waits show Thinking between tools, final text survives done-before-message", async () => {
 await render();
 const events=FakeEvents.instances[0];
 const frame=async(value:any)=>act(async()=>events.listeners.get("stream")!({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,...value})}));
 const at=(seconds:number)=>new Date(Date.parse("2026-09-23T14:36:42.661Z")+seconds*1000).toISOString();
 let revision=0;
 const progress=async(phase:string,seconds:number,call_id="",tool_name="")=>frame({created_at:at(seconds),response_progress:{phase,run_id:"run-list",revision:++revision,after_message_id:863,started_at:at(0),tool_started_at:at(seconds),call_id,tool_name}});
 await act(async()=>events.emit({...message(863,"a","List processes"),created_at:at(0)}));
 await progress("thinking",0);
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await frame({call_id:"ack-text",run_id:"1",text:"I will check the processes.",created_at:at(6),after_message_id:863});
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 // SSE stream completion can beat its durable replacement on the other queue.
 await frame({call_id:"ack-text",run_id:"1",done:true});
 await progress("thinking",6.28);
 expect(element.textContent).toContain("I will check the processes.");
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 const toolTimes=[17.05,21.7,30.62];
 for (let i=0;i<3;i++) {
   const call_id=`tool-${i}`, seconds=toolTimes[i]!;
   await progress("preparing_tool",seconds-0.2,call_id,"apteva-server_app_tool_call");
   expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
   // Preparation stays in the same group as the preceding completed calls.
   expect(element.querySelectorAll(".chat-tool-activity")).toHaveLength(1);
   const tool={id:100+i,chat_id:"a",agent_id:41,thread_id:"chat-a",call_id,name:"apteva-server_app_tool_call",reason:`Lookup ${i+1}`,status:"running",started_at:at(seconds),ended_at:"",revision:1};
   await frame({tool_activity:tool}); await settle();
   expect(element.querySelector(".chat-tool-copy-running")).not.toBeNull();
   expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
   expect(element.textContent!.indexOf("I will check")).toBeLessThan(element.textContent!.indexOf(`Lookup ${i+1}`));
   await frame({tool_activity:{...tool,status:"completed",ended_at:at(seconds+.018),revision:2}});
   await progress("continuing",seconds+.02); await settle();
   expect(element.querySelector(".chat-tool-copy-running")).toBeNull();
   expect(element.querySelector(".chat-tool-activity button")?.getAttribute("aria-busy")).toBeNull();
   expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
   // A delayed preparation for the same completed call must not revive it.
   await progress("preparing_tool",seconds-.1,call_id,tool.name);
   expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
   // A genuinely new call takes over the same response without inserting a
   // second Thinking row between the completed group and its next tool.
   const nextCall = `tool-${i}-next`;
   await progress("preparing_tool",seconds+.03,nextCall,tool.name);
   expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
   expect(element.querySelector(".chat-tool-copy-running")).not.toBeNull();
 }
 await act(async()=>events.emit({...message(864,"a","I will check the processes."),role:"agent",agent_id:41,phase:"acknowledgement",created_at:at(6.27)}));
 expect(element.textContent!.match(/I will check/g)).toHaveLength(1);
 await progress("continuing",30.66);
 await frame({call_id:"final-text",run_id:"2",text:"There is one",created_at:at(32.58),after_message_id:863});
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 await frame({call_id:"final-text",run_id:"2",text:"There is one process.",created_at:at(32.58),after_message_id:863});
 await frame({call_id:"final-text",run_id:"2",done:true});
 await progress("idle",35.59);
 expect(element.textContent).toContain("There is one process.");
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 await act(async()=>events.emit({...message(865,"a","There is one process."),role:"agent",agent_id:41,phase:"final",created_at:at(35.59)}));
 expect(element.textContent!.match(/There is one process/g)).toHaveLength(1);
 expect(element.textContent!.indexOf("Lookup 3")).toBeLessThan(element.textContent!.indexOf("There is one process"));
});

test("proactive progress uses its start time after a completed conversation", async () => {
 await render();
 const events=FakeEvents.instances[0];
 const frame=async(value:any)=>act(async()=>events.listeners.get("stream")!({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,...value})}));
 const previous={...message(100,"a","Previous reply"),role:"agent",agent_id:41,phase:"final",created_at:"2026-10-07T10:19:34Z"};
 await act(async()=>events.emit(previous));
 await act(async()=>events.emit({...message(101),role:"agent",agent_id:41,component_kind:"approval",created_at:"2026-10-07T10:19:35Z",components:[{app:"conversations",name:"approval-card",props:{title:"Previous decision",status:"approved",actions:[]}}]}));
 const progress={phase:"thinking",run_id:"subscription",revision:10,after_message_id:0,started_at:"2026-10-07T10:20:00Z"};
 await frame({response_progress:progress});
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await frame({snapshot:true,frames:[{chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:progress}]});
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 await act(async()=>events.emit({...message(102,"a","Subscription reply"),role:"agent",agent_id:41,phase:"final",created_at:"2026-10-07T10:20:02Z"}));
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 await frame({response_progress:{...progress,phase:"idle",revision:11}});
 await frame({snapshot:true,frames:[]});
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 expect(element.textContent).toContain("Subscription reply");
});

test("reconnect restores authoritative progress/text and ignores snapshot overlap and durable-first text", async () => {
 await render();const events=FakeEvents.instances[0];
 const frame=async(value:any)=>act(async()=>events.listeners.get("stream")!({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,...value})}));
 const progress={chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{phase:"thinking",run_id:"run",revision:4,after_message_id:10,started_at:"2026-09-23T14:36:42Z"}};
 await frame(progress);
 await act(async()=>events.listeners.get("error")?.({data:""}));
 expect(element.querySelector('[aria-label="Thinking"]')).not.toBeNull();
 const text={chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"text",run_id:"1",text:"A complete reply",after_message_id:10,created_at:"2026-09-23T14:36:48Z"};
 await frame({snapshot:true,frames:[progress,text]});
 await frame({...text,text:"A complete"});
 expect(element.textContent).toContain("A complete reply");
 await act(async()=>events.emit({...message(11,"a","A complete reply"),role:"agent",agent_id:41}));
 await frame({...text,call_id:"late-text"});
 expect(element.textContent!.match(/A complete reply/g)).toHaveLength(1);
 await frame({snapshot:true,frames:[]});
 expect(element.querySelector('[aria-label="Thinking"]')).toBeNull();
 expect(element.textContent).toContain("A complete reply");
});

for (const order of ["card-first", "completion-first", "reconnect"] as const) test(`approval preparation keeps Thinking until its card arrives (${order})`, async () => {
 await render();
 const events=FakeEvents.instances[0];
 const frame=async(value:any)=>act(async()=>events.listeners.get("stream")!({data:JSON.stringify({chat_id:"a",agent_id:41,thread_id:"chat-a",call_id:"",text:"",done:false,...value})}));
 const progress={run_id:"approval-turn",revision:1,after_message_id:300,started_at:message(300).created_at};
 const card=(id:number,agent_id=41)=>({...message(id),role:"agent",agent_id,component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:`Decision ${id}`,status:"pending",actions:[{id:"approve",label:"Approve"}]}}]});
 const thinking=()=>expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(1);
 const noThinking=()=>expect(element.querySelectorAll('[aria-label="Thinking"]').length).toBe(0);
 await frame({response_progress:{...progress,phase:"thinking"}}); thinking();
 await act(async()=>events.emit(card(200))); thinking(); // Historical card.
 await act(async()=>events.emit(card(301,42))); thinking(); // Other room participant.
 const preparing={chat_id:"a",agent_id:41,thread_id:"chat-a",response_progress:{...progress,phase:"preparing_tool",revision:2,tool_name:"conversations_conversations_request_approval",call_id:"approval-call"}};
 await frame(preparing); thinking();
 if(order==="reconnect") { await frame({snapshot:true,frames:[preparing]}); thinking(); }
 await frame({response_progress:{...preparing.response_progress,phase:"running",revision:3}}); thinking();
 expect(element.querySelectorAll('.chat-tool-activity').length).toBe(0);
 const complete={response_progress:{...progress,phase:"idle",revision:4,completion_message_id:302}};
 if(order==="card-first") {
   await act(async()=>events.emit(card(302))); noThinking();
   await frame(complete); noThinking();
 } else {
   await frame(complete); thinking();
   await settle(); thinking();
   await act(async()=>events.emit(card(302))); noThinking();
 }
 expect(element.textContent).toContain("Decision 302");
 expect(element.querySelectorAll('.chat-tool-activity').length).toBe(0);
 // Approval verdict belongs to a fresh response. Updating that existing card
 // must not dismiss the next response's Thinking indicator.
 await frame({response_progress:{...progress,phase:"thinking",run_id:"verdict-turn",revision:5,after_message_id:302}}); thinking();
 await act(async()=>events.emit({...card(302),revision:2})); thinking();
 await frame({response_progress:{...progress,phase:"idle",revision:6,after_message_id:302}}); noThinking();
});
