import { test, expect } from "@playwright/test";

test.beforeEach(async ({ request }) => { await request.get("/__fixture?mode=full"); await request.get("/__delay?ms=0"); });
test("overview shows complete statistics, filters, cancellations and a full-width trend", async ({ page }) => {
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 await page.goto("/?widget=overview");const widget=page.getByRole("region",{name:"Tests overview"});
 await expect(widget.locator(".tw-stat").filter({hasText:"Runs"}).first().locator("strong")).toHaveText("131");
 await expect(widget.getByText("126 of 129 completed")).toBeVisible();
 await expect(widget.getByText("1 cancelled · excluded from pass rate")).toBeVisible();
 await expect(widget.getByText("1 running · 1 queued")).toBeVisible();
 await expect(widget.locator(".tw-row")).toHaveCount(3);
 await expect(widget.getByText("Private project result")).toHaveCount(0);
 await widget.getByLabel("Environment",{exact:true}).selectOption("stage");
 await expect(widget.getByText("125 of 128 completed")).toBeVisible();
 await expect(widget.getByText("0 running · 0 queued")).toBeVisible();
 await widget.getByLabel("Suite",{exact:true}).selectOption({label:"Browser checkout"});
 await expect(widget.getByText("125 of 127 completed")).toBeVisible();
 await page.evaluate(()=>window.renderWidgets({widgetSize:"full",widgetSettings:{window:"7d"}}));
 await expect(widget.getByLabel("Time window")).toHaveValue("168");
 await expect(widget.getByRole("img",{name:"Daily test runs by status"})).toBeVisible();
 expect(errors).toEqual([]);
});
test("failures group repeated checks, show owned evidence and deep-link to the saved run", async ({ page, request }) => {
 const ids=await (await request.get("/__fixture?mode=full")).json();
 await page.goto("/?widget=failures");const widget=page.getByRole("region",{name:"Recent failures"});
 await expect(widget.getByText("2 occurrences")).toBeVisible();
 await expect(widget.getByLabel("Screenshot available")).toBeVisible();
 await expect(widget.getByText("Saved total did not match the expected value")).toBeVisible();
 await expect(widget.getByText("Environment unavailable")).toBeVisible();
 await expect(widget.getByText("2 failure groups · past 24 hours")).toBeVisible();
 await widget.getByLabel("Failure grouping").selectOption("false");
 await expect(widget.getByText("3 failures · past 24 hours")).toBeVisible();
 const link=widget.getByRole("link",{name:`Open failed run ${ids.failure}: Save sale`});
 await expect(link).toHaveAttribute("href",`/apps/tests/page?project_id=project-a&install_id=6&suite_id=${ids.browser_suite}&run_id=${ids.failure}`);
 await link.click();
 await expect(page.getByRole("heading",{name:`Run #${ids.failure} · stage`})).toBeVisible();
 await expect(page.getByText("Saved total did not match the expected value")).toBeVisible();
 await expect(page.getByAltText("Masked failure screenshot for save")).toBeVisible();
});
test("archived suite links open read-only history", async ({ page, request }) => {
 const ids=await (await request.get("/__fixture?mode=full")).json();
 await page.goto("/?widget=failures");
 await page.getByRole("link",{name:`Open failed run ${ids.archived}: Suite execution`}).click();
 await expect(page.getByRole("heading",{name:`Run #${ids.archived} · stage`})).toBeVisible();
 await expect(page.getByText("Archived suite · saved history is available.")).toBeVisible();
 await expect(page.getByRole("button",{name:"Run suite",exact:true})).toBeDisabled();
 await expect(page.getByRole("button",{name:"Edit suite",exact:true})).toBeDisabled();
});
test("empty, unavailable and missing-project states are honest and recoverable", async ({ page, request }) => {
 await request.get("/__fixture?mode=empty");await page.goto("/");
 await expect(page.getByText("No tests run yet",{exact:true})).toBeVisible();
 await expect(page.getByText("No failures in this period",{exact:true})).toBeVisible();
 await expect(page.locator(".tw-stat").filter({hasText:"Pass rate"}).locator("strong")).toHaveText("—");
 await request.get("/__unavailable?on=1");await page.evaluate(()=>window.renderWidgets({eventRevision:1}));
 await expect(page.getByText("Results unavailable",{exact:true})).toHaveCount(2);
 await request.get("/__unavailable?on=0");await page.getByRole("button",{name:"Retry",exact:true}).first().click();
 await expect(page.getByText("No tests run yet",{exact:true})).toBeVisible();
 const calls:string[]=[];page.on("request",r=>{if(r.url().includes("/dashboard/"))calls.push(r.url())});
 await page.evaluate(()=>window.renderWidgets({projectId:"",eventRevision:2}));
 await expect(page.getByText("Select a project",{exact:true})).toHaveCount(2);expect(calls).toEqual([]);
});
test("host events and polling refresh results; stale requests and project changes do not leak", async ({ page, request }) => {
 await page.clock.install();
 await page.goto("/?widget=overview");const widget=page.getByRole("region",{name:"Tests overview"});
 await expect(widget.getByText("126 of 129 completed")).toBeVisible();
 await request.get("/__delay?environment=prod&ms=400");
 await widget.getByLabel("Environment",{exact:true}).selectOption("prod");
 await widget.getByLabel("Environment",{exact:true}).selectOption("stage");
 await expect(widget.getByText("125 of 128 completed")).toBeVisible();
 await expect.poll(()=>widget.getByLabel("Environment",{exact:true}).inputValue()).toBe("stage");
 await request.get("/__fixture?mode=empty");await page.evaluate(()=>window.renderWidgets({eventRevision:3}));
 await expect(widget.getByText("No tests run yet",{exact:true})).toBeVisible();
 await request.get("/__fixture?mode=full");await page.clock.fastForward(30001);
 await expect(widget.getByText("125 of 128 completed")).toBeVisible();
 await page.evaluate(()=>window.renderWidgets({projectId:"project-b",eventRevision:4}));
 await expect(widget.getByText("Results unavailable",{exact:true})).toBeVisible();
 await expect(widget.getByText("Browser checkout",{exact:true})).toHaveCount(0);
 const denied=await request.get("/api/apps/tests/dashboard/overview?project_id=project-b&install_id=6");expect(denied.status()).toBe(403);
});
test("both widgets fit narrow, half and full host frames in light and dark themes", async ({ page },testInfo) => {
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));await page.goto("/");
 await expect(page.getByText("2 occurrences")).toBeVisible();
 for(const theme of ["light","dark"]){
  await page.evaluate(t=>document.body.classList.toggle("dark",t==="dark"),theme);
  for(const [width,size,containerWidth]of [[1280,"half",0],[1280,"full",0],[390,"half",0],[390,"half",240]]as const){
   await page.setViewportSize({width,height:1000});await page.evaluate(s=>window.renderWidgets({widgetSize:s}),size);
   await expect(page.getByText("2 occurrences")).toBeVisible();
   await page.locator("#widgets").evaluate((e,w)=>{e.style.maxWidth=w?w+"px":"1200px"},containerWidth);
   await expect.poll(()=>page.evaluate(()=>[...document.querySelectorAll(".tests-widget")].every(e=>e.scrollWidth<=e.clientWidth))).toBe(true);
   const geometry=await page.evaluate(()=>({viewport:innerWidth,width:document.documentElement.scrollWidth,items:[...document.querySelectorAll('body,#root,#widgets,.widget-frame,.tests-widget')].map(e=>({tag:e.tagName,id:e.id,class:e.className,width:e.getBoundingClientRect().width,scroll:e.scrollWidth}))}));
   expect(geometry.width<=geometry.viewport,JSON.stringify(geometry)).toBe(true);
   await page.screenshot({path:testInfo.outputPath(`widgets-${theme}-${width}-${size}-${containerWidth || "auto"}.png`),fullPage:true});
  }
 }
 expect(errors).toEqual([]);
});

declare global {interface Window{renderWidgets:(patch?:Record<string,unknown>)=>void}}
