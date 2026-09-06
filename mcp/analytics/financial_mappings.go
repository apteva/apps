package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

func financialTarget(db sqlRunner, project string, id int64) (ObjectiveTarget, error) {
	var target ObjectiveTarget
	var raw string
	err := db.QueryRow(`SELECT t.id,t.objective_id,t.name,t.metric_key,t.target_value,t.unit,t.currency,t.direction,t.period_start,t.period_end,t.timezone,t.query_json,t.created_at,t.updated_at FROM objective_targets t JOIN objectives o ON o.id=t.objective_id WHERE t.id=? AND o.project_id=? AND t.retired_at IS NULL AND o.status='active'`, id, project).Scan(&target.ID, &target.ObjectiveID, &target.Name, &target.MetricKey, &target.TargetValue, &target.Unit, &target.Currency, &target.Direction, &target.PeriodStart, &target.PeriodEnd, &target.Timezone, &raw, &target.CreatedAt, &target.UpdatedAt)
	if err != nil {
		return target, err
	}
	err = json.Unmarshal([]byte(raw), &target.Query)
	return target, err
}

func grantFinancialShare(db sqlRunner, project string, targetID int64, destination, meaning, actor, economicKey string) (string, error) {
	if project == "" || destination == "" || len(destination) > 200 || project == destination || actor == "" {
		return "", errors.New("source and destination must be different projects with explicit operator approval")
	}
	economicKey = strings.ToLower(strings.Join(strings.Fields(economicKey), " "))
	if economicKey == "" || len(economicKey) > 120 {
		return "", errors.New("an income stream key is required; revenue and its settlement must use the same key")
	}
	if !oneOf(meaning, "revenue", "realized_profit", "other") {
		return "", errors.New("metric_meaning must be revenue, realized_profit or other; settlement payouts must not duplicate underlying revenue")
	}
	target, err := financialTarget(db, project, targetID)
	if err != nil {
		return "", errors.New("active source target not found")
	}
	if target.Unit != "money" {
		return "", errors.New("financial sharing requires a money target")
	}
	id := uuid.NewString()
	_, err = db.Exec(`INSERT INTO financial_shares(id,source_project,target_id,destination_project,definition_revision,metric_meaning,approved_by,approved_at,economic_key) VALUES(?,?,?,?,?,?,?,?,?)`, id, project, targetID, destination, target.UpdatedAt, meaning, actor, time.Now().UnixMilli(), economicKey)
	return id, err
}
func revokeFinancialShare(db sqlRunner, project, id string) error {
	res, err := db.Exec(`UPDATE financial_shares SET revoked_at=? WHERE id=? AND source_project=? AND revoked_at IS NULL`, time.Now().UnixMilli(), id, project)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("active share not found in current project")
	}
	return nil
}

type financialShare struct {
	ID, Source, Destination, Meaning, EconomicKey string
	Target, Definition                            int64
}

