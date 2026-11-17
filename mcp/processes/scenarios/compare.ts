// Compares two Tier 3 report directories (for example the previous release and
// this checkout) scenario by scenario: pass/fail, model iterations, reported
// tokens, tool calls, Processes tool calls and worker threads.
//
//   bun run scenarios/compare.ts <before-run-dir[,dir…]> <after-run-dir[,dir…]> [--md]

type ToolCall = { name: string; thread_id?: string; ok?: boolean };
type Result = {
  scenario: string;
  ok: boolean;
  budget_ok?: boolean;
  elapsed_ms: number;
  iterations: number;
  tokens: { total: number };
  tool_calls: ToolCall[];
};
type Row = {
  ok: boolean;
  budgetOk: boolean;
  iterations: number;
  tokens: number;
  calls: number;
  processCalls: number;
  failedCalls: number;
  threads: number;
  seconds: number;
};

// A side may list several report dirs separated by commas (for example a full run
// plus a rerun of some scenarios); a later dir's result for a scenario wins.
async function load(dirs: string) {
  const rows = new Map<string, Row>();
  for (const dir of dirs.split(",")) {
    const report = await Bun.file(`${dir}/results.json`).json();
    for (const r of report.results as Result[]) {
      const calls = r.tool_calls ?? [];
      rows.set(r.scenario, {
        ok: r.ok,
        budgetOk: r.budget_ok !== false,
        iterations: r.iterations,
        tokens: r.tokens?.total ?? 0,
        calls: calls.length,
        processCalls: calls.filter((c) => c.name.startsWith("processes_"))
          .length,
        failedCalls: calls.filter((c) => c.ok === false).length,
        threads: new Set(calls.map((c) => c.thread_id || "main")).size,
        seconds: Math.round(r.elapsed_ms / 1000),
      });
    }
  }
  return rows;
}

const [beforeDir, afterDir] = process.argv
  .slice(2)
  .filter((a) => !a.startsWith("--"));
if (!beforeDir || !afterDir) {
  console.error(
    "usage: bun run scenarios/compare.ts <before-run-dir> <after-run-dir> [--md]",
  );
  process.exit(2);
}
const before = await load(beforeDir);
const after = await load(afterDir);
const names = [...new Set([...before.keys(), ...after.keys()])].sort();

const status = (r?: Row) =>
  !r ? "-" : r.ok ? "pass" : r.budgetOk ? "FAIL" : "over budget";
const pct = (a: number, b: number) =>
  a ? `${Math.round(((b - a) / a) * 100)}%` : "";
const header = [
  "scenario",
  "before",
  "after",
  "iterations",
  "tokens",
  "tool calls",
  "processes_* calls",
  "failed calls",
  "seconds",
];
const lines: string[][] = [];
const total = {
  before: { it: 0, tok: 0, calls: 0, pc: 0, fail: 0, s: 0 },
  after: { it: 0, tok: 0, calls: 0, pc: 0, fail: 0, s: 0 },
};
for (const name of names) {
  const a = before.get(name);
  const b = after.get(name);
  for (const [k, r] of [
    ["before", a],
    ["after", b],
  ] as const) {
    if (!r) continue;
    const t = total[k];
    t.it += r.iterations;
    t.tok += r.tokens;
    t.calls += r.calls;
    t.pc += r.processCalls;
    t.fail += r.failedCalls;
    t.s += r.seconds;
  }
  const pair = (f: (r: Row) => number) =>
    `${a ? f(a) : "-"} → ${b ? f(b) : "-"}`;
  lines.push([
    name.replace(/^processes-/, ""),
    status(a),
    status(b),
    pair((r) => r.iterations),
    pair((r) => r.tokens),
    pair((r) => r.calls),
    pair((r) => r.processCalls),
    pair((r) => r.failedCalls),
    pair((r) => r.seconds),
  ]);
}
const t = total;
lines.push([
  "TOTAL",
  `${[...before.values()].filter((r) => r.ok).length}/${before.size}`,
  `${[...after.values()].filter((r) => r.ok).length}/${after.size}`,
  `${t.before.it} → ${t.after.it} (${pct(t.before.it, t.after.it)})`,
  `${t.before.tok} → ${t.after.tok} (${pct(t.before.tok, t.after.tok)})`,
  `${t.before.calls} → ${t.after.calls} (${pct(t.before.calls, t.after.calls)})`,
  `${t.before.pc} → ${t.after.pc} (${pct(t.before.pc, t.after.pc)})`,
  `${t.before.fail} → ${t.after.fail}`,
  `${t.before.s} → ${t.after.s} (${pct(t.before.s, t.after.s)})`,
]);

if (process.argv.includes("--md")) {
  console.log(`| ${header.join(" | ")} |`);
  console.log(`| ${header.map(() => "---").join(" | ")} |`);
  for (const l of lines) console.log(`| ${l.join(" | ")} |`);
} else {
  const widths = header.map((h, i) =>
    Math.max(h.length, ...lines.map((l) => l[i].length)),
  );
  const fmt = (l: string[]) => l.map((c, i) => c.padEnd(widths[i])).join("  ");
  console.log(fmt(header));
  for (const l of lines) console.log(fmt(l));
}
