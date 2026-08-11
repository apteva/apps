package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
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
	Platform          string                       `json:"platform"`
	Provider          string                       `json:"provider"`
	Channel           string                       `json:"channel"`
	AppID             string                       `json:"app_id,omitempty"`
	PackageName       string                       `json:"package_name,omitempty"`
	GroupID           string                       `json:"group_id,omitempty"`
	GroupName         string                       `json:"group_name,omitempty"`
	Configured        bool                         `json:"configured"`
	Audience          []distributionAudienceMember `json:"audience"`
	Count             int                          `json:"count"`
	DesiredConfigured bool                         `json:"desired_configured"`
	DesiredAudience   []distributionAudienceMember `json:"desired_audience"`
	DesiredCount      int                          `json:"desired_count"`
	Synced            bool                         `json:"synced"`
	TesterAccess      string                       `json:"tester_access"`
	InstallURL        string                       `json:"install_url,omitempty"`
	InstallURLSource  string                       `json:"install_url_source,omitempty"`
	ConsoleURL        string                       `json:"console_url,omitempty"`
	LastSyncedAt      string                       `json:"last_synced_at,omitempty"`
}

type distributionTarget struct {
	Channel           string
	AppID             string
	PackageName       string
	BetaGroupID       string
	BetaGroupName     string
	DesiredConfigured bool
	DesiredAudience   []distributionAudienceMember
	InstallURL        string
	LastSyncedAt      string
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
	var state *mobileDistributionState
	switch d.TargetKind {
	case "ios":
		state, err = a.iosDistributionStatus(target)
	case "android":
		state, err = a.androidDistributionStatus(target)
	default:
		return nil, errors.New("distribution audiences apply only to Android and iOS deployments")
	}
	if err != nil {
		return nil, err
	}
	if err := a.persistDistributionObservation(d, state, ""); err != nil {
		return nil, err
	}
	return state, nil
}

