package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var retentionMu sync.Mutex

type retentionSummary struct {
	RetainRollbackBuilds int   `json:"retain_rollback_builds"`
	RetainedBuilds       int   `json:"retained_builds"`
	BuildsWithArtifacts  int   `json:"builds_with_artifacts"`
	ArtifactBytes        int64 `json:"artifact_bytes"`
	PrunableBuilds       int   `json:"prunable_builds"`
	OrphanBuildDirs      int   `json:"orphan_build_dirs"`
	ArtifactsPruned      int   `json:"artifacts_pruned,omitempty"`
	OrphanDirsPruned     int   `json:"orphan_dirs_pruned,omitempty"`
	BytesPruned          int64 `json:"bytes_pruned,omitempty"`
}

func (a *App) pruneBuildArtifactsAsync(reason string) {
	ctx := globalCtx
	if ctx == nil || ctx.AppDB() == nil {
		return
	}
	if !a.retentionQueued.CompareAndSwap(false, true) {
		return
	}
	db := ctx.AppDB()
	go func() {
		defer a.retentionQueued.Store(false)
		if _, err := a.pruneBuildArtifactsWithContext(db, reason, ctx); err != nil {
			ctx.Logger().Warn("build retention prune failed", "reason", reason, "err", err)
		}
	}()
}

func (a *App) pruneBuildArtifacts(db *sql.DB, reason string) (retentionSummary, error) {
	return a.pruneBuildArtifactsWithContext(db, reason, globalCtx)
}

