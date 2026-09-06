package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
)

type entryCursor struct {
	Game   string  `json:"g"`
	Board  int64   `json:"b"`
	Period string  `json:"p"`
	Score  float64 `json:"s"`
	At     string  `json:"t"`
	Player int64   `json:"u"`
}

func makeEntryCursor(scope GameScope, board int64, period string, e LeaderboardEntry) string {
	b, _ := json.Marshal(entryCursor{scope.GameID, board, period, e.Score, e.UpdatedAt, e.PlayerID})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeEntryCursor(scope GameScope, board int64, period, raw string) (*LeaderboardEntry, error) {
	if len(raw) > 2048 {
		return nil, errors.New("invalid cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	var c entryCursor
	if json.Unmarshal(b, &c) != nil || c.Game != scope.GameID || c.Board != board || c.Period != period || math.IsNaN(c.Score) || math.IsInf(c.Score, 0) || c.Player <= 0 {
		return nil, errors.New("cursor does not belong to this game, board and period")
	}
	return &LeaderboardEntry{PlayerID: c.Player, Score: c.Score, UpdatedAt: c.At}, nil
}

// Neighbors are sought by the same total order used for rank. No deep OFFSET.
func seekEntries(db DBTX, scope GameScope, lb *Leaderboard, period string, anchor *LeaderboardEntry, before bool, limit int) ([]LeaderboardEntry, error) {
	op := "<"
	if lb.Sort == "asc" {
		op = ">"
	}
	tieOp := ">"
	sort := orderForSort(lb.Sort)
	if before {
		if op == "<" {
			op = ">"
		} else {
			op = "<"
		}
		tieOp = "<"
		if lb.Sort == "asc" {
			sort = "e.score DESC,e.updated_at DESC,e.player_id DESC"
		} else {
			sort = "e.score ASC,e.updated_at DESC,e.player_id DESC"
		}
	}
	// Separate score and tie ranges so SQLite can seek into each index range,
	// instead of scanning the board to evaluate an OR predicate.
	base := `SELECT e.player_id,e.score,e.updated_at FROM leaderboard_entries e WHERE e.project_id=? AND e.game_id=? AND e.leaderboard_id=? AND e.period=? AND `
	scoreQuery := base + `e.score ` + op + ` ? ORDER BY ` + sort + ` LIMIT ?`
	tieQuery := base + `e.score=? AND (e.updated_at,e.player_id) ` + tieOp + ` (?,?) ORDER BY ` + sort + ` LIMIT ?`
	query := `SELECT e.player_id,p.display_name,e.score,e.updated_at FROM (SELECT * FROM (` + scoreQuery + `) UNION ALL SELECT * FROM (` + tieQuery + `)) e JOIN players p ON p.id=e.player_id ORDER BY ` + sort + ` LIMIT ?`
	rows, err := db.Query(query, scope.ProjectID, scope.GameID, lb.ID, period, anchor.Score, limit, scope.ProjectID, scope.GameID, lb.ID, period, anchor.Score, anchor.UpdatedAt, anchor.PlayerID, limit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []LeaderboardEntry{}
	for rows.Next() {
		var e LeaderboardEntry
		if err = rows.Scan(&e.PlayerID, &e.DisplayName, &e.Score, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if before {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	return entries, nil
}
