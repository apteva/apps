const builds = [
  { entrypoints: ['./SportsbookPanel.tsx'], external: ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime'], naming: '[name].mjs' },
  { entrypoints: ['./src/standalone.tsx'], external: [], naming: 'app.mjs' },
];
for (const options of builds) {
  const result = await Bun.build({ ...options, outdir: '.', target: 'browser', format: 'esm', minify: true, define: {'process.env.NODE_ENV': '"production"'} });
  if (!result.success) { for (const log of result.logs) console.error(log); process.exit(1); }
  for (const output of result.outputs) console.log(output.path, output.size);
}

export {};
