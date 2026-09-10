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
  async fetch(req) {
    const path = new URL(req.url).pathname;
    // For the real large-body test, serve the control API here so Playwright
    // never intercepts/buffers the 2 GiB of browser-to-backend traffic.
    if(path.startsWith('/api/apps/storage/') && !path.includes('/ui/')){
      if(req.method==='PUT')return new Response('Unexpected proxy upload',{status:400});
      const body=req.method==='POST'?await req.text():'';apiReceived+=body.length;
      if(path.endsWith('/uploads'))return Response.json(req.method==='GET'?{max_file_bytes:5*1024**3,max_pending_bytes:5*1024**3}:{upload_id:'STREAMREAL2G',mode:'s3_multipart',part_size:16*1024**2,max_parallel:4,max_parts:10000});
      if(path.includes('/parts/'))return Response.json({url:'http://127.0.0.1:19181/STREAMREAL2G/'+path.split('/').at(-1),headers:{}});
      if(path.endsWith('/complete'))return Response.json({file:{id:10,name:'real-2g.mp4',size_bytes:2*1024**3,folder:'/',content_type:'video/mp4',visibility:'private'}});
      return Response.json(path.endsWith('/folders')?{folders:[]}:{files:[]});
    }
    // Serve exact release artifacts without rebuilding their TSX sources.
    if (['/api/apps/storage/ui/StoragePanel.mjs', '/api/apps/storage/ui/FileCard.mjs'].includes(path)) {
      return new Response(Bun.file(resolve(import.meta.dir, '../../ui', path.split('/').at(-1)!)), { headers: { 'Content-Type': 'text/javascript' } });
    }
    const asset = assets.get(path);
    if (asset) return new Response(asset, { headers: { 'Content-Type': 'text/javascript' } });
    return path === '/' ? new Response(html, { headers: { 'Content-Type': 'text/html' } }) : new Response('Not found', { status: 404 });
  },
});

// Real cross-origin streaming sink for the large-transfer regression. It counts
// bytes without retaining parts, so a 2 GiB test stays bounded in memory.
let apiReceived=0;
const transfers = new Map<string, { bytes: number; parts: number; active: number; peak: number }>();
Bun.serve({
 port:19181, hostname:'127.0.0.1', maxRequestBodySize:128*1024**2,
 async fetch(req){
  const u=new URL(req.url),id=u.pathname.split('/')[1];
  const headers={'Access-Control-Allow-Origin':'http://127.0.0.1:19180','Access-Control-Allow-Methods':'PUT, GET, OPTIONS','Access-Control-Allow-Headers':'content-type','Content-Type':'application/json'};
  if(req.method==='OPTIONS')return new Response(null,{status:204,headers});
  let stats=transfers.get(id);if(!stats){stats={bytes:0,parts:0,active:0,peak:0};transfers.set(id,stats)}
  if(req.method==='GET')return Response.json({...stats,apiBytes:apiReceived},{headers});
  if(req.method!=='PUT')return new Response(null,{status:405,headers});
  stats.active++;stats.peak=Math.max(stats.peak,stats.active);
  try{
   const reader=req.body!.getReader();let size=0;
   while(true){const {done,value}=await reader.read();if(done)break;size+=value.byteLength}
   stats.bytes+=size;stats.parts++;
   return new Response(null,{status:200,headers});
  }finally{stats.active--}
 }
});
