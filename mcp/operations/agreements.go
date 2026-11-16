package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ─── recurring work ─────────────────────────────────────────────────
//
// An agreement generates work on a schedule: a weekly home clean, a
// yearly boiler service, a restaurant's daily prep run or opening
// checklist. With auto_book the engine books each occurrence at its
// time, preferring the same resources.

type Recurrence struct {
	Every    int    `json:"every"`              // default 1
	Unit     string `json:"unit"`               // day | week | month
	Weekdays []int  `json:"weekdays,omitempty"` // 1=Mon … 7=Sun (unit week)
	Time     string `json:"time"`               // HH:MM site local
}

type Agreement struct {
	ID                   int64          `json:"id"`
	Name                 string         `json:"name"`
	BrandID              int64          `json:"brand_id"`
	Brand                string         `json:"brand,omitempty"`
	WorkTypeID           int64          `json:"work_type_id"`
	WorkType             string         `json:"work_type"`
	CRMContactID         *int64         `json:"crm_contact_id,omitempty"`
	CustomerName         string         `json:"customer_name"`
	CustomerEmail        string         `json:"customer_email"`
	CustomerPhone        string         `json:"customer_phone"`
	SiteID               *int64         `json:"site_id"`
	Recurrence           Recurrence     `json:"recurrence"`
	Items                []ItemRequest  `json:"items"`
	Answers              map[string]any `json:"answers"`
	PreferredResourceIDs []int64        `json:"preferred_resource_ids"`
	AutoBook             bool           `json:"auto_book"`
	StartsOn             string         `json:"starts_on"`
	EndsOn               string         `json:"ends_on,omitempty"`
	NextOn               string         `json:"next_on"`
	Status               string         `json:"status"`
}

const agreementCols = `a.id,a.name,a.work_type_id,t.key,a.crm_contact_id,a.customer_name,a.customer_email,a.customer_phone,a.site_id,
	a.recurrence_json,a.items_json,a.answers_json,a.preferred_resource_ids_json,a.auto_book,a.starts_on,a.ends_on,a.next_on,a.status,
	a.brand_id,COALESCE((SELECT key FROM brands b WHERE b.id=a.brand_id),'')`

func scanAgreement(s scanner) (*Agreement, error) {
	var a Agreement
	var crm, site sql.NullInt64
	var rec, items, answers, pref string
	var auto int
	if err := s.Scan(&a.ID, &a.Name, &a.WorkTypeID, &a.WorkType, &crm, &a.CustomerName, &a.CustomerEmail, &a.CustomerPhone, &site,
		&rec, &items, &answers, &pref, &auto, &a.StartsOn, &a.EndsOn, &a.NextOn, &a.Status, &a.BrandID, &a.Brand); err != nil {
		return nil, err
	}
	if crm.Valid {
		a.CRMContactID = &crm.Int64
	}
	if site.Valid {
		a.SiteID = &site.Int64
	}
	decodeInto(rec, &a.Recurrence)
	a.Items, a.Answers, a.PreferredResourceIDs = []ItemRequest{}, map[string]any{}, []int64{}
	decodeInto(items, &a.Items)
	decodeInto(answers, &a.Answers)
	decodeInto(pref, &a.PreferredResourceIDs)
	a.AutoBook = auto == 1
	return &a, nil
}

func getAgreement(db *sql.DB, pid string, id int64) (*Agreement, error) {
	return scanAgreement(db.QueryRow(`SELECT `+agreementCols+` FROM agreements a JOIN work_types t ON t.id=a.work_type_id WHERE a.project_id=? AND a.id=?`, pid, id))
}

