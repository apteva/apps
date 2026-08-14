import { type DragEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

interface NativePanelProps { appName: string; installId: number; projectId: string; instanceId?: number }
interface Area { id:number; slug:string; name:string; color:string; sort_order:number; archived:boolean }
interface Ticket { id:number; key:string; title:string; description:string; area_id?:number; area_slug?:string; area_name?:string; area_color?:string; type:string; status:string; priority:string; requester_name?:string; requester_email?:string; requester_organization?:string; requester_crm_contact_id?:number; assignee_name?:string; due_at?:string; portal_url?:string; created_at:string; updated_at:string; public_comment_count?:number; internal_note_count?:number; attachment_count?:number }
interface Comment { id:number; visibility:"public"|"internal"; body:string; author_kind:string; author_name?:string; edited_at?:string; created_at:string }
interface Event { id:number; event_type:string; visibility:"public"|"internal"; actor_kind:string; actor_name?:string; data:Record<string,unknown>; created_at:string }
interface Attachment { id:number; name:string; content_type:string; size_bytes:number; url?:string; visibility:string; created_at:string }
interface Link { id:number; kind:string; label?:string; app_name?:string; external_id?:string; url?:string; created_at:string }
interface Detail { ticket:Ticket; comments:Comment[]; events:Event[]; attachments:Attachment[]; links:Link[] }
interface Portal { title:string; welcome_text:string; enabled:boolean; intake_url:string }
type ViewMode="list"|"board";

const API="/api/apps/tickets";
const statuses=["new","acknowledged","planned","in_progress","waiting_client","resolved","closed"];
const types=["feedback","bug","feature","change_request","question","support"];
const priorities=["low","normal","high","urgent"];

export default function TicketsPanel({projectId}:NativePanelProps){
  const [tickets,setTickets]=useState<Ticket[]>([]),[areas,setAreas]=useState<Area[]>([]);
  const [selectedId,setSelectedId]=useState(0),[detail,setDetail]=useState<Detail|null>(null);
  const [query,setQuery]=useState(""),[statusFilter,setStatusFilter]=useState(""),[areaFilter,setAreaFilter]=useState("");
  const [view,setView]=useState<ViewMode>("list"),[draggingId,setDraggingId]=useState(0);
  const [message,setMessage]=useState(""),[busy,setBusy]=useState(false),[creating,setCreating]=useState(false),[showAreas,setShowAreas]=useState(false);
  const [portal,setPortal]=useState<Portal|null>(null),[composer,setComposer]=useState(""),[internal,setInternal]=useState(false);
  const [draft,setDraft]=useState(emptyDraft());
  const withProject=useCallback((path:string)=>`${API}${path}${path.includes("?")?"&":"?"}project_id=${encodeURIComponent(projectId)}`,[projectId]);

  const loadAreas=useCallback(async()=>{const r=await fetch(withProject("/areas"),{credentials:"same-origin"});if(r.ok)setAreas((await r.json()).areas??[])},[withProject]);
  const loadPortal=useCallback(async()=>{const r=await fetch(withProject("/portal"),{credentials:"same-origin"});if(r.ok)setPortal((await r.json()).portal??null)},[withProject]);
  const loadTickets=useCallback(async()=>{const p=new URLSearchParams();if(query.trim())p.set("q",query.trim());if(view==="list"&&statusFilter)p.set("status",statusFilter);if(areaFilter)p.set("area",areaFilter);p.set("limit","200");try{const r=await fetch(withProject(`/tickets?${p}`),{credentials:"same-origin"});if(!r.ok)throw new Error(await r.text());const out=await r.json();const rows=out.tickets??[];setTickets(rows);setSelectedId(current=>current&&rows.some((t:Ticket)=>t.id===current)?current:0);setMessage(`${out.total??rows.length} ticket${(out.total??rows.length)===1?"":"s"}`)}catch(e){setMessage((e as Error).message)}},[areaFilter,query,statusFilter,view,withProject]);
  const loadDetail=useCallback(async(id:number)=>{if(!id){setDetail(null);return}try{const r=await fetch(withProject(`/tickets/${id}`),{credentials:"same-origin"});if(!r.ok)throw new Error(await r.text());const out=await r.json();setDetail(out);const t=out.ticket as Ticket;setDraft({title:t.title,description:t.description,type:t.type,status:t.status,priority:t.priority,area:t.area_slug||"general",requester_name:t.requester_name||"",requester_email:t.requester_email||"",requester_organization:t.requester_organization||"",assignee_name:t.assignee_name||"",due_at:t.due_at||""})}catch(e){setMessage((e as Error).message)}},[withProject]);
  useEffect(()=>{void loadAreas();void loadPortal()},[loadAreas,loadPortal]);
  useEffect(()=>{const timer=window.setTimeout(()=>void loadTickets(),query?220:0);return()=>window.clearTimeout(timer)},[loadTickets,query]);
  useEffect(()=>{if(!creating&&!showAreas)void loadDetail(selectedId)},[creating,loadDetail,selectedId,showAreas]);

  const startNew=()=>{setView("list");setCreating(true);setShowAreas(false);setSelectedId(0);setDetail(null);setDraft(emptyDraft());setMessage("New ticket")};
  const createTicket=async()=>{if(!draft.title.trim()){setMessage("Title is required.");return}setBusy(true);try{const body={...draft,status:undefined,due_at:draft.due_at||""};const r=await fetch(withProject("/tickets"),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});const out=await r.json();if(!r.ok)throw new Error(out.error||"Create failed");setCreating(false);await loadTickets();setSelectedId(out.ticket.id);setMessage(`${out.ticket.key} created.`)}catch(e){setMessage((e as Error).message)}finally{setBusy(false)}};
  const saveTicket=async()=>{if(!detail)return;setBusy(true);try{const patch={title:draft.title,description:draft.description,type:draft.type,priority:draft.priority,area:draft.area,requester_name:draft.requester_name,requester_email:draft.requester_email,requester_organization:draft.requester_organization,assignee_name:draft.assignee_name,due_at:draft.due_at};let r=await fetch(withProject(`/tickets/${detail.ticket.id}`),{method:"PATCH",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify(patch)});let out=await r.json();if(!r.ok)throw new Error(out.error||"Save failed");if(draft.status!==detail.ticket.status){r=await fetch(withProject(`/tickets/${detail.ticket.id}/status`),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({status:draft.status})});out=await r.json();if(!r.ok)throw new Error(out.error||"Status update failed")}await loadTickets();await loadDetail(detail.ticket.id);setMessage("Saved.")}catch(e){setMessage((e as Error).message)}finally{setBusy(false)}};
  const addComment=async()=>{if(!detail||!composer.trim())return;setBusy(true);try{const path=internal?"internal-notes":"comments";const r=await fetch(withProject(`/tickets/${detail.ticket.id}/${path}`),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({body:composer})});const out=await r.json();if(!r.ok)throw new Error(out.error||"Reply failed");setComposer("");await loadDetail(detail.ticket.id);await loadTickets();setMessage(internal?"Internal note added.":"Reply added.")}catch(e){setMessage((e as Error).message)}finally{setBusy(false)}};
  const upload=async(files:FileList|null)=>{if(!detail||!files?.length)return;setBusy(true);try{for(const file of Array.from(files)){if(file.size>10*1024*1024)throw new Error(`${file.name} exceeds 10 MB`);setMessage(`Uploading ${file.name}…`);const content_base64=await fileBase64(file);const r=await fetch(withProject(`/tickets/${detail.ticket.id}/attachments`),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:file.name,content_type:file.type||"application/octet-stream",content_base64,visibility:internal?"internal":"public"})});const out=await r.json();if(!r.ok)throw new Error(out.error||"Upload failed")}await loadDetail(detail.ticket.id);setMessage("Attachment added.")}catch(e){setMessage((e as Error).message)}finally{setBusy(false)}};
  const copyPortal=async()=>{if(!portal)return;await navigator.clipboard.writeText(portal.intake_url);setMessage("Client intake link copied.")};
  const moveTicket=async(ticket:Ticket,status:string)=>{if(ticket.status===status||busy)return;const previous=ticket.status;setBusy(true);setTickets(rows=>rows.map(row=>row.id===ticket.id?{...row,status,updated_at:new Date().toISOString()}:row));setMessage(`Moving ${ticket.key} to ${label(status)}…`);try{const r=await fetch(withProject(`/tickets/${ticket.id}/status`),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({status,reason:"Moved on Kanban board"})});const out=await r.json();if(!r.ok)throw new Error(out.error||"Status update failed");setMessage(`${ticket.key} moved to ${label(status)}.`);await loadTickets()}catch(e){setTickets(rows=>rows.map(row=>row.id===ticket.id?{...row,status:previous}:row));setMessage((e as Error).message)}finally{setBusy(false);setDraggingId(0)}};
  const openTicket=(ticket:Ticket)=>{setDetail(null);setSelectedId(ticket.id);setCreating(false);setShowAreas(false)};
  const closeTicket=()=>{setSelectedId(0);setDetail(null);setComposer("");setInternal(false)};

  return <div className="h-full min-h-0 flex flex-col bg-bg text-text" data-ticket-view={view}>
    <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
      <div><h1 className="text-sm font-semibold">Tickets</h1><p className="text-xs text-text-muted">Client feedback, support requests, and permanent history.</p></div>
      <input value={query} onChange={e=>setQuery(e.target.value)} placeholder="Search tickets" className="ml-auto w-64 bg-bg-input border border-border rounded px-2.5 py-1.5 text-sm"/>
      <div className="flex rounded border border-border overflow-hidden" aria-label="Ticket view">
        <button onClick={()=>setView("list")} aria-pressed={view==="list"} className={`px-2.5 py-1.5 text-xs ${view==="list"?"bg-accent text-bg":"hover:bg-bg-input text-text-muted"}`}>List</button>
        <button onClick={()=>{setStatusFilter("");setCreating(false);setShowAreas(false);setView("board")}} aria-pressed={view==="board"} className={`px-2.5 py-1.5 text-xs border-l border-border ${view==="board"?"bg-accent text-bg":"hover:bg-bg-input text-text-muted"}`}>Board</button>
      </div>
      <button onClick={()=>{closeTicket();setView("list");setShowAreas(true);setCreating(false)}} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input">Areas</button>
      {portal&&<button onClick={copyPortal} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input">Copy intake link</button>}
      <button onClick={startNew} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium">New ticket</button>
    </header>
    {view==="board"&&!creating&&!showAreas?<KanbanBoard tickets={tickets} areas={areas} areaFilter={areaFilter} setAreaFilter={setAreaFilter} draggingId={draggingId} setDraggingId={setDraggingId} busy={busy} message={message} moveTicket={moveTicket} openTicket={openTicket}/>:<>
    <main className="flex-1 min-h-0 grid" style={{gridTemplateColumns:"minmax(300px, 380px) minmax(0, 1fr)"}}>
      <aside className="border-r border-border min-h-0 flex flex-col">
        <div className="p-3 border-b border-border grid grid-cols-2 gap-2">
          <select value={statusFilter} onChange={e=>setStatusFilter(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"><option value="">All statuses</option>{statuses.map(v=><option key={v} value={v}>{label(v)}</option>)}</select>
          <select value={areaFilter} onChange={e=>setAreaFilter(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"><option value="">All areas</option>{areas.map(v=><option key={v.id} value={v.slug}>{v.name}</option>)}</select>
        </div>
        <div className="flex-1 min-h-0 overflow-auto">{tickets.length===0?<div className="p-4 text-sm text-text-muted">No tickets match.</div>:<ul className="divide-y divide-border">{tickets.map(t=><li key={t.id}><button onClick={()=>openTicket(t)} className={`w-full text-left px-4 py-3 hover:bg-bg-input ${selectedId===t.id&&!creating&&!showAreas?"bg-bg-input":""}`}>
          <div className="flex items-center gap-2"><span className="text-[10px] text-text-dim">{t.key}</span><span className={`text-[10px] rounded px-1.5 py-0.5 ${statusClass(t.status)}`}>{label(t.status)}</span><span className="ml-auto text-[10px] text-text-muted">{relative(t.updated_at)}</span></div>
          <div className="mt-1 text-sm font-medium line-clamp-2">{t.title}</div><div className="mt-1 flex items-center gap-2 text-[11px] text-text-muted"><span>{t.area_name||"General"}</span><span>·</span><span>{label(t.type)}</span>{t.requester_name&&<><span>·</span><span className="truncate">{t.requester_name}</span></>}</div>
        </button></li>)}</ul>}</div><footer className="px-3 py-2 border-t border-border text-xs text-text-muted">{message}</footer>
      </aside>
      {showAreas?<AreaManager areas={areas} withProject={withProject} reload={loadAreas} close={()=>setShowAreas(false)}/>:creating?<section className="min-h-0 overflow-auto"><TicketEditor draft={draft} setDraft={setDraft} areas={areas} busy={busy} onSave={createTicket} create/></section>:<div className="grid place-items-center px-6 text-center text-sm text-text-muted">Select a ticket to open its details on the side.</div>}
    </main>
    </>}
    {selectedId>0&&!creating&&!showAreas?<TicketDrawer detail={detail} draft={draft} setDraft={setDraft} areas={areas} busy={busy} composer={composer} setComposer={setComposer} internal={internal} setInternal={setInternal} onSave={saveTicket} onReply={addComment} onUpload={upload} onClose={closeTicket}/>:null}
  </div>
}

function TicketDrawer({detail,draft,setDraft,areas,busy,composer,setComposer,internal,setInternal,onSave,onReply,onUpload,onClose}:{detail:Detail|null;draft:Draft;setDraft:(value:Draft)=>void;areas:Area[];busy:boolean;composer:string;setComposer:(value:string)=>void;internal:boolean;setInternal:(value:boolean)=>void;onSave:()=>void;onReply:()=>void;onUpload:(files:FileList|null)=>Promise<void>;onClose:()=>void}){
  const closeRef=useRef<HTMLButtonElement>(null),dialogRef=useRef<HTMLElement>(null),closeHandlerRef=useRef(onClose);closeHandlerRef.current=onClose;
  useEffect(()=>{const previous=document.activeElement as HTMLElement|null;closeRef.current?.focus();const keydown=(event:KeyboardEvent)=>{if(event.key==="Escape")closeHandlerRef.current();if(event.key!=="Tab")return;const focusable=Array.from(dialogRef.current?.querySelectorAll<HTMLElement>('button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')||[]);if(!focusable.length)return;const first=focusable[0],last=focusable[focusable.length-1];if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus()}};document.addEventListener("keydown",keydown);return()=>{document.removeEventListener("keydown",keydown);previous?.focus()}},[]);
  return <div className="fixed inset-0 z-50 flex" onMouseDown={onClose} data-ticket-detail-drawer>
    <div className="flex-1 bg-black/55"/>
    <aside ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="ticket-detail-title" onMouseDown={event=>event.stopPropagation()} style={{width:"min(860px, 94vw)"}} className="h-full min-h-0 flex flex-col bg-bg border-l border-border shadow-2xl">
      <header className="shrink-0 border-b border-border px-4 py-3 flex items-center gap-3">
        <div className="min-w-0"><div className="text-xs text-text-muted">{detail?.ticket.key||"Ticket"}</div><div id="ticket-detail-title" className="font-semibold truncate">{detail?.ticket.title||"Loading details…"}</div></div>
        {detail?.ticket.requester_crm_contact_id?<span className="shrink-0 text-[10px] border border-border rounded px-2 py-1">CRM #{detail.ticket.requester_crm_contact_id}</span>:null}
        <div className="ml-auto flex items-center gap-2">{detail?.ticket.portal_url?<a href={detail.ticket.portal_url} target="_blank" rel="noreferrer" className="text-xs text-accent hover:underline">Open client view</a>:null}<button disabled={busy||!detail} onClick={onSave} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50">{busy?"Saving…":"Save"}</button><button ref={closeRef} type="button" aria-label="Close ticket details" onClick={onClose} className="px-2 py-1 text-lg leading-none text-text-muted hover:text-text">×</button></div>
      </header>
      {detail?<div className="flex-1 min-h-0 overflow-auto"><TicketEditor draft={draft} setDraft={setDraft} areas={areas} busy={busy} onSave={onSave}/><div className="border-t border-border p-4">
        <div className="flex gap-2 mb-2"><button onClick={()=>setInternal(false)} className={`px-2.5 py-1 text-xs rounded ${!internal?"bg-accent text-bg":"border border-border"}`}>Public reply</button><button onClick={()=>setInternal(true)} className={`px-2.5 py-1 text-xs rounded ${internal?"bg-yellow/20 text-yellow":"border border-border"}`}>Internal note</button></div>
        <textarea value={composer} onChange={event=>setComposer(event.target.value)} placeholder={internal?"Visible only to the team and agents":"Visible to the client"} className="w-full min-h-24 bg-bg-input border border-border rounded p-3 text-sm resize-y"/>
        <div className="mt-2 flex items-center gap-2"><label className="px-3 py-1.5 text-xs border border-border rounded cursor-pointer hover:bg-bg-input">Attach file<input type="file" multiple className="hidden" onChange={event=>void onUpload(event.target.files)}/></label><button disabled={busy||!composer.trim()} onClick={onReply} className="px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50">{internal?"Add note":"Send reply"}</button></div>
      </div><Timeline detail={detail}/></div>:<div className="flex-1 grid place-items-center text-sm text-text-muted">Loading ticket…</div>}
    </aside>
  </div>
}

function KanbanBoard({tickets,areas,areaFilter,setAreaFilter,draggingId,setDraggingId,busy,message,moveTicket,openTicket}:{tickets:Ticket[];areas:Area[];areaFilter:string;setAreaFilter:(value:string)=>void;draggingId:number;setDraggingId:(id:number)=>void;busy:boolean;message:string;moveTicket:(ticket:Ticket,status:string)=>Promise<void>;openTicket:(ticket:Ticket)=>void}){
  const grouped=useMemo(()=>Object.fromEntries(statuses.map(status=>[status,tickets.filter(ticket=>ticket.status===status)])) as Record<string,Ticket[]>,[tickets]);
  const drop=(event:DragEvent<HTMLElement>,status:string)=>{event.preventDefault();const id=Number(event.dataTransfer.getData("text/plain")||draggingId);const ticket=tickets.find(row=>row.id===id);if(ticket)void moveTicket(ticket,status)};
  return <main className="flex-1 min-h-0 flex flex-col">
    <div className="shrink-0 px-4 py-2.5 border-b border-border flex items-center gap-3">
      <select value={areaFilter} onChange={event=>setAreaFilter(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"><option value="">All areas</option>{areas.map(area=><option key={area.id} value={area.slug}>{area.name}</option>)}</select>
      <span className="text-xs text-text-muted">Drag tickets between columns to update status and record the change in history.</span>
      <span className="ml-auto text-xs text-text-dim">{message}</span>
    </div>
    <div className="flex-1 min-h-0 overflow-auto p-3">
      <div className="h-full flex gap-3 min-w-max" data-ticket-kanban>
        {statuses.map(status=><section key={status} data-kanban-status={status} onDragOver={event=>{event.preventDefault();event.dataTransfer.dropEffect="move"}} onDrop={event=>drop(event,status)} className="w-72 h-full min-h-0 flex flex-col rounded-lg border border-border bg-bg-input">
          <header style={{borderTopWidth:2}} className={`shrink-0 px-3 py-2.5 border-b border-border flex items-center gap-2 ${statusAccent(status)}`}><h2 className="text-xs font-semibold">{label(status)}</h2><span className="ml-auto min-w-5 text-center text-[10px] rounded-full bg-bg px-1.5 py-0.5 text-text-muted">{grouped[status].length}</span></header>
          <div className="flex-1 min-h-0 overflow-y-auto p-2 space-y-2">
            {grouped[status].map(ticket=><button key={ticket.id} draggable={!busy} onDragStart={event=>{event.dataTransfer.effectAllowed="move";event.dataTransfer.setData("text/plain",String(ticket.id));setDraggingId(ticket.id)}} onDragEnd={()=>setDraggingId(0)} onClick={()=>openTicket(ticket)} className={`w-full text-left rounded-md border border-border bg-bg p-3 shadow-sm hover:border-accent/60 hover:bg-bg-input cursor-grab active:cursor-grabbing ${draggingId===ticket.id?"opacity-40":""}`}>
              <div className="flex items-center gap-2"><span className="text-[10px] text-text-dim">{ticket.key}</span><span className={`ml-auto text-[10px] rounded px-1.5 py-0.5 ${priorityClass(ticket.priority)}`}>{label(ticket.priority)}</span></div>
              <div className="mt-1.5 text-sm font-medium leading-snug line-clamp-3">{ticket.title}</div>
              <div className="mt-2 flex items-center gap-1.5 text-[10px] text-text-muted"><span className="w-2 h-2 rounded-full" style={{background:ticket.area_color||"#6b7280"}}/><span className="truncate">{ticket.area_name||"General"}</span><span className="ml-auto">{relative(ticket.updated_at)}</span></div>
              {(ticket.public_comment_count||ticket.attachment_count)?<div className="mt-2 text-[10px] text-text-dim">{ticket.public_comment_count?`${ticket.public_comment_count} repl${ticket.public_comment_count===1?"y":"ies"}`:""}{ticket.public_comment_count&&ticket.attachment_count?" · ":""}{ticket.attachment_count?`${ticket.attachment_count} file${ticket.attachment_count===1?"":"s"}`:""}</div>:null}
            </button>)}
            {grouped[status].length===0?<div className="rounded border border-dashed border-border p-4 text-center text-[11px] text-text-dim">Drop a ticket here</div>:null}
          </div>
        </section>)}
      </div>
    </div>
  </main>
}

type Draft=ReturnType<typeof emptyDraft>;
function TicketEditor({draft,setDraft,areas,busy,onSave,create=false}:{draft:Draft;setDraft:(v:Draft)=>void;areas:Area[];busy:boolean;onSave:()=>void;create?:boolean}){const set=(key:keyof Draft,value:string)=>setDraft({...draft,[key]:value});return <div className="p-4 border-b border-border">
  <div className="max-w-5xl space-y-5">
    <div className="grid content-start grid-cols-1 gap-3">
      <label className="text-xs text-text-muted">Title<input autoFocus={create} value={draft.title} onChange={e=>set("title",e.target.value)} placeholder="Short summary" className="mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text"/></label>
      <label className="text-xs text-text-muted">Description<textarea value={draft.description} onChange={e=>set("description",e.target.value)} placeholder="Describe the feedback, issue, or support request" className="mt-1 w-full min-h-28 bg-bg-input border border-border rounded p-3 text-sm resize-y"/></label>
    </div>
    <div className={`grid content-start grid-cols-1 sm:grid-cols-2 ${create?"xl:grid-cols-3":"xl:grid-cols-4"} gap-3`}>
      <label className="text-xs text-text-muted">Area<select value={draft.area} onChange={e=>set("area",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">{areas.map(a=><option key={a.id} value={a.slug}>{a.name}</option>)}</select></label>
      <label className="text-xs text-text-muted">Type<select value={draft.type} onChange={e=>set("type",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">{types.map(v=><option key={v} value={v}>{label(v)}</option>)}</select></label>
      <label className="text-xs text-text-muted">Priority<select value={draft.priority} onChange={e=>set("priority",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">{priorities.map(v=><option key={v} value={v}>{label(v)}</option>)}</select></label>
      {!create&&<label className="text-xs text-text-muted">Status<select value={draft.status} onChange={e=>set("status",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm">{statuses.map(v=><option key={v} value={v}>{label(v)}</option>)}</select></label>}
    </div>
    <div className="border-t border-border pt-4">
      <div className="mb-3"><h3 className="text-xs font-semibold text-text">Requester and assignment</h3><p className="mt-0.5 text-[11px] text-text-dim">Optional details for ownership and CRM matching.</p></div>
      <div className="grid content-start grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-3">
        <label className="text-xs text-text-muted">Requester name <span className="text-text-dim">optional</span><input value={draft.requester_name} onChange={e=>set("requester_name",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"/></label>
        <label className="text-xs text-text-muted">Requester email <span className="text-text-dim">optional</span><input type="email" value={draft.requester_email} onChange={e=>set("requester_email",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"/></label>
        <label className="text-xs text-text-muted">Organization <span className="text-text-dim">optional</span><input value={draft.requester_organization} onChange={e=>set("requester_organization",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"/></label>
        {!create&&<label className="text-xs text-text-muted">Assignee <span className="text-text-dim">optional</span><input value={draft.assignee_name} onChange={e=>set("assignee_name",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"/></label>}
        <label className="text-xs text-text-muted">Deadline <span className="text-text-dim">optional</span><input type="datetime-local" value={draft.due_at} onChange={e=>set("due_at",e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"/></label>
      </div>
    </div>
    {create&&<button disabled={busy} onClick={onSave} className="px-4 py-2 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50">{busy?"Creating…":"Create ticket"}</button>}
  </div>
</div>}

function Timeline({detail}:{detail:Detail}){const items=[...detail.comments.map(c=>({kind:"comment",at:c.created_at,id:`c${c.id}`,visibility:c.visibility,title:c.visibility==="internal"?"Internal note":"Comment",actor:c.author_name||label(c.author_kind),body:c.body})),...detail.events.filter(e=>!["ticket.commented","ticket.internal_note.added"].includes(e.event_type)).map(e=>({kind:"event",at:e.created_at,id:`e${e.id}`,visibility:e.visibility,title:label(e.event_type.replace("ticket.","")),actor:e.actor_name||label(e.actor_kind),body:eventSummary(e)}))].sort((a,b)=>new Date(a.at).getTime()-new Date(b.at).getTime());return <div className="p-4"><h3 className="text-xs font-semibold uppercase tracking-wide text-text-muted mb-3">History</h3><div className="space-y-3">{items.map(i=><div key={i.id} className={`border rounded p-3 ${i.visibility==="internal"?"border-yellow/30 bg-yellow/5":"border-border bg-bg-input/30"}`}><div className="flex gap-2 text-xs"><strong>{i.title}</strong>{i.visibility==="internal"&&<span className="text-yellow">internal</span>}<span className="text-text-muted">by {i.actor}</span><span className="ml-auto text-text-dim">{formatDate(i.at)}</span></div>{i.body&&<p className="mt-2 whitespace-pre-wrap text-sm">{i.body}</p>}</div>)}{items.length===0&&<p className="text-sm text-text-muted">No activity yet.</p>}</div>{detail.attachments.length>0&&<div className="mt-5"><h3 className="text-xs font-semibold uppercase tracking-wide text-text-muted mb-2">Attachments</h3>{detail.attachments.map(a=><a key={a.id} href={a.url} target="_blank" rel="noreferrer" className="block text-sm text-accent hover:underline">{a.name} <span className="text-text-muted">{formatBytes(a.size_bytes)}</span></a>)}</div>}{detail.links.length>0&&<div className="mt-5"><h3 className="text-xs font-semibold uppercase tracking-wide text-text-muted mb-2">Linked work</h3>{detail.links.map(l=><div key={l.id} className="text-sm">{l.url?<a href={l.url} target="_blank" rel="noreferrer" className="text-accent hover:underline">{l.label||`${label(l.kind)} ${l.external_id}`}</a>:<span>{l.label||`${label(l.kind)} ${l.external_id}`}</span>}</div>)}</div>}</div>}

function AreaManager({areas,withProject,reload,close}:{areas:Area[];withProject:(p:string)=>string;reload:()=>Promise<void>;close:()=>void}){const [name,setName]=useState(""),[color,setColor]=useState("#6b7280"),[message,setMessage]=useState("");const create=async()=>{const r=await fetch(withProject("/areas"),{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({name,color})});const out=await r.json();if(!r.ok){setMessage(out.error||"Could not create area");return}setName("");await reload();setMessage("Area created.")};return <section className="p-5 overflow-auto"><div className="flex items-center"><div><h2 className="font-semibold">Feedback areas</h2><p className="text-xs text-text-muted">Use areas for Backend, Frontend, Design, or any client-specific category.</p></div><button onClick={close} className="ml-auto px-3 py-1.5 text-xs border border-border rounded">Close</button></div><div className="mt-5 max-w-xl border border-border rounded divide-y divide-border">{areas.map(a=><div key={a.id} className="p-3 flex items-center gap-3"><span className="w-3 h-3 rounded-full" style={{background:a.color}}/><span className="font-medium text-sm">{a.name}</span><span className="text-xs text-text-muted">{a.slug}</span></div>)}</div><div className="mt-4 max-w-xl flex gap-2"><input value={name} onChange={e=>setName(e.target.value)} placeholder="New area" className="flex-1 bg-bg-input border border-border rounded px-3 py-2 text-sm"/><input type="color" value={color} onChange={e=>setColor(e.target.value)} className="h-9 w-12 bg-bg-input border border-border rounded"/><button disabled={!name.trim()} onClick={create} className="px-3 py-2 text-sm bg-accent text-bg rounded disabled:opacity-50">Add</button></div><p className="mt-2 text-xs text-text-muted">{message}</p></section>}

function emptyDraft(){return{title:"",description:"",type:"feedback",status:"new",priority:"normal",area:"general",requester_name:"",requester_email:"",requester_organization:"",assignee_name:"",due_at:""}}
function label(v:string){return v.replaceAll("_"," ").replace(/\b\w/g,c=>c.toUpperCase())}
function statusClass(v:string){if(v==="resolved"||v==="closed")return"bg-green/15 text-green";if(v==="waiting_client")return"bg-yellow/15 text-yellow";if(v==="in_progress")return"bg-accent/15 text-accent";return"bg-border text-text-muted"}
function statusAccent(v:string){if(v==="resolved"||v==="closed")return"border-t-text-dim text-green";if(v==="waiting_client")return"border-t-text-dim text-yellow";if(v==="in_progress")return"border-t-accent text-accent";return"border-t-text-dim"}
function priorityClass(v:string){if(v==="urgent")return"bg-red/15 text-red";if(v==="high")return"bg-yellow/15 text-yellow";if(v==="low")return"bg-border text-text-muted";return"bg-accent/10 text-accent"}
function formatDate(v:string){const d=new Date(v);return Number.isNaN(d.getTime())?v:d.toLocaleString()}
function relative(v:string){const ms=Date.now()-new Date(v).getTime();if(!v||Number.isNaN(ms))return"";const m=Math.floor(ms/60000);if(m<1)return"now";if(m<60)return`${m}m`;const h=Math.floor(m/60);if(h<24)return`${h}h`;return`${Math.floor(h/24)}d`}
function eventSummary(e:Event){const d=e.data||{};if(d.reason)return String(d.reason);if(d.from!==undefined&&d.to!==undefined)return`${label(String(d.from))} → ${label(String(d.to))}`;if(d.changes&&typeof d.changes==="object")return`Changed ${Object.keys(d.changes).map(label).join(", ")}`;return""}
function fileBase64(file:File){return new Promise<string>((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result).split(",")[1]||"");r.onerror=()=>reject(r.error);r.readAsDataURL(file)})}
function formatBytes(n:number){if(!n)return"";if(n<1024)return`${n} B`;if(n<1024*1024)return`${(n/1024).toFixed(1)} KB`;return`${(n/1024/1024).toFixed(1)} MB`}
