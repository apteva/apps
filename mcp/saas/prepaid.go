package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Prepaid plans (billing_mode: prepaid).
//
// A prepaid account is active without a subscription: access depends on the
// balance of its Credits wallet, not on a plan payment. Usage is priced from
// Catalog metered prices linked per plan meter, and the authorize/settle
// tools are the admission hook any metering app (llm today) calls on every
// request. Who does what:
//
//	llm      runs requests and reports token counts
//	credits  holds the money (wallet, holds, caps)
//	catalog  holds the prices
//	saas     turns counts into a price and decides whether a request may run
//
// Credits wallets for currency units are kept in micro-units
// (1 usd = 1,000,000), so a Catalog price of C cents per unit_size U costs
// C*10000/U micro-units per unit. Token prices are usually below a cent per
// token, so configure them with a large unit_size (e.g. 75 cents per 10M
// tokens). Each request is rounded up once to whole micro-units.

const (
	billingModePrepaid     = "prepaid"
	accountSubjectType     = "saas_account"
	microUnitsPerCent      = 10000
	defaultMaxOutputTokens = 4096
	// defaultInputMarginPct pads the input-token estimate when pricing a hold,
	// so a request whose real prompt is larger than llm estimated still fits.
	defaultInputMarginPct = 20
	accountSnapshotTTL    = 5 * time.Second
	planMetersTTL         = 30 * time.Second
	keyStatusActive       = "active"
	keyStatusRevoked      = "revoked"
)

type credentialProvider struct {
	App        string `json:"app"`
	CreateTool string `json:"create_tool"`
	ListTool   string `json:"list_tool,omitempty"`
	RevokeTool string `json:"revoke_tool"`
}

type prepaidSettings struct {
	CreditsUnit             string
	SignupCredit            int64
	SignupCreditExpiresDays int64
	LowBalanceThreshold     int64
	MaxOutputTokens         int64
	InputMarginPct          int64
	Provider                credentialProvider
}

func defaultCredentialProvider() credentialProvider {
	return credentialProvider{App: "llm", CreateTool: "llm_tokens_create", ListTool: "llm_tokens_list", RevokeTool: "llm_tokens_revoke"}
}

func prepaidSettingsFromMetadata(meta map[string]any) prepaidSettings {
	s := prepaidSettings{
		CreditsUnit:             strings.ToLower(strings.TrimSpace(strArg(meta, "credits_unit"))),
		SignupCredit:            int64Arg(meta, "signup_credit"),
		SignupCreditExpiresDays: int64Arg(meta, "signup_credit_expires_days"),
		LowBalanceThreshold:     int64Arg(meta, "low_balance_threshold"),
		MaxOutputTokens:         int64Arg(meta, "max_output_tokens_estimate"),
		InputMarginPct:          defaultInputMarginPct,
		Provider:                defaultCredentialProvider(),
	}
	// Presence check: an explicit 0 turns the margin off; a missing key keeps the default.
	if raw, ok := meta["input_estimate_margin_pct"]; ok && raw != nil {
		s.InputMarginPct = int64Arg(meta, "input_estimate_margin_pct")
	}
	if s.InputMarginPct < 0 {
		s.InputMarginPct = 0
	}
	if raw := mapFromAny(meta["credential_provider"]); len(raw) > 0 {
		s.Provider = credentialProvider{
			App:        strings.TrimSpace(strArg(raw, "app")),
			CreateTool: strings.TrimSpace(strArg(raw, "create_tool")),
			ListTool:   strings.TrimSpace(strArg(raw, "list_tool")),
			RevokeTool: strings.TrimSpace(strArg(raw, "revoke_tool")),
		}
	}
	if s.MaxOutputTokens <= 0 {
		s.MaxOutputTokens = defaultMaxOutputTokens
	}
	return s
}

func prepaidSettingsFor(plan *Plan) prepaidSettings {
	if plan == nil {
		return prepaidSettingsFromMetadata(nil)
	}
	return prepaidSettingsFromMetadata(mapFromAny(plan.Metadata))
}

func isPrepaidPlan(plan *Plan) bool { return plan != nil && plan.BillingMode == billingModePrepaid }

// validatePrepaidPlan runs inside dbPlanUpsert for billing_mode prepaid.
func validatePrepaidPlan(args map[string]any, metadata map[string]any) error {
	if boolArg(args, "subscription_required") {
		return errors.New("prepaid plans cannot require a subscription")
	}
	if int64Arg(args, "catalog_discount_id") != 0 {
		return errors.New("prepaid plans cannot carry a subscription discount")
	}
	if strings.TrimSpace(strArg(metadata, "credits_unit")) == "" {
		return errors.New("prepaid plans require metadata.credits_unit (e.g. usd)")
	}
	for _, key := range []string{"signup_credit", "signup_credit_expires_days", "low_balance_threshold", "max_output_tokens_estimate", "input_estimate_margin_pct"} {
		if int64Arg(metadata, key) < 0 {
			return fmt.Errorf("metadata.%s must not be negative", key)
		}
	}
	if raw, ok := metadata["credential_provider"]; ok && raw != nil {
		p := prepaidSettingsFromMetadata(metadata).Provider
		if p.App == "" || p.CreateTool == "" || p.RevokeTool == "" {
			return errors.New("metadata.credential_provider requires app, create_tool and revoke_tool")
		}
	}
	return nil
}

func walletRef(pid, accountID, unit string) map[string]any {
	return map[string]any{"_project_id": pid, "subject_type": accountSubjectType, "subject_id": accountID, "unit": unit}
}

// ensurePrepaidWallet is the provisioning step for prepaid plans: one wallet
// per account, plus the optional signup credit (idempotent by source_ref).
func (a *App) ensurePrepaidWallet(ctx *sdk.AppCtx, pid string, acct *Account, customer *Customer, plan *Plan) (any, error) {
	s := prepaidSettingsFor(plan)
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	input := walletRef(pid, acct.ID, s.CreditsUnit)
	input["metadata"] = map[string]any{"source_app": "saas", "saas_account_id": acct.ID, "plan_key": plan.Key}
	if s.LowBalanceThreshold > 0 {
		input["low_threshold"] = s.LowBalanceThreshold
	}
	if customer != nil && int64PtrValue(customer.BillingCustomerID) != 0 {
		input["billing_customer_id"] = int64PtrValue(customer.BillingCustomerID)
	}
	var wallet map[string]any
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_ensure", input, &wallet); err != nil {
		return nil, fmt.Errorf("credits wallet: %w", err)
	}
	out := map[string]any{"wallet_id": unwrapMap(wallet, "wallet")["id"], "unit": s.CreditsUnit}
	if s.SignupCredit > 0 {
		grant := walletRef(pid, acct.ID, s.CreditsUnit)
		grant["amount"] = s.SignupCredit
		grant["source"] = "promo"
		grant["source_ref"] = "saas:signup:" + acct.ID
		grant["metadata"] = map[string]any{"source_app": "saas", "saas_account_id": acct.ID, "plan_key": plan.Key, "reason": "signup_credit"}
		if s.SignupCreditExpiresDays > 0 {
			grant["expires_in_days"] = s.SignupCreditExpiresDays
		}
		var granted map[string]any
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_grant", grant, &granted); err != nil {
			return nil, fmt.Errorf("signup credit: %w", err)
		}
		out["signup_credit"] = s.SignupCredit
	}
	return out, nil
}

