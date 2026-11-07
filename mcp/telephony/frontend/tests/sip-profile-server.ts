const built=await Bun.build({entrypoints:[import.meta.dir+'/sip-profile-host.tsx'],target:'browser',format:'esm'});
if(!built.success)throw new AggregateError(built.logs);
const js=await built.outputs[0].text();
Bun.serve({hostname:'127.0.0.1',port:5411,fetch(r){if(new URL(r.url).pathname==='/host.js')return new Response(js,{headers:{'Content-Type':'text/javascript'}});return new Response('<!doctype html><div id="root"></div><script type="module" src="/host.js"></script>',{headers:{'Content-Type':'text/html'}})}});
