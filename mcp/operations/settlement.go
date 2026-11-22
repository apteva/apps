package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─── settlement ─────────────────────────────────────────────────────
//
// How the money flows when a contractor (a resource: a person, a crew,
// a partner company) does the work.
//
//   resale      the brand invoices the client and pays the contractor.
//               The default, and how every job worked before 0.4.0.
//   commission  the contractor invoices the client directly; the brand
//               invoices the contractor a commission per job.
//
// Terms live on the resource, with optional per-brand overrides. When a
// contractor is assigned to a job (explicitly, or by booking it) the
// terms are copied onto the job, so changing them later never touches
// jobs already assigned or done.

const (
	modeResale     = "resale"
	modeCommission = "commission"
)

type Settlement struct {
	Mode                 string  `json:"mode"`
	CommissionPct        float64 `json:"commission_pct,omitempty"`
	CommissionFixedCents int64   `json:"commission_fixed_cents,omitempty"`
	CommissionBase       string  `json:"commission_base,omitempty"` // net (default) | gross
	InvoiceCadence       string  `json:"invoice_cadence,omitempty"` // per_job (default) | monthly
	BillingEmail         string  `json:"billing_email,omitempty"`   // where commission invoices go
	BillingName          string  `json:"billing_name,omitempty"`    // legal name on those invoices
}

func (s Settlement) normalized() Settlement {
	if s.Mode == "" {
		s.Mode = modeResale
	}
	if s.Mode == modeCommission {
		if s.CommissionBase == "" {
			s.CommissionBase = "net"
		}
		if s.InvoiceCadence == "" {
			s.InvoiceCadence = "per_job"
		}
	}
	return s
}

func parseSettlement(v any) (Settlement, error) {
	var s Settlement
	if v == nil {
		return Settlement{Mode: modeResale}, nil
	}
	if err := remarshal(v, &s); err != nil {
		return s, fmt.Errorf("settlement: %w", err)
	}
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	s.CommissionBase = strings.ToLower(strings.TrimSpace(s.CommissionBase))
	s.InvoiceCadence = strings.ToLower(strings.TrimSpace(s.InvoiceCadence))
	s.BillingEmail = strings.TrimSpace(s.BillingEmail)
	s.BillingName = strings.TrimSpace(s.BillingName)
	s = s.normalized()
	switch s.Mode {
	case modeResale:
		return Settlement{Mode: modeResale}, nil
	case modeCommission:
	default:
		return s, errors.New("settlement mode must be resale or commission")
	}
	if s.CommissionPct < 0 || s.CommissionPct > 100 {
		return s, errors.New("commission_pct must be between 0 and 100")
	}
	if s.CommissionFixedCents < 0 {
		return s, errors.New("commission_fixed_cents must be >= 0")
	}
	if s.CommissionPct == 0 && s.CommissionFixedCents == 0 {
		return s, errors.New("commission terms need commission_pct, commission_fixed_cents or both")
	}
	if s.CommissionBase != "net" && s.CommissionBase != "gross" {
		return s, errors.New("commission_base must be net or gross")
	}
	if s.InvoiceCadence != "per_job" && s.InvoiceCadence != "monthly" {
		return s, errors.New("invoice_cadence must be per_job or monthly")
	}
	if s.BillingEmail != "" && !strings.Contains(s.BillingEmail, "@") {
		return s, errors.New("billing_email is not an email address")
	}
	return s, nil
}

// commissionAmount: percentage of the base plus the fixed fee.
func commissionAmount(s Settlement, base int64) int64 {
	return int64(math.Round(float64(base)*s.CommissionPct/100)) + s.CommissionFixedCents
}

// termsFor gives the resource's terms for a brand: the brand override,
// else the default.
func (r *Resource) termsFor(brandID int64) Settlement {
	if brandID > 0 {
		if s, ok := r.settlementByBrandID[brandID]; ok {
			return s.normalized()
		}
	}
	return r.Settlement.normalized()
}

// decodeResourceSettlement fills the resource's terms from the stored JSON.
func decodeResourceSettlement(r *Resource, raw, byBrand string) {
	r.Settlement = Settlement{}
	decodeInto(raw, &r.Settlement)
	r.Settlement = r.Settlement.normalized()
	r.settlementByBrandID = map[int64]Settlement{}
	m := map[string]Settlement{}
	decodeInto(byBrand, &m)
	for k, v := range m {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil && id > 0 {
			r.settlementByBrandID[id] = v.normalized()
		}
	}
}

func encodeByBrand(m map[int64]Settlement) string {
	out := map[string]Settlement{}
	for id, s := range m {
		out[strconv.FormatInt(id, 10)] = s
	}
	return mustJSON(out)
}

// applySettlementArgs reads settlement / settlement_by_brand from tool
// args. settlement_by_brand replaces the overrides: {brand_key: terms},
// a null value removes that brand's override.
func applySettlementArgs(db *sql.DB, pid string, r *Resource, args map[string]any) error {
	if r.settlementByBrandID == nil {
		r.settlementByBrandID = map[int64]Settlement{}
	}
	if hasArg(args, "settlement") {
		s, err := parseSettlement(args["settlement"])
		if err != nil {
			return err
		}
		r.Settlement = s
	}
	if hasArg(args, "settlement_by_brand") {
		raw, ok := args["settlement_by_brand"].(map[string]any)
		if args["settlement_by_brand"] != nil && !ok {
			return errors.New("settlement_by_brand must be an object {brand_key: terms}")
		}
		next := map[int64]Settlement{}
		for k, v := range r.settlementByBrandID {
			next[k] = v
		}
		if args["settlement_by_brand"] == nil {
			next = map[int64]Settlement{}
		}
		for key, v := range raw {
			b, err := getBrandByKey(db, pid, key)
			if err != nil {
				return notFound(err, "brand "+key)
			}
			if v == nil {
				delete(next, b.ID)
				continue
			}
			s, err := parseSettlement(v)
			if err != nil {
				return fmt.Errorf("brand %s: %w", key, err)
			}
			next[b.ID] = s
		}
		r.settlementByBrandID = next
	}
	return nil
}

