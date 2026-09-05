package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Registrar provider abstraction ───────────────────────────────

type registrarProviderImpl interface {
	CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error)
	Pricing(ctx *sdk.AppCtx, tld string) (any, error)
	Register(ctx *sdk.AppCtx, req DomainRegistrationRequest) (json.RawMessage, error)
}

func (a *App) registrarFor(ctx *sdk.AppCtx, connID int64, projectID string) (registrarProviderImpl, *sdk.BoundIntegration, error) {
	if connID > 0 {
		conn, err := ctx.PlatformAPI().GetConnection(connID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up connection %d: %w", connID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %d not found", connID)
		}
		if conn.Status != "" && !strings.EqualFold(conn.Status, "active") {
			return nil, nil, fmt.Errorf("connection %d is not active (status %q)", connID, conn.Status)
		}
		if projectID != "" && conn.ProjectID != "" && conn.ProjectID != projectID {
			return nil, nil, fmt.Errorf("connection %d belongs to project %q, not %q", connID, conn.ProjectID, projectID)
		}
		bound := &sdk.BoundIntegration{
			Role:         "registrar_provider",
			Kind:         "integration",
			ConnectionID: connID,
			AppSlug:      conn.AppSlug,
		}
		switch conn.AppSlug {
		case "porkbun":
			return &porkbunRegistrar{bound: bound}, bound, nil
		case "spaceship":
			return &spaceshipRegistrar{bound: bound}, bound, nil
		}
		return nil, bound, fmt.Errorf("unsupported registrar provider %q on connection %d (compatible: porkbun, spaceship for availability)", conn.AppSlug, connID)
	}
	id, err := selectedConnectionID(ctx, "registrar_provider", "dns_provider")
	if err != nil {
		return nil, nil, err
	}
	if id == 0 {
		return nil, nil, errors.New("no compatible registrar provider bound")
	}
	prov, validated, err := a.registrarFor(ctx, id, projectID)
	if validated != nil {
		validated.IsDefault = true
	}
	return prov, validated, err
}

func (a *App) toolDomainAvailabilityCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	reg, _, err := a.registrarFor(ctx, int64(intArg(args, "connection_id", 0)), connectionProject(ctx, args))
	if err != nil {
		avail, ferr := publicRDAPAvailability(domain, err)
		if ferr != nil {
			return nil, err
		}
		return map[string]any{"availability": avail}, nil
	}
	avail, err := reg.CheckAvailability(ctx, domain)
	if err != nil {
		avail, ferr := publicRDAPAvailability(domain, err)
		if ferr != nil {
			return nil, err
		}
		return map[string]any{"availability": avail}, nil
	}
	return map[string]any{"availability": avail}, nil
}

func (a *App) toolDomainPricingGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tld := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(strArg(args, "tld"))), ".")
	reg, bound, err := a.registrarFor(ctx, int64(intArg(args, "connection_id", 0)), connectionProject(ctx, args))
	if err != nil {
		return nil, err
	}
	pricing, err := reg.Pricing(ctx, tld)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider":      bound.AppSlug,
		"connection_id": bound.ConnectionID,
		"tld":           tld,
		"pricing":       pricing,
	}, nil
}

func (a *App) registrationResult(ctx *sdk.AppCtx, intent *RegistrationIntent, replay bool) map[string]any {
	if replay && len(intent.Result) > 0 {
		var result map[string]any
		if json.Unmarshal(intent.Result, &result) == nil {
			result["idempotent_replay"] = true
			return result
		}
	}
	d, err := dbDomainGetByName(ctx.AppDB(), intent.ProjectID, intent.Domain)
	if !replay && err == nil && d == nil {
		d, err = upsertDomainInventory(ctx, intent.ProjectID, intent.Domain, intent.Provider, intent.Provider, intent.Notes, intent.ConnectionID)
	}
	out := map[string]any{
		"registered": true, "domain": d, "provider": intent.Provider,
		"connection_id": intent.ConnectionID, "raw": intent.Raw, "idempotent_replay": replay,
	}
	if err != nil {
		out["inventory_warning"] = err.Error()
	}
	return out
}

func randomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
