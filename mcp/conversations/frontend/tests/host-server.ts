import { join } from "node:path";
const root=join(import.meta.dir,"..");
const result=await Bun.build({entrypoints:[join(root,"example/main.tsx"),join(root,"example/dashboard.tsx")],outdir:join(root,".example"),target:"browser",format:"esm",define:{"process.env.NODE_ENV":'"development"'},plugins:process.env.APTEVA_SDK_ENTRY?[{name:"sdk-candidate",setup(build){build.onResolve({filter:/^@apteva\/web-sdk$/},()=>({path:process.env.APTEVA_SDK_ENTRY!}));}}]:[]});
if(!result.success)throw new AggregateError(result.logs,"example build");
const rows=new Map<string,any[]>();const calls:any[]=[];
const resolved=new Set<string>();
const report=(user:string)=>({id:91,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Report summary",component_kind:"report",components:[{app:"conversations",name:"report-card",props:{title:"Daily report",summary:"Report summary",sections:[{title:"Results",body:"All systems healthy"}]}}],created_at:new Date().toISOString()});
const approval=(user:string)=>({id:92,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Approval needed",component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approve maintenance",body:"Restart the service?",status:resolved.has(user)?"approve":"pending",actions:[{id:"approve",label:"Approve",style:"primary"},{id:"deny",label:"Deny"}]}}],created_at:new Date().toISOString()});
const conversation=(user:string)=>({id:`chat-${user}`,project_id:"project",lead_agent_id:41,title:"Support chat",kind:"direct",origin:"web",audience:"public",created_at:"",updated_at:""});
Bun.serve({port:5292,hostname:"127.0.0.1",async fetch(req){
 const url=new URL(req.url);
 if(url.pathname==="/health")return new Response("ok");
 if(url.pathname==="/dashboard.js")return new Response(Bun.file(join(root,".example/dashboard.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/main.js")return new Response(Bun.file(join(root,".example/main.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/styles.css")return new Response(Bun.file(join(root,"dist/styles.css")),{headers:{"Content-Type":"text/css"}});
 if(url.pathname==="/requests")return Response.json(calls);
 if(url.pathname==="/")return new Response(`<!doctype html><html><head>${url.searchParams.get("host")==="dashboard"?'<link rel="stylesheet" href="/styles.css">':""}<style>body{margin:0;background:#0a0a0a}</style></head><body><div id="root" class="apteva-conversations"></div><script>window.CONVERSATIONS_EXAMPLE=${JSON.stringify({baseURL:"http://127.0.0.1:5292",projectId:"project",installId:7,agentId:41,...(url.searchParams.get("host")==="dashboard"?{}:{accessToken:"fixture-visitor-a"})})}</script><script type="module" src="/${url.searchParams.get("host")==="dashboard"?"dashboard":"main"}.js"></script></body></html>`,{headers:{"Content-Type":"text/html","Set-Cookie":"host_session=fixture; SameSite=Lax; Path=/"}});
 const user=req.headers.get("authorization")==="Bearer fixture-visitor-a"?"visitor-a":req.headers.get("cookie")?.includes("host_session=fixture")?"operator":"";
 if(!user)return new Response("unauthorized",{status:401});
 calls.push({path:url.pathname,query:Object.fromEntries(url.searchParams),bearer:Boolean(req.headers.get("authorization")),cookie:Boolean(req.headers.get("cookie")),method:req.method});
 if(url.searchParams.get("project_id")!=="project"||url.searchParams.get("install_id")!=="7")return new Response("scope missing",{status:403});
 const path=url.pathname.replace("/api/apps/conversations","");
 if(path==="/ui/frontend.json")return new Response(Bun.file(join(root,"../ui/frontend.json")),{headers:{"Content-Type":"application/json"}});
 if(/^\/ui\/frontend\/[a-z]+-[a-f0-9]{16}\.(mjs|css)$/.test(path))return new Response(Bun.file(join(root,"..",path.slice(1))),{headers:{"Content-Type":path.endsWith(".css")?"text/css":"text/javascript"}});
 if(path==="/inbox") {const items=[{message:report(user),priority:2},...(!resolved.has(user)?[{message:approval(user),priority:0}]:[])];return Response.json({items,total:items.length,next_cursor:""});}
 if(path==="/message-action"){const body=await req.json();if(body.message_id!==92)return new Response("missing",{status:404});resolved.add(user);return Response.json({message:approval(user)});}
 if(path==="/message-dismiss")return Response.json({ok:true});
 if(path==="/agents")return Response.json([{id:41,name:"Assistant",attached:true}]);
 if(path==="/chats")return Response.json([conversation(user)]);
 if(path==="/stream")return new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode(': connected\n\n'));}}),{headers:{"Content-Type":"text/event-stream"}});
 if(path==="/messages"&&req.method==="POST"){
  const body=await req.json();const existing=rows.get(user)??[];const row={id:existing.length+1,conversation_id:conversation(user).id,role:"user",content:body.content,components:[],created_at:new Date().toISOString(),client_message_id:body.client_message_id};existing.push(row);rows.set(user,existing);return Response.json(row);
 }
 if(path==="/messages"||path==="/changes")return Response.json({messages:rows.get(user)??[],cursor:0,before:0,has_more:false});
 if(path==="/seen")return Response.json({ok:true});
 if(path==="/deliveries")return Response.json([]);
 return new Response("unknown",{status:404});
}});
