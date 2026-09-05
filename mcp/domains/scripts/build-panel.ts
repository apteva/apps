const result = await Bun.build({
  entrypoints: [new URL('../ui/DomainsPanel.tsx', import.meta.url).pathname],
  outdir: new URL('../ui', import.meta.url).pathname,
  target: 'browser',
  format: 'esm',
  minify: true,
  sourcemap: 'external',
  external: ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', '@apteva/ui-kit'],
  define: {'process.env.NODE_ENV': '"production"'},
  naming: '[name].mjs',
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
console.log('Built DomainsPanel.mjs and source map');
