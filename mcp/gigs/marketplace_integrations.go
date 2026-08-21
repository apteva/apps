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
	comp, err := loadGigCompensation(ctx.AppDB(), pid, gigID)
	if err != nil {
		return nil, nil, err
	}
	if comp == nil {
		return nil, nil, errors.New("gig has no compensation snapshot")
	}
	if comp.WorkerAmountMinor <= 0 {
		return comp, nil, errors.New("gig has no payable worker compensation")
	}
	if comp.PayableBillID > 0 {
		return comp, &billsBillRef{ID: comp.PayableBillID, Status: "linked", TotalCents: comp.WorkerAmountMinor}, nil
	}
	g, err := loadGig(ctx, pid, gigID)
	if err != nil {
		return comp, nil, err
	}
	if g == nil {
		return comp, nil, errors.New("gig not found")
	}
	if g.Status != "reviewed" {
		return comp, nil, errors.New("payable can only be created after gig review")
	}
	wid := comp.WorkerID
	if wid == 0 && len(g.Assignments) > 0 {
		for _, ass := range g.Assignments {
			if ass.Status == "reviewed" {
				wid = ass.WorkerID
				break
			}
		}
	}
	if wid == 0 {
		return comp, nil, errors.New("compensation snapshot has no reviewed worker")
	}
	w, err := getWorker(ctx.AppDB(), pid, wid)
	if err != nil {
		return comp, nil, err
	}
	if w == nil {
		return comp, nil, errors.New("worker not found")
	}
	contact, err := crmGetContact(ctx, pid, w.ContactID)
	if err != nil {
		return comp, nil, err
	}
	if contact == nil || contact.PrimaryEmail == "" {
		return comp, nil, errors.New("worker needs a primary email before creating a Bills vendor")
	}
	api := ctx.WithProject(pid).PlatformAPI()
	var vendorOut struct {
		Vendor *billsVendorRef `json:"vendor"`
	}
	name := contact.DisplayName
	if name == "" {
		name = strings.TrimSpace(contact.FirstName + " " + contact.LastName)
	}
	if err := api.CallAppResult("bills", "vendors_upsert_by_email", map[string]any{"email": contact.PrimaryEmail, "defaults": map[string]any{"name": name, "phone": contact.PrimaryPhone, "currency": comp.Currency}}, &vendorOut); err != nil {
		return markPayableFailure(ctx, pid, comp, fmt.Errorf("bills vendor upsert: %w", err))
	}
	if vendorOut.Vendor == nil || vendorOut.Vendor.ID == 0 {
		return markPayableFailure(ctx, pid, comp, errors.New("bills vendor upsert returned no vendor"))
	}
	invoiceNumber := fmt.Sprintf("GIG-%d", gigID)
	var billOut struct {
		Bill *billsBillRef `json:"bill"`
	}
	createArgs := map[string]any{"vendor_id": vendorOut.Vendor.ID, "vendor_invoice_number": invoiceNumber, "currency": comp.Currency,
		"line_items":  []any{map[string]any{"description": "Gig: " + g.Title, "quantity": 1, "unit_price_cents": comp.WorkerAmountMinor}},
		"total_cents": comp.WorkerAmountMinor, "category": "contractor-compensation", "notes": fmt.Sprintf("Generated from Gigs gig %d", gigID),
		"metadata": map[string]any{"source_app": "gigs", "gig_id": gigID, "contract_id": comp.ContractID, "worker_id": wid, "rate_source": comp.RateSource},
	}
	if _, err = ctx.AppDB().Exec(`UPDATE gig_compensation SET payable_status='pending',payable_error=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, comp.ID); err != nil {
		return comp, nil, err
	}
	if err = api.CallAppResult("bills", "bills_create", createArgs, &billOut); err != nil {
		var recovered struct {
			Bill *billsBillRef `json:"bill"`
		}
		if getErr := api.CallAppResult("bills", "bills_get", map[string]any{"vendor_id": vendorOut.Vendor.ID, "vendor_invoice_number": invoiceNumber}, &recovered); getErr == nil && recovered.Bill != nil {
			billOut = recovered
		} else {
			return markPayableFailure(ctx, pid, comp, fmt.Errorf("bills create: %w", err))
		}
	}
	if billOut.Bill == nil || billOut.Bill.ID == 0 {
		return markPayableFailure(ctx, pid, comp, errors.New("bills create returned no bill"))
	}
	_, err = ctx.AppDB().Exec(`UPDATE gig_compensation SET payable_status='created',payable_bill_id=?,payable_error=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, billOut.Bill.ID, comp.ID)
	if err != nil {
		return comp, billOut.Bill, err
	}
	comp, err = loadGigCompensation(ctx.AppDB(), pid, gigID)
	if err == nil {
		ctx.EmitWithProject("gig.payable_created", pid, map[string]any{"gig_id": gigID, "bill_id": billOut.Bill.ID, "amount_minor": comp.WorkerAmountMinor, "currency": comp.Currency})
	}
	return comp, billOut.Bill, err
}

func markPayableFailure(ctx *sdk.AppCtx, pid string, comp *gigCompensation, cause error) (*gigCompensation, *billsBillRef, error) {
	if comp != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE gig_compensation SET payable_status='failed',payable_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cause.Error(), comp.ID)
		comp, _ = loadGigCompensation(ctx.AppDB(), pid, comp.GigID)
	}
	return comp, nil, cause
}
