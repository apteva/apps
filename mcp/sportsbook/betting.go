package main

import (
	"database/sql"
	"math"
	"math/big"
)

// Keep ledger balances exactly representable by the JSON/JavaScript interface.
const maxMoneyMinor int64 = 9007199254740991

func checkRisk(q queryer, project, bankroll string, stake int64) error {
	var maxStake, maxExposure int64
	if err := q.QueryRow("SELECT max_stake_bps,max_exposure_bps FROM bankrolls WHERE project_id=? AND id=?", project, bankroll).Scan(&maxStake, &maxExposure); err == sql.ErrNoRows {
		return fail("not_found", 404, "Bankroll not found")
	} else if err != nil {
		return err
	}
	cash, locked, err := balances(q, project, bankroll)
	if err != nil {
		return err
	}
	if cash < 0 || locked < 0 || locked > maxMoneyMinor || cash > maxMoneyMinor-locked {
		return fail("accounting_range", 409, "Bankroll exceeds supported accounting range")
	}
	equity := cash + locked
	if stake <= 0 || stake > cash {
		return fail("insufficient_funds", 409, "Stake exceeds available paper funds")
	}
	if stake > riskAllowance(equity, maxStake) {
		return fail("stake_limit", 409, "Stake exceeds the bankroll's per-bet limit")
	}
	if locked+stake > riskAllowance(equity, maxExposure) {
		return fail("exposure_limit", 409, "Bet would exceed total open exposure")
	}
	return nil
}

