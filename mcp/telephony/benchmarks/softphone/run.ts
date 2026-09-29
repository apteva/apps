import {resolve, join} from 'node:path';
import {mkdir} from 'node:fs/promises';

const root = resolve(import.meta.dir, '../..');
const args = process.argv.slice(2);
if (args.includes('--help')) {
  console.log('bun run benchmark:softphone [--profiles all|name,name] [--seconds 12] [--seed 20260929] [--output /absolute/path]');
  process.exit(0);
}
const options = new Map<string, string>();
for (let i = 0; i < args.length; i += 2) {
  const key = args[i];
  if (!['--profiles', '--seconds', '--seed', '--output'].includes(key)) throw new Error(`Unknown option: ${key}`);
  if (!args[i + 1] || args[i + 1].startsWith('--')) throw new Error(`Missing value for ${key}`);
  if (options.has(key)) throw new Error(`Duplicate option: ${key}`);
  options.set(key, args[i + 1]);
}
const seconds = Number(options.get('--seconds') ?? '12');
if (!Number.isInteger(seconds) || seconds < 8 || seconds > 60) throw new Error('--seconds must be an integer from 8 to 60');
const seed = options.get('--seed') ?? '20260929';
if (!/^-?\d+$/.test(seed) || !Number.isSafeInteger(Number(seed))) throw new Error('--seed must be a safe integer');
const profiles = options.get('--profiles') ?? 'all';
const available: {name: string}[] = await Bun.file(join(import.meta.dir, 'profiles.json')).json();
if (profiles !== 'all') {
  for (const name of profiles.split(',')) {
    if (!available.some(profile => profile.name === name)) throw new Error(`Unknown profile: ${name}`);
  }
}
const output = resolve(options.get('--output') ?? join(root, 'benchmarks/softphone/results', new Date().toISOString().replace(/[:.]/g, '-')));
await mkdir(output, {recursive: true});
const built = await Bun.build({entrypoints: [join(import.meta.dir, 'browser-entry.ts')], target: 'browser', format: 'esm'});
if (!built.success) throw new AggregateError(built.logs, 'Benchmark browser build failed');
await Bun.write(join(output, 'browser-entry.js'), built.outputs[0]);
const env: NodeJS.ProcessEnv = {
  ...process.env,
  GOWORK: 'off',
  TELEPHONY_BENCHMARK_OUTPUT: output,
  TELEPHONY_BENCHMARK_SECONDS: String(seconds),
  TELEPHONY_BENCHMARK_PROFILES: profiles,
  TELEPHONY_BENCHMARK_SEED: seed,
};
delete env.TELEPHONY_DEPLOYED_FRONTEND;
delete env.RUN_TELEPHONY_LIVE_CARRIER;
const child = Bun.spawn(['go', 'test', '-tags', 'integration', '-run', '^TestSoftphoneNetworkBenchmark$', '-count=1', '-timeout', '30m', '-v', '.'], {
  cwd: root, env, stdout: 'inherit', stderr: 'inherit',
});
const code = await child.exited;
console.log(`Benchmark artifacts: ${output}`);
process.exit(code);
