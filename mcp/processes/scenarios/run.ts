/** Real Codex/Terra scenarios with independent checks of the sidecar's saved state. */
import { resolve, basename } from "node:path";
import { mkdir, mkdtemp, readdir } from "node:fs/promises";
import { Database } from "bun:sqlite";
import YAML from "yaml";
import {
  check,
  verifyHistory,
  verifyMultiAgentTrajectory,
} from "./verify-outcomes";
const appDir = resolve(import.meta.dir, "..");
const outputRoot = resolve(
  process.env.APTEVA_TEST_ARTIFACTS_DIR || "/tmp/processes-tier3",
);
await mkdir(outputRoot, { recursive: true });
const outputDir = await mkdtemp(resolve(outputRoot, "run-"));
const scenarioDir = resolve(outputDir, "scenarios");
await mkdir(scenarioDir);
const files = process.argv[2]
  ? [resolve(appDir, process.argv[2])]
  : (await readdir(import.meta.dir))
      .filter((f) => f.endsWith(".yaml"))
      .sort()
      .map((f) => resolve(import.meta.dir, f));
const databases = new Map<string, string>();
for (const file of files) {
  const scenario = YAML.parse(await Bun.file(file).text());
  const dbPath = resolve(outputDir, `${basename(file, ".yaml")}.db`);
  scenario.setup.app.path = appDir;
  scenario.setup.app.env = { ...scenario.setup.app.env, DB_PATH: dbPath };
  databases.set(scenario.name, dbPath);
  await Bun.write(
    resolve(scenarioDir, basename(file)),
    YAML.stringify(scenario),
  );
}
const child = Bun.spawn(
  [
    process.env.APTEVA_TEST_CLI || "apteva",
    "test",
    "--provider",
    "openai-codex",
    "--model",
    "gpt-5.6-terra",
    "--max-budget-usd",
    "7.50",
    "--app-dir",
    appDir,
    "--artifacts-dir",
    outputDir,
    "--json",
    scenarioDir,
  ],
  {
    cwd: appDir,
    env: { ...process.env, GOWORK: "off" },
    stdout: "pipe",
    stderr: "inherit",
  },
);
const stdout = await new Response(child.stdout).text();
const exit = await child.exited;
await Bun.write(resolve(outputDir, "results.json"), stdout);
check(exit === 0, `Tier 3 runner failed (${exit}); see ${outputDir}`);
const report = JSON.parse(stdout);
check(report.ok === true, "Runner reported failure");
check(report.results.length === databases.size, "Incomplete suite");
const observed: Record<string, unknown> = {};
for (const scenario of report.results) {
  const path = databases.get(scenario.scenario);
  check(scenario.ok && path, `${scenario.scenario}: missing scenario result`);
  const db = new Database(path, { readonly: true });
  try {
    // DB_PATH keeps only this app's test state after the runner removes its server.
    // Read it after shutdown: credentials and the server database are not retained.
    const processes = db.query("SELECT id FROM processes").all() as {
      id: string;
    }[];
    check(processes.length === 1, "Expected exactly one saved procedure");
    const records = db
      .query("SELECT * FROM process_runs WHERE process_id=?")
      .all(processes[0].id) as any[];
    const runs = records.map((r) => ({
      ...r,
      workflow: !!r.workflow,
      assignment: JSON.parse(r.assignment_json),
      steps: (
        db
          .query(
            "SELECT * FROM process_step_runs WHERE run_id=? ORDER BY position",
          )
          .all(r.id) as any[]
      ).map((s) => ({
        ...s,
        key: s.step_key,
        definition: JSON.parse(s.definition_json),
        executor: JSON.parse(s.executor_json),
        events: db
          .query(
            "SELECT * FROM process_step_events WHERE step_id=? ORDER BY id",
          )
          .all(s.id),
      })),
    }));
    const history = {
      direct_runs: runs.filter((r) => r.backend === "agent"),
      runs: runs.filter((r) => r.backend === "tasks"),
    };
    verifyHistory(scenario.scenario, history);
    if (scenario.scenario === "processes-multi-agent-workflow")
      verifyMultiAgentTrajectory(scenario.tool_calls, runs[0]);
    observed[scenario.scenario] = history;
    console.log(
      `PASS ${scenario.scenario}: saved outcome verified (${scenario.iterations} iterations, ${scenario.tokens.total} tokens)`,
    );
  } finally {
    db.close();
  }
}
await Bun.write(
  resolve(outputDir, "verified-state.json"),
  JSON.stringify(observed, null, 2),
);
console.log(
  `Codex / gpt-5.6-terra · ${report.results.length} scenarios passed · reports: ${outputDir}`,
);
