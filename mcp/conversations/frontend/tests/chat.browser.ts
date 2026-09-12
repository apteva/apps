import { expect,test } from "@playwright/test";
for (const host of ["dashboard", "external"]) {
 test(`${host}: tool activity streams, updates in place and survives reload`, async ({page,request}) => {
  await request.post("/reset");await page.goto(`/?host=${host}`);
  await expect(page.getByTitle("Live")).toBeVisible();
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
  const activity={id:71,chat_id:chat,agent_id:41,thread_id:chat,call_id:"tool-71",name:"tasks_list",reason:"Checking your tasks",status:"running",started_at:new Date().toISOString(),ended_at:"",revision:1};
  const emit=async(item:typeof activity)=>request.post("/emit",{data:{chat_id:item.chat_id,tool_activity:item}});
  await emit(activity);
  const row=page.locator(".chat-tool-activity");
  await expect(row).toHaveCount(1);await expect(row).toHaveAttribute("aria-label",/Running/);
  await page.setViewportSize({width:390,height:800});
  await page.screenshot({path:test.info().outputPath("live-tool-mobile.png")});
  await expect(page.getByRole("button",{name:"Ask the agent to pause and reconsider"})).toBeEnabled();
  await page.getByRole("textbox").fill("Also check today");
  await expect(page.getByRole("button",{name:"Send",exact:true})).toBeEnabled();
  await page.getByRole("textbox").fill("");
  await emit({...activity,status:"completed",ended_at:new Date().toISOString(),revision:2});
  await expect(row).toHaveAttribute("aria-label",/Done/);
  // Delayed snapshots/events cannot regress the completed row.
  await emit(activity);await expect(row).toHaveAttribute("aria-label",/Done/);
  await emit({...activity,status:"completed",ended_at:new Date().toISOString(),revision:2});
  await page.reload();await expect(row).toHaveAttribute("aria-label",/Done/);
  await expect(row).toHaveCount(1);
  await emit({...activity,id:72,call_id:"tool-72",status:"failed",revision:2});
  await expect(row).toHaveAttribute("aria-label",/failed/i);
  // The original component groups the burst into one expandable summary.
  await expect(row).toHaveCount(1);
  const summary=row.getByRole("button").first();
  await expect(summary).toHaveAttribute("aria-expanded","false");
  await expect(row.getByText("+1",{exact:true})).toBeVisible();
  await summary.click();
  await expect(summary).toHaveAttribute("aria-expanded","true");
  await expect(row.locator("[id^=tools-] > div")).toHaveCount(2);
  await expect(row.locator(".chat-tool-failed-text").last()).toBeVisible();
  await page.screenshot({path:test.info().outputPath("original-tools-expanded.png")});
  await emit({...activity,id:99,chat_id:"another-chat",reason:"Must not appear"});
  await expect(page.getByText("Must not appear")).toHaveCount(0);
 });
}

for (const host of ["dashboard", "external"]) {
 test(`${host}: composer switches between pause and sending during a response`, async ({page,request}) => {
  await request.post("/reset");
  await page.goto(`/?host=${host}`);
  const composer=page.locator("form").filter({has:page.getByRole("textbox")});
  const action=composer.locator(".chat-composer-toolbar > button");
  await expect(action).toHaveCount(1);
  await expect(action).toHaveAccessibleName("Send");
  await expect(action).toBeDisabled();
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
  const frame={chat_id:chat,agent_id:41,thread_id:chat,call_id:"composer-test",run_id:"run",text:"Thinking through the request…"};
  await expect(page.getByTitle("Live")).toBeVisible();
  await request.post("/emit",{data:frame});
  await expect(action).toHaveAccessibleName("Ask the agent to pause and reconsider");
  await expect(action).toBeEnabled();
  await page.getByRole("textbox").press("Enter");
  await expect(page.getByText("Pause here and reconsider before continuing.",{exact:true})).toHaveCount(0);
  await page.getByRole("textbox").fill("Also check tomorrow");
  await expect(action).toHaveAccessibleName("Send");
  await action.click();
  await expect(page.getByText("Also check tomorrow",{exact:true})).toBeVisible();
  await expect(action).toHaveAccessibleName("Ask the agent to pause and reconsider");
  await action.click();
  await expect(action).toHaveAccessibleName("Break requested");
  await expect(action).toBeDisabled();
  await page.getByRole("textbox").fill("One more detail");
  await expect(action).toHaveAccessibleName("Send");
  await expect(action).toBeEnabled();
  await page.getByRole("textbox").press("Enter");
  await expect(page.getByText("One more detail",{exact:true})).toBeVisible();
  await request.post("/emit",{data:{...frame,text:"",done:true}});
  await expect(action).toHaveAccessibleName("Send");
  await expect(action).toBeDisabled();
 });
}

