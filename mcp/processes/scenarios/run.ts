/** Real Codex/Terra scenarios with independent checks of the sidecar's saved state. */
import { resolve, basename } from "node:path";
import { mkdir, mkdtemp, readdir, cp } from "node:fs/promises";
import { Database } from "bun:sqlite";
import YAML from "yaml";
import { startBrowserFixture, verifyBrowserContinuity } from "./browser-fixture";
let browserFixture: ReturnType<typeof startBrowserFixture> | undefined;
process.on("exit", () => browserFixture?.stop());
import {
  check,
  verifyHistory,
  verifyMultiAgentTrajectory,
  verifyStepWorkers,
  verifySequentialWorker,
  verifyOperatorConfirmation,
  OPERATOR_CONFIRMATION,
} from "./verify-outcomes";
import { confirmWhenWaiting, type ConfirmationReport } from "./operator-confirm";
const OPERATOR_SCENARIO = "processes-operator-confirmation";
// Provider is selectable so the suite can run on a connection-backed provider
// (opencode-go) as well as the Codex default. Each provider has its own large
// model, so the model default follows the provider rather than the other way round.
const DEFAULT_MODELS: Record<string, string> = {
  "openai-codex": "gpt-5.6-terra",
  "opencode-go": "kimi-k3",
};
const PROVIDER = process.env.APTEVA_TEST_PROVIDER || "openai-codex";
const MODEL = process.env.APTEVA_TEST_MODEL || DEFAULT_MODELS[PROVIDER] || "";
check(
  MODEL !== "",
  `No default model for provider ${PROVIDER}; set APTEVA_TEST_MODEL`,
);
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
  if (scenario.name === "processes-browser-continuity") {
    browserFixture = startBrowserFixture();
    scenario.directive = scenario.directive.replaceAll("__BROWSER_FIXTURE_URL__", browserFixture.url);
    scenario.setup.apps = [{path: resolve(appDir, "../computer"), spawnable: true}];
  }
  if (scenario.name === "processes-event-trigger-workflow") {
    const fixtureRoot = resolve(outputDir, "event-fixture");
    const processCopy = resolve(fixtureRoot, "processes");
    const sourceCopy = resolve(fixtureRoot, "test-signups");
    await cp(appDir, processCopy, { recursive: true });
    await cp(resolve(import.meta.dir, "fixtures/test-signups"), sourceCopy, {
      recursive: true,
    });
    const manifestPath = resolve(processCopy, "apteva.yaml");
    const manifest = YAML.parse(await Bun.file(manifestPath).text());
    manifest.requires.apps.push({ name: "test-signups", optional: true });
    await Bun.write(manifestPath, YAML.stringify(manifest));
    scenario.setup.app.path = processCopy;
  }

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
    PROVIDER,
    "--model",
    MODEL,
    "--max-budget-usd",
    "7.50",
    "--app-dir",
    appDir,
    "--artifacts-dir",
    outputDir,
    "--json",
    ...(process.env.APTEVA_TEST_SERVER ? ["--server", process.env.APTEVA_TEST_SERVER] : []),
    scenarioDir,
  ],
  {
    cwd: appDir,
    env: { ...process.env, GOWORK: "off" },
    stdout: "pipe",
    stderr: "inherit",
  },
);
// The CLI cannot release a parked step mid-run, so the operator acts from here
// while the child is still running. Failures are captured and asserted below.
const operatorAbort = new AbortController();
const operatorDb = databases.get(OPERATOR_SCENARIO);
const operatorConfirmation: Promise<ConfirmationReport | Error | null> = operatorDb
  ? confirmWhenWaiting(operatorDb, {
      stepKey: "confirm",
      output: OPERATOR_CONFIRMATION,
      signal: operatorAbort.signal,
      log: (message) => console.log(message),
    }).catch((error: Error) => error)
  : Promise.resolve(null);
const stdout = await new Response(child.stdout).text();
const exit = await child.exited;
operatorAbort.abort();
const confirmed = await operatorConfirmation;
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
    if (scenario.scenario === "processes-sequential-worker") {
      const workers = db.query("SELECT * FROM process_run_workers WHERE run_id=?").all(runs[0].id) as any[];
      verifySequentialWorker(scenario.tool_calls, runs[0], workers);
    }
    if (scenario.scenario === "processes-browser-continuity") {
      check(runs.length === 1 && history.runs.length === 0, "Expected one direct browser run");
      const workers = db.query("SELECT * FROM process_run_workers WHERE run_id=?").all(runs[0].id) as any[];
      verifySequentialWorker(scenario.tool_calls, runs[0], workers);
      verifyBrowserContinuity(scenario.tool_calls, runs[0], workers, browserFixture!);
      await Bun.write(resolve(outputDir, "browser-evidence.json"), JSON.stringify({visits: browserFixture!.visits, receipts: browserFixture!.receipts}, null, 2));
      browserFixture!.stop();
    }
    if (
      [
        "processes-multi-agent-workflow",
        "processes-event-trigger-workflow",
      ].includes(scenario.scenario)
    )
      verifyMultiAgentTrajectory(scenario.tool_calls, runs[0]);
    if (scenario.scenario === "processes-multi-agent-workflow") verifyStepWorkers(scenario.tool_calls, runs[0]);
    if (scenario.scenario === OPERATOR_SCENARIO) {
      check(
        confirmed && !(confirmed instanceof Error),
        `Operator confirmation failed: ${confirmed instanceof Error ? confirmed.message : "never performed"}`,
      );
      verifyOperatorConfirmation(runs[0], confirmed as ConfirmationReport);
      Object.assign(history, { operator_confirmation: confirmed });
    }
    if (scenario.scenario === "processes-event-trigger-workflow") {
      const events = db
        .query("SELECT * FROM process_trigger_events")
        .all() as any[];
      check(
        events.length === 1 &&
          events[0].status === "started" &&
          events[0].run_id === runs[0].id &&
          events[0].id === runs[0].trigger_event_id,
        "Expected one bus-triggered run",
      );
      const event = JSON.parse(events[0].event_json);
      check(
        event.source_app === "test-signups" &&
          event.delivery_id.startsWith("app-target-") &&
          event.data.event_id.endsWith(":tier3-signup-1"),
        "Run did not originate from the real app bus",
      );
      check(
        JSON.parse(events[0].parameters_json).page === "photography",
        "Event parameter mapping failed",
      );
      Object.assign(history, { trigger_events: events });
    }
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
  `${PROVIDER} / ${MODEL} · ${report.results.length} scenarios passed · reports: ${outputDir}`,
);
