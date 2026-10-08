//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar, talk MCP +
// REST. Validates the SDK wiring (manifest parsing at boot,
// migrations on disk, JSON-RPC dispatch, route mounting, /health,
// auth header) end-to-end.
//
// Run with:  go test -tags integration ./...

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_AttributeScalarCompatibilityAndReadback(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	for key, typ := range map[string]string{"opportunity_score": "number", "custom_flag": "bool", "external_code": "text"} {
		sc.MCP("contacts_define_attribute", map[string]any{"key": key, "label": key, "type": typ})
	}
	for _, row := range []struct {
		name  string
		input any
		want  float64
	}{{"ACC", "88", 88}, {"MAYU", "83", 83}, {"DiagVision", float64(89), 89}} {
		created := sc.MCP("contacts_create", map[string]any{"display_name": row.name})
		id := created["contact"].(map[string]any)["id"]
		for key, value := range map[string]any{"opportunity_score": row.input, "custom_flag": "false", "external_code": "00123"} {
			sc.MCP("contacts_set_attribute", map[string]any{"contact_id": id, "key": key, "value": value, "source": "agent:integration"})
		}
		read := func() map[string]any {
			contact := sc.MCP("contacts_get", map[string]any{"id": id})["contact"].(map[string]any)
			values := map[string]any{}
			for _, raw := range contact["attributes"].([]any) {
				attribute := raw.(map[string]any)
				values[attribute["key"].(string)] = attribute["value"]
				if attribute["source"] != "agent:integration" {
					t.Fatalf("attribute provenance lost: %v", attribute)
				}
			}
			return values
		}
		values := read()
		if values["opportunity_score"] != row.want || values["custom_flag"] != false || values["external_code"] != "00123" {
			t.Fatalf("incorrect typed readback for %s: %v", row.name, values)
		}
		// The fix is limited to the MCP scalar boundary, not lax HTTP typing.
		resp := sc.POST("/contacts/"+anyString(id)+"/attributes", map[string]any{"key": "opportunity_score", "value": "77"}, nil)
		if resp.Status != http.StatusInternalServerError || !strings.Contains(string(resp.Body), "expects a number") {
			t.Fatalf("HTTP did not reject string number via strict validation: %d %s", resp.Status, resp.Body)
		}
		if got := read()["opportunity_score"]; got != row.want {
			t.Fatalf("rejected HTTP write changed score: %v", got)
		}
	}
	listed, err := sc.MCPRaw("tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range listed["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["name"] == "contacts_set_attribute" {
			properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			value := properties["value"].(map[string]any)
			if len(value["anyOf"].([]any)) != 5 || !strings.Contains(tool["description"].(string), "contacts_get") {
				t.Fatalf("live schema/readback guidance missing: %v", tool)
			}
			return
		}
	}
	t.Fatal("contacts_set_attribute missing from live tools/list")
}

func TestSidecar_ResolveAudienceContextHandler(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	created := sc.MCP("contacts_upsert_by_channel", map[string]any{
		"kind": "email", "value": "audience@example.test",
	})
	contact := created["contact"].(map[string]any)
	for _, counts := range []bool{false, true} {
		r := sc.MCP("contacts_resolve_audience", map[string]any{
			"channel": "email", "contact_id": contact["id"], "include_counts": counts,
		})
		recipients, ok := r["recipients"].([]any)
		if !ok || len(recipients) != 1 || recipients[0].(map[string]any)["address"] != "audience@example.test" {
			t.Fatalf("unexpected audience result: %#v", r)
		}
		want := float64(0)
		if counts {
			want = 1
		}
		if r["raw_count"] != want || r["eligible_count"] != want {
			t.Fatalf("counts=%v: unexpected count fields: %#v", counts, r)
		}
	}
}

