import {test,expect,type Page} from "@playwright/test";

const apps=(version="1.0.0")=>[
 {name:"computer",install_id:19,project_id:"",version,status:"running",ui_components:[{name:"browser-view",entry:"/ui/BrowserViewCard.mjs",slots:["chat.message_attachment"]}]},
 {name:"tasks",install_id:20,project_id:"project",version,status:"running",ui_components:[{name:"task-card",entry:"/ui/TaskCard.mjs",slots:["chat.message_attachment"]}]},
];
const message=()=>({id:601,conversation_id:"chat-operator",role:"agent",agent_id:41,phase:"final",content:"The browser and task results are ready.",created_at:new Date().toISOString(),components:[
 {app:"computer",name:"browser-view",props:{session_id:"br_card",projectId:"forged",installId:888}},
 {app:"tasks",name:"task-card",props:{taskId:"task-card",title:"Dispatch checklist",projectId:"forged",installId:888}},
]});
async function fixtures(page:Page){
 await page.route("**/api/apps?*",route=>route.fulfill({json:apps()}));
 await page.route("**/api/apps/computer/sessions/**",route=>{
  const path=new URL(route.request().url()).pathname;
  if(path.endsWith("/presentation"))return route.fulfill({json:{session:{session_id:"br_card",current_url:"https://example.com",status:"closed",backend:"local"},screenshot_url:"/api/apps/computer/sessions/br_card/screenshot"}});
  if(path.endsWith("/stream"))return route.fulfill({json:{kind:"final",status:"closed",url:""}});
  return route.fulfill({body:'<svg xmlns="http://www.w3.org/2000/svg" width="800" height="500"><rect width="800" height="500" fill="white"/><text x="40" y="80">Dispatch verified CARD-742</text></svg>',contentType:"image/svg+xml"});
 });
 await page.route("**/api/apps/tasks/tasks/task-card?*",route=>route.fulfill({json:{task:{id:"task-card",title:"Dispatch checklist",state:"draft",agent_id:41,project_id:"project",description:"Ready to review",created_at:"",updated_at:""},events:[],can_edit:true}}));
 await page.route("**/api/app-events/**",route=>route.fulfill({body:": connected\n\n",contentType:"text/event-stream"}));
}
for(const surface of ["widget","panel","project"]){
 test(`${surface} loads actual Computer and Tasks modules with a shared React runtime`,async({page,request})=>{
  await request.post("/reset");await request.post("/append-message",{data:message()});
  if(surface!=="widget")await request.post("/seed-panel",{data:[{id:"chat-operator",project_id:"project",lead_agent_id:41,title:"Support chat",kind:"direct",origin:"web",created_at:"",updated_at:""}]});
  await fixtures(page);const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
  await page.goto(`/__app-card-test?surface=${surface}`);
  if(surface!=="widget")await page.getByText("Support chat",{exact:true}).first().click();
  const card=page.locator('[data-app-card="computer/browser-view"]');await expect(card).toBeVisible();
  await expect.poll(()=>card.locator("img").evaluateAll((images:HTMLImageElement[])=>images.some(image=>image.naturalWidth>0))).toBe(true);
  await expect(page.getByRole("button",{name:"Open task Dispatch checklist"})).toBeVisible();
  for(const width of [390,1280]){await page.setViewportSize({width,height:900});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);}
  expect(errors).toEqual([]);
  const calls=await (await request.get("/requests")).json();
  expect(calls.some((c:any)=>c.path.endsWith("/BrowserViewCard.mjs")&&c.query.install_id==="19"&&c.query.project_id==="project")).toBe(true);
 });
}
test("a failed card retries while the other app card remains usable",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:message()});await fixtures(page);
 let fail=true;
 await page.route("**/api/apps/computer/ui/BrowserViewCard.mjs?*",async route=>{if(fail)return route.fulfill({status:503,body:"unavailable"});return route.continue()});
 await page.goto("/__app-card-test");await expect(page.getByText("This preview is unavailable.",{exact:true})).toBeVisible();
 await expect(page.getByRole("button",{name:"Open task Dispatch checklist"})).toBeVisible();
 fail=false;await page.getByRole("button",{name:"Retry",exact:true}).click();await expect(page.locator('[data-app-card="computer/browser-view"]')).toBeVisible();
 await expect(page.locator("summary").filter({hasText:"computer: browser-view"})).toHaveCount(0);
});
test("an unavailable app never imports arbitrary message props",async({page,request})=>{
 const row=message();row.components[0].app="missing";row.components[0].props={...row.components[0].props,entry:"https://example.com/evil.mjs"} as any;
 await request.post("/reset");await request.post("/append-message",{data:row});await fixtures(page);
 let imported=false;await page.route("https://example.com/**",route=>{imported=true;return route.abort()});
 await page.goto("/__app-card-test");await expect(page.getByText("This preview is unavailable.",{exact:true})).toBeVisible();
 expect(imported).toBe(false);await expect(page.getByRole("button",{name:"Open task Dispatch checklist"})).toBeVisible();
});
test("Inbox review renders app cards beside its question using the same resolver",async({page,request})=>{
 const row:any=message();row.component_kind="interactive";
 row.components.unshift({id:"review",app:"conversations",name:"question-card",props:{title:"Review dispatch"},interaction:{blocking:false,inbox:true,status:"pending",fields:[],actions:[{id:"skip",label:"Skip",cancel:true}]}});
 await request.post("/reset");await request.post("/append-message",{data:row});await fixtures(page);
 await page.goto("/__app-card-test?surface=inbox");await page.locator("article").filter({hasText:"Review dispatch"}).getByRole("button",{name:"Review",exact:true}).click();
 await expect(page.getByRole("dialog").locator('[data-app-card="computer/browser-view"]')).toBeVisible();
 await expect(page.getByRole("dialog").getByRole("button",{name:"Open task Dispatch checklist"})).toBeVisible();
});
test("global Inbox resolves cards in the item's project rather than the dashboard project",async({page,request})=>{
 await request.post("/reset");await fixtures(page);
 const row:any=message();row.component_kind="interactive";
 row.components.unshift({id:"review",app:"conversations",name:"question-card",props:{title:"Review another project"},interaction:{blocking:false,inbox:true,status:"pending",fields:[],actions:[{id:"skip",label:"Skip",cancel:true}]}});
 await page.route("**/api/apps/conversations/inbox?*",route=>route.fulfill({json:{items:[{message:row,priority:0,project_id:"other",project_name:"Other project"}],total:1,next_cursor:"",attention:{},projects:[{id:"other",name:"Other project"}]}}));
 const scopes:string[]=[];
 await page.route("**/api/apps?*",route=>{scopes.push(new URL(route.request().url()).searchParams.get("project_id")!);const rows=apps();rows[1].project_id="other";return route.fulfill({json:rows})});
 await page.goto("/__app-card-test?surface=global-inbox");await page.locator("article").getByRole("button",{name:"Review",exact:true}).click();
 await expect(page.getByRole("dialog").locator('[data-app-card="computer/browser-view"]')).toBeVisible();
 expect(scopes).toEqual(["other"]);
 const calls=await (await request.get("/requests")).json();
 expect(calls.some((c:any)=>c.path.endsWith("/BrowserViewCard.mjs")&&c.query.project_id==="other"&&c.query.install_id==="19")).toBe(true);
});

