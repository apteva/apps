import { expect,test } from "@playwright/test";
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