func TestSidecar_InboxReadOnlyAnnotations(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out, err := sc.MCPRaw("tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := out["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list missing tools array: %#v", out)
	}
	found := false
	draftReads := 0
	for _, raw := range tools {
		tool := raw.(map[string]any)
		annotations, _ := tool["annotations"].(map[string]any)
		if tool["name"] == "conversations_inbox" {
			found = true
			if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false {
				t.Fatalf("inbox annotations absent or incorrect in tools/list: %#v", tool)
			}
		} else if tool["name"] == "conversation_drafts_get" || tool["name"] == "conversation_drafts_list" {
			draftReads++
			if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false {
				t.Fatalf("draft read annotation incorrect: %v", tool)
			}
		} else if annotations["readOnlyHint"] == true {
			t.Errorf("unexpected read-only annotation on %v", tool["name"])
		}
	}
	if !found {
		t.Fatal("conversations_inbox missing from tools/list")
	}
	if draftReads != 2 {
		t.Fatalf("draft read tools found=%d", draftReads)
	}
	// Discovery must not rename the tool or alter its query behavior.
	inbox := sc.MCP("conversations_inbox", map[string]any{"limit": 1})
	if inbox["count"] != float64(0) || inbox["total"] != float64(0) {
		t.Fatalf("unexpected empty inbox response: %#v", inbox)
	}
}

func TestSidecar_SavedReplyDraftRoundTrip(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var inbound map[string]any
	resp := sc.POST("/inbound", map[string]any{"channel": "email", "from": "draft-customer@example.test", "matched_recipient": "support@example.test", "subject": "Draft roundtrip", "body_text": "Question", "message_id": 721}, &inbound)
	if resp.Status != 200 {
		t.Fatalf("inbound: %d %s", resp.Status, resp.Body)
	}
	created := sc.MCP("conversation_drafts_create", map[string]any{"conversation_id": inbound["conversation_id"], "body": "Saved through MCP", "source": "agent:Writer"})
	draft := created["draft"].(map[string]any)
	var edited map[string]any
	resp = sc.PATCH("/drafts/"+anyString(draft["id"]), map[string]any{"body": "Edited through HTTP", "expected_revision": draft["revision"], "source": "human"}, &edited)
	if resp.Status != 200 {
		t.Fatalf("HTTP save: %d %s", resp.Status, resp.Body)
	}
	updated := edited["draft"].(map[string]any)
	read := sc.MCP("conversation_drafts_get", map[string]any{"id": draft["id"]})["draft"].(map[string]any)
	if read["content"].(map[string]any)["body"] != "Edited through HTTP" || read["revision"] != updated["revision"] {
		t.Fatalf("draft roundtrip lost content: %v", read)
	}
	var list map[string]any
	resp = sc.GET("/drafts?conversation_id="+anyString(inbound["conversation_id"]), &list)
	if resp.Status != 200 || list["total"] != float64(1) {
		t.Fatalf("HTTP list: %d %v", resp.Status, list)
	}
	resp = sc.PATCH("/drafts/"+anyString(draft["id"]), map[string]any{"body": "Stale edit", "expected_revision": draft["revision"]}, nil)
	if resp.Status != 409 {
		t.Fatalf("stale save: %d %s", resp.Status, resp.Body)
	}
	// No Messaging binding is configured: explicit send must fail safely.
	out := sc.MCP("conversation_drafts_send", map[string]any{"id": draft["id"], "expected_revision": updated["revision"]})
	if out["sent"] != false || out["draft"].(map[string]any)["content"].(map[string]any)["body"] != "Edited through HTTP" {
		t.Fatalf("failed send lost draft: %v", out)
	}
	conversation := sc.MCP("contacts_get_conversation", map[string]any{"id": inbound["contact_id"], "conversation_id": inbound["conversation_id"]})
	if conversation["conversation"].(map[string]any)["status"] != "open" || len(conversation["activities"].([]any)) != 1 {
		t.Fatalf("save/failed send changed thread: %v", conversation)
	}
}

