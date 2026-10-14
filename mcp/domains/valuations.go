package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
	"strings"
)

func valuationConnection(ctx *sdk.AppCtx, args map[string]any) (*sdk.BoundIntegration, error) {
	id := int64(intArg(args, "connection_id", 0))
	if id == 0 {
		var err error
		id, err = selectedConnectionID(ctx, "valuation_provider")
		if err != nil {
			return nil, err
		}
	}
	if id == 0 {
		return nil, errors.New("no valuation_provider connection selected")
	}
	c, err := ctx.PlatformAPI().GetConnection(id)
	if err != nil {
		return nil, err
	}
	if c == nil || c.AppSlug != "replicate" {
		return nil, errors.New("valuation provider must be Replicate")
	}
	if c.ProjectID != "" && c.ProjectID != connectionProject(ctx, args) {
		return nil, errors.New("valuation connection belongs to another project")
	}
	if c.Status != "" && c.Status != "active" {
		return nil, errors.New("valuation connection is not active")
	}
	return &sdk.BoundIntegration{Role: "valuation_provider", Kind: "integration", ConnectionID: id, AppSlug: c.AppSlug}, nil
}

func valuationDomains(args map[string]any) ([]string, error) {
	input := strArg(args, "domains")
	if input == "" {
		input = strArg(args, "domain")
	}
	parts := strings.Split(input, ",")
	if len(parts) > 2560 {
		return nil, errors.New("at most 2560 domains per estimate")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range parts {
		d, err := normaliseDomainName(s)
		if err != nil {
			return nil, err
		}
		if !seen[d] {
			out = append(out, d)
			seen[d] = true
		}
	}
	return out, nil
}

func valuationEnvelope(raw json.RawMessage) (map[string]any, error) {
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("invalid prediction response: %w", err)
	}
	if strArg(env, "id") == "" || !includes([]string{"starting", "processing", "succeeded", "failed", "canceled"}, strArg(env, "status")) {
		return nil, errors.New("prediction response missing ID or valid status")
	}
	return env, nil
}

func (a *App) toolDomainEstimateStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domains, err := valuationDomains(args)
	if err != nil {
		return nil, err
	}
	b, err := valuationConnection(ctx, args)
	if err != nil {
		return nil, err
	}
	raw, err := providerCall(ctx, b, "appraise_domains", map[string]any{"domains": strings.Join(domains, ",")})
	if err != nil {
		return nil, err
	}
	env, err := valuationEnvelope(raw)
	if err != nil {
		return nil, err
	}
	pred := strArg(env, "id")
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, domain := range domains {
		if _, err = tx.Exec(`INSERT INTO domain_valuations(project_id,domain,provider_slug,status,prediction_id,raw_json,connection_id) VALUES(?,?,?,?,?,?,?)`, pid, domain, b.AppSlug, strArg(env, "status"), pred, string(raw), b.ConnectionID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = storeValuationPrediction(ctx, pid, pred, raw, env); err != nil {
		return nil, err
	}
	return map[string]any{"provider": b.AppSlug, "domain": strings.Join(domains, ","), "prediction_id": pred, "status": env["status"], "raw": env}, nil
}

// Persist each returned domain separately; zero is a valid estimate and must not become NULL.
func valuationPrices(env map[string]any, domain string) (any, any, any, string) {
	errorText := strArg(env, "error")
	if strArg(env, "status") != "succeeded" {
		return nil, nil, nil, errorText
	}
	output, ok := env["output"].(map[string]any)
	if !ok {
		return nil, nil, nil, "prediction succeeded without valuation output"
	}
	if e := strArg(output, "error"); e != "" {
		return nil, nil, nil, e
	}
	vals, _ := output["valuations"].([]any)
	for _, v := range vals {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		d, err := normaliseDomainName(strArg(m, "domain"))
		if err != nil || d != domain {
			continue
		}
		if e := strArg(m, "error"); e != "" {
			return nil, nil, nil, e
		}
		prices := make([]float64, 3)
		for i, key := range []string{"auction", "marketplace", "brokerage"} {
			n, ok := m[key].(float64)
			if !ok || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, nil, nil, "invalid or missing " + key + " estimate"
			}
			prices[i] = n
		}
		return prices[0], prices[1], prices[2], ""
	}
	return nil, nil, nil, "prediction did not return this domain"
}

