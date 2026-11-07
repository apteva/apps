// Rebuild an extracted manufacturing ZIP without the original Design database.
// Usage: bun replay.mjs /absolute/path/to/extracted-package [/output/directory]
import { readFile, mkdir, realpath } from "node:fs/promises";
import { resolve, relative, isAbsolute, join } from "node:path";
const root = await realpath(process.argv[2] || ".");
const definition = JSON.parse(
  await readFile(join(root, "resolved-design.json"), "utf8"),
);
const manifest = JSON.parse(
  await readFile(join(root, "manifest.json"), "utf8"),
);
const parameters = JSON.parse(
  await readFile(join(root, "parameters.json"), "utf8"),
);
const checksums = JSON.parse(
  await readFile(join(root, "checksums.json"), "utf8"),
);
async function sha(path) {
  return new Bun.CryptoHasher("sha256")
    .update(await readFile(path))
    .digest("hex");
}
for (const [name, expected] of Object.entries(checksums)) {
  const p = await realpath(resolve(root, name)),
    rel = relative(root, p);
  if (isAbsolute(rel) || rel === ".." || rel.startsWith("../"))
    throw new Error("Package file escapes root");
  if ((await sha(p)) !== expected)
    throw new Error(`Package checksum mismatch: ${name}`);
}
async function walk(d) {
  for (const asset of Object.values(d._assets || {}))
    for (const c of asset.metadata.components) {
      const p = await realpath(resolve(root, asset.files[c.id]));
      const rel = relative(root, p);
      if (isAbsolute(rel) || rel === ".." || rel.startsWith("../"))
        throw new Error("Source asset escapes package directory");
      if ((await sha(p)) !== c.sha256)
        throw new Error(`Component hash mismatch: ${c.id}`);
      asset.files[c.id] = p;
      if (asset.meshes?.[c.id]) {
        const path = await realpath(resolve(root, asset.meshes[c.id]));
        const rel = relative(root, path);
        if (isAbsolute(rel) || rel === ".." || rel.startsWith("../"))
          throw new Error("Mesh cache escapes package");
        if ((await sha(path)) !== c.mesh_sha256)
          throw new Error("Mesh cache checksum mismatch");
        asset.meshes[c.id] = path;
      }
    }
  for (const c of d._resolved_components || []) await walk(c.definition);
}
await walk(definition);
const out = resolve(process.argv[3] || join(root, "rebuilt"));
await mkdir(out, { recursive: true });
const runner = join(import.meta.dir, "dist", "runner.mjs");
const child = Bun.spawn(["bun", runner], {
  stdin: "pipe",
  stdout: "inherit",
  stderr: "inherit",
});
child.stdin.write(
  JSON.stringify({
    definition,
    parameters,
    expected_engine_version: manifest.engine,
    name: "Rebuilt package",
    formats: ["mesh-json", "step", "stl"],
    output_dir: out,
  }),
);
child.stdin.end();
const code = await child.exited;
if (code) throw new Error(`Geometry replay failed (${code})`);
console.error(
  `Replayed source revision ${manifest.revision_id} with the installed Design runner. Compare manifest engine version before treating results as release-equivalent.`,
);