func TestSidecar_FullToolFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Create via MCP.
	r := sc.MCP("contacts_upsert_by_channel", map[string]any{
		"kind":  "email",
		"value": "alice@example.com",
		"defaults": map[string]any{
			"first_name": "Alice",
			"last_name":  "Cooper",
		},
		"source": "test",
	})
	if r["was_created"] != true {
		t.Fatalf("expected was_created=true, got %#v", r["was_created"])
	}
	contact := r["contact"].(map[string]any)
	id := int64(contact["id"].(float64))

	// Same call again — should return the existing row.
	r2 := sc.MCP("contacts_upsert_by_channel", map[string]any{
		"kind":  "email",
		"value": "alice@example.com",
	})
	if r2["was_created"] != false {
		t.Errorf("expected was_created=false on dedupe, got %#v", r2["was_created"])
	}

	// Fetch via REST.
	var rest map[string]any
	sc.GET("/contacts/?id="+itoa(id), &rest)
	// /contacts/<id> path used by the panel — go ServeMux normalises
	// trailing slashes per its rules. Use the panel-shape path:
	resp := sc.GET("/contacts/"+itoa(id), &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET /contacts/%d: %d body=%s", id, resp.Status, string(resp.Body))
	}
	gotContact := rest["contact"].(map[string]any)
	if gotContact["primary_email"] != "alice@example.com" {
		t.Errorf("primary_email=%v", gotContact["primary_email"])
	}

	// Log activity via REST.
	var actOut map[string]any
	sc.POST("/contacts/"+itoa(id)+"/activities", map[string]any{
		"kind": "note", "body": "first contact made", "source": "test",
	}, &actOut)
	if actOut["activity"] == nil {
		t.Errorf("no activity in response: %v", actOut)
	}

	// Fetch context via MCP — should include the activity.
	ctxOut := sc.MCP("contacts_get_context", map[string]any{
		"id":             id,
		"activity_limit": 10,
	})
	acts := ctxOut["activities"].([]any)
	if len(acts) != 1 {
		t.Errorf("activities=%d, want 1", len(acts))
	}
}

func TestSidecar_ProjectScopeIsolation(t *testing.T) {
	// One sidecar pinned to project A.
	a := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-A"))
	a.MCP("contacts_create", map[string]any{
		"first_name": "AOnly",
		"channels":   []any{map[string]any{"kind": "email", "value": "a@x.com", "is_primary": true}},
	})
	// Search in A finds it.
	out := a.MCP("contacts_search", map[string]any{"q": "AOnly"})
	if out["count"].(float64) != 1 {
		t.Errorf("project A: expected 1, got %v", out["count"])
	}

	// Spawning a second binary pinned to project B doesn't see A's data
	// because each sidecar gets its own temp DB. (Tests cross-DB
	// isolation — the partition column is irrelevant when DBs differ.)
	b := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-B"))
	out2 := b.MCP("contacts_search", map[string]any{"q": "AOnly"})
	if out2["count"].(float64) != 0 {
		t.Errorf("project B: expected 0, got %v", out2["count"])
	}
}

func TestSidecar_GlobalScope_RequiresProjectIDPerCall(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".") // no project_id = global
	// Without _project_id, the call should fail with a clear error.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "contacts_search",
		"arguments": map[string]any{"q": "x"},
	})
	if err == nil {
		t.Fatal("expected MCP error when scope=global and project_id is missing")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}

	// Same call with _project_id should work.
	out := sc.MCP("contacts_search", map[string]any{
		"_project_id": "proj-X",
		"q":           "anything",
	})
	if out["count"].(float64) != 0 {
		t.Errorf("expected 0 results in fresh project, got %v", out["count"])
	}
}

func itoa(i int64) string {
	return strconvFormatInt(i)
}

// avoid pulling in strconv just for this helper inside the build-tagged file
func strconvFormatInt(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	if neg {
		out = "-" + out
	}
	return out
}

// Both binaries run with temporary SQLite databases. The local gateway supplies
// install identity and relays real MCP envelopes; no provider is configured.
func TestSidecar_RealMessagingBindingAndRouting(t *testing.T) {
	messaging := tk.SpawnSidecar(t, "../messaging", tk.WithProjectID("test-proj"))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/whoami":
			json.NewEncoder(w).Encode(map[string]any{"app_name": "crm", "install_id": 99, "project_id": "test-proj", "bindings": map[string]any{"messaging": 42}})
		case "/api/apps/callback/agents/42":
			json.NewEncoder(w).Encode(map[string]any{"id": 42, "name": "messaging", "status": "running", "project_id": "test-proj"})
		case "/api/app-events/internal/emit":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/apps/callback/apps/messaging/call":
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			result, err := messaging.MCPRaw("tools/call", map[string]any{"name": body.Tool, "arguments": body.Input})
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			payload, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	crm := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("APTEVA_INSTALL_ID", "99"))
	var routes map[string]any
	res := crm.GET("/messaging/routes", &routes)
	if res.Status != 200 {
		t.Fatalf("read bound routes: %d %s", res.Status, res.Body)
	}
	var configured map[string]any
	res = crm.POST("/messaging/routes", map[string]any{"channel": "email"}, &configured)
	if res.Status != 200 {
		t.Fatalf("configure bound routes: %d %s", res.Status, res.Body)
	}
	got := messaging.MCP("inbound_route_list", map[string]any{})
	encoded, _ := json.Marshal(got)
	if !strings.Contains(string(encoded), "crm") {
		t.Fatalf("real Messaging has no CRM route: %s", encoded)
	}
	// Read-only sender lookup traverses the actual bound app's MCP endpoint.
	crm.MCP("messaging_senders_list", map[string]any{"channel": "email"})
}

