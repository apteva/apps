package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func testGame(t *testing.T, f *fixture, slug string) *Game {
	t.Helper()
	out, err := gameAction(f.ctx, "create", map[string]any{"slug": slug, "name": slug})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["game"].(*Game)
}
func gameLogin(t *testing.T, f *fixture, g *Game) (map[string]any, string) {
	t.Helper()
	rec := doReq(f.app.handleLoginDevice, "POST", "/v2/games/"+g.ID+"/login/device", map[string]any{}, map[string]string{"game_id": g.ID})
	if rec.Code != 201 {
		t.Fatalf("game login: %d %s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	return out, out["access_token"].(string)
}

func TestGames_IsolationAndLegacyClient(t *testing.T) {
	f := newFixture(t)
	old, oldToken := f.loginDevice(t, "legacy-device", "Legacy")
	a, b := testGame(t, f, "racer"), testGame(t, f, "puzzle")
	first, tokenA := gameLogin(t, f, a)
	second, tokenB := gameLogin(t, f, b)
	idA, idB := playerID(t, first), playerID(t, second)
	if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"name": "score", "aggregation": "sum"}); err == nil {
		t.Fatal("ambiguous tool should require game_id")
	}
	for _, g := range []*Game{a, b} {
		if _, err := f.app.toolStatsDefine(f.ctx, map[string]any{"game_id": g.ID, "name": "score", "aggregation": "sum"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"game_id": a.ID, "player_id": idA, "stat": "score", "value": 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.toolStatsUpdate(f.ctx, map[string]any{"game_id": b.ID, "player_id": idA, "stat": "score", "value": 99}); err == nil {
		t.Fatal("cross-game player ID accepted")
	}
	stats, err := dbGetPlayerStats(f.ctx.AppDB(), b.Scope(), idB)
	if err != nil || len(stats) != 0 {
		t.Fatalf("other game stats: %v %v", stats, err)
	}
	rec := doReq(f.app.handleMe, "GET", "/v2/games/"+b.ID+"/me", nil, map[string]string{"game_id": b.ID}, bearer(tokenA)...)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-game token accepted: %d", rec.Code)
	}
	rec = doReq(f.app.handleMe, "GET", "/v2/games/"+b.ID+"/me", nil, map[string]string{"game_id": b.ID}, bearer(tokenB)...)
	if rec.Code != 200 {
		t.Fatalf("own token rejected: %s", rec.Body.String())
	}
	rec = doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(oldToken)...)
	if rec.Code != 200 || playerID(t, decode(t, rec)) != playerID(t, old) {
		t.Fatal("v1 moved after new games were created")
	}
	rec = doReq(f.app.handleMe, "GET", "/v1/me?game_id="+b.ID, nil, nil, bearer(oldToken)...)
	if rec.Code == 200 {
		t.Fatal("v1 query overrode legacy binding")
	}
	if _, err := f.ctx.AppDB().Exec(`INSERT INTO player_data(project_id,game_id,player_id,key,value,visibility) VALUES(?,?,?,?,?,?)`, a.ProjectID, b.ID, idA, "bad", "1", "private"); err == nil {
		t.Fatal("database accepted mismatched game/player foreign key")
	}
	if _, err := selectGame(f.ctx, "other-project", a.ID, false); err == nil {
		t.Fatal("game escaped project boundary")
	}
	if _, err := gameAction(f.ctx, "archive", map[string]any{"game_id": a.ID}); err != nil {
		t.Fatal(err)
	}
	rec = doReq(f.app.handleMe, "GET", "/v2/games/"+a.ID+"/me", nil, map[string]string{"game_id": a.ID}, bearer(tokenA)...)
	if rec.Code == 200 {
		t.Fatal("archived game accessible")
	}
	if _, err := gameAction(f.ctx, "restore", map[string]any{"game_id": a.ID}); err != nil {
		t.Fatal(err)
	}
	rec = doReq(f.app.handleMe, "GET", "/v2/games/"+a.ID+"/me", nil, map[string]string{"game_id": a.ID}, bearer(tokenA)...)
	if rec.Code != 200 {
		t.Fatal("restored game inaccessible")
	}
}

func TestGames_SharedAccountModerationAndErasure(t *testing.T) {
	f := newFixture(t)
	a, b := testGame(t, f, "one"), testGame(t, f, "two")
	// Same Auth account can have separate memberships even if a studio opts
	// into shared identity later. Moderation must not touch the Auth account.
	idA, err := dbCreatePlayer(f.ctx.AppDB(), a.Scope(), 123, "One", "account")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := dbCreatePlayer(f.ctx.AppDB(), b.Scope(), 123, "Two", "account")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := dbGetPlayer(f.ctx.AppDB(), a.Scope(), idA)
	if _, err = banPlayer(f.ctx, a.Scope(), p, "test", "", "test"); err != nil {
		t.Fatal(err)
	}
	other, err := dbGetPlayer(f.ctx.AppDB(), b.Scope(), idB)
	if err != nil || other.Status != "active" || f.auth.callCount("auth.auth_users_disable") != 0 {
		t.Fatal("ban crossed game boundary")
	}
	if err = erasePlayer(f.ctx, a.Scope(), p, "test"); err != nil {
		t.Fatal(err)
	}
	if other, err = dbGetPlayer(f.ctx.AppDB(), b.Scope(), idB); err != nil || other == nil {
		t.Fatal("erasure crossed game boundary")
	}
	if f.auth.callCount("auth.auth_users_revoke_sessions") != 0 {
		t.Fatal("game erasure revoked shared sessions")
	}
}

func TestGames_CustomTicketsAndGuestCredentials(t *testing.T) {
	f := newFixture(t)
	a, b := testGame(t, f, "ticket-a"), testGame(t, f, "ticket-b")
	rec := doReq(f.app.handleLoginCustom, "POST", "/v2/games/"+a.ID+"/login/custom", map[string]any{"custom_id": "known-account"}, map[string]string{"game_id": a.ID})
	if rec.Code != 403 {
		t.Fatal("bare custom ID accepted")
	}
	out, err := createLoginTicket(f.ctx, map[string]any{"game_id": a.ID, "custom_id": "known-account"})
	if err != nil {
		t.Fatal(err)
	}
	ticket := out.(map[string]any)["login_ticket"].(string)
	rec = doReq(f.app.handleLoginCustom, "POST", "/v2/games/"+b.ID+"/login/custom", map[string]any{"login_ticket": ticket}, map[string]string{"game_id": b.ID})
	if rec.Code != 401 {
		t.Fatal("foreign ticket accepted")
	}
	rec = doReq(f.app.handleLoginCustom, "POST", "/v2/games/"+a.ID+"/login/custom", map[string]any{"login_ticket": ticket}, map[string]string{"game_id": a.ID})
	if rec.Code != 201 {
		t.Fatalf("ticket failed: %s", rec.Body.String())
	}
	rec = doReq(f.app.handleLoginCustom, "POST", "/v2/games/"+a.ID+"/login/custom", map[string]any{"login_ticket": ticket}, map[string]string{"game_id": a.ID})
	if rec.Code != 401 {
		t.Fatal("replayed ticket accepted")
	}
	guest, _ := gameLogin(t, f, b)
	credential := guest["device_id"].(string)
	if len(credential) < 32 {
		t.Fatal("guest secret not returned")
	}
	rec = doReq(f.app.handleLoginDevice, "POST", "/v2/games/"+b.ID+"/login/device", map[string]any{"device_id": credential}, map[string]string{"game_id": b.ID})
	if rec.Code != 200 || playerID(t, decode(t, rec)) != playerID(t, guest) {
		t.Fatal("returning guest lost identity")
	}
	rec = doReq(f.app.handleLoginDevice, "POST", "/v2/games/"+b.ID+"/login/device", map[string]any{"device_id": "predictable"}, map[string]string{"game_id": b.ID})
	if rec.Code != 400 {
		t.Fatal("short device identifier accepted")
	}
}

func TestGames_IdempotencyAndRollback(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	if _, err := defineStat(f.ctx, f.pid, "coins", "sum", false, ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"coins", 1}}, "test", false, "same-operation")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stat, _ := dbGetPlayerStat(f.ctx.AppDB(), f.pid, p.ID, "coins")
	if stat.Value != 1 {
		t.Fatalf("retries changed sum to %v", stat.Value)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"coins", 2}}, "test", false, "same-operation"); err == nil {
		t.Fatal("changed payload reused operation key")
	}
	if _, err := f.ctx.AppDB().Exec(`CREATE TRIGGER reject_outbox BEFORE INSERT ON game_outbox BEGIN SELECT RAISE(ABORT,'outbox failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"coins", 10}}, "test", false, "failed-operation"); err == nil {
		t.Fatal("outbox failure did not fail operation")
	}
	stat, _ = dbGetPlayerStat(f.ctx.AppDB(), f.pid, p.ID, "coins")
	if stat.Value != 1 {
		t.Fatal("outbox failure left committed stats")
	}
}

func TestGames_ServerSaveProtectionAndQuota(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	db := f.ctx.AppDB()
	if _, err := dbSetData(db, f.pid, p.ID, "save", "1", "private", 0); err != nil {
		t.Fatal(err)
	}
	// A client pre-read can become stale while an agent changes visibility.
	if _, err := dbSetData(db, f.pid, p.ID, "save", "2", "server", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetData(db, f.pid, p.ID, "save", "3", "public", 0, true); err != errServerOnly {
		t.Fatalf("protected write: %v", err)
	}
	if deleted, err := dbDeleteData(db, f.pid, p.ID, "save", true); err != nil || deleted {
		t.Fatal("client deleted server key")
	}
	row, _ := dbGetData(db, f.pid, p.ID, "save")
	if string(row.Value) != "2" || row.Visibility != "server" {
		t.Fatal("protected value changed")
	}
	for i := 0; i < 127; i++ {
		if _, err := dbSetData(db, f.pid, p.ID, "key"+idStr(int64(i)), "1", "private", 0, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dbSetData(db, f.pid, p.ID, "overflow", "1", "private", 0, true); err == nil {
		t.Fatal("key quota not enforced")
	}
}

func TestGames_CursorAndNeighborOrder(t *testing.T) {
	for _, sort := range []string{"asc", "desc"} {
		t.Run(sort, func(t *testing.T) {
			f := newFixture(t)
			lb, err := createLeaderboard(f.ctx, f.pid, "board", "", "score", sort, "none", 0)
			if err != nil {
				t.Fatal(err)
			}
			ids := []int64{}
			for i := 0; i < 30; i++ {
				id, e := dbCreatePlayer(f.ctx.AppDB(), f.pid, int64(i+1), "Player", "guest")
				if e != nil {
					t.Fatal(e)
				}
				ids = append(ids, id)
				if e = dbUpsertEntry(f.ctx.AppDB(), f.pid, lb.ID, "all", id, float64(i/3)); e != nil {
					t.Fatal(e)
				}
			}
			if _, err = f.ctx.AppDB().Exec(`UPDATE leaderboard_entries SET updated_at='2026-09-06T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}
			first, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			second, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, 0, leaderboardReadOptions{Cursor: first.NextCursor})
			if err != nil {
				t.Fatal(err)
			}
			offset, err := dbTopEntries(f.ctx.AppDB(), f.pid, lb.ID, "all", sort, 10, 10)
			if err != nil {
				t.Fatal(err)
			}
			for i := range offset {
				if offset[i].PlayerID != second.Entries[i].PlayerID {
					t.Fatal("cursor differs from total ordering")
				}
			}
			middle := second.Entries[4].PlayerID
			around, err := leaderboardAround(f.ctx, f.pid, lb, "all", middle, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(around.Entries) != 5 || around.Entries[2].PlayerID != middle || around.Entries[2].Rank != around.Me.Rank {
				t.Fatal("around-player rank/order mismatch")
			}
			fast, err := leaderboardAround(f.ctx, f.pid, lb, "all", middle, 2, false)
			if err != nil {
				t.Fatal(err)
			}
			if fast.Me.Rank != 0 {
				t.Fatal("optional rank was still computed")
			}
			if _, err = decodeEntryCursor(GameScope{f.pid.ProjectID, "foreign"}, lb.ID, "all", first.NextCursor); err == nil {
				t.Fatal("cross-game cursor accepted")
			}
		})
	}
}

