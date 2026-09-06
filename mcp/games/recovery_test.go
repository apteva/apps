package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRecovery_LegacyAuthBanOwnership(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "games_disable", false: "independent_disable"}[owned], func(t *testing.T) {
			f := newFixture(t)
			out, _ := f.loginDevice(t, "legacy-banned", "Old")
			p, _ := dbGetPlayer(f.ctx.AppDB(), f.pid, playerID(t, out))
			if _, err := dbCreateBan(f.ctx.AppDB(), f.pid, p.ID, "old ban", "test", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); err != nil {
				t.Fatal(err)
			}
			if err := dbSetPlayerStatus(f.ctx.AppDB(), f.pid, p.ID, "banned"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ctx.AppDB().Exec(`INSERT INTO game_legacy_auth_bans(project_id,game_id,auth_user_id,reason) VALUES(?,?,?,'old ban')`, f.pid.ProjectID, f.pid.GameID, p.AuthUserID); err != nil {
				t.Fatal(err)
			}
			reason := "security suspension"
			if owned {
				reason = "games ban: old ban"
			}
			if err := authDisableUser(f.ctx, f.pid, p.AuthUserID, reason); err != nil {
				t.Fatal(err)
			}
			recovered, err := recoverLegacyBan(f.ctx, f.pid, p.AuthUserID, false)
			if owned {
				if err != nil || !recovered || f.auth.disabled[p.AuthUserID] {
					t.Fatalf("owned ban did not recover: %v", err)
				}
			} else {
				if err == nil || recovered || !f.auth.disabled[p.AuthUserID] {
					t.Fatal("independent Auth suspension was changed")
				}
			}
		})
	}
}

func TestRecovery_OutboxFailureRetryAndRetention(t *testing.T) {
	f := newFixture(t)
	if err := queueEvent(f.ctx.AppDB(), f.pid, "stat.updated", map[string]any{"player_id": 1}, false); err != nil {
		t.Fatal(err)
	}
	fail := func(GameScope, string, map[string]any) error { return errors.New("temporary delivery failure") }
	for i := 0; i < 10; i++ {
		if _, err := f.ctx.AppDB().Exec(`UPDATE game_outbox SET next_attempt='2000-01-01T00:00:00Z'`); err != nil {
			t.Fatal(err)
		}
		if err := drainOutbox(f.ctx, fail); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	var detail string
	if err := f.ctx.AppDB().QueryRow(`SELECT attempts,last_error FROM game_outbox`).Scan(&attempts, &detail); err != nil {
		t.Fatal(err)
	}
	if attempts != 10 || detail == "" {
		t.Fatal("failure was discarded")
	}
	calls := 0
	ok := func(GameScope, string, map[string]any) error { calls++; return nil }
	if err := drainOutbox(f.ctx, ok); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("dead letter kept retrying without intervention")
	}
	if _, err := retryGameEvents(f.ctx, map[string]any{"game_id": f.pid.GameID}); err != nil {
		t.Fatal(err)
	}
	if err := drainOutbox(f.ctx, ok); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("explicit retry did not deliver")
	}
	var remaining int
	if err := f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM game_outbox`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("acknowledged event was retained")
	}
}

func TestRecovery_JWKSBackoffAndStaleLimit(t *testing.T) {
	f := newFixture(t)
	_, token := f.loginDevice(t, "jwks-player", "JWKS")
	if _, err := verifyPlayerToken(f.ctx, f.pid, token); err != nil {
		t.Fatal(err)
	}
	cache := scopeKeys(f.pid)
	cache.mu.Lock()
	cache.fetched = time.Now().Add(-11 * time.Minute)
	cache.lastAttempt = time.Now().Add(-time.Minute)
	cache.mu.Unlock()
	f.auth.failTool = "auth.auth_jwks_get"
	before := f.auth.callCount("auth.auth_jwks_get")
	for i := 0; i < 100; i++ {
		if _, err := verifyPlayerToken(f.ctx, f.pid, token); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.auth.callCount("auth.auth_jwks_get") - before; n != 1 {
		t.Fatalf("outage caused %d repeated JWKS requests", n)
	}
	cache.mu.Lock()
	cache.fetched = time.Now().Add(-21 * time.Minute)
	cache.mu.Unlock()
	if _, err := verifyPlayerToken(f.ctx, f.pid, token); err == nil {
		t.Fatal("unbounded stale key accepted")
	}
}

func TestRecovery_ConcurrentProvisioningAndFirstLogin(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := ensureAuthClient(f.ctx, f.pid); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.auth.callCount("auth.auth_clients_create") != 1 {
		t.Fatal("racing first logins provisioned multiple clients")
	}
	errs = make(chan error, 30)
	created := make(chan bool, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := finishLoginState(f.ctx, f.pid, "device", &authLoginResult{User: authUser{ID: 123, DisplayName: "First"}})
			created <- c
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(created)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for c := range created {
		if c {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reported %d first creations", count)
	}
	player, err := dbGetPlayerByAuthUser(f.ctx.AppDB(), f.pid, 123)
	if err != nil || player.LoginCount != 30 {
		t.Fatalf("login count: %+v %v", player, err)
	}
}

func TestRecovery_ResetRetryAndRolloverFailure(t *testing.T) {
	f := newFixture(t)
	lb, err := createLeaderboard(f.ctx, f.pid, "daily", "", "score", "desc", "daily", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = resetLeaderboardNow(f.ctx, f.pid, lb, time.Now(), "reset-1"); err != nil {
		t.Fatal(err)
	}
	first := lb.CurrentPeriod
	if err = resetLeaderboardNow(f.ctx, f.pid, lb, time.Now(), "reset-1"); err != nil || lb.CurrentPeriod != first {
		t.Fatal("reset retry changed period")
	}
	if err = dbSetLeaderboardPeriod(f.ctx.AppDB(), f.pid, lb.ID, "2000-01-01", "2000-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	lb, err = dbGetLeaderboard(f.ctx.AppDB(), f.pid, "daily")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ctx.AppDB().Exec(`CREATE TRIGGER fail_rollover BEFORE UPDATE ON leaderboards BEGIN SELECT RAISE(ABORT,'rollover failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = leaderboardPageFor(f.ctx, f.pid, lb, "", 10, 0, 0); err == nil {
		t.Fatal("read silently continued after rollover failed")
	}
}

