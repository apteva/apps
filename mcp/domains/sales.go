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
		var err error
		id, err = selectedConnectionID(ctx, "registrar_provider", "dns_provider")
		if err != nil {
			return nil, err
		}
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
	if c.ProjectID != "" && c.ProjectID != connectionProject(ctx, args) {
		return nil, errors.New("sale connection belongs to another project")
	}
	if c.Status != "" && c.Status != "active" {
		return nil, errors.New("sale connection is not active")
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
	mode := strArg(args, "listing_type")
	if mode == "" {
		mode = "buy_now"
	}
	if !includes([]string{"buy_now", "make_offer", "buy_now_and_make_offer"}, mode) {
		return nil, errors.New("invalid listing_type")
	}
	p = map[string]any{"name": d, "binPriceEnabled": mode != "make_offer", "minPriceEnabled": false}
	money := func(value string) (map[string]any, error) {
		cents, err := decimalCents(strings.TrimSpace(value))
		if err != nil || cents <= 0 {
			return nil, errors.New("listing prices must be positive USD amounts with at most two decimal places")
		}
		return map[string]any{"amount": fmt.Sprintf("%d.%02d", cents/100, cents%100), "currency": "USD"}, nil
	}
	if mode != "make_offer" {
		price, err := money(strArg(args, "price"))
		if err != nil {
			return nil, err
		}
		p["binPrice"] = price
	}
	if offer := strArg(args, "minimum_offer"); offer != "" {
		minimum, err := money(offer)
		if err != nil {
			return nil, err
		}
		p["minPriceEnabled"] = true
		p["minPrice"] = minimum
		if mode != "make_offer" {
			lo, _ := decimalCents(offer)
			hi, _ := decimalCents(strArg(args, "price"))
			if lo > hi {
				return nil, errors.New("minimum offer cannot exceed buy-now price")
			}
		}
	}
	if desc := strArg(args, "description"); desc != "" {
		p["description"] = desc
	}
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
