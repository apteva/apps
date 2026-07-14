package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type SubscriptionDiscount struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SubscriptionID     int64           `json:"subscription_id"`
	SubscriptionItemID int64           `json:"subscription_item_id"`
	SourceApp          string          `json:"source_app"`
	SourceRef          string          `json:"source_ref"`
	CatalogDiscountID  int64           `json:"catalog_discount_id,omitempty"`
	CatalogCodeID      int64           `json:"catalog_code_id,omitempty"`
	Code               string          `json:"code,omitempty"`
	Name               string          `json:"name"`
	DiscountType       string          `json:"discount_type"`
	PercentageBPS      int64           `json:"percentage_bps,omitempty"`
	ValueCents         int64           `json:"value_cents,omitempty"`
	Currency           string          `json:"currency,omitempty"`
	Duration           string          `json:"duration"`
	DurationCycles     int64           `json:"duration_cycles,omitempty"`
	StartsCycleNumber  int64           `json:"starts_cycle_number"`
	EndsCycleNumber    int64           `json:"ends_cycle_number,omitempty"`
	Status             string          `json:"status"`
	Application        json.RawMessage `json:"application"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	CancelledAt        string          `json:"cancelled_at,omitempty"`
}

type discountInput struct {
	SourceApp      string
	SourceRef      string
	CatalogPriceID int64
	ItemPosition   *int64
	ItemID         int64
	Application    map[string]any
	Metadata       any
}

func subscriptionDiscountTools(a *App) []sdk.Tool {
	application := map[string]any{"type": "object"}
	return []sdk.Tool{
		{Name: "subscription_discounts_create", Description: "Attach an immutable discount application to a subscription item. Eligibility must already have been decided by the source app.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "subscription_item_id": map[string]any{"type": "integer"}, "catalog_price_id": map[string]any{"type": "integer"}, "item_position": map[string]any{"type": "integer"},
			"source_app": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"}, "application": application, "metadata": map[string]any{"type": "object"},
		}, []string{"subscription_id", "source_app", "source_ref", "application"}), Handler: a.toolSubscriptionDiscountCreate},
		{Name: "subscription_discounts_list", Description: "List immutable discount applications attached to a subscription.", InputSchema: schemaObject(map[string]any{"subscription_id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}}, []string{"subscription_id"}), Handler: a.toolSubscriptionDiscountList},
		{Name: "subscription_discounts_cancel", Description: "Stop a discount application from applying to future cycles.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolSubscriptionDiscountCancel},
	}
}

func (a *App) toolSubscriptionDiscountCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	in, err := normalizeDiscountInput(args)
	if err != nil {
		return nil, err
	}
	discount, created, err := dbSubscriptionDiscountCreate(ctx.AppDB(), pid, int64Arg(args, "subscription_id"), in)
	if err != nil {
		return nil, err
	}
	if created {
		ctx.Emit("subscription.discount.created", map[string]any{"subscription_id": discount.SubscriptionID, "subscription_item_id": discount.SubscriptionItemID, "discount_id": discount.ID})
	}
	return map[string]any{"discount": discount, "created": created}, nil
}

func (a *App) toolSubscriptionDiscountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	discounts, err := dbSubscriptionDiscountsList(ctx.AppDB(), pid, int64Arg(args, "subscription_id"), strArg(args, "status"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"discounts": discounts, "count": len(discounts)}, nil
}

func (a *App) toolSubscriptionDiscountCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	discount, changed, err := dbSubscriptionDiscountCancel(ctx.AppDB(), pid, int64Arg(args, "id"), strArg(args, "reason"))
	if err != nil {
		return nil, err
	}
	if changed {
		ctx.Emit("subscription.discount.cancelled", map[string]any{"subscription_id": discount.SubscriptionID, "subscription_item_id": discount.SubscriptionItemID, "discount_id": discount.ID})
	}
	return map[string]any{"discount": discount, "changed": changed}, nil
}

func normalizeDiscountInputs(raw []any) ([]discountInput, error) {
	out := make([]discountInput, 0, len(raw))
	for i, value := range raw {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("discounts[%d] must be an object", i)
		}
		in, err := normalizeDiscountInput(m)
		if err != nil {
			return nil, fmt.Errorf("discounts[%d]: %w", i, err)
		}
		out = append(out, in)
	}
	return out, nil
}

func normalizeDiscountInput(m map[string]any) (discountInput, error) {
	in := discountInput{
		SourceApp:      strings.ToLower(strArg(m, "source_app")),
		SourceRef:      strArg(m, "source_ref"),
		CatalogPriceID: int64Arg(m, "catalog_price_id"),
		ItemID:         int64Arg(m, "subscription_item_id"),
		Application:    mapFromAny(m["application"]),
		Metadata:       m["metadata"],
	}
	if _, ok := m["item_position"]; ok {
		position := int64Arg(m, "item_position")
		if position < 0 {
			return in, errors.New("item_position must be at least 0")
		}
		in.ItemPosition = &position
	}
	if in.SourceApp == "" || in.SourceRef == "" {
		return in, errors.New("source_app and source_ref required")
	}
	if len(in.Application) == 0 {
		return in, errors.New("application required")
	}
	typeName := strings.ToLower(strArg(in.Application, "discount_type"))
	switch typeName {
	case "percentage":
		bps := int64Arg(in.Application, "percentage_bps")
		if bps <= 0 || bps > 10000 {
			return in, errors.New("percentage_bps must be between 1 and 10000")
		}
	case "amount", "price_override":
		if typeName == "amount" && int64Arg(in.Application, "value_cents") <= 0 {
			return in, errors.New("amount value_cents must be greater than 0")
		}
		if typeName == "price_override" {
			if _, ok := in.Application["value_cents"]; !ok || int64Arg(in.Application, "value_cents") < 0 {
				return in, errors.New("price_override value_cents must be at least 0")
			}
		}
		if !validCurrency(strArg(in.Application, "currency")) {
			return in, errors.New("currency must be a three-letter code for amount and price_override discounts")
		}
	default:
		return in, fmt.Errorf("invalid discount_type %q", typeName)
	}
	duration := strings.ToLower(strArg(in.Application, "duration"))
	switch duration {
	case "once", "forever":
	case "repeating":
		if int64Arg(in.Application, "duration_cycles") <= 0 {
			return in, errors.New("repeating duration_cycles must be greater than 0")
		}
	default:
		return in, fmt.Errorf("invalid duration %q", duration)
	}
	if strArg(in.Application, "name") == "" {
		return in, errors.New("application name required")
	}
	return in, nil
}

func validCurrency(currency string) bool {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		return false
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func attachInitialDiscountsTx(tx *sql.Tx, pid string, subID int64, items []*SubItem, inputs []discountInput) error {
	for _, in := range inputs {
		if in.ItemID != 0 {
			return errors.New("subscription_item_id cannot be used while creating a subscription")
		}
		item, err := resolveDiscountItem(items, in)
		if err != nil {
			return err
		}
		discountID, err := insertSubscriptionDiscountTx(tx, pid, subID, item, in, 1)
		if err != nil {
			return err
		}
		if err := writeEventTx(tx, pid, subID, "system", "subscription.discount_created", map[string]any{"discount_id": discountID, "subscription_item_id": item.ID, "source_app": in.SourceApp, "source_ref": in.SourceRef}); err != nil {
			return err
		}
	}
	return nil
}

func dbSubscriptionDiscountCreate(db *sql.DB, pid string, subID int64, in discountInput) (*SubscriptionDiscount, bool, error) {
	if subID == 0 {
		return nil, false, errors.New("subscription_id required")
	}
	existing, err := dbSubscriptionDiscountBySource(db, pid, in.SourceApp, in.SourceRef)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.SubscriptionID != subID {
			return nil, false, errors.New("source_ref already belongs to another subscription")
		}
		if jsonOrEmpty(mapFromAny(existing.Application), "{}") != jsonOrEmpty(in.Application, "{}") {
			return nil, false, errors.New("source_ref was already used with a different discount application")
		}
		sub, loadErr := dbSubscriptionGet(db, pid, subID, true)
		if loadErr != nil || sub == nil {
			return nil, false, firstErr(loadErr, errors.New("subscription not found"))
		}
		item, resolveErr := resolveDiscountItem(sub.Items, in)
		if resolveErr != nil {
			return nil, false, resolveErr
		}
		if item.ID != existing.SubscriptionItemID {
			return nil, false, errors.New("source_ref was already used for a different subscription item")
		}
		return existing, false, nil
	}
	sub, err := dbSubscriptionGet(db, pid, subID, true)
	if err != nil || sub == nil {
		return nil, false, firstErr(err, errors.New("subscription not found"))
	}
	item, err := resolveDiscountItem(sub.Items, in)
	if err != nil {
		return nil, false, err
	}
	var starts int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(cycle_number),0)+1 FROM subscription_cycles WHERE project_id=? AND subscription_id=?`, pid, subID).Scan(&starts); err != nil {
		return nil, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	id, err := insertSubscriptionDiscountTx(tx, pid, subID, item, in, starts)
	if err != nil {
		return nil, false, err
	}
	if err := writeEventTx(tx, pid, subID, "system", "subscription.discount_created", map[string]any{"discount_id": id, "subscription_item_id": item.ID, "source_app": in.SourceApp, "source_ref": in.SourceRef}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	discount, err := dbSubscriptionDiscountGet(db, pid, id)
	return discount, true, err
}

func resolveDiscountItem(items []*SubItem, in discountInput) (*SubItem, error) {
	var matches []*SubItem
	for _, item := range items {
		match := false
		switch {
		case in.ItemID != 0:
			match = item.ID == in.ItemID
		case in.ItemPosition != nil:
			match = int64(item.Position) == *in.ItemPosition
		case in.CatalogPriceID != 0:
			match = derefInt64(item.CatalogPriceID) == in.CatalogPriceID
		case len(items) == 1:
			match = true
		}
		if match {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return nil, errors.New("discount must resolve to exactly one subscription item")
	}
	return matches[0], nil
}

func insertSubscriptionDiscountTx(tx *sql.Tx, pid string, subID int64, item *SubItem, in discountInput, starts int64) (int64, error) {
	application := in.Application
	discountType := strings.ToLower(strArg(application, "discount_type"))
	currency := strings.ToUpper(strArg(application, "currency"))
	if (discountType == "amount" || discountType == "price_override") && currency != strings.ToUpper(item.Currency) {
		return 0, fmt.Errorf("discount currency %s does not match item currency %s", currency, item.Currency)
	}
	rows, err := tx.Query(discountSelect()+` WHERE project_id=? AND subscription_id=? AND subscription_item_id=?`, pid, subID, item.ID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		existing, scanErr := scanSubscriptionDiscount(rows)
		if scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		if _, overlaps := discountForCycle(existing, starts); overlaps {
			rows.Close()
			return 0, fmt.Errorf("subscription item already has a discount for cycle %d", starts)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.Exec(`INSERT INTO subscription_discounts
		(project_id,subscription_id,subscription_item_id,source_app,source_ref,catalog_discount_id,catalog_code_id,code,name,discount_type,percentage_bps,value_cents,currency,duration,duration_cycles,starts_cycle_number,ends_cycle_number,status,application_json,metadata,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, subID, item.ID, in.SourceApp, in.SourceRef, int64Arg(application, "discount_id"), int64Arg(application, "code_id"), strArg(application, "code"), strArg(application, "name"),
		discountType, int64Arg(application, "percentage_bps"), int64Arg(application, "value_cents"), currency, strings.ToLower(strArg(application, "duration")), int64Arg(application, "duration_cycles"), starts, int64(0), "active",
		jsonOrEmpty(application, "{}"), jsonOrEmpty(in.Metadata, "{}"), now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func dbSubscriptionDiscountsList(db *sql.DB, pid string, subID int64, status string) ([]*SubscriptionDiscount, error) {
	if subID == 0 {
		return nil, errors.New("subscription_id required")
	}
	query := discountSelect() + ` WHERE project_id=? AND subscription_id=?`
	args := []any{pid, subID}
	if status != "" {
		if status != "active" && status != "cancelled" {
			return nil, fmt.Errorf("invalid discount status %q", status)
		}
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SubscriptionDiscount
	for rows.Next() {
		discount, err := scanSubscriptionDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, discount)
	}
	return out, rows.Err()
}

func dbSubscriptionDiscountGet(db *sql.DB, pid string, id int64) (*SubscriptionDiscount, error) {
	discount, err := scanSubscriptionDiscount(db.QueryRow(discountSelect()+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return discount, err
}

func dbSubscriptionDiscountBySource(db *sql.DB, pid, sourceApp, sourceRef string) (*SubscriptionDiscount, error) {
	discount, err := scanSubscriptionDiscount(db.QueryRow(discountSelect()+` WHERE project_id=? AND source_app=? AND source_ref=?`, pid, sourceApp, sourceRef))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return discount, err
}

func dbSubscriptionDiscountCancel(db *sql.DB, pid string, id int64, reason string) (*SubscriptionDiscount, bool, error) {
	discount, err := dbSubscriptionDiscountGet(db, pid, id)
	if err != nil || discount == nil {
		return nil, false, firstErr(err, errors.New("discount not found"))
	}
	if discount.Status == "cancelled" {
		return discount, false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var lastCycle int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(cycle_number),0) FROM subscription_cycles WHERE project_id=? AND subscription_id=?`, pid, discount.SubscriptionID).Scan(&lastCycle); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`UPDATE subscription_discounts SET status='cancelled',ends_cycle_number=?,cancelled_at=?,updated_at=? WHERE project_id=? AND id=? AND status='active'`, lastCycle, now, now, pid, id); err != nil {
		return nil, false, err
	}
	if err := writeEventTx(tx, pid, discount.SubscriptionID, "system", "subscription.discount_cancelled", map[string]any{"discount_id": id, "subscription_item_id": discount.SubscriptionItemID, "reason": reason}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	discount, err = dbSubscriptionDiscountGet(db, pid, id)
	return discount, true, err
}

func discountSelect() string {
	return `SELECT id,project_id,subscription_id,subscription_item_id,source_app,source_ref,catalog_discount_id,catalog_code_id,code,name,discount_type,percentage_bps,value_cents,currency,duration,duration_cycles,starts_cycle_number,ends_cycle_number,status,application_json,metadata,created_at,updated_at,cancelled_at FROM subscription_discounts`
}

func scanSubscriptionDiscount(row rowScanner) (*SubscriptionDiscount, error) {
	var discount SubscriptionDiscount
	var application, metadata string
	if err := row.Scan(&discount.ID, &discount.ProjectID, &discount.SubscriptionID, &discount.SubscriptionItemID, &discount.SourceApp, &discount.SourceRef, &discount.CatalogDiscountID, &discount.CatalogCodeID, &discount.Code, &discount.Name, &discount.DiscountType, &discount.PercentageBPS, &discount.ValueCents, &discount.Currency, &discount.Duration, &discount.DurationCycles, &discount.StartsCycleNumber, &discount.EndsCycleNumber, &discount.Status, &application, &metadata, &discount.CreatedAt, &discount.UpdatedAt, &discount.CancelledAt); err != nil {
		return nil, err
	}
	discount.Application = json.RawMessage(application)
	discount.Metadata = json.RawMessage(metadata)
	return &discount, nil
}

func discountForCycle(discount *SubscriptionDiscount, cycleNumber int64) (int64, bool) {
	if discount == nil || (discount.Status != "active" && discount.EndsCycleNumber == 0) || cycleNumber < discount.StartsCycleNumber || (discount.EndsCycleNumber > 0 && cycleNumber > discount.EndsCycleNumber) {
		return 0, false
	}
	applicationNumber := cycleNumber - discount.StartsCycleNumber + 1
	switch discount.Duration {
	case "once":
		return applicationNumber, applicationNumber == 1
	case "repeating":
		return applicationNumber, applicationNumber <= discount.DurationCycles
	case "forever":
		return applicationNumber, true
	default:
		return applicationNumber, false
	}
}

func calculateDiscount(discount *SubscriptionDiscount, quantity float64, unitAmount int64, currency string, cycleNumber int64) (int64, int64, bool) {
	applicationNumber, applies := discountForCycle(discount, cycleNumber)
	if !applies {
		return 0, applicationNumber, false
	}
	base := int64(math.Round(float64(unitAmount) * quantity))
	var amount int64
	switch discount.DiscountType {
	case "percentage":
		amount = int64(math.Round(float64(base) * float64(discount.PercentageBPS) / 10000))
	case "amount":
		if strings.EqualFold(discount.Currency, currency) {
			amount = discount.ValueCents
		}
	case "price_override":
		if strings.EqualFold(discount.Currency, currency) {
			amount = base - int64(math.Round(float64(discount.ValueCents)*quantity))
		}
	}
	if amount < 0 {
		amount = 0
	}
	if amount > base {
		amount = base
	}
	return amount, applicationNumber, true
}

func preparedDiscountSummary(discount *SubscriptionDiscount, itemID, cycleNumber, applicationNumber, baseAmount, discountAmount int64) map[string]any {
	return map[string]any{
		"id": discount.ID, "subscription_item_id": itemID, "source_app": discount.SourceApp, "source_ref": discount.SourceRef,
		"name": discount.Name, "code": discount.Code, "discount_type": discount.DiscountType, "duration": discount.Duration,
		"cycle_number": cycleNumber, "application_number": applicationNumber, "base_amount_cents": baseAmount,
		"discount_cents": discountAmount, "net_amount_cents": baseAmount - discountAmount,
	}
}

func discountsByItem(discounts []*SubscriptionDiscount) map[int64][]*SubscriptionDiscount {
	out := make(map[int64][]*SubscriptionDiscount, len(discounts))
	for _, discount := range discounts {
		if discount.Status == "active" || discount.EndsCycleNumber > 0 {
			out[discount.SubscriptionItemID] = append(out[discount.SubscriptionItemID], discount)
		}
	}
	return out
}

func discountForItemCycle(discounts map[int64][]*SubscriptionDiscount, itemID, cycleNumber int64) *SubscriptionDiscount {
	for _, discount := range discounts[itemID] {
		if _, applies := discountForCycle(discount, cycleNumber); applies {
			return discount
		}
	}
	return nil
}

func resolveInvoiceCycleNumber(db *sql.DB, pid string, subID int64, args map[string]any, start, end time.Time) (int64, error) {
	if cycleID := int64Arg(args, "cycle_id"); cycleID != 0 {
		cycle, err := dbCycleGet(db, pid, cycleID)
		if err != nil || cycle == nil {
			return 0, firstErr(err, errors.New("cycle not found"))
		}
		if cycle.SubscriptionID != subID {
			return 0, errors.New("cycle does not belong to subscription")
		}
		return cycle.CycleNumber, nil
	}
	var cycleNumber int64
	err := db.QueryRow(`SELECT cycle_number FROM subscription_cycles WHERE project_id=? AND subscription_id=? AND period_start=? AND period_end=?`, pid, subID, start.Format(time.RFC3339), end.Format(time.RFC3339)).Scan(&cycleNumber)
	if err == nil {
		return cycleNumber, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(cycle_number),0)+1 FROM subscription_cycles WHERE project_id=? AND subscription_id=? AND period_start < ?`, pid, subID, start.Format(time.RFC3339)).Scan(&cycleNumber); err != nil {
		return 0, err
	}
	return cycleNumber, nil
}