func storeValuationPrediction(ctx *sdk.AppCtx, pid, pred string, raw json.RawMessage, env map[string]any) error {
	rows, err := ctx.AppDB().Query(`SELECT id,domain FROM domain_valuations WHERE project_id=? AND prediction_id=?`, pid, pred)
	if err != nil {
		return err
	}
	type item struct {
		id     int64
		domain string
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.domain); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range items {
		auction, marketplace, brokerage, e := valuationPrices(env, v.domain)
		status := strArg(env, "status")
		if status == "succeeded" && e != "" {
			status = "failed"
		}
		if _, err = tx.Exec(`UPDATE domain_valuations SET status=?,auction=?,marketplace=?,brokerage=?,raw_json=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, status, auction, marketplace, brokerage, string(raw), e, v.id, pid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) refreshValuations(ctx *sdk.AppCtx, pid, domain string) []string {
	rows, err := ctx.AppDB().Query(`SELECT DISTINCT prediction_id,connection_id FROM domain_valuations WHERE project_id=? AND status IN ('starting','processing') AND (?='' OR domain=?) ORDER BY prediction_id LIMIT 20`, pid, domain, domain)
	if err != nil {
		return []string{err.Error()}
	}
	type pending struct {
		pred string
		conn int64
	}
	jobs := []pending{}
	for rows.Next() {
		var v pending
		if err = rows.Scan(&v.pred, &v.conn); err != nil {
			break
		}
		jobs = append(jobs, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return []string{err.Error()}
	}
	warnings := []string{}
	for _, job := range jobs {
		b, e := valuationConnection(ctx, map[string]any{"connection_id": job.conn, "_project_id": pid})
		if e == nil {
			var raw json.RawMessage
			raw, e = providerCall(ctx, b, "get_prediction", map[string]any{"prediction_id": job.pred})
			if e == nil {
				var env map[string]any
				env, e = valuationEnvelope(raw)
				if e == nil && strArg(env, "id") != job.pred {
					e = errors.New("prediction ID mismatch")
				}
				if e == nil {
					e = storeValuationPrediction(ctx, pid, job.pred, raw, env)
				}
			}
		}
		if e != nil {
			warnings = append(warnings, e.Error())
		}
	}
	return warnings
}

func (a *App) toolDomainEstimateList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domain := strArg(args, "domain")
	if domain != "" {
		var err error
		domain, err = normaliseDomainName(domain)
		if err != nil {
			return nil, err
		}
	}
	warnings := []string{}
	if boolArg(args, "refresh", false) {
		warnings = a.refreshValuations(ctx, pid, domain)
	}
	rows, err := ctx.AppDB().Query(`SELECT id,domain,provider_slug,status,auction,marketplace,brokerage,prediction_id,raw_json,error_message,created_at FROM domain_valuations WHERE project_id=? AND (?='' OR domain=?) ORDER BY created_at DESC,id DESC LIMIT 200`, pid, domain, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var d, p, s, pred, raw, er, at string
		var auction, marketplace, brokerage *float64
		if err = rows.Scan(&id, &d, &p, &s, &auction, &marketplace, &brokerage, &pred, &raw, &er, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "domain": d, "provider": p, "status": s, "auction": auction, "marketplace": marketplace, "brokerage": brokerage, "prediction_id": pred, "raw": raw, "error": er, "created_at": at})
	}
	return map[string]any{"valuations": out, "warnings": warnings}, rows.Err()
}
