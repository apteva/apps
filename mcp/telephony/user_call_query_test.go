package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplicationUserCallCandidatesAgainstBusyProject(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	alice, boss := phoneTestIdentity("alice"), phoneTestIdentity("boss")

	// This mirrors a project with thousands of calls but very few belonging to
	// one adviser. More recent calls for another destination must not hide hers.
	phoneTestCall(t, app, "direct-old", "pending")
	owned := phoneTestCall(t, app, "owned-old", "completed")
	if _, err := app.db().db.Exec(`INSERT INTO telephony_call_owners(call_id,project_id,principal,destination_id) VALUES(?,?,?,?)`, owned.ID, owned.ProjectID, alice.key(), "sales"); err != nil {
		t.Fatal(err)
	}
	supervised := phoneTestCall(t, app, "supervised-old", "completed")
	outbound := phoneTestCall(t, app, "outbound-old", "completed")
	if _, err := app.db().db.Exec(`UPDATE calls SET direction='outbound',from_number='+13502231050' WHERE id=?`, outbound.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3641; i++ {
		row := phoneTestCall(t, app, fmt.Sprintf("hidden-%04d", i), "completed")
		if _, err := app.db().db.Exec(`UPDATE calls SET routing_destination_id='support',placed_at='2099-01-01T00:00:00Z' WHERE id=?`, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Create the live offer after populating history so the race-instrumented
	// test cannot consume its short offer lease while inserting fixture rows.
	offered := phoneTestRingOfferCall(t, app, "offered-old")
	if _, err := app.db().db.Exec(`UPDATE calls SET routing_destination_id='support' WHERE id=?`, offered.ID); err != nil {
		t.Fatal(err)
	}
	request := func(identity phoneIdentity) *http.Request {
		r := httptest.NewRequest("GET", "/calls", nil)
		principal, err := app.phonePrincipal("project-a", identity)
		if err != nil {
			t.Fatal(err)
		}
		return withPhonePrincipal(r, principal)
	}
	rows, err := app.recentPhoneCalls(request(alice), "project-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if len(rows) != 3 || !seen["direct-old"] || !seen["owned-old"] || !seen["offered-old"] {
		t.Fatalf("adviser selection: %v", seen)
	}
	rows, err = app.recentPhoneCalls(request(boss), "project-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen[supervised.ID] || !seen[outbound.ID] || seen["hidden-0000"] {
		t.Fatalf("supervisor selection: %v", seen)
	}
	// The exact-ID path must still enforce visibility after skipping the list.
	w := phoneTestRequest(app, &alice, "GET", "/calls?call_id=hidden-0000", nil)
	if w.Code != 404 {
		t.Fatalf("hidden detail: %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(app, &alice, "GET", "/calls?call_id=owned-old", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "owned-old") {
		t.Fatalf("owned detail: %d %s", w.Code, w.Body)
	}
}

func TestApplicationUserCallCandidateQueryUsesIndexes(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	for _, identity := range []phoneIdentity{phoneTestIdentity("alice"), phoneTestIdentity("boss")} {
		principal, err := app.phonePrincipal("project-a", identity)
		if err != nil {
			t.Fatal(err)
		}
		candidates, args := phoneCallCandidates(principal, "project-a")
		args = append(args, "project-a")
		rows, err := app.db().db.Query(`EXPLAIN QUERY PLAN SELECT id FROM calls WHERE id IN (`+candidates+`) AND project_id=? AND ingress_path<>'ring_group' ORDER BY placed_at DESC,id DESC LIMIT 200`, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(plan, "\n")
		if strings.Contains(joined, "SCAN calls") || !strings.Contains(joined, "telephony_call_owners_principal") || !strings.Contains(joined, "idx_call_offers_destination_active") || !strings.Contains(joined, "idx_calls_user_inbound_destination") {
			t.Fatalf("unindexed adviser path for %s:\n%s", identity.SubjectID, joined)
		}
		if principal.Supervisor && (!strings.Contains(joined, "idx_call_owners_destination") || !strings.Contains(joined, "idx_calls_user_outbound_number")) {
			t.Fatalf("unindexed supervisor path:\n%s", joined)
		}
	}
}
