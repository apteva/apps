package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
)

func readPublicationAppRecord(ctx *sdk.AppCtx, app, tool string, args map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult(app, tool, args, &out); err != nil {
		return nil, err
	}
	if isError, _ := out["isError"].(bool); isError {
		return nil, errors.New("record app rejected the request")
	}
	if message := toString(out["error"]); message != "" {
		return nil, errors.New(redactDeliverySecrets(message))
	}
	return out, nil
}
func correctionEvidence(id string, before, after publicationFacts) string {
	raw, _ := json.Marshal(map[string]any{"social_correction_id": id, "before": before, "after": after, "note": "Record-only correction; native identity, audience and confidence preserved; no publishing."})
	return string(raw)
}
func sameFactTime(a, b string) bool {
	left, right := nativeTimestamp(a), nativeTimestamp(b)
	return left != nil && right != nil && *left == *right
}

func repairCatalogPublication(ctx *sdk.AppCtx, r recoveryTarget, ref publicationRecordRef, id string, before, after publicationFacts) (map[string]any, error) {
	out, err := readPublicationAppRecord(ctx, "content-catalog", "content_catalog_posts_get", map[string]any{"id": ref.ID})
	if err != nil {
		return nil, err
	}
	record, ok := out["post"].(map[string]any)
	if !ok {
		return nil, errors.New("Catalog did not return the existing record")
	}
	install, err := currentSocialInstall(ctx)
	if err != nil {
		return nil, err
	}
	if toString(record["id"]) != ref.ID || toString(record["destination"]) != r.Platform || toString(record["account_ref"]) != stringAccountReference(install, r.AccountID) || toString(record["external_post_id"]) != r.PublicID {
		return nil, errors.New("Catalog platform/account/native identity mismatch; no record changed")
	}
	actual := toString(record["actual_at"])
	// Independently verified membership dates cannot be replaced by upload dates.
	if after.PublishedAt != nil && toString(record["access_policy"]) != "membership" {
		actual = *after.PublishedAt
	}
	marker := `"social_correction_id":"` + id + `"`
	link := valueOrEmpty(after.URL)
	if after.URL == nil {
		link = safePublicationURL(r.Platform, toString(record["external_url"]))
	}
	if strings.Contains(toString(record["evidence_source"]), marker) && toString(record["external_url"]) == link && toString(record["actual_at"]) == actual {
		return map[string]any{"id": ref.ID, "revision": record["revision"], "idempotent": true}, nil
	}
	if intArg(record, "revision", -1) != ref.Revision {
		return nil, errors.New("Catalog revision conflict; reload the same record before repairing")
	}
	input := map[string]any{"post_id": ref.ID, "expected_revision": ref.Revision, "status": record["status"], "external_url": link, "actual_at": actual, "observed_at": record["observed_at"], "evidence_source": toString(record["evidence_source"]) + "\n" + correctionEvidence(id, before, after)}
	result, err := readPublicationAppRecord(ctx, "content-catalog", "content_catalog_posts_record", input)
	if err != nil {
		return nil, err
	}
	if toString(result["post_id"]) != ref.ID && toString(result["publication_id"]) != ref.ID {
		return nil, errors.New("Catalog mutation returned a different record identity")
	}
	return result, nil
}

func repairEditorialPublication(ctx *sdk.AppCtx, r recoveryTarget, ref publicationRecordRef, id string, before, after publicationFacts) (map[string]any, error) {
	releaseID, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil || releaseID <= 0 {
		return nil, errors.New("Editorial release ID must be a positive integer")
	}
	record, err := readPublicationAppRecord(ctx, "editorial", "releases_get", map[string]any{"id": releaseID, "include_results": true})
	if err != nil {
		return nil, err
	}
	if toInt64Loose(record["id"]) != releaseID || toString(record["app"]) != "social" || toInt64Loose(record["external_id"]) != r.PostID {
		return nil, errors.New("Editorial publisher binding mismatch")
	}
	results, ok := record["results"].(map[string]any)
	if !ok {
		return nil, errors.New("Editorial has no account-bound target snapshot; mapping review required")
	}
	targets, ok := results["targets"].([]any)
	if !ok {
		return nil, errors.New("Editorial target snapshot unavailable")
	}
	matches, platformMatches := 0, 0
	var target map[string]any
	for _, value := range targets {
		t, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if toString(t["platform"]) == r.Platform {
			platformMatches++
		}
		if toInt64Loose(t["id"]) == r.TargetID && toInt64Loose(t["social_account_id"]) == r.AccountID && toString(t["platform_post_id"]) == r.PublicID {
			matches++
			target = t
		}
	}
	if matches != 1 {
		return nil, errors.New("Editorial account/native target identity is missing or ambiguous")
	}
	corrections, _ := results["social_publication_corrections"].(map[string]any)
	if _, ok := corrections[id]; ok && (after.URL == nil || toString(target["platform_url"]) == *after.URL) && (after.PublishedAt == nil || sameFactTime(toString(target["published_at"]), *after.PublishedAt)) {
		return map[string]any{"id": releaseID, "revision": record["revision"], "idempotent": true}, nil
	}
	if intArg(record, "revision", -1) != ref.Revision {
		return nil, errors.New("Editorial revision conflict; reload the same release before repairing")
	}
	// Preserve every sibling and custom result key in the revision-checked snapshot.
	link := valueOrEmpty(after.URL)
	if after.URL == nil {
		link = safePublicationURL(r.Platform, toString(target["platform_url"]))
	}
	target["platform_url"], target["published_at"], target["publication_facts"] = link, valueOrEmpty(after.PublishedAt), after
	if corrections == nil {
		corrections = map[string]any{}
	}
	corrections[id] = map[string]any{"social_account_id": r.AccountID, "target_id": r.TargetID, "native_post_id": r.PublicID, "before": before, "after": after}
	results["social_publication_corrections"] = corrections
	patch := map[string]any{"results": results}
	// Scalar dates/links can describe only one destination, never a fan-out.
	if strings.ToLower(strings.TrimSpace(toString(record["channel"]))) == r.Platform && platformMatches == 1 {
		patch["url"] = link
		if after.PublishedAt != nil {
			patch["published_at"] = *after.PublishedAt
		}
	}
	result, err := readPublicationAppRecord(ctx, "editorial", "releases_update", map[string]any{"id": releaseID, "revision": ref.Revision, "patch": patch})
	if err != nil {
		return nil, err
	}
	if toInt64Loose(result["id"]) != releaseID {
		return nil, errors.New("Editorial mutation returned a different release")
	}
	return result, nil
}
