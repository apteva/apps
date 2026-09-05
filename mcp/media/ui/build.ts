// Build only Media's shipped UI entries, without touching other apps.
const root = new URL('.', import.meta.url).pathname;
for (const name of ['MediaPanel', 'MediaCard', 'RenderCard', 'TranscriptCard']) {
  const result = await Bun.build({
    entrypoints: [`${root}/${name}.tsx`], outdir: root,
    target: 'browser', format: 'esm', minify: true, sourcemap: 'external',
    external: ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', '@apteva/ui-kit'],
    define: {'process.env.NODE_ENV': '"production"'}, naming: '[name].mjs',
  });
  if (!result.success) throw new Error(result.logs.join('\n'));
  console.log(`${name} built`);
}
