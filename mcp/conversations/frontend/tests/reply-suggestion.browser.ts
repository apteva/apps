import {test,expect} from "@playwright/test";
const text="Run the dry-run validation next.";
const message=(chat:string,id=2,value=text)=>({id,revision:1,conversation_id:chat,role:"agent",agent_id:41,phase:"final",content:"The plan is ready.",created_at:new Date().toISOString(),components:[{id:"next",version:1,app:"conversations",name:"reply-suggestions",props:{items:[{id:"next",text:value}]}}]});
for(const mobile of [false,true]){
 test.describe(mobile?"touch":"desktop",()=>{
  test.use(mobile?{viewport:{width:390,height:800},isMobile:true,hasTouch:true}:{viewport:{width:1280,height:800}});
  for(const host of ["dashboard","external","package"]){
   test(`${host}: one ghost suggestion prepares a draft without sending`,async({page,request})=>{
    await request.post("/reset");const chat=host==="dashboard"?"chat-operator":"chat-visitor-a";
    await request.post("/append-message",{data:message(chat)});await page.goto(`/?host=${host}`);
    const ghost=page.getByRole("button",{name:"Use suggested reply",exact:true});const input=page.locator("textarea");
    await expect(ghost).toBeVisible();await expect(ghost).toContainText(text);await expect(input).toHaveValue("");await expect(page.getByRole("button",{name:"Send",exact:true})).toBeDisabled();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);if(mobile)expect(await page.evaluate(()=>innerWidth)).toBe(390);
    const box=await page.locator(".chat-composer-box").boundingBox(),hint=await ghost.boundingBox();expect(hint!.x).toBeGreaterThanOrEqual(box!.x);expect(hint!.x+hint!.width).toBeLessThanOrEqual(box!.x+box!.width);
    await page.screenshot({path:test.info().outputPath(`ghost-${mobile?"mobile":"desktop"}.png`)});
    await expect(page.locator(".chat-transcript details")).toHaveCount(0);
    if(mobile)await ghost.tap();else{
     await input.focus();await input.press("Enter");await expect(input).toHaveValue("");await input.dispatchEvent("keydown",{key:"Tab",isComposing:true});await expect(input).toHaveValue("");await input.press("Shift+Tab");await expect(ghost).toBeVisible();await input.focus();await input.press("Tab");
    }
    await expect(input).toHaveValue(text);await expect(input).toBeFocused();await expect(ghost).toHaveCount(0);
    const calls=await (await request.get("/requests")).json();expect(calls.filter((r:any)=>r.method==="POST"&&(/\/messages$/.test(r.path)||/message-action$/.test(r.path)))).toHaveLength(0);
    await page.getByRole("button",{name:"Send",exact:true}).click();await expect(page.getByText(text,{exact:true})).toBeVisible();
   });
  }
 });
}
test("dismissal, custom typing and accepted drafts survive operator reload; a newer reply resets the suggestion",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:message("chat-operator")});await page.goto("/?host=dashboard");const input=page.locator("textarea"),ghost=page.getByRole("button",{name:"Use suggested reply",exact:true});
 await expect(ghost).toBeVisible();await input.focus();await input.press("Escape");await expect(ghost).toHaveCount(0);await page.reload();await expect(ghost).toHaveCount(0);
 const history=page.getByRole("button",{name:"History",exact:true});await history.focus();await request.post("/append-message",{data:message("chat-operator",3,"Review the validation report.")});await expect(ghost).toBeVisible();await expect(history).toBeFocused();
 await input.fill("My own request");await expect(ghost).toHaveCount(0);await input.fill("");await expect(ghost).toHaveCount(0);await page.reload();await expect(ghost).toHaveCount(0);
 await request.post("/append-message",{data:message("chat-operator",4)});await expect(ghost).toBeVisible();await input.focus();await input.press("Tab");await page.reload();await expect(input).toHaveValue(text);await expect(ghost).toHaveCount(0);
});
test("existing drafts/attachments, active responses, queued turns and newer messages suppress stale suggestions",async({page,request})=>{
 await request.post("/reset");await page.goto("/?host=dashboard");await page.getByTitle("Live").waitFor();const input=page.locator("textarea"),ghost=page.getByRole("button",{name:"Use suggested reply",exact:true});
 await input.fill("Preserve my draft");await request.post("/append-message",{data:message("chat-operator")});await expect(input).toHaveValue("Preserve my draft");await expect(ghost).toHaveCount(0);
 await input.fill("");await page.locator('input[type="file"]').setInputFiles({name:"notes.txt",mimeType:"text/plain",buffer:Buffer.from("notes")});await expect(page.getByText("notes.txt",{exact:true})).toBeVisible();await expect(ghost).toHaveCount(0);await expect(input).toHaveValue("");
 await expect.poll(()=>page.evaluate(()=>Object.keys(sessionStorage).some(key=>key.endsWith(":attachments")&&JSON.parse(sessionStorage.getItem(key)||"[]").some((i:any)=>i.name==="notes.txt"&&i.attachment)))).toBe(true);
 await page.reload();await expect(page.getByText("notes.txt",{exact:true})).toBeVisible();await expect(ghost).toHaveCount(0);
});
test("active or queued work and later turns prevent an older prompt from reappearing",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:message("chat-operator")});await page.goto("/?host=dashboard");const ghost=page.getByRole("button",{name:"Use suggested reply",exact:true});await expect(ghost).toBeVisible();
 const frame={chat_id:"chat-operator",agent_id:41,thread_id:"chat-operator",response_progress:{phase:"thinking",revision:1,run_id:"new-run",after_message_id:2,started_at:new Date().toISOString()}};
 await request.post("/emit",{data:frame});await expect(ghost).toHaveCount(0);await request.post("/emit",{data:{...frame,response_progress:{...frame.response_progress,phase:"idle",revision:2}}});await expect(ghost).toBeVisible();
 await request.post("/append-message",{data:{...message("chat-operator",3),role:"user",components:[],content:"A queued follow-up",queue_state:"queued"}});await expect(ghost).toHaveCount(0);
 await request.post("/append-message",{data:message("chat-operator",4)});await expect(ghost).toHaveCount(0);
 await request.post("/append-message",{data:{...message("chat-operator",5),components:[]}});await expect(ghost).toHaveCount(0);await page.reload();await expect(ghost).toHaveCount(0);
});
test("embedded hosts can disable agent suggestions",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:message("chat-visitor-a")});await page.addInitScript(()=>{(window as any).REPLY_SUGGESTIONS=false});await page.goto("/?host=external");await expect(page.getByText("The plan is ready.",{exact:true})).toBeVisible();await expect(page.getByRole("button",{name:"Use suggested reply",exact:true})).toHaveCount(0);
});