// withBrandKeys exposes per-brand terms keyed by brand key for tools.
func withBrandKeys(db *sql.DB, pid string, rs ...*Resource) {
	var keys map[int64]string
	for _, r := range rs {
		if r == nil || len(r.settlementByBrandID) == 0 {
			continue
		}
		if keys == nil {
			keys = map[int64]string{}
			if bs, err := listBrands(db, pid, ""); err == nil {
				for _, b := range bs {
					keys[b.ID] = b.Key
				}
			}
		}
		r.SettlementByBrand = map[string]Settlement{}
		for id, s := range r.settlementByBrandID {
			k := keys[id]
			if k == "" {
				k = strconv.FormatInt(id, 10)
			}
			r.SettlementByBrand[k] = s
		}
	}
}

// ─── the contractor on a job ────────────────────────────────────────

type PartnerInvoice struct {
	Ref                   string `json:"ref,omitempty"`
	NetCents              int64  `json:"net_cents,omitempty"`
	GrossCents            int64  `json:"gross_cents,omitempty"`
	IssuedBy              string `json:"issued_by,omitempty"`
	IssuedOn              string `json:"issued_on,omitempty"`
	RecordedAt            string `json:"recorded_at,omitempty"`
	ClientPaidAt          string `json:"client_paid_at,omitempty"`
	ClientPaidConfirmedBy string `json:"client_paid_confirmed_by,omitempty"`
}

func (p *PartnerInvoice) empty() bool { return p == nil || p.Ref == "" }

func workDone(w *Work) bool {
	return w.Phase == "bill" || w.Phase == "closed" || w.Phase == statusCancelled
}

// assignPartner puts a contractor on the job and copies its terms. An
// accepted quote keeps its price; the event says so.
func assignPartner(db *sql.DB, pid string, w *Work, resourceID int64, actor, reason string) (*Work, error) {
	if w.PartnerResourceID != nil && *w.PartnerResourceID == resourceID {
		return w, nil
	}
	if workDone(w) {
		return nil, fmt.Errorf("work is %s; its contractor and terms are fixed", w.Status)
	}
	r, err := getResource(db, pid, resourceID)
	if err != nil {
		return nil, notFound(err, "resource")
	}
	if !r.worksFor(w.BrandID) {
		return nil, fmt.Errorf("%s does not work for this brand", r.Name)
	}
	terms := r.termsFor(w.BrandID)
	if _, err := db.Exec(`UPDATE work SET partner_resource_id=?, settlement_mode=?, settlement_json=?, updated_at=? WHERE project_id=? AND id=?`,
		resourceID, terms.Mode, mustJSON(terms), nowUTC(), pid, w.ID); err != nil {
		return nil, err
	}
	payload := map[string]any{"resource_id": resourceID, "name": r.Name, "mode": terms.Mode, "terms": terms}
	if reason != "" {
		payload["reason"] = reason
	}
	if w.PartnerResourceID != nil {
		payload["previous_resource_id"] = *w.PartnerResourceID
		if w.QuoteStatus == "accepted" {
			payload["quote_kept"] = true
		}
	}
	_ = logEvent(db, pid, w.ID, actor, "partner_assigned", w.SettlementMode, terms.Mode, payload)
	nw, err := getWork(db, pid, w.ID)
	if err != nil {
		return nil, err
	}
	*w = *nw
	return nw, nil
}

// leadResource picks whose terms apply among the booked resources: the
// job's contractor if booked, else a resource booked in the "lead" role,
// else the first crew, else the first person.
func leadResource(w *Work, rs []SlotResource) int64 {
	if len(rs) == 0 {
		return 0
	}
	if w.PartnerResourceID != nil {
		for _, r := range rs {
			if r.ID == *w.PartnerResourceID {
				return r.ID
			}
		}
	}
	for _, r := range rs {
		if r.Role == "lead" {
			return r.ID
		}
	}
	for _, kind := range []string{"crew", "person"} {
		for _, r := range rs {
			if r.Kind == kind {
				return r.ID
			}
		}
	}
	return 0
}

// assignFromBooking re-points the job at whoever was booked.
func assignFromBooking(db *sql.DB, pid string, w *Work, rs []SlotResource, actor, reason string) {
	lead := leadResource(w, rs)
	if lead == 0 || (w.PartnerResourceID != nil && *w.PartnerResourceID == lead) {
		return
	}
	_, _ = assignPartner(db, pid, w, lead, actor, reason)
}

// ─── commission lines ───────────────────────────────────────────────

type CommissionLine struct {
	ID          int64   `json:"id"`
	WorkID      int64   `json:"work_id"`
	WorkNumber  string  `json:"work_number,omitempty"`
	BrandID     int64   `json:"brand_id"`
	Brand       string  `json:"brand,omitempty"`
	ResourceID  int64   `json:"resource_id"`
	Partner     string  `json:"partner,omitempty"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	BaseCents   int64   `json:"base_cents"`
	BaseSource  string  `json:"base_source"`
	RatePct     float64 `json:"rate_pct"`
	FixedCents  int64   `json:"fixed_cents"`
	AmountCents int64   `json:"amount_cents"`
	Currency    string  `json:"currency"`
	Cadence     string  `json:"cadence"`
	InvoiceRef  string  `json:"invoice_ref,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	CreatedAt   string  `json:"created_at"`
	InvoicedAt  string  `json:"invoiced_at,omitempty"`
	PaidAt      string  `json:"paid_at,omitempty"`
	VoidedAt    string  `json:"voided_at,omitempty"`
}

