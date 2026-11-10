import {expect,test} from "@playwright/test";
const card=(blocking=false)=>({id:501,revision:1,conversation_id:"chat-operator",role:"agent",agent_id:41,phase:"final",content:"I will use concise and continue.",component_kind:"interactive",created_at:new Date().toISOString(),components:[{id:"tone",version:1,app:"conversations",name:"question-card",props:{title:"Which tone?"},interaction:{blocking,inbox:true,status:"pending",fields:[{id:"tone",label:"Tone",type:"choice",required:true,allow_custom:true,options:[{id:"__custom",label:"Standard"},{id:"detailed",label:"Detailed"}]}],actions:[{id:"submit",label:"Submit",style:"primary"},{id:"skip",label:"Skip",cancel:true}]}}]});
for(const surface of ["widget","panel"]){
 test(`${surface}: optional question continues, answers persist and clear the shared inbox`,async({page,request})=>{
  await request.post("/reset");
  if(surface==="panel")await request.post("/seed-panel",{data:[{id:"chat-operator",project_id:"project",lead_agent_id:41,title:"Support chat",kind:"direct",audience:"operator",origin:"web",created_at:"",updated_at:""}]});
  await request.post("/append-message",{data:card()});
  await page.goto(surface==="panel"?"/?host=dashboard&surface=panel":"/?host=dashboard");
  const component=page.locator('[data-component-id="tone"]');
  await expect(component.getByText("Optional — work can continue",{exact:true})).toBeVisible();
  await expect(page.locator(".chat-thinking-dots")).toHaveCount(0);
  await expect(component.getByRole("combobox")).toHaveCount(0);
  await expect(component.getByRole("radio")).toHaveCount(0);
  await expect(component.getByRole("button",{name:"Detailed"})).toBeVisible();
  for(const width of [390,1280]){
   await page.setViewportSize({width,height:800});
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await component.getByRole("button",{name:"Standard",exact:true}).click();
  await expect(component.getByText("Answered",{exact:true})).toBeVisible();
  await expect(component.getByText("Tone: Standard",{exact:true})).toBeVisible();
  await page.reload();
  await expect(component.getByText("Tone: Standard",{exact:true})).toBeVisible();
  await page.goto("/?host=dashboard&surface=inbox");
  await expect(page.getByText("Which tone?",{exact:true})).toHaveCount(0);
 });
}
test("the inbox displays clickable choices and submits the selected answer",async({page,request})=>{
 await request.post("/reset");
 await request.post("/append-message",{data:card()});
 await page.setViewportSize({width:1280,height:900});
 await page.goto("/?host=dashboard&surface=inbox");
 const item=page.locator("article").filter({hasText:"Which tone?"});
 await item.getByRole("button",{name:"Review",exact:true}).click();
 const dialog=page.getByRole("dialog");
 const standard=dialog.getByRole("button",{name:"Standard"});
 const detailed=dialog.getByRole("button",{name:"Detailed"});
 await expect(standard).toBeVisible();
 await expect(detailed).toBeVisible();
 await expect(dialog.getByRole("radio")).toHaveCount(0);
 await expect(dialog.getByRole("button",{name:"Submit",exact:true})).toHaveCount(0);
 await expect(dialog.getByRole("button",{name:"Skip",exact:true})).toBeVisible();
 await detailed.press("Enter");
 await expect(item).toHaveCount(0);
 const calls=(await (await request.get("/requests")).json()).filter((r:any)=>r.path.endsWith("/message-action"));
 expect(calls).toHaveLength(1);
 expect(calls[0].body.values).toEqual({tone:"detailed"});
});

test("a custom answer is sent with Enter",async({page,request})=>{
 await request.post("/reset");
 await request.post("/append-message",{data:card()});
 await page.setViewportSize({width:390,height:844});
 await page.goto("/?host=dashboard&surface=inbox");
 const item=page.locator("article").filter({hasText:"Which tone?"});
 await item.getByRole("button",{name:"Review",exact:true}).click();
 const dialog=page.getByRole("dialog");
 await dialog.getByRole("button",{name:"Your own answer",exact:true}).click();
 const answer=dialog.getByRole("textbox",{name:"Your own answer"});
 await expect(answer).toBeFocused();
 await answer.press("Enter");
 await answer.fill("Something in between");
 expect((await (await request.get("/requests")).json()).filter((r:any)=>r.path.endsWith("/message-action"))).toHaveLength(0);
 await answer.press("Enter");
 await expect(item).toHaveCount(0);
 const calls=(await (await request.get("/requests")).json()).filter((r:any)=>r.path.endsWith("/message-action"));
 expect(calls).toHaveLength(1);
 expect(calls[0].body.values).toEqual({tone:"Something in between"});
});

test("inbox uses the same blocking widget; skipping bypasses required answers and updates the chat",async({page,request})=>{
 const form=card(true);
 form.components[0].name="form-card";
 await request.post("/reset");await request.post("/append-message",{data:form});
 await page.setViewportSize({width:390,height:844});
 await page.goto("/?host=dashboard&surface=inbox");
 const item=page.locator("article").filter({hasText:"Which tone?"});
 await expect(item.getByText("Form",{exact:true})).toBeVisible();
 await expect(item.getByText("Answer needed",{exact:true})).toBeVisible();
 await expect(item.getByRole("button",{name:"Dismiss",exact:true})).toHaveCount(0);
 await item.getByRole("button",{name:"Review",exact:true}).click();
 const component=page.locator('[data-component-id="tone"]');
 await expect(component.getByText("Waiting for your answer",{exact:true})).toBeVisible();
 await component.getByRole("button",{name:"Skip",exact:true}).click();
 await expect(page.getByText("Which tone?",{exact:true})).toHaveCount(0);
 await page.goto("/?host=dashboard");
 await expect(component.getByText("Skipped",{exact:true})).toBeVisible();
 await expect(page.locator(".chat-thinking-dots")).toHaveCount(0);
});
