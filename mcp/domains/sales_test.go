package main

import (
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestSpaceshipListingMoneyContract(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"create_sellerhub_domain": successJSON(map[string]any{"name": "vectorlantern.com"})})
	ctx := newTestCtx(t, plat)
	_, err := (&App{}).toolDomainSalePublish(ctx, map[string]any{"domain": "vectorlantern.com", "listing_type": "buy_now_and_make_offer", "price": "145", "minimum_offer": "100.50"})
	if err != nil {
		t.Fatal(err)
	}
	p := plat.callsFor("create_sellerhub_domain")[0].Input
	if p["name"] != "vectorlantern.com" || p["domain"] != nil || p["binPriceEnabled"] != true || p["minPriceEnabled"] != true {
		t.Fatal(p)
	}
	price := p["binPrice"].(map[string]any)
	if price["amount"] != "145.00" || price["currency"] != "USD" {
		t.Fatal(p)
	}
	if p["minPrice"].(map[string]any)["amount"] != "100.50" {
		t.Fatal(p)
	}
}
func TestSpaceshipRejectsBadListingPrices(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx := newTestCtx(t, plat)
	for _, args := range []map[string]any{{"price": "0"}, {"price": "NaN"}, {"price": "145.001"}, {"price": "145", "minimum_offer": "146"}, {"listing_type": "unknown", "price": "145"}} {
		args["domain"] = "vectorlantern.com"
		if _, err := (&App{}).toolDomainSalePublish(ctx, args); err == nil {
			t.Fatal(args)
		}
	}
	if len(plat.calls) != 0 {
		t.Fatal("invalid listing submitted")
	}
}
