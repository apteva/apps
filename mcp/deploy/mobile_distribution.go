package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type distributionAudienceMember struct {
	Kind       string `json:"kind"`
	Email      string `json:"email"`
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	State      string `json:"state,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

type mobileDistributionState struct {
	Platform    string                       `json:"platform"`
	Provider    string                       `json:"provider"`
	Channel     string                       `json:"channel"`
	AppID       string                       `json:"app_id,omitempty"`
	PackageName string                       `json:"package_name,omitempty"`
	GroupID     string                       `json:"group_id,omitempty"`
	GroupName   string                       `json:"group_name,omitempty"`
	Configured  bool                         `json:"configured"`
	Audience    []distributionAudienceMember `json:"audience"`
	Count       int                          `json:"count"`
}

type distributionTarget struct {
	Channel       string
	AppID         string
	PackageName   string
	BetaGroupID   string
	BetaGroupName string
}

func (a *App) toolDistributionStatus(_ *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	return a.mobileDistributionStatus(d, args)
}

func (a *App) toolDistributionUpdate(_ *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := a.lookupDeployment(args)
	if err != nil {
		return nil, err
	}
	return a.updateMobileDistribution(d, args)
}

func (a *App) mobileDistributionStatus(d *Deployment, args map[string]any) (*mobileDistributionState, error) {
	target, err := a.resolveDistributionTarget(d, args)
	if err != nil {
		return nil, err
	}
	switch d.TargetKind {
	case "ios":
		return a.iosDistributionStatus(target)
	case "android":
		return a.androidDistributionStatus(target)
	default:
		return nil, errors.New("distribution audiences apply only to Android and iOS deployments")
	}
}

func (a *App) updateMobileDistribution(d *Deployment, args map[string]any) (*mobileDistributionState, error) {
	target, err := a.resolveDistributionTarget(d, args)
	if err != nil {
		return nil, err
	}
	audience, err := distributionAudienceFromArgs(args)
	if err != nil {
		return nil, err
	}
	if len(audience) == 0 {
		return nil, errors.New("audience must contain at least one tester or group")
	}
	var state *mobileDistributionState
	switch d.TargetKind {
	case "ios":
		state, err = a.updateIOSDistribution(target, audience)
	case "android":
		state, err = a.updateAndroidDistribution(target, audience)
	default:
		err = errors.New("distribution audiences apply only to Android and iOS deployments")
	}
	if err != nil {
		return nil, err
	}
	emit("deploy.distribution.updated", map[string]any{
		"deployment_id": d.ID, "environment": d.EnvironmentName,
		"provider": state.Provider, "channel": state.Channel, "count": state.Count,
	})
	return state, nil
}

func (a *App) resolveDistributionTarget(d *Deployment, args map[string]any) (distributionTarget, error) {
	if d == nil {
		return distributionTarget{}, errors.New("deployment required")
	}
	if d.TargetKind != "ios" && d.TargetKind != "android" {
		return distributionTarget{}, errors.New("distribution audiences apply only to Android and iOS deployments")
	}
	target := distributionTarget{
		Channel:       strArg(args, "channel"),
		BetaGroupID:   strArg(args, "beta_group_id"),
		BetaGroupName: strArg(args, "group_name"),
	}
	var releaseMeta mobileReleaseMeta
	if releaseID := int64(intArg(args, "release_id")); releaseID > 0 {
		rel, err := dbGetRelease(globalCtx.AppDB(), releaseID)
		if err != nil || rel == nil || rel.DeploymentID != d.ID || rel.Provider == "" {
			return target, fmt.Errorf("mobile release %d not found for deployment", releaseID)
		}
		if target.Channel == "" {
			target.Channel = rel.Channel
		}
		if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &releaseMeta); err != nil {
			return target, fmt.Errorf("release metadata: %w", err)
		}
		if target.BetaGroupID == "" {
			target.BetaGroupID = releaseMeta.BetaGroupID
		}
	}
	channel, err := normalizeMobileChannel(d.TargetKind, target.Channel)
	if err != nil {
		return target, err
	}
	if channel == "production" {
		return target, errors.New("production channels do not have a test audience")
	}
	target.Channel = channel

	cfg, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return target, err
	}
	if d.TargetKind == "android" {
		target.PackageName = firstNonEmpty(releaseMeta.PackageName, cfg.PackageName)
		if target.PackageName == "" {
			return target, errors.New("Android distribution requires target_config_json.package_name")
		}
		return target, nil
	}
	target.AppID = firstNonEmpty(releaseMeta.AppID, cfg.AppStoreAppID)
	if target.BetaGroupID == "" {
		target.BetaGroupID = cfg.BetaGroupID
	}
	if target.AppID == "" {
		bundleID := firstNonEmpty(releaseMeta.BundleID, cfg.BundleID)
		if bundleID == "" {
			return target, errors.New("iOS distribution requires app_store_app_id or bundle_id")
		}
		bound, err := boundIntegration("app_store")
		if err != nil {
			return target, err
		}
		apps, err := executeIntegration(bound, "list_apps", map[string]any{"bundle_id": bundleID, "limit": 2})
		if err != nil {
			return target, err
		}
		target.AppID = firstJSONAPIID(apps)
		if target.AppID == "" {
			return target, fmt.Errorf("App Store Connect has no app record for bundle id %s", bundleID)
		}
	}
	return target, nil
}

func distributionAudienceFromArgs(args map[string]any) ([]distributionAudienceMember, error) {
	var audience []distributionAudienceMember
	appendMember := func(raw map[string]any) {
		audience = append(audience, distributionAudienceMember{
			Kind:      strings.ToLower(strings.TrimSpace(mapStringValue(raw, "kind"))),
			Email:     strings.ToLower(strings.TrimSpace(mapStringValue(raw, "email"))),
			FirstName: strings.TrimSpace(mapStringValue(raw, "first_name")),
			LastName:  strings.TrimSpace(mapStringValue(raw, "last_name")),
		})
	}
	switch raw := args["audience"].(type) {
	case []any:
		for _, value := range raw {
			if item, ok := value.(map[string]any); ok {
				appendMember(item)
			}
		}
	case []map[string]any:
		for _, item := range raw {
			appendMember(item)
		}
	}
	for _, email := range stringSliceValue(args["tester_emails"]) {
		audience = append(audience, distributionAudienceMember{Kind: "individual", Email: strings.ToLower(strings.TrimSpace(email))})
	}
	for _, email := range stringSliceValue(args["google_groups"]) {
		audience = append(audience, distributionAudienceMember{Kind: "group", Email: strings.ToLower(strings.TrimSpace(email))})
	}
	seen := map[string]bool{}
	out := make([]distributionAudienceMember, 0, len(audience))
	for _, member := range audience {
		if member.Kind == "" {
			member.Kind = "individual"
		}
		if member.Kind == "tester" || member.Kind == "email" {
			member.Kind = "individual"
		}
		if member.Kind != "individual" && member.Kind != "group" {
			return nil, fmt.Errorf("audience kind %q must be individual or group", member.Kind)
		}
		if err := validateAudienceEmail(member.Email); err != nil {
			return nil, err
		}
		key := member.Kind + "\x00" + member.Email
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, member)
	}
	return out, nil
}

func mapStringValue(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func stringSliceValue(value any) []string {
	var out []string
	switch values := value.(type) {
	case []string:
		out = append(out, values...)
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func validateAudienceEmail(value string) error {
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) {
		return fmt.Errorf("invalid audience email %q", value)
	}
	return nil
}

func (a *App) iosDistributionStatus(target distributionTarget) (*mobileDistributionState, error) {
	bound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	group, err := a.findIOSBetaGroup(bound, target, false)
	if err != nil {
		return nil, err
	}
	state := &mobileDistributionState{
		Platform: "ios", Provider: "app_store_connect", Channel: target.Channel,
		AppID: target.AppID, Audience: []distributionAudienceMember{},
	}
	if group.ID == "" {
		return state, nil
	}
	state.Configured = true
	state.GroupID = group.ID
	state.GroupName = group.Name
	testers, err := executeIntegration(bound, "list_beta_testers", map[string]any{"group_id": group.ID, "limit": 200})
	if err != nil {
		return nil, err
	}
	state.Audience = parseAppleTesters(testers)
	state.Count = len(state.Audience)
	return state, nil
}

func (a *App) updateIOSDistribution(target distributionTarget, audience []distributionAudienceMember) (*mobileDistributionState, error) {
	for _, member := range audience {
		if member.Kind != "individual" {
			return nil, errors.New("App Store Connect audiences support individual tester emails; use kind=individual")
		}
	}
	bound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	group, err := a.findIOSBetaGroup(bound, target, true)
	if err != nil {
		return nil, err
	}
	currentRaw, err := executeIntegration(bound, "list_beta_testers", map[string]any{"group_id": group.ID, "limit": 200})
	if err != nil {
		return nil, err
	}
	current := parseAppleTesters(currentRaw)
	inGroup := map[string]bool{}
	for _, member := range current {
		inGroup[strings.ToLower(member.Email)] = true
	}
	for _, member := range audience {
		if inGroup[member.Email] {
			continue
		}
		foundRaw, err := executeIntegration(bound, "list_beta_testers", map[string]any{"email": member.Email, "limit": 2})
		if err != nil {
			return nil, err
		}
		found := parseAppleTesters(foundRaw)
		if len(found) > 0 {
			_, err = executeIntegration(bound, "add_beta_testers_to_beta_group", map[string]any{
				"group_id": group.ID,
				"body":     map[string]any{"data": []map[string]any{{"type": "betaTesters", "id": found[0].ExternalID}}},
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if target.Channel == "internal" && (member.FirstName == "" || member.LastName == "") {
			userRaw, userErr := executeIntegration(bound, "list_users", map[string]any{
				"username": member.Email, "visible_app_id": target.AppID, "limit": 2,
			})
			if userErr != nil {
				return nil, userErr
			}
			member.FirstName, member.LastName = firstAppleUserName(userRaw)
			if member.FirstName == "" || member.LastName == "" {
				return nil, fmt.Errorf("%s is not an App Store Connect user with access to this app; invite the user before adding them to an internal TestFlight group", member.Email)
			}
		}
		input := map[string]any{"email": member.Email, "group_ids": []string{group.ID}}
		if member.FirstName != "" {
			input["firstName"] = member.FirstName
		}
		if member.LastName != "" {
			input["lastName"] = member.LastName
		}
		if _, err := executeIntegration(bound, "create_beta_tester", input); err != nil {
			return nil, err
		}
	}
	target.BetaGroupID = group.ID
	return a.iosDistributionStatus(target)
}

type appleBetaGroup struct {
	ID         string
	Name       string
	IsInternal bool
}

func (a *App) findIOSBetaGroup(bound *sdk.BoundIntegration, target distributionTarget, create bool) (appleBetaGroup, error) {
	if target.BetaGroupID != "" {
		raw, err := executeIntegration(bound, "get_beta_group", map[string]any{"group_id": target.BetaGroupID})
		if err != nil {
			return appleBetaGroup{}, err
		}
		group := parseAppleBetaGroup(raw)
		if group.ID != "" && group.IsInternal != (target.Channel == "internal") {
			return appleBetaGroup{}, fmt.Errorf("TestFlight group %s does not match %s channel", group.ID, target.Channel)
		}
		return group, nil
	}
	groupName := strings.TrimSpace(target.BetaGroupName)
	input := map[string]any{"app_id": target.AppID, "internal": target.Channel == "internal", "limit": 200}
	if groupName != "" {
		input["name"] = groupName
	}
	raw, err := executeIntegration(bound, "list_beta_groups", input)
	if err != nil {
		return appleBetaGroup{}, err
	}
	group := parseAppleBetaGroup(raw)
	if group.ID != "" || !create {
		return group, nil
	}
	if groupName == "" {
		groupName = "Deploy " + upperFirst(target.Channel)
	}
	raw, err = executeIntegration(bound, "create_beta_group", map[string]any{
		"app_id": target.AppID, "name": groupName, "isInternalGroup": target.Channel == "internal",
	})
	if err != nil {
		return appleBetaGroup{}, err
	}
	group = parseAppleBetaGroup(raw)
	if group.ID == "" {
		return group, errors.New("App Store Connect create_beta_group response missing id")
	}
	if group.Name == "" {
		group.Name = groupName
	}
	return group, nil
}

func parseAppleBetaGroup(raw json.RawMessage) appleBetaGroup {
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return appleBetaGroup{}
	}
	var one struct {
		ID         string `json:"id"`
		Attributes struct {
			Name       string `json:"name"`
			IsInternal bool   `json:"isInternalGroup"`
		} `json:"attributes"`
	}
	if len(payload.Data) > 0 && payload.Data[0] == '[' {
		var many []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name       string `json:"name"`
				IsInternal bool   `json:"isInternalGroup"`
			} `json:"attributes"`
		}
		if json.Unmarshal(payload.Data, &many) == nil && len(many) > 0 {
			return appleBetaGroup{
				ID: many[0].ID, Name: many[0].Attributes.Name,
				IsInternal: many[0].Attributes.IsInternal,
			}
		}
		return appleBetaGroup{}
	}
	if json.Unmarshal(payload.Data, &one) != nil {
		return appleBetaGroup{}
	}
	return appleBetaGroup{ID: one.ID, Name: one.Attributes.Name, IsInternal: one.Attributes.IsInternal}
}

func parseAppleTesters(raw json.RawMessage) []distributionAudienceMember {
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Email     string `json:"email"`
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
				State     string `json:"state"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return []distributionAudienceMember{}
	}
	out := make([]distributionAudienceMember, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, distributionAudienceMember{
			Kind: "individual", Email: item.Attributes.Email,
			FirstName: item.Attributes.FirstName, LastName: item.Attributes.LastName,
			State: strings.ToLower(item.Attributes.State), ExternalID: item.ID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out
}

func firstAppleUserName(raw json.RawMessage) (string, string) {
	var payload struct {
		Data []struct {
			Attributes struct {
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil || len(payload.Data) == 0 {
		return "", ""
	}
	return payload.Data[0].Attributes.FirstName, payload.Data[0].Attributes.LastName
}

func (a *App) androidDistributionStatus(target distributionTarget) (*mobileDistributionState, error) {
	bound, err := boundIntegration("play_store")
	if err != nil {
		return nil, err
	}
	edit, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": target.PackageName})
	if err != nil {
		return nil, err
	}
	editID := jsonStringAt(edit, "id")
	if editID == "" {
		return nil, errors.New("Google Play create_edit response missing id")
	}
	defer func() {
		_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": target.PackageName, "editId": editID})
	}()
	raw, err := executeIntegration(bound, "get_track_testers", map[string]any{
		"packageName": target.PackageName, "editId": editID, "track": target.Channel,
	})
	if err != nil {
		return nil, err
	}
	return androidDistributionState(target, parseGoogleGroups(raw)), nil
}

func (a *App) updateAndroidDistribution(target distributionTarget, audience []distributionAudienceMember) (*mobileDistributionState, error) {
	for _, member := range audience {
		if member.Kind != "group" {
			return nil, errors.New("Google Play's publishing API supports Google Group addresses only; use kind=group")
		}
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return nil, err
	}
	edit, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": target.PackageName})
	if err != nil {
		return nil, err
	}
	editID := jsonStringAt(edit, "id")
	if editID == "" {
		return nil, errors.New("Google Play create_edit response missing id")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": target.PackageName, "editId": editID})
		}
	}()
	raw, err := executeIntegration(bound, "get_track_testers", map[string]any{
		"packageName": target.PackageName, "editId": editID, "track": target.Channel,
	})
	if err != nil {
		return nil, err
	}
	groups := parseGoogleGroups(raw)
	seen := map[string]bool{}
	for _, group := range groups {
		seen[strings.ToLower(group)] = true
	}
	changed := false
	for _, member := range audience {
		if !seen[member.Email] {
			groups = append(groups, member.Email)
			seen[member.Email] = true
			changed = true
		}
	}
	sort.Strings(groups)
	if !changed {
		return androidDistributionState(target, groups), nil
	}
	if _, err := executeIntegration(bound, "update_track_testers", map[string]any{
		"packageName": target.PackageName, "editId": editID, "track": target.Channel, "googleGroups": groups,
	}); err != nil {
		return nil, err
	}
	if _, err := executeIntegration(bound, "commit_edit", map[string]any{"packageName": target.PackageName, "editId": editID}); err != nil {
		return nil, err
	}
	committed = true
	return androidDistributionState(target, groups), nil
}

func parseGoogleGroups(raw json.RawMessage) []string {
	var payload struct {
		GoogleGroups []string `json:"googleGroups"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return []string{}
	}
	for i := range payload.GoogleGroups {
		payload.GoogleGroups[i] = strings.ToLower(strings.TrimSpace(payload.GoogleGroups[i]))
	}
	sort.Strings(payload.GoogleGroups)
	return payload.GoogleGroups
}

func androidDistributionState(target distributionTarget, groups []string) *mobileDistributionState {
	state := &mobileDistributionState{
		Platform: "android", Provider: "google_play", Channel: target.Channel,
		PackageName: target.PackageName, Configured: true,
		Audience: make([]distributionAudienceMember, 0, len(groups)),
	}
	for _, group := range groups {
		state.Audience = append(state.Audience, distributionAudienceMember{Kind: "group", Email: group})
	}
	state.Count = len(state.Audience)
	return state
}
