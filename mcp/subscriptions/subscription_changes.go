package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type SubscriptionChange struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SubscriptionID     int64           `json:"subscription_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	RequestFingerprint string          `json:"-"`
	SourceApp          string          `json:"source_app"`
	SourceRef          string          `json:"source_ref,omitempty"`
	Status             string          `json:"status"`
	EffectiveMode      string          `json:"effective_at"`
	EffectiveTime      string          `json:"effective_time"`
	ProrationPolicy    string          `json:"proration_policy"`
	DiscountPolicy     string          `json:"discount_policy"`
	OldItems           json.RawMessage `json:"old_items"`
	NewItems           json.RawMessage `json:"new_items"`
	Proration          json.RawMessage `json:"proration"`
	MetadataPatch      json.RawMessage `json:"subscription_metadata_patch,omitempty"`
	Interval           string          `json:"interval,omitempty"`
	IntervalCount      int64           `json:"interval_count,omitempty"`
	LastError          string          `json:"last_error,omitempty"`
	AttemptCount       int64           `json:"attempt_count"`
	AppliedAt          string          `json:"applied_at,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

func subscriptionChangeTools(a *App) []sdk.Tool {
	return []sdk.Tool{
		{Name: "subscription_changes_create", Description: "Create an idempotent immediate or next-cycle replacement of subscription items and calculate any generic proration.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "items": map[string]any{"type": "array"},
			"effective_at": map[string]any{"type": "string"}, "proration_policy": map[string]any{"type": "string"},
			"discount_policy": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"},
			"source_app": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
			"interval": map[string]any{"type": "string"}, "interval_count": map[string]any{"type": "integer"},
			"subscription_metadata_patch": map[string]any{"type": "object"},
			"defer_apply":                 map[string]any{"type": "boolean"},
		}, []string{"subscription_id", "items", "idempotency_key"}), Handler: a.toolSubscriptionChangesCreate},
		{Name: "subscription_changes_get", Description: "Fetch one durable subscription item change.", InputSchema: schemaObject(map[string]any{"change_id": map[string]any{"type": "integer"}}, []string{"change_id"}), Handler: a.toolSubscriptionChangesGet},
		{Name: "subscription_changes_apply", Description: "Apply a due pending subscription item change idempotently.", InputSchema: schemaObject(map[string]any{"change_id": map[string]any{"type": "integer"}}, []string{"change_id"}), Handler: a.toolSubscriptionChangesApply},
	}
}

