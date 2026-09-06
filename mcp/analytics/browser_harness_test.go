package main

// Opt-in local fixture server. Never compiled into the released app.
import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrowserHarness(t *testing.T) {
	if os.Getenv("ANALYTICS_BROWSER_HARNESS") != "1" {
		t.Skip("browser fixture only")
	}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithEmitter(rec))
	globalCtx = ctx
	a := &App{}
	routes := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		routes.HandleFunc(pattern, route.Handler)
	}
	fixtureDir := os.Getenv("ANALYTICS_BROWSER_DIR")
	if fixtureDir == "" {
		t.Fatal("ANALYTICS_BROWSER_DIR required")
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__test/stop", func(w http.ResponseWriter, r *http.Request) { stopOnce.Do(func() { close(stop) }); w.WriteHeader(204) })
	mux.HandleFunc("/api/apps/analytics/", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project_id")
		if project == "" {
			project = "p1"
		}
		r.Header.Set(trustedProjectHeader, project)
		r.Header.Set("X-User-ID", "fixture-user")
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/analytics")
		routes.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) })
	mux.HandleFunc("/__test/reset", func(w http.ResponseWriter, r *http.Request) {
		for _, table := range []string{"financial_mappings", "financial_shares", "financial_targets", "financial_fx_requests", "financial_projects", "dashboard_target_links", "dashboard_widgets", "dashboards", "objective_progress", "objective_targets", "objectives", "reference_values", "reference_sets", "event_spec_violations", "event_property_specs", "event_specs", "ingest_receipts", "events", "write_keys", "fx_rates"} {
			if _, err := ctx.AppDB().Exec("DELETE FROM " + table); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /__test/financial-worker", func(w http.ResponseWriter, r *http.Request) {
		before := len(rec.Events())
		if err := a.financialRefreshWorker(r.Context(), ctx); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"events": rec.Events()[before:]})
	})
	mux.HandleFunc("/__test/track", func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		if json.NewDecoder(r.Body).Decode(&args) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		out, err := a.toolTrack(ctx, args)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, out)
	})
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(fixtureDir, "assets")))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><style>body{font:14px system-ui;margin:0;color:#dae4ef;background:#101722}input,select,button,textarea{font:inherit;color:inherit;background:#1d2939;border:1px solid #506070;border-radius:4px;padding:5px;margin:3px}button{cursor:pointer}section{padding:12px}header,nav{padding:10px;border-bottom:1px solid #394452}label{margin:4px}table{border-collapse:collapse;width:100%}th,td{padding:6px;text-align:left;border-bottom:1px solid #394452}svg{max-height:150px}main{padding:12px}.flex{display:flex}.flex-col{flex-direction:column}.flex-wrap{flex-wrap:wrap}.grid{display:grid}.gap-2{gap:8px}.gap-3{gap:12px}.gap-4{gap:16px}.grid-cols-2{grid-template-columns:1fr 1fr}.border{border:1px solid #394452}.rounded{border-radius:6px}.p-4{padding:16px}.p-3{padding:12px}.space-y-3>*{margin-bottom:12px}.text-error{color:#ff8491}.text-text-dim{color:#acbace}.overflow-auto{overflow:auto}.flex-1{flex:1}.h-full{min-height:100vh}.text-2xl{font-size:28px}</style></head><body><div id="root"></div><script type="module" src="/assets/entry.js"></script></body></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(fixtureDir, "url"), []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(server.URL)
	select {
	case <-time.After(30 * time.Minute):
	case <-ctx.Done():
	case <-stop:
	}
}