for (const host of ["dashboard", "external"]) {
 test(`${host}: app-owned CSS renders real replies and preserves host themes`, async ({page,request}) => {
  await request.post("/reset"); await request.post("/seed", {data:{}});
  const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
  for (const theme of ["clean", "terminal"]) {
   await page.goto(`/?host=${host}&theme=${theme}`);
   const answer=page.locator(".chat-md").first();
   await expect(answer.getByRole("heading",{name:"Here’s the update"})).toBeVisible();
   await expect(page.getByText("Agent 41",{exact:true})).toHaveCount(0);
   await expect(page.getByRole("status")).toHaveCount(0);
   const style=await answer.evaluate(el=>({font:getComputedStyle(el).fontFamily,color:getComputedStyle(el).color,fontSize:getComputedStyle(el).fontSize}));
   expect(style.font).toContain(theme==="clean"?"Arial":"monospace");
   expect(style.color).toBe(theme==="clean"?"rgb(32, 32, 32)":"rgb(232, 232, 232)");
   expect(await answer.locator("ul").evaluate(el=>getComputedStyle(el).listStyleType)).toBe("disc");
   expect(await answer.locator("pre").evaluate(el=>getComputedStyle(el).borderTopWidth)).toBe("1px");
   expect(await answer.locator("strong").evaluate(el=>getComputedStyle(el).fontWeight)).toBe("600");
   expect(await page.locator("#host-sentinel").evaluate(el=>({spacing:getComputedStyle(el).getPropertyValue("--spacing"),color:getComputedStyle(el).getPropertyValue("--color-border")}))).toEqual({spacing:"13px",color:""});
   for (const width of [1280,390]) {
    await page.setViewportSize({width,height:1000});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    const box=await page.getByRole("textbox").boundingBox();expect(box!.width).toBeGreaterThan(100);expect(box!.x+box!.width).toBeLessThanOrEqual(width);
    await page.screenshot({path:test.info().outputPath(`reply-${theme}-${width}.png`)});
   }
  }
  expect(errors).toEqual([]);
 });
 test(`${host}: named speakers and actionable delivery failures share the same UI`, async ({page,request}) => {
  await request.post("/reset");await request.post("/seed",{data:{room:true,deliveryStatus:"ambiguous"}});
  await page.goto(`/?host=${host}`);
  await expect(page.getByText("Scheduling assistant",{exact:true})).toBeVisible();
  await expect(page.getByText("Agent 42",{exact:true})).toHaveCount(0);
  await expect(page.getByRole("status")).toHaveText(/Delivery could not be confirmed/);
  await expect(page.getByText(/agent-inbound|internal transport detail/)).toHaveCount(0);
  await page.getByRole("button",{name:"Retry (may duplicate)",exact:true}).click();
  await expect(page.getByRole("status")).toHaveCount(0);
  await request.post("/seed",{data:{room:true,deliveryStatus:"failed"}});
  await page.reload();
  await expect(page.getByRole("status")).toHaveText(/Message could not be delivered/);
  await page.getByRole("button",{name:"Retry delivery",exact:true}).click();
  await expect(page.getByRole("status")).toHaveCount(0);
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
  await request.post("/emit",{data:{chat_id:chat,agent_id:42,thread_id:chat,call_id:"fixture-stream",run_id:"run",text:"Checking **availability**…"}});
  await expect(page.getByText("Checking availability…",{exact:true})).toBeVisible();
  await expect(page.getByText("Scheduling assistant",{exact:true})).toHaveCount(2);
 });
}

