import { test, expect, type APIRequestContext } from "@playwright/test";
const base = "/api/apps/calendar/_install/1";
async function create(request: APIRequestContext, path: string, data: unknown) {
  const res = await request.post(base + path, { data });
  expect(res.ok()).toBe(true);
  return res.json();
}
test.beforeEach(async ({ request, page }) => {
  const res = await request.get(base + "/calendars");
  for (const c of (await res.json()).calendars)
    await request.delete(base + `/calendars/${c.id}`);
  await page.clock.install({ time: new Date("2026-05-04T08:00:00Z") });
});
test("renaming a later occurrence preserves series dates and supports calendar moves", async ({
  page,
  request,
}) => {
  const cal = await create(request, "/calendars", { name: "Work" });
  const other = await create(request, "/calendars", { name: "Personal" });
  const event = await create(request, "/items", {
    calendar_id: cal.id,
    title: "Daily review",
    start_at: "2026-05-04T09:00:00Z",
    end_at: "2026-05-04T10:00:00Z",
    rrule: "FREQ=DAILY;COUNT=5",
    timezone: "Europe/Madrid",
    description: "Notes",
  });
  await page.goto("/");
  await page
    .getByRole("button", { name: "Daily review,", exact: false })
    .nth(2)
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("option", { name: "Entire series" }),
  ).toBeEnabled();
  await page.getByLabel("Apply changes to").selectOption("all");
  await page.getByLabel("Title", { exact: true }).fill("Renamed review");
  await page
    .getByRole("textbox", { name: "Description", exact: true })
    .fill("");
  await page
    .getByRole("combobox", { name: "Calendar", exact: true })
    .selectOption(String(other.id));
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const result = await (await request.get(base + `/items/${event.id}`)).json();
  expect(result.start_at).toBe("2026-05-04T09:00:00Z");
  expect(result.description).toBe("");
  expect(result.calendar_id).toBe(other.id);
  const list = await (
    await request.get(
      base + "/items?from=2026-05-01T00:00:00Z&to=2026-06-01T00:00:00Z",
    )
  ).json();
  expect(list.events).toHaveLength(5);
});
test("overlapping meetings remain individually visible and keyboard operable", async ({
  page,
  request,
}) => {
  const cal = await create(request, "/calendars", { name: "Work" });
  for (const title of ["First", "Second"])
    await create(request, "/items", {
      calendar_id: cal.id,
      title,
      start_at: "2026-05-04T09:00:00Z",
      end_at: "2026-05-04T10:00:00Z",
    });
  await page.goto("/");
  const a = page.getByRole("button", { name: "First," });
  const b = page.getByRole("button", { name: "Second," });
  const ar = await a.boundingBox(),
    br = await b.boundingBox();
  expect(ar && br).toBeTruthy();
  expect(ar!.x + ar!.width).toBeLessThanOrEqual(br!.x + 1);
  const hour = await page.locator('[data-hour-label="11"]').boundingBox();
  expect(Math.abs(ar!.y - hour!.y)).toBeLessThan(2);
  await a.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Title", { exact: true })).toHaveValue("First");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.screenshot({
    path: "browser/results/week-overlap.png",
    fullPage: true,
  });
});
test("failed deletion keeps the dialog open and shows the error", async ({
  page,
  request,
}) => {
  const cal = await create(request, "/calendars", { name: "Work" });
  await create(request, "/items", {
    calendar_id: cal.id,
    title: "Keep me",
    start_at: "2026-05-04T09:00:00Z",
    end_at: "2026-05-04T10:00:00Z",
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Keep me," }).focus();
  await page.keyboard.press("Enter");
  await page.route("**/items/*", async (route) => {
    if (route.request().method() === "DELETE")
      return route.fulfill({ status: 500, body: "Database unavailable" });
    return route.continue();
  });
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await page
    .getByRole("dialog", { name: "Delete this event?" })
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("Database unavailable");
  await expect(page.getByRole("dialog", { name: "Edit event" })).toBeVisible();
});
test("create an all-day recurring event on mobile", async ({
  page,
  request,
}) => {
  await create(request, "/calendars", { name: "Personal" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "New event", exact: true }).click();
  await page.getByLabel("Title", { exact: true }).fill("Annual holiday");
  await page.getByLabel("All day", { exact: true }).check();
  await page.getByLabel("Start", { exact: true }).fill("2026-07-14");
  await page.getByLabel("End (exclusive)", { exact: true }).fill("2026-07-15");
  await page.getByLabel("Repeat preset").selectOption("FREQ=YEARLY");
  await page.screenshot({
    path: "browser/results/mobile-editor.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const list = await (
    await request.get(
      base + "/items?from=2026-07-01T00:00:00Z&to=2026-08-01T00:00:00Z",
    )
  ).json();
  expect(list.events).toHaveLength(1);
  expect(list.events[0].all_day).toBe(true);
});
test("cross-day drag shows the destination preview and commits one occurrence", async ({
  page,
  request,
}) => {
  const cal = await create(request, "/calendars", { name: "Work" });
  await create(request, "/items", {
    calendar_id: cal.id,
    title: "Move me",
    start_at: "2026-05-04T09:00:00Z",
    end_at: "2026-05-04T10:00:00Z",
  });
  await page.goto("/");
  const event = page.getByRole("button", { name: "Move me," });
  const box = await event.boundingBox();
  expect(box).toBeTruthy();
  const columns = page.locator("[data-day-date]");
  const target = await columns.nth(1).boundingBox();
  expect(target).toBeTruthy();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + 10);
  await page.mouse.down();
  await page.mouse.move(target!.x + target!.width / 2, box!.y + 10, {
    steps: 8,
  });
  await expect(page.getByTestId("drag-preview")).toBeVisible();
  const preview = await page.getByTestId("drag-preview").boundingBox();
  expect(preview!.x).toBeGreaterThanOrEqual(target!.x);
  await page.mouse.up();
  await expect
    .poll(async () => {
      const data = await (
        await request.get(
          base + "/items?from=2026-05-01T00:00:00Z&to=2026-06-01T00:00:00Z",
        )
      ).json();
      return data.events[0].start_at;
    })
    .toBe("2026-05-05T09:00:00Z");
});
test("following-scope title edit preserves the remaining recurrence count", async ({
  page,
  request,
}) => {
  const cal = await create(request, "/calendars", { name: "Work" });
  await create(request, "/items", {
    calendar_id: cal.id,
    title: "Five sessions",
    start_at: "2026-05-04T09:00:00Z",
    end_at: "2026-05-04T10:00:00Z",
    rrule: "FREQ=DAILY;COUNT=5",
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Five sessions," }).nth(2).focus();
  await page.keyboard.press("Enter");
  await page.getByLabel("Apply changes to").selectOption("this_and_following");
  await page.getByLabel("Title", { exact: true }).fill("Later sessions");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const list = await (
    await request.get(
      base + "/items?from=2026-05-01T00:00:00Z&to=2026-06-01T00:00:00Z",
    )
  ).json();
  expect(list.events).toHaveLength(5);
  expect(
    list.events.filter((e: any) => e.title === "Later sessions"),
  ).toHaveLength(3);
});
