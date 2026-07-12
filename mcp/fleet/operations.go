package main

import (
	"fmt"
	"strings"
)

func (a *App) beginTenantOperation(tenantID, operation string) (func(), error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if a.operations == nil {
		a.operations = map[string]string{}
	}
	if active := a.operations[tenantID]; active != "" {
		return nil, fmt.Errorf("tenant already has an operation in progress: %s", active)
	}
	a.operations[tenantID] = operation
	return func() {
		a.opMu.Lock()
		if a.operations[tenantID] == operation {
			delete(a.operations, tenantID)
		}
		a.opMu.Unlock()
	}, nil
}

func (a *App) tenantOperation(tenantID string) string {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	return a.operations[tenantID]
}
