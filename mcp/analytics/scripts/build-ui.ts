// Standalone Analytics build for CI. The repository build-panels script also
// verifies these bundles against the sibling dashboard's actual React exports.
for (const entry of ["AnalyticsPanel", "AnalyticsDashboardWidget"]) {
  const result = await Bun.build({
    entrypoints: [`ui/${entry}.tsx`], outdir: "ui", target: "browser",
    format: "esm", minify: true, sourcemap: "external",
    external: ["react", "react/jsx-runtime", "react/jsx-dev-runtime", "@apteva/ui-kit"],
    define: { "process.env.NODE_ENV": '"production"' }, naming: "[name].mjs",
  });
  if (!result.success) { for (const log of result.logs) console.error(log); process.exit(1); }
  console.log(`Built ${entry}.mjs`);
}
