package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type CheckoutDiscount struct {
	ProjectID         string          `json:"project_id"`
	CheckoutID        string          `json:"checkout_id"`
	ReservationID     string          `json:"reservation_id"`
	ReservationKey    string          `json:"-"`
	CatalogDiscountID int64           `json:"catalog_discount_id"`
	DiscountCode      string          `json:"discount_code,omitempty"`
	Status            string          `json:"status"`
	Application       json.RawMessage `json:"application"`
	Currency          string          `json:"currency"`
	SubtotalCents     int64           `json:"subtotal_cents"`
	DiscountCents     int64           `json:"discount_cents"`
	TotalCents        int64           `json:"total_cents"`
	ExpiresAt         string          `json:"expires_at,omitempty"`
	AttemptCount      int64           `json:"attempt_count"`
	LastError         string          `json:"last_error,omitempty"`
	RedeemedAt        string          `json:"redeemed_at,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

func normalizeDiscountCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func checkoutDiscountIdentity(plan *Plan, code string) (int64, string, error) {
	discountID := int64PtrValue(plan.CatalogDiscountID)
	code = normalizeDiscountCode(code)
	if code != "" {
		if len(code) < 3 || len(code) > 64 {
			return 0, "", errors.New("discount_code must be between 3 and 64 characters")
		}
		for _, r := range code {
			if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
				return 0, "", errors.New("discount_code may contain only letters, numbers, hyphens, and underscores")
			}
		}
	}
	if discountID != 0 && code != "" {
		return 0, "", errors.New("discount_code cannot be combined with the plan's automatic discount")
	}
	return discountID, code, nil
}

func (a *App) ensureCheckoutDiscountReservation(ctx *sdk.AppCtx, checkout *Checkout, customer *Customer, price checkoutPrice, discountID int64, code string) (*CheckoutDiscount, error) {
	if discountID == 0 && code == "" {
		return nil, nil
	}
	return a.ensureCheckoutDiscountReservationAttempt(ctx, checkout, customer, price, discountID, code, false)
}

func (a *App) ensureCheckoutDiscountReservationAttempt(ctx *sdk.AppCtx, checkout *Checkout, customer *Customer, price checkoutPrice, discountID int64, code string, renewed bool) (*CheckoutDiscount, error) {
	existing, err := dbCheckoutDiscountGet(ctx.AppDB(), checkout.ProjectID, checkout.ID)
	if err != nil {
		return nil, err
	}
	attempt := int64(1)
	reservationKey := fmt.Sprintf("saas:%s:discount:%d", checkout.ID, attempt)
	if existing != nil {
		if existing.CatalogDiscountID != discountID && discountID != 0 {
			return nil, errors.New("checkout discount no longer matches the requested automatic discount")
		}
		if existing.DiscountCode != code && code != "" {
			return nil, errors.New("checkout discount no longer matches the requested code")
		}
		if existing.Status == "redeemed" {
			return existing, nil
		}
		if existing.Status == "reserved" && !checkoutDiscountExpired(existing, time.Now().UTC()) {
			return existing, nil
		}
		if int64PtrValue(checkout.SubscriptionID) != 0 {
			return nil, errors.New("discount reservation expired after subscription creation; manual reconciliation required")
		}
		attempt = existing.AttemptCount + 1
		reservationKey = fmt.Sprintf("saas:%s:discount:%d", checkout.ID, attempt)
	}
	input := map[string]any{
		"_project_id": checkout.ProjectID, "price_id": price.PriceID, "product_id": price.ProductID,
		"quantity": int64(1), "subtotal_cents": price.UnitAmountCents, "currency": price.Currency,
		"customer_ref": fmt.Sprintf("saas_customer:%d", customer.ID), "context_ref": checkout.ID,
		"idempotency_key": reservationKey, "expires_in_seconds": int64(86400),
	}
	if discountID != 0 {
		input["discount_id"] = discountID
	} else {
		input["code"] = code
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_reserve", input, &out); err != nil {
		return nil, fmt.Errorf("reserve Catalog discount: %w", err)
	}
	reservation := unwrapMap(out, "reservation")
	reservationID := strArg(reservation, "reservation_id")
	status := strings.ToLower(strArg(reservation, "status"))
	if reservationID == "" || len(mapFromAny(reservation["application"])) == 0 {
		return nil, errors.New("Catalog discount reservation response is incomplete")
	}
	if status == "reserved" {
		expiresAt := timeStringFromAny(reservation["expires_at"])
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			return nil, errors.New("Catalog discount reservation response has an invalid expires_at")
		}
	}
	if status != "reserved" && status != "redeemed" {
		if err := dbCheckoutDiscountSave(ctx.AppDB(), checkout.ProjectID, checkout.ID, reservationKey, code, attempt, reservation); err != nil {
			return nil, err
		}
		if status == "expired" && !renewed && int64PtrValue(checkout.SubscriptionID) == 0 {
			return a.ensureCheckoutDiscountReservationAttempt(ctx, checkout, customer, price, discountID, code, true)
		}
		return nil, fmt.Errorf("Catalog discount reservation is %s", firstNonEmpty(status, "invalid"))
	}
	if err := dbCheckoutDiscountSave(ctx.AppDB(), checkout.ProjectID, checkout.ID, reservationKey, code, attempt, reservation); err != nil {
		return nil, err
	}
	return dbCheckoutDiscountGet(ctx.AppDB(), checkout.ProjectID, checkout.ID)
}

func (a *App) ensureCheckoutDiscountRedeemed(ctx *sdk.AppCtx, discount *CheckoutDiscount) error {
	if discount == nil || discount.Status == "redeemed" {
		return nil
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_discounts_redeem", map[string]any{
		"_project_id": discount.ProjectID, "reservation_id": discount.ReservationID,
	}, &out); err != nil {
		_ = dbCheckoutDiscountSetError(ctx.AppDB(), discount.ProjectID, discount.CheckoutID, err.Error())
		return fmt.Errorf("redeem Catalog discount: %w", err)
	}
	redemption := unwrapMap(out, "redemption")
	if status := strings.ToLower(strArg(redemption, "status")); status != "redeemed" {
		err := fmt.Errorf("Catalog discount redemption returned status %q", status)
		_ = dbCheckoutDiscountSetError(ctx.AppDB(), discount.ProjectID, discount.CheckoutID, err.Error())
		return err
	}
	return dbCheckoutDiscountSetRedeemed(ctx.AppDB(), discount.ProjectID, discount.CheckoutID, timeStringFromAny(redemption["redeemed_at"]))
}

func (a *App) reconcileCheckoutDiscounts(ctx *sdk.AppCtx, pid string) error {
	discounts, err := dbCheckoutDiscountsForReconciliation(ctx.AppDB(), pid, 20)
	if err != nil {
		return err
	}
	for _, discount := range discounts {
		if err := a.ensureCheckoutDiscountRedeemed(ctx, discount); err != nil {
			ctx.Logger().Warn("reconcile SaaS checkout discount", "checkout_id", discount.CheckoutID, "reservation_id", discount.ReservationID, "err", err)
		}
	}
	return nil
}

func checkoutDiscountExpired(discount *CheckoutDiscount, now time.Time) bool {
	if discount == nil || discount.ExpiresAt == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, discount.ExpiresAt)
	return err == nil && !now.Before(expires)
}

func dbCheckoutDiscountSave(db *sql.DB, pid, checkoutID, reservationKey, code string, attempt int64, reservation map[string]any) error {
	status := strings.ToLower(firstNonEmpty(strArg(reservation, "status"), "reserved"))
	if status != "reserved" && status != "redeemed" && status != "released" && status != "expired" && status != "failed" {
		return fmt.Errorf("invalid discount reservation status %q", status)
	}
	_, err := db.Exec(`INSERT INTO saas_checkout_discounts
		(project_id,checkout_id,reservation_id,reservation_key,catalog_discount_id,discount_code,status,application_json,currency,subtotal_cents,discount_cents,total_cents,expires_at,attempt_count,last_error,redeemed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,checkout_id) DO UPDATE SET
			reservation_id=excluded.reservation_id,reservation_key=excluded.reservation_key,catalog_discount_id=excluded.catalog_discount_id,
			discount_code=excluded.discount_code,status=excluded.status,application_json=excluded.application_json,currency=excluded.currency,
			subtotal_cents=excluded.subtotal_cents,discount_cents=excluded.discount_cents,total_cents=excluded.total_cents,
			expires_at=excluded.expires_at,attempt_count=excluded.attempt_count,last_error='',redeemed_at=excluded.redeemed_at,updated_at=CURRENT_TIMESTAMP`,
		pid, checkoutID, strArg(reservation, "reservation_id"), reservationKey, int64Arg(reservation, "discount_id"), firstNonEmpty(normalizeDiscountCode(strArg(reservation, "code")), code),
		status, jsonOrEmpty(reservation["application"], "{}"), strings.ToUpper(strArg(reservation, "currency")), int64Arg(reservation, "subtotal_cents"), int64Arg(reservation, "discount_cents"), int64Arg(reservation, "total_cents"),
		nullString(timeStringFromAny(reservation["expires_at"])), attempt, "", nullString(timeStringFromAny(reservation["redeemed_at"])))
	return err
}

func dbCheckoutDiscountGet(db *sql.DB, pid, checkoutID string) (*CheckoutDiscount, error) {
	discount, err := scanCheckoutDiscount(db.QueryRow(checkoutDiscountSelect()+` WHERE project_id=? AND checkout_id=?`, pid, checkoutID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return discount, err
}

func dbCheckoutDiscountSetError(db *sql.DB, pid, checkoutID, message string) error {
	_, err := db.Exec(`UPDATE saas_checkout_discounts SET last_error=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND checkout_id=?`, message, pid, checkoutID)
	return err
}

func dbCheckoutDiscountSetRedeemed(db *sql.DB, pid, checkoutID, redeemedAt string) error {
	if redeemedAt == "" {
		redeemedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := db.Exec(`UPDATE saas_checkout_discounts SET status='redeemed',last_error='',redeemed_at=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND checkout_id=?`, redeemedAt, pid, checkoutID)
	return err
}

func dbCheckoutDiscountsForReconciliation(db *sql.DB, pid string, limit int) ([]*CheckoutDiscount, error) {
	rows, err := db.Query(checkoutDiscountSelect()+` JOIN saas_checkouts c ON c.project_id=d.project_id AND c.id=d.checkout_id
		WHERE d.project_id=? AND d.status='reserved' AND c.subscription_id IS NOT NULL ORDER BY d.updated_at LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CheckoutDiscount
	for rows.Next() {
		discount, err := scanCheckoutDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, discount)
	}
	return out, rows.Err()
}

