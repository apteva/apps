package main

import (
	"database/sql"
	"errors"
	"math"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// APIUsagePlan is an operational token-bucket traffic policy. It is not a
// billing plan, quota ledger, subscription, or entitlement model.
type APIUsagePlan struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	APIID           int64  `json:"api_id"`
	Name            string `json:"name"`
	RateLimit       int    `json:"rate_limit"`
	IntervalSeconds int    `json:"interval_seconds"`
	Burst           int    `json:"burst"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

const usagePlanColumns = `id, project_id, api_id, name, rate_limit, interval_seconds, burst, created_at, updated_at`

func scanUsagePlan(scanner interface{ Scan(...any) error }) (*APIUsagePlan, error) {
	var plan APIUsagePlan
	if err := scanner.Scan(&plan.ID, &plan.ProjectID, &plan.APIID, &plan.Name, &plan.RateLimit, &plan.IntervalSeconds, &plan.Burst, &plan.CreatedAt, &plan.UpdatedAt); err != nil {
		return nil, err
	}
	return &plan, nil
}

func dbGetUsagePlan(db *sql.DB, pid string, id int64) (*APIUsagePlan, error) {
	plan, err := scanUsagePlan(db.QueryRow(`SELECT `+usagePlanColumns+` FROM api_usage_plans WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return plan, err
}

