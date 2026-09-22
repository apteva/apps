package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type CreditTransaction struct {
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id"`
	SubjectType    string          `json:"subject_type"`
	SubjectID      string          `json:"subject_id"`
	FeatureKey     string          `json:"feature_key"`
	Amount         int64           `json:"amount"`
	Kind           string          `json:"kind"`
	SourceType     string          `json:"source_type"`
	SourceID       string          `json:"source_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	OccurredAt     string          `json:"occurred_at"`
	CreatedAt      string          `json:"created_at"`
}

type CreditReservation struct {
	ID              int64           `json:"id"`
	ProjectID       string          `json:"project_id"`
	SubjectType     string          `json:"subject_type"`
	SubjectID       string          `json:"subject_id"`
	FeatureKey      string          `json:"feature_key"`
	Amount          int64           `json:"amount"`
	CommittedAmount int64           `json:"committed_amount"`
	Status          string          `json:"status"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	SourceType      string          `json:"source_type"`
	SourceID        string          `json:"source_id,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	ExpiresAt       string          `json:"expires_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

type CreditBalance struct {
	ProjectID   string `json:"project_id"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	FeatureKey  string `json:"feature_key"`
	Granted     int64  `json:"granted"`
	Debited     int64  `json:"debited"`
	Refunded    int64  `json:"refunded"`
	Adjustments int64  `json:"adjustments"`
	Expired     int64  `json:"expired"`
	Lifetime    int64  `json:"lifetime"`
	Reserved    int64  `json:"reserved"`
	Available   int64  `json:"available"`
}

func creditTools(a *App) []sdk.Tool {
	base := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{
			"subject_type": map[string]any{"type": "string"},
			"subject_id":   map[string]any{"type": "string"},
			"feature_key":  map[string]any{"type": "string"},
		}
		for k, v := range extra {
			props[k] = v
		}
		return schemaObject(props, append([]string{"subject_id", "feature_key"}, required...))
	}
	return []sdk.Tool{
		{Name: "credits_grant", Description: "Append a retry-safe prepaid credit grant or compensating transaction.", InputSchema: base(map[string]any{
			"amount": map[string]any{"type": "integer"}, "kind": map[string]any{"type": "string", "enum": []string{"grant", "refund", "adjustment", "expiry"}},
			"source_type": map[string]any{"type": "string"}, "source_id": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"},
			"occurred_at": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
		}, "amount", "idempotency_key"), Handler: a.toolCreditsGrant},
		{Name: "credits_reserve", Description: "Atomically reserve available prepaid credits without allowing overspend.", InputSchema: base(map[string]any{
			"amount": map[string]any{"type": "integer"}, "idempotency_key": map[string]any{"type": "string"}, "expires_at": map[string]any{"type": "string"},
			"source_type": map[string]any{"type": "string"}, "source_id": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
		}, "amount", "idempotency_key"), Handler: a.toolCreditsReserve},
		{Name: "credits_commit", Description: "Commit an active reservation into one append-only debit transaction.", InputSchema: schemaObject(map[string]any{"reservation_id": map[string]any{"type": "integer"}, "amount": map[string]any{"type": "integer"}, "metadata": map[string]any{"type": "object"}}, []string{"reservation_id"}), Handler: a.toolCreditsCommit},
		{Name: "credits_release", Description: "Release an active reservation without deleting ledger history.", InputSchema: schemaObject(map[string]any{"reservation_id": map[string]any{"type": "integer"}}, []string{"reservation_id"}), Handler: a.toolCreditsRelease},
		{Name: "credits_balance", Description: "Return spendable, reserved, and lifetime credit totals for one subject and feature.", InputSchema: base(nil), Handler: a.toolCreditsBalance},
		{Name: "credits_transactions", Description: "List append-only credit transactions for one subject and optional feature.", InputSchema: schemaObject(map[string]any{
			"subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"}, "feature_key": map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}, "offset": map[string]any{"type": "integer"},
		}, []string{"subject_id"}), Handler: a.toolCreditsTransactions},
	}
}

func (a *App) toolCreditsGrant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	tx, deduped, err := dbCreditsGrant(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if !deduped {
		ctx.Emit("credits.transaction.created", map[string]any{"transaction_id": tx.ID, "subject_id": tx.SubjectID, "feature_key": tx.FeatureKey, "amount": tx.Amount, "kind": tx.Kind})
	}
	bal, err := dbCreditsBalance(ctx.AppDB(), pid, tx.SubjectType, tx.SubjectID, tx.FeatureKey)
	return map[string]any{"transaction": tx, "balance": bal, "deduped": deduped}, err
}

func (a *App) toolCreditsReserve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, deduped, err := dbCreditsReserve(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	bal, err := dbCreditsBalance(ctx.AppDB(), pid, r.SubjectType, r.SubjectID, r.FeatureKey)
	return map[string]any{"reservation": r, "balance": bal, "deduped": deduped}, err
}

func (a *App) toolCreditsCommit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, tx, deduped, err := dbCreditsCommit(ctx.AppDB(), pid, int64Arg(args, "reservation_id"), int64Arg(args, "amount"), args["metadata"])
	if err != nil {
		return nil, err
	}
	bal, err := dbCreditsBalance(ctx.AppDB(), pid, r.SubjectType, r.SubjectID, r.FeatureKey)
	return map[string]any{"reservation": r, "transaction": tx, "balance": bal, "deduped": deduped}, err
}

func (a *App) toolCreditsRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	r, deduped, err := dbCreditsRelease(ctx.AppDB(), pid, int64Arg(args, "reservation_id"))
	if err != nil {
		return nil, err
	}
	bal, err := dbCreditsBalance(ctx.AppDB(), pid, r.SubjectType, r.SubjectID, r.FeatureKey)
	return map[string]any{"reservation": r, "balance": bal, "deduped": deduped}, err
}

func (a *App) toolCreditsBalance(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if strArg(args, "subject_id") == "" || strArg(args, "feature_key") == "" {
		return nil, errors.New("subject_id and feature_key required")
	}
	bal, err := dbCreditsBalance(ctx.AppDB(), pid, subjectType(args), strArg(args, "subject_id"), strArg(args, "feature_key"))
	return map[string]any{"balance": bal}, err
}

func (a *App) toolCreditsTransactions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	items, err := dbCreditsTransactions(ctx.AppDB(), pid, args)
	return map[string]any{"transactions": items, "count": len(items)}, err
}

func dbCreditsGrant(db *sql.DB, pid string, args map[string]any) (*CreditTransaction, bool, error) {
	subjectID, feature, key := strArg(args, "subject_id"), strArg(args, "feature_key"), strArg(args, "idempotency_key")
	amount := int64Arg(args, "amount")
	kind := strings.ToLower(firstNonEmpty(strArg(args, "kind"), "grant"))
	if subjectID == "" || feature == "" || key == "" {
		return nil, false, errors.New("subject_id, feature_key, and idempotency_key required")
	}
	if amount == 0 {
		return nil, false, errors.New("amount must be non-zero")
	}
	if kind == "debit" || (kind == "grant" && amount < 0) || ((kind == "refund" || kind == "expiry") && amount > 0) {
		return nil, false, errors.New("amount sign does not match transaction kind")
	}
	if kind != "grant" && kind != "refund" && kind != "adjustment" && kind != "expiry" {
		return nil, false, errors.New("kind must be grant, refund, adjustment, or expiry")
	}
	occurred := strArg(args, "occurred_at")
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := db.Exec(`INSERT INTO credit_transactions
		(project_id,subject_type,subject_id,feature_key,amount,kind,source_type,source_id,idempotency_key,metadata,occurred_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key!='' DO NOTHING`,
		pid, subjectType(args), subjectID, feature, amount, kind, firstNonEmpty(strArg(args, "source_type"), "manual"), nullStr(strArg(args, "source_id")), key, jsonOrEmpty(args["metadata"], "{}"), occurred)
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	rowsAffected, _ := res.RowsAffected()
	deduped := rowsAffected == 0
	var out *CreditTransaction
	if deduped {
		out, err = dbCreditTransactionByKey(db, pid, key)
	} else {
		out, err = dbCreditTransactionGet(db, pid, id)
	}
	if err == nil && deduped && (out.SubjectType != subjectType(args) || out.SubjectID != subjectID || out.FeatureKey != feature || out.Amount != amount || out.Kind != kind) {
		return nil, false, errors.New("idempotency_key was already used with different credit transaction parameters")
	}
	return out, deduped, err
}

func dbCreditsReserve(db *sql.DB, pid string, args map[string]any) (*CreditReservation, bool, error) {
	subjectID, feature, key := strArg(args, "subject_id"), strArg(args, "feature_key"), strArg(args, "idempotency_key")
	amount := int64Arg(args, "amount")
	if subjectID == "" || feature == "" || key == "" {
		return nil, false, errors.New("subject_id, feature_key, and idempotency_key required")
	}
	if amount <= 0 {
		return nil, false, errors.New("amount must be greater than zero")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE credit_reservations SET status='expired',updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=? AND status='active' AND expires_at IS NOT NULL AND datetime(expires_at)<=datetime('now')`, pid, subjectType(args), subjectID, feature)
	if err != nil {
		return nil, false, err
	}
	var existingID int64
	err = tx.QueryRow(`SELECT id FROM credit_reservations WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		r, err := dbCreditReservationGet(db, pid, existingID)
		if err == nil && (r.SubjectType != subjectType(args) || r.SubjectID != subjectID || r.FeatureKey != feature || r.Amount != amount) {
			return nil, false, errors.New("idempotency_key was already used with different reservation parameters")
		}
		return r, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var available int64
	if err := tx.QueryRow(`SELECT
		COALESCE((SELECT SUM(amount) FROM credit_transactions WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=?),0)
		- COALESCE((SELECT SUM(amount) FROM credit_reservations WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=? AND status='active'),0)`,
		pid, subjectType(args), subjectID, feature, pid, subjectType(args), subjectID, feature).Scan(&available); err != nil {
		return nil, false, err
	}
	if available < amount {
		return nil, false, fmt.Errorf("insufficient credits: available=%d requested=%d", available, amount)
	}
	res, err := tx.Exec(`INSERT INTO credit_reservations
		(project_id,subject_type,subject_id,feature_key,amount,status,idempotency_key,source_type,source_id,metadata,expires_at)
		VALUES (?,?,?,?,?,'active',?,?,?,?,?)`, pid, subjectType(args), subjectID, feature, amount, key, firstNonEmpty(strArg(args, "source_type"), "manual"), nullStr(strArg(args, "source_id")), jsonOrEmpty(args["metadata"], "{}"), nullStr(strArg(args, "expires_at")))
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	r, err := dbCreditReservationGet(db, pid, id)
	return r, false, err
}

func dbCreditsCommit(db *sql.DB, pid string, reservationID, amount int64, metadata any) (*CreditReservation, *CreditTransaction, bool, error) {
	if reservationID == 0 {
		return nil, nil, false, errors.New("reservation_id required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	r, err := scanCreditReservation(tx.QueryRow(creditReservationSelect()+` WHERE project_id=? AND id=?`, pid, reservationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, errors.New("reservation not found")
	}
	if err != nil {
		return nil, nil, false, err
	}
	key := fmt.Sprintf("reservation:%d:commit", r.ID)
	if r.Status == "committed" {
		ct, err := scanCreditTransaction(tx.QueryRow(creditTransactionSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
		if err != nil {
			return nil, nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, false, err
		}
		return r, ct, true, nil
	}
	if r.Status != "active" {
		return nil, nil, false, fmt.Errorf("reservation is %s", r.Status)
	}
	if r.ExpiresAt != "" {
		if t, parseErr := time.Parse(time.RFC3339, r.ExpiresAt); parseErr == nil && !time.Now().UTC().Before(t) {
			_, _ = tx.Exec(`UPDATE credit_reservations SET status='expired',updated_at=CURRENT_TIMESTAMP WHERE id=?`, r.ID)
			_ = tx.Commit()
			return nil, nil, false, errors.New("reservation expired")
		}
	}
	if amount == 0 {
		amount = r.Amount
	}
	if amount <= 0 || amount > r.Amount {
		return nil, nil, false, errors.New("commit amount must be greater than zero and no more than reserved amount")
	}
	meta := mapFromAny(r.Metadata)
	for k, v := range mapFromAny(metadata) {
		meta[k] = v
	}
	res, err := tx.Exec(`INSERT INTO credit_transactions
		(project_id,subject_type,subject_id,feature_key,amount,kind,source_type,source_id,idempotency_key,metadata)
		VALUES (?,?,?,?,?,'debit',?,?,?,?)`, pid, r.SubjectType, r.SubjectID, r.FeatureKey, -amount, r.SourceType, nullStr(r.SourceID), key, jsonOrEmpty(meta, "{}"))
	if err != nil {
		return nil, nil, false, err
	}
	txID, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE credit_reservations SET status='committed',committed_amount=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND status='active'`, amount, pid, r.ID); err != nil {
		return nil, nil, false, err
	}
	r, err = scanCreditReservation(tx.QueryRow(creditReservationSelect()+` WHERE project_id=? AND id=?`, pid, r.ID))
	if err != nil {
		return nil, nil, false, err
	}
	ct, err := scanCreditTransaction(tx.QueryRow(creditTransactionSelect()+` WHERE project_id=? AND id=?`, pid, txID))
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	return r, ct, false, nil
}

func dbCreditsRelease(db *sql.DB, pid string, reservationID int64) (*CreditReservation, bool, error) {
	if reservationID == 0 {
		return nil, false, errors.New("reservation_id required")
	}
	r, err := dbCreditReservationGet(db, pid, reservationID)
	if err != nil || r == nil {
		return r, false, firstCreditErr(err, errors.New("reservation not found"))
	}
	if r.Status == "released" || r.Status == "expired" {
		return r, true, nil
	}
	if r.Status != "active" {
		return nil, false, fmt.Errorf("reservation is %s", r.Status)
	}
	res, err := db.Exec(`UPDATE credit_reservations SET status='released',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND status='active'`, pid, reservationID)
	if err != nil {
		return nil, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, false, errors.New("reservation state changed concurrently")
	}
	r, err = dbCreditReservationGet(db, pid, reservationID)
	return r, false, err
}

func dbCreditsBalance(db *sql.DB, pid, subjectType, subjectID, feature string) (*CreditBalance, error) {
	if subjectID == "" || feature == "" {
		return nil, errors.New("subject_id and feature_key required")
	}
	_, err := db.Exec(`UPDATE credit_reservations SET status='expired',updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=? AND status='active' AND expires_at IS NOT NULL AND datetime(expires_at)<=datetime('now')`, pid, subjectType, subjectID, feature)
	if err != nil {
		return nil, err
	}
	b := &CreditBalance{ProjectID: pid, SubjectType: subjectType, SubjectID: subjectID, FeatureKey: feature}
	if err := db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN kind='grant' THEN amount ELSE 0 END),0),
		COALESCE(-SUM(CASE WHEN kind='debit' THEN amount ELSE 0 END),0),
		COALESCE(-SUM(CASE WHEN kind='refund' THEN amount ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='adjustment' THEN amount ELSE 0 END),0),
		COALESCE(-SUM(CASE WHEN kind='expiry' THEN amount ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='grant' THEN amount ELSE 0 END),0),
		COALESCE((SELECT SUM(amount) FROM credit_reservations WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=? AND status='active'),0)
		FROM credit_transactions WHERE project_id=? AND subject_type=? AND subject_id=? AND feature_key=?`,
		pid, subjectType, subjectID, feature, pid, subjectType, subjectID, feature).Scan(&b.Granted, &b.Debited, &b.Refunded, &b.Adjustments, &b.Expired, &b.Lifetime, &b.Reserved); err != nil {
		return nil, err
	}
	b.Available = b.Granted - b.Debited - b.Refunded + b.Adjustments - b.Expired - b.Reserved
	return b, nil
}

func dbCreditsTransactions(db *sql.DB, pid string, args map[string]any) ([]*CreditTransaction, error) {
	if strArg(args, "subject_id") == "" {
		return nil, errors.New("subject_id required")
	}
	where := []string{"project_id=?", "subject_type=?", "subject_id=?"}
	qargs := []any{pid, subjectType(args), strArg(args, "subject_id")}
	if v := strArg(args, "feature_key"); v != "" {
		where = append(where, "feature_key=?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "kind"); v != "" {
		where = append(where, "kind=?")
		qargs = append(qargs, v)
	}
	limit := clampLimit(int(int64Arg(args, "limit")), 200)
	offset := int64Arg(args, "offset")
	if offset < 0 {
		offset = 0
	}
	qargs = append(qargs, limit, offset)
	rows, err := db.Query(creditTransactionSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CreditTransaction
	for rows.Next() {
		item, err := scanCreditTransaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func dbCreditTransactionGet(db *sql.DB, pid string, id int64) (*CreditTransaction, error) {
	return scanCreditTransaction(db.QueryRow(creditTransactionSelect()+` WHERE project_id=? AND id=?`, pid, id))
}
func dbCreditTransactionByKey(db *sql.DB, pid, key string) (*CreditTransaction, error) {
	return scanCreditTransaction(db.QueryRow(creditTransactionSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}
func creditTransactionSelect() string {
	return `SELECT id,project_id,subject_type,subject_id,feature_key,amount,kind,source_type,COALESCE(source_id,''),COALESCE(idempotency_key,''),metadata,occurred_at,created_at FROM credit_transactions`
}
func scanCreditTransaction(row rowScanner) (*CreditTransaction, error) {
	var v CreditTransaction
	var meta string
	err := row.Scan(&v.ID, &v.ProjectID, &v.SubjectType, &v.SubjectID, &v.FeatureKey, &v.Amount, &v.Kind, &v.SourceType, &v.SourceID, &v.IdempotencyKey, &meta, &v.OccurredAt, &v.CreatedAt)
	v.Metadata = json.RawMessage(meta)
	return &v, err
}
func dbCreditReservationGet(db *sql.DB, pid string, id int64) (*CreditReservation, error) {
	v, err := scanCreditReservation(db.QueryRow(creditReservationSelect()+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}
func creditReservationSelect() string {
	return `SELECT id,project_id,subject_type,subject_id,feature_key,amount,committed_amount,status,COALESCE(idempotency_key,''),source_type,COALESCE(source_id,''),metadata,expires_at,created_at,updated_at FROM credit_reservations`
}
func scanCreditReservation(row rowScanner) (*CreditReservation, error) {
	var v CreditReservation
	var meta string
	var expires sql.NullString
	err := row.Scan(&v.ID, &v.ProjectID, &v.SubjectType, &v.SubjectID, &v.FeatureKey, &v.Amount, &v.CommittedAmount, &v.Status, &v.IdempotencyKey, &v.SourceType, &v.SourceID, &meta, &expires, &v.CreatedAt, &v.UpdatedAt)
	v.Metadata = json.RawMessage(meta)
	if expires.Valid {
		v.ExpiresAt = expires.String
	}
	return &v, err
}
func firstCreditErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