func TestGames_MigratePopulatedDatabase(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(newFakeAuth(t)), tk.WithConfig(map[string]string{"auth_organization_slug": "existing-org"}))
	db := ctx.AppDB()
	sql := `INSERT INTO players(id,project_id,auth_user_id,display_name) VALUES(42,'test-proj',99,'Kept');
 INSERT INTO settings(project_id,key,value) VALUES('test-proj','auth_client_id','original-client');
 INSERT INTO player_data(project_id,player_id,key,value,visibility,version) VALUES('test-proj',42,'save','{"level":9}','private',7);
 INSERT INTO leaderboards(id,project_id,name,stat,current_period) VALUES(8,'test-proj','weekly','score','old-period');
 INSERT INTO leaderboard_entries(project_id,leaderboard_id,period,player_id,score) VALUES('test-proj',8,'old-period',42,77);
 INSERT INTO player_audit(project_id,player_id,event) VALUES('test-proj',42,'kept');`
	if _, err := db.Exec(sql); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(ctx); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	scope := GameScope{"test-proj", "legacy-test-proj"}
	g, err := getGame(db, scope.ProjectID, scope.GameID)
	if err != nil || g.AuthOrganization != "existing-org" {
		t.Fatal("Auth organization lost")
	}
	client, err := dbGetSetting(db, scope, settingAuthClient)
	if err != nil || client != "original-client" {
		t.Fatal("Auth client changed")
	}
	save, err := dbGetData(db, scope, 42, "save")
	if err != nil || save.Version != 7 || string(save.Value) != "{\"level\":9}" {
		t.Fatalf("save not preserved: %+v %v", save, err)
	}
	entry, err := dbGetEntry(db, scope, 8, "old-period", 42)
	if err != nil || entry.Score != 77 {
		t.Fatal("board history lost")
	}
	if gameIdentity(ctx, scope, "old-id") != identitySubject("old-id") {
		t.Fatal("legacy identity hashes changed")
	}
}

