import { test, expect } from "@playwright/test";

test("workspace desktop, responsive navigation and review flow", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await expect(page.getByText("128 exchanges in total")).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/a2a-v060-overview.png",
    fullPage: true,
  });
  await page.getByRole("tab", { name: "Agents" }).click();
  await expect(page.locator(".agent-card")).toHaveCount(6);
  await page.screenshot({
    path: "/private/tmp/a2a-v060-agents.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Map", exact: true }).click();
  await expect(page.locator(".map-peer")).toHaveCount(2);
  await page.screenshot({
    path: "/private/tmp/a2a-v060-map.png",
    fullPage: true,
  });
  await page.getByRole("tab", { name: "Exchanges" }).click();
  await page
    .locator(".list-row")
    .filter({ hasText: "Compare the three" })
    .click();
  await expect(
    page.getByRole("link", { name: "Operations thread" }),
  ).toHaveAttribute("href", "/agents/42?thread=launch-plan");
  await page.screenshot({
    path: "/private/tmp/a2a-v060-exchanges.png",
    fullPage: true,
  });
  await page
    .locator(".list-row")
    .filter({ hasText: "Check the forecast" })
    .click();
  await expect(
    page.getByRole("link", { name: "forecast.pdf" }),
  ).toHaveAttribute("href", "https://weather.example/forecast.pdf");
  await page.getByRole("tab", { name: "Connections" }).click();
  await page
    .getByRole("button", { name: "Check connection", exact: true })
    .first()
    .click();
  await expect(
    page.getByText("Discovery verified", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/a2a-v060-connections.png",
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Add connection", exact: true })
    .click();
  await page.getByRole("button", { name: "Apteva installation" }).click();
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Connection ID", exact: true })
    .fill("new-node");
  await page
    .getByRole("textbox", { name: "A2A base URL", exact: true })
    .fill("https://node.example/api/apps/a2a");
  await page
    .getByLabel("Pairing token", { exact: true })
    .fill("synthetic-preview-token");
  await page
    .getByRole("textbox", {
      name: "Agents this node may discover",
      exact: true,
    })
    .fill("41, 42");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(
    page.getByRole("dialog").getByText("41, 42", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("No inbound access", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/a2a-v060-setup.png",
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  for (const view of ["Overview", "Agents", "Exchanges", "Connections"]) {
    await page.getByRole("tab", { name: view }).click();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(
      await page
        .locator(".a2a")
        .evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
  }
  await page.getByRole("tab", { name: "Overview" }).click();
  await page.screenshot({
    path: "/private/tmp/a2a-v060-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1040 });
  await page.evaluate(() => {
    document.documentElement.dataset.mode = "light";
    const vars = {
      "--bg": "#ffffff",
      "--bg-card": "#f7f8fa",
      "--bg-input": "#ffffff",
      "--bg-hover": "#edf0f4",
      "--text": "#18212b",
      "--text-muted": "#586679",
      "--border": "#d6dde5",
      "--accent": "#1b796b",
    };
    Object.entries(vars).forEach(([key, val]) =>
      document.documentElement.style.setProperty(key, val),
    );
  });
  await page.screenshot({
    path: "/private/tmp/a2a-v060-light.png",
    fullPage: true,
  });
  expect(errors).toEqual([]);
});

test("failed loading is visible and retry recovers", async ({ page }) => {
  let fail = true;
  await page.route("**/tasks/128/messages?**", (route) =>
    fail
      ? route.fulfill({ status: 503, body: "Temporarily unavailable" })
      : route.continue(),
  );
  await page.goto("/");
  await page.getByRole("tab", { name: "Exchanges" }).click();
  await page
    .locator(".list-row")
    .filter({ hasText: "Compare the three" })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Could not load this exchange",
  );
  fail = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(
    page.getByText("I’m reviewing delivery records", { exact: false }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
});
