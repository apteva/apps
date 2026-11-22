package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// appCaller calls a sibling app's tool (crm, billing). nil in tests or
// when the platform is unavailable; bridges then degrade gracefully.
type appCaller func(app, tool string, input map[string]any, out any) error

type Work struct {
	ID              int64           `json:"id"`
	Number          string          `json:"number"`
	BrandID         int64           `json:"brand_id"`        // 0 = no brand
	Brand           string          `json:"brand,omitempty"` // brand key
	WorkTypeID      int64           `json:"work_type_id"`
	WorkType        string          `json:"work_type"`
	Mode            string          `json:"mode"`
	Phase           string          `json:"phase"`
	Status          string          `json:"status"`
	HoldFromStatus  string          `json:"hold_from_status,omitempty"`
	Priority        string          `json:"priority"`
	Source          string          `json:"source"`
	CRMContactID    *int64          `json:"crm_contact_id,omitempty"`
	CustomerName    string          `json:"customer_name"`
	CustomerEmail   string          `json:"customer_email"`
	CustomerPhone   string          `json:"customer_phone"`
	SiteID          *int64          `json:"site_id"`
	ObjectIDs       []int64         `json:"object_ids"`
	PartySize       int             `json:"party_size"`
	Summary         string          `json:"summary"`
	Intake          map[string]any  `json:"intake"`
	Custom          json.RawMessage `json:"custom"`
	AgreementID     *int64          `json:"agreement_id,omitempty"`
	ParentID        *int64          `json:"parent_id,omitempty"`
	DurationMin     int             `json:"duration_min"`
	Currency        string          `json:"currency"`
	EstimateCents   int64           `json:"estimate_cents"`
	QuoteStatus     string          `json:"quote_status"`
	QuoteAcceptedAt string          `json:"quote_accepted_at,omitempty"`
	InvoiceRef      string          `json:"invoice_ref,omitempty"`
	InvoicedCents   int64           `json:"invoiced_cents"`
	PaidAt          string          `json:"paid_at,omitempty"`
	SLADueAt        string          `json:"sla_due_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	ClosedAt        string          `json:"closed_at,omitempty"`
	// Settlement: the contractor doing the job and its terms, copied
	// when assigned. resale (default) or commission.
	PartnerResourceID *int64          `json:"partner_resource_id,omitempty"`
	Partner           string          `json:"partner,omitempty"`
	SettlementMode    string          `json:"settlement_mode"`
	Settlement        Settlement      `json:"settlement"`
	PartnerInvoice    *PartnerInvoice `json:"partner_invoice,omitempty"`
}

const workCols = `w.id,w.number,w.work_type_id,t.key,w.mode,w.phase,w.status,w.hold_from_status,w.priority,w.source,w.crm_contact_id,
	w.customer_name,w.customer_email,w.customer_phone,w.site_id,w.object_ids_json,w.party_size,w.summary,w.intake_json,w.custom_json,
	w.agreement_id,w.parent_id,w.duration_min,w.currency,w.estimate_cents,w.quote_status,w.quote_accepted_at,w.invoice_ref,
	w.invoiced_cents,w.paid_at,w.sla_due_at,w.created_at,w.updated_at,w.closed_at,w.brand_id,COALESCE(b.key,''),
	w.partner_resource_id,COALESCE(pr.name,''),w.settlement_mode,w.settlement_json,w.partner_invoice_json`

const workFrom = ` FROM work w JOIN work_types t ON t.id=w.work_type_id LEFT JOIN brands b ON b.id=w.brand_id
	LEFT JOIN resources pr ON pr.id=w.partner_resource_id`

func scanWork(s scanner) (*Work, error) {
	var w Work
	var crm, site, agr, parent, partner sql.NullInt64
	var objs, intake, custom, settle, pinv string
	var qa, paid, sla, closed sql.NullString
	if err := s.Scan(&w.ID, &w.Number, &w.WorkTypeID, &w.WorkType, &w.Mode, &w.Phase, &w.Status, &w.HoldFromStatus, &w.Priority, &w.Source,
		&crm, &w.CustomerName, &w.CustomerEmail, &w.CustomerPhone, &site, &objs, &w.PartySize, &w.Summary, &intake, &custom,
		&agr, &parent, &w.DurationMin, &w.Currency, &w.EstimateCents, &w.QuoteStatus, &qa, &w.InvoiceRef,
		&w.InvoicedCents, &paid, &sla, &w.CreatedAt, &w.UpdatedAt, &closed, &w.BrandID, &w.Brand,
		&partner, &w.Partner, &w.SettlementMode, &settle, &pinv); err != nil {
		return nil, err
	}
	if partner.Valid {
		w.PartnerResourceID = &partner.Int64
	}
	decodeInto(settle, &w.Settlement)
	if w.SettlementMode == "" {
		w.SettlementMode = modeResale
	}
	w.Settlement.Mode = w.SettlementMode
	w.Settlement = w.Settlement.normalized()
	var pi PartnerInvoice
	decodeInto(pinv, &pi)
	if pi.Ref != "" {
		w.PartnerInvoice = &pi
	}
	if crm.Valid {
		w.CRMContactID = &crm.Int64
	}
	if site.Valid {
		w.SiteID = &site.Int64
	}
	if agr.Valid {
		w.AgreementID = &agr.Int64
	}
	if parent.Valid {
		w.ParentID = &parent.Int64
	}
	w.ObjectIDs = []int64{}
	decodeInto(objs, &w.ObjectIDs)
	w.Intake = map[string]any{}
	decodeInto(intake, &w.Intake)
	w.Custom = json.RawMessage(custom)
	w.QuoteAcceptedAt, w.PaidAt, w.SLADueAt, w.ClosedAt = qa.String, paid.String, sla.String, closed.String
	return &w, nil
}

func getWork(db *sql.DB, pid string, id int64) (*Work, error) {
	return scanWork(db.QueryRow(`SELECT `+workCols+workFrom+` WHERE w.project_id=? AND w.id=?`, pid, id))
}

func getWorkByRef(db *sql.DB, pid string, args map[string]any) (*Work, error) {
	if id := int64Arg(args, "work_id"); id > 0 {
		w, err := getWork(db, pid, id)
		return w, notFound(err, "work")
	}
	if n := stringArg(args, "number"); n != "" {
		w, err := scanWork(db.QueryRow(`SELECT `+workCols+workFrom+` WHERE w.project_id=? AND w.number=?`, pid, n))
		return w, notFound(err, "work")
	}
	return nil, errors.New("work_id or number required")
}

// ─── intake ─────────────────────────────────────────────────────────

type IntakeResult struct {
	Work     *Work        `json:"work"`
	Quote    *PriceResult `json:"price,omitempty"`
	Missing  []string     `json:"missing_answers,omitempty"`
	Playbook string       `json:"playbook,omitempty"`
	CRMError string       `json:"crm_error,omitempty"`
}

func intakeWork(db *sql.DB, pid string, call appCaller, args map[string]any) (*IntakeResult, error) {
	brandID, wt, err := resolveWorkBrand(db, pid, args, stringArg(args, "work_type"), int64Arg(args, "site_id"))
	if err != nil {
		return nil, err
	}
	if !wt.Active {
		return nil, fmt.Errorf("work type %s is inactive", wt.Key)
	}
	answers := objectArg(args, "answers")
	if answers == nil {
		answers = map[string]any{}
	}
	res := &IntakeResult{Playbook: wt.Playbook}
	if wt.IntakeFormKey != "" {
		f, err := getForm(db, pid, wt.IntakeFormKey, brandID)
		if err != nil {
			return nil, notFound(err, "intake form")
		}
		missing, invalid := checkFormData(f, answers, false)
		if len(invalid) > 0 {
			return nil, fmt.Errorf("invalid answers: %s", strings.Join(invalid, "; "))
		}
		res.Missing = missing
	}
	customer := objectArg(args, "customer")
	if customer == nil {
		customer = map[string]any{}
	}
	name, email, phone := stringArg(customer, "name"), strings.ToLower(stringArg(customer, "email")), stringArg(customer, "phone")
	crmID := int64Arg(customer, "crm_contact_id")
	if crmID == 0 && call != nil && (email != "" || phone != "") {
		kind, value := "email", email
		if email == "" {
			kind, value = "phone", phone
		}
		var got struct {
			Contact struct {
				ID int64 `json:"id"`
			} `json:"contact"`
		}
		if err := call("crm", "contacts_upsert_by_channel", map[string]any{"kind": kind, "value": value,
			"defaults": map[string]any{"display_name": name}, "source": "operations"}, &got); err != nil {
			res.CRMError = err.Error()
		} else {
			crmID = got.Contact.ID
		}
	}

	siteID := int64Arg(args, "site_id")
	if siteID == 0 && stringArg(args, "address") != "" {
		s, err := upsertSite(db, pid, map[string]any{"kind": "customer", "name": firstNonEmpty(name, stringArg(args, "address")),
			"address": stringArg(args, "address"), "lat": args["lat"], "lng": args["lng"], "crm_contact_id": nilIfZero(crmID)})
		if err != nil {
			return nil, err
		}
		siteID = s.ID
	}
	if siteID == 0 && wt.Mode != "at_customer" {
		// Premises work defaults to the only own site the brand can use
		// (its own sites and the shared ones).
		var only int64
		var n int
		_ = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(id),0) FROM sites WHERE project_id=? AND kind='own' AND brand_id IN (?,0)`,
			pid, brandID).Scan(&n, &only)
		if n == 1 {
			siteID = only
		}
	}
	if siteID > 0 {
		if _, err := getSite(db, pid, siteID); err != nil {
			return nil, notFound(err, "site")
		}
	}
	if wt.Mode == "at_customer" && siteID == 0 {
		return nil, errors.New("at_customer work needs site_id or address")
	}
	summary := stringArg(args, "summary")
	priority := stringArg(args, "priority")
	if priority == "" {
		priority = derivePriority(wt, summary, answers)
	}
	partySize := intArg(args, "party_size", 0)
	if partySize == 0 {
		partySize = intArg(answers, "party_size", 0)
	}
	currency := strings.ToUpper(firstNonEmpty(stringArg(args, "currency"), policyString(db, pid, brandID, "currency", "EUR")))
	now := clock()
	var sla any
	if mins, ok := wt.SLA[priority]; ok && mins > 0 {
		sla = fmtTime(now.Add(time.Duration(mins) * time.Minute))
	}
	phase := wt.Phases[0]
	status := firstStatus(wt, phase)
	prefix := defaultNumberPrefix
	if brandID > 0 {
		b, err := getBrand(db, pid, brandID)
		if err != nil {
			return nil, err
		}
		prefix = b.NumberPrefix
	}
	number, err := nextNumber(db, pid, prefix)
	if err != nil {
		return nil, err
	}
	objIDs := int64sArg(args, "object_ids")
	ts := fmtTime(now)
	r, err := db.Exec(`INSERT INTO work(project_id,number,brand_id,work_type_id,mode,phase,status,priority,source,crm_contact_id,customer_name,customer_email,
		customer_phone,site_id,object_ids_json,party_size,summary,intake_json,custom_json,agreement_id,parent_id,duration_min,currency,sla_due_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, number, brandID, wt.ID, wt.Mode, phase, status, priority, firstNonEmpty(stringArg(args, "source"), "agent"), nilIfZero(crmID), name, email, phone,
		nilIfZero(siteID), jsonOr(objIDs, "[]"), partySize, summary, mustJSON(answers), jsonOr(objectArg(args, "custom"), "{}"),
		nilIfZero(int64Arg(args, "agreement_id")), nilIfZero(int64Arg(args, "parent_id")), wt.DefaultDurationMin, currency, sla, ts, ts)
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	if wt.IntakeFormKey != "" && len(answers) > 0 {
		f, _ := getForm(db, pid, wt.IntakeFormKey, brandID)
		if f != nil {
			_, _ = db.Exec(`INSERT INTO form_responses(project_id,form_key,form_version,work_id,site_id,responder,data_json,submitted_at) VALUES(?,?,?,?,?,?,?,?)`,
				pid, f.Key, f.Version, id, nilIfZero(siteID), "intake", mustJSON(answers), ts)
		}
	}
	_ = logEvent(db, pid, id, stringArg(args, "actor"), "created", "", status, map[string]any{"priority": priority, "source": stringArg(args, "source")})
	w, err := getWork(db, pid, id)
	if err != nil {
		return nil, err
	}
	// A contractor named at intake: its terms and prices apply from the
	// first estimate.
	if rid := int64Arg(args, "partner_resource_id"); rid > 0 {
		if w, err = assignPartner(db, pid, w, rid, stringArg(args, "actor"), "intake"); err != nil {
			return nil, err
		}
	}
	if items := parseItems(args["items"]); len(items) > 0 {
		pr, err := buildEstimate(db, pid, w, wt, items, args)
		if err != nil {
			return nil, err
		}
		res.Quote = pr
		w, _ = getWork(db, pid, id)
	}
	res.Work = w
	return res, nil
}

func derivePriority(wt *WorkType, summary string, answers map[string]any) string {
	text := strings.ToLower(summary + " " + mustJSON(answers))
	for _, r := range wt.PriorityRules {
		for _, k := range r.Keywords {
			if k != "" && strings.Contains(text, strings.ToLower(k)) {
				return r.Priority
			}
		}
	}
	return "p3"
}

// nextNumber returns the next job number in a prefix's series:
// W-00001 for work without a brand, FF-00001 for brand FlowFix.
func nextNumber(db *sql.DB, pid, prefix string) (string, error) {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND number LIKE ?`, pid, prefix+"-%").Scan(&n); err != nil {
		return "", err
	}
	for {
		n++
		num := fmt.Sprintf("%s-%05d", prefix, n)
		var exists int
		_ = db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND number=?`, pid, num).Scan(&exists)
		if exists == 0 {
			return num, nil
		}
	}
}

func parseItems(raw any) []ItemRequest {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := []ItemRequest{}
	for _, x := range arr {
		switch v := x.(type) {
		case string:
			out = append(out, ItemRequest{Code: v})
		case map[string]any:
			it := ItemRequest{Code: stringArg(v, "code")}
			if hasArg(v, "qty") {
				q := floatArg(v, "qty", 1)
				it.Qty = &q
			}
			out = append(out, it)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func policyString(db *sql.DB, pid string, brandID int64, key, def string) string {
	raw, ok := policyRaw(db, pid, brandID, key)
	if !ok {
		return def
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil || s == "" {
		return def
	}
	return s
}

// ─── pricing on a work item ─────────────────────────────────────────

func priceContextFor(db *sql.DB, pid string, w *Work, wt *WorkType, args map[string]any) PriceContext {
	pc := PriceContext{BrandID: w.BrandID, WorkTypeKey: wt.Key, Priority: w.Priority, PartySize: w.PartySize, Answers: w.Intake,
		DefaultDur: wt.DefaultDurationMin, TypeReqs: append([]Requirement{}, wt.Requirements...), AutoMax: wt.QuotePolicy.AutoApproveMaxCents,
		SettlementMode: w.SettlementMode, Terms: w.Settlement}
	if w.PartnerResourceID != nil {
		pc.ResourceID = *w.PartnerResourceID
	}
	if extra := objectArg(args, "answers"); extra != nil {
		merged := map[string]any{}
		for k, v := range w.Intake {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		pc.Answers = merged
	}
	pc.DiscountPct = floatArg(args, "discount_pct", 0)
	if fl := arrayArg(args, "flags"); fl != nil {
		for _, f := range fl {
			if s, ok := f.(string); ok {
				pc.Flags = append(pc.Flags, s)
			}
		}
	}
	start := stringArg(args, "starts_at")
	if start == "" {
		var s string
		if db.QueryRow(`SELECT starts_at FROM slots WHERE work_id=? AND status<>'cancelled' ORDER BY starts_at LIMIT 1`, w.ID).Scan(&s) == nil {
			start = s
		}
	}
	if t, err := parseTime(start); err == nil {
		pc.StartsAt = &t
	}
	if w.SiteID != nil {
		if s, err := getSite(db, pid, *w.SiteID); err == nil {
			pc.Location = loadLocation(s.Timezone)
		}
	}
	if pc.Location == nil {
		pc.Location = loadLocation(policyString(db, pid, w.BrandID, "timezone", "UTC"))
	}
	return pc
}

// buildEstimate prices items and stores them as the estimate lines.
func buildEstimate(db *sql.DB, pid string, w *Work, wt *WorkType, items []ItemRequest, args map[string]any) (*PriceResult, error) {
	pr, err := priceItems(db, pid, items, priceContextFor(db, pid, w, wt, args))
	if err != nil {
		return nil, err
	}
	if err := replaceLines(db, pid, w.ID, "estimate", pr.Lines); err != nil {
		return nil, err
	}
	qs := "draft"
	if pr.NeedsOwner {
		qs = "needs_owner"
	}
	if _, err := db.Exec(`UPDATE work SET estimate_cents=?, duration_min=?, quote_status=?, updated_at=? WHERE id=?`,
		pr.TotalCents, pr.DurationMin, qs, nowUTC(), w.ID); err != nil {
		return nil, err
	}
	_ = logEvent(db, pid, w.ID, stringArg(args, "actor"), "estimate", "", "", map[string]any{"total_cents": pr.TotalCents, "flags": pr.Flags})
	return pr, nil
}

func replaceLines(db *sql.DB, pid string, workID int64, source string, lines []PricedLine) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM work_lines WHERE work_id=? AND source=?`, workID, source); err != nil {
		return err
	}
	now := nowUTC()
	for _, l := range lines {
		if _, err := tx.Exec(`INSERT INTO work_lines(project_id,work_id,source,rate_item_id,code,kind,description,qty,unit,unit_price_cents,total_cents,cost_cents,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, workID, source, nilIfZero(l.RateItemID), l.Code, l.Kind, l.Description, l.Qty, l.Unit,
			l.UnitPriceCents, l.TotalCents, l.CostCents, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func listLines(db *sql.DB, workID int64, source string) ([]PricedLine, error) {
	rows, err := db.Query(`SELECT COALESCE(rate_item_id,0),code,kind,description,qty,unit,unit_price_cents,total_cents,cost_cents
		FROM work_lines WHERE work_id=? AND source=? ORDER BY id`, workID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PricedLine{}
	for rows.Next() {
		var l PricedLine
		if err := rows.Scan(&l.RateItemID, &l.Code, &l.Kind, &l.Description, &l.Qty, &l.Unit, &l.UnitPriceCents, &l.TotalCents, &l.CostCents); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func itemsFromLines(lines []PricedLine) []ItemRequest {
	out := []ItemRequest{}
	for _, l := range lines {
		if l.Kind != "item" {
			continue
		}
		q := l.Qty
		out = append(out, ItemRequest{Code: l.Code, Qty: &q})
	}
	return out
}

// workRequirements recomputes the resources a work item needs: the
// type's requirements plus those of its estimate items.
func workRequirements(db *sql.DB, pid string, w *Work, wt *WorkType) []Requirement {
	reqs := mergeRequirements(append([]Requirement{}, wt.Requirements...), nil)
	lines, _ := listLines(db, w.ID, "estimate")
	for _, l := range lines {
		if l.Kind != "item" {
			continue
		}
		if ri, err := getRateItemFor(db, pid, l.Code, w.BrandID, partnerID(w)); err == nil {
			reqs = mergeRequirements(reqs, ri.Requirements)
		}
	}
	return reqs
}

// ─── quotes ─────────────────────────────────────────────────────────

func quoteDecision(db *sql.DB, pid string, w *Work, decision, actor, reason string) (*Work, string, error) {
	if w.EstimateCents == 0 && countLines(db, w.ID, "estimate") == 0 {
		return nil, "", errors.New("build an estimate first (quote_build)")
	}
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, "", err
	}
	event := ""
	switch decision {
	case "approve": // owner clears a policy flag
		if w.QuoteStatus != "needs_owner" {
			return nil, "", errors.New("quote does not need owner approval")
		}
		if err := setQuoteStatus(db, w.ID, "approved"); err != nil {
			return nil, "", err
		}
		if w.Status == statusNeedsOwner {
			if _, err := transition(db, pid, w, "resume", actor, "quote approved", nil); err != nil {
				return nil, "", err
			}
		}
		event = "quote.approved"
	case "send":
		if w.QuoteStatus == "needs_owner" {
			return nil, "", errors.New("quote needs owner approval before it is sent")
		}
		if err := setQuoteStatus(db, w.ID, "sent"); err != nil {
			return nil, "", err
		}
		if wt.usesPhase("quote") && (w.Phase == "quote" || w.Phase == "intake") {
			if w.Phase == "intake" {
				if _, err := transition(db, pid, w, "quote", actor, "", nil); err != nil {
					return nil, "", err
				}
			}
			if _, err := transition(db, pid, w, "quote_sent", actor, "", nil); err != nil {
				return nil, "", err
			}
		}
		event = "quote.sent"
	case "accept":
		if w.QuoteStatus == "needs_owner" {
			return nil, "", errors.New("quote needs owner approval before it can be accepted")
		}
		if _, err := db.Exec(`UPDATE work SET quote_status='accepted', quote_accepted_at=?, updated_at=? WHERE id=?`, nowUTC(), nowUTC(), w.ID); err != nil {
			return nil, "", err
		}
		w.QuoteStatus = "accepted"
		if wt.usesPhase("quote") && (w.Phase == "intake" || w.Phase == "quote") {
			if w.Phase == "intake" {
				if _, err := transition(db, pid, w, "quote", actor, "", nil); err != nil {
					return nil, "", err
				}
			}
			if next := nextPhase(wt, "quote"); next != "" {
				if _, err := transition(db, pid, w, next, actor, "quote accepted", nil); err != nil {
					return nil, "", err
				}
			}
		}
		event = "quote.accepted"
	case "decline":
		if err := setQuoteStatus(db, w.ID, "declined"); err != nil {
			return nil, "", err
		}
		event = "quote.declined"
	default:
		return nil, "", errors.New("decision must be approve, send, accept or decline")
	}
	_ = logEvent(db, pid, w.ID, actor, "quote", "", decision, map[string]any{"reason": reason})
	nw, err := getWork(db, pid, w.ID)
	return nw, event, err
}

func setQuoteStatus(db *sql.DB, id int64, s string) error {
	_, err := db.Exec(`UPDATE work SET quote_status=?, updated_at=? WHERE id=?`, s, nowUTC(), id)
	return err
}

func countLines(db *sql.DB, workID int64, source string) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM work_lines WHERE work_id=? AND source=?`, workID, source).Scan(&n)
	return n
}

