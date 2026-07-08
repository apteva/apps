package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestManifestParse(t *testing.T) {
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if embedded.Name != fromFile.Name || embedded.Version != fromFile.Version {
		t.Fatalf("embedded manifest %s@%s != file %s@%s", embedded.Name, embedded.Version, fromFile.Name, fromFile.Version)
	}
	if embedded.Name != "commerce" {
		t.Fatalf("manifest name=%q, want commerce", embedded.Name)
	}
	if len(fromFile.Provides.MCPTools) == 0 {
		t.Fatal("commerce manifest should expose MCP tools")
	}
}

func TestStoreListingCartAndSaleFlow(t *testing.T) {
	db := openCommerceTestDB(t)
	pid := "proj-test"

	store, err := dbStoreCreate(db, pid, map[string]any{
		"slug":             "main",
		"name":             "Main Store",
		"default_currency": "eur",
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if store.DefaultCurrency != "EUR" {
		t.Fatalf("default currency=%q, want EUR", store.DefaultCurrency)
	}

	listing, err := dbListingCreate(db, pid, map[string]any{
		"store_id":           store.ID,
		"title":              "Apteva Hoodie",
		"description_html":   "<p>Heavyweight</p>",
		"catalog_product_id": int64(101),
		"status":             "active",
	})
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	variant, err := dbVariantCreate(db, pid, map[string]any{
		"listing_id":       listing.ID,
		"sku":              "HD-001",
		"title":            "Black / M",
		"catalog_price_id": int64(201),
		"price_cents":      int64(4900),
		"currency":         "eur",
	})
	if err != nil {
		t.Fatalf("create variant: %v", err)
	}

	cart, err := dbCartCreate(db, pid, map[string]any{
		"store_id":      store.ID,
		"session_token": "sess_123",
		"currency":      store.DefaultCurrency,
	})
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if err := dbCartAddItem(db, pid, cart.ID, variant, 2); err != nil {
		t.Fatalf("add cart item: %v", err)
	}
	cart, err = dbCartGet(db, pid, cart.ID, true)
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	if got := cart.TotalCents; got != 9800 {
		t.Fatalf("cart total=%d, want 9800", got)
	}
	if len(cart.Items) != 1 || cart.Items[0].TitleSnapshot != "Apteva Hoodie - Black / M" {
		t.Fatalf("unexpected cart item snapshot: %#v", cart.Items)
	}

	checkout, err := dbCheckoutCreate(db, pid, cart, 301, []int64{401, 402})
	if err != nil {
		t.Fatalf("create checkout: %v", err)
	}
	if err := dbCheckoutPatch(db, pid, checkout.ID, map[string]any{"email": "buyer@example.com", "name": "Buyer"}); err != nil {
		t.Fatalf("patch checkout: %v", err)
	}
	checkout, err = dbCheckoutGet(db, pid, checkout.ID)
	if err != nil {
		t.Fatalf("get checkout: %v", err)
	}
	if checkout.CustomerEmail != "buyer@example.com" || len(checkout.ReservationIDs) != 2 {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
	if err := dbCheckoutInvoice(db, pid, checkout.ID, 501, "INV-501"); err != nil {
		t.Fatalf("set invoice: %v", err)
	}
	checkout, _ = dbCheckoutGet(db, pid, checkout.ID)
	sale, err := dbSaleCreateFromCheckout(db, pid, checkout)
	if err != nil {
		t.Fatalf("create sale: %v", err)
	}
	if sale.TotalCents != 9800 || sale.InvoiceNumber != "INV-501" {
		t.Fatalf("unexpected sale: %#v", sale)
	}
}

func openCommerceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("exec migration %s: %v\n%s", path, err, strings.TrimSpace(string(body)))
		}
	}
	return db
}
