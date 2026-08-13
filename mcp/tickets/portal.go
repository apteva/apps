package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var intakeTemplate = template.Must(template.New("intake").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title><style>
:root{color-scheme:light dark;--bg:#f5f6f8;--card:#fff;--text:#17181a;--muted:#6b7280;--border:#dfe2e7;--accent:#2563eb} @media(prefers-color-scheme:dark){:root{--bg:#111214;--card:#1a1b1f;--text:#f4f4f5;--muted:#9ca3af;--border:#303238;--accent:#60a5fa}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,sans-serif}.wrap{max-width:760px;margin:0 auto;padding:48px 20px}.card{background:var(--card);border:1px solid var(--border);border-radius:14px;padding:28px;box-shadow:0 10px 30px #0000000d}h1{font-size:26px;margin:0 0 6px}.lead{color:var(--muted);margin:0 0 26px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.full{grid-column:1/-1}label{display:block;font-size:13px;font-weight:600;margin-bottom:5px}input,select,textarea{width:100%;border:1px solid var(--border);border-radius:8px;background:transparent;color:var(--text);padding:10px 11px;font:inherit}textarea{min-height:150px;resize:vertical}button{border:0;border-radius:8px;background:var(--accent);color:white;padding:11px 18px;font-weight:700;cursor:pointer}button:disabled{opacity:.55}.actions{display:flex;align-items:center;gap:14px}.message{color:var(--muted);font-size:13px}.success{padding:20px;border:1px solid #22c55e66;background:#22c55e12;border-radius:10px}.success a{color:var(--accent);font-weight:700}@media(max-width:600px){.wrap{padding:20px 12px}.card{padding:20px}.grid{grid-template-columns:1fr}.full{grid-column:auto}}
</style></head><body><main class="wrap"><section class="card"><h1>{{.Title}}</h1><p class="lead">{{.Welcome}}</p><div id="success" hidden></div><form id="form"><div class="grid">
<div><label for="name">Your name</label><input id="name" name="requester_name" autocomplete="name"></div>
<div><label for="email">Email</label><input id="email" name="requester_email" type="email" autocomplete="email" required></div>
<div><label for="org">Organization</label><input id="org" name="requester_organization" autocomplete="organization"></div>
<div><label for="area">Project area</label><select id="area" name="area">{{range .Areas}}<option value="{{.Slug}}">{{.Name}}</option>{{end}}</select></div>
<div><label for="type">Request type</label><select id="type" name="type"><option value="feedback">Feedback</option><option value="bug">Bug</option><option value="feature">Feature idea</option><option value="change_request">Change request</option><option value="question">Question</option><option value="support">Support</option></select></div>
<div><label for="priority">Priority</label><select id="priority" name="priority"><option value="normal">Normal</option><option value="low">Low</option><option value="high">High</option><option value="urgent">Urgent</option></select></div>
<div class="full"><label for="title">Summary</label><input id="title" name="title" required maxlength="240"></div>
<div class="full"><label for="description">Details</label><textarea id="description" name="description" required placeholder="What happened, what did you expect, and where in the project is it?"></textarea></div>
<div class="full"><label for="files">Screenshots or files (optional, up to 10 MB each)</label><input id="files" type="file" multiple></div>
<div class="full actions"><button id="submit" type="submit">Submit ticket</button><span id="message" class="message"></span></div>
</div></form></section></main><script>
const endpoint={{.Endpoint}};const form=document.getElementById('form'),msg=document.getElementById('message'),submit=document.getElementById('submit');
function fileBase64(file){return new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result).split(',')[1]||'');r.onerror=reject;r.readAsDataURL(file)})}
form.addEventListener('submit',async e=>{e.preventDefault();submit.disabled=true;msg.textContent='Creating ticket…';try{const data=Object.fromEntries(new FormData(form).entries());delete data.files;const res=await fetch(endpoint+'/tickets',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});const out=await res.json();if(!res.ok)throw new Error(out.error||'Could not create ticket');const files=[...document.getElementById('files').files];for(const file of files){if(file.size>10*1024*1024)throw new Error(file.name+' exceeds 10 MB');msg.textContent='Uploading '+file.name+'…';const content_base64=await fileBase64(file);const up=await fetch(out.ticket.portal_url+'/attachments',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:file.name,content_type:file.type||'application/octet-stream',content_base64})});if(!up.ok){const u=await up.json();throw new Error(u.error||'Upload failed')}}form.hidden=true;const success=document.getElementById('success');success.hidden=false;success.className='success';success.innerHTML='<strong>Ticket '+out.ticket.key+' created.</strong><p>Keep this private link to follow progress and reply:</p><a href="'+out.ticket.portal_url+'">Open your ticket</a>'}catch(err){msg.textContent=err.message}finally{submit.disabled=false}});
</script></body></html>`))

var ticketTemplate = template.Must(template.New("ticket").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Ticket</title><style>
:root{color-scheme:light dark;--bg:#f5f6f8;--card:#fff;--text:#17181a;--muted:#6b7280;--border:#dfe2e7;--accent:#2563eb} @media(prefers-color-scheme:dark){:root{--bg:#111214;--card:#1a1b1f;--text:#f4f4f5;--muted:#9ca3af;--border:#303238;--accent:#60a5fa}}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,sans-serif}.wrap{max-width:840px;margin:0 auto;padding:36px 18px}.card{background:var(--card);border:1px solid var(--border);border-radius:12px;padding:22px;margin-bottom:14px}h1{font-size:24px;margin:4px 0}.meta,.muted{color:var(--muted);font-size:13px}.pill{display:inline-block;border:1px solid var(--border);border-radius:999px;padding:3px 8px;margin-right:5px;font-size:12px}.body{white-space:pre-wrap;margin-top:18px}.comment{border-top:1px solid var(--border);padding:14px 0}.comment:first-child{border-top:0}.comment p{white-space:pre-wrap;margin:5px 0}.attachments a{color:var(--accent);display:block;margin:6px 0}textarea{width:100%;min-height:100px;border:1px solid var(--border);border-radius:8px;background:transparent;color:var(--text);padding:10px;font:inherit;resize:vertical}input[type=file]{margin:12px 0;width:100%}button{border:0;border-radius:8px;background:var(--accent);color:#fff;padding:10px 15px;font-weight:700;cursor:pointer}button.secondary{background:transparent;color:var(--text);border:1px solid var(--border)}.row{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.right{margin-left:auto}.event{padding:8px 0;border-top:1px solid var(--border);font-size:13px}.error{color:#dc2626}@media(max-width:600px){.wrap{padding:14px 10px}.card{padding:16px}}
</style></head><body><main class="wrap"><div id="app" class="card">Loading ticket…</div></main><script>
const endpoint={{.Endpoint}},app=document.getElementById('app');let detail;
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));const fmt=s=>s?new Date(s).toLocaleString():'';const label=s=>String(s||'').replaceAll('_',' ');
function render(){const t=detail.ticket;const comments=detail.comments||[],atts=detail.attachments||[],events=detail.events||[];let html='<div class="row"><span class="meta">'+esc(t.key)+'</span><span class="right pill">'+esc(label(t.status))+'</span></div><h1>'+esc(t.title)+'</h1><div><span class="pill">'+esc(t.area_name||'General')+'</span><span class="pill">'+esc(label(t.type))+'</span><span class="pill">'+esc(t.priority)+'</span></div><div class="body">'+esc(t.description)+'</div><p class="meta">Submitted '+esc(fmt(t.created_at))+(t.requester_name?' by '+esc(t.requester_name):'')+'</p>';if(atts.length){html+='<div class="attachments"><h3>Attachments</h3>'+atts.map(a=>a.url?'<a href="'+esc(a.url)+'" target="_blank" rel="noopener">'+esc(a.name)+'</a>':'<span>'+esc(a.name)+'</span>').join('')+'</div>'}html+='<h3>Conversation</h3>'+(comments.length?comments.map(c=>'<div class="comment"><strong>'+esc(c.author_name||label(c.author_kind))+'</strong> <span class="meta">'+esc(fmt(c.created_at))+(c.edited_at?' · edited':'')+'</span><p>'+esc(c.body)+'</p></div>').join(''):'<p class="muted">No replies yet.</p>')+'<form id="reply"><textarea id="body" required placeholder="Add a reply…"></textarea><input id="files" type="file" multiple><div class="row"><button type="submit">Send reply</button>'+(['resolved','closed'].includes(t.status)?'<button id="reopen" class="secondary" type="button">Reopen ticket</button>':'')+'<span id="message" class="meta"></span></div></form><details style="margin-top:22px"><summary class="muted">Ticket history</summary>'+events.map(e=>'<div class="event"><strong>'+esc(label(e.event_type.replace('ticket.','')))+'</strong> · '+esc(e.actor_name||label(e.actor_kind))+' <span class="meta">'+esc(fmt(e.created_at))+'</span></div>').join('')+'</details>';app.innerHTML=html;bind()}
function fileBase64(file){return new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result).split(',')[1]||'');r.onerror=reject;r.readAsDataURL(file)})}
function bind(){document.getElementById('reply').addEventListener('submit',async e=>{e.preventDefault();const message=document.getElementById('message');try{message.textContent='Sending…';const body=document.getElementById('body').value;let res=await fetch(endpoint+'/comments',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({body})});if(!res.ok)throw new Error((await res.json()).error||'Reply failed');for(const file of [...document.getElementById('files').files]){if(file.size>10*1024*1024)throw new Error(file.name+' exceeds 10 MB');message.textContent='Uploading '+file.name+'…';res=await fetch(endpoint+'/attachments',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:file.name,content_type:file.type||'application/octet-stream',content_base64:await fileBase64(file)})});if(!res.ok)throw new Error((await res.json()).error||'Upload failed')}await load()}catch(err){message.textContent=err.message}});const reopen=document.getElementById('reopen');if(reopen)reopen.onclick=async()=>{await fetch(endpoint+'/reopen',{method:'POST'});await load()}}
async function load(){try{const res=await fetch(endpoint+'?format=json');const out=await res.json();if(!res.ok)throw new Error(out.error||'Ticket unavailable');detail=out;render()}catch(err){app.innerHTML='<p class="error">'+esc(err.message)+'</p>'}}load();
</script></body></html>`))