func listAgreements(db *sql.DB, pid, status string) ([]*Agreement, error) {
	q := `SELECT ` + agreementCols + ` FROM agreements a JOIN work_types t ON t.id=a.work_type_id WHERE a.project_id=?`
	params := []any{pid}
	if status != "" {
		q += " AND a.status=?"
		params = append(params, status)
	}
	rows, err := db.Query(q+" ORDER BY a.next_on", params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Agreement{}
	for rows.Next() {
		a, err := scanAgreement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func validateRecurrence(r *Recurrence) error {
	if r.Every <= 0 {
		r.Every = 1
	}
	if r.Unit != "day" && r.Unit != "week" && r.Unit != "month" {
		return errors.New("recurrence.unit must be day, week or month")
	}
	for _, d := range r.Weekdays {
		if d < 1 || d > 7 {
			return errors.New("recurrence.weekdays use 1=Mon … 7=Sun")
		}
	}
	if r.Time == "" {
		r.Time = "09:00"
	}
	if _, err := hhmm(r.Time); err != nil {
		return fmt.Errorf("recurrence.time: %w", err)
	}
	return nil
}

func createAgreement(db *sql.DB, pid string, args map[string]any) (*Agreement, error) {
	brandID, wt, err := resolveWorkBrand(db, pid, args, stringArg(args, "work_type"), int64Arg(args, "site_id"))
	if err != nil {
		return nil, err
	}
	var rec Recurrence
	if err := remarshal(objectArg(args, "recurrence"), &rec); err != nil {
		return nil, fmt.Errorf("recurrence: %w", err)
	}
	if err := validateRecurrence(&rec); err != nil {
		return nil, err
	}
	starts := stringArg(args, "starts_on")
	if starts == "" {
		starts = clock().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", starts); err != nil {
		return nil, errors.New("starts_on must be YYYY-MM-DD")
	}
	siteID := int64Arg(args, "site_id")
	if siteID > 0 {
		if _, err := getSite(db, pid, siteID); err != nil {
			return nil, notFound(err, "site")
		}
	}
	if wt.Mode == "at_customer" && siteID == 0 {
		return nil, errors.New("at_customer agreements need site_id")
	}
	items := parseItems(args["items"])
	for _, it := range items {
		if _, err := getRateItem(db, pid, 0, it.Code, brandID); err != nil {
			return nil, notFound(err, "rate item "+it.Code)
		}
	}
	first := firstOccurrence(starts, rec)
	customer := objectArg(args, "customer")
	if customer == nil {
		customer = map[string]any{}
	}
	now := nowUTC()
	r, err := db.Exec(`INSERT INTO agreements(project_id,brand_id,name,work_type_id,crm_contact_id,customer_name,customer_email,customer_phone,site_id,
		recurrence_json,items_json,answers_json,preferred_resource_ids_json,auto_book,starts_on,ends_on,next_on,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,?)`,
		pid, brandID, firstNonEmpty(stringArg(args, "name"), wt.Name), wt.ID, nilIfZero(int64Arg(customer, "crm_contact_id")), stringArg(customer, "name"),
		stringArg(customer, "email"), stringArg(customer, "phone"), nilIfZero(siteID), mustJSON(rec), jsonOr(items, "[]"),
		jsonOr(objectArg(args, "answers"), "{}"), jsonOr(int64sArg(args, "preferred_resource_ids"), "[]"), boolInt(boolArg(args, "auto_book", true)),
		starts, stringArg(args, "ends_on"), first, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	return getAgreement(db, pid, id)
}

func weekdayISO(t time.Time) int {
	d := int(t.Weekday())
	if d == 0 {
		return 7
	}
	return d
}

func firstOccurrence(starts string, r Recurrence) string {
	d, _ := time.Parse("2006-01-02", starts)
	if r.Unit == "week" && len(r.Weekdays) > 0 {
		for i := 0; i < 7; i++ {
			c := d.AddDate(0, 0, i)
			for _, wd := range r.Weekdays {
				if weekdayISO(c) == wd {
					return c.Format("2006-01-02")
				}
			}
		}
	}
	return d.Format("2006-01-02")
}

func nextOccurrence(cur string, r Recurrence) string {
	d, _ := time.Parse("2006-01-02", cur)
	switch r.Unit {
	case "day":
		return d.AddDate(0, 0, r.Every).Format("2006-01-02")
	case "month":
		return d.AddDate(0, r.Every, 0).Format("2006-01-02")
	}
	if len(r.Weekdays) == 0 {
		return d.AddDate(0, 0, 7*r.Every).Format("2006-01-02")
	}
	// Next listed weekday in a week that is a multiple of Every weeks
	// after the current one.
	monday := func(t time.Time) time.Time { return t.AddDate(0, 0, -(weekdayISO(t) - 1)) }
	base := monday(d)
	for i := 1; i <= 7*r.Every+7; i++ {
		c := d.AddDate(0, 0, i)
		weeks := int(monday(c).Sub(base).Hours() / (24 * 7))
		if weeks%r.Every != 0 {
			continue
		}
		for _, wd := range r.Weekdays {
			if weekdayISO(c) == wd {
				return c.Format("2006-01-02")
			}
		}
	}
	return d.AddDate(0, 0, 7*r.Every).Format("2006-01-02")
}

type GeneratedOccurrence struct {
	AgreementID int64  `json:"agreement_id"`
	Date        string `json:"date"`
	WorkID      int64  `json:"work_id"`
	Number      string `json:"number"`
	SlotID      int64  `json:"slot_id,omitempty"`
	BookError   string `json:"book_error,omitempty"`
}

// generateAgreements creates the work for every occurrence due up to
// `until` (inclusive). Re-running it is safe: next_on only moves forward.
func generateAgreements(db *sql.DB, pid string, until string, onlyID int64) ([]GeneratedOccurrence, error) {
	if _, err := time.Parse("2006-01-02", until); err != nil {
		return nil, errors.New("until must be YYYY-MM-DD")
	}
	list, err := listAgreements(db, pid, "active")
	if err != nil {
		return nil, err
	}
	archived := map[int64]bool{}
	if bs, err := listBrands(db, pid, "archived"); err == nil {
		for _, b := range bs {
			archived[b.ID] = true
		}
	}
	out := []GeneratedOccurrence{}
	for _, a := range list {
		if onlyID > 0 && a.ID != onlyID {
			continue
		}
		if archived[a.BrandID] {
			continue // an archived brand takes no new work
		}
		guard := 0
		for a.NextOn <= until && guard < 400 {
			guard++
			if a.EndsOn != "" && a.NextOn > a.EndsOn {
				_, _ = db.Exec(`UPDATE agreements SET status='ended', updated_at=? WHERE id=?`, nowUTC(), a.ID)
				break
			}
			occ, err := generateOne(db, pid, a)
			if err != nil {
				return out, fmt.Errorf("agreement %d on %s: %w", a.ID, a.NextOn, err)
			}
			out = append(out, occ)
			a.NextOn = nextOccurrence(a.NextOn, a.Recurrence)
			if _, err := db.Exec(`UPDATE agreements SET next_on=?, updated_at=? WHERE id=?`, a.NextOn, nowUTC(), a.ID); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func generateOne(db *sql.DB, pid string, a *Agreement) (GeneratedOccurrence, error) {
	occ := GeneratedOccurrence{AgreementID: a.ID, Date: a.NextOn}
	wt, err := getWorkType(db, pid, a.WorkTypeID, "")
	if err != nil {
		return occ, err
	}
	items := []any{}
	for _, it := range a.Items {
		m := map[string]any{"code": it.Code}
		if it.Qty != nil {
			m["qty"] = *it.Qty
		}
		items = append(items, m)
	}
	args := map[string]any{"work_type": wt.Key, "brand_id": float64(a.BrandID), "source": "agreement", "agreement_id": float64(a.ID), "answers": a.Answers,
		"summary": a.Name + " " + a.NextOn, "items": items, "actor": "agreement",
		"customer": map[string]any{"name": a.CustomerName, "email": a.CustomerEmail, "phone": a.CustomerPhone}}
	if a.CRMContactID != nil {
		args["customer"].(map[string]any)["crm_contact_id"] = float64(*a.CRMContactID)
	}
	if a.SiteID != nil {
		args["site_id"] = float64(*a.SiteID)
	}
	if ps := intArg(a.Answers, "party_size", 0); ps > 0 {
		args["party_size"] = float64(ps)
	}
	res, err := intakeWork(db, pid, nil, args)
	if err != nil {
		return occ, err
	}
	w := res.Work
	occ.WorkID, occ.Number = w.ID, w.Number
	// Agreement prices are agreed once: accept the estimate up front.
	if wt.usesPhase("quote") && w.QuoteStatus != "needs_owner" {
		if _, _, err := quoteDecision(db, pid, w, "accept", "agreement", "agreement"); err == nil {
			w, _ = getWork(db, pid, w.ID)
		}
	}
	if !a.AutoBook {
		return occ, nil
	}
	loc := time.UTC
	if a.SiteID != nil {
		if s, err := getSite(db, pid, *a.SiteID); err == nil {
			loc = loadLocation(s.Timezone)
		}
	}
	day, _ := time.Parse("2006-01-02", a.NextOn)
	m, _ := hhmm(a.Recurrence.Time)
	start := time.Date(day.Year(), day.Month(), day.Day(), m/60, m%60, 0, 0, loc)
	prefs := []any{}
	for _, id := range a.PreferredResourceIDs {
		prefs = append(prefs, float64(id))
	}
	br, err := bookSlot(db, pid, w, map[string]any{"starts_at": fmtTime(start), "preferred_resource_ids": prefs, "actor": "agreement"})
	if err != nil {
		occ.BookError = err.Error()
		return occ, nil
	}
	occ.SlotID = br.Slot.ID
	return occ, nil
}
