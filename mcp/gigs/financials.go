package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxFinancialAmount int64 = 9000000000000

type financialAgreement struct {
	ID          string `json:"id"`
	Revision    int    `json:"revision"`
	Model       string `json:"model"`
	AmountMinor *int64 `json:"amount_minor"`
	RateMinor   *int64 `json:"rate_minor"`
	Quantity    string `json:"quantity"`
	Unit        string `json:"unit"`
	Currency    string `json:"currency"`
	Terms       string `json:"terms"`
	PayeeName   string `json:"payee_name"`
	PayeeEmail  string `json:"payee_email"`
	WorkerID    int64  `json:"worker_id"`
	Settlement  string `json:"settlement"`
	ConfirmedAt string `json:"confirmed_at"`
	Evidence    string `json:"evidence"`
	Reason      string `json:"reason"`
	CreatedAt   string `json:"created_at"`
	Actor       string `json:"actor"`
}
type financialExpense struct {
	StorageInstallID int64 `json:"storage_install_id,omitempty"`

	Description string `json:"description"`
	AmountMinor int64  `json:"amount_minor"`
	FileID      int64  `json:"file_id,omitempty"`
}
type financialAllocation struct {
	Kind        string `json:"kind"`
	Reference   string `json:"reference"`
	SiteID      int64  `json:"site_id,omitempty"`
	InstallID   int64  `json:"install_id,omitempty"`
	AmountMinor int64  `json:"amount_minor"`
}
type financialFile struct {
	FileID    int64 `json:"file_id"`
	InstallID int64 `json:"install_id"`
}
type financialApproval struct {
	AdoptLegacyBill bool  `json:"adopt_legacy_bill"`
	LegacyBillID    int64 `json:"legacy_bill_id,omitempty"`

	ID               string                `json:"id"`
	AgreementID      string                `json:"agreement_id"`
	RequestKey       string                `json:"request_key"`
	Kind             string                `json:"kind"`
	ParentID         string                `json:"parent_id,omitempty"`
	AcceptedQuantity string                `json:"accepted_quantity"`
	WorkAmountMinor  *int64                `json:"work_amount_minor"`
	Expenses         []financialExpense    `json:"expenses"`
	TotalMinor       int64                 `json:"total_minor"`
	Currency         string                `json:"currency"`
	Allocations      []financialAllocation `json:"allocations"`
	DeliveredFiles   []financialFile       `json:"delivered_files"`
	DueDate          string                `json:"due_date"`
	Reason           string                `json:"reason"`
	ApprovedAt       string                `json:"approved_at"`
	Actor            string                `json:"actor"`
	Agreement        financialAgreement    `json:"agreement"`
}
type obligationBill struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Currency    string `json:"currency"`
	Total       int64  `json:"total_cents"`
	Paid        int64  `json:"amount_paid_cents"`
	Credit      int64  `json:"credit_minor"`
	Outstanding int64  `json:"outstanding_minor"`
	DueDate     string `json:"due_date"`
	SourceKey   string `json:"source_key"`
}
type financialObligation struct {
	financialApproval
	BillsInstallID int64           `json:"bills_install_id"`
	BillID         int64           `json:"bill_id"`
	SyncStatus     string          `json:"sync_status"`
	SyncError      string          `json:"sync_error,omitempty"`
	SyncedAt       string          `json:"synced_at,omitempty"`
	Bill           *obligationBill `json:"bill"`
}
type gigFinancials struct {
	Agreements      []financialAgreement  `json:"agreements"`
	Obligations     []financialObligation `json:"obligations"`
	Legacy          *gigCompensation      `json:"legacy_compensation,omitempty"`
	BillsConnected  bool                  `json:"bills_connected"`
	ConnectionError string                `json:"connection_error,omitempty"`
}

