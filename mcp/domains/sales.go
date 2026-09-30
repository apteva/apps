package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func saleConnection(ctx *sdk.AppCtx, args map[string]any) (*sdk.BoundIntegration, error) {
	id := int64(intArg(args, "connection_id", 0))
	if id == 0 {
		id, _ = selectedConnectionID(ctx, "registrar_provider", "dns_provider")
	}
	if id == 0 {
		return nil, errors.New("no provider connection selected")
	}
	c, e := ctx.PlatformAPI().GetConnection(id)
	if e != nil {
		return nil, e
	}
	if c == nil {
		return nil, errors.New("connection not found")
	}
	if c.AppSlug != "dynadot" && c.AppSlug != "spaceship" {
		return nil, fmt.Errorf("provider %q does not support marketplace sales", c.AppSlug)
	}
	return &sdk.BoundIntegration{Role: "registrar_provider", Kind: "integration", ConnectionID: id, AppSlug: c.AppSlug}, nil
}
func (a *App) toolDomainSalePublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	b, e := saleConnection(ctx, args)
	if e != nil {
		return nil, e
	}
	d, e := normaliseDomainName(strArg(args, "domain"))
	if e != nil {
		return nil, e
	}
	p := map[string]any{"domain": d}
	if b.AppSlug == "dynadot" {
		p["listing_type"] = strArg(args, "listing_type")
		p["price"] = strArg(args, "price")
		p["minimum_offer"] = strArg(args, "minimum_offer")
		p["description"] = strArg(args, "description")
		raw, e := providerCall(ctx, b, "set_for_sale", p)
		return map[string]any{"provider": b.AppSlug, "domain": d, "status": "listed", "raw": raw}, e
	}
	p["domain"] = d
	raw, e := providerCall(ctx, b, "create_sellerhub_domain", p)
	return map[string]any{"provider": b.AppSlug, "domain": d, "status": "listed", "raw": raw}, e
}
func (a *App) toolDomainSaleRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	b, e := saleConnection(ctx, args)
	if e != nil {
		return nil, e
	}
	d, e := normaliseDomainName(strArg(args, "domain"))
	if e != nil {
		return nil, e
	}
	tool := "remove_for_sale"
	if b.AppSlug == "spaceship" {
		tool = "delete_sellerhub_domain"
	}
	raw, e := providerCall(ctx, b, tool, map[string]any{"domain": d})
	return map[string]any{"provider": b.AppSlug, "domain": d, "status": "unlisted", "raw": raw}, e
}
func (a *App) toolDomainSaleGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	b, e := saleConnection(ctx, args)
	if e != nil {
		return nil, e
	}
	d, e := normaliseDomainName(strArg(args, "domain"))
	if e != nil {
		return nil, e
	}
	tool := "get_domain_info"
	if b.AppSlug == "spaceship" {
		tool = "get_sellerhub_domain"
	}
	raw, e := providerCall(ctx, b, tool, map[string]any{"domain": d})
	return map[string]any{"provider": b.AppSlug, "domain": d, "raw": raw}, e
}

var _ = strings.TrimSpace
