package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func valuationConnection(ctx *sdk.AppCtx, args map[string]any) (*sdk.BoundIntegration, error) {
	id := int64(intArg(args, "connection_id", 0))
	if id == 0 {
		id, _ = selectedConnectionID(ctx, "valuation_provider")
	}
	if id == 0 {
		return nil, errors.New("no valuation_provider connection selected")
	}
	c, e := ctx.PlatformAPI().GetConnection(id)
	if e != nil {
		return nil, e
	}
	if c == nil || c.AppSlug != "replicate" {
		return nil, errors.New("valuation provider must be Replicate")
	}
	return &sdk.BoundIntegration{Role: "valuation_provider", Kind: "integration", ConnectionID: id, AppSlug: c.AppSlug}, nil
}
func (a *App) toolDomainEstimateStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	b, e := valuationConnection(ctx, args)
	if e != nil {
		return nil, e
	}
	d := strings.TrimSpace(strArg(args, "domains"))
	if d == "" {
		d = strArg(args, "domain")
	}
	if d == "" {
		return nil, errors.New("domain or domains required")
	}
	pid := connectionProject(ctx, args)
	raw, e := providerCall(ctx, b, "appraise_domains", map[string]any{"domains": d})
	if e != nil {
		return nil, e
	}
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	predID := fmt.Sprint(env["id"])
	_, e = ctx.AppDB().Exec(`INSERT INTO domain_valuations(project_id,domain,provider_slug,status,prediction_id,raw_json) VALUES(?,?,?,?,?,?)`, pid, d, b.AppSlug, fmt.Sprint(env["status"]), predID, string(raw))
	if e != nil {
		return nil, e
	}
	return map[string]any{"provider": b.AppSlug, "domain": d, "prediction_id": predID, "status": env["status"], "raw": env}, nil
}
func (a *App) toolDomainEstimateList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := connectionProject(ctx, args)
	rows, e := ctx.AppDB().Query(`SELECT id,domain,provider_slug,status,auction,marketplace,brokerage,prediction_id,raw_json,error_message,created_at FROM domain_valuations WHERE project_id=? ORDER BY created_at DESC LIMIT 200`, pid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var d, p, s, pred, raw, er, at string
		var a, m, b *float64
		if e = rows.Scan(&id, &d, &p, &s, &a, &m, &b, &pred, &raw, &er, &at); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "domain": d, "provider": p, "status": s, "auction": a, "marketplace": m, "brokerage": b, "prediction_id": pred, "raw": raw, "error": er, "created_at": at})
	}
	return map[string]any{"valuations": out}, rows.Err()
}
