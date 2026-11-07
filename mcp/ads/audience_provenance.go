package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

var provenancePlatform = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var provenanceInteraction = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)

func (a *App) extendAudienceProvenanceTools(tools []sdk.Tool) []sdk.Tool {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	for i := range tools {
		if tools[i].Name == "audience_list" {
			p := asMap(tools[i].InputSchema["properties"])
			p["provenance_status"] = map[string]any{"type": "string", "enum": []string{"verified", "unknown"}}
			for _, key := range []string{"source_platform", "source_account_id", "source_channel_id", "source_asset_id", "interaction_type"} {
				p[key] = text("Filter verified provenance by exact value; unverified audience names are never matched.")
			}
		}
	}
	return append(tools, sdk.Tool{
		Name:        "audience_provenance_set",
		Description: "Remember or clear a verified audience-source association locally for any provider, including read-only audiences. Requires confirmed=true and verification evidence. Does not alter the provider audience or targeting. Provenance survives refreshes and restarts. Manual attestations remain distinct from provider-verified data.",
		InputSchema: schemaObject(map[string]any{
			"ad_account_id": map[string]any{"type": "integer"}, "audience_id": map[string]any{"type": "integer"},
			"confirmed": map[string]any{"type": "boolean", "description": "Explicitly attest that the association was checked against source configuration, or confirm clearing it."},
			"clear":     map[string]any{"type": "boolean", "description": "Clear the saved association without changing the provider audience."},
			"provenance": schemaObject(map[string]any{
				"source_platform":   text("Source system slug, for example youtube, facebook, website, mobile_app or crm."),
				"source_name":       text("Optional human-readable source label; never replaces an identity ID or evidence."),
				"source_account_id": text("Source account ID; distinct from the advertising account."), "source_channel_id": text("Source channel or page ID."), "source_asset_id": text("Source video, app, site or other object ID."),
				"interaction_type":       text("Verified interaction/event, for example video_view, channel_subscription or website_visit."),
				"verification_evidence":  text("What was checked to establish this exact list-to-source mapping; names alone are not evidence."),
				"verification_reference": text("Optional supporting screenshot, document or source-configuration reference."),
			}, []string{"source_platform", "interaction_type", "verification_evidence"}),
		}, []string{"ad_account_id", "audience_id", "confirmed"}), Handler: a.toolAudienceProvenanceSet,
	})
}

func verifiedAudienceProvenance(resource adResource) map[string]any {
	if resource.Provenance["status"] != "verified" {
		return map[string]any{"status": "unknown"}
	}
	return cloneMap(resource.Provenance)
}

func audienceKind(platform string, resource adResource) string {
	if (platform == "google" || strings.HasPrefix(resource.ProviderType, "google_")) && resource.ProviderType == "google_user_list" {
		native := strings.ToUpper(firstString(resource.Metadata, "native_type"))
		if native == "" {
			native = strings.ToUpper(firstString(resource.Metadata, "type"))
		}
		// Correct old cached RULE_BASED -> website labels before the first refresh.
		if native == "RULE_BASED" {
			return "rule_based"
		}
	}
	return normalizedAudienceType(firstString(resource.Metadata, "subtype", "type", "list_type"))
}

func audienceMetadata(resource adResource) map[string]any {
	metadata := cloneMap(resource.Metadata)
	if resource.ProviderType == "google_user_list" && audienceKind("google", resource) == "rule_based" {
		metadata["type"] = "rule_based"
		metadata["native_type"] = "RULE_BASED"
		if metadata["engagement_source"] == nil {
			metadata["engagement_source"] = "unknown"
		}
	}
	return metadata
}

func audienceSourceLabel(resource adResource) string {
	p := verifiedAudienceProvenance(resource)
	if p["status"] == "verified" {
		identity := firstString(p, "source_channel_id", "source_asset_id", "source_account_id")
		if name := firstString(p, "source_name"); name != "" {
			identity = name + " [" + identity + "]"
		}
		return firstString(p, "source_platform") + " · " + identity + " · " + firstString(p, "interaction_type")
	}
	return "Source unverified"
}

