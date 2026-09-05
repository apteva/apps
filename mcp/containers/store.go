package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

func nowUTC() string     { return time.Now().UTC().Format(time.RFC3339) }
func nowUnixNano() int64 { return time.Now().UnixNano() }

func ensureLocalHost(db *sql.DB) error {
	now := nowUTC()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO containers_hosts (
			id, instance_id, name, kind, status, docker_available, endpoint,
			labels_json, capacity_json, last_probe_at, created_at, updated_at
		) VALUES (
			0, 0, 'localhost', 'local', 'unknown', 0, '',
			'{}', '{}', NULL, ?, ?
		)`, now, now)
	return err
}

func seedBlueprints(db *sql.DB) error {
	blueprints := []Blueprint{
		{
			Slug:        "apteva",
			Name:        "Apteva",
			Description: "One isolated Apteva server with persistent /data.",
			Spec: RunSpec{
				Image: "apteva:latest",
				Ports: []PortSpec{{ContainerPort: 5280, BindAddr: "127.0.0.1", Protocol: "tcp"}},
				Env: map[string]string{
					"PORT":              "5280",
					"APTEVA_BIND":       "0.0.0.0",
					"DB_PATH":           "/data/apteva.db",
					"DATA_DIR":          "/data",
					"APTEVA_APPS_CACHE": "/data/apps",
					"CORE_CMD":          "/usr/local/bin/apteva-core",
				},
				Volumes:       []VolumeSpec{{Name: "data", MountPath: "/data"}},
				HealthPath:    "/health",
				Resources:     ResourceSpec{MemoryMB: 1024, CPU: 1},
				RestartPolicy: "unless-stopped",
			},
		},
		{
			Slug:        "custom-image",
			Name:        "Custom Image",
			Description: "Run a single Docker image with explicit ports, env, and volumes.",
			Spec:        RunSpec{RestartPolicy: "unless-stopped", HealthPath: "/"},
		},
	}
	for _, bp := range blueprints {
		if _, err := db.Exec(`
			INSERT INTO containers_blueprints (slug, name, description, status, spec_json, created_at, updated_at)
			VALUES (?, ?, ?, 'active', ?, ?, ?)
			ON CONFLICT(slug) DO UPDATE SET
				name=excluded.name,
				description=excluded.description,
				spec_json=excluded.spec_json,
				updated_at=excluded.updated_at`,
			bp.Slug, bp.Name, bp.Description, encodeJSON(bp.Spec), nowUTC(), nowUTC()); err != nil {
			return err
		}
	}
	return nil
}

func scrubStoredSecrets(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, config_json, env_json FROM containers_workloads`)
	if err != nil {
		return err
	}
	type storedWorkload struct {
		id, configJSON, envJSON string
	}
	var stored []storedWorkload
	for rows.Next() {
		var item storedWorkload
		if err := rows.Scan(&item.id, &item.configJSON, &item.envJSON); err != nil {
			_ = rows.Close()
			return err
		}
		stored = append(stored, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range stored {
		var spec RunSpec
		configJSON := "{}"
		if json.Unmarshal([]byte(item.configJSON), &spec) == nil {
			configJSON = encodeJSON(sanitizeRunSpecForStorage(spec))
		}
		envJSON := encodeJSON(redactEnvironment(decodeMap(item.envJSON)))
		if _, err := db.Exec(`UPDATE containers_workloads SET config_json=?, env_json=? WHERE id=?`, configJSON, envJSON, item.id); err != nil {
			return err
		}
	}
	return nil
}

func listBlueprints(db *sql.DB) ([]Blueprint, error) {
	rows, err := db.Query(`SELECT slug, name, description, spec_json FROM containers_blueprints WHERE status='active' ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Blueprint
	for rows.Next() {
		var bp Blueprint
		var raw string
		if err := rows.Scan(&bp.Slug, &bp.Name, &bp.Description, &raw); err != nil {
			return nil, err
		}
		_ = jsonUnmarshal(raw, &bp.Spec)
		out = append(out, bp)
	}
	return out, rows.Err()
}

func getBlueprint(db *sql.DB, slug string) (*Blueprint, error) {
	var bp Blueprint
	var raw string
	err := db.QueryRow(`SELECT slug, name, description, spec_json FROM containers_blueprints WHERE slug=? AND status='active'`, slug).
		Scan(&bp.Slug, &bp.Name, &bp.Description, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = jsonUnmarshal(raw, &bp.Spec)
	return &bp, nil
}

func insertWorkload(db *sql.DB, w *Workload, ports []PortSpec, volumes []VolumeSpec) error {
	now := nowUTC()
	log.Printf("[containers] db insert workload begin workload_id=%s name=%q image=%q", w.ID, w.Name, w.Image)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO containers_workloads (
			id, name, blueprint_slug, host_id, instance_id, kind, image, status,
			desired_status, container_id, container_name, network_name, public_url,
			health_status, health_path, health_url, config_json, env_json,
			resources_json, restart_policy, last_error, owner_app_install_id,
			owner_app_name, project_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.BlueprintSlug, w.HostID, w.InstanceID, "container", w.Image, w.Status,
		w.DesiredStatus, w.ContainerID, w.ContainerName, w.NetworkName, w.PublicURL,
		w.HealthStatus, w.HealthPath, w.HealthURL, w.ConfigJSON, w.EnvJSON,
		w.ResourcesJSON, w.RestartPolicy, w.LastError, w.OwnerAppInstallID,
		w.OwnerAppName, w.ProjectID, now, now)
	if err != nil {
		log.Printf("[containers] db insert workload error workload_id=%s err=%q", w.ID, err.Error())
		return err
	}
	log.Printf("[containers] db insert workload ok workload_id=%s", w.ID)
	for _, p := range ports {
		log.Printf("[containers] db insert port begin workload_id=%s host_port=%d container_port=%d protocol=%s",
			w.ID, p.HostPort, p.ContainerPort, p.Protocol)
		if _, err := tx.ExecContext(ctx, `INSERT INTO containers_ports (workload_id, protocol, container_port, host_port, bind_addr) VALUES (?, ?, ?, ?, ?)`,
			w.ID, p.Protocol, p.ContainerPort, p.HostPort, p.BindAddr); err != nil {
			log.Printf("[containers] db insert port error workload_id=%s err=%q", w.ID, err.Error())
			return err
		}
	}
	for _, v := range volumes {
		log.Printf("[containers] db insert volume begin workload_id=%s volume=%q mount=%q", w.ID, v.DockerVolumeName, v.MountPath)
		if _, err := tx.ExecContext(ctx, `INSERT INTO containers_volumes (workload_id, name, docker_volume_name, mount_path) VALUES (?, ?, ?, ?)`,
			w.ID, v.Name, v.DockerVolumeName, v.MountPath); err != nil {
			log.Printf("[containers] db insert volume error workload_id=%s err=%q", w.ID, err.Error())
			return err
		}
	}
	log.Printf("[containers] db record created event begin workload_id=%s", w.ID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO containers_events (workload_id, kind, actor, payload_json) VALUES (?, 'created', 'tool', ?)`,
		w.ID, encodeJSON(map[string]any{"image": w.Image})); err != nil {
		return err
	}
	return tx.Commit()
}

func execDB(db *sql.DB, label, query string, args ...any) (sql.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		log.Printf("[containers] db exec error label=%q duration=%s err=%q ctx_err=%v",
			label, time.Since(start).Round(time.Millisecond), err.Error(), ctx.Err())
		return nil, err
	}
	log.Printf("[containers] db exec ok label=%q duration=%s", label, time.Since(start).Round(time.Millisecond))
	return res, nil
}

