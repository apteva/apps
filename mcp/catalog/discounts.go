package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultDiscountReservationSeconds = 15 * 60

type Discount struct {
	ID                        int64            `json:"id"`
	ProjectID                 string           `json:"project_id,omitempty"`
	Name                      string           `json:"name"`
	Description               string           `json:"description,omitempty"`
	DiscountType              string           `json:"discount_type"`
	PercentageBPS             int64            `json:"percentage_bps,omitempty"`
	ValueCents                int64            `json:"value_cents,omitempty"`
	Currency                  string           `json:"currency,omitempty"`
	Duration                  string           `json:"duration"`
	DurationCycles            int64            `json:"duration_cycles,omitempty"`
	StartsAt                  string           `json:"starts_at,omitempty"`
	EndsAt                    string           `json:"ends_at,omitempty"`
	MaxRedemptions            int64            `json:"max_redemptions,omitempty"`
	MaxRedemptionsPerCustomer int64            `json:"max_redemptions_per_customer,omitempty"`
	MinimumSubtotalCents      int64            `json:"minimum_subtotal_cents,omitempty"`
	Active                    bool             `json:"active"`
	Metadata                  json.RawMessage  `json:"metadata,omitempty"`
	CreatedAt                 string           `json:"created_at,omitempty"`
	UpdatedAt                 string           `json:"updated_at,omitempty"`
	ArchivedAt                string           `json:"archived_at,omitempty"`
	Scopes                    []*DiscountScope `json:"scopes,omitempty"`
	Codes                     []*DiscountCode  `json:"codes,omitempty"`
	ReservedCount             int64            `json:"reserved_count"`
	RedeemedCount             int64            `json:"redeemed_count"`
}

type DiscountScope struct {
	ID        int64  `json:"id"`
	ScopeType string `json:"scope_type"`
	ScopeID   int64  `json:"scope_id,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type DiscountCode struct {
	ID             int64           `json:"id"`
	DiscountID     int64           `json:"discount_id"`
	Code           string          `json:"code"`
	Active         bool            `json:"active"`
	MaxRedemptions int64           `json:"max_redemptions,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
	ArchivedAt     string          `json:"archived_at,omitempty"`
	ReservedCount  int64           `json:"reserved_count"`
	RedeemedCount  int64           `json:"redeemed_count"`
}

type DiscountQuote struct {
	Eligible      bool                `json:"eligible"`
	Reason        string              `json:"reason,omitempty"`
	DiscountID    int64               `json:"discount_id,omitempty"`
	CodeID        int64               `json:"code_id,omitempty"`
	Code          string              `json:"code,omitempty"`
	CustomerRef   string              `json:"customer_ref,omitempty"`
	ContextRef    string              `json:"context_ref,omitempty"`
	ProductID     int64               `json:"product_id,omitempty"`
	PriceID       int64               `json:"price_id,omitempty"`
	Quantity      int64               `json:"quantity"`
	Currency      string              `json:"currency,omitempty"`
	SubtotalCents int64               `json:"subtotal_cents"`
	DiscountCents int64               `json:"discount_cents"`
	TotalCents    int64               `json:"total_cents"`
	Application   DiscountApplication `json:"application,omitempty"`
}

