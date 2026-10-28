package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func reviewH1Intent(token string) numberPurchaseIntent {
	return numberPurchaseIntent{
		Token: token, ProjectID: "project-a", Provider: "twilio",
		CarrierConnectionID: 9, Country: "EE", PhoneNumber: "+3726692354",
		NumberType: "local", MonthlyPrice: "1.00", InboundPrice: "0.0115",
		Currency: "USD", AddressRequirement: "none", ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

func TestReviewH1NumberPurchaseRejectionClassification(t *testing.T) {
	for status, want := range map[int]bool{400: true, 401: true, 403: true, 404: true, 422: true, 408: false, 409: false, 429: false, 500: false, 503: false} {
		err := fmt.Errorf("wrapped: %w", &carrierHTTPError{status: status, message: "x"})
		if got := numberPurchaseDefinitivelyRejected(err); got != want {
			t.Errorf("status %d: got %v want %v", status, got, want)
		}
	}
	if numberPurchaseDefinitivelyRejected(errors.New("connection reset")) {
		t.Fatal("transport error treated as definitive")
	}
}

func TestReviewH1NumberPurchaseBlockingIntents(t *testing.T) {
	db := testCallsDB(t).db
	old := reviewH1Intent("old")
	fresh := reviewH1Intent("fresh")
	for _, intent := range []numberPurchaseIntent{old, fresh} {
		if err := dbNumberPurchaseIntentInsert(db, intent); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() bool {
		ok, err := dbNumberPurchaseIntentClaim(db, "project-a", "fresh", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			_, _ = db.Exec(`UPDATE number_purchase_intents SET status='quoted' WHERE token='fresh'`)
		}
		return ok
	}
	// failed (definitive rejection) does not block.
	_ = dbNumberPurchaseIntentStatus(db, "old", "failed", nil, "rejected")
	if !claim() {
		t.Fatal("failed intent blocked the number")
	}
	// A recent unknown blocks; one past the window does not, and is abandoned.
	_ = dbNumberPurchaseIntentStatus(db, "old", "unknown", nil, "timeout")
	if claim() {
		t.Fatal("recent unknown intent did not block")
	}
	if abandoned, err := dbNumberPurchaseIntentAbandonStaleUnknown(db, "old"); err != nil || abandoned {
		t.Fatalf("recent unknown abandoned: %v %v", abandoned, err)
	}
	_, _ = db.Exec(`UPDATE number_purchase_intents SET updated_at=datetime('now','-25 hours') WHERE token='old'`)
	if !claim() {
		t.Fatal("stale unknown intent still blocks")
	}
	if abandoned, err := dbNumberPurchaseIntentAbandonStaleUnknown(db, "old"); err != nil || !abandoned {
		t.Fatalf("stale unknown not abandoned: %v %v", abandoned, err)
	}
	// succeeded blocks until retired as released.
	_ = dbNumberPurchaseIntentStatus(db, "old", "succeeded", nil, "")
	if claim() {
		t.Fatal("succeeded intent did not block")
	}
	if retired, err := dbNumberPurchaseIntentRetireSucceeded(db, &fresh); err != nil || !retired {
		t.Fatalf("retire: %v %v", retired, err)
	}
	if !claim() {
		t.Fatal("released intent still blocks")
	}
}
