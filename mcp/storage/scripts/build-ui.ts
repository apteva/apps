const result = await Bun.build({
  entrypoints: ['ui/StoragePanel.tsx', 'ui/FileCard.tsx'],
  outdir: 'ui',
  target: 'browser',
  format: 'esm',
  external: ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', '@apteva/ui-kit'],
  // Minification alone does not switch Bun's JSX compiler to production.
  define: { 'process.env.NODE_ENV': '"production"' },
  naming: '[name].mjs',
  minify: true,
  sourcemap: 'external',
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
for (const output of result.outputs) console.log(`${output.path}: ${output.size} bytes`);
