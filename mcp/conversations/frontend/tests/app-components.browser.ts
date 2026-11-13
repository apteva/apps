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
