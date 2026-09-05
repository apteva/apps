import { resolve } from 'node:path';

const external = ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', 'react-dom', 'react-dom/client', '@apteva/ui-kit'];
const define = { 'process.env.NODE_ENV': '"production"' };
const built = await Bun.build({
  entrypoints: ['tests/browser/harness.tsx'], target: 'browser', format: 'esm', define,
  external: [...external, '/api/apps/storage/ui/StoragePanel.mjs', '/api/apps/storage/ui/FileCard.mjs'],
});
if (!built.success) throw new Error(String(built.logs));

// Production JSX intentionally has no jsxDEV export, matching the dashboard.
const assets = new Map<string, Blob | string>();
assets.set('/test.js', built.outputs[0]);
const hostBase = process.env.STORAGE_TEST_HOST_URL;
const vendorEntries = {
  'react.mjs': 'react.ts',
  'react-jsx-runtime.mjs': 'jsx-runtime.ts',
  'react-dom-client.mjs': 'react-dom-client.ts',
  'react-dom.mjs': 'react-dom.ts',
  'ui-kit.mjs': '../kit.tsx',
};
if (hostBase) {
  for (const file of Object.keys(vendorEntries)) {
    const response = await fetch(new URL('/vendor/' + file, hostBase));
    if (!response.ok) throw new Error(`Host vendor ${file}: HTTP ${response.status}`);
    assets.set('/vendor/' + file, await response.text());
  }
} else {
  for (const [file, entry] of Object.entries(vendorEntries)) {
    const result = await Bun.build({
      entrypoints: [resolve(import.meta.dir, 'vendor', entry)],
      target: 'browser', format: 'esm', define,
      external: file === 'react.mjs' ? [] : file === 'react-jsx-runtime.mjs' || file === 'react-dom.mjs' ? ['react'] : external,
    });
    if (!result.success) throw new Error(String(result.logs));
    assets.set('/vendor/' + file, result.outputs[0]);
  }
}
const imports = {
  react: '/vendor/react.mjs',
  'react/jsx-runtime': '/vendor/react-jsx-runtime.mjs',
  'react/jsx-dev-runtime': '/vendor/react-jsx-runtime.mjs',
  'react-dom': '/vendor/react-dom.mjs',
  'react-dom/client': '/vendor/react-dom-client.mjs',
  '@apteva/ui-kit': '/vendor/ui-kit.mjs',
};
const html = `<div id="root"></div><script type="importmap">${JSON.stringify({ imports })}</script><script type="module" src="/test.js"></script>`;
Bun.serve({
  port: 19180, hostname: '127.0.0.1',
  fetch(req) {
    const path = new URL(req.url).pathname;
    // Serve exact release artifacts without rebuilding their TSX sources.
    if (['/api/apps/storage/ui/StoragePanel.mjs', '/api/apps/storage/ui/FileCard.mjs'].includes(path)) {
      return new Response(Bun.file(resolve(import.meta.dir, '../../ui', path.split('/').at(-1)!)), { headers: { 'Content-Type': 'text/javascript' } });
    }
    const asset = assets.get(path);
    if (asset) return new Response(asset, { headers: { 'Content-Type': 'text/javascript' } });
    return path === '/' ? new Response(html, { headers: { 'Content-Type': 'text/html' } }) : new Response('Not found', { status: 404 });
  },
});
