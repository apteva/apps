import { test, expect, Page } from "@playwright/test";
const tenants = [
  {
    id: "a",
    slug: "alpha",
    kind: "local",
    status: "stopped",
    base_url: "http://localhost:6100",
    owner_email: "alpha@example.com",
    current_version: "0.41.0",
    target_version: "0.41.1",
  },
  {
    id: "b",
    slug: "beta",
    kind: "local",
    status: "active",
    base_url: "http://localhost:6101",
    owner_email: "beta@example.com",
    current_version: "0.41.0",
  },
];
const result = (data: unknown) => ({
  jsonrpc: "2.0",
  id: 1,
  result: { content: [{ type: "text", text: JSON.stringify(data) }] },
});
async function setup(
  page: Page,
  opts: {
    delayAlpha?: boolean;
    operation?: boolean;
    setupPending?: boolean;
    toolError?: boolean;
  } = {},
) {
  const calls: any[] = [];
  await page.route("**/api/apps/fleet/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    let data: unknown = {};
    if (path.endsWith("/_meta"))
      data = {
        domains_available: false,
        domains: [],
        certs: {},
        apteva_latest: "0.99.0",
        instances: [],
      };
    else if (path.endsWith("/maintenance"))
      data = { free_bytes: 100 * 2 ** 30, candidates: [] };
    else if (path.endsWith("/tenants")) data = { tenants, has_more: false };
    else if (path.endsWith("/mcp")) {
      const body = route.request().postDataJSON();
      calls.push(body.params);
      if (body.params.name === "tenant_platform_call")
        data = result({
          result: [
            { id: "p1", name: "Operations" },
            { id: "p2", name: "Support" },
          ],
        });
      else if (body.params.name === "tenant_template_list")
        data = result({
          templates: [
            { id: "template", name: "Standard", description: "Template" },
          ],
        });
      else if (opts.toolError)
        data = {
          result: {
            isError: true,
            content: [{ text: "provider rejected action" }],
          },
        };
      else
        data = result({
          note: "Quarantine rehearsal complete. Start again to activate.",
        });
    } else {
      const id = path.split("/").at(-1);
      const tenant = tenants.find((t) => t.id === id);
      if (opts.delayAlpha && id === "a")
        await new Promise((r) => setTimeout(r, 350));
      data = {
        tenant,
        setup_complete: !opts.setupPending,
        events: [
          {
            id: 1,
            tenant_id: id,
            kind: `${id}-only-event`,
            created_at: "2026-09-05T10:00:00Z",
          },
        ],
        hosts: [],
        ...(opts.operation
          ? {
              operation: {
                id: "op-123",
                operation: "migration",
                phase: "recovery_required",
              },
            }
          : {}),
      };
    }
    await route.fulfill({ json: data }).catch(() => {});
  });
  await page.goto("/");
  await expect(
    page.getByRole("button").filter({ hasText: "alpha" }).first(),
  ).toBeVisible();
  return calls;
}
test("rapid selection never mixes tenant details", async ({ page }) => {
  await setup(page, { delayAlpha: true });
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await page.getByRole("button").filter({ hasText: "beta" }).first().click();
  await expect(page.getByText("b-only-event", { exact: true })).toBeVisible();
  await page.waitForTimeout(450);
  await expect(page.getByText("a-only-event", { exact: true })).toHaveCount(0);
});
test("update submits the displayed pending target", async ({ page }) => {
  const calls = await setup(page);
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await page.getByRole("button", { name: "Apply 0.41.1", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("textbox")).toHaveValue(
    "0.41.1",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Update", exact: true })
    .click();
  await expect
    .poll(
      () => calls.find((c) => c.name === "tenant_update")?.arguments.version,
    )
    .toBe("0.41.1");
});
test("clone rehearsal next step is visible", async ({ page }) => {
  await setup(page);
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(page.getByRole("status")).toHaveText(
    "Quarantine rehearsal complete. Start again to activate.",
  );
});
test("tool errors are visible and never reported as success", async ({
  page,
}) => {
  await setup(page, { toolError: true });
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(
    page.getByText("provider rejected action", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
});
test("durable operation exposes recovery and blocks lifecycle buttons", async ({
  page,
}) => {
  await setup(page, { operation: true });
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await expect(page.getByText("migration: recovery_required")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Recover interrupted operation" }),
  ).toBeVisible();
  await expect(page.locator("[inert]")).toHaveCount(1);
  await expect(
    page.getByRole("button", { name: "Apply 0.41.1", exact: true }),
  ).toBeDisabled();
});
test("template dialog loads a real target project selector", async ({
  page,
}) => {
  await setup(page);
  await page.getByRole("button").filter({ hasText: "beta" }).first().click();
  await page
    .getByRole("button", { name: "Apply template…", exact: true })
    .click();
  await expect(page.getByLabel("Target project")).toBeVisible();
  await page.getByLabel("Target project").selectOption("p2");
  await expect(page.getByLabel("Target project")).toHaveValue("p2");
});

test("storage cleanup requires an eligible selection", async ({ page }) => {
  await setup(page);
  await page
    .getByRole("button", { name: "Local storage and cleanup…" })
    .click();
  await expect(page.getByText("100.0 GiB available")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Delete selected copies" }),
  ).toBeDisabled();
});
test("stopped tenants retain setup recovery controls", async ({ page }) => {
  await setup(page, { setupPending: true });
  await page.getByRole("button").filter({ hasText: "alpha" }).first().click();
  await expect(
    page.getByRole("button", { name: "Resume setup", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Start", exact: true }),
  ).toHaveCount(0);
});
