package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustCatalogPrice(t *testing.T, db *sql.DB, pid string, amount int64, currency, interval string) (*Product, *Price) {
	t.Helper()
	product, err := dbProductCreate(db, pid, map[string]any{
		"name": "Discount test product",
		"type": map[bool]string{true: "recurring", false: "one_time"}[interval != ""],
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	price, err := dbPriceCreate(db, pid, product.ID, map[string]any{
		"unit_amount_cents": amount,
		"currency":          currency,
		"interval":          interval,
	})
	if err != nil {
		t.Fatalf("create price: %v", err)
	}
	return product, price
}

func TestDiscountAmountLimitedEcommerceLifecycle(t *testing.T) {
	db := newTestDB(t)
	product, price := mustCatalogPrice(t, db, testPID, 10000, "USD", "")
	discount, err := dbDiscountCreate(db, testPID, map[string]any{
		"name":                         "Launch sale",
		"discount_type":                "amount",
		"value_cents":                  2000,
		"currency":                     "USD",
		"duration":                     "once",
		"max_redemptions":              2,
		"max_redemptions_per_customer": 1,
		"product_ids":                  []any{product.ID},
	})
	if err != nil {
		t.Fatalf("create discount: %v", err)
	}
	code, err := dbDiscountCodeCreate(db, testPID, map[string]any{
		"discount_id":     discount.ID,
		"code":            "Launch-20",
		"max_redemptions": 2,
	})
	if err != nil {
		t.Fatalf("create code: %v", err)
	}
	if code.Code != "Launch-20" {
		t.Fatalf("code = %q", code.Code)
	}

	now := time.Now().UTC().Truncate(time.Second)
	base := map[string]any{
		"code":         "launch-20",
		"customer_ref": "crm:customer-1",
		"context_ref":  "checkout:1",
		"price_id":     price.ID,
		"quantity":     2,
		"currency":     "usd",
	}
	quote, _, _, err := dbDiscountQuote(db, testPID, base, now)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if !quote.Eligible || quote.SubtotalCents != 20000 || quote.DiscountCents != 2000 || quote.TotalCents != 18000 {
		t.Fatalf("unexpected quote: %+v", quote)
	}
	if quote.Quantity != 2 {
		t.Fatalf("quote quantity = %d, want 2", quote.Quantity)
	}

	reserveArgs := cloneArgs(base)
	reserveArgs["idempotency_key"] = "checkout:1:discount"
	first, err := dbDiscountReserve(db, testPID, reserveArgs, now)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	again, err := dbDiscountReserve(db, testPID, reserveArgs, now)
	if err != nil || again.PublicID != first.PublicID {
		t.Fatalf("idempotent reserve = %+v, %v; want %s", again, err, first.PublicID)
	}

	different := cloneArgs(reserveArgs)
	different["customer_ref"] = "crm:customer-other"
	if _, err := dbDiscountReserve(db, testPID, different, now); err == nil || !strings.Contains(err.Error(), "different discount parameters") {
		t.Fatalf("idempotency conflict error = %v", err)
	}

	repeatCustomer := cloneArgs(base)
	repeatCustomer["context_ref"] = "checkout:2"
	repeatCustomer["idempotency_key"] = "checkout:2:discount"
	if _, err := dbDiscountReserve(db, testPID, repeatCustomer, now); err == nil || !strings.Contains(err.Error(), "customer_limit_reached") {
		t.Fatalf("customer limit error = %v", err)
	}

	secondArgs := cloneArgs(base)
	secondArgs["customer_ref"] = "crm:customer-2"
	secondArgs["context_ref"] = "checkout:3"
	secondArgs["idempotency_key"] = "checkout:3:discount"
	second, err := dbDiscountReserve(db, testPID, secondArgs, now)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}

	thirdArgs := cloneArgs(base)
	thirdArgs["customer_ref"] = "crm:customer-3"
	thirdArgs["context_ref"] = "checkout:4"
	thirdArgs["idempotency_key"] = "checkout:4:discount"
	if _, err := dbDiscountReserve(db, testPID, thirdArgs, now); err == nil || !strings.Contains(err.Error(), "limit_reached") {
		t.Fatalf("global/code limit error = %v", err)
	}

	if _, err := dbDiscountRelease(db, testPID, second.PublicID, now); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, err := dbDiscountReserve(db, testPID, thirdArgs, now)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	if third.Status != "reserved" {
		t.Fatalf("third status = %q", third.Status)
	}

	redeemed, err := dbDiscountRedeem(db, testPID, first.PublicID, now)
	if err != nil || redeemed.Status != "redeemed" {
		t.Fatalf("redeem = %+v, %v", redeemed, err)
	}
	redeemedAgain, err := dbDiscountRedeem(db, testPID, first.PublicID, now)
	if err != nil || redeemedAgain.PublicID != first.PublicID {
		t.Fatalf("idempotent redeem = %+v, %v", redeemedAgain, err)
	}
	if _, err := dbDiscountRelease(db, testPID, first.PublicID, now); err == nil {
		t.Fatal("redeemed reservation should not be releasable")
	}
}

func TestDiscountRepeatingSaaSSnapshot(t *testing.T) {
	db := newTestDB(t)
	_, price := mustCatalogPrice(t, db, testPID, 2900, "EUR", "month")
	discount, err := dbDiscountCreate(db, testPID, map[string]any{
		"name":            "First three months",
		"discount_type":   "percentage",
		"percentage_bps":  5000,
		"duration":        "repeating",
		"duration_cycles": 3,
		"price_ids":       []any{price.ID},
		"metadata":        map[string]any{"campaign": "saas-launch"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	quote, _, _, err := dbDiscountQuote(db, testPID, map[string]any{
		"discount_id": discount.ID,
		"price_id":    price.ID,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if !quote.Eligible || quote.DiscountCents != 1450 || quote.TotalCents != 1450 {
		t.Fatalf("unexpected quote: %+v", quote)
	}
	if quote.Application.Duration != "repeating" || quote.Application.DurationCycles != 3 || quote.Application.PercentageBPS != 5000 {
		t.Fatalf("application snapshot: %+v", quote.Application)
	}

	if _, err := dbDiscountUpdate(db, testPID, discount.ID, map[string]any{"percentage_bps": 2500}); err != nil {
		t.Fatalf("update discount: %v", err)
	}
	// The caller would persist the first quote. It must remain unchanged when
	// the campaign definition changes later.
	if quote.Application.PercentageBPS != 5000 {
		t.Fatalf("existing snapshot changed: %+v", quote.Application)
	}
	newQuote, _, _, err := dbDiscountQuote(db, testPID, map[string]any{"discount_id": discount.ID, "price_id": price.ID}, time.Now().UTC())
	if err != nil || newQuote.Application.PercentageBPS != 2500 {
		t.Fatalf("new quote = %+v, %v", newQuote, err)
	}
}

func TestDiscountEligibilityBoundariesAndTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	product, price := mustCatalogPrice(t, db, testPID, 5000, "USD", "")
	otherProduct, otherPrice := mustCatalogPrice(t, db, testPID, 5000, "USD", "")
	_ = product
	now := time.Now().UTC().Truncate(time.Second)
	discount, err := dbDiscountCreate(db, testPID, map[string]any{
		"name":                   "Future override",
		"discount_type":          "price_override",
		"value_cents":            3000,
		"currency":               "USD",
		"starts_at":              now.Add(time.Hour).Format(time.RFC3339),
		"ends_at":                now.Add(2 * time.Hour).Format(time.RFC3339),
		"minimum_subtotal_cents": 4000,
		"price_ids":              []any{price.ID},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	assertReason := func(name string, args map[string]any, at time.Time, want string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			quote, _, _, err := dbDiscountQuote(db, testPID, args, at)
			if err != nil {
				t.Fatalf("quote: %v", err)
			}
			if quote.Eligible || quote.Reason != want {
				t.Fatalf("quote = %+v, want reason %q", quote, want)
			}
		})
	}
	assertReason("not started", map[string]any{"discount_id": discount.ID, "price_id": price.ID}, now, "not_started")
	assertReason("wrong scope", map[string]any{"discount_id": discount.ID, "price_id": otherPrice.ID}, now.Add(90*time.Minute), "scope_mismatch")
	assertReason("wrong currency", map[string]any{"discount_id": discount.ID, "price_id": price.ID, "currency": "EUR"}, now.Add(90*time.Minute), "currency_mismatch")
	assertReason("expired", map[string]any{"discount_id": discount.ID, "price_id": price.ID}, now.Add(3*time.Hour), "expired")

	quote, _, _, err := dbDiscountQuote(db, testPID, map[string]any{"discount_id": discount.ID, "price_id": price.ID}, now.Add(90*time.Minute))
	if err != nil || !quote.Eligible || quote.TotalCents != 3000 || quote.DiscountCents != 2000 {
		t.Fatalf("eligible override = %+v, %v", quote, err)
	}
	twoUnits, _, _, err := dbDiscountQuote(db, testPID, map[string]any{"discount_id": discount.ID, "price_id": price.ID, "quantity": 2}, now.Add(90*time.Minute))
	if err != nil || !twoUnits.Eligible || twoUnits.SubtotalCents != 10000 || twoUnits.TotalCents != 6000 {
		t.Fatalf("two-unit override = %+v, %v", twoUnits, err)
	}

	if found, err := dbDiscountGet(db, "other-project", discount.ID, true); err != nil || found != nil {
		t.Fatalf("cross-project get = %+v, %v", found, err)
	}
	if _, err := dbDiscountCreate(db, "other-project", map[string]any{
		"name": "Bad cross-project scope", "discount_type": "percentage", "percentage_bps": 1000,
		"product_ids": []any{otherProduct.ID},
	}); err == nil {
		t.Fatal("cross-project product scope should be rejected")
	}
}

func TestDiscountValidationAndReservationExpiry(t *testing.T) {
	db := newTestDB(t)
	_, price := mustCatalogPrice(t, db, testPID, 1000, "USD", "")
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing scope", map[string]any{"name": "X", "discount_type": "percentage", "percentage_bps": 1000}, "scope required"},
		{"bad percent", map[string]any{"name": "X", "discount_type": "percentage", "percentage_bps": 10001, "all_products": true}, "between 1 and 10000"},
		{"amount currency", map[string]any{"name": "X", "discount_type": "amount", "value_cents": 100, "all_products": true}, "currency"},
		{"repeating cycles", map[string]any{"name": "X", "discount_type": "percentage", "percentage_bps": 1000, "duration": "repeating", "all_products": true}, "duration_cycles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dbDiscountCreate(db, testPID, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	discount, err := dbDiscountCreate(db, testPID, map[string]any{
		"name": "Short reservation", "discount_type": "percentage", "percentage_bps": 1000,
		"max_redemptions": 1, "all_products": true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	args := map[string]any{"discount_id": discount.ID, "price_id": price.ID, "idempotency_key": "short", "expires_in_seconds": 60}
	r, err := dbDiscountReserve(db, testPID, args, now)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := dbDiscountRedeem(db, testPID, r.PublicID, now.Add(61*time.Second)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry error = %v", err)
	}
	expiredRetry, err := dbDiscountReserve(db, testPID, args, now.Add(61*time.Second))
	if err != nil || expiredRetry.Status != "expired" || expiredRetry.PublicID != r.PublicID {
		t.Fatalf("expired idempotent retry = %+v, %v", expiredRetry, err)
	}
	secondArgs := cloneArgs(args)
	secondArgs["idempotency_key"] = "after-expiry"
	if _, err := dbDiscountReserve(db, testPID, secondArgs, now.Add(61*time.Second)); err != nil {
		t.Fatalf("expired reservation should release capacity: %v", err)
	}
}

func TestDiscountReservationCapacityIsAtomic(t *testing.T) {
	db := newConcurrentDiscountTestDB(t)
	_, price := mustCatalogPrice(t, db, testPID, 1000, "USD", "")
	discount, err := dbDiscountCreate(db, testPID, map[string]any{
		"name": "Only one", "discount_type": "percentage", "percentage_bps": 1000,
		"max_redemptions": 1, "all_products": true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	const attempts = 8
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := dbDiscountReserve(db, testPID, map[string]any{
				"discount_id":     discount.ID,
				"price_id":        price.ID,
				"customer_ref":    fmt.Sprintf("customer:%d", i),
				"context_ref":     fmt.Sprintf("checkout:%d", i),
				"idempotency_key": fmt.Sprintf("atomic:%d", i),
			}, now)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful reservations = %d, want exactly 1", successes)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_discount_reservations WHERE project_id=? AND discount_id=? AND status='reserved'`, testPID, discount.ID).Scan(&stored); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored reservations = %d, want 1", stored)
	}
}

func newConcurrentDiscountTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	matches, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, migrationPath := range matches {
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("read migration %s: %v", migrationPath, err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", migrationPath, err)
		}
	}
	db.SetMaxOpenConns(attemptsForAtomicTest)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

const attemptsForAtomicTest = 8

func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
