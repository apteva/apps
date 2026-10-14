// Domains — DNS, inventory, purchases, valuations, sales, and SDK-managed lifecycle refresh.
//
// Apps that need to write DNS records (messaging for SES MX/DKIM,
// storage for CDN CNAMEs, future certs app for ACME challenges) call
// this app instead of speaking each registrar's API directly.
//
// Architecture:
//   - manifest declares one required integration: dns_provider with
//     compatible_slugs [porkbun, namecheap, ionos, spaceship].
//   - all DNS record CRUD goes through the bound provider via
//     ctx.PlatformAPI().ExecuteIntegrationTool.
//   - no local record cache — records are always live; the local
//     `domains` table just tracks which domains this project uses.
//   - upsert (domain_records_set) is composed: list-by-name+type;
//     edit if present, create if absent. Most providers don't expose
//     atomic upsert.
package main

import (
	"context"
	_ "embed"
	"errors"
	"os"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest ──────────────────────────────────────────────────────

//go:embed apteva.yaml
var manifestYAML string

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("domains requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("domains mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "domain_lifecycle_refresh", Schedule: "@every 30s", Run: func(c context.Context, ctx *sdk.AppCtx) error {
			r, e := a.runLifecycleRefresh(c, ctx, false)
			if e == nil {
				if warnings, ok := r["warnings"].([]string); ok && len(warnings) > 0 {
					ctx.Logger().Warn("domain refresh warnings", "warnings", warnings)
				}
			}
			return e
		}},
		{Name: "domain_event_delivery", Schedule: "@every 5s", Run: func(c context.Context, ctx *sdk.AppCtx) error {
			if ctx.CurrentProject() == "" {
				return nil
			}
			return drainDomainEvents(c, ctx, ctx.CurrentProject())
		}},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/domains", Handler: a.handleDomainsList},
		{Pattern: "/domains/", Handler: a.handleDomainItem},
		{Pattern: "/connections", Handler: a.handleConnectionsList},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
		{
			Name:        "domain_add",
			Description: "Register a domain with this app. By default the bound DNS provider is probed to confirm the domain exists there before recording it locally — pass skip_validation:true to bypass (just-registered domains, provider outage, externally-hosted DNS). Args: name (e.g. 'acme.com'), connection_id? (specific provider connection; defaults to the install's role binding), registrar?, dns_provider?, notes?, skip_validation?.",
			InputSchema: schemaObject(map[string]any{
				"name":                   map[string]any{"type": "string"},
				"connection_id":          map[string]any{"type": "integer"},
				"registrar":              map[string]any{"type": "string"},
				"dns_provider":           map[string]any{"type": "string"},
				"notes":                  map[string]any{"type": "string"},
				"skip_validation":        map[string]any{"type": "boolean"},
				"use_default_connection": map[string]any{"type": "boolean"},
			}, []string{"name"}),
			Handler: a.toolDomainAdd,
		},
		{
			Name:        "domain_remove",
			Description: "Soft-delete a domain from this app's inventory. Doesn't touch the actual registration. Args: name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolDomainRemove,
		},
		{
			Name:        "domain_list",
			Description: "List domains for this project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolDomainList,
		},
		{
			Name:        "domain_get",
			Description: "Fetch one domain by name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolDomainGet,
		},
		{
			Name:        "domain_availability_check",
			Description: "Check whether a domain is available for registration via the registrar provider. Safe/read-only. Args: domain, connection_id?. Porkbun and Spaceship are supported; if no registrar_provider is bound, the DNS provider is used when it is Porkbun or Spaceship.",
			InputSchema: schemaObject(map[string]any{
				"domain":        map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, []string{"domain"}),
			Handler: a.toolDomainAvailabilityCheck,
		},
		{
			Name:        "domain_registration_prepare",
			Description: "Prepare a domain registration after checking availability. Returns an expiring confirmation_token; show the quote or Spaceship account-rate billing terms to the user and call domain_register only after explicit confirmation. Args: domain, years? (must match registry minimum), auto_renew?, whois_privacy?, connection_id?, notes?, contacts?, accept_account_rate?. Spaceship bills account rates without a price cap and uses a saved async operation.",
			InputSchema: schemaObject(map[string]any{
				"domain":              map[string]any{"type": "string"},
				"years":               map[string]any{"type": "integer"},
				"auto_renew":          map[string]any{"type": "boolean"},
				"whois_privacy":       map[string]any{"type": "boolean"},
				"coupon":              map[string]any{"type": "string"},
				"connection_id":       map[string]any{"type": "integer"},
				"notes":               map[string]any{"type": "string"},
				"accept_account_rate": map[string]any{"type": "boolean", "description": "Spaceship: accept account-rate billing without an API price cap."},
				"contacts":            map[string]any{"type": "object", "description": "Spaceship contact IDs: registrant required, admin/tech/billing and attributes optional."},
			}, []string{"domain"}),
			Handler: a.toolDomainRegistrationPrepare,
		},
		{
			Name:        "domain_pricing_get",
			Description: "Fetch registrar pricing via the registrar provider. Args: tld? (e.g. com), connection_id?. Porkbun is supported; Spaceship does not expose a guaranteed standard-domain price quote or charge cap.",
			InputSchema: schemaObject(map[string]any{
				"tld":           map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolDomainPricingGet,
		},
		{
			Name:        "domain_register",
			Description: "Register a domain from a prepared intent. Spaceship submits once and then polls the saved operation; Porkbun retries retain its idempotency key. This SPENDS REAL MONEY. Call domain_registration_prepare, present its quote or account-rate billing terms to the user, obtain explicit confirmation, then pass its confirmation_token. Retries with a completed token are idempotent. Args: confirmation_token.",
			InputSchema: schemaObject(map[string]any{
				"confirmation_token": map[string]any{"type": "string"},
				"resume":             map[string]any{"type": "boolean"},
			}, []string{"confirmation_token"}),
			Handler: a.toolDomainRegister,
		},
		{Name: "domain_update", Description: "Update domain metadata or explicitly select a DNS connection. notes may be empty to clear; connection_id:0 marks the domain unmanaged.", InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}, "registrar": map[string]any{"type": "string"}}, []string{"name"}), Handler: a.toolDomainUpdate},
		{Name: "domain_sync", Description: "Refresh registration expiry from the registrar (Porkbun) and return the current inventory entry.", InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}}, []string{"name"}), Handler: a.toolDomainSync},
		{Name: "domain_registration_status", Description: "Inspect pending purchases. Pass confirmation_token for one purchase; cancel:true cancels only unsubmitted intents; inspect_ownership:true reads the registrar inventory without submitting a purchase.", InputSchema: schemaObject(map[string]any{"confirmation_token": map[string]any{"type": "string"}, "cancel": map[string]any{"type": "boolean"}, "inspect_ownership": map[string]any{"type": "boolean"}, "refresh": map[string]any{"type": "boolean"}}, nil), Handler: a.toolRegistrationStatus},
		{Name: "domain_dns_recovery", Description: "List interrupted DNS replacements or reconcile one recovery_id by reading provider state. Does not write DNS.", InputSchema: schemaObject(map[string]any{"recovery_id": map[string]any{"type": "string"}}, nil), Handler: a.toolDNSRecovery},
		{Name: "domain_sale_publish", Description: "Publish a domain marketplace listing with Dynadot or Spaceship. This changes public commercial state; require explicit user confirmation before calling.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}, "listing_type": map[string]any{"type": "string"}, "price": map[string]any{"type": "string"}, "minimum_offer": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, []string{"domain"}), Handler: a.toolDomainSalePublish},
		{Name: "domain_sale_remove", Description: "Remove a domain marketplace listing from Dynadot or Spaceship.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}}, []string{"domain"}), Handler: a.toolDomainSaleRemove},
		{Name: "domain_sale_get", Description: "Read marketplace listing status for a domain.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}, "refresh": map[string]any{"type": "boolean"}}, []string{"domain"}), Handler: a.toolDomainSaleGet},
		{Name: "domain_refresh", Description: "Refresh tracked listing states, confirmed sales, and pending purchases/valuations. Read-only at providers; never buys, publishes, removes, or starts a paid estimate.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolDomainRefresh},
		{Name: "domain_sale_history", Description: "Read persisted listing transitions and provider-confirmed sales for this project.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}}, nil), Handler: a.toolDomainSaleHistory},
		{Name: "domain_events_list", Description: "Read durable domain lifecycle events and bus delivery status.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}}, nil), Handler: a.toolDomainEventsList},
		{Name: "domain_estimate_start", Description: "Start a persisted paid domain valuation through Replicate/HumbleWorth.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}, "domains": map[string]any{"type": "string"}, "connection_id": map[string]any{"type": "integer"}}, nil), Handler: a.toolDomainEstimateStart},
		{Name: "domain_estimate_list", Description: "List stored domain valuation results. refresh:true polls pending jobs and persists their results; it does not start paid predictions.", InputSchema: schemaObject(map[string]any{"domain": map[string]any{"type": "string"}, "refresh": map[string]any{"type": "boolean"}}, nil), Handler: a.toolDomainEstimateList},

		{
			Name:        "domain_records_list",
			Description: "List DNS records on a domain via the bound dns_provider. Args: domain, type? (filter), name? (subdomain filter).",
			InputSchema: schemaObject(map[string]any{
				"domain": map[string]any{"type": "string"},
				"type":   map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
			}, []string{"domain"}),
			Handler: a.toolDomainRecordsList,
		},
		{
			Name: "domain_records_set",
			Description: "Create or update a DNS record. mode:create appends a new value without overwriting; mode:ensure leaves an existing value intact (including its TTL); mode:upsert is the compatibility default. Pass record_id to update exactly one listed record; without it, a single name/type match is updated, no match is created, and ambiguous RRsets are rejected. " +
				"Args: domain (e.g. 'acme.com'), name ('@' for apex, or subdomain like 'mail'), type (A|AAAA|CNAME|MX|TXT|NS|SRV|CAA), value, ttl? (default 600). " +
				"For MX records, value should be 'priority host', e.g. '10 inbound-smtp.eu-west-1.amazonaws.com'.",
			InputSchema: schemaObject(map[string]any{
				"domain":                 map[string]any{"type": "string"},
				"name":                   map[string]any{"type": "string"},
				"type":                   map[string]any{"type": "string"},
				"value":                  map[string]any{"type": "string"},
				"ttl":                    map[string]any{"type": "integer"},
				"record_id":              map[string]any{"type": "string"},
				"expected_connection_id": map[string]any{"type": "integer"},
				"namecheap_email_type":   map[string]any{"type": "string", "enum": []string{"MX", "MXE", "FWD", "OX"}, "description": "Explicit Namecheap mail routing mode when getHosts omits it. MX=custom mail, MXE=mail forwarding IP, FWD=Namecheap forwarding, OX=Namecheap Private Email."},
				"expected_record":        map[string]any{"type": "object"},
				"mode":                   map[string]any{"type": "string", "enum": []string{"upsert", "create", "ensure"}},
			}, []string{"domain", "name", "type", "value"}),
			Handler: a.toolDomainRecordsSet,
		},
		{
			Name:        "domain_records_delete",
			Description: "Delete DNS records. Pass record_id to delete exactly one listed record. Without record_id, all records matching (domain, name, type) are deleted. Args: domain, name, type, record_id?.",
			InputSchema: schemaObject(map[string]any{
				"domain":                 map[string]any{"type": "string"},
				"name":                   map[string]any{"type": "string"},
				"type":                   map[string]any{"type": "string"},
				"record_id":              map[string]any{"type": "string"},
				"expected_connection_id": map[string]any{"type": "integer"},
				"namecheap_email_type":   map[string]any{"type": "string", "enum": []string{"MX", "MXE", "FWD", "OX"}, "description": "Explicit Namecheap mail routing mode when getHosts omits it. MX=custom mail, MXE=mail forwarding IP, FWD=Namecheap forwarding, OX=Namecheap Private Email."},
				"expected_record":        map[string]any{"type": "object"},
			}, []string{"domain", "name", "type"}),
			Handler: a.toolDomainRecordsDelete,
		},
	}
	for i := range tools {
		handler := tools[i].Handler
		schema := tools[i].InputSchema
		tools[i].Handler = func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
			if err := validateToolInput(schema, args); err != nil {
				return nil, err
			}
			return handler(ctx, args)
		}
	}
	return tools
}

func main() { sdk.Run(&App{}) }
