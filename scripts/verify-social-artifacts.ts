// Bun can choose different names for imported bindings on Linux and macOS.
// Check source-map inputs and allow only a consistent rename of those bindings;
// changes to generated behavior, imports, assets, or source inputs still fail.
import { readdir } from "node:fs/promises";

export function comparableCode(source: string): string {
  return source.replace(/^\/\/# debugId=.*$/gm, "").trim();
}
export function equivalentBindings(original: string, rebuilt: string): boolean {
  const a = comparableCode(original), b = comparableCode(rebuilt);
  if (a === b) return true;
  const bindings = (code: string) => {
    const out = new Map<string, string>();
    for (const match of code.matchAll(/import\{([^}]+)\}from"([^"]+)"/g)) {
      for (const spec of match[1].split(",")) {
        const terms = spec.trim().split(/\s+as\s+/);
        out.set(`${match[2]}:${terms[0]}`, terms.at(-1)!);
      }
    }
    return out;
  };
  const before = bindings(a), after = bindings(b), renames = new Map<string, string>();
  if (before.size !== after.size) return false;
  for (const [key, value] of before) {
    if (!after.has(key)) return false;
    renames.set(value, after.get(key)!);
  }
  return a.replace(/[A-Za-z_$][\w$]*/g, (name) => renames.get(name) ?? name) === b;
}

async function main() {
  const dir = "mcp/social/ui";
  const git = (...args: string[]) => {
    const result = Bun.spawnSync(["git", ...args]);
    if (result.exitCode !== 0) throw new Error(result.stderr.toString());
    return result.stdout.toString();
  };
  const tracked = git("ls-files", `${dir}/*.mjs`).trim().split("\n").sort();
  const generated = (await readdir(dir)).filter((name) => name.endsWith(".mjs")).map((name) => `${dir}/${name}`).sort();
  if (JSON.stringify(tracked) !== JSON.stringify(generated)) throw new Error("Social bundle set differs from committed assets");
  const parser = new Bun.Transpiler({ loader: "js" });
  for (const path of tracked) {
    const original = git("show", `HEAD:${path}`), rebuilt = await Bun.file(path).text();
    parser.scan(rebuilt);
    const a = JSON.parse(git("show", `HEAD:${path}.map`));
    const b = await Bun.file(`${path}.map`).json();
    if (JSON.stringify([a.sources, a.sourcesContent]) !== JSON.stringify([b.sources, b.sourcesContent])) throw new Error(`${path}: source inputs differ`);
    if (!equivalentBindings(original, rebuilt)) throw new Error(`${path}: generated code differs beyond imported-binding names`);
  }
  console.log(`Verified ${tracked.length} Social bundles against committed sources and code.`);
}
if (import.meta.main) await main();