func TestRecovery_SeasonKeepsScheduledBoundaryAfterDowntime(t *testing.T) {
	f := newFixture(t)
	lb, err := createLeaderboard(f.ctx, f.pid, "seasonal", "", "score", "desc", "season", 7)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := dbSetLeaderboardPeriod(f.ctx.AppDB(), f.pid, lb.ID, "season-1", start.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetLeaderboard(f.ctx.AppDB(), f.pid, "seasonal")
	if err != nil {
		t.Fatal(err)
	}
	*lb = *fresh
	if err := ensureCurrentPeriod(f.ctx, f.pid, lb, start.Add(17*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if lb.CurrentPeriod != "season-3" || lb.PeriodStartedAt != start.Add(14*24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("season drifted after downtime: %+v", lb)
	}
	if got := periodKey(lb, start.Add(21*24*time.Hour)); got != "season-4" {
		t.Fatalf("next boundary drifted: %s", got)
	}
}

func TestRecovery_AuditFailureRollsBackModerationProfileAndGrant(t *testing.T) {
	f := newFixture(t)
	out, _ := f.loginDevice(t, "atomic-player", "Before")
	p, _ := dbGetPlayer(f.ctx.AppDB(), f.pid, playerID(t, out))
	if _, err := defineAchievement(f.ctx, f.pid, AchievementDef{Key: "manual", Name: "Manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctx.AppDB().Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON player_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := banPlayer(f.ctx, f.pid, p, "test", "", "test"); err == nil {
		t.Fatal("ban succeeded without audit")
	}
	if ban, err := dbActiveBan(f.ctx.AppDB(), f.pid, p.ID); err != nil || ban != nil {
		t.Fatal("partial ban persisted")
	}
	if _, err := applyProfilePatch(f.ctx, f.pid, p, map[string]any{"display_name": "After"}, "client"); err == nil {
		t.Fatal("profile succeeded without audit")
	}
	fresh, err := dbGetPlayer(f.ctx.AppDB(), f.pid, p.ID)
	if err != nil || fresh.DisplayName != "Before" {
		t.Fatal("partial profile persisted")
	}
	if _, err := grantAchievement(f.ctx, f.pid, p, "manual", "test"); err == nil {
		t.Fatal("grant succeeded without audit")
	}
	achievements, err := dbPlayerAchievements(f.ctx.AppDB(), f.pid, p.ID)
	if err != nil || len(achievements) != 0 {
		t.Fatal("partial grant persisted")
	}
}

func TestRecovery_IDsAreNotReusedAfterEraseAndDelivery(t *testing.T) {
	f := newFixture(t)
	out, _ := f.loginDevice(t, "erase-first", "First")
	p, _ := dbGetPlayer(f.ctx.AppDB(), f.pid, playerID(t, out))
	if err := erasePlayer(f.ctx, f.pid, p, "test"); err != nil {
		t.Fatal(err)
	}
	next, _ := f.loginDevice(t, "erase-next", "Next")
	if id := playerID(t, next); id <= p.ID {
		t.Fatalf("erased player id reused: %d", id)
	}
	if _, err := f.ctx.AppDB().Exec(`DELETE FROM game_outbox`); err != nil {
		t.Fatal(err)
	}
	last := int64(0)
	for i := 0; i < 2; i++ {
		if err := queueEvent(f.ctx.AppDB(), f.pid, "player.created", map[string]any{"player_id": i + 1}, false); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := f.ctx.AppDB().QueryRow(`SELECT id FROM game_outbox`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id <= last {
			t.Fatal("event id reused after acknowledgment")
		}
		last = id
		if err := drainOutbox(f.ctx, func(GameScope, string, map[string]any) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecovery_V2LeaderboardExpensiveFieldsAreOptIn(t *testing.T) {
	f := newFixture(t)
	g := testGame(t, f, "optional")
	scope := g.Scope()
	lb, err := createLeaderboard(f.ctx, scope, "top", "", "score", "desc", "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := leaderboardPageFor(f.ctx, scope, lb, "", 10, 0, 0, leaderboardReadOptions{OmitTotal: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != nil || page.Me != nil {
		t.Fatal("v2 computed optional fields")
	}
	legacy, err := leaderboardPageFor(f.ctx, scope, lb, "", 10, 0, 0)
	if err != nil || legacy.Total == nil || *legacy.Total != 0 {
		t.Fatal("compatibility count omitted")
	}
}
