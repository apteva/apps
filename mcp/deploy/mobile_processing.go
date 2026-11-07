package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

const testerAccessNoTesters = "no_testers"

const emptyTesterGroupWarning = "This build's internal TestFlight group has no testers, which is the likely reason the build has not become available. Add a tester with deploy_distribution_update."

// Availability is provider evidence, not merely beta-group membership. Its
// status writes apply only while the release still has the status this sync
// read, because a stop or another sync can land during the Apple call.
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
	observeTesterGroup(bound, rel, groupID, state, meta)
	switch state {
	case "IN_BETA_TESTING":
		return markTestFlightAvailable(rel, buildID, groupID, meta)
	case "EXPIRED", "BETA_REJECTED", "INVALID_BINARY":
		_, err = dbUpdateReleaseIfStatus(globalCtx.AppDB(), rel.ID, rel.Status, map[string]any{"status": "failed", "external_id": buildID, "external_status": strings.ToLower(state), "error": "TestFlight build is unavailable: " + state, "release_meta_json": mustJSON(meta)})
		return err
	default:
		if state == "" {
			state = "AVAILABILITY_UNCONFIRMED"
		}
		_, err = dbUpdateReleaseIfStatus(globalCtx.AppDB(), rel.ID, rel.Status, map[string]any{"status": "starting", "external_id": buildID, "external_status": strings.ToLower(state), "release_meta_json": mustJSON(meta), "error": ""})
		return err
	}
}

// observeTesterGroup records whether an internal build Apple is holding at
// READY_FOR_BETA_TESTING has anyone to reach. Only the count is kept. A failed
// lookup never fails the availability check; the last answer stays until the next.
func observeTesterGroup(bound *sdk.BoundIntegration, rel *Release, groupID, state string, meta *mobileReleaseMeta) {
	if rel.Channel != "internal" || state != "READY_FOR_BETA_TESTING" {
		if meta.TesterAccess == testerAccessNoTesters {
			meta.TesterAccess = ""
		}
		meta.TesterWarning = ""
		return
	}
	raw, err := executeIntegration(bound, "list_beta_testers", map[string]any{"group_id": groupID, "limit": 200})
	if err != nil {
		return
	}
	meta.TesterCount = len(parseAppleTesters(raw))
	meta.TesterSyncedAt = nowUTC()
	meta.TesterAccess, meta.TesterWarning = "configured", ""
	if meta.TesterCount == 0 {
		meta.TesterAccess, meta.TesterWarning = testerAccessNoTesters, emptyTesterGroupWarning
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
