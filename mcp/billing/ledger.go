package main

import "database/sql"

// Settlement is when the chronological running balance last becomes covered.
// Backdated inserts must not move settlement before a later necessary payment.
func recomputePaidAtTx(tx *sql.Tx, id int64, status string, total int64) error {
	if status != "paid" {
		_, err := tx.Exec("UPDATE invoices SET paid_at=NULL WHERE id=?", id)
		return err
	}
	rows, err := tx.Query("SELECT amount_cents,received_at FROM payments WHERE invoice_id=? ORDER BY julianday(received_at),id", id)
	if err != nil {
		return err
	}
	var balance int64
	settled := ""
	for rows.Next() {
		var amount int64
		var received string
		if err = rows.Scan(&amount, &received); err != nil {
			rows.Close()
			return err
		}
		before := balance
		balance += amount
		if before < total && balance >= total {
			settled = received
		}
		if balance < total {
			settled = ""
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE invoices SET paid_at=NULLIF(?,'') WHERE id=?", settled, id)
	return err
}

func providerReceivedAt(seconds int64)string { if seconds>0{return timestampFromUnix(seconds)};return nowRFC3339() }
