import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

// Use an installed Playwright and one consistent React/ReactDOM dependency tree.
const playwrightPath = process.env.PLAYWRIGHT_MODULE || "playwright";
const { chromium } = await import(playwrightPath);
const deps =
  process.env.REACT_MODULES || resolve(import.meta.dir, "../node_modules");
const source = resolve(import.meta.dir, "../ui/DomainsPanel.tsx");
let browser: any, server: ReturnType<typeof Bun.serve>, temp: string;
const calls: any[] = [];
const waiting = new Map<string, () => void>();
let delayAlpha = false;
let needsMailMode = false;
let spaceshipMode = false;
let spaceshipSubmitted = false;
let storedValuation = false;
let activeListing = false;
let soldListing = false;
const caps = {
  write_types: ["A", "TXT", "MX", "SRV"],
  delete_types: ["A", "TXT", "MX", "SRV"],
  min_ttl: 600,
  max_ttl: 60000,
};

beforeAll(async () => {
  temp = await mkdtemp(join(tmpdir(), "domains-panel-test-"));
  const entry = join(temp, "entry.tsx");
  await Bun.write(
    entry,
    `import React,{useState} from 'react';import {createRoot} from 'react-dom/client';import Panel from ${JSON.stringify(source)};
 window.__aptevaAppEvents={subscribe:(app,project,fn)=>{const handler=e=>fn(e.detail);window.addEventListener('test-domain-event',handler);return()=>window.removeEventListener('test-domain-event',handler)}};function Test(){const [scope,setScope]=useState({projectId:'project-a',installId:1});React.useEffect(()=>{const f=e=>setScope(e.detail);window.addEventListener('test-scope',f);return()=>window.removeEventListener('test-scope',f)},[]);return <Panel {...scope} appName="domains"/>}createRoot(document.getElementById('root')).render(<Test/>);`,
  );
  const build = await Bun.build({
    entrypoints: [entry],
    target: "browser",
    plugins: [
      {
        name: "react",
        setup(b) {
          b.onResolve({ filter: /^react(?:-dom)?(?:\/.*)?$/ }, (args) => ({
            path: Bun.resolveSync(args.path, deps),
          }));
        },
      },
    ],
  });
  if (!build.success) throw new Error(build.logs.join("\n"));
  const js = await build.outputs[0].text();
  server = Bun.serve({
    port: 0,
    fetch: async (req) => {
      const u = new URL(req.url);
      if (u.pathname === "/bundle.js")
        return new Response(js, {
          headers: { "content-type": "application/javascript" },
        });
      if (u.pathname.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: 1,
              app_slug: spaceshipMode ? "spaceship" : "porkbun",
              name: "Account one",
              status: "active",
              dns_default: true,
              dns_bound: true,
              registrar_default: true,
              registrar_bound: true,
            },
          ],
        });
      if (u.pathname.endsWith("/domains"))
        return Response.json({
          domains: [
            {
              id: 1,
              name: "alpha.example",
              connection_id: 1,
              connection_mode: "pinned",
            },
            {
              id: 2,
              name: "beta.example",
              connection_id: 1,
              connection_mode: "pinned",
            },
          ],
        });
      if (u.pathname.endsWith("/tools/call")) {
        const body: any = await req.json();
        calls.push({
          ...body,
          project: u.searchParams.get("project_id"),
          install: u.searchParams.get("install_id"),
        });
        if (body.tool === "domain_records_list") {
          if (delayAlpha && body.args.domain === "alpha.example")
            await new Promise<void>((r) =>
              waiting.set(body.args._project_id, r),
            );
          return Response.json({
            connection_id: 1,
            namecheap_email_type_required: needsMailMode,
            capabilities: caps,
            records: [
              {
                id: "shared-id",
                name: "www",
                type: "TXT",
                value: `token-for-${body.args.domain}`,
                ttl: 600,
                prio: 0,
              },
            ],
          });
        }
        if(body.tool==="domain_sale_get") return Response.json(activeListing?{provider:"spaceship",state:soldListing?"sold":"on_sale",status:soldListing?"sold":"onSale",listed:!soldListing,snapshot:{state:soldListing?"sold":"on_sale",checked_at:"2026-10-14T10:00:00Z"},raw:{name:body.args.domain,status:soldListing?"sold":"onSale",binPriceEnabled:true,binPrice:{amount:"145",currency:"USD"}}}:{});
        if(body.tool==="domain_sale_history")return Response.json({history:activeListing?[{id:1,state:"on_sale",source:"worker",observed_at:"2026-10-14T10:00:00Z"}]:[],sales:soldListing?[{id:1,sold_at:"2026-10-14T10:10:00Z",sale_price:"145.00",payout:"130.50",currency:"USD"}]:[]});
        if (body.tool === "domain_estimate_start") return Response.json({cached:storedValuation&&!body.args.force_new});
        if (body.tool === "domain_estimate_list") return Response.json({valuations:storedValuation?[{id:1,domain:body.args.domain,status:"succeeded",auction:0,marketplace:145,brokerage:457,created_at:"2026-10-14T09:15:37Z"}]:[]});
        if (spaceshipMode) {
          const purchase = {confirmation_token: "spaceship-token", status: spaceshipSubmitted ? "processing" : "prepared", domain: "fresh.example", provider:"spaceship", connection_id:1, years:2, auto_renew:true, whois_privacy:true, billing_mode:"account_rate", options:{contacts:{registrant:"1ZdMXpapqp9sle5dl8BlppTJXAzf5"}}, expires_at:"2030-01-01T00:00:00Z"};
          if (body.tool === "domain_availability_check") return Response.json({availability:{domain:body.args.domain,available:true,known:true,provider:"spaceship",min_duration:1}});
          if (body.tool === "domain_registration_prepare") return Response.json(purchase);
          if (body.tool === "domain_register") {spaceshipSubmitted=true; return Response.json({registered:false,status:"processing",operation_id:"operation123"})}
          if (body.tool === "domain_registration_status") return Response.json(body.args.confirmation_token ? {...purchase,operation_id:"operation123"} : {intents:[]});
        }
        if (body.tool === "domain_registration_status")
          return Response.json({ intents: [] });
        if (body.tool === "domain_dns_recovery")
          return Response.json({ recoveries: [] });
        if (body.tool === "domain_availability_check")
          return Response.json({
            availability: {
              domain: body.args.domain,
              available: true,
              known: true,
              min_duration: 1,
              provider: "porkbun",
              connection_id: 1,
              price: "9.73",
              currency: "USD",
            },
          });
        if (body.tool === "domain_registration_prepare")
          return Response.json({
            confirmation_token: "quote-a",
            status: "prepared",
            domain: body.args.domain,
            provider: "porkbun",
            connection_id: 1,
            years: 1,
            auto_renew: true,
            whois_privacy: true,
            price: "9.73",
            currency: "USD",
            expires_at: "2030-01-01T00:00:00Z",
          });
        return Response.json({ action: "updated" });
      }
      return new Response(
        '<!doctype html><div id="root"></div><script type="module" src="/bundle.js"></script>',
        { headers: { "content-type": "text/html" } },
      );
    },
  });
  browser = await chromium.launch({
    channel: process.env.CHROME_CHANNEL || "chrome",
    headless: true,
  });
});
afterAll(async () => {
  for (const r of waiting.values()) r();
  await browser?.close();
  server?.stop(true);
  if (temp) await rm(temp, { recursive: true, force: true });
});
const loadPage = async () => {
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  await page.goto(`http://127.0.0.1:${server.port}`);
  await page.getByText("alpha.example", { exact: true }).waitFor();
  return page;
};
async function until(check: () => boolean) {
  const deadline = Date.now() + 5000;
  while (!check()) {
    if (Date.now() > deadline) throw new Error("condition timed out");
    await Bun.sleep(10);
  }
}

