package main

// Database layer. Every read + write the sidecar performs lands here;
// tools.go and exec.go call into this file. Thin wrappers over SQL —
// no business logic lives below this line.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── Domain types (mirror the JSON shapes returned by tools / REST) ──

type Portfolio struct {
	ID             int64    `json:"id"`
	ProjectID      string   `json:"project_id,omitempty"`
	Name           string   `json:"name"`
	AgentID        string   `json:"agent_id,omitempty"`
	Mandate        string   `json:"mandate"`
	AllowedClasses []string `json:"allowed_classes"`
	StartingCash   float64  `json:"starting_cash"`
	Cash           float64  `json:"cash"`
	Status         string   `json:"status"`
	Mode           string   `json:"mode"`
	CreatedAt      string   `json:"created_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`

	// Computed by snapshot — not stored.
	Equity        float64 `json:"equity,omitempty"`
	DayPnL        float64 `json:"day_pnl,omitempty"`
	DayPnLPct     float64 `json:"day_pnl_pct,omitempty"`
	OpenPnL       float64 `json:"open_pnl,omitempty"`
	OpenPnLPct    float64 `json:"open_pnl_pct,omitempty"`
	BuyingPower   float64 `json:"buying_power,omitempty"`
	Watchlist     []string  `json:"watchlist,omitempty"`
}

type Position struct {
	Symbol           string  `json:"symbol"`
	AssetClass       string  `json:"asset_class"`
	Outcome          string  `json:"outcome,omitempty"`
	Qty              float64 `json:"qty"`
	AvgCost          float64 `json:"avg_cost"`
	MarketPrice      float64 `json:"market_price"`
	MarketValue      float64 `json:"market_value"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	RealizedPnL      float64 `json:"realized_pnl"`
	DayPnL           float64 `json:"day_pnl"`
	WeightPct        float64 `json:"weight_pct"`
}

type Order struct {
	ID              string  `json:"id"`
	PortfolioID     int64   `json:"portfolio_id"`
	Symbol          string  `json:"symbol"`
	AssetClass      string  `json:"asset_class"`
	Side            string  `json:"side"`
	Type            string  `json:"type"`
	Qty             float64 `json:"qty"`
	FilledQty       float64 `json:"filled_qty"`
	AvgFillPrice    float64 `json:"avg_fill_price,omitempty"`
	LimitPrice      *float64 `json:"limit_price,omitempty"`
	StopPrice       *float64 `json:"stop_price,omitempty"`
	TIF             string  `json:"tif"`
	Status          string  `json:"status"`
	Rationale       string  `json:"rationale"`
	Source          string  `json:"source"`
	RejectionCode   string  `json:"rejection_code,omitempty"`
	RejectionDetail string  `json:"rejection_detail,omitempty"`
	PlacedAt        string  `json:"placed_at"`
	ResolvedAt      string  `json:"resolved_at,omitempty"`
}

type Fill struct {
	ID         int64   `json:"id"`
	OrderID    string  `json:"order_id"`
	Qty        float64 `json:"qty"`
	Price      float64 `json:"price"`
	Fee        float64 `json:"fee"`
	FilledAt   string  `json:"filled_at"`
}

type JournalEntry struct {
	ID          int64                  `json:"id"`
	PortfolioID int64                  `json:"portfolio_id"`
	Kind        string                 `json:"kind"`
	Body        string                 `json:"body"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
	CreatedAt   string                 `json:"created_at"`
}

type Mark struct {
	Symbol     string  `json:"symbol"`
	AssetClass string  `json:"asset_class"`
	Price      float64 `json:"price"`
	NoPrice    *float64 `json:"no_price,omitempty"`
	PrevClose  *float64 `json:"prev_close,omitempty"`
	Volume24h  *float64 `json:"volume_24h,omitempty"`
	MarkedAt   string  `json:"marked_at"`
}

