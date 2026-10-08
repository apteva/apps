import { test,expect } from "@playwright/test";

test("compact history pages load exact selected details, checkpoints and scoped knowledge",async({page})=>{
 const calls:string[]=[];
 const step={key:"check",name:"Check receipt",role:"worker",instructions:"Preserve exact identity",expected_output:"Receipt",depends_on:[]};
 const definition={id:"weather",name:"Hourly weather alerts",description:"Compact fixture",status:"active",version:1,instructions:"Frozen policy",completion_criteria:"Receipt",steps:[step],assignments:[],summary_fields:[{key:"checked",label:"Checked areas"}]};
 const row=(id:string)=>({id,process_id:"weather",process_name:definition.name,version:1,assignment_id:"assignment-a",assignment:{id:"assignment-a",name:"October",owner_agent_id:7},state:"completed",workflow:true,created_at:"2026-10-08T10:00:00Z",progress:100,metadata_only:true});
 const summary={revision:2,author:"agent:7:worker",created_at:"2026-10-08T10:00:00Z",outcome:"North checked",references:["source:9007199254740993"],fields:{checked:1}};
 await page.route("**/api/apps/processes/processes**",async route=>{
  const url=new URL(route.request().url());calls.push(url.pathname+url.search);
  if(url.pathname.endsWith("/runs/old/summary"))return route.fulfill({json:{available:true,summary}});
  if(url.pathname.endsWith("/runs/old"))return route.fulfill({json:{run:{...row("old"),metadata_only:false,result:"Exact receipt source:9007199254740993"},definition,steps:[{id:"step-old",key:"check",run_id:"old",definition:step,executor:{kind:"agent",agent_id:7},state:"completed",progress:100,output:"source:9007199254740993",updated_by:"agent:7:worker"}]}});
  if(url.pathname.endsWith("/memory")) {expect(url.searchParams.get("assignment_id")).toBe("assignment-a");expect(url.searchParams.get("scope")).toBe("campaign-a");return route.fulfill({json:{entries:[{key:"area:north",kind:"searched",content:"North was exhausted",references:["source:9007199254740993"],run_id:"old",revision:1}],has_more:false}});}
  if(url.pathname.endsWith("/runs")){expect(url.searchParams.get("view")).toBe("compact");return route.fulfill({json:url.searchParams.has("cursor")?{runs:[row("old")],has_more:false}:{runs:[row("latest")],has_more:true,next_cursor:"fixture-cursor"}});}
  if(url.pathname.endsWith("/weather"))return route.fulfill({json:{process:definition,versions:[{version:1,definition}]}});
  return route.fulfill({json:{processes:[definition]}});
 });
 await page.goto("/?compact_history");
 await page.getByRole("button",{name:"Runs",exact:true}).click();
 expect(calls.filter(c=>c.includes("/runs/old")).length).toBe(0);
 await page.getByRole("button",{name:"Load older runs",exact:true}).click();
 await expect(page.locator(".run-list > button")).toHaveCount(2);
 await page.locator(".run-list > button").last().click();
 await expect(page.locator(".run-detail-page")).toBeVisible();
 await page.getByText("Run checkpoint · revision 2",{exact:true}).click();
 await expect(page.getByText("North checked",{exact:true})).toBeVisible();
 await expect(page.getByText("Checked areas:",{exact:true})).toBeVisible();
 await page.getByRole("button",{name:"View result",exact:true}).click();
 await expect(page.getByRole("dialog")).toContainText("source:9007199254740993");
 await page.keyboard.press("Escape");
 await page.getByText("Cross-run knowledge",{exact:true}).click();
 await page.getByLabel("Knowledge scope").fill("campaign-a");
 await page.getByRole("button",{name:"Search knowledge",exact:true}).click();
 await expect(page.getByText("North was exhausted",{exact:true})).toBeVisible();
 await page.screenshot({path:"/private/tmp/processes-run-memory-ui.png",fullPage:true});
 await page.getByRole("button",{name:"← Back to runs",exact:true}).click();
 await page.getByRole("button",{name:"Refresh runs",exact:true}).click();
 await expect(page.locator(".run-list > button")).toHaveCount(2);
 await expect(page.getByRole("button",{name:"Load older runs",exact:true})).toHaveCount(0);
});

test("procedure summary labels can be configured and saved",async({page})=>{
 await page.goto("/");
 await page.getByRole("button",{name:"Hourly weather alerts",exact:true}).click();
 await page.getByRole("button",{name:"Procedure",exact:true}).click();
 await page.getByRole("button",{name:"Edit procedure",exact:true}).click();
 await page.getByText("General instructions & settings",{exact:true}).click();
 await page.getByText("Run summary fields",{exact:true}).click();
 await page.getByRole("button",{name:"Add summary field",exact:true}).click();
 await page.getByLabel("Summary field 1 key",{exact:true}).fill("campaign");
 await page.getByLabel("Summary field 1 label",{exact:true}).fill("Campaign");
 await page.getByRole("button",{name:/Save/}).first().click();
 const saved=await page.evaluate(()=>JSON.parse(sessionStorage.getItem("process") || "null"));
 expect(saved.summary_fields).toEqual([{key:"campaign",label:"Campaign"}]);
});
