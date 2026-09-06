package main

// Regressions found in the v0.1 audit. Keep these passing across releases.
import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"
)

func auditPlayer(t *testing.T, f *fixture) *Player {
	t.Helper()
	id, err := dbCreatePlayer(f.ctx.AppDB(), f.pid, 1001, "Audit", "guest")
	if err != nil {
		t.Fatal(err)
	}
	p, err := dbGetPlayer(f.ctx.AppDB(), f.pid, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAudit_PeriodScoreBelowLifetimeBest(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	if _, err := defineStat(f.ctx, f.pid, "score", "max", false, ""); err != nil {
		t.Fatal(err)
	}
	lb, err := createLeaderboard(f.ctx, f.pid, "weekly", "", "score", "desc", "weekly", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"score", 100}}, "test", false); err != nil {
		t.Fatal(err)
	}
	if err := resetLeaderboardNow(f.ctx, f.pid, lb, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"score", 50}}, "test", false); err != nil {
		t.Fatal(err)
	}
	e, err := dbGetEntry(f.ctx.AppDB(), f.pid, lb.ID, lb.CurrentPeriod, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e == nil || e.Score != 50 {
		t.Fatalf("new-period score should be 50; got %+v", e)
	}
}

func TestAudit_StatAndLeaderboardRollbackTogether(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	if _, err := createLeaderboard(f.ctx, f.pid, "best", "", "score", "desc", "none", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctx.AppDB().Exec(`CREATE TRIGGER fail_entry BEFORE INSERT ON leaderboard_entries BEGIN SELECT RAISE(ABORT, 'injected board write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"score", 100}}, "test", false); err == nil {
		t.Fatal("expected injected failure")
	}
	s, err := dbGetPlayerStat(f.ctx.AppDB(), f.pid, p.ID, "score")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("stat committed despite failed leaderboard write: %+v", s)
	}
}

func TestAudit_ConcurrentSumDoesNotLoseUpdates(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	if _, err := defineStat(f.ctx, f.pid, "kills", "sum", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpsertPlayerStat(f.ctx.AppDB(), f.pid, p.ID, "kills", 0); err != nil {
		t.Fatal(err)
	}
	const n = 100
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"kills", 1}}, "test", false)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := dbGetPlayerStat(f.ctx.AppDB(), f.pid, p.ID, "kills")
	if err != nil {
		t.Fatal(err)
	}
	if s.Value != n {
		t.Fatalf("100 successful increments produced %v, want 100", s.Value)
	}
}

func TestAudit_ExpiredBanAllowsFreshLogin(t *testing.T) {
	f := newFixture(t)
	out, _ := f.loginDevice(t, "ban-device", "Banned")
	p, err := dbGetPlayer(f.ctx.AppDB(), f.pid, playerID(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := banPlayer(f.ctx, f.pid, p, "temporary", time.Now().Add(time.Hour).Format(time.RFC3339), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctx.AppDB().Exec(`UPDATE player_bans SET expires_at = ? WHERE player_id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), p.ID); err != nil {
		t.Fatal(err)
	}
	rec := doReq(f.app.handleLoginDevice, "POST", "/v1/login/device", map[string]any{"device_id": "ban-device"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expired ban blocks fresh login: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAudit_TokenClaimsAreBoundToGames(t *testing.T) {
	for _, variant := range []string{"wrong_audience", "missing_exp", "missing_org"} {
		t.Run(variant, func(t *testing.T) {
			f := newFixture(t)
			out, _ := f.loginDevice(t, "token-device", "Token")
			uid := int64(out["player"].(map[string]any)["auth_user_id"].(float64))
			claims := map[string]any{"sub": idStr(uid), "org": "default", "exp": time.Now().Add(time.Hour).Unix(), "iss": "http://test/orgs/default", "aud": "akc_test", "azp": "akc_test"}
			switch variant {
			case "wrong_audience":
				claims["aud"] = "other-app"
				claims["azp"] = "other-app"
			case "missing_exp":
				delete(claims, "exp")
			case "missing_org":
				delete(claims, "org")
			}
			h, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": f.auth.kid})
			c, _ := json.Marshal(claims)
			enc := base64.RawURLEncoding.EncodeToString
			input := enc(h) + "." + enc(c)
			token := input + "." + enc(ed25519.Sign(f.auth.priv, []byte(input)))
			rec := doReq(f.app.handleMe, "GET", "/v1/me", nil, nil, bearer(token)...)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s token accepted: status=%d", variant, rec.Code)
			}
		})
	}
}

func TestAudit_TiedRankMatchesPage(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	id, err := dbCreatePlayer(f.ctx.AppDB(), f.pid, 1002, "Second", "guest")
	if err != nil {
		t.Fatal(err)
	}
	lb, err := createLeaderboard(f.ctx, f.pid, "ties", "", "score", "desc", "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int64{p.ID, id} {
		if err := dbUpsertEntry(f.ctx.AppDB(), f.pid, lb.ID, "all", pid, 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.ctx.AppDB().Exec(`UPDATE leaderboard_entries SET updated_at='2026-09-06T12:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	page, err := leaderboardPageFor(f.ctx, f.pid, lb, "all", 10, 0, id)
	if err != nil {
		t.Fatal(err)
	}
	if page.Me.Rank != page.Entries[1].Rank {
		t.Fatalf("same player is rank %d in me but rank %d in entries", page.Me.Rank, page.Entries[1].Rank)
	}
}

func TestAudit_SumOverflowDoesNotPoisonJSON(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	if _, err := defineStat(f.ctx, f.pid, "total", "sum", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"total", math.MaxFloat64}}, "test", false); err != nil {
		t.Fatal(err)
	}
	out, err := applyStatUpdates(f.ctx, f.pid, p, []statUpdate{{"total", math.MaxFloat64}}, "test", false)
	if err != nil {
		return
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("accepted finite inputs created unencodable result: %v", err)
	}
}

func TestAudit_ExportIncludesAllAuditRowsAndBoardEntries(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	for i := 0; i < 501; i++ {
		dbAudit(f.ctx.AppDB(), f.pid, p.ID, "test", "audit", nil)
	}
	lb, err := createLeaderboard(f.ctx, f.pid, "export", "", "score", "desc", "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertEntry(f.ctx.AppDB(), f.pid, lb.ID, "all", p.ID, 1); err != nil {
		t.Fatal(err)
	}
	out, err := exportPlayer(f.ctx, f.pid, p)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out["audit"].([]AuditEvent)); n != 501 {
		t.Errorf("export includes %d of 501 audit records", n)
	}
	if _, ok := out["leaderboard_entries"]; !ok {
		t.Error("export omits leaderboard entries")
	}
}

func TestAudit_TwoResetsInOneSecondAreDistinct(t *testing.T) {
	f := newFixture(t)
	p := auditPlayer(t, f)
	lb, err := createLeaderboard(f.ctx, f.pid, "reset", "", "score", "desc", "none", 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	if err := resetLeaderboardNow(f.ctx, f.pid, lb, now); err != nil {
		t.Fatal(err)
	}
	first := lb.CurrentPeriod
	if err := dbUpsertEntry(f.ctx.AppDB(), f.pid, lb.ID, first, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := resetLeaderboardNow(f.ctx, f.pid, lb, now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if lb.CurrentPeriod == first {
		t.Fatalf("second reset reused %s and retained entries", first)
	}
}
