package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Admission hooks let another app (typically billing) approve each request
// before it reaches a provider and receive a settlement once it finishes.
// llm only ever sends token quantities and the provider's own cost; it never
// sees what the customer is charged.

const (
	defaultAdmissionTimeout = 2 * time.Second
	maxAdmissionTimeout     = 30 * time.Second
	staleReservationAge     = time.Hour
	outboxBatchSize         = 50
	outboxClaimTimeout      = 5 * time.Minute
	outboxMaxBackoff        = time.Hour
)

type AdmissionConfig struct {
	App           string `json:"app"`
	AuthorizeTool string `json:"authorize_tool"`
	SettleTool    string `json:"settle_tool"`
	FailMode      string `json:"fail_mode,omitempty"`
	TimeoutMS     int64  `json:"timeout_ms,omitempty"`
}

func (c *AdmissionConfig) enabled() bool {
	return c != nil && strings.TrimSpace(c.App) != "" && strings.TrimSpace(c.AuthorizeTool) != ""
}

func (c *AdmissionConfig) failOpen() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.FailMode), "open")
}

func (c *AdmissionConfig) timeout() time.Duration {
	if c == nil || c.TimeoutMS <= 0 {
		return defaultAdmissionTimeout
	}
	d := time.Duration(c.TimeoutMS) * time.Millisecond
	if d > maxAdmissionTimeout {
		return maxAdmissionTimeout
	}
	return d
}

// admissionFromAny parses and validates an admission config. A nil, empty
// object, or explicit null clears the hook.
func admissionFromAny(v any) (*AdmissionConfig, error) {
	if v == nil {
		return nil, nil
	}
	raw := jsonFromAny(v)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil, nil
	}
	var cfg AdmissionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("admission must be an object")
	}
	cfg.App = strings.TrimSpace(cfg.App)
	cfg.AuthorizeTool = strings.TrimSpace(cfg.AuthorizeTool)
	cfg.SettleTool = strings.TrimSpace(cfg.SettleTool)
	cfg.FailMode = strings.ToLower(strings.TrimSpace(cfg.FailMode))
	if cfg.App == "" || cfg.AuthorizeTool == "" || cfg.SettleTool == "" {
		return nil, errors.New("admission requires app, authorize_tool, and settle_tool")
	}
	switch cfg.FailMode {
	case "":
		cfg.FailMode = "closed"
	case "closed", "open":
	default:
		return nil, errors.New("admission.fail_mode must be closed or open")
	}
	if cfg.TimeoutMS < 0 || time.Duration(cfg.TimeoutMS)*time.Millisecond > maxAdmissionTimeout {
		return nil, fmt.Errorf("admission.timeout_ms must be between 0 and %d", maxAdmissionTimeout.Milliseconds())
	}
	return &cfg, nil
}

