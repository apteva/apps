import { expect, test } from "@playwright/test";
test("step timing is editable, persists, and cleans up removed anchors", async ({
  page,
}, info) => {
  await page.goto("/");
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Edit procedure", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Step 2: Post alert in Conversations",
      exact: true,
    })
    .press("Enter");
  await page.getByLabel("Enable start after", { exact: true }).check();
  await expect(
    page.getByLabel("Start after reference", { exact: true }),
  ).toHaveValue("fetch_weather");
  await page.getByLabel("Start after amount", { exact: true }).fill("10");
  await page.getByLabel("Enable due after", { exact: true }).check();
  await page.getByLabel("Due after amount", { exact: true }).fill("30");
  await page
    .getByLabel("Due after reference", { exact: true })
    .scrollIntoViewIfNeeded();
  await page.screenshot({
    path: info.outputPath("timing-editor.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  const saved = await page.evaluate(() =>
    JSON.parse(sessionStorage.getItem("process")!),
  );
  expect(saved.steps[1].start_after).toEqual({
    after: "step_completed",
    step_key: "fetch_weather",
    offset: 10,
    unit: "minutes",
  });
  expect(saved.steps[1].due_after.offset).toBe(30);
  await page.reload();
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Step 2: Post alert in Conversations",
      exact: true,
    })
    .press("Enter");
  await expect(
    page.getByRole("complementary", { name: "Step details" }),
  ).toContainText("10 minutes after Fetch current weather completes");
  await page
    .getByRole("button", { name: "Edit procedure", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Step 2: Post alert in Conversations",
      exact: true,
    })
    .press("Enter");
  await page.getByLabel("Fetch current weather", { exact: true }).uncheck();
  await expect(
    page.getByLabel("Enable start after", { exact: true }),
  ).not.toBeChecked();
});
test("timed live step shows waiting time and updates when dispatched", async ({
  page,
  request,
}, info) => {
  const start = new Date(Date.now() + 10 * 60_000).toISOString();
  const definition = (key: string, name: string, depends_on: string[]) => ({
    key,
    name,
    depends_on,
    role: "worker",
    kind: "work",
    instructions: "Follow this step",
    expected_output: "Evidence",
  });
  const defs = [
    definition("fetch_weather", "Fetch current weather", []),
    {
      ...definition("post_conversations", "Post alert in Conversations", [
        "fetch_weather",
      ]),
      start_after: {
        after: "step_completed",
        step_key: "fetch_weather",
        offset: 10,
        unit: "minutes",
      },
    },
  ];
  const run = {
    id: "timed-live",
    process_id: "weather",
    version: 1,
    workflow: true,
    backend: "agent",
    state: "scheduled",
    created_at: new Date().toISOString(),
    steps: defs.map((definition, i) => ({
      id: `step-${i}`,
      run_id: "timed-live",
      key: definition.key,
      definition,
      executor: { kind: "agent", agent_id: 7 },
      state: i ? "scheduled" : "completed",
      progress: i ? 0 : 100,
      start_at: i ? start : "",
      output: "",
      error: "",
    })),
  };
  await request.post("/fixture/runs", { data: [run] });
  await page.goto("/?live");
  await page
    .getByRole("button", { name: "Hourly weather alerts", exact: true })
    .click();
  await page.getByRole("button", { name: "Runs", exact: true }).click();
  await page.locator(".run-list > button").first().click();
  await expect(
    page.locator('.pf-execution[data-state="scheduled"]'),
  ).toContainText("Starts in 10 min");
  await page
    .getByRole("button", {
      name: "Step 2: Post alert in Conversations",
      exact: true,
    })
    .press("Enter");
  await expect(
    page.getByRole("complementary", { name: "Step details" }),
  ).toContainText("Earliest start:");
  await page.screenshot({
    path: info.outputPath("timing-live.png"),
    fullPage: true,
  });
  run.state = "running";
  run.steps[1].state = "running";
  await request.post("/fixture/runs", { data: [run] });
  await request.post("/fixture/events", {
    data: {
      app: "processes",
      project_id: "test",
      install_id: 77,
      seq: 701,
      topic: "task.state_changed",
      data: { run_id: run.id, process_id: "weather" },
    },
  });
  await expect(page.locator('.pf-execution[data-state="running"]')).toHaveCount(
    1,
  );
});
