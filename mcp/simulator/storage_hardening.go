package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const storageRetention = 30 * 24 * time.Hour

func (a *App) validateArtifactPath(path, platform string) (string, error) {
	root, err := filepath.Abs(a.artifactsDir)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.Dir(rel) != "." {
		return "", fmt.Errorf("artifact_path must name a generated artifact under %s", root)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve artifact: %w", err)
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact_path resolves outside simulator artifact storage")
	}
	expectedResolved := filepath.Join(resolvedRoot, filepath.Base(target))
	if filepath.Clean(resolvedTarget) != filepath.Clean(expectedResolved) {
		return "", fmt.Errorf("artifact_path may not be a symlink")
	}
	base := filepath.Base(target)
	ext := filepath.Ext(base)
	digest := strings.TrimSuffix(base, ext)
	if len(digest) != 64 || !isLowerHex(digest) {
		return "", fmt.Errorf("artifact_path is not a content-addressed simulator artifact")
	}
	wantExt := ".apk"
	if platform == "ios" {
		wantExt = ".app"
	}
	if ext != wantExt {
		return "", fmt.Errorf("artifact %s does not match %s simulator", base, platform)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("artifact unavailable: %w", err)
	}
	if platform == "android" && !info.Mode().IsRegular() {
		return "", fmt.Errorf("android artifact must be a regular APK")
	}
	if platform == "ios" && !info.IsDir() {
		return "", fmt.Errorf("ios artifact must be an .app directory")
	}
	return target, nil
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// cleanupStorage prunes old, non-current run history and then removes only
// artifacts/logs that are both unreferenced and older than the retention
// window. The latest run for every sim and all active runs remain intact.
func (a *App) cleanupStorage(db *sql.DB) (err error) {
	if db == nil || a.artifactsDir == "" {
		return nil
	}
	a.cleanupMu.Lock()
	if !a.lastCleanup.IsZero() && time.Since(a.lastCleanup) < time.Hour {
		a.cleanupMu.Unlock()
		return nil
	}
	a.lastCleanup = time.Now()
	a.cleanupMu.Unlock()
	defer func() {
		if err != nil {
			a.cleanupMu.Lock()
			a.lastCleanup = time.Time{}
			a.cleanupMu.Unlock()
		}
	}()

	cutoff := time.Now().UTC().Add(-storageRetention)
	cutoffText := cutoff.Format(time.RFC3339)
	if _, err := db.Exec(`
		DELETE FROM sims
		WHERE status IN ('shutdown','crashed')
		  AND datetime(COALESCE(booted_at, created_at, CURRENT_TIMESTAMP)) < datetime(?)
	`, cutoffText); err != nil {
		return err
	}
	if _, err := db.Exec(`
		DELETE FROM sim_runs
		WHERE id NOT IN (SELECT MAX(id) FROM sim_runs GROUP BY sim_id)
		  AND status NOT IN ('building','installing','running')
		  AND datetime(COALESCE(stopped_at, started_at, CURRENT_TIMESTAMP)) < datetime(?)
	`, cutoffText); err != nil {
		return err
	}

	artifacts, logs, err := referencedRunFiles(db)
	if err != nil {
		return err
	}
	if err := removeOldUnreferenced(a.artifactsDir, artifacts, cutoff); err != nil {
		return err
	}
	if err := removeOldUnreferenced(a.simLogsDir, logs, cutoff); err != nil {
		return err
	}
	if a.bootLogsDir == "" {
		return nil
	}
	knownBootLogs, err := knownSimBootLogs(db)
	if err != nil {
		return err
	}
	return removeOldUnreferenced(a.bootLogsDir, knownBootLogs, cutoff)
}

func knownSimBootLogs(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT id FROM sims`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id+".log"] = struct{}{}
	}
	return known, rows.Err()
}

func referencedRunFiles(db *sql.DB) (map[string]struct{}, map[string]struct{}, error) {
	rows, err := db.Query(`SELECT artifact_path, log_path FROM sim_runs`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	artifacts := map[string]struct{}{}
	logs := map[string]struct{}{}
	for rows.Next() {
		var artifact, logPath string
		if err := rows.Scan(&artifact, &logPath); err != nil {
			return nil, nil, err
		}
		if artifact != "" {
			artifacts[filepath.Clean(artifact)] = struct{}{}
		}
		if logPath != "" {
			logs[filepath.Clean(logPath)] = struct{}{}
		}
	}
	return artifacts, logs, rows.Err()
}

func removeOldUnreferenced(dir string, referenced map[string]struct{}, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		_, keepAbsolute := referenced[filepath.Clean(path)]
		_, keepLog := referenced[filepath.Clean(entry.Name())]
		if keepAbsolute || keepLog {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}
