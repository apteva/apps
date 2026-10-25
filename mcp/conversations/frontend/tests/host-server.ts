import { join } from "node:path";
const root=join(import.meta.dir,"..");
const result=await Bun.build({entrypoints:[join(root,"example/main.tsx"),join(root,"example/dashboard.tsx"),join(root,"example/package.tsx")],outdir:join(root,".example"),target:"browser",format:"esm",define:{"process.env.NODE_ENV":'"development"'},plugins:process.env.APTEVA_SDK_ENTRY?[{name:"sdk-candidate",setup(build){build.onResolve({filter:/^@apteva\/web-sdk$/},()=>({path:process.env.APTEVA_SDK_ENTRY!}));}}]:[]});
if(!result.success)throw new AggregateError(result.logs,"example build");
const voice=await Bun.build({entrypoints:[join(root,"src/voiceAudio.ts")],outdir:join(root,".example"),target:"browser",format:"esm"});
if(!voice.success)throw new AggregateError(voice.logs,"voice build");
const uploaded=new Map<string,any>();
const rows=new Map<string,any[]>();const calls:any[]=[];
let panelRows:any[]=[],panelOlderRows:any[]=[];
let activeConversations:string[]=[];
const unreadConversations=new Map<string,number>();
const activityRows=new Map<string,any[]>();
const responseFrames=new Map<string,any>();
const resolved=new Set<string>();
let room=false;let deliveryStatus="delivered";
const streams = new Set<ReadableStreamDefaultController>();
const activityStreams = new Set<ReadableStreamDefaultController>();
const activitySnapshot = () => `event: stream\ndata: ${JSON.stringify({type:"stream",snapshot:true,chat_id:"",frames:activeConversations.map(chat_id=>({type:"stream",chat_id,response_progress:{phase:"thinking"}}))})}\n\n`;
const publishActivity = () => {for(const stream of activityStreams){try{stream.enqueue(new TextEncoder().encode(activitySnapshot()));}catch{activityStreams.delete(stream);}}};
const publishListMessage = (chat:string) => {const event=`event: message\ndata: ${JSON.stringify({id:101,conversation_id:chat,role:"agent",content:"Reply ready",created_at:new Date().toISOString()})}\n\n`;for(const stream of activityStreams){try{stream.enqueue(new TextEncoder().encode(event));}catch{activityStreams.delete(stream);}}};
const reply = "## Here’s the update\n\nThe conversation is **working well**. Here are the next steps:\n\n- Review the summary\n- Confirm the schedule\n\nYou can use `status` to check progress.\n\n```js\nconst result = await conversations.history(\"support\");\nconsole.log(result);\n```\n\n| Task | Status |\n| --- | --- |\n| Review | Complete |\n\n[Read the details](https://example.com/" + "long-path-".repeat(30) + ")";

