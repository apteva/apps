package main

// Cross-app reads against the Domains app. Messaging uses the
// per-project inventory to populate the Add Sender form in the panel —
// when the operator has the Domains app installed and curated, picking
// from the list is safer than free-text (no typos, no asking SES to
// verify a domain the operator can't actually put DNS records on).
//
// Domains is global-scoped on prod, so every call must inject
// _project_id; without it a global Domains install rejects the request
// with "project_id missing". senders_create's bootstrapPublishDNSRecord
// path predates that rule and may need the same treatment — out of
// scope for this file.

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

type domainRow struct {
	Name            string `json:"name"`
	RegistrarSlug   string `json:"registrar_slug,omitempty"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
}

type domainListResp struct {
	Domains []domainRow `json:"domains"`
	Count   int         `json:"count"`
}

// listDomainsForProject calls domains.domain_list. Returns a nil slice
// and nil error when the Domains app isn't bound — callers fall back
// to free-text input.
func listDomainsForProject(ctx *sdk.AppCtx, projectID string) ([]domainRow, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform unavailable")
	}
	if !isAppDepBound(ctx, "domains") {
		return nil, nil
	}
	var resp domainListResp
	err := ctx.PlatformAPI().CallAppResult("domains", "domain_list", map[string]any{
		"_project_id": projectID,
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("domains.domain_list: %w", err)
	}
	return resp.Domains, nil
}