func admissionJSON(cfg *AdmissionConfig) string {
	if !cfg.enabled() {
		return "{}"
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func parseAdmissionJSON(raw string) *AdmissionConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	var cfg AdmissionConfig
	if json.Unmarshal([]byte(raw), &cfg) != nil || !cfg.enabled() {
		return nil
	}
	return &cfg
}

// effectiveAdmission picks the hook for a request. The token's own config
// wins, then the subject policy, then the project policy.
func effectiveAdmission(ident *TokenIdentity, policies []*Policy) *AdmissionConfig {
	if ident != nil && ident.Admission.enabled() {
		return ident.Admission
	}
	for i := len(policies) - 1; i >= 0; i-- {
		if policies[i] != nil && policies[i].Admission.enabled() {
			return policies[i].Admission
		}
	}
	return nil
}

type admissionDecision struct {
	Allow           bool   `json:"allow"`
	DenyReason      string `json:"deny_reason"`
	HTTPStatus      int    `json:"http_status"`
	ErrorType       string `json:"error_type"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	AdmissionRef    string `json:"admission_ref"`
	allowSet        bool
}

func (d *admissionDecision) UnmarshalJSON(b []byte) error {
	type plain admissionDecision
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(b, &probe)
	_, p.allowSet = probe["allow"]
	*d = admissionDecision(p)
	return nil
}

// hookCaller is swapped in tests; production calls the platform proxy.
var hookCaller = func(ctx *sdk.AppCtx, projectID, app, tool string, input map[string]any, out any) error {
	api := ctx.PlatformAPI()
	if projectID != "" {
		if scoped := ctx.WithProject(projectID); scoped != nil {
			api = scoped.PlatformAPI()
		}
	}
	if api == nil {
		return errors.New("platform API unavailable")
	}
	return api.CallAppResult(app, tool, input, out)
}

func callHookWithTimeout(reqCtx context.Context, ctx *sdk.AppCtx, projectID, app, tool string, input map[string]any, out any, timeout time.Duration) error {
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	done := make(chan error, 1)
	go func() { done <- hookCaller(ctx, projectID, app, tool, input, out) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%s.%s timed out after %s", app, tool, timeout)
	case <-reqCtx.Done():
		return reqCtx.Err()
	}
}

func admissionDeniedError(d admissionDecision) error {
	status := d.HTTPStatus
	if status < 400 || status > 599 {
		status = http.StatusForbidden
	}
	typ := firstNonEmpty(strings.TrimSpace(d.ErrorType), "admission_denied")
	msg := firstNonEmpty(strings.TrimSpace(d.DenyReason), "request denied by admission policy")
	return typedErr{status: status, typ: typ, msg: msg}
}

func admissionUnavailableError(err error) error {
	return typedErr{status: http.StatusServiceUnavailable, typ: "admission_unavailable", msg: "admission service unavailable: " + err.Error()}
}

// ---- Settlement outbox ----

func ensureGatewayV06Schema(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS admission_outbox (
			id INTEGER PRIMARY KEY,
			usage_event_id INTEGER NOT NULL UNIQUE,
			project_id TEXT NOT NULL DEFAULT '',
			app TEXT NOT NULL,
			tool TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			claimed_at TIMESTAMP,
			delivered_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS ix_admission_outbox_due ON admission_outbox(status, next_attempt_at)`,
		`CREATE TABLE IF NOT EXISTS public_models (
			id INTEGER PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			routes_json TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_status_created ON usage_events(status, created_at)`,
		`CREATE INDEX IF NOT EXISTS ix_usage_token ON usage_events(project_id, token_id, period)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// settlementPayload is what the settle tool receives. Field names are a
// contract with billing apps; add fields, never rename them.
func settlementPayload(ev *UsageEvent, partial bool) map[string]any {
	var details struct {
		CachedInputTokens int64          `json:"cached_input_tokens"`
		Units             map[string]any `json:"units"`
		Partial           bool           `json:"partial"`
	}
	_ = json.Unmarshal(ev.UsageDetails, &details)
	status := ev.Status
	if status != "completed" {
		status = "failed"
	}
	payload := map[string]any{
		"request_id":             ev.RequestID,
		"admission_ref":          ev.AdmissionRef,
		"status":                 status,
		"partial":                partial || details.Partial,
		"input_tokens":           ev.RequestTokens,
		"output_tokens":          ev.ResponseTokens,
		"cached_tokens":          details.CachedInputTokens,
		"latency_ms":             ev.LatencyMS,
		"time_to_first_token_ms": ev.TTFTMS,
		"requested_model":        firstNonEmpty(ev.RequestedModel, ev.Model),
		"provider":               ev.Provider,
		"model":                  ev.Model,
		"endpoint":               ev.Endpoint,
		"provider_cost":          ev.ProviderCostMicrounits,
		"provider_cost_currency": ev.ProviderCostCurrency,
		"provider_cost_status":   ev.ProviderCostStatus,
		"token_id":               ev.TokenID,
		"token_metadata":         rawJSON(string(ev.TokenMetadata)),
		"subject_type":           ev.SubjectType,
		"subject_id":             ev.SubjectID,
		"project_id":             ev.ProjectID,
		"_project_id":            ev.ProjectID,
		"usage_event_id":         ev.ID,
	}
	if len(details.Units) > 0 {
		payload["units"] = details.Units
	}
	return payload
}

// enqueueSettlement writes the settle call for a finished usage row in the
// caller's transaction, so the row and its settlement commit together.
func enqueueSettlement(q execer, ev *UsageEvent, partial bool) error {
	cfg := parseAdmissionJSON(ev.AdmissionJSON)
	if cfg == nil || cfg.SettleTool == "" {
		return nil
	}
	payload, _ := json.Marshal(settlementPayload(ev, partial))
	_, err := q.Exec(`INSERT OR IGNORE INTO admission_outbox (usage_event_id, project_id, app, tool, payload_json)
		VALUES (?, ?, ?, ?, ?)`, ev.ID, ev.ProjectID, cfg.App, cfg.SettleTool, string(payload))
	return err
}

type outboxRow struct {
	ID        int64
	ProjectID string
	App       string
	Tool      string
	Payload   string
	Attempts  int64
}

var outboxMu sync.Mutex

// deliverOutbox sends due settlements. It is safe to run from several
// goroutines: rows are claimed before delivery, and billing treats
// request_id as the idempotency key in case a claim expires mid-call.
func deliverOutbox(ctx *sdk.AppCtx, onlyID int64) (delivered int, err error) {
	if ctx == nil || ctx.AppDB() == nil {
		return 0, nil
	}
	db := ctx.AppDB()
	rows, err := claimOutbox(db, onlyID)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		var input map[string]any
		_ = json.Unmarshal([]byte(row.Payload), &input)
		var out any
		callErr := callHookWithTimeout(context.Background(), ctx, row.ProjectID, row.App, row.Tool, input, &out, maxAdmissionTimeout)
		if callErr == nil {
			_, err = db.Exec(`UPDATE admission_outbox SET status='delivered', delivered_at=CURRENT_TIMESTAMP, claimed_at=NULL, last_error='', attempts=attempts+1 WHERE id=?`, row.ID)
			if err == nil {
				delivered++
			}
			continue
		}
		backoff := outboxBackoff(row.Attempts + 1)
		_, err = db.Exec(`UPDATE admission_outbox SET status='pending', claimed_at=NULL, attempts=attempts+1, last_error=?,
			next_attempt_at=datetime('now', ?) WHERE id=?`,
			truncate(callErr.Error(), 500), fmt.Sprintf("+%d seconds", int(backoff.Seconds())), row.ID)
		if ctx.Logger() != nil {
			ctx.Logger().Warn("llm settlement delivery failed", "outbox_id", row.ID, "app", row.App, "tool", row.Tool, "attempts", row.Attempts+1, "error", callErr.Error())
		}
	}
	return delivered, err
}

func claimOutbox(db *sql.DB, onlyID int64) ([]outboxRow, error) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id, project_id, app, tool, payload_json, attempts FROM admission_outbox
		WHERE (status='pending' AND next_attempt_at <= CURRENT_TIMESTAMP)
		   OR (status='delivering' AND claimed_at < datetime('now', ?))`
	args := []any{fmt.Sprintf("-%d seconds", int(outboxClaimTimeout.Seconds()))}
	if onlyID > 0 {
		query = `SELECT id, project_id, app, tool, payload_json, attempts FROM admission_outbox WHERE id=? AND status='pending'`
		args = []any{onlyID}
	}
	query += ` ORDER BY id LIMIT ` + fmt.Sprint(outboxBatchSize)
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.App, &r.Tool, &r.Payload, &r.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	for _, r := range out {
		if _, err := tx.Exec(`UPDATE admission_outbox SET status='delivering', claimed_at=CURRENT_TIMESTAMP WHERE id=?`, r.ID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func outboxBackoff(attempts int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 5 * time.Second
	for i := int64(1); i < attempts && d < outboxMaxBackoff; i++ {
		d *= 2
	}
	if d > outboxMaxBackoff {
		d = outboxMaxBackoff
	}
	return d
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// sweepStaleReservations fails reservations whose request never finished
// (for example after a crash) and queues their settlement, so a billing app
// does not hold credit forever.
func sweepStaleReservations(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id FROM usage_events WHERE status='reserved' AND created_at < datetime('now', ?)`,
		fmt.Sprintf("-%d seconds", int(staleReservationAge.Seconds())))
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	swept := 0
	for _, id := range ids {
		tx, err := db.Begin()
		if err != nil {
			return swept, err
		}
		res, err := tx.Exec(`UPDATE usage_events SET status='failed', response_tokens=0, total_tokens=request_tokens,
			usage_details_json=json_set(CASE WHEN json_valid(usage_details_json) THEN usage_details_json ELSE '{}' END, '$.abandoned', json('true'))
			WHERE id=? AND status='reserved'`, id)
		if err != nil {
			tx.Rollback()
			return swept, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			ev, err := scanUsageEvent(tx.QueryRow(`SELECT `+usageEventColumns+` FROM usage_events WHERE id=?`, id))
			if err != nil {
				tx.Rollback()
				return swept, err
			}
			if err := enqueueSettlement(tx, ev, false); err != nil {
				tx.Rollback()
				return swept, err
			}
			swept++
		}
		if err := tx.Commit(); err != nil {
			return swept, err
		}
	}
	return swept, nil
}

func dbOutboxList(db *sql.DB, projectID, status string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conds := []string{"project_id = ?"}
	args := []any{projectID}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id, usage_event_id, app, tool, status, attempts, last_error,
		COALESCE(next_attempt_at,''), COALESCE(delivered_at,''), COALESCE(created_at,''), payload_json
		FROM admission_outbox WHERE `+strings.Join(conds, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, usageID, attempts int64
		var app, tool, st, lastErr, next, delivered, created, payload string
		if err := rows.Scan(&id, &usageID, &app, &tool, &st, &attempts, &lastErr, &next, &delivered, &created, &payload); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "usage_event_id": usageID, "app": app, "tool": tool, "status": st, "attempts": attempts,
			"last_error": lastErr, "next_attempt_at": next, "delivered_at": delivered, "created_at": created, "payload": rawJSON(payload),
		})
	}
	return out, rows.Err()
}

func (a *App) toolOutboxList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbOutboxList(ctx.AppDB(), projectFromArgs(args), strArg(args, "status"), intArg(args, "limit", 100))
	if err != nil {
		return nil, err
	}
	return map[string]any{"settlements": rows}, nil
}

func (a *App) handleOutbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rows, err := dbOutboxList(globalCtx.AppDB(), projectFromRequest(r), r.URL.Query().Get("status"), intArgQuery(r, "limit", 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"settlements": rows})
}
