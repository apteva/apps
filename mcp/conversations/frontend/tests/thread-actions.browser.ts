import {test,expect} from "@playwright/test";
const chats=["one","two"].map((name,index)=>({id:`thread-${name}`,project_id:"project",lead_agent_id:41,title:`Thread ${name}`,kind:"direct",origin:"web",audience:"operator",created_at:"",updated_at:"",archived_at:null}));
for(const surface of ["panel","widget-browser","history"]){
 test(`${surface}: right click targets the row without navigating, preserves drafts and supports rename and read`,async({page,request})=>{
  await request.post("/reset");await request.post("/seed-panel",{data:chats});
  await page.goto(`/?host=dashboard${surface==="history"?"":`&surface=${surface}`}`);
  await expect(page.getByRole("heading",{name:"Thread one",exact:true})).toBeVisible();
  await page.locator("textarea").fill("Keep this draft");
  if(surface==="history")await page.getByRole("button",{name:"History",exact:true}).click();
  await request.post("/seed-unread",{data:{chat_id:"thread-two",count:2}});
  if(surface==="history"){await page.getByRole("button",{name:"Close",exact:true}).click();await page.getByRole("button",{name:"History",exact:true}).click();}
  const row=page.locator('[data-thread-actions="thread-two"]');
  await row.locator("button").first().click({button:"right"});
  const menu=page.getByRole("menu");await expect(menu).toBeVisible();
  await expect(page.getByRole("heading",{name:"Thread one",exact:true})).toBeVisible();
  const calls=await (await request.get("/requests")).json();
  expect(calls.some((c:any)=>c.body?.chat_id==="thread-two"&&c.path.endsWith("/seen"))).toBe(false);
  await menu.getByRole("menuitem",{name:"Mark as read",exact:true}).click();
  await expect.poll(async()=>((await (await request.get("/requests")).json()).filter((c:any)=>c.path.endsWith("/seen")&&c.body?.chat_id==="thread-two").length)).toBe(1);
  await row.locator("button").first().focus();await page.keyboard.press("Shift+F10");
  await expect(menu).toBeVisible();await page.keyboard.press("Escape");await expect(row.locator("button").first()).toBeFocused();
  await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Rename…",exact:true}).click();
  const dialog=page.getByRole("dialog",{name:"Rename…",exact:true});await dialog.getByRole("textbox",{name:"Title",exact:true}).fill("Renamed two");await dialog.getByRole("button",{name:"Save",exact:true}).click();
  await expect(row).toContainText("Renamed two");
  if(surface==="history")await page.getByRole("button",{name:"Close",exact:true}).click();
  await expect(page.locator("textarea")).toHaveValue("Keep this draft");
 });
}
test("panel: delete requires confirmation, archive/restore work, errors retain the dialog, and menu fits mobile viewport",async({page,request})=>{
 await request.post("/reset");await request.post("/seed-panel",{data:chats});await page.setViewportSize({width:390,height:700});await page.goto("/?host=dashboard&surface=panel");
 const row=page.locator('[data-thread-actions="thread-two"]');await row.getByRole("button",{name:"Options for Thread two",exact:true}).click();
 const menu=page.getByRole("menu");const rect=await menu.boundingBox();expect(rect!.x).toBeGreaterThanOrEqual(0);expect(rect!.x+rect!.width).toBeLessThanOrEqual(390);expect(rect!.y+rect!.height).toBeLessThanOrEqual(700);
 await menu.getByRole("menuitem",{name:"Delete…",exact:true}).click();const deleting=page.getByRole("dialog",{name:"Delete…",exact:true});await deleting.getByRole("button",{name:"Cancel",exact:true}).click();await expect(row).toBeVisible();await expect(row.getByRole("button",{name:"Options for Thread two",exact:true})).toBeFocused();
 await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Archive",exact:true}).click();await page.getByRole("dialog",{name:"Archive",exact:true}).getByRole("button",{name:"Archive",exact:true}).click();await expect(row).toHaveCount(0);
 await page.getByRole("button",{name:"More options",exact:true}).click();await page.getByRole("button",{name:"Archived conversations",exact:true}).click();await expect(row).toBeVisible();
 await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Restore",exact:true}).click();await page.getByRole("dialog",{name:"Restore",exact:true}).getByRole("button",{name:"Restore",exact:true}).click();await expect(row).toHaveCount(0);
 await page.getByRole("button",{name:"More options",exact:true}).click();await page.getByRole("button",{name:"Back to active conversations",exact:true}).click();await expect(row).toBeVisible();
 await page.route("**/chats?**",route=>route.request().method()==="PATCH"?route.fulfill({status:403,body:"Permission denied"}):route.continue());
 await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Rename…",exact:true}).click();const renaming=page.getByRole("dialog",{name:"Rename…",exact:true});await renaming.getByRole("textbox").fill("Denied");await renaming.getByRole("button",{name:"Save",exact:true}).click();await expect(renaming.getByRole("alert")).toContainText("Permission denied");await renaming.getByRole("button",{name:"Cancel",exact:true}).click();
 await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Delete…",exact:true}).click();await page.getByRole("dialog",{name:"Delete…",exact:true}).getByRole("button",{name:"Delete conversation",exact:true}).click();await expect(row).toHaveCount(0);
});

test("host can disable menus or add a keyboard-accessible action; details do not navigate and viewport-edge menus stay visible",async({page,request})=>{
 await request.post("/reset");await request.post("/seed-panel",{data:chats});
 await page.addInitScript(()=>{(window as any).THREAD_ACTIONS=false});
 await page.goto("/?host=external");await page.getByRole("button",{name:"History",exact:true}).click();
 await expect(page.locator(".thread-actions-trigger")).toHaveCount(0);
 await page.addInitScript(()=>{(window as any).THREAD_ACTIONS={actions:[{id:"inspect",label:"Inspect",onSelect:(conversation:any)=>{(window as any).INSPECTED=conversation.id}}]}});
 await page.reload();await page.getByRole("button",{name:"History",exact:true}).click();
 const row=page.locator('[data-thread-actions="thread-two"]');
 await row.locator("button").first().dispatchEvent("contextmenu",{clientX:1278,clientY:718,bubbles:true});
 const menu=page.getByRole("menu");await expect(menu).toBeVisible();const rect=await menu.boundingBox();expect(rect!.x+rect!.width).toBeLessThanOrEqual(1280);expect(rect!.y+rect!.height).toBeLessThanOrEqual(720);
 await page.keyboard.press("End");await expect(menu.getByRole("menuitem",{name:"Delete…",exact:true})).toBeFocused();await page.keyboard.press("ArrowUp");await expect(menu.getByRole("menuitem",{name:"Inspect",exact:true})).toBeFocused();await page.keyboard.press("Enter");await expect(menu).toHaveCount(0);expect(await page.evaluate(()=>(window as any).INSPECTED)).toBe("thread-two");
 await row.locator("button").first().click({button:"right"});await menu.getByRole("menuitem",{name:"Conversation details…",exact:true}).click();
 await expect(page.getByRole("dialog")).toBeVisible();await expect(page.getByRole("heading",{name:"Thread one",exact:true})).toBeVisible();
 expect((await (await request.get("/requests")).json()).some((c:any)=>c.path.endsWith("/participants")&&c.query.id==="thread-two")).toBe(true);
});
