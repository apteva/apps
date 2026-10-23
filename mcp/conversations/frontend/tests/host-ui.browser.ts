import { expect, test, type Page } from "@playwright/test";

const conversation = {id:"host-chat",project_id:"project",lead_agent_id:41,lead_agent_name:"Assistant",title:"Host presentation",kind:"direct",origin:"web",audience:"operator",created_at:"2026-10-23T07:00:00Z",updated_at:"2026-10-23T07:00:00Z"};

// The platform suite verifies foreground ownership and responsive policy. This
// contract fixture checks the released Conversations bundles actually request
// and release that policy at the right app-owned view boundaries.
async function installHost(page: Page, supported = true) {
  await page.addInitScript(({supported}) => {
    const state = {active:0,events:[] as string[]};
    (window as any).HOST_UI_STATE = state;
    const compact = matchMedia("(max-width: 767px)");
    const update = () => {
      const button = document.querySelector<HTMLButtonElement>("#host-launcher");
      if (button) button.hidden = compact.matches && state.active > 0;
    };
    (window as any).CONVERSATIONS_HOST = {
      capabilities:{ui:{chatAssistantLauncher:supported}},
      ui:{request(settings: {chatAssistant:{launcher:string}}) {
        state.events.push(settings.chatAssistant.launcher);
        state.active++;update();let released=false;
        return () => {if(released)return;released=true;state.active--;state.events.push("release");update();};
      }},
    };
    compact.addEventListener("change", update);
    document.addEventListener("DOMContentLoaded", () => {
      const button=document.createElement("button");button.id="host-launcher";button.textContent="Host assistant";
      button.style.cssText="position:fixed;right:20px;bottom:20px;z-index:9999";
      document.body.append(button);update();
    });
  }, {supported});
}

test.beforeEach(async ({request}) => {
  await request.post("/reset");
  await request.post("/seed-panel",{data:[conversation]});
});

test("mobile panel hides only for an open conversation and restores the launcher on Back without losing the draft", async ({page}) => {
  await page.setViewportSize({width:390,height:780});await installHost(page);
  await page.goto("/?host=dashboard&surface=panel");
  const launcher=page.locator("#host-launcher");
  const row=page.getByRole("button",{name:/^Host presentation/});
  await expect(row).toBeVisible();await expect(launcher).toBeVisible();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual([]);
  await row.click();await expect(page.locator("textarea")).toBeVisible();await expect(launcher).toBeHidden();
  await page.locator("textarea").fill("Preserve my mobile draft");
  await page.getByRole("button",{name:"Back to conversations",exact:true}).click();
  await expect(row).toBeVisible();await expect(launcher).toBeVisible();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual(["hidden_on_compact","release"]);
  await row.click();await expect(launcher).toBeHidden();
  await expect(page.locator("textarea")).toHaveValue("Preserve my mobile draft");
  await page.getByRole("button",{name:"Back to conversations",exact:true}).click();
  await page.getByRole("button",{name:/Inbox/}).first().click();
  await expect(launcher).toBeVisible();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.active)).toBe(0);
});

for (const host of ["dashboard","external","package"] as const) {
  test(`${host}: shared single chat requests compact-only suppression and leaves desktop usable`, async ({page}) => {
    await installHost(page);await page.setViewportSize({width:390,height:780});
    await page.goto(`/?host=${host}`);await expect(page.locator("textarea")).toBeVisible();
    await expect(page.locator("#host-launcher")).toBeHidden();
    expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual(["hidden_on_compact"]);
    await page.locator("textarea").fill("Keep this draft across resize");
    await page.setViewportSize({width:1280,height:900});
    await expect(page.locator("#host-launcher")).toBeVisible();
    await expect(page.locator("textarea")).toHaveValue("Keep this draft across resize");
    expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual(["hidden_on_compact"]);
  });
}

test("browser widget forwards the host to its open conversation", async ({page}) => {
  await installHost(page);await page.setViewportSize({width:390,height:780});
  await page.goto("/?host=dashboard&surface=widget-browser");
  await expect(page.locator("textarea")).toBeVisible();await expect(page.locator("#host-launcher")).toBeHidden();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual(["hidden_on_compact"]);
});

test("desktop panel remains visible and mobile list does not inherit a saved selection's request", async ({page}) => {
  await installHost(page);await page.setViewportSize({width:1280,height:900});
  await page.goto("/?host=dashboard&surface=panel");
  await expect(page.locator("textarea")).toBeVisible();await expect(page.locator("#host-launcher")).toBeVisible();
  await page.setViewportSize({width:390,height:780});
  await expect(page.getByRole("button",{name:/^Host presentation/})).toBeVisible();
  await expect(page.locator("textarea")).toHaveCount(0);await expect(page.locator("#host-launcher")).toBeVisible();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.active)).toBe(0);
});

test("unsupported host does not receive requests while chat remains usable", async ({page}) => {
  await installHost(page,false);await page.setViewportSize({width:390,height:780});
  await page.goto("/?host=dashboard");await expect(page.locator("textarea")).toBeVisible();
  await expect(page.locator("#host-launcher")).toBeVisible();
  expect(await page.evaluate(()=>(window as any).HOST_UI_STATE.events)).toEqual([]);
});
