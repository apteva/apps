package main

import (
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type reviewH1EventPlatform struct {
	tk.BasePlatformClient
	mu      sync.Mutex
	sent    []int64
	onSend  func()
	release chan struct{}
}

func (p *reviewH1EventPlatform) SendEvent(agent int64, _ string) error {
	p.mu.Lock()
	p.sent = append(p.sent, agent)
	hook := p.onSend
	p.onSend = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func reviewH1OutboxCall(t *testing.T, app *App, id string) {
	t.Helper()
	call := testCall(id, "pending")
	call.Direction = "inbound"
	call.ProjectID = "project-a"
	call.AgentID = 7
	if _, _, err := app.db().insertInboundCallWithEvent(call, "incoming"); err != nil {
		t.Fatal(err)
	}
}

// Inline delivery and the lifecycle tick both see a due row. Only the first
// claimant sends; the other sees the lease and skips.
func TestReviewH1InboundOutboxSendsOnceUnderRace(t *testing.T) {
	platform := &reviewH1EventPlatform{}
	app, ctx := withTelephonyTestContext(t, platform)
	reviewH1OutboxCall(t, app, "outbox-race")
	platform.onSend = func() {
		// While the first send is in flight, a second sender tries the row.
		if err := app.deliverOutboxCall(ctx, "outbox-race"); err != nil {
			t.Error(err)
		}
	}
	if err := app.deliverOutboxCall(ctx, "outbox-race"); err != nil {
		t.Fatal(err)
	}
	if len(platform.sent) != 1 {
		t.Fatalf("sent %d times", len(platform.sent))
	}
	var delivered string
	_ = app.db().db.QueryRow(`SELECT delivered_at FROM inbound_event_outbox WHERE call_id='outbox-race'`).Scan(&delivered)
	if delivered == "" {
		t.Fatal("not marked delivered")
	}
}

// A re-arm for a new agent while the old send is in flight must stay pending:
// the old send's completion is conditional on its lease token.
func TestReviewH1InboundOutboxReArmNotMarkedByOldSend(t *testing.T) {
	platform := &reviewH1EventPlatform{}
	app, ctx := withTelephonyTestContext(t, platform)
	reviewH1OutboxCall(t, app, "outbox-rearm")
	platform.onSend = func() {
		// Same upsert shape writeRoutingProgressTx uses for a new agent.
		if _, err := app.db().db.Exec(`INSERT INTO inbound_event_outbox(call_id,project_id,agent_id,message,next_attempt_at) VALUES(?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET agent_id=excluded.agent_id,message=excluded.message,delivered_at='',lease_token='',next_attempt_at=excluded.next_attempt_at WHERE inbound_event_outbox.agent_id<>excluded.agent_id`,
			"outbox-rearm", "project-a", 8, "new agent", time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Error(err)
		}
	}
	if err := app.deliverOutboxCall(ctx, "outbox-rearm"); err != nil {
		t.Fatal(err)
	}
	var agent int64
	var delivered string
	_ = app.db().db.QueryRow(`SELECT agent_id, delivered_at FROM inbound_event_outbox WHERE call_id='outbox-rearm'`).Scan(&agent, &delivered)
	if agent != 8 || delivered != "" {
		t.Fatalf("re-armed row agent=%d delivered=%q", agent, delivered)
	}
	if err := app.deliverOutboxCall(ctx, "outbox-rearm"); err != nil {
		t.Fatal(err)
	}
	if len(platform.sent) != 2 || platform.sent[1] != 8 {
		t.Fatalf("sends=%v", platform.sent)
	}
}
