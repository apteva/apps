package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sequenceToken = regexp.MustCompile(`\{seq(?::([0-9]{1,2}))?\}`)

func mintInvoiceNumberTx(tx *sql.Tx, pid, format string, seqStart int64) (string, error) {
	return mintInvoiceNumberAtTx(tx, pid, format, seqStart, time.Now().UTC())
}

func mintInvoiceNumberAtTx(tx *sql.Tx, pid, format string, seqStart int64, now time.Time) (string, error) {
	if strings.TrimSpace(format) == "" {
		format = "INV-{yyyy}-{seq:04}"
	}
	if len(format) > 120 || len(sequenceToken.FindAllString(format, -1)) != 1 {
		return "", errors.New("invoice number format requires exactly one {seq} token")
	}
	if width := sequenceToken.FindStringSubmatch(format)[1]; width != "" {
		n, _ := strconv.Atoi(width)
		if n < 1 || n > 12 {
			return "", errors.New("sequence width must be between 1 and 12")
		}
	}
	stripped := sequenceToken.ReplaceAllString(format, "")
	for _, token := range []string{"{yyyy}", "{yy}", "{mm}", "{dd}"} {
		stripped = strings.ReplaceAll(stripped, token, "")
	}
	if strings.ContainsAny(stripped, "{}\r\n\"") {
		return "", errors.New("invalid invoice number format")
	}
	if seqStart < 1 {
		seqStart = 1001
	}
	if seqStart > 999999999999 {
		return "", errors.New("sequence start too large")
	}
	now = now.UTC()
	series := format
	annual := strings.Contains(format, "{yyyy}") || strings.Contains(format, "{yy}")
	if annual {
		series += fmt.Sprintf(":%d", now.Year())
	}
	var present int
	if err := tx.QueryRow("SELECT count(*) FROM billing_invoice_sequences WHERE project_id=? AND series=?", pid, series).Scan(&present); err != nil {
		return "", err
	}
	if present == 0 {
		var count int64
		if err := tx.QueryRow(`SELECT count(*) FROM invoices WHERE project_id=? AND number IS NOT NULL AND (?=0 OR strftime('%Y',finalized_at)=?)`, pid, boolInt(annual), now.Format("2006")).Scan(&count); err != nil {
			return "", err
		}
		if _, err := tx.Exec(`INSERT INTO billing_invoice_sequences(project_id,series,next_value) VALUES(?,?,?) ON CONFLICT(project_id,series) DO NOTHING`, pid, series, seqStart+count); err != nil {
			return "", err
		}
	}
	base := strings.NewReplacer("{yyyy}", now.Format("2006"), "{yy}", now.Format("06"), "{mm}", now.Format("01"), "{dd}", now.Format("02")).Replace(format)
	for tries := 0; tries < 100000; tries++ {
		var seq int64
		if err := tx.QueryRow(`UPDATE billing_invoice_sequences SET next_value=max(next_value,?)+1 WHERE project_id=? AND series=? RETURNING next_value-1`, seqStart, pid, series).Scan(&seq); err != nil {
			return "", err
		}
		num := renderSeqToken(base, seq)
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM invoices WHERE project_id=? AND number=?", pid, num).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return num, nil
		}
	}
	return "", errors.New("number series exhausted; choose a new format")
}
