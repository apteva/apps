package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Opt-in diagnostic, not a production load test or a wall-clock assertion.
func TestPerformance_LeaderboardReads(t *testing.T) {
	if os.Getenv("GAMES_PERF") != "1" {
		t.Skip("set GAMES_PERF=1 for large SQLite read diagnostics")
	}
	for _, n := range []int{1000, 100000, 1000000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newFixture(t)
			db := f.ctx.AppDB()
			if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < ?) INSERT INTO players(id,project_id,game_id,auth_user_id,display_name) SELECT n,?,?,n,'Player' FROM seq`, n, f.pid.ProjectID, f.pid.GameID); err != nil {
				t.Fatal(err)
			}
			lb, err := createLeaderboard(f.ctx, f.pid, "perf", "", "score", "desc", "none", 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO leaderboard_entries(project_id,game_id,leaderboard_id,period,player_id,score,updated_at) SELECT project_id,game_id,?,'all',id,id,'2026-09-06T00:00:00Z' FROM players`, lb.ID); err != nil {
				t.Fatal(err)
			}
			anchor, err := dbGetEntry(db, f.pid, lb.ID, "all", int64(n/2))
			if err != nil {
				t.Fatal(err)
			}
			cursor := makeEntryCursor(f.pid, lb.ID, "all", *anchor)
			cases := []struct {
				name string
				run  func() error
			}{
				{"top_10_only", func() error { _, err := dbTopEntries(db, f.pid, lb.ID, "all", "desc", 10, 0); return err }},
				{"v1_top_with_bottom_rank", func() error { _, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, 1); return err }},
				{"v2_top", func() error {
					_, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, 0, leaderboardReadOptions{OmitTotal: true})
					return err
				}},
				{"v2_cursor_midpoint", func() error {
					_, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, 0, leaderboardReadOptions{Cursor: cursor, OmitTotal: true})
					return err
				}},
				{"v2_around_bottom", func() error { _, err := leaderboardAround(f.ctx, f.pid, lb, "all", 1, 5, false, false); return err }},
			}
			for _, c := range cases {
				if err := c.run(); err != nil {
					t.Fatal(err)
				}
				const rounds = 20
				start := time.Now()
				for i := 0; i < rounds; i++ {
					if err := c.run(); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("rows=%d %s mean=%s (%d serial warm reads)", n, c.name, time.Since(start)/rounds, rounds)
			}
		})
	}
}