func (a *App) updateMobileDistribution(d *Deployment, args map[string]any) (*mobileDistributionState, error) {
	target, err := a.resolveDistributionTarget(d, args)
	if err != nil {
		return nil, err
	}
	if _, hasAudience := args["audience"]; !hasAudience {
		if _, hasGroups := args["google_groups"]; !hasGroups {
			return nil, errors.New("audience or google_groups must be provided as the complete desired tester list")
		}
	}
	audience, err := distributionAudienceFromArgs(args)
	if err != nil {
		return nil, err
	}
	if d.TargetKind == "ios" && len(audience) == 0 {
		return nil, errors.New("an empty TestFlight audience cannot be reconciled safely with the available App Store Connect operations")
	}
	installURL := target.InstallURL
	if raw, ok := args["install_url"]; ok {
		installURL = strings.TrimSpace(fmt.Sprint(raw))
	}
	if err := validateTestingInstallURL(installURL); err != nil {
		return nil, err
	}
	if err := validateDistributionAudienceForPlatform(d.TargetKind, audience); err != nil {
		return nil, err
	}
	if err := a.persistDesiredDistribution(d, target.Channel, audience, installURL); err != nil {
		return nil, err
	}
	target.DesiredConfigured = true
	target.DesiredAudience = append([]distributionAudienceMember(nil), audience...)
	target.InstallURL = installURL
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
		_ = a.persistDistributionFailure(d, target.Channel, err)
		return nil, err
	}
	if err := a.persistDistributionObservation(d, state, ""); err != nil {
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
	if isProductionMobileChannel(d.TargetKind, channel) {
		return target, errors.New("production channels do not have a test audience")
	}
	target.Channel = channel
	if err := a.loadDesiredDistribution(d, &target); err != nil {
		return target, err
	}

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
		email := mapStringValue(raw, "email")
		if email == "" {
			email = mapStringValue(raw, "identifier")
		}
		audience = append(audience, distributionAudienceMember{
			Kind:      strings.ToLower(strings.TrimSpace(mapStringValue(raw, "kind"))),
			Email:     strings.ToLower(strings.TrimSpace(email)),
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

func validateDistributionAudienceForPlatform(platform string, audience []distributionAudienceMember) error {
	for _, member := range audience {
		switch platform {
		case "android":
			if member.Kind != "group" {
				return errors.New("Google Play's publishing API supports Google Group addresses only; use kind=group")
			}
		case "ios":
			if member.Kind != "individual" {
				return errors.New("App Store Connect audiences support individual tester emails; use kind=individual")
			}
		}
	}
	return nil
}

func normalizeStoreTesting(platform string, testing StoreTesting) (StoreTesting, error) {
	out := StoreTesting{Channels: map[string]StoreTestingChannel{}}
	for rawChannel, config := range testing.Channels {
		channel, err := normalizeMobileChannel(platform, rawChannel)
		if err != nil {
			return StoreTesting{}, err
		}
		if isProductionMobileChannel(platform, channel) {
			return StoreTesting{}, errors.New("production channels do not support tester configuration")
		}
		if err := validateTestingInstallURL(config.InstallURL); err != nil {
			return StoreTesting{}, err
		}
		audience := make([]distributionAudienceMember, 0, len(config.Audience))
		for _, member := range config.Audience {
			audience = append(audience, distributionAudienceMember{
				Kind: strings.ToLower(strings.TrimSpace(member.Kind)), Email: strings.ToLower(strings.TrimSpace(member.Identifier)),
				FirstName: strings.TrimSpace(member.FirstName), LastName: strings.TrimSpace(member.LastName),
			})
		}
		normalized, err := normalizeDistributionAudience(audience)
		if err != nil {
			return StoreTesting{}, err
		}
		if err := validateDistributionAudienceForPlatform(platform, normalized); err != nil {
			return StoreTesting{}, err
		}
		config.Audience = storeTestingAudience(normalized)
		config.InstallURL = strings.TrimSpace(config.InstallURL)
		out.Channels[channel] = config
	}
	return out, nil
}

func normalizeDistributionAudience(audience []distributionAudienceMember) ([]distributionAudienceMember, error) {
	args := map[string]any{"audience": make([]map[string]any, 0, len(audience))}
	for _, member := range audience {
		args["audience"] = append(args["audience"].([]map[string]any), map[string]any{
			"kind": member.Kind, "email": member.Email, "first_name": member.FirstName, "last_name": member.LastName,
		})
	}
	return distributionAudienceFromArgs(args)
}

func storeTestingAudience(audience []distributionAudienceMember) []StoreTestingAudience {
	out := make([]StoreTestingAudience, 0, len(audience))
	for _, member := range audience {
		out = append(out, StoreTestingAudience{
			Kind: member.Kind, Identifier: member.Email, FirstName: member.FirstName, LastName: member.LastName,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Identifier < out[j].Identifier
	})
	return out
}

func distributionAudienceFromStore(config StoreTestingChannel) []distributionAudienceMember {
	out := make([]distributionAudienceMember, 0, len(config.Audience))
	for _, member := range config.Audience {
		out = append(out, distributionAudienceMember{
			Kind: member.Kind, Email: member.Identifier, FirstName: member.FirstName, LastName: member.LastName,
		})
	}
	return out
}

func validateTestingInstallURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("testing install_url must be an absolute HTTPS URL")
	}
	return nil
}

func (a *App) loadDesiredDistribution(d *Deployment, target *distributionTarget) error {
	if d == nil || target == nil {
		return nil
	}
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return err
	}
	if channel, ok := doc.Testing.Channels[target.Channel]; ok {
		target.DesiredConfigured = true
		target.DesiredAudience = distributionAudienceFromStore(channel)
		target.InstallURL = channel.InstallURL
	}
	if cfg == nil || strings.TrimSpace(cfg.ObservedJSON) == "" {
		return nil
	}
	var observed struct {
		Testing struct {
			Channels map[string]struct {
				LastSyncedAt string `json:"last_synced_at"`
			} `json:"channels"`
		} `json:"testing"`
	}
	if json.Unmarshal([]byte(cfg.ObservedJSON), &observed) == nil {
		target.LastSyncedAt = observed.Testing.Channels[target.Channel].LastSyncedAt
	}
	return nil
}

func (a *App) persistDesiredDistribution(d *Deployment, channel string, audience []distributionAudienceMember, installURL string) error {
	_, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return err
	}
	if doc.Testing.Channels == nil {
		doc.Testing.Channels = map[string]StoreTestingChannel{}
	}
	doc.Testing.Channels[channel] = StoreTestingChannel{
		Audience: storeTestingAudience(audience), InstallURL: strings.TrimSpace(installURL),
	}
	_, err = dbUpsertMobileStoreConfig(globalCtx.AppDB(), d, doc)
	return err
}

