package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type ovhFixture struct {
	product                                                                     string
	checkoutLost, installLost, missing, priceChanged, wrongPlan, recoveredOrder bool
	terminated, confirmed, installs, purchases                                  int
	calls                                                                       []string
}

func (f *ovhFixture) call(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	f.calls = append(f.calls, tool)
	prefix := "dedicated"
	if f.product == "vps" {
		prefix = "vps"
	}
	switch tool {
	case "get_account":
		return providerJSON(`{"ovhSubsidiary":"ES"}`), nil
	case "vps_catalog", "dedicated_catalog", "eco_catalog":
		product := "vps"
		if tool == "dedicated_catalog" {
			product = "baremetalServers"
		}
		if tool == "eco_catalog" {
			product = "eco"
		}
		dc, osName, osValue := "dedicated_datacenter", "dedicated_os", "none_64.en"
		if product == "vps" {
			dc, osName, osValue = "vps_datacenter", "vps_os", "Ubuntu 24.04"
		}
		return providerJSON(fmt.Sprintf(`{"locale":{"currencyCode":"EUR"},"plans":[{"planCode":"plan-1","invoiceName":"Test offer","configurations":[{"name":%q,"isMandatory":true,"values":["MAD"]},{"name":%q,"isMandatory":true,"values":[%q]}],"addonFamilies":[{"name":"memory","mandatory":true,"exclusive":true,"default":"ram-1","addons":["ram-1","ram-2"]}],"pricings":[{"mode":"default","interval":1,"intervalUnit":"month","price":1200000000,"capacities":["installation","renew"]}]}]}`, dc, osName, osValue)), nil
	case "dedicated_images_info":
		return providerJSON(`[{"templateName":"ubuntu2404_64","description":"Ubuntu 24.04","family":"linux","endOfInstall":"2099-01-01","project":{"os":{"name":"Ubuntu 24.04"}}},{"templateName":"ubuntu1804_64","description":"Ubuntu 18.04","family":"linux","endOfInstall":"2020-01-01"}]`), nil
	case "cart_create":
		return providerJSON(`{"cartId":"owned-cart"}`), nil
	case "cart_vps_option_add", "cart_dedicated_option_add", "cart_eco_option_add":
		return providerJSON(`{"itemId":22}`), nil
	case "cart_assign", "cart_configuration_add":
		return providerJSON(`{}`), nil
	case "cart_vps_plans", "cart_dedicated_plans", "cart_eco_plans":
		return providerJSON(`[{"planCode":"plan-1","prices":[{"duration":"P1M","pricingMode":"default"}]}]`), nil
	case "cart_vps_add", "cart_dedicated_add", "cart_eco_add":
		return providerJSON(`{"itemId":21}`), nil
	case "cart_required_configuration":
		return providerJSON(`[]`), nil
	case "cart_quote":
		amount := 12
		if f.priceChanged {
			amount = 18
		}
		orderID := "null"
		if f.recoveredOrder {
			orderID = "123"
		}
		return providerJSON(fmt.Sprintf(`{"orderId":%s,"prices":{"withTax":{"value":%d,"currencyCode":"EUR"}},"details":[{"description":"Server and hardware options","cartItemID":21}],"contracts":[]}`, orderID, amount)), nil
	case "cart_checkout":
		f.purchases++
		if args["waiveRetractationPeriod"] != false || args["autoPayWithPreferredPaymentMethod"] != true {
			return nil, errors.New("wrong payment flags")
		}
		if f.checkoutLost {
			return nil, errors.New("lost payment acknowledgement")
		}
		return providerJSON(`{"orderId":123}`), nil
	case "order_status":
		return providerJSON(`"delivered"`), nil
	case "order_details_list":
		return providerJSON(`[42,43]`), nil
	case "order_detail_extension":
		plan := "plan-1"
		if f.wrongPlan || fmt.Sprint(args["orderDetailId"]) == "43" {
			plan = "unrelated"
		}
		return providerJSON(fmt.Sprintf(`{"order":{"type":"plan","action":"installation","plan":{"code":%q}}}`, plan)), nil
	case "order_detail_get":
		return providerJSON(`{"domain":"owned.ovh.net"}`), nil
	case "vps_get", "dedicated_get":
		if f.missing {
			return missingProviderResource(), nil
		}
		if tool == "vps_get" {
			return providerJSON(`{"name":"owned.ovh.net","state":"running","model":{"vcore":2,"memory":4096}}`), nil
		}
		return providerJSON(`{"name":"owned.ovh.net","ip":"203.0.113.11","powerState":"on","state":"ok","serverId":99}`), nil
	case "vps_ips", "dedicated_ips":
		return providerJSON(`["203.0.113.11","2001:db8::11"]`), nil
	case "vps_images_list":
		return providerJSON(`["image-24"]`), nil
	case "vps_image_get":
		return providerJSON(`{"id":"image-24","name":"Ubuntu 24.04"}`), nil
	case "dedicated_compatible_images":
		return providerJSON(`{"ovh":["ubuntu2404_64"]}`), nil
	case "vps_rebuild", "dedicated_reinstall":
		f.installs++
		if args["serviceName"] != "owned.ovh.net" {
			return nil, errors.New("reinstall identity mismatch")
		}
		if f.installLost {
			return nil, errors.New("lost installation acknowledgement")
		}
		return providerJSON(`{"id":71,"taskId":71}`), nil
	case "vps_task_get":
		return providerJSON(`{"state":"done"}`), nil
	case "dedicated_task_get":
		return providerJSON(`{"status":"done"}`), nil
	}
	if tool == prefix+"_terminate" {
		f.terminated++
		return providerJSON(`"email sent"`), nil
	}
	if tool == prefix+"_termination_confirm" {
		f.confirmed++
		return providerJSON(`"confirmed"`), nil
	}
	return nil, errors.New("unexpected OVH tool " + tool)
}
func newOVHTest(t *testing.T, product string) (*sdk.AppCtx, *ovhFixture, CreateInstanceInput) {
	f := &ovhFixture{product: product}
	ctx, _ := cloudingUpcloudTestCtx(t, "ovhcloud", f.call)
	stopInstanceWorkers(ctx) // Exercise the durable recovery steps deterministically without live SSH.
	image := "ovh-dedicated/ubuntu2404_64"
	if product == "vps" {
		image = "ovh-vps/Ubuntu 24.04"
	}
	in := CreateInstanceInput{Name: "OVH test", Provider: "ovhcloud", ProviderConnectionID: 7, Size: ovhProductSize(product, "plan-1"), Region: ovhRegion(product, "MAD"), Image: image, OVH: &OVHOrderOptions{RequestKey: "test-request-123"}}
	return ctx, f, in
}
func TestOVHQuoteDeliveryAndIdempotency(t *testing.T) {
	for _, product := range ovhOrderProducts {
		t.Run(product, func(t *testing.T) {
			ctx, f, in := newOVHTest(t, product)
			quote, err := ovhQuote(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := ovhQuote(ctx, in)
			if err != nil || duplicate.ID != quote.ID {
				t.Fatalf("quote retry: %v", err)
			}
			encoded, _ := json.Marshal(ovhOrderPublic(quote))
			if strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), quote.State.PublicKey) {
				t.Fatal("quote exposed key material")
			}
			in.OVH.QuoteID = quote.ID
			inst, err := provisionInstance(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := provisionInstance(ctx, in)
			if err != nil || retry.ID != inst.ID || f.purchases != 1 {
				t.Fatalf("duplicate purchase: %+v %v count=%d", retry, err, f.purchases)
			}
			r, _ := ovhOrderForInstance(ctx, inst)
			probes := 0
			probe := func(host *Instance, _ time.Duration) error {
				probes++
				if host.ProviderID != "owned.ovh.net" || host.SSHPrivateKey == "" {
					return errors.New("missing SSH identity")
				}
				return nil
			}
			if err = ovhContractStep(ctx, inst, r, probe); err != nil {
				t.Fatal(err)
			}
			fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
			if fresh.Status != "ready" || f.installs != 1 || probes != 1 {
				t.Fatalf("not ready: %+v installs=%d", fresh, f.installs)
			}
			if err = ovhContractStep(ctx, fresh, r, probe); !errors.Is(err, errOVHPending) || f.installs != 1 {
				t.Fatal("ready recovery reinstalled host")
			}
			compare, err := compareInstanceProvider(ctx, fresh)
			if err != nil || len(compare.Differences) > 0 || !compare.Complete {
				t.Fatalf("comparison: %+v %v", compare, err)
			}
		})
	}
}
func TestOVHRejectsChangedQuoteAndUnownedConfiguration(t *testing.T) {
	for _, which := range []string{"account", "image", "region", "addon", "price", "request-key"} {
		t.Run(which, func(t *testing.T) {
			ctx, f, in := newOVHTest(t, "vps")
			quote, err := ovhQuote(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			in.OVH.QuoteID = quote.ID
			switch which {
			case "account":
				in.ProviderConnectionID = 88
			case "image":
				in.Image = "ovh-vps/Windows"
			case "region":
				in.Region = "ovh/vps/UNKNOWN"
			case "addon":
				in.OVH.Addons = map[string][]string{"memory": {"unknown"}}
			case "price":
				f.priceChanged = true
			case "request-key":
				in.Name = "different"
				if _, err = ovhQuote(ctx, in); err == nil {
					t.Fatal("conflicting retry accepted")
				}
				return
			}
			if _, err = provisionInstance(ctx, in); err == nil {
				t.Fatal("invalid purchase accepted")
			}
			if f.purchases != 0 {
				t.Fatal("charged invalid purchase")
			}
		})
	}
}
func TestOVHUnknownCheckoutNeverRepurchases(t *testing.T) {
	ctx, f, in := newOVHTest(t, "vps")
	q, e := ovhQuote(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	f.checkoutLost = true
	in.OVH.QuoteID = q.ID
	inst, e := provisionInstance(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	r, e := ovhOrderForInstance(ctx, inst)
	if e != nil {
		t.Fatal(e)
	}
	if r.State.Phase != "checkout_unknown" || !inst.CreatePending {
		t.Fatal("lost outcome not retained")
	}
	for n := 0; n < 3; n++ {
		_, e = provisionInstance(ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return nil }); !errors.Is(e, errOVHPending) {
			t.Fatal(e)
		}
	}
	if f.purchases != 1 || f.installs != 0 {
		t.Fatalf("unsafe recovery %+v", f)
	}
	f.recoveredOrder = true
	if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if f.purchases != 1 || f.installs != 1 {
		t.Fatal("lost checkout recovery repeated a mutation")
	}

}
func TestOVHInterruptedInstallNeverReinstalls(t *testing.T) {
	ctx, f, in := newOVHTest(t, "eco")
	q, e := ovhQuote(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	in.OVH.QuoteID = q.ID
	inst, e := provisionInstance(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	f.installLost = true
	r, _ := ovhOrderForInstance(ctx, inst)
	if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return errors.New("not ready") }); !errors.Is(e, errOVHPending) {
		t.Fatal(e)
	}
	recovered, _ := ovhOrderForInstance(ctx, inst)
	if !recovered.State.InstallStarted || recovered.State.TaskID != 0 {
		t.Fatal("install intent not durable")
	}
	if e = ovhContractStep(ctx, inst, recovered, func(*Instance, time.Duration) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if f.installs != 1 {
		t.Fatal("interrupted install repeated")
	}
}
func TestOVHCancellationRetainsServiceUntilConfirmedGone(t *testing.T) {
	for _, product := range []string{"vps", "baremetalServers", "eco"} {
		t.Run(product, func(t *testing.T) {
			ctx, f, in := newOVHTest(t, product)
			q, e := ovhQuote(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			in.OVH.QuoteID = q.ID
			inst, e := provisionInstance(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			r, _ := ovhOrderForInstance(ctx, inst)
			if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return nil }); e != nil {
				t.Fatal(e)
			}
			inst, _ = dbGetInstance(ctx.AppDB(), inst.ID)
			if e = destroyManagedInstance(ctx, inst); e == nil {
				t.Fatal("cancellation falsely reported deleted")
			}
			inst, _ = dbGetInstance(ctx.AppDB(), inst.ID)
			if inst.Status != "destroying" {
				t.Fatal("inventory removed early")
			}
			app := &App{}
			_, e = app.toolOrderStatus(ctx, map[string]any{"id": inst.ID, "action": "confirm_termination", "token": "test-secret-token"})
			if e != nil {
				t.Fatal(e)
			}
			r, _ = ovhOrderForInstance(ctx, inst)
			state, _ := json.Marshal(r.State)
			if strings.Contains(string(state), "test-secret-token") {
				t.Fatal("confirmation token persisted")
			}
			if f.terminated != 1 || f.confirmed != 1 {
				t.Fatalf("termination calls: %+v", f)
			}
			if _, e = dbGetInstance(ctx.AppDB(), inst.ID); e != nil {
				t.Fatal("server removed before provider absence")
			}
			f.missing = true
			if e = resumeInstanceDestroy(ctx, inst); e != nil {
				t.Fatal(e)
			}
			if _, e = dbGetInstance(ctx.AppDB(), inst.ID); !errors.Is(e, ErrInstanceNotFound) {
				t.Fatal("verified deletion not finalized")
			}
		})
	}
}
func TestOVHDeliveryOwnershipAndCancelledOrderNeverInstalls(t *testing.T) {
	ctx, f, in := newOVHTest(t, "vps")
	q, e := ovhQuote(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	in.OVH.QuoteID = q.ID
	inst, e := provisionInstance(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	r, _ := ovhOrderForInstance(ctx, inst)
	f.wrongPlan = true
	if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return nil }); !errors.Is(e, errOVHPending) || f.installs != 0 {
		t.Fatal("installed unrelated order")
	}
	f.wrongPlan = false
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"status": "destroying"})
	if e = ovhContractStep(ctx, inst, r, func(*Instance, time.Duration) error { return nil }); !errors.Is(e, errOVHPending) || f.installs != 0 {
		t.Fatal("installed cancelled host")
	}
}
func TestOVHCatalogsAreFamilyScoped(t *testing.T) {
	ctx, _, _ := newOVHTest(t, "vps")
	types, e := ovhServerTypes(ctx)
	if e != nil || len(types) != 3 {
		t.Fatalf("types=%+v %v", types, e)
	}
	if types[0].OVHOffer.Prices[0].Amount != 12 || *types[0].PriceKnown || types[1].ResourceClass != "bare_metal" {
		t.Fatal("fabricated specs/price")
	}
	images, e := ovhImages(ctx)
	if e != nil || len(images) != 2 {
		t.Fatalf("image rows=%+v %v", images, e)
	}
	for _, im := range images {
		if im.Name == "ovh-dedicated/Ubuntu 24.04" || strings.Contains(im.Name, "1804") {
			t.Fatal("nested or expired template accepted")
		}
	}
}

func TestOVHRegionalProductAvailabilityDoesNotHideOtherFamilies(t *testing.T) {
	f := &ovhFixture{product: "baremetalServers"}
	ctx, _ := cloudingUpcloudTestCtx(t, "ovhcloud", func(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "vps_catalog" || tool == "eco_catalog" {
			return missingProviderResource(), nil
		}
		return f.call(id, tool, args)
	})
	types, e := ovhServerTypes(ctx)
	if e != nil || len(types) != 1 || types[0].ProviderFamily != "baremetalServers" {
		t.Fatalf("regional catalog: %+v %v", types, e)
	}
	images, e := ovhImages(ctx)
	if e != nil || len(images) != 1 || images[0].ResourceClass != "bare_metal" {
		t.Fatalf("regional images: %+v %v", images, e)
	}
}
func TestOVHProjectOnlyCredentialsKeepPublicCloudUsable(t *testing.T) {
	ctx, p := cloudingUpcloudTestCtx(t, "ovhcloud", func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "get_account", "dedicated_images_info":
			return &sdk.ExecuteResult{Status: 403}, nil
		case "list_flavors":
			return providerJSON(`[{"id":"flavor-id","name":"Cloud VM","vcpus":2,"ram":4096,"disk":40,"region":"MAD"}]`), nil
		case "list_regions":
			return providerJSON(`["MAD"]`), nil
		case "list_images":
			return providerJSON(`[{"id":"image-id","name":"Ubuntu 24.04","region":"MAD","type":"linux","status":"active"}]`), nil
		}
		return nil, errors.New("unexpected " + tool)
	})
	p.public = map[string]string{"service_name": "legacy-project"}
	types, e := ovhServerTypes(ctx)
	if e != nil || len(types) != 1 || types[0].ProviderFamily != "public_cloud" {
		t.Fatalf("legacy types: %+v %v", types, e)
	}
	locations, e := ovhLocations(ctx)
	if e != nil || len(locations) != 1 {
		t.Fatalf("legacy locations: %+v %v", locations, e)
	}
	images, e := ovhImages(ctx)
	if e != nil || len(images) != 1 || images[0].Name != "image-id" {
		t.Fatalf("legacy images: %+v %v", images, e)
	}
}

