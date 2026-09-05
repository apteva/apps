package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
)

func scopedBuild(db *sql.DB, project string, id int64) (*Build, error) {
	b, err := dbGetBuild(db, id)
	if err != nil || b == nil {
		return nil, errors.New("build not found")
	}
	d, err := dbGetDeployment(db, project, b.DeploymentID)
	if err != nil || d == nil {
		return nil, errors.New("build not found")
	}
	return b, nil
}
func scopedRelease(db *sql.DB, project string, id int64) (*Release, error) {
	r, err := dbGetRelease(db, id)
	if err != nil || r == nil {
		return nil, errors.New("release not found")
	}
	d, err := dbGetDeployment(db, project, r.DeploymentID)
	if err != nil || d == nil {
		return nil, errors.New("release not found")
	}
	return r, nil
}
func releaseFromArgs(ctx *sdk.AppCtx, args map[string]any, id int64) (*Release, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return scopedRelease(ctx.AppDB(), project, id)
}