func (a *App) persistDistributionObservation(d *Deployment, state *mobileDistributionState, lastError string) error {
	if d == nil || state == nil {
		return nil
	}
	cfg, err := dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil || cfg == nil {
		return err
	}
	observed := map[string]any{}
	_ = json.Unmarshal([]byte(defaultStr(cfg.ObservedJSON, "{}")), &observed)
	testing, _ := observed["testing"].(map[string]any)
	if testing == nil {
		testing = map[string]any{}
	}
	channels, _ := testing["channels"].(map[string]any)
	if channels == nil {
		channels = map[string]any{}
	}
	now := nowUTC()
	state.LastSyncedAt = now
	channels[state.Channel] = map[string]any{
		"audience": state.Audience, "count": state.Count, "synced": state.Synced,
		"tester_access": state.TesterAccess, "install_url": state.InstallURL,
		"last_synced_at": now, "last_error": lastError,
	}
	testing["channels"] = channels
	observed["testing"] = testing
	return dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, cfg.Status, mustJSON(observed), cfg.ValidationJSON, "", cfg.LastError)
}

func (a *App) persistDistributionFailure(d *Deployment, channel string, syncErr error) error {
	if d == nil || syncErr == nil {
		return nil
	}
	cfg, err := dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil || cfg == nil {
		return err
	}
	observed := map[string]any{}
	_ = json.Unmarshal([]byte(defaultStr(cfg.ObservedJSON, "{}")), &observed)
	testing, _ := observed["testing"].(map[string]any)
	if testing == nil {
		testing = map[string]any{}
	}
	channels, _ := testing["channels"].(map[string]any)
	if channels == nil {
		channels = map[string]any{}
	}
	entry, _ := channels[channel].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["synced"] = false
	entry["tester_access"] = "sync_error"
	entry["last_error"] = syncErr.Error()
	channels[channel] = entry
	testing["channels"] = channels
	observed["testing"] = testing
	return dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, cfg.Status, mustJSON(observed), cfg.ValidationJSON, "", cfg.LastError)
}

func finalizeDistributionState(state *mobileDistributionState, target distributionTarget) *mobileDistributionState {
	if state == nil {
		return nil
	}
	state.DesiredConfigured = target.DesiredConfigured
	state.DesiredAudience = append([]distributionAudienceMember(nil), target.DesiredAudience...)
	state.DesiredCount = len(state.DesiredAudience)
	state.InstallURL = target.InstallURL
	if state.InstallURL != "" {
		state.InstallURLSource = "manual"
	}
	state.LastSyncedAt = target.LastSyncedAt
	state.Synced = target.DesiredConfigured && distributionAudienceEqual(state.Audience, target.DesiredAudience)
	switch {
	case !target.DesiredConfigured || len(target.DesiredAudience) == 0:
		state.TesterAccess = "not_configured"
	case state.Synced:
		state.TesterAccess = "configured"
	default:
		state.TesterAccess = "sync_required"
	}
	if state.Platform == "android" {
		state.ConsoleURL = "https://play.google.com/console/"
	} else if state.Platform == "ios" {
		state.ConsoleURL = "https://appstoreconnect.apple.com/apps"
	}
	return state
}