func authorizedFinancialShare(db sqlRunner, destination, id string) (financialShare, error) {
	var s financialShare
	s.ID = id
	err := db.QueryRow(`SELECT source_project,destination_project,target_id,definition_revision,metric_meaning,economic_key FROM financial_shares WHERE id=? AND destination_project=? AND revoked_at IS NULL`, id, destination).Scan(&s.Source, &s.Destination, &s.Target, &s.Definition, &s.Meaning, &s.EconomicKey)
	if err != nil {
		return s, errors.New("source sharing is unavailable or revoked")
	}
	if s.Source == destination {
		return s, errors.New("a project cannot be its own component")
	}
	return s, nil
}
func validateComponent(db sqlRunner, project string, target ObjectiveTarget, eventID int64) (string, error) {
	if !oneOf(target.Query.Aggregation, "sum", "sum_money") || target.Unit != "money" || !strings.HasPrefix(target.Query.Value, "props.") {
		return "", errors.New("destination must sum a money amount in event props")
	}
	if target.Query.Aggregation == "sum_money" && target.Query.ReportingCurrency != target.Currency {
		return "", errors.New("destination reporting currency mismatch")
	}
	for key := range target.Query.Where {
		if key == target.Query.Value || (target.Query.Aggregation == "sum_money" && key == target.Query.CurrencyField) {
			return "", errors.New("component amount/currency cannot also be a destination filter")
		}
	}
	var rollup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events e JOIN event_specs s ON s.project_id=e.project_id AND s.app=e.app AND s.topic=e.topic WHERE e.id=? AND e.project_id=? AND s.ingest_mode IN ('raw_plus_rollup','rollup')`, eventID, project).Scan(&rollup); err != nil {
		return "", err
	}
	if rollup > 0 {
		return "", errors.New("rollup-backed events cannot be adopted as financial components")
	}
	// Existing records are explicitly adopted; IDs, month filters, timestamps,
	// delivery receipts and all other properties stay intact.
	f := Filter{ProjectID: project, App: target.Query.App, Topic: target.Query.Topic, Source: target.Query.Source, Where: target.Query.Where, Since: target.PeriodStart, Until: target.PeriodEnd}
	where, args, err := f.buildWhere()
	if err != nil {
		return "", err
	}
	args = append(args, eventID)
	var props string
	err = db.QueryRow(`SELECT props FROM events WHERE `+where+` AND id=? AND source='track' AND app!='billing'`, args...).Scan(&props)
	if err != nil {
		return "", errors.New("component must be an explicitly tracked non-Billing event matching the destination query and saved period")
	}
	return props, nil
}
func createFinancialMapping(ctx context.Context, db *sql.DB, project, shareID string, destinationTarget, eventID int64, actor string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Lock consent/configuration while validating the new edge.
	if _, err = tx.Exec(`UPDATE financial_projects SET revision=revision WHERE project_id=?`, project); err != nil {
		return "", err
	}
	share, err := authorizedFinancialShare(tx, project, shareID)
	if err != nil {
		return "", err
	}
	source, err := financialTarget(tx, share.Source, share.Target)
	if err != nil || source.UpdatedAt != share.Definition {
		return "", errors.New("source definition changed; renew sharing approval")
	}
	dest, err := financialTarget(tx, project, destinationTarget)
	if err != nil {
		return "", errors.New("destination target not found")
	}
	if source.Currency != dest.Currency || source.PeriodStart != dest.PeriodStart || source.PeriodEnd != dest.PeriodEnd || source.Timezone != dest.Timezone {
		return "", errors.New("source and destination must use the same saved period, timezone and reporting currency")
	}
	var cycle int
	err = tx.QueryRow(`WITH RECURSIVE reachable(project) AS (SELECT ? UNION SELECT m.destination_project FROM financial_mappings m JOIN financial_shares s ON s.id=m.share_id JOIN reachable r ON s.source_project=r.project WHERE m.enabled=1 AND s.revoked_at IS NULL) SELECT COUNT(*) FROM reachable WHERE project=?`, project, share.Source).Scan(&cycle)
	if err != nil {
		return "", err
	}
	if cycle > 0 {
		return "", errors.New("circular combined-goal dependency")
	}
	var overlapping int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM financial_mappings m JOIN financial_shares s ON s.id=m.share_id WHERE m.destination_target=? AND s.source_project=? AND s.economic_key=? AND s.target_id!=?`, destinationTarget, share.Source, share.EconomicKey, share.Target).Scan(&overlapping); err != nil {
		return "", err
	}
	if overlapping > 0 {
		return "", errors.New("income stream is already included; do not count revenue and its settlement twice")
	}
	var existingID string
	var existingEvent int64
	err = tx.QueryRow(`SELECT m.id,m.component_event_id FROM financial_mappings m JOIN financial_shares s ON s.id=m.share_id WHERE m.destination_target=? AND s.source_project=? AND s.target_id=?`, destinationTarget, share.Source, share.Target).Scan(&existingID, &existingEvent)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if existingID != "" && existingEvent != eventID {
		return "", errors.New("source target already mapped to a different component for this period")
	}
	if _, err = validateComponent(tx, project, dest, eventID); err != nil {
		return "", err
	}
	id := existingID
	if id != "" {
		_, err = tx.Exec(`UPDATE financial_mappings SET share_id=?,definition_revision=?,approved_by=?,approved_at=?,enabled=1,last_error='refresh pending',last_attempt=0 WHERE id=?`, shareID, dest.UpdatedAt, actor, time.Now().UnixMilli(), id)
	} else {
		id = uuid.NewString()
		_, err = tx.Exec(`INSERT INTO financial_mappings(id,destination_project,destination_target,share_id,component_event_id,definition_revision,approved_by,approved_at) VALUES(?,?,?,?,?,?,?,?)`, id, project, destinationTarget, shareID, eventID, dest.UpdatedAt, actor, time.Now().UnixMilli())
	}
	if err != nil {
		return "", err
	}
	if err = queueFinancial(tx, project); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func refreshFinancialMappings(ctx context.Context, app *sdk.AppCtx, project, token string) error {
	db := contextualDB{db: app.AppDB(), ctx: ctx}
	rows, err := db.Query(`SELECT id FROM financial_mappings WHERE destination_project=? AND enabled=1 ORDER BY last_attempt,id LIMIT 16`, project)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	failures := []string{}
	for _, id := range ids {
		err = refreshFinancialMapping(ctx, app.AppDB(), project, id, token)
		if err != nil {
			failures = append(failures, err.Error())
			if _, e := db.Exec(`UPDATE financial_mappings SET last_attempt=?,last_error=? WHERE id=? AND destination_project=?`, time.Now().UnixMilli(), err.Error(), id, project); e != nil {
				return e
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
func refreshFinancialMapping(ctx context.Context, db *sql.DB, project, id, token string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `UPDATE financial_projects SET lease_until=lease_until WHERE project_id=? AND lease_token=? AND lease_until>?`, project, token, now)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errFinancialChanged
	}
	var shareID string
	var targetID, eventID, definition int64
	if err = tx.QueryRow(`SELECT share_id,destination_target,component_event_id,definition_revision FROM financial_mappings WHERE id=? AND destination_project=? AND enabled=1`, id, project).Scan(&shareID, &targetID, &eventID, &definition); err != nil {
		return err
	}
	share, err := authorizedFinancialShare(tx, project, shareID)
	if err != nil {
		return err
	}
	source, err := financialTarget(tx, share.Source, share.Target)
	if err != nil || source.UpdatedAt != share.Definition {
		return errors.New("source definition changed; sharing approval required")
	}
	dest, err := financialTarget(tx, project, targetID)
	if err != nil || dest.UpdatedAt != definition {
		return errors.New("destination definition changed; renew component mapping")
	}
	// Only this approved target's cached measurement is exported. No cross-project
	// event query/evaluation is performed by the destination worker.
	p, err := cachedTargetProgress(tx, source)
	if err != nil {
		return err
	}
	var input, current, success, verified, through int64
	var state string
	err = tx.QueryRow(`SELECT f.input_revision,p.revision,f.last_success,f.state,f.verified_revision,f.verified_through FROM financial_targets f JOIN financial_projects p ON p.project_id=? WHERE f.target_id=? AND p.enabled=1`, share.Source, source.ID).Scan(&input, &current, &success, &state, &verified, &through)
	if err != nil || p == nil || p.Status != "ok" || p.ActualValue == nil || p.MeasuredAt == nil {
		return errors.New("source measurement unavailable; last component retained")
	}
	if oneOf(state, "missing_fx", "source_unavailable") {
		return errors.New("source financial refresh failed; last component retained")
	}
	if input != current || success < now-financialReconcile.Milliseconds()-60000 {
		return errors.New("source refresh pending or stale; last component retained")
	}
	if *p.ActualValue == 0 {
		required := now - financialReconcile.Milliseconds()
		if source.PeriodEnd < required {
			required = source.PeriodEnd
		}
		if state != "confirmed_zero" || verified != current || through < required {
			return errors.New("source zero is unverified; reconcile source completeness first")
		}
	}
	raw, err := validateComponent(tx, project, dest, eventID)
	if err != nil {
		return err
	}
	value := *p.ActualValue
	if dest.Query.Aggregation == "sum_money" && dest.Query.AmountUnit == "minor" {
		value = math.Round(value * math.Pow10(currencyMinorDigits(dest.Currency)))
	}
	var props map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&props); err != nil {
		return err
	}
	if props == nil {
		return errors.New("component props must be an object")
	}
	if err = setFinancialProp(props, dest.Query.Value, value); err != nil {
		return err
	}
	if dest.Query.Aggregation == "sum_money" {
		if err = setFinancialProp(props, dest.Query.CurrencyField, dest.Currency); err != nil {
			return err
		}
	}
	// Source and aggregation timestamps are separate from the saved event period.
	meta := map[string]any{"mapping_id": id, "source_project": share.Source, "source_target": source.ID, "source_measured_at": *p.MeasuredAt, "metric_meaning": share.Meaning, "reporting_currency": dest.Currency}
	props["analytics_financial"] = meta
	encoded, err := json.Marshal(props)
	if err != nil {
		return err
	}
	// Stable source measurement => exact repeat is a no-op. Event timestamp,
	// identity and the saved period filters are intentionally preserved.
	if _, err = tx.Exec(`UPDATE events SET props=? WHERE id=? AND project_id=? AND props!=?`, string(encoded), eventID, project, string(encoded)); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE financial_mappings SET last_attempt=?,last_success=?,source_measured_at=?,last_error='' WHERE id=?`, now, now, *p.MeasuredAt, id); err != nil {
		return err
	}
	return tx.Commit()
}
func setFinancialProp(props map[string]any, path string, value any) error {
	if !strings.HasPrefix(path, "props.") {
		return errors.New("component amount must be a props field")
	}
	keys := strings.Split(strings.TrimPrefix(path, "props."), ".")
	if len(keys) == 0 || keys[0] == "analytics_financial" {
		return errors.New("invalid component property")
	}
	current := props
	for _, key := range keys[:len(keys)-1] {
		if key == "" {
			return errors.New("invalid nested property")
		}
		child, ok := current[key].(map[string]any)
		if !ok {
			return fmt.Errorf("component object %s does not exist", key)
		}
		current = child
	}
	current[keys[len(keys)-1]] = value
	return nil
}
func combinedTargetError(db sqlRunner, project string, target int64) error {
	rows, err := db.Query(`SELECT m.last_error,m.last_success,m.enabled,s.revoked_at FROM financial_mappings m JOIN financial_shares s ON s.id=m.share_id WHERE m.destination_project=? AND m.destination_target=?`, project, target)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var message string
		var success int64
		var enabled bool
		var revoked sql.NullInt64
		if err = rows.Scan(&message, &success, &enabled, &revoked); err != nil {
			return err
		}
		if revoked.Valid || !enabled {
			return errors.New("source sharing revoked or component disabled; combined amount is stale")
		}
		if message != "" {
			return errors.New(message)
		}
		if success < time.Now().Add(-financialReconcile-time.Minute).UnixMilli() {
			return errors.New("combined source refresh pending")
		}
	}
	return rows.Err()
}
