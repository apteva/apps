package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type OVHOrderOptions struct {
	QuoteID             string                       `json:"quote_id,omitempty"`
	RequestKey          string                       `json:"request_key,omitempty"`
	Duration            string                       `json:"duration,omitempty"`
	PricingMode         string                       `json:"pricing_mode,omitempty"`
	Configurations      map[string]string            `json:"configurations,omitempty"`
	Addons              map[string][]string          `json:"addons,omitempty"`
	AddonConfigurations map[string]map[string]string `json:"addon_configurations,omitempty"`
}
type ovhOrderState struct {
	Phase                string          `json:"phase"`
	CartID               string          `json:"cart_id"`
	ItemID               int64           `json:"item_id"`
	DeliveryStatus       string          `json:"delivery_status,omitempty"`
	OrderID              int64           `json:"order_id,omitempty"`
	MainDetailID         int64           `json:"main_detail_id,omitempty"`
	ServiceName          string          `json:"service_name,omitempty"`
	TaskID               int64           `json:"task_id,omitempty"`
	InstallStarted       bool            `json:"install_started,omitempty"`
	TerminateStarted     bool            `json:"terminate_started,omitempty"`
	TerminationConfirmed bool            `json:"termination_confirmed,omitempty"`
	PrivateKey           string          `json:"private_key"`
	PublicKey            string          `json:"public_key"`
	Quote                json.RawMessage `json:"quote,omitempty"`
	QuoteDigest          string          `json:"quote_digest,omitempty"`
	ExpiresAt            string          `json:"expires_at"`
	InputDigest          string          `json:"input_digest"`
	Error                string          `json:"error,omitempty"`
}
type ovhOrderRecord struct {
	ID           string
	ConnectionID int64
	InstanceID   sql.NullInt64
	Input        CreateInstanceInput
	State        ovhOrderState
}

