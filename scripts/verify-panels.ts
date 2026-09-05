// Regression check: every built panel must be importable against the
// dashboard's vendor react.mjs. Catches the class of bug where a
// panel uses a hook the vendor doesn't re-export — symptom in the
// browser is a cryptic SyntaxError at panel-mount time. Running this
// after build-panels.ts makes it a build-time failure instead.
//
// Strategy:
//   1. Load the dashboard's vendor/react.entry.ts → grab the set of
//      named exports it produces.
//   2. For every mcp/<app>/ui/*Panel.mjs, scan the import declaration
//      that targets "react" and verify each named import is in (1).
//   3. Same for "react/jsx-runtime" — expects `jsx`, `jsxs`, `Fragment`.
//
// We don't try to actually run the panels (they need a browser DOM
// and host props). Static import-spec parsing is enough to catch the
// vendor-export-mismatch class of bug — every other class would have
// already failed during the build.

import { readdir, readFile } from "fs/promises";
import { existsSync } from "fs";
import { join } from "path";

const ROOT = new URL("..", import.meta.url).pathname;
const DASHBOARD_VENDOR = join(
  process.env.APTEVA_DASHBOARD_DIR || join(ROOT, "..", "dashboard"),
  "vendor",
  "react.entry.ts",
);
const MCP_DIR = join(ROOT, "mcp");
const PORTABLE_VENDOR_SURFACE = join(ROOT, "scripts", "panel-react-surface.json");

async function vendorExports(): Promise<Set<string>> {
  if (!existsSync(DASHBOARD_VENDOR)) {
    const portable = JSON.parse(await readFile(PORTABLE_VENDOR_SURFACE, "utf8"));
    if (!Array.isArray(portable) || !portable.every((name) => typeof name === "string")) {
      throw new Error(`invalid portable vendor surface: ${PORTABLE_VENDOR_SURFACE}`);
    }
    console.log(`dashboard vendor unavailable; using portable React surface contract`);
    return new Set(portable);
  }
  const src = await readFile(DASHBOARD_VENDOR, "utf8");
  const names = new Set<string>();
  // Old hand-written format: `export const { useFoo, useBar, … } = React;`
  const destruct = src.match(/export\s+const\s*\{([\s\S]*?)\}\s*=\s*React/);
  if (destruct) {
    const cleaned = destruct[1].replace(/\/\/[^\n]*/g, "");
    for (const tok of cleaned.split(",")) {
      const name = tok.trim();
      if (/^[A-Za-z_]\w*$/.test(name)) names.add(name);
    }
  }
  // Auto-generated format (scripts/gen-vendor-entries.ts): one
  // `export const X = (Mod as any).X;` per name. Pick those up too —
  // otherwise the verifier sees an empty surface and aborts.
  const perName = /export\s+const\s+([A-Za-z_]\w*)\s*=/g;
  let pm: RegExpExecArray | null;
  while ((pm = perName.exec(src))) names.add(pm[1]);
  if (/export\s+default\b/.test(src)) names.add("default");
  return names;
}

// Pull each name out of a panel's `import { … } from "react"` line.
// Handles renames (`useState as foo`), trailing commas, multi-line.
function parseImportNames(spec: string): string[] {
  const inside = spec.replace(/^\s*\{/, "").replace(/\}\s*$/, "");
  const out: string[] = [];
  for (const tok of inside.split(",")) {
    const cleaned = tok.replace(/\s+as\s+\w+/, "").trim();
    if (cleaned && /^[A-Za-z_]\w*$/.test(cleaned)) out.push(cleaned);
  }
  return out;
}

async function checkPanel(file: string, vendor: Set<string>, requireDefault: boolean): Promise<string[]> {
  const src = await readFile(file, "utf8");
  const errors: string[] = [];
  // Every panel must have a default export — that's what the
  // dashboard's resolvePanelComponent imports and mounts. Bun
  // minifies `export default function FooPanel(...)` to either
  // `export{X as default}` or a literal `export default …`.
  const hasDefault =
    /export\s*\{[^}]*\bas\s+default\b[^}]*\}/.test(src) ||
    /export\s+default\b/.test(src);
  if (requireDefault && !hasDefault) {
    errors.push(
      `${file}: no default export. Convert "export function FooPanel(...)" to "export default function FooPanel(...)" — the dashboard's panel loader imports the default.`,
    );
  }
  // Parse every `import { … } from "react"` and `… from "react/jsx-runtime"`.
  const reactRe = /import\s*(\{[^}]*\}|\w+)\s*from\s*"react"/g;
  let m: RegExpExecArray | null;
  while ((m = reactRe.exec(src))) {
    const spec = m[1];
    if (!spec.startsWith("{")) continue; // default-only — covered below
    for (const name of parseImportNames(spec)) {
      if (!vendor.has(name)) {
        errors.push(
          `${file}: imports "${name}" from "react" but vendor/react.entry.ts doesn't re-export it. Add ${name} to the destructured export const block.`,
        );
      }
    }
  }
  // jsx-runtime: panels run against the dashboard import map, which
  // exposes the production runtime only. Dev-runtime output imports
  // jsxDEV and crashes at panel mount time.
  const jsxRe = /import\s*\{([^}]+)\}\s*from\s*"react\/jsx(-dev)?-runtime"/g;
  while ((m = jsxRe.exec(src))) {
    if (m[2]) {
      errors.push(
        `${file}: imports react/jsx-dev-runtime. Rebuild with process.env.NODE_ENV defined as "production".`,
      );
      continue;
    }
    for (const name of parseImportNames("{" + m[1] + "}")) {
      if (!["jsx", "jsxs", "Fragment"].includes(name)) {
        errors.push(
          `${file}: jsx-runtime import "${name}" — only jsx/jsxs/Fragment are valid.`,
        );
      }
    }
  }
  return errors;
}

async function main() {
  const vendor = await vendorExports();
  if (vendor.size === 0) {
    console.error("verify-panels: no exports found in dashboard/vendor/react.entry.ts");
    process.exit(1);
  }
  console.log(`vendor surface: ${vendor.size} names (${Array.from(vendor).sort().slice(0, 6).join(", ")}…)`);

  const apps = await readdir(MCP_DIR, { withFileTypes: true });
  const failures: string[] = [];
  let checked = 0;
  for (const a of apps) {
    if (!a.isDirectory()) continue;
    const flag=Bun.argv.indexOf("--app");
    if(flag>=0 && a.name!==Bun.argv[flag+1])continue;
    const uiDir = join(MCP_DIR, a.name, "ui");
    if (!existsSync(uiDir)) continue;
    const entries = await readdir(uiDir);
    for (const f of entries) {
      if (!f.endsWith(".mjs") || f.endsWith(".mjs.map")) continue;
      const sourceName = f.replace(/\.mjs$/, ".tsx");
      const errs = await checkPanel(join(uiDir, f), vendor, existsSync(join(uiDir, sourceName)));
      if (errs.length > 0) {
        failures.push(...errs);
      } else {
        console.log(`✓ ${a.name}/ui/${f}`);
      }
      checked++;
    }
  }
  if (checked === 0) {
    console.log("no panels found — nothing to verify");
    return;
  }
  if (failures.length > 0) {
    console.error("\n" + failures.length + " problem(s):");
    for (const f of failures) console.error("  ✗ " + f);
    process.exit(1);
  }
  console.log(`\n${checked} panel(s) all importable against the host React surface.`);
}

await main();
