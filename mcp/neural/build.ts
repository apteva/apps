// The native project.page panel uses Apteva's React import map.
const root = new URL('.', import.meta.url).pathname;
const result = await Bun.build({
  entrypoints: [root + 'ui/NeuralPanel.tsx'],
  outdir: root + 'ui', naming: '[name].mjs',
  target: 'browser', format: 'esm', minify: true, sourcemap: 'external',
  external: ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', '@apteva/ui-kit'],
  define: { 'process.env.NODE_ENV': '"production"' },
});
if (!result.success) throw new Error(result.logs.join('\n'));
console.log('Built native Apteva panel: ui/NeuralPanel.mjs');
