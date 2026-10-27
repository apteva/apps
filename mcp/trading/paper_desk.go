package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

var paperDeskMu sync.Mutex

type paperDeskPolicy struct {
	PortfolioID      int64   `json:"portfolio_id"`
	ProjectID        string  `json:"project_id"`
	ManagerID        int64   `json:"manager_id"`
	ReviewerID       int64   `json:"reviewer_id"`
	ExecutorID       int64   `json:"executor_id"`
	MaxPlannedLoss   float64 `json:"max_planned_loss"`
	MaxOrderNotional float64 `json:"max_order_notional"`
	MinCashPct       float64 `json:"min_cash_pct"`
	Enabled          bool    `json:"enabled"`
}

type paperPacket struct {
	DecisionID        string   `json:"decision_id"`
	Symbol            string   `json:"symbol"`
	Qty               float64  `json:"qty"`
	Type              string   `json:"type"`
	TIF               string   `json:"tif"`
	LimitPrice        float64  `json:"limit_price,omitempty"`
	ReferencePrice    float64  `json:"reference_price"`
	ReferenceAt       string   `json:"reference_at"`
	StopLoss          float64  `json:"stop_loss"`
	TakeProfit        float64  `json:"take_profit"`
	MaxHoldingMinutes int      `json:"max_holding_minutes"`
	EntryExpiresAt    string   `json:"entry_expires_at"`
	Setup             string   `json:"setup"`
	Purpose           string   `json:"purpose"`
	Thesis            string   `json:"thesis"`
	Evidence          []string `json:"evidence"`
}

type paperDecision struct {
	PortfolioID       int64       `json:"portfolio_id"`
	Packet            paperPacket `json:"packet"`
	PacketHash        string      `json:"packet_hash"`
	Status            string      `json:"status"`
	ManagerID         int64       `json:"manager_id"`
	ReviewerID        int64       `json:"reviewer_id"`
	ReviewRef         string      `json:"risk_review_ref"`
	ReviewReason      string      `json:"review_reason"`
	ApprovedAt        string      `json:"approved_at,omitempty"`
	ApprovalExpiresAt string      `json:"approval_expires_at,omitempty"`
	OrderID           string      `json:"order_id,omitempty"`
	ExitOrderID       string      `json:"exit_order_id,omitempty"`
	ExitAt            string      `json:"exit_at,omitempty"`
	ExitReason        string      `json:"exit_reason,omitempty"`
	LastError         string      `json:"last_error,omitempty"`
	CreatedAt         string      `json:"created_at"`
	UpdatedAt         string      `json:"updated_at"`
}

// This capability is a Go value, never a model-supplied argument. It can only
// be constructed after authenticated review/submission or by the exit worker.
type paperOrderAuthorization struct {
	decision   *paperDecision
	exit       bool
	exitReason string
}

func paperNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func loadPaperDesk(db *sql.DB, pf int64) (*paperDeskPolicy, error) {
	p := &paperDeskPolicy{}
	err := db.QueryRow(`SELECT portfolio_id,project_id,manager_id,reviewer_id,executor_id,max_planned_loss,max_order_notional,min_cash_pct,enabled FROM paper_desks WHERE portfolio_id=?`, pf).Scan(&p.PortfolioID, &p.ProjectID, &p.ManagerID, &p.ReviewerID, &p.ExecutorID, &p.MaxPlannedLoss, &p.MaxOrderNotional, &p.MinCashPct, &p.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func loadPaperDecision(db *sql.DB, pf int64, id string) (*paperDecision, error) {
	d := &paperDecision{PortfolioID: pf}
	var raw string
	err := db.QueryRow(`SELECT packet_json,packet_hash,status,manager_id,reviewer_id,review_ref,review_reason,approved_at,approval_expires_at,order_id,exit_order_id,exit_at,exit_reason,last_error,created_at,updated_at FROM paper_decisions WHERE portfolio_id=? AND decision_id=?`, pf, id).Scan(&raw, &d.PacketHash, &d.Status, &d.ManagerID, &d.ReviewerID, &d.ReviewRef, &d.ReviewReason, &d.ApprovedAt, &d.ApprovalExpiresAt, &d.OrderID, &d.ExitOrderID, &d.ExitAt, &d.ExitReason, &d.LastError, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &d.Packet); err != nil {
		return nil, err
	}
	return d, nil
}

func paperScope(ctx *sdk.AppCtx, args map[string]any) (*Portfolio, *paperDeskPolicy, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, nil, err
	}
	p, err := loadPaperDesk(ctx.AppDB(), pf.ID)
	if err != nil {
		return nil, nil, err
	}
	if p == nil || !p.Enabled {
		return nil, nil, errors.New("paper desk is not enabled for this portfolio")
	}
	if pf.Mode != "paper" || normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug) != "simulation" || pf.LiveArmed {
		return nil, nil, errors.New("paper desk only supports unarmed paper simulation")
	}
	return pf, p, nil
}