func checkoutDiscountSelect() string {
	return `SELECT d.project_id,d.checkout_id,d.reservation_id,d.reservation_key,d.catalog_discount_id,d.discount_code,d.status,d.application_json,d.currency,d.subtotal_cents,d.discount_cents,d.total_cents,d.expires_at,d.attempt_count,d.last_error,d.redeemed_at,d.created_at,d.updated_at FROM saas_checkout_discounts d`
}

func scanCheckoutDiscount(row rowScanner) (*CheckoutDiscount, error) {
	var discount CheckoutDiscount
	var application string
	var expiresAt, redeemedAt sql.NullString
	if err := row.Scan(&discount.ProjectID, &discount.CheckoutID, &discount.ReservationID, &discount.ReservationKey, &discount.CatalogDiscountID, &discount.DiscountCode, &discount.Status, &application, &discount.Currency, &discount.SubtotalCents, &discount.DiscountCents, &discount.TotalCents, &expiresAt, &discount.AttemptCount, &discount.LastError, &redeemedAt, &discount.CreatedAt, &discount.UpdatedAt); err != nil {
		return nil, err
	}
	discount.Application = json.RawMessage(application)
	if expiresAt.Valid {
		discount.ExpiresAt = expiresAt.String
	}
	if redeemedAt.Valid {
		discount.RedeemedAt = redeemedAt.String
	}
	return &discount, nil
}
