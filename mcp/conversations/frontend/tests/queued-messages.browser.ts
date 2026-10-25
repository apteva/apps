import {expect,test} from "@playwright/test";

const queued=(conversation_id:string,id=31,content="Run regression checks",position=id)=>({
 id,conversation_id,role:"user",content,components:[],queue_behavior:"queue",queue_state:"queued",queue_position:position,
 attachments:[{type:"file",name:"queued-notes.txt",mime_type:"text/plain",size:32}],
 created_at:new Date(Date.UTC(2026,9,25,15,40,id)).toISOString(),
});

for(const host of ["dashboard","external","package"] as const)for(const width of [1280,390]) {
 test(`${host} ${width}: queued turns sit above the composer and edit/remove/steer preserve the new draft`,async({page,request})=>{
  await request.post("/reset");await request.post("/seed",{data:{}});
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
  await request.post("/append-message",{data:queued(chat,31,"Run regression checks",2)});
  await request.post("/append-message",{data:queued(chat,32,"Then confirm",1)});
  await page.setViewportSize({width,height:900});await page.goto(`/?host=${host}&theme=terminal`);
  const tray=page.getByRole("region",{name:"Queued messages"}),input=page.locator(".chat-composer-input");
  await expect(tray).toBeVisible();await expect(tray.getByRole("listitem")).toHaveCount(2);
  await expect(tray.getByRole("listitem").first()).toContainText("Then confirm");
  expect(await tray.locator(".chat-queue-text").first().evaluate(el=>el.getBoundingClientRect().height)).toBe(20);
  expect((await tray.getByRole("listitem").first().boundingBox())!.height).toBeLessThanOrEqual(48);
  await expect(page.locator(".chat-transcript").getByText("Then confirm",{exact:true})).toHaveCount(0);
  await expect(page.locator(".chat-transcript").getByText("Run regression checks",{exact:true})).toHaveCount(0);
  const bounds=await tray.boundingBox(),composer=await page.locator(".chat-composer-box").boundingBox();
  expect(bounds!.y).toBeLessThan(composer!.y);expect(bounds!.y+bounds!.height).toBeGreaterThan(composer!.y);
  await page.reload();await expect(tray.getByRole("listitem")).toHaveCount(2);
  await input.fill("A separate draft");
  await page.locator('input[type="file"]').setInputFiles({name:"new-draft.txt",mimeType:"text/plain",buffer:Buffer.from("Keep this attachment")});
  await expect.poll(()=>page.evaluate(()=>Object.keys(sessionStorage).some(key=>key.endsWith(":attachments")&&JSON.parse(sessionStorage.getItem(key)||"[]").some((i:any)=>i.name==="new-draft.txt"&&i.attachment)))).toBe(true);
  if(host==="dashboard") {
   await page.screenshot({path:`/private/tmp/conversations-queue-${width===1280?"desktop":"mobile"}.png`});
   await page.locator(".chat-composer-safe").screenshot({path:`/private/tmp/conversations-queue-${width===1280?"desktop":"mobile"}-detail.png`});
  }
  const first=tray.getByRole("listitem").first();
  await first.getByRole("button",{name:"Edit queued message",exact:true}).click();
  await first.getByRole("textbox",{name:"Edit queued message",exact:true}).fill("Then confirm the result");
  await expect(input).toHaveValue("A separate draft");
  await first.getByRole("button",{name:"Save",exact:true}).click();
  await expect(tray.getByText("Then confirm the result",{exact:true})).toBeVisible();
  await expect(tray.getByRole("listitem").first().getByRole("img",{name:"Attachments: queued-notes.txt",exact:true})).toBeVisible();
  // Dashboard has stable per-user draft storage; unconfigured visitor clients
  // deliberately isolate new client instances on reload. Queue rows persist
  // server-side in either case (checked before typing and after steering).
  if(host==="dashboard"){await page.reload();await expect(tray.getByText("Then confirm the result",{exact:true})).toBeVisible();}
  await expect(input).toHaveValue("A separate draft");await expect(page.getByText("new-draft.txt",{exact:true})).toBeVisible();
  await tray.getByRole("listitem").last().getByRole("button",{name:"Remove queued message",exact:true}).click();
  await expect(tray.getByRole("listitem")).toHaveCount(1);
  await expect(page.getByText("Run regression checks",{exact:true})).toHaveCount(0);
  await tray.getByRole("button",{name:"Steer with queued message",exact:true}).click();
  await expect(tray).toHaveCount(0);await expect(page.locator(".chat-transcript").getByText("Then confirm the result",{exact:true})).toHaveCount(1);
  await expect(input).toHaveValue("A separate draft");await expect(page.getByText("new-draft.txt",{exact:true})).toBeVisible();
  await page.reload();await expect(tray).toHaveCount(0);await expect(page.getByText("Then confirm the result",{exact:true})).toHaveCount(1);
  const calls=await (await request.get("/requests")).json();
  expect(calls.filter((r:any)=>r.method==="POST"&&r.path.endsWith("/messages"))).toHaveLength(0);
  expect(calls.find((r:any)=>r.method==="PATCH"&&r.path.endsWith("/messages"))).toMatchObject({query:{id:"32"},body:{content:"Then confirm the result"}});
  expect(calls.find((r:any)=>r.path.endsWith("/message-queue"))).toMatchObject({body:{message_id:32,chat_id:chat,action:"steer"}});
 });
}

