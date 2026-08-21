package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var pricingModels = map[string]bool{
	"fixed": true, "hourly": true, "per_unit": true,
	"daily": true, "milestone": true, "recurring": true,
}

type payGrade struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id"`
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	Rank                int    `json:"rank"`
	Description         string `json:"description,omitempty"`
	DefaultPricingModel string `json:"default_pricing_model,omitempty"`
	DefaultAmountMinor  int64  `json:"default_amount_minor,omitempty"`
	Currency            string `json:"currency,omitempty"`
	Active              bool   `json:"active"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type workerPayProfile struct {
	WorkerID  int64          `json:"worker_id"`
	PayGrade  *payGrade      `json:"pay_grade"`
	Currency  string         `json:"currency,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	UpdatedAt string         `json:"updated_at"`
}

type standardOffer struct {
	ID               int64           `json:"id"`
	ProjectID        string          `json:"project_id"`
	TemplateID       int64           `json:"template_id"`
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Category         string          `json:"category,omitempty"`
	Visibility       string          `json:"visibility"`
	Status           string          `json:"status"`
	Version          int             `json:"version"`
	CatalogProductID int64           `json:"catalog_product_id,omitempty"`
	Packages         []*offerPackage `json:"packages,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	PublishedAt      string          `json:"published_at,omitempty"`
}

type offerPackage struct {
	ID                  int64          `json:"id"`
	ProjectID           string         `json:"project_id"`
	OfferID             int64          `json:"offer_id"`
	Slug                string         `json:"slug"`
	Name                string         `json:"name"`
	Tier                string         `json:"tier,omitempty"`
	Description         string         `json:"description,omitempty"`
	Scope               map[string]any `json:"scope,omitempty"`
	PricingModel        string         `json:"pricing_model"`
	Quantity            float64        `json:"quantity,omitempty"`
	Unit                string         `json:"unit,omitempty"`
	DeliveryDays        int            `json:"delivery_days,omitempty"`
	Revisions           int            `json:"revisions,omitempty"`
	CustomerAmountMinor int64          `json:"customer_amount_minor,omitempty"`
	Currency            string         `json:"currency,omitempty"`
	CatalogPriceID      int64          `json:"catalog_price_id,omitempty"`
	Active              bool           `json:"active"`
	SortOrder           int            `json:"sort_order"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
}

type rateCard struct {
	ID              int64   `json:"id"`
	ProjectID       string  `json:"project_id"`
	TemplateID      int64   `json:"template_id,omitempty"`
	OfferPackageID  int64   `json:"offer_package_id,omitempty"`
	PayGradeID      int64   `json:"pay_grade_id,omitempty"`
	WorkerID        int64   `json:"worker_id,omitempty"`
	PricingModel    string  `json:"pricing_model"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	Unit            string  `json:"unit,omitempty"`
	MinimumQuantity float64 `json:"minimum_quantity,omitempty"`
	MaximumQuantity float64 `json:"maximum_quantity,omitempty"`
	EffectiveFrom   string  `json:"effective_from"`
	EffectiveUntil  string  `json:"effective_until,omitempty"`
	Status          string  `json:"status"`
	Source          string  `json:"source"`
	Notes           string  `json:"notes,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type rateQuote struct {
	Configured          bool      `json:"configured"`
	WorkerID            int64     `json:"worker_id,omitempty"`
	TemplateID          int64     `json:"template_id,omitempty"`
	OfferPackageID      int64     `json:"offer_package_id,omitempty"`
	PayGrade            *payGrade `json:"pay_grade,omitempty"`
	RateCardID          int64     `json:"rate_card_id,omitempty"`
	PricingModel        string    `json:"pricing_model,omitempty"`
	RateAmountMinor     int64     `json:"rate_amount_minor,omitempty"`
	Quantity            float64   `json:"quantity,omitempty"`
	Unit                string    `json:"unit,omitempty"`
	WorkerAmountMinor   int64     `json:"worker_amount_minor,omitempty"`
	CustomerAmountMinor int64     `json:"customer_amount_minor,omitempty"`
	Currency            string    `json:"currency,omitempty"`
	Source              string    `json:"source,omitempty"`
	Explanation         []string  `json:"explanation"`
}