// ─── completion ─────────────────────────────────────────────────────

type CompleteResult struct {
	Work      *Work        `json:"work"`
	Actual    *PriceResult `json:"actual,omitempty"`
	Materials []Material   `json:"materials_used,omitempty"`
	Next      string       `json:"next"`
}

// completeWork closes the open slots, records the actual lines (the
// estimate unless items are given), stamps serviced objects, and moves
// the work to its next phase (bill or closed).
func completeWork(db *sql.DB, pid string, w *Work, args map[string]any) (*CompleteResult, error) {
	wt, err := getWorkType(db, pid, w.WorkTypeID, "")
	if err != nil {
		return nil, err
	}
	if w.Phase != "execute" {
		return nil, fmt.Errorf("work is in phase %s; only execute work can be completed", w.Phase)
	}
	// Check required forms before touching anything, so a refused
	// completion leaves the slots as they were.
	var missing []string
	for _, key := range wt.RequiredForms[nextPhase(wt, "execute")] {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM form_responses WHERE work_id=? AND form_key=?`, w.ID, key).Scan(&n)
		if n == 0 {
			missing = append(missing, "form "+key+" must be submitted")
		}
	}
	if len(missing) > 0 {
		return nil, &GuardError{To: nextPhase(wt, "execute"), Reasons: missing}
	}
	now := nowUTC()
	if _, err := db.Exec(`UPDATE slots SET status='done', completed_at=?, started_at=COALESCE(started_at, starts_at), updated_at=?
		WHERE work_id=? AND status IN ('booked','in_progress')`, now, now, w.ID); err != nil {
		return nil, err
	}
	res := &CompleteResult{}
	items := parseItems(args["items"])
	if len(items) == 0 {
		lines, _ := listLines(db, w.ID, "estimate")
		items = itemsFromLines(lines)
	}
	if len(items) > 0 {
		pr, err := priceItems(db, pid, items, priceContextFor(db, pid, w, wt, args))
		if err != nil {
			return nil, err
		}
		if err := replaceLines(db, pid, w.ID, "actual", pr.Lines); err != nil {
			return nil, err
		}
		res.Actual, res.Materials = pr, pr.Materials
	}
	for _, oid := range w.ObjectIDs {
		_, _ = db.Exec(`UPDATE objects SET last_serviced_at=?, updated_at=? WHERE project_id=? AND id=?`, now, now, pid, oid)
	}
	payload := map[string]any{"notes": stringArg(args, "notes")}
	if res.Actual != nil {
		payload["actual_cents"] = res.Actual.TotalCents
	}
	_ = logEvent(db, pid, w.ID, stringArg(args, "actor"), "completed", "", "", payload)
	next := nextPhase(wt, "execute")
	tr, err := transition(db, pid, w, next, stringArg(args, "actor"), "work completed", nil)
	if err != nil {
		return nil, err
	}
	res.Work, res.Next = tr.Work, next
	// Commission jobs owe their commission once the work is done.
	if res.Work.SettlementMode == modeCommission {
		if err := reconcileCommission(db, pid, res.Work, stringArg(args, "actor"), "work completed", false); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func partnerID(w *Work) int64 {
	if w.PartnerResourceID == nil {
		return 0
	}
	return *w.PartnerResourceID
}

// ─── billing bridge ─────────────────────────────────────────────────

func invoiceWork(db *sql.DB, pid string, call appCaller, w *Work, args map[string]any) (*Work, map[string]any, error) {
	// Commission: the contractor invoices the client; record their invoice.
	if w.SettlementMode == modeCommission {
		return recordPartnerInvoice(db, pid, w, args)
	}
	if w.Phase != "bill" {
		return nil, nil, errNotBillable
	}
	if w.InvoiceRef != "" {
		return w, map[string]any{"already_invoiced": true}, nil // idempotent
	}
	lines, err := listLines(db, w.ID, "actual")
	if err != nil {
		return nil, nil, err
	}
	if len(lines) == 0 {
		lines, _ = listLines(db, w.ID, "estimate")
	}
	if len(lines) == 0 {
		return nil, nil, errors.New("no lines to invoice")
	}
	var total int64
	items := []any{}
	for _, l := range lines {
		total += l.TotalCents
		qty, unit := l.Qty, l.UnitPriceCents
		if qty <= 0 || float64(unit)*qty != float64(l.TotalCents) {
			qty, unit = 1, l.TotalCents
		}
		items = append(items, map[string]any{"description": l.Description, "quantity": qty, "unit_price_cents": unit,
			"metadata": map[string]any{"code": l.Code, "kind": l.Kind}})
	}
	ref := stringArg(args, "external_ref")
	info := map[string]any{"total_cents": total}
	switch {
	case ref != "":
		info["mode"] = "external"
	case call == nil:
		return nil, nil, errors.New("billing app not reachable; pass external_ref to record an invoice made elsewhere")
	default:
		if w.CustomerEmail == "" {
			return nil, nil, errors.New("customer email required to invoice through billing (or pass external_ref)")
		}
		var cust struct {
			Customer struct {
				ID int64 `json:"id"`
			} `json:"customer"`
		}
		if err := call("billing", "customers_upsert_by_email", map[string]any{"email": w.CustomerEmail,
			"defaults": map[string]any{"name": firstNonEmpty(w.CustomerName, w.CustomerEmail)}}, &cust); err != nil {
			return nil, nil, fmt.Errorf("billing customer: %w", err)
		}
		var inv struct {
			Invoice struct {
				ID     int64  `json:"id"`
				Number string `json:"number"`
			} `json:"invoice"`
		}
		meta := map[string]any{"source": "operations", "work_id": w.ID, "work_number": w.Number}
		notes := firstNonEmpty(stringArg(args, "notes"), w.Number+" "+w.Summary)
		if w.BrandID > 0 {
			if b, err := getBrand(db, pid, w.BrandID); err == nil {
				meta["brand"] = map[string]any{"key": b.Key, "name": b.Name, "legal_name": b.LegalName, "email": b.Email,
					"phone": b.Phone, "website": b.Website}
				notes = firstNonEmpty(b.LegalName, b.Name) + " · " + notes
			}
		}
		if err := call("billing", "invoices_create_from_prepared_lines", map[string]any{
			"customer_id": cust.Customer.ID, "currency": w.Currency, "line_items": items,
			"notes": notes, "finalize": boolArg(args, "finalize", true), "metadata": meta,
		}, &inv); err != nil {
			return nil, nil, fmt.Errorf("billing invoice: %w", err)
		}
		ref = fmt.Sprintf("billing:%d", inv.Invoice.ID)
		info["mode"], info["invoice_id"], info["invoice_number"] = "billing", inv.Invoice.ID, inv.Invoice.Number
	}
	if _, err := db.Exec(`UPDATE work SET invoice_ref=?, invoiced_cents=?, updated_at=? WHERE id=?`, ref, total, nowUTC(), w.ID); err != nil {
		return nil, nil, err
	}
	w.InvoiceRef = ref
	if _, err := transition(db, pid, w, "invoiced", stringArg(args, "actor"), "", map[string]any{"invoice_ref": ref}); err != nil {
		return nil, nil, err
	}
	info["invoice_ref"] = ref
	nw, err := getWork(db, pid, w.ID)
	return nw, info, err
}

// ─── search & metrics ───────────────────────────────────────────────

func searchWork(db *sql.DB, pid string, args map[string]any) ([]*Work, error) {
	q := `SELECT ` + workCols + workFrom + ` WHERE w.project_id=?`
	params := []any{pid}
	add := func(cond string, v any) {
		q += " AND " + cond
		params = append(params, v)
	}
	if v := stringArg(args, "phase"); v != "" {
		if v == "open" {
			q += " AND w.phase NOT IN ('closed','cancelled')"
		} else {
			add("w.phase=?", v)
		}
	}
	if v := stringArg(args, "status"); v != "" {
		add("w.status=?", v)
	}
	if v := stringArg(args, "brand"); v != "" {
		add("b.key=?", strings.ToLower(v))
	}
	if v := stringArg(args, "work_type"); v != "" {
		add("t.key=?", v)
	}
	if v := stringArg(args, "mode"); v != "" {
		add("w.mode=?", v)
	}
	if v := stringArg(args, "priority"); v != "" {
		add("w.priority=?", v)
	}
	if v := int64Arg(args, "site_id"); v > 0 {
		add("w.site_id=?", v)
	}
	if v := int64Arg(args, "crm_contact_id"); v > 0 {
		add("w.crm_contact_id=?", v)
	}
	if v := stringArg(args, "settlement_mode"); v != "" {
		add("w.settlement_mode=?", v)
	}
	if v := int64Arg(args, "partner_resource_id"); v > 0 {
		add("w.partner_resource_id=?", v)
	}
	if v := stringArg(args, "q"); v != "" {
		like := "%" + strings.ToLower(v) + "%"
		q += " AND (lower(w.summary) LIKE ? OR lower(w.customer_name) LIKE ? OR lower(w.customer_email) LIKE ? OR w.customer_phone LIKE ? OR w.number LIKE ?)"
		params = append(params, like, like, like, like, like)
	}
	if boolArg(args, "sla_breached", false) {
		add("w.sla_due_at IS NOT NULL AND w.sla_due_at<?", nowUTC())
		q += " AND NOT EXISTS (SELECT 1 FROM slots s WHERE s.work_id=w.id AND s.status<>'cancelled')"
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q += fmt.Sprintf(" ORDER BY CASE w.priority WHEN 'p1' THEN 1 WHEN 'p2' THEN 2 WHEN 'p3' THEN 3 ELSE 4 END, w.updated_at DESC LIMIT %d", limit)
	rows, err := db.Query(q, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Work{}
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// metrics reports on the project, or on one brand when brandID > 0.
// Utilisation stays per resource across brands, since resources are
// shared. With brands, by_brand puts each brand's figures side by side.
func metrics(db *sql.DB, pid string, from, to time.Time, brandID int64) (map[string]any, error) {
	f, t := fmtTime(from), fmtTime(to)
	out := map[string]any{"from": f, "to": t}
	// bw narrows a query on work w to the brand.
	bw := ""
	if brandID > 0 {
		bw = fmt.Sprintf(" AND w.brand_id=%d", brandID)
		out["brand"] = brandKey(db, pid, brandID)
	}
	byPhase := map[string]int{}
	rows, err := db.Query(`SELECT phase, COUNT(*) FROM work w WHERE project_id=?`+bw+` GROUP BY phase`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var n int
		_ = rows.Scan(&p, &n)
		byPhase[p] = n
	}
	rows.Close()
	out["open_by_phase"] = byPhase

	var created, closed, cancelled, needsOwner int
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND created_at>=? AND created_at<?`+bw, pid, f, t).Scan(&created)
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND phase='closed' AND closed_at>=? AND closed_at<?`+bw, pid, f, t).Scan(&closed)
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND phase='cancelled' AND closed_at>=? AND closed_at<?`+bw, pid, f, t).Scan(&cancelled)
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND status='needs_owner'`+bw, pid).Scan(&needsOwner)
	out["created"], out["closed"], out["cancelled"], out["waiting_on_owner"] = created, closed, cancelled, needsOwner

	var sent, accepted int
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND quote_status IN ('sent','accepted','declined') AND created_at>=? AND created_at<?`+bw, pid, f, t).Scan(&sent)
	_ = db.QueryRow(`SELECT COUNT(*) FROM work w WHERE project_id=? AND quote_status='accepted' AND created_at>=? AND created_at<?`+bw, pid, f, t).Scan(&accepted)
	if sent > 0 {
		out["quote_acceptance_pct"] = round2(float64(accepted) / float64(sent) * 100)
	}

	var invoiced, cost int64
	// invoiced_cents and margin_pct cover resale jobs, which the brand
	// invoices itself; commission jobs are in gmv, revenue and commissions.
	_ = db.QueryRow(`SELECT COALESCE(SUM(invoiced_cents),0) FROM work w WHERE project_id=? AND settlement_mode<>'commission' AND invoice_ref<>''
		AND updated_at>=? AND updated_at<?`+bw, pid, f, t).Scan(&invoiced)
	_ = db.QueryRow(`SELECT COALESCE(SUM(l.cost_cents),0) FROM work_lines l JOIN work w ON w.id=l.work_id
		WHERE w.project_id=? AND l.source='actual' AND w.settlement_mode<>'commission' AND w.invoice_ref<>'' AND w.updated_at>=? AND w.updated_at<?`+bw,
		pid, f, t).Scan(&cost)
	out["invoiced_cents"] = invoiced
	if invoiced > 0 {
		out["margin_pct"] = round2(float64(invoiced-cost) / float64(invoiced) * 100)
	}
	settlementMetrics(db, pid, f, t, bw, brandID, invoiced, out)

	// Minutes from intake to first slot start, median.
	lead := []float64{}
	rows, err = db.Query(`SELECT w.created_at, MIN(s.starts_at) FROM work w JOIN slots s ON s.work_id=w.id
		WHERE w.project_id=? AND w.created_at>=? AND w.created_at<? AND s.status<>'cancelled'`+bw+` GROUP BY w.id`, pid, f, t)
	if err == nil {
		for rows.Next() {
			var c, s string
			_ = rows.Scan(&c, &s)
			ct, e1 := parseTime(c)
			st, e2 := parseTime(s)
			if e1 == nil && e2 == nil {
				lead = append(lead, st.Sub(ct).Minutes())
			}
		}
		rows.Close()
	}
	if len(lead) > 0 {
		sort.Float64s(lead)
		out["median_minutes_to_first_slot"] = math.Round(lead[len(lead)/2])
	}

	// Utilisation: booked minutes / shift minutes per shift-bound resource.
	util := []map[string]any{}
	res, _ := listResources(db, pid, "", 0, true)
	for _, r := range res {
		if !r.NeedsShift {
			continue
		}
		shift := overlapMinutes(db, `SELECT starts_at, ends_at FROM resource_blocks WHERE resource_id=? AND kind='shift' AND ends_at>? AND starts_at<?`, r.ID, from, to)
		booked := overlapMinutes(db, `SELECT s.starts_at, s.ends_at FROM slots s JOIN slot_resources sr ON sr.slot_id=s.id
			WHERE sr.resource_id=? AND s.status IN ('booked','in_progress','done') AND s.ends_at>? AND s.starts_at<?`, r.ID, from, to)
		if shift > 0 {
			util = append(util, map[string]any{"resource_id": r.ID, "name": r.Name, "shift_minutes": shift, "booked_minutes": booked,
				"utilisation_pct": round2(booked / shift * 100)})
		}
	}
	out["utilisation"] = util

	if brandID == 0 {
		if byBrand := brandBreakdown(db, pid, f, t); len(byBrand) > 0 {
			out["by_brand"] = byBrand
		}
	}
	return out, nil
}

