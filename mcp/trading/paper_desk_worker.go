package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func authorizePaperOrder(ctx *sdk.AppCtx, pf *Portfolio, args map[string]any, auth *paperOrderAuthorization) error {
	p, err := loadPaperDesk(ctx.AppDB(), pf.ID)
	if err != nil {
		return err
	}
	if p == nil || !p.Enabled {
		if auth != nil {
			return errors.New("paper desk is no longer enabled")
		}
		return nil
	}
	if pf.Mode != "paper" || pf.ExecutionEnvironment != "simulation" || pf.LiveArmed {
		return errors.New("paper desk cannot execute a broker/live order")
	}
	if auth == nil || auth.decision == nil {
		return errors.New("use desk_propose, independent desk_review, then desk_submit; raw order_place cannot bypass approval")
	}
	d, err := loadPaperDecision(ctx.AppDB(), pf.ID, auth.decision.Packet.DecisionID)
	if err != nil {
		return err
	}
	if d.ReviewRef == "" || d.ReviewRef != auth.decision.ReviewRef || d.ReviewerID != p.ReviewerID {
		return errors.New("stored independent approval does not match")
	}
	if !auth.exit {
		if d.Status != "approved" {
			return errors.New("entry is not approved")
		}
		canonical := paperEntryArgs(pf.ID, d.Packet)
		if orderIntentHash(canonical) != orderIntentHash(args) {
			return errors.New("order differs from immutable approved packet")
		}
		expires, err := time.Parse(time.RFC3339Nano, d.ApprovalExpiresAt)
		if err != nil || !time.Now().Before(expires) {
			return errors.New("approval expired")
		}
	} else {
		if !oneOfString(d.Status, "submitted", "active", "exiting") || strArg(args, "symbol") != d.Packet.Symbol || strArg(args, "outcome") != d.Packet.Outcome || strArg(args, "side") != "sell" || strArg(args, "type") != "market" || floatArg(args, "qty", 0) > d.Packet.Qty+1e-9 {
			return errors.New("exit differs from approved reduce-only plan")
		}
	}
	return nil
}

