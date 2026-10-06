import { resolve } from "node:path";

const root = resolve(import.meta.dir, "..");
const result = await Bun.build({
  entrypoints: [resolve(root, "ui/StudioPanel.tsx")],
  outdir: resolve(root, "ui"),
  naming: "StudioPanel.mjs",
  target: "browser",
  format: "esm",
  minify: true,
  external: ["react", "react/jsx-runtime", "react/jsx-dev-runtime", "@apteva/ui-kit"],
  // The host import map provides the production JSX runtime, even in dev mode.
  define: { "process.env.NODE_ENV": '"production"' },
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
const output = await Bun.file(resolve(root, "ui/StudioPanel.mjs")).text();
if (/jsxDEV|react\/jsx-dev-runtime/.test(output)) {
  throw new Error("Panel contains development JSX; the dashboard cannot load it");
}
console.log("Built StudioPanel.mjs with the dashboard's production React runtime");
