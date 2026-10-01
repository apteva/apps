// Loopback-only preview. Uses a fresh local app token and database, never the user's API keys.
import {mkdtemp} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
const root = new URL('../', import.meta.url).pathname;
const backendPort = Number(process.env.SPORTSBOOK_BACKEND_PORT || 8078);
const previewPort = Number(process.env.SPORTSBOOK_PREVIEW_PORT || 8079);
const directory = await mkdtemp(join(tmpdir(),'apteva-sportsbook-'));
const binary = join(directory,'sportsbook');
const build = Bun.spawn(['go','build','-o',binary,'.'], {cwd:root,env:{...process.env,GOWORK:'off'},stdout:'inherit',stderr:'inherit'});
if(await build.exited!==0)process.exit(1);
const token = crypto.randomUUID();
const backend = Bun.spawn([binary],{cwd:root,env:{...process.env,APTEVA_APP_PORT:String(backendPort),APTEVA_PROJECT_ID:'sportsbook-preview',APTEVA_INSTALL_ID:'0',APTEVA_APP_TOKEN:token,APTEVA_GATEWAY_URL:'http://127.0.0.1:1',DB_PATH:join(directory,'sportsbook.db')},stdout:'inherit',stderr:'inherit'});
const address = `http://127.0.0.1:${previewPort}`;
const server = Bun.serve({hostname:'127.0.0.1',port:previewPort,async fetch(request){
 const url = new URL(request.url);
 if(url.pathname==='/')return Response.redirect(`${address}/ui/index.html`,303);
 if(url.pathname!=='/rpc'&&!url.pathname.startsWith('/ui/'))return new Response('Not found',{status:404});
 if(request.headers.get('origin')&&request.headers.get('origin')!==address)return new Response('Origin denied',{status:403});
 const headers = new Headers();headers.set('Authorization',`Bearer ${token}`);
 if(request.headers.has('content-type'))headers.set('Content-Type',request.headers.get('content-type')!);
 try{return await fetch(`http://127.0.0.1:${backendPort}${url.pathname}${url.search}`,{method:request.method,headers,body:request.method==='POST'?await request.arrayBuffer():undefined});}
 catch{return new Response('Sportsbook is starting. Refresh shortly.',{status:503});}
}});
const close=()=>{server.stop(true);backend.kill();process.exit(0);};
process.on('SIGINT',close);process.on('SIGTERM',close);
console.log(`Sportsbook preview: ${address}/ui/index.html`);
console.log(`Preview database: ${directory}`);
backend.exited.then(code=>{server.stop(true);process.exit(code);});
