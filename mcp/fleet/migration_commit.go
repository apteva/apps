package main

import "time"

// Publish the new authoritative location and the stopped source in the same
// transaction. Even automatic cleanup has a durable retained-source record
// until deletion succeeds, so a controller crash never loses that directory.
func (s *store) commitMigration(id string, instance int64, base, dir string, source *RetainedSource) error {
	tenant, _, err := s.get(id)
	if err != nil {
		return err
	}
	tenant.InstanceID = instance
	tenant.BaseURL = base
	tenant.ConfigDir = dir
	if err = s.reserveManagementPort(tenant); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if source != nil {
		if _, err = tx.Exec(`INSERT INTO fleet_retained_sources(tenant_id,source_instance_id,source_config_dir,source_slug,created_at) VALUES(?,?,?,?,?)`, id, source.SourceInstanceID, source.SourceConfigDir, source.SourceSlug, time.Now().UTC()); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE fleet_tenants SET instance_id=?,base_url=?,config_dir=?,updated_at=? WHERE id=?`, instance, base, dir, time.Now().UTC(), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *store) releaseRetiredPorts(id string) error {
	t, _, err := s.get(id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM fleet_port_reservations WHERE tenant_id=? AND (instance_id!=? OR port!=?)`, id, t.InstanceID, portFromTenant(t)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM fleet_app_port_blocks WHERE tenant_id=? AND instance_id!=?`, id, t.InstanceID); err != nil {
		return err
	}
	return tx.Commit()
}
