import { join } from "node:path";
import { mkdir, readdir, unlink } from "node:fs/promises";
const root = import.meta.dir;
const out = join(root, "../ui/frontend");
await mkdir(out, { recursive: true });
const result = await Bun.build({ entrypoints: [join(root, "client-entry.ts")], target: "browser", format: "esm", minify: true });
if (!result.success) throw new AggregateError(result.logs, "Telephony client build failed");
const source = await result.outputs[0].text();
const sha256 = new Bun.CryptoHasher("sha256").update(source).digest("hex");
const filename = `client-${sha256.slice(0, 16)}.mjs`;
await Bun.write(join(out, filename), source);
const version = (await Bun.file(join(root, "../apteva.yaml")).text()).match(/^version:\s*(\S+)/m)![1];
await Bun.write(join(root, "../ui/frontend.json"), JSON.stringify({
  schema: "apteva-app-frontend/v1", app: "telephony", version,
  client: { path: `/ui/frontend/${filename}`, sha256 },
}, null, 2) + "\n");
for (const file of await readdir(out)) {
  if (file !== filename && /^client-[a-f0-9]{16}\.mjs$/.test(file)) await unlink(join(out, file));
}
console.log(`Built Telephony headless client (${source.length} bytes); no React or SDK changes.`);
