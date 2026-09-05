package main

import (
	"database/sql"
	"encoding/json"
	"os"
)

func captureSnapshotTx(tx *sql.Tx, id int64, provenance string) error {
	var pid, cust, doc string
	if err := tx.QueryRow(`SELECT i.project_id,json_object('id',c.id,'project_id',c.project_id,'name',c.name,'email',c.email,'phone',c.phone,'billing_address',json(c.billing_address),'tax_ids',json(c.tax_ids),'currency',c.currency),json_object('notes',i.notes,'due_date',i.due_date,'tax_treatment',i.tax_treatment) FROM invoices i JOIN customers c ON c.id=i.customer_id WHERE i.id=?`, id).Scan(&pid, &cust, &doc); err != nil {
		return err
	}
	var issuer string
	err := tx.QueryRow(`SELECT json_object('configured',json('true'),'display_name',display_name,'legal_name',legal_name,'email',email,'phone',phone,'address',json(address),'tax_ids',json(tax_ids),'bank',json(bank),'footer_text',footer_text,'default_terms',default_terms) FROM issuer_settings WHERE project_id=? OR (project_id='' AND ?<>'') ORDER BY CASE WHEN project_id=? THEN 0 ELSE 1 END LIMIT 1`, pid, os.Getenv("APTEVA_PROJECT_ID"), pid).Scan(&issuer)
	if err == sql.ErrNoRows {
		issuer = "{}"
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO billing_invoice_snapshots(invoice_id,customer_json,issuer_json,document_json,provenance) VALUES(?,?,?,?,?)`, id, cust, issuer, doc, provenance)
	return err
}
func backfillInvoiceSnapshots(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM invoices WHERE status<>'draft' AND id NOT IN (SELECT invoice_id FROM billing_invoice_snapshots)`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		e = captureSnapshotTx(tx, id, "migration-current-identity")
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}
func loadIssuedInvoiceForRender(db *sql.DB, pid string, id int64) (*Invoice, *Customer, *Issuer, error) {
	inv, cust, err := loadInvoiceForRender(db, pid, id)
	if err != nil || inv == nil {
		return inv, cust, nil, err
	}
	var cr, ir, dr string
	err = db.QueryRow(`SELECT customer_json,issuer_json,document_json FROM billing_invoice_snapshots WHERE invoice_id=?`, id).Scan(&cr, &ir, &dr)
	if err == sql.ErrNoRows {
		iss, e := dbIssuerGet(db, pid)
		return inv, cust, iss, e
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var c Customer
	var iss Issuer
	var d struct {
		Notes        string `json:"notes"`
		DueDate      string `json:"due_date"`
		TaxTreatment string `json:"tax_treatment"`
	}
	for raw, out := range map[string]any{cr: &c, ir: &iss, dr: &d} {
		if e := json.Unmarshal([]byte(raw), out); e != nil {
			return nil, nil, nil, e
		}
	}
	inv.Notes = d.Notes
	inv.DueDate = d.DueDate
	inv.TaxTreatment = d.TaxTreatment
	return inv, &c, &iss, nil
}