func ovhOrderArg(args map[string]any) *OVHOrderOptions {
	raw, ok := args["ovh"].(map[string]any)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var o OVHOrderOptions
	if json.Unmarshal(b, &o) != nil {
		return nil
	}
	return &o
}
func ovhInputDigest(in CreateInstanceInput) string {
	in.SSHPrivateKey = ""
	in.SSHPublicKey = ""
	in.Status = ""
	in.ProviderMetadataJSON = ""
	in.ProviderID = ""
	in.Platform = ""
	in.ResourceClass = ""
	in.SSHUser = ""
	in.MonthlyCostCents = 0
	if in.OVH != nil {
		copy := *in.OVH
		copy.QuoteID = ""
		copy.RequestKey = ""
		in.OVH = &copy
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func ovhJSONDigest(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func ovhReadOrder(ctx *sdk.AppCtx, id string) (*ovhOrderRecord, error) {
	r := &ovhOrderRecord{ID: id}
	var input, state string
	e := ctx.AppDB().QueryRow(`SELECT connection_id,instance_id,input_json,state_json FROM provider_orders WHERE id=? AND provider='ovhcloud'`, id).Scan(&r.ConnectionID, &r.InstanceID, &input, &state)
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal([]byte(input), &r.Input); e != nil {
		return nil, e
	}
	if e = json.Unmarshal([]byte(state), &r.State); e != nil {
		return nil, e
	}
	if _, e = storageBinding(ctx, "ovhcloud", r.ConnectionID); e != nil {
		return nil, e
	}
	return r, nil
}
func ovhSaveOrder(ctx *sdk.AppCtx, r *ovhOrderRecord) error {
	b, e := json.Marshal(r.State)
	if e != nil {
		return e
	}
	_, e = ctx.AppDB().Exec(`UPDATE provider_orders SET state_json=?,instance_id=?,updated_at=? WHERE id=? AND connection_id=?`, string(b), r.InstanceID, nowUTC(), r.ID, r.ConnectionID)
	return e
}
func ovhOrderPublic(r *ovhOrderRecord) map[string]any {
	return map[string]any{"quote_id": r.ID, "instance_id": r.InstanceID.Int64, "status": r.State.Phase, "order_id": r.State.OrderID, "delivery_status": r.State.DeliveryStatus, "service_name": r.State.ServiceName, "expires_at": r.State.ExpiresAt, "quote": r.State.Quote, "error": r.State.Error}
}
func ovhQuote(ctx *sdk.AppCtx, in CreateInstanceInput) (*ovhOrderRecord, error) {
	in.Provider = normalizeProvider(in.Provider)
	if in.Provider != "ovhcloud" {
		return nil, errors.New("order quotes are currently implemented for OVH contract products")
	}
	product, plan := ovhSizeParts(in.Size)
	if product == "" {
		return nil, errors.New("select a VPS, dedicated or Eco offer")
	}
	if strings.TrimSpace(in.Name) == "" || in.Image == "" {
		return nil, errors.New("name and image are required")
	}
	if in.Storage.Boot != nil {
		return nil, errors.New("OVH contract boot hardware is selected using offer options, not storage.boot")
	}
	if in.OVH == nil || len(in.OVH.RequestKey) < 8 || len(in.OVH.RequestKey) > 128 {
		return nil, errors.New("ovh.request_key (8-128 characters) is required for idempotent quoting")
	}
	bound, e := storageBinding(ctx, "ovhcloud", in.ProviderConnectionID)
	if e != nil {
		return nil, e
	}
	in.ProviderConnectionID = bound.ConnectionID
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", bound.ConnectionID, in.OVH.RequestKey)))
	unlock, err := lockResource(ctx.AppDB(), "ovh-quote", int64(sum[0])<<48|int64(sum[1])<<40|int64(sum[2])<<32|int64(sum[3])<<24|int64(sum[4])<<16|int64(sum[5])<<8|int64(sum[6]))
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, _ = ctx.AppDB().Exec(`DELETE FROM provider_orders WHERE instance_id IS NULL AND created_at < ?`, time.Now().UTC().Add(-7*24*time.Hour).Format(time.RFC3339))
	var pending int
	if err = ctx.AppDB().QueryRow(`SELECT count(*) FROM provider_orders WHERE connection_id=? AND instance_id IS NULL`, bound.ConnectionID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending >= 1024 {
		return nil, errors.New("OVH quote quota reached; abandon unused carts in OVH and wait for expired quote cleanup")
	}

	scoped, release := scopedCatalog(ctx, catalogOptions{ConnectionID: bound.ConnectionID, Zone: in.Region})
	defer release()
	ctx = scoped
	digest := ovhInputDigest(in)
	var oldID string
	e = ctx.AppDB().QueryRow(`SELECT id FROM provider_orders WHERE provider='ovhcloud' AND connection_id=? AND request_key=?`, bound.ConnectionID, in.OVH.RequestKey).Scan(&oldID)
	if e == nil {
		old, e := ovhReadOrder(ctx, oldID)
		if e != nil {
			return nil, e
		}
		if old.State.InputDigest != digest {
			return nil, errors.New("OVH quote request key conflicts with a different configuration")
		}
		return old, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	catalog, e := ovhLoadCatalog(ctx, product)
	if e != nil {
		return nil, e
	}
	var offer *ovhPlan
	for i := range catalog.Plans {
		if catalog.Plans[i].PlanCode == plan {
			offer = &catalog.Plans[i]
			break
		}
	}
	if offer == nil {
		return nil, errors.New("OVH offer is absent from the account's current catalog")
	}
	if product != "vps" {
		var blob map[string]any
		_ = json.Unmarshal(offer.Blobs, &blob)
		templates := collectMaps(nestedValue(blob, "technical", "os", "features"))
		if len(templates) > 0 {
			eligible := false
			for _, row := range templates {
				if "ovh-dedicated/"+mapString(row, "name") == in.Image {
					eligible = true
				}
			}
			if !eligible {
				return nil, errors.New("dedicated OS template is incompatible with this offer")
			}
		}
	}
	zone, e := ovhRawRegion(product, in.Region)
	if e != nil {
		return nil, e
	}
	cfg := map[string]string{}
	for k, v := range in.OVH.Configurations {
		cfg[k] = v
	}
	if product == "vps" {
		cfg["vps_datacenter"] = zone
		cfg["vps_os"] = strings.TrimPrefix(in.Image, "ovh-vps/")
		if !strings.HasPrefix(in.Image, "ovh-vps/") || !ovhLinuxName(cfg["vps_os"]) {
			return nil, errors.New("choose an eligible OVH VPS Linux image")
		}
	} else {
		cfg["dedicated_datacenter"] = zone
		cfg["dedicated_os"] = "none_64.en"
		if !strings.HasPrefix(in.Image, "ovh-dedicated/") {
			return nil, errors.New("choose an eligible dedicated Linux image")
		}
	}
	if product != "vps" {
		images, err := ovhContractImages(ctx)
		if err != nil {
			return nil, err
		}
		eligible := false
		for _, image := range images {
			if image.Name == in.Image && image.ResourceClass == "bare_metal" {
				eligible = true
			}
		}
		if !eligible {
			return nil, errors.New("dedicated Linux image is absent from the current OVH installation catalog")
		}
	}
	for _, c := range offer.Configurations {
		value := cfg[c.Name]
		if value == "" && c.Mandatory && len(c.Values) == 1 {
			value = c.Values[0]
			cfg[c.Name] = value
		}
		if c.Mandatory && value == "" {
			return nil, fmt.Errorf("OVH configuration %s is required", c.Name)
		}
		if value != "" && !c.Custom && len(c.Values) > 0 && !containsString(c.Values, value) {
			return nil, fmt.Errorf("invalid OVH configuration %s=%s", c.Name, value)
		}
	}
	for k := range cfg {
		known := false
		for _, c := range offer.Configurations {
			if c.Name == k {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown OVH configuration %s", k)
		}
	}
	options := map[string][]string{}
	for k, v := range in.OVH.Addons {
		options[k] = v
	}
	for _, family := range offer.Addons {
		if len(options[family.Name]) == 0 && family.Mandatory {
			if family.Default != "" {
				options[family.Name] = []string{family.Default}
			}
			if len(options[family.Name]) == 0 && len(family.Addons) == 1 {
				options[family.Name] = []string{family.Addons[0]}
			}
		}
		value := options[family.Name]
		if family.Mandatory && len(value) == 0 {
			return nil, fmt.Errorf("select OVH %s option", family.Name)
		}
		if family.Exclusive && len(value) > 1 {
			return nil, fmt.Errorf("OVH %s requires a single option", family.Name)
		}
		seen := map[string]bool{}
		for _, code := range value {
			if !containsString(family.Addons, code) || seen[code] {
				return nil, fmt.Errorf("invalid or repeated OVH %s option", family.Name)
			}
			seen[code] = true
		}
	}
	for k := range options {
		known := false
		for _, f := range offer.Addons {
			if f.Name == k {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown OVH option family %s", k)
		}
	}
	mode, duration := in.OVH.PricingMode, in.OVH.Duration
	if mode == "" {
		mode = "default"
	}
	if duration == "" {
		duration = "P1M"
	}
	sub, e := ovhSubsidiary(ctx)
	if e != nil {
		return nil, e
	}
	private, public, e := generateSSHKeypair()
	if e != nil {
		return nil, e
	}
	r := &ovhOrderRecord{ID: newRequestID(), ConnectionID: bound.ConnectionID, Input: in, State: ovhOrderState{Phase: "preparing", PrivateKey: private, PublicKey: public, ExpiresAt: time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339), InputDigest: digest}}
	b, _ := json.Marshal(in)
	state, _ := json.Marshal(r.State)
	if _, e = ctx.AppDB().Exec(`INSERT INTO provider_orders(id,provider,connection_id,request_key,input_json,state_json,created_at,updated_at)VALUES(?,'ovhcloud',?,?,?,?,?,?)`, r.ID, r.ConnectionID, in.OVH.RequestKey, string(b), string(state), nowUTC(), nowUTC()); e != nil {
		return nil, e
	}
	fail := func(err error) (*ovhOrderRecord, error) {
		r.State.Phase = "quote_error"
		r.State.Error = err.Error()
		_ = ovhSaveOrder(ctx, r)
		return r, err
	}
	cart, e := executeProviderTool(ctx, "ovhcloud", "cart_create", map[string]any{"ovhSubsidiary": sub, "description": "apteva-instances-" + r.ID})
	if e != nil {
		return fail(e)
	}
	r.State.CartID = jsonStringAt(cart, "cartId")
	if r.State.CartID == "" {
		return fail(errors.New("OVH did not return cart identity"))
	}
	if e = ovhSaveOrder(ctx, r); e != nil {
		return nil, e
	}
	if _, e = executeProviderTool(ctx, "ovhcloud", "cart_assign", map[string]any{"cartId": r.State.CartID}); e != nil {
		return fail(e)
	}
	prices, e := executeProviderTool(ctx, "ovhcloud", "cart_"+ovhProductTool(product)+"_plans", map[string]any{"cartId": r.State.CartID})
	if e != nil {
		return fail(e)
	}
	var available []struct {
		PlanCode string `json:"planCode"`
		Prices   []struct {
			Duration string `json:"duration"`
			Mode     string `json:"pricingMode"`
		} `json:"prices"`
	}
	if e = json.Unmarshal(prices, &available); e != nil {
		return fail(e)
	}
	valid := false
	for _, p := range available {
		if p.PlanCode == plan {
			for _, price := range p.Prices {
				if price.Duration == duration && price.Mode == mode {
					valid = true
				}
			}
		}
	}
	if !valid {
		return fail(errors.New("OVH plan/duration/pricing mode is not currently orderable"))
	}
	item, e := executeProviderTool(ctx, "ovhcloud", "cart_"+ovhProductTool(product)+"_add", map[string]any{"cartId": r.State.CartID, "planCode": plan, "duration": duration, "pricingMode": mode, "quantity": 1})
	if e != nil {
		return fail(e)
	}
	r.State.ItemID = anyToInt64JSON(item, "itemId")
	if r.State.ItemID <= 0 {
		return fail(errors.New("OVH cart item identity missing"))
	}
	if e = ovhSaveOrder(ctx, r); e != nil {
		return nil, e
	}
	keys := []string{}
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if cfg[k] == "" {
			continue
		}
		if _, e = executeProviderTool(ctx, "ovhcloud", "cart_configuration_add", map[string]any{"cartId": r.State.CartID, "itemId": r.State.ItemID, "label": k, "value": cfg[k]}); e != nil {
			return fail(e)
		}
	}
	for _, family := range offer.Addons {
		for _, code := range options[family.Name] {
			addon, e := executeProviderTool(ctx, "ovhcloud", "cart_"+ovhProductTool(product)+"_option_add", map[string]any{"cartId": r.State.CartID, "itemId": r.State.ItemID, "planCode": code, "quantity": 1, "duration": duration, "pricingMode": mode})
			if e != nil {
				return fail(e)
			}
			addonID := anyToInt64JSON(addon, "itemId")
			if addonID <= 0 {
				return fail(errors.New("OVH addon cart item identity missing"))
			}
			values := map[string]string{}
			for k, v := range in.OVH.AddonConfigurations[code] {
				values[k] = v
			}
			var addonPlan *ovhPlan
			for i := range catalog.Addons {
				if catalog.Addons[i].PlanCode == code {
					addonPlan = &catalog.Addons[i]
					break
				}
			}
			if addonPlan != nil {
				for _, c := range addonPlan.Configurations {
					if values[c.Name] == "" && cfg[c.Name] != "" && (c.Custom || containsString(c.Values, cfg[c.Name])) {
						values[c.Name] = cfg[c.Name]
					}
					if values[c.Name] == "" && c.Mandatory && len(c.Values) == 1 {
						values[c.Name] = c.Values[0]
					}
					if c.Mandatory && values[c.Name] == "" {
						return fail(fmt.Errorf("OVH option %s requires configuration %s", code, c.Name))
					}
					if v := values[c.Name]; v != "" && !c.Custom && len(c.Values) > 0 && !containsString(c.Values, v) {
						return fail(fmt.Errorf("invalid OVH option %s configuration %s", code, c.Name))
					}
				}
				for k := range values {
					known := false
					for _, c := range addonPlan.Configurations {
						if c.Name == k {
							known = true
						}
					}
					if !known {
						return fail(fmt.Errorf("unknown OVH option configuration %s", k))
					}
				}
			} else if len(values) > 0 {
				return fail(errors.New("OVH option configuration is absent from current catalog"))
			}
			for label, value := range values {
				if value != "" {
					if _, e = executeProviderTool(ctx, "ovhcloud", "cart_configuration_add", map[string]any{"cartId": r.State.CartID, "itemId": addonID, "label": label, "value": value}); e != nil {
						return fail(e)
					}
				}
			}
			required, e := executeProviderTool(ctx, "ovhcloud", "cart_required_configuration", map[string]any{"cartId": r.State.CartID, "itemId": addonID})
			if e != nil {
				return fail(e)
			}
			var reqs []struct {
				Label    string `json:"label"`
				Required bool   `json:"required"`
			}
			if e = json.Unmarshal(required, &reqs); e != nil {
				return fail(e)
			}
			for _, req := range reqs {
				if req.Required && values[req.Label] == "" {
					return fail(fmt.Errorf("OVH option %s requires configuration %s", code, req.Label))
				}
			}
		}
	}
	required, e := executeProviderTool(ctx, "ovhcloud", "cart_required_configuration", map[string]any{"cartId": r.State.CartID, "itemId": r.State.ItemID})
	if e != nil {
		return fail(e)
	}
	var requirements []struct {
		Label    string `json:"label"`
		Required bool   `json:"required"`
	}
	if e = json.Unmarshal(required, &requirements); e != nil {
		return fail(e)
	}
	for _, req := range requirements {
		if req.Required && cfg[req.Label] == "" {
			return fail(fmt.Errorf("OVH requires configuration %s", req.Label))
		}
	}
	quote, e := executeProviderTool(ctx, "ovhcloud", "cart_quote", map[string]any{"cartId": r.State.CartID})
	if e != nil {
		return fail(e)
	}
	var reviewed map[string]json.RawMessage
	if e = json.Unmarshal(quote, &reviewed); e != nil {
		return fail(e)
	}
	r.State.Quote = mustMarshal(map[string]json.RawMessage{"prices": reviewed["prices"], "details": reviewed["details"], "contracts": reviewed["contracts"]})
	r.State.QuoteDigest = ovhQuoteDigest(quote)
	if r.State.QuoteDigest == "" {
		return fail(errors.New("OVH quote has no valid prices/details/contracts"))
	}
	r.State.Phase = "quoted"
	if e = ovhSaveOrder(ctx, r); e != nil {
		return nil, e
	}
	return r, nil
}
func anyToInt64JSON(raw json.RawMessage, key string) int64 {
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	return int64(numberValue(root[key]))
}
func ovhQuoteDigest(raw json.RawMessage) string {
	var q map[string]json.RawMessage
	if json.Unmarshal(raw, &q) != nil || len(q["prices"]) == 0 || len(q["details"]) == 0 {
		return ""
	}
	return ovhJSONDigest(mustMarshal(map[string]json.RawMessage{"prices": q["prices"], "details": q["details"], "contracts": q["contracts"]}))
}
func mustMarshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func ovhOrderForInstance(ctx *sdk.AppCtx, inst *Instance) (*ovhOrderRecord, error) {
	var meta struct {
		Order string `json:"ovh_order_record"`
	}
	if json.Unmarshal([]byte(inst.ProviderMetadataJSON), &meta) != nil || meta.Order == "" {
		return nil, errors.New("OVH owned order record is missing")
	}
	r, e := ovhReadOrder(ctx, meta.Order)
	if e != nil {
		return nil, e
	}
	if !r.InstanceID.Valid || r.InstanceID.Int64 != inst.ID || r.ConnectionID != inst.ProviderConnectionID {
		return nil, errors.New("OVH order ownership mismatch")
	}
	return r, nil
}
func ovhContractProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	if in.OVH == nil || in.OVH.QuoteID == "" {
		return nil, errors.New("review an instance_quote result, then pass ovh.quote_id to purchase this contract offer")
	}
	unlock, e := ovhLockOrder(ctx, in.OVH.QuoteID)
	if e != nil {
		return nil, e
	}
	defer unlock()
	r, e := ovhReadOrder(ctx, in.OVH.QuoteID)
	if e != nil {
		return nil, e
	}
	if r.ConnectionID != in.ProviderConnectionID || r.State.InputDigest != ovhInputDigest(in) {
		return nil, errors.New("OVH quote does not match this account and exact requested configuration")
	}
	if r.InstanceID.Valid {
		return dbGetInstance(ctx.AppDB(), r.InstanceID.Int64)
	}
	if r.State.Phase != "quoted" {
		return nil, fmt.Errorf("OVH quote is %s; no purchase was attempted", r.State.Phase)
	}
	expiry, e := time.Parse(time.RFC3339, r.State.ExpiresAt)
	if e != nil || !time.Now().Before(expiry) {
		return nil, errors.New("OVH quote expired; request and review a fresh quote")
	}
	current, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "cart_quote", map[string]any{"cartId": r.State.CartID})
	if e != nil {
		return nil, e
	}
	if ovhQuoteDigest(current) != r.State.QuoteDigest {
		return nil, errors.New("OVH order price, details or contracts changed; request and review a fresh quote")
	}
	product, _ := ovhSizeParts(in.Size)
	in.Status = "provisioning"
	in.Platform = "linux"
	in.ResourceClass = "virtual"
	if product != "vps" {
		in.ResourceClass = "bare_metal"
	}
	in.SSHUser = "root"
	in.SSHPrivateKey = r.State.PrivateKey
	in.SSHPublicKey = r.State.PublicKey
	in.ProviderMetadataJSON = string(mustMarshal(map[string]string{"ovh_order_record": r.ID}))
	// Recover a local claim if Instances stopped between row insertion and
	// saving the order pointer. Never infer ownership from a display name.
	var existingID int64
	claimErr := ctx.AppDB().QueryRow(`SELECT id FROM instances WHERE provider='ovhcloud' AND provider_connection_id=? AND json_extract(provider_metadata_json,'$.ovh_order_record')=?`, r.ConnectionID, r.ID).Scan(&existingID)
	var inst *Instance
	if claimErr == nil {
		inst, e = dbGetInstance(ctx.AppDB(), existingID)
	} else if errors.Is(claimErr, sql.ErrNoRows) {
		inst, e = dbCreateInstance(ctx.AppDB(), in)
	} else {
		return nil, claimErr
	}
	if e != nil {
		return nil, e
	}
	r.InstanceID = sql.NullInt64{Int64: inst.ID, Valid: true}
	r.State.Phase = "checkout_unknown"
	if e = ovhSaveOrder(ctx, r); e != nil {
		// No checkout has occurred. Remove the unclaimed local row on a failed durable claim.
		_, _ = ctx.AppDB().Exec(`DELETE FROM instances WHERE id=?`, inst.ID)
		return nil, e
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)
	response, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "cart_checkout", map[string]any{"cartId": r.State.CartID, "autoPayWithPreferredPaymentMethod": true, "waiveRetractationPeriod": false})
	if e != nil {
		r.State.Error = "Checkout outcome is unknown; inspect the recorded cart/order in OVH before recovery. Automatic checkout retries are disabled."
		_ = ovhSaveOrder(ctx, r)
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"lifecycle_stage": "OrderUnknown", "error_message": r.State.Error})
		kickOVHContract(ctx, inst.ID)
		return dbGetInstance(ctx.AppDB(), inst.ID)
	}
	r.State.OrderID = anyToInt64JSON(response, "orderId")
	if r.State.OrderID <= 0 {
		r.State.Error = "OVH checkout returned no order identity; outcome is unknown. Inventory retained; checkout will not be repeated."
		_ = ovhSaveOrder(ctx, r)
		_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"lifecycle_stage": "OrderUnknown", "error_message": r.State.Error})
		kickOVHContract(ctx, inst.ID)
		return dbGetInstance(ctx.AppDB(), inst.ID)
	}
	r.State.Phase = "delivery"
	if e = ovhSaveOrder(ctx, r); e != nil {
		return nil, e
	}
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"create_pending": false, "lifecycle_stage": "Delivery"})
	kickOVHContract(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}
