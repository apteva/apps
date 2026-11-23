package main

import (
	"encoding/json"
	"testing"
	"time"
)

func widgetTestExec(t *testing.T, a *App, q string, args ...any) {
	t.Helper()
	if _, err := a.db().db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestLiveCallsWidgetPhasesFiltersAndAnswerers(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	now := time.Now().UTC().Format(time.RFC3339)
	widgetTestExec(t, a, `INSERT INTO routing_destinations(id,project_id,name,kind,config_json,enabled,created_at,updated_at) VALUES
 ('sales','project-a','Sales desk','browser','{}',1,?,?),
 ('support','project-a','Support desk','browser','{}',1,?,?),
 ('night','project-a','Night desk','browser','{}',1,?,?),
 ('bot','project-a','Booking AI','ai','{}',1,?,?),
 ('off','project-a','Disabled desk','browser','{}',0,?,?)`, now, now, now, now, now, now, now, now, now, now)
	policy := phonePolicy{Revision: 1, Users: []phoneUser{
		{Identity: alice, Enabled: true, phoneGrant: phoneGrant{Destinations: []string{"sales"}}},
		{Identity: bob, Enabled: true, phoneGrant: phoneGrant{Destinations: []string{"support"}}},
	}}
	raw, _ := json.Marshal(policy)
	widgetTestExec(t, a, `INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES('project-a',1,?)`, string(raw))

	phoneTestCall(t, a, "ringing-in", "pending")
	phoneTestCall(t, a, "talking", "in-progress")
	widgetTestExec(t, a, `UPDATE calls SET answered_at=?, routing_destination_id='sales' WHERE id='talking'`, now)
	widgetTestExec(t, a, `INSERT INTO telephony_call_owners(call_id,project_id,principal,destination_id) VALUES('talking','project-a',?,'sales')`, alice.key())
	phoneTestCall(t, a, "held", "in-progress")
	widgetTestExec(t, a, `UPDATE calls SET answered_at=?, hold_state='held', peer_kind='realtime', agent_id=7, routing_destination_id='' WHERE id='held'`, now)
	phoneTestCall(t, a, "outbound", "ringing")
	widgetTestExec(t, a, `UPDATE calls SET direction='outbound', from_number='+15550001111', to_number='+15559990000', routing_destination_id='' WHERE id='outbound'`)
	phoneTestCall(t, a, "ended", "completed")
	phoneTestCall(t, a, "child-leg", "ringing")
	widgetTestExec(t, a, `UPDATE calls SET ingress_path='ring_group' WHERE id='child-leg'`)

	leave := a.softphoneStreams.enter("project-a", bob)
	defer leave()

	result := dashboardTestResult(t, a, "?view=live")
	calls := result["calls"].([]any)
	byID := map[string]map[string]any{}
	for _, c := range calls {
		m := c.(map[string]any)
		byID[m["call_id"].(string)] = m
	}
	if len(byID) != 4 || byID["ended"] != nil || byID["child-leg"] != nil {
		t.Fatalf("unexpected calls %v", encodeJSONForTest(calls))
	}
	if byID["ringing-in"]["phase"] != "ringing" || byID["ringing-in"]["handler"] != "Sales desk" || byID["ringing-in"]["number"] != "+13502231050" {
		t.Fatal(byID["ringing-in"])
	}
	if byID["talking"]["phase"] != "in_progress" || byID["talking"]["answered_by"] != "person" {
		t.Fatal(byID["talking"])
	}
	if byID["held"]["phase"] != "on_hold" || byID["held"]["answered_by"] != "ai" || byID["held"]["handler"] != "AI agent #7" {
		t.Fatal(byID["held"])
	}
	if byID["outbound"]["number"] != "+15550001111" || byID["outbound"]["remote"] != "+15559990000" {
		t.Fatal(byID["outbound"])
	}
	summary := result["summary"].(map[string]any)
	if summary["ringing"] != float64(2) || summary["in_progress"] != float64(1) || summary["on_hold"] != float64(1) {
		t.Fatal(summary)
	}
	states := map[string]string{}
	for _, v := range result["answerers"].([]any) {
		m := v.(map[string]any)
		states[m["name"].(string)] = m["state"].(string)
	}
	want := map[string]string{"Sales desk": "busy", "Support desk": "connected", "Night desk": "offline", "Booking AI": "available"}
	if len(states) != len(want) {
		t.Fatal(states)
	}
	for k, v := range want {
		if states[k] != v {
			t.Fatalf("%s: %s, want %s (%v)", k, states[k], v, states)
		}
	}

	filtered := dashboardTestResult(t, a, "?view=live&direction=outbound")
	if n := len(filtered["calls"].([]any)); n != 1 {
		t.Fatal("direction filter", n)
	}
	filtered = dashboardTestResult(t, a, "?view=live&number=%2B13502231050")
	if n := len(filtered["calls"].([]any)); n != 3 {
		t.Fatal("number filter", n)
	}
	if w := phoneTestRequest(a, nil, "GET", "/audio-health?view=live&direction=sideways", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}

	leave()
	result = dashboardTestResult(t, a, "?view=live")
	for _, v := range result["answerers"].([]any) {
		m := v.(map[string]any)
		if m["name"] == "Support desk" && m["state"] != "offline" {
			t.Fatal("presence survived stream close", m)
		}
	}
}

func TestSoftphonePresenceCountsStreams(t *testing.T) {
	var p softphonePresence
	id := phoneTestIdentity("alice")
	first, second := p.enter("project-a", id), p.enter("project-a", id)
	if !p.connected("project-a")[id.key()] {
		t.Fatal("not connected")
	}
	first()
	first() // idempotent
	if !p.connected("project-a")[id.key()] {
		t.Fatal("second stream lost")
	}
	second()
	if len(p.connected("project-a")) != 0 || len(p.open) != 0 {
		t.Fatal("presence leaked", p.open)
	}
	p.enter("project-a", phoneIdentity{})() // invalid identities are ignored
}

func TestCallsOverviewWidgetOutcomes(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	at := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339) }
	add := func(id, status, direction, peer, initiator string, answerAfter, endAfter time.Duration, talk int) {
		phoneTestCall(t, a, id, status)
		answered := ""
		if answerAfter > 0 {
			answered = at(answerAfter)
		}
		ended := ""
		if endAfter > 0 {
			ended = at(endAfter)
		}
		widgetTestExec(t, a, `UPDATE calls SET placed_at=?, direction=?, peer_kind=?, termination_initiator=?, answered_at=NULLIF(?,''), ended_at=NULLIF(?,''), talk_duration_seconds=? WHERE id=?`, at(0), direction, peer, initiator, answered, ended, talk, id)
	}
	add("person", "completed", "inbound", "human", "caller", 10*time.Second, 70*time.Second, 60)
	add("ai", "completed", "inbound", "realtime", "agent", 20*time.Second, 140*time.Second, 0)
	add("abandoned", "canceled", "inbound", "human", "caller", 0, 15*time.Second, 0)
	add("hung-up", "completed", "inbound", "human", "caller", 0, 5*time.Second, 0)
	add("missed", "no-answer", "inbound", "human", "", 0, 30*time.Second, 0)
	add("failed", "failed", "outbound", "human", "telephony", 0, time.Second, 0)
	add("live", "in-progress", "inbound", "human", "", 5*time.Second, 0, 0)
	add("outbound-missed", "no-answer", "outbound", "human", "caller", 0, 30*time.Second, 0)
	phoneTestCall(t, a, "old", "completed")
	widgetTestExec(t, a, `UPDATE calls SET placed_at=? WHERE id='old'`, base.Add(-48*time.Hour).Format(time.RFC3339))

	from := base.Add(-time.Hour).Format(time.RFC3339)
	until := time.Now().UTC().Add(time.Minute).Truncate(time.Second).Format(time.RFC3339)
	result := dashboardTestResult(t, a, "?view=overview&from="+from+"&until="+until)
	totals := result["totals"].(map[string]any)
	expect := map[string]float64{"calls": 8, "answered_person": 1, "answered_ai": 1, "abandoned": 2, "missed": 2, "failed": 1, "active": 1}
	for k, v := range expect {
		if totals[k] != v {
			t.Fatalf("%s=%v want %v (%v)", k, totals[k], v, totals)
		}
	}
	// 2 answered of 6 decided calls (failed and active excluded).
	if rate := totals["answer_rate"].(float64); rate < 33.3 || rate > 33.4 {
		t.Fatal(rate)
	}
	// Wait averages answered calls (10, 20, 5); length averages ended answered calls (60, 120).
	if totals["average_wait_seconds"].(float64) < 11.6 || totals["average_wait_seconds"].(float64) > 11.7 {
		t.Fatal(totals["average_wait_seconds"])
	}
	if totals["average_length_seconds"] != float64(90) {
		t.Fatal(totals["average_length_seconds"])
	}
	if result["granularity"] != "hour" {
		t.Fatal(result["granularity"])
	}
	sum := 0.0
	for _, b := range result["buckets"].([]any) {
		m := b.(map[string]any)
		sum += m["answered_person"].(float64) + m["answered_ai"].(float64) + m["missed"].(float64) + m["abandoned"].(float64)
	}
	if sum != 7 { // every terminal call lands in exactly one bucket; failed counts as missed
		t.Fatal("bucket sum", sum)
	}
	inbound := dashboardTestResult(t, a, "?view=overview&direction=inbound&from="+from+"&until="+until)
	if inbound["totals"].(map[string]any)["calls"] != float64(6) {
		t.Fatal(inbound["totals"])
	}
	if w := phoneTestRequest(a, nil, "GET", "/audio-health?view=overview&direction=bad", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestDashboardWidgetViewsRequireOperator(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	user := phoneTestIdentity("alice")
	for _, view := range []string{"live", "overview"} {
		if w := phoneTestRequest(a, &user, "GET", "/audio-health?view="+view, nil); w.Code != 403 {
			t.Fatal(view, w.Code)
		}
		if w := phoneTestRequest(a, nil, "GET", "/audio-health?view="+view, nil); w.Code != 200 {
			t.Fatal(view, w.Code, w.Body.String())
		}
	}
}

func encodeJSONForTest(v any) string { b, _ := json.Marshal(v); return string(b) }