func paperActor(call context.Context, p *paperDeskPolicy, role string) (int64, error) {
	c := sdk.CallerFrom(call)
	want := p.ManagerID
	if role == "review" {
		want = p.ReviewerID
	}
	if role == "execute" {
		want = p.ExecutorID
	}
	if c == nil || c.AgentID <= 0 || c.AgentID != want || (c.ProjectID != "" && c.ProjectID != p.ProjectID) {
		return 0, fmt.Errorf("authenticated %s agent required", role)
	}
	return c.AgentID, nil
}

func (a *App) paperDeskTools() []sdk.Tool {
	props := func(extra map[string]any) map[string]any {
		m := map[string]any{"portfolio_id": map[string]any{"type": "integer"}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "number"} }
	return []sdk.Tool{
		{Name: "desk_configure", Description: "Operator: enable an authenticated paper desk with manager, reviewer, executor and bounded experiment policy.", InputSchema: schemaObject(props(map[string]any{"manager_id": num(), "reviewer_id": num(), "executor_id": num(), "max_planned_loss": num(), "max_order_notional": num(), "min_cash_pct": num()}), []string{"portfolio_id", "manager_id", "reviewer_id", "executor_id"}), Handler: a.toolPaperConfigure},
		{Name: "desk_propose", Description: "Manager: persist an immutable long-only paper experiment with exact entry, loss budget and automatic stop/target/time exits. Unknown predictive edge is allowed; no performance guarantee is required.", InputSchema: schemaObject(props(map[string]any{"decision_id": str(), "symbol": str(), "qty": num(), "type": map[string]any{"type": "string", "enum": []string{"market", "limit"}}, "tif": str(), "limit_price": num(), "stop_loss": num(), "take_profit": num(), "max_holding_minutes": num(), "setup": str(), "purpose": map[string]any{"type": "string", "enum": []string{"experiment", "rehearsal"}}, "thesis": str(), "evidence": map[string]any{"type": "array", "items": str()}}), []string{"portfolio_id", "decision_id", "symbol", "qty", "stop_loss", "take_profit", "setup", "thesis"}), HandlerCtx: a.toolPaperPropose},
		{Name: "desk_review", Description: "Independent reviewer: approve or reject a stored immutable packet. Approval is bound to packet hash, authenticated reviewer, fresh quote and 15-minute expiry.", InputSchema: schemaObject(props(map[string]any{"decision_id": str(), "verdict": map[string]any{"type": "string", "enum": []string{"approved", "rejected"}}, "reason": str()}), []string{"portfolio_id", "decision_id", "verdict", "reason"}), HandlerCtx: a.toolPaperReview},
		{Name: "desk_submit", Description: "Executor: submit the exact stored approved packet by decision ID and risk_review_ref. Do not pass changed order terms. Safe retries return the same order. Trading owns durable preapproved exits.", InputSchema: schemaObject(props(map[string]any{"decision_id": str(), "risk_review_ref": str()}), []string{"portfolio_id", "decision_id", "risk_review_ref"}), HandlerCtx: a.toolPaperSubmit},
		{Name: "desk_cancel", Description: "Executor: cancel a decision's unfilled entry. Filled entries and preapproved automatic exits cannot be cancelled through this tool.", InputSchema: schemaObject(props(map[string]any{"decision_id": str(), "reason": str()}), []string{"portfolio_id", "decision_id", "reason"}), HandlerCtx: a.toolPaperCancel},
		{Name: "desk_decision_get", Description: "Read a stored proposal, approval, entry and automatic-exit status.", InputSchema: schemaObject(props(map[string]any{"decision_id": str()}), []string{"portfolio_id", "decision_id"}), Handler: a.toolPaperGet},
		{Name: "desk_observe", Description: "Record a timestamped setup observation or hold with a stable ID and explicit reason; these are not realized trades or P&L.", InputSchema: schemaObject(props(map[string]any{"observation_id": str(), "setup": str(), "symbol": str(), "verdict": map[string]any{"type": "string", "enum": []string{"hold", "setup_present", "data_unavailable"}}, "reason": str()}), []string{"portfolio_id", "observation_id", "setup", "symbol", "verdict", "reason"}), Handler: a.toolPaperObserve},
		{Name: "desk_scorecard", Description: "Read experiment/rehearsal counts, actual cash-flow P&L after fees, observations, pending exits and execution errors. Rehearsals are excluded from experiment performance.", InputSchema: schemaObject(props(nil), []string{"portfolio_id"}), Handler: a.toolPaperScorecard},
		{Name: "desk_health", Description: "Check paper mode, balances, positions, orders, executable symbols and durable exit errors before a desk cycle. Closed/stale symbols are reported individually.", InputSchema: schemaObject(props(nil), []string{"portfolio_id"}), Handler: a.toolPaperHealth},
		{Name: "desk_market_context", Description: "Read deterministic completed-bar facts for a symbol: interval, latest/prior OHLCV, SMA20/50 and previous range/volume. No trade signals or profitability claims.", InputSchema: schemaObject(props(map[string]any{"symbol": str()}), []string{"portfolio_id", "symbol"}), Handler: a.toolPaperMarketContext},
	}
}

