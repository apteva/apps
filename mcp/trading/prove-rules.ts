// Rebuilds the executable proof from controlled tapes; never contacts a broker.
import { mkdir, mkdtemp, readdir, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const app = import.meta.dir;
const evidence = resolve(Bun.argv[2] || join(tmpdir(), "apteva-rule-engine-proof"));
await mkdir(evidence, { recursive: true });
const scratch = await mkdtemp(join(tmpdir(), "apteva-rule-proof-"));
async function run(args: string[], env: Record<string, string> = {}) {
 const process = Bun.spawn(args, { cwd: app, env: { ...Bun.env, GOWORK: "off", ...env }, stdout: "pipe", stderr: "pipe" });
 const [stdout, stderr, code] = await Promise.all([new Response(process.stdout).text(), new Response(process.stderr).text(), process.exited]);
 if (code !== 0) throw new Error(`${args.join(" ")} failed (${code})\n${stdout}\n${stderr}`);
 return stdout;
}
try {
 console.log("Running screenshot, API/replay, and deadline-race acceptance tests…");
 const log = await run(["go", "test", "./...", "-short", "-run", "TestScreenshot|TestRuleBacktest|TestRuleDeadline|TestRuleMCP", "-v"], { RULE_PROOF_DIR: evidence });
 await Bun.write(join(evidence, "acceptance.log"), log);
 const executable = join(scratch, "rule-backtest");
 await run(["go", "build", "-o", executable, "./cmd/rule-backtest"]);
 const examples = new Map<string, string>();
 for (const name of await readdir(join(app, "rule_examples"))) {
  if (!name.endsWith(".json")) continue;
  const path = join(app, "rule_examples", name);
  examples.set((await Bun.file(path).json()).id, path);
 }
 const cases: Record<string, any>[] = [];
 for (const name of (await readdir(evidence)).sort()) {
  if (!name.endsWith(".json") || name.endsWith("-cli.json")) continue;
  const path = join(evidence, name);
  const expected = await Bun.file(path).json();
  if (!expected.case?.startsWith("TestScreenshot")) continue;
  const example = [...examples].find(([id]) => name.startsWith(id + "-"))?.[1];
  if (!example) throw new Error(`No definition for ${name}`);
  const configuration = join(scratch, "configuration.json");
  await Bun.write(configuration, JSON.stringify(expected.simulation));
  const cliName = name.replace(/\.json$/, "-cli.json");
  await run([executable, "--example", example, "--input", path, "--config", configuration, "--output", join(evidence, cliName)]);
  const replay = await Bun.file(join(evidence, cliName)).json();
  for (const key of ["input_sha256", "output_sha256", "execution_source_sha256", "rule_source_sha256", "filled_entries"]) {
   if (expected[key] !== replay[key]) throw new Error(`${name}: ${key} differs`);
  }
  for (const [key, value] of Object.entries(expected.metrics)) {
   if (replay.metrics[key] !== value) throw new Error(`${name}: metric ${key} differs`);
  }
  cases.push({ case: expected.case, file: name, cli_file: cliName, entries: replay.filled_entries, realized_pnl: replay.metrics.realized_pnl, input_sha256: replay.input_sha256, output_sha256: replay.output_sha256 });
  console.log(`${expected.case}: ${replay.filled_entries} entries, P&L ${replay.metrics.realized_pnl.toFixed(2)}, identical replay`);
 }
 if (cases.length !== 9) throw new Error(`Expected 9 screenshot cases; got ${cases.length}`);
 await Bun.write(join(evidence, "summary.json"), JSON.stringify({ data_class: "synthetic_acceptance", independent_cli_hash_matches: cases.length, cases }, null, 2) + "\n");
 const rows = cases.map(c => `| ${c.case.replace("TestScreenshot", "")} | ${c.entries} | ${c.realized_pnl.toFixed(2)} | [Events](${join(evidence, c.file)}) · [CLI replay](${join(evidence, c.cli_file)}) |`).join("\n");
 await Bun.write(join(evidence, "REPORT.md"), `# Trading rule-engine proof\n\nAll three screenshot strategies execute as ordinary generic rule definitions. Nine controlled cases passed and each standalone CLI replay matched the full input/output hashes, engine fingerprints, entry counts and metrics. API tests also passed worker persistence, ledger export/import, and checkpoint replay; a deadline cancellation-race test passed.\n\n**Data: synthetic acceptance tapes. These P&L numbers verify mechanics, not historical performance or profitability.** Costs are zero in these controlled cases; contract specifications are illustrative.\n\n| Case | Filled entries | Account-currency P&L | Evidence |\n|---|---:|---:|---|\n${rows}\n\nDonchian cases exercise trailing activation/exit, no early reentry, midpoint reset and target exit. ATR cases exercise both directions and both stop/target boundaries at $100 planned risk. Session cases exercise paired entries, OCO cancellation, one daily trade, 18:00 liquidation, and cancellation of unfilled orders.\n\nDefinitions: [Donchian](${join(app, "rule_examples/donchian.json")}), [ATR candle](${join(app, "rule_examples/atr_candle.json")}), [Session range](${join(app, "rule_examples/session_range.json")}). [Engine guide](${join(app, "RULE_ENGINE.md")}) · [Acceptance tests](${join(app, "strategy_rules_test.go")}) · [Test log](${join(evidence, "acceptance.log")}) · [Hashes and summary](${join(evidence, "summary.json")}).\n\nReproduce from the trading app directory:\n\n\`\`\`sh\nbun run prove-rules.ts /absolute/path/to/evidence\n\`\`\`\n\nCurrent limits: one instrument per rule program, no implicit pyramiding, same-day windows, bounded indicator seeds, fixed currency conversion, no financing or automatic margin liquidation, and no live rule adapter. Historical backtests need sourced gold and DE40 candles plus executable quotes and correct broker specifications. Session broker timezone and ATR convention must be confirmed for an exact historical reproduction.\n`);
 console.log(`Proof report: ${join(evidence, "REPORT.md")}`);
} finally {
 await rm(scratch, { recursive: true, force: true });
}
