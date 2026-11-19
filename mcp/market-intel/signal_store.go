package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func ensureDefaultFeed(db *sql.DB, projectID string) (*SignalFeed, error) {
	_, err := db.Exec(`INSERT INTO signal_feeds(project_id,slug,name,description,asset_class,interval,min_confidence,min_net_edge_bps,visibility)
		VALUES(?, 'crypto-momentum', 'Crypto Momentum', 'Audited crypto signals from normalized market data.', 'crypto','1h',0.62,20,'private')
		ON CONFLICT(project_id,slug) DO NOTHING`, projectID)
	if err != nil {
		return nil, err
	}
	return getFeed(db, projectID, "crypto-momentum")
}
func getFeed(db *sql.DB, pid, slug string) (*SignalFeed, error) {
	var f SignalFeed
	err := db.QueryRow(`SELECT id,slug,name,description,asset_class,interval,min_confidence,min_net_edge_bps,visibility,delay_seconds,status FROM signal_feeds WHERE project_id=? AND slug=?`, pid, slug).
		Scan(&f.ID, &f.Slug, &f.Name, &f.Description, &f.AssetClass, &f.Interval, &f.MinConfidence, &f.MinNetEdgeBps, &f.Visibility, &f.DelaySeconds, &f.Status)
	return &f, err
}
func listFeeds(db *sql.DB, pid string) ([]SignalFeed, error) {
	rows, err := db.Query(`SELECT id,slug,name,description,asset_class,interval,min_confidence,min_net_edge_bps,visibility,delay_seconds,status FROM signal_feeds WHERE project_id=? ORDER BY id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SignalFeed{}
	for rows.Next() {
		var f SignalFeed
		if err := rows.Scan(&f.ID, &f.Slug, &f.Name, &f.Description, &f.AssetClass, &f.Interval, &f.MinConfidence, &f.MinNetEdgeBps, &f.Visibility, &f.DelaySeconds, &f.Status); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func startSignalRun(db *sql.DB, pid, provider, feed string) (int64, error) {
	r, err := db.Exec(`INSERT INTO signal_runs(project_id,provider,feed_slug) VALUES(?,?,?)`, pid, provider, feed)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
func finishSignalRun(db *sql.DB, id int64, universe, scanned, emitted, rejected int, runErr error) {
	status, msg := "completed", ""
	if runErr != nil {
		status = "failed"
		msg = runErr.Error()
	}
	_, _ = db.Exec(`UPDATE signal_runs SET finished_at=CURRENT_TIMESTAMP,universe_size=?,scanned=?,emitted=?,rejected=?,status=?,error=NULLIF(?,'') WHERE id=?`, universe, scanned, emitted, rejected, status, msg, id)
}

func insertSignal(db *sql.DB, s *SignalOpportunity) (bool, error) {
	rationale, _ := json.Marshal(s.Rationale)
	features, _ := json.Marshal(s.Features)
	provenance, _ := json.Marshal(s.Provenance)
	res, err := db.Exec(`INSERT INTO signal_opportunities(public_id,project_id,feed_id,run_id,provider,venue,provider_symbol,canonical_symbol,asset_class,strategy,direction,interval,signal_time,valid_until,entry_price,bid_price,ask_price,stop_loss,target_1,target_2,confidence,score,expected_move_bps,gross_edge_bps,cost_bps,net_edge_bps,spread_bps,quote_volume_24h,rationale,features,provenance,status)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'open') ON CONFLICT(project_id,provider,provider_symbol,strategy,direction,interval,signal_time) DO NOTHING`,
		s.PublicID, s.ProjectID, s.FeedID, s.RunID, s.Provider, s.Venue, s.ProviderSymbol, s.CanonicalSymbol, s.AssetClass, s.Strategy, s.Direction, s.Interval, s.SignalTime, s.ValidUntil.UTC(), s.EntryPrice, s.BidPrice, s.AskPrice, s.StopLoss, s.Target1, s.Target2, s.Confidence, s.Score, s.ExpectedMoveBps, s.GrossEdgeBps, s.CostBps, s.NetEdgeBps, s.SpreadBps, s.QuoteVolume24h, string(rationale), string(features), string(provenance))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func listSignals(db *sql.DB, pid, feed, status string, limit int) ([]SignalOpportunity, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT s.id,s.public_id,s.feed_id,f.slug,s.run_id,s.provider,s.venue,s.provider_symbol,s.canonical_symbol,s.asset_class,s.strategy,s.direction,s.interval,s.signal_time,s.generated_at,s.valid_until,s.entry_price,s.bid_price,s.ask_price,s.stop_loss,s.target_1,s.target_2,s.confidence,s.score,s.expected_move_bps,s.gross_edge_bps,s.cost_bps,s.net_edge_bps,s.spread_bps,s.quote_volume_24h,s.rationale,s.features,s.provenance,s.status,s.evaluation_price,s.realized_return_bps,COALESCE(s.outcome,''),s.evaluated_at FROM signal_opportunities s JOIN signal_feeds f ON f.id=s.feed_id WHERE s.project_id=?`
	args := []any{pid}
	if feed != "" {
		q += " AND f.slug=?"
		args = append(args, feed)
	}
	if status != "" && status != "all" {
		q += " AND s.status=?"
		args = append(args, status)
	}
	q += " ORDER BY s.generated_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SignalOpportunity{}
	for rows.Next() {
		var s SignalOpportunity
		var generated, valid string
		var rat, feat, prov string
		var ep, rr sql.NullFloat64
		var evaluated sql.NullString
		if err := rows.Scan(&s.ID, &s.PublicID, &s.FeedID, &s.FeedSlug, &s.RunID, &s.Provider, &s.Venue, &s.ProviderSymbol, &s.CanonicalSymbol, &s.AssetClass, &s.Strategy, &s.Direction, &s.Interval, &s.SignalTime, &generated, &valid, &s.EntryPrice, &s.BidPrice, &s.AskPrice, &s.StopLoss, &s.Target1, &s.Target2, &s.Confidence, &s.Score, &s.ExpectedMoveBps, &s.GrossEdgeBps, &s.CostBps, &s.NetEdgeBps, &s.SpreadBps, &s.QuoteVolume24h, &rat, &feat, &prov, &s.Status, &ep, &rr, &s.Outcome, &evaluated); err != nil {
			return nil, err
		}
		s.GeneratedAt = parseDBTime(generated)
		s.ValidUntil = parseDBTime(valid)
		_ = json.Unmarshal([]byte(rat), &s.Rationale)
		_ = json.Unmarshal([]byte(feat), &s.Features)
		_ = json.Unmarshal([]byte(prov), &s.Provenance)
		if ep.Valid {
			s.EvaluationPrice = &ep.Float64
		}
		if rr.Valid {
			s.RealizedReturnBps = &rr.Float64
		}
		if evaluated.Valid {
			t := parseDBTime(evaluated.String)
			s.EvaluatedAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func evaluateExpiredSignals(db *sql.DB, pid, provider string, prices map[string]float64) (int, error) {
	rows, err := db.Query(`SELECT id,provider_symbol,direction,entry_price FROM signal_opportunities WHERE project_id=? AND provider=? AND status='open' AND valid_until<=CURRENT_TIMESTAMP`, pid, provider)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id          int64
		symbol, dir string
		entry       float64
	}
	pending := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.symbol, &r.dir, &r.entry); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	count := 0
	for _, r := range pending {
		p := prices[r.symbol]
		if p <= 0 {
			continue
		}
		ret := (p/r.entry - 1) * 10000
		if r.dir == "SELL" {
			ret = -ret
		}
		outcome := "flat"
		if ret > 10 {
			outcome = "win"
		} else if ret < -10 {
			outcome = "loss"
		}
		if _, err := db.Exec(`UPDATE signal_opportunities SET status='evaluated',evaluation_price=?,realized_return_bps=?,outcome=?,evaluated_at=CURRENT_TIMESTAMP WHERE id=?`, p, ret, outcome, r.id); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func signalMetrics(db *sql.DB, pid, feed string) (map[string]any, error) {
	q := `SELECT COUNT(*),SUM(CASE WHEN outcome='win' THEN 1 ELSE 0 END),SUM(CASE WHEN outcome='loss' THEN 1 ELSE 0 END),AVG(realized_return_bps),AVG(confidence),AVG(net_edge_bps) FROM signal_opportunities s JOIN signal_feeds f ON f.id=s.feed_id WHERE s.project_id=? AND s.status='evaluated'`
	args := []any{pid}
	if feed != "" {
		q += " AND f.slug=?"
		args = append(args, feed)
	}
	var total int
	var wins, losses sql.NullInt64
	var avg, conf, edge sql.NullFloat64
	if err := db.QueryRow(q, args...).Scan(&total, &wins, &losses, &avg, &conf, &edge); err != nil {
		return nil, err
	}
	hit := 0.0
	if wins.Int64+losses.Int64 > 0 {
		hit = float64(wins.Int64) / float64(wins.Int64+losses.Int64)
	}
	return map[string]any{"evaluated": total, "wins": wins.Int64, "losses": losses.Int64, "hit_rate": hit, "avg_return_bps": avg.Float64, "avg_confidence": conf.Float64, "avg_net_edge_bps": edge.Float64}, nil
}

func upsertProviderHealth(db *sql.DB, pid, provider, status string, latencyMS, instruments int, runErr error) {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	success, errorAt := any(nil), any(nil)
	if runErr == nil {
		success = time.Now().UTC()
	} else {
		errorAt = time.Now().UTC()
	}
	_, _ = db.Exec(`INSERT INTO provider_health(project_id,provider,status,latency_ms,instruments,last_success_at,last_error_at,error,updated_at) VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(project_id,provider) DO UPDATE SET status=excluded.status,latency_ms=excluded.latency_ms,instruments=excluded.instruments,last_success_at=COALESCE(excluded.last_success_at,provider_health.last_success_at),last_error_at=COALESCE(excluded.last_error_at,provider_health.last_error_at),error=excluded.error,updated_at=CURRENT_TIMESTAMP`, pid, provider, status, latencyMS, instruments, success, errorAt, msg)
}
func parseDBTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, e := time.Parse(layout, s); e == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
func mustJSON(v any) string                    { b, _ := json.Marshal(v); return string(b) }
func dbSignalError(op string, err error) error { return fmt.Errorf("signal store %s: %w", op, err) }