const commissionCols = `c.id,c.work_id,w.number,c.brand_id,COALESCE(b.key,''),c.resource_id,COALESCE(r.name,''),c.kind,c.status,
	c.base_cents,c.base_source,c.rate_pct,c.fixed_cents,c.amount_cents,c.currency,c.cadence,c.invoice_ref,c.reason,c.created_at,
	c.invoiced_at,c.paid_at,c.voided_at`

const commissionFrom = ` FROM commission_lines c JOIN work w ON w.id=c.work_id LEFT JOIN brands b ON b.id=c.brand_id
	LEFT JOIN resources r ON r.id=c.resource_id`

func scanCommission(s scanner) (*CommissionLine, error) {
	var c CommissionLine
	var inv, paid, void sql.NullString
	if err := s.Scan(&c.ID, &c.WorkID, &c.WorkNumber, &c.BrandID, &c.Brand, &c.ResourceID, &c.Partner, &c.Kind, &c.Status,
		&c.BaseCents, &c.BaseSource, &c.RatePct, &c.FixedCents, &c.AmountCents, &c.Currency, &c.Cadence, &c.InvoiceRef, &c.Reason,
		&c.CreatedAt, &inv, &paid, &void); err != nil {
		return nil, err
	}
	c.InvoicedAt, c.PaidAt, c.VoidedAt = inv.String, paid.String, void.String
	if strings.HasPrefix(c.InvoiceRef, "pending:") {
		c.InvoiceRef = "" // being invoiced right now
	}
	return &c, nil
}

