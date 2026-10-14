package main

import (
	"encoding/json"
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
		_, e := providerCall(ctx, b, "set_for_sale", p)
		if e != nil {
			return nil, e
		}
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return nil, err
		}
		snapshot, err := saveListing(ctx, pid, d, b, "on_sale", "listed", map[string]any{"name": d, "isForSale": true, "listing_type": p["listing_type"], "price": p["price"], "minimum_offer": p["minimum_offer"]}, "publish")
		if err != nil {
			return nil, err
		}
		return snapshotResult(snapshot), nil
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
	if e != nil {
		return nil, e
	}
	var listing map[string]any
	if json.Unmarshal(raw, &listing) != nil || strArg(listing, "name") != d || strArg(listing, "status") == "" {
		return nil, errors.New("Spaceship returned an unconfirmed listing; inspect its status before publishing again")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	state, status, clean, err := normalizeListing(b.AppSlug, d, listing)
	if err != nil {
		return nil, err
	}
	snapshot, err := saveListing(ctx, pid, d, b, state, status, clean, "publish")
	if err != nil {
		return nil, err
	}
	return snapshotResult(snapshot), nil
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
	_, e = providerCall(ctx, b, tool, map[string]any{"domain": d})
	if e != nil {
		return nil, e
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	snapshot, err := saveListing(ctx, pid, d, b, "unlisted", "unlisted", map[string]any{"name": d, "status": "unlisted"}, "remove")
	if err != nil {
		return nil, err
	}
	return snapshotResult(snapshot), nil
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
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	snapshot, err := listingGet(ctx.AppDB(), pid, d, b.ConnectionID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || boolArg(args, "refresh", false) {
		refreshed, e := refreshListing(ctx, pid, d, b, "manual")
		if e != nil {
			snapshot, _ = listingGet(ctx.AppDB(), pid, d, b.ConnectionID)
			if snapshot == nil {
				return nil, e
			}
		} else {
			snapshot = refreshed
		}
	}
	return snapshotResult(snapshot), nil
}
