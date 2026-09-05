package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

// Real SDK processes and databases, with an isolated platform callback adapter.
// The adapter validates the project's outbound credential and replaces it with
// the target sidecar's token, as the server's authenticated callback does.
func TestSidecarProjectScopeAndBoundHTTPDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts real SDK sidecars")
	}
	target := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	targetURL, _ := url.Parse(target.URL())
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/callback/apps/upstream/proxy")
		r.Header.Set("Authorization", "Bearer "+target.Token())
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/apps/callback/apps/upstream/proxy/") {
			if r.Header.Get("Authorization") != "Bearer outbound-test" || r.URL.Query().Get("project_id") != testProject {
				http.Error(w, "forbidden", 403)
				return
			}
			if r.Header.Get("X-API-Key") != "" || r.Header.Get("X-Apteva-App-Token") != "" || r.URL.Query().Get("api_key") != "" {
				t.Error("public/internal credentials crossed callback boundary")
			}
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(platform.Close)
	gateway := tk.SpawnSidecar(t, ".", tk.WithProjectID(""), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "outbound-test"))
	call := func(sidecar *tk.Sidecar, path, project, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", sidecar.URL()+path+"?project_id="+url.QueryEscape(project), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sidecar.Token())
		req.Header.Set("X-Apteva-Project-ID", project)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var data map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	if code, out := call(target, "/apis", testProject, `{"slug":"backend"}`); code != 200 {
		t.Fatalf("target setup: %d %v", code, out)
	}
	if code, out := call(gateway, "/apis", testProject, `{"slug":"outer"}`); code != 200 {
		t.Fatalf("gateway setup: %d %v", code, out)
	}
	if code, out := call(gateway, "/tools/call", testProject, `{"tool":"api_route_add","args":{"api_slug":"outer","method":"GET","path_pattern":"/data","target_kind":"app","target_ref":"upstream","target_path":"/apis"}}`); code != 200 {
		t.Fatalf("route: %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", gateway.URL()+"/gw/outer/data?project_id="+testProject+"&api_key=public-test", nil)
	req.Header.Set("X-Apteva-App-Token", "internal-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "backend") {
		t.Fatalf("bound dispatch: %d %s", resp.StatusCode, body)
	}
	if code, out := call(gateway, "/apis", "victim", `{"slug":"private"}`); code != 200 {
		t.Fatalf("victim setup: %d %v", code, out)
	}
	if code, _ := call(gateway, "/tools/call", testProject, `{"tool":"api_key_create","args":{"project_id":"victim","api_slug":"private","name":"unauthorized"}}`); code != 403 {
		t.Fatalf("cross-project body accepted: %d", code)
	}
}
