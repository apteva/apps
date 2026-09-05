package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type retentionCandidate struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	TenantID string `json:"tenant_id,omitempty"`
}

func (a *App) retentionPlan() ([]retentionCandidate, error) {
	tenants, err := a.store.list(map[string]string{})
	if err != nil {
		return nil, err
	}
	pinned := map[string]bool{}
	canPruneVersions := true
	candidates := []retentionCandidate{}
	for _, t := range tenants {
		pinned[t.CurrentVersion] = true
		pinned[t.TargetVersion] = true
		if hostedTenantExpectedRunning(t.Status) && t.CurrentVersion == "" {
			canPruneVersions = false
		}
		if a.tenantOperation(t.ID) != "" {
			canPruneVersions = false
			continue
		}
		if t.Kind != KindLocal || t.IsHosted() || validateLocalTenantDir(t.Slug, t.ConfigDir) != nil {
			continue
		}
		for _, pattern := range []string{t.ConfigDir + ".prerestore-*", t.ConfigDir + ".failedrestore-*"} {
			paths, _ := filepath.Glob(pattern)
			for _, path := range paths {
				if info, err := os.Lstat(path); err == nil && info.IsDir() && time.Since(info.ModTime()) > 30*24*time.Hour {
					candidates = append(candidates, retentionCandidate{path, "restore", t.ID})
				}
			}
		}
	}
	if canPruneVersions {
		entries, err := os.ReadDir(versionsRoot())
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || pinned[entry.Name()] || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if time.Since(info.ModTime()) > 30*24*time.Hour {
				candidates = append(candidates, retentionCandidate{filepath.Join(versionsRoot(), entry.Name()), "version", ""})
			}
		}
	}
	return candidates, nil
}

func (a *App) httpMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", 405)
		return
	}
	plan, err := a.retentionPlan()
	if err != nil {
		writeJSONErr(w, 500, err)
		return
	}
	var fs unix.Statfs_t
	statPath := localDataRoot()
	for {
		err = unix.Statfs(statPath, &fs)
		if err == nil {
			break
		}
		parent := filepath.Dir(statPath)
		if !os.IsNotExist(err) || parent == statPath {
			writeJSONErr(w, 500, err)
			return
		}
		statPath = parent
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"free_bytes": uint64(fs.Bavail) * uint64(fs.Bsize), "candidates": plan, "retention_days": 30, "note": "Restore directories are never deleted automatically. POST selected paths with confirm=true to remove them."})
		return
	}
	var req struct {
		Confirm bool     `json:"confirm"`
		Paths   []string `json:"paths"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || !req.Confirm {
		writeJSONErr(w, 400, fmt.Errorf("confirm=true and selected paths are required"))
		return
	}
	removed := []string{}
	for _, path := range req.Paths {
		candidate := retentionCandidate{}
		for _, item := range plan {
			if item.Path == path {
				candidate = item
				break
			}
		}
		if candidate.Path == "" {
			writeJSONErr(w, 400, fmt.Errorf("path is not an eligible retention candidate"))
			return
		}
		var done func()
		if candidate.Kind == "version" {
			done, err = lockResource(r.Context(), "local-version:"+filepath.Base(path))
		} else {
			done, err = a.beginTenantOperation(candidate.TenantID, "retention cleanup")
		}
		if err != nil {
			writeJSONErr(w, 409, err)
			return
		}
		// Recheck runtime pins after obtaining the version lock.
		if candidate.Kind == "version" {
			var count int
			err = a.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM fleet_tenants WHERE current_version=? OR target_version=?)+(SELECT COUNT(*) FROM fleet_active_operations)`, filepath.Base(path), filepath.Base(path)).Scan(&count)
			if err != nil || count > 0 {
				done()
				writeJSONErr(w, 409, fmt.Errorf("version became pinned"))
				return
			}
		}
		err = os.RemoveAll(path)
		done()
		if err != nil {
			writeJSONErr(w, 500, err)
			return
		}
		removed = append(removed, path)
	}
	writeJSON(w, 200, map[string]any{"removed": removed})
}
