//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Sanity ───────────────────────────────────────────────────────

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

// ─── Template lifecycle ───────────────────────────────────────────

func TestSidecar_TemplateCRUD(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	created := sc.MCP("template_create", map[string]any{
		"name":      "welcome",
		"subject":   "Welcome {{name}}",
		"body_text": "Hi {{name}}, your code is {{code}}.",
	})
	tpl := created["template"].(map[string]any)
	id := int64(tpl["id"].(float64))
	if tpl["name"] != "welcome" || tpl["channel"] != "email" {
		t.Errorf("template=%+v", tpl)
	}

	got := sc.MCP("template_get", map[string]any{"id": id})
	if !got["found"].(bool) {
		t.Fatalf("template not found after create: %+v", got)
	}

	updated := sc.MCP("template_update", map[string]any{
		"id":      id,
		"subject": "Welcome back {{name}}",
	})
	if updated["template"].(map[string]any)["subject"] != "Welcome back {{name}}" {
		t.Errorf("update did not stick: %+v", updated)
	}

	listed := sc.MCP("template_list", map[string]any{})
	if listed["count"].(float64) != 1 {
		t.Errorf("expected 1 template, got %v", listed["count"])
	}

	sc.MCP("template_delete", map[string]any{"id": id})
	listed = sc.MCP("template_list", map[string]any{})
	if listed["count"].(float64) != 0 {
		t.Errorf("expected 0 after delete, got %v", listed["count"])
	}
}

// ─── Inbound route lifecycle ──────────────────────────────────────

