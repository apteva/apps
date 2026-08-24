// Build every Apteva app panel (TSX source) into an ESM bundle the
// dashboard can dynamically import.
//
// Convention: each app keeps its panel source at
//   mcp/<app>/ui/<Name>Panel.tsx
// This script bundles each .tsx alongside the source as
//   mcp/<app>/ui/<Name>Panel.mjs
// externalizing `react` and `react/jsx-runtime` so panels resolve
// those from the dashboard's importmap at runtime instead of
// shipping their own React copy.
//
// Run: `bun run scripts/build-panels.ts`
//
// We don't have a workspace package.json — bun strips TypeScript at
// build time without needing the type packages installed. If you
// want IDE completions, drop a tsconfig + @types/react under the
// app you're editing.

import { readdir, unlink } from "fs/promises";
import { existsSync } from "fs";
import { join, dirname, basename } from "path";

const ROOT = new URL("..", import.meta.url).pathname;
const MCP_DIR = join(ROOT, "mcp");
const appFlag = Bun.argv.indexOf("--app");
const requestedApp = appFlag >= 0 ? Bun.argv[appFlag + 1] : "";

async function findPanels(): Promise<string[]> {
  const apps = await readdir(MCP_DIR, { withFileTypes: true });
  const out: string[] = [];
  for (const a of apps) {
    if (!a.isDirectory()) continue;
    if (requestedApp && a.name !== requestedApp) continue;
    const uiDir = join(MCP_DIR, a.name, "ui");
    if (!existsSync(uiDir)) continue;
    const entries = await readdir(uiDir);
    for (const f of entries) {
      // Panel = full-pane dashboard surface; Widget = small slot
      // surface; Card = chat-attached UIComponent (file-card,
      // post-card, …). All bundle the same way — just different
      // naming conventions so the discovery glob is explicit
      // about what counts as bundleable UI.
      if (f.endsWith("Panel.tsx") || f.endsWith("Widget.tsx") || f.endsWith("Card.tsx")) {
        out.push(join(uiDir, f));
      }
    }
  }
  return out;
}

async function main() {
  const panels = await findPanels();
  if (panels.length === 0) {
    console.log("no panels found under mcp/*/ui/");
    return;
  }
  // Run the build…
  console.log(`Found ${panels.length} panel source(s):`);
  for (const p of panels) console.log("  ", p.replace(ROOT, ""));

  const grouped = new Map<string, string[]>();
  for (const panel of panels) {
    const directory = dirname(panel);
    grouped.set(directory, [...(grouped.get(directory) || []), panel]);
  }

  const build = async (sources: string[], split: boolean) => {
    const outDir = dirname(sources[0]);
    if (split) {
      for (const file of await readdir(outDir)) {
        if (file.startsWith("shared-") && (file.endsWith(".mjs") || file.endsWith(".mjs.map"))) {
          await unlink(join(outDir, file));
        }
      }
    }
    const result = await Bun.build({
      entrypoints: sources,
      outdir: outDir,
      target: "browser",
      format: "esm",
      minify: true,
      sourcemap: "external",
      splitting: split,
      external: ["react", "react/jsx-runtime", "react/jsx-dev-runtime", "@apteva/ui-kit"],
      define: {
        "process.env.NODE_ENV": '"production"',
      },
      naming: split
        ? { entry: "[name].mjs", chunk: "shared-[hash].mjs" }
        : "[name].mjs",
    });
    if (!result.success) {
      console.error(`✗ ${sources.map(basename).join(", ")}`);
      for (const log of result.logs) console.error("  ", log);
      process.exit(1);
    }
    for (const src of sources) {
      const outputName = basename(src).replace(/\.tsx$/, ".mjs");
      const out = result.outputs.find((item) => basename(item.path) === outputName);
      const size = out ? (out.size / 1024).toFixed(1) + " KB" : "?";
      console.log(`✓ ${src.replace(ROOT, "")} → ${outputName} (${size})`);
    }
  };

  for (const [uiDir, sources] of grouped) {
    if (existsSync(join(uiDir, "split-bundle.json"))) {
      await build(sources, true);
      continue;
    }
    for (const src of sources) {
      await build([src], false);
    }
  }

  // Verification — every named import in every built panel must be
  // re-exported by the dashboard's vendor/react.entry.ts. Skipping
  // this check is how we shipped a Storage build that referenced
  // useCallback against a vendor that only had a default export.
  console.log("\nVerifying panel imports against host React surface…");
  const verifyURL = new URL("./verify-panels.ts", import.meta.url).pathname;
  const proc = Bun.spawn(["bun", "run", verifyURL], {
    stdout: "inherit",
    stderr: "inherit",
  });
  const code = await proc.exited;
  if (code !== 0) {
    process.exit(code);
  }
}

await main();