type DiscountApplication struct {
	DiscountID     int64           `json:"discount_id"`
	CodeID         int64           `json:"code_id,omitempty"`
	Code           string          `json:"code,omitempty"`
	Name           string          `json:"name"`
	DiscountType   string          `json:"discount_type"`
	PercentageBPS  int64           `json:"percentage_bps,omitempty"`
	ValueCents     int64           `json:"value_cents,omitempty"`
	Currency       string          `json:"currency,omitempty"`
	Duration       string          `json:"duration"`
	DurationCycles int64           `json:"duration_cycles,omitempty"`
	StartsAt       string          `json:"starts_at,omitempty"`
	EndsAt         string          `json:"ends_at,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type DiscountReservation struct {
	ID                 int64               `json:"id"`
	PublicID           string              `json:"reservation_id"`
	IdempotencyKey     string              `json:"idempotency_key"`
	DiscountID         int64               `json:"discount_id"`
	CodeID             int64               `json:"code_id,omitempty"`
	CustomerRef        string              `json:"customer_ref,omitempty"`
	ContextRef         string              `json:"context_ref,omitempty"`
	ProductID          int64               `json:"product_id,omitempty"`
	PriceID            int64               `json:"price_id,omitempty"`
	Quantity           int64               `json:"quantity"`
	Currency           string              `json:"currency"`
	SubtotalCents      int64               `json:"subtotal_cents"`
	DiscountCents      int64               `json:"discount_cents"`
	TotalCents         int64               `json:"total_cents"`
	Status             string              `json:"status"`
	Application        DiscountApplication `json:"application"`
	ExpiresAt          string              `json:"expires_at"`
	CreatedAt          string              `json:"created_at"`
	UpdatedAt          string              `json:"updated_at"`
	RedeemedAt         string              `json:"redeemed_at,omitempty"`
	ReleasedAt         string              `json:"released_at,omitempty"`
	RequestFingerprint string              `json:"-"`
}

func (a *App) discountTools() []sdk.Tool {
	integer := map[string]any{"type": "integer"}
	stringValue := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	object := map[string]any{"type": "object"}
	integerArray := map[string]any{"type": "array", "items": integer}
	discountFields := map[string]any{
		"name": stringValue, "description": stringValue, "discount_type": stringValue,
		"percentage_bps": integer, "value_cents": integer, "currency": stringValue,
		"duration": stringValue, "duration_cycles": integer,
		"starts_at": stringValue, "ends_at": stringValue,
		"max_redemptions": integer, "max_redemptions_per_customer": integer,
		"minimum_subtotal_cents": integer, "active": boolean, "metadata": object,
		"all_products": boolean, "product_ids": integerArray, "price_ids": integerArray,
	}
	quoteFields := map[string]any{
		"discount_id": integer, "code": stringValue, "customer_ref": stringValue,
		"context_ref": stringValue, "product_id": integer, "price_id": integer,
		"quantity": integer, "subtotal_cents": integer, "currency": stringValue,
	}
	return []sdk.Tool{
		{Name: "catalog_discounts_list", Description: "List generic Catalog discounts. Args: active, archived, limit.", InputSchema: schemaObject(map[string]any{"active": boolean, "archived": boolean, "limit": integer}, nil), Handler: a.toolDiscountsList},
		{Name: "catalog_discounts_create", Description: "Create a generic discount scoped to all Catalog items or selected product/price IDs. Types: percentage, amount, price_override. Durations: once, repeating, forever.", InputSchema: schemaObject(discountFields, []string{"name", "discount_type"}), Handler: a.toolDiscountsCreate},
		{Name: "catalog_discounts_get", Description: "Fetch one discount with scopes, codes, and reservation/redemption counts.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolDiscountsGet},
		{Name: "catalog_discounts_update", Description: "Patch a discount definition or replace its scopes. Existing reservations retain their immutable application snapshots.", InputSchema: schemaObject(map[string]any{"id": integer, "patch": object}, []string{"id", "patch"}), Handler: a.toolDiscountsUpdate},
		{Name: "catalog_discounts_archive", Description: "Archive a discount so it cannot be used for new quotes or reservations.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolDiscountsArchive},
		{Name: "catalog_discount_codes_create", Description: "Create a case-insensitive code for a discount. Args: discount_id, code, max_redemptions, metadata.", InputSchema: schemaObject(map[string]any{"discount_id": integer, "code": stringValue, "max_redemptions": integer, "metadata": object}, []string{"discount_id", "code"}), Handler: a.toolDiscountCodesCreate},
		{Name: "catalog_discount_codes_list", Description: "List codes for a discount, including reservation/redemption counts.", InputSchema: schemaObject(map[string]any{"discount_id": integer, "active": boolean, "archived": boolean, "limit": integer}, []string{"discount_id"}), Handler: a.toolDiscountCodesList},
		{Name: "catalog_discount_codes_update", Description: "Update a discount code's active flag, redemption limit, or metadata. Code identity is immutable.", InputSchema: schemaObject(map[string]any{"id": integer, "patch": object}, []string{"id", "patch"}), Handler: a.toolDiscountCodesUpdate},
		{Name: "catalog_discounts_quote", Description: "Evaluate a discount or code without consuming capacity. Returns net amounts and an immutable application snapshot.", InputSchema: schemaObject(quoteFields, nil), Handler: a.toolDiscountsQuote},
		{Name: "catalog_discounts_reserve", Description: "Atomically reserve discount capacity. Adds required idempotency_key and optional expires_in_seconds to quote args.", InputSchema: schemaObject(mergeSchemas(quoteFields, map[string]any{"idempotency_key": stringValue, "expires_in_seconds": integer}), []string{"idempotency_key"}), Handler: a.toolDiscountsReserve},
		{Name: "catalog_discounts_redeem", Description: "Commit a reserved discount after the caller creates the purchase or subscription.", InputSchema: schemaObject(map[string]any{"reservation_id": stringValue}, []string{"reservation_id"}), Handler: a.toolDiscountsRedeem},
		{Name: "catalog_discounts_release", Description: "Release unused reserved capacity. Redeemed reservations cannot be released.", InputSchema: schemaObject(map[string]any{"reservation_id": stringValue}, []string{"reservation_id"}), Handler: a.toolDiscountsRelease},
	}
}

func mergeSchemas(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (a *App) toolDiscountsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := dbDiscountsList(ctx.AppDB(), pid, boolArg(args, "active", false), boolArg(args, "archived", false), clampLimit(intArg(args, "limit", 50), 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"discounts": items, "count": len(items)}, nil
}

func (a *App) toolDiscountsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := dbDiscountCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.created", d, "")
	return map[string]any{"discount": d}, nil
}

func (a *App) toolDiscountsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := dbDiscountGet(ctx.AppDB(), pid, int64Arg(args, "id"), true)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("discount not found")
	}
	return map[string]any{"discount": d}, nil
}

func (a *App) toolDiscountsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch required (object)")
	}
	d, err := dbDiscountUpdate(ctx.AppDB(), pid, int64Arg(args, "id"), patch)
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.updated", d, "")
	return map[string]any{"discount": d}, nil
}

func (a *App) toolDiscountsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	d, err := dbDiscountArchive(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.archived", d, "")
	return map[string]any{"discount": d}, nil
}

func (a *App) toolDiscountCodesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := dbDiscountCodeCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.code_created", &Discount{ID: c.DiscountID}, c.Code)
	return map[string]any{"code": c}, nil
}

func (a *App) toolDiscountCodesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := dbDiscountCodesList(ctx.AppDB(), pid, int64Arg(args, "discount_id"), boolArg(args, "active", false), boolArg(args, "archived", false), clampLimit(intArg(args, "limit", 50), 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"codes": items, "count": len(items)}, nil
}

func (a *App) toolDiscountCodesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	patch, _ := args["patch"].(map[string]any)
	if patch == nil {
		return nil, errors.New("patch required (object)")
	}
	c, err := dbDiscountCodeUpdate(ctx.AppDB(), pid, int64Arg(args, "id"), patch)
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.code_updated", &Discount{ID: c.DiscountID}, c.Code)
	return map[string]any{"code": c}, nil
}

func (a *App) toolDiscountsQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	quote, _, _, err := dbDiscountQuote(ctx.AppDB(), pid, args, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"quote": quote}, nil
}

func (a *App) toolDiscountsReserve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := dbDiscountReserve(ctx.AppDB(), pid, args, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.reserved", &Discount{ID: r.DiscountID}, r.PublicID)
	return map[string]any{"reservation": r}, nil
}

func (a *App) toolDiscountsRedeem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := dbDiscountRedeem(ctx.AppDB(), pid, strArg(args, "reservation_id"), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.redeemed", &Discount{ID: r.DiscountID}, r.PublicID)
	return map[string]any{"redemption": r}, nil
}

func (a *App) toolDiscountsRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := dbDiscountRelease(ctx.AppDB(), pid, strArg(args, "reservation_id"), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	emitDiscount(ctx, "discount.released", &Discount{ID: r.DiscountID}, r.PublicID)
	return map[string]any{"reservation": r}, nil
}

func dbDiscountCreate(db *sql.DB, pid string, args map[string]any) (*Discount, error) {
	d, err := discountFromArgs(args, nil)
	if err != nil {
		return nil, err
	}
	scopes, err := discountScopesFromArgs(db, pid, args, true)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowRFC3339()
	res, err := tx.Exec(`INSERT INTO catalog_discounts
		(project_id,name,description,discount_type,percentage_bps,value_cents,currency,duration,duration_cycles,starts_at,ends_at,max_redemptions,max_redemptions_per_customer,minimum_subtotal_cents,active,metadata,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, d.Name, d.Description, d.DiscountType, d.PercentageBPS, d.ValueCents, d.Currency,
		d.Duration, d.DurationCycles, d.StartsAt, d.EndsAt, d.MaxRedemptions,
		d.MaxRedemptionsPerCustomer, d.MinimumSubtotalCents, boolIntDiscount(d.Active), string(d.Metadata), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := replaceDiscountScopes(tx, pid, id, scopes, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbDiscountGet(db, pid, id, true)
}

func dbDiscountUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Discount, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	current, err := dbDiscountGet(db, pid, id, true)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ArchivedAt != "" {
		return nil, errors.New("discount not found or archived")
	}
	allowed := map[string]bool{"name": true, "description": true, "discount_type": true, "percentage_bps": true, "value_cents": true, "currency": true, "duration": true, "duration_cycles": true, "starts_at": true, "ends_at": true, "max_redemptions": true, "max_redemptions_per_customer": true, "minimum_subtotal_cents": true, "active": true, "metadata": true, "all_products": true, "product_ids": true, "price_ids": true}
	for k := range patch {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported discount field %q", k)
		}
	}
	merged := discountAsArgs(current)
	for k, v := range patch {
		merged[k] = v
	}
	d, err := discountFromArgs(merged, current)
	if err != nil {
		return nil, err
	}
	changeScopes := hasAnyKey(patch, "all_products", "product_ids", "price_ids")
	var scopes []*DiscountScope
	if changeScopes {
		scopes, err = discountScopesFromArgs(db, pid, patch, true)
		if err != nil {
			return nil, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowRFC3339()
	res, err := tx.Exec(`UPDATE catalog_discounts SET name=?,description=?,discount_type=?,percentage_bps=?,value_cents=?,currency=?,duration=?,duration_cycles=?,starts_at=?,ends_at=?,max_redemptions=?,max_redemptions_per_customer=?,minimum_subtotal_cents=?,active=?,metadata=?,updated_at=? WHERE id=? AND project_id=? AND archived_at=''`,
		d.Name, d.Description, d.DiscountType, d.PercentageBPS, d.ValueCents, d.Currency, d.Duration, d.DurationCycles, d.StartsAt, d.EndsAt, d.MaxRedemptions, d.MaxRedemptionsPerCustomer, d.MinimumSubtotalCents, boolIntDiscount(d.Active), string(d.Metadata), now, id, pid)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("discount not found or archived")
	}
	if changeScopes {
		if _, err := tx.Exec(`DELETE FROM catalog_discount_scopes WHERE project_id=? AND discount_id=?`, pid, id); err != nil {
			return nil, err
		}
		if err := replaceDiscountScopes(tx, pid, id, scopes, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbDiscountGet(db, pid, id, true)
}

func dbDiscountArchive(db *sql.DB, pid string, id int64) (*Discount, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	now := nowRFC3339()
	res, err := db.Exec(`UPDATE catalog_discounts SET active=0,archived_at=?,updated_at=? WHERE id=? AND project_id=? AND archived_at=''`, now, now, id, pid)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("discount not found or archived")
	}
	return dbDiscountGet(db, pid, id, true)
}

func dbDiscountGet(db *sql.DB, pid string, id int64, hydrate bool) (*Discount, error) {
	if id == 0 {
		return nil, nil
	}
	d, err := scanDiscount(db.QueryRow(discountSelect()+` WHERE id=? AND project_id=?`, id, pid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hydrate {
		d.Scopes, err = dbDiscountScopes(db, pid, id)
		if err != nil {
			return nil, err
		}
		d.Codes, err = dbDiscountCodesList(db, pid, id, false, true, 200)
		if err != nil {
			return nil, err
		}
		d.ReservedCount, d.RedeemedCount, err = dbDiscountCounts(db, pid, id, 0, "", time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

func dbDiscountsList(db *sql.DB, pid string, activeOnly, includeArchived bool, limit int) ([]*Discount, error) {
	where := []string{"project_id=?"}
	values := []any{pid}
	if activeOnly {
		where = append(where, "active=1")
	}
	if !includeArchived {
		where = append(where, "archived_at=''")
	}
	values = append(values, limit)
	rows, err := db.Query(discountSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ?`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Discount
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func discountSelect() string {
	return `SELECT id,project_id,name,description,discount_type,percentage_bps,value_cents,currency,duration,duration_cycles,starts_at,ends_at,max_redemptions,max_redemptions_per_customer,minimum_subtotal_cents,active,metadata,created_at,updated_at,archived_at FROM catalog_discounts`
}

func scanDiscount(s rowScanner) (*Discount, error) {
	var d Discount
	var active int
	var metadata string
	if err := s.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.DiscountType, &d.PercentageBPS, &d.ValueCents, &d.Currency, &d.Duration, &d.DurationCycles, &d.StartsAt, &d.EndsAt, &d.MaxRedemptions, &d.MaxRedemptionsPerCustomer, &d.MinimumSubtotalCents, &active, &metadata, &d.CreatedAt, &d.UpdatedAt, &d.ArchivedAt); err != nil {
		return nil, err
	}
	d.Active = active != 0
	d.Metadata = json.RawMessage(metadata)
	return &d, nil
}

func discountFromArgs(args map[string]any, current *Discount) (*Discount, error) {
	d := &Discount{
		Name: strings.TrimSpace(strArg(args, "name")), Description: strings.TrimSpace(strArg(args, "description")),
		DiscountType:  strings.ToLower(strings.TrimSpace(strArg(args, "discount_type"))),
		PercentageBPS: int64Arg(args, "percentage_bps"), ValueCents: int64Arg(args, "value_cents"),
		Currency: strings.ToUpper(strings.TrimSpace(strArg(args, "currency"))),
		Duration: strings.ToLower(strings.TrimSpace(strArg(args, "duration"))), DurationCycles: int64Arg(args, "duration_cycles"),
		StartsAt: strings.TrimSpace(strArg(args, "starts_at")), EndsAt: strings.TrimSpace(strArg(args, "ends_at")),
		MaxRedemptions: int64Arg(args, "max_redemptions"), MaxRedemptionsPerCustomer: int64Arg(args, "max_redemptions_per_customer"),
		MinimumSubtotalCents: int64Arg(args, "minimum_subtotal_cents"), Active: boolArg(args, "active", true),
		Metadata: json.RawMessage(jsonOrEmpty(args["metadata"], "{}")),
	}
	if d.Name == "" {
		return nil, errors.New("name required")
	}
	if d.Duration == "" {
		d.Duration = "once"
	}
	if d.MaxRedemptions < 0 || d.MaxRedemptionsPerCustomer < 0 || d.MinimumSubtotalCents < 0 {
		return nil, errors.New("redemption limits and minimum_subtotal_cents must be >= 0")
	}
	if d.DiscountType == "percentage" {
		if d.PercentageBPS < 1 || d.PercentageBPS > 10000 {
			return nil, errors.New("percentage_bps must be between 1 and 10000")
		}
		d.ValueCents = 0
		d.Currency = ""
	} else if d.DiscountType == "amount" || d.DiscountType == "price_override" {
		if d.DiscountType == "amount" && d.ValueCents <= 0 {
			return nil, errors.New("value_cents must be > 0 for amount discounts")
		}
		if d.DiscountType == "price_override" {
			if _, ok := args["value_cents"]; !ok && current == nil {
				return nil, errors.New("value_cents required for price_override discounts")
			}
			if d.ValueCents < 0 {
				return nil, errors.New("value_cents must be >= 0 for price_override discounts")
			}
		}
		if !looksLikeISO4217(d.Currency) {
			return nil, errors.New("currency must be a 3-letter ISO 4217 code for amount and price_override discounts")
		}
		d.PercentageBPS = 0
	} else {
		return nil, errors.New("discount_type must be percentage, amount, or price_override")
	}
	if d.Duration == "repeating" {
		if d.DurationCycles < 1 {
			return nil, errors.New("duration_cycles must be >= 1 for repeating discounts")
		}
	} else if d.Duration == "once" || d.Duration == "forever" {
		d.DurationCycles = 0
	} else {
		return nil, errors.New("duration must be once, repeating, or forever")
	}
	start, startSet, err := parseDiscountTime(d.StartsAt)
	if err != nil {
		return nil, fmt.Errorf("starts_at: %w", err)
	}
	end, endSet, err := parseDiscountTime(d.EndsAt)
	if err != nil {
		return nil, fmt.Errorf("ends_at: %w", err)
	}
	if startSet {
		d.StartsAt = start.Format(time.RFC3339)
	}
	if endSet {
		d.EndsAt = end.Format(time.RFC3339)
	}
	if startSet && endSet && !end.After(start) {
		return nil, errors.New("ends_at must be after starts_at")
	}
	return d, nil
}

func discountAsArgs(d *Discount) map[string]any {
	var metadata any = map[string]any{}
	_ = json.Unmarshal(d.Metadata, &metadata)
	return map[string]any{"name": d.Name, "description": d.Description, "discount_type": d.DiscountType, "percentage_bps": d.PercentageBPS, "value_cents": d.ValueCents, "currency": d.Currency, "duration": d.Duration, "duration_cycles": d.DurationCycles, "starts_at": d.StartsAt, "ends_at": d.EndsAt, "max_redemptions": d.MaxRedemptions, "max_redemptions_per_customer": d.MaxRedemptionsPerCustomer, "minimum_subtotal_cents": d.MinimumSubtotalCents, "active": d.Active, "metadata": metadata}
}

func discountScopesFromArgs(db *sql.DB, pid string, args map[string]any, required bool) ([]*DiscountScope, error) {
	all := boolArg(args, "all_products", false)
	productIDs, err := int64ArrayArg(args, "product_ids")
	if err != nil {
		return nil, err
	}
	priceIDs, err := int64ArrayArg(args, "price_ids")
	if err != nil {
		return nil, err
	}
	if all && (len(productIDs) > 0 || len(priceIDs) > 0) {
		return nil, errors.New("all_products cannot be combined with product_ids or price_ids")
	}
	if !all && len(productIDs) == 0 && len(priceIDs) == 0 {
		if required {
			return nil, errors.New("discount scope required: set all_products or provide product_ids/price_ids")
		}
		return nil, nil
	}
	var scopes []*DiscountScope
	if all {
		return []*DiscountScope{{ScopeType: "all"}}, nil
	}
	seen := map[string]bool{}
	for _, id := range productIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM products WHERE project_id=? AND id=? AND archived_at IS NULL`, pid, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("product %d not found or archived", id)
		}
		key := fmt.Sprintf("product:%d", id)
		if !seen[key] {
			scopes = append(scopes, &DiscountScope{ScopeType: "product", ScopeID: id})
			seen[key] = true
		}
	}
	for _, id := range priceIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM prices WHERE project_id=? AND id=? AND archived_at IS NULL`, pid, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("price %d not found or archived", id)
		}
		key := fmt.Sprintf("price:%d", id)
		if !seen[key] {
			scopes = append(scopes, &DiscountScope{ScopeType: "price", ScopeID: id})
			seen[key] = true
		}
	}
	return scopes, nil
}

func replaceDiscountScopes(tx *sql.Tx, pid string, discountID int64, scopes []*DiscountScope, now string) error {
	for _, s := range scopes {
		if _, err := tx.Exec(`INSERT INTO catalog_discount_scopes(project_id,discount_id,scope_type,scope_id,created_at) VALUES(?,?,?,?,?)`, pid, discountID, s.ScopeType, s.ScopeID, now); err != nil {
			return err
		}
	}
	return nil
}

func dbDiscountScopes(db *sql.DB, pid string, discountID int64) ([]*DiscountScope, error) {
	rows, err := db.Query(`SELECT id,scope_type,scope_id,created_at FROM catalog_discount_scopes WHERE project_id=? AND discount_id=? ORDER BY scope_type,scope_id`, pid, discountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DiscountScope
	for rows.Next() {
		s := &DiscountScope{}
		if err := rows.Scan(&s.ID, &s.ScopeType, &s.ScopeID, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbDiscountCodeCreate(db *sql.DB, pid string, args map[string]any) (*DiscountCode, error) {
	discountID := int64Arg(args, "discount_id")
	d, err := dbDiscountGet(db, pid, discountID, false)
	if err != nil {
		return nil, err
	}
	if d == nil || d.ArchivedAt != "" {
		return nil, errors.New("discount not found or archived")
	}
	code, normalized, err := normalizeDiscountCode(strArg(args, "code"))
	if err != nil {
		return nil, err
	}
	max := int64Arg(args, "max_redemptions")
	if max < 0 {
		return nil, errors.New("max_redemptions must be >= 0")
	}
	now := nowRFC3339()
	res, err := db.Exec(`INSERT INTO catalog_discount_codes(project_id,discount_id,code,normalized_code,active,max_redemptions,metadata,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, pid, discountID, code, normalized, max, jsonOrEmpty(args["metadata"], "{}"), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("discount code %q already exists in this project", code)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbDiscountCodeGet(db, pid, id)
}

func dbDiscountCodeGet(db *sql.DB, pid string, id int64) (*DiscountCode, error) {
	c, err := scanDiscountCode(db.QueryRow(discountCodeSelect()+` WHERE c.project_id=? AND c.id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbDiscountCodeByValue(db *sql.DB, pid, code string) (*DiscountCode, error) {
	_, normalized, err := normalizeDiscountCode(code)
	if err != nil {
		return nil, nil
	}
	c, err := scanDiscountCode(db.QueryRow(discountCodeSelect()+` WHERE c.project_id=? AND c.normalized_code=?`, pid, normalized))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbDiscountCodesList(db *sql.DB, pid string, discountID int64, activeOnly, includeArchived bool, limit int) ([]*DiscountCode, error) {
	if discountID == 0 {
		return nil, errors.New("discount_id required")
	}
	where := []string{"c.project_id=?", "c.discount_id=?"}
	values := []any{pid, discountID}
	if activeOnly {
		where = append(where, "c.active=1")
	}
	if !includeArchived {
		where = append(where, "c.archived_at=''")
	}
	values = append(values, limit)
	rows, err := db.Query(discountCodeSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY c.created_at DESC LIMIT ?`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DiscountCode
	for rows.Next() {
		c, err := scanDiscountCode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func discountCodeSelect() string {
	return `SELECT c.id,c.discount_id,c.code,c.active,c.max_redemptions,c.metadata,c.created_at,c.updated_at,c.archived_at,
	(SELECT COUNT(*) FROM catalog_discount_reservations r WHERE r.project_id=c.project_id AND r.code_id=c.id AND r.status='reserved' AND datetime(r.expires_at)>CURRENT_TIMESTAMP),
	(SELECT COUNT(*) FROM catalog_discount_reservations r WHERE r.project_id=c.project_id AND r.code_id=c.id AND r.status='redeemed') FROM catalog_discount_codes c`
}

func scanDiscountCode(s rowScanner) (*DiscountCode, error) {
	c := &DiscountCode{}
	var active int
	var metadata string
	if err := s.Scan(&c.ID, &c.DiscountID, &c.Code, &active, &c.MaxRedemptions, &metadata, &c.CreatedAt, &c.UpdatedAt, &c.ArchivedAt, &c.ReservedCount, &c.RedeemedCount); err != nil {
		return nil, err
	}
	c.Active = active != 0
	c.Metadata = json.RawMessage(metadata)
	return c, nil
}

func dbDiscountCodeUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*DiscountCode, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	allowed := map[string]bool{"active": true, "max_redemptions": true, "metadata": true}
	for k := range patch {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported discount code field %q", k)
		}
	}
	current, err := dbDiscountCodeGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ArchivedAt != "" {
		return nil, errors.New("discount code not found or archived")
	}
	active := current.Active
	if _, ok := patch["active"]; ok {
		active = boolArg(patch, "active", current.Active)
	}
	max := current.MaxRedemptions
	if _, ok := patch["max_redemptions"]; ok {
		max = int64Arg(patch, "max_redemptions")
	}
	if max < 0 {
		return nil, errors.New("max_redemptions must be >= 0")
	}
	metadata := string(current.Metadata)
	if v, ok := patch["metadata"]; ok {
		metadata = jsonOrEmpty(v, "{}")
	}
	now := nowRFC3339()
	_, err = db.Exec(`UPDATE catalog_discount_codes SET active=?,max_redemptions=?,metadata=?,updated_at=? WHERE project_id=? AND id=? AND archived_at=''`, boolIntDiscount(active), max, metadata, now, pid, id)
	if err != nil {
		return nil, err
	}
	return dbDiscountCodeGet(db, pid, id)
}

func dbDiscountQuote(db *sql.DB, pid string, args map[string]any, now time.Time) (*DiscountQuote, *Discount, *DiscountCode, error) {
	quote := &DiscountQuote{CustomerRef: strings.TrimSpace(strArg(args, "customer_ref")), ContextRef: strings.TrimSpace(strArg(args, "context_ref")), ProductID: int64Arg(args, "product_id"), PriceID: int64Arg(args, "price_id"), Quantity: int64Arg(args, "quantity")}
	if quote.Quantity == 0 {
		quote.Quantity = 1
	}
	if quote.Quantity < 1 {
		return nil, nil, nil, errors.New("quantity must be >= 1")
	}
	discountID := int64Arg(args, "discount_id")
	codeValue := strings.TrimSpace(strArg(args, "code"))
	if (discountID == 0) == (codeValue == "") {
		return nil, nil, nil, errors.New("provide exactly one of discount_id or code")
	}
	var code *DiscountCode
	var d *Discount
	var err error
	if codeValue != "" {
		code, err = dbDiscountCodeByValue(db, pid, codeValue)
		if err != nil {
			return nil, nil, nil, err
		}
		if code == nil {
			return ineligibleQuote(quote, "code_not_found"), nil, nil, nil
		}
		quote.CodeID = code.ID
		quote.Code = code.Code
		discountID = code.DiscountID
	}
	d, err = dbDiscountGet(db, pid, discountID, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if d == nil {
		return ineligibleQuote(quote, "discount_not_found"), nil, code, nil
	}
	quote.DiscountID = d.ID
	if !d.Active || d.ArchivedAt != "" {
		return ineligibleQuote(quote, "discount_inactive"), d, code, nil
	}
	if code != nil && (!code.Active || code.ArchivedAt != "") {
		return ineligibleQuote(quote, "code_inactive"), d, code, nil
	}
	if t, set, _ := parseDiscountTime(d.StartsAt); set && now.Before(t) {
		return ineligibleQuote(quote, "not_started"), d, code, nil
	}
	if t, set, _ := parseDiscountTime(d.EndsAt); set && !now.Before(t) {
		return ineligibleQuote(quote, "expired"), d, code, nil
	}
	if quote.PriceID != 0 {
		p, err := dbPriceGet(db, pid, quote.PriceID)
		if err != nil {
			return nil, nil, nil, err
		}
		if p == nil || !p.Active || p.ArchivedAt != "" {
			return ineligibleQuote(quote, "price_not_available"), d, code, nil
		}
		if quote.ProductID != 0 && quote.ProductID != p.ProductID {
			return nil, nil, nil, errors.New("product_id does not match price_id")
		}
		quote.ProductID = p.ProductID
		quote.Currency = p.Currency
		if p.UnitAmountCents > 0 && quote.Quantity > math.MaxInt64/p.UnitAmountCents {
			return nil, nil, nil, errors.New("price quantity overflows cents")
		}
		quote.SubtotalCents = p.UnitAmountCents * quote.Quantity
	}
	if requested := strings.ToUpper(strings.TrimSpace(strArg(args, "currency"))); requested != "" {
		if !looksLikeISO4217(requested) {
			return nil, nil, nil, errors.New("currency must be a 3-letter ISO 4217 code")
		}
		if quote.Currency != "" && requested != quote.Currency {
			return ineligibleQuote(quote, "currency_mismatch"), d, code, nil
		}
		quote.Currency = requested
	}
	if subtotal, ok := optionalInt64(args, "subtotal_cents"); ok {
		quote.SubtotalCents = subtotal
	}
	if quote.SubtotalCents <= 0 {
		return nil, nil, nil, errors.New("subtotal_cents must be > 0 or derivable from price_id")
	}
	if quote.Currency == "" {
		return nil, nil, nil, errors.New("currency required when price_id is omitted")
	}
	if quote.ProductID != 0 && quote.PriceID == 0 {
		p, err := dbProductGetByID(db, pid, quote.ProductID)
		if err != nil {
			return nil, nil, nil, err
		}
		if p == nil || p.ArchivedAt != "" {
			return ineligibleQuote(quote, "product_not_available"), d, code, nil
		}
	}
	matched, err := dbDiscountScopeMatches(db, pid, d.ID, quote.ProductID, quote.PriceID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !matched {
		return ineligibleQuote(quote, "scope_mismatch"), d, code, nil
	}
	if quote.SubtotalCents < d.MinimumSubtotalCents {
		return ineligibleQuote(quote, "minimum_subtotal_not_met"), d, code, nil
	}
	reserved, redeemed, err := dbDiscountCounts(db, pid, d.ID, 0, "", now)
	if err != nil {
		return nil, nil, nil, err
	}
	if d.MaxRedemptions > 0 && reserved+redeemed >= d.MaxRedemptions {
		return ineligibleQuote(quote, "discount_limit_reached"), d, code, nil
	}
	if d.MaxRedemptionsPerCustomer > 0 {
		if quote.CustomerRef == "" {
			return ineligibleQuote(quote, "customer_ref_required"), d, code, nil
		}
		reserved, redeemed, err = dbDiscountCounts(db, pid, d.ID, 0, quote.CustomerRef, now)
		if err != nil {
			return nil, nil, nil, err
		}
		if reserved+redeemed >= d.MaxRedemptionsPerCustomer {
			return ineligibleQuote(quote, "customer_limit_reached"), d, code, nil
		}
	}
	if code != nil && code.MaxRedemptions > 0 {
		reserved, redeemed, err = dbDiscountCounts(db, pid, d.ID, code.ID, "", now)
		if err != nil {
			return nil, nil, nil, err
		}
		if reserved+redeemed >= code.MaxRedemptions {
			return ineligibleQuote(quote, "code_limit_reached"), d, code, nil
		}
	}
	if d.DiscountType != "percentage" && d.Currency != quote.Currency {
		return ineligibleQuote(quote, "currency_mismatch"), d, code, nil
	}
	var discount int64
	switch d.DiscountType {
	case "percentage":
		if quote.SubtotalCents > (math.MaxInt64-5000)/d.PercentageBPS {
			return nil, nil, nil, errors.New("subtotal too large")
		}
		discount = (quote.SubtotalCents*d.PercentageBPS + 5000) / 10000
	case "amount":
		discount = minInt64(d.ValueCents, quote.SubtotalCents)
	case "price_override":
		if d.ValueCents > 0 && quote.Quantity > math.MaxInt64/d.ValueCents {
			return nil, nil, nil, errors.New("price override quantity overflows cents")
		}
		overrideTotal := d.ValueCents * quote.Quantity
		if overrideTotal >= quote.SubtotalCents {
			return ineligibleQuote(quote, "override_not_lower_than_subtotal"), d, code, nil
		}
		discount = quote.SubtotalCents - overrideTotal
	}
	quote.Eligible = true
	quote.DiscountCents = discount
	quote.TotalCents = quote.SubtotalCents - discount
	quote.Application = discountApplication(d, code)
	return quote, d, code, nil
}

func dbDiscountReserve(db *sql.DB, pid string, args map[string]any, now time.Time) (*DiscountReservation, error) {
	idempotency := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idempotency == "" {
		return nil, errors.New("idempotency_key required")
	}
	if len(idempotency) > 200 {
		return nil, errors.New("idempotency_key must be at most 200 characters")
	}
	nowText := now.Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE catalog_discount_reservations SET status='expired',updated_at=? WHERE project_id=? AND status='reserved' AND expires_at<=?`, nowText, pid, nowText); err != nil {
		return nil, err
	}
	fingerprint, err := discountRequestFingerprint(args)
	if err != nil {
		return nil, err
	}
	existing, err := dbDiscountReservationByIdempotency(db, pid, idempotency)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.RequestFingerprint != fingerprint {
			return nil, errors.New("idempotency_key already used with different discount parameters")
		}
		return existing, nil
	}
	quote, d, code, err := dbDiscountQuote(db, pid, args, now)
	if err != nil {
		return nil, err
	}
	if !quote.Eligible {
		return nil, fmt.Errorf("discount is not eligible: %s", quote.Reason)
	}
	expiresSeconds := int64Arg(args, "expires_in_seconds")
	if expiresSeconds == 0 {
		expiresSeconds = defaultDiscountReservationSeconds
	}
	if expiresSeconds < 60 || expiresSeconds > 86400 {
		return nil, errors.New("expires_in_seconds must be between 60 and 86400")
	}
	expires := now.Add(time.Duration(expiresSeconds) * time.Second).Format(time.RFC3339)
	publicID, err := newDiscountReservationID()
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(quote.Application)
	if err != nil {
		return nil, err
	}
	codeID := int64(0)
	codeMax := int64(0)
	codeUpdatedAt := ""
	if code != nil {
		codeID = code.ID
		codeMax = code.MaxRedemptions
		codeUpdatedAt = code.UpdatedAt
	}
	res, err := db.Exec(`INSERT INTO catalog_discount_reservations(project_id,public_id,idempotency_key,request_fingerprint,discount_id,code_id,customer_ref,context_ref,product_id,price_id,quantity,currency,subtotal_cents,discount_cents,total_cents,status,snapshot_json,expires_at,created_at,updated_at)
		SELECT ?,?,?,?,?,NULLIF(?,0),?,?,?,?,?,?,?,?,?, 'reserved',?,?,?,?
		WHERE EXISTS (SELECT 1 FROM catalog_discounts WHERE project_id=? AND id=? AND active=1 AND archived_at='' AND updated_at=?)
		AND (?=0 OR EXISTS (SELECT 1 FROM catalog_discount_codes WHERE project_id=? AND id=? AND active=1 AND archived_at='' AND updated_at=?))
		AND (?=0 OR (SELECT COUNT(*) FROM catalog_discount_reservations WHERE project_id=? AND discount_id=? AND (status='redeemed' OR (status='reserved' AND expires_at>?)))<?)
		AND (?=0 OR (?<>'' AND (SELECT COUNT(*) FROM catalog_discount_reservations WHERE project_id=? AND discount_id=? AND customer_ref=? AND (status='redeemed' OR (status='reserved' AND expires_at>?)))<?))
		AND (?=0 OR (SELECT COUNT(*) FROM catalog_discount_reservations WHERE project_id=? AND code_id=? AND (status='redeemed' OR (status='reserved' AND expires_at>?)))<?)`,
		pid, publicID, idempotency, fingerprint, d.ID, codeID, quote.CustomerRef, quote.ContextRef, quote.ProductID, quote.PriceID, quote.Quantity, quote.Currency, quote.SubtotalCents, quote.DiscountCents, quote.TotalCents, string(snapshot), expires, nowText, nowText,
		pid, d.ID, d.UpdatedAt,
		codeID, pid, codeID, codeUpdatedAt,
		d.MaxRedemptions, pid, d.ID, nowText, d.MaxRedemptions,
		d.MaxRedemptionsPerCustomer, quote.CustomerRef, pid, d.ID, quote.CustomerRef, nowText, d.MaxRedemptionsPerCustomer,
		codeMax, pid, codeID, nowText, codeMax)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			existing, getErr := dbDiscountReservationByIdempotency(db, pid, idempotency)
			if getErr == nil && existing != nil && existing.RequestFingerprint == fingerprint {
				return existing, nil
			}
		}
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		latest, _, _, quoteErr := dbDiscountQuote(db, pid, args, now)
		if quoteErr != nil {
			return nil, quoteErr
		}
		if !latest.Eligible {
			return nil, fmt.Errorf("discount is not eligible: %s", latest.Reason)
		}
		return nil, errors.New("discount changed during reservation; retry")
	}
	return dbDiscountReservationGet(db, pid, publicID)
}

func dbDiscountRedeem(db *sql.DB, pid, publicID string, now time.Time) (*DiscountReservation, error) {
	if strings.TrimSpace(publicID) == "" {
		return nil, errors.New("reservation_id required")
	}
	r, err := dbDiscountReservationGet(db, pid, publicID)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("discount reservation not found")
	}
	if r.Status == "redeemed" {
		return r, nil
	}
	if r.Status == "released" {
		return nil, errors.New("released discount reservation cannot be redeemed")
	}
	nowText := now.Format(time.RFC3339)
	if r.Status == "expired" || !now.Before(parseTimeOrZero(r.ExpiresAt)) {
		_, _ = db.Exec(`UPDATE catalog_discount_reservations SET status='expired',updated_at=? WHERE project_id=? AND public_id=? AND status='reserved'`, nowText, pid, publicID)
		return nil, errors.New("discount reservation expired")
	}
	res, err := db.Exec(`UPDATE catalog_discount_reservations SET status='redeemed',redeemed_at=?,updated_at=? WHERE project_id=? AND public_id=? AND status='reserved' AND expires_at>?`, nowText, nowText, pid, publicID, nowText)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		latest, getErr := dbDiscountReservationGet(db, pid, publicID)
		if getErr != nil {
			return nil, getErr
		}
		if latest != nil && latest.Status == "redeemed" {
			return latest, nil
		}
		return nil, errors.New("discount reservation is no longer redeemable")
	}
	return dbDiscountReservationGet(db, pid, publicID)
}

func dbDiscountRelease(db *sql.DB, pid, publicID string, now time.Time) (*DiscountReservation, error) {
	if strings.TrimSpace(publicID) == "" {
		return nil, errors.New("reservation_id required")
	}
	r, err := dbDiscountReservationGet(db, pid, publicID)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("discount reservation not found")
	}
	if r.Status == "redeemed" {
		return nil, errors.New("redeemed discount reservation cannot be released")
	}
	if r.Status == "released" || r.Status == "expired" {
		return r, nil
	}
	nowText := now.Format(time.RFC3339)
	if !now.Before(parseTimeOrZero(r.ExpiresAt)) {
		if _, err := db.Exec(`UPDATE catalog_discount_reservations SET status='expired',updated_at=? WHERE project_id=? AND public_id=? AND status='reserved'`, nowText, pid, publicID); err != nil {
			return nil, err
		}
		return dbDiscountReservationGet(db, pid, publicID)
	}
	_, err = db.Exec(`UPDATE catalog_discount_reservations SET status='released',released_at=?,updated_at=? WHERE project_id=? AND public_id=? AND status='reserved'`, nowText, nowText, pid, publicID)
	if err != nil {
		return nil, err
	}
	latest, err := dbDiscountReservationGet(db, pid, publicID)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Status == "redeemed" {
		return nil, errors.New("redeemed discount reservation cannot be released")
	}
	return latest, nil
}

func dbDiscountReservationGet(db *sql.DB, pid, publicID string) (*DiscountReservation, error) {
	r, err := scanDiscountReservation(db.QueryRow(discountReservationSelect()+` WHERE project_id=? AND public_id=?`, pid, publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}
func dbDiscountReservationByIdempotency(db *sql.DB, pid, key string) (*DiscountReservation, error) {
	r, err := scanDiscountReservation(db.QueryRow(discountReservationSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}
func discountReservationSelect() string {
	return `SELECT id,public_id,idempotency_key,request_fingerprint,discount_id,COALESCE(code_id,0),customer_ref,context_ref,product_id,price_id,quantity,currency,subtotal_cents,discount_cents,total_cents,status,snapshot_json,expires_at,created_at,updated_at,redeemed_at,released_at FROM catalog_discount_reservations`
}
func scanDiscountReservation(s rowScanner) (*DiscountReservation, error) {
	r := &DiscountReservation{}
	var snapshot string
	if err := s.Scan(&r.ID, &r.PublicID, &r.IdempotencyKey, &r.RequestFingerprint, &r.DiscountID, &r.CodeID, &r.CustomerRef, &r.ContextRef, &r.ProductID, &r.PriceID, &r.Quantity, &r.Currency, &r.SubtotalCents, &r.DiscountCents, &r.TotalCents, &r.Status, &snapshot, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt, &r.RedeemedAt, &r.ReleasedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(snapshot), &r.Application); err != nil {
		return nil, err
	}
	return r, nil
}

func dbDiscountScopeMatches(db *sql.DB, pid string, discountID, productID, priceID int64) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM catalog_discount_scopes WHERE project_id=? AND discount_id=? AND (scope_type='all' OR (scope_type='product' AND scope_id=?) OR (scope_type='price' AND scope_id=?))`, pid, discountID, productID, priceID).Scan(&n)
	return n > 0, err
}

func dbDiscountCounts(db *sql.DB, pid string, discountID, codeID int64, customerRef string, now time.Time) (int64, int64, error) {
	where := []string{"project_id=?", "discount_id=?"}
	args := []any{pid, discountID}
	if codeID != 0 {
		where = append(where, "code_id=?")
		args = append(args, codeID)
	}
	if customerRef != "" {
		where = append(where, "customer_ref=?")
		args = append(args, customerRef)
	}
	base := strings.Join(where, " AND ")
	var reserved, redeemed int64
	args1 := append(append([]any{}, args...), now.Format(time.RFC3339))
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_discount_reservations WHERE `+base+` AND status='reserved' AND expires_at>?`, args1...).Scan(&reserved); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_discount_reservations WHERE `+base+` AND status='redeemed'`, args...).Scan(&redeemed); err != nil {
		return 0, 0, err
	}
	return reserved, redeemed, nil
}

func discountApplication(d *Discount, code *DiscountCode) DiscountApplication {
	a := DiscountApplication{DiscountID: d.ID, Name: d.Name, DiscountType: d.DiscountType, PercentageBPS: d.PercentageBPS, ValueCents: d.ValueCents, Currency: d.Currency, Duration: d.Duration, DurationCycles: d.DurationCycles, StartsAt: d.StartsAt, EndsAt: d.EndsAt, Metadata: append(json.RawMessage(nil), d.Metadata...)}
	if code != nil {
		a.CodeID = code.ID
		a.Code = code.Code
	}
	return a
}
func ineligibleQuote(q *DiscountQuote, reason string) *DiscountQuote {
	q.Eligible = false
	q.Reason = reason
	q.DiscountCents = 0
	q.TotalCents = q.SubtotalCents
	return q
}
func parseDiscountTime(value string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false, errors.New("must be RFC3339")
	}
	return t.UTC(), true, nil
}
func parseTimeOrZero(value string) time.Time { t, _ := time.Parse(time.RFC3339, value); return t }
func optionalInt64(args map[string]any, key string) (int64, bool) {
	_, ok := args[key]
	if !ok {
		return 0, false
	}
	return int64Arg(args, key), true
}
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func boolIntDiscount(v bool) int {
	if v {
		return 1
	}
	return 0
}
func hasAnyKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func int64ArrayArg(args map[string]any, key string) ([]int64, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	var out []int64
	switch values := v.(type) {
	case []any:
		for i, item := range values {
			n := int64Arg(map[string]any{"v": item}, "v")
			if n <= 0 {
				return nil, fmt.Errorf("%s[%d] must be a positive integer", key, i)
			}
			out = append(out, n)
		}
	case []int64:
		out = append(out, values...)
	case []int:
		for _, n := range values {
			out = append(out, int64(n))
		}
	default:
		return nil, fmt.Errorf("%s must be an array of integers", key)
	}
	for _, n := range out {
		if n <= 0 {
			return nil, fmt.Errorf("%s values must be positive integers", key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func normalizeDiscountCode(value string) (string, string, error) {
	code := strings.TrimSpace(value)
	if len(code) < 3 || len(code) > 64 {
		return "", "", errors.New("code must be between 3 and 64 characters")
	}
	normalized := strings.ToUpper(code)
	for _, r := range normalized {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", "", errors.New("code may contain only letters, numbers, hyphens, and underscores")
		}
	}
	return code, normalized, nil
}

func discountRequestFingerprint(args map[string]any) (string, error) {
	normalized := map[string]any{}
	for _, key := range []string{"discount_id", "code", "customer_ref", "context_ref", "product_id", "price_id", "quantity", "subtotal_cents", "currency", "expires_in_seconds"} {
		if value, ok := args[key]; ok {
			normalized[key] = value
		}
	}
	if code, ok := normalized["code"].(string); ok {
		normalized["code"] = strings.ToUpper(strings.TrimSpace(code))
	}
	if currency, ok := normalized["currency"].(string); ok {
		normalized["currency"] = strings.ToUpper(strings.TrimSpace(currency))
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func newDiscountReservationID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "dres_" + hex.EncodeToString(raw), nil
}

func emitDiscount(ctx *sdk.AppCtx, topic string, d *Discount, ref string) {
	if ctx == nil || d == nil {
		return
	}
	ctx.Emit(topic, map[string]any{"discount_id": d.ID, "ref": ref})
}