test("dashboard and exported chat have matching computed layout",async({page,request})=>{
 await request.post("/reset");await request.post("/seed",{data:{}});
 const snapshots=[];
 for(const host of ["dashboard","external"]){
  await page.goto(`/?host=${host}&theme=clean`);
  await expect(page.locator(".chat-md h2")).toBeVisible();
  snapshots.push(await page.evaluate(()=>[".chat-md",".chat-md h2",".chat-md ul",".chat-md pre",".chat-md th","textarea"].map(selector=>{
   const el=document.querySelector(selector)!;const s=getComputedStyle(el);const r=el.getBoundingClientRect();
   return {selector,font:s.fontFamily,size:s.fontSize,line:s.lineHeight,color:s.color,margin:s.margin,padding:s.padding,width:r.width};
  })));
 }
 expect(snapshots[0]).toEqual(snapshots[1]);
});
for(const host of ["dashboard","external"]){
 test(`${host}: shared chat sends, scopes transport and renders at desktop/mobile widths`,async({page,request})=>{
  const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
  await page.goto(`/?host=${host}`);await expect(page.getByText("Support chat",{exact:true}).first()).toBeVisible();
  await page.getByRole("textbox").fill(`Hello from ${host}`);await page.getByRole("button",{name:"Send",exact:true}).click();await expect(page.getByText(`Hello from ${host}`,{exact:true})).toBeVisible();
  for(const width of [1280,390]){await page.setViewportSize({width,height:800});const box=await page.getByRole("textbox").boundingBox();expect(box!.width).toBeGreaterThan(100);expect(box!.x+box!.width).toBeLessThanOrEqual(width);}
  expect(errors).toEqual([]);
  const calls=await (await request.get("/requests")).json();const own=calls.filter((c:any)=>c.bearer===(host==="external"));expect(own.some((c:any)=>c.path.endsWith("/messages")&&c.method==="POST")).toBe(true);
  expect(own.every((c:any)=>c.query.install_id==="7"&&c.query.project_id==="project")).toBe(true);
  if(host==="external")expect(own.every((c:any)=>!c.cookie)).toBe(true);
 });
}

