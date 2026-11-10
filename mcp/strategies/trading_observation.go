package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxTradingObservationBytes = 64 << 20
	maxTradingObservationPages = 64
)

type tradingObservationPage struct {
	Version         int                          `json:"version"`
	SourceInstallID int64                        `json:"source_install_id"`
	ProjectID       string                       `json:"project_id"`
	PortfolioID     int64                        `json:"portfolio_id"`
	Revision        string                       `json:"revision"`
	Page            int                          `json:"page"`
	Done            bool                         `json:"done"`
	Tables          map[string][]json.RawMessage `json:"tables"`
}

func tradingObservationArgs(db *sql.DB, pid string, id int64, request map[string]any) (map[string]any, error) {
	args := map[string]any{"_project_id": pid, "portfolio_id": id, "paged": true}
	var symbols []string
	if raw, ok := request["symbols"]; ok {
		_ = decodeResearchValue(raw, &symbols)
	}
	if symbol := strArg(request, "symbol"); symbol != "" {
		symbols = append(symbols, symbol)
	}
	if inputs, ok := request["inputs"].([]any); ok && len(symbols) == 0 {
		for _, input := range inputs {
			if event, ok := input.(map[string]any); ok && strArg(event, "type") == "market.quote" {
				symbols = append(symbols, strArg(event, "symbol"))
			}
		}
	}
	addStrategy := func(strategy *Strategy) error {
		def, err := parseStrategyDefinition(strategy.Definition)
		if err != nil {
			return err
		}
		symbols = append(symbols, def.Universe...)
		return nil
	}
	if assignment, err := dbActiveStrategyAssignment(db, pid, id); err == nil {
		strategy, err := dbGetStrategyVersion(db, pid, assignment.StrategyID, assignment.StrategyVersion)
		if err != nil {
			return nil, err
		}
		if err := addStrategy(strategy); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if strategyID := int64Arg(request, "strategy_id", 0); strategyID > 0 {
		strategy, err := dbGetStrategy(db, pid, strategyID)
		if err != nil {
			return nil, err
		}
		if err := addStrategy(strategy); err != nil {
			return nil, err
		}
	}

	from, to := strArg(request, "start_at"), strArg(request, "end_at")
	for _, key := range []string{"backtest_id", "source_backtest_id"} {
		if runID := int64Arg(request, key, 0); runID > 0 {
			run, err := dbGetBacktestRun(db, pid, runID)
			if err != nil {
				return nil, err
			}
			if run.PortfolioID == id {
				symbols = append(symbols, run.Symbols...)
				from, to = run.StartAt, run.EndAt
			}
			break
		}
	}
	if historical, _ := request["_observation_history"].(bool); historical || from != "" || to != "" {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if len(from) > len(time.DateOnly) {
			from = from[:len(time.DateOnly)]
		}
		if len(to) > len(time.DateOnly) {
			to = to[:len(time.DateOnly)]
		}
		start, end, err := resolveBacktestRange(from, to)
		if err == nil {
			args["session_start"], args["session_end"] = start.Format(time.DateOnly), end.Format(time.DateOnly)
		}
	}
	if symbols = cleanSymbols(symbols); len(symbols) > 0 {
		args["symbols"] = symbols
	}
	return args, nil
}

func fetchTradingObservation(ctx *sdk.AppCtx, pid string, id int64, args map[string]any) ([]tradingObservationPage, error) {
	var first tradingObservationPage
	if err := callTrading(ctx, tradingSnapshotTool, args, &first); err != nil {
		return nil, err
	}
	if first.ProjectID != pid || len(first.Tables["portfolios"]) != 1 {
		return nil, errors.New("invalid Trading portfolio observation")
	}
	var portfolio struct {
		ID        int64  `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(first.Tables["portfolios"][0], &portfolio); err != nil || portfolio.ID != id || portfolio.ProjectID != pid {
		return nil, errors.New("Trading observation belongs to a different portfolio")
	}
	pages := []tradingObservationPage{first}
	if first.Version == 1 {
		return pages, nil
	}
	if first.Version != tradingSnapshotVersion || first.PortfolioID != id || first.Revision == "" || first.Page != 1 {
		return nil, errors.New("invalid Trading observation revision")
	}
	size := observationRowsBytes(first)
	for page := first; !page.Done; {
		next := page.Page + 1
		if next > maxTradingObservationPages {
			return nil, errors.New("Trading observation exceeded the page limit")
		}
		// A fresh struct is required: omitted fields must never inherit values
		// from the previous page.
		page = tradingObservationPage{}
		request := map[string]any{"_project_id": pid, "portfolio_id": id, "revision": first.Revision, "page": next, "paged": true}
		if err := callTrading(ctx, tradingSnapshotTool, request, &page); err != nil {
			return nil, fmt.Errorf("Trading observation page %d: %w", next, err)
		}
		if page.Version != first.Version || page.SourceInstallID != first.SourceInstallID || page.ProjectID != pid ||
			page.PortfolioID != id || page.Revision != first.Revision || page.Page != next || len(page.Tables["portfolios"]) != 0 {
			return nil, errors.New("Trading observation pages do not match one revision")
		}
		size += observationRowsBytes(page)
		if size > maxTradingObservationBytes {
			return nil, errors.New("Trading observation exceeded the transfer budget")
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func observationRowsBytes(page tradingObservationPage) int {
	size := 0
	for _, records := range page.Tables {
		for _, row := range records {
			size += len(row)
		}
	}
	return size
}