func (a *App) pruneBuildArtifactsWithContext(db *sql.DB, reason string, eventCtx interface {
	Emit(string, any)
}) (retentionSummary, error) {
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()
	retentionMu.Lock()
	defer retentionMu.Unlock()

	summary, builds, keep, err := a.retentionSummaryLocked(db)
	if err != nil {
		return summary, err
	}
	buildsByID := make(map[int64]Build, len(builds))
	for _, b := range builds {
		buildsByID[b.ID] = b
		if keep[b.ID] || b.Status == "pending" || b.Status == "running" {
			continue
		}
		paths := a.prunableArtifactPaths(b)
		prunedThisBuild := false
		for _, p := range paths {
			if !a.isSafeBuildPath(p) || !pathExists(p) {
				continue
			}
			size, _ := dirSize(p)
			if err := os.RemoveAll(p); err != nil {
				return summary, fmt.Errorf("prune build %d %s: %w", b.ID, p, err)
			}
			summary.BytesPruned += size
			prunedThisBuild = true
		}
		if prunedThisBuild {
			summary.ArtifactsPruned++
			if b.ArtifactPath != "" {
				_ = dbUpdateBuild(db, b.ID, map[string]any{
					"artifact_path": "",
					"artifact_size": int64(0),
				})
			}
			if eventCtx != nil {
				eventCtx.Emit("deploy.build.pruned", map[string]any{
					"build_id": b.ID, "deployment_id": b.DeploymentID, "reason": reason,
				})
			}
		}
	}

	orphanDirs, err := a.orphanBuildDirs(buildsByID)
	if err != nil {
		return summary, err
	}
	for _, p := range orphanDirs {
		id, _ := strconv.ParseInt(filepath.Base(p), 10, 64)
		var present int
		if err := db.QueryRow(`SELECT COUNT(*) FROM builds WHERE id=?`, id).Scan(&present); err != nil || present > 0 {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		size, _ := dirSize(p)
		if err := os.RemoveAll(p); err != nil {
			return summary, fmt.Errorf("prune orphan build dir %s: %w", p, err)
		}
		summary.OrphanDirsPruned++
		summary.BytesPruned += size
	}
	return summary, nil
}

func (a *App) retentionStatus(db *sql.DB) (retentionSummary, error) {
	a.retentionCacheMu.Lock()
	defer a.retentionCacheMu.Unlock()
	if !a.retentionCacheAt.IsZero() && time.Since(a.retentionCacheAt) < time.Minute {
		return a.retentionCache, nil
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	retentionMu.Lock()
	defer retentionMu.Unlock()
	summary, _, _, err := a.retentionSummaryLocked(db)
	if err == nil {
		a.retentionCache = summary
		a.retentionCacheAt = time.Now()
	}
	return summary, err
}

func (a *App) retentionSummaryLocked(db *sql.DB) (retentionSummary, []Build, map[int64]bool, error) {
	builds, err := dbListAllBuilds(db)
	if err != nil {
		return retentionSummary{}, nil, nil, err
	}
	keep, err := retainedBuildIDs(db, a.retentionRollbackCount())
	if err != nil {
		return retentionSummary{}, nil, nil, err
	}
	summary := retentionSummary{
		RetainRollbackBuilds: a.retentionRollbackCount(),
		RetainedBuilds:       len(keep),
	}
	buildsByID := make(map[int64]Build, len(builds))
	for _, b := range builds {
		buildsByID[b.ID] = b
		if buildArtifactAvailable(&b) {
			summary.BuildsWithArtifacts++
			summary.ArtifactBytes += b.ArtifactSize
		}
		if !keep[b.ID] && b.Status != "pending" && b.Status != "running" && len(a.prunableArtifactPaths(b)) > 0 {
			summary.PrunableBuilds++
		}
	}
	orphanDirs, err := a.orphanBuildDirs(buildsByID)
	if err != nil {
		return summary, builds, keep, err
	}
	summary.OrphanBuildDirs = len(orphanDirs)
	return summary, builds, keep, nil
}

func (a *App) retentionRollbackCount() int {
	if a.retainRollbacks < 0 {
		return 0
	}
	return a.retainRollbacks
}

func retainedBuildIDs(db *sql.DB, rollbackCount int) (map[int64]bool, error) {
	keep := map[int64]bool{}
	rows, err := db.Query(`
		SELECT id FROM builds WHERE release_requested = 1
 UNION SELECT r.build_id FROM deployment_intents i JOIN releases r ON r.id=i.release_id WHERE i.desired_state='running'
 UNION
 SELECT r.build_id
		  FROM deployments d
		  JOIN releases r ON r.id = d.current_release_id
		 WHERE r.build_id > 0
		UNION
		SELECT r.build_id
		  FROM deployment_environments e
		  JOIN releases r ON r.id = e.current_release_id
		 WHERE r.build_id > 0
		UNION
		SELECT build_id
		  FROM releases
		 WHERE (
		       (status = 'starting' AND (provider = '' OR external_id = '' OR external_id LIKE 'uploaded-%'))
		       OR (status = 'live' AND provider = '')
		 ) AND build_id > 0
	`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		keep[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query(`SELECT id, deployment_id, COALESCE(environment_id,0) FROM builds WHERE status = 'succeeded' ORDER BY deployment_id, COALESCE(environment_id,0), id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	perEnvironment := map[string]int{}
	for rows.Next() {
		var id, deploymentID, environmentID int64
		if err := rows.Scan(&id, &deploymentID, &environmentID); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d/%d", deploymentID, environmentID)
		if perEnvironment[key] < rollbackCount {
			keep[id] = true
			perEnvironment[key]++
		}
	}
	return keep, rows.Err()
}

func dbListAllBuilds(db *sql.DB) ([]Build, error) {
	rows, err := db.Query(`SELECT ` + buildColumns + ` FROM builds ORDER BY deployment_id, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Build{}
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (a *App) prunableArtifactPaths(b Build) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if seen[p] || !a.isSafeBuildPath(p) || !pathExists(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(b.ArtifactPath)
	add(filepath.Join(a.buildDir(b.ID), "dist"))
	add(filepath.Join(a.buildDir(b.ID), "src"))
	add(filepath.Join(a.buildDir(b.ID), "source-capsule-src"))
	add(filepath.Join(a.buildDir(b.ID), sourceCapsuleFilename))
	add(filepath.Join(a.buildDir(b.ID), sourceCapsuleMetadata))
	return out
}

func (a *App) orphanBuildDirs(builds map[int64]Build) ([]string, error) {
	root := filepath.Join(a.dataDir, "builds")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := builds[id]; ok {
			continue
		}
		p := filepath.Join(root, e.Name())
		if a.isSafeBuildPath(p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (a *App) buildDir(buildID int64) string {
	return filepath.Join(a.dataDir, "builds", strconv.FormatInt(buildID, 10))
}

func (a *App) removeBuildDirs(builds []Build) {
	for _, b := range builds {
		p := a.buildDir(b.ID)
		if a.isSafeBuildPath(p) {
			_ = os.RemoveAll(p)
		}
	}
}

func (a *App) isSafeBuildPath(path string) bool {
	root := filepath.Clean(filepath.Join(a.dataDir, "builds"))
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return rel != "" && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func buildArtifactAvailable(b *Build) bool {
	if b == nil || strings.TrimSpace(b.ArtifactPath) == "" {
		return false
	}
	st, err := os.Stat(b.ArtifactPath)
	return err == nil && st.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
