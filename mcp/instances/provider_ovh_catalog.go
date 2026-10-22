package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const ovhContractPrefix = "ovh/"

var ovhOrderProducts = []string{"vps", "baremetalServers", "eco"}

type OVHConfiguration struct {
	Name     string   `json:"name"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
	Custom   bool     `json:"custom,omitempty"`
}
type OVHAddonFamily struct {
	Name      string   `json:"name"`
	Required  bool     `json:"required"`
	Exclusive bool     `json:"exclusive"`
	Default   string   `json:"default,omitempty"`
	Options   []string `json:"options"`
}
type OVHOrderPrice struct {
	Mode       string  `json:"mode"`
	Duration   string  `json:"duration"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	Commitment int     `json:"commitment_months,omitempty"`
}
type OVHAddonOffer struct {
	Code           string             `json:"code"`
	Description    string             `json:"description"`
	Configurations []OVHConfiguration `json:"configurations,omitempty"`
	Prices         []OVHOrderPrice    `json:"prices,omitempty"`
}
type OVHOffer struct {
	Options        map[string]OVHAddonOffer `json:"options,omitempty"`
	Images         []string                 `json:"images,omitempty"`
	Product        string                   `json:"product"`
	PlanCode       string                   `json:"plan_code"`
	Configurations []OVHConfiguration       `json:"configurations"`
	AddonFamilies  []OVHAddonFamily         `json:"addon_families"`
	Prices         []OVHOrderPrice          `json:"prices"`
}
type ovhCatalog struct {
	Locale struct {
		Currency   string `json:"currencyCode"`
		Subsidiary string `json:"subsidiary"`
	} `json:"locale"`
	Plans    []ovhPlan `json:"plans"`
	Addons   []ovhPlan `json:"addons"`
	Products []struct {
		Name           string          `json:"name"`
		Blobs          json.RawMessage `json:"blobs"`
		Configurations []ovhConfig     `json:"configurations"`
	} `json:"products"`
}
type ovhPlan struct {
	PlanCode       string      `json:"planCode"`
	InvoiceName    string      `json:"invoiceName"`
	Product        string      `json:"product"`
	Family         string      `json:"family"`
	Configurations []ovhConfig `json:"configurations"`
	Addons         []struct {
		Name      string   `json:"name"`
		Mandatory bool     `json:"mandatory"`
		Exclusive bool     `json:"exclusive"`
		Default   string   `json:"default"`
		Addons    []string `json:"addons"`
	} `json:"addonFamilies"`
	Pricings []struct {
		Mode         string   `json:"mode"`
		Interval     int      `json:"interval"`
		IntervalUnit string   `json:"intervalUnit"`
		Price        float64  `json:"price"`
		Capacities   []string `json:"capacities"`
		Commitment   int      `json:"commitment"`
	} `json:"pricings"`
	Blobs json.RawMessage `json:"blobs"`
}
type ovhConfig struct {
	Name      string   `json:"name"`
	Mandatory bool     `json:"isMandatory"`
	Custom    bool     `json:"isCustom"`
	Values    []string `json:"values"`
}

