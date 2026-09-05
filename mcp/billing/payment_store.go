package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func dbPaymentList(db *sql.DB, pid string, f paymentFilters) ([]*Payment, error) {
	if err := validateDateRange(f.since, f.until); err != nil {
		return nil, err
	}
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.customerID != 0 {
		where = append(where, "customer_id = ?")
		args = append(args, f.customerID)
	}
	if f.invoiceID != 0 {
		where = append(where, "invoice_id = ?")
		args = append(args, f.invoiceID)
	}
	if f.method != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToLower(f.method))
	}
	if f.since != "" {
		where = append(where, "julianday(received_at) >= julianday(?)")
		args = append(args, f.since)
	}
	if f.until != "" {
		where = append(where, "julianday(received_at) < julianday(?)")
		args = append(args, f.until)
	}
	limit := f.limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, min(limit, 1001), max(0, f.offset))
	rows, err := db.Query(
		`SELECT id, project_id, invoice_id, customer_id, amount_cents, currency,
		        method, external_id, received_at, notes, created_at
		 FROM payments
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY julianday(received_at) DESC, id DESC
		 LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		var p Payment
		var iid sql.NullInt64
		var ext, notes sql.NullString
		if err := rows.Scan(&p.ID, &p.ProjectID, &iid, &p.CustomerID, &p.AmountCents,
			&p.Currency, &p.Method, &ext, &p.ReceivedAt, &notes, &p.CreatedAt); err != nil {
			return nil, err
		}
		if iid.Valid {
			v := iid.Int64
			p.InvoiceID = &v
		}
		p.ExternalID = ext.String
		p.Notes = notes.String
		out = append(out, &p)
	}
	return out, rows.Err()
}

func dbPaymentGetByID(db *sql.DB, pid string, id int64) (*Payment, error) {
	row := db.QueryRow(
		`SELECT id, project_id, invoice_id, customer_id, amount_cents, currency,
		        method, external_id, received_at, notes, created_at
		 FROM payments WHERE id = ? AND project_id = ?`, id, pid)
	var p Payment
	var iid sql.NullInt64
	var ext, notes sql.NullString
	if err := row.Scan(&p.ID, &p.ProjectID, &iid, &p.CustomerID, &p.AmountCents,
		&p.Currency, &p.Method, &ext, &p.ReceivedAt, &notes, &p.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if iid.Valid {
		v := iid.Int64
		p.InvoiceID = &v
	}
	p.ExternalID = ext.String
	p.Notes = notes.String
	return &p, nil
}

// dbPaymentRecord inserts a non-Stripe payment and rolls forward the
// invoice's amount_paid_cents + status. Single Tx so the invoice
// state can never disagree with its payments.
func dbPaymentRecord(db *sql.DB, pid string, invID int64, amount int64,
	method, externalID, receivedAt, notes, actor string) (*Payment, *Invoice, error) {
	return dbPaymentRecordSource(db, pid, invID, amount, method, externalID, receivedAt, notes, actor, 0)
}

func dbPaymentRecordSource(db *sql.DB, pid string, invID, amount int64, method, externalID, receivedAt, notes, actor string, sourceID int64) (*Payment, *Invoice, error) {
	unlock := operationLock(fmt.Sprintf("ledger:%p:%s:%d", db, pid, invID))
	defer unlock()
	if amount == 0 || amount > maxMoney || amount < -maxMoney {
		return nil, nil, errors.New("payment amount must be nonzero and within supported range")
	}
	if err := validateReceivedAt(receivedAt); err != nil {
		return nil, nil, err
	}
	t, _ := parseBillingTime(receivedAt)
	receivedAt = t.Format(time.RFC3339Nano)
	// Idempotency for processor-driven writes (Stripe webhook, future
	// reconciler): when method='stripe' (or any non-empty external_id),
	// check the unique (method, external_id) index BEFORE the
	// transaction. If a payment already exists with the same key,
	// return it as a no-op rather than failing on constraint.
	if externalID != "" {
		var existingPayID, existingInvID int64
		var existingPID string
		if err := db.QueryRow(
			`SELECT id, project_id, COALESCE(invoice_id, 0)
			 FROM payments WHERE method = ? AND external_id = ?`,
			method, externalID).Scan(&existingPayID, &existingPID, &existingInvID); err == nil {
			if existingPID != pid || existingInvID != invID {
				return nil, nil, fmt.Errorf("external payment %q already belongs to another invoice", externalID)
			}
			// Re-deliver — return current state.
			pay, _ := dbPaymentGetByID(db, pid, existingPayID)
			inv, _ := dbInvoiceGetByID(db, pid, invID)
			if inv != nil {
				_ = loadInvoiceChildren(db, pid, inv)
			}
			if pay != nil {
				if pay.AmountCents != amount {
					return nil, nil, errors.New("external payment key reused with a different amount")
				}
				return pay, inv, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var (
		status, currency string
		cid              int64
		total, paid      int64
	)
	if err := tx.QueryRow(
		`SELECT customer_id, status, currency, total_cents, amount_paid_cents
		 FROM invoices
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		invID, pid).Scan(&cid, &status, &currency, &total, &paid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("invoice %d not found", invID)
		}
		return nil, nil, err
	}
	providerWrite := method == "stripe" && externalID != "" && strings.HasPrefix(actor, "system:stripe")
	if amount > 0 && status != "open" && status != "uncollectible" && !(providerWrite && (status == "paid" || status == "void")) {
		return nil, nil, fmt.Errorf("cannot record payment on %s invoice — only 'open' or 'uncollectible' accept payments", status)
	}
	if amount < 0 && status != "open" && status != "uncollectible" && status != "paid" && !(providerWrite && status == "void") {
		return nil, nil, fmt.Errorf("cannot record refund on %s invoice", status)
	}
	if amount < 0 && paid+amount < 0 {
		return nil, nil, fmt.Errorf("refund amount exceeds the invoice's recorded payments")
	}
	res, err := tx.Exec(
		`INSERT INTO payments (project_id, invoice_id, customer_id, amount_cents,
		                      currency, method, external_id, received_at, notes, source_payment_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,0))`,
		pid, invID, cid, amount, currency, method,
		nullStr(externalID), receivedAt, nullStr(notes), sourceID)
	if err != nil {
		return nil, nil, err
	}
	pid64, _ := res.LastInsertId()
	newPaid := paid + amount
	newStatus := status
	action := "partial_payment"
	if amount < 0 {
		action = "refund"
		if newPaid < total && status != "void" {
			newStatus = "open"
		}
	} else if newPaid >= total && total > 0 && status != "void" {
		newStatus = "paid"
		action = "paid"
	}
	result, err := tx.Exec(
		`UPDATE invoices
		 SET amount_paid_cents = ?,
		     status = ?,
		     paid_at = CASE
		       WHEN ? = 'paid' AND paid_at IS NULL THEN ?
		       WHEN ? <> 'paid' THEN NULL
		       ELSE paid_at
		     END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ? AND amount_paid_cents = ?`,
		newPaid, newStatus, newStatus, receivedAt, newStatus, invID, pid, paid)
	if err != nil {
		return nil, nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, nil, errors.New("invoice payment state changed concurrently; retry")
	}
	if err := recomputePaidAtTx(tx, invID, newStatus, total); err != nil {
		return nil, nil, err
	}
	if amount < 0 {
		if _, err := tx.Exec("UPDATE invoices SET collection_hold=1 WHERE id=?", invID); err != nil {
			return nil, nil, err
		}
	}
	topic := "payment.recorded"
	if amount < 0 {
		topic = "invoice.refunded"
	}
	if err := queueInvoiceEventTx(tx, invID, topic, fmt.Sprintf("payment:%d", pid64), pid64); err != nil {
		return nil, nil, err
	}
	if newStatus == "paid" && status != "paid" {
		if err := queueInvoiceEventTx(tx, invID, "invoice.paid", fmt.Sprintf("payment:%d:paid", pid64), pid64); err != nil {
			return nil, nil, err
		}
	}
	if err := writeAuditTx(tx, invID, actor, action, map[string]any{
		"payment_id":   pid64,
		"amount_cents": amount,
		"method":       method,
		"new_paid":     newPaid,
		"total":        total,
	}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	pay, err := dbPaymentGetByID(db, pid, pid64)
	if err != nil {
		return nil, nil, err
	}
	inv, err := dbInvoiceGetByID(db, pid, invID)
	if err != nil || inv == nil {
		return pay, inv, err
	}
	if err := loadInvoiceChildren(db, pid, inv); err != nil {
		return pay, inv, err
	}
	return pay, inv, nil
}
