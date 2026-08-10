package main

import (
	"database/sql"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestEmbeddedManifestTools(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "taxes" {
		t.Fatalf("manifest.Name=%q, want taxes", m.Name)
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
		if tool.Description == "" {
			t.Fatalf("tool %q missing description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("tool %q schema type=%v, want object", tool.Name, tool.InputSchema["type"])
		}
	}
	for name := range declared {
		if !implemented[name] {
			t.Fatalf("manifest declares %q but handler missing", name)
		}
	}
	if len(implemented) != 35 {
		t.Fatalf("implemented tools=%d, want 35", len(implemented))
	}
}

func TestExternalManifestParses(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "taxes" {
		t.Fatalf("external manifest name=%q", m.Name)
	}
}

func TestVATCalculationFormula(t *testing.T) {
	db := openTestDB(t)
	if err := seedTaxRules(db); err != nil {
		t.Fatal(err)
	}
	rule, err := findRule(db, "ES", "ES_SL", "vat", 2026)
	if err != nil {
		t.Fatal(err)
	}
	out := calculateOutputs("vat", rule, map[string]any{
		"revenue_cents":  int64(1000000),
		"expenses_cents": int64(200000),
	}, "2026-04-01", "2026-06-30")
	if out["estimated_payable_cents"].(int64) != 168000 {
		t.Fatalf("payable=%v, want 168000", out["estimated_payable_cents"])
	}
}

func TestInsertEstimatedObligation(t *testing.T) {
	db := openTestDB(t)
	profile := Profile{
		ProjectID: "p1",
		ID:        1,
		Name:      "Spanish SL",
		Country:   "ES",
		Structure: "ES_SL",
		Currency:  "EUR",
	}
	_, err := db.Exec(`INSERT INTO tax_profiles (id,project_id,name,country,structure,currency) VALUES (?,?,?,?,?,?)`,
		profile.ID, profile.ProjectID, profile.Name, profile.Country, profile.Structure, profile.Currency)
	if err != nil {
		t.Fatal(err)
	}
	obligation, err := insertObligation(db, profile, 0, 0, "vat", "Q2 IVA", 168000, "EUR", "2026-07-20", "Agencia Tributaria", "payable", "estimated", nil)
	if err != nil {
		t.Fatal(err)
	}
	if obligation.AmountCents != 168000 {
		t.Fatalf("amount=%d, want 168000", obligation.AmountCents)
	}
	got, err := getObligation(db, "p1", obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Authority != "Agencia Tributaria" {
		t.Fatalf("authority=%q", got.Authority)
	}
}

func TestSeedRulesIncludesSpainAndFrance(t *testing.T) {
	db := openTestDB(t)
	if err := seedTaxRules(db); err != nil {
		t.Fatal(err)
	}
	rules, err := listRules(db, map[string]any{
		"year": 2026,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rules {
		seen[r.Country+"/"+r.Structure+"/"+r.TaxType] = true
	}
	for _, want := range []string{"ES/ES_AUTONOMO/social_contributions", "ES/ES_SL/corporate_tax", "FR/FR_SAS/vat"} {
		if !seen[want] {
			t.Fatalf("missing rule %s", want)
		}
	}
}

func TestInferPeriodsForSpanishAutonomo(t *testing.T) {
	db := openTestDB(t)
	profile := Profile{
		ID:            1,
		ProjectID:     "p1",
		Name:          "Autonomo",
		Country:       "ES",
		Structure:     "ES_AUTONOMO",
		VATRegistered: true,
		FilingCadence: "quarterly",
		Currency:      "EUR",
	}
	if _, err := db.Exec(`INSERT INTO tax_profiles (id,project_id,name,country,structure,filing_cadence,currency) VALUES (?,?,?,?,?,?,?)`,
		profile.ID, profile.ProjectID, profile.Name, profile.Country, profile.Structure, profile.FilingCadence, profile.Currency); err != nil {
		t.Fatal(err)
	}
	periods, err := generatePeriodsForProfile(db, profile, 2026)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, period := range periods {
		counts[period["tax_type"].(string)]++
	}
	if counts["vat"] != 4 {
		t.Fatalf("vat periods=%d, want 4", counts["vat"])
	}
	if counts["income_tax"] != 4 {
		t.Fatalf("income_tax periods=%d, want 4", counts["income_tax"])
	}
	if counts["social_contributions"] != 12 {
		t.Fatalf("social periods=%d, want 12", counts["social_contributions"])
	}
}

func TestSocialContributionEstimate(t *testing.T) {
	db := openTestDB(t)
	if err := seedTaxRules(db); err != nil {
		t.Fatal(err)
	}
	rule, err := findRule(db, "ES", "ES_AUTONOMO", "social_contributions", 2026)
	if err != nil {
		t.Fatal(err)
	}
	out := calculateOutputs("social_contributions", rule, map[string]any{
		"social_contribution_cents": int64(35000),
		"months":                    int64(3),
	}, "2026-01-01", "2026-03-31")
	if out["estimated_payable_cents"].(int64) != 105000 {
		t.Fatalf("payable=%v, want 105000", out["estimated_payable_cents"])
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, migration := range []string{"migrations/001_init.sql", "migrations/002_integrity.sql"} {
		raw, err := os.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