func ovhProductSize(product, plan string) string { return ovhContractPrefix + product + "/" + plan }
func ovhSizeParts(size string) (string, string) {
	if !strings.HasPrefix(size, ovhContractPrefix) {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimPrefix(size, ovhContractPrefix), "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	for _, p := range ovhOrderProducts {
		if p == parts[0] && parts[1] != "" {
			return p, parts[1]
		}
	}
	return "", ""
}
func isOVHContract(inst *Instance) bool {
	if inst == nil || inst.Provider != "ovhcloud" {
		return false
	}
	p, _ := ovhSizeParts(inst.Size)
	return p != ""
}
func ovhProductTool(product string) string {
	if product == "baremetalServers" {
		return "dedicated"
	}
	return product
}
func ovhRegion(product, zone string) string { return ovhContractPrefix + product + "/" + zone }
func ovhRawRegion(product, region string) (string, error) {
	prefix := ovhRegion(product, "")
	if !strings.HasPrefix(region, prefix) || len(region) == len(prefix) {
		return "", fmt.Errorf("select an OVH %s location from the matching catalog", product)
	}
	return strings.TrimPrefix(region, prefix), nil
}
func ovhPublicConfig(ctx *sdk.AppCtx) (map[string]string, error) {
	bound, e := apiProviderBound(ctx, "ovhcloud")
	if e != nil {
		return nil, e
	}
	config, e := sdk.GetConnectionPublicConfig(ctx.PlatformAPI(), bound.ConnectionID)
	if e != nil {
		return nil, e
	}
	if config == nil {
		return nil, errors.New("OVH public connection configuration unavailable; refresh integration catalog")
	}
	return config.Fields, nil
}
func ovhSubsidiary(ctx *sdk.AppCtx) (string, error) {
	data, e := executeProviderTool(ctx, "ovhcloud", "get_account", map[string]any{})
	if e != nil {
		return "", e
	}
	sub := jsonStringAt(data, "ovhSubsidiary")
	if sub == "" {
		return "", errors.New("OVH account did not report its billing subsidiary")
	}
	return sub, nil
}
func ovhLoadCatalog(ctx *sdk.AppCtx, product string) (ovhCatalog, error) {
	sub, e := ovhSubsidiary(ctx)
	if e != nil {
		return ovhCatalog{}, e
	}
	data, e := executeProviderTool(ctx, "ovhcloud", ovhProductTool(product)+"_catalog", map[string]any{"ovhSubsidiary": sub})
	if e != nil {
		return ovhCatalog{}, e
	}
	var c ovhCatalog
	e = json.Unmarshal(data, &c)
	if e != nil {
		return c, e
	}
	for i := range c.Plans {
		ovhEnrichPlan(&c, c.Plans[i].Product, &c.Plans[i])
	}
	for i := range c.Addons {
		ovhEnrichPlan(&c, c.Addons[i].Product, &c.Addons[i])
	}
	return c, nil
}
func ovhOfferForPlan(product string, p ovhPlan, currency string) OVHOffer {
	o := OVHOffer{Product: product, PlanCode: p.PlanCode, Configurations: []OVHConfiguration{}, AddonFamilies: []OVHAddonFamily{}, Prices: []OVHOrderPrice{}}
	for _, c := range p.Configurations {
		o.Configurations = append(o.Configurations, OVHConfiguration{c.Name, c.Mandatory, c.Values, c.Custom})
	}
	for _, a := range p.Addons {
		o.AddonFamilies = append(o.AddonFamilies, OVHAddonFamily{a.Name, a.Mandatory, a.Exclusive, a.Default, a.Addons})
	}
	for _, r := range p.Pricings {
		if r.Interval <= 0 || !containsString(r.Capacities, "renew") {
			continue
		}
		duration := ""
		switch r.IntervalUnit {
		case "month":
			duration = fmt.Sprintf("P%dM", r.Interval)
		case "year":
			duration = fmt.Sprintf("P%dY", r.Interval)
		}
		if duration != "" {
			o.Prices = append(o.Prices, OVHOrderPrice{r.Mode, duration, r.Price / 1e8, currency, r.Commitment})
		}
	}
	return o
}
func ovhContractTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	out := []ServerType{}
	failures := []string{}
	for _, product := range ovhOrderProducts {
		cat, e := ovhLoadCatalog(ctx, product)
		if e != nil {
			failures = append(failures, product+": "+e.Error())
			continue
		}
		for _, p := range cat.Plans {
			if p.PlanCode == "" {
				continue
			}
			offer := ovhOfferForPlan(product, p, cat.Locale.Currency)
			offer.Options = map[string]OVHAddonOffer{}
			for _, family := range p.Addons {
				for _, code := range family.Addons {
					for _, a := range cat.Addons {
						if a.PlanCode == code {
							details := ovhOfferForPlan(product, a, cat.Locale.Currency)
							offer.Options[code] = OVHAddonOffer{code, a.InvoiceName, details.Configurations, details.Prices}
							break
						}
					}
				}
			}
			var productBlobs map[string]any
			_ = json.Unmarshal(p.Blobs, &productBlobs)
			for _, im := range collectMaps(nestedValue(productBlobs, "technical", "os", "features")) {
				if name := mapString(im, "name"); name != "" {
					offer.Images = append(offer.Images, "ovh-dedicated/"+name)
				}
			}
			class := "bare_metal"
			cpu := "dedicated"
			if product == "vps" {
				class = "virtual"
				cpu = "shared"
			}
			t := ServerType{Name: ovhProductSize(product, p.PlanCode), Description: p.InvoiceName, ProviderFamily: product, Platform: "linux", ResourceClass: class, CPUType: cpu, Architecture: "x86", ConfigurableBoot: boolPtr(false), PriceKnown: boolPtr(false), OVHOffer: &offer}
			for _, c := range p.Configurations {
				if c.Name == "vps_datacenter" || c.Name == "dedicated_datacenter" {
					for _, zone := range c.Values {
						t.AvailableIn = append(t.AvailableIn, ovhRegion(product, zone))
					}
				}
			}
			// Capacities are only exposed when the provider supplies structured values;
			// plan names and marketing descriptions are never treated as hardware facts.
			var blobs map[string]any
			_ = json.Unmarshal(p.Blobs, &blobs)
			if tech, ok := blobs["technical"].(map[string]any); ok {
				t.Cores = nestedInt(tech, "cpu", "cores")
				if server, ok := tech["server"].(map[string]any); ok {
					t.Cores = nestedInt(server, "cpu", "cores")
					if n := nestedInt(server, "cpu", "number"); n > 1 {
						t.Cores *= n
					}
				}
				t.MemoryGB = numberValue(nestedValue(tech, "memory", "size"))
				for _, disk := range collectMaps(nestedValue(tech, "storage", "disks")) {
					t.DiskGB += int(numberValue(disk["capacity"]))
				}
				if arch := firstMapString(tech, "architecture"); arch != "" {
					t.Architecture = arch
				}
			}
			if len(offer.Prices) == 0 {
				t.Available = boolPtr(false)
				t.UnavailableReason = "No currently orderable recurring price"
			}
			out = append(out, t)
		}
	}
	if len(out) == 0 && len(failures) > 0 {
		return nil, errors.New(strings.Join(failures, "; "))
	}
	return out, nil
}
func ovhServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	config, e := ovhPublicConfig(ctx)
	if e != nil {
		return nil, e
	}
	out, contractErr := ovhContractTypes(ctx)
	if contractErr != nil && config["service_name"] == "" {
		return nil, contractErr
	}
	if out == nil {
		out = []ServerType{}
	}
	if config["service_name"] != "" {
		args := map[string]any{}
		if z := catalogScope(ctx).Zone; z != "" && !strings.HasPrefix(z, ovhContractPrefix) {
			args["region"] = z
		}
		data, e := executeProviderTool(ctx, "ovhcloud", "list_flavors", args)
		if e != nil {
			return nil, e
		}
		rows, e := parseProviderServerTypes("ovhcloud", data)
		if e != nil {
			return nil, e
		}
		for i := range rows {
			rows[i].ProviderFamily = "public_cloud"
			rows[i].Platform = "linux"
			rows[i].ResourceClass = "virtual"
			rows[i].ConfigurableBoot = boolPtr(false)
			rows[i].PriceKnown = boolPtr(false)
			rows[i].MonthlyPriceUSD = 0
			rows[i].HourlyPriceUSD = 0
		}
		out = append(rows, out...)
	}
	filter := catalogScope(ctx).ResourceClass
	if filter != "" {
		selected := out[:0]
		for _, t := range out {
			if t.ResourceClass == filter {
				selected = append(selected, t)
			}
		}
		out = selected
	}
	return out, nil
}
func ovhLocations(ctx *sdk.AppCtx) ([]Location, error) {
	config, e := ovhPublicConfig(ctx)
	if e != nil {
		return nil, e
	}
	types, e := ovhContractTypes(ctx)
	if e != nil && config["service_name"] == "" {
		return nil, e
	}
	byID := map[string]Location{}
	for _, t := range types {
		for _, z := range t.AvailableIn {
			_, zone, _ := strings.Cut(strings.TrimPrefix(z, ovhContractPrefix), "/")
			byID[z] = Location{Name: z, Description: zone + " (" + t.ProviderFamily + ")", NetworkZone: zone}
		}
	}
	if config["service_name"] != "" {
		data, e := executeProviderTool(ctx, "ovhcloud", "list_regions", map[string]any{})
		if e != nil {
			return nil, e
		}
		rows, e := parseProviderLocations("ovhcloud", data)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			byID[r.Name] = r
		}
	}
	out := []Location{}
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func ovhLinuxName(name string) bool {
	n := strings.ToLower(name)
	if strings.Contains(n, "windows") || strings.Contains(n, "plesk") || strings.Contains(n, "cpanel") || strings.Contains(n, "freebsd") {
		return false
	}
	for _, flavor := range []string{"ubuntu", "debian", "rocky", "alma", "centos", "fedora", "archlinux"} {
		if strings.Contains(n, flavor) {
			return true
		}
	}
	return false
}
func ovhContractImages(ctx *sdk.AppCtx) ([]Image, error) {
	out := []Image{}
	seen := map[string]int{}
	cat, vpsErr := ovhLoadCatalog(ctx, "vps")
	for _, plan := range cat.Plans {
		for _, c := range plan.Configurations {
			if c.Name != "vps_os" {
				continue
			}
			for _, name := range c.Values {
				if !ovhLinuxName(name) {
					continue
				}
				id := "ovh-vps/" + name
				if i, ok := seen[id]; ok {
					out[i].CompatibleTypes = append(out[i].CompatibleTypes, ovhProductSize("vps", plan.PlanCode))
					continue
				}
				seen[id] = len(out)
				out = append(out, Image{Name: id, Description: name, OSFlavor: imageFlavor(name), OSVersion: imageVersion(name), Architecture: "x86", Platform: "linux", ResourceClass: "virtual", CompatibleTypes: []string{ovhProductSize("vps", plan.PlanCode)}})
			}
		}
	}
	data, e := executeProviderTool(ctx, "ovhcloud", "dedicated_images_info", map[string]any{})
	if e != nil {
		if len(out) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("OVH image catalogs unavailable: VPS %v, dedicated %w", vpsErr, e)
	}
	var raw any
	if e = json.Unmarshal(data, &raw); e != nil {
		return nil, e
	}
	for _, row := range collectMaps(raw) {
		id := firstMapString(row, "templateName")
		desc := firstMapString(row, "description", "distribution", "name")
		if id == "" || mapString(row, "family") != "linux" || !ovhLinuxName(id+" "+desc) {
			continue
		}
		if end := mapString(row, "endOfInstall"); end != "" && end < time.Now().UTC().Format("2006-01-02") {
			continue
		}
		out = append(out, Image{Name: "ovh-dedicated/" + id, Description: desc, OSFlavor: imageFlavor(desc), OSVersion: imageVersion(desc), Architecture: "x86", Platform: "linux", ResourceClass: "bare_metal"})
	}
	return out, nil
}
func ovhImages(ctx *sdk.AppCtx) ([]Image, error) {
	config, e := ovhPublicConfig(ctx)
	if e != nil {
		return nil, e
	}
	out, contractErr := ovhContractImages(ctx)
	if contractErr != nil && config["service_name"] == "" {
		return nil, contractErr
	}
	if out == nil {
		out = []Image{}
	}
	if config["service_name"] != "" {
		args := map[string]any{"osType": "linux"}
		if z := catalogScope(ctx).Zone; z != "" && !strings.HasPrefix(z, ovhContractPrefix) {
			args["region"] = z
		}
		data, e := executeProviderTool(ctx, "ovhcloud", "list_images", args)
		if e != nil {
			return nil, e
		}
		images, e := parseProviderImages("ovhcloud", data)
		if e != nil {
			return nil, e
		}
		for i := range images {
			images[i].Platform = "linux"
			images[i].ResourceClass = "virtual"
		}
		out = append(images, out...)
	}
	return out, nil
}

// Product-level metadata carries hardware facts and additional configuration
// labels; plan-level fields override it. Never parse specs from plan codes.
func ovhEnrichPlan(cat *ovhCatalog, product string, plan *ovhPlan) {
	for _, p := range cat.Products {
		if p.Name != product {
			continue
		}
		var base, extra map[string]json.RawMessage
		_ = json.Unmarshal(p.Blobs, &base)
		_ = json.Unmarshal(plan.Blobs, &extra)
		if base == nil {
			base = map[string]json.RawMessage{}
		}
		for k, v := range extra {
			base[k] = v
		}
		plan.Blobs = mustMarshal(base)
		for _, cfg := range p.Configurations {
			found := false
			for _, existing := range plan.Configurations {
				if existing.Name == cfg.Name {
					found = true
				}
			}
			if !found {
				plan.Configurations = append(plan.Configurations, cfg)
			}
		}
		return
	}
}
