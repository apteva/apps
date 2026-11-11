package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strings"
)

//go:embed apteva.yaml
var manifest []byte

//go:embed main.go
var bundle []byte

type App struct{ ctx *sdk.AppCtx }

func (a *App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest(manifest)
	if e != nil {
		panic(e)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error     { a.ctx = ctx; return nil }
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/ui/", Handler: a.ui, NoAuth: true}, {Pattern: "/records", Handler: a.records}}
}
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "fixture_configure", Description: "Configure runtime", Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			raw, _ := json.Marshal(args)
			_, e := ctx.AppDB().Exec("INSERT INTO config(value) VALUES(?)", string(raw))
			return map[string]any{"ok": e == nil}, e
		}},
		{Name: "fixture_digest", Description: "Measure bundle", Handler: func(*sdk.AppCtx, map[string]any) (any, error) {
			return map[string]any{"digest": fmt.Sprintf("%x", sha256.Sum256(bundle))}, nil
		}},
		{Name: "fixture_credentials", Description: "Read fixture identity", Handler: func(*sdk.AppCtx, map[string]any) (any, error) {
			return map[string]any{"email": "operator@fixture.invalid", "password": "SyntheticPassword123!"}, nil
		}},
		{Name: "fixture_count", Description: "Count persisted records", Handler: func(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
			var n int
			e := ctx.AppReadDB().QueryRow("SELECT count(*) FROM records").Scan(&n)
			return map[string]any{"count": n}, e
		}},
	}
}
func (a *App) ui(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/config") {
		var raw string
		if a.ctx.AppReadDB().QueryRow("SELECT value FROM config LIMIT 1").Scan(&raw) != nil {
			http.Error(w, "not configured", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, raw)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><link rel="icon" href="data:,"></head><body>
 <form id="login"><label>Email<input data-testid="email"></label><label>Password<input type="password" data-testid="password"></label><button type="submit">Sign in</button></form>
 <main hidden><h1>Dashboard</h1><form id="create"><label>Name<input data-testid="name"></label><button type="submit">Save</button></form><button id="retry">Retry save</button><p data-testid="saved">0</p></main><p data-testid="error"></p>
 <script>
 let cfg;const config=fetch('./config').then(r=>r.json()).then(c=>cfg=c);
 async function list(){let r=await fetch(cfg.api+'/records',{headers:{'X-Test-Session':sessionStorage.getItem('token')}});if(!r.ok)throw Error('Read denied');let value=await r.json();document.querySelector('[data-testid=saved]').textContent=String(value.count)}
 async function show(){document.querySelector('#login').hidden=true;document.querySelector('main').hidden=false;await list()}
 config.then(()=>{if(sessionStorage.getItem('token'))show().catch(e=>document.querySelector('[data-testid=error]').textContent=e.message)});
 document.querySelector('#login').onsubmit=async event=>{event.preventDefault();await config;let r=await fetch(cfg.auth,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({client_id:cfg.client_id,email:document.querySelector('[data-testid=email]').value,password:document.querySelector('[data-testid=password]').value})});if(!r.ok){document.querySelector('[data-testid=error]').textContent='Sign-in denied';return}let value=await r.json();sessionStorage.setItem('token',value.access_token);await show()};
 async function save(){let r=await fetch(cfg.api+'/records',{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':'one-synthetic-record','X-Test-Session':sessionStorage.getItem('token')},body:JSON.stringify({name:document.querySelector('[data-testid=name]').value})});if(!r.ok)throw Error('Save denied');await list()}
 document.querySelector('#create').onsubmit=async e=>{e.preventDefault();await save()};
 document.querySelector('#retry').onclick=save;
 </script></body></html>`)
}
func (a *App) records(w http.ResponseWriter, r *http.Request) {
	var verified struct {
		Authenticated bool `json:"authenticated"`
	}
	e := sdk.CallAppResultContext(context.Background(), a.ctx.PlatformAPI(), "auth", "auth_verify_session", map[string]any{"credential": map[string]any{"type": "bearer", "token": r.Header.Get("X-Test-Session")}}, &verified)
	if e != nil || !verified.Authenticated {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "POST" {
		var in struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		if _, e = a.ctx.AppDB().Exec("INSERT INTO records(request_key,name) VALUES(?,?) ON CONFLICT(request_key) DO NOTHING", r.Header.Get("Idempotency-Key"), in.Name); e != nil {
			http.Error(w, "write failed", 500)
			return
		}
	}
	var n int
	_ = a.ctx.AppReadDB().QueryRow("SELECT count(*) FROM records").Scan(&n)
	json.NewEncoder(w).Encode(map[string]any{"count": n})
}
func main() { sdk.Run(&App{}) }
