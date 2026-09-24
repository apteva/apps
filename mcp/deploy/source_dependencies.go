package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Code dependencies are independent, pinned Code repository snapshots. The
// app is placed under app/ and dependencies beside it, preserving ordinary
// sibling package paths in a capsule and on a remote runner.
type sourceDependency struct {
	Slug       string `json:"slug"`
	Path       string `json:"path"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Subdir     string `json:"subdir,omitempty"`
}

func sourceBuildSubdir(d *Deployment) (string, error) {
	if d == nil || d.SourceKind != "code" {
		return "", nil
	}
	var selection sourceSelection
	if err := decodeSourceSelection(d.SourceExtraJSON, &selection); err != nil {
		return "", err
	}
	if len(selection.Dependencies) == 0 {
		return "", nil
	}
	if len(selection.Dependencies) > 8 {
		return "", errors.New("Code source supports at most eight sibling dependencies")
	}
	seen := map[string]bool{"app": true}
	for _, dep := range selection.Dependencies {
		if strings.TrimSpace(dep.Slug) == "" {
			return "", errors.New("Code dependency slug is required")
		}
		if dep.Path == "" || dep.Path != filepath.Base(dep.Path) || dep.Path == "." || dep.Path == ".." || strings.HasPrefix(dep.Path, ".") || strings.ContainsAny(dep.Path, "/\\") {
			return "", fmt.Errorf("Code dependency path %q must be a simple sibling directory name", dep.Path)
		}
		if seen[dep.Path] {
			return "", fmt.Errorf("duplicate Code dependency path %q", dep.Path)
		}
		seen[dep.Path] = true
	}
	return "app", nil
}

func decodeSourceSelection(raw string, out *sourceSelection) error {
	if out == nil {
		return errors.New("source selection required")
	}
	if err := json.Unmarshal([]byte(defaultStr(raw, "{}")), out); err != nil {
		return fmt.Errorf("source_extra_json: %w", err)
	}
	return nil
}

func sourceBuildRoot(d *Deployment, root string) (string, error) {
	subdir, err := sourceBuildSubdir(d)
	if err != nil {
		return "", err
	}
	if subdir == "" {
		return root, nil
	}
	return filepath.Join(root, subdir), nil
}

func fetchCodeSourceWithDependencies(d *Deployment, dest string, cfg sourceConfig, platform sdk.PlatformClient) error {
	subdir, err := sourceBuildSubdir(d)
	if err != nil {
		return err
	}
	if subdir == "" {
		return (&codeFetcher{platform: platform, config: cfg}).Fetch(d, dest)
	}
	var selection sourceSelection
	if err := decodeSourceSelection(d.SourceExtraJSON, &selection); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	main := filepath.Join(dest, subdir)
	if err := (&codeFetcher{platform: platform, config: cfg}).Fetch(d, main); err != nil {
		return err
	}
	for _, dep := range selection.Dependencies {
		dependent := *d
		dependent.SourceRef = dep.Slug
		dependent.SourceExtraJSON = mustJSON(sourceSelection{SnapshotID: dep.SnapshotID, Subdir: dep.Subdir, MaxBytes: selection.MaxBytes, MaxExpandedBytes: selection.MaxExpandedBytes})
		depConfig := cfg
		if cfg.CacheDir != "" {
			depConfig.CacheDir = filepath.Join(cfg.CacheDir, "source-dependencies", dep.Path)
		}
		if err := (&codeFetcher{platform: platform, config: depConfig}).Fetch(&dependent, filepath.Join(dest, dep.Path)); err != nil {
			return fmt.Errorf("fetch Code dependency %s: %w", dep.Slug, err)
		}
	}
	return nil
}
