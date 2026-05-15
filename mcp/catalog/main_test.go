package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Tier-1 tests: pure CRUD + invariants via direct DB calls. No HTTP,
// no SDK runtime. Mirrors billing's tier-1 test approach.

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	mig, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

const testPID = "test-project"

// ─── Products ───────────────────────────────────────────────────────

func TestProductCreate_RequiresNameAndValidType(t *testing.T) {
	db := newTestDB(t)
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"no name", map[string]any{"type": "recurring"}, "name required"},
		{"empty name", map[string]any{"name": "  ", "type": "recurring"}, "name required"},
		{"invalid type", map[string]any{"name": "X", "type": "weird"}, "type must be"},
		{"no type", map[string]any{"name": "X"}, "type must be"},
		{"bad tax", map[string]any{"name": "X", "type": "service", "tax_category": "lol"}, "tax_category must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := dbProductCreate(db, testPID, c.args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestProductCreate_HappyPath(t *testing.T) {
	db := newTestDB(t)
	p, err := dbProductCreate(db, testPID, map[string]any{
		"name":         "Apteva SaaS",
		"slug":         "apteva-saas",
		"type":         "recurring",
		"category":     "subscription",
		"tax_category": "standard",
		"description":  "Continuous thinking platform",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 || p.Name != "Apteva SaaS" || p.Slug != "apteva-saas" ||
		p.Type != "recurring" || p.TaxCategory != "standard" {
		t.Errorf("unexpected product: %+v", p)
	}
}

func TestProductSlug_PerProjectUniqueOnNonArchived(t *testing.T) {
	db := newTestDB(t)
	create := func(slug string) error {
		_, err := dbProductCreate(db, testPID, map[string]any{
			"name": "X-" + slug, "type": "one_time", "slug": slug,
		})
		return err
	}
	if err := create("pro"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := create("pro"); err == nil {
		t.Fatal("expected unique-violation on duplicate slug")
	}
	// Different project ≠ collision (per-project uniqueness)
	if _, err := dbProductCreate(db, "other-project", map[string]any{
		"name": "Pro", "type": "one_time", "slug": "pro",
	}); err != nil {
		t.Errorf("same slug in different project should be allowed: %v", err)
	}
	// Archive frees the slug
	got, _ := dbProductGetBySlug(db, testPID, "pro")
	if _, err := dbProductArchive(db, testPID, got.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := create("pro"); err != nil {
		t.Errorf("slug should be reusable after archive: %v", err)
	}
}

// ─── Prices ─────────────────────────────────────────────────────────

func mustProduct(t *testing.T, db *sql.DB, name, typ string) *Product {
	t.Helper()
	p, err := dbProductCreate(db, testPID, map[string]any{"name": name, "type": typ})
	if err != nil {
		t.Fatalf("product: %v", err)
	}
	return p
}

func TestPriceCreate_RequiresProductAmountCurrency(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"no amount", map[string]any{"currency": "EUR"}, "unit_amount_cents required"},
		{"bad currency", map[string]any{"unit_amount_cents": 1000, "currency": "eu"}, "ISO 4217"},
		{"bad interval", map[string]any{"unit_amount_cents": 1000, "currency": "EUR", "interval": "fortnight"}, "interval must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := dbPriceCreate(db, testPID, prod.ID, c.args)
			if err == nil {
				t.Fatalf("expected error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestPriceCreate_RejectsMissingProduct(t *testing.T) {
	db := newTestDB(t)
	_, err := dbPriceCreate(db, testPID, 99999, map[string]any{
		"unit_amount_cents": 1000, "currency": "EUR",
	})
	if err == nil {
		t.Fatal("expected error for missing product")
	}
}

func TestPriceCreate_RejectsArchivedProduct(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	if _, err := dbProductArchive(db, testPID, prod.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	_, err := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"unit_amount_cents": 1000, "currency": "EUR",
	})
	if err == nil {
		t.Fatal("expected error for archived product")
	}
}

func TestPriceCreate_HappyPathBothTypes(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	// One-time price
	oneOff, err := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"nickname":          "Setup",
		"unit_amount_cents": 5000,
		"currency":          "eur", // normalised to upper
	})
	if err != nil {
		t.Fatalf("one-time create: %v", err)
	}
	if oneOff.Currency != "EUR" || oneOff.Interval != "" || !oneOff.Active {
		t.Errorf("unexpected one-off: %+v", oneOff)
	}
	// Recurring price
	rec, err := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"nickname":          "Pro monthly",
		"unit_amount_cents": 2900,
		"currency":          "EUR",
		"interval":          "month",
		"trial_days":        14,
	})
	if err != nil {
		t.Fatalf("recurring create: %v", err)
	}
	if rec.Interval != "month" || rec.TrialDays != 14 || rec.IntervalCount != 1 {
		t.Errorf("unexpected recurring: %+v", rec)
	}
}

// Critical invariant: prices are immutable for financial fields.
// To change pricing, you create a new Price and archive the old one.
func TestPriceUpdate_RejectsFinancialFieldChanges(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	p, _ := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"unit_amount_cents": 2900, "currency": "EUR", "interval": "month",
	})
	locked := []string{"unit_amount_cents", "currency", "interval", "interval_count", "trial_days", "product_id"}
	for _, field := range locked {
		t.Run(field, func(t *testing.T) {
			_, err := dbPriceUpdate(db, testPID, p.ID, map[string]any{field: "x"})
			if err == nil {
				t.Errorf("expected rejection of %s change", field)
			} else if !strings.Contains(err.Error(), "cannot be changed") {
				t.Errorf("error %q should mention immutability", err.Error())
			}
		})
	}
}

