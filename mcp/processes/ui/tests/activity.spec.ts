import {expect,test} from "@playwright/test";
const step=(id:string,state:string,depends_on:string[]=[])=>({id,run_id:"activity-run",key:id,definition:{key:id,name:id==="weather"?"Load weather":"Send notification",role:"worker",instructions:"Follow the exact instructions.",expected_output:"Exact receipt",depends_on},executor:{kind:"agent",agent_id:7},state,progress:0,output:"",error:"",execution_id:`exe-${id}`,target_thread_id:"activity-worker"});
const run=(state="ready")=>({id:"activity-run",process_id:"weather",version:3,workflow:true,backend:"agent",state:"running",created_at:new Date().toISOString(),steps:[step("weather",state),step("notify","pending",["weather"])]});
const event=(type:string,second:number,data:any,thread_id="activity-worker")=>({id:`${type}-${second}`,thread_id,instance_id:7,type,time:`2026-10-06T12:00:${String(second).padStart(2,"0")}Z`,data});
test("run aligns graph, animates ready/waiting and respects reduced motion",async({page,request})=>{
 await request.post("/fixture/telemetry",{data:[]});await request.post("/fixture/runs",{data:[run()]});await page.goto("/?live&activity");
 await page.getByRole("button",{name:"Hourly weather alerts",exact:true}).click();await page.getByRole("button",{name:"Runs",exact:true}).click();await page.locator(".run-list > button").first().click();
 const graph=(await page.locator(".pf-shell").boundingBox())!,current=(await page.locator(".run-current-step").boundingBox())!;expect(graph.y).toBeCloseTo(current.y,0);
 const ready=page.locator('.flow-status[data-state="ready"] svg');expect(await ready.evaluate(el=>getComputedStyle(el).animationName)).toBe("flow-status-pulse");
 await request.post("/fixture/runs",{data:[run("waiting")]});await request.post("/fixture/events",{data:{app:"processes",project_id:"test",install_id:77,seq:801,topic:"step.updated",data:{process_id:"weather"}}});
 const waiting=page.locator('.flow-status[data-state="waiting"] svg');await expect(waiting).toHaveCount(1);expect(await waiting.evaluate(el=>getComputedStyle(el).animationName)).toBe("flow-status-pulse");
 await page.emulateMedia({reducedMotion:"reduce"});expect(await waiting.evaluate(el=>getComputedStyle(el).animationName)).toBe("none");
});
test("current worker activity streams thoughts and tools and recovers missed events",async({page,request})=>{
 await page.route("**/fixture/process-icon.svg",route=>route.fulfill({contentType:"image/svg+xml",body:'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="black" d="M3 3h18v18H3z"/></svg>'}));
 await request.post("/fixture/telemetry",{data:[]});await request.post("/fixture/runs",{data:[run("running")]});await page.goto("/?live&activity");await page.getByRole("button",{name:"Hourly weather alerts",exact:true}).click();await page.getByRole("button",{name:"Runs",exact:true}).click();await page.locator(".run-list > button").first().click();
 const panel=page.locator(".run-current-step");await expect(panel).toContainText("Waiting for worker activity");
 const events=[event("tool.call",1,{id:"claim",name:"processes_step_claim",args:{step_id:"weather",_reason:"Claim weather"},execution_ids:["exe-weather"]}),event("llm.start",2,{iteration:2}),event("llm.thinking",3,{iteration:2,text:"Check the exact weather receipt."}),event("tool.call",4,{id:"fetch",name:"weather_get",reason:"Read live weather",execution_ids:["exe-weather"]})];
 await page.evaluate(events=>events.forEach(e=>(window as any).__emitWorkerTelemetry(e)),events);
 await expect(panel).toContainText("weather_get");await panel.getByText("Thinking · in progress",{exact:true}).click();await expect(panel).toContainText("Check the exact weather receipt.");
 await page.evaluate(e=>(window as any).__emitWorkerTelemetry(e),event("llm.thinking",5,{iteration:2,text:"Wrong worker text"},"another-worker"));await expect(panel).not.toContainText("Wrong worker text");
 expect(await panel.locator(".tool-icon-mask").evaluate(el=>getComputedStyle(el).backgroundColor)).not.toBe("rgb(0, 0, 0)");
 await request.post("/fixture/telemetry",{data:[...events,event("tool.result",6,{id:"fetch",name:"weather_get",execution_ids:["exe-weather"]}),event("llm.done",7,{iteration:2})]});await page.evaluate(()=>window.dispatchEvent(new Event("apteva.telemetry.reconnected")));
 await expect(panel).toContainText("Thought · completed");await expect(panel.locator(".tool-call-copy").filter({hasText:"weather_get"})).toContainText("finished");
 await page.evaluate(e=>(window as any).__emitWorkerTelemetry(e),event("tool.call",8,{id:"sleep",name:"pace",reason:"Await operator approval",execution_ids:["exe-weather"]}));await expect(panel.locator(".run-activity-status")).toContainText("Worker is waiting");
 await panel.getByRole("button",{name:"Thoughts",exact:true}).click();await expect(panel).not.toContainText("weather_get");
 await page.screenshot({path:"/private/tmp/processes-live-worker-activity.png",fullPage:true});
});
test("step editor exposes process-wide approval policy",async({page})=>{
 await page.goto("/?activity");await page.getByRole("button",{name:"Hourly weather alerts",exact:true}).click();await page.getByRole("button",{name:"Step 2: Post alert in Conversations",exact:true}).click();const dialog=page.getByRole("dialog",{name:"Edit step"});await dialog.getByText("Process-wide rules also apply",{exact:true}).click();await expect(dialog).toContainText("Separate operator approval is required before notification.");
});
