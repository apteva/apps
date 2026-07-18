package main

import (
	"database/sql"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestManifestParses(t *testing.T) {
	if (&App{}).Manifest().Name != "inventory" {
		t.Fatal("embedded manifest did not parse as inventory")
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "inventory" {
		t.Fatalf("external manifest name = %q, want inventory", m.Name)
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	runtimeTools := (&App{}).MCPTools()
	if len(runtimeTools) != len(declared) {
		t.Fatalf("manifest declares %d tools but runtime exposes %d", len(declared), len(runtimeTools))
	}
	for _, tool := range runtimeTools {
		if !declared[tool.Name] {
			t.Errorf("runtime tool %q is missing from the manifest", tool.Name)
		}
	}
}

func TestReservationCommitLifecycle(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"

	loc, err := createLocation(db, pid, map[string]any{"name": "Main Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := createItem(db, pid, map[string]any{"sku": "KIT-001", "name": "Starter Kit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adjustStock(db, pid, "receive", map[string]any{"item_id": item.ID, "location_id": loc.ID, "quantity_delta": 10.0, "reason": "initial"}); err != nil {
		t.Fatal(err)
	}
	resOut, err := reserveStock(db, pid, map[string]any{
		"item_id":        item.ID,
		"location_id":    loc.ID,
		"quantity":       3.0,
		"reference_app":  "commerce",
		"reference_type": "cart",
		"reference_id":   "cart-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := resOut["reservation"].(*Reservation)
	level, err := getOneLevel(db, pid, item.ID, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if level.OnHand != 10 || level.Reserved != 3 || level.Available != 7 {
		t.Fatalf("after reserve level = %+v, want on_hand=10 reserved=3 available=7", level)
	}
	if _, err := finishReservation(db, pid, map[string]any{"reservation_id": res.ID}, "committed"); err != nil {
		t.Fatal(err)
	}
	if _, err := finishReservation(db, pid, map[string]any{"reservation_id": res.ID}, "committed"); err != nil {
		t.Fatalf("repeat commit should be idempotent: %v", err)
	}
	level, err = getOneLevel(db, pid, item.ID, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if level.OnHand != 7 || level.Reserved != 0 || level.Available != 7 {
		t.Fatalf("after commit level = %+v, want on_hand=7 reserved=0 available=7", level)
	}
	movements, err := listMovements(db, pid, map[string]any{"item_id": item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(movements) != 3 {
		t.Fatalf("movement count = %d, want one receive/reserve/commit sequence", len(movements))
	}
}

func TestReleaseReservationRestoresAvailability(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	loc, _ := createLocation(db, pid, map[string]any{"name": "Main"})
	item, _ := createItem(db, pid, map[string]any{"sku": "TEE-BLK-M", "name": "Black Tee"})
	if _, err := adjustStock(db, pid, "receive", map[string]any{"item_id": item.ID, "location_id": loc.ID, "quantity_delta": 5.0}); err != nil {
		t.Fatal(err)
	}
	out, err := reserveStock(db, pid, map[string]any{"item_id": item.ID, "location_id": loc.ID, "quantity": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	res := out["reservation"].(*Reservation)
	if _, err := finishReservation(db, pid, map[string]any{"reservation_id": res.ID}, "released"); err != nil {
		t.Fatal(err)
	}
	level, err := getOneLevel(db, pid, item.ID, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if level.OnHand != 5 || level.Reserved != 0 || level.Available != 5 {
		t.Fatalf("after release level = %+v, want on_hand=5 reserved=0 available=5", level)
	}
}

func TestTransferRequiresAvailableStock(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	from, _ := createLocation(db, pid, map[string]any{"name": "Warehouse"})
	to, _ := createLocation(db, pid, map[string]any{"name": "Store"})
	item, _ := createItem(db, pid, map[string]any{"sku": "MUG-001", "name": "Mug"})
	if _, err := adjustStock(db, pid, "receive", map[string]any{"item_id": item.ID, "location_id": from.ID, "quantity_delta": 4.0}); err != nil {
		t.Fatal(err)
	}
	if _, err := transferStock(db, pid, map[string]any{"item_id": item.ID, "from_location_id": from.ID, "to_location_id": to.ID, "quantity": 1.5}); err != nil {
		t.Fatal(err)
	}
	fromLevel, _ := getOneLevel(db, pid, item.ID, from.ID)
	toLevel, _ := getOneLevel(db, pid, item.ID, to.ID)
	if fromLevel.OnHand != 2.5 || toLevel.OnHand != 1.5 {
		t.Fatalf("transfer levels: from=%+v to=%+v", fromLevel, toLevel)
	}
	if _, err := transferStock(db, pid, map[string]any{"item_id": item.ID, "from_location_id": from.ID, "to_location_id": to.ID, "quantity": 10.0}); err == nil {
		t.Fatal("expected insufficient stock error")
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