type Alert struct {
	ID          int64   `json:"id"`
	PortfolioID int64   `json:"portfolio_id"`
	Symbol      string  `json:"symbol"`
	Rule        string  `json:"rule"`
	Threshold   float64 `json:"threshold"`
	Status      string  `json:"status"`
	ExpiresAt   string  `json:"expires_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	FiredAt     string  `json:"fired_at,omitempty"`
}

// ─── Portfolio ─────────────────────────────────────────────────────

func dbCreatePortfolio(db *sql.DB, p *Portfolio) (int64, error) {
	classesJSON, err := json.Marshal(p.AllowedClasses)
	if err != nil {
		return 0, err
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "paper"
	}
	res, err := db.Exec(`
		INSERT INTO portfolios (project_id, name, agent_id, mandate, allowed_classes, starting_cash, cash, mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ProjectID, p.Name, p.AgentID, p.Mandate, string(classesJSON), p.StartingCash, p.StartingCash, mode)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetPortfolio(db *sql.DB, projectID string, id int64) (*Portfolio, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, status, mode, created_at, updated_at
		FROM portfolios WHERE id = ? AND project_id = ?`, id, projectID)
	return scanPortfolio(row)
}

// dbGetPortfolioAnyProject is for the engine (which doesn't carry a
// project context) — every other read uses the project-scoped variant.
func dbGetPortfolioAnyProject(db *sql.DB, id int64) (*Portfolio, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, status, mode, created_at, updated_at
		FROM portfolios WHERE id = ?`, id)
	return scanPortfolio(row)
}

