import { expect, test } from "@playwright/test";

test("the shipped Conversations panel creates an operator conversation without an audience selector", async ({page,request}) => {
 await request.post("/reset");
 await page.goto("/?host=dashboard&surface=panel");
 await page.getByRole("button",{name:"New conversation",exact:true}).click();
 const dialog=page.getByRole("dialog",{name:"New conversation"});
 await expect(dialog).toBeVisible();
 await expect(dialog.getByText("Audience",{exact:true})).toHaveCount(0);
 await expect(dialog.getByRole("combobox")).toHaveCount(0);
 await dialog.getByRole("checkbox",{name:"Assistant",exact:true}).check();
 await dialog.getByRole("button",{name:"Create",exact:true}).click();
 await expect.poll(async()=>{
  const calls=await (await request.get("/requests")).json();
  return calls.filter((call:any)=>call.path.endsWith("/chats")&&call.method==="POST").at(-1)?.body;
 }).toMatchObject({agent_ids:[41],lead_agent_id:41,audience:"operator",project_id:"project"});
});