func (a *App) handlePublicPortal(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/p/"))
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	if parts[0] == "ticket" {
		a.handlePublicTicket(w, r, parts)
		return
	}
	portal, err := getPortalByToken(globalCtx.AppDB(), parts[0])
	if err != nil || !portal.Enabled {
		http.NotFound(w, r)
		return
	}
	if err := ensureProject(globalCtx.AppDB(), portal.ProjectID); err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	ctx := globalCtx.WithProject(portal.ProjectID)
	if len(parts) == 1 && r.Method == http.MethodGet {
		areas, err := listAreas(ctx.AppDB(), portal.ProjectID, false)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = intakeTemplate.Execute(w, map[string]any{"Title": portal.Title, "Welcome": portal.Welcome, "Areas": areas, "Endpoint": r.URL.Path})
		return
	}
	if len(parts) == 2 && parts[1] == "tickets" && r.Method == http.MethodPost {
		body, err := decodeMapLimited(w, r, 1<<20)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		body["source"] = "portal"
		actor := Actor{Kind: "client", Name: firstNonEmpty(stringArg(body, "requester_name"), stringArg(body, "requester_email"), "Client")}
		ticket, err := createTicket(ctx.AppDB(), portal.ProjectID, body, actor)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		ticket = bestEffortCRMLink(ctx, ticket, actor)
		decorateTicketURLs(ctx, ticket)
		emitTicket(ctx, "ticket.created", ticket, nil)
		publicTicket(ticket)
		writeJSON(w, http.StatusCreated, map[string]any{"ticket": ticket})
		return
	}
	http.NotFound(w, r)
}

