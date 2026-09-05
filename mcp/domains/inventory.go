package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Local domain CRUD ────────────────────────────────────────────

func (a *App) toolDomainAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	reg := strings.ToLower(strings.TrimSpace(strArg(args, "registrar")))
	unlock, lockErr := acquireDNSMutation(ctx.Done(), name)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()

	dns := strings.ToLower(strings.TrimSpace(strArg(args, "dns_provider")))

	existing, err := dbDomainGetByName(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	connID, err := strictIntArg(args, "connection_id", 0, 0, 9007199254740991)
	if err != nil {
		return nil, err
	}
	_, explicitConn := args["connection_id"]
	if existing != nil && !explicitConn {
		connID = int(existing.ConnectionID)
	}
	if existing == nil && !explicitConn && boolArg(args, "use_default_connection", true) {
		id, err := selectedConnectionID(ctx, "dns_provider")
		if err != nil {
			return nil, err
		}
		connID = int(id)
	}
	var validationProvider dnsProviderImpl

	// If we have a connection, derive its slug and use that as the
	// authoritative dns_provider_slug. The free-text dns_provider arg
	// is now just a hint for the "Other / unknown" path.
	if connID > 0 {
		var bound *sdk.BoundIntegration
		validationProvider, bound, err = a.providerFor(ctx, int64(connID), pid)
		if err != nil {
			return nil, fmt.Errorf("validate provider connection: %w", err)
		}
		dns = bound.AppSlug
		if reg == "" {
			reg = dns
		}
	}
	if dns == "" {
		dns = reg
	}
	notes := strArg(args, "notes")

	// Validate the domain exists at the resolved provider before
	// recording it. Catches typos and wrong-provider bindings up
	// front. Skipped when no provider can be resolved (no connection
	// pinned and no role bound), the slug is unsupported, or the
	// caller opts out.
	if !boolArg(args, "skip_validation", false) {
		if validationProvider != nil {
			if _, lerr := validationProvider.List(ctx, name); lerr != nil {
				return nil, fmt.Errorf("validate %q at provider: %w (pass skip_validation:true to add anyway)", name, lerr)
			}
		}
	}

	d, err := upsertDomainInventory(ctx, pid, name, reg, dns, notes, int64(connID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"domain": d}, nil
}

func (a *App) toolDomainRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	unlock, lockErr := acquireDNSMutation(ctx.Done(), name)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	_, err = ctx.AppDB().Exec(
		`UPDATE domains SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "name": name}, nil
}

func (a *App) toolDomainList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbDomainList(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"domains": out, "count": len(out)}, nil
}

func (a *App) toolDomainGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	d, err := dbDomainGetByName(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return map[string]any{"domain": nil, "found": false}, nil
	}
	return map[string]any{"domain": d, "found": true}, nil
}