func TestPriceUpdate_AllowedFields(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	p, _ := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"unit_amount_cents": 2900, "currency": "EUR",
	})
	updated, err := dbPriceUpdate(db, testPID, p.ID, map[string]any{
		"nickname":      "Pro",
		"active":        false,
		"tax_inclusive": true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Nickname != "Pro" || updated.Active || !updated.TaxInclusive {
		t.Errorf("update didn't apply: %+v", updated)
	}
}

func TestPriceArchive_SetsActiveFalseToo(t *testing.T) {
	db := newTestDB(t)
	prod := mustProduct(t, db, "P", "recurring")
	p, _ := dbPriceCreate(db, testPID, prod.ID, map[string]any{
		"unit_amount_cents": 2900, "currency": "EUR",
	})
	archived, err := dbPriceArchive(db, testPID, p.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Active {
		t.Error("archiving should also set active=false")
	}
	if archived.ArchivedAt == "" {
		t.Error("archived_at should be populated")
	}
}

func TestProductsList_FiltersAndArchive(t *testing.T) {
	db := newTestDB(t)
	a := mustProduct(t, db, "A SaaS", "recurring")
	mustProduct(t, db, "B Service", "service")
	mustProduct(t, db, "C One", "one_time")
	if _, err := dbProductArchive(db, testPID, a.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	all, _ := dbProductsList(db, testPID, productFilters{})
	if len(all) != 2 {
		t.Errorf("default list should exclude archived, got %d (want 2)", len(all))
	}
	withArchived, _ := dbProductsList(db, testPID, productFilters{includeArchived: true})
	if len(withArchived) != 3 {
		t.Errorf("with archived = %d (want 3)", len(withArchived))
	}
	services, _ := dbProductsList(db, testPID, productFilters{typeFilter: "service"})
	if len(services) != 1 || services[0].Type != "service" {
		t.Errorf("service-only filter wrong: %d items", len(services))
	}
	search, _ := dbProductsList(db, testPID, productFilters{query: "SaaS", includeArchived: true})
	if len(search) != 1 {
		t.Errorf("search for SaaS should hit 1 (incl archived), got %d", len(search))
	}
}

// ─── MCP tool registration sanity ──────────────────────────────────

func TestMCPTools_AllRegisteredHaveSchema(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	want := []string{
		"catalog_products_list", "catalog_products_create", "catalog_products_get",
		"catalog_products_update", "catalog_products_archive", "catalog_products_search",
		"catalog_prices_list", "catalog_prices_create", "catalog_prices_get",
		"catalog_prices_update", "catalog_prices_archive",
	}
	if len(tools) != len(want) {
		t.Errorf("MCPTools count = %d, want %d", len(tools), len(want))
	}
	implemented := map[string]bool{}
	for _, tool := range tools {
		implemented[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q missing description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q schema not an object", tool.Name)
		}
	}
	for _, name := range want {
		if !implemented[name] {
			t.Errorf("expected tool %q not registered", name)
		}
	}
}

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "catalog" {
		t.Errorf("manifest.Name=%q, want catalog", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version empty")
	}
	scopes := map[string]bool{}
	for _, s := range m.Scopes {
		scopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !scopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}