const liveMessage=(id=701,title="Live dispatch")=>({id,conversation_id:"chat-operator",role:"agent",agent_id:41,phase:"final",content:"The resource is ready.",created_at:new Date().toISOString(),components:[{app:"view-fixture",name:"preview-card",resource:{app_install_id:21,resource_type:"preview.document",resource_id:"dispatch"},props:{record_id:"dispatch",title}}]});
async function workspaceFixtures(page:Page){
 await page.route("**/api/apps?*",route=>route.fulfill({json:[{name:"view-fixture",install_id:21,project_id:"project",version:"1.0.0",status:"running",ui_components:[{name:"preview-card",entry:"/ui/PreviewCard.mjs",slots:["chat.message_attachment"],resource_types:["preview.document"]}]}]}));
 await page.route("**/api/apps/view-fixture/records/**",route=>route.fulfill({json:{value:2}}));
 await page.route("**/api/apps/view-fixture/frame?*",route=>route.fulfill({contentType:"text/html",body:'<button onclick="this.textContent=\'Frame state retained\'">Frame control</button>'}));
}
test("docking, floating, resizing and return preserve the mounted component and iframe",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:liveMessage()});await workspaceFixtures(page);
 await page.setViewportSize({width:1440,height:1000});let frames=0;page.on("request",r=>{if(new URL(r.url()).pathname.endsWith("/frame"))frames++});
 await page.goto("/__app-card-test");const card=page.locator('[data-live-resource="dispatch"]');
 await card.getByRole("button",{name:"Local count: 0"}).click();await card.frameLocator("iframe").getByRole("button",{name:"Frame control"}).click();
 await page.getByRole("button",{name:"Keep visible",exact:true}).click();const panel=page.getByRole("complementary",{name:"Pinned app view"});
 await expect(panel).toHaveAttribute("data-card-workspace","side-panel");
 const previous=await panel.boundingBox();await panel.getByRole("separator").press("ArrowLeft");expect((await panel.boundingBox())!.width).toBeGreaterThan(previous!.width);
 await panel.getByRole("button",{name:"Float view",exact:true}).click();await expect(panel).toHaveAttribute("data-card-workspace","floating");
 const start=await panel.locator("header span").boundingBox();await page.mouse.move(start!.x+8,start!.y+8);await page.mouse.down();await page.mouse.move(start!.x-60,start!.y-40);await page.mouse.up();
 const moved=await panel.boundingBox();expect(moved!.x).toBeGreaterThanOrEqual(0);
 await expect(card.getByRole("button",{name:"Local count: 1"})).toBeVisible();await expect(card.frameLocator("iframe").getByRole("button",{name:"Frame state retained"})).toBeVisible();
 await panel.getByRole("button",{name:"Minimize view",exact:true}).click();await expect(card).not.toBeVisible();await panel.getByRole("button",{name:"Show view",exact:true}).click();
 await page.setViewportSize({width:390,height:844});await expect(panel).toBeVisible();const b=await panel.boundingBox();expect(b!.x+b!.width).toBeLessThanOrEqual(391);
 await panel.getByRole("button",{name:"Return inline",exact:true}).click();await expect(panel).toHaveCount(0);
 await expect(card.getByRole("button",{name:"Local count: 1"})).toBeVisible();await expect(card.frameLocator("iframe").getByRole("button",{name:"Frame state retained"})).toBeVisible();expect(frames).toBe(1);
});
test("repeated resource refs update one pinned view and reload restores its authorized reference",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:liveMessage()});await workspaceFixtures(page);
 await page.goto("/__app-card-test");const card=page.locator('[data-live-resource="dispatch"]');await card.getByRole("button",{name:"Local count: 0"}).click();
 await page.getByRole("button",{name:"Keep visible",exact:true}).click();
 await request.post("/append-message",{data:liveMessage(702,"Updated dispatch")});
 await expect(card).toHaveCount(1);await expect(card.getByText("Updated dispatch",{exact:true})).toBeVisible();await expect(card.getByRole("button",{name:"Local count: 1"})).toBeVisible();
 await request.post("/append-message",{data:{...liveMessage(701,"Older reference"),revision:2}});await expect(page.getByRole("complementary").locator("header")).toContainText("Updated dispatch");
 await page.getByRole("textbox").last().fill("Continuing the conversation");await expect(page.getByRole("complementary")).toBeVisible();
 const composer=await page.getByRole("textbox").last().boundingBox();expect(composer!.y+composer!.height).toBeLessThanOrEqual(721);
 await page.reload();await expect(page.getByRole("complementary").locator('[data-live-resource="dispatch"]')).toBeVisible();
 const preferences=await page.evaluate(()=>Object.entries(sessionStorage).filter(([key])=>key.startsWith("conversations:views:")));expect(JSON.stringify(preferences)).not.toContain("record_id");expect(JSON.stringify(preferences)).not.toContain("dispatch");
});
test("resource references cannot substitute another installation or undeclared resource type",async({page,request})=>{
 await request.post("/reset");const row=liveMessage();row.components[0].resource.app_install_id=999;await request.post("/append-message",{data:row});await workspaceFixtures(page);
 await page.goto("/__app-card-test");await expect(page.getByText("This preview is unavailable.",{exact:true})).toBeVisible();await expect(page.locator('[data-live-resource]')).toHaveCount(0);
});
test("revoking an app removes its pinned view instead of retaining stale content",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:liveMessage()});await workspaceFixtures(page);await page.goto("/__app-card-test");
 await page.getByRole("button",{name:"Keep visible",exact:true}).click();await expect(page.getByRole("complementary")).toBeVisible();
 await page.route("**/api/apps?*",route=>route.fulfill({json:[]}));await page.evaluate(()=>window.dispatchEvent(new Event("apteva:apps-changed")));
 await expect(page.getByRole("complementary")).toHaveCount(0);await expect(page.locator('[data-live-resource]')).toHaveCount(0);await expect(page.getByText("This preview is unavailable.",{exact:true})).toBeVisible();
});
test("switching conversations hides the old workspace and restores its view on return",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:liveMessage()});await workspaceFixtures(page);
 const conv=(id:string,title:string)=>({id,title,project_id:"project",lead_agent_id:41,kind:"direct",origin:"web",created_at:"",updated_at:""});
 await request.post("/seed-panel",{data:[conv("chat-operator","Support chat"),conv("chat-other","Other chat")]});
 await page.route("**/api/apps/conversations/messages?*",route=>new URL(route.request().url()).searchParams.get("chat_id")==="chat-other"?route.fulfill({json:{messages:[],cursor:0,has_more:false,before:0}}):route.continue());
 await page.goto("/__app-card-test?surface=panel");await page.getByText("Support chat",{exact:true}).first().click();
 const workspace=page.getByRole("complementary",{name:"Pinned app view"});
 await page.getByRole("button",{name:"Keep visible",exact:true}).click();await expect(workspace).toBeVisible();
 await page.getByText("Other chat",{exact:true}).first().click();await expect(workspace).toHaveCount(0);
 await page.getByText("Support chat",{exact:true}).first().click();await expect(workspace.locator('[data-live-resource="dispatch"]')).toBeVisible();
});
test("a long conversation scrolls independently while its panel and composer stay in view",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:liveMessage()});await workspaceFixtures(page);
 for(let id=710;id<735;id++)await request.post("/append-message",{data:{...liveMessage(id),content:"A follow-up in the same conversation. ".repeat(8),components:[]}});
 await page.setViewportSize({width:1440,height:1000});await page.goto("/__app-card-test");await page.getByRole("button",{name:"Keep visible",exact:true}).click();
 const panel=page.getByRole("complementary",{name:"Pinned app view"});await page.getByRole("textbox").last().fill("Continue with the same view");
 expect(await page.evaluate(()=>document.documentElement.scrollHeight)).toBeLessThanOrEqual(1001);
 const bounds=await panel.boundingBox();expect(bounds!.y).toBeGreaterThanOrEqual(0);expect(bounds!.y+bounds!.height).toBeLessThanOrEqual(1001);
 const composer=await page.getByRole("textbox").last().boundingBox();expect(composer!.y+composer!.height).toBeLessThanOrEqual(1001);
});