// Divide first so large bankrolls do not overflow during basis-point scaling.
func riskAllowance(equity, bps int64) int64 {
	return equity/10000*bps + equity%10000*bps/10000
}
func (a *App) propose(project, actor string, args map[string]any) (any, error) {
	bankroll, prediction := textArg(args, "bankroll_id"), textArg(args, "prediction_id")
	quote, stake := intArg(args, "quote_id"), intArg(args, "stake_minor")
	rationale := textArg(args, "rationale")
	if quote <= 0 || stake <= 0 || stake > 100000000000 || len(rationale) < 10 || len(rationale) > 2000 {
		return nil, fail("invalid_proposal", 400, "Provide a quote, positive integer stake in minor units and a rationale of 10–2000 characters")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := a.clock()
	var market, selection, eventStatus string
	var odds, observed, start int64
	var eventExample int
	err = tx.QueryRow(`SELECT o.market_id,o.selection,o.odds_micros,o.observed_at,e.status,e.starts_at,e.example FROM odds_observations o JOIN quote_heads h ON h.project_id=o.project_id AND h.market_id=o.market_id AND h.source=o.source AND h.connection_id=o.connection_id AND h.snapshot_id=o.snapshot_id JOIN markets m ON m.project_id=o.project_id AND m.id=o.market_id JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE o.project_id=? AND o.id=?`, project, quote).Scan(&market, &selection, &odds, &observed, &eventStatus, &start, &eventExample)
	if err == sql.ErrNoRows {
		return nil, fail("quote_unavailable", 409, "Quote was superseded or is not in this project")
	}
	if err != nil {
		return nil, err
	}
	if eventStatus != "scheduled" || start <= now {
		return nil, fail("event_closed", 409, "Event is no longer eligible for pre-match betting")
	}
	if observed <= now-quoteTTL || observed > now+30 {
		return nil, fail("stale_quote", 409, "Refresh odds before proposing a bet")
	}
	var probabilities, predMarket string
	var predExpires int64
	err = tx.QueryRow("SELECT probabilities,market_id,expires_at FROM predictions WHERE project_id=? AND id=?", project, prediction).Scan(&probabilities, &predMarket, &predExpires)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", 404, "Prediction not found")
	}
	if err != nil {
		return nil, err
	}
	if predMarket != market {
		return nil, fail("market_mismatch", 400, "Quote and prediction must refer to the same market")
	}
	if predExpires <= now {
		return nil, fail("stale_prediction", 409, "Run a fresh prediction")
	}
	var bankrollExample int
	if err = tx.QueryRow("SELECT example FROM bankrolls WHERE project_id=? AND id=?", project, bankroll).Scan(&bankrollExample); err == sql.ErrNoRows {
		return nil, fail("not_found", 404, "Bankroll not found")
	} else if err != nil {
		return nil, err
	}
	if bankrollExample != eventExample {
		return nil, fail("data_mode_mismatch", 409, "Example events require an example bankroll")
	}
	if err = checkRisk(tx, project, bankroll, stake); err != nil {
		return nil, err
	}
	probs, err := decodeProbabilities(probabilities)
	if err != nil {
		return nil, err
	}
	p := probs[selection]
	if p <= 0 || p >= 1 || math.IsNaN(p) {
		return nil, fail("invalid_probability", 409, "Prediction does not cover this selection")
	}
	expiry := observed + quoteTTL
	if predExpires < expiry {
		expiry = predExpires
	}
	if start < expiry {
		expiry = start
	}
	id := newID()
	ev := p*float64(odds)/1000000 - 1
	_, err = tx.Exec(`INSERT INTO proposals VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, project, id, bankroll, prediction, quote, stake, p, ev, rationale, "proposed", now, expiry)
	if err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, "proposal.created", id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "expected_value": ev, "probability": p, "expires_at": expiry, "status": "proposed"}, nil
}
func (a *App) accept(project, actor, proposal string) (any, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT id FROM bets WHERE project_id=? AND proposal_id=?", project, proposal).Scan(&existing)
	if err == nil {
		return map[string]any{"id": existing, "replayed": true}, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var bankroll, status, eventStatus string
	var stake, expiry, odds, quote, starts int64
	err = tx.QueryRow(`SELECT p.bankroll_id,p.stake_minor,p.expires_at,p.status,p.quote_id,q.odds_micros,e.status,e.starts_at FROM proposals p JOIN odds_observations q ON q.id=p.quote_id AND q.project_id=p.project_id JOIN markets m ON m.project_id=q.project_id AND m.id=q.market_id JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE p.project_id=? AND p.id=?`, project, proposal).Scan(&bankroll, &stake, &expiry, &status, &quote, &odds, &eventStatus, &starts)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", 404, "Proposal not found")
	}
	if err != nil {
		return nil, err
	}
	now := a.clock()
	if status != "proposed" || expiry <= now {
		return nil, fail("proposal_expired", 409, "Proposal expired; refresh quotes and create a new proposal")
	}
	if eventStatus != "scheduled" || starts <= now {
		return nil, fail("event_closed", 409, "Event is no longer eligible for pre-match betting")
	}
	var current int
	err = tx.QueryRow(`SELECT COUNT(*) FROM odds_observations o JOIN quote_heads h ON h.project_id=o.project_id AND h.market_id=o.market_id AND h.source=o.source AND h.connection_id=o.connection_id AND h.snapshot_id=o.snapshot_id WHERE o.project_id=? AND o.id=? AND o.observed_at>? AND o.observed_at<=?`, project, quote, now-quoteTTL, now+30).Scan(&current)
	if err != nil {
		return nil, err
	}
	if current != 1 {
		return nil, fail("quote_changed", 409, "Quote changed; create a new proposal")
	}
	if err = checkRisk(tx, project, bankroll, stake); err != nil {
		return nil, err
	}
	id := newID()
	_, err = tx.Exec(`INSERT INTO bets(project_id,id,proposal_id,bankroll_id,stake_minor,odds_micros,status,accepted_at) VALUES(?,?,?,?,?,?,'open',?)`, project, id, proposal, bankroll, stake, odds, now)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec("UPDATE proposals SET status='accepted' WHERE project_id=? AND id=?", project, proposal)
	if err != nil {
		return nil, err
	}
	if err = ledger(tx, project, bankroll, id+":accept", map[string]int64{"cash": -stake, "locked": stake}, now); err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, "paper.bet.accepted", id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "status": "open", "mode": "paper"}, nil
}
func payout(stake, odds int64) (int64, error) {
	n := new(big.Int).Mul(big.NewInt(stake), big.NewInt(odds))
	n.Add(n, big.NewInt(500000))
	n.Quo(n, big.NewInt(1000000))
	if !n.IsInt64() {
		return 0, fail("payout_overflow", 409, "Payout exceeds supported accounting range")
	}
	return n.Int64(), nil
}
func (a *App) settle(project, actor string, args map[string]any) (any, error) {
	id, outcome, note := textArg(args, "bet_id"), textArg(args, "outcome"), textArg(args, "note")
	if !(outcome == "won" || outcome == "lost" || outcome == "void") || len(note) < 5 || len(note) > 2000 {
		return nil, fail("invalid_settlement", 400, "Choose won, lost or void, and provide a settlement note")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var bankroll, status string
	var stake, odds int64
	err = tx.QueryRow("SELECT bankroll_id,status,stake_minor,odds_micros FROM bets WHERE project_id=? AND id=?", project, id).Scan(&bankroll, &status, &stake, &odds)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", 404, "Bet not found")
	}
	if err != nil {
		return nil, err
	}
	if status == outcome {
		return map[string]any{"id": id, "status": status, "replayed": true}, nil
	}
	if status != "open" {
		return nil, fail("already_settled", 409, "Paper settlement is immutable; bet is already settled")
	}
	returns := int64(0)
	if outcome == "won" {
		returns, err = payout(stake, odds)
		if err != nil {
			return nil, err
		}
	} else if outcome == "void" {
		returns = stake
	}
	cash, locked, err := balances(tx, project, bankroll)
	if err != nil {
		return nil, err
	}
	if cash < 0 || locked < stake || locked > maxMoneyMinor || cash > maxMoneyMinor-locked || returns > maxMoneyMinor-cash-(locked-stake) {
		return nil, fail("accounting_range", 409, "Settlement exceeds supported accounting range")
	}
	now := a.clock()
	_, err = tx.Exec("UPDATE bets SET status=?,settled_at=?,settlement_note=? WHERE project_id=? AND id=?", outcome, now, note, project, id)
	if err != nil {
		return nil, err
	}
	if err = ledger(tx, project, bankroll, id+":settle", map[string]int64{"locked": -stake, "cash": returns, "pnl": stake - returns}, now); err != nil {
		return nil, err
	}
	if err = audit(tx, project, actor, "paper.bet."+outcome, id, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "status": outcome, "return_minor": returns, "profit_minor": returns - stake}, nil
}
