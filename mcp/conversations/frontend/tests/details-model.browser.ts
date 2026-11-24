import {test,expect} from "@playwright/test";

const mk=(id:string,title:string)=>({id,project_id:"project",lead_agent_id:41,title,kind:"direct",origin:"web",audience:"operator",created_at:"",updated_at:"",archived_at:null});

// The example host's /participants reports, per conversation id: its own
// chat thread with an OpenAI reply, main with an Anthropic reply ("main" in
// the id), or no reply yet ("nomodel" in the id).
test("panel: Details shows each agent's thread and the model behind its last reply",async({page,request})=>{
 await request.post("/reset");
 await request.post("/seed-panel",{data:[mk("conv-own","Own thread chat"),mk("conv-main","Main chat"),mk("conv-nomodel","Unanswered chat")]});
 await page.setViewportSize({width:1280,height:800});
 await page.goto("/?host=dashboard&surface=panel");
 const cases:[string,string,string|null][]=[
  ["Own thread chat","gpt-5.6-terra","Own thread · openai"],
  ["Main chat","claude-sonnet-5-5","Main thread · anthropic"],
  ["Unanswered chat","Model not recorded yet",null],
 ];
 for(const [title,model,source] of cases){
  await page.getByText(title,{exact:true}).first().click();
  await expect(page.getByRole("heading",{name:title,exact:true})).toBeVisible();
  const row=page.locator('[data-agent-model="41"]');
  await expect(row.locator(".chat-agent-model-name")).toHaveText(model);
  if(source)await expect(row.locator(".chat-agent-model-source")).toHaveText(source);
  else await expect(row.locator(".chat-agent-model-source")).toHaveCount(0);
  const line=await row.locator(".chat-agent-model").boundingBox();const panel=await row.boundingBox();
  expect(line!.x+line!.width).toBeLessThanOrEqual(panel!.x+panel!.width+1);
 }
 // The full model name and thread stay available on hover.
 await page.getByText("Main chat",{exact:true}).first().click();
 await expect(page.locator('[data-agent-model="41"] .chat-agent-model')).toHaveAttribute("title",/claude-sonnet-5-5\nmain\nModel of the last reply, /);
});
