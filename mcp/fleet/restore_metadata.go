package main

// These encrypted fields are part of the database snapshot's control state.
// They must move together with the restored database, never independently.
type restoreMetadata struct {
	APIKey         []byte `json:"api_key_enc"`
	SetupToken     []byte `json:"setup_token_enc"`
	SetupPassword  []byte `json:"setup_password_enc"`
	SetupPhase     string `json:"setup_phase"`
	SetupComplete  bool   `json:"setup_complete"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
}

func (s *store) backupMetadata(id string) (restoreMetadata, error) {
	var m restoreMetadata
	err := s.db.QueryRow(`SELECT t.api_key_enc,t.setup_token_enc,s.setup_password_enc,s.setup_phase,s.setup_complete,COALESCE(t.current_version,''),COALESCE(t.target_version,'') FROM fleet_tenants t JOIN fleet_tenant_state s ON s.tenant_id=t.id WHERE t.id=?`, id).Scan(&m.APIKey, &m.SetupToken, &m.SetupPassword, &m.SetupPhase, &m.SetupComplete, &m.CurrentVersion, &m.TargetVersion)
	return m, err
}
func (s *store) restoreMetadata(id string, m restoreMetadata) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE fleet_tenants SET api_key_enc=?,setup_token_enc=?,current_version=?,target_version=? WHERE id=?`, m.APIKey, m.SetupToken, m.CurrentVersion, m.TargetVersion, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE fleet_tenant_state SET setup_complete=?,setup_password_enc=?,setup_phase=? WHERE tenant_id=?`, m.SetupComplete, m.SetupPassword, m.SetupPhase, id); err != nil {
		return err
	}
	return tx.Commit()
}