func TestSidecar_InboundRouteCRUD(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	out := sc.MCP("inbound_route_set", map[string]any{
		"pattern":      "mailto:support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	})
	r := out["route"].(map[string]any)
	id := int64(r["id"].(float64))
	if r["pattern"] != "mailto:support+*@acme.com" {
		t.Errorf("pattern=%v", r["pattern"])
	}

	// Idempotent — same payload returns same id.
	out2 := sc.MCP("inbound_route_set", map[string]any{
		"pattern":      "mailto:support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	})
	id2 := int64(out2["route"].(map[string]any)["id"].(float64))
	if id != id2 {
		t.Errorf("expected idempotent ids, got %d vs %d", id, id2)
	}

	listed := sc.MCP("inbound_route_list", map[string]any{})
	if listed["count"].(float64) != 1 {
		t.Errorf("count=%v", listed["count"])
	}

	sc.MCP("inbound_route_delete", map[string]any{"id": id})
	listed = sc.MCP("inbound_route_list", map[string]any{})
	if listed["count"].(float64) != 0 {
		t.Errorf("count after delete=%v", listed["count"])
	}
}

// ─── Suppression manual lifecycle ─────────────────────────────────

func TestSidecar_SuppressionFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	sc.MCP("suppression_add", map[string]any{
		"address": "blocked@example.com",
		"reason":  "user-requested",
	})
	listed := sc.MCP("suppression_list", map[string]any{})
	rows := listed["suppressions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	first := rows[0].(map[string]any)
	if first["address"] != "mailto:blocked@example.com" {
		t.Errorf("address=%v", first["address"])
	}
	if first["source"] != "manual" {
		t.Errorf("source=%v", first["source"])
	}

	sc.MCP("suppression_remove", map[string]any{"address": "blocked@example.com"})
	listed = sc.MCP("suppression_list", map[string]any{})
	if listed["count"].(float64) != 0 {
		t.Errorf("expected 0 after remove, got %v", listed["count"])
	}
}

// ─── HTTP read endpoints ──────────────────────────────────────────

func TestSidecar_HTTPListEndpoints(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Pre-seed via MCP so the HTTP read shows non-empty.
	sc.MCP("template_create", map[string]any{"name": "t1"})
	sc.MCP("inbound_route_set", map[string]any{
		"pattern": "mailto:support@acme.com", "target_app": "support", "target_route": "/inbound",
	})
	sc.MCP("suppression_add", map[string]any{"address": "x@y.com", "reason": "test"})

	for _, c := range []struct {
		path string
		key  string
	}{
		{"/templates", "templates"},
		{"/inbound-routes", "routes"},
		{"/suppressions", "suppressions"},
	} {
		var got map[string]any
		resp := sc.GET(c.path, &got)
		if resp.Status != 200 {
			t.Errorf("%s status=%d", c.path, resp.Status)
			continue
		}
		arr, ok := got[c.key].([]any)
		if !ok || len(arr) == 0 {
			t.Errorf("%s body=%v (expected non-empty %s array)", c.path, got, c.key)
		}
	}
}

// ─── Bounce webhook — unknown provider id ─────────────────────────

func TestSidecar_BounceWebhook_UnknownProviderID(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	innerSES := map[string]any{
		"notificationType": "Bounce",
		"mail":             map[string]any{"messageId": "ses-no-such-id"},
		"bounce": map[string]any{
			"bounceType": "Permanent",
			"bouncedRecipients": []map[string]any{
				{"emailAddress": "ghost@example.com"},
			},
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	req, _ := http.NewRequest("POST", sc.URL()+"/webhooks/ses-bounces?project_id=test-proj", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Sns-Message-Type", "Notification")
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["matched"] != false {
		t.Errorf("expected matched=false for unknown provider id, got %v", out)
	}
}

// ─── Inbound webhook end-to-end (no provider needed) ──────────────

const inboundEml = "From: customer@example.com\r\n" +
	"To: support+T-9001@acme.com\r\n" +
	"Subject: Re: Order #9001\r\n" +
	"Message-ID: <abc-tier2@example.com>\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"Where is my package?\r\n"

func TestSidecar_InboundWebhook_PersistsAndAttemptsDispatch(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Register a route first so the dispatcher has a target.
	sc.MCP("inbound_route_set", map[string]any{
		"pattern":      "mailto:support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	})

	innerSES := map[string]any{
		"notificationType": "Received",
		"content":          inboundEml,
		"mail":             map[string]any{"messageId": "ses-inbound-tier2"},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	req, _ := http.NewRequest("POST", sc.URL()+"/webhooks/ses-inbound?project_id=test-proj", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Sns-Message-Type", "Notification")
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("inbound status=%d", resp.StatusCode)
	}

	// Confirm the row landed via HTTP GET.
	var listed map[string]any
	listResp := sc.GET("/messages?direction=in&limit=10", &listed)
	if listResp.Status != 200 {
		t.Fatalf("list status=%d", listResp.Status)
	}
	msgs := listed["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 inbound message, got %d", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if !strings.Contains(m["subject"].(string), "Order #9001") {
		t.Errorf("subject=%v", m["subject"])
	}
	if m["from"] != "mailto:customer@example.com" {
		t.Errorf("from=%v", m["from"])
	}
	if m["matched_pattern"] != "mailto:support+*@acme.com" {
		t.Errorf("matched_pattern=%v", m["matched_pattern"])
	}
	// In tier 2 there's no real platform, so CallApp will fail and
	// route_status will be target_failed (or pending → target_failed
	// depending on the platform stub's behaviour). What matters is the
	// matching logic ran and recorded the chosen route.
	if m["route_target_app"] != "support" {
		t.Errorf("route_target_app=%v", m["route_target_app"])
	}
	if m["to_subaddress"] != "t-9001" {
		t.Errorf("to_subaddress=%v (canonicalised lowercase)", m["to_subaddress"])
	}
}

// ─── senders + send_message contract ──────────────────────────────
//
// Without a real SES binding we can't roundtrip through the provider,
// but we can assert the pre-provider contract: "no provider bound"
// and "from required" surface as MCP errors before any HTTP call.

func TestSidecar_Senders_NoProviderBound(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "senders_list",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatalf("expected error from senders_list with no provider bound")
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "no email_provider") && !strings.Contains(low, "unbound") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSidecar_SendMessage_RequiresFrom(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":   "alice@example.com",
			"body": "hi",
		},
	})
	if err == nil {
		t.Fatalf("expected error from send_message without 'from'")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "from") {
		t.Errorf("error should mention 'from': %v", err)
	}
}
