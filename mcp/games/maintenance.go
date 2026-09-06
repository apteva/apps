package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
func checkActiveGame(db DBTX, scope GameScope) error {
	g, err := getGame(db, scope.ProjectID, scope.GameID)
	if err != nil {
		return err
	}
	if g.Status != "active" {
		return errors.New("game is archived")
	}
	return nil
}
func queueEvent(db DBTX, scope GameScope, topic string, payload map[string]any, analytics bool) error {
	payload["project_id"] = scope.ProjectID
	payload["game_id"] = scope.GameID
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO game_outbox(project_id,game_id,topic,payload,analytics,next_attempt,created_at) VALUES(?,?,?,?,?,?,?)`, scope.ProjectID, scope.GameID, topic, string(body), analytics, nowRFC(), nowRFC())
	return err
}
func emitGame(ctx *sdk.AppCtx, scope GameScope, topic string, payload map[string]any) {
	if err := queueEvent(ctx.AppDB(), scope, topic, payload, false); err != nil {
		ctx.Logger().Error("queue event failed", "error", err)
	}
}

var outboxMu sync.Mutex

// Delivery is bounded. Analytics failures retry with backoff, never on the
// gameplay request. SDK EmitWithProject has no acknowledgement return value.
type EventDelivery func(GameScope, string, map[string]any) error

func drainOutbox(ctx *sdk.AppCtx, delivery ...EventDelivery) error {
	if !outboxMu.TryLock() {
		return nil
	}
	defer outboxMu.Unlock()
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,game_id,topic,payload,analytics,attempts FROM game_outbox WHERE next_attempt<=? AND attempts<10 ORDER BY id LIMIT 100`, nowRFC())
	if err != nil {
		return err
	}
	type item struct {
		id                   int64
		p, g, topic, payload string
		analytics            bool
		attempts             int
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.p, &it.g, &it.topic, &it.payload, &it.analytics, &it.attempts); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		var payload map[string]any
		if err = json.Unmarshal([]byte(it.payload), &payload); err != nil {
			return err
		}
		payload["event_id"] = it.id
		if it.analytics {
			var out any
			err = ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", map[string]any{"_project_id": it.p, "event": it.topic, "app": "games", "user_id": fmt.Sprintf("game:%s:player:%v", it.g, payload["player_id"]), "props": payload}, &out)
		} else {
			if len(delivery) > 0 {
				err = delivery[0](GameScope{it.p, it.g}, it.topic, payload)
			} else {
				err = deliverEvent(GameScope{it.p, it.g}, it.topic, payload)
			}
		}
		if err != nil {
			delay := time.Duration(1<<min(it.attempts, 10)) * time.Second
			if _, e := ctx.AppDB().Exec(`UPDATE game_outbox SET attempts=attempts+1,next_attempt=?,last_error=? WHERE id=?`, time.Now().Add(delay).UTC().Format(time.RFC3339), err.Error(), it.id); e != nil {
				return e
			}
		} else if _, err = ctx.AppDB().Exec(`DELETE FROM game_outbox WHERE id=?`, it.id); err != nil {
			return err
		}
	}
	return nil
}
func maintainGames(ctx *sdk.AppCtx, project string, now time.Time) error {
	games, err := listGames(ctx.AppDB(), project)
	if err != nil {
		return err
	}
	for _, g := range games {
		scope := g.Scope()
		if g.Legacy {
			recoverLegacyBans(ctx, scope)
		}
		if g.Status == "active" {
			if err = rolloverLeaderboards(ctx, scope, now); err != nil {
				return err
			}
		}
		// Expiry does not require a player session or contact with Auth.
		tx, e := ctx.AppDB().Begin()
		if e != nil {
			return e
		}
		_, e = tx.Exec(`UPDATE player_bans SET lifted_at=? WHERE project_id=? AND game_id=? AND lifted_at IS NULL AND expires_at IS NOT NULL AND expires_at<=?`, nowRFC(), project, g.ID, now.UTC().Format(time.RFC3339))
		if e == nil {
			var rows *sql.Rows
			rows, e = tx.Query(`UPDATE players SET status='active' WHERE project_id=? AND game_id=? AND status='banned' AND NOT EXISTS(SELECT 1 FROM player_bans b WHERE b.project_id=players.project_id AND b.game_id=players.game_id AND b.player_id=players.id AND b.lifted_at IS NULL) RETURNING id`, project, g.ID)
			ids := []int64{}
			if e == nil {
				for rows.Next() {
					var id int64
					if e = rows.Scan(&id); e != nil {
						break
					}
					ids = append(ids, id)
				}
				if e == nil {
					e = rows.Err()
				}
				rows.Close()
			}
			if e == nil {
				for _, id := range ids {
					if e = dbAudit(tx, scope, id, "player.unbanned", "expiry", nil); e != nil {
						break
					}
					if e = queueEvent(tx, scope, "player.unbanned", map[string]any{"player_id": id, "source": "expiry"}, false); e != nil {
						break
					}
				}
			}
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	// Bounded retention: operation replay window is seven days; event delivery
	// failures remain inspectable; audit/history pruning is explicitly configured.
	for _, q := range []struct {
		sql  string
		days int
	}{
		{`DELETE FROM game_operations WHERE project_id=? AND created_at<?`, 7},

		{`DELETE FROM player_audit WHERE project_id=? AND occurred_at<?`, configDays(ctx, "audit_retention_days")},
		{`DELETE FROM leaderboard_entries WHERE project_id=? AND updated_at<? AND NOT EXISTS(SELECT 1 FROM leaderboards b WHERE b.id=leaderboard_entries.leaderboard_id AND b.current_period=leaderboard_entries.period)`, configDays(ctx, "history_retention_days")},
	} {
		if q.days == 0 {
			continue
		}
		if _, err = ctx.AppDB().Exec(q.sql, project, now.AddDate(0, 0, -q.days).UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if _, err = ctx.AppDB().Exec(`DELETE FROM game_login_tickets WHERE expires_at<?`, nowRFC()); err != nil {
		return err
	}
	if _, err = ctx.AppDB().Exec(`DELETE FROM game_limits WHERE window<?`, time.Now().Unix()/60-60); err != nil {
		return err
	}
	return nil
}

// Limits are keyed by authenticated player (or a hashed guest credential),
// not an untrusted forwarded IP. Each new window replaces the previous one.
func allowRequest(ctx *sdk.AppCtx, scope GameScope, bucket string, max int) bool {
	window := time.Now().Unix() / 60
	var n int
	err := ctx.AppDB().QueryRow(`INSERT INTO game_limits(project_id,game_id,bucket,window,count) VALUES(?,?,?,?,1) ON CONFLICT(project_id,game_id,bucket) DO UPDATE SET window=excluded.window,count=CASE WHEN game_limits.window=excluded.window THEN game_limits.count+1 ELSE 1 END RETURNING count`, scope.ProjectID, scope.GameID, bucket, window).Scan(&n)
	return err == nil && n <= max
}

func configDays(ctx *sdk.AppCtx, key string) int {
	n, _ := strconv.Atoi(cfgStr(ctx, key, "0"))
	if n < 0 {
		return 0
	}
	return n
}

var eventHTTPClient = &http.Client{Timeout: 5 * time.Second}

func deliverEvent(scope GameScope, topic string, payload map[string]any) error {
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gateway == "" {
		gateway = "http://127.0.0.1:5280"
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	data, err := json.Marshal(map[string]any{"topic": topic, "project_id": scope.ProjectID, "data": payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", gateway+"/api/app-events/internal/emit", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := eventHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("event delivery HTTP %d", resp.StatusCode)
	}
	return nil
}
