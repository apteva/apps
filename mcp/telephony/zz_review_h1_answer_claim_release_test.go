package main

import "testing"

// Returning the call to 'pending' and reopening its claimed ring run must
// commit together. If reopening fails, the call stays 'answering' (retryable
// by the expiry/reset paths) instead of 'pending' with a run stuck in
// 'claimed' that never rings the remaining members.
func TestReviewH1AnswerClaimReleaseIsAtomic(t *testing.T) {
	_, db, plan := ringFixture(t, "priority")
	insertRingCall(t, db, plan, "atomic")
	if ok, err := db.claimPendingCall("atomic", 2, "p1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER zz_fail_reopen BEFORE UPDATE OF status ON call_ring_runs WHEN NEW.status='ringing' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.releaseAnswerClaim("atomic"); err == nil {
		t.Fatal("expected injected failure")
	}
	row, _ := db.findCall("atomic")
	var run string
	_ = db.db.QueryRow(`SELECT status FROM call_ring_runs WHERE call_id='atomic'`).Scan(&run)
	if row.Status != "answering" || run != "claimed" {
		t.Fatalf("partial release committed: call=%s run=%s", row.Status, run)
	}
	if _, err := db.db.Exec(`DROP TRIGGER zz_fail_reopen`); err != nil {
		t.Fatal(err)
	}
	if err := db.releaseAnswerClaim("atomic"); err != nil {
		t.Fatal(err)
	}
	row, _ = db.findCall("atomic")
	_ = db.db.QueryRow(`SELECT status FROM call_ring_runs WHERE call_id='atomic'`).Scan(&run)
	offers, _ := db.activeRingOffers("atomic", "p1")
	if row.Status != "pending" || run != "ringing" || ringOfferIDs(offers) != "d0" {
		t.Fatalf("release: call=%s run=%s offers=%v", row.Status, run, offers)
	}
}
