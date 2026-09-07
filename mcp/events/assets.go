package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Bundled assets make native binary installs self-contained.
//
//go:embed migrations/*.sql ui/*
var bundledAssets embed.FS

func prepareAssets() (func(), error) {
	root, err := os.MkdirTemp("", "apteva-events-assets-")
	if err != nil {
		return nil, err
	}
	err = fs.WalkDir(bundledAssets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(root, path), 0700)
		}
		data, err := bundledAssets.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, path), data, 0600)
	})
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	if os.Getenv("APTEVA_MIGRATIONS_DIR") == "" {
		os.Setenv("APTEVA_MIGRATIONS_DIR", filepath.Join(root, "migrations"))
	}
	if os.Getenv("APTEVA_UI_DIR") == "" {
		os.Setenv("APTEVA_UI_DIR", filepath.Join(root, "ui"))
	}
	return func() { os.RemoveAll(root) }, nil
}