func audienceMatchesProvenance(resource adResource, args map[string]any) bool {
	p := verifiedAudienceProvenance(resource)
	if status := firstString(args, "provenance_status"); status != "" && p["status"] != status {
		return false
	}
	for _, key := range []string{"source_platform", "source_account_id", "source_channel_id", "source_asset_id", "interaction_type"} {
		want := strings.TrimSpace(firstString(args, key))
		if want == "" {
			continue
		}
		if key == "source_platform" || key == "interaction_type" {
			want = strings.ToLower(want)
		}
		if p["status"] != "verified" || p[key] != want {
			return false
		}
	}
	return true
}

func validateAudienceProvenance(raw map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, spec := range []struct {
		key string
		max int
	}{{"source_platform", 64}, {"source_name", 256}, {"source_account_id", 256}, {"source_channel_id", 256}, {"source_asset_id", 256}, {"interaction_type", 128}, {"verification_evidence", 4000}, {"verification_reference", 2048}} {
		value, ok := raw[spec.key].(string)
		if raw[spec.key] != nil && !ok {
			return nil, fmt.Errorf("%s must be a string", spec.key)
		}
		value = strings.TrimSpace(value)
		if utf8.RuneCountInString(value) > spec.max {
			return nil, fmt.Errorf("%s exceeds %d characters", spec.key, spec.max)
		}
		if value != "" {
			out[spec.key] = value
		}
	}
	platform := strings.ToLower(firstString(out, "source_platform"))
	interaction := strings.ToLower(firstString(out, "interaction_type"))
	if !provenancePlatform.MatchString(platform) || !provenanceInteraction.MatchString(interaction) {
		return nil, fmt.Errorf("source_platform and interaction_type must be nonempty source/event identifiers")
	}
	out["source_platform"], out["interaction_type"] = platform, interaction
	if firstString(out, "source_account_id", "source_channel_id", "source_asset_id") == "" {
		return nil, fmt.Errorf("a source account, channel or asset ID is required")
	}
	if firstString(out, "verification_evidence") == "" {
		return nil, fmt.Errorf("verification_evidence is required; do not infer a source from the audience name")
	}
	// Server owns verification metadata. Caller-supplied status, method or dates
	// cannot turn this local attestation into provider confirmation.
	out["status"] = "verified"
	out["verification_method"] = "manual_attestation"
	out["verified_at"] = time.Now().UTC().Format(time.RFC3339)
	return out, nil
}

func (a *App) toolAudienceProvenanceSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	r, err := a.getResource(ctx, acct, int64(intArg(args, "audience_id", 0)))
	if err != nil || r.Kind != resourceAudience || r.Status == "stale" {
		return mcpError("audience not found in this ad account"), nil
	}
	if args["confirmed"] != true {
		return mcpError("confirmed=true is required after checking the audience source configuration"), nil
	}
	p := map[string]any{}
	if boolArg(args, "clear") {
		if len(asMap(args["provenance"])) > 0 {
			return mcpError("clear cannot be combined with a new provenance association"), nil
		}
	} else {
		p, err = validateAudienceProvenance(asMap(args["provenance"]))
		if err != nil {
			return mcpError(err.Error()), nil
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	result, err := ctx.AppDB().Exec(`UPDATE ad_resources SET audience_provenance_json=? WHERE id=? AND project_id=? AND ad_account_id=? AND kind='audience'`, string(raw), r.ID, acct.ProjectID, acct.ID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return mcpError("audience no longer exists"), nil
	}
	r.Provenance = p
	a.emitAudienceEvent(ctx, acct, "audience.provenance.updated", r, 0, "completed")
	return normalizedAudience(acct.Platform, *r), nil
}
