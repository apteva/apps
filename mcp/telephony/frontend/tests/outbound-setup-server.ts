const bundle=await Bun.build({entrypoints:[import.meta.dir+"/outbound-setup-host.tsx"],target:"browser",format:"esm"});
if(!bundle.success)throw new AggregateError(bundle.logs);
const js=await bundle.outputs[0].text();
Bun.serve({hostname:"127.0.0.1",port:5398,fetch(request){
 if(new URL(request.url).pathname==="/host.js")return new Response(js,{headers:{"Content-Type":"text/javascript"}});
 return new Response('<!doctype html><title>Outbound setup fixture</title><div id="root"></div><output id="result"></output><output id="closed"></output><script type="module" src="/host.js"></script>',{headers:{"Content-Type":"text/html"}});
}});
