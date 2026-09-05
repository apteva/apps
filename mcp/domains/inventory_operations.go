package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolDomainUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	unlock, err := acquireDNSMutation(ctx.Done(), name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	d, err := dbDomainGetByName(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, apiError(404, "domain not found")
	}
	if _, present := args["connection_id"]; present {
		id, err := strictIntArg(args, "connection_id", 0, 0, 9007199254740991)
		if err != nil {
			return nil, err
		}
		d.ConnectionID = int64(id)
		d.ConnectionMode = "unmanaged"
		d.DNSProviderSlug = ""
		if id > 0 {
			prov, bound, err := a.providerFor(ctx, int64(id), pid)
			if err != nil {
				return nil, err
			}
			if _, err = prov.List(ctx, name); err != nil {
				return nil, err
			}
			d.ConnectionMode = "pinned"
			d.DNSProviderSlug = bound.AppSlug
		}
	}
	if notes, ok := args["notes"].(string); ok {
		d.Notes = notes
	}
	if reg, ok := args["registrar"].(string); ok {
		d.RegistrarSlug = strings.ToLower(strings.TrimSpace(reg))
	}
	_, err = ctx.AppDB().Exec(`UPDATE domains SET connection_id=NULLIF(?,0),connection_mode=?,dns_provider_slug=?,registrar_slug=?,notes=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND deleted_at IS NULL`, d.ConnectionID, d.ConnectionMode, d.DNSProviderSlug, d.RegistrarSlug, d.Notes, d.ID, pid)
	if err != nil {
		return nil, err
	}
	d, err = dbDomainGetByName(ctx.AppDB(), pid, name)
	return map[string]any{"domain": d}, err
}

func (a *App) toolDomainSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
		return nil, apiError(404, "domain not found")
	}
	id, err := strictIntArg(args, "connection_id", 0, 0, 9007199254740991)
	if err != nil {
		return nil, err
	}
	if id == 0 && d.RegistrarSlug == "porkbun" && d.DNSProviderSlug == "porkbun" {
		id = int(d.ConnectionID)
	}
	_, bound, err := a.registrarFor(ctx, int64(id), pid)
	if err != nil {
		return nil, err
	}
	if bound.AppSlug != "porkbun" {
		return nil, errors.New("expiry sync is currently supported for Porkbun")
	}
	row, err := porkbunOwnedDomain(ctx, bound, name)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, apiError(404, "domain not found at registrar")
	}
	expires := firstString([]map[string]any{row}, "expireDate", "expirationDate", "expiresAt")
	valid := false
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if _, err := time.Parse(layout, expires); err == nil {
			valid = true
		}
	}
	if !valid {
		return nil, errors.New("registrar omitted a valid expiry date")
	}
	_, err = ctx.AppDB().Exec(`UPDATE domains SET expires_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, expires, d.ID, pid)
	if err != nil {
		return nil, err
	}
	d, err = dbDomainGetByName(ctx.AppDB(), pid, name)
	return map[string]any{"domain": d}, err
}

func validateToolInput(schema map[string]any, args map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"].([]string); ok {
		for _, key := range required {
			if _, ok := args[key]; !ok {
				return fmt.Errorf("%s required", key)
			}
		}
	}
	for key, v := range args {
		if key == "_project_id" {
			if _, ok := v.(string); !ok {
				return errors.New("project ID must be a string")
			}
			continue
		}
		raw, ok := props[key]
		if !ok {
			return fmt.Errorf("unknown argument %q", key)
		}
		def, _ := raw.(map[string]any)
		switch def["type"] {
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		case "object":
			object, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object", key)
			}
			if key == "expected_record" {
				if _, ok := object["value"].(string); !ok {
					return errors.New("expected_record.value must be a string")
				}
				for _, field := range []string{"ttl", "prio"} {
					if _, ok := object[field]; !ok {
						return fmt.Errorf("expected_record.%s required", field)
					}
					if _, err := strictIntArg(object, field, 0, 0, 2147483647); err != nil {
						return err
					}
				}
			}
		case "integer":
			if _, err := strictIntArg(args, key, 0, -9007199254740991, 9007199254740991); err != nil {
				return err
			}
		}
		if allowed, ok := def["enum"].([]string); ok && !includes(allowed, fmt.Sprint(v)) {
			return fmt.Errorf("invalid %s", key)
		}
	}
	return nil
}