func TestGames_MigrationRollbackOnCrossProjectOrphan(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"))
	db := ctx.AppDB()
	if _, err := db.Exec(`INSERT INTO players(id,project_id,auth_user_id) VALUES(1,'a',1);INSERT INTO player_data(project_id,player_id,key,value) VALUES('b',1,'bad','1')`); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(ctx); err == nil {
		t.Fatal("migration should reject a cross-project child")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM player_data WHERE project_id='b'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed migration lost original data")
	}
	if _, err := db.Exec(`SELECT game_id FROM players`); err == nil {
		t.Fatal("failed migration partially replaced schema")
	}
	if _, err := db.Exec(`DELETE FROM player_data WHERE project_id='b'`); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(ctx); err != nil {
		t.Fatalf("retry after repairing data: %v", err)
	}
}

func TestGames_TokenIssuerNbfAndCacheIsolation(t *testing.T) {
	f := newFixture(t)
	_, token := f.loginDevice(t, "validation", "Player")
	for _, claims := range []map[string]any{{"iss": "https://wrong.example"}, {"nbf": time.Now().Add(time.Hour).Unix()}, {"azp": "another-client"}, {"exp": 0}} {
		bad := resignClaims(f.auth.priv, token, claims)
		rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(bad)...)
		if rec.Code != 401 {
			b, _ := json.Marshal(claims)
			t.Fatalf("accepted invalid claims %s", b)
		}
	}
	// Cache is scoped even when two Auth organizations use the same kid.
	if scopeKeys(f.pid) == scopeKeys(GameScope{f.pid.ProjectID, "different"}) {
		t.Fatal("shared cross-game signing cache")
	}
	if _, err := decodeEntryCursor(f.pid, 1, "all", strings.Repeat("x", 3000)); err == nil {
		t.Fatal("oversized cursor accepted")
	}
}

