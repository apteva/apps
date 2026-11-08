package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
)

type sqlStore interface {
	sqlWriter
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func (a *App) updateMetadata(id string, patch map[string]any) (Device, error) {
	a.mutationMu.Lock()
	d, err := a.updateMetadataLocked(id, patch)
	a.mutationMu.Unlock()
	if err == nil {
		a.wakeOutbox()
	}
	return d, err
}
func (a *App) updateMetadataLocked(id string, patch map[string]any) (Device, error) {
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	d, err := updateDeviceFields(tx, a.projectID, id, patch)
	if err != nil {
		return d, err
	}
	if err = a.event(tx, id, "devices.device.updated", map[string]any{"changed": "metadata"}); err != nil {
		return d, err
	}
	return d, tx.Commit()
}
func (a *App) updateCapabilities(id string, manifest map[string]any) (Device, error) {
	a.mutationMu.Lock()
	d, err := a.updateCapabilitiesLocked(id, manifest)
	a.mutationMu.Unlock()
	if err == nil {
		a.wakeOutbox()
	}
	return d, err
}
func (a *App) updateCapabilitiesLocked(id string, manifest map[string]any) (Device, error) {
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return d, err
	}
	if manifest == nil {
		return d, errors.New("manifest required")
	}
	if p := str(manifest, "protocol"); p != "" && p != d.Protocol {
		return d, errors.New("capabilities cannot change enrolled protocol")
	}
	manifest["protocol"] = d.Protocol
	if err = validateManifest(manifest); err != nil {
		return d, err
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if err = saveManifest(tx, a.projectID, id, manifest, nowText()); err != nil {
		return d, err
	}
	if err = a.event(tx, id, "devices.device.updated", map[string]any{"changed": "capabilities"}); err != nil {
		return d, err
	}
	d, err = getDevice(tx, a.projectID, id, true)
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}
func (a *App) connectionChanged(event sdk.Event, connected bool) error {
	if event.SourceApp != "mqtt" || event.ProjectID != a.projectID || str(event.Data, "scope_id") != a.projectID {
		return nil
	}
	a.mutationMu.Lock()
	err := a.connectionLocked(event, connected)
	a.mutationMu.Unlock()
	if err == nil {
		a.wakeOutbox()
	}
	return err
}
func (a *App) connectionLocked(event sdk.Event, connected bool) error {
	username := str(event.Data, "username")
	d, err := getDeviceByUsername(a.ctx.AppDB(), a.projectID, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !d.Enabled {
		return nil
	}
	clientID := str(event.Data, "client_id")
	if clientID == "" {
		return errors.New("MQTT connection event missing client id")
	}
	field := "disconnected_at"
	if connected {
		field = "connected_at"
	}
	at := str(event.Data, field)
	if at == "" {
		at = nowText()
	}
	at, err = parseQueryTime(at)
	if err != nil {
		return err
	}
	if connected {
		if d.ConnectedAt != nil && at < *d.ConnectedAt {
			return nil
		}
		if d.ConnectedAt != nil && at == *d.ConnectedAt && d.MQTTClientID == clientID {
			return nil
		}
	} else {
		if d.MQTTClientID != "" && d.MQTTClientID != clientID {
			return nil
		}
		if d.ConnectedAt != nil {
			if started := str(event.Data, "connected_at"); started != "" {
				normalized, e := parseQueryTime(started)
				if e != nil {
					return e
				}
				if normalized != *d.ConnectedAt {
					return nil
				}
			}
			if at < *d.ConnectedAt {
				return nil
			}
		}
		if d.MQTTClientID == "" && d.Status == "offline" {
			return nil
		}
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	topic := "devices.device.disconnected"
	status := "offline"
	if connected {
		topic = "devices.device.connected"
		status = "online"
		_, err = tx.Exec(`UPDATE devices SET status='online',availability='online',mqtt_client_id=?,connected_at=?,last_seen=?,updated_at=? WHERE id=? AND project_id=?`, clientID, at, at, at, d.ID, a.projectID)
	} else {
		_, err = tx.Exec(`UPDATE devices SET status='offline',availability='offline',mqtt_client_id='',disconnected_at=?,updated_at=? WHERE id=? AND project_id=?`, at, at, d.ID, a.projectID)
	}
	if err != nil {
		return err
	}
	if err = a.event(tx, d.ID, topic, map[string]any{"client_id": clientID, field: at}); err != nil {
		return err
	}
	if status != d.Status {
		if err = a.event(tx, d.ID, "devices.device.status_changed", map[string]any{"status": status}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
