package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func scopeKey(ctx *sdk.AppCtx, args map[string]any) string {
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid
		}
	}
	if args != nil {
		if pid, _ := args["_project_id"].(string); strings.TrimSpace(pid) != "" {
			return strings.TrimSpace(pid)
		}
		if pid, _ := args["project_id"].(string); strings.TrimSpace(pid) != "" {
			return strings.TrimSpace(pid)
		}
	}
	return "global"
}

func normalizeCode(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 3 {
		return "", errors.New("currency code must be a three-letter ISO 4217 code")
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return "", errors.New("currency code must contain only ASCII letters")
		}
	}
	return code, nil
}

func getCurrency(db *sql.DB, code string) (CurrencyDefinition, error) {
	code, err := normalizeCode(code)
	if err != nil {
		return CurrencyDefinition{}, err
	}
	var c CurrencyDefinition
	var minor sql.NullInt64
	var active int
	err = db.QueryRow(`SELECT code,COALESCE(numeric_code,''),name,minor_units,kind,active,data_version
        FROM currency_definitions WHERE code=?`, code).Scan(
		&c.Code, &c.NumericCode, &c.Name, &minor, &c.Kind, &active, &c.DataVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return CurrencyDefinition{}, fmt.Errorf("unsupported ISO 4217 currency %s", code)
	}
	if err != nil {
		return CurrencyDefinition{}, err
	}
	if minor.Valid {
		v := int(minor.Int64)
		c.MinorUnits = &v
	}
	c.Active = active != 0
	return c, nil
}