func TestGames_TitleAndDescriptionPersistAndUpdateIndependently(t *testing.T) {
	f := newFixture(t)
	out, err := gameAction(f.ctx, "create", map[string]any{"slug": "story-game", "name": "Story World", "description": "A cooperative adventure."})
	if err != nil {
		t.Fatal(err)
	}
	g := out.(map[string]any)["game"].(*Game)
	stored, err := getGame(f.ctx.AppDB(), g.ProjectID, g.ID)
	if err != nil || stored.Name != "Story World" || stored.Description != "A cooperative adventure." {
		t.Fatalf("game text not persisted: %+v %v", stored, err)
	}
	other := testGame(t, f, "other-game")
	out, err = gameAction(f.ctx, "update", map[string]any{"game_id": g.ID, "description": "New description"})
	if err != nil {
		t.Fatal(err)
	}
	updated := out.(map[string]any)["game"].(*Game)
	if updated.Name != g.Name || updated.Description != "New description" {
		t.Fatalf("description-only update: %+v", updated)
	}
	if _, err = gameAction(f.ctx, "update", map[string]any{"game_id": g.ID, "name": "Story World 2"}); err != nil {
		t.Fatal(err)
	}
	stored, err = getGame(f.ctx.AppDB(), g.ProjectID, g.ID)
	if err != nil || stored.Name != "Story World 2" || stored.Description != "New description" || stored.Slug != g.Slug {
		t.Fatal("title update changed description or slug")
	}
	if _, err = gameAction(f.ctx, "update", map[string]any{"game_id": g.ID, "description": ""}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{42, strings.Repeat("x", 4001)} {
		if _, err = gameAction(f.ctx, "update", map[string]any{"game_id": g.ID, "description": value}); err == nil {
			t.Fatal("invalid description accepted")
		}
	}
	if err = initializeGames(f.ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = getGame(f.ctx.AppDB(), g.ProjectID, g.ID)
	if err != nil || stored.Description != "" || stored.Name != "Story World 2" {
		t.Fatal("description clearing or restart failed")
	}
	isolated, err := getGame(f.ctx.AppDB(), other.ProjectID, other.ID)
	if err != nil || isolated.Description != "" || isolated.Name != other.Name {
		t.Fatal("another game's text changed")
	}
	legacy, err := getGame(f.ctx.AppDB(), f.pid.ProjectID, f.pid.GameID)
	if err != nil || legacy.Description != "" {
		t.Fatal("legacy description default failed")
	}
}

func TestGames_DescriptionUpgradeForExistingCatalog(t *testing.T) {
	f := newFixture(t)
	g := testGame(t, f, "existing")
	if _, err := f.ctx.AppDB().Exec(`ALTER TABLE games DROP COLUMN description`); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(f.ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := getGame(f.ctx.AppDB(), g.ProjectID, g.ID)
	if err != nil || restored.Name != g.Name || restored.Description != "" {
		t.Fatalf("existing catalog upgrade failed: %+v %v", restored, err)
	}
	if _, err := gameAction(f.ctx, "update", map[string]any{"game_id": g.ID, "description": "Added after upgrade"}); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(f.ctx); err != nil {
		t.Fatal(err)
	}
	restored, err = getGame(f.ctx.AppDB(), g.ProjectID, g.ID)
	if err != nil || restored.Description != "Added after upgrade" {
		t.Fatal("restart erased description")
	}
}
