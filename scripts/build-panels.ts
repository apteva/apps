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

import { readdir } from "fs/promises";
import { existsSync } from "fs";
import { join, dirname, basename } from "path";

const ROOT = new URL("..", import.meta.url).pathname;
const MCP_DIR = join(ROOT, "mcp");

async function findPanels(): Promise<string[]> {
  const apps = await readdir(MCP_DIR, { withFileTypes: true });
  const out: string[] = [];
  for (const a of apps) {
    if (!a.isDirectory()) continue;
    const uiDir = join(MCP_DIR, a.name, "ui");
    if (!existsSync(uiDir)) continue;
    const entries = await readdir(uiDir);
    for (const f of entries) {
      if (f.endsWith("Panel.tsx") || f.endsWith("Widget.tsx")) {
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
  console.log(`Found ${panels.length} panel source(s):`);
  for (const p of panels) console.log("  ", p.replace(ROOT, ""));

  for (const src of panels) {
    const outFile = src.replace(/\.tsx$/, ".mjs");
    const outDir = dirname(outFile);
    const result = await Bun.build({
      entrypoints: [src],
      outdir: outDir,
      target: "browser",
      format: "esm",
      minify: true,
      sourcemap: "external",
      // External both prod and dev jsx-runtime — even though we
      // pin NODE_ENV=production below, anyone re-running this
      // script in a dev shell shouldn't accidentally bake a
      // dev-runtime panel that 404s in the dashboard's importmap.
      external: ["react", "react/jsx-runtime", "react/jsx-dev-runtime"],
      // Substitute process.env.NODE_ENV so React's package
      // entrypoint picks the prod jsx-runtime path. Without this
      // Bun emits `import { jsx } from "react/jsx-dev-runtime"`
      // which the host importmap doesn't expose.
      define: {
        "process.env.NODE_ENV": '"production"',
      },
      naming: "[name].mjs",
    });
    if (!result.success) {
      console.error(`✗ ${basename(src)}`);
      for (const log of result.logs) console.error("  ", log);
      process.exit(1);
    }
    const out = result.outputs.find((o) => o.path.endsWith(".mjs"));
    const size = out ? (out.size / 1024).toFixed(1) + " KB" : "?";
    console.log(`✓ ${src.replace(ROOT, "")} → ${basename(outFile)} (${size})`);
  }
}

await main();
