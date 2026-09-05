package main

import (
	"database/sql"
	"encoding/json"
)

func scanCustomer(s rowScanner) (*Customer, error) {
	var c Customer
	var email, phone, currency, ext sql.NullString
	var addr, taxes, meta sql.NullString
	if err := s.Scan(
		&c.ID, &c.ProjectID, &c.Name, &email, &phone,
		&addr, &taxes, &currency, &ext, &meta,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Email = email.String
	c.Phone = phone.String
	c.Currency = currency.String
	c.ExternalID = ext.String
	if addr.Valid {
		c.BillingAddress = json.RawMessage(addr.String)
	}
	if taxes.Valid {
		c.TaxIDs = json.RawMessage(taxes.String)
	}
	if meta.Valid {
		c.Metadata = json.RawMessage(meta.String)
	}
	return &c, nil
}

// scanInvoice expects the SELECT to end with two extra LEFT-JOINed
// columns: customer name and customer email. They're populated via
// COALESCE in the query so missing/deleted customers scan as empty.
func scanInvoice(s rowScanner) (*Invoice, error) {
	var inv Invoice
	var number, accountingDate, dueDate, notes sql.NullString
	var ext, extURL, syncedAt sql.NullString
	var meta sql.NullString
	var finalizedAt, paidAt, voidedAt sql.NullString
	var custName, custEmail sql.NullString
	if err := s.Scan(
		&inv.ID, &inv.ProjectID, &inv.CustomerID, &inv.Provider, &number,
		&inv.Status, &inv.Currency, &inv.SubtotalCents, &inv.TaxCents,
		&inv.TotalCents, &inv.AmountPaidCents,
		&accountingDate, &dueDate, &notes, &ext, &extURL, &syncedAt, &meta,
		&finalizedAt, &paidAt, &voidedAt, &inv.CreatedAt, &inv.UpdatedAt,
		&custName, &custEmail, &inv.TaxTreatment, &inv.CollectionHold); err != nil {
		return nil, err
	}
	inv.Number = number.String
	inv.AccountingDate = accountingDate.String
	inv.DueDate = dueDate.String
	inv.Notes = notes.String
	inv.ExternalID = ext.String
	inv.ExternalURL = extURL.String
	inv.LastSyncedAt = syncedAt.String
	inv.FinalizedAt = finalizedAt.String
	inv.PaidAt = paidAt.String
	inv.VoidedAt = voidedAt.String
	if meta.Valid {
		inv.Metadata = json.RawMessage(meta.String)
	}
	inv.CustomerName = custName.String
	inv.CustomerEmail = custEmail.String
	inv.CreditCents = maxInt(inv.AmountPaidCents-inv.TotalCents, 0)
	return &inv, nil
}