func distributionAudienceEqual(left, right []distributionAudienceMember) bool {
	normalize := func(values []distributionAudienceMember) []string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, strings.ToLower(strings.TrimSpace(value.Kind))+"\x00"+strings.ToLower(strings.TrimSpace(value.Email)))
		}
		sort.Strings(out)
		return out
	}
	a, b := normalize(left), normalize(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		return finalizeDistributionState(state, target), nil
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
	return finalizeDistributionState(state, target), nil
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
	ID                   string
	Name                 string
	IsInternal           bool
	HasAccessToAllBuilds bool
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
	groups, err := listIOSBetaGroups(bound, target, groupName)
	if err != nil {
		return appleBetaGroup{}, err
	}
	group := selectAppleBetaGroup(groups, target.Channel, groupName)
	if group.ID != "" || !create {
		return group, nil
	}
	if groupName == "" {
		groupName = "Deploy " + upperFirst(target.Channel)
	}

	// Re-query by the stable group name immediately before creating. This makes
	// retries and concurrent publishers converge on the existing group.
	groups, err = listIOSBetaGroups(bound, target, groupName)
	if err != nil {
		return appleBetaGroup{}, err
	}
	if group = selectAppleBetaGroup(groups, target.Channel, groupName); group.ID != "" {
		return group, nil
	}

	raw, err := executeIntegration(bound, "create_beta_group", map[string]any{
		"app_id": target.AppID, "name": groupName, "isInternalGroup": target.Channel == "internal",
	})
	if err != nil {
		groups, lookupErr := listIOSBetaGroups(bound, target, groupName)
		if lookupErr == nil {
			if group = selectAppleBetaGroup(groups, target.Channel, groupName); group.ID != "" {
				return group, nil
			}
		}
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

func listIOSBetaGroups(bound *sdk.BoundIntegration, target distributionTarget, groupName string) ([]appleBetaGroup, error) {
	input := map[string]any{"app_id": target.AppID, "internal": target.Channel == "internal", "limit": 200}
	if groupName != "" {
		input["name"] = groupName
	}
	raw, err := executeIntegration(bound, "list_beta_groups", input)
	if err != nil {
		return nil, err
	}
	return parseAppleBetaGroups(raw), nil
}

func selectAppleBetaGroup(groups []appleBetaGroup, channel, groupName string) appleBetaGroup {
	wantInternal := channel == "internal"
	groupName = strings.TrimSpace(groupName)
	defaultName := "Deploy " + upperFirst(channel)
	matches := make([]appleBetaGroup, 0, len(groups))
	for _, group := range groups {
		if group.ID == "" || group.IsInternal != wantInternal {
			continue
		}
		if groupName != "" && !strings.EqualFold(group.Name, groupName) {
			continue
		}
		matches = append(matches, group)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].HasAccessToAllBuilds != matches[j].HasAccessToAllBuilds {
			return matches[i].HasAccessToAllBuilds
		}
		iDefault := strings.EqualFold(matches[i].Name, defaultName)
		jDefault := strings.EqualFold(matches[j].Name, defaultName)
		if iDefault != jDefault {
			return iDefault
		}
		return matches[i].ID < matches[j].ID
	})
	if len(matches) == 0 {
		return appleBetaGroup{}
	}
	return matches[0]
}

func parseAppleBetaGroup(raw json.RawMessage) appleBetaGroup {
	groups := parseAppleBetaGroups(raw)
	if len(groups) == 0 {
		return appleBetaGroup{}
	}
	return groups[0]
}

func parseAppleBetaGroups(raw json.RawMessage) []appleBetaGroup {
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	type betaGroupResource struct {
		ID         string `json:"id"`
		Attributes struct {
			Name                 string `json:"name"`
			IsInternal           bool   `json:"isInternalGroup"`
			HasAccessToAllBuilds bool   `json:"hasAccessToAllBuilds"`
		} `json:"attributes"`
	}
	var resources []betaGroupResource
	if len(payload.Data) > 0 && payload.Data[0] == '[' {
		if json.Unmarshal(payload.Data, &resources) != nil {
			return nil
		}
	} else {
		var one betaGroupResource
		if json.Unmarshal(payload.Data, &one) != nil {
			return nil
		}
		resources = append(resources, one)
	}
	groups := make([]appleBetaGroup, 0, len(resources))
	for _, resource := range resources {
		groups = append(groups, appleBetaGroup{
			ID:                   resource.ID,
			Name:                 resource.Attributes.Name,
			IsInternal:           resource.Attributes.IsInternal,
			HasAccessToAllBuilds: resource.Attributes.HasAccessToAllBuilds,
		})
	}
	return groups
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
	return readAndroidDistribution(bound, target)
}

func readAndroidDistribution(bound *sdk.BoundIntegration, target distributionTarget) (*mobileDistributionState, error) {
	edit, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": target.PackageName})
	if err != nil {
		return nil, fmt.Errorf("open Google Play edit for %s: %w", target.PackageName, err)
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
		return nil, fmt.Errorf("read Google Play testers for track %s: %w", target.Channel, err)
	}
	return androidDistributionState(target, parseGoogleGroups(raw)), nil
}

