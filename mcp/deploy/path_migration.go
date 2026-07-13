package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type pathRebaseSummary struct {
	BuildArtifacts int
	BuildLogs      int
	ReleaseLogs    int
}

func (s pathRebaseSummary) total() int {
	return s.BuildArtifacts + s.BuildLogs + s.ReleaseLogs
}

// rebaseLegacyPaths repairs absolute paths written before an app install's
// DataDir moved. Only Deploy's canonical, ID-scoped layouts are accepted so a
// manually configured or corrupt absolute path is never redirected.
func rebaseLegacyPaths(db *sql.DB, dataDir string) (summary pathRebaseSummary, err error) {
	if db == nil {
		return summary, fmt.Errorf("database required")
	}
	dataDir = filepath.Clean(dataDir)

	tx, err := db.Begin()
	if err != nil {
		return summary, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	type buildPaths struct {
		id       int64
		artifact string
		log      string
	}
	builds := []buildPaths{}
	rows, err := tx.Query(`SELECT id, artifact_path, log_path FROM builds WHERE artifact_path != '' OR log_path != ''`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var b buildPaths
		if err = rows.Scan(&b.id, &b.artifact, &b.log); err != nil {
			rows.Close()
			return summary, err
		}
		builds = append(builds, b)
	}
	if err = rows.Close(); err != nil {
		return summary, err
	}

	for _, b := range builds {
		artifact, artifactChanged := rebaseCanonicalPath(b.artifact, dataDir, "builds", b.id, "dist")
		logPath, logChanged := rebaseCanonicalPath(b.log, dataDir, "builds", b.id, "build.log")
		if !artifactChanged && !logChanged {
			continue
		}
		if _, err = tx.Exec(`UPDATE builds SET artifact_path = ?, log_path = ? WHERE id = ?`, artifact, logPath, b.id); err != nil {
			return summary, err
		}
		if artifactChanged {
			summary.BuildArtifacts++
		}
		if logChanged {
			summary.BuildLogs++
		}
	}

	type releasePath struct {
		id  int64
		log string
	}
	releases := []releasePath{}
	rows, err = tx.Query(`SELECT id, log_path FROM releases WHERE log_path != ''`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var r releasePath
		if err = rows.Scan(&r.id, &r.log); err != nil {
			rows.Close()
			return summary, err
		}
		releases = append(releases, r)
	}
	if err = rows.Close(); err != nil {
		return summary, err
	}

	for _, r := range releases {
		logPath, changed := rebaseCanonicalPath(r.log, dataDir, "releases", r.id, "runtime.log")
		if !changed {
			continue
		}
		if _, err = tx.Exec(`UPDATE releases SET log_path = ? WHERE id = ?`, logPath, r.id); err != nil {
			return summary, err
		}
		summary.ReleaseLogs++
	}

	err = tx.Commit()
	return summary, err
}

func rebaseCanonicalPath(stored, dataDir, area string, id int64, leaf string) (string, bool) {
	if strings.TrimSpace(stored) == "" || !filepath.IsAbs(stored) || id <= 0 {
		return stored, false
	}

	clean := filepath.Clean(stored)
	relative := filepath.Join(area, strconv.FormatInt(id, 10), leaf)
	current := filepath.Join(dataDir, relative)
	if clean == current {
		return stored, false
	}
	if !strings.HasSuffix(clean, string(filepath.Separator)+relative) {
		return stored, false
	}
	return current, true
}
