package main

func exportEntries(db DBTX, scope GameScope, playerID int64) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT leaderboard_id,period,score,updated_at FROM leaderboard_entries WHERE project_id=? AND game_id=? AND player_id=? ORDER BY leaderboard_id,period`, scope.ProjectID, scope.GameID, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var board int64
		var period, updated string
		var score float64
		if err = rows.Scan(&board, &period, &score, &updated); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"leaderboard_id": board, "period": period, "score": score, "updated_at": updated})
	}
	return out, rows.Err()
}
