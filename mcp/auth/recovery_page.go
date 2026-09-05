package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

var recoveryTemplate = template.Must(template.New("recovery").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Account recovery</title></head><body><main><h1>{{.Title}}</h1><p>{{.Org}}</p><form id="recovery">{{if .Reset}}<label>New password <input id="password" type="password" autocomplete="new-password" minlength="{{.Minimum}}" maxlength="4096" required></label><label>Confirm password <input id="confirm" type="password" autocomplete="new-password" required></label>{{end}}<button id="submit" type="submit">{{.Title}}</button></form><p id="status" role="status" aria-live="polite"></p><script nonce="{{.Nonce}}">
const fragment=new URLSearchParams(location.hash.slice(1));const token=fragment.get({{.Action}});const client=fragment.get('client_id');const project=fragment.get('project_id');history.replaceState(null,'',location.pathname+location.search);
const form=document.getElementById('recovery'),status=document.getElementById('status'),button=document.getElementById('submit');
if(!token||!client||!project){button.disabled=true;status.textContent='This recovery link is incomplete. Request a new link.'}
form.addEventListener('submit',async(event)=>{event.preventDefault();const password=document.getElementById('password');const confirm=document.getElementById('confirm');if(password&&password.value!==confirm.value){status.textContent='Passwords do not match.';return}button.disabled=true;status.textContent='Working…';try{const base=location.pathname.slice(0,location.pathname.lastIndexOf('/orgs/'));const response=await fetch(base+{{.Endpoint}}+'?project_id='+encodeURIComponent(project),{method:'POST',credentials:'omit',headers:{'Content-Type':'application/json'},body:JSON.stringify({token,client_id:client,organization_slug:{{.Slug}},password:password?.value})});const data=await response.json();if(!response.ok)throw Error(data.error||'Recovery failed');form.hidden=true;status.textContent='Complete. Return to your app and sign in.'}catch(error){status.textContent=error.message;button.disabled=false}});
</script></main></body></html>`))

func (a *App) handleRecoveryPage(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, "project required")
		return
	}
	org, err := orgFromRequest(getAppCtx(r), r, pid)
	if err != nil || org.Status != "active" {
		httpErr(w, 404, "organization unavailable")
		return
	}
	policy, err := effectivePolicy(getAppCtx(r), org)
	if err != nil {
		httpErr(w, 500, "policy unavailable")
		return
	}
	nonce, err := randSlug(24)
	if err != nil {
		httpErr(w, 500, "recovery unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'nonce-%s'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'", nonce))
	reset := strings.HasSuffix(r.URL.Path, "/password/reset")
	title, action, endpoint := "Verify email", "verify", "/email/verify"
	if reset {
		title, action, endpoint = "Reset password", "reset", "/password/reset/confirm"
	}
	_ = recoveryTemplate.Execute(w, map[string]any{"Title": title, "Org": org.Name, "Slug": org.Slug, "Reset": reset, "Action": action, "Endpoint": endpoint, "Minimum": policy.MinLength, "Nonce": nonce})
}