// prepaidWallet reads the account's wallet; wallet is nil when none exists.
func (a *App) prepaidWallet(ctx *sdk.AppCtx, pid, accountID, unit string) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_get", walletRef(pid, accountID, unit), &out); err != nil {
		return nil, fmt.Errorf("credits wallet: %w", err)
	}
	return out, nil
}

// ─── Plan meters ─────────────────────────────────────────────────────

type PlanMeter struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	PlanKey            string          `json:"plan_key"`
	SourceApp          string          `json:"source_app"`
	Meter              string          `json:"meter"`
	InputPriceID       *int64          `json:"input_price_id,omitempty"`
	OutputPriceID      *int64          `json:"output_price_id,omitempty"`
	CachedInputPriceID *int64          `json:"cached_input_price_id,omitempty"`
	PerRequestPriceID  *int64          `json:"per_request_price_id,omitempty"`
	UnitPriceID        *int64          `json:"unit_price_id,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

const planMeterColumns = `id, project_id, plan_key, source_app, meter, input_price_id, output_price_id, cached_input_price_id, per_request_price_id, unit_price_id, metadata_json, created_at, updated_at`

func scanPlanMeter(r interface{ Scan(...any) error }) (*PlanMeter, error) {
	var m PlanMeter
	var in, out, cached, per, unit sql.NullInt64
	var meta string
	if err := r.Scan(&m.ID, &m.ProjectID, &m.PlanKey, &m.SourceApp, &m.Meter, &in, &out, &cached, &per, &unit, &meta, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.InputPriceID, m.OutputPriceID, m.CachedInputPriceID, m.PerRequestPriceID = nullInt(in), nullInt(out), nullInt(cached), nullInt(per)
	m.UnitPriceID = nullInt(unit)
	if meta == "" || meta == "null" {
		meta = "{}"
	}
	m.Metadata = json.RawMessage(meta)
	return &m, nil
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func dbPlanMetersList(db *sql.DB, pid, planKey, sourceApp string) ([]*PlanMeter, error) {
	q := `SELECT ` + planMeterColumns + ` FROM saas_plan_meters WHERE project_id=? AND plan_key=?`
	args := []any{pid, planKey}
	if sourceApp != "" {
		q += ` AND source_app=?`
		args = append(args, sourceApp)
	}
	rows, err := db.Query(q+` ORDER BY source_app, meter`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PlanMeter{}
	for rows.Next() {
		m, err := scanPlanMeter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

var meterPriceFields = []string{"input_price_id", "output_price_id", "cached_input_price_id", "per_request_price_id", "unit_price_id"}

func (a *App) toolPlanMeterSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	plan, err := dbPlanGet(ctx.AppDB(), pid, strArg(args, "plan_key"))
	if err != nil || plan == nil {
		return nil, firstErr(err, errors.New("plan not found"))
	}
	if !isPrepaidPlan(plan) {
		return nil, errors.New("usage meters require a prepaid plan")
	}
	sourceApp := strings.TrimSpace(strArg(args, "source_app"))
	meter := strings.TrimSpace(strArg(args, "meter"))
	if sourceApp == "" || meter == "" {
		return nil, errors.New("source_app and meter required")
	}
	unit := prepaidSettingsFor(plan).CreditsUnit
	ids := map[string]int64{}
	for _, field := range meterPriceFields {
		id := int64Arg(args, field)
		if id < 0 {
			return nil, fmt.Errorf("%s must be positive", field)
		}
		if id == 0 {
			continue
		}
		price, err := a.meterPrice(ctx, pid, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if !strings.EqualFold(price.Currency, unit) {
			return nil, fmt.Errorf("%s is priced in %s but the plan's credits_unit is %s", field, strings.ToUpper(price.Currency), unit)
		}
		ids[field] = id
	}
	if len(ids) == 0 {
		return nil, errors.New("at least one of input_price_id, output_price_id, cached_input_price_id, per_request_price_id or unit_price_id is required")
	}
	meterMetadata := mapFromAny(args["metadata"])
	if meterMetadata == nil {
		meterMetadata = map[string]any{}
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO saas_plan_meters
		(project_id, plan_key, source_app, meter, input_price_id, output_price_id, cached_input_price_id, per_request_price_id, unit_price_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, plan_key, source_app, meter) DO UPDATE SET
			input_price_id=excluded.input_price_id,
			output_price_id=excluded.output_price_id,
			cached_input_price_id=excluded.cached_input_price_id,
			per_request_price_id=excluded.per_request_price_id,
			unit_price_id=excluded.unit_price_id,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, plan.Key, sourceApp, meter,
		nullableInt64(ids["input_price_id"]), nullableInt64(ids["output_price_id"]),
		nullableInt64(ids["cached_input_price_id"]), nullableInt64(ids["per_request_price_id"]), nullableInt64(ids["unit_price_id"]),
		jsonOrEmpty(meterMetadata, "{}"))
	if err != nil {
		return nil, err
	}
	pricing.invalidatePlan(pid, plan.Key)
	m, err := scanPlanMeter(ctx.AppDB().QueryRow(`SELECT `+planMeterColumns+` FROM saas_plan_meters WHERE project_id=? AND plan_key=? AND source_app=? AND meter=?`, pid, plan.Key, sourceApp, meter))
	if err != nil {
		return nil, err
	}
	return map[string]any{"meter": m}, nil
}

func (a *App) toolPlanMetersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	planKey := strArg(args, "plan_key")
	if planKey == "" {
		return nil, errors.New("plan_key required")
	}
	meters, err := dbPlanMetersList(ctx.AppDB(), pid, planKey, strings.TrimSpace(strArg(args, "source_app")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"meters": meters, "count": len(meters)}, nil
}

// ─── Pricing caches ──────────────────────────────────────────────────

// meterPriceInfo is the part of a Catalog metered price SaaS needs. Catalog
// prices never change once created, so they are cached for the process.
type meterPriceInfo struct {
	ID              int64  `json:"id"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	UnitSize        int64  `json:"unit_size"`
	Currency        string `json:"currency"`
}

type accountSnapshot struct {
	ID       string
	Status   string
	PlanKey  string
	Prepaid  bool
	Settings prepaidSettings
	Included []includedUsageItem
	// IncludedNextCheck is when the allowance is next due for a grant
	// (zero: due now).
	IncludedNextCheck time.Time
	expires           time.Time
}

type planMeterSet struct {
	meters  map[string]*PlanMeter // source_app + "\x00" + meter
	expires time.Time
}

type pricingCache struct {
	mu       sync.Mutex
	prices   map[string]meterPriceInfo
	accounts map[string]accountSnapshot
	meters   map[string]planMeterSet
}

var pricing = &pricingCache{}

func (c *pricingCache) invalidateAccount(pid, accountID string) {
	c.mu.Lock()
	delete(c.accounts, pid+"\x00"+accountID)
	c.mu.Unlock()
}

func (c *pricingCache) invalidatePlan(pid, planKey string) {
	c.mu.Lock()
	delete(c.meters, pid+"\x00"+planKey)
	for k, s := range c.accounts {
		if strings.HasPrefix(k, pid+"\x00") && s.PlanKey == planKey {
			delete(c.accounts, k)
		}
	}
	c.mu.Unlock()
}

