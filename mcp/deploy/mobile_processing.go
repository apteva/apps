package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Availability is provider evidence, not merely beta-group membership.
func (a *App) syncTestFlightAvailability(bound *sdk.BoundIntegration, rel *Release, buildID, groupID string, meta *mobileReleaseMeta) error {
	raw, err := executeIntegration(bound, "get_build", map[string]any{"build_id": buildID, "include": "buildBetaDetail,betaAppReviewSubmission"})
	if err != nil {
		return err
	}
	var response struct {
		Included []struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"included"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return err
	}
	state := ""
	key := "internalBuildState"
	if rel.Channel == "external" {
		key = "externalBuildState"
	}
	for _, item := range response.Included {
		if item.Type == "buildBetaDetails" {
			state, _ = item.Attributes[key].(string)
		}
	}
	meta.BetaGroupID = groupID
	switch state {
	case "IN_BETA_TESTING":
		return markTestFlightAvailable(rel, buildID, groupID, meta)
	case "EXPIRED", "BETA_REJECTED", "INVALID_BINARY":
		return dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": "failed", "external_id": buildID, "external_status": strings.ToLower(state), "error": "TestFlight build is unavailable: " + state, "release_meta_json": mustJSON(meta)})
	default:
		if state == "" {
			state = "AVAILABILITY_UNCONFIRMED"
		}
		return dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": "starting", "external_id": buildID, "external_status": strings.ToLower(state), "release_meta_json": mustJSON(meta)})
	}
}
func appleProcessingBuild(raw json.RawMessage, buildNumber string) (string, string, error) {
	var response struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Version string `json:"version"`
				State   string `json:"processingState"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", "", err
	}
	for _, build := range response.Data {
		if buildNumber != "" && build.Attributes.Version != buildNumber {
			continue
		}
		if build.Attributes.State == "FAILED" || build.Attributes.State == "INVALID" {
			return build.ID, build.Attributes.State, fmt.Errorf("App Store processing %s for build %s", strings.ToLower(build.Attributes.State), buildNumber)
		}
		if build.Attributes.State == "VALID" {
			return build.ID, "VALID", nil
		}
	}
	return "", "PROCESSING", nil
}
