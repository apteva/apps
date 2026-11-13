// Real model + Server/Core/sidecars + Chromium. Run through apteva test only.
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
 const response=await fetch(url,{method,headers:{Authorization:`Bearer ${env.APTEVA_TEST_SERVER_API_KEY}`,"Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{})});
 if(!response.ok)throw Error(`Runner API ${method} ${url.pathname}: ${response.status} ${(await response.text()).slice(0,200)}`);
 return response.json();
}
const site=Bun.serve({hostname:"127.0.0.1",port:0,fetch:()=>new Response('<!doctype html><html><body><h1>Dispatch readiness</h1><p>The launch checklist has 7 verified items. Reference: CARD-742.</p></body></html>',{headers:{"Content-Type":"text/html"}})});
const dir=await mkdtemp(join(tmpdir(),"conversations-app-cards-"));
let chat="",taskId="",sessionId="";
const browser=await chromium.launch({headless:true});
try{
 const draft=await api("/api/apps/tasks/tasks","POST",{state:"draft",title:"Check dispatch readiness",description:"Isolated rendering test. Leave this draft unstarted."});
 taskId=draft.task?.id??draft.id;if(!taskId)throw Error("Missing draft task id");
 const conversation=await api("/api/apps/conversations/chats","POST",{agent_id:agentId,title:"App card rendering validation"});chat=conversation.id;
 await api(`/api/apps/conversations/messages?chat_id=${chat}`,"POST",{content:`Open http://127.0.0.1:${site.port} and show me the browser result. In the same final reply also attach this operator-owned draft card: {"app":"tasks","name":"task-card","props":{"taskId":"${taskId}"}}. The draft is called “Check dispatch readiness”. This supplied reference is for my operator UI only; do not use Tasks tools to inspect or start the draft.`,client_message_id:"app-card-request"});
 let final:any;
 const until=Date.now()+240000;
 while(Date.now()<until){
  const messages=await api(`/api/apps/conversations/messages?chat_id=${chat}`);
  final=messages.find((m:any)=>m.role==="agent"&&m.phase==="final"&&m.components?.some((c:any)=>c.app==="computer"&&c.name==="browser-view")&&m.components?.some((c:any)=>c.app==="tasks"&&c.name==="task-card"));
  if(final)break;await Bun.sleep(1000);
 }
 if(!final)throw Error("Model did not attach both declared app cards to its final reply");
 sessionId=final.components.find((c:any)=>c.app==="computer").props.session_id;
 const presentation=await api(`/api/apps/computer/sessions/${sessionId}/presentation`);
 if(!presentation.session.current_url.startsWith(`http://127.0.0.1:${site.port}`)||presentation.session.status==="active")throw Error("Browser result is not the closed local test page");
 if(final.components.find((c:any)=>c.app==="tasks").props.taskId!==taskId)throw Error("Wrong task reference");
 const record=await api(`/api/apps/tasks/tasks/${taskId}`);if((record.task??record).state!=="draft")throw Error("Draft task was started");
 await api(`/api/apps/conversations/messages?chat_id=${chat}`,"POST",{content:"Ask me an optional yes/no question titled ‘Follow up on dispatch?’ using a question card in my Inbox. Include the same browser-view and task-card beside that question in the same message so I can review both results. Do not perform more browser actions, start the draft, or change any records.",client_message_id:"app-card-inbox-request"});
 let review:any;
 const reviewUntil=Date.now()+150000;
 while(Date.now()<reviewUntil){
  const inbox=await api("/api/apps/conversations/inbox?page=1");
  review=inbox.items.find((item:any)=>item.message.conversation_id===chat&&item.message.components?.some((c:any)=>c.app==="conversations"&&c.name==="question-card")&&item.message.components?.some((c:any)=>c.app==="computer"&&c.name==="browser-view")&&item.message.components?.some((c:any)=>c.app==="tasks"&&c.name==="task-card"));
  if(review)break;await Bun.sleep(1000);
 }
 if(!review)throw Error("Question and both app cards were not delivered together to Inbox");
 const questionTitle=review.message.components.find((c:any)=>c.name==="question-card").props.title;
 const assets=await buildAppCardAssets(dir);
 const context=await browser.newContext({extraHTTPHeaders:{Authorization:`Bearer ${env.APTEVA_TEST_SERVER_API_KEY}`}});
 const page=await context.newPage();const errors:string[]=[];
 page.on("pageerror",error=>errors.push(error.message));
 await page.addInitScript(config=>(window as any).APP_CARD_TEST=config,{projectId,installId:installs.conversations,agentId});
 await page.route(`${base}/__card-assets/**`,async route=>{const file=assets.get(new URL(route.request().url()).pathname);if(!file)throw Error("Missing test asset");await route.fulfill({body:Buffer.from(await file.arrayBuffer()),contentType:"text/javascript"})});
 await page.route(`${base}/__app-card-test*`,route=>route.fulfill({body:appCardHTML,contentType:"text/html"}));
 for(const surface of ["widget","panel","project","inbox"]){
  await page.goto(`${base}/__app-card-test?surface=${surface}`);
  if(surface==="panel"||surface==="project")await page.getByText("App card rendering validation",{exact:true}).first().click();
  if(surface==="inbox")await page.locator("article").filter({hasText:questionTitle}).getByRole("button",{name:"Review",exact:true}).click();
  const computer=page.locator('[data-app-card="computer/browser-view"]').last();
  const task=page.locator('[data-app-card="tasks/task-card"]').last();
  await expect(computer).toBeVisible({timeout:30000});await expect(task).toBeVisible();
  await expect.poll(()=>computer.locator("img").evaluateAll((images:HTMLImageElement[])=>images.some(image=>image.complete&&image.naturalWidth>0))).toBe(true);
  await expect(task.getByRole("button",{name:"Open task Check dispatch readiness"})).toBeVisible();
  if(process.env.APTEVA_CARD_ARTIFACTS)await page.screenshot({path:join(process.env.APTEVA_CARD_ARTIFACTS,`${surface}.png`),fullPage:true});
  await task.getByRole("button").first().click();await expect(page.getByText("Isolated rendering test. Leave this draft unstarted.",{exact:true}).first()).toBeVisible();
  if(process.env.APTEVA_CARD_ARTIFACTS)await page.screenshot({path:join(process.env.APTEVA_CARD_ARTIFACTS,`${surface}-details.png`),fullPage:true});
 }
 if(errors.length)throw Error(`Browser errors: ${errors.join("; ")}`);
 const after=await api(`/api/apps/tasks/tasks/${taskId}`);if((after.task??after).state!=="draft")throw Error("Review changed the draft state");
 console.log(JSON.stringify({result:"PASS",surfaces:["widget","panel","project","inbox"],message_id:final.id,inbox_message_id:review.message.id,computer:"real local webpage session and loaded final screenshot",tasks:"real operator draft record and working details action; draft stays unstarted",model:"runner verifies requested provider/model"}));
 await context.close();
}finally{
 if(sessionId)await api("/api/apps/computer/mcp","POST",{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"browser_session",arguments:{action:"close",session_id:sessionId}}}).catch(()=>{});
 if(chat)await api(`/api/apps/conversations/chats?id=${chat}`,"DELETE").catch(()=>{});
 if(taskId)await api(`/api/apps/tasks/tasks/${taskId}`,"DELETE").catch(()=>{});
 await browser.close();site.stop(true);await rm(dir,{recursive:true,force:true});
}