// Exit intent is durable in Trading's database and independent of agent wakes.
// The worker only executes already reviewed reduce-only stop/target/time exits.
func paperDeskTick(_ context.Context, ctx *sdk.AppCtx) error {
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	rows, err := ctx.AppDB().Query(`SELECT d.portfolio_id,d.decision_id FROM paper_decisions d JOIN paper_desks p ON p.portfolio_id=d.portfolio_id WHERE p.enabled=1 AND d.status IN ('submitted','active','exiting')`)
	if err != nil {
		return err
	}
	type key struct {
		pf int64
		id string
	}
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.pf, &k.id); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, k := range keys {
		d, err := loadPaperDecision(ctx.AppDB(), k.pf, k.id)
		if err == nil {
			err = advancePaperPlan(ctx, d)
		}
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE paper_decisions SET last_error=?,updated_at=? WHERE portfolio_id=? AND decision_id=?`, err.Error(), paperNow(), k.pf, k.id)
			ctx.Logger().Warn("paper desk exit/recovery pending", "portfolio_id", k.pf, "decision_id", k.id, "error", err)
		} else if d != nil && d.LastError != "" {
			_, _ = ctx.AppDB().Exec(`UPDATE paper_decisions SET last_error='',updated_at=? WHERE portfolio_id=? AND decision_id=?`, paperNow(), k.pf, k.id)
		}
	}
	return nil
}

func paperPlanState(ctx *sdk.AppCtx, d *paperDecision, status, reason string) error {
	_, err := ctx.AppDB().Exec(`UPDATE paper_decisions SET status=?,exit_reason=?,last_error='',updated_at=? WHERE portfolio_id=? AND decision_id=?`, status, reason, paperNow(), d.PortfolioID, d.Packet.DecisionID)
	return err
}

func cancelPaperEntryRemainder(ctx *sdk.AppCtx, pf *Portfolio, entry *Order, reason string) (*Order, error) {
	orderPlacementMu.Lock()
	defer orderPlacementMu.Unlock()
	if _, err := dbCancelOrder(ctx.AppDB(), pf.ProjectID, entry.ID, reason); err != nil {
		return nil, err
	}
	return dbGetOrder(ctx.AppDB(), pf.ProjectID, entry.ID)
}

func advancePaperPlan(ctx *sdk.AppCtx, d *paperDecision) error {
	pf, err := dbGetPortfolioAnyProject(ctx.AppDB(), d.PortfolioID)
	if err != nil {
		return err
	}
	if pf.Mode != "paper" || pf.ExecutionEnvironment != "simulation" || pf.LiveArmed {
		return errors.New("paper exit blocked: portfolio environment changed")
	}
	entry, err := dbGetOrder(ctx.AppDB(), pf.ProjectID, d.OrderID)
	if err != nil {
		return err
	}
	outcome := polyOutcome(entry)
	if entry.PortfolioID != pf.ID || entry.Symbol != d.Packet.Symbol || outcome != strings.ToUpper(d.Packet.Outcome) || !isBuySide(entry.Side) || entry.FilledQty > d.Packet.Qty+1e-9 {
		return errors.New("entry differs from approved paper plan")
	}
	if entry.Status == "working" {
		expires, err := time.Parse(time.RFC3339Nano, d.Packet.EntryExpiresAt)
		if err != nil {
			return err
		}
		if !time.Now().Before(expires) {
			entry, err = cancelPaperEntryRemainder(ctx, pf, entry, "paper desk entry expired")
			if err != nil {
				return err
			}
			if entry.FilledQty == 0 {
				return paperPlanState(ctx, d, "cancelled", "entry expired")
			}
		}
	}
	if entry.FilledQty == 0 {
		if oneOfString(entry.Status, "cancelled", "rejected") {
			return paperPlanState(ctx, d, entry.Status, "entry "+entry.Status)
		}
		return nil
	}
	if d.ExitOrderID != "" {
		exit, err := dbGetOrder(ctx.AppDB(), pf.ProjectID, d.ExitOrderID)
		if err != nil {
			return err
		}
		if exit.Status == "filled" {
			if err = paperPlanState(ctx, d, "closed", d.ExitReason); err != nil {
				return err
			}
			_, err = dbInsertJournal(ctx.AppDB(), pf.ProjectID, pf.ID, "desk_closed", "Paper plan "+d.Packet.DecisionID+" completed: "+d.ExitReason, map[string]any{"decision_id": d.Packet.DecisionID, "purpose": d.Packet.Purpose, "entry_order_id": d.OrderID, "exit_order_id": d.ExitOrderID})
			return err
		}
		if oneOfString(exit.Status, "cancelled", "rejected") {
			return fmt.Errorf("approved exit %s is %s; requires reconciliation", exit.ID, exit.Status)
		}
		return nil
	}
	if d.ExitAt == "" {
		var at string
		if err = ctx.AppDB().QueryRow(`SELECT MIN(filled_at) FROM fills WHERE order_id=?`, entry.ID).Scan(&at); err != nil {
			return err
		}
		filledAt, err := parseOrderTime(at)
		if err != nil {
			return err
		}
		d.ExitAt = filledAt.Add(time.Duration(d.Packet.MaxHoldingMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
		_, err = ctx.AppDB().Exec(`UPDATE paper_decisions SET status='active',exit_at=?,updated_at=? WHERE portfolio_id=? AND decision_id=?`, d.ExitAt, paperNow(), pf.ID, d.Packet.DecisionID)
		if err != nil {
			return err
		}
		d.Status = "active"
	}
	if pf.Status != "active" {
		return errors.New("approved exit pending: portfolio paused or halted")
	}
	mark, err := dbGetMark(ctx.AppDB(), d.Packet.Symbol)
	if err != nil {
		return err
	}
	mark, err = paperTradeMark(mark, d.Packet.Symbol, d.Packet.Outcome)
	if err != nil {
		return fmt.Errorf("approved exit waiting for executable market data: %w", err)
	}
	price := markPriceForSide(mark, outcome)
	if mark.BidPrice != nil && *mark.BidPrice > 0 {
		q := *mark
		q.MarkedAt = mark.QuoteAt
		if mark.QuoteAt != "" && markFresh(&q, time.Now()) {
			price = *mark.BidPrice
		}
	}
	reason := d.ExitReason
	if reason == "" {
		if price <= d.Packet.StopLoss {
			reason = "stop_loss"
		} else if price >= d.Packet.TakeProfit {
			reason = "take_profit"
		} else if due, err := time.Parse(time.RFC3339Nano, d.ExitAt); err != nil {
			return err
		} else if !time.Now().Before(due) {
			reason = "time_exit"
		}
		if reason == "" {
			return nil
		}
		// Keep the trigger even if submission fails or the price recovers.
		if _, err := ctx.AppDB().Exec(`UPDATE paper_decisions SET exit_reason=?,updated_at=? WHERE portfolio_id=? AND decision_id=?`, reason, paperNow(), pf.ID, d.Packet.DecisionID); err != nil {
			return err
		}
	}
	if entry.Status == "working" {
		// An exit owns only fills already recorded for this entry. Stop any
		// further entry fills before computing the protected quantity.
		entry, err = cancelPaperEntryRemainder(ctx, pf, entry, "approved paper exit triggered")
		if err != nil {
			return err
		}
	}
	pos, err := dbGetPosition(ctx.AppDB(), pf.ID, d.Packet.Symbol, outcome)
	if err != nil {
		return err
	}
	if pos == nil || pos.Qty < entry.FilledQty-1e-9 {
		return errors.New("position changed outside this plan; reconcile before automated exit")
	}
	args := map[string]any{"_project_id": pf.ProjectID, "portfolio_id": pf.ID, "symbol": d.Packet.Symbol, "side": "sell", "type": "market", "qty": entry.FilledQty, "tif": "gtc", "decision_ref": d.Packet.DecisionID, "idempotency_key": "desk:" + d.Packet.DecisionID + ":exit", "rationale": "Preapproved paper plan " + d.Packet.DecisionID + " automatic reduce-only " + reason}
	if d.Packet.Outcome != "" {
		args["outcome"] = d.Packet.Outcome
	}
	out, err := (&App{}).placeOrder(ctx, args, &paperOrderAuthorization{decision: d, exit: true, exitReason: reason})
	if err != nil {
		return err
	}
	r, ok := out.(map[string]any)
	if !ok {
		return errors.New("invalid automatic exit response")
	}
	oid, _ := r["order_id"].(string)
	if oid == "" {
		return fmt.Errorf("automatic exit rejected: %v", r)
	}
	_, err = ctx.AppDB().Exec(`UPDATE paper_decisions SET status='exiting',exit_order_id=?,exit_reason=?,last_error='',updated_at=? WHERE portfolio_id=? AND decision_id=?`, oid, reason, paperNow(), pf.ID, d.Packet.DecisionID)
	return err
}