func (a *App) meterPrice(ctx *sdk.AppCtx, pid string, id int64) (meterPriceInfo, error) {
	key := pid + "\x00" + strconv.FormatInt(id, 10)
	pricing.mu.Lock()
	if p, ok := pricing.prices[key]; ok {
		pricing.mu.Unlock()
		return p, nil
	}
	pricing.mu.Unlock()
	if ctx.PlatformAPI() == nil {
		return meterPriceInfo{}, errors.New("platform API unavailable")
	}
	var resp struct {
		Price struct {
			meterPriceInfo
			BillingScheme string `json:"billing_scheme"`
			Active        bool   `json:"active"`
			ArchivedAt    string `json:"archived_at"`
		} `json:"price"`
	}
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get", map[string]any{"_project_id": pid, "id": id}, &resp); err != nil {
		return meterPriceInfo{}, fmt.Errorf("catalog price %d: %w", id, err)
	}
	p := resp.Price
	if p.ID == 0 {
		return meterPriceInfo{}, fmt.Errorf("catalog price %d not found", id)
	}
	if !strings.EqualFold(p.BillingScheme, "metered") || p.UnitSize <= 0 || p.UnitAmountCents < 0 {
		return meterPriceInfo{}, fmt.Errorf("catalog price %d must be metered with a positive unit_size", id)
	}
	if !p.Active || p.ArchivedAt != "" {
		return meterPriceInfo{}, fmt.Errorf("catalog price %d is not active", id)
	}
	info := p.meterPriceInfo
	info.Currency = strings.ToLower(info.Currency)
	pricing.mu.Lock()
	if pricing.prices == nil {
		pricing.prices = map[string]meterPriceInfo{}
	}
	pricing.prices[key] = info
	pricing.mu.Unlock()
	return info, nil
}

