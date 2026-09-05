package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func dbCustomerSearch(db *sql.DB, pid, q, email string, limit int, offsets ...int) ([]*Customer, error) {
	var (
		where = []string{"project_id = ?", "deleted_at IS NULL"}
		args  = []any{pid}
	)
	if email != "" {
		where = append(where, "email = ?")
		args = append(args, normaliseEmail(email))
	}
	if q != "" {
		where = append(where, "(name LIKE ? OR email LIKE ?)")
		pat := "%" + q + "%"
		args = append(args, pat, pat)
	}
	offset := 0
	if len(offsets) > 0 {
		offset = max(0, offsets[0])
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 1001)
	args = append(args, limit, offset)
	sqlStr := `SELECT id, project_id, name, email, phone, billing_address, tax_ids,
	             currency, external_id, metadata, created_at, updated_at
	           FROM customers
	           WHERE ` + strings.Join(where, " AND ") + `
	           ORDER BY updated_at DESC,id DESC
	           LIMIT ? OFFSET ?`
	rows, err := db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Customer
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbCustomerGetByID(db *sql.DB, pid string, id int64) (*Customer, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, external_id, metadata, created_at, updated_at
		 FROM customers
		 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, pid)
	c, err := scanCustomer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbCustomerGetByEmail(db *sql.DB, pid, email string) (*Customer, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, email, phone, billing_address, tax_ids,
		        currency, external_id, metadata, created_at, updated_at
		 FROM customers
		 WHERE project_id = ? AND email = ? AND deleted_at IS NULL AND email_conflict=0
		 ORDER BY id DESC
		 LIMIT 1`, pid, email)
	c, err := scanCustomer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbCustomerUpsertByEmail(db *sql.DB, pid, email string, defaults map[string]any) (*Customer, bool, error) {
	existing, err := dbCustomerGetByEmail(db, pid, email)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	name := strArg(defaults, "name")
	if name == "" {
		name = email // fall back to email so the row has a non-empty name
	}
	addr := jsonOrEmpty(defaults["billing_address"], "{}")
	taxes := jsonOrEmpty(defaults["tax_ids"], "[]")
	meta := jsonOrEmpty(defaults["metadata"], "{}")
	now := nowRFC3339()
	res, err := db.Exec(
		`INSERT INTO customers (project_id, name, email, phone, billing_address, tax_ids,
		                       currency, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		pid, name, email, strArg(defaults, "phone"), addr, taxes,
		strArg(defaults, "currency"), meta, now, now)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	c, err := dbCustomerGetByEmail(db, pid, email)
	return c, n == 1, err
}

func dbCustomerUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Customer, error) {
	if err := validateInput(patch); err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	allowed := map[string]bool{
		"name": true, "email": true, "phone": true,
		"currency": true, "billing_address": true, "tax_ids": true, "metadata": true,
	}
	var (
		sets []string
		args []any
	)
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		switch k {
		case "email":
			sets = append(sets, "email_conflict=0")
			s, _ := v.(string)
			args = append(args, normaliseEmail(s))
		case "billing_address", "tax_ids", "metadata":
			args = append(args, jsonOrEmpty(v, ifThen(k == "tax_ids", "[]", "{}")))
		default:
			args = append(args, v)
		}
		sets = append(sets, k+" = ?")
	}
	if len(sets) == 0 {
		return dbCustomerGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE customers SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, args...); err != nil {
		return nil, err
	}
	return dbCustomerGetByID(db, pid, id)
}

func dbCustomerMerge(db *sql.DB, pid string, loser, winner int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Both sides must exist + not deleted.
	for _, id := range []int64{loser, winner} {
		var n int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM customers
			 WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
			id, pid).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("customer %d not found or deleted", id)
		}
	}
	if _, err := tx.Exec(
		`UPDATE invoices SET customer_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE payments SET customer_id = ?
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	var providerConflict int
	if err := tx.QueryRow(`SELECT count(*) FROM billing_customer_provider_ids l JOIN billing_customer_provider_ids w ON l.connection_id=w.connection_id WHERE l.customer_id=? AND w.customer_id=? AND l.provider_id<>w.provider_id`, loser, winner).Scan(&providerConflict); err != nil {
		return err
	}
	if providerConflict > 0 {
		return errors.New("customers have different identities on the same processor; reconcile provider customers before merging")
	}
	if _, err := tx.Exec(`UPDATE billing_customer_provider_ids SET customer_id=? WHERE customer_id=? AND connection_id NOT IN(SELECT connection_id FROM billing_customer_provider_ids WHERE customer_id=?)`, winner, loser, winner); err != nil {
		return err
	}
	// The winner may already have a default method; demote the loser's default
	// before reassignment to preserve the one-default-per-customer index.
	if _, err := tx.Exec(
		`UPDATE billing_payment_methods SET is_default = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE customer_id = ? AND project_id = ? AND is_default = 1 AND EXISTS(SELECT 1 FROM billing_payment_methods WHERE customer_id=? AND project_id=? AND is_default=1 AND status='active')`, loser, pid, winner, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE billing_payment_methods SET customer_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE billing_setup_sessions SET customer_id = ?
		 WHERE customer_id = ? AND project_id = ?`, winner, loser, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE customers SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, loser, pid); err != nil {
		return err
	}
	return tx.Commit()
}

func dbCustomerTotals(db *sql.DB, pid string, cid int64) (map[string]any, error) {
	rows, err := db.Query(`SELECT currency,count(*),COALESCE(sum(total_cents),0),COALESCE(sum(amount_paid_cents),0),COALESCE(sum(max(total_cents-amount_paid_cents,0)),0),COALESCE(sum(max(amount_paid_cents-total_cents,0)),0) FROM invoices WHERE project_id=? AND customer_id=? AND deleted_at IS NULL AND status IN ('open','paid','uncollectible') GROUP BY currency ORDER BY currency`, pid, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []map[string]any{}
	count := 0
	for rows.Next() {
		var cur string
		var n int
		var total, paid, due, credit int64
		if err = rows.Scan(&cur, &n, &total, &paid, &due, &credit); err != nil {
			return nil, err
		}
		count += n
		groups = append(groups, map[string]any{"currency": cur, "invoice_count": n, "invoiced_cents": total, "paid_cents": paid, "outstanding_cents": due, "credit_cents": credit})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]any{"by_currency": groups, "invoice_count": count, "mixed_currencies": len(groups) > 1}
	if len(groups) == 1 {
		for k, v := range groups[0] {
			out[k] = v
		}
	} else if len(groups) == 0 {
		out["invoiced_cents"] = int64(0)
		out["paid_cents"] = int64(0)
		out["outstanding_cents"] = int64(0)
	}
	return out, nil
}