test("late domain response never becomes an edit for another domain", async () => {
  delayAlpha = true;
  calls.length = 0;
  const page = await loadPage();
  try {
    await page.getByText("alpha.example", { exact: true }).click();
    await until(() => waiting.has("project-a"));
    await page.getByText("beta.example", { exact: true }).click();
    await page.getByText("token-for-beta.example", { exact: true }).waitFor();
    const responded = page.waitForResponse(
      async (r) =>
        r.url().includes("/tools/call") &&
        (await r.json()).records?.[0]?.value === "token-for-alpha.example",
    );
    waiting.get("project-a")!();
    waiting.delete("project-a");
    await responded;
    await page.evaluate(() => new Promise(requestAnimationFrame));
    expect(
      await page.getByText("token-for-alpha.example", { exact: true }).count(),
    ).toBe(0);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page
      .getByRole("button", { name: "Save", exact: true })
      .last()
      .click();
    await until(() => calls.some((c) => c.tool === "domain_records_set"));
    const write = calls.find((c) => c.tool === "domain_records_set");
    expect(write.args).toMatchObject({
      domain: "beta.example",
      value: "token-for-beta.example",
      expected_connection_id: 1,
      expected_record: { value: "token-for-beta.example", ttl: 600, prio: 0 },
    });
  } finally {
    delayAlpha = false;
    await page.close();
  }
});