func dbListPortfolios(db *sql.DB, projectID string) ([]*Portfolio, error) {
	rows, err := db.Query(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, status, mode, created_at, updated_at
		FROM portfolios WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Portfolio
	for rows.Next() {
		p, err := scanPortfolioRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// dbAllPortfolios — engine sweep across every portfolio (e.g. tick).
func dbAllPortfolios(db *sql.DB) ([]*Portfolio, error) {
	rows, err := db.Query(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, status, mode, created_at, updated_at
		FROM portfolios ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Portfolio
	for rows.Next() {
		p, err := scanPortfolioRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPortfolio(row *sql.Row) (*Portfolio, error) {
	var p Portfolio
	var classesJSON string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.AgentID, &p.Mandate,
		&classesJSON, &p.StartingCash, &p.Cash, &p.Status, &p.Mode,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classesJSON), &p.AllowedClasses); err != nil {
		p.AllowedClasses = []string{"equity", "etf"}
	}
	return &p, nil
}

func scanPortfolioRows(rows *sql.Rows) (*Portfolio, error) {
	var p Portfolio
	var classesJSON string
	if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.AgentID, &p.Mandate,
		&classesJSON, &p.StartingCash, &p.Cash, &p.Status, &p.Mode,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classesJSON), &p.AllowedClasses); err != nil {
		p.AllowedClasses = []string{"equity", "etf"}
	}
	return &p, nil
}

func dbSetPortfolioStatus(db *sql.DB, id int64, status string) error {
	_, err := db.Exec(`UPDATE portfolios SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func dbAddCash(db *sql.DB, id int64, delta float64) error {
	_, err := db.Exec(`UPDATE portfolios SET cash = cash + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, delta, id)
	return err
}

// ─── Positions ─────────────────────────────────────────────────────

func dbListPositions(db *sql.DB, portfolioID int64) ([]*Position, error) {
	rows, err := db.Query(`
		SELECT symbol, asset_class, COALESCE(outcome, ''), qty, avg_cost, realized_pnl
		FROM positions WHERE portfolio_id = ? AND qty != 0
		ORDER BY ABS(qty * avg_cost) DESC`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.Symbol, &p.AssetClass, &p.Outcome,
			&p.Qty, &p.AvgCost, &p.RealizedPnL); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// dbInsertPositionRaw — direct insert without going through dbApplyFill.
// Used by the live-portfolio bootstrap seed: when we create a live
// portfolio, the broker's existing holdings come in as positions with
// no fill history. avg_cost is "best-known" (current mark or 0); the
// reconciler updates it on subsequent fills.
func dbInsertPositionRaw(db *sql.DB, projectID string, portfolioID int64, symbol, assetClass, outcome string, qty, avgCost float64) error {
	var outcomeArg any
	if outcome != "" {
		outcomeArg = outcome
	}
	_, err := db.Exec(`
		INSERT OR IGNORE INTO positions (project_id, portfolio_id, symbol, asset_class, outcome, qty, avg_cost)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, portfolioID, symbol, assetClass, outcomeArg, qty, avgCost)
	return err
}

func dbGetPosition(db *sql.DB, portfolioID int64, symbol, outcome string) (*Position, error) {
	row := db.QueryRow(`
		SELECT symbol, asset_class, COALESCE(outcome, ''), qty, avg_cost, realized_pnl
		FROM positions WHERE portfolio_id = ? AND symbol = ? AND COALESCE(outcome, '') = ?`,
		portfolioID, symbol, outcome)
	var p Position
	err := row.Scan(&p.Symbol, &p.AssetClass, &p.Outcome, &p.Qty, &p.AvgCost, &p.RealizedPnL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// dbApplyFill mutates a position to absorb a fill. Buys add qty
// (weighted-avg cost); sells reduce qty (realized P&L per share).
// Polymarket: 'yes' is a buy on the YES outcome; 'no' is a buy on NO.
// Selling polymarket exits an existing leg.
func dbApplyFill(tx *sql.Tx, portfolioID int64, projectID string, o *Order, qty, price float64) error {
	outcome := ""
	if o.AssetClass == "polymarket" {
		outcome = strings.ToUpper(o.Side) // YES or NO
	}

	// Read current position (if any).
	row := tx.QueryRow(`
		SELECT id, qty, avg_cost, realized_pnl
		FROM positions WHERE portfolio_id = ? AND symbol = ? AND COALESCE(outcome, '') = ?`,
		portfolioID, o.Symbol, outcome)
	var (
		posID                      int64
		curQty, curAvgCost, curRPL float64
		exists                     = true
	)
	if err := row.Scan(&posID, &curQty, &curAvgCost, &curRPL); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		exists = false
	}

	isBuy := o.Side == "buy" || o.Side == "yes" || o.Side == "no"
	if !isBuy {
		// Sell on equity/crypto: reduces an existing long position.
		if !exists || curQty < qty-1e-9 {
			return fmt.Errorf("cannot sell %v %s — only %v available", qty, o.Symbol, curQty)
		}
		realized := (price - curAvgCost) * qty
		newQty := curQty - qty
		if newQty < 1e-9 {
			// Close it.
			if _, err := tx.Exec(`DELETE FROM positions WHERE id = ?`, posID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`
				UPDATE positions SET qty = ?, realized_pnl = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`, newQty, curRPL+realized, posID); err != nil {
				return err
			}
		}
		// Sell credits cash (less fees, applied at the engine layer).
		if _, err := tx.Exec(`UPDATE portfolios SET cash = cash + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			qty*price, portfolioID); err != nil {
			return err
		}
		return nil
	}

	// Buy: weighted avg cost; debit cash.
	if exists {
		newQty := curQty + qty
		newAvg := (curQty*curAvgCost + qty*price) / newQty
		if _, err := tx.Exec(`
			UPDATE positions SET qty = ?, avg_cost = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`, newQty, newAvg, posID); err != nil {
			return err
		}
	} else {
		var outcomeArg any
		if outcome != "" {
			outcomeArg = outcome
		}
		if _, err := tx.Exec(`
			INSERT INTO positions (project_id, portfolio_id, symbol, asset_class, outcome, qty, avg_cost)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, portfolioID, o.Symbol, o.AssetClass, outcomeArg, qty, price); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE portfolios SET cash = cash - ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		qty*price, portfolioID); err != nil {
		return err
	}
	return nil
}

// ─── Orders ────────────────────────────────────────────────────────

func dbInsertOrder(db *sql.DB, o *Order, projectID string) error {
	_, err := db.Exec(`
		INSERT INTO orders (id, project_id, portfolio_id, symbol, asset_class, side, type,
		                    qty, limit_price, stop_price, tif, status, rationale, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, projectID, o.PortfolioID, o.Symbol, o.AssetClass, o.Side, o.Type,
		o.Qty, nullable(o.LimitPrice), nullable(o.StopPrice), o.TIF, o.Status, o.Rationale, o.Source)
	return err
}

func dbGetOrder(db *sql.DB, projectID, id string) (*Order, error) {
	row := db.QueryRow(`
		SELECT id, portfolio_id, symbol, asset_class, side, type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, '')
		FROM orders WHERE id = ? AND project_id = ?`, id, projectID)
	return scanOrder(row)
}

func dbListOrders(db *sql.DB, portfolioID int64, status string, limit int) ([]*Order, error) {
	q := `SELECT id, portfolio_id, symbol, asset_class, side, type, qty, filled_qty, avg_fill_price,
	             limit_price, stop_price, tif, status, rationale, source,
	             COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
	             placed_at, COALESCE(resolved_at, '')
	      FROM orders WHERE portfolio_id = ?`
	args := []any{portfolioID}
	if status != "" && status != "all" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY placed_at DESC LIMIT ?`
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrderRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// dbWorkingOrders — engine-side, no project filter (the engine sees all).
func dbWorkingOrders(db *sql.DB) ([]*Order, error) {
	rows, err := db.Query(`
		SELECT id, portfolio_id, symbol, asset_class, side, type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, '')
		FROM orders WHERE status = 'working' ORDER BY placed_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrderRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func scanOrder(row *sql.Row) (*Order, error) {
	var o Order
	var lp, sp sql.NullFloat64
	var resolvedAt string
	if err := row.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.AssetClass, &o.Side, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt); err != nil {
		return nil, err
	}
	if lp.Valid { v := lp.Float64; o.LimitPrice = &v }
	if sp.Valid { v := sp.Float64; o.StopPrice = &v }
	if resolvedAt != "" { o.ResolvedAt = resolvedAt }
	return &o, nil
}

func scanOrderRows(rows *sql.Rows) (*Order, error) {
	var o Order
	var lp, sp sql.NullFloat64
	var resolvedAt string
	if err := rows.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.AssetClass, &o.Side, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt); err != nil {
		return nil, err
	}
	if lp.Valid { v := lp.Float64; o.LimitPrice = &v }
	if sp.Valid { v := sp.Float64; o.StopPrice = &v }
	if resolvedAt != "" { o.ResolvedAt = resolvedAt }
	return &o, nil
}

func dbCancelOrder(db *sql.DB, projectID, id, reason string) (string, error) {
	o, err := dbGetOrder(db, projectID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("order %s not found", id)
		}
		return "", err
	}
	if o.Status != "working" {
		return o.Status, nil
	}
	_, err = db.Exec(`UPDATE orders SET status='cancelled', resolved_at=CURRENT_TIMESTAMP,
		rejection_detail = ? WHERE id = ?`, reason, id)
	if err != nil {
		return "", err
	}
	return "cancelled", nil
}

func dbRejectOrder(db *sql.DB, id, code, detail string) error {
	_, err := db.Exec(`
		UPDATE orders SET status='rejected', rejection_code=?, rejection_detail=?,
		                  resolved_at=CURRENT_TIMESTAMP WHERE id = ?`, code, detail, id)
	return err
}

// dbBrokerOrderIDFor — pulls the broker's order id out of the rationale
// journal row that order_place writes when in live mode. The journal
// metadata is the authoritative store; no broker_order_id column on
// orders. Returns "" when not found (paper order, or rationale row
// missing for an old order).
func dbBrokerOrderIDFor(db *sql.DB, orderID string) (string, error) {
	row := db.QueryRow(`
		SELECT json_extract(metadata, '$.broker_order_id')
		FROM journal
		WHERE kind = 'rationale' AND json_extract(metadata, '$.order_id') = ?
		ORDER BY created_at DESC LIMIT 1`, orderID)
	var s sql.NullString
	if err := row.Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return s.String, nil
}

// ─── Fills + journal ───────────────────────────────────────────────

func dbInsertFill(tx *sql.Tx, projectID, orderID string, portfolioID int64, qty, price, fee float64) error {
	_, err := tx.Exec(`
		INSERT INTO fills (project_id, order_id, portfolio_id, qty, price, fee)
		VALUES (?, ?, ?, ?, ?, ?)`, projectID, orderID, portfolioID, qty, price, fee)
	return err
}

func dbMarkOrderFilled(tx *sql.Tx, orderID string, qty, avgFill float64) error {
	_, err := tx.Exec(`
		UPDATE orders SET status='filled', filled_qty=?, avg_fill_price=?,
		                  resolved_at=CURRENT_TIMESTAMP WHERE id = ?`, qty, avgFill, orderID)
	return err
}

func dbInsertJournal(db *sql.DB, projectID string, portfolioID int64, kind, body string, metadata map[string]any) (int64, error) {
	metaBytes, _ := json.Marshal(metadata)
	if metaBytes == nil {
		metaBytes = []byte("{}")
	}
	res, err := db.Exec(`
		INSERT INTO journal (project_id, portfolio_id, kind, body, metadata)
		VALUES (?, ?, ?, ?, ?)`, projectID, portfolioID, kind, body, string(metaBytes))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbInsertJournalTx(tx *sql.Tx, projectID string, portfolioID int64, kind, body string, metadata map[string]any) error {
	metaBytes, _ := json.Marshal(metadata)
	if metaBytes == nil {
		metaBytes = []byte("{}")
	}
	_, err := tx.Exec(`
		INSERT INTO journal (project_id, portfolio_id, kind, body, metadata)
		VALUES (?, ?, ?, ?, ?)`, projectID, portfolioID, kind, body, string(metaBytes))
	return err
}

func dbReadJournal(db *sql.DB, portfolioID int64, kind, since string, limit int) ([]*JournalEntry, error) {
	q := `SELECT id, portfolio_id, kind, body, metadata, created_at
	      FROM journal WHERE portfolio_id = ?`
	args := []any{portfolioID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if since != "" {
		q += ` AND created_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JournalEntry
	for rows.Next() {
		var e JournalEntry
		var metaJSON string
		if err := rows.Scan(&e.ID, &e.PortfolioID, &e.Kind, &e.Body, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metaJSON), &e.Metadata)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ─── Marks ─────────────────────────────────────────────────────────

func dbUpsertMark(db *sql.DB, m *Mark) error {
	_, err := db.Exec(`
		INSERT INTO marks (symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
			asset_class = excluded.asset_class,
			price       = excluded.price,
			no_price    = excluded.no_price,
			prev_close  = excluded.prev_close,
			volume_24h  = excluded.volume_24h,
			marked_at   = excluded.marked_at`,
		m.Symbol, m.AssetClass, m.Price, nullable(m.NoPrice), nullable(m.PrevClose),
		nullable(m.Volume24h), m.MarkedAt)
	return err
}

func dbGetMark(db *sql.DB, symbol string) (*Mark, error) {
	row := db.QueryRow(`
		SELECT symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at
		FROM marks WHERE symbol = ?`, symbol)
	var m Mark
	var no, pc, vol sql.NullFloat64
	if err := row.Scan(&m.Symbol, &m.AssetClass, &m.Price, &no, &pc, &vol, &m.MarkedAt); err != nil {
		return nil, err
	}
	if no.Valid  { v := no.Float64;  m.NoPrice = &v }
	if pc.Valid  { v := pc.Float64;  m.PrevClose = &v }
	if vol.Valid { v := vol.Float64; m.Volume24h = &v }
	return &m, nil
}

// ─── Watchlist ─────────────────────────────────────────────────────

func dbWatchlistAdd(db *sql.DB, projectID string, portfolioID int64, symbol string) (bool, error) {
	res, err := db.Exec(`
		INSERT OR IGNORE INTO watchlist (project_id, portfolio_id, symbol)
		VALUES (?, ?, ?)`, projectID, portfolioID, symbol)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbWatchlistRemove(db *sql.DB, portfolioID int64, symbol string) (bool, error) {
	res, err := db.Exec(`DELETE FROM watchlist WHERE portfolio_id = ? AND symbol = ?`, portfolioID, symbol)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbWatchlist(db *sql.DB, portfolioID int64) ([]string, error) {
	rows, err := db.Query(`SELECT symbol FROM watchlist WHERE portfolio_id = ? ORDER BY added_at`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── Alerts ────────────────────────────────────────────────────────

func dbInsertAlert(db *sql.DB, projectID string, a *Alert) (int64, error) {
	var expiresArg any
	if a.ExpiresAt != "" {
		expiresArg = a.ExpiresAt
	}
	res, err := db.Exec(`
		INSERT INTO alerts (project_id, portfolio_id, symbol, rule, threshold, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, a.PortfolioID, a.Symbol, a.Rule, a.Threshold, expiresArg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbActiveAlerts(db *sql.DB) ([]*Alert, error) {
	rows, err := db.Query(`
		SELECT id, portfolio_id, symbol, rule, threshold, status,
		       COALESCE(expires_at, ''), created_at, COALESCE(fired_at, '')
		FROM alerts WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.PortfolioID, &a.Symbol, &a.Rule, &a.Threshold,
			&a.Status, &a.ExpiresAt, &a.CreatedAt, &a.FiredAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func dbFireAlert(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE alerts SET status='fired', fired_at=CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ─── Day baselines ─────────────────────────────────────────────────

func dbGetDayBaseline(db *sql.DB, portfolioID int64, utcDay string) (float64, bool, error) {
	row := db.QueryRow(`SELECT equity FROM day_baselines WHERE portfolio_id = ? AND utc_day = ?`,
		portfolioID, utcDay)
	var v float64
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return v, true, nil
}

func dbSetDayBaseline(db *sql.DB, portfolioID int64, utcDay string, equity float64) error {
	_, err := db.Exec(`
		INSERT INTO day_baselines (portfolio_id, utc_day, equity) VALUES (?, ?, ?)
		ON CONFLICT(portfolio_id, utc_day) DO UPDATE SET equity = excluded.equity`,
		portfolioID, utcDay, equity)
	return err
}

// ─── Helpers ───────────────────────────────────────────────────────

func nullable(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func utcDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// snapshotPortfolio computes equity, day P&L, open P&L, weights for a
// portfolio by joining current marks against open positions. Pure read,
// no DB writes.
func snapshotPortfolio(db *sql.DB, p *Portfolio) (*Portfolio, error) {
	pos, err := dbListPositions(db, p.ID)
	if err != nil {
		return nil, err
	}
	var openValue, openCost, openDay float64
	for _, q := range pos {
		mark, err := dbGetMark(db, q.Symbol)
		if err != nil {
			// No mark yet — use avg_cost (assume flat).
			q.MarketPrice = q.AvgCost
		} else {
			q.MarketPrice = markPriceForSide(mark, q.Outcome)
		}
		q.MarketValue = q.MarketPrice * q.Qty
		q.UnrealizedPnL = (q.MarketPrice - q.AvgCost) * q.Qty
		if q.AvgCost > 0 && q.Qty > 0 {
			q.UnrealizedPnLPct = (q.MarketPrice/q.AvgCost - 1) * 100
		}
		openValue += q.MarketValue
		openCost += q.AvgCost * q.Qty
	}
	equity := p.Cash + openValue
	for _, q := range pos {
		if equity > 0 {
			q.WeightPct = q.MarketValue / equity * 100
		}
	}

	// Day P&L from baseline. If there's no row yet, treat now as the baseline.
	day := utcDay(time.Now())
	baseline, ok, _ := dbGetDayBaseline(db, p.ID, day)
	if !ok {
		baseline = equity
		_ = dbSetDayBaseline(db, p.ID, day, equity)
	}
	openDay = equity - baseline

	p.Equity = equity
	p.DayPnL = openDay
	if baseline > 0 {
		p.DayPnLPct = openDay / baseline * 100
	}
	p.OpenPnL = openValue - openCost
	if openCost > 0 {
		p.OpenPnLPct = p.OpenPnL / openCost * 100
	}
	p.BuyingPower = p.Cash // v0.1 — long-only, no margin
	wl, _ := dbWatchlist(db, p.ID)
	if wl == nil {
		wl = []string{}
	}
	p.Watchlist = wl
	return p, nil
}

// markPriceForSide picks YES vs NO for polymarket positions; passes
// through for everything else.
func markPriceForSide(m *Mark, outcome string) float64 {
	if m.AssetClass == "polymarket" && outcome == "NO" && m.NoPrice != nil {
		return *m.NoPrice
	}
	return m.Price
}

// computeEquity is snapshotPortfolio's lightweight cousin — just the
// number, no per-position fluff. Used by the engine on every tick.
func computeEquity(db *sql.DB, p *Portfolio) (float64, error) {
	pos, err := dbListPositions(db, p.ID)
	if err != nil {
		return 0, err
	}
	value := p.Cash
	for _, q := range pos {
		mark, err := dbGetMark(db, q.Symbol)
		if err != nil {
			continue
		}
		value += markPriceForSide(mark, q.Outcome) * q.Qty
	}
	return value, nil
}