func listCurrencies(db *sql.DB, q, kind string, active *bool, limit int) ([]CurrencyDefinition, error) {
	if limit <= 0 || limit > 500 {
		limit = 250
	}
	where := []string{"1=1"}
	args := []any{}
	if q = strings.TrimSpace(q); q != "" {
		where = append(where, "(code LIKE ? OR name LIKE ?)")
		like := "%" + strings.ToUpper(q) + "%"
		args = append(args, like, "%"+q+"%")
	}
	if kind = strings.ToLower(strings.TrimSpace(kind)); kind != "" {
		where = append(where, "kind=?")
		args = append(args, kind)
	}
	if active != nil {
		where = append(where, "active=?")
		args = append(args, boolInt(*active))
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT code,COALESCE(numeric_code,''),name,minor_units,kind,active,data_version
        FROM currency_definitions WHERE `+strings.Join(where, " AND ")+` ORDER BY code LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CurrencyDefinition{}
	for rows.Next() {
		var c CurrencyDefinition
		var minor sql.NullInt64
		var enabled int
		if err := rows.Scan(&c.Code, &c.NumericCode, &c.Name, &minor, &c.Kind, &enabled, &c.DataVersion); err != nil {
			return nil, err
		}
		if minor.Valid {
			v := int(minor.Int64)
			c.MinorUnits = &v
		}
		c.Active = enabled != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func insertObservation(db *sql.DB, projectID string, in ObservationInput) (RateObservation, bool, error) {
	base, err := normalizeCode(in.Base)
	if err != nil {
		return RateObservation{}, false, err
	}
	quote, err := normalizeCode(in.Quote)
	if err != nil {
		return RateObservation{}, false, err
	}
	if base == quote {
		return RateObservation{}, false, errors.New("rate observation currencies must differ")
	}
	_, canonical, err := parsePositiveDecimal(in.Rate)
	if err != nil {
		return RateObservation{}, false, err
	}
	if in.RateKind == "" {
		in.RateKind = "spot"
	}
	if in.Granularity == "" {
		in.Granularity = "instant"
	}
	if in.EffectiveAt.IsZero() {
		in.EffectiveAt = time.Now().UTC()
	}
	if in.EffectiveDate == "" {
		in.EffectiveDate = in.EffectiveAt.UTC().Format("2006-01-02")
	}
	if in.ObservedAt.IsZero() {
		in.ObservedAt = time.Now().UTC()
	}
	if in.ProviderSlug == "" {
		in.ProviderSlug = "manual"
	}
	if in.OriginalBase == "" {
		in.OriginalBase = base
	}
	if in.OriginalQuote == "" {
		in.OriginalQuote = quote
	}
	if in.AdapterVersion == "" {
		in.AdapterVersion = "v1"
	}
	flags, _ := json.Marshal(in.QualityFlags)
	var conn any
	if in.ConnectionID != 0 {
		conn = in.ConnectionID
	}
	res, err := db.Exec(`INSERT OR IGNORE INTO rate_observations
        (project_id,base,quote,rate_text,rate_kind,effective_at,effective_date,granularity,
         observed_at,provider_slug,connection_id,provider_ref,original_base,original_quote,
         payload_hash,adapter_version,quality_flags_json)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, base, quote, canonical, in.RateKind, in.EffectiveAt.UTC().Format(time.RFC3339Nano),
		in.EffectiveDate, in.Granularity, in.ObservedAt.UTC().Format(time.RFC3339Nano), in.ProviderSlug,
		conn, in.ProviderRef, strings.ToUpper(in.OriginalBase), strings.ToUpper(in.OriginalQuote),
		in.PayloadHash, in.AdapterVersion, string(flags))
	if err != nil {
		return RateObservation{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return RateObservation{}, false, err
	}
	created := affected > 0
	var id int64
	if created {
		id, _ = res.LastInsertId()
	}
	if !created {
		err = db.QueryRow(`SELECT id FROM rate_observations WHERE project_id=? AND provider_slug=?
			AND base=? AND quote=? AND rate_kind=? AND effective_at=? AND rate_text=?`,
			projectID, in.ProviderSlug, base, quote, in.RateKind,
			in.EffectiveAt.UTC().Format(time.RFC3339Nano), canonical).Scan(&id)
		if err != nil {
			return RateObservation{}, false, err
		}
	}
	obs, err := getObservation(db, projectID, id)
	return obs, created, err
}

const observationColumns = `id,project_id,base,quote,rate_text,rate_kind,effective_at,effective_date,
 granularity,observed_at,ingested_at,provider_slug,connection_id,provider_ref,original_base,
 original_quote,payload_hash,adapter_version,quality_flags_json,supersedes_rate_id`

type rowScanner interface{ Scan(...any) error }

func scanObservation(s rowScanner) (RateObservation, error) {
	var o RateObservation
	var conn, supersedes sql.NullInt64
	var flags string
	err := s.Scan(&o.ID, &o.ProjectID, &o.Base, &o.Quote, &o.Rate, &o.RateKind,
		&o.EffectiveAt, &o.EffectiveDate, &o.Granularity, &o.ObservedAt, &o.IngestedAt,
		&o.ProviderSlug, &conn, &o.ProviderRef, &o.OriginalBase, &o.OriginalQuote,
		&o.PayloadHash, &o.AdapterVersion, &flags, &supersedes)
	if err != nil {
		return RateObservation{}, err
	}
	if conn.Valid {
		v := conn.Int64
		o.ConnectionID = &v
	}
	if supersedes.Valid {
		v := supersedes.Int64
		o.SupersedesRateID = &v
	}
	_ = json.Unmarshal([]byte(flags), &o.QualityFlags)
	if o.QualityFlags == nil {
		o.QualityFlags = []string{}
	}
	return o, nil
}

func getObservation(db *sql.DB, projectID string, id int64) (RateObservation, error) {
	return scanObservation(db.QueryRow(`SELECT `+observationColumns+` FROM rate_observations WHERE project_id=? AND id=?`, projectID, id))
}

func candidateObservations(db *sql.DB, req SelectionRequest, base, quote string) ([]RateObservation, error) {
	where := []string{"project_id=?", "base=?", "quote=?", "effective_at<=?"}
	args := []any{req.ProjectID, base, quote, req.AsOf.UTC().Format(time.RFC3339Nano)}
	if req.Selection == "exact_date" {
		where = append(where, "effective_date=?")
		args = append(args, req.AsOf.UTC().Format("2006-01-02"))
	}
	if len(req.RateKinds) > 0 {
		where = append(where, "rate_kind IN ("+placeholders(len(req.RateKinds))+")")
		for _, v := range req.RateKinds {
			args = append(args, v)
		}
	}
	if len(req.Providers) > 0 {
		where = append(where, "provider_slug IN ("+placeholders(len(req.Providers))+")")
		for _, v := range req.Providers {
			args = append(args, v)
		}
	}
	rows, err := db.Query(`SELECT `+observationColumns+` FROM rate_observations WHERE `+
		strings.Join(where, " AND ")+` ORDER BY effective_at DESC,id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RateObservation{}
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func historyObservations(db *sql.DB, projectID, base, quote, from, to string, providers, kinds []string, limit int) ([]RateObservation, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	where := []string{"project_id=?", "base=?", "quote=?"}
	args := []any{projectID, base, quote}
	if from != "" {
		where = append(where, "effective_at>=?")
		args = append(args, from)
	}
	if to != "" {
		where = append(where, "effective_at<=?")
		args = append(args, to)
	}
	for _, item := range []struct {
		column string
		values []string
	}{{"provider_slug", providers}, {"rate_kind", kinds}} {
		if len(item.values) > 0 {
			where = append(where, item.column+" IN ("+placeholders(len(item.values))+")")
			for _, v := range item.values {
				args = append(args, v)
			}
		}
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+observationColumns+` FROM rate_observations WHERE `+
		strings.Join(where, " AND ")+` ORDER BY effective_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RateObservation{}
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func trackPair(db *sql.DB, projectID, base, quote string) error {
	if base == quote {
		return nil
	}
	_, err := db.Exec(`INSERT INTO tracked_pairs(project_id,base,quote,requested_at)
        VALUES(?,?,?,CURRENT_TIMESTAMP)
        ON CONFLICT(project_id,base,quote) DO UPDATE SET enabled=1,requested_at=CURRENT_TIMESTAMP`,
		projectID, base, quote)
	return err
}

func trackedPairs(db *sql.DB, projectID string) ([][2]string, error) {
	rows, err := db.Query(`SELECT base,quote FROM tracked_pairs WHERE project_id=? AND enabled=1 ORDER BY requested_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][2]string{}
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func providerPriority(ctx *sdk.AppCtx) map[string]int {
	order := "alpaca-market-data,saltedge,alpha-vantage,manual"
	if ctx != nil && strings.TrimSpace(ctx.Config().Get("provider_priority")) != "" {
		order = ctx.Config().Get("provider_priority")
	}
	out := map[string]int{}
	for i, slug := range splitCSV(order) {
		out[slug] = (i + 1) * 10
	}
	return out
}

func sortCandidates(ctx *sdk.AppCtx, rows []RateObservation) {
	priority := providerPriority(ctx)
	sort.SliceStable(rows, func(i, j int) bool {
		pi, ok := priority[rows[i].ProviderSlug]
		if !ok {
			pi = 1000
		}
		pj, ok := priority[rows[j].ProviderSlug]
		if !ok {
			pj = 1000
		}
		if pi != pj {
			return pi < pj
		}
		return rows[i].EffectiveAt > rows[j].EffectiveAt
	})
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
