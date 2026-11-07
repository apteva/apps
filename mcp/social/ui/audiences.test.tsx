import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { MembershipAudience, MembershipConfirmation, type AudienceCapabilities } from "./audiences";
const browser = new Window({url:"http://localhost:3000"});
const originalFetch = globalThis.fetch;
let root:ReturnType<typeof createRoot> | undefined;
function globals(){Object.assign(globalThis,{window:browser,document:browser.document,navigator:browser.navigator,HTMLElement:browser.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true});}
afterEach(async()=>{globals();if(root)await act(async()=>root!.unmount());root=undefined;browser.document.body.innerHTML="";globalThis.fetch=originalFetch;await browser.happyDOM.abort();});
async function render(node:React.ReactNode){globals();if(!root){const host=browser.document.createElement("div");browser.document.body.append(host);root=createRoot(host);}await act(async()=>root!.render(node));}
const capabilities:AudienceCapabilities={discovery:"supported",publishing:"manual",tier_targeting:true,verification:"human_confirmed",staging_visibility:"unlisted"};
const click=async(label:string)=>{const button=[...browser.document.querySelectorAll("button")].find(b=>b.textContent?.includes(label));expect(button).toBeTruthy();await act(async()=>button!.click());};
test("opening standard audience never starts membership discovery",async()=>{
 let calls=0;globalThis.fetch=(async()=>{calls++;return Response.json({});}) as typeof fetch;
 await render(<MembershipAudience accountId={7} capabilities={capabilities} onChange={()=>{}}/>);
 expect(calls).toBe(0);expect(browser.document.body.textContent).toContain("Standard");
 expect(browser.document.body.textContent).not.toContain("Discover membership tiers");
 await render(<MembershipAudience accountId={8} capabilities={{...capabilities,publishing:"unsupported"}} onChange={()=>{}}/>);
 expect(browser.document.body.textContent).toBe("");expect(calls).toBe(0);
});
test("tiers are discovered explicitly for the selected account and project",async()=>{
 let selection:unknown;const urls:URL[]=[];
 globalThis.fetch=(async(input,init)=>{urls.push(new URL(String(input),"http://localhost"));expect(init?.credentials).toBe("same-origin");return Response.json({status:"available",stale:false,levels:[{id:"tier-7",name:"Supporters",price:null}]});}) as typeof fetch;
 await render(<MembershipAudience accountId={7} projectId="brand" capabilities={capabilities} value={{type:"members"}} onChange={value=>{selection=value}}/>);
 expect(urls.length).toBe(0);expect(browser.document.body.textContent).toContain("Anyone with the link");
 await click("Discover membership tiers");
 expect(urls[0].pathname).toBe("/api/apps/social/audiences/accounts/7/memberships");expect(urls[0].searchParams.get("project_id")).toBe("brand");expect(urls[0].searchParams.get("refresh")).toBe("true");
 const select=browser.document.querySelector('select[aria-label="Membership tier"]')!;
 await act(async()=>{(select.querySelector("option") as HTMLOptionElement).selected=true;select.dispatchEvent(new browser.Event("change",{bubbles:true}));});
 expect(selection).toEqual({type:"members",level_ids:["tier-7"]});
});
test("permission failures are not shown as empty tiers or selectable cached tiers",async()=>{
 globalThis.fetch=(async()=>Response.json({status:"permission_required",reason:"Reauthorize the existing connection",stale:true,levels:[{id:"old",name:"Cached"}]})) as typeof fetch;
 await render(<MembershipAudience accountId={7} capabilities={capabilities} value={{type:"members"}} onChange={()=>{}}/>);
 await click("Discover membership tiers");expect(browser.document.body.textContent).toContain("Reauthorize");expect(browser.document.querySelector('select[aria-label="Membership tier"]')).toBeNull();expect(browser.document.body.textContent).not.toContain("No membership tiers");
});
test("an old discovery response cannot replace another account's tiers",async()=>{
 let finish!:(res:Response)=>void;
 globalThis.fetch=(()=>new Promise<Response>(resolve=>{finish=resolve})) as typeof fetch;
 await render(<MembershipAudience accountId={7} capabilities={capabilities} value={{type:"members"}} onChange={()=>{throw new Error("stale onChange")}}/>);
 const button=browser.document.querySelector("button")!;
 await act(async()=>{button.click();});
 await render(<MembershipAudience accountId={8} capabilities={capabilities} value={{type:"members"}} onChange={()=>{}}/>);
 await act(async()=>{finish(Response.json({status:"available",stale:false,levels:[{id:"old",name:"Old account"}]}));});
 expect(browser.document.body.textContent).not.toContain("Old account");
});
test("membership confirmation requires an explicit checkbox and sends target plus revision",async()=>{
 let payload:any;let url:URL|undefined;let confirmed=0;
 globalThis.fetch=(async(input,init)=>{url=new URL(String(input),"http://localhost");payload=JSON.parse(String(init?.body));return Response.json({verification:"human_confirmed"});}) as typeof fetch;
 await render(<MembershipConfirmation postId={4} targetId={9} revision={3} projectId="brand" setupURL="https://studio.youtube.com/video/v/edit" levelIds={["tier-7"]} onConfirmed={()=>{confirmed++}}/>);
 const button=browser.document.querySelector("button")!;expect(button.disabled).toBe(true);expect(payload).toBeUndefined();expect(browser.document.body.textContent).toContain("cannot verify");
 await act(async()=>{(browser.document.querySelector('input[type="checkbox"]') as HTMLInputElement).click();});
 expect(button.disabled).toBe(false);await click("Confirm membership setup");
 expect(payload).toEqual({post_id:4,target_id:9,expected_revision:3,confirmed:true});expect(url?.searchParams.get("project_id")).toBe("brand");expect(confirmed).toBe(1);
});
test("stale confirmation stays pending and displays its error",async()=>{
 globalThis.fetch=(async()=>new Response("stale confirmation",{status:409})) as typeof fetch;
 let confirmed=0;
 await render(<MembershipConfirmation postId={4} targetId={9} revision={2} setupURL="" onConfirmed={()=>{confirmed++}}/>);
 await act(async()=>{(browser.document.querySelector("input") as HTMLInputElement).click();});await click("Confirm membership setup");
 expect(confirmed).toBe(0);expect(browser.document.body.textContent).toContain("stale confirmation");
});
