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
 function Test(){const [scope,setScope]=useState({projectId:'project-a',installId:1});React.useEffect(()=>{const f=e=>setScope(e.detail);window.addEventListener('test-scope',f);return()=>window.removeEventListener('test-scope',f)},[]);return <Panel {...scope} appName="domains"/>}createRoot(document.getElementById('root')).render(<Test/>);`,
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
              app_slug: "porkbun",
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