type jobPost struct {
	ID                int64          `json:"id"`
	ProjectID         string         `json:"project_id"`
	TemplateID        int64          `json:"template_id,omitempty"`
	CustomerContactID int64          `json:"customer_contact_id,omitempty"`
	Title             string         `json:"title"`
	Description       string         `json:"description,omitempty"`
	Scope             map[string]any `json:"scope,omitempty"`
	PricingModels     []string       `json:"pricing_models,omitempty"`
	BudgetMinMinor    int64          `json:"budget_min_minor,omitempty"`
	BudgetMaxMinor    int64          `json:"budget_max_minor,omitempty"`
	Currency          string         `json:"currency,omitempty"`
	DeadlineAt        string         `json:"deadline_at,omitempty"`
	Visibility        string         `json:"visibility"`
	Status            string         `json:"status"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
}

type proposal struct {
	ID             int64            `json:"id"`
	ProjectID      string           `json:"project_id"`
	JobPostID      int64            `json:"job_post_id"`
	WorkerID       int64            `json:"worker_id"`
	OfferPackageID int64            `json:"offer_package_id,omitempty"`
	PricingModel   string           `json:"pricing_model"`
	AmountMinor    int64            `json:"amount_minor"`
	Currency       string           `json:"currency"`
	EstimatedDays  int              `json:"estimated_days,omitempty"`
	Message        string           `json:"message,omitempty"`
	Milestones     []map[string]any `json:"milestones,omitempty"`
	Status         string           `json:"status"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
}

type contract struct {
	ID                  int64                `json:"id"`
	ProjectID           string               `json:"project_id"`
	SourceType          string               `json:"source_type"`
	SourceID            int64                `json:"source_id,omitempty"`
	CustomerContactID   int64                `json:"customer_contact_id,omitempty"`
	WorkerID            int64                `json:"worker_id,omitempty"`
	TemplateID          int64                `json:"template_id,omitempty"`
	OfferID             int64                `json:"offer_id,omitempty"`
	OfferPackageID      int64                `json:"offer_package_id,omitempty"`
	Title               string               `json:"title"`
	Scope               map[string]any       `json:"scope,omitempty"`
	PricingModel        string               `json:"pricing_model"`
	CustomerAmountMinor int64                `json:"customer_amount_minor,omitempty"`
	WorkerAmountMinor   int64                `json:"worker_amount_minor,omitempty"`
	Currency            string               `json:"currency"`
	Quantity            float64              `json:"quantity,omitempty"`
	Unit                string               `json:"unit,omitempty"`
	RevisionLimit       int                  `json:"revision_limit,omitempty"`
	RateSource          string               `json:"rate_source,omitempty"`
	RateCardID          int64                `json:"rate_card_id,omitempty"`
	PayGradeID          int64                `json:"pay_grade_id,omitempty"`
	Status              string               `json:"status"`
	BillingInvoiceID    int64                `json:"billing_invoice_id,omitempty"`
	OrderID             int64                `json:"order_id,omitempty"`
	Terms               map[string]any       `json:"terms,omitempty"`
	Milestones          []*contractMilestone `json:"milestones,omitempty"`
	CreatedAt           string               `json:"created_at"`
	UpdatedAt           string               `json:"updated_at"`
}

type contractMilestone struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id"`
	ContractID          int64  `json:"contract_id"`
	Title               string `json:"title"`
	Description         string `json:"description,omitempty"`
	SortOrder           int    `json:"sort_order"`
	DueAt               string `json:"due_at,omitempty"`
	CustomerAmountMinor int64  `json:"customer_amount_minor,omitempty"`
	WorkerAmountMinor   int64  `json:"worker_amount_minor,omitempty"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	GigID               int64  `json:"gig_id,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type gigCompensation struct {
	ID                  int64     `json:"id"`
	ProjectID           string    `json:"project_id"`
	GigID               int64     `json:"gig_id"`
	ContractID          int64     `json:"contract_id,omitempty"`
	MilestoneID         int64     `json:"milestone_id,omitempty"`
	WorkerID            int64     `json:"worker_id,omitempty"`
	PayGrade            *payGrade `json:"pay_grade,omitempty"`
	RateCardID          int64     `json:"rate_card_id,omitempty"`
	PricingModel        string    `json:"pricing_model"`
	RateAmountMinor     int64     `json:"rate_amount_minor"`
	Quantity            float64   `json:"quantity"`
	Unit                string    `json:"unit,omitempty"`
	WorkerAmountMinor   int64     `json:"worker_amount_minor"`
	CustomerAmountMinor int64     `json:"customer_amount_minor,omitempty"`
	Currency            string    `json:"currency"`
	RateSource          string    `json:"rate_source"`
	OverrideReason      string    `json:"override_reason,omitempty"`
	AgreedAt            string    `json:"agreed_at"`
	PayableStatus       string    `json:"payable_status"`
	PayableBillID       int64     `json:"payable_bill_id,omitempty"`
	PayableError        string    `json:"payable_error,omitempty"`
}

