import { join } from "node:path";
const root=join(import.meta.dir,"..");
const result=await Bun.build({entrypoints:[join(root,"example/main.tsx"),join(root,"example/dashboard.tsx")],outdir:join(root,".example"),target:"browser",format:"esm",define:{"process.env.NODE_ENV":'"development"'},plugins:process.env.APTEVA_SDK_ENTRY?[{name:"sdk-candidate",setup(build){build.onResolve({filter:/^@apteva\/web-sdk$/},()=>({path:process.env.APTEVA_SDK_ENTRY!}));}}]:[]});
if(!result.success)throw new AggregateError(result.logs,"example build");
const rows=new Map<string,any[]>();const calls:any[]=[];
const activityRows=new Map<string,any[]>();
const resolved=new Set<string>();
let room=false;let deliveryStatus="delivered";
const streams = new Set<ReadableStreamDefaultController>();
const reply = "## Here’s the update\n\nThe conversation is **working well**. Here are the next steps:\n\n- Review the summary\n- Confirm the schedule\n\nYou can use `status` to check progress.\n\n```js\nconst result = await conversations.history(\"support\");\nconsole.log(result);\n```\n\n| Task | Status |\n| --- | --- |\n| Review | Complete |\n\n[Read the details](https://example.com/" + "long-path-".repeat(30) + ")";

const report=(user:string)=>({id:91,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Report summary",component_kind:"report",components:[{app:"conversations",name:"report-card",props:{title:"Daily report",summary:"Report summary",sections:[{title:"Results",body:"All systems healthy"}]}}],created_at:new Date().toISOString()});
const approval=(user:string)=>({id:92,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Approval needed",component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approve maintenance",body:"Restart the service?",status:resolved.has(user)?"approve":"pending",actions:[{id:"approve",label:"Approve",style:"primary"},{id:"deny",label:"Deny"}]}}],created_at:new Date().toISOString()});
const conversation=(user:string)=>({id:`chat-${user}`,project_id:"project",lead_agent_id:41,lead_agent_name:"Assistant",title:"Support chat",kind:room?"room":"direct",origin:"web",audience:"public",created_at:"",updated_at:""});
Bun.serve({port:5292,hostname:"127.0.0.1",async fetch(req){
 const url=new URL(req.url);
 if(url.pathname==="/reset" && req.method==="POST"){rows.clear();activityRows.clear();resolved.clear();calls.length=0;room=false;deliveryStatus="delivered";return Response.json({ok:true});}
 if(url.pathname==="/seed" && req.method==="POST") {
  const options=await req.json();room=Boolean(options.room);deliveryStatus=options.deliveryStatus || "delivered";
  for(const user of ["operator","visitor-a"]) rows.set(user,[
   {id:1,conversation_id:`chat-${user}`,role:"user",content:"Can you give me an update?",components:[],created_at:"2026-09-10T10:05:14Z"},
   {id:2,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:reply,components:[],created_at:"2026-09-10T10:05:18Z"},
   ...(room ? [{id:3,conversation_id:`chat-${user}`,role:"agent",agent_id:42,content:"I checked the schedule too.",components:[],created_at:"2026-09-10T10:05:19Z"}] : []),
  ]);
  return Response.json({ok:true});
 }
 if(url.pathname==="/emit" && req.method==="POST") {
  const frame=await req.json();
  if(frame.tool_activity) {
   const current=activityRows.get(frame.chat_id)??[];
   activityRows.set(frame.chat_id,[...current.filter(a=>a.id!==frame.tool_activity.id),frame.tool_activity]);
  }
  for(const c of streams) {try {c.enqueue(new TextEncoder().encode(`event: stream\ndata: ${JSON.stringify(frame)}\n\n`));}catch{streams.delete(c);}}
  return Response.json({ok:true});
 }
 if(url.pathname==="/health")return new Response("ok");
 if(url.pathname==="/dashboard.js")return new Response(Bun.file(join(root,".example/dashboard.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/main.js")return new Response(Bun.file(join(root,".example/main.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/styles.css")return new Response(Bun.file(join(root,"dist/styles.css")),{headers:{"Content-Type":"text/css"}});
 if(url.pathname==="/requests")return Response.json(calls);
 if(url.pathname==="/")return new Response(`<!doctype html><html><head><style>body{margin:0;background:#0a0a0a} ${url.searchParams.get("theme")==="clean" ? ':root{--bg:#ffffff;--text:#202020;--text-muted:#555555;--bg-card:#f5f5f5;--border:#e5e5e5;--border-subtle:#dddddd;--bg-input:#f5f5f5;--text-dim:#777777;--accent:#2563eb;--font-base:Arial,sans-serif;--radius-lg:10px}' : url.searchParams.get("theme")==="terminal" ? ':root{--font-base:monospace;--accent:#f97316}' : ''}</style></head><body><div id="root"></div><div id="host-sentinel" style="position:fixed;left:-1000px;--spacing:13px">Host</div><script>window.CONVERSATIONS_EXAMPLE=${JSON.stringify({baseURL:"http://127.0.0.1:5292",projectId:"project",installId:7,agentId:41,...(url.searchParams.get("host")==="dashboard"?{}:{accessToken:"fixture-visitor-a"})})}</script><script type="module" src="/${url.searchParams.get("host")==="dashboard"?"dashboard":"main"}.js"></script></body></html>`,{headers:{"Content-Type":"text/html","Set-Cookie":"host_session=fixture; SameSite=Lax; Path=/"}});
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
 if(path==="/activity")return Response.json(activityRows.get(`chat-${user}`)??[]);
 if(path==="/agents")return Response.json([{id:41,name:"Assistant",attached:true},{id:42,name:"Scheduling assistant",attached:true}]);
 if(path==="/chats")return Response.json([conversation(user)]);
 if(path==="/stream")return new Response(new ReadableStream({start(c){streams.add(c);c.enqueue(new TextEncoder().encode(': connected\n\n'));}}),{headers:{"Content-Type":"text/event-stream"}});
 if(path==="/messages"&&req.method==="POST"){
  const body=await req.json();const existing=rows.get(user)??[];const row={id:existing.length+1,conversation_id:conversation(user).id,role:"user",content:body.content,components:[],created_at:new Date().toISOString(),client_message_id:body.client_message_id};existing.push(row);rows.set(user,existing);return Response.json(row);
 }
 if(path==="/messages"||path==="/changes")return Response.json({messages:rows.get(user)??[],cursor:0,before:0,has_more:false});
 if(path==="/seen")return Response.json({ok:true});
 if(path==="/deliveries")return Response.json([{id:1,message_id:1,status:deliveryStatus,target:"agent-inbound:41",last_error:"internal transport detail",attempts:1}]);
 if(path==="/delivery-failures") {deliveryStatus="delivered";return Response.json({ok:true});}
 return new Response("unknown",{status:404});
}});
