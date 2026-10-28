// Race-detector run for this package, sharded across processes. Go runs one
// package's tests sequentially and the race detector also instruments the
// pure-Go SQLite engine, so a single-process -race run takes about an hour.
// This compiles the race test binary once and runs disjoint shards in
// parallel. Every test still runs exactly once under -race.
import { cpus } from "node:os";
import { join } from "node:path";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";

const root = join(import.meta.dir, "..");
const shards = Number(process.env.RACE_SHARDS ?? Math.max(2, Math.min(8, cpus().length - 2)));
const env = { ...process.env, GOWORK: "off" };
const temp = await mkdtemp(join(tmpdir(), "telephony-race-"));
const binary = join(temp, "telephony.race.test");

async function run(cmd: string[], cwd = root) {
  const child = Bun.spawn(cmd, { cwd, env, stdout: "pipe", stderr: "pipe" });
  const [out, err, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
  return { out: out + err, code };
}

try {
  const started = Date.now();
  const build = await run(["go", "test", "-race", "-c", "-o", binary, "."]);
  if (build.code) throw new Error(`race build failed:\n${build.out}`);
  const list = await run([binary, "-test.list", ".*"]);
  if (list.code) throw new Error(`test listing failed:\n${list.out}`);
  const names = list.out.split("\n").filter((n) => /^(Test|Example|Fuzz)/.test(n)).sort();
  const groups: string[][] = Array.from({ length: shards }, () => []);
  names.forEach((name, i) => groups[i % shards].push(name));
  console.log(`Running ${names.length} tests under -race in ${shards} shards.`);
  const results = await Promise.all(groups.map(async (group, i) => {
    const pattern = `^(${group.join("|")})$`;
    const r = await run([binary, "-test.run", pattern, "-test.count=1", "-test.timeout=30m"]);
    const races = (r.out.match(/WARNING: DATA RACE/g) ?? []).length;
    const failed = r.out.split("\n").filter((l) => /^\s*--- FAIL/.test(l) || /^panic:/.test(l));
    return { shard: i, code: r.code, races, failed, out: r.out };
  }));
  // A data race always fails. A test that fails only while sharing the CPU
  // with other instrumented shards (real-time pacing budgets) is re-run alone,
  // still under -race; it must pass there, and is reported either way.
  let bad = false;
  const retry = new Set<string>();
  for (const r of results) {
    if (r.races) {
      bad = true;
      console.log(`shard ${r.shard}: ${r.races} data race(s)`);
      console.log(r.out);
      continue;
    }
    for (const line of r.failed) {
      const name = line.match(/--- FAIL: (\w+)/)?.[1];
      if (name) retry.add(name);
    }
    if (r.code && !r.failed.some((l) => l.includes("--- FAIL"))) {
      bad = true;
      console.log(`shard ${r.shard}: exit ${r.code} without a test failure`);
      console.log(r.out);
    }
  }
  for (const name of retry) {
    const r = await run([binary, "-test.run", `^${name}$`, "-test.count=1", "-test.timeout=10m"]);
    const races = (r.out.match(/WARNING: DATA RACE/g) ?? []).length;
    if (r.code || races) {
      bad = true;
      console.log(`${name}: failed again when run alone`);
      console.log(r.out);
    } else {
      console.log(`${name}: failed under parallel load, passed alone (timing)`);
    }
  }
  console.log(`${bad ? "FAIL" : "ok"}: ${names.length} tests, ${shards} shards, ${retry.size} retried alone, ${Math.round((Date.now() - started) / 1000)} s`);
  process.exitCode = bad ? 1 : 0;
} finally {
  await rm(temp, { recursive: true, force: true });
}
