package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type catalogProductRef struct {
	ID int64 `json:"id"`
}

type catalogPriceRef struct {
	ID              int64  `json:"id"`
	ProductID       int64  `json:"product_id"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	Currency        string `json:"currency"`
	Interval        string `json:"interval,omitempty"`
	Active          bool   `json:"active"`
}

// syncOfferToCatalog projects Gigs' domain-rich offer into Catalog's
// product/immutable-price model. Catalog remains the sell-side source of truth;
// Gigs stores only IDs and snapshots the chosen price on a contract/gig.
func syncOfferToCatalog(ctx *sdk.AppCtx, pid string, offer *standardOffer) (map[string]any, error) {
	if offer == nil {
		return nil, errors.New("offer required")
	}
	api := ctx.WithProject(pid).PlatformAPI()
	productID := offer.CatalogProductID
	if productID == 0 {
		var out struct {
			Product *catalogProductRef `json:"product"`
		}
		err := api.CallAppResult("catalog", "catalog_products_create", map[string]any{
			"name": offer.Name, "slug": "gigs-" + offer.Slug, "type": "service",
			"description": offer.Description, "category": offer.Category,
			"metadata": map[string]any{"source_app": "gigs", "offer_id": offer.ID, "offer_version": offer.Version},
		}, &out)
		if err != nil {
			return nil, fmt.Errorf("catalog product create: %w", err)
		}
		if out.Product == nil || out.Product.ID == 0 {
			return nil, errors.New("catalog product create returned no product")
		}
		productID = out.Product.ID
		if _, err := ctx.AppDB().Exec(`UPDATE standard_offers SET catalog_product_id=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, productID, pid, offer.ID); err != nil {
			return nil, err
		}
	} else {
		var ignored map[string]any
		if err := api.CallAppResult("catalog", "catalog_products_update", map[string]any{"id": productID, "patch": map[string]any{
			"name": offer.Name, "description": offer.Description, "category": offer.Category,
			"metadata": map[string]any{"source_app": "gigs", "offer_id": offer.ID, "offer_version": offer.Version},
		}}, &ignored); err != nil {
			return nil, fmt.Errorf("catalog product update: %w", err)
		}
	}

	synced := make([]map[string]any, 0)
	for _, pkg := range offer.Packages {
		if !pkg.Active {
			if pkg.CatalogPriceID > 0 {
				var ignored map[string]any
				if err := api.CallAppResult("catalog", "catalog_prices_archive", map[string]any{"id": pkg.CatalogPriceID}, &ignored); err != nil {
					return nil, fmt.Errorf("archive removed Catalog price for package %s: %w", pkg.Slug, err)
				}
			}
			continue
		}
		if pkg.CustomerAmountMinor <= 0 {
			return nil, fmt.Errorf("package %s needs customer_amount_minor > 0 before publish", pkg.Slug)
		}
		currency, err := normaliseCurrency(pkg.Currency)
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", pkg.Slug, err)
		}
		needsPrice := pkg.CatalogPriceID == 0
		if pkg.CatalogPriceID > 0 {
			var got struct {
				Price *catalogPriceRef `json:"price"`
			}
			if err := api.CallAppResult("catalog", "catalog_prices_get", map[string]any{"id": pkg.CatalogPriceID}, &got); err != nil {
				needsPrice = true
			} else if got.Price == nil || got.Price.UnitAmountCents != pkg.CustomerAmountMinor || !strings.EqualFold(got.Price.Currency, currency) || !got.Price.Active {
				var ignored map[string]any
				_ = api.CallAppResult("catalog", "catalog_prices_archive", map[string]any{"id": pkg.CatalogPriceID}, &ignored)
				needsPrice = true
			}
		}
		if needsPrice {
			priceArgs := map[string]any{"product_id": productID, "unit_amount_cents": pkg.CustomerAmountMinor, "currency": currency,
				"nickname": pkg.Name, "unit_label": pkg.Unit, "unit_size": int64(1), "metadata": map[string]any{
					"source_app": "gigs", "offer_id": offer.ID, "offer_version": offer.Version, "package_id": pkg.ID, "package_slug": pkg.Slug,
				}}
			if pkg.PricingModel == "recurring" {
				priceArgs["interval"] = "month"
			}
			var got struct {
				Price *catalogPriceRef `json:"price"`
			}
			if err := api.CallAppResult("catalog", "catalog_prices_create", priceArgs, &got); err != nil {
				return nil, fmt.Errorf("catalog price create for %s: %w", pkg.Slug, err)
			}
			if got.Price == nil || got.Price.ID == 0 {
				return nil, fmt.Errorf("catalog price create for %s returned no price", pkg.Slug)
			}
			pkg.CatalogPriceID = got.Price.ID
			if _, err := ctx.AppDB().Exec(`UPDATE offer_packages SET catalog_price_id=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pkg.CatalogPriceID, pid, pkg.ID); err != nil {
				return nil, err
			}
		}
		synced = append(synced, map[string]any{"package_id": pkg.ID, "catalog_price_id": pkg.CatalogPriceID})
	}
	return map[string]any{"catalog_product_id": productID, "prices": synced}, nil
}

type billsVendorRef struct {
	ID int64 `json:"id"`
}
type billsBillRef struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	TotalCents int64  `json:"total_cents"`
}

// createGigPayable turns an approved gig's immutable compensation snapshot
// into a Bills AP record. The deterministic vendor invoice number lets a
// retry recover a remotely-created bill if the local link update was lost.
func createGigPayable(ctx *sdk.AppCtx, pid string, gigID int64) (*gigCompensation, *billsBillRef, error) {
	f, e := loadFinancials(ctx, pid, gigID)
	if e != nil {
		return nil, nil, e
	}
	if len(f.Obligations) == 0 {
		return nil, nil, errors.New("approve compensation in Financials before creating a payable")
	}
	for _, o := range f.Obligations {
		if e = syncFinancialObligation(ctx, pid, o.ID); e != nil {
			return f.Legacy, nil, e
		}
	}
	f, e = loadFinancials(ctx, pid, gigID)
	if e != nil {
		return nil, nil, e
	}
	for _, o := range f.Obligations {
		if o.Bill != nil {
			return f.Legacy, &billsBillRef{ID: o.Bill.ID, Status: o.Bill.Status, TotalCents: o.Bill.Total}, nil
		}
	}
	return f.Legacy, nil, errors.New("Bills is optional and is not connected or payable is not applicable")
}

func markPayableFailure(ctx *sdk.AppCtx, pid string, comp *gigCompensation, cause error) (*gigCompensation, *billsBillRef, error) {
	if comp != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE gig_compensation SET payable_status='failed',payable_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cause.Error(), comp.ID)
		comp, _ = loadGigCompensation(ctx.AppDB(), pid, comp.GigID)
	}
	return comp, nil, cause
}
