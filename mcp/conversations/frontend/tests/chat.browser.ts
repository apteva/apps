import { expect,test } from "@playwright/test";
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
