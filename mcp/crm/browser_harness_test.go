//go:build browser

package main

// Opt-in local browser fixture. All Messaging calls use an in-process double.
// Run with GOWORK=off go test -tags browser -run TestBrowserHarness -v -timeout 30m.
import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"sync"
	"testing"
)

type browserPlatform struct {
	crmRecordingPlatform
	mu sync.Mutex
}

func (p *browserPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tool == "senders_list" {
		raw, _ := json.Marshal(map[string]any{"senders": []map[string]any{{"channel": "email", "address": "sales@example.test", "display_name": "Sales", "is_default": true}, {"channel": "email", "address": "support@example.test", "display_name": "Support"}}})
		return json.Unmarshal(raw, out)
	}
	if tool == "inbound_route_list" {
		return json.Unmarshal([]byte(`{"routes":[]}`), out)
	}
	return p.crmRecordingPlatform.CallAppResult(app, tool, args, out)
}
func TestBrowserHarness(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&browserPlatform{}))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := dbListCreate(ctx.AppDB(), "test-proj", &List{Name: "Browser members", Slug: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	var alice *Contact
	for i := 0; i < 103; i++ {
		name := "Member"
		if i == 0 {
			name = "Alice"
		}
		if i == 1 {
			name = "Bob"
		}
		args := map[string]any{"first_name": name, "last_name": string(rune('A' + i%26))}
		if i == 0 {
			args["channels"] = []any{map[string]any{"kind": "email", "value": "work@example.test", "is_primary": true}, map[string]any{"kind": "email", "value": "private@example.test"}}
		}
		c, err := dbCreate(ctx.AppDB(), "test-proj", args)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			alice = c
		}
		if err = dbListAddContact(ctx.AppDB(), "test-proj", list.ID, c.ID, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "private@example.test", To: []string{"support@example.test"}, MatchedRecipient: "support@example.test", MessageID: 777, Subject: "Private address conversation", BodyText: "Please reply here."}); err != nil {
		t.Fatal(err)
	}
	if _, err = dbOpportunityCreate(ctx.AppDB(), "test-proj", opportunityCreateInput{ContactID: alice.ID, Title: "Default deal"}); err != nil {
		t.Fatal(err)
	}
	p, err := dbPipelineCreate(ctx.AppDB(), "test-proj", &Pipeline{Name: "Partnerships"}, []*PipelineStage{{Name: "New partner", Category: "open", Position: 1}, {Name: "Signed", Category: "won", Position: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 102; i++ {
		if _, err = dbOpportunityCreate(ctx.AppDB(), "test-proj", opportunityCreateInput{ContactID: alice.ID, PipelineID: p.ID, StageID: p.Stages[0].ID, Title: "Partner deal"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	t.Log("Browser API fixture listening on http://127.0.0.1:18762")
	if err = http.ListenAndServe("127.0.0.1:18762", mux); err != nil {
		t.Fatal(err)
	}
}
