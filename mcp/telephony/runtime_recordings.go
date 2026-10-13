package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) recordingRoot() (string, error) {
	var file string
	if err := a.db().db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
		return "", err
	}
	if file == "" {
		return "", errors.New("persistent recording root unavailable")
	}
	return filepath.Join(filepath.Dir(file), "recordings"), nil
}
func (a *App) resolveRecordingPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	root, err := a.recordingRoot()
	if err != nil {
		return "", err
	}
	return relativeRecordingPath(root, path)
}
func relativeRecordingPath(root, path string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(clean, "/")
	if len(parts) != 2 || parts[0] != "recordings" || parts[1] == "." || parts[1] == ".." {
		return "", errors.New("invalid relative recording path")
	}
	return filepath.Join(root, parts[1]), nil
}

// Verify every retained original before activating migrated state. Never copy,
// delete, or silently substitute an unverified file while rewriting references.
type recordingPathChange struct{ id, path string }

func (a *App) managedRecordingPathChanges() ([]recordingPathChange, error) {
	if a.runtime == nil {
		return nil, nil
	}
	var count int
	if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM local_recording_files f JOIN recordings r ON r.id=f.recording_id WHERE r.deleted_at='' AND r.provider_deleted_at=''`).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	root, err := a.recordingRoot()
	if err != nil {
		return nil, err
	}
	rows, err := a.db().db.Query(`SELECT f.recording_id,f.path,r.size_bytes,r.provider_status FROM local_recording_files f JOIN recordings r ON r.id=f.recording_id WHERE r.deleted_at='' AND r.provider_deleted_at=''`)
	if err != nil {
		return nil, err
	}
	var changes []recordingPathChange
	for rows.Next() {
		var id, path, status string
		var size int64
		if err = rows.Scan(&id, &path, &size, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if filepath.IsAbs(path) && (filepath.Base(filepath.Dir(path)) != "recordings" || !strings.HasPrefix(filepath.Base(path), "call-") || filepath.Ext(path) != ".wav") {
			rows.Close()
			return nil, errors.New("unmapped local recording blocks managed activation")
		}
		candidate := filepath.Join(root, filepath.Base(path))
		if !filepath.IsAbs(path) {
			candidate, err = relativeRecordingPath(root, path)
			if err != nil {
				rows.Close()
				return nil, err
			}
		}
		info, e := os.Lstat(candidate)
		if e != nil || !info.Mode().IsRegular() || status == "completed" && size > 0 && info.Size() != size {
			rows.Close()
			return nil, errors.New("local recording missing or unverified after state transfer")
		}
		if filepath.IsAbs(path) {
			changes = append(changes, recordingPathChange{id, filepath.Join("recordings", filepath.Base(path))})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return changes, nil
}

func (a *App) relocateRecordingPaths() error {
	changes, err := a.managedRecordingPathChanges()
	if err != nil || len(changes) == 0 {
		return err
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err = tx.Exec(`UPDATE local_recording_files SET path=? WHERE recording_id=?`, c.path, c.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
