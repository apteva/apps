import { test, expect } from "@playwright/test";

test("workspace desktop, responsive navigation and review flow", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await expect(page.getByText("128 exchanges in total")).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/a2a-v061-overview.png",
    fullPage: true,
  });
  await page.getByRole("tab", { name: "Agents" }).click();
  await expect(page.locator(".agent-card")).toHaveCount(6);
  await page.screenshot({
    path: "/private/tmp/a2a-v061-agents.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Map", exact: true }).click();
  await expect(page.locator(".map-peer")).toHaveCount(2);
  await page.screenshot({
    path: "/private/tmp/a2a-v061-map.png",
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
    path: "/private/tmp/a2a-v061-exchanges.png",
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
    path: "/private/tmp/a2a-v061-connections.png",
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
    path: "/private/tmp/a2a-v061-setup.png",
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
    path: "/private/tmp/a2a-v061-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1040 });
  await page.evaluate(() => {
    document.documentElement.dataset.mode = "light";
    document.documentElement.dataset.theme = "clean";
  });
  await page.screenshot({
    path: "/private/tmp/a2a-v061-light.png",
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

// Theme identity is more than a palette: verify the same font, radii and
// full-width shell used by Tasks/CRM, including after a live theme switch.
test("host themes and full-width layout across every view", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.goto("/?theme=terminal&mode=dark");
  for (const theme of ["terminal", "clean"]) {
    for (const mode of ["dark", "light"]) {
      await page.evaluate(
        ({ theme, mode }) => {
          document.documentElement.dataset.theme = theme;
          document.documentElement.dataset.mode = mode;
        },
        { theme, mode },
      );
      for (const view of ["Overview", "Agents", "Exchanges", "Connections"]) {
        await page.getByRole("tab", { name: view }).click();
        const geometry = await page.locator(".a2a").evaluate((el) => {
          const root = getComputedStyle(document.documentElement),
            panel = getComputedStyle(el),
            main = el.querySelector(".main")!,
            title = el.querySelector("h1")!,
            card = el.querySelector(".panel");
          return {
            font: panel.fontFamily,
            hostFont: getComputedStyle(document.body).fontFamily,
            width: main.getBoundingClientRect().width,
            parentWidth: el.clientWidth,
            left: main.getBoundingClientRect().left,
            titleSize: getComputedStyle(title).fontSize,
            radius: card ? getComputedStyle(card).borderRadius : null,
            hostRadius: root.getPropertyValue("--radius-md").trim(),
            maxWidth: getComputedStyle(main).maxWidth,
          };
        });
        expect(geometry.font).toBe(geometry.hostFont);
        expect(geometry.width).toBe(geometry.parentWidth);
        expect(geometry.left).toBe(0);
        expect(geometry.maxWidth).toBe("none");
        expect(geometry.titleSize).toBe("18px");
        if (geometry.radius) expect(geometry.radius).toBe(geometry.hostRadius);
        if (view === "Overview")
          await page.screenshot({
            path: `/private/tmp/a2a-v061-${theme}-${mode}.png`,
          });
      }
    }
  }
});

test("a small ledger stays compact and uses full width", async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.route("**/api/apps/a2a/tasks?**", async (route) => {
    const status = new URL(route.request().url()).searchParams.get("status");
    await route.fulfill({
      json: {
        tasks: status
          ? []
          : [
              {
                id: 1,
                kind: "message",
                status: "completed",
                from_agent_id: 41,
                from_agent_name: "Agent",
                to_agent_id: 42,
                to_agent_name: "Worker",
                preview: "Hello!",
                direction: "local",
                created_at: "2026-09-01T12:00:00Z",
                updated_at: "2026-09-01T12:00:00Z",
              },
            ],
        total: status ? 0 : 1,
      },
    });
  });
  await page.route("**/api/apps/a2a/overview?**", (route) =>
    route.fulfill({
      json: {
        total: 1,
        active: 0,
        input_required: 0,
        attention: 0,
        completed: 1,
        as_of: new Date().toISOString(),
      },
    }),
  );
  await page.goto("/?theme=terminal&mode=dark");
  await expect(
    page.getByText("1 exchange in total", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Nothing needs attention", { exact: true }),
  ).toBeVisible();
  const recent = page
    .locator("section")
    .filter({
      has: page.getByRole("heading", { name: "Recent exchanges", exact: true }),
    });
  expect((await recent.boundingBox())!.height).toBeLessThan(200);
  await page.screenshot({ path: "/private/tmp/a2a-v061-small-ledger.png" });
});
