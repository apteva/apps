package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// StandaloneVolume is a named Docker volume that belongs to a caller rather
// than to one workload. Any number of the caller's local workloads can mount
// it at once (read-write or read-only); destroying them leaves it in place.
type StandaloneVolume struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	DockerVolumeName  string            `json:"docker_volume_name"`
	HostID            int64             `json:"host_id"`
	ProjectID         string            `json:"project_id,omitempty"`
	OwnerAppInstallID int64             `json:"owner_app_install_id,omitempty"`
	OwnerAppName      string            `json:"owner_app_name,omitempty"`
	Labels            map[string]string `json:"labels"`
	SizeBytes         int64             `json:"size_bytes"`
	CreatedAt         string            `json:"created_at"`
	LastUsedAt        string            `json:"last_used_at,omitempty"`
	MountedBy         []string          `json:"mounted_by"`
}

const maxVolumeLabels = 32

var (
	errVolumeNotFound = errors.New("volume not found")
	labelKeyRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)
)

func normalizeLabels(raw map[string]string) (map[string]string, error) {
	if len(raw) > maxVolumeLabels {
		return nil, fmt.Errorf("labels supports at most %d entries", maxVolumeLabels)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if !labelKeyRE.MatchString(k) {
			return nil, fmt.Errorf("label key %q must be 1-63 letters, numbers, '.', '_', '/', or '-'", k)
		}
		if len(v) > 256 || strings.ContainsAny(v, "\x00\n\r") {
			return nil, fmt.Errorf("label %q value must be at most 256 characters on one line", k)
		}
		out[k] = v
	}
	return out, nil
}

func volumeOwnerCanAccess(owner ownerIdentity, v *StandaloneVolume) bool {
	if v == nil || v.ProjectID != owner.ProjectID {
		return false
	}
	if v.OwnerAppInstallID > 0 {
		return owner.InstallID > 0 && owner.InstallID == v.OwnerAppInstallID
	}
	return owner.InstallID == 0
}

const standaloneVolumeColumns = `id, name, docker_volume_name, host_id, project_id, owner_app_install_id, owner_app_name, labels_json, size_bytes, created_at, last_used_at`

func scanStandaloneVolume(scanner interface{ Scan(...any) error }) (*StandaloneVolume, error) {
	var v StandaloneVolume
	var labels string
	if err := scanner.Scan(&v.ID, &v.Name, &v.DockerVolumeName, &v.HostID, &v.ProjectID, &v.OwnerAppInstallID, &v.OwnerAppName, &labels, &v.SizeBytes, &v.CreatedAt, &v.LastUsedAt); err != nil {
		return nil, err
	}
	v.Labels = decodeMap(labels)
	return &v, nil
}

