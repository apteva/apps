// Runner-owned real model, sidecars and UI modules. No mocked producer APIs.
import {chromium,expect} from "@playwright/test";
import {mkdtemp,rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import {buildAppCardAssets,appCardHTML} from "./app-card-assets";
const env=process.env;
for(const name of ["APTEVA_TEST_SERVER_URL","APTEVA_TEST_SERVER_API_KEY","APTEVA_TEST_PROJECT_ID","APTEVA_TEST_INSTALLS","APTEVA_TEST_AGENT_IDS"])if(!env[name])throw Error(`Missing runner environment ${name}`);
const base=env.APTEVA_TEST_SERVER_URL!,projectId=env.APTEVA_TEST_PROJECT_ID!,installs=JSON.parse(env.APTEVA_TEST_INSTALLS!),agentId=JSON.parse(env.APTEVA_TEST_AGENT_IDS!)[0];
async function api(path:string,method="GET",body?:unknown){
 const url=new URL(path,base);
 if(path.startsWith("/api/apps/")){const app=path.split("/")[3];url.searchParams.set("project_id",projectId);url.searchParams.set("install_id",String(installs[app]));}
 const r=await fetch(url,{method,headers:{Authorization:`Bearer ${env.APTEVA_TEST_SERVER_API_KEY}`,"Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{})});
 if(!r.ok)throw Error(`Runner API ${method} ${url.pathname}: ${r.status}`);return r.json();
}
const dir=await mkdtemp(join(tmpdir(),"conversations-persistent-views-")),browser=await chromium.launch({headless:true});let chat="";
try{
 const conversation=await api("/api/apps/conversations/chats","POST",{agent_id:agentId,title:"Persistent live resource"});chat=conversation.id;
 const history=()=>api(`/api/apps/conversations/messages?chat_id=${chat}`);
 const send=async(text:string,key:string)=>{
  const before=await history();const last=Math.max(0,...before.map((m:any)=>m.id));
  await api(`/api/apps/conversations/messages?chat_id=${chat}`,"POST",{content:text,client_message_id:key});
  const until=Date.now()+150000;
  while(Date.now()<until){const rows=await history();if(rows.some((m:any)=>m.id>last&&m.role==="agent"&&m.phase==="final"))return rows;await Bun.sleep(500)}throw Error(`No final for ${key}`);
 };
 const previews=(rows:any[])=>rows.flatMap(m=>(m.components??[]).filter((c:any)=>c.app==="view-fixture"));
 let rows=await send("Open the existing dispatch preview and show me its live view BEFORE advancing it. After presenting the view, advance dispatch once and finish this response. We will continue updating this same resource later.","open-preview");
 if(previews(rows).length!==1)throw Error("Initial resource must be presented once");
 if(!rows.some((m:any)=>m.phase==="progress"&&m.components?.some((c:any)=>c.app==="view-fixture")))throw Error("Live view was not presented during work");
 const first=previews(rows)[0];if(first.resource?.app_install_id!==installs["view-fixture"]||first.resource?.resource_id!=="dispatch")throw Error("Producer resource locator was not preserved");
 const assets=await buildAppCardAssets(dir);
 const context=await browser.newContext({viewport:{width:1440,height:1000},extraHTTPHeaders:{Authorization:`Bearer ${env.APTEVA_TEST_SERVER_API_KEY}`}}),page=await context.newPage();
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));let frameLoads=0;
 page.on("request",request=>{if(new URL(request.url()).pathname==="/api/apps/view-fixture/frame")frameLoads++});
 await page.addInitScript(config=>(window as any).APP_CARD_TEST=config,{projectId,installId:installs.conversations,agentId});
 await page.route(`${base}/__card-assets/**`,async route=>{const file=assets.get(new URL(route.request().url()).pathname);if(!file)throw Error("Missing host asset");await route.fulfill({body:Buffer.from(await file.arrayBuffer()),contentType:"text/javascript"})});
 await page.route(`${base}/__persistent-test*`,route=>route.fulfill({body:appCardHTML,contentType:"text/html"}));
 await page.goto(`${base}/__persistent-test`);
 const resource=page.locator('[data-live-resource="dispatch"]');await expect(resource.getByText("Value: 1",{exact:true})).toBeVisible();
 await resource.getByRole("button",{name:"Local count: 0",exact:true}).click();
 await resource.frameLocator("iframe").getByRole("button",{name:"Frame control",exact:true}).click();
 await page.getByRole("button",{name:"Keep visible",exact:true}).first().click();
 const panel=page.getByRole("complementary",{name:"Pinned app view"});await expect(panel).toHaveAttribute("data-card-workspace","side-panel");
 await expect(panel.getByText("Layout: side-panel",{exact:true})).toBeVisible();
 const separator=panel.getByRole("separator");await separator.focus();await separator.press("ArrowLeft");
 rows=await send("Advance dispatch by one and tell me the result.","advance-one");
 rows=await send("Advance dispatch once more.","advance-two");
 if(previews(rows).length!==1)throw Error("Agent created duplicate cards during ordinary follow-ups");
 await expect(resource.getByText("Value: 3",{exact:true})).toBeVisible();await expect(resource).toHaveCount(1);
 await page.getByRole("textbox").last().fill("The view stays visible while I type.");
 await expect(panel).toBeVisible();await panel.getByRole("button",{name:"Float view",exact:true}).click();
 await expect(panel).toHaveAttribute("data-card-workspace","floating");
 await expect(resource.getByRole("button",{name:"Local count: 1",exact:true})).toBeVisible();
 await expect(resource.frameLocator("iframe").getByRole("button",{name:"Frame state retained",exact:true})).toBeVisible();
 if(frameLoads!==1)throw Error(`Moving view reloaded iframe ${frameLoads} times`);
 await panel.getByRole("button",{name:"Minimize view",exact:true}).click();await panel.getByRole("button",{name:"Show view",exact:true}).click();
 await panel.getByRole("button",{name:"Expand view",exact:true}).click();await panel.getByRole("button",{name:"Restore size",exact:true}).click();
 await page.setViewportSize({width:390,height:844});await expect(panel).toHaveAttribute("data-card-workspace","floating");
 const bounds=await panel.boundingBox();if(!bounds||bounds.x<0||bounds.x+bounds.width>391)throw Error("Floating view escaped mobile viewport");
 if(env.APTEVA_CARD_ARTIFACTS)await page.screenshot({path:join(env.APTEVA_CARD_ARTIFACTS,"persistent-floating.png"),fullPage:true});
 await page.setViewportSize({width:1440,height:1000});await panel.getByRole("button",{name:"Return inline",exact:true}).click();await expect(panel).toHaveCount(0);
 await expect(resource.getByRole("button",{name:"Local count: 1",exact:true})).toBeVisible();
 rows=await send("For this explicit refresh, attach the SAME dispatch preview reference again, changing only its title prop to ‘Dispatch refreshed’. Preserve its complete resource locator and record_id. This is the same resource, not another preview.","explicit-refresh");
 if(previews(rows).length!==2)throw Error("Explicit refresh did not preserve a second durable reference");
 const second=previews(rows)[1];if(JSON.stringify(second.resource)!==JSON.stringify(first.resource))throw Error("Refresh changed resource identity");
 await expect(resource).toHaveCount(1);await expect(resource.getByText("Dispatch refreshed",{exact:true})).toBeVisible();
 await expect(resource.getByRole("button",{name:"Local count: 1",exact:true})).toBeVisible();
 await page.getByRole("button",{name:"Keep visible",exact:true}).first().click();
 rows=await send("Also show me the existing inventory preview. Leave dispatch as it is.","second-resource");
 const inventory=page.locator('[data-live-resource="inventory"]');await expect(inventory).toBeVisible();
 await inventory.locator('xpath=ancestor::*[@data-card-reference]').getByRole("button",{name:"Keep visible",exact:true}).click();
 await expect(panel.getByRole("combobox",{name:"Switch pinned view"})).toBeVisible();
 await panel.getByRole("combobox").selectOption({label:"Dispatch refreshed"});await expect(panel.locator('[data-live-resource="dispatch"]')).toBeVisible();
 const composerBox=await page.getByRole("textbox").last().boundingBox();
 if(!composerBox||composerBox.y+composerBox.height>1001)throw Error("Pinned view pushed the conversation composer outside the viewport");
 const panelBox=await panel.boundingBox();
 if(!panelBox||panelBox.y<0||panelBox.y+panelBox.height>1001)throw Error("Pinned view scrolled outside the conversation viewport");
 await expect(resource.frameLocator("iframe").getByRole("button",{name:"Frame state retained",exact:true})).toBeVisible();
 if(frameLoads!==2)throw Error(`Expected one iframe per resource; saw ${frameLoads}`);
 if(env.APTEVA_CARD_ARTIFACTS)await page.screenshot({path:join(env.APTEVA_CARD_ARTIFACTS,"persistent-side-panel.png"),fullPage:true});
 await page.reload();await expect(panel.locator('[data-live-resource="dispatch"]')).toBeVisible();await expect(resource.getByText("Value: 3",{exact:true})).toBeVisible();
 if(errors.length)throw Error(`Browser errors: ${errors.join("; ")}`);
 console.log(JSON.stringify({result:"PASS",reuse:"two normal updates generated no extra cards; explicit refresh kept one mounted view",placements:["side-panel","floating","inline"],state:"React and iframe state preserved across moves and resource switches",history:"selected resource restored after reload",resources:2,producer:"real runner-owned view-fixture",model:"runner verifies openai-codex/gpt-6.1-sol"}));
 await context.close();
}finally{
 if(chat)await api(`/api/apps/conversations/chats?id=${chat}`,"DELETE").catch(()=>{});
 await browser.close();await rm(dir,{recursive:true,force:true});
}
