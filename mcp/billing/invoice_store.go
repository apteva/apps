package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func dbInvoiceSearch(db *sql.DB, pid string, f invoiceFilters) ([]*Invoice, error) {
	if err := validateDateRange(f.since, f.until); err != nil {
		return nil, err
	}
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
	if f.customerID != 0 {
		where = append(where, "customer_id = ?")
		args = append(args, f.customerID)
	}
	if f.status != "" {
		where = append(where, "status = ?")
		args = append(args, f.status)
	}
	if f.provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.provider)
	}
	if f.currency != "" {
		where = append(where, "currency = ?")
		args = append(args, strings.ToUpper(f.currency))
	}
	if f.since != "" {
		where = append(where, "julianday(created_at) >= julianday(?)")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "julianday(created_at) < julianday(?)")
		args = append(args, f.until)
	}
	if f.minTotalCents != 0 {
		where = append(where, "total_cents >= ?")
		args = append(args, f.minTotalCents)
	}
	if f.maxTotalCents != 0 {
		where = append(where, "total_cents <= ?")
		args = append(args, f.maxTotalCents)
	}
	if f.q != "" {
		where = append(where, "(number LIKE ? OR notes LIKE ? OR CAST(id AS TEXT)=?)")
		pat := "%" + f.q + "%"
		args = append(args, pat, pat, f.q)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, min(limit, 1001), max(0, f.offset))
	// Rewrite WHERE clause column refs to use the `i.` alias since we're
	// joining now. Cheap string replace — column names here are
	// hand-controlled above so no ambiguity.
	for k, w := range where {
		if strings.HasPrefix(w, "julianday(") {
			where[k] = strings.ReplaceAll(w, "created_at", "i.created_at")
		} else if strings.HasPrefix(w, "(") {
			where[k] = strings.NewReplacer("number", "i.number", "notes", "i.notes", "CAST(id", "CAST(i.id").Replace(w)
		} else {
			where[k] = "i." + w
		}
	}
	orderBy := "julianday(i.created_at) DESC,i.id DESC"
	if strings.EqualFold(f.sort, "due_date") {
		orderBy = "CASE WHEN i.due_date IS NULL OR i.due_date = '' THEN 1 ELSE 0 END, julianday(i.due_date) DESC, julianday(i.created_at) DESC, i.id DESC"
	}
	rows, err := db.Query(
		`SELECT i.id, i.project_id, i.customer_id, i.provider, i.number, i.status, i.currency,
		        i.subtotal_cents, i.tax_cents, i.total_cents, i.amount_paid_cents,
		        i.accounting_date, i.due_date, i.notes, i.external_id, i.external_url, i.last_synced_at, i.metadata,
		        i.finalized_at, i.paid_at, i.voided_at, i.created_at, i.updated_at,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),i.tax_treatment,i.collection_hold
		 FROM invoices i
		 LEFT JOIN customers c ON c.id = i.customer_id AND c.deleted_at IS NULL
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY `+orderBy+`
		 LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func dbInvoiceGetByID(db *sql.DB, pid string, id int64) (*Invoice, error) {
	row := db.QueryRow(
		`SELECT i.id, i.project_id, i.customer_id, i.provider, i.number, i.status, i.currency,
		        i.subtotal_cents, i.tax_cents, i.total_cents, i.amount_paid_cents,
		        i.accounting_date, i.due_date, i.notes, i.external_id, i.external_url, i.last_synced_at, i.metadata,
		        i.finalized_at, i.paid_at, i.voided_at, i.created_at, i.updated_at,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),i.tax_treatment,i.collection_hold
		 FROM invoices i
		 LEFT JOIN customers c ON c.id = i.customer_id AND c.deleted_at IS NULL
		 WHERE i.id = ? AND i.project_id = ? AND i.deleted_at IS NULL`, id, pid)
	inv, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err == nil && inv != nil {
		inv.CreditCents = maxInt(inv.AmountPaidCents-inv.TotalCents, 0)
	}
	return inv, err
}

func dbInvoiceGetByNumber(db *sql.DB, pid, number string) (*Invoice, error) {
	row := db.QueryRow(
		`SELECT i.id, i.project_id, i.customer_id, i.provider, i.number, i.status, i.currency,
		        i.subtotal_cents, i.tax_cents, i.total_cents, i.amount_paid_cents,
		        i.accounting_date, i.due_date, i.notes, i.external_id, i.external_url, i.last_synced_at, i.metadata,
		        i.finalized_at, i.paid_at, i.voided_at, i.created_at, i.updated_at,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),i.tax_treatment,i.collection_hold
		 FROM invoices i
		 LEFT JOIN customers c ON c.id = i.customer_id AND c.deleted_at IS NULL
		 WHERE i.project_id = ? AND i.number = ? AND i.deleted_at IS NULL`, pid, number)
	inv, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err == nil && inv != nil {
		inv.CreditCents = maxInt(inv.AmountPaidCents-inv.TotalCents, 0)
	}
	return inv, err
}

func dbInvoiceCreate(db *sql.DB, inv *Invoice, actor string) (*Invoice, error) {
	inv.AccountingDate = strings.TrimSpace(inv.AccountingDate)
	if err := validateAccountingDate(inv.AccountingDate); err != nil {
		return nil, err
	}
	// Verify customer exists + not deleted.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM customers
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		inv.CustomerID, inv.ProjectID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("customer %d not found", inv.CustomerID)
	}
	subtotal, tax, total := computeTotals(inv.LineItems)
	if total > maxMoney || total < -maxMoney {
		return nil, errors.New("invoice total exceeds supported range")
	}
	inv.SubtotalCents, inv.TaxCents, inv.TotalCents = subtotal, tax, total

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowRFC3339()
	res, err := tx.Exec(
		`INSERT INTO invoices (project_id, customer_id, provider, status, currency,
		                       subtotal_cents, tax_cents, total_cents, amount_paid_cents,
		                       accounting_date, due_date, notes, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, 'draft', ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		inv.ProjectID, inv.CustomerID, inv.Provider, inv.Currency,
		inv.SubtotalCents, inv.TaxCents, inv.TotalCents,
		nullStr(inv.AccountingDate), nullStr(inv.DueDate), nullStr(inv.Notes), jsonOrEmpty(inv.Metadata, "{}"),
		now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	for i, li := range inv.LineItems {
		if _, err := tx.Exec(
			`INSERT INTO invoice_line_items
			   (invoice_id, position, description, quantity, unit_price_cents,
			    amount_cents, tax_rate_bps, metadata, price_id, product_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, li.Description, li.Quantity, li.UnitPriceCents,
			li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}"),
			nullInt(li.PriceID), nullInt(li.ProductID)); err != nil {
			return nil, err
		}
	}
	if err := writeAuditTx(tx, id, actor, "create", map[string]any{
		"provider":       inv.Provider,
		"currency":       inv.Currency,
		"line_count":     len(inv.LineItems),
		"total_cents":    inv.TotalCents,
		"subtotal_cents": inv.SubtotalCents,
		"tax_cents":      inv.TaxCents,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out, err := dbInvoiceGetByID(db, inv.ProjectID, id)
	if err != nil || out == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, inv.ProjectID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func dbInvoiceAddLineItem(db *sql.DB, pid string, id int64, li LineItem, actor string) (*Invoice, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	if status != "draft" {
		return nil, fmt.Errorf("cannot add line item: invoice %d is %s, only drafts accept line items", id, status)
	}
	var pos int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position)+1, 0) FROM invoice_line_items WHERE invoice_id = ?`,
		id).Scan(&pos); err != nil {
		return nil, err
	}
	li.Position = pos
	if _, err := tx.Exec(
		`INSERT INTO invoice_line_items
		   (invoice_id, position, description, quantity, unit_price_cents,
		    amount_cents, tax_rate_bps, metadata, price_id, product_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, li.Position, li.Description, li.Quantity, li.UnitPriceCents,
		li.AmountCents, li.TaxRateBps, jsonOrEmpty(li.Metadata, "{}"),
		nullInt(li.PriceID), nullInt(li.ProductID)); err != nil {
		return nil, err
	}
	if err := recomputeInvoiceTotalsTx(tx, id); err != nil {
		return nil, err
	}
	if err := writeAuditTx(tx, id, actor, "add_line_item", map[string]any{
		"description":      li.Description,
		"quantity":         li.Quantity,
		"unit_price_cents": li.UnitPriceCents,
		"amount_cents":     li.AmountCents,
		"tax_rate_bps":     li.TaxRateBps,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// dbInvoiceFinalize transitions draft → open. Idempotent: re-finalizing
// an already-open invoice returns the existing record. seqStart is the
// first sequence value to use for the current year; defaults to 1001
// for a professional-looking starting number.
func dbInvoiceFinalize(db *sql.DB, pid string, id int64, format string, seqStart int64, actor string) (*Invoice, error) {
	// Allocate the number, snapshot, and transition in one transaction.
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		status, currency, provider string
		number                     sql.NullString
	)
	if err := tx.QueryRow(
		`SELECT status, currency, provider, number FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status, &currency, &provider, &number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	if status == "open" || status == "paid" || status == "void" || status == "uncollectible" {
		// Idempotent — return current state.
		_ = tx.Commit()
		inv, err := dbInvoiceGetByID(db, pid, id)
		if err != nil || inv == nil {
			return nil, err
		}
		if err := loadInvoiceChildren(db, pid, inv); err != nil {
			return nil, err
		}
		return inv, nil
	}
	if status != "draft" {
		return nil, fmt.Errorf("cannot finalize: invoice %d has unexpected status %s", id, status)
	}
	// v0.1.0 only knows how to finalize local invoices. v0.1.1 will
	// branch here on provider.
	if provider != "local" {
		return nil, fmt.Errorf("provider=%q finalize unsupported in v0.1.0 — use 'local'", provider)
	}

	// Fail-fast: drafts with zero line items shouldn't finalize.
	var lineCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM invoice_line_items WHERE invoice_id = ?`, id).Scan(&lineCount); err != nil {
		return nil, err
	}
	if lineCount == 0 {
		return nil, errors.New("cannot finalize an empty draft — add at least one line item")
	}

	var total, tax int64
	var treatment string
	if err := tx.QueryRow("SELECT total_cents,tax_cents,tax_treatment FROM invoices WHERE id=?", id).Scan(&total, &tax, &treatment); err != nil {
		return nil, err
	}
	if total < 0 {
		return nil, errors.New("negative invoice totals require a credit adjustment, not finalization")
	}
	if treatment != "standard" && tax != 0 {
		return nil, errors.New("exempt and reverse-charge invoices must have zero tax")
	}
	// Mint number.
	num, err := mintInvoiceNumberTx(tx, pid, format, seqStart)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`UPDATE invoices
		 SET status = 'open', number = ?, finalized_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, num, id); err != nil {
		// Unique-index conflict on number → race. Caller can retry.
		return nil, fmt.Errorf("finalize: %w", err)
	}
	if err := writeAuditTx(tx, id, actor, "finalize", map[string]any{
		"provider": provider,
		"number":   num,
	}); err != nil {
		return nil, err
	}
	if total == 0 {
		if _, err := tx.Exec("UPDATE invoices SET status='paid',paid_at=CURRENT_TIMESTAMP WHERE id=?", id); err != nil {
			return nil, err
		}
	}
	if err := captureSnapshotTx(tx, id, "finalized"); err != nil {
		return nil, err
	}
	if err := queueInvoiceEventTx(tx, id, "invoice.finalized", fmt.Sprintf("invoice:%d:finalized", id), 0); err != nil {
		return nil, err
	}
	if total == 0 {
		if err := queueInvoiceEventTx(tx, id, "invoice.paid", fmt.Sprintf("invoice:%d:zero-paid", id), 0); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func dbInvoiceVoid(db *sql.DB, pid string, id int64, reason, actor string) (*Invoice, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(
		`SELECT status FROM invoices WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	var paid, active int
	if err := tx.QueryRow("SELECT amount_paid_cents FROM invoices WHERE id=?", id).Scan(&paid); err != nil {
		return nil, err
	}
	if err := tx.QueryRow("SELECT count(*) FROM billing_payment_locks WHERE invoice_id=?", id).Scan(&active); err != nil {
		return nil, err
	}
	if paid != 0 || active > 0 {
		return nil, errors.New("cannot void a paid or partially paid invoice with recorded payments or an active provider payment; cancel/reconcile it first")
	}
	switch status {
	case "void":
		// Idempotent.
	case "paid":
		return nil, errors.New("cannot void a paid invoice — record a refund via payments_record(amount<0)")
	case "draft":
		return nil, errors.New("cannot void a draft — delete it instead (drafts have no lasting effect)")
	case "open", "uncollectible":
		if _, err := tx.Exec(
			`UPDATE invoices SET status='void', voided_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if err := writeAuditTx(tx, id, actor, "void", map[string]any{"reason": reason}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invoice has unexpected status %s", status)
	}
	if err := queueInvoiceEventTx(tx, id, "invoice.voided", fmt.Sprintf("invoice:%d:voided", id), 0); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// dbInvoiceUpdate patches an invoice. Field rules:
//   - notes, accounting_date, due_date, metadata: any status
//   - customer_id, currency, line_items: drafts only
//
// line_items, when present, replaces the entire array (drafts only).
// We DELETE + INSERT inside the same tx so totals stay consistent;
// recomputeInvoiceTotalsTx is called at the end if any quantity-affecting
// field changed.
func dbInvoiceUpdate(db *sql.DB, pid string, id int64, patch map[string]any, actor string) (*Invoice, error) {
	if err := validateInput(patch); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRow(
		`SELECT status FROM invoices WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invoice %d not found", id)
		}
		return nil, err
	}
	if status == "void" {
		return nil, errors.New("cannot edit a voided invoice")
	}
	isDraft := status == "draft"

	// Reject draft-only fields when not a draft.
	if !isDraft {
		for _, k := range []string{"customer_id", "currency", "line_items", "tax_treatment"} {
			if _, ok := patch[k]; ok {
				return nil, fmt.Errorf("%s can only be changed while invoice is draft (status=%s)", k, status)
			}
		}
	}

	var sets []string
	var args []any
	auditDetails := map[string]any{}
	if v, ok := patch["tax_treatment"]; ok {
		t, _ := v.(string)
		if t != "standard" && t != "reverse_charge" && t != "exempt" {
			return nil, errors.New("invalid tax treatment")
		}
		sets = append(sets, "tax_treatment=?")
		args = append(args, t)
		auditDetails["tax_treatment"] = t
	}

	if v, ok := patch["notes"]; ok {
		sets = append(sets, "notes = ?")
		s, _ := v.(string)
		args = append(args, s)
		auditDetails["notes_changed"] = true
	}
	if v, ok := patch["accounting_date"]; ok {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if err := validateAccountingDate(s); err != nil {
			return nil, err
		}
		sets = append(sets, "accounting_date = ?")
		if s == "" {
			args = append(args, nil)
		} else {
			args = append(args, s)
		}
		auditDetails["accounting_date"] = s
	}
	if v, ok := patch["due_date"]; ok {
		s, _ := v.(string)
		sets = append(sets, "due_date = ?")
		if s == "" {
			args = append(args, nil)
		} else {
			args = append(args, s)
		}
		auditDetails["due_date"] = s
	}
	if v, ok := patch["metadata"]; ok {
		sets = append(sets, "metadata = ?")
		args = append(args, jsonOrEmpty(v, "{}"))
	}

	totalsAffected := false

	if isDraft {
		if v, ok := patch["customer_id"]; ok {
			cid := int64Arg(map[string]any{"customer_id": v}, "customer_id")
			if cid == 0 {
				return nil, errors.New("customer_id must be a positive integer")
			}
			// Verify customer exists in this project (and isn't soft-deleted).
			var n int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM customers WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
				cid, pid).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, fmt.Errorf("customer %d not found in this project", cid)
			}
			sets = append(sets, "customer_id = ?")
			args = append(args, cid)
			auditDetails["customer_id"] = cid
		}
		if v, ok := patch["currency"]; ok {
			cur := strings.ToUpper(strings.TrimSpace(toString(v)))
			if !looksLikeISO4217(cur) {
				return nil, fmt.Errorf("currency %q must be a supported 2-decimal ISO 4217 code", cur)
			}
			sets = append(sets, "currency = ?")
			args = append(args, cur)
			auditDetails["currency"] = cur
		}
		if rawItems, ok := patch["line_items"].([]any); ok {
			var cur string
			if err := tx.QueryRow("SELECT currency FROM invoices WHERE id=?", id).Scan(&cur); err != nil {
				return nil, err
			}
			if v := strArg(patch, "currency"); v != "" {
				cur = strings.ToUpper(v)
			}
			for _, r := range rawItems {
				if c := strArg(mapFromAny(r), "_catalog_currency"); c != "" && c != cur {
					return nil, errors.New("catalog currency does not match invoice")
				}
			}
			items, err := normaliseLineItems(rawItems, 0)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`DELETE FROM invoice_line_items WHERE invoice_id = ?`, id); err != nil {
				return nil, err
			}
			for i, it := range items {
				meta := "{}"
				if len(it.Metadata) > 0 {
					meta = string(it.Metadata)
				}
				if _, err := tx.Exec(
					`INSERT INTO invoice_line_items
					    (invoice_id, position, description, quantity, unit_price_cents,
					     amount_cents, tax_rate_bps, metadata, price_id, product_id)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					id, i, it.Description, it.Quantity, it.UnitPriceCents,
					it.AmountCents, it.TaxRateBps, meta,
					nullInt(it.PriceID), nullInt(it.ProductID)); err != nil {
					return nil, err
				}
			}
			totalsAffected = true
			auditDetails["line_items"] = len(items)
		}
	}

	if len(sets) > 0 {
		sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, id, pid)
		q := `UPDATE invoices SET ` + strings.Join(sets, ", ") +
			` WHERE id = ? AND project_id = ? AND deleted_at IS NULL`
		if _, err := tx.Exec(q, args...); err != nil {
			return nil, err
		}
	}
	if totalsAffected {
		if err := recomputeInvoiceTotalsTx(tx, id); err != nil {
			return nil, err
		}
	}
	if len(auditDetails) > 0 {
		if err := writeAuditTx(tx, id, actor, "update", auditDetails); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, id)
	if err != nil || inv == nil {
		return nil, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func loadInvoiceChildren(db *sql.DB, pid string, inv *Invoice) error {
	inv.LineItems = nil
	inv.AuditLog = nil
	inv.Payments = nil
	rows, err := db.Query(
		`SELECT id, invoice_id, position, description, quantity, unit_price_cents,
		        amount_cents, tax_rate_bps, external_id, metadata,
		        price_id, product_id
		 FROM invoice_line_items
		 WHERE invoice_id = ?
		 ORDER BY position ASC`, inv.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var li LineItem
		var ext sql.NullString
		var meta sql.NullString
		var priceID, productID sql.NullInt64
		if err := rows.Scan(&li.ID, &li.InvoiceID, &li.Position, &li.Description,
			&li.Quantity, &li.UnitPriceCents, &li.AmountCents, &li.TaxRateBps,
			&ext, &meta, &priceID, &productID); err != nil {
			rows.Close()
			return err
		}
		li.ExternalID = ext.String
		if meta.Valid {
			li.Metadata = json.RawMessage(meta.String)
		}
		if priceID.Valid {
			v := priceID.Int64
			li.PriceID = &v
		}
		if productID.Valid {
			v := productID.Int64
			li.ProductID = &v
		}
		inv.LineItems = append(inv.LineItems, li)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	pays, err := dbPaymentList(db, pid, paymentFilters{invoiceID: inv.ID, limit: 200})
	if err != nil {
		return err
	}
	inv.Payments = pays

	auditRows, err := db.Query(
		`SELECT id, invoice_id, actor, action, details, created_at
		 FROM invoice_audit_log
		 WHERE invoice_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 100`, inv.ID)
	if err != nil {
		return err
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var a AuditEntry
		var det sql.NullString
		if err := auditRows.Scan(&a.ID, &a.InvoiceID, &a.Actor, &a.Action, &det, &a.CreatedAt); err != nil {
			return err
		}
		if det.Valid {
			a.Details = json.RawMessage(det.String)
		}
		inv.AuditLog = append(inv.AuditLog, a)
	}
	return auditRows.Err()
}

func recomputeInvoiceTotalsTx(tx *sql.Tx, id int64) error {
	rows, err := tx.Query(
		`SELECT amount_cents, tax_rate_bps FROM invoice_line_items WHERE invoice_id = ?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var subtotal, tax int64
	for rows.Next() {
		var amount int64
		var bps int
		if err := rows.Scan(&amount, &bps); err != nil {
			return err
		}
		subtotal += amount
		tax += taxForAmount(amount, bps)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	total := subtotal + tax
	if total > maxMoney || total < -maxMoney {
		return errors.New("invoice total exceeds supported range")
	}
	_, err = tx.Exec(
		`UPDATE invoices
		 SET subtotal_cents = ?, tax_cents = ?, total_cents = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, subtotal, tax, total, id)
	return err
}

func writeAuditTx(tx *sql.Tx, invoiceID int64, actor, action string, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	if actor == "" {
		actor = "system"
	}
	_, err = tx.Exec(
		`INSERT INTO invoice_audit_log (invoice_id, actor, action, details)
		 VALUES (?, ?, ?, ?)`, invoiceID, actor, action, string(raw))
	return err
}
