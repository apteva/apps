import { test, expect } from "@playwright/test";

for (const host of ["dashboard", "external", "package"]) {
 test(`${host}: ordered streamed acknowledgement, tool stack, thinking and final reply`, async ({page,request}) => {
  await request.post("/reset"); await page.goto(`/?host=${host}`);
  await expect(page.getByTitle("Live")).toBeVisible();
  const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
  const base={chat_id:chat,agent_id:41,thread_id:chat,call_id:"",text:"",done:false};
  const at=(ms:number)=>new Date(Date.now()-60_000+ms).toISOString();
  const start=at(0); let revision=0;
  const emit=(extra:object)=>request.post("/emit",{data:{...base,...extra}});
  const progress=(phase:string,extra={})=>emit({response_progress:{phase,run_id:"response",revision:++revision,after_message_id:1,started_at:start,...extra}});
  const message=(id:number,content:string,created_at:string)=>request.post("/append-message",{data:{id,conversation_id:chat,agent_id:41,role:id===1?"user":"agent",components:[],content,created_at}});
  const thinking=page.getByRole("status",{name:"Thinking",exact:true});
  await message(1,"List processes",start); await progress("thinking");
  await expect(thinking).toBeVisible();
  await emit({call_id:"ack-text",run_id:"1",text:"I will check the processes.",created_at:at(6000),after_message_id:1});
  await expect(page.getByText("I will check the processes.",{exact:true})).toBeVisible();
  await expect(thinking).toHaveCount(0);
  await emit({call_id:"ack-text",run_id:"1",done:true}); await progress("thinking");
  await expect(thinking).toBeVisible();
  const row=page.locator(".chat-tool-activity");
  for(let i=0;i<3;i++) {
   const call=`lookup-${i}`,when=17_000+i*5000;
   await progress("preparing_tool",{tool_name:"apteva-server_app_tool_call",call_id:call,tool_started_at:at(when-200)});
   await expect(row).toHaveCount(1); await expect(thinking).toHaveCount(0);
   const tool={id:800+i,chat_id:chat,agent_id:41,thread_id:chat,call_id:call,name:"apteva-server_app_tool_call",reason:`Lookup ${i+1}`,status:"running",started_at:at(when),revision:1};
   await emit({tool_activity:tool});
   await expect(row).toHaveAttribute("aria-label",new RegExp(`Lookup ${i+1}`));
   await expect(row.locator(".chat-tool-copy-running")).toHaveCount(1);
   await expect.poll(async()=>{
    const acknowledgement=await page.getByText("I will check the processes.",{exact:true}).boundingBox(), tools=await row.boundingBox();
    return Boolean(acknowledgement && tools && acknowledgement.y < tools.y);
   }).toBe(true);
   await emit({tool_activity:{...tool,status:"completed",ended_at:at(when+18),revision:2}}); await progress("continuing");
   await expect(row.locator(".chat-tool-copy-running")).toHaveCount(0); await expect(thinking).toBeVisible();
  }
  await message(2,"I will check the processes.",at(6270));
  await expect(page.getByText("I will check the processes.",{exact:true})).toHaveCount(1);
  await emit({call_id:"final",run_id:"2",text:"There is one",created_at:at(32_580),after_message_id:1});
  await expect(page.getByText("There is one",{exact:true})).toBeVisible(); await expect(thinking).toHaveCount(0);
  await emit({call_id:"final",run_id:"2",text:"There is one process.",created_at:at(32_580),after_message_id:1});
  await emit({call_id:"final",run_id:"2",done:true}); await progress("idle");
  await expect(page.getByText("There is one process.",{exact:true})).toBeVisible();
  await message(3,"There is one process.",at(35_590));
  const final=page.getByText("There is one process.",{exact:true});
  await expect(final).toHaveCount(1); await expect(thinking).toHaveCount(0);
  await expect.poll(async()=>{
   const tools=await row.boundingBox(), reply=await final.boundingBox();
   return Boolean(tools && reply && tools.y < reply.y);
  }).toBe(true);
  await page.screenshot({path:test.info().outputPath("ordered-lifecycle.png")});
 });
}

for (const host of ["dashboard", "panel", "external", "package"]) {
 test(`${host}: second and third responses keep animated progress through refresh`, async ({page,request}) => {
  await request.post("/reset");
  if(host==="panel") await request.post("/seed-panel",{data:[{id:"chat-operator",project_id:"project",lead_agent_id:41,title:"Support chat",kind:"direct",audience:"operator",origin:"web",created_at:"",updated_at:""}]});
  await page.goto(host==="panel"?"/?host=dashboard&surface=panel":`/?host=${host}`);
  await expect(page.getByTitle("Live")).toBeVisible();
  const chat=(host==="dashboard"||host==="panel")?"chat-operator":"chat-visitor-a";
  const base={chat_id:chat,agent_id:41,thread_id:chat}; let revision=0;
  const thinking=page.getByRole("status",{name:"Thinking",exact:true});
  const animatedThinking=async()=>{
   await expect(thinking).toBeVisible();
   expect(await thinking.locator(".chat-thinking-dots > span").first().evaluate(el=>getComputedStyle(el).animationName)).toContain("chat-thinking-dot");
  };
  const animatedTool=async()=>{
   await expect(page.locator(".chat-tool-copy-running")).toHaveCount(1);
   expect(await page.locator(".chat-tool-copy-running").evaluate(el=>getComputedStyle(el).animationName)).toContain("chat-tool-copy-breathe");
  };
  for(let response=0;response<3;response++) {
   const id=response*2+1, start=new Date().toISOString(), call=`request-${response}`;
   await request.post("/append-message",{data:{id,conversation_id:chat,role:"user",content:`Request ${response+1}`,created_at:start}});
   const progress=(phase:string,extra={})=>request.post("/emit",{data:{...base,response_progress:{phase,run_id:call,revision:++revision,after_message_id:id,started_at:start,...extra}}});
   await progress("thinking"); await animatedThinking();
   await page.reload(); await expect(page.getByTitle("Live")).toBeVisible(); await animatedThinking();
   await progress("preparing_tool",{tool_name:"code_repos_list",call_id:call,tool_started_at:new Date().toISOString()});
   await animatedTool(); await expect(thinking).toHaveCount(0);
   await page.reload(); await expect(page.getByTitle("Live")).toBeVisible(); await animatedTool();
   const tool={id:900+response,chat_id:chat,agent_id:41,thread_id:chat,call_id:call,name:"code_repos_list",reason:"Listing repositories",status:"running",started_at:new Date().toISOString(),revision:1};
   await request.post("/emit",{data:{...base,tool_activity:tool}}); await progress("running"); await animatedTool();
   await page.reload(); await expect(page.getByTitle("Live")).toBeVisible(); await animatedTool();
   await request.post("/emit",{data:{...base,tool_activity:{...tool,status:"completed",ended_at:new Date().toISOString(),revision:2}}});
   await progress("continuing"); await animatedThinking();
   await page.reload(); await expect(page.getByTitle("Live")).toBeVisible(); await animatedThinking();
   await request.post("/append-message",{data:{id:id+1,conversation_id:chat,role:"agent",agent_id:41,content:`Reply ${response+1}`,created_at:new Date().toISOString()}});
   await progress("idle"); await expect(thinking).toHaveCount(0);
   await expect(page.locator(".chat-tool-copy-running")).toHaveCount(0);
  }
 });
}
