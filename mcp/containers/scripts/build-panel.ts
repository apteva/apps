// Build only Containers; other apps may have unrelated stale bundles.
const ui = new URL("../ui/", import.meta.url).pathname;
const result = await Bun.build({
  entrypoints: [ui + "ContainersPanel.tsx"], outdir: ui, target: "browser",
  format: "esm", minify: true, sourcemap: "external", naming: "[name].mjs",
  external: ["react", "react/jsx-runtime", "react/jsx-dev-runtime", "@apteva/ui-kit"],
  define: { "process.env.NODE_ENV": '"production"' },
});
if (!result.success) throw new AggregateError(result.logs, "Containers panel build failed");
const bundle = await Bun.file(ui + "ContainersPanel.mjs").text();
if (bundle.includes('from"react/jsx-dev-runtime"')) throw new Error("Development JSX runtime in panel");
const map = await Bun.file(ui + "ContainersPanel.mjs.map").json();
if (!map.sourcesContent.includes(await Bun.file(ui + "ContainersPanel.tsx").text())) throw new Error("Stale panel source map");
console.log("Containers panel and source map verified");