test("new queued sends stay beside the composer until automatic release, then appear once in the transcript",async({page,request})=>{
 await request.post("/reset");await request.post("/seed",{data:{}});await page.goto("/?host=dashboard");
 await expect(page.locator(".chat-composer-input")).toBeVisible();
 await request.post("/emit",{data:{chat_id:"chat-operator",agent_id:41,thread_id:"chat-operator",response_progress:{phase:"thinking",run_id:"queue-test",revision:1,after_message_id:2,started_at:new Date().toISOString()}}});
 const input=page.locator(".chat-composer-input"),tray=page.getByRole("region",{name:"Queued messages"});
 for(const text of ["Check the result","Then summarize"]){await input.fill(text);await page.getByRole("button",{name:"Send",exact:true}).click();await expect(input).toHaveValue("");await expect(tray.getByText(text,{exact:true})).toBeVisible();}
 await expect(tray.getByRole("listitem")).toHaveCount(2);await expect(page.locator(".chat-transcript").getByText("Check the result",{exact:true})).toHaveCount(0);
 const first={...queued("chat-operator",3,"Check the result"),queue_state:"released",revision:2,attachments:[]};
 await request.post("/append-message",{data:first});
 await expect(tray.getByRole("listitem")).toHaveCount(1);await expect(page.locator(".chat-transcript").getByText("Check the result",{exact:true})).toHaveCount(1);
 await page.reload();await expect(tray.getByText("Then summarize",{exact:true})).toBeVisible();await expect(page.locator(".chat-transcript").getByText("Check the result",{exact:true})).toHaveCount(1);
});

test("inline edit cancellation and failed saves keep both drafts; an offline agent cannot be steered",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:queued("chat-operator")});
 await page.route("**/agents?*",route=>route.fulfill({json:[{id:41,name:"Assistant",status:"stopped",attached:true}]}));
 await page.goto("/?host=dashboard");const tray=page.getByRole("region",{name:"Queued messages"}),input=page.locator(".chat-composer-input");
 await expect(tray.getByRole("button",{name:"Steer with queued message",exact:true})).toBeDisabled();await input.fill("Keep the new draft");
 await tray.getByRole("button",{name:"Edit queued message",exact:true}).click();
 const editor=tray.getByRole("textbox");await editor.fill("Unsaved edit");await editor.press("Escape");
 await expect(tray.getByText("Run regression checks",{exact:true})).toBeVisible();await expect(input).toHaveValue("Keep the new draft");
 await page.route("**/messages?*",route=>route.request().method()==="PATCH"?route.fulfill({status:409,body:"Please try again"}):route.continue());
 await tray.getByRole("button",{name:"Edit queued message",exact:true}).click();await editor.fill("Keep this edited draft");await editor.press("Enter");
 await expect(tray.getByRole("alert")).toContainText("Please try again");await expect(editor).toHaveValue("Keep this edited draft");await expect(input).toHaveValue("Keep the new draft");
 await page.unroute("**/messages?*");await editor.press("Enter");await expect(tray.getByText("Keep this edited draft",{exact:true})).toBeVisible();
});

test("a long queue stays bounded and uses the host's light theme in a narrow panel",async({page,request})=>{
 await request.post("/reset");await request.post("/seed-panel",{data:[{id:"chat-operator",project_id:"project",lead_agent_id:41,title:"Queued follow-ups",kind:"direct",origin:"web",audience:"operator",created_at:"",updated_at:""}]});
 for(let id=31;id<43;id++)await request.post("/append-message",{data:queued("chat-operator",id,`Follow-up ${id}: ${"Check the next part of the result.\n".repeat(8)}`)});
 await page.setViewportSize({width:390,height:740});await page.goto("/?host=dashboard&surface=panel&theme=clean");
 await page.locator('button[data-conversation-id="chat-operator"]').click();
 const tray=page.getByRole("region",{name:"Queued messages"});await expect(tray.getByRole("listitem")).toHaveCount(12);
 const list=tray.locator("ol"),input=page.locator(".chat-composer-input");await expect(input).toBeVisible();
 expect(await list.evaluate(el=>el.scrollHeight>el.clientHeight)).toBe(true);expect((await list.boundingBox())!.height).toBeLessThanOrEqual(220);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 const color=await tray.evaluate(el=>getComputedStyle(el).backgroundColor);expect(color).toBe("rgb(245, 245, 245)");
 const preview=tray.locator(".chat-queue-text").first();
 expect(await preview.evaluate(el=>({height:el.getBoundingClientRect().height,lineHeight:parseFloat(getComputedStyle(el).lineHeight),nowrap:getComputedStyle(el).whiteSpace,overflow:el.scrollWidth>el.clientWidth}))).toEqual({height:20,lineHeight:20,nowrap:"nowrap",overflow:true});
 expect((await tray.getByRole("listitem").first().boundingBox())!.height).toBeLessThanOrEqual(48);
 await page.screenshot({path:"/private/tmp/conversations-queue-light-mobile.png"});
 await input.fill("The composer is still usable");await expect(input).toHaveValue("The composer is still usable");
});