test("a pending approval or uncertain send cannot acquire a suggested next command",async({page,request})=>{
 await request.post("/reset");await request.post("/append-message",{data:{...message("chat-operator",1),component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approve first",status:"pending",actions:[]}}]}});await request.post("/append-message",{data:message("chat-operator")});await page.goto("/?host=dashboard");await expect(page.getByText("The plan is ready.",{exact:true}).last()).toBeVisible();await expect(page.getByRole("button",{name:"Use suggested reply",exact:true})).toHaveCount(0);
 await request.post("/reset");await request.post("/append-message",{data:message("chat-operator")});await page.addInitScript(()=>sessionStorage.setItem("conversations:draft:project:7:dashboard:chat-operator:pending",JSON.stringify({content:"Uncertain previous message",client_message_id:"retry-me"})));await page.reload();await expect(page.getByText("The plan is ready.",{exact:true})).toBeVisible();await expect(page.getByRole("button",{name:"Use suggested reply",exact:true})).toHaveCount(0);
});
test("switching conversations does not offer another conversation's suggestion",async({page,request})=>{
 await request.post("/reset");await request.post("/seed-panel",{data:[{id:"chat-operator",project_id:"project",lead_agent_id:41,title:"First conversation",kind:"direct",audience:"operator",origin:"web",created_at:"",updated_at:""},{id:"other-conversation",project_id:"project",lead_agent_id:41,title:"Second conversation",kind:"direct",audience:"operator",origin:"web",created_at:"",updated_at:""}]});await request.post("/append-message",{data:message("chat-operator")});await page.goto("/?host=dashboard&surface=panel");const ghost=page.getByRole("button",{name:"Use suggested reply",exact:true});await expect(ghost).toBeVisible();await page.locator('button[data-conversation-id="other-conversation"]').click();await expect(ghost).toHaveCount(0);await expect(page.locator("textarea")).toHaveValue("");await page.locator('button[data-conversation-id="chat-operator"]').click();await expect(ghost).toBeVisible();await page.locator("textarea").focus();await page.keyboard.press("Tab");await expect(page.locator("textarea")).toHaveValue(text);await page.locator('button[data-conversation-id="other-conversation"]').click();await expect(page.locator("textarea")).toHaveValue("");
});
