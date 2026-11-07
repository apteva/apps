import { join, resolve } from "node:path";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { cpus, freemem, loadavg, tmpdir } from "node:os";

const root = resolve(import.meta.dir, "../..");
const options = new Map<string, string>();
for (let i = 2; i < process.argv.length; i += 2) {
  const key = process.argv[i], value = process.argv[i + 1];
  if (!["--server", "--core", "--output", "--calls", "--seconds", "--profile"].includes(key) || !value || options.has(key)) {
    throw new Error(`Invalid option ${key}`);
  }
  options.set(key, value);
}
if (!options.has("--server") || !options.has("--core")) {
  throw new Error("Usage: bun run benchmark:internal-audio --server /path/server --core /path/core [--output /new/path] [--calls 24] [--seconds 6]");
}
const profiles = options.get("--profile")?.split(",") ?? [];
const knownProfiles = [
  "human_loopback", "human_server_proxy", "ai_direct_core", "ai_server_proxy",
  "human_browser_pause_400ms", "human_browser_pause_3000ms",
  "core_input_pause_400ms", "core_input_pause_3000ms",
  "core_write_pause_400ms", "core_write_pause_3000ms", "core_transport_reset",
];
if (profiles.some(profile => !knownProfiles.includes(profile))) throw new Error("Unknown benchmark profile");
const testPattern = "^TestInternalAudioPipelineBenchmark$" + (profiles.length ? `/^(${profiles.join("|")})$` : "");
const calls = Number(options.get("--calls") ?? "24");
const seconds = Number(options.get("--seconds") ?? "6");
if (!Number.isInteger(calls) || calls < 2 || calls > 64 || !Number.isInteger(seconds) || seconds < 6 || seconds > 30) {
  throw new Error("calls 2..64 and seconds 6..30 required");
}
// Preserve previous reports, especially failures. No generated files in the repo.
const output = resolve(options.get("--output") ?? join(tmpdir(), `telephony-internal-audio-results-${Date.now()}`));
await mkdir(output, { recursive: false });
const temp = await mkdtemp(join(tmpdir(), "telephony-internal-audio-build-"));
const env: NodeJS.ProcessEnv = { ...process.env, GOWORK: "off", TELEMETRY_URL: "", TELEMETRY_LIVE_URL: "", SERVER_URL: "" };

async function capture(command: string[], cwd: string) {
  const child = Bun.spawn(command, { cwd, env, stdout: "pipe", stderr: "pipe" });
  const [out, err, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
  if (code) throw new Error(`${command[0]} failed (${code}): ${err}`);
  return out.trim();
}
async function hashes(cwd: string) {
  // Include dirty local sources used by overlays, not only commit identities.
  const files = (await capture(["rg", "--files", "-g", "*.go", "-g", "go.mod", "-g", "go.sum"], cwd)).split("\n").sort();
  const result: Record<string, string> = {};
  for (const file of files) {
    result[file] = new Bun.CryptoHasher("sha256").update(await Bun.file(join(cwd, file)).arrayBuffer()).digest("hex");
  }
  return result;
}
const metadata: Record<string, unknown> = {
  platform: process.platform, arch: process.arch, cpu_count: cpus().length,
  bun_version: Bun.version, calls, seconds, profiles: profiles.length ? profiles : knownProfiles,
};
const snapshots = new Map<string, { cwd: string; hashes: Record<string, string> }>();

try {
  for (const component of ["telephony", "core", "server"] as const) {
    const cwd = component === "telephony" ? root : resolve(options.get(`--${component}`)!);
    const sourceHashes = await hashes(cwd);
    snapshots.set(component, { cwd, hashes: sourceHashes });
    metadata[component] = {
      root: cwd, revision: await capture(["git", "rev-parse", "HEAD"], cwd),
      dirty: (await capture(["git", "status", "--porcelain", "--untracked-files=no"], cwd)).length > 0,
      go_version: await capture(["go", "version"], cwd), source_sha256: sourceHashes,
    };
    if (component === "telephony") continue;
    const virtual = join(cwd, "zz_telephony_internal_audio_fixture_test.go");
    if (await Bun.file(virtual).exists()) throw new Error(`Fixture filename occupied: ${virtual}`);
    const originalFixture = join(import.meta.dir, `${component}_fixture.go.txt`);
    const fixture = join(temp, `${component}-fixture.go`);
    const fixtureBytes = await Bun.file(originalFixture).arrayBuffer();
    await Bun.write(fixture, fixtureBytes);
    (metadata[component] as Record<string, unknown>).fixture_sha256 = new Bun.CryptoHasher("sha256").update(fixtureBytes).digest("hex");
    const overlay = join(temp, `${component}-overlay.json`);
    const binary = join(temp, `${component}-fixture`);
    await Bun.write(overlay, JSON.stringify({ Replace: { [virtual]: fixture } }));
    console.log(`Building actual ${component} test fixture with an overlay; repository files stay untouched.`);
    const child = Bun.spawn(["go", "test", "-c", "-overlay", overlay, "-o", binary, "."], { cwd, env, stdout: "pipe", stderr: "pipe" });
    const [out, err, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
    await Bun.write(join(output, `${component}-build.log`), out + err);
    if (code) throw new Error(`${component} fixture build failed; see ${component}-build.log`);
    env[`TELEPHONY_INTERNAL_${component.toUpperCase()}_HELPER`] = binary;
  }
  await Bun.write(join(output, "source.json"), JSON.stringify(metadata, null, 2) + "\n");
  let last = cpus().map(cpu => cpu.times);
  const host: unknown[] = [];
  const timer = setInterval(() => {
    const now = cpus().map(cpu => cpu.times);
    let total = 0, idle = 0;
    now.forEach((cpu, i) => {
      for (const key of ["user", "nice", "sys", "idle", "irq"] as const) total += cpu[key] - last[i][key];
      idle += cpu.idle - last[i].idle;
    });
    last = now;
    host.push({ at: new Date().toISOString(), cpu_busy_pct: total ? 100 * (total - idle) / total : 0, load_average: loadavg(), free_memory_bytes: freemem() });
  }, 1000);
  try {
    const child = Bun.spawn(["go", "test", "-tags", "integration", "-run", testPattern, "-count=1", "-timeout", "15m", "-v", "."], {
      cwd: root,
      env: { ...env, TELEPHONY_INTERNAL_OUTPUT: output, TELEPHONY_INTERNAL_CALLS: String(calls), TELEPHONY_INTERNAL_SECONDS: String(seconds) },
      stdout: "inherit", stderr: "inherit",
    });
    process.exitCode = await child.exited;
  } finally {
    clearInterval(timer);
    await Bun.write(join(output, "host-load.json"), JSON.stringify(host, null, 2) + "\n");
  }
  for (const [component, snapshot] of snapshots) {
    if (JSON.stringify(await hashes(snapshot.cwd)) !== JSON.stringify(snapshot.hashes)) {
      process.exitCode = 1;
      console.error(`${component} source changed during benchmark; repeat with stable sources.`);
    }
  }
  console.log(`Results: ${output}`);
} finally {
  await rm(temp, { recursive: true, force: true });
}
