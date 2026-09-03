package main

import (
	"context"
	"database/sql"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

var errWorkloadNotFound = errors.New("workload not found")

type ownerIdentity struct {
	InstallID int64
	AppName   string
	ProjectID string
}

func ownerFromCaller(callCtx context.Context, app *sdk.AppCtx) ownerIdentity {
	owner := ownerIdentity{}
	if caller := sdk.CallerFrom(callCtx); caller != nil {
		owner.InstallID = caller.AppInstallID
		owner.AppName = caller.AppName
		owner.ProjectID = caller.ProjectID
	}
	if owner.ProjectID == "" && app != nil {
		owner.ProjectID = app.CurrentProject()
	}
	return owner
}

func requireAppOwner(callCtx context.Context, app *sdk.AppCtx) (ownerIdentity, error) {
	owner := ownerFromCaller(callCtx, app)
	if owner.InstallID <= 0 || owner.AppName == "" {
		return ownerIdentity{}, errors.New("authenticated app caller required")
	}
	return owner, nil
}

func ownerCanAccess(owner ownerIdentity, workload *Workload) bool {
	if workload == nil {
		return false
	}
	if workload.ProjectID != "" && owner.ProjectID != "" && workload.ProjectID != owner.ProjectID {
		return false
	}
	if workload.OwnerAppInstallID > 0 {
		return owner.InstallID > 0 && owner.InstallID == workload.OwnerAppInstallID
	}
	// Legacy and manually-created workloads remain available to direct agent
	// calls and the authenticated Containers UI, but never to sibling apps.
	return owner.InstallID == 0
}

func requireOwnedWorkload(db *sql.DB, id string, owner ownerIdentity) (*Workload, error) {
	w, err := getWorkload(db, id)
	if err != nil {
		return nil, err
	}
	if !ownerCanAccess(owner, w) {
		return nil, errWorkloadNotFound
	}
	return w, nil
}