// brandBreakdown gives each brand's created, closed, open and invoiced
// figures for the period. Empty when the project has no brands.
func brandBreakdown(db *sql.DB, pid, f, t string) []map[string]any {
	brands, err := listBrands(db, pid, "")
	if err != nil || len(brands) == 0 {
		return nil
	}
	out := []map[string]any{}
	row := func(id int64, key, name string) {
		var created, closed, open int
		var invoiced, partnerNet, commission int64
		_ = db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND brand_id=? AND created_at>=? AND created_at<?`, pid, id, f, t).Scan(&created)
		_ = db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND brand_id=? AND phase='closed' AND closed_at>=? AND closed_at<?`, pid, id, f, t).Scan(&closed)
		_ = db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND brand_id=? AND phase NOT IN ('closed','cancelled')`, pid, id).Scan(&open)
		_ = db.QueryRow(`SELECT COALESCE(SUM(invoiced_cents),0) FROM work WHERE project_id=? AND brand_id=? AND settlement_mode<>'commission'
			AND invoice_ref<>'' AND updated_at>=? AND updated_at<?`, pid, id, f, t).Scan(&invoiced)
		_ = db.QueryRow(`SELECT COALESCE(SUM(CAST(json_extract(partner_invoice_json,'$.net_cents') AS INTEGER)),0) FROM work WHERE project_id=? AND brand_id=?
			AND settlement_mode='commission' AND invoice_ref<>'' AND updated_at>=? AND updated_at<?`, pid, id, f, t).Scan(&partnerNet)
		_ = db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM commission_lines WHERE project_id=? AND brand_id=? AND status<>'void'
			AND created_at>=? AND created_at<?`, pid, id, f, t).Scan(&commission)
		if id == 0 && created+closed+open == 0 && invoiced == 0 && commission == 0 {
			return // no unbranded work: leave the row out
		}
		out = append(out, map[string]any{"brand": key, "name": name, "created": created, "closed": closed, "open": open, "invoiced_cents": invoiced,
			"gmv_cents": invoiced + partnerNet, "commission_cents": commission, "revenue_cents": invoiced + commission})
	}
	for _, b := range brands {
		row(b.ID, b.Key, b.Name)
	}
	row(0, "", "No brand")
	return out
}

func overlapMinutes(db *sql.DB, q string, id int64, from, to time.Time) float64 {
	rows, err := db.Query(q, id, fmtTime(from), fmtTime(to))
	if err != nil {
		return 0
	}
	defer rows.Close()
	total := 0.0
	for rows.Next() {
		var a, b string
		_ = rows.Scan(&a, &b)
		s, e1 := parseTime(a)
		e, e2 := parseTime(b)
		if e1 != nil || e2 != nil {
			continue
		}
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			total += e.Sub(s).Minutes()
		}
	}
	return total
}
