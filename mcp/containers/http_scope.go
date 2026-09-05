package main

import (
	"errors"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func httpAppContext(r *http.Request) (*sdk.AppCtx, error) {
	if globalCtx == nil {
		return nil, errors.New("app unavailable")
	}
	project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	installed := globalCtx.CurrentProject()
	if installed != "" && project != "" && installed != project {
		return nil, errWorkloadNotFound
	}
	if project == "" {
		project = installed
	}
	return globalCtx.WithProject(project), nil
}

// The server proxy authenticates the viewer/editor role. HTTP is the operator
// surface for all workloads in that authorized project, including app-owned ones.
// It never grants access to another project or silently treats empty as wildcard.
func requireHTTPWorkload(app *sdk.AppCtx, id string) (*Workload, error) {
	w, err := getWorkload(app.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if w == nil || w.ProjectID != app.CurrentProject() {
		return nil, errWorkloadNotFound
	}
	return w, nil
}