func (a *App) toolSubscriptionChangesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	change, created, err := dbSubscriptionChangeCreate(ctx.AppDB(), pid, args, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if created && change.EffectiveMode == "immediate" && !boolArg(args, "defer_apply") {
		change, err = applySubscriptionChange(ctx, change.ID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	sub, err := dbSubscriptionGet(ctx.AppDB(), pid, change.SubscriptionID, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"change": change, "subscription": sub, "created": created}, nil
}

func (a *App) toolSubscriptionChangesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	change, err := dbSubscriptionChangeGet(ctx.AppDB(), pid, int64Arg(args, "change_id"))
	if err != nil || change == nil {
		return nil, firstErr(err, errors.New("subscription change not found"))
	}
	return map[string]any{"change": change}, nil
}

func (a *App) toolSubscriptionChangesApply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	change, err := dbSubscriptionChangeGet(ctx.AppDB(), pid, int64Arg(args, "change_id"))
	if err != nil || change == nil {
		return nil, firstErr(err, errors.New("subscription change not found"))
	}
	change, err = applySubscriptionChange(ctx, change.ID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionGet(ctx.AppDB(), pid, change.SubscriptionID, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"change": change, "subscription": sub}, nil
}

func dbSubscriptionChangeCreate(db *sql.DB, pid string, args map[string]any, now time.Time) (*SubscriptionChange, bool, error) {
	subID := int64Arg(args, "subscription_id")
	sub, err := dbSubscriptionGet(db, pid, subID, true)
	if err != nil || sub == nil {
		return nil, false, firstErr(err, errors.New("subscription not found"))
	}
	if sub.Status == "cancelled" || sub.Status == "ended" {
		return nil, false, fmt.Errorf("cannot change a %s subscription", sub.Status)
	}
	rawItems := arrayArg(args, "items")
	items := normalizeItems(rawItems, sub.Currency)
	if len(items) == 0 {
		return nil, false, errors.New("at least one valid replacement item required")
	}
	for _, item := range items {
		if err := validateItem(item); err != nil {
			return nil, false, err
		}
		if !strings.EqualFold(item.Currency, sub.Currency) {
			return nil, false, errors.New("replacement item currency must match the subscription currency")
		}
	}
	mode := strings.ToLower(firstNonEmpty(strArg(args, "effective_at"), "immediate"))
	if mode != "immediate" && mode != "next_cycle" {
		return nil, false, errors.New("effective_at must be immediate or next_cycle")
	}
	policy := strings.ToLower(firstNonEmpty(strArg(args, "proration_policy"), "prorate"))
	if policy != "none" && policy != "prorate" && policy != "charge_full" {
		return nil, false, errors.New("proration_policy must be none, prorate, or charge_full")
	}
	if mode == "next_cycle" && policy != "none" {
		return nil, false, errors.New("next_cycle changes require proration_policy=none")
	}
	discountPolicy := strings.ToLower(firstNonEmpty(strArg(args, "discount_policy"), "preserve"))
	if discountPolicy != "preserve" && discountPolicy != "drop" {
		return nil, false, errors.New("discount_policy must be preserve or drop")
	}
	idem := strings.TrimSpace(strArg(args, "idempotency_key"))
	if idem == "" {
		return nil, false, errors.New("idempotency_key required")
	}
	effective := now
	if mode == "next_cycle" {
		var ok bool
		effective, ok = parseTime(sub.CurrentPeriodEnd)
		if !ok {
			return nil, false, errors.New("next_cycle requires a valid current_period_end")
		}
	}
	itemMaps := normalizedItemMaps(items)
	metadataPatch := mapFromAny(args["subscription_metadata_patch"])
	fingerprint := subscriptionChangeFingerprint(subID, itemMaps, mode, policy, discountPolicy, strArg(args, "interval"), int64Arg(args, "interval_count"), metadataPatch)
	existing, err := dbSubscriptionChangeByIdempotency(db, pid, idem)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.RequestFingerprint != fingerprint {
			return nil, false, errors.New("idempotency_key was already used for a different subscription change")
		}
		return existing, false, nil
	}
	var pending int64
	if err := db.QueryRow(`SELECT id FROM subscription_changes WHERE project_id=? AND subscription_id=? AND status IN ('pending','awaiting_approval','processing','failed') LIMIT 1`, pid, subID).Scan(&pending); err == nil {
		return nil, false, fmt.Errorf("subscription already has pending change %d", pending)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	proration := calculateChangeProration(sub, items, mode, policy, discountPolicy, now)
	oldJSON := jsonOrEmpty(activeSubscriptionItems(sub.Items), "[]")
	newJSON := jsonOrEmpty(itemMaps, "[]")
	status := "pending"
	if mode == "immediate" && boolArg(args, "defer_apply") {
		status = "awaiting_approval"
	}
	var id int64
	err = db.QueryRow(`INSERT INTO subscription_changes
		(project_id,subscription_id,idempotency_key,request_fingerprint,source_app,source_ref,status,effective_mode,effective_at,proration_policy,discount_policy,old_items_json,new_items_json,proration_json,interval,interval_count,subscription_metadata_patch_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		pid, subID, idem, fingerprint, firstNonEmpty(strings.ToLower(strArg(args, "source_app")), "manual"), strArg(args, "source_ref"),
		status, mode, effective.UTC().Format(time.RFC3339), policy, discountPolicy, oldJSON, newJSON, jsonOrEmpty(proration, "{}"),
		strings.ToLower(strArg(args, "interval")), int64Arg(args, "interval_count"), jsonOrEmpty(metadataPatch, "{}")).Scan(&id)
	if err != nil {
		return nil, false, err
	}
	change, err := dbSubscriptionChangeGet(db, pid, id)
	return change, true, err
}

func normalizedItemMaps(items []itemIn) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"catalog_product_id": derefInt64(item.CatalogProductID), "catalog_price_id": derefInt64(item.CatalogPriceID),
			"sku": item.SKU, "title": item.Title, "quantity": item.Quantity, "unit_amount_cents": item.UnitAmountCents,
			"currency": item.Currency, "billing_scheme": item.BillingScheme, "meter_key": item.MeterKey,
			"included_units": item.IncludedUnits, "unit_size": item.UnitSize, "metadata": item.Metadata,
		})
	}
	return out
}

func activeSubscriptionItems(items []*SubItem) []*SubItem {
	out := make([]*SubItem, 0, len(items))
	for _, item := range items {
		if item.Status == "active" {
			out = append(out, item)
		}
	}
	return out
}

func subscriptionChangeFingerprint(subID int64, items []any, mode, proration, discount, interval string, intervalCount int64, metadataPatch map[string]any) string {
	payload := map[string]any{"subscription_id": subID, "items": items, "effective_at": mode, "proration_policy": proration, "discount_policy": discount, "interval": interval, "interval_count": intervalCount}
	if len(metadataPatch) > 0 {
		payload["subscription_metadata_patch"] = metadataPatch
	}
	sum := sha256.Sum256([]byte(jsonOrEmpty(payload, "{}")))
	return hex.EncodeToString(sum[:])
}

func calculateChangeProration(sub *Subscription, items []itemIn, mode, policy, discountPolicy string, now time.Time) map[string]any {
	oldTotal, newTotal := recurringChangeTotals(sub, items, discountPolicy)
	remainingBPS := int64(10000)
	if start, okStart := parseTime(sub.CurrentPeriodStart); okStart {
		if end, okEnd := parseTime(sub.CurrentPeriodEnd); okEnd && end.After(start) {
			remaining := end.Sub(now)
			if remaining < 0 {
				remaining = 0
			}
			remainingBPS = int64(math.Round(float64(remaining) / float64(end.Sub(start)) * 10000))
			if remainingBPS < 0 {
				remainingBPS = 0
			}
			if remainingBPS > 10000 {
				remainingBPS = 10000
			}
		}
	}
	charge, credit := int64(0), int64(0)
	if mode == "immediate" {
		switch policy {
		case "charge_full":
			charge = newTotal
		case "prorate":
			delta := newTotal - oldTotal
			amount := int64(math.Round(float64(absInt64(delta)) * float64(remainingBPS) / 10000))
			if delta >= 0 {
				charge = amount
			} else {
				credit = amount
			}
		}
	}
	lines := []any{}
	if charge > 0 {
		lines = append(lines, map[string]any{
			"description": "Subscription plan change", "quantity": 1, "unit_price_cents": charge,
			"metadata": map[string]any{"source_app": "subscriptions", "kind": "plan_change_proration", "old_recurring_cents": oldTotal, "new_recurring_cents": newTotal, "remaining_bps": remainingBPS},
		})
	}
	return map[string]any{
		"currency": sub.Currency, "old_recurring_cents": oldTotal, "new_recurring_cents": newTotal,
		"remaining_bps": remainingBPS, "charge_cents": charge, "credit_cents": credit, "line_items": lines,
		"discount_policy": discountPolicy,
	}
}

func recurringChangeTotals(sub *Subscription, replacements []itemIn, discountPolicy string) (int64, int64) {
	active := activeSubscriptionItems(sub.Items)
	oldTotal := recurringItemTotal(active)
	newTotal := recurringInputTotal(replacements)
	cycleNumber := int64(1)
	for _, cycle := range sub.Cycles {
		if cycle.CycleNumber > cycleNumber {
			cycleNumber = cycle.CycleNumber
		}
	}
	for _, discount := range sub.Discounts {
		oldItem, replacementIndex := replacementForDiscount(active, replacements, discount.SubscriptionItemID)
		if oldItem == nil {
			continue
		}
		amount, _, applies := calculateDiscount(discount, oldItem.Quantity, oldItem.UnitAmountCents, oldItem.Currency, cycleNumber)
		if applies {
			oldTotal -= amount
		}
		if discountPolicy != "preserve" || replacementIndex < 0 {
			continue
		}
		replacement := replacements[replacementIndex]
		amount, _, applies = calculateDiscount(discount, replacement.Quantity, replacement.UnitAmountCents, replacement.Currency, cycleNumber)
		if applies {
			newTotal -= amount
		}
	}
	return maxInt64(oldTotal, 0), maxInt64(newTotal, 0)
}

func replacementForDiscount(oldItems []*SubItem, replacements []itemIn, oldItemID int64) (*SubItem, int) {
	var oldItem *SubItem
	for _, item := range oldItems {
		if item.ID == oldItemID {
			oldItem = item
			break
		}
	}
	if oldItem == nil {
		return nil, -1
	}
	productID := derefInt64(oldItem.CatalogProductID)
	if productID != 0 {
		match := -1
		for i, item := range replacements {
			if derefInt64(item.CatalogProductID) != productID {
				continue
			}
			if match != -1 {
				match = -1
				break
			}
			match = i
		}
		if match >= 0 {
			return oldItem, match
		}
	}
	if oldItem.Position >= 0 && oldItem.Position < len(replacements) {
		return oldItem, oldItem.Position
	}
	return oldItem, -1
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func recurringItemTotal(items []*SubItem) int64 {
	var total int64
	for _, item := range items {
		if item.Status == "active" && item.BillingScheme == "flat" {
			total += int64(math.Round(float64(item.UnitAmountCents) * item.Quantity))
		}
	}
	return total
}

func recurringInputTotal(items []itemIn) int64 {
	var total int64
	for _, item := range items {
		if item.BillingScheme == "flat" {
			total += int64(math.Round(float64(item.UnitAmountCents) * item.Quantity))
		}
	}
	return total
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func applySubscriptionChange(ctx *sdk.AppCtx, changeID int64, now time.Time) (*SubscriptionChange, error) {
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		pid = strings.TrimSpace(strArg(map[string]any{"_project_id": ctx.CurrentProject()}, "_project_id"))
	}
	change, err := dbSubscriptionChangeGetAny(ctx.AppDB(), changeID)
	if err != nil || change == nil {
		return nil, firstErr(err, errors.New("subscription change not found"))
	}
	pid = change.ProjectID
	if change.Status == "applied" {
		return change, nil
	}
	effectiveAt, ok := parseTime(change.EffectiveTime)
	if !ok {
		return nil, errors.New("subscription change has invalid effective_time")
	}
	if now.Before(effectiveAt) {
		return nil, fmt.Errorf("subscription change is not due until %s", effectiveAt.Format(time.RFC3339))
	}
	claimed, err := dbClaimSubscriptionChange(ctx.AppDB(), pid, change.ID, now)
	if err != nil {
		return nil, err
	}
	if !claimed {
		latest, getErr := dbSubscriptionChangeGet(ctx.AppDB(), pid, change.ID)
		if getErr != nil {
			return nil, getErr
		}
		if latest != nil && latest.Status == "applied" {
			return latest, nil
		}
		return nil, errors.New("subscription change is already being applied")
	}
	if err := dbApplySubscriptionChange(ctx.AppDB(), change, now); err != nil {
		_ = dbFailSubscriptionChange(ctx.AppDB(), change, now, err)
		return nil, err
	}
	change, err = dbSubscriptionChangeGet(ctx.AppDB(), pid, change.ID)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.change.applied", map[string]any{
		"change_id": change.ID, "subscription_id": change.SubscriptionID, "source_app": change.SourceApp,
		"source_ref": change.SourceRef, "effective_at": change.EffectiveMode, "proration": mapFromAny(change.Proration),
	})
	emitSubscriptionUpdatedByID(ctx, pid, change.SubscriptionID)
	return change, nil
}

func dbApplySubscriptionChange(db *sql.DB, change *SubscriptionChange, now time.Time) error {
	sub, err := dbSubscriptionGet(db, change.ProjectID, change.SubscriptionID, true)
	if err != nil || sub == nil {
		return firstErr(err, errors.New("subscription not found"))
	}
	var raw []any
	if err := json.Unmarshal(change.NewItems, &raw); err != nil {
		return fmt.Errorf("decode replacement items: %w", err)
	}
	items := normalizeItems(raw, sub.Currency)
	if len(items) == 0 {
		return errors.New("subscription change has no replacement items")
	}
	nextCycle := int64(1)
	if err := db.QueryRow(`SELECT COALESCE(MAX(cycle_number),0)+1 FROM subscription_cycles WHERE project_id=? AND subscription_id=?`, change.ProjectID, change.SubscriptionID).Scan(&nextCycle); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	oldEnds := nextCycle - 1
	if _, err := tx.Exec(`UPDATE subscription_items SET status='cancelled',ends_cycle_number=?,updated_at=CURRENT_TIMESTAMP WHERE subscription_id=? AND status='active'`, oldEnds, change.SubscriptionID); err != nil {
		return err
	}
	newItemIDs := make([]int64, 0, len(items))
	for position, item := range items {
		result, err := tx.Exec(`INSERT INTO subscription_items
			(subscription_id,position,catalog_product_id,catalog_price_id,sku,title,quantity,unit_amount_cents,currency,billing_scheme,meter_key,included_units,unit_size,status,starts_cycle_number,ends_cycle_number,metadata)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,0,?)`,
			change.SubscriptionID, position, nullablePtr(item.CatalogProductID), nullablePtr(item.CatalogPriceID), nullStr(item.SKU), item.Title,
			item.Quantity, item.UnitAmountCents, item.Currency, item.BillingScheme, item.MeterKey, item.IncludedUnits, item.UnitSize, nextCycle, jsonOrEmpty(item.Metadata, "{}"))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		newItemIDs = append(newItemIDs, id)
	}
	if err := transitionSubscriptionDiscountsTx(tx, activeSubscriptionItems(sub.Items), items, sub.Discounts, newItemIDs, change.ID, nextCycle, change.DiscountPolicy, now); err != nil {
		return err
	}
	interval := sub.Interval
	if change.Interval != "" {
		interval = change.Interval
	}
	intervalCount := sub.IntervalCount
	if change.IntervalCount > 0 {
		intervalCount = change.IntervalCount
	}
	var currentMetadata string
	if err := tx.QueryRow(`SELECT metadata FROM subscriptions WHERE project_id=? AND id=?`, change.ProjectID, change.SubscriptionID).Scan(&currentMetadata); err != nil {
		return err
	}
	metadata, err := mergeMetadataPatch(json.RawMessage(currentMetadata), change.MetadataPatch)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE subscriptions SET interval=?,interval_count=?,metadata=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, interval, intervalCount, metadata, change.ProjectID, change.SubscriptionID); err != nil {
		return err
	}
	details := map[string]any{"change_id": change.ID, "source_app": change.SourceApp, "source_ref": change.SourceRef, "effective_at": change.EffectiveMode, "starts_cycle_number": nextCycle, "proration": mapFromAny(change.Proration)}
	if err := writeEventTx(tx, change.ProjectID, change.SubscriptionID, "system", "subscription.change_applied", details); err != nil {
		return err
	}
	nowText := now.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE subscription_changes SET status='applied',last_error='',lease_until=NULL,next_attempt_at=NULL,applied_at=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, nowText, change.ProjectID, change.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func transitionSubscriptionDiscountsTx(tx *sql.Tx, oldItems []*SubItem, replacements []itemIn, discounts []*SubscriptionDiscount, newItemIDs []int64, changeID, nextCycle int64, policy string, now time.Time) error {
	if len(discounts) == 0 {
		return nil
	}
	oldEnds := nextCycle - 1
	nowText := now.UTC().Format(time.RFC3339)
	for _, discount := range discounts {
		if discount.Status != "active" {
			continue
		}
		if _, err := tx.Exec(`UPDATE subscription_discounts SET status='cancelled',ends_cycle_number=?,cancelled_at=?,updated_at=? WHERE id=?`, oldEnds, nowText, nowText, discount.ID); err != nil {
			return err
		}
		if policy != "preserve" || len(newItemIDs) == 0 {
			continue
		}
		if _, applies := discountForCycle(discount, nextCycle); !applies {
			continue
		}
		_, replacementIndex := replacementForDiscount(oldItems, replacements, discount.SubscriptionItemID)
		if replacementIndex < 0 || replacementIndex >= len(newItemIDs) {
			continue
		}
		metadata := mapFromAny(discount.Metadata)
		metadata["continued_from_discount_id"] = discount.ID
		metadata["subscription_change_id"] = changeID
		_, err := tx.Exec(`INSERT INTO subscription_discounts
			(project_id,subscription_id,subscription_item_id,source_app,source_ref,catalog_discount_id,catalog_code_id,code,name,discount_type,percentage_bps,value_cents,currency,duration,duration_cycles,starts_cycle_number,ends_cycle_number,status,application_json,metadata,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,?,?,?)`,
			discount.ProjectID, discount.SubscriptionID, newItemIDs[replacementIndex], discount.SourceApp, fmt.Sprintf("%s:change:%d", discount.SourceRef, changeID),
			discount.CatalogDiscountID, discount.CatalogCodeID, discount.Code, discount.Name, discount.DiscountType, discount.PercentageBPS,
			discount.ValueCents, discount.Currency, discount.Duration, discount.DurationCycles, discount.StartsCycleNumber, discount.EndsCycleNumber,
			string(discount.Application), jsonOrEmpty(metadata, "{}"), nowText, nowText)
		if err != nil {
			return err
		}
	}
	return nil
}

func dbClaimSubscriptionChange(db *sql.DB, pid string, id int64, now time.Time) (bool, error) {
	nowText := now.UTC().Format(time.RFC3339)
	lease := now.Add(15 * time.Minute).UTC().Format(time.RFC3339)
	result, err := db.Exec(`UPDATE subscription_changes SET status='processing',attempt_count=attempt_count+1,lease_until=?,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND effective_at<=? AND ((status IN ('pending','awaiting_approval','failed') AND (next_attempt_at IS NULL OR next_attempt_at<=?)) OR (status='processing' AND lease_until<=?))`,
		lease, pid, id, nowText, nowText, nowText)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func dbFailSubscriptionChange(db *sql.DB, change *SubscriptionChange, now time.Time, cause error) error {
	delay := time.Duration(1<<minInt64(change.AttemptCount, 6)) * time.Minute
	_, err := db.Exec(`UPDATE subscription_changes SET status='failed',last_error=?,lease_until=NULL,next_attempt_at=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		cause.Error(), now.Add(delay).UTC().Format(time.RFC3339), change.ProjectID, change.ID)
	return err
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func dbSubscriptionChangeGet(db *sql.DB, pid string, id int64) (*SubscriptionChange, error) {
	if id == 0 {
		return nil, nil
	}
	return scanSubscriptionChange(db.QueryRow(subscriptionChangeSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbSubscriptionChangeGetAny(db *sql.DB, id int64) (*SubscriptionChange, error) {
	if id == 0 {
		return nil, nil
	}
	return scanSubscriptionChange(db.QueryRow(subscriptionChangeSelect()+` WHERE id=?`, id))
}

func dbSubscriptionChangeByIdempotency(db *sql.DB, pid, key string) (*SubscriptionChange, error) {
	return scanSubscriptionChange(db.QueryRow(subscriptionChangeSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func subscriptionChangeSelect() string {
	return `SELECT id,project_id,subscription_id,idempotency_key,request_fingerprint,source_app,source_ref,status,effective_mode,effective_at,proration_policy,discount_policy,old_items_json,new_items_json,proration_json,subscription_metadata_patch_json,interval,interval_count,last_error,attempt_count,applied_at,created_at,updated_at FROM subscription_changes`
}

func scanSubscriptionChange(row rowScanner) (*SubscriptionChange, error) {
	var change SubscriptionChange
	var applied sql.NullString
	var oldItems, newItems, proration, metadataPatch string
	err := row.Scan(&change.ID, &change.ProjectID, &change.SubscriptionID, &change.IdempotencyKey, &change.RequestFingerprint,
		&change.SourceApp, &change.SourceRef, &change.Status, &change.EffectiveMode, &change.EffectiveTime,
		&change.ProrationPolicy, &change.DiscountPolicy, &oldItems, &newItems, &proration, &metadataPatch, &change.Interval,
		&change.IntervalCount, &change.LastError, &change.AttemptCount, &applied, &change.CreatedAt, &change.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	change.OldItems = json.RawMessage(oldItems)
	change.NewItems = json.RawMessage(newItems)
	change.Proration = json.RawMessage(proration)
	change.MetadataPatch = json.RawMessage(metadataPatch)
	if applied.Valid {
		change.AppliedAt = applied.String
	}
	return &change, nil
}

func mergeMetadataPatch(current json.RawMessage, patch any) (string, error) {
	base := map[string]any{}
	if len(current) > 0 && string(current) != "null" {
		if err := json.Unmarshal(current, &base); err != nil {
			return "", fmt.Errorf("decode subscription metadata: %w", err)
		}
	}
	mergeMetadataMap(base, mapFromAny(patch))
	return jsonOrEmpty(base, "{}"), nil
}

func mergeMetadataMap(target, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		childPatch, patchIsMap := value.(map[string]any)
		childTarget, targetIsMap := target[key].(map[string]any)
		if patchIsMap && targetIsMap {
			mergeMetadataMap(childTarget, childPatch)
			target[key] = childTarget
			continue
		}
		target[key] = value
	}
}

func runSubscriptionChangeWorker(ctx *sdk.AppCtx, now time.Time) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("subscription change worker requires app context")
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		pid = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	if pid == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM subscription_changes WHERE project_id=? AND effective_at<=? AND ((status IN ('pending','failed') AND (next_attempt_at IS NULL OR next_attempt_at<=?)) OR (status='processing' AND lease_until<=?)) ORDER BY effective_at,id LIMIT 100`,
		pid, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var first error
	for _, id := range ids {
		if _, err := applySubscriptionChange(ctx, id, now); err != nil && first == nil {
			first = err
		}
	}
	return first
}
