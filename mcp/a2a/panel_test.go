package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelLedgerFiltersAndProjectIsolation(t *testing.T) {
	app, _ := newTestEnv(t)
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	a := &App{}
	// More than a page proves counts and search do not operate on a truncated list.
	for i := 0; i < 35; i++ {
		task, err := createTask(app.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", FromAgentID: 41, FromAgentName: "Research", ToAgentID: 42, ToAgentName: "CRM", FromThreadID: "research-thread"})
		if err != nil {
			t.Fatal(err)
		}
		if err = recordMessage(app.AppDB(), task.ID, 41, 42, fmt.Sprintf("Research topic %d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	hidden, err := createTask(app.AppDB(), &Task{ProjectID: "proj-other", Kind: "ask", Status: "failed", FromAgentName: "Secret", FromAgentID: 77})
	if err != nil {
		t.Fatal(err)
	}
	query := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		a.handlePanelTasks(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %s", path, w.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := query("/tasks?project_id=" + testProject + "&limit=30")
	if data["total"] != float64(35) || len(data["tasks"].([]any)) != 30 {
		t.Fatalf("pagination: %+v", data)
	}
	first := data["tasks"].([]any)[0].(map[string]any)
	if first["preview"] != "Research topic 34" || first["from_thread_id"] != "research-thread" {
		t.Fatalf("missing context: %+v", first)
	}
	data = query("/tasks?project_id=" + testProject + "&limit=30&offset=30")
	if len(data["tasks"].([]any)) != 5 {
		t.Fatal("missing next page")
	}
	data = query("/tasks?project_id=" + testProject + "&q=topic+2&agent_id=41")
	if data["total"] != float64(11) {
		t.Fatalf("message search: %+v", data)
	}
	data = query("/tasks?project_id=" + testProject + "&q=Secret")
	if data["total"] != float64(0) {
		t.Fatal("cross-project search leak")
	}
	w := httptest.NewRecorder()
	a.handleTaskItem(w, httptest.NewRequest("GET", fmt.Sprintf("/tasks/%d/messages?project_id=%s", hidden.ID, testProject), nil))
	if w.Code != 404 {
		t.Fatal("cross-project detail leak")
	}
	for _, q := range []string{"from=bad", "agent_id=oops", "from=2026-09-15&to=2026-09-01"} {
		w = httptest.NewRecorder()
		a.handlePanelTasks(w, httptest.NewRequest("GET", "/tasks?project_id="+testProject+"&"+q, nil))
		if w.Code != 400 {
			t.Fatalf("invalid filter accepted: %s", q)
		}
	}
}

func TestPanelAttentionIncludesDeliveryAndSyncRecovery(t *testing.T) {
	app, _ := newTestEnv(t)
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	a := &App{}
	for _, state := range []string{"working", "input_required", "failed", "completed"} {
		task, err := createTask(app.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: state})
		if err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			_, err = app.AppDB().Exec("INSERT INTO a2a_deliveries(task_id,project_id,to_agent_id,event) VALUES(?,?,41,'reply')", task.ID, testProject)
		}
		if state == "working" {
			_, err = app.AppDB().Exec("UPDATE a2a_tasks SET poll_failures=2 WHERE id=?", task.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := createTask(app.AppDB(), &Task{ProjectID: "elsewhere", Kind: "ask", Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handleOverview(w, httptest.NewRequest("GET", "/overview?project_id="+testProject, nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var counts map[string]any
	json.Unmarshal(w.Body.Bytes(), &counts)
	if counts["total"] != float64(4) || counts["attention"] != float64(4) || counts["pending_delivery"] != float64(1) {
		t.Fatalf("counts: %+v", counts)
	}
	w = httptest.NewRecorder()
	a.handlePanelTasks(w, httptest.NewRequest("GET", "/tasks?status=attention&project_id="+testProject, nil))
	var page struct{ Total int }
	json.Unmarshal(w.Body.Bytes(), &page)
	if page.Total != 4 {
		t.Fatal(w.Body.String())
	}
}

func TestPanelNetworkOnlyListsAttachedProjectAgents(t *testing.T) {
	app, p := newTestEnv(t)
	p.attached[43] = false
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	w := httptest.NewRecorder()
	(&App{}).handleNetwork(w, httptest.NewRequest("GET", "/network?project_id="+testProject, nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var data struct{ Agents []networkAgent }
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data.Agents) != 2 {
		t.Fatalf("unexpected directory: %s", w.Body.String())
	}
	for _, a := range data.Agents {
		if a.ID == 43 || a.ID == 77 || a.Card == nil {
			t.Fatalf("invalid agent: %+v", a)
		}
	}
	p.failList = true
	w = httptest.NewRecorder()
	(&App{}).handleNetwork(w, httptest.NewRequest("GET", "/network?project_id="+testProject, nil))
	if !strings.Contains(w.Body.String(), "Local agent directory is unavailable") {
		t.Fatal("partial directory not explained")
	}
}

func TestPanelConnectionCheckAndAccessPreserveRoutingAndCredentials(t *testing.T) {
	advertised := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer peer-secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if advertised {
			writeJSON(w, map[string]any{"agents": []directoryEntry{{CardID: "card-1", Name: "Public researcher", Online: true}}})
		} else {
			writeJSON(w, map[string]any{"agents": []directoryEntry{}})
		}
	}))
	defer server.Close()
	app, _ := newTestEnv(t)
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	a := &App{}
	keys, err := loadPeerKeyring(app)
	if err != nil {
		t.Fatal(err)
	}
	peer := peerConfig{ID: "remote", Name: "Remote", BaseURL: server.URL, Token: "peer-secret", Kind: "node", ManagedBy: "operator"}
	if err = storePeer(app.AppDB(), keys, peer, nil); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleConnectionItem(w, httptest.NewRequest("POST", "/connections/remote/check?project_id="+testProject, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatal(w.Body.String())
		}
		if strings.Contains(w.Body.String(), "peer-secret") {
			t.Fatal("credential leaked")
		}
	}
	check()
	remote, err := getRemoteAgentByPeerCard(app.AppDB(), "remote", "card-1")
	if err != nil || remote == nil {
		t.Fatal("check did not discover agent")
	}
	advertised = false
	check()
	remote, err = getRemoteAgentByPeerCard(app.AppDB(), "remote", "card-1")
	if err != nil || remote == nil {
		t.Fatal("routing record deleted")
	}
	w := httptest.NewRecorder()
	a.handleNetwork(w, httptest.NewRequest("GET", "/network?project_id="+testProject, nil))
	if strings.Contains(w.Body.String(), "Public researcher") {
		t.Fatal("withdrawn agent remains visible")
	}
	advertised = true
	check()
	w = httptest.NewRecorder()
	a.handleNetwork(w, httptest.NewRequest("GET", "/network?project_id="+testProject, nil))
	if !strings.Contains(w.Body.String(), "Public researcher") {
		t.Fatal("re-advertised agent remains hidden")
	}
	// Remote filtering uses peer/card identity, not fuzzy participant names.
	for _, cid := range []string{"card-1", "different-card"} {
		_, err = createTask(app.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "working", FromAgentID: 41, ToAgentName: "Public researcher", PeerID: "remote", RemoteCardID: cid, Direction: "outbound"})
		if err != nil {
			t.Fatal(err)
		}
	}
	filtered := httptest.NewRecorder()
	a.handlePanelTasks(filtered, httptest.NewRequest("GET", "/tasks?project_id="+testProject+"&agent_address=a2a:"+remote.Ref, nil))
	var filteredPage struct{ Total int }
	if err = json.Unmarshal(filtered.Body.Bytes(), &filteredPage); err != nil || filteredPage.Total != 1 {
		t.Fatalf("remote identity filter: %s", filtered.Body.String())
	}
	w = httptest.NewRecorder()
	a.handleConnectionItem(w, httptest.NewRequest("PATCH", "/connections/remote?project_id="+testProject, strings.NewReader(`{"discover_agents":["41"],"invoke_agents":[]}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err := findPeer(app, "remote")
	if err != nil || saved.Token != "peer-secret" || len(saved.DiscoverAgents) != 1 {
		t.Fatalf("access update corrupted peer: %v", err)
	}
	peer.ManagedBy = "config"
	if err = storePeer(app.AppDB(), keys, peer, nil); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.handleConnectionItem(w, httptest.NewRequest("PATCH", "/connections/remote?project_id="+testProject, strings.NewReader(`{"invoke_agents":["*"]}`)))
	if w.Code != 409 {
		t.Fatal("configuration ownership bypassed")
	}
	server.Close()
	w = httptest.NewRecorder()
	a.handleConnectionItem(w, httptest.NewRequest("POST", "/connections/remote/check?project_id="+testProject, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatal("failed check claimed success")
	}
}