test("project/install changes clear selection and discard pending responses", async () => {
  delayAlpha = true;
  const page = await loadPage();
  try {
    await page.getByText("alpha.example", { exact: true }).click();
    await until(() => waiting.has("project-a"));
    await page.evaluate(() =>
      window.dispatchEvent(
        new CustomEvent("test-scope", {
          detail: { projectId: "project-b", installId: 2 },
        }),
      ),
    );
    waiting.get("project-a")!();
    waiting.delete("project-a");
    await page.getByText("beta.example", { exact: true }).click();
    await page.getByText("token-for-beta.example", { exact: true }).waitFor();
    expect(
      await page.getByText("token-for-alpha.example", { exact: true }).count(),
    ).toBe(0);
    expect(
      calls.some(
        (c) =>
          c.tool === "domain_records_list" &&
          c.project === "project-b" &&
          c.install === "2" &&
          c.args.domain === "beta.example",
      ),
    ).toBe(true);
  } finally {
    delayAlpha = false;
    await page.close();
  }
});

test("cancelled edits reset and confirmation traps/restores keyboard focus", async () => {
  const page = await loadPage();
  try {
    await page.getByText("beta.example", { exact: true }).click();
    await page.getByText("token-for-beta.example", { exact: true }).waitFor();
    const row = page.locator("table").last().locator("tbody tr").first();
    await row.getByRole("button", { name: "Edit", exact: true }).click();
    await row.locator("input").first().fill("discard-this");
    await row.getByRole("button", { name: "Cancel", exact: true }).click();
    await row.getByRole("button", { name: "Edit", exact: true }).click();
    expect(await row.locator("input").first().inputValue()).toBe(
      "token-for-beta.example",
    );
    await row.getByRole("button", { name: "Cancel", exact: true }).click();
    await row.getByRole("button", { name: "Delete", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    expect(
      await dialog
        .getByRole("button", { name: "Cancel", exact: true })
        .evaluate((e: any) => e === document.activeElement),
    ).toBe(true);
    await page.keyboard.press("Shift+Tab");
    expect(
      await dialog
        .getByRole("button", { name: "Delete record", exact: true })
        .evaluate((e: any) => e === document.activeElement),
    ).toBe(true);
    await page.keyboard.press("Tab");
    expect(
      await dialog
        .getByRole("button", { name: "Cancel", exact: true })
        .evaluate((e: any) => e === document.activeElement),
    ).toBe(true);
    await page.keyboard.press("Escape");
    expect(await dialog.count()).toBe(0);
    expect(
      await row
        .getByRole("button", { name: "Delete", exact: true })
        .evaluate((e: any) => e === document.activeElement),
    ).toBe(true);
  } finally {
    await page.close();
  }
});

test("switching project removes a prepared purchase confirmation", async () => {
  const page = await loadPage();
  try {
    await page.getByRole("button", { name: "Register", exact: true }).click();
    await page.getByLabel("Domain", { exact: true }).fill("fresh.example");
    await page
      .getByRole("button", { name: "Check availability", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Review purchase", exact: true })
      .click();
    await page.getByRole("dialog").waitFor();
    await page.evaluate(() =>
      window.dispatchEvent(
        new CustomEvent("test-scope", {
          detail: { projectId: "project-b", installId: 2 },
        }),
      ),
    );
    await page
      .getByRole("button", { name: "Inventory", exact: true })
      .waitFor();
    expect(await page.getByRole("dialog").count()).toBe(0);
    expect(
      await page
        .getByRole("button", { name: "Register and pay", exact: true })
        .count(),
    ).toBe(0);
  } finally {
    await page.close();
  }
});

test("Namecheap requires an explicit mail mode when provider metadata is absent", async () => {
  needsMailMode = true;
  calls.length = 0;
  const page = await loadPage();
  try {
    await page.getByText("beta.example", { exact: true }).click();
    await page.getByText("token-for-beta.example", { exact: true }).waitFor();
    expect(
      await page
        .getByRole("button", { name: "Edit", exact: true })
        .isDisabled(),
    ).toBe(true);
    await page
      .getByLabel("Current Namecheap mail routing", { exact: false })
      .selectOption("FWD");
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page
      .getByRole("button", { name: "Save", exact: true })
      .last()
      .click();
    await until(() => calls.some((c) => c.tool === "domain_records_set"));
    expect(
      calls.find((c) => c.tool === "domain_records_set").args
        .namecheap_email_type,
    ).toBe("FWD");
  } finally {
    needsMailMode = false;
    await page.close();
  }
});

test("Spaceship purchase shows account-rate consent and keeps accepted operations pending", async () => {
  spaceshipMode=true;spaceshipSubmitted=false;calls.length=0;
  const page=await loadPage();
  try {
    await page.getByRole("button",{name:"Register",exact:true}).click();
    await page.getByLabel("Domain",{exact:true}).fill("fresh.example");
    await page.getByRole("button",{name:"Check availability",exact:true}).click();
    const review=page.getByRole("button",{name:"Review purchase",exact:true});
    expect(await review.isDisabled()).toBe(true);
    await page.getByLabel("Spaceship contact ID",{exact:true}).fill("1ZdMXpapqp9sle5dl8BlppTJXAzf5");
    await page.getByLabel("Registration years",{exact:true}).fill("2");
    await page.getByText("I reviewed Spaceship pricing",{exact:false}).click();
    await review.click();
    const dialog=page.getByRole("dialog");await dialog.waitFor();
    expect(await dialog.textContent()).toContain("No guaranteed quote");
    const preparation=calls.find(c=>c.tool==="domain_registration_prepare");expect(preparation.args.accept_account_rate).toBe(true);expect(preparation.args.years).toBe(2);
    await dialog.getByRole("button",{name:"Register and pay",exact:true}).click();
    await dialog.getByRole("button",{name:"Check purchase status",exact:true}).waitFor();
    expect(await dialog.textContent()).toContain("processing");
    expect(await dialog.textContent()).toContain("Checking status does not submit another purchase.");
  } finally {spaceshipMode=false;await page.close()}
});

 test("stored estimates are shown for an available domain before purchase", async()=>{
  storedValuation=true;calls.length=0;const page=await loadPage();
  try{
   await page.getByRole("button",{name:"Register",exact:true}).click();
   await page.getByLabel("Domain",{exact:true}).fill("vectorlantern.com");
   await page.getByRole("button",{name:"Check availability",exact:true}).click();
   await page.getByText(/marketplace \$145/).waitFor();
   expect(await page.getByText(/auction \$0/).count()).toBe(1);
   expect(calls.some(c=>c.tool==="domain_estimate_list"&&c.args.domain==="vectorlantern.com"&&c.args.refresh===false)).toBe(true);
   expect(calls.some(c=>c.tool==="domain_estimate_start"||c.tool==="domain_register")).toBe(false);
  }finally{storedValuation=false;await page.close()}
 });

test("listing status and price survive a panel reload without publishing",async()=>{
 activeListing=true;calls.length=0;const page=await loadPage();
 try{
  await page.getByText("alpha.example",{exact:true}).click();
  await page.getByText("Listing status: For sale · Buy now $145 USD",{exact:true}).waitFor();
  expect(await page.getByLabel("Listing price",{exact:true}).inputValue()).toBe("145");
  await page.reload();await page.getByText("alpha.example",{exact:true}).click();
  await page.getByText("Listing status: For sale · Buy now $145 USD",{exact:true}).waitFor();
  expect(calls.some(c=>c.tool==="domain_sale_publish")).toBe(false);
 }finally{activeListing=false;await page.close()}
});

test("a scoped bus sale event updates saved state and payout without a paid action",async()=>{
 activeListing=true;soldListing=false;calls.length=0;const page=await loadPage();
 try{
  await page.getByText("alpha.example",{exact:true}).click();await page.getByText("Listing status: For sale · Buy now $145 USD",{exact:true}).waitFor();
  soldListing=true;const before=calls.length;
  await page.evaluate(()=>window.dispatchEvent(new CustomEvent('test-domain-event',{detail:{topic:'domain.sold',project_id:'other-project',install_id:1,seq:1,data:{event_id:'other',domain:'alpha.example'}}})));
  expect(calls.length).toBe(before);
  await page.evaluate(()=>window.dispatchEvent(new CustomEvent('test-domain-event',{detail:{topic:'domain.sold',project_id:'project-a',install_id:1,seq:2,data:{event_id:'sale-1',domain:'alpha.example'}}})));
  await page.getByText(/Sold — provider confirmed/).waitFor();await page.getByText(/payout 130.50 USD/).waitFor();
  expect(await page.getByRole('button',{name:'Publish listing',exact:true}).isDisabled()).toBe(true);
  expect(calls.some(c=>['domain_sale_publish','domain_register','domain_estimate_start'].includes(c.tool))).toBe(false);
 }finally{activeListing=false;soldListing=false;await page.close()}
});


test("saved estimate reuse and explicit paid refresh remain separate actions",async()=>{
 storedValuation=true;calls.length=0;const page=await loadPage();
 try{
  await page.getByText("alpha.example",{exact:true}).click();
  await page.getByRole("button",{name:"New paid estimate",exact:true}).waitFor();
  expect(calls.some(c=>c.tool==="domain_estimate_start")).toBe(false);
  await page.getByRole("button",{name:"Estimate with HumbleWorth",exact:true}).click();
  await page.getByText("Reused a stored estimate; no new paid prediction.",{exact:true}).waitFor();
  const first=calls.find(c=>c.tool==="domain_estimate_start");expect(first.args.force_new).toBeUndefined();
  await page.getByRole("button",{name:"New paid estimate",exact:true}).click();
  await page.getByText("Estimate submitted and stored.",{exact:true}).waitFor();
  expect(calls.filter(c=>c.tool==="domain_estimate_start").at(-1).args.force_new).toBe(true);
 }finally{storedValuation=false;await page.close()}
});
