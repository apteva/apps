package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Tier-1: pure-DB tests. Cross-app flows (cart_add_item, checkout_pay)
// need catalog + billing and a stub PlatformAPI — exercised by the
// real deployed apps, not here. The state-machine and schema
// guarantees ARE all testable in-process.

const testPID = "test-project"

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// FK enforcement is OFF by default in SQLite; the SDK turns it on
	// at runtime (app-sdk v0.19.0+). Mirror that in tests so cascade
	// deletes and FK refs behave the same way.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
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

// Helpers that mimic dbCartCreate's INSERT without the AppCtx
// (we don't have a SDK ctx in tier-1 tests). Lets us seed carts
// directly for state-machine tests.
func seedCart(t *testing.T, db *sql.DB, pid, token string, customerID int64, status string) int64 {
	t.Helper()
	var sessionTok any
	if token != "" {
		sessionTok = token
	}
	var custID any
	if customerID != 0 {
		custID = customerID
	}
	res, err := db.Exec(
		`INSERT INTO carts (project_id, session_token, customer_id, status, currency, metadata)
		 VALUES (?, ?, ?, ?, 'USD', '{}')`,
		pid, sessionTok, custID, status)
	if err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedItem(t *testing.T, db *sql.DB, cartID int64, priceID, productID int64, amount int64, qty float64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO cart_items (cart_id, price_id, product_id, description, unit_amount_cents, currency, quantity)
		 VALUES (?, ?, ?, 'Test', ?, 'USD', ?)`,
		cartID, priceID, productID, amount, qty); err != nil {
		t.Fatalf("seed item: %v", err)
	}
}

// ─── Cart schema invariants ─────────────────────────────────────────

func TestCart_UniqueOpenPerToken(t *testing.T) {
	db := newTestDB(t)
	seedCart(t, db, testPID, "tok-a", 0, "open")
	// Second open cart with same token must fail.
	_, err := db.Exec(
		`INSERT INTO carts (project_id, session_token, status, currency, metadata)
		 VALUES (?, 'tok-a', 'open', 'USD', '{}')`, testPID)
	if err == nil {
		t.Fatal("expected UNIQUE constraint to reject second open cart for same token")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("expected UNIQUE error, got: %v", err)
	}
}

func TestCart_TokenReuseAfterConversion(t *testing.T) {
	db := newTestDB(t)
	id := seedCart(t, db, testPID, "tok-b", 0, "open")
	// Convert the first cart.
	if _, err := db.Exec(`UPDATE carts SET status='converted' WHERE id = ?`, id); err != nil {
		t.Fatalf("convert: %v", err)
	}
	// Now a new open cart with the same token should be allowed (partial index).
	if _, err := db.Exec(
		`INSERT INTO carts (project_id, session_token, status, currency, metadata)
		 VALUES (?, 'tok-b', 'open', 'USD', '{}')`, testPID); err != nil {
		t.Errorf("expected token reuse after conversion, got: %v", err)
	}
}

func TestCart_DifferentProjectsCanShareToken(t *testing.T) {
	db := newTestDB(t)
	seedCart(t, db, "proj-1", "shared", 0, "open")
	if _, err := db.Exec(
		`INSERT INTO carts (project_id, session_token, status, currency, metadata)
		 VALUES ('proj-2', 'shared', 'open', 'USD', '{}')`); err != nil {
		t.Errorf("token isolation across projects broken: %v", err)
	}
}

func TestCartItems_UniquePerPrice(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-c", 0, "open")
	seedItem(t, db, cartID, 100, 10, 500, 1)
	// Second insert with same (cart_id, price_id) must conflict.
	_, err := db.Exec(
		`INSERT INTO cart_items (cart_id, price_id, product_id, description, unit_amount_cents, currency, quantity)
		 VALUES (?, 100, 10, 'x', 500, 'USD', 1)`, cartID)
	if err == nil {
		t.Fatal("expected UNIQUE constraint on (cart_id, price_id)")
	}
}

func TestCartItems_CascadeDeleteWithCart(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-d", 0, "open")
	seedItem(t, db, cartID, 100, 10, 500, 1)
	seedItem(t, db, cartID, 101, 10, 200, 1)
	if _, err := db.Exec(`DELETE FROM carts WHERE id = ?`, cartID); err != nil {
		t.Fatalf("delete cart: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cart_items WHERE cart_id = ?`, cartID).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Errorf("cart_items should cascade-delete, %d remain", n)
	}
}

// ─── State machine: cart mutations rejected when not open ──────────

func TestCartSetQuantity_RejectsNonOpenCart(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-e", 0, "checkout")
	seedItem(t, db, cartID, 100, 10, 500, 1)
	var itemID int64
	if err := db.QueryRow(`SELECT id FROM cart_items WHERE cart_id = ?`, cartID).Scan(&itemID); err != nil {
		t.Fatalf("get item id: %v", err)
	}
	_, err := dbCartSetQuantity(db, testPID, cartID, itemID, 2)
	if err == nil {
		t.Fatal("expected error when mutating a non-open cart")
	}
	if !strings.Contains(err.Error(), "checkout") {
		t.Errorf("error should mention the current status: %q", err.Error())
	}
}

func TestCartClear_RejectsNonOpenCart(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-f", 0, "converted")
	_, err := dbCartClear(db, testPID, cartID)
	if err == nil {
		t.Fatal("expected error when clearing a converted cart")
	}
}

