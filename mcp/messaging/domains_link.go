package main

// Cross-app reads against the Domains app. Messaging uses the
// per-project inventory to populate the Add Sender form in the panel —
// when the operator has the Domains app installed and curated, picking
// from the list is safer than free-text (no typos, no asking SES to
// verify a domain the operator can't actually put DNS records on).
//
// Domains may be global-scoped, so every app call injects _project_id.
// Platform DNS grants are merged with Domains inventory when available.

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type domainRow struct {
	Name            string `json:"name"`
	RegistrarSlug   string `json:"registrar_slug,omitempty"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
	Source          string `json:"source,omitempty"`
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
	rows := []domainRow{}
	seen := map[string]bool{}
	if grants, err := ctx.PlatformAPI().ListDomainGrants(); err == nil {
		for _, grant := range grants {
			name := strings.ToLower(strings.Trim(strings.TrimPrefix(grant.Domain, "*."), "."))
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			source := grant.Source
			if source == "" {
				source = "platform"
			}
			rows = append(rows, domainRow{Name: name, Source: source})
		}
	}
	if !isAppDepBound(ctx, "domains") {
		return rows, nil
	}
	var resp domainListResp
	err := ctx.PlatformAPI().CallAppResult("domains", "domain_list", map[string]any{
		"_project_id": projectID,
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("domains.domain_list: %w", err)
	}
	for _, domain := range resp.Domains {
		domain.Name = strings.ToLower(strings.Trim(domain.Name, "."))
		if domain.Name == "" || seen[domain.Name] {
			continue
		}
		seen[domain.Name] = true
		domain.Source = "domains"
		rows = append(rows, domain)
	}
	return rows, nil
}