func TestSidecar_EmailUnsubscribeWithRealMessaging(t *testing.T) {
	messaging := tk.SpawnSidecar(t, "../messaging", tk.WithProjectID("test-proj"))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/whoami":
			json.NewEncoder(w).Encode(map[string]any{"app_name": "crm", "install_id": 99, "project_id": "test-proj", "bindings": map[string]any{"messaging": 42}})
		case "/api/apps/callback/agents/42":
			json.NewEncoder(w).Encode(map[string]any{"id": 42, "name": "messaging", "status": "running", "project_id": "test-proj"})
		case "/api/app-events/internal/emit":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/apps/callback/apps/messaging/call":
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			var result map[string]any
			var err error
			// Only receiving ownership is a fixture; suppression writes/checks
			// traverse the real Messaging binary and its migrated SQLite.
			if body.Tool == "senders_list" {
				result = map[string]any{"senders": []map[string]any{{"address": "support@example.test"}}}
			} else {
				result, err = messaging.MCPRaw("tools/call", map[string]any{"name": body.Tool, "arguments": body.Input})
			}
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			payload, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	crm := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("APTEVA_INSTALL_ID", "99"))
	var inbound map[string]any
	res := crm.POST("/inbound", map[string]any{"channel": "email", "from": "edbis@free.fr", "matched_recipient": "support@example.test", "body_text": "UNSUBSCRIBE!!!!", "message_id": 1800, "message_id_header": "<request@free.fr>"}, &inbound)
	if res.Status != 200 {
		t.Fatalf("inbound: %d %s", res.Status, res.Body)
	}
	path := "/contacts/" + anyString(inbound["contact_id"]) + "/conversations/" + anyString(inbound["conversation_id"]) + "/unsubscribe"
	var preview emailUnsubscribeState
	res = crm.GET(path, &preview)
	if res.Status != 200 || preview.Address != "edbis@free.fr" || preview.OutboundBlocked {
		t.Fatalf("preview: %d %+v %s", res.Status, preview, res.Body)
	}
	for i := 0; i < 2; i++ {
		var saved emailUnsubscribeState
		res = crm.POST(path, map[string]any{"expected_address": preview.Address}, &saved)
		if res.Status != 200 || !saved.Confirmed || !saved.Unsubscribed || saved.InboundBlocked {
			t.Fatalf("save: %d %+v %s", res.Status, saved, res.Body)
		}
	}
	for _, direction := range []string{"outbound", "inbound"} {
		check := messaging.MCP("suppression_check", map[string]any{"address": "edbis@free.fr", "direction": direction})
		if check["suppressed"] != (direction == "outbound") || check["check_direction"] != direction {
			t.Fatalf("direction mismatch: %v", check)
		}
	}
	contact := crm.MCP("contacts_get", map[string]any{"id": inbound["contact_id"]})["contact"].(map[string]any)
	if contact["status"] != "active" {
		t.Fatalf("contact changed status: %v", contact)
	}
	read := crm.MCP("contacts_get_conversation", map[string]any{"id": inbound["contact_id"], "conversation_id": inbound["conversation_id"]})
	if read["conversation"].(map[string]any)["status"] != "open" || len(read["activities"].([]any)) != 2 {
		t.Fatalf("history changed or duplicate audit: %v", read)
	}
	res = crm.POST("/inbound", map[string]any{"channel": "email", "from": "edbis@free.fr", "matched_recipient": "support@example.test", "body_text": "Later reply", "message_id": 1801, "in_reply_to": "<request@free.fr>"}, &inbound)
	if res.Status != 200 {
		t.Fatalf("later inbound rejected: %d %s", res.Status, res.Body)
	}
}