func (a *App) updateAndroidDistribution(target distributionTarget, audience []distributionAudienceMember) (*mobileDistributionState, error) {
	if err := validateDistributionAudienceForPlatform("android", audience); err != nil {
		return nil, err
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
	current := parseGoogleGroups(raw)
	groups := make([]string, 0, len(audience))
	for _, member := range audience {
		groups = append(groups, member.Email)
	}
	sort.Strings(groups)
	if stringSlicesEqual(current, groups) {
		return androidDistributionState(target, current), nil
	}
	if _, err := executeIntegration(bound, "update_track_testers", map[string]any{
		"packageName": target.PackageName, "editId": editID, "track": target.Channel, "googleGroups": groups,
	}); err != nil {
		return nil, fmt.Errorf("replace Google Play testers for track %s: %w", target.Channel, err)
	}
	if _, err := executeIntegration(bound, "validate_edit", map[string]any{"packageName": target.PackageName, "editId": editID}); err != nil {
		return nil, fmt.Errorf("validate Google Play tester edit: %w", err)
	}
	if _, err := executeIntegration(bound, "commit_edit", map[string]any{"packageName": target.PackageName, "editId": editID}); err != nil {
		return nil, fmt.Errorf("commit Google Play tester edit: %w", err)
	}
	committed = true
	state, err := readAndroidDistribution(bound, target)
	if err != nil {
		return nil, fmt.Errorf("verify committed Google Play testers: %w", err)
	}
	if !distributionAudienceEqual(state.Audience, audience) {
		return nil, errors.New("Google Play committed the tester edit but the track audience does not match the desired Google Groups")
	}
	return state, nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
		PackageName: target.PackageName, Configured: len(groups) > 0,
		Audience: make([]distributionAudienceMember, 0, len(groups)),
	}
	for _, group := range groups {
		state.Audience = append(state.Audience, distributionAudienceMember{Kind: "group", Email: group})
	}
	state.Count = len(state.Audience)
	return finalizeDistributionState(state, target)
}

func (a *App) applyConfiguredGoogleTestingToEdit(bound *sdk.BoundIntegration, d *Deployment, packageName, editID, channel string) (*mobileDistributionState, error) {
	target := distributionTarget{Channel: channel, PackageName: packageName}
	if isProductionMobileChannel("android", channel) {
		return finalizeDistributionState(&mobileDistributionState{
			Platform: "android", Provider: "google_play", Channel: channel, PackageName: packageName,
			Audience: []distributionAudienceMember{},
		}, target), nil
	}
	if err := a.loadDesiredDistribution(d, &target); err != nil {
		return nil, err
	}
	if !target.DesiredConfigured {
		return finalizeDistributionState(&mobileDistributionState{
			Platform: "android", Provider: "google_play", Channel: channel, PackageName: packageName,
			Audience: []distributionAudienceMember{},
		}, target), nil
	}
	if err := validateDistributionAudienceForPlatform("android", target.DesiredAudience); err != nil {
		return nil, err
	}
	raw, err := executeIntegration(bound, "get_track_testers", map[string]any{
		"packageName": packageName, "editId": editID, "track": channel,
	})
	if err != nil {
		return nil, fmt.Errorf("read Google Play testers for track %s: %w", channel, err)
	}
	current := parseGoogleGroups(raw)
	desired := make([]string, 0, len(target.DesiredAudience))
	for _, member := range target.DesiredAudience {
		desired = append(desired, member.Email)
	}
	sort.Strings(desired)
	if !stringSlicesEqual(current, desired) {
		if _, err := executeIntegration(bound, "update_track_testers", map[string]any{
			"packageName": packageName, "editId": editID, "track": channel, "googleGroups": desired,
		}); err != nil {
			return nil, fmt.Errorf("replace Google Play testers for track %s: %w", channel, err)
		}
	}
	return androidDistributionState(target, desired), nil
}

