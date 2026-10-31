import {test,expect,type Page} from "@playwright/test";
// Two agents, one conversation each: the project widget must show both.
const chats=[
 {id:"thread-billing",project_id:"project",lead_agent_id:41,lead_agent_name:"Assistant",title:"Billing follow-up",kind:"direct",origin:"web",audience:"operator",created_at:"",updated_at:"",archived_at:null},
 {id:"thread-calendar",project_id:"project",lead_agent_id:42,lead_agent_name:"Scheduling assistant",title:"Calendar sync",kind:"direct",origin:"web",audience:"operator",created_at:"",updated_at:"",archived_at:null},
];
const listRequests=async(request:import("@playwright/test").APIRequestContext)=>((await (await request.get("/requests")).json()) as any[]).filter(c=>c.method==="GET"&&(c.path.endsWith("/chats")||c.path.endsWith("/unread-summary")));
const row=(page:Page,title:string)=>page.locator("button[data-conversation-id]").filter({hasText:title});

for (const host of ["dashboard","external"]) {
 test(`${host}: project widget lists every agent's conversations without an agent filter`, async ({page,request}) => {
  await request.post("/reset");await request.post("/seed-panel",{data:chats});
  await page.setViewportSize({width:1280,height:800});
  await page.goto(`/?host=${host}&surface=project`);
  await expect(row(page,"Billing follow-up")).toContainText("Assistant");
  await expect(row(page,"Calendar sync")).toContainText("Scheduling assistant");
  const requests=await listRequests(request);
  expect(requests.length).toBeGreaterThan(0);
  for(const call of requests)expect(call.query.agent_id).toBeUndefined();
  // Wide tiles keep the list beside the open chat.
  await expect(page.locator("[data-conversations-layout]")).toHaveAttribute("data-conversations-layout","wide");
  await row(page,"Calendar sync").click();
  await expect(page.getByRole("heading",{name:"Calendar sync",exact:true})).toBeVisible();
  await expect(row(page,"Billing follow-up")).toBeVisible();
 });

 test(`${host}: new conversation picks its agent`, async ({page,request}) => {
  await request.post("/reset");await request.post("/seed-panel",{data:chats});
  await page.setViewportSize({width:1280,height:800});
  await page.goto(`/?host=${host}&surface=project`);
  await expect(row(page,"Billing follow-up")).toBeVisible();
  await page.getByRole("button",{name:"New conversation",exact:true}).click();
  const dialog=page.getByRole("dialog",{name:"New conversation"});
  await dialog.getByRole("checkbox",{name:"Scheduling assistant"}).check();
  const created=page.waitForRequest(req=>req.method()==="POST"&&new URL(req.url()).pathname.endsWith("/chats"));
  await dialog.getByRole("button",{name:"Create",exact:true}).click();
  const body=(await created).postDataJSON();
  expect(body.agent_ids).toEqual([42]);expect(body.lead_agent_id).toBe(42);
  // The external fixture is a visitor client, which pins its own audience.
  if(host==="dashboard")expect(body.audience).toBe("operator");
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading",{name:"New conversation",exact:true})).toBeVisible();
 });
}

test("dashboard: a half tile stacks the list and the chat, and back returns focus to the row", async ({page,request}) => {
 await request.post("/reset");await request.post("/seed-panel",{data:chats});
 await page.setViewportSize({width:1280,height:800});
 await page.goto("/?host=dashboard&surface=project&size=half");
 const frame=page.locator("[data-conversations-layout]");
 await expect(frame).toHaveAttribute("data-conversations-layout","stacked");
 await expect(row(page,"Calendar sync")).toBeVisible();
 await expect(page.locator("textarea")).toHaveCount(0);
 await row(page,"Calendar sync").click();
 await expect(page.getByRole("heading",{name:"Calendar sync",exact:true})).toBeVisible();
 await expect(page.locator("button[data-conversation-id]")).toHaveCount(0);
 const back=page.getByRole("button",{name:"Back to conversations"});
 await expect(back).toBeVisible();
 await page.locator("textarea").fill("Draft for the calendar");
 await back.click();
 await expect(row(page,"Calendar sync")).toBeFocused();
 await row(page,"Calendar sync").click();
 await expect(page.locator("textarea")).toHaveValue("Draft for the calendar");
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test("dashboard: project widget links to the full Conversations page and uses Home defaults", async ({page,request}) => {
 await request.post("/reset");await request.post("/seed-panel",{data:chats});
 await page.setViewportSize({width:1280,height:800});
 await page.goto("/?host=dashboard&surface=project");
 await expect(page.getByRole("link",{name:"Open in Conversations"})).toHaveAttribute("href","/apps/conversations/page");
 await row(page,"Billing follow-up").click();
 await expect(page.locator(".chat-composer-box")).toHaveAttribute("data-layout","compact");
 // A fixed height keeps the chat usable whatever the neighbouring Home widgets measure.
 const height=await page.locator("[data-conversations-layout]").evaluate(el=>el.getBoundingClientRect().height);
 expect(height).toBeGreaterThanOrEqual(458);expect(height).toBeLessThanOrEqual(642);
});
