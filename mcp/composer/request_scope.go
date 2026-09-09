package main

import (
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strings"
)

func requestAppCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	pid := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if pid == "" {
		pid = projectScope(globalCtx)
	}
	if pid == "" {
		pid = strings.TrimSpace(r.URL.Query().Get("project_id"))
	}
	return globalCtx.WithProject(pid)
}
