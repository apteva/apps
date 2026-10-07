import {expect, test} from "@playwright/test";
const step=(key:string,index:number,state:string)=>({id:`id-${key}`,run_id:"selection-run",key,definition:{key,name:["Load weather","Send notification","Validate receipts"][index],role:"worker",instructions:`Exact instructions for ${key}.`,expected_output:`Expected ${key} evidence`,depends_on:index ? [index===1?"weather":"notify"]:[]},executor:{kind:"agent",agent_id:7},state,progress:state==="completed"?100:40,output:state==="completed"?JSON.stringify({receipt:`${key}_exact.png`}):"",error:"",execution_id:`exe-${key}`,target_thread_id:"selection-worker",completed_at:state==="completed"?"2026-10-07T10:00:00Z":undefined});
const run=(completed=false,advanced=false)=>({id:"selection-run",process_id:"weather",version:3,workflow:true,backend:"agent",state:completed?"completed":"running",progress:completed?100:50,created_at:"2026-10-07T09:00:00Z",steps:[step("weather",0,"completed"),step("notify",1,completed||advanced?"completed":"running"),step("validate",2,completed?"completed":advanced?"running":"pending")]});
async function open(page:any,project:boolean) {
 await page.goto("/?live&activity");
 if(!project) await page.getByRole("button",{name:"Hourly weather alerts",exact:true}).click();
 await page.getByRole("button",{name:"Runs",exact:true}).click();
 await page.locator(".run-list > button").first().click();
}
for(const project of [true,false]) {
 test(`completed steps use the same sidebar from ${project?"project":"procedure"} runs`,async({page,request})=>{
  await request.post("/fixture/telemetry",{data:[
    {id:"weather-call",thread_id:"selection-worker",instance_id:7,type:"tool.call",time:"2026-10-07T09:01:00Z",data:{id:"weather-call",name:"weather_get",execution_ids:["exe-weather"]}},
    {id:"notify-call",thread_id:"selection-worker",instance_id:7,type:"tool.call",time:"2026-10-07T09:02:00Z",data:{id:"notify-call",name:"notify_send",execution_ids:["exe-notify"]}}
  ]});
  await request.post("/fixture/runs",{data:[run(true)]});await open(page,project);
  const panel=page.getByRole("complementary",{name:"Run step details"});
  await expect(panel).toContainText("validate_exact.png");
  await page.getByRole("button",{name:"Step 1: Load weather",exact:true}).click();
  await expect(panel).toContainText("weather_exact.png");await expect(panel).not.toContainText("validate_exact.png");
  await expect(panel).toContainText("weather_get");await expect(panel).not.toContainText("notify_send");
  await expect(panel).toContainText("Exact instructions for weather.");await expect(panel).toContainText("Expected weather evidence");
  await expect(page.locator(".run-flow-panel .run-step-details")).toHaveCount(0);
  await expect(page.locator(".pf-inspector")).toHaveCount(0);
  await expect(page.locator(".pf-step.is-selected")).toContainText("Load weather");
  await panel.getByLabel("Select step",{exact:true}).selectOption("id-notify");await expect(panel).toContainText("notify_exact.png");
  await expect(panel).toContainText("notify_send");await expect(panel).not.toContainText("weather_get");
  await expect(page.locator(".run-step-details")).toHaveCount(1);
  await page.screenshot({path:`/private/tmp/processes-step-sidebar-${project?"project":"procedure"}.png`,fullPage:true});
 });
}
test("manual selection stays pinned through live updates and can follow current work again",async({page,request})=>{
 await request.post("/fixture/runs",{data:[run()]});await open(page,false);
 const panel=page.getByRole("complementary",{name:"Run step details"});await expect(panel.getByLabel("Select step")).toHaveValue("id-notify");
 const weather=page.getByRole("button",{name:"Step 1: Load weather",exact:true});await weather.focus();await weather.press("Enter");
 await expect(panel.getByLabel("Select step")).toHaveValue("id-weather");
 await request.post("/fixture/runs",{data:[run(false,true)]});await request.post("/fixture/events",{data:{app:"processes",project_id:"test",install_id:77,seq:901,topic:"step.updated",data:{process_id:"weather"}}});
 await expect(panel.getByLabel("Select step")).toHaveValue("id-weather");
 await panel.getByRole("button",{name:"Follow current step",exact:true}).click();
 await expect(panel.getByLabel("Select step")).toHaveValue("id-validate");
 await expect(panel.getByRole("button",{name:"Follow current step",exact:true})).toHaveCount(0);
 await page.setViewportSize({width:375,height:1000});await panel.getByLabel("Select step").selectOption("id-weather");await expect(panel).toContainText("weather_exact.png");
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:"/private/tmp/processes-step-sidebar-mobile.png",fullPage:true});
});
