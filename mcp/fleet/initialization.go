package main

// Reserve a new tenant and its lifecycle operation in one database commit.
// In particular, a clone is never visible as an unlocked, activatable row.
func (a *App) insertTenantForOperation(t *Tenant, key, token []byte, operation string, quarantine bool) (func(), error) {
	if t.ID == "" {
		t.ID = newID()
	}
	opID := newID()
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if a.operations == nil {
		a.operations = map[string]string{}
	}
	if err := a.store.insertInitialized(t, key, token, operation, opID, quarantine); err != nil {
		return nil, err
	}
	a.operations[t.ID] = operation
	return func() {
		a.opMu.Lock()
		defer a.opMu.Unlock()
		if a.operations[t.ID] == operation {
			_, _ = a.store.db.Exec(`DELETE FROM fleet_active_operations WHERE tenant_id=? AND id=? AND phase!='recovery_required'`, t.ID, opID)
			delete(a.operations, t.ID)
		}
	}, nil
}
