import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
const window = new Window({ url:"http://localhost" });
Object.assign(globalThis,{window,document:window.document,navigator:window.navigator,HTMLElement:window.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true});
const { createRoot } = await import("react-dom/client");
const { EnvironmentPicker } = await import("./EvalsPanel");
let root: ReturnType<typeof createRoot>;
const originalFetch=globalThis.fetch;
afterEach(async()=>{if(root) await act(async()=>root.unmount());document.body.innerHTML="";globalThis.fetch=originalFetch;});
test("saved-environment picker pages summaries without fetching specifications",async()=>{
 const requests:string[]=[];
 globalThis.fetch=(async(input:any)=>{
  const url=new URL(String(input),window.location.origin);requests.push(url.pathname+url.search);
  const next=!!url.searchParams.get("cursor");
  return new Response(JSON.stringify({items:[{id:next?"b":"a",name:next?"Beta":"Alpha"}],next_cursor:next?null:"opaque",has_more:!next}));
 }) as any;
 const host=document.createElement("div");document.body.appendChild(host);root=createRoot(host);
 await act(async()=>root.render(<EnvironmentPicker value="saved-outside-page" onChange={()=>{}} />));
 expect(document.querySelector("select")?.value).toBe("saved-outside-page");
 expect(document.body.textContent).toContain("Alpha");
 const next=Array.from(document.querySelectorAll("button")).find(b=>b.textContent==="Next environments")!;
 await act(async()=>next.click());
 expect(document.body.textContent).toContain("Beta");expect(document.body.textContent).not.toContain("Alpha");
 expect(requests.length).toBe(2);expect(requests[1]).toContain("cursor=opaque");
 expect(requests.every(path=>!path.includes("/environments/"))).toBe(true);
});