func financialActor(args map[string]any) string {
	if s := strArg(args, "_caller"); s != "" {
		return s
	}
	return "operator"
}
func financialDecode(args map[string]any, out any) error {
	b, e := json.Marshal(args)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func financeCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// Decimal arithmetic with explicit half-up rounding to the original currency's minor unit.
func financialMultiply(rate int64, q string) (int64, error) {
	if len(q) > 30 || strings.ContainsAny(q, "/eE") {
		return 0, errors.New("quantity must be a plain decimal")
	}
	n, ok := new(big.Rat).SetString(q)
	if !ok || n.Sign() < 0 {
		return 0, errors.New("quantity must be a nonnegative decimal")
	}
	n.Mul(n, new(big.Rat).SetInt64(rate))
	v := new(big.Int).Quo(n.Num(), n.Denom())
	rem := new(big.Int).Rem(n.Num(), n.Denom())
	rem.Mul(rem, big.NewInt(2))
	if rem.Cmp(n.Denom()) >= 0 {
		v.Add(v, big.NewInt(1))
	}
	if !v.IsInt64() || v.Int64() > maxFinancialAmount {
		return 0, errors.New("amount exceeds supported range")
	}
	return v.Int64(), nil
}
func financeIdentity(ctx *sdk.AppCtx) (*sdk.InstallIdentity, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform connection unavailable")
	}
	return ctx.PlatformAPI().WhoAmI()
}
func financeBinding(ctx *sdk.AppCtx, role string) (int64, error) {
	id, e := financeIdentity(ctx)
	if e != nil || id == nil {
		return 0, e
	}
	return int64Cast(id.Bindings[role]), nil
}
func loadFinancials(ctx *sdk.AppCtx, pid string, gid int64, skipConnection ...bool) (*gigFinancials, error) {
	var exists int
	if e := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gigs WHERE id=? AND project_id=?`, gid, pid).Scan(&exists); e != nil {
		return nil, e
	}
	if exists != 1 {
		return nil, errors.New("gig not found")
	}
	f := &gigFinancials{Agreements: []financialAgreement{}, Obligations: []financialObligation{}}
	rows, e := ctx.AppDB().Query(`SELECT snapshot_json,actor,created_at FROM gig_agreements WHERE project_id=? AND gig_id=? ORDER BY revision DESC`, pid, gid)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var raw, actor, date string
		if e = rows.Scan(&raw, &actor, &date); e != nil {
			rows.Close()
			return nil, e
		}
		var v financialAgreement
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			rows.Close()
			return nil, e
		}
		v.Actor = actor
		v.CreatedAt = date
		f.Agreements = append(f.Agreements, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = ctx.AppDB().Query(`SELECT snapshot_json,COALESCE(bills_install_id,0),COALESCE(bill_id,0),sync_status,COALESCE(sync_error,''),COALESCE(synced_at,''),COALESCE(bill_json,'null') FROM gig_obligations WHERE project_id=? AND gig_id=? ORDER BY created_at,id`, pid, gid)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var o financialObligation
		var raw, bill string
		if e = rows.Scan(&raw, &o.BillsInstallID, &o.BillID, &o.SyncStatus, &o.SyncError, &o.SyncedAt, &bill); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &o.financialApproval); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(bill), &o.Bill); e != nil {
			rows.Close()
			return nil, e
		}
		f.Obligations = append(f.Obligations, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	f.Legacy, e = loadGigCompensation(ctx.AppDB(), pid, gid)
	if e != nil {
		return nil, e
	}
	if len(skipConnection) > 0 && skipConnection[0] {
		return f, nil
	}
	binding, err := financeBinding(ctx, "bills")
	f.BillsConnected = binding > 0
	if err != nil {
		f.ConnectionError = "Connection status unavailable"
	}
	return f, nil
}
func (a *App) toolGigsFinancials(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	f, e := loadFinancials(ctx, pid, int64Arg(args, "gig_id"))
	return map[string]any{"financials": f}, e
}
func (a *App) toolGigsAgreement(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	gid := int64Arg(args, "gig_id")
	var v financialAgreement
	if e = financialDecode(args, &v); e != nil {
		return nil, e
	}
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	if v.Model != "fixed" && v.Model != "rate" {
		return nil, errors.New("model must be fixed or rate")
	}
	if v.Settlement != "supplier" && v.Settlement != "payroll" && v.Settlement != "other" {
		return nil, errors.New("settlement must be supplier, payroll or other")
	}
	for _, n := range []*int64{v.AmountMinor, v.RateMinor} {
		if n != nil && (*n < 0 || *n > maxFinancialAmount) {
			return nil, errors.New("amount must be nonnegative and within supported range")
		}
	}
	if (v.AmountMinor != nil || v.RateMinor != nil) && !financeCurrency(v.Currency) {
		return nil, errors.New("original currency is required for known amounts")
	}
	if v.Model == "rate" && v.RateMinor != nil {
		if strings.TrimSpace(v.Unit) == "" {
			return nil, errors.New("unit required for a rate")
		}
		if v.Quantity != "" {
			total, e := financialMultiply(*v.RateMinor, v.Quantity)
			if e != nil {
				return nil, e
			}
			v.AmountMinor = &total
		} else {
			v.AmountMinor = nil
		}
	}
	if v.ConfirmedAt != "" {
		if _, e = time.Parse(time.RFC3339, v.ConfirmedAt); e != nil {
			return nil, errors.New("confirmed_at must be RFC3339")
		}
		if strings.TrimSpace(v.Evidence) == "" {
			return nil, errors.New("confirmation evidence required")
		}
	}
	v.ID = uuid.NewString()
	v.Revision = int(int64Arg(args, "expected_revision")) + 1
	v.Actor = financialActor(args)
	v.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var exists, revision int
	if e = tx.QueryRow(`SELECT COUNT(*) FROM gigs WHERE id=? AND project_id=?`, gid, pid).Scan(&exists); e != nil {
		return nil, e
	}
	if exists != 1 {
		return nil, errors.New("gig not found")
	}
	if e = tx.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM gig_agreements WHERE gig_id=?`, gid).Scan(&revision); e != nil {
		return nil, e
	}
	if v.Revision != revision+1 {
		return nil, errors.New("agreement changed; reload before saving")
	}
	if revision > 0 && strings.TrimSpace(v.Reason) == "" {
		return nil, errors.New("reason required for an agreement amendment")
	}
	if v.WorkerID > 0 {
		var count int
		if e = tx.QueryRow(`SELECT COUNT(*) FROM workers WHERE id=? AND project_id=?`, v.WorkerID, pid).Scan(&count); e != nil {
			return nil, e
		}
		if count != 1 {
			return nil, errors.New("worker not found in project")
		}
	}
	raw, _ := json.Marshal(v)
	_, e = tx.Exec(`INSERT INTO gig_agreements(id,project_id,gig_id,revision,snapshot_json,actor) VALUES(?,?,?,?,?,?)`, v.ID, pid, gid, v.Revision, string(raw), v.Actor)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"agreement": v}, nil
}
func (a *App) toolGigsApproveCompensation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	gid := int64Arg(args, "gig_id")
	var v financialApproval
	if e = financialDecode(args, &v); e != nil {
		return nil, e
	}
	v.LegacyBillID = 0
	for _, name := range []string{"expenses", "allocations"} {
		raw, _ := json.Marshal(args[name])
		var entries []map[string]json.RawMessage
		if e = json.Unmarshal(raw, &entries); e != nil {
			return nil, fmt.Errorf("invalid %s", name)
		}
		for _, entry := range entries {
			value, ok := entry["amount_minor"]
			if !ok || string(value) == "null" {
				return nil, fmt.Errorf("%s require explicit amounts; unknown is not zero", name)
			}
		}
	}

	if v.RequestKey == "" || len(v.RequestKey) > 200 {
		return nil, errors.New("request_key required (maximum 200 characters)")
	}
	// Replay before current agreement validation: a later amendment does not invalidate a completed request.
	var oldRaw string
	e = ctx.AppDB().QueryRow(`SELECT snapshot_json FROM gig_obligations WHERE project_id=? AND request_key=? AND gig_id=?`, pid, v.RequestKey, gid).Scan(&oldRaw)
	if e == nil {
		var old financialApproval
		json.Unmarshal([]byte(oldRaw), &old)
		if !sameApprovalInput(old, v) {
			return nil, errors.New("request_key already used with different approval details")
		}
		return map[string]any{"obligation": old, "was_existing": true}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if v.Kind == "" {
		v.Kind = "base"
	}
	if v.Kind != "base" && v.Kind != "additional" && v.Kind != "credit" {
		return nil, errors.New("kind must be base, additional or credit")
	}
	var agraw string
	e = ctx.AppDB().QueryRow(`SELECT snapshot_json FROM gig_agreements WHERE id=? AND gig_id=? AND project_id=?`, v.AgreementID, gid, pid).Scan(&agraw)
	if e != nil {
		return nil, errors.New("agreement not found")
	}
	if e = json.Unmarshal([]byte(agraw), &v.Agreement); e != nil {
		return nil, e
	}
	if v.Agreement.ConfirmedAt == "" {
		return nil, errors.New("confirm the agreement before approving compensation")
	}
	v.Currency = v.Agreement.Currency
	if !financeCurrency(v.Currency) {
		return nil, errors.New("approved amount requires an original currency")
	}
	if v.WorkAmountMinor == nil || *v.WorkAmountMinor < 0 || *v.WorkAmountMinor > maxFinancialAmount {
		return nil, errors.New("explicit approved work amount required; unknown is not zero")
	}
	if v.AcceptedQuantity == "" {
		return nil, errors.New("accepted_quantity required")
	}
	if _, e = financialMultiply(1, v.AcceptedQuantity); e != nil {
		return nil, e
	}
	if v.Agreement.Model == "rate" && v.Agreement.RateMinor != nil && v.Kind != "credit" {
		expected, e := financialMultiply(*v.Agreement.RateMinor, v.AcceptedQuantity)
		if e != nil {
			return nil, e
		}
		if expected != *v.WorkAmountMinor && strings.TrimSpace(v.Reason) == "" {
			return nil, errors.New("explain why approved compensation differs from rate × accepted quantity")
		}
	}
	v.TotalMinor = *v.WorkAmountMinor
	for _, x := range v.Expenses {
		if x.AmountMinor < 0 || x.AmountMinor > maxFinancialAmount || strings.TrimSpace(x.Description) == "" {
			return nil, errors.New("expenses require a description and nonnegative amount")
		}
		v.TotalMinor += x.AmountMinor
		if v.TotalMinor > maxFinancialAmount {
			return nil, errors.New("approved total exceeds supported range")
		}
	}
	if v.Kind != "base" && (v.ParentID == "" || strings.TrimSpace(v.Reason) == "") {
		return nil, errors.New("adjustments require a parent obligation and reason")
	}
	if v.Agreement.Settlement == "supplier" && v.TotalMinor > 0 && (strings.TrimSpace(v.Agreement.PayeeName) == "" || !strings.Contains(v.Agreement.PayeeEmail, "@")) {
		return nil, errors.New("supplier approval requires payee name and email")
	}
	if v.DueDate != "" {
		if _, e = time.Parse("2006-01-02", v.DueDate); e != nil {
			return nil, errors.New("due_date must be YYYY-MM-DD")
		}
	}
	if e = validateFinancialReferences(ctx, pid, gid, &v); e != nil {
		return nil, e
	}
	v.ID = uuid.NewString()
	v.Actor = financialActor(args)
	v.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var legacyBill int64
	if e = tx.QueryRow(`SELECT COALESCE(MAX(payable_bill_id),0) FROM gig_compensation WHERE gig_id=? AND project_id=?`, gid, pid).Scan(&legacyBill); e != nil {
		return nil, e
	}
	if legacyBill > 0 && v.Kind == "base" {
		if !v.AdoptLegacyBill {
			return nil, errors.New("this gig already has a legacy bill; explicitly adopt it to prevent a duplicate expense")
		}
		v.LegacyBillID = legacyBill
	}

	var status, latest string
	if e = tx.QueryRow(`SELECT status FROM gigs WHERE id=? AND project_id=?`, gid, pid).Scan(&status); e != nil {
		return nil, e
	}
	if status != "reviewed" {
		return nil, errors.New("accept the work before approving compensation")
	}
	if e = tx.QueryRow(`SELECT id FROM gig_agreements WHERE gig_id=? ORDER BY revision DESC LIMIT 1`, gid).Scan(&latest); e != nil {
		return nil, e
	}
	if latest != v.AgreementID {
		return nil, errors.New("agreement was amended; approve the current version")
	}
	var count int
	if e = tx.QueryRow(`SELECT COUNT(*) FROM gig_obligations WHERE gig_id=?`, gid).Scan(&count); e != nil {
		return nil, e
	}
	if v.Kind == "base" && count > 0 {
		return nil, errors.New("gig already has an approval; create an explicit additional obligation or credit")
	}
	if v.Kind != "base" {
		var parentRaw string
		if e = tx.QueryRow(`SELECT snapshot_json FROM gig_obligations WHERE id=? AND gig_id=? AND project_id=?`, v.ParentID, gid, pid).Scan(&parentRaw); e != nil {
			return nil, errors.New("parent obligation not found")
		}
		var parent financialApproval
		json.Unmarshal([]byte(parentRaw), &parent)
		if parent.Currency != v.Currency || parent.Agreement.PayeeEmail != v.Agreement.PayeeEmail || parent.Kind == "credit" {
			return nil, errors.New("adjustment must retain the parent's currency and payee")
		}
		if v.Kind == "credit" {
			if len(v.Expenses) > 0 {
				return nil, errors.New("enter a credit as the approved amount without expenses")
			}
			var credited int64
			if e = tx.QueryRow(`SELECT COALESCE(SUM(json_extract(snapshot_json,'$.total_minor')),0) FROM gig_obligations WHERE gig_id=? AND json_extract(snapshot_json,'$.kind')='credit' AND json_extract(snapshot_json,'$.parent_id')=?`, gid, v.ParentID).Scan(&credited); e != nil {
				return nil, e
			}
			if v.TotalMinor > parent.TotalMinor-credited {
				return nil, errors.New("credit exceeds parent obligation")
			}
		}
	}
	raw, _ := json.Marshal(v)
	syncStatus := "not_connected"
	if v.Agreement.Settlement != "supplier" || v.TotalMinor == 0 {
		syncStatus = "not_applicable"
	}
	_, e = tx.Exec(`INSERT INTO gig_obligations(id,project_id,gig_id,agreement_id,request_key,snapshot_json,actor,sync_status) VALUES(?,?,?,?,?,?,?,?)`, v.ID, pid, gid, v.AgreementID, v.RequestKey, string(raw), v.Actor, syncStatus)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	// The approved snapshot is committed even when the optional integration is offline.
	_ = syncFinancialObligation(ctx, pid, v.ID)
	ctx.EmitWithProject("gig.compensation_approved", pid, map[string]any{"gig_id": gid, "obligation_id": v.ID, "amount_minor": v.TotalMinor, "kind": v.Kind, "currency": v.Currency})
	return a.toolGigsFinancials(ctx, map[string]any{"_project_id": pid, "gig_id": gid})
}
func sameApprovalInput(a, b financialApproval) bool {
	if b.Kind == "" {
		b.Kind = "base"
	}
	return a.AdoptLegacyBill == b.AdoptLegacyBill && a.AgreementID == b.AgreementID && a.Kind == b.Kind && a.ParentID == b.ParentID && a.AcceptedQuantity == b.AcceptedQuantity && stringifyJSON(a.WorkAmountMinor) == stringifyJSON(b.WorkAmountMinor) && sameFinancialExpenses(a.Expenses, b.Expenses) && stringifyJSON(a.Allocations) == stringifyJSON(b.Allocations) && stringifyJSON(a.DeliveredFiles) == stringifyJSON(b.DeliveredFiles) && a.Reason == b.Reason && a.DueDate == b.DueDate
}
func stringifyJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func validateFinancialReferences(ctx *sdk.AppCtx, pid string, gid int64, v *financialApproval) error {
	if len(v.Allocations) > 100 || len(v.DeliveredFiles) > 100 || len(v.Expenses) > 100 {
		return errors.New("maximum 100 allocations, files or expenses")
	}
	var allocated int64
	seen := map[string]bool{}
	for i := range v.Allocations {
		x := &v.Allocations[i]
		if x.AmountMinor < 0 || x.AmountMinor > v.TotalMinor {
			return errors.New("invalid allocation amount")
		}
		allocated += x.AmountMinor
		if x.Kind == "site" {
			binding, e := financeBinding(ctx, "sites")
			if e != nil || binding == 0 || binding != x.InstallID {
				return errors.New("site attribution requires the matching optional Content binding")
			}
			var out struct {
				Site *struct {
					ID int64 `json:"id"`
				} `json:"site"`
			}
			if e = ctx.PlatformAPI().CallAppResult("content", "sites_get", map[string]any{"_project_id": pid, "id": x.SiteID}, &out); e != nil {
				return e
			}
			if out.Site == nil || out.Site.ID != x.SiteID {
				return errors.New("canonical site not found")
			}
			x.Reference = strconv.FormatInt(x.SiteID, 10)
		} else if x.Kind == "" || strings.TrimSpace(x.Reference) == "" {
			return errors.New("allocation kind and reference required")
		}
		key := fmt.Sprintf("%s:%d:%s", x.Kind, x.InstallID, x.Reference)
		if seen[key] {
			return errors.New("duplicate allocation destination")
		}
		seen[key] = true
	}
	if len(v.Allocations) > 0 && allocated != v.TotalMinor {
		return errors.New("allocations must equal approved total; include an explicit unallocated remainder")
	}

	allowed := map[int64]bool{}
	rows, e := ctx.AppDB().Query(`SELECT s.attachment_file_ids_json FROM gig_submissions s JOIN gig_assignments a ON a.id=s.assignment_id JOIN gigs g ON g.id=a.gig_id WHERE a.gig_id=? AND a.status='reviewed' AND s.payload_json=g.result_json`, gid)
	if e != nil {
		return e
	}
	for rows.Next() {
		var raw sql.NullString
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		var ids []int64
		if raw.Valid {
			if e = json.Unmarshal([]byte(raw.String), &ids); e != nil {
				rows.Close()
				return e
			}
		}
		for _, id := range ids {
			allowed[id] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}

	binding, e := financeBinding(ctx, "storage")
	if e != nil && len(v.DeliveredFiles) > 0 {
		return e
	}
	seenFiles := map[int64]bool{}
	for _, f := range v.DeliveredFiles {
		if f.InstallID != binding || binding == 0 || !allowed[f.FileID] || seenFiles[f.FileID] {
			return errors.New("delivered file must be a unique file from this gig's Storage installation")
		}
		seenFiles[f.FileID] = true
	}
	for i := range v.Expenses {
		x := &v.Expenses[i]
		if x.FileID > 0 {
			x.StorageInstallID = binding
			if binding == 0 {
				return errors.New("expense document requires a Storage binding")
			}
			var out map[string]any
			if e = ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"_project_id": pid, "id": x.FileID}, &out); e != nil {
				return fmt.Errorf("expense document: %w", e)
			}
			if out["file"] == nil {
				return errors.New("expense document not found")
			}
		}
	}
	return nil
}