// accountSnapshotFor returns the cached status and plan settings used on the
// hot path; nil when the account does not exist.
func (a *App) accountSnapshotFor(ctx *sdk.AppCtx, pid, accountID string) (*accountSnapshot, error) {
	key := pid + "\x00" + accountID
	now := time.Now()
	pricing.mu.Lock()
	if s, ok := pricing.accounts[key]; ok && now.Before(s.expires) {
		pricing.mu.Unlock()
		return &s, nil
	}
	pricing.mu.Unlock()
	var status, planKey, billingMode, planMeta, acctMeta string
	err := ctx.AppDB().QueryRow(`SELECT a.status, a.plan_key, COALESCE(p.billing_mode,''), COALESCE(p.metadata_json,'{}'), COALESCE(a.metadata_json,'{}')
		FROM saas_accounts a LEFT JOIN saas_plans p ON p.project_id=a.project_id AND p.key=a.plan_key
		WHERE a.project_id=? AND a.id=?`, pid, accountID).Scan(&status, &planKey, &billingMode, &planMeta, &acctMeta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	planMetadata := mapFromAny(json.RawMessage(planMeta))
	s := accountSnapshot{
		ID: accountID, Status: status, PlanKey: planKey, Prepaid: billingMode == billingModePrepaid,
		Settings: prepaidSettingsFromMetadata(planMetadata), Included: includedUsageFromMetadata(planMetadata),
		IncludedNextCheck: includedNextCheck(&Account{PlanKey: planKey, Metadata: json.RawMessage(acctMeta)}),
		expires:           now.Add(accountSnapshotTTL),
	}
	pricing.mu.Lock()
	if pricing.accounts == nil {
		pricing.accounts = map[string]accountSnapshot{}
	}
	pricing.accounts[key] = s
	pricing.mu.Unlock()
	return &s, nil
}

// resolvedMeter holds the prices for one meter; nil parts are free.
type resolvedMeter struct {
	Meter       *PlanMeter
	Input       *meterPriceInfo
	Output      *meterPriceInfo
	CachedInput *meterPriceInfo
	PerRequest  *meterPriceInfo
	// Unit prices the meter's own quantity (e.g. per minute).
	Unit *meterPriceInfo
}

// resolveMeter finds the plan's meter for source_app, trying the exact meter
// first and then "<kind>:*" (e.g. model:*) as the plan's default price.
func (a *App) resolveMeter(ctx *sdk.AppCtx, pid, planKey, sourceApp, meter string) (*resolvedMeter, error) {
	key := pid + "\x00" + planKey
	now := time.Now()
	pricing.mu.Lock()
	set, ok := pricing.meters[key]
	pricing.mu.Unlock()
	if !ok || now.After(set.expires) {
		list, err := dbPlanMetersList(ctx.AppDB(), pid, planKey, "")
		if err != nil {
			return nil, err
		}
		set = planMeterSet{meters: map[string]*PlanMeter{}, expires: now.Add(planMetersTTL)}
		for _, m := range list {
			set.meters[m.SourceApp+"\x00"+m.Meter] = m
		}
		pricing.mu.Lock()
		if pricing.meters == nil {
			pricing.meters = map[string]planMeterSet{}
		}
		pricing.meters[key] = set
		pricing.mu.Unlock()
	}
	m := set.meters[sourceApp+"\x00"+meter]
	if m == nil {
		if i := strings.Index(meter, ":"); i > 0 {
			m = set.meters[sourceApp+"\x00"+meter[:i]+":*"]
		}
	}
	if m == nil {
		return nil, nil
	}
	out := &resolvedMeter{Meter: m}
	for _, part := range []struct {
		id  *int64
		dst **meterPriceInfo
	}{{m.InputPriceID, &out.Input}, {m.OutputPriceID, &out.Output}, {m.CachedInputPriceID, &out.CachedInput}, {m.PerRequestPriceID, &out.PerRequest}, {m.UnitPriceID, &out.Unit}} {
		if part.id == nil {
			continue
		}
		p, err := a.meterPrice(ctx, pid, *part.id)
		if err != nil {
			return nil, err
		}
		*part.dst = &p
	}
	return out, nil
}

type usageQuantities struct {
	Input       int64
	Output      int64
	CachedInput int64
	Requests    int64
	// Quantity is the meter's own unit (e.g. minutes), priced by Unit.
	Quantity int64
}

// priceUsage returns the cost in wallet micro-units, rounded up once.
// Cached input tokens are part of Input and use the cached price when one
// is set, otherwise the input price.
func priceUsage(m *resolvedMeter, q usageQuantities) int64 {
	if m == nil {
		return 0
	}
	if q.CachedInput > q.Input {
		q.CachedInput = q.Input
	}
	if q.CachedInput < 0 {
		q.CachedInput = 0
	}
	total := new(big.Rat)
	add := func(qty int64, p *meterPriceInfo) {
		if p == nil || qty <= 0 || p.UnitSize <= 0 {
			return
		}
		num := new(big.Int).Mul(big.NewInt(qty), big.NewInt(p.UnitAmountCents))
		num.Mul(num, big.NewInt(microUnitsPerCent))
		total.Add(total, new(big.Rat).SetFrac(num, big.NewInt(p.UnitSize)))
	}
	cachedPrice := m.CachedInput
	if cachedPrice == nil {
		cachedPrice = m.Input
	}
	add(q.Input-q.CachedInput, m.Input)
	add(q.CachedInput, cachedPrice)
	add(q.Output, m.Output)
	add(q.Requests, m.PerRequest)
	add(q.Quantity, m.Unit)
	ceil := new(big.Int).Add(total.Num(), new(big.Int).Sub(total.Denom(), big.NewInt(1)))
	ceil.Quo(ceil, total.Denom())
	if !ceil.IsInt64() {
		return 1<<62 - 1
	}
	return ceil.Int64()
}

// ─── Admission hook: authorize and settle ────────────────────────────

func tokenMetadata(v any) map[string]any {
	switch t := v.(type) {
	case string:
		var m map[string]any
		_ = json.Unmarshal([]byte(t), &m)
		return m
	default:
		return mapFromAny(v)
	}
}

func usageMeterKey(args map[string]any, modelField string) string {
	if meter := strings.TrimSpace(strArg(args, "meter")); meter != "" {
		return meter
	}
	model := strings.TrimSpace(strArg(args, modelField))
	if model == "" {
		model = strings.TrimSpace(strArg(args, "model"))
	}
	if model == "" {
		return ""
	}
	return "model:" + model
}

func denyUsage(status int, errorType, reason, code string) map[string]any {
	return map[string]any{"allow": false, "http_status": status, "error_type": errorType, "deny_reason": reason, "reason": code}
}

func (a *App) toolUsageAuthorize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(strArg(args, "request_id"))
	if requestID == "" {
		return nil, errors.New("request_id required")
	}
	meta := tokenMetadata(args["token_metadata"])
	accountID := strArg(meta, "account_id")
	if accountID == "" {
		return denyUsage(http.StatusForbidden, "permission_error", "API key is not linked to an account", "no_account"), nil
	}
	snap, err := a.accountSnapshotFor(ctx, pid, accountID)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return denyUsage(http.StatusForbidden, "permission_error", "account not found", "no_account"), nil
	}
	if snap.Status != StatusActive {
		return denyUsage(http.StatusForbidden, "permission_error", "account is "+snap.Status, "account_"+snap.Status), nil
	}
	sourceApp := firstNonEmpty(strings.TrimSpace(strArg(args, "source_app")), "llm")
	meterKey := usageMeterKey(args, "model")
	included := matchIncludedUsage(snap.Included, sourceApp, meterKey)
	if !snap.Prepaid && included == nil {
		return denyUsage(http.StatusForbidden, "permission_error", "account plan is not prepaid", "not_prepaid"), nil
	}
	var meter *resolvedMeter
	if snap.Prepaid {
		meter, err = a.resolveMeter(ctx, pid, snap.PlanKey, sourceApp, meterKey)
		if err != nil {
			return nil, err
		}
	}
	if meter == nil && included == nil {
		return denyUsage(http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("%s has no price on this plan", firstNonEmpty(strings.TrimPrefix(meterKey, "model:"), "request")), "no_price"), nil
	}
	reference := sourceApp + "/request/" + requestID
	scope := ""
	if project := strings.TrimSpace(strArg(meta, "project")); project != "" {
		scope = "project:" + project
	}

	// Quantity meters (an allowance or a per-unit price) always get a hold,
	// so settle can bill the actual quantity: without an estimate one unit
	// is assumed. Their holds live long enough for a long session
	// (hold_seconds, default 24 hours); settle still bills an expired hold.
	quantityMeter := included != nil || (meter != nil && meter.Unit != nil)
	quantity := int64Arg(args, "estimated_quantity")
	if quantity <= 0 {
		quantity = int64Arg(args, "quantity")
	}
	if quantity <= 0 && quantityMeter {
		quantity = 1
	}
	holdTTL := int64Arg(args, "hold_seconds")
	if holdTTL <= 0 && quantityMeter {
		holdTTL = quantityHoldSeconds
	}
	if holdTTL > maxHoldSeconds {
		holdTTL = maxHoldSeconds
	}

	// Included usage is spent first: hold what the allowance covers and
	// price only the rest from the paid wallet. A period that just started
	// is granted here rather than waiting for the worker.
	var incHold, incTaken, incRemaining int64
	if included != nil && quantity > 0 {
		a.ensureIncludedIfDue(ctx, pid, accountID, snap.IncludedNextCheck)
		incHold, incTaken, incRemaining, err = a.holdIncludedUsage(ctx, pid, accountID, included, quantity, reference, scope, holdTTL)
		if err != nil {
			return nil, err
		}
	}
	releaseIncluded := func() {
		if incHold != 0 {
			var out map[string]any
			_ = ctx.PlatformAPI().CallAppResult("credits", "credits_release", map[string]any{"_project_id": pid, "hold_id": incHold}, &out)
		}
	}
	rest := quantity - incTaken
	if rest > 0 && (meter == nil || meter.Unit == nil) {
		releaseIncluded()
		return denyUsage(http.StatusTooManyRequests, "insufficient_quota", "included usage for this period is used up", "included_exhausted"), nil
	}

	maxOutput := int64Arg(args, "max_output_tokens")
	if maxOutput <= 0 {
		maxOutput = snap.Settings.MaxOutputTokens
	}
	var amount int64
	if meter != nil {
		estimatedInput := inputWithMargin(int64Arg(args, "estimated_input_tokens"), snap.Settings.InputMarginPct)
		amount = priceUsage(meter, usageQuantities{Input: estimatedInput, Output: maxOutput, Requests: 1, Quantity: rest})
	}
	var moneyHold, availableAfter int64
	if amount > 0 {
		input := walletRef(pid, accountID, snap.Settings.CreditsUnit)
		input["amount"] = amount
		input["reference"] = reference
		input["metadata"] = map[string]any{"meter": meter.Meter.Meter, "plan_key": snap.PlanKey}
		if scope != "" {
			input["scope"] = scope
		}
		if holdTTL > 0 {
			input["ttl_seconds"] = holdTTL
		}
		var reserve struct {
			Denied bool   `json:"denied"`
			Reason string `json:"reason"`
			HoldID int64  `json:"hold_id"`
			Hold   *struct {
				ID int64 `json:"id"`
			} `json:"hold"`
			AvailableAfter int64 `json:"available_after"`
		}
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_reserve", input, &reserve); err != nil {
			releaseIncluded()
			return nil, fmt.Errorf("credits reserve: %w", err)
		}
		if reserve.Denied {
			releaseIncluded()
			switch reserve.Reason {
			case "frozen":
				return denyUsage(http.StatusForbidden, "permission_error", "credit wallet is frozen", "frozen"), nil
			case "cap_reached":
				return denyUsage(http.StatusTooManyRequests, "insufficient_quota", "spending cap reached", "cap_reached"), nil
			default:
				return denyUsage(http.StatusTooManyRequests, "insufficient_quota", "insufficient credit balance", "no_credit"), nil
			}
		}
		moneyHold = reserve.HoldID
		if moneyHold == 0 && reserve.Hold != nil {
			moneyHold = reserve.Hold.ID
		}
		if moneyHold == 0 {
			releaseIncluded()
			return nil, errors.New("credits reserve returned no hold")
		}
		availableAfter = reserve.AvailableAfter
	}
	if incHold == 0 && moneyHold == 0 {
		return map[string]any{"allow": true, "admission_ref": "", "amount": 0, "max_output_tokens": maxOutput}, nil
	}
	// Return the output limit the hold was priced on. llm caps the provider
	// request at it (max_tokens), so settle can never exceed the hold on
	// output. Without it a request with no max_tokens runs unbounded.
	out := map[string]any{
		"allow": true, "admission_ref": encodeAdmissionRef(incHold, incTaken, moneyHold), "amount": amount,
		"max_output_tokens": maxOutput,
	}
	if moneyHold != 0 {
		out["hold_id"] = moneyHold
		out["available_after"] = availableAfter
	}
	if included != nil {
		out["included_held"] = incTaken
		out["included_remaining"] = incRemaining - incTaken
		out["included_unit"] = included.Unit
	}
	// max_quantity is how much the account can use right now: what the
	// allowance has left plus what the paid balance covers at the unit
	// price. Callers stop the session there (like max_output_tokens);
	// usage past it is reported as unbilled_quantity on settle.
	if quantityMeter {
		maxQty := incRemaining
		unlimited := false
		if meter != nil && meter.Unit != nil {
			perUnit := meter.Unit.UnitAmountCents * microUnitsPerCent
			paid := availableAfter + amount
			if moneyHold == 0 {
				// Nothing was reserved from the paid wallet: read its balance.
				var wallet map[string]any
				if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_get", walletRef(pid, accountID, snap.Settings.CreditsUnit), &wallet); err == nil {
					paid = max(int64Arg(unwrapMap(wallet, "wallet"), "available"), 0)
				}
			}
			if perUnit <= 0 {
				unlimited = true
			} else {
				maxQty += paid * meter.Unit.UnitSize / perUnit
			}
		}
		if !unlimited {
			out["max_quantity"] = maxQty
		}
	}
	return out, nil
}