func (a *App) toolPaperConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	if pf.Mode != "paper" || normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug) != "simulation" || pf.LiveArmed {
		return nil, errors.New("paper simulation portfolio required")
	}
	p := paperDeskPolicy{PortfolioID: pf.ID, ProjectID: pid, ManagerID: int64Arg(args, "manager_id", 0), ReviewerID: int64Arg(args, "reviewer_id", 0), ExecutorID: int64Arg(args, "executor_id", 0), MaxPlannedLoss: floatArg(args, "max_planned_loss", 25), MaxOrderNotional: floatArg(args, "max_order_notional", 1000), MinCashPct: floatArg(args, "min_cash_pct", 60), Enabled: true}
	if p.ManagerID <= 0 || p.ReviewerID <= 0 || p.ExecutorID <= 0 || p.ManagerID == p.ReviewerID || p.ReviewerID == p.ExecutorID || p.ManagerID == p.ExecutorID {
		return nil, errors.New("three distinct authenticated agent identities required")
	}
	if !finite(p.MaxPlannedLoss) || p.MaxPlannedLoss <= 0 || !finite(p.MaxOrderNotional) || p.MaxOrderNotional <= 0 || p.MaxOrderNotional > 1000 || !finite(p.MinCashPct) || p.MinCashPct < 60 || p.MinCashPct > 100 {
		return nil, errors.New("invalid bounded experiment policy")
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO paper_desks(portfolio_id,project_id,manager_id,reviewer_id,executor_id,max_planned_loss,max_order_notional,min_cash_pct) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(portfolio_id) DO UPDATE SET manager_id=excluded.manager_id,reviewer_id=excluded.reviewer_id,executor_id=excluded.executor_id,max_planned_loss=excluded.max_planned_loss,max_order_notional=excluded.max_order_notional,min_cash_pct=excluded.min_cash_pct,enabled=1`, pf.ID, pid, p.ManagerID, p.ReviewerID, p.ExecutorID, p.MaxPlannedLoss, p.MaxOrderNotional, p.MinCashPct)
	return map[string]any{"policy": p}, err
}

func paperEntryArgs(pf int64, p paperPacket) map[string]any {
	args := map[string]any{"portfolio_id": pf, "symbol": p.Symbol, "side": "buy", "type": p.Type, "qty": p.Qty, "tif": p.TIF, "decision_ref": p.DecisionID, "idempotency_key": "desk:" + p.DecisionID + ":entry", "rationale": p.Thesis}
	if p.LimitPrice > 0 {
		args["limit_price"] = p.LimitPrice
	}
	return args
}

func paperPreflight(ctx *sdk.AppCtx, pf *Portfolio, policy *paperDeskPolicy, p paperPacket, mark *Mark) error {
	if pf.Status != "active" {
		return errors.New("portfolio is not active")
	}
	if mark == nil || !markFresh(mark, time.Now()) || mark.Source == "mock" {
		return errors.New("fresh non-synthetic market data required")
	}
	if !oneOfString(p.Symbol, "SPY", "QQQ", "BTC-USD", "ETH-USD") || !finite(p.Qty) || p.Qty <= 0 || !finite(p.StopLoss) || !finite(p.TakeProfit) || p.StopLoss <= 0 {
		return errors.New("invalid paper experiment terms")
	}
	price := mark.Price
	if mark.AskPrice != nil {
		price = *mark.AskPrice
	}
	riskPrice := price
	if p.LimitPrice > 0 {
		riskPrice = max(price, p.LimitPrice)
	}
	entry := price
	if p.LimitPrice > 0 {
		entry = p.LimitPrice
	}
	if p.StopLoss >= entry || p.TakeProfit <= entry {
		return errors.New("stop must be below entry and target above entry")
	}
	if p.Type != "market" && p.Type != "limit" {
		return errors.New("market or limit entry required")
	}
	if p.Type == "limit" && p.LimitPrice <= 0 {
		return errors.New("limit entry needs limit_price")
	}
	if !finite(riskPrice) || riskPrice*p.Qty > policy.MaxOrderNotional+1e-6 {
		return errors.New("paper order exceeds notional cap")
	}
	profile := resolveVenueProfile(ctx.AppDB(), pf, p.Symbol, inferAssetClass(p.Symbol))
	if v := validateExecutionOrder(profile, mark.Instrument, mark, p.Qty, riskPrice); v != nil {
		return fmt.Errorf("%s: %s", v.Code, v.Detail)
	}
	if p.LimitPrice > 0 {
		if v := validatePriceTick(profile, p.LimitPrice, "limit_price"); v != nil {
			return errors.New(v.Detail)
		}
	}
	spread := 0.0
	if mark.BidPrice != nil && mark.AskPrice != nil {
		spread = max(0, *mark.AskPrice-*mark.BidPrice)
	}
	// A planning allowance, not a guarantee: gaps can exceed a stop price.
	planned := max(0, riskPrice-p.StopLoss)*p.Qty + riskPrice*p.Qty*2*(profile.TakerFeeBps+profile.SlippageBps)/10000 + spread*p.Qty
	if planned > policy.MaxPlannedLoss+1e-6 {
		return fmt.Errorf("planned loss %.2f exceeds budget %.2f", planned, policy.MaxPlannedLoss)
	}
	if v, err := portfolioUniverseViolation(ctx.AppDB(), pf, p.Symbol); err != nil {
		return err
	} else if v != nil {
		return errors.New(v.Detail)
	}
	if v, err := preTradeRiskCheck(ctx.AppDB(), pf, p.Symbol, "buy", p.Qty, riskPrice); err != nil {
		return err
	} else if v != nil {
		return errors.New(v.Detail)
	}
	equity, err := computeEquity(ctx.AppDB(), pf)
	if err != nil {
		return err
	}
	working, err := workingPortfolioOrders(ctx.AppDB(), pf.ID)
	if err != nil {
		return err
	}
	reserved := 0.0
	for _, o := range working {
		if isBuySide(o.Side) {
			m, err := dbGetMark(ctx.AppDB(), o.Symbol)
			if err != nil {
				return err
			}
			px := m.Price
			if o.LimitPrice != nil {
				px = max(px, *o.LimitPrice)
			}
			reserved += (o.Qty - o.FilledQty) * px * (1 + (profile.TakerFeeBps+profile.SlippageBps)/10000)
		}
	}
	if pf.Cash-reserved-riskPrice*p.Qty*(1+(profile.TakerFeeBps+profile.SlippageBps)/10000) < equity*policy.MinCashPct/100-1e-6 {
		return errors.New("cash reserve would fall below policy")
	}
	return nil
}

func (a *App) toolPaperPropose(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, policy, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	actor, err := paperActor(call, policy, "propose")
	if err != nil {
		return nil, err
	}
	p := paperPacket{DecisionID: strings.TrimSpace(strArg(args, "decision_id")), Symbol: canonicalSymbol(strArg(args, "symbol")), Qty: floatArg(args, "qty", 0), Type: strArg(args, "type"), TIF: strArg(args, "tif"), LimitPrice: floatArg(args, "limit_price", 0), StopLoss: floatArg(args, "stop_loss", 0), TakeProfit: floatArg(args, "take_profit", 0), MaxHoldingMinutes: intArg(args, "max_holding_minutes", 240), Setup: strings.TrimSpace(strArg(args, "setup")), Purpose: strArg(args, "purpose"), Thesis: strings.TrimSpace(strArg(args, "thesis"))}
	if p.Type == "" {
		p.Type = "market"
	}
	if p.TIF == "" {
		p.TIF = "gtc"
	}
	if p.Purpose == "" {
		p.Purpose = "experiment"
	}
	raw, _ := json.Marshal(args["evidence"])
	_ = json.Unmarshal(raw, &p.Evidence)
	if p.DecisionID == "" || len(p.DecisionID) > 160 || p.Setup == "" || len(p.Thesis) < 30 || p.MaxHoldingMinutes < 1 || p.MaxHoldingMinutes > 1440 || !oneOfString(p.Purpose, "experiment", "rehearsal") || !oneOfString(p.TIF, "gtc", "day", "ioc", "fok") {
		return nil, errors.New("decision ID, setup, substantive hypothesis and holding period 1..1440 minutes required")
	}
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	if existing, err := loadPaperDecision(ctx.AppDB(), pf.ID, p.DecisionID); err == nil {
		// Reference timestamps are server-owned; compare only submitted terms.
		p.ReferencePrice, p.ReferenceAt, p.EntryExpiresAt = existing.Packet.ReferencePrice, existing.Packet.ReferenceAt, existing.Packet.EntryExpiresAt
		got, _ := json.Marshal(p)
		want, _ := json.Marshal(existing.Packet)
		if string(got) != string(want) {
			return nil, errors.New("decision ID already exists with different immutable terms")
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	mark, err := ensureExecutableMark(ctx, pf, p.Symbol)
	if err != nil {
		return nil, err
	}
	if err = paperPreflight(ctx, pf, policy, p, mark); err != nil {
		return nil, err
	}
	p.ReferencePrice = mark.Price
	p.ReferenceAt = mark.MarkedAt
	p.EntryExpiresAt = time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano)
	raw, _ = json.Marshal(p)
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	now := paperNow()
	_, err = ctx.AppDB().Exec(`INSERT INTO paper_decisions(portfolio_id,decision_id,packet_json,packet_hash,status,manager_id,created_at,updated_at) VALUES(?,?,?,?,'proposed',?,?,?)`, pf.ID, p.DecisionID, string(raw), hash, actor, now, now)
	if err != nil {
		return nil, err
	}
	return loadPaperDecision(ctx.AppDB(), pf.ID, p.DecisionID)
}

func (a *App) toolPaperReview(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, policy, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	actor, err := paperActor(call, policy, "review")
	if err != nil {
		return nil, err
	}
	verdict, reason := strArg(args, "verdict"), strings.TrimSpace(strArg(args, "reason"))
	if !oneOfString(verdict, "approved", "rejected") || len(reason) < 20 {
		return nil, errors.New("approved/rejected verdict and substantive review reason required")
	}
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	d, err := loadPaperDecision(ctx.AppDB(), pf.ID, strArg(args, "decision_id"))
	if err != nil {
		return nil, err
	}
	if actor == d.ManagerID {
		return nil, errors.New("independent reviewer required")
	}
	if d.Status != "proposed" {
		if d.ReviewerID == actor && d.Status == verdict {
			return d, nil
		}
		return nil, errors.New("decision already reviewed; use a new decision for changed terms")
	}
	now := time.Now().UTC()
	expiry, _ := time.Parse(time.RFC3339Nano, d.Packet.EntryExpiresAt)
	if !now.Before(expiry) {
		return nil, errors.New("proposal expired; create a fresh decision")
	}
	ref := ""
	if verdict == "approved" {
		mark, err := ensureExecutableMark(ctx, pf, d.Packet.Symbol)
		if err != nil {
			return nil, err
		}
		if err = paperPreflight(ctx, pf, policy, d.Packet, mark); err != nil {
			return nil, err
		}
		if math.Abs(mark.Price/d.Packet.ReferencePrice-1) > .005 {
			return nil, errors.New("reference price moved over 0.5%; create a fresh decision")
		}
		ref = "review-" + uuid.NewString()
	}
	_, err = ctx.AppDB().Exec(`UPDATE paper_decisions SET status=?,reviewer_id=?,review_ref=?,review_reason=?,approved_at=?,approval_expires_at=?,updated_at=? WHERE portfolio_id=? AND decision_id=? AND status='proposed'`, verdict, actor, ref, reason, now.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano), paperNow(), pf.ID, d.Packet.DecisionID)
	if err != nil {
		return nil, err
	}
	return loadPaperDecision(ctx.AppDB(), pf.ID, d.Packet.DecisionID)
}

func (a *App) toolPaperSubmit(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, policy, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	if _, err = paperActor(call, policy, "execute"); err != nil {
		return nil, err
	}
	for k := range args {
		if !strings.HasPrefix(k, "_") && !oneOfString(k, "portfolio_id", "decision_id", "risk_review_ref") {
			return nil, errors.New("submit stored terms only; changed order terms require a new proposal and review")
		}
	}
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	d, err := loadPaperDecision(ctx.AppDB(), pf.ID, strArg(args, "decision_id"))
	if err != nil {
		return nil, err
	}
	if d.ReviewRef == "" || strArg(args, "risk_review_ref") != d.ReviewRef || d.ReviewerID != policy.ReviewerID {
		return nil, errors.New("matching independent stored risk approval required")
	}
	entryArgs := paperEntryArgs(pf.ID, d.Packet)
	// Recover an accepted intent even after a crash between order insertion and
	// decision linking. Retrying never creates another order after expiry.
	prior, err := previousOrderRequest(ctx.AppDB(), pf.ProjectID, pf.ID, strArg(entryArgs, "idempotency_key"), orderIntentHash(entryArgs))
	if err != nil {
		return nil, err
	}
	if prior != nil {
		oid := fmt.Sprint(prior["order_id"])
		_, err = ctx.AppDB().Exec(`UPDATE paper_decisions SET order_id=?,status=CASE WHEN status='approved' THEN 'submitted' ELSE status END,updated_at=? WHERE portfolio_id=? AND decision_id=?`, oid, paperNow(), pf.ID, d.Packet.DecisionID)
		return prior, err
	}
	if d.Status != "approved" {
		return nil, errors.New("decision is not approved")
	}
	expires, err := time.Parse(time.RFC3339Nano, d.ApprovalExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return nil, errors.New("risk approval expired")
	}
	mark, err := ensureExecutableMark(ctx, pf, d.Packet.Symbol)
	if err != nil {
		return nil, err
	}
	if math.Abs(mark.Price/d.Packet.ReferencePrice-1) > .005 {
		return nil, errors.New("price moved over 0.5%; new review required")
	}
	if err = paperPreflight(ctx, pf, policy, d.Packet, mark); err != nil {
		return nil, err
	}
	out, err := a.placeOrder(ctx, entryArgs, &paperOrderAuthorization{decision: d})
	if err != nil {
		return nil, err
	}
	r, ok := out.(map[string]any)
	if !ok {
		return out, nil
	}
	oid, _ := r["order_id"].(string)
	if oid == "" {
		return out, nil
	}
	_, err = ctx.AppDB().Exec(`UPDATE paper_decisions SET order_id=?,status='submitted',updated_at=? WHERE portfolio_id=? AND decision_id=?`, oid, paperNow(), pf.ID, d.Packet.DecisionID)
	return out, err
}

func (a *App) toolPaperGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, _, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	return loadPaperDecision(ctx.AppDB(), pf.ID, strArg(args, "decision_id"))
}

func (a *App) toolPaperCancel(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, p, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	if _, err = paperActor(call, p, "execute"); err != nil {
		return nil, err
	}
	paperDeskMu.Lock()
	defer paperDeskMu.Unlock()
	d, err := loadPaperDecision(ctx.AppDB(), pf.ID, strArg(args, "decision_id"))
	if err != nil {
		return nil, err
	}
	if d.OrderID == "" {
		return nil, errors.New("entry has not been submitted")
	}
	o, err := dbGetOrder(ctx.AppDB(), pf.ProjectID, d.OrderID)
	if err != nil {
		return nil, err
	}
	if o.FilledQty > 0 || o.Status == "filled" {
		return nil, errors.New("filled entry retains its approved exit plan")
	}
	status, err := dbCancelOrder(ctx.AppDB(), pf.ProjectID, d.OrderID, strArg(args, "reason"))
	if err != nil {
		return nil, err
	}
	if status == "cancelled" {
		err = paperPlanState(ctx, d, "cancelled", "entry cancelled by executor")
	}
	return map[string]any{"order_id": d.OrderID, "status": status}, err
}

func (a *App) toolPaperObserve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, _, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	id, setup, symbol, verdict, reason := strArg(args, "observation_id"), strArg(args, "setup"), canonicalSymbol(strArg(args, "symbol")), strArg(args, "verdict"), strings.TrimSpace(strArg(args, "reason"))
	if id == "" || setup == "" || !oneOfString(symbol, "SPY", "QQQ", "BTC-USD", "ETH-USD") || !oneOfString(verdict, "hold", "setup_present", "data_unavailable") || len(reason) < 20 {
		return nil, errors.New("stable observation, setup, symbol, verdict and substantive reason required")
	}
	price := 0.0
	if m, err := dbGetMark(ctx.AppDB(), symbol); err == nil && markFresh(m, time.Now()) {
		price = m.Price
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO paper_observations(portfolio_id,observation_id,setup,symbol,verdict,reason,reference_price,observed_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, pf.ID, id, setup, symbol, verdict, reason, price, paperNow())
	return map[string]any{"observation_id": id, "recorded": err == nil, "hypothetical": true}, err
}

func (a *App) toolPaperScorecard(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, policy, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT decision_id FROM paper_decisions WHERE portfolio_id=? ORDER BY created_at`, pf.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	decisions := []map[string]any{}
	net, fees := 0.0, 0.0
	closed, wins := 0, 0
	for _, id := range ids {
		d, err := loadPaperDecision(ctx.AppDB(), pf.ID, id)
		if err != nil {
			return nil, err
		}
		pnl, fee := 0.0, 0.0
		if d.OrderID != "" {
			err = ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(CASE WHEN o.side='sell' THEN f.qty*f.price ELSE -f.qty*f.price END-f.fee),0),COALESCE(SUM(f.fee),0) FROM fills f JOIN orders o ON o.id=f.order_id WHERE o.id IN (?,?)`, d.OrderID, d.ExitOrderID).Scan(&pnl, &fee)
			if err != nil {
				return nil, err
			}
		}
		row := map[string]any{"decision": d, "fees_paid": fee, "performance_included": d.Packet.Purpose == "experiment" && d.Status == "closed"}
		if d.Status == "closed" {
			row["realized_cashflow_pnl"] = pnl
			if d.Packet.Purpose == "experiment" {
				net += pnl
				fees += fee
				closed++
				if pnl > 0 {
					wins++
				}
			}
		}
		decisions = append(decisions, row)
	}
	obs, err := ctx.AppDB().Query(`SELECT setup,verdict,COUNT(*) FROM paper_observations WHERE portfolio_id=? GROUP BY setup,verdict`, pf.ID)
	if err != nil {
		return nil, err
	}
	observations := []map[string]any{}
	for obs.Next() {
		var setup, verdict string
		var count int
		if err = obs.Scan(&setup, &verdict, &count); err != nil {
			obs.Close()
			return nil, err
		}
		observations = append(observations, map[string]any{"setup": setup, "verdict": verdict, "count": count})
	}
	err = obs.Err()
	obs.Close()
	return map[string]any{"policy": policy, "decisions": decisions, "observations": observations, "closed_experiments": closed, "winning_experiments": wins, "experiment_net_pnl": net, "experiment_fees": fees, "cash_benchmark_pnl": 0, "rehearsals_excluded": true, "planned_loss_is_not_guaranteed": true}, err
}
