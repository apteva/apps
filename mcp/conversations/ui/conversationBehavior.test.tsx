import "./testDom";
import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { createRoot, type Root } from "react-dom/client";
import { act } from "react";
import { ConversationChat, refreshConversationList, type Conversation } from "./ConversationsPanel";
import { reportSectionsText } from "./messageContent";

let win: Window, root: Root, element: HTMLElement;
let fetcher: (url:string, init?:RequestInit) => Promise<Response>;
class FakeEvents {
 static instances: FakeEvents[]=[];
 onopen: (()=>void)|null=null; onmessage: ((e:{data:string})=>void)|null=null; onerror: (()=>void)|null=null;
 listeners=new Map<string,(e:{data:string})=>void>();
 constructor(public url:string) {FakeEvents.instances.push(this);}
 addEventListener(name:string,fn:(e:{data:string})=>void){this.listeners.set(name,fn);}
 close(){}
 emit(message:unknown){this.onmessage?.({data:JSON.stringify(message)});}
}
const conv=(id:string):Conversation=>({id,project_id:"project",lead_agent_id:41,title:id,kind:"direct",origin:"web",created_at:"",updated_at:""});
const message=(id:number,conversation_id="a",content=`message ${id}`)=>({id,conversation_id,content,role:"user",components:[],created_at:"2026-09-05T00:00:00Z"});
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
 fetcher=(url)=>url.includes("/deliveries")?json([]):json(url.includes("/changes")?{messages:[],cursor:0,has_more:false}:{messages:[],cursor:0,has_more:false,before:0});
 globalThis.fetch=((url:unknown,init?:RequestInit)=>fetcher(String(url),init)) as typeof fetch;
});
afterEach(async()=>{await act(async()=>root.unmount());await win.happyDOM.abort();});

test("a live row arriving before snapshot never advances durable replay cursor",async()=>{
 let resolveSnapshot!:(value:Response)=>void;const paths:string[]=[];
 fetcher=(url)=>{paths.push(url);if(url.includes("/deliveries"))return json([]);if(url.includes("page=1"))return new Promise(resolve=>{resolveSnapshot=resolve});return json({messages:[message(201)],cursor:201,has_more:false});};
 await act(async()=>root.render(<ConversationChat conversation={conv("a")} archived={false} onActed={()=>{}} onRemoved={()=>{}}/>));
 await act(async()=>FakeEvents.instances[0].emit(message(501)));
 await act(async()=>resolveSnapshot(new Response(JSON.stringify({messages:[message(200)],cursor:200,before:200,has_more:true}))));await settle();
 expect(paths.some(path=>path.includes("cursor=200"))).toBe(true);
 expect(element.textContent).toContain("message 201");expect(element.textContent).toContain("message 501");
});
test("switching conversations keeps drafts and late replies out of the next chat",async()=>{
 fetcher=(url)=>url.includes("/deliveries")?json([]):json(url.includes("/changes")?{messages:[],cursor:0,has_more:false}:{messages:[message(1,url.includes("chat_id=b")?"b":"a")],cursor:1,before:1,has_more:false});
 await render("a");await type("private draft");await render("b");
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
 await act(async()=>FakeEvents.instances.at(-1)!.emit(message(900,"a","private late reply")));
 expect(element.textContent).not.toContain("private late reply");
});
test("ordinary retry preserves the original submission id",async()=>{
 const bodies:any[]=[];
 fetcher=(url,init)=>{if(init?.method==="POST"&&url.includes("/messages")){bodies.push(JSON.parse(String(init.body)));return bodies.length===1?Promise.reject(new Error("lost response")):json(message(1,"a",bodies[0].content));}return url.includes("/deliveries")?json([]):json({messages:[],cursor:0,has_more:false,before:0});};
 await render();await type("hello");await send();await settle();await send();await settle();
 expect(bodies.length).toBe(2);expect(bodies[1]).toEqual(bodies[0]);
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
 expect(element.textContent).not.toContain("Alpha progress");expect(element.textContent).toContain("Beta progress");
 await act(async()=>emit(41,"New response",false,"2"));
 expect(element.textContent).toContain("New response");
});
test("send completion preserves text typed while the request was pending",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?new Promise(resolve=>{complete=resolve}):url.includes("/deliveries")?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render();await type("first");await send();await type("next draft");
 await act(async()=>complete(new Response(JSON.stringify(message(1,"a","first")))));await settle();
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("next draft");
});
test("a send acknowledged after switching clears only its own saved draft",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url,init)=>init?.method==="POST"&&url.includes("/messages")?new Promise(resolve=>{complete=resolve}):url.includes("/deliveries")?json([]):json({messages:[],cursor:0,has_more:false,before:0});
 await render("a");await type("first");await send();await render("b");await type("second draft");
 await act(async()=>complete(new Response(JSON.stringify(message(1,"a","first")))));await settle();
 expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("second draft");
 await render("a");expect((element.querySelector("textarea") as HTMLTextAreaElement).value).toBe("");
});
test("read marks require a visible loaded transcript at the bottom",async()=>{
 const marks:any[]=[];
 fetcher=(url,init)=>{if(url.includes("/seen")){marks.push(JSON.parse(String(init?.body)));return json({ok:true});}return url.includes("/deliveries")?json([]):json(url.includes("/changes")?{messages:[],cursor:10,has_more:false}:{messages:[message(10)],cursor:10,before:10,has_more:false});};
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
 const rows=await refreshConversationList("/chats?agent_id=41","project",101);
 expect(paths.length).toBe(2);expect(paths.every(path=>path.includes("agent_id=41"))).toBe(true);expect(rows.length).toBe(101);expect(rows.at(-1)?.id).toBe("older-survivor");
});

test("an older history page cannot overwrite a newer live revision",async()=>{
 let complete!:(r:Response)=>void;
 fetcher=(url)=>url.includes("/deliveries")?json([]):url.includes("before=200")?new Promise(resolve=>{complete=resolve}):url.includes("/changes")?json({messages:[],cursor:200,has_more:false}):json({messages:[{...message(200),revision:200}],cursor:200,before:200,has_more:true});
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