var financialSyncMu sync.Mutex

func syncFinancialObligation(ctx *sdk.AppCtx, pid, id string) error {
	financialSyncMu.Lock()
	defer financialSyncMu.Unlock()
	var raw, status, request, source string
	var bound, billID, vendor, gid int64
	var documentsSynced int
	e := ctx.AppDB().QueryRow(`SELECT snapshot_json,sync_status,COALESCE(request_json,''),COALESCE(source_key,''),COALESCE(bills_install_id,0),COALESCE(bill_id,0),COALESCE(vendor_id,0),gig_id,documents_synced FROM gig_obligations WHERE id=? AND project_id=?`, id, pid).Scan(&raw, &status, &request, &source, &bound, &billID, &vendor, &gid, &documentsSynced)
	if e != nil {
		return e
	}
	if status == "not_applicable" {
		return nil
	}
	var v financialApproval
	if e = json.Unmarshal([]byte(raw), &v); e != nil {
		return e
	}
	fail := func(state string, err error) error {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_, dbErr := ctx.AppDB().Exec(`UPDATE gig_obligations SET sync_status=?,sync_error=?,next_sync_at=datetime('now','+5 minutes') WHERE id=?`, state, message, id)
		if dbErr != nil {
			return dbErr
		}
		return err
	}
	identity, e := financeIdentity(ctx)
	if e != nil || identity == nil {
		if e == nil {
			e = errors.New("platform identity unavailable")
		}
		return fail("unavailable", e)
	}
	current := int64Cast(identity.Bindings["bills"])
	if current == 0 {
		return fail("not_connected", nil)
	}
	if bound > 0 && bound != current {
		return fail("connection_changed", errors.New("the Bills binding changed; restore the original connection to synchronize this obligation"))
	}
	if bound == 0 {
		if !boolArg(map[string]any{"enabled": ctx.Config().Get("auto_create_approved_payables")}, "enabled", true) {
			return fail("disabled", nil)
		}
		if identity.InstallID <= 0 {
			return fail("unavailable", errors.New("Gigs installation identity unavailable"))
		}
		source = fmt.Sprintf("gigs:%d:%s:%d:%s", identity.InstallID, pid, gid, id)
		bound = current
		_, e = ctx.AppDB().Exec(`UPDATE gig_obligations SET bills_install_id=?,source_key=?,sync_status='pending' WHERE id=?`, bound, source, id)
		if e != nil {
			return e
		}
	}
	api := ctx.PlatformAPI()
	var out struct {
		Bill *obligationBill `json:"bill"`
	}
	if v.Kind == "credit" {
		var parentBill, parentInstall int64
		if e = ctx.AppDB().QueryRow(`SELECT COALESCE(bill_id,0),COALESCE(bills_install_id,0) FROM gig_obligations WHERE id=? AND project_id=?`, v.ParentID, pid).Scan(&parentBill, &parentInstall); e != nil {
			return fail("pending", e)
		}
		if parentBill == 0 || parentInstall != bound {
			return fail("pending", errors.New("parent bill must be synchronized before its credit"))
		}
		e = api.CallAppResult("bills", "bills_record_adjustment", map[string]any{"_project_id": pid, "bill_id": parentBill, "request_key": source, "kind": "credit", "amount_minor": v.TotalMinor, "reason": v.Reason}, &out)
	} else if billID > 0 {
		e = api.CallAppResult("bills", "bills_get_obligation", map[string]any{"_project_id": pid, "source_key": source}, &out)
	} else {
		if request == "" {
			if vendor == 0 {
				var vo struct {
					Vendor *billsVendorRef `json:"vendor"`
				}
				e = api.CallAppResult("bills", "vendors_upsert_by_email", map[string]any{"_project_id": pid, "email": v.Agreement.PayeeEmail, "defaults": map[string]any{"name": v.Agreement.PayeeName, "currency": v.Currency}}, &vo)
				if e != nil {
					return fail("pending", e)
				}
				if vo.Vendor == nil || vo.Vendor.ID <= 0 {
					return fail("pending", errors.New("Bills returned no payee"))
				}
				vendor = vo.Vendor.ID
			}
			lines := []any{map[string]any{"description": fmt.Sprintf("Gig %d: accepted work (%s %s)", gid, v.AcceptedQuantity, v.Agreement.Unit), "quantity": 1, "unit_price_cents": *v.WorkAmountMinor, "tax_rate_bps": 0}}
			for _, x := range v.Expenses {
				lines = append(lines, map[string]any{"description": x.Description, "quantity": 1, "unit_price_cents": x.AmountMinor, "tax_rate_bps": 0})
			}
			input := map[string]any{"_project_id": pid, "source_key": source, "vendor_id": vendor, "currency": v.Currency, "line_items": lines, "due_date": v.DueDate, "notes": v.Agreement.Terms, "metadata": map[string]any{"source_app": "gigs", "source_install_id": identity.InstallID, "gig_id": gid, "obligation_id": id, "agreement_id": v.AgreementID, "approval": v}}
			if v.LegacyBillID > 0 {
				input["existing_bill_id"] = v.LegacyBillID
			}
			request = stringifyJSON(input)
			if _, e = ctx.AppDB().Exec(`UPDATE gig_obligations SET vendor_id=?,request_json=? WHERE id=?`, vendor, request, id); e != nil {
				return e
			}
		}
		var input map[string]any
		if e = json.Unmarshal([]byte(request), &input); e != nil {
			return e
		}
		e = api.CallAppResult("bills", "bills_create_obligation", input, &out)
	}
	if e != nil {
		return fail("pending", e)
	}
	if out.Bill == nil || out.Bill.ID <= 0 {
		return fail("pending", errors.New("Bills returned no bill"))
	}
	if out.Bill.Currency != v.Currency || (v.Kind != "credit" && out.Bill.Total != v.TotalMinor) {
		return fail("conflict", errors.New("Bills amount or currency does not match the approved obligation"))
	}
	if v.Kind != "credit" && out.Bill.SourceKey != source {
		return fail("conflict", errors.New("Bills source identity does not match this obligation"))
	}
	// The payable already exists. Keep its durable link even if a later receipt
	// attachment fails, so retries and the UI can still locate the obligation.
	if _, e = ctx.AppDB().Exec(`UPDATE gig_obligations SET bill_id=?,bill_json=?,synced_at=CURRENT_TIMESTAMP WHERE id=?`, out.Bill.ID, stringifyJSON(out.Bill), id); e != nil {
		return e
	}

	if documentsSynced == 0 && out.Bill.Status != "void" {
		for _, x := range v.Expenses {
			if x.FileID > 0 {
				var attached map[string]any
				if e = api.CallAppResult("bills", "bills_attach_file", map[string]any{"_project_id": pid, "bill_id": out.Bill.ID, "file_id": x.FileID, "expected_storage_install_id": x.StorageInstallID, "append_only": true}, &attached); e != nil {
					return fail("documents_pending", e)
				}
			}
		}
		if _, e = ctx.AppDB().Exec(`UPDATE gig_obligations SET documents_synced=1 WHERE id=?`, id); e != nil {
			return e
		}
	}

	_, e = ctx.AppDB().Exec(`UPDATE gig_obligations SET bill_id=?,bill_json=?,sync_status='synced',sync_error=NULL,synced_at=CURRENT_TIMESTAMP,next_sync_at=datetime('now','+5 minutes') WHERE id=?`, out.Bill.ID, stringifyJSON(out.Bill), id)
	return e
}
func runFinancialSync(ctx context.Context, app *sdk.AppCtx) error {
	rows, e := app.AppDB().Query(`SELECT id,project_id FROM gig_obligations WHERE sync_status!='not_applicable' AND (next_sync_at IS NULL OR next_sync_at<=CURRENT_TIMESTAMP) ORDER BY COALESCE(next_sync_at,'') LIMIT 25`)
	if e != nil {
		return e
	}
	type item struct{ id, pid string }
	var items []item
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.pid); e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := syncFinancialObligation(app, x.pid, x.id); err != nil {
			app.Logger().Warn("financial synchronization pending", "obligation_id", x.id, "error", err.Error())
		}
	}
	return nil
}
func (a *App) toolGigsFinancialSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	gid := int64Arg(args, "gig_id")
	f, e := loadFinancials(ctx, pid, gid)
	if e != nil {
		return nil, e
	}
	for _, o := range f.Obligations {
		_ = syncFinancialObligation(ctx, pid, o.ID)
	}
	return a.toolGigsFinancials(ctx, args)
}

