import { test, expect, type Page } from "@playwright/test";

async function checkoutFixture(page: Page, recurring = false) {
  let selected = false, paymentAttempts = 0, identityClaims = 0;
  const community = { id: "academy", slug: "main", name: "Academy", project_id: "project" };
  const member = { id: "alice", community_id: "academy", display_name: "Alice", status: "active" };
  const session = () => ({ offer_kind: recurring ? "recurring" : "one_time", checkout_session_id: 1, recovery_token: "opaque-recovery", amount_cents: 9900 + (selected ? 1900 : 0), currency: "EUR", publishable_key: "pk_test_fixture", presentation: "deferred", order_bumps: [{ id: 7, title: "Add the workbook", description: "Exercises to keep after the course", amount_cents: 1900, currency: "EUR", selected }] });
  await page.addInitScript(() => {
    (window as any).Stripe = () => ({
      elements: (options: any) => {
        (window as any).lastStripeAmount = options.amount;
        return { update: (next: any) => { (window as any).lastStripeAmount = next.amount; }, submit: async () => ({}), create: () => ({
          mount: (element: HTMLElement) => { element.textContent = "Secure payment test fields"; (window as any).paymentMounts = ((window as any).paymentMounts || 0) + 1; },
          on: (name: string, cb: (value?: any) => void) => { setTimeout(() => cb(name === "change" ? { complete: true } : undefined), 10); },
          destroy: () => {},
        }) };
      },
      confirmPayment: async () => ({ error: { message: "Test bank declined. Please try again." } }),
    });
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request(), url = new URL(request.url());
    const json = (value: unknown) => route.fulfill({ contentType: "application/json", body: JSON.stringify(value) });
    if (url.pathname.endsWith("/portal/bootstrap")) return json({ community, brand: { name: "Academy", primary_color: "#059669", accent_color: "#059669" }, auth: { client_id: "client" }, signup: { enabled: false } });
    if (url.pathname.includes("/portal/products/")) return json({ product: { slug: "course", catalog_product_id: 10, name: "Learn together", description: "A practical course", type: recurring ? "recurring" : "one_time", storefront: {}, testimonials: [], courses: [], offers: [{ catalog_price_id: 100, kind: recurring ? "recurring" : "one_time", name: "Course", unit_amount_cents: 9900, currency: "EUR", ...(recurring ? { interval: "month", interval_count: 1 } : {}), course_slugs: [] }] } });
    if (url.pathname.endsWith("/portal/products")) return json({ products: [] });
    if (url.pathname.endsWith("/portal/checkout/prepare")) return json(session());
    if (url.pathname.endsWith("/portal/checkout/bumps")) {
      expect(request.postDataJSON()).toMatchObject({ recovery_token: "opaque-recovery", bump_id: 7, catalog_price_id: 100 });
      selected = request.postDataJSON().selected;
      return json(session());
    }
    if (url.pathname.endsWith("/login")) return json({ user: { id: 1, email: "alice@example.test" }, apteva_access_token: "fixture-token", apteva_expires_in: 3600 });
    if (!url.pathname.endsWith("/mcp")) return json({});
    const { name, arguments: args } = request.postDataJSON().params;
    let result: any;
    switch (name) {
      case "members_me": result = { communities: [community], memberships: [member] }; break;
      case "members_ensure": result = { member }; break;
      case "members_list": result = { members: [member] }; break;
      case "spaces_list": result = { spaces: [] }; break;
      case "dms_list_threads": result = { threads: [] }; break;
      case "membership_plans_list": result = { plans: [] }; break;
      case "membership_status": result = { subscription: null }; break;
      case "storefront_checkout_claim":
        if (args.prepare_only) { identityClaims++; result = { claimed: true }; }
        else { paymentAttempts++; result = { client_secret: "pi_fixture_secret", invoice_id: 91, presentation: "deferred" }; }
        break;
      default: throw new Error(`Unexpected tool ${name}`);
    }
    return json({ jsonrpc: "2.0", id: 1, result: { content: [{ type: "text", text: JSON.stringify(result) }] } });
  });
  await page.goto("/?community=main&project_id=project&product=course&offer=100&intent=buy&auth=login");
  return { counts: () => ({ paymentAttempts, identityClaims }), selected: () => selected };
}

test("bumps survive sign-in and reload; payment begins only on Pay", async ({ page }) => {
  const fixture = await checkoutFixture(page);
  const bump = page.getByRole("checkbox", { name: /Add the workbook/ });
  await expect(bump).not.toBeChecked();
  await bump.click();
  await expect(bump).toBeChecked();
  await expect.poll(() => page.evaluate(() => (window as any).lastStripeAmount)).toBe(11800);
  expect(fixture.counts().paymentAttempts).toBe(0);
  expect(await page.evaluate(() => (window as any).paymentMounts)).toBe(1);
  await page.getByLabel("Email address", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByLabel("Password", { exact: true }).fill("correct-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).last().click();
  await expect(page.getByText("Account ready")).toBeVisible();
  await expect.poll(() => fixture.counts().identityClaims).toBeGreaterThan(0);
  expect(fixture.counts().paymentAttempts).toBe(0);
  await bump.click(); await expect(bump).not.toBeChecked();
  await expect.poll(() => page.evaluate(() => (window as any).lastStripeAmount)).toBe(9900);
  await bump.click(); await page.reload();
  await expect(bump).toBeChecked();
  await page.getByRole("button", { name: "Pay securely" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Test bank declined" }).last()).toBeVisible();
  expect(fixture.counts().paymentAttempts).toBe(1);
  await expect(bump).toBeDisabled();
  await page.screenshot({ path: test.info().outputPath("checkout-order-bumps.png"), fullPage: true });
});

test("mobile membership checkout separates today's extras from renewal pricing", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await checkoutFixture(page, true);
  await page.getByRole("checkbox", { name: /Add the workbook/ }).click();
  await expect(page.locator(".checkout-heading")).toContainText("€118.00");
  await expect(page.locator(".checkout-terms")).toContainText("€99.00 / month");
  await expect(page.locator(".checkout-terms")).toContainText("Optional extras are charged once");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const bounds = await page.locator(".order-bump").boundingBox();
  expect(bounds!.height).toBeLessThan(180);
  expect(await page.locator(".order-bump input").evaluate((e) => e.getBoundingClientRect().width)).toBeLessThan(24);
  await page.screenshot({ path: test.info().outputPath("checkout-order-bumps-mobile.png"), fullPage: true });
});
