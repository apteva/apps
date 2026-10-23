import { expect,test } from "@playwright/test";

for(const host of ["dashboard","external","package"] as const) {
 test(`${host}: live voice revisions replace prefixes, group adjacent fragments and survive reload without shutdown duplicates`,async({page,request})=>{
  await request.post("/reset");await page.goto(`/?host=${host}`);await expect(page.locator("textarea")).toBeVisible();
  const conversation_id=host==="dashboard"?"chat-operator":"chat-visitor-a";
  const segment=(id:number,content:string,start:number,end:number,role="agent",status="application_grouped",revision=1)=>({id,conversation_id,role,agent_id:41,content,revision,components:[],created_at:new Date(Date.UTC(2026,9,23,9,6)+start).toISOString(),metadata:{source:"voice",voice_session_id:"voice-replay",transcript:{id:`segment-${id}`,generation:1,start_ms:start,end_ms:end,status}}});
  const publish=(message:unknown)=>request.post("/append-message",{data:message});
  await publish(segment(101,"I'm",100,200,"user"));await expect(page.getByText("I'm",{exact:true})).toBeVisible();
  await publish(segment(101,"I'm good and you",100,600,"user","application_grouped",2));
  await expect(page.getByText("I'm",{exact:true})).toHaveCount(0);await expect(page.getByText("I'm good and you",{exact:true})).toHaveCount(1);
  await publish(segment(102,"I'm doing",700,900));await publish(segment(103,"good.",900,1100));
  await expect(page.getByText("I'm doing good.",{exact:true})).toHaveCount(1);
  await publish(segment(103,"good. What's on your mind?",900,2100,"agent","application_grouped",3));
  await expect(page.getByText("I'm doing good.",{exact:true})).toHaveCount(0);
  await expect(page.getByText("I'm doing good. What's on your mind?",{exact:true})).toHaveCount(1);
  for(const status of ["interrupted","interrupted"]) {
   await publish(segment(101,"I'm good and you",100,600,"user",status,4));
   await publish(segment(102,"I'm doing",700,900,"agent",status,4));
   await publish(segment(103,"good. What's on your mind?",900,2100,"agent",status,4));
  }
  await page.reload();await expect(page.locator("textarea")).toBeVisible();
  await expect(page.getByText("I'm good and you",{exact:true})).toHaveCount(1);
  await expect(page.getByText("I'm doing good. What's on your mind?",{exact:true})).toHaveCount(1);
  await publish(segment(104,"Do not display blocked speech",3000,3500,"agent","blocked",5));
  await page.reload();await expect(page.locator("textarea")).toBeVisible();
  await expect(page.getByText("Do not display blocked speech",{exact:true})).toHaveCount(0);
 });
}