// One obligation fact per identity. Bills is a linked representation, never an additional expense.
func (a *App) toolGigsFinancialFacts(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	limit := int64Arg(args, "limit")
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := int64Arg(args, "offset")
	if offset < 0 {
		return nil, errors.New("offset must be nonnegative")
	}
	rows, e := ctx.AppDB().Query(`SELECT id FROM gigs WHERE project_id=? ORDER BY id LIMIT ? OFFSET ?`, pid, limit+1, offset)
	if e != nil {
		return nil, e
	}
	var gids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		gids = append(gids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	more := int64(len(gids)) > limit
	if more {
		gids = gids[:limit]
	}
	facts := []map[string]any{}
	for _, gid := range gids {
		f, e := loadFinancials(ctx, pid, gid, true)
		if e != nil {
			return nil, e
		}
		if len(f.Obligations) == 0 {
			facts = append(facts, map[string]any{"gig_id": gid, "approved_amount_minor": nil, "payment_amount_minor": nil, "cost_state": "unknown"})
			continue
		}
		for _, o := range f.Obligations {
			amount := o.TotalMinor
			if o.Kind == "credit" {
				amount = -amount
			}
			fact := map[string]any{"gig_id": gid, "obligation_id": o.ID, "approved_amount_minor": amount, "currency": o.Currency, "payment_history_complete": o.LegacyBillID == 0, "approved_at": o.ApprovedAt, "allocations": o.Allocations, "delivered_files": o.DeliveredFiles, "bills_install_id": o.BillsInstallID, "bill_id": o.BillID, "payment_amount_minor": nil, "sync_status": o.SyncStatus, "synced_at": o.SyncedAt}
			if o.Bill != nil && o.Kind != "credit" {
				fact["payment_amount_minor"] = o.Bill.Paid
				fact["bill_credit_minor"] = o.Bill.Credit
				fact["bill_status"] = o.Bill.Status
				fact["outstanding_minor"] = o.Bill.Outstanding
			}
			facts = append(facts, fact)
		}
	}
	return map[string]any{"facts": facts, "has_more": more, "next_offset": offset + int64(len(gids)), "counted_source": "Gigs obligations; linked Bills records are not additional costs"}, nil
}
func (a *App) handleHTTPFinancials(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/financials/")
	parts := strings.Split(rest, "/")
	gid, _ := strconv.ParseInt(parts[0], 10, 64)
	args, e := marketplaceRequestArgs(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	args["gig_id"] = gid
	if gid <= 0 {
		httpErr(w, 400, "gig_id required")
		return
	}
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "options" {
		ctx := getAppCtx(r)
		pid := strArg(args, "_project_id")
		if _, e := loadFinancials(ctx, pid, gid, true); e != nil {
			httpErr(w, 404, e.Error())
			return
		}
		storage, _ := financeBinding(ctx, "storage")
		sites, _ := financeBinding(ctx, "sites")
		var out struct {
			Sites []map[string]any `json:"sites"`
		}
		out.Sites = []map[string]any{}
		if sites > 0 {
			_ = ctx.PlatformAPI().CallAppResult("content", "sites_list", map[string]any{"_project_id": pid}, &out)
		}
		httpJSON(w, map[string]any{"storage_install_id": storage, "sites_install_id": sites, "sites": out.Sites})
		return
	}

	var out any
	if r.Method == http.MethodGet && len(parts) == 1 {
		out, e = a.toolGigsFinancials(getAppCtx(r), args)
	} else if r.Method == http.MethodPost && len(parts) == 2 {
		switch parts[1] {
		case "agreement":
			out, e = a.toolGigsAgreement(getAppCtx(r), args)
		case "approve":
			out, e = a.toolGigsApproveCompensation(getAppCtx(r), args)
		case "sync":
			out, e = a.toolGigsFinancialSync(getAppCtx(r), args)
		default:
			httpErr(w, 404, "not found")
			return
		}
	} else {
		httpErr(w, 405, "method not allowed")
		return
	}
	writeMarketplaceResult(w, out, e)
}
func (a *App) financialTools() []sdk.Tool {
	props := map[string]any{"adopt_legacy_bill": map[string]any{"type": "boolean"}}
	for _, k := range []string{"gig_id", "expected_revision", "amount_minor", "rate_minor", "worker_id", "work_amount_minor", "limit", "offset"} {
		props[k] = map[string]any{"type": "integer"}
	}
	for _, k := range []string{"model", "quantity", "unit", "currency", "terms", "payee_name", "payee_email", "settlement", "confirmed_at", "evidence", "reason", "agreement_id", "request_key", "kind", "parent_id", "accepted_quantity", "due_date"} {
		props[k] = map[string]any{"type": "string"}
	}
	for _, k := range []string{"expenses", "allocations", "delivered_files"} {
		props[k] = map[string]any{"type": "array"}
	}
	return []sdk.Tool{
		{Name: "gigs_financials_get", Description: "Read agreements, immutable approvals and optional Bills payment status. Unknown amounts remain null.", InputSchema: schemaObject(props, []string{"gig_id"}), Handler: a.toolGigsFinancials},
		{Name: "gigs_agreement_save", Description: "Append an agreement revision. Fixed fee or rate × decimal quantity, arbitrary unit, original currency, supplier/payroll/other settlement. Unknown amounts may be omitted. expected_revision prevents overwriting concurrent edits.", InputSchema: schemaObject(props, []string{"gig_id", "expected_revision", "model", "settlement"}), Handler: a.toolGigsAgreement},
		{Name: "gigs_compensation_approve", Description: "Approve compensation for accepted work with itemized expenses and explicit allocations. Creates a durable obligation; optional connected Bills receives an unapproved bill automatically. Never pays. kind base/additional/credit; adjustments require parent_id and reason. request_key is mandatory for retry safety.", InputSchema: schemaObject(props, []string{"gig_id", "agreement_id", "request_key", "accepted_quantity", "work_amount_minor"}), Handler: a.toolGigsApproveCompensation},
		{Name: "gigs_financials_sync", Description: "Retry optional Bills creation or refresh authoritative bill and payment balances.", InputSchema: schemaObject(props, []string{"gig_id"}), Handler: a.toolGigsFinancialSync},
		{Name: "gigs_financial_facts", Description: "Paginated project financial facts keyed by obligation. Count each obligation once; its linked bill is not another expense. Currency is original; payments are separate. No approved obligation means unknown cost.", InputSchema: schemaObject(props, nil), Handler: a.toolGigsFinancialFacts},
	}
}

func (a *App) handleFinancialBillEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "bills" || event.SourceInstallID <= 0 || event.ProjectID == "" {
		return nil
	}
	id := int64Cast(event.Data["id"])
	if id <= 0 {
		return nil
	}
	// Repeated deliveries only invalidate the cached view; they never add money.
	_, e := ctx.AppDB().Exec(`UPDATE gig_obligations SET next_sync_at=NULL WHERE project_id=? AND bills_install_id=? AND bill_id=?`, event.ProjectID, event.SourceInstallID, id)
	return e
}

func sameFinancialExpenses(a, b []financialExpense) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Description != b[i].Description || a[i].AmountMinor != b[i].AmountMinor || a[i].FileID != b[i].FileID {
			return false
		}
	}
	return true
}