const (
	// quantityHoldSeconds is the default hold lifetime for quantity meters
	// (calls, sessions). Credits' own default is 15 minutes.
	quantityHoldSeconds = 24 * 60 * 60
	maxHoldSeconds      = 7 * 24 * 60 * 60
)

// holdIncludedUsage holds up to quantity units from the allowance wallet. It
// returns the hold, the units held and the units that were available.
func (a *App) holdIncludedUsage(ctx *sdk.AppCtx, pid, accountID string, item *includedUsageItem, quantity int64, reference, scope string, ttlSeconds int64) (int64, int64, int64, error) {
	var wallet map[string]any
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_get", walletRef(pid, accountID, item.Unit), &wallet); err != nil {
		return 0, 0, 0, fmt.Errorf("credits wallet %s: %w", item.Unit, err)
	}
	w := unwrapMap(wallet, "wallet")
	if w == nil || strArg(w, "status") == "frozen" {
		return 0, 0, 0, nil
	}
	available := int64Arg(w, "available")
	take := quantity
	if take > available {
		take = available
	}
	if take <= 0 {
		return 0, 0, available, nil
	}
	input := walletRef(pid, accountID, item.Unit)
	input["amount"] = take
	input["reference"] = reference + "/included"
	input["metadata"] = map[string]any{"meter": item.key(), "included": true}
	if scope != "" {
		input["scope"] = scope
	}
	if ttlSeconds > 0 {
		input["ttl_seconds"] = ttlSeconds
	}
	var reserve struct {
		Denied bool  `json:"denied"`
		HoldID int64 `json:"hold_id"`
		Hold   *struct {
			ID int64 `json:"id"`
		} `json:"hold"`
	}
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_reserve", input, &reserve); err != nil {
		return 0, 0, available, fmt.Errorf("credits reserve %s: %w", item.Unit, err)
	}
	holdID := reserve.HoldID
	if holdID == 0 && reserve.Hold != nil {
		holdID = reserve.Hold.ID
	}
	if reserve.Denied || holdID == 0 {
		// The balance moved since it was read (another request, or a cap);
		// the rest is priced from the paid wallet.
		return 0, 0, available, nil
	}
	return holdID, take, available, nil
}

// Admission refs: a plain hold id is a paid-wallet hold only (as before
// v0.14.0). With included usage the ref is "inc:<included hold>:<units
// held>:<paid hold or 0>".
func encodeAdmissionRef(incHold, incTaken, moneyHold int64) string {
	if incHold == 0 {
		return strconv.FormatInt(moneyHold, 10)
	}
	return fmt.Sprintf("inc:%d:%d:%d", incHold, incTaken, moneyHold)
}

func decodeAdmissionRef(ref string) (incHold, incTaken, moneyHold int64, ok bool) {
	if !strings.HasPrefix(ref, "inc:") {
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil || id <= 0 {
			return 0, 0, 0, false
		}
		return 0, 0, id, true
	}
	parts := strings.Split(strings.TrimPrefix(ref, "inc:"), ":")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var vals [3]int64
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v < 0 {
			return 0, 0, 0, false
		}
		vals[i] = v
	}
	if vals[0] == 0 {
		return 0, 0, 0, false
	}
	return vals[0], vals[1], vals[2], true
}

func (a *App) toolUsageSettle(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(strArg(args, "admission_ref"))
	if ref == "" {
		return map[string]any{"settled": false, "skipped": "no_admission_ref"}, nil
	}
	incHold, incTaken, moneyHold, ok := decodeAdmissionRef(ref)
	if !ok {
		return map[string]any{"settled": false, "skipped": "foreign_admission_ref"}, nil
	}
	result := map[string]any{"settled": true}
	if moneyHold != 0 {
		result["hold_id"] = moneyHold
	}
	if incHold != 0 {
		result["included_hold_id"] = incHold
	}
	release := func(id int64) error {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_release", map[string]any{"_project_id": pid, "hold_id": id}, &out); err != nil && !holdGone(err) {
			return fmt.Errorf("credits release: %w", err)
		}
		return nil
	}
	if strArg(args, "status") != "completed" {
		for _, id := range []int64{incHold, moneyHold} {
			if id != 0 {
				if err := release(id); err != nil {
					return nil, err
				}
			}
		}
		result["released"] = true
		return result, nil
	}
	quantity := int64Arg(args, "quantity")
	if quantity < 0 {
		quantity = 0
	}
	meta := tokenMetadata(args["token_metadata"])
	accountID := strArg(meta, "account_id")
	snap, err := a.accountSnapshotFor(ctx, pid, accountID)
	if err != nil {
		return nil, err
	}
	sourceApp := firstNonEmpty(strings.TrimSpace(strArg(args, "source_app")), "llm")
	meterKey := usageMeterKey(args, "requested_model")
	reference := sourceApp + "/request/" + firstNonEmpty(strings.TrimSpace(strArg(args, "request_id")), "ref-"+ref)
	var included *includedUsageItem
	if snap != nil {
		included = matchIncludedUsage(snap.Included, sourceApp, meterKey)
	}

	// The allowance covers as much of the quantity as it has left: what was
	// held plus whatever else is available now. Nothing is drawn past it, so
	// the allowance never records a shortfall.
	var incUsed int64
	if incHold != 0 || (included != nil && quantity > 0) {
		incUsed, err = a.settleIncluded(ctx, pid, accountID, included, incHold, incTaken, quantity, reference)
		if err != nil {
			return nil, err
		}
		result["included_used"] = incUsed
	}
	over := quantity - incUsed
	if over < 0 {
		over = 0
	}

	// The rest is priced from the plan's meter and taken from the paid
	// wallet: through the request's hold when it has one, otherwise (or when
	// the hold expired during a long session) directly.
	var meter *resolvedMeter
	if snap != nil && (snap.Prepaid || moneyHold != 0) {
		meter, err = a.resolveMeter(ctx, pid, snap.PlanKey, sourceApp, meterKey)
		if err != nil {
			return nil, err
		}
	}
	if over > 0 && (meter == nil || meter.Unit == nil) {
		// No price for usage past the allowance (e.g. a free plan): it is
		// reported, not billed. Authorize's max_quantity is the limit.
		result["unbilled_quantity"] = over
		over = 0
	}
	var amount int64
	priced := meter != nil
	if priced {
		q := usageQuantities{Quantity: over}
		if moneyHold != 0 {
			q.Input, q.Output, q.CachedInput, q.Requests = int64Arg(args, "input_tokens"), int64Arg(args, "output_tokens"), int64Arg(args, "cached_tokens"), 1
		}
		amount = priceUsage(meter, q)
		result["amount"] = amount
	} else if moneyHold != 0 {
		// The price vanished between authorize and settle (plan or meter
		// changed). The hold was priced when it was authorized, so it is
		// committed in full rather than given away.
		result["unpriced"] = true
	}
	if moneyHold != 0 {
		commit := map[string]any{"_project_id": pid, "hold_id": moneyHold}
		if priced {
			commit["amount"] = amount
		}
		var out struct {
			Committed int64 `json:"committed"`
			Shortfall int64 `json:"shortfall"`
		}
		err := ctx.PlatformAPI().CallAppResult("credits", "credits_commit", commit, &out)
		switch {
		case err == nil:
			result["committed"] = out.Committed
			if out.Shortfall > 0 {
				result["shortfall"] = out.Shortfall
			}
			return result, nil
		case !holdGone(err) || !priced || snap == nil:
			return nil, fmt.Errorf("credits commit: %w", err)
		}
		result["hold_expired"] = true
	}
	if amount > 0 && snap != nil {
		committed, short, err := a.chargeWithoutHold(ctx, pid, accountID, snap.Settings.CreditsUnit, amount, reference+"/settle", nil)
		if err != nil {
			return nil, err
		}
		result["committed"] = committed
		if short > 0 {
			result["shortfall"] = short
		}
	}
	return result, nil
}

