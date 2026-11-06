import { mkdir, readdir, unlink } from "node:fs/promises";
import { join } from "node:path";

const root = import.meta.dir;
const ui = join(root, "../ui");
const out = join(ui, "frontend");
await mkdir(out, { recursive: true });

async function bundle(entry: string, format: "esm" | "iife") {
  const result = await Bun.build({
    entrypoints: [join(root, entry)], target: "browser", format, minify: true,
  });
  if (!result.success) throw new AggregateError(result.logs, "Calls frontend build failed");
  return result.outputs[0].text();
}

const [source, guest, manifest] = await Promise.all([
  bundle("index.ts", "esm"),
  bundle("guest.ts", "iife"),
  Bun.file(join(root, "../apteva.yaml")).text(),
]);
const version = manifest.match(/^version:\s*(\S+)/m)?.[1];
if (!version) throw new Error("Calls manifest has no version");

const sha256 = new Bun.CryptoHasher("sha256").update(source).digest("hex");
const filename = "client-" + sha256.slice(0, 16) + ".mjs";
await Bun.write(join(out, filename), source);
await Bun.write(join(ui, "join.js"), guest);
await Bun.write(join(ui, "frontend.json"), JSON.stringify({
  schema: "apteva-app-frontend/v1", app: "calls", version,
  client: { path: "/ui/frontend/" + filename, sha256 },
}, null, 2) + "\n");
for (const file of await readdir(out)) {
  if (file !== filename && /^client-[a-f0-9]{16}\.mjs$/.test(file)) await unlink(join(out, file));
}
