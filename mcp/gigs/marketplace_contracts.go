package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func scanJobPost(scanner interface{ Scan(...any) error }) (*jobPost, error) {
	j := &jobPost{}
	var templateID, customerID, minBudget, maxBudget sql.NullInt64
	var desc, scope, models, currency, deadline sql.NullString
	if err := scanner.Scan(&j.ID, &j.ProjectID, &templateID, &customerID, &j.Title, &desc, &scope, &models,
		&minBudget, &maxBudget, &currency, &deadline, &j.Visibility, &j.Status, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	j.TemplateID, j.CustomerContactID = templateID.Int64, customerID.Int64
	j.Description, j.BudgetMinMinor, j.BudgetMaxMinor, j.Currency, j.DeadlineAt = desc.String, minBudget.Int64, maxBudget.Int64, currency.String, deadline.String
	_ = parseJSON(scope.String, &j.Scope)
	_ = parseJSON(models.String, &j.PricingModels)
	return j, nil
}

const jobPostSelect = `SELECT id,project_id,template_id,customer_contact_id,title,description,scope_json,
 pricing_models_json,budget_min_minor,budget_max_minor,currency,deadline_at,visibility,status,created_at,updated_at FROM job_posts`

func getJobPost(db *sql.DB, pid string, id int64) (*jobPost, error) {
	j, err := scanJobPost(db.QueryRow(jobPostSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

func (a *App) toolJobPostsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	minB, maxB := int64Arg(args, "budget_min_minor"), int64Arg(args, "budget_max_minor")
	if minB < 0 || maxB < 0 || (maxB > 0 && minB > maxB) {
		return nil, errors.New("invalid budget range")
	}
	currency := strArg(args, "currency")
	if minB > 0 || maxB > 0 || currency != "" {
		currency, err = normaliseCurrency(currency)
		if err != nil {
			return nil, err
		}
	}
	deadline := strArg(args, "deadline_at")
	if err = validDateTimeOrEmpty(deadline); err != nil {
		return nil, err
	}
	visibility := strArg(args, "visibility")
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "unlisted" && visibility != "public" {
		return nil, errors.New("invalid visibility")
	}
	status := "draft"
	var published any = nil
	if boolArg(args, "publish", false) {
		status = "open"
		published = "now"
	}
	models := sliceArg(args, "pricing_models")
	for _, v := range models {
		if _, err = normalisePricingModel(strOf(v)); err != nil {
			return nil, err
		}
	}
	query := `INSERT INTO job_posts(project_id,template_id,customer_contact_id,title,description,scope_json,pricing_models_json,budget_min_minor,budget_max_minor,currency,deadline_at,visibility,status,published_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)`
	res, err := ctx.AppDB().Exec(query, pid, nullInt64(int64Arg(args, "template_id")), nullInt64(int64Arg(args, "customer_contact_id")), title, nullStr(strArg(args, "description")), nullStr(mustJSON(mapArg(args, "scope"))), nullStr(mustJSON(models)), nullInt64(minB), nullInt64(maxB), nullStr(currency), nullStr(deadline), visibility, status, published)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	j, err := getJobPost(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("job_post.created", pid, map[string]any{"job_post_id": id, "status": status})
	}
	return map[string]any{"job_post": j}, err
}

func (a *App) toolJobPostsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q := jobPostSelect + ` WHERE project_id=?`
	vals := []any{pid}
	if status := strArg(args, "status"); status != "" {
		q += ` AND status=?`
		vals = append(vals, status)
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	vals = append(vals, limit)
	rows, err := ctx.AppDB().Query(q, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*jobPost, 0)
	for rows.Next() {
		j, e := scanJobPost(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return map[string]any{"job_posts": out}, rows.Err()
}

func scanProposal(scanner interface{ Scan(...any) error }) (*proposal, error) {
	p := &proposal{}
	var pkg sql.NullInt64
	var message, milestones sql.NullString
	var days sql.NullInt64
	if err := scanner.Scan(&p.ID, &p.ProjectID, &p.JobPostID, &p.WorkerID, &pkg, &p.PricingModel, &p.AmountMinor, &p.Currency, &days, &message, &milestones, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.OfferPackageID, p.EstimatedDays, p.Message = pkg.Int64, int(days.Int64), message.String
	_ = parseJSON(milestones.String, &p.Milestones)
	return p, nil
}

const proposalSelect = `SELECT id,project_id,job_post_id,worker_id,offer_package_id,pricing_model,amount_minor,currency,estimated_days,message,milestones_json,status,created_at,updated_at FROM proposals`

func getProposal(db *sql.DB, pid string, id int64) (*proposal, error) {
	p, err := scanProposal(db.QueryRow(proposalSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (a *App) toolProposalsSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	jid, wid := int64Arg(args, "job_post_id"), int64Arg(args, "worker_id")
	if jid == 0 || wid == 0 {
		return nil, errors.New("job_post_id and worker_id required")
	}
	j, err := getJobPost(ctx.AppDB(), pid, jid)
	if err != nil {
		return nil, err
	}
	if j == nil || j.Status != "open" {
		return nil, errors.New("open job post not found")
	}
	w, err := getWorker(ctx.AppDB(), pid, wid)
	if err != nil {
		return nil, err
	}
	if w == nil || w.Status != "active" {
		return nil, errors.New("active worker not found")
	}
	model, err := normalisePricingModel(strArg(args, "pricing_model"))
	if err != nil {
		return nil, err
	}
	amount := int64Arg(args, "amount_minor")
	if amount < 0 {
		return nil, errors.New("amount_minor must be >= 0")
	}
	currency, err := normaliseCurrency(strArg(args, "currency"))
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO proposals(project_id,job_post_id,worker_id,offer_package_id,pricing_model,amount_minor,currency,estimated_days,message,milestones_json,status)
      VALUES(?,?,?,?,?,?,?,?,?,?,'submitted') ON CONFLICT(job_post_id,worker_id) DO UPDATE SET offer_package_id=excluded.offer_package_id,pricing_model=excluded.pricing_model,
      amount_minor=excluded.amount_minor,currency=excluded.currency,estimated_days=excluded.estimated_days,message=excluded.message,milestones_json=excluded.milestones_json,status='submitted',updated_at=CURRENT_TIMESTAMP`, pid, jid, wid, nullInt64(int64Arg(args, "offer_package_id")), model, amount, currency, nullInt64(int64Arg(args, "estimated_days")), nullStr(strArg(args, "message")), nullStr(mustJSON(sliceArg(args, "milestones"))))
	if err != nil {
		return nil, err
	}
	var id int64
	err = ctx.AppDB().QueryRow(`SELECT id FROM proposals WHERE job_post_id=? AND worker_id=?`, jid, wid).Scan(&id)
	if err != nil {
		return nil, err
	}
	p, err := getProposal(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("proposal.submitted", pid, map[string]any{"proposal_id": id, "job_post_id": jid, "worker_id": wid})
	}
	return map[string]any{"proposal": p}, err
}

func (a *App) toolProposalsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	jid := int64Arg(args, "job_post_id")
	if jid == 0 {
		return nil, errors.New("job_post_id required")
	}
	q := proposalSelect + ` WHERE project_id=? AND job_post_id=?`
	vals := []any{pid, jid}
	if s := strArg(args, "status"); s != "" {
		q += ` AND status=?`
		vals = append(vals, s)
	}
	q += ` ORDER BY created_at`
	rows, err := ctx.AppDB().Query(q, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*proposal, 0)
	for rows.Next() {
		p, e := scanProposal(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return map[string]any{"proposals": out}, rows.Err()
}

func (a *App) toolProposalsAccept(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	p, err := getProposal(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Status != "submitted" {
		return nil, errors.New("submitted proposal not found")
	}
	j, err := getJobPost(ctx.AppDB(), pid, p.JobPostID)
	if err != nil {
		return nil, err
	}
	if j == nil || j.Status != "open" {
		return nil, errors.New("job post is not open")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO contracts(project_id,source_type,source_id,customer_contact_id,worker_id,template_id,title,scope_json,pricing_model,worker_amount_minor,currency,status,accepted_at)
      VALUES(?,'proposal',?,?,?,?,?,?,?,?,?,'active',CURRENT_TIMESTAMP)`, pid, p.ID, nullInt64(j.CustomerContactID), p.WorkerID, nullInt64(j.TemplateID), j.Title, nullStr(mustJSON(j.Scope)), p.PricingModel, p.AmountMinor, p.Currency)
	if err != nil {
		return nil, err
	}
	contractID, _ := res.LastInsertId()
	if len(p.Milestones) > 0 {
		for i, m := range p.Milestones {
			title := strings.TrimSpace(strOf(m["title"]))
			if title == "" {
				title = fmt.Sprintf("Milestone %d", i+1)
			}
			currency := strOf(m["currency"])
			if currency == "" {
				currency = p.Currency
			}
			_, err = tx.Exec(`INSERT INTO contract_milestones(project_id,contract_id,title,description,sort_order,due_at,customer_amount_minor,worker_amount_minor,currency)
        VALUES(?,?,?,?,?,?,?,?,?)`, pid, contractID, title, nullStr(strOf(m["description"])), i, nullStr(strOf(m["due_at"])), nullInt64(int64Cast(m["customer_amount_minor"])), nullInt64(int64Cast(m["worker_amount_minor"])), currency)
			if err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.Exec(`UPDATE proposals SET status=CASE WHEN id=? THEN 'accepted' ELSE 'rejected' END,accepted_at=CASE WHEN id=? THEN CURRENT_TIMESTAMP ELSE accepted_at END,updated_at=CURRENT_TIMESTAMP WHERE job_post_id=? AND status='submitted'`, id, id, p.JobPostID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE job_posts SET status='awarded',updated_at=CURRENT_TIMESTAMP,closed_at=CURRENT_TIMESTAMP WHERE id=?`, p.JobPostID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	p, _ = getProposal(ctx.AppDB(), pid, id)
	c, err := loadContract(ctx.AppDB(), pid, contractID)
	if err == nil {
		ctx.EmitWithProject("contract.created", pid, map[string]any{"contract_id": contractID, "source_type": "proposal"})
	}
	return map[string]any{"proposal": p, "contract": c}, err
}

func scanContract(scanner interface{ Scan(...any) error }) (*contract, error) {
	c := &contract{}
	var sourceID, customerID, workerID, templateID, offerID, pkgID, customerAmt, workerAmt, revisionLimit, rateID, gradeID, invoiceID, orderID sql.NullInt64
	var scope, unit, rateSource, terms sql.NullString
	var quantity sql.NullFloat64
	if err := scanner.Scan(&c.ID, &c.ProjectID, &c.SourceType, &sourceID, &customerID, &workerID, &templateID, &offerID, &pkgID, &c.Title, &scope, &c.PricingModel, &customerAmt, &workerAmt, &c.Currency, &quantity, &unit, &revisionLimit, &rateSource, &rateID, &gradeID, &c.Status, &invoiceID, &orderID, &terms, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.SourceID, c.CustomerContactID, c.WorkerID, c.TemplateID, c.OfferID, c.OfferPackageID = sourceID.Int64, customerID.Int64, workerID.Int64, templateID.Int64, offerID.Int64, pkgID.Int64
	c.CustomerAmountMinor, c.WorkerAmountMinor, c.Quantity, c.Unit, c.RevisionLimit = customerAmt.Int64, workerAmt.Int64, quantity.Float64, unit.String, int(revisionLimit.Int64)
	c.RateSource, c.RateCardID, c.PayGradeID, c.BillingInvoiceID, c.OrderID = rateSource.String, rateID.Int64, gradeID.Int64, invoiceID.Int64, orderID.Int64
	_ = parseJSON(scope.String, &c.Scope)
	_ = parseJSON(terms.String, &c.Terms)
	return c, nil
}

const contractSelect = `SELECT id,project_id,source_type,source_id,customer_contact_id,worker_id,template_id,offer_id,offer_package_id,title,scope_json,pricing_model,customer_amount_minor,worker_amount_minor,currency,quantity,unit,revision_limit,rate_source,rate_card_id,pay_grade_id,status,billing_invoice_id,order_id,terms_json,created_at,updated_at FROM contracts`

func loadContract(db *sql.DB, pid string, id int64) (*contract, error) {
	c, err := scanContract(db.QueryRow(contractSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ms, err := listContractMilestones(db, pid, id)
	if err != nil {
		return nil, err
	}
	c.Milestones = ms
	return c, nil
}

func scanMilestone(scanner interface{ Scan(...any) error }) (*contractMilestone, error) {
	m := &contractMilestone{}
	var desc, due sql.NullString
	var ca, wa, gid sql.NullInt64
	if err := scanner.Scan(&m.ID, &m.ProjectID, &m.ContractID, &m.Title, &desc, &m.SortOrder, &due, &ca, &wa, &m.Currency, &m.Status, &gid, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Description, m.DueAt, m.CustomerAmountMinor, m.WorkerAmountMinor, m.GigID = desc.String, due.String, ca.Int64, wa.Int64, gid.Int64
	return m, nil
}

const milestoneSelect = `SELECT id,project_id,contract_id,title,description,sort_order,due_at,customer_amount_minor,worker_amount_minor,currency,status,gig_id,created_at,updated_at FROM contract_milestones`

func listContractMilestones(db *sql.DB, pid string, cid int64) ([]*contractMilestone, error) {
	rows, err := db.Query(milestoneSelect+` WHERE project_id=? AND contract_id=? ORDER BY sort_order,id`, pid, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*contractMilestone, 0)
	for rows.Next() {
		m, e := scanMilestone(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (a *App) toolContractsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	source := strArg(args, "source_type")
	validSource := map[string]bool{"direct": true, "package": true, "order": true, "subscription": true}
	if !validSource[source] {
		return nil, errors.New("source_type must be direct, package, order, or subscription")
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	model, err := normalisePricingModel(strArg(args, "pricing_model"))
	if err != nil {
		return nil, err
	}
	currency, err := normaliseCurrency(strArg(args, "currency"))
	if err != nil {
		return nil, err
	}
	status := "draft"
	var accepted any = nil
	if boolArg(args, "activate", false) {
		status = "active"
		accepted = "now"
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO contracts(project_id,source_type,source_id,customer_contact_id,worker_id,template_id,offer_id,offer_package_id,title,scope_json,pricing_model,customer_amount_minor,worker_amount_minor,currency,quantity,unit,revision_limit,status,billing_invoice_id,order_id,terms_json,accepted_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CASE WHEN ? IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)`, pid, source, nullInt64(int64Arg(args, "source_id")), nullInt64(int64Arg(args, "customer_contact_id")), nullInt64(int64Arg(args, "worker_id")), nullInt64(int64Arg(args, "template_id")), nullInt64(int64Arg(args, "offer_id")), nullInt64(int64Arg(args, "offer_package_id")), title, nullStr(mustJSON(mapArg(args, "scope"))), model, nullInt64(int64Arg(args, "customer_amount_minor")), nullInt64(int64Arg(args, "worker_amount_minor")), currency, nullableFloat(floatArg(args, "quantity", 0)), nullStr(strArg(args, "unit")), nullInt64(int64Arg(args, "revision_limit")), status, nullInt64(int64Arg(args, "billing_invoice_id")), nullInt64(int64Arg(args, "order_id")), nullStr(mustJSON(mapArg(args, "terms"))), accepted)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	c, err := loadContract(ctx.AppDB(), pid, id)
	if err == nil {
		ctx.EmitWithProject("contract.created", pid, map[string]any{"contract_id": id, "source_type": source})
	}
	return map[string]any{"contract": c}, err
}

func (a *App) toolContractsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := loadContract(ctx.AppDB(), pid, int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("contract not found")
	}
	return map[string]any{"contract": c}, nil
}

func (a *App) toolContractsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q := contractSelect + ` WHERE project_id=?`
	vals := []any{pid}
	if s := strArg(args, "status"); s != "" {
		q += ` AND status=?`
		vals = append(vals, s)
	}
	if wid := int64Arg(args, "worker_id"); wid > 0 {
		q += ` AND worker_id=?`
		vals = append(vals, wid)
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	vals = append(vals, limit)
	rows, err := ctx.AppDB().Query(q, vals...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		c, e := scanContract(rows)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		ids = append(ids, c.ID)
	}
	_ = rows.Close()
	out := make([]*contract, 0, len(ids))
	for _, id := range ids {
		c, e := loadContract(ctx.AppDB(), pid, id)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return map[string]any{"contracts": out}, nil
}

func (a *App) toolContractsAddMilestone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "contract_id")
	title := strings.TrimSpace(strArg(args, "title"))
	if cid == 0 || title == "" {
		return nil, errors.New("contract_id and title required")
	}
	c, err := loadContract(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("contract not found")
	}
	currency := strArg(args, "currency")
	if currency == "" {
		currency = c.Currency
	}
	currency, err = normaliseCurrency(currency)
	if err != nil {
		return nil, err
	}
	due := strArg(args, "due_at")
	if err = validDateTimeOrEmpty(due); err != nil {
		return nil, err
	}
	sortOrder := intArg(args, "sort_order", len(c.Milestones))
	res, err := ctx.AppDB().Exec(`INSERT INTO contract_milestones(project_id,contract_id,title,description,sort_order,due_at,customer_amount_minor,worker_amount_minor,currency) VALUES(?,?,?,?,?,?,?,?,?)`, pid, cid, title, nullStr(strArg(args, "description")), sortOrder, nullStr(due), nullInt64(int64Arg(args, "customer_amount_minor")), nullInt64(int64Arg(args, "worker_amount_minor")), currency)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	m, err := scanMilestone(ctx.AppDB().QueryRow(milestoneSelect+` WHERE id=? AND project_id=?`, id, pid))
	if err != nil {
		return nil, err
	}
	c, err = loadContract(ctx.AppDB(), pid, cid)
	return map[string]any{"contract": c, "milestone": m}, err
}

func (a *App) toolContractsDispatchMilestone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid, mid := int64Arg(args, "contract_id"), int64Arg(args, "milestone_id")
	if cid == 0 || mid == 0 {
		return nil, errors.New("contract_id and milestone_id required")
	}
	c, err := loadContract(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	if c == nil || c.Status != "active" {
		return nil, errors.New("active contract not found")
	}
	if c.TemplateID == 0 || c.WorkerID == 0 {
		return nil, errors.New("contract needs template_id and worker_id before dispatch")
	}
	m, err := scanMilestone(ctx.AppDB().QueryRow(milestoneSelect+` WHERE project_id=? AND contract_id=? AND id=?`, pid, cid, mid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("contract milestone not found")
	}
	if err != nil {
		return nil, err
	}
	if m.Status != "pending" || m.GigID != 0 {
		return nil, fmt.Errorf("milestone is %s and cannot be dispatched", m.Status)
	}

	workerAmount := m.WorkerAmountMinor
	if workerAmount == 0 {
		workerAmount = c.WorkerAmountMinor
	}
	quote := &rateQuote{
		Configured:          true,
		WorkerID:            c.WorkerID,
		TemplateID:          c.TemplateID,
		OfferPackageID:      c.OfferPackageID,
		PricingModel:        "milestone",
		RateAmountMinor:     workerAmount,
		Quantity:            1,
		WorkerAmountMinor:   workerAmount,
		CustomerAmountMinor: m.CustomerAmountMinor,
		Currency:            m.Currency,
		Source:              "contract_milestone",
		Explanation:         []string{"accepted contract milestone terms"},
	}
	if profile, e := getWorkerPayProfile(ctx.AppDB(), pid, c.WorkerID); e != nil {
		return nil, e
	} else if profile != nil {
		quote.PayGrade = profile.PayGrade
	}
	if workerAmount == 0 {
		resolved, e := resolveRate(ctx.AppDB(), pid, c.TemplateID, c.OfferPackageID, c.WorkerID, 1, m.Currency)
		if e != nil {
			return nil, e
		}
		if resolved == nil || !resolved.Configured {
			return nil, errors.New("milestone has no worker amount and no standard worker rate resolves")
		}
		quote = resolved
		quote.CustomerAmountMinor = m.CustomerAmountMinor
		quote.Source = "contract_milestone:" + quote.Source
	}

	createArgs := map[string]any{
		"_project_id":         pid,
		"template_id":         c.TemplateID,
		"worker_id":           c.WorkerID,
		"vars":                mapArg(args, "vars"),
		"notify_worker":       boolArg(args, "notify_worker", false),
		"public_domain_id":    int64Arg(args, "public_domain_id"),
		"scheduled_for":       strArg(args, "scheduled_for"),
		"due_at":              strArg(args, "due_at"),
		"deadline_at":         strArg(args, "deadline_at"),
		"access_expires_at":   strArg(args, "access_expires_at"),
		"access_grace_days":   intArg(args, "access_grace_days", 0),
		"priority":            strArg(args, "priority"),
		"_contract_id":        cid,
		"_milestone_id":       mid,
		"_offer_package_id":   c.OfferPackageID,
		"_compensation_quote": quote,
	}
	if createArgs["due_at"] == "" && createArgs["deadline_at"] == "" {
		createArgs["due_at"] = m.DueAt
	}
	created, err := a.toolGigsCreateFromTemplate(ctx, createArgs)
	if err != nil {
		return nil, err
	}
	out, ok := created.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected gig creation result")
	}
	g, ok := out["gig"].(*gig)
	if !ok || g == nil {
		return nil, errors.New("gig creation did not return a gig")
	}
	res, err := ctx.AppDB().Exec(`UPDATE contract_milestones SET status='active',gig_id=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND contract_id=? AND id=? AND status='pending' AND gig_id IS NULL`, g.ID, pid, cid, mid)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("milestone changed while it was being dispatched")
	}
	m, err = scanMilestone(ctx.AppDB().QueryRow(milestoneSelect+` WHERE project_id=? AND id=?`, pid, mid))
	if err != nil {
		return nil, err
	}
	c, err = loadContract(ctx.AppDB(), pid, cid)
	if err != nil {
		return nil, err
	}
	out["contract"], out["milestone"] = c, m
	ctx.EmitWithProject("contract.milestone_dispatched", pid, map[string]any{"contract_id": cid, "milestone_id": mid, "gig_id": g.ID})
	return out, nil
}

// syncContractFromGig keeps the commercial delivery record aligned with the
// operational gig without allowing later contract/rate edits to alter pay.
func syncContractFromGig(db *sql.DB, pid string, gigID int64, gigStatus string) error {
	milestoneStatus := map[string]string{
		"submitted": "submitted",
		"reviewed":  "approved",
		"rejected":  "rejected",
		"cancelled": "cancelled",
		"expired":   "cancelled",
	}[gigStatus]
	if milestoneStatus == "" {
		return nil
	}
	var contractID int64
	err := db.QueryRow(`SELECT contract_id FROM contract_milestones WHERE project_id=? AND gig_id=?`, pid, gigID).Scan(&contractID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = db.Exec(`UPDATE contract_milestones SET status=?,accepted_at=CASE WHEN ?='approved' THEN CURRENT_TIMESTAMP ELSE accepted_at END,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND gig_id=?`, milestoneStatus, milestoneStatus, pid, gigID); err != nil {
		return err
	}
	if milestoneStatus != "approved" {
		return nil
	}
	_, err = db.Exec(`UPDATE contracts SET status='completed',completed_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND status='active'
		AND NOT EXISTS (SELECT 1 FROM contract_milestones WHERE contract_id=? AND status NOT IN ('approved','cancelled'))`, pid, contractID, contractID)
	return err
}

func (a *App) toolGigsCreatePayable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	gid := int64Arg(args, "gig_id")
	if gid == 0 {
		return nil, errors.New("gig_id required")
	}
	c, bill, err := createGigPayable(ctx, pid, gid)
	out := map[string]any{"compensation": c}
	if bill != nil {
		out["bill"] = bill
	}
	return out, err
}