func queryCommissions(db *sql.DB, q string, args ...any) ([]*CommissionLine, error) {
	rows, err := db.Query(`SELECT `+commissionCols+commissionFrom+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CommissionLine{}
	for rows.Next() {
		c, err := scanCommission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func workCommissions(db *sql.DB, pid string, workID int64) ([]*CommissionLine, error) {
	return queryCommissions(db, ` WHERE c.project_id=? AND c.work_id=? ORDER BY c.id`, pid, workID)
}

// commissionBase picks what the commission is computed on: the
// contractor's invoice to the client (net or gross, per the terms),
// else the job's actual lines, else its estimate.
func commissionBase(db *sql.DB, w *Work) (int64, string) {
	s := w.Settlement.normalized()
	if pi := w.PartnerInvoice; !pi.empty() {
		if s.CommissionBase == "gross" && pi.GrossCents > 0 {
			return pi.GrossCents, "partner_invoice_gross"
		}
		if pi.NetCents > 0 {
			return pi.NetCents, "partner_invoice_net"
		}
	}
	var actual int64
	var n int
	_ = db.QueryRow(`SELECT COALESCE(SUM(total_cents),0), COUNT(*) FROM work_lines WHERE work_id=? AND source='actual'`, w.ID).Scan(&actual, &n)
	if n > 0 {
		return actual, "actual_lines"
	}
	return w.EstimateCents, "estimate"
}

// reconcileCommission makes the job's commission lines add up to what
// is owed now. Lines not yet invoiced are corrected in place; once a
// line is invoiced, the difference goes on an adjustment line (which
// may be negative). cancel = nothing is owed any more.
func reconcileCommission(db *sql.DB, pid string, w *Work, actor, reason string, cancel bool) error {
	if w.SettlementMode != modeCommission || w.PartnerResourceID == nil {
		return nil
	}
	terms := w.Settlement.normalized()
	base, source := commissionBase(db, w)
	target := commissionAmount(terms, base)
	if cancel {
		target, base = 0, 0
	}
	lines, err := workCommissions(db, pid, w.ID)
	if err != nil {
		return err
	}
	var billed int64 // invoiced or paid: can't be edited any more
	var open []*CommissionLine
	for _, l := range lines {
		switch l.Status {
		case "invoiced", "paid":
			billed += l.AmountCents
		case "accrued":
			open = append(open, l)
		}
	}
	if billed != 0 || hasStatus(lines, "invoiced", "paid") {
		target -= billed // what still has to be added or credited
	}
	now := nowUTC()
	kind := "commission"
	if hasStatus(lines, "invoiced", "paid") {
		kind = "adjustment"
	}
	// One open line carries the difference; any others are voided.
	var keep *CommissionLine
	for _, l := range open {
		if keep == nil && l.Kind == kind {
			keep = l
			continue
		}
		if _, err := db.Exec(`UPDATE commission_lines SET status='void', voided_at=?, reason=?, updated_at=? WHERE id=?`, now, "replaced: "+reason, now, l.ID); err != nil {
			return err
		}
	}
	if target == 0 && (cancel || kind == "adjustment") {
		if keep != nil {
			if _, err := db.Exec(`UPDATE commission_lines SET status='void', voided_at=?, reason=?, updated_at=? WHERE id=?`, now, reason, now, keep.ID); err != nil {
				return err
			}
			_ = logEvent(db, pid, w.ID, actor, "commission_voided", "", "", map[string]any{"line_id": keep.ID, "reason": reason})
		}
		return nil
	}
	if keep != nil {
		if keep.AmountCents == target && keep.BaseCents == base {
			return nil
		}
		if _, err := db.Exec(`UPDATE commission_lines SET base_cents=?, base_source=?, rate_pct=?, fixed_cents=?, amount_cents=?, reason=?, updated_at=? WHERE id=?`,
			base, source, terms.CommissionPct, terms.CommissionFixedCents, target, reason, now, keep.ID); err != nil {
			return err
		}
		_ = logEvent(db, pid, w.ID, actor, "commission_recalculated", "", "", map[string]any{"line_id": keep.ID, "base_cents": base,
			"base_source": source, "amount_cents": target, "previous_cents": keep.AmountCents, "reason": reason})
		return nil
	}
	if cancel && kind == "commission" {
		return nil // nothing was owed and nothing was billed
	}
	res, err := db.Exec(`INSERT INTO commission_lines(project_id,work_id,brand_id,resource_id,kind,status,base_cents,base_source,rate_pct,fixed_cents,
		amount_cents,currency,cadence,reason,created_at,updated_at) VALUES(?,?,?,?,?, 'accrued',?,?,?,?,?,?,?,?,?,?)`,
		pid, w.ID, w.BrandID, *w.PartnerResourceID, kind, base, source, terms.CommissionPct, terms.CommissionFixedCents, target,
		w.Currency, terms.InvoiceCadence, reason, now, now)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	ev := "commission_accrued"
	if kind == "adjustment" {
		ev = "commission_adjusted"
	}
	_ = logEvent(db, pid, w.ID, actor, ev, "", "", map[string]any{"line_id": id, "base_cents": base, "base_source": source,
		"rate_pct": terms.CommissionPct, "fixed_cents": terms.CommissionFixedCents, "amount_cents": target, "reason": reason})
	return nil
}

func hasStatus(lines []*CommissionLine, statuses ...string) bool {
	for _, l := range lines {
		for _, s := range statuses {
			if l.Status == s {
				return true
			}
		}
	}
	return false
}

// recordPartnerInvoice stores the contractor's own invoice to the
// client (commission jobs) and recomputes the commission on it.
func recordPartnerInvoice(db *sql.DB, pid string, w *Work, args map[string]any) (*Work, map[string]any, error) {
	if w.Phase != "bill" && w.Phase != "closed" {
		return nil, nil, errNotBillable
	}
	ref := stringArg(args, "external_ref")
	if ref == "" {
		return nil, nil, errors.New("this job is settled by commission: the contractor invoices the client. Record their invoice with external_ref (its number) and amount_net_cents")
	}
	net := int64Arg(args, "amount_net_cents")
	if net == 0 {
		net = int64Arg(args, "amount_cents")
	}
	gross := int64Arg(args, "amount_gross_cents")
	if net < 0 || gross < 0 {
		return nil, nil, errors.New("amounts must be >= 0")
	}
	pi := PartnerInvoice{}
	if w.PartnerInvoice != nil {
		pi = *w.PartnerInvoice
	}
	changed := pi.Ref != ref || (net > 0 && pi.NetCents != net) || (gross > 0 && pi.GrossCents != gross)
	if !changed {
		return w, map[string]any{"mode": "partner", "already_recorded": true, "partner_invoice": pi}, nil
	}
	pi.Ref = ref
	if net > 0 {
		pi.NetCents = net
	}
	if gross > 0 {
		pi.GrossCents = gross
	}
	pi.IssuedBy = firstNonEmpty(stringArg(args, "issued_by"), pi.IssuedBy, w.Partner)
	pi.IssuedOn = firstNonEmpty(stringArg(args, "issued_on"), pi.IssuedOn)
	pi.RecordedAt = nowUTC()
	if _, err := db.Exec(`UPDATE work SET partner_invoice_json=?, invoice_ref=?, updated_at=? WHERE id=?`, mustJSON(pi), "partner:"+ref, nowUTC(), w.ID); err != nil {
		return nil, nil, err
	}
	actor := stringArg(args, "actor")
	_ = logEvent(db, pid, w.ID, actor, "partner_invoice_recorded", "", "", map[string]any{"ref": ref, "net_cents": pi.NetCents,
		"gross_cents": pi.GrossCents, "issued_by": pi.IssuedBy})
	nw, err := getWork(db, pid, w.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := reconcileCommission(db, pid, nw, actor, "contractor invoice "+ref, false); err != nil {
		return nil, nil, err
	}
	if nw.Phase == "bill" && nw.Status != "invoiced" {
		if _, err := transition(db, pid, nw, "invoiced", actor, "", map[string]any{"invoice_ref": nw.InvoiceRef, "issued_by": pi.IssuedBy}); err != nil {
			return nil, nil, err
		}
	}
	nw, err = getWork(db, pid, w.ID)
	return nw, map[string]any{"mode": "partner", "invoice_ref": nw.InvoiceRef, "partner_invoice": pi}, err
}

// confirmClientPaid records that the client paid the contractor.
func confirmClientPaid(db *sql.DB, pid string, w *Work, by, actor string) error {
	pi := PartnerInvoice{}
	if w.PartnerInvoice != nil {
		pi = *w.PartnerInvoice
	}
	if pi.ClientPaidAt != "" {
		return nil
	}
	pi.ClientPaidAt, pi.ClientPaidConfirmedBy = nowUTC(), firstNonEmpty(by, actor, "owner")
	if _, err := db.Exec(`UPDATE work SET partner_invoice_json=?, updated_at=? WHERE id=?`, mustJSON(pi), nowUTC(), w.ID); err != nil {
		return err
	}
	return logEvent(db, pid, w.ID, actor, "client_paid_confirmed", "", "", map[string]any{"confirmed_by": pi.ClientPaidConfirmedBy})
}

// ─── listing and invoicing commissions ──────────────────────────────

// commissionFilter builds the WHERE for commissions_list / _invoice.
func commissionFilter(db *sql.DB, pid string, args map[string]any) (string, []any, error) {
	q := ` WHERE c.project_id=?`
	params := []any{pid}
	if w, err := getWorkByRef(db, pid, args); err == nil {
		q += ` AND c.work_id=?`
		params = append(params, w.ID)
	} else if int64Arg(args, "work_id") > 0 || stringArg(args, "number") != "" {
		return "", nil, err
	}
	if id := int64Arg(args, "resource_id"); id > 0 {
		q += ` AND c.resource_id=?`
		params = append(params, id)
	}
	if k := stringArg(args, "brand"); k != "" {
		b, err := getBrandByKey(db, pid, k)
		if err != nil {
			return "", nil, notFound(err, "brand "+k)
		}
		q += ` AND c.brand_id=?`
		params = append(params, b.ID)
	}
	if s := stringArg(args, "status"); s != "" {
		if s == "open" {
			q += ` AND c.status IN ('accrued','invoiced')`
		} else {
			q += ` AND c.status=?`
			params = append(params, s)
		}
	}
	if f := stringArg(args, "from"); f != "" {
		t, err := parseTime(f)
		if err != nil {
			return "", nil, err
		}
		q += ` AND c.created_at>=?`
		params = append(params, fmtTime(t))
	}
	if to := stringArg(args, "to"); to != "" {
		t, err := parseTime(to)
		if err != nil {
			return "", nil, err
		}
		q += ` AND c.created_at<?`
		params = append(params, fmtTime(t))
	}
	return q, params, nil
}

func listCommissions(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	q, params, err := commissionFilter(db, pid, args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	lines, err := queryCommissions(db, q+fmt.Sprintf(` ORDER BY c.id DESC LIMIT %d`, limit), params...)
	if err != nil {
		return nil, err
	}
	totals := map[string]int64{"accrued": 0, "invoiced": 0, "paid": 0, "void": 0}
	for _, l := range lines {
		totals[l.Status] += l.AmountCents
	}
	return map[string]any{"lines": lines, "count": len(lines), "totals": totals, "owed_by_partner": owedByPartner(db, pid, 0)}, nil
}

// owedByPartner: per contractor, commission not yet invoiced (accrued)
// and invoiced but not paid (receivable). All time.
func owedByPartner(db *sql.DB, pid string, brandID int64) []map[string]any {
	q := `SELECT c.resource_id, COALESCE(r.name,''), c.currency,
		SUM(CASE WHEN c.status='accrued' THEN c.amount_cents ELSE 0 END),
		SUM(CASE WHEN c.status='invoiced' THEN c.amount_cents ELSE 0 END)
		FROM commission_lines c LEFT JOIN resources r ON r.id=c.resource_id
		WHERE c.project_id=? AND c.status IN ('accrued','invoiced')`
	params := []any{pid}
	if brandID > 0 {
		q += ` AND c.brand_id=?`
		params = append(params, brandID)
	}
	rows, err := db.Query(q+` GROUP BY c.resource_id, c.currency ORDER BY 5 DESC, 4 DESC`, params...)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, accrued, invoiced int64
		var name, cur string
		if rows.Scan(&id, &name, &cur, &accrued, &invoiced) == nil {
			out = append(out, map[string]any{"resource_id": id, "name": name, "currency": cur, "accrued_cents": accrued, "receivable_cents": invoiced})
		}
	}
	return out
}

// lineDue: per_job lines are due once the job's contractor invoice is
// recorded or the job closed; monthly lines once their month is over.
func lineDue(db *sql.DB, l *CommissionLine, now time.Time) bool {
	if l.Cadence == "monthly" {
		created, err := parseTime(l.CreatedAt)
		if err != nil {
			return false
		}
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return created.Before(monthStart)
	}
	var phase, ref string
	_ = db.QueryRow(`SELECT phase, invoice_ref FROM work WHERE id=?`, l.WorkID).Scan(&phase, &ref)
	return ref != "" || phase == "closed" || phase == statusCancelled
}

type commissionGroup struct {
	ResourceID int64
	BrandID    int64
	Currency   string
	Lines      []*CommissionLine
}

// invoiceCommissions bills accrued lines to the contractor through
// Billing (or records an invoice made elsewhere with external_ref):
// one invoice per contractor, brand and currency. Lines are claimed
// before Billing is called, so a second run never bills them twice.
func invoiceCommissions(db *sql.DB, pid string, call appCaller, args map[string]any) (map[string]any, error) {
	if int64Arg(args, "work_id") == 0 && stringArg(args, "number") == "" && int64Arg(args, "resource_id") == 0 && !boolArg(args, "all", false) {
		return nil, errors.New("give work_id/number, resource_id, or all: true")
	}
	q, params, err := commissionFilter(db, pid, map[string]any{"work_id": args["work_id"], "number": args["number"],
		"resource_id": args["resource_id"], "brand": args["brand"]})
	if err != nil {
		return nil, err
	}
	lines, err := queryCommissions(db, q+` AND c.status='accrued' AND c.invoice_ref='' ORDER BY c.id`, params...)
	if err != nil {
		return nil, err
	}
	dueOnly := boolArg(args, "due_only", int64Arg(args, "work_id") == 0 && stringArg(args, "number") == "")
	now := clock().UTC()
	groups := map[string]*commissionGroup{}
	order := []string{}
	skipped := []map[string]any{}
	for _, l := range lines {
		if dueOnly && !lineDue(db, l, now) {
			skipped = append(skipped, map[string]any{"line_id": l.ID, "work_number": l.WorkNumber, "reason": "not due yet (" + l.Cadence + ")"})
			continue
		}
		k := fmt.Sprintf("%d|%d|%s", l.ResourceID, l.BrandID, l.Currency)
		if groups[k] == nil {
			groups[k] = &commissionGroup{ResourceID: l.ResourceID, BrandID: l.BrandID, Currency: l.Currency}
			order = append(order, k)
		}
		groups[k].Lines = append(groups[k].Lines, l)
	}
	ext := stringArg(args, "external_ref")
	if ext != "" && len(order) > 1 {
		return nil, fmt.Errorf("external_ref records one invoice, but these lines make %d invoices (one per contractor, brand and currency); narrow with resource_id and brand", len(order))
	}
	actor := stringArg(args, "actor")
	invoices := []map[string]any{}
	for _, k := range order {
		g := groups[k]
		res, err := invoiceGroup(db, pid, call, g, ext, actor, args)
		if err != nil {
			invoices = append(invoices, map[string]any{"resource_id": g.ResourceID, "brand_id": g.BrandID, "error": err.Error(),
				"line_ids": lineIDs(g.Lines)})
			continue
		}
		invoices = append(invoices, res)
	}
	return map[string]any{"invoices": invoices, "skipped": skipped}, nil
}

func lineIDs(ls []*CommissionLine) []int64 {
	out := []int64{}
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

func invoiceGroup(db *sql.DB, pid string, call appCaller, g *commissionGroup, ext, actor string, args map[string]any) (map[string]any, error) {
	var total int64
	for _, l := range g.Lines {
		total += l.AmountCents
	}
	if total <= 0 && ext == "" {
		return nil, fmt.Errorf("these lines total %d (a credit); issue the credit note elsewhere and record it with external_ref", total)
	}
	r, err := getResource(db, pid, g.ResourceID)
	if err != nil {
		return nil, notFound(err, "resource")
	}
	terms := r.termsFor(g.BrandID)
	ids := lineIDs(g.Lines)
	token := "pending:" + randomToken()
	claimed, err := claimLines(db, ids, token)
	if err != nil {
		return nil, err
	}
	if claimed != len(ids) {
		_, _ = db.Exec(`UPDATE commission_lines SET invoice_ref='' WHERE invoice_ref=?`, token)
		return nil, errors.New("some lines are being invoiced already; list them again")
	}
	release := func() { _, _ = db.Exec(`UPDATE commission_lines SET invoice_ref='' WHERE invoice_ref=?`, token) }
	out := map[string]any{"resource_id": g.ResourceID, "partner": r.Name, "brand_id": g.BrandID, "currency": g.Currency,
		"total_cents": total, "line_ids": ids}
	ref := ext
	if ref != "" {
		out["mode"] = "external"
	} else {
		if call == nil {
			release()
			return nil, errors.New("billing app not reachable; pass external_ref to record an invoice made elsewhere")
		}
		if terms.BillingEmail == "" {
			release()
			return nil, fmt.Errorf("%s has no billing_email in its settlement terms; set it with resources_upsert or pass external_ref", r.Name)
		}
		var cust struct {
			Customer struct {
				ID int64 `json:"id"`
			} `json:"customer"`
		}
		if err := call("billing", "customers_upsert_by_email", map[string]any{"email": terms.BillingEmail,
			"defaults": map[string]any{"name": firstNonEmpty(terms.BillingName, r.Name)}}, &cust); err != nil {
			release()
			return nil, fmt.Errorf("billing customer: %w", err)
		}
		items := []any{}
		for _, l := range g.Lines {
			desc := fmt.Sprintf("Commission %s", l.WorkNumber)
			if l.RatePct > 0 {
				desc += fmt.Sprintf(" · %.4g%% of %s", l.RatePct, centsString(l.BaseCents))
			}
			if l.FixedCents > 0 {
				desc += " · fixed " + centsString(l.FixedCents)
			}
			if l.Kind == "adjustment" {
				desc = "Adjustment " + l.WorkNumber + firstNonEmpty(" · "+l.Reason, "")
			}
			items = append(items, map[string]any{"description": desc, "quantity": 1, "unit_price_cents": l.AmountCents,
				"metadata": map[string]any{"commission_line_id": l.ID, "work_id": l.WorkID, "work_number": l.WorkNumber, "kind": l.Kind}})
		}
		meta := map[string]any{"source": "operations", "kind": "commission", "resource_id": r.ID, "commission_line_ids": ids}
		notes := "Commissions · " + r.Name
		if g.BrandID > 0 {
			if b, err := getBrand(db, pid, g.BrandID); err == nil {
				meta["brand"] = map[string]any{"key": b.Key, "name": b.Name, "legal_name": b.LegalName}
				notes = firstNonEmpty(b.LegalName, b.Name) + " · " + notes
			}
		}
		var inv struct {
			Invoice struct {
				ID     int64  `json:"id"`
				Number string `json:"number"`
			} `json:"invoice"`
		}
		if err := call("billing", "invoices_create_from_prepared_lines", map[string]any{"customer_id": cust.Customer.ID,
			"currency": g.Currency, "line_items": items, "notes": firstNonEmpty(stringArg(args, "notes"), notes),
			"finalize": boolArg(args, "finalize", true), "metadata": meta}, &inv); err != nil {
			release()
			return nil, fmt.Errorf("billing invoice: %w", err)
		}
		ref = fmt.Sprintf("billing:%d", inv.Invoice.ID)
		out["mode"], out["invoice_id"], out["invoice_number"] = "billing", inv.Invoice.ID, inv.Invoice.Number
	}
	now := nowUTC()
	if _, err := db.Exec(`UPDATE commission_lines SET status='invoiced', invoice_ref=?, invoiced_at=?, updated_at=? WHERE invoice_ref=?`, ref, now, now, token); err != nil {
		return nil, err
	}
	for _, l := range g.Lines {
		_ = logEvent(db, pid, l.WorkID, actor, "commission_invoiced", "", "", map[string]any{"line_id": l.ID, "invoice_ref": ref, "amount_cents": l.AmountCents})
	}
	out["invoice_ref"] = ref
	return out, nil
}

func claimLines(db *sql.DB, ids []int64, token string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	params := []any{token}
	for _, id := range ids {
		params = append(params, id)
	}
	res, err := db.Exec(`UPDATE commission_lines SET invoice_ref=? WHERE status='accrued' AND invoice_ref='' AND id IN (`+ph+`)`, params...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func randomToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func centsString(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// setCommissionStatus: mark_paid moves invoiced lines to paid (by ids
// or by invoice_ref); void cancels accrued lines.
func setCommissionStatus(db *sql.DB, pid string, args map[string]any, to string) ([]*CommissionLine, error) {
	reason := stringArg(args, "reason")
	if to == "void" && reason == "" {
		return nil, errors.New("reason required")
	}
	idList := int64sArg(args, "line_ids")
	if id := int64Arg(args, "line_id"); id > 0 {
		idList = append(idList, id)
	}
	if ref := stringArg(args, "invoice_ref"); ref != "" && to == "paid" {
		rows, err := db.Query(`SELECT id FROM commission_lines WHERE project_id=? AND invoice_ref=?`, pid, ref)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			idList = append(idList, id)
		}
		rows.Close()
	}
	if len(idList) == 0 {
		return nil, errors.New("line_id, line_ids or invoice_ref required")
	}
	from := map[string]string{"paid": "invoiced", "void": "accrued"}[to]
	actor := stringArg(args, "actor")
	now := nowUTC()
	out := []*CommissionLine{}
	for _, id := range uniqueIDs(idList) {
		ls, err := queryCommissions(db, ` WHERE c.project_id=? AND c.id=?`, pid, id)
		if err != nil {
			return nil, err
		}
		if len(ls) == 0 {
			return nil, fmt.Errorf("commission line %d not found", id)
		}
		l := ls[0]
		if l.Status == to {
			out = append(out, l)
			continue
		}
		if l.Status != from || strings.HasPrefix(l.InvoiceRef, "pending:") {
			msg := fmt.Sprintf("line %d is %s; only %s lines can be marked %s", id, l.Status, from, to)
			if to == "void" && (l.Status == "invoiced" || l.Status == "paid") {
				msg += ". An invoiced line is corrected by a new partner invoice amount or by cancelling the job, which adds an adjustment"
			}
			return nil, errors.New(msg)
		}
		col := "paid_at"
		if to == "void" {
			col = "voided_at"
		}
		if _, err := db.Exec(`UPDATE commission_lines SET status=?, `+col+`=?, reason=CASE WHEN ?<>'' THEN ? ELSE reason END, updated_at=? WHERE id=?`,
			to, now, reason, reason, now, id); err != nil {
			return nil, err
		}
		_ = logEvent(db, pid, l.WorkID, actor, "commission_"+map[string]string{"paid": "paid", "void": "voided"}[to], "", "",
			map[string]any{"line_id": id, "amount_cents": l.AmountCents, "reason": reason})
		nl, _ := queryCommissions(db, ` WHERE c.project_id=? AND c.id=?`, pid, id)
		out = append(out, nl...)
	}
	return out, nil
}

// ─── auto-close ─────────────────────────────────────────────────────

// autoCloseCommissionJobs closes commission jobs whose contractor
// invoice was recorded more than commission_autoclose_days ago (a brand
// or project policy; 0 or unset = never). Runs from the hourly worker.
func autoCloseCommissionJobs(db *sql.DB, onClosed func(pid string, w *Work)) (int, error) {
	rows, err := db.Query(`SELECT project_id, id FROM work WHERE settlement_mode='commission' AND phase='bill' AND invoice_ref<>''`)
	if err != nil {
		return 0, err
	}
	type ref struct {
		pid string
		id  int64
	}
	var cands []ref
	for rows.Next() {
		var r ref
		if rows.Scan(&r.pid, &r.id) == nil {
			cands = append(cands, r)
		}
	}
	rows.Close()
	n := 0
	now := clock()
	for _, c := range cands {
		w, err := getWork(db, c.pid, c.id)
		if err != nil || w.PartnerInvoice.empty() {
			continue
		}
		days := policyInt(db, c.pid, w.BrandID, "commission_autoclose_days", 0)
		if days <= 0 {
			continue
		}
		rec, err := parseTime(w.PartnerInvoice.RecordedAt)
		if err != nil || now.Sub(rec) < time.Duration(days)*24*time.Hour {
			continue
		}
		reason := fmt.Sprintf("auto-closed %d days after the contractor's invoice", days)
		if err := confirmClientPaid(db, c.pid, w, "auto", "system"); err != nil {
			continue
		}
		if _, err := db.Exec(`UPDATE work SET paid_at=COALESCE(paid_at, ?), updated_at=? WHERE id=?`, nowUTC(), nowUTC(), w.ID); err != nil {
			continue
		}
		w, _ = getWork(db, c.pid, c.id)
		if _, err := transition(db, c.pid, w, "closed", "system", reason, nil); err == nil {
			n++
			if onClosed != nil {
				onClosed(c.pid, w)
			}
		}
	}
	return n, nil
}

// ─── metrics ────────────────────────────────────────────────────────

// settlementMetrics adds GMV, revenue and commissions for the period,
// split by mode and by contractor. bw narrows to a brand (" AND w.brand_id=N").
func settlementMetrics(db *sql.DB, pid, f, t, bw string, brandID int64, resaleInvoiced int64, out map[string]any) {
	var partnerNet int64
	var commissionJobs, resaleJobs int
	_ = db.QueryRow(`SELECT COALESCE(SUM(CAST(json_extract(partner_invoice_json,'$.net_cents') AS INTEGER)),0), COUNT(*) FROM work w
		WHERE project_id=? AND settlement_mode='commission' AND invoice_ref<>'' AND updated_at>=? AND updated_at<?`+bw, pid, f, t).Scan(&partnerNet, &commissionJobs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND settlement_mode<>'commission' AND invoice_ref<>'' AND updated_at>=? AND updated_at<?`+bw,
		pid, f, t).Scan(&resaleJobs)
	byStatus := map[string]int64{"accrued": 0, "invoiced": 0, "paid": 0}
	rows, err := db.Query(`SELECT c.status, COALESCE(SUM(c.amount_cents),0) FROM commission_lines c JOIN work w ON w.id=c.work_id
		WHERE c.project_id=? AND c.status<>'void' AND c.created_at>=? AND c.created_at<?`+bw+` GROUP BY c.status`, pid, f, t)
	if err == nil {
		for rows.Next() {
			var s string
			var v int64
			if rows.Scan(&s, &v) == nil {
				byStatus[s] = v
			}
		}
		rows.Close()
	}
	commission := byStatus["accrued"] + byStatus["invoiced"] + byStatus["paid"]
	out["gmv_cents"] = resaleInvoiced + partnerNet
	out["revenue_cents"] = resaleInvoiced + commission
	out["commissions"] = map[string]any{"accrued_cents": byStatus["accrued"], "invoiced_cents": byStatus["invoiced"], "paid_cents": byStatus["paid"],
		"total_cents": commission}
	out["by_mode"] = map[string]any{
		"resale":     map[string]any{"jobs_invoiced": resaleJobs, "invoiced_cents": resaleInvoiced},
		"commission": map[string]any{"jobs_invoiced": commissionJobs, "client_invoiced_net_cents": partnerNet, "commission_cents": commission},
	}
	out["receivables"] = owedByPartner(db, pid, brandID)
	out["by_contractor"] = contractorBreakdown(db, pid, f, t, bw)
}

func contractorBreakdown(db *sql.DB, pid, f, t, bw string) []map[string]any {
	rows, err := db.Query(`SELECT w.partner_resource_id, COALESCE(r.name,''), w.settlement_mode, COUNT(*),
		COALESCE(SUM(CASE WHEN w.settlement_mode='commission' THEN CAST(json_extract(w.partner_invoice_json,'$.net_cents') AS INTEGER) ELSE w.invoiced_cents END),0)
		FROM work w LEFT JOIN resources r ON r.id=w.partner_resource_id
		WHERE w.project_id=? AND w.partner_resource_id IS NOT NULL AND w.invoice_ref<>'' AND w.updated_at>=? AND w.updated_at<?`+bw+`
		GROUP BY w.partner_resource_id, w.settlement_mode`, pid, f, t)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	type key struct {
		id   int64
		mode string
	}
	idx := map[key]map[string]any{}
	for rows.Next() {
		var id int64
		var name, mode string
		var jobs int
		var gmv int64
		if rows.Scan(&id, &name, &mode, &jobs, &gmv) != nil {
			continue
		}
		m := map[string]any{"resource_id": id, "name": name, "mode": mode, "jobs_invoiced": jobs, "gmv_cents": gmv,
			"commission_accrued_cents": int64(0), "commission_invoiced_cents": int64(0), "commission_paid_cents": int64(0)}
		idx[key{id, mode}] = m
		out = append(out, m)
	}
	rows.Close()
	crow, err := db.Query(`SELECT c.resource_id, c.status, COALESCE(SUM(c.amount_cents),0) FROM commission_lines c JOIN work w ON w.id=c.work_id
		WHERE c.project_id=? AND c.status<>'void' AND c.created_at>=? AND c.created_at<?`+bw+` GROUP BY c.resource_id, c.status`, pid, f, t)
	if err == nil {
		for crow.Next() {
			var id, v int64
			var s string
			if crow.Scan(&id, &s, &v) != nil {
				continue
			}
			m := idx[key{id, modeCommission}]
			if m == nil {
				var name string
				_ = db.QueryRow(`SELECT name FROM resources WHERE id=?`, id).Scan(&name)
				m = map[string]any{"resource_id": id, "name": name, "mode": modeCommission, "jobs_invoiced": 0, "gmv_cents": int64(0),
					"commission_accrued_cents": int64(0), "commission_invoiced_cents": int64(0), "commission_paid_cents": int64(0)}
				idx[key{id, modeCommission}] = m
				out = append(out, m)
			}
			m["commission_"+s+"_cents"] = v
		}
		crow.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["gmv_cents"].(int64) > out[j]["gmv_cents"].(int64) })
	return out
}
