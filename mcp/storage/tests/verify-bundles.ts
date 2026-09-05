const parser = new Bun.Transpiler({ loader: 'js' });
const allowed = new Set(['react', 'react/jsx-runtime', '@apteva/ui-kit']);
for (const file of ['ui/StoragePanel.mjs', 'ui/FileCard.mjs']) {
  const source = await Bun.file(file).text();
  const scanned = parser.scan(source);
  if (!scanned.exports.includes('default')) throw new Error(`${file}: missing default export`);
  for (const entry of scanned.imports) {
    if (!allowed.has(entry.path)) throw new Error(`${file}: unsupported host import ${entry.path}`);
  }
  if (/\bjsxDEV\b/.test(source)) throw new Error(`${file}: development JSX in release output`);
  console.log(`${file}: production host imports verified`);
}
