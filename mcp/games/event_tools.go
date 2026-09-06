package main

import sdk "github.com/apteva/app-sdk"

func retryGameEvents(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	game, err := getGame(ctx.AppDB(), project, stringArg(args, "game_id", ""))
	if err != nil {
		return nil, err
	}
	result, err := ctx.AppDB().Exec(`UPDATE game_outbox SET attempts=0,last_error='',next_attempt=? WHERE project_id=? AND game_id=? AND attempts>=10`, nowRFC(), project, game.ID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	return map[string]any{"retried": n}, err
}
