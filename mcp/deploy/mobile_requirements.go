package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"howett.net/plist"
)

const mobileFeatureIOSPushNotifications = "ios.push_notifications"

type mobileRequirementSnapshot struct {
	Platform string   `json:"platform"`
	Features []string `json:"features"`
	Hash     string   `json:"hash"`
}

func detectMobileRequirements(root string, d *Deployment) (mobileRequirementSnapshot, error) {
	if d == nil {
		return mobileRequirementSnapshot{}, fmt.Errorf("deployment required")
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return mobileRequirementSnapshot{}, err
	}
	platform := strings.ToLower(strings.TrimSpace(d.TargetKind))
	if platform == "" || platform == "service" {
		platform = strings.ToLower(strings.TrimSpace(d.Framework))
	}
	features := append([]string(nil), target.RequiredFeatures...)
	if platform == "ios" {
		detected, err := detectIOSRequiredFeatures(root)
		if err != nil {
			return mobileRequirementSnapshot{}, err
		}
		features = append(features, detected...)
	}
	features, err = normalizeMobileFeatures(platform, features)
	if err != nil {
		return mobileRequirementSnapshot{}, err
	}
	sum := sha256.Sum256([]byte(platform + "\n" + strings.Join(features, "\n")))
	return mobileRequirementSnapshot{
		Platform: platform,
		Features: features,
		Hash:     hex.EncodeToString(sum[:]),
	}, nil
}

func (a *App) inspectMobileRequirements(ctx context.Context, d *Deployment) (mobileRequirementSnapshot, error) {
	root, err := os.MkdirTemp("", "apteva-mobile-requirements-*")
	if err != nil {
		return mobileRequirementSnapshot{}, err
	}
	defer os.RemoveAll(root)
	if err := fetchSource(globalCtx, d, root, a.cfg); err != nil {
		return mobileRequirementSnapshot{}, fmt.Errorf("inspect mobile source: %w", err)
	}
	return detectMobileRequirements(root, d)
}

func normalizeMobileFeatures(platform string, features []string) ([]string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	seen := map[string]struct{}{}
	out := make([]string, 0, len(features))
	for _, feature := range features {
		feature = strings.ToLower(strings.TrimSpace(feature))
		if feature == "" {
			continue
		}
		if platform != "" && !strings.HasPrefix(feature, platform+".") {
			return nil, fmt.Errorf("mobile feature %q does not apply to %s", feature, platform)
		}
		if _, ok := seen[feature]; ok {
			continue
		}
		seen[feature] = struct{}{}
		out = append(out, feature)
	}
	sort.Strings(out)
	return out, nil
}

func detectIOSRequiredFeatures(root string) ([]string, error) {
	metadata, err := readIOSProjectMetadata(root)
	if err != nil {
		return nil, err
	}
	paths, err := resolveIOSProjectFiles(root, metadata.entitlementReferences, "CODE_SIGN_ENTITLEMENTS")
	if err != nil {
		return nil, err
	}
	referencedCount := len(paths)
	fallback, err := discoverIOSEntitlements(root, paths)
	if err != nil {
		return nil, err
	}
	paths = append(paths, fallback...)
	var referencedErrors []error
	for i, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var document map[string]any
		if _, err := plist.Unmarshal(body, &document); err != nil {
			if i < referencedCount {
				referencedErrors = append(referencedErrors, fmt.Errorf("%s: %w", relativeDisplayPath(root, path), err))
			}
			continue
		}
		if value, ok := document["aps-environment"]; ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
			return []string{mobileFeatureIOSPushNotifications}, nil
		}
	}
	if len(referencedErrors) > 0 {
		return nil, fmt.Errorf("parse referenced entitlements: %w", errors.Join(referencedErrors...))
	}
	return nil, nil
}

func discoverIOSEntitlements(root string, referenced []string) ([]string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, path := range referenced {
		seen[path] = struct{}{}
	}
	var paths []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if shouldSkipIOSGeneratedDir(info.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(info.Name()), ".entitlements") {
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if !pathWithinRoot(rootReal, real) {
			return nil
		}
		if _, ok := seen[real]; ok {
			return nil
		}
		seen[real] = struct{}{}
		paths = append(paths, real)
		return nil
	})
	return paths, err
}

func mobileFeaturesJSON(features []string) string {
	body, _ := json.Marshal(features)
	return string(body)
}

func mobileFeaturesFromJSON(raw string) []string {
	var features []string
	_ = json.Unmarshal([]byte(defaultStr(strings.TrimSpace(raw), "[]")), &features)
	return features
}

func mobileFeaturesContainAll(have, required []string) bool {
	set := map[string]struct{}{}
	for _, feature := range have {
		set[feature] = struct{}{}
	}
	for _, feature := range required {
		if _, ok := set[feature]; !ok {
			return false
		}
	}
	return true
}