func normaliseCurrency(currency string) (string, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		return "", errors.New("currency must be a 3-letter ISO 4217 code")
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return "", errors.New("currency must be a 3-letter ISO 4217 code")
		}
	}
	return currency, nil
}

func normalisePricingModel(model string) (string, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if !pricingModels[model] {
		return "", fmt.Errorf("unsupported pricing_model %q", model)
	}
	return model, nil
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

func calculateRateTotal(model string, amount int64, quantity float64) int64 {
	if quantity <= 0 {
		quantity = 1
	}
	if model == "fixed" || model == "milestone" {
		return amount
	}
	return int64(math.Round(float64(amount) * quantity))
}

func scanPayGrade(scanner interface{ Scan(...any) error }) (*payGrade, error) {
	g := &payGrade{}
	var desc, model, currency sql.NullString
	var amount sql.NullInt64
	if err := scanner.Scan(&g.ID, &g.ProjectID, &g.Slug, &g.Name, &g.Rank, &desc, &model, &amount, &currency, &g.Active, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.Description, g.DefaultPricingModel, g.DefaultAmountMinor, g.Currency = desc.String, model.String, amount.Int64, currency.String
	return g, nil
}

const payGradeSelect = `SELECT id,project_id,slug,name,rank,description,default_pricing_model,
       default_amount_minor,currency,active,created_at,updated_at FROM pay_grades`

func getPayGrade(db *sql.DB, pid string, id int64) (*payGrade, error) {
	g, err := scanPayGrade(db.QueryRow(payGradeSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

func listPayGrades(db *sql.DB, pid string, includeInactive bool) ([]*payGrade, error) {
	q := payGradeSelect + ` WHERE project_id=?`
	if !includeInactive {
		q += ` AND active=1`
	}
	q += ` ORDER BY rank,name`
	rows, err := db.Query(q, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*payGrade, 0)
	for rows.Next() {
		g, err := scanPayGrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func getWorkerPayProfile(db *sql.DB, pid string, workerID int64) (*workerPayProfile, error) {
	var gradeID int64
	var currency, metadata, updated sql.NullString
	err := db.QueryRow(`SELECT pay_grade_id,currency,metadata_json,updated_at FROM worker_pay_profiles WHERE project_id=? AND worker_id=?`, pid, workerID).Scan(&gradeID, &currency, &metadata, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g, err := getPayGrade(db, pid, gradeID)
	if err != nil {
		return nil, err
	}
	p := &workerPayProfile{WorkerID: workerID, PayGrade: g, Currency: currency.String, UpdatedAt: updated.String}
	_ = parseJSON(metadata.String, &p.Metadata)
	return p, nil
}

func loadOffer(db *sql.DB, pid string, id int64) (*standardOffer, error) {
	o := &standardOffer{}
	var desc, category, published sql.NullString
	var catalogProduct sql.NullInt64
	err := db.QueryRow(`SELECT id,project_id,template_id,slug,name,description,category,visibility,status,version,
        catalog_product_id,created_at,updated_at,published_at FROM standard_offers WHERE project_id=? AND id=? AND archived_at IS NULL`, pid, id).
		Scan(&o.ID, &o.ProjectID, &o.TemplateID, &o.Slug, &o.Name, &desc, &category, &o.Visibility, &o.Status, &o.Version, &catalogProduct, &o.CreatedAt, &o.UpdatedAt, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Description, o.Category, o.CatalogProductID, o.PublishedAt = desc.String, category.String, catalogProduct.Int64, published.String
	packages, err := listOfferPackages(db, pid, id, true)
	if err != nil {
		return nil, err
	}
	o.Packages = packages
	return o, nil
}

func loadOfferBySlug(db *sql.DB, pid, slug string) (*standardOffer, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM standard_offers WHERE project_id=? AND slug=? AND archived_at IS NULL`, pid, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadOffer(db, pid, id)
}

func scanPackage(scanner interface{ Scan(...any) error }) (*offerPackage, error) {
	p := &offerPackage{}
	var tier, desc, scope, unit, currency sql.NullString
	var quantity sql.NullFloat64
	var delivery, revisions, customerAmount, catalogPrice sql.NullInt64
	if err := scanner.Scan(&p.ID, &p.ProjectID, &p.OfferID, &p.Slug, &p.Name, &tier, &desc, &scope,
		&p.PricingModel, &quantity, &unit, &delivery, &revisions, &customerAmount, &currency,
		&catalogPrice, &p.Active, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Tier, p.Description, p.Quantity, p.Unit = tier.String, desc.String, quantity.Float64, unit.String
	p.DeliveryDays, p.Revisions, p.CustomerAmountMinor, p.Currency = int(delivery.Int64), int(revisions.Int64), customerAmount.Int64, currency.String
	p.CatalogPriceID = catalogPrice.Int64
	_ = parseJSON(scope.String, &p.Scope)
	return p, nil
}

const packageSelect = `SELECT id,project_id,offer_id,slug,name,tier,description,scope_json,pricing_model,
       quantity,unit,delivery_days,revisions,customer_amount_minor,currency,catalog_price_id,
       active,sort_order,created_at,updated_at FROM offer_packages`

func getOfferPackage(db *sql.DB, pid string, id int64) (*offerPackage, error) {
	p, err := scanPackage(db.QueryRow(packageSelect+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func listOfferPackages(db *sql.DB, pid string, offerID int64, includeInactive bool) ([]*offerPackage, error) {
	q := packageSelect + ` WHERE project_id=? AND offer_id=?`
	if !includeInactive {
		q += ` AND active=1`
	}
	q += ` ORDER BY sort_order,id`
	rows, err := db.Query(q, pid, offerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*offerPackage, 0)
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func listOffers(db *sql.DB, pid, status, q string, limit int) ([]*standardOffer, error) {
	query := `SELECT id FROM standard_offers WHERE project_id=? AND archived_at IS NULL`
	args := []any{pid}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	if q != "" {
		query += ` AND (lower(name) LIKE ? OR lower(slug) LIKE ? OR lower(description) LIKE ? OR lower(category) LIKE ?)`
		needle := "%" + strings.ToLower(q) + "%"
		args = append(args, needle, needle, needle, needle)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query += ` ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'draft' THEN 1 ELSE 2 END,name LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]*standardOffer, 0, len(ids))
	for _, id := range ids {
		o, err := loadOffer(db, pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func scanRateCard(scanner interface{ Scan(...any) error }) (*rateCard, error) {
	r := &rateCard{}
	var templateID, packageID, gradeID, workerID sql.NullInt64
	var unit, minQty, maxQty, until, notes sql.NullString
	if err := scanner.Scan(&r.ID, &r.ProjectID, &templateID, &packageID, &gradeID, &workerID,
		&r.PricingModel, &r.AmountMinor, &r.Currency, &unit, &minQty, &maxQty,
		&r.EffectiveFrom, &until, &r.Status, &r.Source, &notes, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.TemplateID, r.OfferPackageID, r.PayGradeID, r.WorkerID = templateID.Int64, packageID.Int64, gradeID.Int64, workerID.Int64
	r.Unit, r.EffectiveUntil, r.Notes = unit.String, until.String, notes.String
	if minQty.Valid {
		_, _ = fmt.Sscan(minQty.String, &r.MinimumQuantity)
	}
	if maxQty.Valid {
		_, _ = fmt.Sscan(maxQty.String, &r.MaximumQuantity)
	}
	return r, nil
}

const rateSelect = `SELECT id,project_id,template_id,offer_package_id,pay_grade_id,worker_id,
       pricing_model,amount_minor,currency,unit,CAST(minimum_quantity AS TEXT),CAST(maximum_quantity AS TEXT),
       effective_from,effective_until,status,source,notes,created_at,updated_at FROM rate_cards`

func listRateCards(db *sql.DB, pid string, templateID, packageID, gradeID, workerID int64, includeArchived bool) ([]*rateCard, error) {
	q := rateSelect + ` WHERE project_id=?`
	args := []any{pid}
	for _, f := range []struct {
		name string
		id   int64
	}{{"template_id", templateID}, {"offer_package_id", packageID}, {"pay_grade_id", gradeID}, {"worker_id", workerID}} {
		if f.id > 0 {
			q += ` AND ` + f.name + `=?`
			args = append(args, f.id)
		}
	}
	if !includeArchived {
		q += ` AND status<>'archived'`
	}
	q += ` ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'draft' THEN 1 ELSE 2 END,updated_at DESC,id DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*rateCard, 0)
	for rows.Next() {
		r, err := scanRateCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolveRate implements the documented specificity order:
// worker+package, worker+template, package+grade, template+grade, grade default.
func resolveRate(db *sql.DB, pid string, templateID, packageID, workerID int64, quantity float64, requestedCurrency string) (*rateQuote, error) {
	if quantity <= 0 {
		quantity = 1
	}
	quote := &rateQuote{WorkerID: workerID, TemplateID: templateID, OfferPackageID: packageID, Quantity: quantity, Explanation: []string{}}
	var grade *payGrade
	if workerID > 0 {
		profile, err := getWorkerPayProfile(db, pid, workerID)
		if err != nil {
			return nil, err
		}
		if profile != nil {
			grade = profile.PayGrade
			quote.PayGrade = grade
			if requestedCurrency == "" {
				requestedCurrency = profile.Currency
			}
		}
	}
	requestedCurrency = strings.ToUpper(strings.TrimSpace(requestedCurrency))

	q := rateSelect + ` WHERE project_id=? AND status='active'
        AND (effective_from IS NULL OR datetime(effective_from)<=datetime('now'))
        AND (effective_until IS NULL OR datetime(effective_until)>datetime('now'))
        AND (minimum_quantity IS NULL OR minimum_quantity<=?)
        AND (maximum_quantity IS NULL OR maximum_quantity>=?)`
	args := []any{pid, quantity, quantity}
	if requestedCurrency != "" {
		q += ` AND currency=?`
		args = append(args, requestedCurrency)
	}
	q += ` AND (worker_id=? OR (worker_id IS NULL AND pay_grade_id=?))
        AND (offer_package_id=? OR (offer_package_id IS NULL AND template_id=?) OR (offer_package_id IS NULL AND template_id IS NULL))
        ORDER BY
          CASE WHEN worker_id=? AND offer_package_id=? THEN 0
               WHEN worker_id=? AND template_id=? AND offer_package_id IS NULL THEN 1
               WHEN worker_id IS NULL AND pay_grade_id=? AND offer_package_id=? THEN 2
               WHEN worker_id IS NULL AND pay_grade_id=? AND template_id=? AND offer_package_id IS NULL THEN 3
               WHEN worker_id IS NULL AND pay_grade_id=? AND template_id IS NULL AND offer_package_id IS NULL THEN 4
               ELSE 9 END,
          datetime(effective_from) DESC,id DESC LIMIT 1`
	gradeID := int64(0)
	if grade != nil {
		gradeID = grade.ID
	}
	args = append(args, workerID, gradeID, packageID, templateID,
		workerID, packageID, workerID, templateID, gradeID, packageID, gradeID, templateID, gradeID)
	r, err := scanRateCard(db.QueryRow(q, args...))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && r != nil {
		quote.Configured = true
		quote.RateCardID, quote.PricingModel, quote.RateAmountMinor = r.ID, r.PricingModel, r.AmountMinor
		quote.Currency, quote.Unit, quote.Source = r.Currency, r.Unit, rateSourceLabel(r, workerID, gradeID, templateID, packageID)
		quote.WorkerAmountMinor = calculateRateTotal(r.PricingModel, r.AmountMinor, quantity)
		quote.Explanation = append(quote.Explanation, "Applied "+quote.Source+" rate card")
		if grade != nil {
			quote.Explanation = append(quote.Explanation, "Worker pay grade is "+grade.Name)
		}
		return quote, nil
	}
	if grade != nil && grade.DefaultPricingModel != "" && grade.DefaultAmountMinor > 0 && (requestedCurrency == "" || requestedCurrency == grade.Currency) {
		quote.Configured = true
		quote.PricingModel, quote.RateAmountMinor, quote.Currency = grade.DefaultPricingModel, grade.DefaultAmountMinor, grade.Currency
		quote.WorkerAmountMinor = calculateRateTotal(grade.DefaultPricingModel, grade.DefaultAmountMinor, quantity)
		quote.Source = "pay_grade_default"
		quote.Explanation = append(quote.Explanation, "Applied the "+grade.Name+" pay-grade default")
		return quote, nil
	}
	if grade == nil {
		quote.Explanation = append(quote.Explanation, "Worker has no pay grade")
	} else {
		quote.Explanation = append(quote.Explanation, "No active rate matches the worker, template, package, quantity, and currency")
	}
	return quote, nil
}

func rateSourceLabel(r *rateCard, workerID, gradeID, templateID, packageID int64) string {
	switch {
	case r.WorkerID == workerID && r.OfferPackageID == packageID && packageID > 0:
		return "worker_package_override"
	case r.WorkerID == workerID && r.TemplateID == templateID && templateID > 0:
		return "worker_template_override"
	case r.PayGradeID == gradeID && r.OfferPackageID == packageID && packageID > 0:
		return "package_pay_grade"
	case r.PayGradeID == gradeID && r.TemplateID == templateID && templateID > 0:
		return "template_pay_grade"
	default:
		return "pay_grade_rate"
	}
}

func loadGigCompensation(db *sql.DB, pid string, gigID int64) (*gigCompensation, error) {
	c := &gigCompensation{}
	var contractID, milestoneID, workerID, gradeID, rateID, customerAmount, billID sql.NullInt64
	var unit, reason, payableErr sql.NullString
	err := db.QueryRow(`SELECT id,project_id,gig_id,contract_id,milestone_id,worker_id,pay_grade_id,rate_card_id,
        pricing_model,rate_amount_minor,quantity,unit,worker_amount_minor,customer_amount_minor,currency,
        rate_source,override_reason,agreed_at,payable_status,payable_bill_id,payable_error
        FROM gig_compensation WHERE project_id=? AND gig_id=?`, pid, gigID).Scan(
		&c.ID, &c.ProjectID, &c.GigID, &contractID, &milestoneID, &workerID, &gradeID, &rateID,
		&c.PricingModel, &c.RateAmountMinor, &c.Quantity, &unit, &c.WorkerAmountMinor, &customerAmount,
		&c.Currency, &c.RateSource, &reason, &c.AgreedAt, &c.PayableStatus, &billID, &payableErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ContractID, c.MilestoneID, c.WorkerID, c.RateCardID = contractID.Int64, milestoneID.Int64, workerID.Int64, rateID.Int64
	c.Unit, c.CustomerAmountMinor, c.OverrideReason, c.PayableBillID, c.PayableError = unit.String, customerAmount.Int64, reason.String, billID.Int64, payableErr.String
	if gradeID.Valid {
		c.PayGrade, _ = getPayGrade(db, pid, gradeID.Int64)
	}
	return c, nil
}

func insertGigCompensationTx(tx *sql.Tx, pid string, gigID int64, q *rateQuote, contractID, milestoneID int64, overrideReason string) error {
	if q == nil || !q.Configured {
		return nil
	}
	gradeID := int64(0)
	if q.PayGrade != nil {
		gradeID = q.PayGrade.ID
	}
	_, err := tx.Exec(`INSERT INTO gig_compensation
        (project_id,gig_id,contract_id,milestone_id,worker_id,pay_grade_id,rate_card_id,pricing_model,
         rate_amount_minor,quantity,unit,worker_amount_minor,customer_amount_minor,currency,rate_source,override_reason)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, gigID, nullInt64(contractID), nullInt64(milestoneID),
		nullInt64(q.WorkerID), nullInt64(gradeID), nullInt64(q.RateCardID), q.PricingModel,
		q.RateAmountMinor, q.Quantity, nullStr(q.Unit), q.WorkerAmountMinor, nullInt64(q.CustomerAmountMinor),
		q.Currency, q.Source, nullStr(overrideReason))
	return err
}

func validDateTimeOrEmpty(value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return errors.New("date/time must be RFC3339")
	}
	return nil
}