// ─── Session state machine ─────────────────────────────────────────

func TestCheckoutStart_LocksCart(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-g", 0, "open")
	seedItem(t, db, cartID, 100, 10, 500, 2) // subtotal 1000
	// Recompute the cart totals so the seed reflects items.
	tx, _ := db.Begin()
	if err := recomputeCartTotalsTx(tx, cartID, "USD"); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	tx.Commit()

	// We can't call dbCheckoutStart directly without an AppCtx, so
	// inline the equivalent SQL the function would issue.
	res, err := db.Exec(
		`INSERT INTO checkout_sessions
		    (project_id, cart_id, provider, subtotal_cents, total_cents, currency,
		     status, created_at, updated_at)
		 VALUES (?, ?, 'manual', 1000, 1000, 'USD', 'started', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		testPID, cartID)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	sessionID, _ := res.LastInsertId()
	if _, err := db.Exec(`UPDATE carts SET status='checkout' WHERE id = ?`, cartID); err != nil {
		t.Fatalf("lock cart: %v", err)
	}

	// Verify mutations are now rejected.
	if _, err := dbCartClear(db, testPID, cartID); err == nil {
		t.Error("locked cart should reject clear")
	}

	// Cancel the session → cart should release back to open.
	if _, err := dbCheckoutCancel(db, testPID, sessionID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cart, _ := dbCartGetByID(db, testPID, cartID)
	if cart.Status != "open" {
		t.Errorf("cart should release to open after session cancel, got %q", cart.Status)
	}
}

func TestCheckoutUpdate_RejectsNonStartedSession(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-h", 0, "checkout")
	res, _ := db.Exec(
		`INSERT INTO checkout_sessions (project_id, cart_id, provider, currency, status)
		 VALUES (?, ?, 'manual', 'USD', 'paid')`, testPID, cartID)
	sessionID, _ := res.LastInsertId()
	_, err := dbCheckoutUpdate(db, testPID, sessionID, map[string]any{"email": "x@y.z"})
	if err == nil {
		t.Fatal("expected rejection of update on 'paid' session")
	}
}

func TestCheckoutUpdate_HappyPath(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-i", 0, "checkout")
	res, _ := db.Exec(
		`INSERT INTO checkout_sessions (project_id, cart_id, provider, currency, status)
		 VALUES (?, ?, 'manual', 'EUR', 'started')`, testPID, cartID)
	sessionID, _ := res.LastInsertId()
	patched, err := dbCheckoutUpdate(db, testPID, sessionID, map[string]any{
		"email":         "Alice@Example.COM ",
		"customer_name": "Alice",
		"shipping_address": map[string]any{
			"line1": "1 Main", "city": "Madrid", "country": "ES",
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// Email should be lowercased + trimmed.
	if patched.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", patched.Email)
	}
	if patched.CustomerName != "Alice" {
		t.Errorf("name not persisted: %q", patched.CustomerName)
	}
	if !strings.Contains(string(patched.ShippingAddress), "Madrid") {
		t.Errorf("shipping_address not persisted: %s", patched.ShippingAddress)
	}
}

func TestCheckoutCancel_RejectsTerminalStates(t *testing.T) {
	db := newTestDB(t)
	cartID := seedCart(t, db, testPID, "tok-j", 0, "converted")
	for _, status := range []string{"paid", "cancelled", "expired"} {
		t.Run(status, func(t *testing.T) {
			res, _ := db.Exec(
				`INSERT INTO checkout_sessions (project_id, cart_id, provider, currency, status)
				 VALUES (?, ?, 'manual', 'USD', ?)`, testPID, cartID, status)
			sessionID, _ := res.LastInsertId()
			if _, err := dbCheckoutCancel(db, testPID, sessionID); err == nil {
				t.Errorf("cancel should reject terminal status %q", status)
			}
		})
	}
}

// ─── List helpers ──────────────────────────────────────────────────

func TestCartsList_FiltersAndOrders(t *testing.T) {
	db := newTestDB(t)
	seedCart(t, db, testPID, "tok-1", 0, "open")
	seedCart(t, db, testPID, "tok-2", 0, "converted")
	seedCart(t, db, testPID, "tok-3", 0, "abandoned")
	all, _ := dbCartsList(db, testPID, cartFilters{})
	if len(all) != 3 {
		t.Errorf("expected 3 carts, got %d", len(all))
	}
	openOnly, _ := dbCartsList(db, testPID, cartFilters{status: "open"})
	if len(openOnly) != 1 {
		t.Errorf("status=open should return 1, got %d", len(openOnly))
	}
}

// ─── MCP registration ─────────────────────────────────────────────

func TestMCPTools_AllRegisteredHaveSchema(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	want := []string{
		"cart_get", "cart_add_item", "cart_set_quantity", "cart_clear",
		"checkout_start", "checkout_update", "checkout_pay",
		"checkout_get", "checkout_cancel",
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
	if m.Name != "checkout" {
		t.Errorf("manifest.Name=%q, want checkout", m.Name)
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

// Session-token generator: collision rate over a large batch should
// be effectively zero with 128 bits of entropy.
func TestNewSessionToken_NoCollisions(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		tok := newSessionToken()
		if seen[tok] {
			t.Errorf("collision in 1000 tokens: %s", tok)
		}
		seen[tok] = true
		if len(tok) < 16 {
			t.Errorf("token suspiciously short: %s", tok)
		}
	}
}