const report=(user:string)=>({id:91,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Report summary",component_kind:"report",components:[{app:"conversations",name:"report-card",props:{title:"Daily report",summary:"Report summary",sections:[{title:"Results",body:"All systems healthy"}]}}],created_at:new Date().toISOString()});
const approval=(user:string)=>({id:92,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:"Approval needed",component_kind:"approval",components:[{app:"conversations",name:"approval-card",props:{title:"Approve maintenance",body:"Restart the service?",status:resolved.has(user)?"approve":"pending",actions:[{id:"approve",label:"Approve",style:"primary"},{id:"deny",label:"Deny"}]}}],created_at:new Date().toISOString()});
const conversation=(user:string)=>({id:`chat-${user}`,project_id:"project",lead_agent_id:41,lead_agent_name:"Assistant",title:"Support chat",kind:room?"room":"direct",origin:"web",audience:"public",created_at:"",updated_at:""});
Bun.serve({port:5292,hostname:"127.0.0.1",async fetch(req){
 const url=new URL(req.url);
 if(url.pathname==="/reset" && req.method==="POST"){rows.clear();panelRows=[];panelOlderRows=[];activeConversations=[];unreadConversations.clear();activityRows.clear();responseFrames.clear();resolved.clear();calls.length=0;room=false;deliveryStatus="delivered";return Response.json({ok:true});}
 if(url.pathname==="/seed-panel" && req.method==="POST"){const seed=await req.json();panelRows=Array.isArray(seed)?seed:seed.current;panelOlderRows=Array.isArray(seed)?[]:seed.older;return Response.json({ok:true});}
 if(url.pathname==="/seed-activity" && req.method==="POST"){activeConversations=await req.json();publishActivity();return Response.json({ok:true});}
 if(url.pathname==="/seed-unread" && req.method==="POST"){const {chat_id,count}=await req.json();unreadConversations.set(chat_id,count);publishListMessage(chat_id);return Response.json({ok:true});}
 if(url.pathname==="/seed" && req.method==="POST") {
  const options=await req.json();room=Boolean(options.room);deliveryStatus=options.deliveryStatus || "delivered";
  for(const user of ["operator","visitor-a"]) rows.set(user,[
   {id:1,conversation_id:`chat-${user}`,role:"user",content:"Can you give me an update?",components:[],created_at:"2026-09-10T10:05:14Z"},
   {id:2,conversation_id:`chat-${user}`,role:"agent",agent_id:41,content:reply,components:[],created_at:"2026-09-10T10:05:18Z"},
   ...(room ? [{id:3,conversation_id:`chat-${user}`,role:"agent",agent_id:42,content:"I checked the schedule too.",components:[],created_at:"2026-09-10T10:05:19Z"}] : []),
  ]);
  return Response.json({ok:true});
 }
 if(url.pathname==="/append-message" && req.method==="POST") {
  const message=await req.json();const user=message.conversation_id.slice("chat-".length);
  rows.set(user,[...(rows.get(user)??[]).filter(row=>row.id!==message.id),message].sort((a,b)=>a.id-b.id));
  for(const c of streams) {try {c.enqueue(new TextEncoder().encode(`event: message\ndata: ${JSON.stringify(message)}\n\n`));}catch{streams.delete(c);}}
  return Response.json({ok:true});
 }
 if(url.pathname==="/emit" && req.method==="POST") {
  const frame=await req.json();
  if(frame.response_progress) {
   const key=`${frame.chat_id}:${frame.agent_id}`;
   if(frame.response_progress.phase==="idle")responseFrames.delete(key);
   else responseFrames.set(key,frame);
  }
  if(frame.tool_activity) {
   const current=activityRows.get(frame.chat_id)??[];
   activityRows.set(frame.chat_id,[...current.filter(a=>a.id!==frame.tool_activity.id),frame.tool_activity]);
  }
  for(const c of streams) {try {c.enqueue(new TextEncoder().encode(`event: stream\ndata: ${JSON.stringify(frame)}\n\n`));}catch{streams.delete(c);}}
  return Response.json({ok:true});
 }
 if(url.pathname==="/voiceAudio.js")return new Response(Bun.file(join(root,".example/voiceAudio.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/realtime-capture-worklet.js")return new Response(Bun.file(join(root,"../ui/realtime-capture-worklet.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/voice-probe-worklet.js")return new Response(Bun.file(join(root,"tests/voice-probe-worklet.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/health")return new Response("ok");
 if(url.pathname==="/dashboard.js")return new Response(Bun.file(join(root,".example/dashboard.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/package.js")return new Response(Bun.file(join(root,".example/package.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/main.js")return new Response(Bun.file(join(root,".example/main.js")),{headers:{"Content-Type":"text/javascript"}});
 if(url.pathname==="/styles.css")return new Response(Bun.file(join(root,"dist/styles.css")),{headers:{"Content-Type":"text/css"}});
 if(url.pathname==="/requests")return Response.json(calls);
 if(url.pathname==="/")return new Response(`<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1">${url.searchParams.get("host")==="package"?'<link rel="stylesheet" href="/styles.css">':""}<style>body{margin:0;background:#0a0a0a} ${url.searchParams.get("theme")==="clean" ? ':root{--bg:#ffffff;--text:#202020;--text-muted:#555555;--bg-card:#f5f5f5;--border:#e5e5e5;--border-subtle:#dddddd;--bg-input:#f5f5f5;--text-dim:#777777;--accent:#2563eb;--font-base:Arial,sans-serif;--radius-lg:10px}' : url.searchParams.get("theme")==="terminal" ? ':root{--font-base:monospace;--accent:#f97316}' : ''}</style></head><body><div id="root"></div><div id="host-sentinel" style="position:fixed;left:-1000px;--spacing:13px">Host</div><script>window.CONVERSATIONS_EXAMPLE=${JSON.stringify({baseURL:"http://127.0.0.1:5292",projectId:"project",installId:7,agentId:41,...(url.searchParams.get("host")==="dashboard"?{}:{accessToken:"fixture-visitor-a"})})}</script><script type="module" src="/${url.searchParams.get("host")==="package"?"package":url.searchParams.get("host")==="dashboard"?"dashboard":"main"}.js"></script></body></html>`,{headers:{"Content-Type":"text/html","Set-Cookie":"host_session=fixture; SameSite=Lax; Path=/"}});
 const user=req.headers.get("authorization")==="Bearer fixture-visitor-a"?"visitor-a":req.headers.get("cookie")?.includes("host_session=fixture")?"operator":"";
 if(!user)return new Response("unauthorized",{status:401});
 calls.push({path:url.pathname,query:Object.fromEntries(url.searchParams),bearer:Boolean(req.headers.get("authorization")),cookie:Boolean(req.headers.get("cookie")),method:req.method});
 if(url.searchParams.get("project_id")!=="project"||url.searchParams.get("install_id")!=="7")return new Response("scope missing",{status:403});
 const path=url.pathname.replace("/api/apps/conversations","");
 if(path==="/ui/frontend.json")return new Response(Bun.file(join(root,"../ui/frontend.json")),{headers:{"Content-Type":"application/json"}});
 if(/^\/ui\/frontend\/[a-z]+-[a-f0-9]{16}\.(mjs|css)$/.test(path))return new Response(Bun.file(join(root,"..",path.slice(1))),{headers:{"Content-Type":path.endsWith(".css")?"text/css":"text/javascript"}});
 if(path==="/inbox") {const questions=(rows.get(user)??[]).filter(m=>m.component_kind==="interactive"&&m.components.some((c:any)=>c.interaction?.status==="pending"&&c.interaction.inbox!==false)).map(message=>({message,priority:0}));const items=[...questions,{message:report(user),priority:2},...(!resolved.has(user)?[{message:approval(user),priority:0}]:[])];return Response.json({items,total:items.length,next_cursor:""});}
 if(path==="/message-action"){const body=await req.json();calls.at(-1).body=body;
  if(body.component_id){
   const m=(rows.get(user)??[]).find(m=>m.id===body.message_id);const c=m?.components.find((c:any)=>c.id===body.component_id);
   if(!c?.interaction)return new Response("missing",{status:404});
   const action=c.interaction.actions.find((a:any)=>a.id===body.action_id);if(!action)return new Response("invalid action",{status:400});
   c.interaction.status=action.cancel?"cancelled":"answered";c.interaction.action_id=action.id;c.interaction.values=action.cancel?{}:body.values;m.revision=(m.revision??0)+1;
   for(const stream of streams){try{stream.enqueue(new TextEncoder().encode(`event: message\ndata: ${JSON.stringify(m)}\n\n`));}catch{streams.delete(stream);}}
   return Response.json({message:m});
  }
  if(body.message_id!==92)return new Response("missing",{status:404});resolved.add(user);return Response.json({message:approval(user)});}
 if(path==="/message-dismiss")return Response.json({ok:true});
 if(path==="/attachments"){
  const chat=url.searchParams.get("chat_id");
  if(req.method==="POST") {const body=await req.json();const type=body.name.endsWith(".png")?"image":"file";const attachment={id:body.id,type,name:body.name,mime_type:type==="image"?"image/png":"text/plain",size:Buffer.from(body.content_base64,"base64").length,...(type==="image"?{data_url:`data:image/png;base64,${body.content_base64}`}:{})};uploaded.set(`${chat}:${body.id}`,{attachment,content_base64:body.content_base64});return Response.json(attachment);}
  const found=uploaded.get(`${chat}:${url.searchParams.get("id")}`);return found?Response.json(found):new Response("missing",{status:404});
 }
 if(path==="/activity")return Response.json(activityRows.get(`chat-${user}`)??[]);
 if(path==="/activity-summary")return Response.json({active_conversation_ids:activeConversations});
 if(path==="/agents")return Response.json([{id:41,name:"Assistant",status:"running",attached:true},{id:42,name:"Scheduling assistant",status:"running",attached:true}]);
 if(path==="/unread-summary")return Response.json([...unreadConversations].map(([conversation_id,unread])=>({conversation_id,unread,latest_id:101})));
 if(path==="/chats"&&(req.method==="PATCH"||req.method==="DELETE")){
  const id=url.searchParams.get("id");const index=panelRows.findIndex(c=>c.id===id);if(index<0)return new Response("missing",{status:404});
  if(req.method==="DELETE"){panelRows.splice(index,1);return Response.json({ok:true});}
  const body=await req.json();calls.at(-1).body=body;panelRows[index]={...panelRows[index],...body,...(body.archived!==undefined?{archived_at:body.archived?new Date().toISOString():null}:{})};return Response.json(panelRows[index]);
 }
 if(path==="/participants")return Response.json({agent_ids:[41],user_ids:[],lead_agent_id:41});
 if(path==="/chats"&&req.method==="POST") {const body=await req.json();calls.at(-1).body=body;const created={...conversation(user),id:"new-operator-chat",title:body.title||"New conversation",lead_agent_id:body.lead_agent_id,audience:body.audience};panelRows=[created,...panelRows];return Response.json(created);}
 if(path==="/chats"&&url.searchParams.has("id"))return Response.json(panelRows.find(item=>item.id===url.searchParams.get("id"))??conversation(user));
 if(path==="/chats"&&url.searchParams.has("page"))return Response.json({conversations:(url.searchParams.get("cursor")==="older"?panelOlderRows:panelRows).filter(c=>Boolean(c.archived_at)===(url.searchParams.get("archived")==="1")),next_cursor:url.searchParams.get("cursor")!=="older"&&panelOlderRows.length?"older":""});
 if(path==="/chats"&&url.searchParams.has("lead_agent_id")&&panelRows.length)return Response.json(panelRows.filter(c=>!c.archived_at).slice(0,Number(url.searchParams.get("limit")??1)));
 if(path==="/chats")return Response.json([conversation(user)]);
 if(path==="/stream"&&url.searchParams.get("scope")==="user")return new Response(new ReadableStream({start(c){activityStreams.add(c);c.enqueue(new TextEncoder().encode(activitySnapshot()));},cancel(){/* Closed controllers are pruned on the next publish. */}}),{headers:{"Content-Type":"text/event-stream"}});
 if(path==="/stream")return new Response(new ReadableStream({start(c){streams.add(c);c.enqueue(new TextEncoder().encode(`event: stream\ndata: ${JSON.stringify({type:"stream",snapshot:true,done:true,chat_id:url.searchParams.get("chat_id"),frames:[...responseFrames.values()].filter(f=>f.chat_id===url.searchParams.get("chat_id"))})}\n\n`));}}),{headers:{"Content-Type":"text/event-stream"}});
 if((path==="/messages"&&(req.method==="PATCH"||req.method==="DELETE"))||path==="/message-queue") {
  const body=req.method==="DELETE"?{}:await req.json();calls.at(-1).body=body;
  const id=Number(body.message_id??url.searchParams.get("id"));
  const row=(rows.get(user)??[]).find(m=>m.id===id);
  if(!row||row.queue_state!=="queued")return new Response("Message is no longer queued",{status:409});
  if(req.method==="PATCH")row.content=body.content;
  else row.queue_state=req.method==="DELETE"?"cancelled":"steered";
  row.revision=(row.revision??0)+1;
  for(const c of streams){try{c.enqueue(new TextEncoder().encode(`event: message\ndata: ${JSON.stringify(row)}\n\n`));}catch{streams.delete(c);}}
  return Response.json(row);
 }
 if(path==="/messages"&&req.method==="POST"){
  const body=await req.json();calls.at(-1).body=body;const existing=rows.get(user)??[];
  const queued=body.next_message_behavior!=="steer"&&body.next_message_behavior!=="legacy"&&[...responseFrames.values()].some(f=>f.chat_id===conversation(user).id);
  const id=Math.max(0,...existing.map((m:any)=>m.id))+1;
  const row={id,conversation_id:conversation(user).id,role:"user",content:body.content,attachments:(body.attachments??[]).map((a:any)=>uploaded.get(`${conversation(user).id}:${a.id}`)?.attachment??a),components:[],created_at:new Date().toISOString(),client_message_id:body.client_message_id,...(queued?{queue_state:"queued",queue_position:id,queue_behavior:"queue"}:{})};existing.push(row);rows.set(user,existing);return Response.json(row);
 }
 if(path==="/messages"||path==="/changes")return Response.json({messages:rows.get(user)??[],cursor:0,before:0,has_more:false});
 if(path==="/seen"){const body=await req.json();calls.at(-1).body=body;unreadConversations.delete(body.chat_id);return Response.json({ok:true});}
 if(path==="/deliveries")return Response.json([{id:1,message_id:1,status:deliveryStatus,target:"agent-inbound:41",last_error:"internal transport detail",attempts:1}]);
 if(path==="/delivery-failures") {deliveryStatus="delivered";return Response.json({ok:true});}
 return new Response("unknown",{status:404});
}});
