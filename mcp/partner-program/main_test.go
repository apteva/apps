package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const testProjectID = "test-project"

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	matches, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, path := range matches {
		mig, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := db.Exec(string(mig)); err != nil {
			t.Fatalf("apply migration %s: %v", path, err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPartnerProgramCoreFlow(t *testing.T) {
	db := newTestDB(t)

	partner, err := dbPartnerCreate(db, testProjectID, map[string]any{
		"name":         "Best Client Advocate",
		"email":        "advocate@example.com",
		"type":         "agency",
		"status":       "approved",
		"payout_email": "pay@example.com",
	})
	if err != nil {
		t.Fatalf("partner create: %v", err)
	}
	if partner.ID == 0 || partner.Type != "agency" || partner.Status != "approved" {
		t.Fatalf("unexpected partner: %+v", partner)
	}

	campaign, err := dbCampaignCreate(db, testProjectID, map[string]any{
		"name":                     "Q3 SaaS Referral",
		"slug":                     "q3-saas",
		"destination_url":          "https://client.example.com",
		"default_commission_type":  "percent",
		"default_commission_value": 25.0,
	})
	if err != nil {
		t.Fatalf("campaign create: %v", err)
	}

	link, err := dbReferralLinkCreate(db, testProjectID, map[string]any{
		"partner_id":  partner.ID,
		"campaign_id": campaign.ID,
		"code":        "ADVOCATE25",
	})
	if err != nil {
		t.Fatalf("link create: %v", err)
	}
	if link.Code != "advocate25" {
		t.Fatalf("code was not normalized: %+v", link)
	}

	referral, commission, err := dbReferralRecord(db, testProjectID, map[string]any{
		"code":              "advocate25",
		"status":            "converted",
		"customer_email":    "buyer@example.com",
		"amount_cents":      20000,
		"currency":          "usd",
		"external_order_id": "ord_123",
	})
	if err != nil {
		t.Fatalf("referral record: %v", err)
	}
	if referral.PartnerID != partner.ID || referral.CampaignID != campaign.ID {
		t.Fatalf("referral did not inherit partner/campaign from link: %+v", referral)
	}
	if commission == nil || commission.AmountCents != 5000 || commission.Currency != "USD" {
		t.Fatalf("unexpected commission: %+v", commission)
	}

	paid, err := dbCommissionUpdate(db, testProjectID, commission.ID, map[string]any{
		"status":       "paid",
		"payout_batch": "batch-2026-07",
	})
	if err != nil {
		t.Fatalf("commission update: %v", err)
	}
	if paid.Status != "paid" || paid.PaidAt == "" || paid.ApprovedAt == "" || paid.PayoutBatch != "batch-2026-07" {
		t.Fatalf("unexpected paid commission: %+v", paid)
	}

	stats, err := dbProgramStats(db, testProjectID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["partners"] != int64(1) || stats["conversions"] != int64(1) {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestPartnerValidation(t *testing.T) {
	db := newTestDB(t)
	_, err := dbPartnerCreate(db, testProjectID, map[string]any{"name": "Bad", "type": "influencer"})
	if err == nil || !strings.Contains(err.Error(), "type must be") {
		t.Fatalf("expected type validation error, got %v", err)
	}
}