func (a *App) toolInstanceQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	setup, e := parseSetup(args)
	if e != nil {
		return nil, e
	}
	in := CreateInstanceInput{Name: strArg(args, "name"), Provider: "ovhcloud", ProviderConnectionID: int64Arg(args, "provider_connection_id"), Size: strArg(args, "size"), Region: strArg(args, "region"), Image: strArg(args, "image"), TagsJSON: strArg(args, "tags_json"), Setup: setup, Storage: storageRequestArg(args), OVH: ovhOrderArg(args)}
	if value, present := args["retain_for_diagnosis"]; present {
		in.RetainOnFailureSet = true
		in.RetainOnFailure, _ = value.(bool)
	}
	r, e := ovhQuote(ctx, in)
	if e != nil {
		return nil, e
	}
	return ovhOrderPublic(r), nil
}
func (a *App) handleInstanceQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	args := map[string]any{}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&args); e != nil {
		httpErr(w, 400, "invalid quote request")
		return
	}
	v, e := a.toolInstanceQuote(appCtxForRequest(r), args)
	if e != nil {
		httpProviderErr(w, e)
		return
	}
	httpJSON(w, v)
}

func ovhOrderOptionsSchema() map[string]any {
	return schemaObject(map[string]any{
		"quote_id": map[string]any{"type": "string"}, "request_key": map[string]any{"type": "string"}, "duration": map[string]any{"type": "string"}, "pricing_mode": map[string]any{"type": "string"}, "configurations": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "addons": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "addon_configurations": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}},
	}, nil)
}
func ovhQuoteSchema() map[string]any {
	return schemaObject(map[string]any{"name": map[string]any{"type": "string"}, "provider_connection_id": map[string]any{"type": "integer"}, "size": map[string]any{"type": "string"}, "region": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "tags_json": map[string]any{"type": "string"}, "setup": setupSchema(), "retain_for_diagnosis": map[string]any{"type": "boolean"}, "ovh": ovhOrderOptionsSchema()}, []string{"name", "size", "region", "image", "ovh"})
}

func ovhLockOrder(ctx *sdk.AppCtx, id string) (func(), error) {
	hash := sha256.Sum256([]byte(id))
	key := int64(0)
	for _, b := range hash[:7] {
		key = key<<8 | int64(b)
	}
	return lockResource(ctx.AppDB(), "ovh-order", key)
}