// holdGone reports a commit or release on a hold that already expired or
// was released, e.g. a phone session that outlived its hold.
func holdGone(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "is already expired") || strings.Contains(msg, "is already released")
}

// settleIncluded takes up to quantity units from the allowance: the held
// units plus whatever the allowance wallet still has available, never more.
// It returns the units taken.
func (a *App) settleIncluded(ctx *sdk.AppCtx, pid, accountID string, item *includedUsageItem, incHold, incTaken, quantity int64, reference string) (int64, error) {
	available := func() (int64, error) {
		if item == nil {
			return 0, nil
		}
		var wallet map[string]any
		if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_get", walletRef(pid, accountID, item.Unit), &wallet); err != nil {
			return 0, fmt.Errorf("credits wallet %s: %w", item.Unit, err)
		}
		w := unwrapMap(wallet, "wallet")
		if w == nil || strArg(w, "status") == "frozen" {
			return 0, nil
		}
		return max(int64Arg(w, "available"), 0), nil
	}
	if incHold != 0 {
		want := min(quantity, incTaken)
		if quantity > incTaken {
			avail, err := available()
			if err != nil {
				return 0, err
			}
			want = min(quantity, incTaken+avail)
		}
		if want == 0 {
			var out map[string]any
			if err := ctx.PlatformAPI().CallAppResult("credits", "credits_release", map[string]any{"_project_id": pid, "hold_id": incHold}, &out); err != nil && !holdGone(err) {
				return 0, fmt.Errorf("credits release: %w", err)
			}
			return 0, nil
		}
		var out struct {
			Committed int64 `json:"committed"`
		}
		err := ctx.PlatformAPI().CallAppResult("credits", "credits_commit", map[string]any{"_project_id": pid, "hold_id": incHold, "amount": want}, &out)
		if err == nil {
			if out.Committed > 0 {
				return out.Committed, nil
			}
			return want, nil
		}
		if !holdGone(err) || item == nil {
			return 0, fmt.Errorf("credits commit: %w", err)
		}
		// The hold expired and its units went back to the wallet: take
		// them directly below.
	}
	if item == nil || quantity <= 0 {
		return 0, nil
	}
	avail, err := available()
	if err != nil {
		return 0, err
	}
	take := min(quantity, avail)
	if take <= 0 {
		return 0, nil
	}
	committed, _, err := a.chargeWithoutHold(ctx, pid, accountID, item.Unit, take, reference+"/included/settle", map[string]any{"meter": item.key(), "included": true})
	return committed, err
}

// chargeWithoutHold takes amount from a wallet for usage that already
// happened and has no live hold: it holds what the wallet has and commits
// the full amount, so Credits records any rest as a shortfall. The
// reference makes a retry return the same hold and replay the commit.
// It returns the amount committed and the shortfall.
func (a *App) chargeWithoutHold(ctx *sdk.AppCtx, pid, accountID, unit string, amount int64, reference string, metadata map[string]any) (int64, int64, error) {
	if amount <= 0 {
		return 0, 0, nil
	}
	var wallet map[string]any
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_wallet_get", walletRef(pid, accountID, unit), &wallet); err != nil {
		return 0, 0, fmt.Errorf("credits wallet %s: %w", unit, err)
	}
	w := unwrapMap(wallet, "wallet")
	if w == nil {
		return 0, amount, nil
	}
	take := min(amount, int64Arg(w, "available"))
	if take <= 0 {
		return 0, amount, nil
	}
	input := walletRef(pid, accountID, unit)
	input["amount"] = take
	input["reference"] = reference
	if metadata != nil {
		input["metadata"] = metadata
	}
	var reserve struct {
		Denied bool  `json:"denied"`
		HoldID int64 `json:"hold_id"`
		Hold   *struct {
			ID int64 `json:"id"`
		} `json:"hold"`
	}
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_reserve", input, &reserve); err != nil {
		return 0, 0, fmt.Errorf("credits reserve %s: %w", unit, err)
	}
	holdID := reserve.HoldID
	if holdID == 0 && reserve.Hold != nil {
		holdID = reserve.Hold.ID
	}
	if reserve.Denied || holdID == 0 {
		return 0, amount, nil
	}
	var out struct {
		Committed int64 `json:"committed"`
		Shortfall int64 `json:"shortfall"`
	}
	if err := ctx.PlatformAPI().CallAppResult("credits", "credits_commit", map[string]any{"_project_id": pid, "hold_id": holdID, "amount": amount}, &out); err != nil {
		return 0, 0, fmt.Errorf("credits commit: %w", err)
	}
	return out.Committed, out.Shortfall, nil
}

// ─── Account API keys ────────────────────────────────────────────────