func dbListUsagePlans(db *sql.DB, pid string, apiID int64) ([]*APIUsagePlan, error) {
	rows, err := db.Query(`SELECT `+usagePlanColumns+` FROM api_usage_plans WHERE project_id=? AND api_id=? ORDER BY name`, pid, apiID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*APIUsagePlan
	for rows.Next() {
		plan, err := scanUsagePlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func dbCreateUsagePlan(db *sql.DB, pid string, apiID int64, name string, rate, interval, burst int) (*APIUsagePlan, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO api_usage_plans(project_id,api_id,name,rate_limit,interval_seconds,burst,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, pid, apiID, name, rate, interval, burst, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetUsagePlan(db, pid, id)
}

func dbUpdateUsagePlan(db *sql.DB, pid string, id int64, name string, rate, interval, burst int) (*APIUsagePlan, error) {
	res, err := db.Exec(`UPDATE api_usage_plans SET name=?,rate_limit=?,interval_seconds=?,burst=?,updated_at=? WHERE project_id=? AND id=?`, name, rate, interval, burst, time.Now().UTC().Format(time.RFC3339), pid, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("usage plan not found")
	}
	return dbGetUsagePlan(db, pid, id)
}

func dbAttachUsagePlan(db *sql.DB, pid string, keyID, planID int64) error {
	var keyAPI, planAPI int64
	if err := db.QueryRow(`SELECT api_id FROM api_keys WHERE project_id=? AND id=?`, pid, keyID).Scan(&keyAPI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("api key not found")
		}
		return err
	}
	if err := db.QueryRow(`SELECT api_id FROM api_usage_plans WHERE project_id=? AND id=?`, pid, planID).Scan(&planAPI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("usage plan not found")
		}
		return err
	}
	if keyAPI != planAPI {
		return errors.New("api key and usage plan belong to different APIs")
	}
	_, err := db.Exec(`INSERT INTO api_key_usage_plans(project_id,api_key_id,usage_plan_id,created_at) VALUES(?,?,?,?)
		ON CONFLICT(api_key_id) DO UPDATE SET project_id=excluded.project_id,usage_plan_id=excluded.usage_plan_id,created_at=excluded.created_at`,
		pid, keyID, planID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func dbDetachUsagePlan(db *sql.DB, pid string, keyID int64) (bool, error) {
	res, err := db.Exec(`DELETE FROM api_key_usage_plans WHERE project_id=? AND api_key_id=?`, pid, keyID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbUsagePlanForKey(db *sql.DB, pid string, apiID, keyID int64) (*APIUsagePlan, error) {
	plan, err := scanUsagePlan(db.QueryRow(`SELECT p.id,p.project_id,p.api_id,p.name,p.rate_limit,p.interval_seconds,p.burst,p.created_at,p.updated_at FROM api_usage_plans p
		JOIN api_key_usage_plans a ON a.usage_plan_id=p.id
		WHERE a.project_id=? AND a.api_key_id=? AND p.project_id=? AND p.api_id=?`, pid, keyID, pid, apiID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return plan, err
}

func usagePlanInputSchema(update bool) map[string]any {
	required := []string{"name", "rate_limit", "interval_seconds"}
	if update {
		required = []string{"id"}
	}
	return schemaObject(map[string]any{
		"project_id": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"},
		"api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"},
		"name":             map[string]any{"type": "string"},
		"rate_limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000},
		"interval_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400},
		"burst":            map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000},
	}, required)
}

func validatedUsagePlanValues(args map[string]any, current *APIUsagePlan) (string, int, int, int, error) {
	name := stringArg(args, "name", "")
	rate, interval, burst := 0, 0, 0
	if current != nil {
		if name == "" {
			name = current.Name
		}
		rate, interval, burst = current.RateLimit, current.IntervalSeconds, current.Burst
	}
	var err error
	if _, ok := args["rate_limit"]; ok || current == nil {
		rate, err = boundedIntArg(args, "rate_limit", 0, 1, 1000000)
		if err != nil {
			return "", 0, 0, 0, err
		}
	}
	if _, ok := args["interval_seconds"]; ok || current == nil {
		interval, err = boundedIntArg(args, "interval_seconds", 0, 1, 86400)
		if err != nil {
			return "", 0, 0, 0, err
		}
	}
	if _, ok := args["burst"]; ok {
		burst, err = boundedIntArg(args, "burst", 0, 1, 1000000)
		if err != nil {
			return "", 0, 0, 0, err
		}
	} else if current == nil {
		burst = rate
	}
	if name == "" || len(name) > 200 {
		return "", 0, 0, 0, errors.New("usage plan name must be 1-200 characters")
	}
	return name, rate, interval, burst, nil
}

func (a *App) toolUsagePlanCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	name, rate, interval, burst, err := validatedUsagePlanValues(args, nil)
	if err != nil {
		return nil, err
	}
	plan, err := dbCreateUsagePlan(ctx.AppDB(), api.ProjectID, api.ID, name, rate, interval, burst)
	return map[string]any{"usage_plan": plan}, err
}

func (a *App) toolUsagePlanGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	plan, err := dbGetUsagePlan(ctx.AppDB(), pid, int64(intArg(args, "id", 0)))
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("usage plan not found")
	}
	return map[string]any{"usage_plan": plan}, nil
}

func (a *App) toolUsagePlanList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	plans, err := dbListUsagePlans(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"usage_plans": plans, "count": len(plans)}, err
}

func (a *App) toolUsagePlanUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64(intArg(args, "id", 0))
	current, err := dbGetUsagePlan(ctx.AppDB(), pid, id)
	if err != nil || current == nil {
		if err == nil {
			err = errors.New("usage plan not found")
		}
		return nil, err
	}
	name, rate, interval, burst, err := validatedUsagePlanValues(args, current)
	if err != nil {
		return nil, err
	}
	plan, err := dbUpdateUsagePlan(ctx.AppDB(), pid, id, name, rate, interval, burst)
	a.resetThrottles()
	return map[string]any{"usage_plan": plan}, err
}

func (a *App) toolUsagePlanAttachKey(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	keyID, planID := int64(intArg(args, "key_id", 0)), int64(intArg(args, "usage_plan_id", 0))
	if err := dbAttachUsagePlan(ctx.AppDB(), pid, keyID, planID); err != nil {
		return nil, err
	}
	a.forgetThrottle(keyID)
	return map[string]any{"attached": true, "key_id": keyID, "usage_plan_id": planID}, nil
}

func (a *App) toolUsagePlanDetachKey(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	keyID := int64(intArg(args, "key_id", 0))
	detached, err := dbDetachUsagePlan(ctx.AppDB(), pid, keyID)
	a.forgetThrottle(keyID)
	return map[string]any{"detached": detached}, err
}

type apiKeyBucket struct {
	planID   int64
	rate     int
	interval int
	burst    int
	tokens   float64
	updated  time.Time
}

type throttleRegistry struct {
	mu      sync.Mutex
	buckets map[int64]*apiKeyBucket
}

func (a *App) allowAPIKeyRequest(pid string, apiID, keyID int64) (bool, time.Duration, error) {
	plan, err := dbUsagePlanForKey(a.ctx.AppReadDB(), pid, apiID, keyID)
	if err != nil || plan == nil {
		if plan == nil {
			a.forgetThrottle(keyID)
		}
		return true, 0, err
	}
	now := time.Now()
	a.throttles.mu.Lock()
	defer a.throttles.mu.Unlock()
	if a.throttles.buckets == nil {
		a.throttles.buckets = map[int64]*apiKeyBucket{}
	}
	bucket := a.throttles.buckets[keyID]
	if bucket == nil || bucket.planID != plan.ID || bucket.rate != plan.RateLimit || bucket.interval != plan.IntervalSeconds || bucket.burst != plan.Burst {
		bucket = &apiKeyBucket{planID: plan.ID, rate: plan.RateLimit, interval: plan.IntervalSeconds, burst: plan.Burst, tokens: float64(plan.Burst), updated: now}
		a.throttles.buckets[keyID] = bucket
	}
	perSecond := float64(bucket.rate) / float64(bucket.interval)
	bucket.tokens = math.Min(float64(bucket.burst), bucket.tokens+now.Sub(bucket.updated).Seconds()*perSecond)
	bucket.updated = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0, nil
	}
	retry := time.Duration(math.Ceil((1-bucket.tokens)/perSecond)) * time.Second
	if retry < time.Second {
		retry = time.Second
	}
	return false, retry, nil
}

func (a *App) forgetThrottle(keyID int64) {
	a.throttles.mu.Lock()
	delete(a.throttles.buckets, keyID)
	a.throttles.mu.Unlock()
}

func (a *App) resetThrottles() {
	a.throttles.mu.Lock()
	a.throttles.buckets = nil
	a.throttles.mu.Unlock()
}
