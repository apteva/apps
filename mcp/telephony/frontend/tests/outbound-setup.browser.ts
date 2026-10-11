import {test,expect} from "@playwright/test";
test("Telnyx automatic setup selects an existing profile without asking for credentials",async({page})=>{
 await page.goto("/?provider=telnyx");
 await page.getByLabel("Carrier transport").selectOption("sip_direct");
 await expect(page.getByLabel("Setup",{exact:true})).toHaveValue("automatic");
 await expect(page.getByLabel("SIP password",{exact:true})).toHaveCount(0);
 await page.getByLabel("Outbound calling profile").selectOption("profile-1");
 await page.getByRole("button",{name:"Set up and enable SIP"}).click();
 await expect(page.locator("#closed")).toHaveText("closed");
 expect(JSON.parse(await page.locator("#result").innerText())).toEqual({carrier_connection_id:10,phone_number:"+33123456780",transport:"sip_direct",provision:true,outbound_profile_id:"profile-1"});
});
test("new Telnyx calling profile carries the user's countries and budget",async({page})=>{
 await page.goto("/?provider=telnyx");
 await page.getByLabel("Carrier transport").selectOption("sip_direct");
 await page.getByLabel("Outbound calling profile").selectOption("new");
 await page.getByLabel("Allowed destination countries").fill("fr, es");
 await page.getByLabel("Daily spending limit").fill("20.00");
 await page.getByRole("button",{name:"Set up and enable SIP"}).click();
 const result=JSON.parse(await page.locator("#result").innerText());
 expect(result.profile_policy).toEqual({destinations:["FR","ES"],daily_spend_limit:"20.00"});
});
test("Twilio setup is automatic while the existing-resource form remains usable",async({page})=>{
 await page.goto("/?provider=twilio");
 await page.getByLabel("Carrier transport").selectOption("sip_direct");
 await expect(page.getByLabel("Outbound calling profile")).toHaveCount(0);
 await page.getByRole("button",{name:"Set up and enable SIP"}).click();
 expect(JSON.parse(await page.locator("#result").innerText()).provision).toBe(true);
 await page.goto("/?provider=twilio");
 await page.getByLabel("Carrier transport").selectOption("sip_direct");
 await page.getByLabel("Setup",{exact:true}).selectOption("existing");
 await page.getByLabel("Trunk SID").fill("TKexisting");
 await page.getByLabel("Termination hostname").fill("existing.pstn.twilio.com");
 await page.getByLabel("SIP username",{exact:true}).fill("user");
 await page.getByLabel("SIP password",{exact:true}).fill("password");
 await page.getByRole("button",{name:"Apply to new calls"}).click();
 const result=JSON.parse(await page.locator("#result").innerText());
 expect(result.provision).toBeUndefined();expect(result.config.resource_id).toBe("TKexisting");expect(result.password).toBe("password");
});
