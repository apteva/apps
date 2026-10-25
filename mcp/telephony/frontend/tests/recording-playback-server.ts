const bundle=await Bun.build({entrypoints:[import.meta.dir+"/recording-playback-host.tsx"],target:"browser",format:"esm"});
if(!bundle.success)throw new AggregateError(bundle.logs);
const js=await bundle.outputs[0].text();
const pcm=new ArrayBuffer(44+1600),view=new DataView(pcm),bytes=new Uint8Array(pcm);const text=(at:number,s:string)=>bytes.set(new TextEncoder().encode(s),at);
text(0,"RIFF");view.setUint32(4,bytes.length-8,true);text(8,"WAVEfmt ");view.setUint32(16,16,true);view.setUint16(20,1,true);view.setUint16(22,1,true);view.setUint32(24,8000,true);view.setUint32(28,16000,true);view.setUint16(32,2,true);view.setUint16(34,16,true);text(36,"data");view.setUint32(40,1600,true);
Bun.serve({hostname:"127.0.0.1",port:5399,fetch(request){const path=new URL(request.url).pathname;
 if(path==="/host.js")return new Response(js,{headers:{"Content-Type":"text/javascript"}});
 if(path==="/audio/mp3") {
  const file=Bun.file(import.meta.dir+"/fixtures/recording-stereo.mp3");
  const range=request.headers.get("range")?.match(/^bytes=(\d+)-(\d*)$/);
  const headers:Record<string,string>={"Content-Type":"audio/mpeg","Cache-Control":"private, no-store","Accept-Ranges":"bytes"};
  if(range){const start=Number(range[1]),end=Math.min(range[2]?Number(range[2]):file.size-1,file.size-1);headers["Content-Range"]=`bytes ${start}-${end}/${file.size}`;return new Response(file.slice(start,end+1),{status:206,headers});}
  return new Response(file,{headers});
 }
 if(path.startsWith("/audio/"))return new Response(pcm,{headers:{"Content-Type":"audio/wav"}});
 return new Response('<!doctype html><title>Recording playback fixture</title><div id="root"></div><script type="module" src="/host.js"></script>',{headers:{"Content-Type":"text/html"}});
}});