func (a *App) handlePublicTicket(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	ticket, err := getTicketByToken(globalCtx.AppDB(), parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := globalCtx.WithProject(ticket.ProjectID)
	endpoint := strings.TrimSuffix(r.URL.Path, "/")
	if len(parts) > 2 {
		endpoint = strings.TrimSuffix(r.URL.Path, "/"+strings.Join(parts[2:], "/"))
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		if r.URL.Query().Get("format") != "json" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_ = ticketTemplate.Execute(w, map[string]any{"Endpoint": endpoint})
			return
		}
		detail, err := ticketDetail(ctx.AppDB(), ticket.ProjectID, ticket.ID, false)
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		publicTicket(detail.Ticket)
		decorateAttachmentURLs(ctx, ticket, detail.Attachments)
		writeJSON(w, http.StatusOK, detail)
		return
	}
	actor := Actor{Kind: "client", Ref: strings.ToLower(ticket.RequesterEmail), Name: firstNonEmpty(ticket.RequesterName, ticket.RequesterEmail, "Client")}
	action := ""
	if len(parts) > 2 {
		action = parts[2]
	}
	switch {
	case r.Method == http.MethodGet && action == "attachments" && len(parts) == 5 && parts[4] == "content":
		a.servePublicAttachment(w, ctx, ticket, parts[3])
	case r.Method == http.MethodPost && action == "comments":
		body, err := decodeMapLimited(w, r, 1<<20)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		comment, err := addComment(ctx.AppDB(), ticket.ProjectID, ticket.ID, "public", stringArg(body, "body"), actor)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		emitTicket(ctx, "ticket.commented", ticket, map[string]any{"comment_id": comment.ID})
		bestEffortCRMActivity(ctx, ticket, "Client replied on "+ticket.Key+": "+truncate(comment.Body, 180))
		writeJSON(w, http.StatusCreated, map[string]any{"comment": comment})
	case r.Method == http.MethodPost && action == "attachments":
		body, err := decodeMapLimited(w, r, 14<<20)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		body["visibility"] = "public"
		attachment, err := addAttachment(ctx, ticket.ProjectID, ticket.ID, body, actor)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		emitTicket(ctx, "ticket.attachment.added", ticket, map[string]any{"attachment_id": attachment.ID})
		decorateAttachmentURLs(ctx, ticket, []*Attachment{attachment})
		writeJSON(w, http.StatusCreated, map[string]any{"attachment": attachment})
	case r.Method == http.MethodPost && action == "reopen":
		if ticket.Status != "resolved" && ticket.Status != "closed" {
			httpErr(w, errors.New("only resolved or closed tickets can be reopened"), http.StatusConflict)
			return
		}
		updated, err := setTicketStatus(ctx.AppDB(), ticket.ProjectID, ticket.ID, "acknowledged", "Client reopened the ticket", actor)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		emitTicket(ctx, "ticket.reopened", updated, map[string]any{"from": ticket.Status, "to": updated.Status})
		publicTicket(updated)
		writeJSON(w, http.StatusOK, map[string]any{"ticket": updated})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) servePublicAttachment(w http.ResponseWriter, ctx *sdk.AppCtx, ticket *Ticket, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	attachment, err := getAttachment(ctx.AppDB(), ticket.ProjectID, ticket.ID, id)
	if err != nil || attachment.Visibility != "public" || attachment.StorageFileID == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	fileID, err := strconv.ParseInt(attachment.StorageFileID, 10, 64)
	if err != nil || fileID <= 0 || ctx.PlatformAPI() == nil {
		httpErr(w, errors.New("attachment is unavailable"), http.StatusServiceUnavailable)
		return
	}
	var stored struct {
		ContentBase64 string `json:"content_base64"`
		ContentType   string `json:"content_type"`
		Name          string `json:"name"`
	}
	if err := ctx.WithProject(ticket.ProjectID).PlatformAPI().CallAppResult("storage", "files_get_content", map[string]any{"id": fileID}, &stored); err != nil {
		httpErr(w, fmt.Errorf("attachment is unavailable: %w", err), http.StatusBadGateway)
		return
	}
	body, err := base64.StdEncoding.DecodeString(stored.ContentBase64)
	if err != nil {
		httpErr(w, errors.New("attachment content is invalid"), http.StatusBadGateway)
		return
	}
	contentType := firstNonEmpty(stored.ContentType, attachment.ContentType, "application/octet-stream")
	name := firstNonEmpty(stored.Name, attachment.Name, "attachment")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func publicTicket(ticket *Ticket) {
	if ticket == nil {
		return
	}
	ticket.ProjectID = ""
	ticket.RequesterCRMContactID = nil
	ticket.CreatedByRef = ""
	ticket.AssigneeRef = ""
}