func updateWorkload(db *sql.DB, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := []string{
		"status", "desired_status", "container_id", "public_url", "health_status",
		"health_url", "last_health_at", "last_error", "updated_at",
	}
	set := []string{}
	args := []any{}
	for _, k := range allowed {
		if v, ok := fields[k]; ok {
			set = append(set, k+"=?")
			args = append(args, v)
		}
	}
	if len(set) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := db.Exec(`UPDATE containers_workloads SET `+strings.Join(set, ", ")+` WHERE id=?`, args...)
	return err
}

func getWorkload(db *sql.DB, id string) (*Workload, error) {
	w, err := getWorkloadBase(db, `WHERE id=?`, id)
	if err != nil || w == nil {
		return w, err
	}
	if err := hydrateWorkload(db, w); err != nil {
		return nil, err
	}
	return w, nil
}

func getWorkloadBase(db *sql.DB, where string, arg any) (*Workload, error) {
	q := `SELECT id, name, blueprint_slug, host_id, instance_id, kind, image, status,
		desired_status, container_id, container_name, network_name, public_url,
		health_status, health_path, health_url, config_json, env_json, resources_json,
		restart_policy, COALESCE(last_health_at,''), last_error,
		owner_app_install_id, owner_app_name, project_id, created_at, updated_at
		FROM containers_workloads ` + where
	var w Workload
	var envRaw, resRaw string
	err := db.QueryRow(q, arg).Scan(
		&w.ID, &w.Name, &w.BlueprintSlug, &w.HostID, &w.InstanceID, &w.Kind, &w.Image, &w.Status,
		&w.DesiredStatus, &w.ContainerID, &w.ContainerName, &w.NetworkName, &w.PublicURL,
		&w.HealthStatus, &w.HealthPath, &w.HealthURL, &w.ConfigJSON, &envRaw, &resRaw,
		&w.RestartPolicy, &w.LastHealthAt, &w.LastError,
		&w.OwnerAppInstallID, &w.OwnerAppName, &w.ProjectID, &w.CreatedAt, &w.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.EnvJSON = envRaw
	w.ResourcesJSON = resRaw
	w.Env = decodeMap(envRaw)
	w.EnvKeys = envKeys(w.Env)
	w.Resources = decodeResources(resRaw)
	loadWorkloadRuntimeOptions(&w)
	return &w, nil
}

func listWorkloads(db *sql.DB, status string) ([]*Workload, error) {
	q := `SELECT id, name, blueprint_slug, host_id, instance_id, kind, image, status,
		desired_status, container_id, container_name, network_name, public_url,
		health_status, health_path, health_url, config_json, env_json, resources_json,
		restart_policy, COALESCE(last_health_at,''), last_error,
		owner_app_install_id, owner_app_name, project_id, created_at, updated_at
		FROM containers_workloads`
	args := []any{}
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	} else {
		q += ` WHERE status != ?`
		args = append(args, StatusDestroyed)
	}
	q += ` ORDER BY created_at DESC`
	log.Printf("[containers] db list workloads begin status=%q", status)
	rows, err := db.Query(q, args...)
	if err != nil {
		log.Printf("[containers] db list workloads query error status=%q err=%q", status, err.Error())
		return nil, err
	}
	out := make([]*Workload, 0)
	for rows.Next() {
		w, err := scanWorkload(rows)
		if err != nil {
			_ = rows.Close()
			log.Printf("[containers] db list workloads scan error status=%q err=%q", status, err.Error())
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		log.Printf("[containers] db list workloads rows error status=%q err=%q", status, err.Error())
		return nil, err
	}
	if err := rows.Close(); err != nil {
		log.Printf("[containers] db list workloads close error status=%q err=%q", status, err.Error())
		return nil, err
	}
	if err := hydrateWorkloads(db, out); err != nil {
		return nil, err
	}
	log.Printf("[containers] db list workloads done count=%d status=%q", len(out), status)
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkload(scanner rowScanner) (*Workload, error) {
	var w Workload
	var envRaw, resRaw string
	if err := scanner.Scan(
		&w.ID, &w.Name, &w.BlueprintSlug, &w.HostID, &w.InstanceID, &w.Kind, &w.Image, &w.Status,
		&w.DesiredStatus, &w.ContainerID, &w.ContainerName, &w.NetworkName, &w.PublicURL,
		&w.HealthStatus, &w.HealthPath, &w.HealthURL, &w.ConfigJSON, &envRaw, &resRaw,
		&w.RestartPolicy, &w.LastHealthAt, &w.LastError,
		&w.OwnerAppInstallID, &w.OwnerAppName, &w.ProjectID, &w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	w.EnvJSON = envRaw
	w.ResourcesJSON = resRaw
	w.Env = decodeMap(envRaw)
	w.EnvKeys = envKeys(w.Env)
	w.Resources = decodeResources(resRaw)
	loadWorkloadRuntimeOptions(&w)
	return &w, nil
}

func loadWorkloadRuntimeOptions(w *Workload) {
	if w == nil {
		return
	}
	var storedSpec RunSpec
	_ = jsonUnmarshal(w.ConfigJSON, &storedSpec)
	w.Command = storedSpec.Command
	w.WorkingDirectory = storedSpec.WorkingDirectory
	w.User = storedSpec.User
}

func hydrateWorkloads(db *sql.DB, workloads []*Workload) error {
	byID := make(map[string]*Workload, len(workloads))
	for _, w := range workloads {
		byID[w.ID] = w
	}
	portRows, err := db.Query(`SELECT workload_id, protocol, container_port, host_port, bind_addr FROM containers_ports ORDER BY id`)
	if err != nil {
		return err
	}
	for portRows.Next() {
		var workloadID string
		var p PortSpec
		if err := portRows.Scan(&workloadID, &p.Protocol, &p.ContainerPort, &p.HostPort, &p.BindAddr); err != nil {
			_ = portRows.Close()
			return err
		}
		if w := byID[workloadID]; w != nil {
			w.Ports = append(w.Ports, p)
		}
	}
	if err := portRows.Err(); err != nil {
		_ = portRows.Close()
		return err
	}
	if err := portRows.Close(); err != nil {
		return err
	}
	volumeRows, err := db.Query(`SELECT workload_id, name, docker_volume_name, mount_path, size_bytes FROM containers_volumes ORDER BY id`)
	if err != nil {
		return err
	}
	for volumeRows.Next() {
		var workloadID string
		var v VolumeSpec
		if err := volumeRows.Scan(&workloadID, &v.Name, &v.DockerVolumeName, &v.MountPath, &v.SizeBytes); err != nil {
			_ = volumeRows.Close()
			return err
		}
		if w := byID[workloadID]; w != nil {
			w.Volumes = append(w.Volumes, v)
		}
	}
	if err := volumeRows.Err(); err != nil {
		_ = volumeRows.Close()
		return err
	}
	return volumeRows.Close()
}

func hydrateWorkload(db *sql.DB, w *Workload) error {
	portRows, err := db.Query(`SELECT protocol, container_port, host_port, bind_addr FROM containers_ports WHERE workload_id=? ORDER BY id`, w.ID)
	if err != nil {
		return err
	}
	for portRows.Next() {
		var p PortSpec
		if err := portRows.Scan(&p.Protocol, &p.ContainerPort, &p.HostPort, &p.BindAddr); err != nil {
			return err
		}
		w.Ports = append(w.Ports, p)
	}
	if err := portRows.Err(); err != nil {
		_ = portRows.Close()
		return err
	}
	if err := portRows.Close(); err != nil {
		return err
	}
	volRows, err := db.Query(`SELECT name, docker_volume_name, mount_path, size_bytes FROM containers_volumes WHERE workload_id=? ORDER BY id`, w.ID)
	if err != nil {
		return err
	}
	defer volRows.Close()
	for volRows.Next() {
		var v VolumeSpec
		if err := volRows.Scan(&v.Name, &v.DockerVolumeName, &v.MountPath, &v.SizeBytes); err != nil {
			return err
		}
		w.Volumes = append(w.Volumes, v)
	}
	return volRows.Err()
}

func updateVolumeSize(db *sql.DB, workloadID, volumeName string, sizeBytes int64) error {
	_, err := execDB(db, "update volume size", `UPDATE containers_volumes SET size_bytes=? WHERE workload_id=? AND name=?`, sizeBytes, workloadID, volumeName)
	return err
}

func deleteWorkloadRows(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM containers_ports WHERE workload_id=?`,
		`DELETE FROM containers_volumes WHERE workload_id=?`,
		`UPDATE containers_workloads
		 SET status='destroyed', desired_status='stopped', health_status='destroyed', last_error='', updated_at=?
		 WHERE id=?`,
	} {
		if strings.Contains(q, "updated_at=?") {
			if _, err := tx.Exec(q, nowUTC(), id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO containers_events (workload_id, kind, actor, payload_json) VALUES (?, 'destroyed', 'tool', '{}')`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func recordEvent(db *sql.DB, workloadID, kind, actor string, payload map[string]any) error {
	_, err := execDB(db, "insert event", `INSERT INTO containers_events (workload_id, kind, actor, payload_json) VALUES (?, ?, ?, ?)`,
		workloadID, kind, actor, encodeJSON(payload))
	return err
}

func jsonUnmarshal(raw string, dst any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), dst)
}