type AccountKey struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	AccountID   string `json:"account_id"`
	ProviderApp string `json:"provider_app"`
	TokenID     int64  `json:"token_id"`
	Label       string `json:"label"`
	Space       string `json:"project,omitempty"`
	Display     string `json:"display,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	RevokedAt   string `json:"revoked_at,omitempty"`
}

const accountKeyColumns = `id, project_id, account_id, provider_app, token_id, label, space, display, status, created_at, COALESCE(revoked_at,'')`

func scanAccountKey(r interface{ Scan(...any) error }) (*AccountKey, error) {
	var k AccountKey
	if err := r.Scan(&k.ID, &k.ProjectID, &k.AccountID, &k.ProviderApp, &k.TokenID, &k.Label, &k.Space, &k.Display, &k.Status, &k.CreatedAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	return &k, nil
}

func dbAccountKeys(db *sql.DB, pid, accountID string, includeRevoked bool) ([]*AccountKey, error) {
	q := `SELECT ` + accountKeyColumns + ` FROM saas_account_keys WHERE project_id=? AND account_id=?`
	if !includeRevoked {
		q += ` AND status='active'`
	}
	rows, err := db.Query(q+` ORDER BY created_at DESC, id DESC`, pid, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AccountKey{}
	for rows.Next() {
		k, err := scanAccountKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// accountForKeys loads the account and its plan's credential provider.
func (a *App) accountForKeys(ctx *sdk.AppCtx, pid, accountID string) (*Account, credentialProvider, error) {
	acct, err := dbAccountGet(ctx.AppDB(), pid, accountID)
	if err != nil || acct == nil {
		return nil, credentialProvider{}, firstErr(err, errors.New("account not found"))
	}
	plan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil {
		return nil, credentialProvider{}, err
	}
	return acct, prepaidSettingsFor(plan).Provider, nil
}

func (a *App) toolAccountKeysCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, provider, err := a.accountForKeys(ctx, pid, strArg(args, "account_id"))
	if err != nil {
		return nil, err
	}
	if acct.Status != StatusActive {
		return nil, fmt.Errorf("account is %s", acct.Status)
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	label := strings.TrimSpace(strArg(args, "label"))
	space := strings.TrimSpace(strArg(args, "project"))
	metadata := map[string]any{"account_id": acct.ID}
	if space != "" {
		metadata["project"] = space
	}
	input := map[string]any{
		"_project_id":  pid,
		"subject_type": accountSubjectType,
		"subject_id":   acct.ID,
		"label":        label,
		"metadata":     metadata,
		"admission":    map[string]any{"app": "saas", "authorize_tool": "saas_usage_authorize", "settle_tool": "saas_usage_settle", "fail_mode": "closed"},
	}
	if prefix := strings.TrimSpace(strArg(args, "prefix")); prefix != "" {
		input["prefix"] = prefix
	}
	if scopes, ok := args["scopes"].([]any); ok && len(scopes) > 0 {
		input["scopes"] = scopes
	}
	var created map[string]any
	if err := ctx.PlatformAPI().CallAppResult(provider.App, provider.CreateTool, input, &created); err != nil {
		return nil, fmt.Errorf("create key: %w", err)
	}
	created = firstNonEmptyMap(unwrapMap(created, "token"), created)
	tokenID := int64FromAny(created["id"])
	secret := strFromAny(created["token"])
	if tokenID == 0 || secret == "" {
		return nil, errors.New("credential provider returned no token id or secret")
	}
	key := &AccountKey{
		ID: newID("sak"), ProjectID: pid, AccountID: acct.ID, ProviderApp: provider.App, TokenID: tokenID,
		Label: label, Space: space, Display: strFromAny(created["display"]), Status: keyStatusActive,
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO saas_account_keys (id, project_id, account_id, provider_app, token_id, label, space, display, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, key.ID, pid, acct.ID, key.ProviderApp, tokenID, label, space, key.Display, keyStatusActive); err != nil {
		// The key exists at the provider but SaaS could not record it, so
		// revoke it rather than leave an untracked credential behind.
		var ignored map[string]any
		_ = ctx.PlatformAPI().CallAppResult(provider.App, provider.RevokeTool, map[string]any{"_project_id": pid, "token_id": tokenID}, &ignored)
		return nil, err
	}
	key, _ = scanAccountKey(ctx.AppDB().QueryRow(`SELECT `+accountKeyColumns+` FROM saas_account_keys WHERE id=?`, key.ID))
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "api_key.created", actor(args), map[string]any{"key_id": key.ID, "label": label, "project": space})
	out := map[string]any{"key": key, "token": secret, "secret_shown_once": true}
	if base := strFromAny(created["base_url"]); base != "" {
		out["base_url"] = base
	}
	return out, nil
}

func firstNonEmptyMap(maps ...map[string]any) map[string]any {
	for _, m := range maps {
		if len(m) > 0 {
			return m
		}
	}
	return map[string]any{}
}

func (a *App) toolAccountKeysList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strArg(args, "account_id")
	if acct, err := dbAccountGet(ctx.AppDB(), pid, accountID); err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	keys, err := dbAccountKeys(ctx.AppDB(), pid, accountID, boolArg(args, "include_revoked"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"keys": keys, "count": len(keys)}, nil
}

func (a *App) toolAccountKeysRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, provider, err := a.accountForKeys(ctx, pid, strArg(args, "account_id"))
	if err != nil {
		return nil, err
	}
	key, err := scanAccountKey(ctx.AppDB().QueryRow(`SELECT `+accountKeyColumns+` FROM saas_account_keys WHERE project_id=? AND account_id=? AND id=?`, pid, acct.ID, strArg(args, "key_id")))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("key not found")
	}
	if err != nil {
		return nil, err
	}
	if key.Status == keyStatusRevoked {
		return map[string]any{"key": key, "changed": false}, nil
	}
	if err := a.revokeAccountKey(ctx, pid, provider, key); err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "api_key.revoked", actor(args), map[string]any{"key_id": key.ID})
	key, _ = scanAccountKey(ctx.AppDB().QueryRow(`SELECT `+accountKeyColumns+` FROM saas_account_keys WHERE id=?`, key.ID))
	return map[string]any{"key": key, "changed": true}, nil
}

func (a *App) revokeAccountKey(ctx *sdk.AppCtx, pid string, provider credentialProvider, key *AccountKey) error {
	if ctx.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	app := firstNonEmpty(key.ProviderApp, provider.App)
	tool := provider.RevokeTool
	if app != provider.App {
		tool = defaultCredentialProvider().RevokeTool
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult(app, tool, map[string]any{"_project_id": pid, "token_id": key.TokenID}, &out); err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	_, err := ctx.AppDB().Exec(`UPDATE saas_account_keys SET status=?, revoked_at=CURRENT_TIMESTAMP WHERE id=? AND status=?`, keyStatusRevoked, key.ID, keyStatusActive)
	return err
}

// revokeAllAccountKeys runs when an account is cancelled. Suspension needs
// nothing here: authorize already refuses requests for inactive accounts.
func (a *App) revokeAllAccountKeys(ctx *sdk.AppCtx, pid, accountID string) {
	keys, err := dbAccountKeys(ctx.AppDB(), pid, accountID, false)
	if err != nil || len(keys) == 0 {
		return
	}
	_, provider, err := a.accountForKeys(ctx, pid, accountID)
	if err != nil {
		return
	}
	for _, key := range keys {
		if err := a.revokeAccountKey(ctx, pid, provider, key); err != nil {
			_ = recordEvent(ctx.AppDB(), pid, accountID, "api_key.revoke_failed", "saas", map[string]any{"key_id": key.ID, "error": err.Error()})
			continue
		}
		_ = recordEvent(ctx.AppDB(), pid, accountID, "api_key.revoked", "saas", map[string]any{"key_id": key.ID, "reason": "account_cancelled"})
	}
}

// ─── Account reads ───────────────────────────────────────────────────

// attachWallets adds the Credits wallet to prepaid accounts. Best-effort so
// a missing Credits app never breaks account reads.
func (a *App) attachWallets(ctx *sdk.AppCtx, pid string, accounts []*Account) {
	if ctx.PlatformAPI() == nil {
		return
	}
	plans := map[string]*Plan{}
	for _, acct := range accounts {
		if acct == nil {
			continue
		}
		plan, ok := plans[acct.PlanKey]
		if !ok {
			plan, _ = dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
			plans[acct.PlanKey] = plan
		}
		if !isPrepaidPlan(plan) {
			continue
		}
		if out, err := a.prepaidWallet(ctx, pid, acct.ID, prepaidSettingsFor(plan).CreditsUnit); err == nil {
			acct.Wallet = out
		}
	}
}

// ─── Emails ──────────────────────────────────────────────────────────

func formatWalletAmount(amount int64, unit string) string {
	u := strings.ToUpper(strings.TrimSpace(unit))
	if len(u) == 3 {
		return formatAmountCents(amount/microUnitsPerCent, u)
	}
	return fmt.Sprintf("%d %s", amount, unit)
}

func (a *App) accountForWalletEvent(ctx *sdk.AppCtx, event sdk.Event) (string, *Account) {
	if event.SourceApp != "" && event.SourceApp != "credits" {
		return "", nil
	}
	if strFromAny(event.Data["subject_type"]) != accountSubjectType {
		return "", nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	acct, err := dbAccountGet(ctx.AppDB(), pid, strFromAny(event.Data["subject_id"]))
	if err != nil || acct == nil {
		return "", nil
	}
	return pid, acct
}

func (a *App) handleCreditsBalanceLow(ctx *sdk.AppCtx, event sdk.Event) error {
	pid, acct := a.accountForWalletEvent(ctx, event)
	if acct == nil {
		return nil
	}
	unit := strFromAny(event.Data["unit"])
	balance := formatWalletAmount(int64FromAny(event.Data["available"]), unit)
	planName := a.planDisplayName(ctx, pid, acct.PlanKey)
	body := fmt.Sprintf("Your %s balance is low: %s left.\n\nTop up to keep your requests running. When the balance reaches zero, new requests are refused.", planName, balance)
	bodyHTML := fmt.Sprintf("<p>Your <strong>%s</strong> balance is low: <strong>%s</strong> left.</p><p>Top up to keep your requests running. When the balance reaches zero, new requests are refused.</p>",
		html.EscapeString(planName), html.EscapeString(balance))
	a.sendAccountEmail(ctx, pid, acct, accountEmail{
		Kind:           "credits_low",
		Subject:        "Your balance is running low",
		Body:           body,
		BodyHTML:       bodyHTML,
		IdempotencyKey: fmt.Sprintf("saas:%s:credits_low:%s", acct.ID, time.Now().UTC().Format("2006-01-02")),
		Detail:         map[string]any{"wallet_id": event.Data["wallet_id"], "available": event.Data["available"]},
	})
	return nil
}

func (a *App) handleCreditsTopupPaid(ctx *sdk.AppCtx, event sdk.Event) error {
	pid, acct := a.accountForWalletEvent(ctx, event)
	if acct == nil {
		return nil
	}
	topupID := int64FromAny(event.Data["topup_id"])
	unit := strFromAny(event.Data["unit"])
	paid := formatAmountCents(int64FromAny(event.Data["price_cents"]), firstNonEmpty(strFromAny(event.Data["currency"]), unit))
	added := formatWalletAmount(int64FromAny(event.Data["amount"]), unit)
	balance := formatWalletAmount(int64FromAny(event.Data["available"]), unit)
	body := fmt.Sprintf("Thank you — your top-up is complete.\n\nPaid: %s\nCredit added: %s\nNew balance: %s\n\nNo action is needed.", paid, added, balance)
	bodyHTML := fmt.Sprintf("<p>Thank you — your top-up is complete.</p><p>Paid: <strong>%s</strong><br>Credit added: %s<br>New balance: <strong>%s</strong></p><p>No action is needed.</p>",
		html.EscapeString(paid), html.EscapeString(added), html.EscapeString(balance))
	a.sendAccountEmail(ctx, pid, acct, accountEmail{
		Kind:           "topup_receipt",
		Subject:        "Top-up received",
		Body:           body,
		BodyHTML:       bodyHTML,
		IdempotencyKey: fmt.Sprintf("saas:%s:topup:%d", acct.ID, topupID),
		Detail:         map[string]any{"topup_id": topupID, "invoice_id": event.Data["invoice_id"], "price_cents": event.Data["price_cents"]},
	})
	return nil
}

// ─── Tool definitions ────────────────────────────────────────────────

func (a *App) prepaidMCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "saas_plan_meter_set", Description: "Link a prepaid plan's usage meter (source_app + meter, e.g. llm + model:text-fast, or model:* as the default) to Catalog metered prices for input, output, cached_input and per_request. unit_price_id prices the meter's own quantity (e.g. per minute) for usage reported as estimated_quantity / quantity, including usage beyond a plan's included_usage. Prices must be metered, active and in the plan's credits_unit.", InputSchema: schemaObject(map[string]any{
			"plan_key": strSchema(), "source_app": strSchema(), "meter": strSchema(),
			"input_price_id": intSchema(), "output_price_id": intSchema(), "cached_input_price_id": intSchema(), "per_request_price_id": intSchema(),
			"unit_price_id": intSchema(), "metadata": objSchema(),
		}, []string{"plan_key", "source_app", "meter"}), Handler: a.toolPlanMeterSet},
		{Name: "saas_plan_meters_list", Description: "List a plan's usage meters and their Catalog price links.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "source_app": strSchema()}, []string{"plan_key"}), Handler: a.toolPlanMetersList},
		{Name: "saas_usage_authorize", Description: "Admission hook: price a request's worst case from the account's plan meters and hold that much credit. For quantity meters pass estimated_quantity (e.g. minutes; 1 if omitted); the plan's included_usage for the meter is held first and only the rest is priced from the paid wallet. Quantity holds last hold_seconds (default 24 hours). Returns {allow, admission_ref, max_quantity} (stop the session at max_quantity) or a refusal with http_status and error_type (included_exhausted when the allowance is used up and the plan has no paid price for the meter).", InputSchema: schemaObject(map[string]any{
			"request_id": strSchema(), "token_metadata": objSchema(), "model": strSchema(), "meter": strSchema(), "source_app": strSchema(),
			"estimated_input_tokens": intSchema(), "max_output_tokens": intSchema(),
			"estimated_quantity": intSchema(), "hold_seconds": intSchema(),
		}, []string{"request_id"}), Handler: a.toolUsageAuthorize},
		{Name: "saas_usage_settle", Description: "Admission hook settlement: price the actual usage and commit the hold, or release it when the request failed. Pass request_id and, for quantity meters, the actual quantity: it is taken from the included allowance first (up to what is left), the rest is billed from the paid wallet at the meter's unit price, also when the hold expired during a long session. Usage with no price past the allowance is returned as unbilled_quantity. Safe to retry.", InputSchema: schemaObject(map[string]any{
			"request_id": strSchema(), "admission_ref": strSchema(), "status": strSchema(), "token_metadata": objSchema(),
			"requested_model": strSchema(), "model": strSchema(), "meter": strSchema(), "source_app": strSchema(),
			"input_tokens": intSchema(), "output_tokens": intSchema(), "cached_tokens": intSchema(),
			"quantity": intSchema(),
		}, []string{"admission_ref"}), Handler: a.toolUsageSettle},
		{Name: "saas_account_keys_create", Description: "Issue an API key for an active account through its plan's credential provider (llm by default), linked to the account and its admission hook. The secret is returned once.", InputSchema: schemaObject(map[string]any{
			"account_id": strSchema(), "label": strSchema(), "project": strSchema(), "prefix": strSchema(), "scopes": strArraySchema(),
		}, []string{"account_id"}), Handler: a.toolAccountKeysCreate},
		{Name: "saas_account_keys_list", Description: "List an account's API keys without secrets.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "include_revoked": boolSchema()}, []string{"account_id"}), Handler: a.toolAccountKeysList},
		{Name: "saas_account_keys_revoke", Description: "Revoke one of an account's API keys.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "key_id": strSchema()}, []string{"account_id", "key_id"}), Handler: a.toolAccountKeysRevoke},
	}
}

// inputWithMargin pads an input-token estimate by pct percent, rounding the
// token count up once (15 tokens at 20% -> 18).
func inputWithMargin(tokens, pct int64) int64 {
	if tokens <= 0 || pct <= 0 {
		return tokens
	}
	return (tokens*(100+pct) + 99) / 100
}
