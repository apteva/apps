import {flowThemes, applyFlowTheme, expectBorderContrast} from "./flow-themes";
import { test, expect } from "@playwright/test";
const step=(key:string,name:string,depends_on:string[],state:string,agent:number)=>({id:key,run_id:"run-live",key,state,progress:state==="completed"?100:0,output:"",error:"",decision:"",updated_at:new Date().toISOString(),executor:{kind:"agent",agent_id:agent},definition:{key,name,depends_on,kind:"work",role:"worker",instructions:"Follow this step",expected_output:"Evidence"}});
const run=(done=false)=>({id:"run-live",process_id:"weather",version:1,workflow:true,state:done?"completed":"running",progress:done?100:0,created_at:new Date().toISOString(),steps:[step("fetch_weather","Fetch current weather",[],done?"completed":"running",7),step("post_conversations","Post alert in Conversations",["fetch_weather"],done?"completed":"pending",8),step("send_pushover","Send Pushover notification",["post_conversations"],done?"completed":"pending",9)]});
test("SSE refreshes graphical multi-agent progress, batches events and preserves selection",async({page,request})=>{
 await request.post('/fixture/runs',{data:[run()]});
 await page.goto('/?live');
 await page.getByRole('button',{name:'Hourly weather alerts',exact:true}).click();
 await page.getByRole('button',{name:'Runs',exact:true}).click();
 await page.locator('.run-list > button').first().click();
 await expect(page.locator('.pf-execution[data-state="running"]')).toHaveCount(1);
 await expect(page.getByRole('button',{name:'Step 1: Fetch current weather',exact:true})).toContainText('Weather agent');
 await expect(page.getByRole('button',{name:'Step 2: Post alert in Conversations',exact:true})).toContainText('Agent 8');
 await page.getByRole('button',{name:'Step 2: Post alert in Conversations',exact:true}).click();
 await page.waitForTimeout(350);
 const before=(await (await request.get('/fixture/stats')).json()).reads;
 await request.post('/fixture/runs',{data:[run(true)]});
 const event=(seq:number,install=77,project='test')=>({app:'processes',project_id:project,install_id:install,seq,topic:'task.state_changed',data:{run_id:'run-live',process_id:'weather'}});
 await request.post('/fixture/events',{data:event(1,88)});
 await request.post('/fixture/events',{data:event(2,77,'other')});
 await page.waitForTimeout(300);
 expect((await (await request.get('/fixture/stats')).json()).reads).toBe(before);
 for(let i=3;i<8;i++) await request.post('/fixture/events',{data:event(i)});
 await expect(page.locator('.pf-execution[data-state="completed"]')).toHaveCount(3,{timeout:2500});
 await expect(page.getByRole('complementary',{name:'Step details'})).toContainText('Post alert in Conversations');
 expect((await (await request.get('/fixture/stats')).json()).reads-before).toBe(1);
 await request.post('/fixture/events',{data:event(7)});
 await page.waitForTimeout(300);
 expect((await (await request.get('/fixture/stats')).json()).reads-before).toBe(1);
});
test("reconnect reconciles a missed completion without waiting for polling",async({page,request})=>{
 await request.post('/fixture/runs',{data:[run()]});await page.goto('/?live');
 await page.getByRole('button',{name:'Hourly weather alerts',exact:true}).click();await page.getByRole('button',{name:'Runs',exact:true}).click();
 await page.locator('.run-list > button').first().click();
 await expect(page.locator('.pf-execution[data-state="running"]')).toHaveCount(1);
 await page.waitForTimeout(300);
 await request.post('/fixture/runs',{data:[run(true)]});
 await request.post('/fixture/disconnect');
 await expect(page.locator('.pf-execution[data-state="completed"]')).toHaveCount(3,{timeout:8000});
});
test("procedure edits survive background events",async({page,request})=>{
 await page.goto('/?live');await page.getByRole('button',{name:'Hourly weather alerts',exact:true}).click();
 await page.getByRole('button',{name:'Edit procedure',exact:true}).click();
 await page.getByRole('button',{name:'Step 1: Fetch current weather',exact:true}).click();
 await page.getByLabel('Step name',{exact:true}).fill('Unsaved Barcelona edit');
 await request.post('/fixture/events',{data:{app:'processes',project_id:'test',install_id:77,seq:100,topic:'process.updated',data:{process_id:'weather'}}});
 await page.waitForTimeout(400);await expect(page.getByLabel('Step name',{exact:true})).toHaveValue('Unsaved Barcelona edit');
});

test("flow labels keep human executors distinct and wrap long agent names", async ({ page, request }) => {
 const data = run();
 data.steps[1].executor = { kind: "human" } as any;
 await request.post('/fixture/runs', { data: [data] });
 await page.goto('/?live&long_agent');
 await page.getByRole('button', { name: 'Hourly weather alerts', exact: true }).click();
 await page.getByRole('button', { name: 'Runs', exact: true }).click();
 await page.locator('.run-list > button').first().click();
 const first = page.getByRole('button', { name: 'Step 1: Fetch current weather', exact: true });
 await expect(first).toContainText('Barcelona weather operations coordinator');
 await expect(page.getByRole('button', { name: 'Step 2: Post alert in Conversations', exact: true }).locator('.pf-executor')).toHaveText('0% · Human');
 await expect(page.getByRole('button', { name: 'Step 3: Send Pushover notification', exact: true })).toContainText('Agent 9');
 const fits = await first.locator('.pf-executor').evaluate(el => el.scrollWidth <= el.clientWidth);
 expect(fits).toBe(true);
});


test("detail flow shares themed statuses, borders and selection", async ({page,request}) => {
 const data=run();
 data.steps[0].state='completed'; data.steps[1].state='running'; data.steps[2].state='pending';
 await request.post('/fixture/runs',{data:[data]});
 await page.goto('/?live');
 await page.getByRole('button',{name:'Hourly weather alerts',exact:true}).click();
 await page.getByRole('button',{name:'Runs',exact:true}).click();
 await page.locator('.run-list > button').first().click();
 await expect(page.locator('.pf-step[data-state="running"]')).toHaveCount(1);
 await expect(page.locator('.pf-step[data-state="completed"]')).toHaveCount(1);
 for(const theme of flowThemes) {
  await applyFlowTheme(page,theme);
  await expectBorderContrast(page,'.pf-step[data-state="pending"]');
  await expect(page.locator('.pf-step').first()).toHaveCSS('border-top-left-radius',theme.radius);
  await page.getByRole('region',{name:'Process flow',exact:true}).screenshot({path:`/private/tmp/processes-detail-${theme.name}.png`});
 }
 await page.getByRole('button',{name:'Step 2: Post alert in Conversations',exact:true}).click();
 await expect(page.locator('.pf-step[data-state="running"]')).toHaveClass(/is-selected/);
 await expect(page.getByRole('complementary',{name:'Step details'})).toBeVisible();
});
