package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolMediaJobGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := strArg(args, "project_id", strArg(args, "_project_id", projectScopeFromArgs(ctx, args)))
	if pid == "" || (ctx.CurrentProject() != "" && ctx.CurrentProject() != pid) {
		return nil, errors.New("valid project scope required")
	}
	var id, sid, gid int64
	var status, msg string
	var cost float64
	err := ctx.AppDB().QueryRow(`SELECT id,status,error,result_storage_id,generation_id,cost_usd FROM video_jobs WHERE id=? AND project_id=?`, intArg(args, "job_id", 0), pid).Scan(&id, &status, &msg, &sid, &gid, &cost)
	if err != nil {
		return nil, errors.New("generation job not found in project")
	}
	out := map[string]any{"job_id": id, "status": status, "error": msg, "result_storage_id": sid, "generation_id": gid, "cost_usd": nil}
	if cost > 0 {
		out["cost_usd"] = cost
	}
	return out, nil
}
