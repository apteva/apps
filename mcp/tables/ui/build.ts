const result = await Bun.build({
  entrypoints: [
    "TablesPanel.tsx",
    "TableCard.tsx",
    "RowCard.tsx",
    "TableRowListCard.tsx",
  ].map((name) => `${import.meta.dir}/${name}`),
  outdir: import.meta.dir,
  target: "browser",
  format: "esm",
  minify: true,
  sourcemap: "external",
  external: [
    "react",
    "react/jsx-runtime",
    "react/jsx-dev-runtime",
    "@apteva/ui-kit",
  ],
  define: { "process.env.NODE_ENV": '"production"' },
  naming: "[name].mjs",
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
for (const output of result.outputs)
  console.log(`${output.path}: ${output.size} bytes`);