func (a *App) verifyConfiguredGoogleTesting(d *Deployment, packageName, channel string, pending *mobileDistributionState) (*mobileDistributionState, error) {
	if pending == nil || !pending.DesiredConfigured || isProductionMobileChannel("android", channel) {
		return pending, nil
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return nil, err
	}
	target := distributionTarget{Channel: channel, PackageName: packageName}
	if err := a.loadDesiredDistribution(d, &target); err != nil {
		return nil, err
	}
	state, err := readAndroidDistribution(bound, target)
	if err != nil {
		return nil, err
	}
	if !state.Synced {
		return nil, errors.New("Google Play release committed but tester access does not match the configured Google Groups")
	}
	if err := a.persistDistributionObservation(d, state, ""); err != nil {
		return nil, err
	}
	return state, nil
}

func (a *App) reconcileConfiguredGoogleTesting(d *Deployment, packageName, channel string) (*mobileDistributionState, error) {
	target := distributionTarget{Channel: channel, PackageName: packageName}
	if isProductionMobileChannel("android", channel) {
		return finalizeDistributionState(&mobileDistributionState{
			Platform: "android", Provider: "google_play", Channel: channel, PackageName: packageName,
			Audience: []distributionAudienceMember{},
		}, target), nil
	}
	if err := a.loadDesiredDistribution(d, &target); err != nil {
		return nil, err
	}
	if !target.DesiredConfigured {
		return finalizeDistributionState(&mobileDistributionState{
			Platform: "android", Provider: "google_play", Channel: channel, PackageName: packageName,
			Audience: []distributionAudienceMember{},
		}, target), nil
	}
	state, err := a.updateAndroidDistribution(target, target.DesiredAudience)
	if err != nil {
		_ = a.persistDistributionFailure(d, channel, err)
		return nil, err
	}
	if err := a.persistDistributionObservation(d, state, ""); err != nil {
		return nil, err
	}
	return state, nil
}

func setReleaseTesterAccess(meta *mobileReleaseMeta, state *mobileDistributionState) {
	if meta == nil || state == nil {
		return
	}
	meta.TesterAccess = state.TesterAccess
	meta.TesterCount = state.DesiredCount
	meta.TesterGroups = make([]string, 0, len(state.DesiredAudience))
	for _, member := range state.DesiredAudience {
		meta.TesterGroups = append(meta.TesterGroups, member.Email)
	}
	meta.InstallURL = state.InstallURL
	meta.TesterSyncedAt = state.LastSyncedAt
}

func (a *App) applyDesiredTesting(d *Deployment, doc StoreDocument) error {
	channels := make([]string, 0, len(doc.Testing.Channels))
	for channel := range doc.Testing.Channels {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		switch d.TargetKind {
		case "android":
			target, err := a.resolveDistributionTarget(d, map[string]any{"channel": channel})
			if err != nil {
				return err
			}
			if _, err := a.reconcileConfiguredGoogleTesting(d, target.PackageName, channel); err != nil {
				return err
			}
		case "ios":
			target, err := a.resolveDistributionTarget(d, map[string]any{"channel": channel})
			if err != nil {
				return err
			}
			if len(target.DesiredAudience) == 0 {
				return fmt.Errorf("TestFlight channel %s has an empty desired audience, which cannot be removed safely with the configured provider operations", channel)
			}
			state, err := a.updateIOSDistribution(target, target.DesiredAudience)
			if err != nil {
				return err
			}
			if err := a.persistDistributionObservation(d, state, ""); err != nil {
				return err
			}
		default:
			return fmt.Errorf("testing audiences are unsupported for %s", d.TargetKind)
		}
	}
	return nil
}

func (a *App) observeDesiredTesting(d *Deployment, doc StoreDocument) (map[string]any, error) {
	channels := make([]string, 0, len(doc.Testing.Channels))
	for channel := range doc.Testing.Channels {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	states := map[string]any{}
	for _, channel := range channels {
		target, err := a.resolveDistributionTarget(d, map[string]any{"channel": channel})
		if err != nil {
			return nil, err
		}
		var state *mobileDistributionState
		if d.TargetKind == "android" {
			state, err = a.androidDistributionStatus(target)
		} else {
			state, err = a.iosDistributionStatus(target)
		}
		if err != nil {
			return nil, err
		}
		state.LastSyncedAt = nowUTC()
		states[channel] = state
	}
	return map[string]any{"channels": states, "observed_at": nowUTC()}, nil
}
