const result=await Bun.build({entrypoints:['tests/browser/harness.tsx'],target:'browser',format:'esm',plugins:[{name:'host-kit',setup(build){build.onResolve({filter:/^@apteva\/ui-kit$/},()=>({path:import.meta.dir+'/kit.tsx'}))}}]});
if(!result.success)throw new Error(String(result.logs));
Bun.serve({port:19180,hostname:'127.0.0.1',fetch(req){return new URL(req.url).pathname==='/test.js'?new Response(result.outputs[0],{headers:{'Content-Type':'text/javascript'}}):new Response('<div id="root"></div><script type="module" src="/test.js"></script>',{headers:{'Content-Type':'text/html'}})}});