for(const host of ["dashboard","external"]){
 test(`${host}: inbox displays report sections and resolves approvals`,async({page})=>{
  await page.goto(`/?host=${host}&surface=inbox`);
  await expect(page.getByText("Daily report",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Open",exact:true}).click();
  await expect(page.getByText("All systems healthy",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Close",exact:true}).last().click();
  await page.getByRole("button",{name:"Review",exact:true}).click();
  await page.getByRole("button",{name:"Approve",exact:true}).click();
  await expect(page.getByText("Approve maintenance",{exact:true})).toHaveCount(0);
 });
}

for (const host of ["dashboard", "external"]) {
 test(`${host}: localization survives a live language switch and mobile layout`, async ({page,request}) => {
  const errors:string[]=[]; page.on("pageerror", error=>errors.push(error.message));
  const welcome="Aucun message pour le moment. Comment puis-je vous aider ?";
  await request.post("/reset");
  await page.goto(`/?host=${host}&locale=fr-FR&empty=${encodeURIComponent(welcome)}`);
  await expect(page.getByRole("button",{name:"Historique",exact:true})).toBeVisible();
  await expect(page.getByText(welcome,{exact:true})).toBeVisible();
  await expect(page.getByRole("textbox")).toHaveAttribute("placeholder","Écrivez à l’agent…");
  await page.getByRole("textbox").fill("Brouillon conservé");
  await page.getByRole("combobox",{name:"Example language"}).selectOption("es-ES");
  await expect(page.getByRole("textbox")).toHaveValue("Brouillon conservé");
  await expect(page.getByRole("textbox")).toHaveAttribute("placeholder","Escribe al agente…");
  await expect(page.getByRole("button",{name:"Historial",exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Historial",exact:true}).click();
  await expect(page.getByText("Historial de conversaciones",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Cerrar",exact:true}).click();
  for(const width of [1280,390]) {
   await page.setViewportSize({width,height:800});
   const box=await page.getByRole("textbox").boundingBox();
   expect(box!.width).toBeGreaterThan(100); expect(box!.x+box!.width).toBeLessThanOrEqual(width);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await page.screenshot({path:test.info().outputPath("localized-chat-mobile.png")});
  await page.getByRole("button",{name:"Enviar",exact:true}).click();
  await expect(page.getByText("Brouillon conservé",{exact:true})).toBeVisible();
  expect(errors).toEqual([]);
 });
}

for (const host of ["dashboard", "external"]) {
 test(`${host}: translated inbox retains authored content and action IDs`, async ({page,request}) => {
  await request.post("/reset");
  await page.goto(`/?host=${host}&surface=inbox&locale=fr-FR`);
  await expect(page.getByRole("heading",{name:"Boîte de réception",exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Ouvrir",exact:true}).click();
  await expect(page.getByText("All systems healthy",{exact:true})).toBeVisible();
  await page.getByRole("combobox",{name:"Example language"}).selectOption("es-ES");
  await page.getByRole("button",{name:"Cerrar",exact:true}).last().click();
  await page.getByRole("button",{name:"Revisar",exact:true}).click();
  await expect(page.getByRole("textbox")).toHaveAttribute("placeholder","Nota opcional para el agente");
  // Action labels belong to the authored card; the app translates its surrounding UI.
  await page.getByRole("button",{name:"Approve",exact:true}).click();
  await expect(page.getByText("Approve maintenance",{exact:true})).toHaveCount(0);
 });
}

for(const host of ["dashboard","external"]){
 test(`${host}: attachment-only images and files persist, enlarge and download`,async({page,request})=>{
  await request.post("/reset");await page.goto(`/?host=${host}`);await expect(page.getByTitle("Live")).toBeVisible();
  await page.getByRole("button",{name:"Add attachment",exact:true}).click();await expect(page.getByRole("button",{name:"Take a screenshot"})).toBeVisible();
  const chooser=page.waitForEvent("filechooser");await page.getByRole("button",{name:"Add files or photos"}).click();
  await(await chooser).setFiles([{name:"picture.png",mimeType:"image/png",buffer:Buffer.from(await page.evaluate(()=>{const canvas=document.createElement("canvas");canvas.width=480;canvas.height=240;const ctx=canvas.getContext("2d")!;ctx.fillStyle="#2563eb";ctx.fillRect(0,0,480,240);ctx.fillStyle="white";ctx.font="28px sans-serif";ctx.fillText("Conversation attachment",35,125);return canvas.toDataURL("image/png").split(",")[1];}),"base64")},{name:"notes.txt",mimeType:"text/plain",buffer:Buffer.from("Read these notes")}]);
  await expect(page.locator(".chat-attachment-chip")).toHaveCount(2);await expect(page.getByRole("button",{name:"Send",exact:true})).toBeEnabled();
  await page.setViewportSize({width:390,height:800});await page.screenshot({path:test.info().outputPath("attachment-composer.png")});
  await page.getByRole("button",{name:"Send",exact:true}).click();await expect(page.locator(".chat-message-attachment")).toHaveCount(2);await expect(page.locator(".chat-attachment-chip")).toHaveCount(0);
  await page.reload();await expect(page.locator(".chat-message-attachment")).toHaveCount(2);
  await page.getByRole("button",{name:"Enlarge picture.png"}).click();await expect(page.locator("dialog")).toBeVisible();await page.keyboard.press("Escape");await expect(page.locator("dialog")).toHaveCount(0);
  const downloaded=page.waitForEvent("download");await page.locator(".chat-message-attachment").filter({hasText:"notes.txt"}).getByRole("button",{name:"Download"}).click();expect((await downloaded).suggestedFilename()).toBe("notes.txt");
  await page.screenshot({path:test.info().outputPath("attachment-history.png")});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 });
 test(`${host}: screenshot captures one frame and stops sharing`,async({page,request})=>{
  await request.post("/reset");await page.addInitScript(()=>{
   navigator.mediaDevices.getDisplayMedia=async()=>{const canvas=document.createElement("canvas");canvas.width=320;canvas.height=200;const ctx=canvas.getContext("2d")!;ctx.fillStyle="green";ctx.fillRect(0,0,320,200);const stream=canvas.captureStream(10);(window as any).captureTrack=stream.getVideoTracks()[0];return stream;};
  });
  await page.goto(`/?host=${host}`);await expect(page.getByTitle("Live")).toBeVisible();
  await page.getByRole("button",{name:"Add attachment",exact:true}).click();await page.getByRole("button",{name:"Take a screenshot"}).click();
  await expect(page.locator(".chat-attachment-chip img")).toBeVisible();expect(await page.evaluate(()=>(window as any).captureTrack.readyState)).toBe("ended");
  await expect(page.getByRole("button",{name:"Send",exact:true})).toBeEnabled();await page.locator(".chat-attachment-chip button[aria-label]").click();await expect(page.getByRole("button",{name:"Send",exact:true})).toBeDisabled();
 });
}

for(const host of ["dashboard","external"]){
 test(`${host}: host-configured actions use the shared attachment pipeline`,async({page,request})=>{
  await request.post("/reset");await page.addInitScript(()=>{(window as any).COMPOSER_OPTIONS={files:false,screenshot:false,actions:[{id:"notes",label:"Attach my notes",run:async()=>new File(["Configured action"],"custom.txt",{type:"text/plain"})}]};});
  await page.goto(`/?host=${host}`);await expect(page.getByTitle("Live")).toBeVisible();await page.getByRole("button",{name:"Add attachment",exact:true}).click();
  await expect(page.getByRole("button",{name:"Add files or photos"})).toHaveCount(0);await expect(page.getByRole("button",{name:"Take a screenshot"})).toHaveCount(0);
  await page.getByRole("button",{name:"Attach my notes"}).click();await expect(page.locator(".chat-attachment-chip")).toHaveText(/custom.txt/);await page.getByRole("button",{name:"Send",exact:true}).click();await expect(page.locator(".chat-message-attachment")).toHaveText(/custom.txt/);
 });
 test(`${host}: upload retry preserves identity and pasted files can send while active`,async({page,request})=>{
  await request.post("/reset");const uploadIDs:string[]=[];let attempts=0;
  await page.route("**/attachments?**",async route=>{if(route.request().method()!=="POST")return route.continue();uploadIDs.push(route.request().postDataJSON().id);if(attempts++===0)return route.fulfill({status:503,body:"retry"});return route.continue();});
  await page.goto(`/?host=${host}`);await expect(page.getByTitle("Live")).toBeVisible();
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";await request.post("/emit",{data:{chat_id:chat,tool_activity:{id:555,chat_id:chat,agent_id:41,thread_id:chat,call_id:"busy",name:"tasks_list",reason:"Checking tasks",status:"running",started_at:new Date().toISOString(),revision:1}}});
  await expect(page.getByRole("button",{name:"Ask the agent to pause and reconsider"})).toBeEnabled();
  await page.getByRole("textbox").evaluate(el=>{const data=new DataTransfer();data.items.add(new File(["Pasted file"],"pasted.txt",{type:"text/plain"}));el.dispatchEvent(new ClipboardEvent("paste",{bubbles:true,clipboardData:data}));});
  await expect(page.getByRole("button",{name:"Retry upload"})).toBeVisible();await expect(page.getByRole("button",{name:"Send",exact:true})).toBeDisabled();await page.getByRole("button",{name:"Retry upload"}).click();
  await expect(page.getByRole("button",{name:"Send",exact:true})).toBeEnabled();expect(uploadIDs).toHaveLength(2);expect(uploadIDs[0]).toBe(uploadIDs[1]);
  await page.getByRole("button",{name:"Send",exact:true}).click();await expect(page.locator(".chat-message-attachment")).toHaveText(/pasted.txt/);
 });
}
