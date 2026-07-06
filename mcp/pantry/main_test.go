package main

import (
	"database/sql"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestManifestParses(t *testing.T) {
	if (&App{}).Manifest().Name != "pantry" {
		t.Fatal("embedded manifest did not parse as pantry")
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "pantry" {
		t.Fatalf("external manifest name = %q, want pantry", m.Name)
	}
}

func TestUseStockConsumesEarliestExpiryFirst(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"

	first, err := addStock(db, pid, map[string]any{
		"name":       "Milk",
		"quantity":   1.0,
		"location":   "Fridge",
		"expires_at": "2026-07-10",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := addStock(db, pid, map[string]any{
		"name":       "Milk",
		"quantity":   2.0,
		"location":   "Fridge",
		"expires_at": "2026-07-20",
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := useStock(db, pid, map[string]any{"name": "Milk", "quantity": 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lots) != 2 {
		t.Fatalf("used %d lots, want 2", len(res.Lots))
	}
	if res.Lots[0].ID != first.ID || res.Lots[0].Quantity != 1 {
		t.Fatalf("first consumed lot = %#v, want lot %d qty 1", res.Lots[0], first.ID)
	}
	if res.Lots[1].ID != second.ID || res.Lots[1].Quantity != 0.5 {
		t.Fatalf("second consumed lot = %#v, want lot %d qty .5", res.Lots[1], second.ID)
	}

	lots, err := listLots(db, pid, map[string]any{"q": "Milk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lots) != 1 {
		t.Fatalf("remaining lots = %d, want 1", len(lots))
	}
	if lots[0].ID != second.ID || lots[0].Quantity != 1.5 {
		t.Fatalf("remaining lot = %#v, want lot %d qty 1.5", lots[0], second.ID)
	}
}

func TestLowStockShoppingListUsesTargetQuantity(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	item, err := createItem(db, pid, map[string]any{
		"name":            "Eggs",
		"default_unit":    "each",
		"min_quantity":    6.0,
		"target_quantity": 12.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addStock(db, pid, map[string]any{"item_id": item.ID, "quantity": 4.0, "location": "Fridge"}); err != nil {
		t.Fatal(err)
	}
	lines, err := lowStockItems(db, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("shopping lines = %d, want 1", len(lines))
	}
	if lines[0].BuyQuantity != 8 {
		t.Fatalf("buy quantity = %.2f, want 8", lines[0].BuyQuantity)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	return db
}
