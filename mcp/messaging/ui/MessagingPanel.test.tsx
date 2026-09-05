import { Window } from "happy-dom";
import { beforeEach, afterEach, expect, test } from "bun:test";
const browser = new Window({url:"https://app.example.test"});
for (const name of ["window","document","navigator","HTMLElement","HTMLInputElement","HTMLTextAreaElement","Event","MouseEvent","DOMParser","MutationObserver","FileReader"]) {
  Object.defineProperty(globalThis,name,{configurable:true,value:name === "window" ? browser : (browser as any)[name]});
}
(globalThis as any).IS_REACT_ACT_ENVIRONMENT=true;
const {act}=await import("react");
const {createRoot}=await import("react-dom/client");
const {ComposeView,EmailBodyViewer,safeEmailHTMLDocument,composeDraftFromMessage}=await import("./MessagingPanel");
let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
beforeEach(()=>{container=document.createElement("div");document.body.appendChild(container);root=createRoot(container)});
afterEach(async()=>{await act(async()=>root.unmount());container.remove()});
const sender={address:"support@example.com",channel:"email",kind:"email_mailbox",verified:true,is_default:true,sending_enabled:true};
const draft={key:10,channel:"email",from:sender.address,to:"alice@example.com",subject:"Re: Help",body:"Original"};
const base:any={senders:[sender],templates:[],quota:null,inbox:[],draft,onSent:()=>{},gotoSenders:()=>{},gotoTemplates:()=>{},api:async()=>({status:"sent"})};
async function render(props:any){await act(async()=>root.render(<ComposeView {...props}/>))}
async function typeBody(value:string){
 const area=container.querySelector("textarea")!;
 await act(async()=>{Object.getOwnPropertyDescriptor(browser.HTMLTextAreaElement.prototype,"value")!.set!.call(area,value);area.dispatchEvent(new browser.Event("input",{bubbles:true}) as any)});
}
async function submit(){await act(async()=>{container.querySelector("form")!.dispatchEvent(new browser.Event("submit",{bubbles:true,cancelable:true}) as any)})}
test("sender reload preserves an edited reply",async()=>{
 await render(base);await typeBody("Do not lose my reply");
 expect(container.querySelector("textarea")!.value).toBe("Do not lose my reply");
 await render({...base,senders:[{...sender,display_name:"Updated name"}]});
 expect(container.querySelector("textarea")!.value).toBe("Do not lose my reply");
});
test("provider failure preserves draft and exposes reason",async()=>{
 let sent=false;
 await render({...base,onSent:()=>{sent=true},api:async()=>({status:"failed",status_reason:"Provider rejected sender"})});
 await typeBody("Important unsent reply");await submit();
 expect(sent).toBe(false);expect(container.querySelector("textarea")!.value).toBe("Important unsent reply");
 expect(container.textContent).toContain("Provider rejected sender");
});
test("uncertain network retry reuses the same send identity",async()=>{
 const keys:string[]=[];let attempt=0;
 await render({...base,api:async(_m:any,_p:any,_q:any,b:any)=>{keys.push(b.args.idempotency_key);if(++attempt===1)throw new Error("connection lost");return {status:"sent"}}});
 await submit();await submit();expect(keys).toHaveLength(2);expect(keys[0]).toBe(keys[1]);expect(keys[0]).toBeTruthy();
});
test("images off enforces a network-denying CSP including CSS backgrounds",()=>{
 const doc=new DOMParser().parseFromString(safeEmailHTMLDocument('<div style="background:url(//tracker.example/pixel)">mail</div>',{loadRemoteImages:false}),"text/html");
 const csp=doc.querySelector('meta[http-equiv="Content-Security-Policy"]')!.getAttribute("content")!;
 expect(csp).toContain("default-src 'none'");expect(csp).toContain("img-src data:");expect(csp).not.toContain("img-src https:");
});
test("switching message keys resets image opt-in",async()=>{
 await act(async()=>root.render(<EmailBodyViewer key={1} bodyHTML="<img src='https://example.com/image'>" bodyText=""/>));
 const input=container.querySelector('input[type="checkbox"]') as HTMLInputElement;
 await act(async()=>input.click());expect(input.checked).toBe(true);
 await act(async()=>root.render(<EmailBodyViewer key={2} bodyHTML="<img src='https://example.com/other'>" bodyText=""/>));
 expect((container.querySelector('input[type="checkbox"]') as HTMLInputElement).checked).toBe(false);
});
test("reply uses Reply-To",()=>{
 const message:any={id:1,channel:"email",from:"bounce@example.com",to:[sender.address],subject:"hello",headers:{"Reply-To":"reply@example.com"}};
 expect(composeDraftFromMessage(message,[sender] as any)!.to).toBe("reply@example.com");
});

test("quoted display-name Reply-To produces a canonical mailbox",()=>{
 const message:any={id:2,channel:"email",from:"bounce@example.com",to:[sender.address],headers:{"reply-to":'"Support, Team" <reply@example.com>'}};
 expect(composeDraftFromMessage(message,[sender] as any)!.to).toBe("reply@example.com");
});
test("compose honors configured default sender",async()=>{
 await render({...base,draft:null,senders:[{...sender,address:"first@example.com",is_default:false},sender]});
 expect((container.querySelector("select") as HTMLSelectElement).value).toContain(sender.address);
});
test("reply selects WhatsApp when SMS uses the same number",async()=>{
 const number="+15551234567";
 await render({...base,senders:[{...sender,address:number,channel:"sms"},{...sender,address:number,channel:"whatsapp"}],draft:{...draft,key:99,from:number,to:"+15557654321",channel:"whatsapp"}});
 expect((container.querySelector("select") as HTMLSelectElement).value).toContain("whatsapp");
});
test("attachment-only compose submits a valid empty body",async()=>{
 let args:any;
 await render({...base,api:async(_m:any,_p:any,_q:any,b:any)=>{args=b.args;return {status:"sent"}}});
 await typeBody("");
 const input=container.querySelector('input[type="file"]') as HTMLInputElement;
 const file=new browser.File(["document"],"document.txt",{type:"text/plain"});
 Object.defineProperty(input,"files",{configurable:true,value:[file]});
 await act(async()=>{input.dispatchEvent(new browser.Event("change",{bubbles:true}) as any);await browser.happyDOM.waitUntilComplete()});
 expect(container.querySelector("textarea")!.required).toBe(false);
 await submit();expect(args.attachments).toHaveLength(1);expect(args.body).toBe("");
});