func TestOVHProductMetadataAndMultipleOptionValidation(t *testing.T) {
	var cat ovhCatalog
	err := json.Unmarshal([]byte(`{"plans":[{"planCode":"cpu-plan","product":"cpu-product","configurations":[]}],"products":[{"name":"cpu-product","configurations":[{"name":"region","isMandatory":true,"values":["europe"]}],"blobs":{"technical":{"server":{"cpu":{"cores":12,"number":2}},"os":{"features":[{"name":"ubuntu2404_64"}]}}}}]}`), &cat)
	if err != nil {
		t.Fatal(err)
	}
	ovhEnrichPlan(&cat, cat.Plans[0].Product, &cat.Plans[0])
	var blobs map[string]any
	_ = json.Unmarshal(cat.Plans[0].Blobs, &blobs)
	if nestedInt(blobs, "technical", "server", "cpu", "cores") != 12 || len(cat.Plans[0].Configurations) != 1 {
		t.Fatal("product metadata not merged")
	}
	ctx, f, in := newOVHTest(t, "vps")
	in.OVH.Addons = map[string][]string{"memory": {"ram-1", "ram-2"}}
	if _, err = ovhQuote(ctx, in); err == nil {
		t.Fatal("exclusive hardware selection accepted multiple values")
	}
	for _, name := range f.calls {
		if strings.HasPrefix(name, "cart_") {
			t.Fatal("mutated cart before validating hardware")
		}
	}
	if ovhOrderArg(map[string]any{"ovh": map[string]any{"request_key": "fixture-key", "addons": map[string]any{"memory": "wrong-type"}}}) != nil {
		t.Fatal("malformed option input silently accepted")
	}
}
