package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type appleResourceLinkage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type appleReviewResource struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		State         string `json:"state"`
		SubmittedDate string `json:"submittedDate"`
		Version       string `json:"version"`
		VersionString string `json:"versionString"`
	} `json:"attributes"`
	Relationships struct {
		Items struct {
			Data []appleResourceLinkage `json:"data"`
		} `json:"items"`
		AppStoreVersionForReview struct {
			Data appleResourceLinkage `json:"data"`
		} `json:"appStoreVersionForReview"`
		AppStoreVersion struct {
			Data appleResourceLinkage `json:"data"`
		} `json:"appStoreVersion"`
		Build struct {
			Data appleResourceLinkage `json:"data"`
		} `json:"build"`
	} `json:"relationships"`
}

func decodeAppleReviewResources(raw json.RawMessage) ([]appleReviewResource, []appleReviewResource, error) {
	var envelope struct {
		Data     json.RawMessage       `json:"data"`
		Included []appleReviewResource `json:"included"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, nil, err
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, envelope.Included, nil
	}
	if data[0] == '[' {
		var resources []appleReviewResource
		if err := json.Unmarshal(data, &resources); err != nil {
			return nil, nil, err
		}
		return resources, envelope.Included, nil
	}
	var resource appleReviewResource
	if err := json.Unmarshal(data, &resource); err != nil {
		return nil, nil, err
	}
	return []appleReviewResource{resource}, envelope.Included, nil
}

func appleResource(resources []appleReviewResource, resourceType, id string) *appleReviewResource {
	for i := range resources {
		if resources[i].Type == resourceType && (id == "" || resources[i].ID == id) {
			return &resources[i]
		}
	}
	return nil
}

func appleBuildFromVersion(raw json.RawMessage) (string, string) {
	resources, included, err := decodeAppleReviewResources(raw)
	if err != nil || len(resources) == 0 {
		return "", ""
	}
	buildID := resources[0].Relationships.Build.Data.ID
	if build := appleResource(included, "builds", buildID); build != nil {
		return buildID, build.Attributes.Version
	}
	return buildID, ""
}

func firstAppleBuild(raw json.RawMessage) (string, string) {
	resources, _, err := decodeAppleReviewResources(raw)
	if err != nil || len(resources) == 0 {
		return "", ""
	}
	return resources[0].ID, resources[0].Attributes.Version
}

func applyAppleSubmission(outcome *mobileReviewOutcome, submission *appleReviewResource) {
	if outcome == nil || submission == nil {
		return
	}
	outcome.SubmissionID = submission.ID
	outcome.SubmissionState = submission.Attributes.State
	outcome.SubmittedAt = submission.Attributes.SubmittedDate
}

func appleSubmissionMatchingVersion(raw json.RawMessage, versionID, preferredSubmissionID string) (*appleReviewResource, error) {
	resources, included, err := decodeAppleReviewResources(raw)
	if err != nil {
		return nil, err
	}
	itemVersion := map[string]string{}
	for i := range included {
		if included[i].Type == "reviewSubmissionItems" {
			itemVersion[included[i].ID] = included[i].Relationships.AppStoreVersion.Data.ID
		}
	}
	for i := range resources {
		if preferredSubmissionID != "" && resources[i].ID == preferredSubmissionID {
			return &resources[i], nil
		}
		if resources[i].Relationships.AppStoreVersionForReview.Data.ID == versionID {
			return &resources[i], nil
		}
		for _, item := range resources[i].Relationships.Items.Data {
			if itemVersion[item.ID] == versionID {
				return &resources[i], nil
			}
		}
	}
	return nil, nil
}

func applyAppleReviewItem(outcome *mobileReviewOutcome, raw json.RawMessage, versionID string) error {
	resources, included, err := decodeAppleReviewResources(raw)
	if err != nil {
		return err
	}
	for i := range resources {
		version := resources[i].Relationships.AppStoreVersion.Data.ID
		if version == "" {
			if includedVersion := appleResource(included, "appStoreVersions", ""); includedVersion != nil {
				version = includedVersion.ID
			}
		}
		if versionID != "" && version != versionID {
			continue
		}
		outcome.ItemID = resources[i].ID
		outcome.ItemState = resources[i].Attributes.State
		return nil
	}
	return nil
}

func (a *App) observeAppleReviewOutcome(bound *sdk.BoundIntegration, rel *Release, meta *mobileReleaseMeta, version json.RawMessage) *mobileReviewOutcome {
	outcome := &mobileReviewOutcome{
		Provider: "app_store_connect", VersionID: meta.AppStoreVersionID, VersionName: meta.VersionName,
		DetailsAvailable: false, DetailsSource: "provider_console", SyncedAt: nowUTC(),
	}
	if meta.AppID != "" {
		outcome.ProviderConsoleURL = "https://appstoreconnect.apple.com/apps/" + url.PathEscape(meta.AppID) + "/appstore"
	}
	outcome.SubmittedArtifactID, outcome.SubmittedArtifactVersion = appleBuildFromVersion(version)
	if outcome.SubmittedArtifactID == "" && rel != nil {
		outcome.SubmittedArtifactID = rel.ExternalID
	}
	if outcome.SubmittedArtifactVersion == "" {
		outcome.SubmittedArtifactVersion = meta.BuildNumber
	}

	errorsFound := []string{}
	if meta.AppID != "" {
		latest, err := executeIntegration(bound, "list_builds", map[string]any{
			"app_id": meta.AppID, "processing_state": "VALID", "limit": 1, "sort": "-uploadedDate",
		})
		if err != nil {
			errorsFound = append(errorsFound, "latest build: "+err.Error())
		} else {
			outcome.LatestArtifactID, outcome.LatestArtifactVersion = firstAppleBuild(latest)
		}
	}

	submissionID := meta.ReviewSubmissionID
	if submissionID != "" {
		raw, err := executeIntegration(bound, "get_review_submission", map[string]any{
			"submission_id": submissionID, "include": "appStoreVersionForReview",
			"submission_fields": "platform,submittedDate,state,appStoreVersionForReview",
			"version_fields":    "versionString,appStoreState,build",
		})
		if err != nil {
			errorsFound = append(errorsFound, "review submission: "+err.Error())
		} else {
			resources, _, decodeErr := decodeAppleReviewResources(raw)
			if decodeErr != nil {
				errorsFound = append(errorsFound, "decode review submission: "+decodeErr.Error())
			} else if len(resources) > 0 {
				applyAppleSubmission(outcome, &resources[0])
			}
		}
	}
	if outcome.SubmissionID == "" && meta.AppID != "" {
		raw, err := executeIntegration(bound, "list_review_submissions", map[string]any{
			"app_id": meta.AppID, "platform": "IOS", "include": "items,appStoreVersionForReview", "limit": 200, "limit_items": 50,
			"submission_fields": "platform,submittedDate,state,items,appStoreVersionForReview",
			"item_fields":       "state,appStoreVersion", "version_fields": "versionString,appStoreState,build",
		})
		if err != nil {
			errorsFound = append(errorsFound, "list review submissions: "+err.Error())
		} else if submission, matchErr := appleSubmissionMatchingVersion(raw, meta.AppStoreVersionID, submissionID); matchErr != nil {
			errorsFound = append(errorsFound, "decode review submissions: "+matchErr.Error())
		} else if submission != nil {
			applyAppleSubmission(outcome, submission)
		}
	}
	if outcome.SubmissionID != "" {
		raw, err := executeIntegration(bound, "list_review_submission_items", map[string]any{
			"submission_id": outcome.SubmissionID, "include": "appStoreVersion", "limit": 200,
			"item_fields": "state,appStoreVersion", "version_fields": "versionString,appStoreState,build",
		})
		if err != nil {
			errorsFound = append(errorsFound, "review submission items: "+err.Error())
		} else if err := applyAppleReviewItem(outcome, raw, meta.AppStoreVersionID); err != nil {
			errorsFound = append(errorsFound, "decode review submission items: "+err.Error())
		}
	}
	if outcome.ItemState == "REJECTED" || outcome.SubmissionState == "UNRESOLVED_ISSUES" {
		outcome.ActionRequired = "Open App Store Connect to read and reply to the App Review message."
	}
	outcome.SyncError = strings.Join(errorsFound, "; ")
	return outcome
}

func appleReviewFailureMessage(state string, outcome *mobileReviewOutcome) string {
	message := "App Store state: " + state
	if outcome != nil && outcome.ItemState != "" {
		message += "; review item: " + outcome.ItemState
	}
	message += ". Apple does not expose App Review message text through the App Store Connect API; open App Store Connect for the rejection explanation."
	return message
}

func mobileReviewOutcomeChanged(previous, current *mobileReviewOutcome) bool {
	if previous == nil || current == nil {
		return previous != current
	}
	a, b := *previous, *current
	a.SyncedAt, b.SyncedAt = "", ""
	return mustJSON(a) != mustJSON(b)
}

func (a *App) syncMobileReleaseState(rel *Release) error {
	if rel == nil {
		return errors.New("release required")
	}
	switch rel.Provider {
	case "app_store_connect":
		return a.syncIOSRelease(rel)
	case "google_play":
		return a.syncGooglePlayReleaseState(rel)
	default:
		return fmt.Errorf("release %d has unsupported provider %q", rel.ID, rel.Provider)
	}
}

func (a *App) syncGooglePlayReleaseState(rel *Release) error {
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return err
	}
	if meta.PackageName == "" || rel.Channel == "" {
		return errors.New("Google Play release sync requires package_name and channel")
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return err
	}
	created, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": meta.PackageName})
	if err != nil {
		return err
	}
	editID := jsonStringAt(created, "id")
	if editID == "" {
		return errors.New("Google Play create_edit response missing id")
	}
	defer func() {
		_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": meta.PackageName, "editId": editID})
	}()
	track, err := executeIntegration(bound, "get_track", map[string]any{
		"packageName": meta.PackageName, "editId": editID, "track": rel.Channel,
	})
	if err != nil {
		return err
	}
	status := googleTrackVersionStatus(track, meta.VersionCode)
	if status == "" {
		return fmt.Errorf("Google Play track %q does not contain version code %s", rel.Channel, meta.VersionCode)
	}
	fields := map[string]any{"external_status": status, "error": ""}
	switch strings.ToLower(status) {
	case "completed", "inprogress":
		fields["status"] = "live"
	case "halted":
		fields["status"] = "stopped"
	case "draft":
		fields["status"] = "starting"
	}
	return dbUpdateRelease(globalCtx.AppDB(), rel.ID, fields)
}

func googleTrackVersionStatus(raw json.RawMessage, versionCode string) string {
	var payload struct {
		Releases []struct {
			Status       string   `json:"status"`
			VersionCodes []string `json:"versionCodes"`
		} `json:"releases"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, release := range payload.Releases {
		for _, code := range release.VersionCodes {
			if code == versionCode {
				return release.Status
			}
		}
	}
	return ""
}

func releaseReviewOutcome(rel *Release) *mobileReviewOutcome {
	if rel == nil || strings.TrimSpace(rel.ReleaseMetaJSON) == "" {
		return nil
	}
	var meta mobileReleaseMeta
	if json.Unmarshal([]byte(rel.ReleaseMetaJSON), &meta) != nil {
		return nil
	}
	return meta.ReviewOutcome
}

func latestProductionMobileRelease(db *sql.DB, d *Deployment) (*Release, error) {
	if db == nil || d == nil {
		return nil, nil
	}
	provider := mobileStoreProvider(d.TargetKind)
	if provider == "" {
		return nil, nil
	}
	return dbGetLatestMobileReleaseForChannel(db, d.ID, d.EnvironmentID, provider, "production")
}

func (a *App) syncLatestProductionMobileRelease(d *Deployment) (*Release, error) {
	release, err := latestProductionMobileRelease(globalCtx.AppDB(), d)
	if err != nil || release == nil {
		return release, err
	}
	if err := a.syncMobileReleaseState(release); err != nil {
		return release, err
	}
	return dbGetRelease(globalCtx.AppDB(), release.ID)
}

func addMobileStoreReleaseState(response map[string]any, release *Release, syncErr error) {
	if response == nil {
		return
	}
	if release != nil {
		response["release"] = release
		response["review_outcome"] = releaseReviewOutcome(release)
	}
	if syncErr != nil {
		response["release_sync_error"] = syncErr.Error()
	}
}

func (a *App) toolReleaseSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	releaseID := int64(intArg(args, "release_id"))
	if releaseID <= 0 {
		return nil, errors.New("release_id required")
	}
	rel, err := dbGetRelease(ctx.AppDB(), releaseID)
	if err != nil || rel == nil {
		return nil, fmt.Errorf("release %d not found", releaseID)
	}
	if rel.Provider == "" {
		return nil, errors.New("release sync is only available for mobile store releases")
	}
	if err := a.syncMobileReleaseState(rel); err != nil {
		return nil, err
	}
	fresh, err := dbGetRelease(ctx.AppDB(), releaseID)
	if err != nil || fresh == nil {
		return nil, errors.New("synchronized release not found")
	}

	response := map[string]any{"release": fresh, "review_outcome": releaseReviewOutcome(fresh)}
	base, deploymentErr := dbGetDeploymentByID(ctx.AppDB(), rel.DeploymentID)
	if deploymentErr != nil {
		response["store_sync_error"] = deploymentErr.Error()
		return response, nil
	}
	if base == nil || (base.TargetKind != "ios" && base.TargetKind != "android") {
		return response, nil
	}
	d := base
	if rel.EnvironmentID > 0 {
		env, envErr := dbGetEnvironment(ctx.AppDB(), rel.EnvironmentID)
		if envErr != nil {
			response["store_sync_error"] = envErr.Error()
			return response, nil
		}
		if env != nil {
			d = effectiveDeploymentForEnvironment(base, env)
		}
	}
	storeConfig, observed, storeErr := a.observeStoreConfig(d)
	if storeErr != nil {
		response["store_sync_error"] = storeErr.Error()
	} else {
		response["store_config"] = storeConfig
		response["store_observed"] = observed
	}
	return response, nil
}