func getStandaloneVolume(db *sql.DB, id string) (*StandaloneVolume, error) {
	v, err := scanStandaloneVolume(db.QueryRow(`SELECT `+standaloneVolumeColumns+` FROM containers_standalone_volumes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func requireOwnedStandaloneVolume(db *sql.DB, id string, owner ownerIdentity) (*StandaloneVolume, error) {
	v, err := getStandaloneVolume(db, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !volumeOwnerCanAccess(owner, v) {
		return nil, errVolumeNotFound
	}
	return v, nil
}

// volumeMounts lists live workloads whose run spec mounts the volume.
func volumeMounts(db *sql.DB, volumeID string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT v.workload_id FROM containers_volumes v JOIN containers_workloads w ON w.id=v.workload_id WHERE v.volume_id=? AND w.status!='destroyed' ORDER BY v.workload_id`, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// standaloneVolumeForRun resolves volumes[].volume_id while the workload is
// being prepared (under admissionMu, which delete also holds).
func standaloneVolumeForRun(db *sql.DB, id string, owner ownerIdentity, targetID int64) (string, error) {
	v, err := requireOwnedStandaloneVolume(db, id, owner)
	if err != nil {
		return "", err
	}
	if targetID != 0 || v.HostID != 0 {
		return "", errors.New("standalone volumes are supported on local workloads only")
	}
	return v.DockerVolumeName, nil
}

func touchStandaloneVolumes(db *sql.DB, w *Workload) {
	if w == nil {
		return
	}
	now := nowUTC()
	for _, v := range w.Volumes {
		if v.VolumeID != "" {
			_, _ = db.Exec(`UPDATE containers_standalone_volumes SET last_used_at=? WHERE id=?`, now, v.VolumeID)
		}
	}
}

func newVolumeID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "vol_" + fmt.Sprintf("%016x", nowUnixNano())
	}
	return "vol_" + hex.EncodeToString(b[:])
}

func (a *App) createStandaloneVolume(ctx context.Context, app *sdk.AppCtx, db *sql.DB, owner ownerIdentity, name string, labels map[string]string) (*StandaloneVolume, bool, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !workloadNameRE.MatchString(name) {
		return nil, false, typedInputError(errors.New("name must be 1-63 chars: lowercase letters, numbers, dashes, starting with a letter/number"))
	}
	labels, err := normalizeLabels(labels)
	if err != nil {
		return nil, false, typedInputError(err)
	}
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	var existingID string
	err = db.QueryRow(`SELECT id FROM containers_standalone_volumes WHERE project_id=? AND owner_app_install_id=? AND name=?`, owner.ProjectID, owner.InstallID, name).Scan(&existingID)
	if err == nil {
		// Idempotent: creating the same name again returns the existing volume.
		v, err := a.describeStandaloneVolume(ctx, app, db, existingID, false)
		return v, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM containers_standalone_volumes`).Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= 1000 {
		return nil, false, fmt.Errorf("%w: standalone volume limit reached (1000)", errConflict)
	}
	id := newVolumeID()
	safe := dockerSafeName(name)
	if len(safe) > 30 {
		safe = safe[:30]
	}
	dockerName := "containers-vol-" + safe + "-" + strings.TrimPrefix(id, "vol_")[:8]
	backend, err := a.backendForTarget(app, 0)
	if err != nil {
		return nil, false, err
	}
	if _, err := backend.EnsureVolume(ctx, dockerName); err != nil {
		return nil, false, err
	}
	if _, err := db.Exec(`INSERT INTO containers_standalone_volumes (id, name, docker_volume_name, host_id, project_id, owner_app_install_id, owner_app_name, labels_json, created_at) VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		id, name, dockerName, owner.ProjectID, owner.InstallID, owner.AppName, encodeJSON(labels), nowUTC()); err != nil {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = backend.RemoveVolume(c, dockerName)
		cancel()
		return nil, false, err
	}
	v, err := a.describeStandaloneVolume(ctx, app, db, id, false)
	return v, false, err
}

func (a *App) describeStandaloneVolume(ctx context.Context, app *sdk.AppCtx, db *sql.DB, id string, includeSize bool) (*StandaloneVolume, error) {
	v, err := getStandaloneVolume(db, id)
	if err != nil || v == nil {
		if err == nil {
			err = errVolumeNotFound
		}
		return nil, err
	}
	if v.MountedBy, err = volumeMounts(db, v.ID); err != nil {
		return nil, err
	}
	if v.MountedBy == nil {
		v.MountedBy = []string{}
	}
	if includeSize {
		backend, err := a.backendForTarget(app, v.HostID)
		if err != nil {
			return nil, err
		}
		size, err := backend.VolumeUsage(ctx, v.DockerVolumeName)
		if err != nil {
			return nil, err
		}
		v.SizeBytes = size
		_, _ = db.Exec(`UPDATE containers_standalone_volumes SET size_bytes=? WHERE id=?`, size, v.ID)
	}
	return v, nil
}

func (a *App) listStandaloneVolumes(ctx context.Context, app *sdk.AppCtx, db *sql.DB, owner ownerIdentity, labels map[string]string, ownerAppName string, includeSize bool) ([]*StandaloneVolume, error) {
	q := `SELECT ` + standaloneVolumeColumns + ` FROM containers_standalone_volumes WHERE project_id=?`
	args := []any{owner.ProjectID}
	if owner.InstallID > 0 {
		q += ` AND owner_app_install_id=?`
		args = append(args, owner.InstallID)
	} else {
		q += ` AND owner_app_install_id=0`
	}
	if ownerAppName != "" {
		q += ` AND owner_app_name=?`
		args = append(args, ownerAppName)
	}
	rows, err := db.Query(q+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		v, err := scanStandaloneVolume(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		match := true
		for k, want := range labels {
			if got, ok := v.Labels[k]; !ok || got != want {
				match = false
				break
			}
		}
		if match {
			ids = append(ids, v.ID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]*StandaloneVolume, 0, len(ids))
	for _, id := range ids {
		v, err := a.describeStandaloneVolume(ctx, app, db, id, includeSize)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *App) deleteStandaloneVolume(ctx context.Context, app *sdk.AppCtx, db *sql.DB, owner ownerIdentity, id string) error {
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	v, err := requireOwnedStandaloneVolume(db, id, owner)
	if err != nil {
		return err
	}
	mounts, err := volumeMounts(db, v.ID)
	if err != nil {
		return err
	}
	if len(mounts) > 0 {
		return fmt.Errorf("%w: volume %s is mounted by workloads %s; destroy them first", errConflict, v.ID, strings.Join(mounts, ", "))
	}
	backend, err := a.backendForTarget(app, v.HostID)
	if err != nil {
		return err
	}
	// Containers Docker still knows about, running or stopped, also count.
	if sb, ok := backend.(sandboxRuntime); ok {
		out, err := sb.sandboxDocker()(ctx, "ps", "-a", "--filter", "volume="+v.DockerVolumeName, "--format", "{{.Names}}")
		if err != nil {
			return err
		}
		if names := strings.Fields(out); len(names) > 0 {
			sort.Strings(names)
			return fmt.Errorf("%w: volume %s is still attached to containers %s", errConflict, v.ID, strings.Join(names, ", "))
		}
	}
	if err := backend.RemoveVolume(ctx, v.DockerVolumeName); err != nil && !isDockerMissingResourceError(err, "volume") {
		return err
	}
	_, err = db.Exec(`DELETE FROM containers_standalone_volumes WHERE id=?`, v.ID)
	return err
}

// --- MCP tools ---

func labelsArg(args map[string]any) (map[string]string, error) {
	raw, ok := args["labels"]
	if !ok || raw == nil {
		return nil, nil
	}
	b, _ := json.Marshal(raw)
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, typedInputError(errors.New("labels must be an object of string values"))
	}
	return out, nil
}

func (a *App) toolVolumeCreate(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	labels, err := labelsArg(args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, time.Minute)
	defer cancel()
	v, existing, err := a.createStandaloneVolume(ctx, app, app.AppDB(), ownerFromCaller(callCtx, app), getStr(args, "name"), labels)
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": v, "existing": existing}, nil
}

func (a *App) toolVolumeList(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	labels, err := labelsArg(args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 2*time.Minute)
	defer cancel()
	vols, err := a.listStandaloneVolumes(ctx, app, app.AppDB(), ownerFromCaller(callCtx, app), labels, getStr(args, "owner_app_name"), boolArg(args, "include_size"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"volumes": vols, "count": len(vols)}, nil
}

func (a *App) toolVolumeDelete(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	ctx, cancel := context.WithTimeout(callCtx, time.Minute)
	defer cancel()
	id := getStr(args, "volume_id")
	if err := a.deleteStandaloneVolume(ctx, app, app.AppDB(), ownerFromCaller(callCtx, app), id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "volume_id": id}, nil
}

func volumeCreateSchema() map[string]any {
	return schemaObject(map[string]any{
		"name":   map[string]any{"type": "string", "description": "Lowercase letters, numbers, dashes. Creating an existing name returns that volume."},
		"labels": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	}, []string{"name"})
}

func volumeListSchema() map[string]any {
	return schemaObject(map[string]any{
		"labels":         map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Only volumes carrying all of these labels."},
		"owner_app_name": map[string]any{"type": "string"},
		"include_size":   map[string]any{"type": "boolean", "description": "Measure current disk usage of each volume."},
	}, nil)
}

func volumeDeleteSchema() map[string]any {
	return schemaObject(map[string]any{"volume_id": map[string]any{"type": "string"}}, []string{"volume_id"})
}
