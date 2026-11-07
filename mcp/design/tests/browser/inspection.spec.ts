import { test, expect } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
const definition = {
  schema: "apteva-design/v1",
  units: "mm",
  operations: [
    { id: "outer", type: "box", size: [30, 20, 10] },
    { id: "inner", type: "box", origin: [1, 1, 1], size: [28, 18, 10] },
    { id: "shell", type: "cut", inputs: ["outer", "inner"] },
    { id: "board", type: "box", origin: [5, 5, 3], size: [20, 10, 1] },
  ],
  output: "shell",
  parts: [
    {
      id: "shell",
      name: "Front shell",
      output: "shell",
      manufacturing: { classification: "printed" },
    },
    {
      id: "board",
      name: "Purchased board",
      output: "board",
      manufacturing: { classification: "purchased" },
    },
  ],
  assembly: {
    instances: [
      { id: "shell_instance", part_id: "shell" },
      { id: "board_instance", part_id: "board" },
    ],
  },
  checks: [
    {
      id: "clearance",
      type: "part_clearance",
      a: "shell_instance",
      b: "board_instance",
      min: 1,
    },
  ],
};
let mesh: any, report: any;
test.beforeAll(async () => {
  const out = await mkdtemp(join(tmpdir(), "design-browser-"));
  const child = spawnSync(
    "bun",
    [join(process.cwd(), "mcp/design/runner/dist/runner.mjs")],
    {
      input: JSON.stringify({
        definition,
        parameters: {},
        formats: ["mesh-json"],
        output_dir: out,
      }),
      encoding: "utf8",
    },
  );
  expect(child.status, child.stderr).toBe(0);
  const result = JSON.parse(
    child.stdout
      .trim()
      .split("\n")
      .findLast((l) => l.startsWith('{"ok":'))!,
  );
  report = result.report;
  mesh = JSON.parse(await readFile(join(out, "model.mesh.json"), "utf8"));
});
test("inspection controls preserve validation and render a visible assembly", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  let builds = 0;
  const design = {
    id: 1,
    name: "Pocket fixture",
    kind: "parametric",
    current_revision_id: 1,
    current_revision: {
      id: 1,
      revision_number: 1,
      definition,
      parameters: {},
      source_sha256: "fixture",
    },
    artifacts: [
      { id: 1, format: "mesh-json", name: "assembly.mesh.json", metadata: {} },
    ],
    latest_build: {
      id: 1,
      status: "passed",
      report,
      checks: report.exact_checks,
    },
  };
  await page.route("**/api/apps/design/api/**", async (route) => {
    const url = new URL(route.request().url()),
      p = url.pathname;
    if (p.includes("/artifacts/1/content"))
      return route.fulfill({ json: mesh });
    if (p.endsWith("/designs/1")) return route.fulfill({ json: { design } });
    if (p.endsWith("/designs"))
      return route.fulfill({ json: { designs: [design] } });
    if (p.endsWith("/pcb-source"))
      return route.fulfill({ json: { bound: false } });
    if (p.endsWith("/examples")) return route.fulfill({ json: {} });
    if (p.endsWith("/build")) builds++;
    return route.fulfill({ json: {} });
  });
  await page.goto("/");
  await expect(
    page.getByText("Assembly instances", { exact: true }),
  ).toBeVisible();
  await expect(page.locator("canvas")).toBeVisible();
  await page.getByRole("button", { name: "top", exact: true }).click();
  await page.getByLabel("Section plane").selectOption("2");
  await expect(page.getByLabel("Section position")).toBeVisible();
  await page.getByLabel("Section position").fill("4");
  await page.getByLabel("Show Purchased board").uncheck();
  await page.getByRole("button", { name: "Transparency", exact: true }).click();
  await expect(page.getByLabel("Opacity")).toBeVisible();
  await page.getByRole("button", { name: "Show all", exact: true }).click();
  await expect(page.getByLabel("Show Purchased board")).toBeChecked();
  expect(builds).toBe(0);
  await page.getByRole("button", { name: "Measure", exact: true }).click();
  await expect(page.getByLabel("Measurement mode")).toBeVisible();
  await page.getByLabel("Section plane").selectOption("");
  await page.getByRole("button", { name: "iso", exact: true }).click();
  await page.screenshot({ path: join(tmpdir(),"design-v050-inspection.png") });
  expect(errors).toEqual([]);
});
