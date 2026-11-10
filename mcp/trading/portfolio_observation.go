package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

const (
	portfolioObservationVersion = 3
	observationPageBytes        = 4 << 20
	observationSnapshotBytes    = 64 << 20
	observationCacheBytes       = 128 << 20
	observationTTL              = 2 * time.Minute
)

type portfolioObservationPage struct {
	Version         int                          `json:"version"`
	SourceInstallID int64                        `json:"source_install_id"`
	ProjectID       string                       `json:"project_id"`
	PortfolioID     int64                        `json:"portfolio_id"`
	Revision        string                       `json:"revision,omitempty"`
	Page            int                          `json:"page,omitempty"`
	Done            bool                         `json:"done,omitempty"`
	Tables          map[string][]json.RawMessage `json:"tables"`
}

type observationScope struct {
	project           string
	portfolio, caller int64
}

type cachedObservation struct {
	scope     observationScope
	pages     []json.RawMessage
	bytes     int
	completed bool
}

var observationCache = struct {
	sync.Mutex
	items map[string]cachedObservation
	bytes int
}{items: make(map[string]cachedObservation)}

// Serialize captures so concurrent large requests cannot each allocate a full
// snapshot. Continuation requests never use a database connection.
var observationCapture = make(chan struct{}, 1)

func exportPortfolioObservation(call context.Context, ctx *sdk.AppCtx, args map[string]any, caller int64) (json.RawMessage, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	scope := observationScope{pid, int64Arg(args, "portfolio_id", 0), caller}
	if scope.portfolio <= 0 {
		return nil, errors.New("portfolio_id required")
	}
	revision := strArg(args, "revision")
	page := intArg(args, "page", 1)
	paged, _ := args["paged"].(bool)
	if revision != "" {
		observationCache.Lock()
		defer observationCache.Unlock()
		saved, ok := observationCache.items[revision]
		if !ok || saved.scope != scope {
			return nil, errors.New("Trading observation revision expired or unavailable; request a new snapshot")
		}
		if page < 2 || page > len(saved.pages) {
			return nil, errors.New("invalid Trading observation page")
		}
		if page == len(saved.pages) {
			saved.completed = true
			observationCache.items[revision] = saved
		}
		return saved.pages[page-1], nil
	}
	if page != 1 {
		return nil, errors.New("Trading observation continuation requires a revision")
	}
	var symbols []string
	if raw, exists := args["symbols"]; exists {
		var ok bool
		if symbols, ok = stringSliceValue(raw); !ok || len(symbols) > 2048 {
			return nil, errors.New("symbols must be an array of at most 2048 symbols")
		}
		for _, symbol := range symbols {
			if len(symbol) > 256 {
				return nil, errors.New("observation symbol exceeds 256 bytes")
			}
		}
	}
	from, to, err := observationSessionRange(strArg(args, "session_start"), strArg(args, "session_end"))
	if err != nil {
		return nil, err
	}

	select {
	case observationCapture <- struct{}{}:
		defer func() { <-observationCapture }()
	case <-call.Done():
		return nil, call.Err()
	}
	install, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	header := portfolioObservationPage{Version: 1, SourceInstallID: install, ProjectID: pid, PortfolioID: scope.portfolio}
	if paged {
		header.Version, header.Revision, header.Page = portfolioObservationVersion, uuid.NewString(), 1
	}
	b := observationBuilder{page: header}
	b.page.Tables = make(map[string][]json.RawMessage)
	if err := capturePortfolioObservation(call, ctx.AppReadDB(), &b, symbols, from, to); err != nil {
		return nil, err
	}
	if err := b.flush(true); err != nil {
		return nil, err
	}
	if len(b.pages) > 1 {
		if err := retainObservation(header.Revision, scope, b.pages, b.total); err != nil {
			return nil, err
		}
	}
	return b.pages[0], nil
}

func retainObservation(revision string, scope observationScope, pages []json.RawMessage, size int) error {
	observationCache.Lock()
	defer observationCache.Unlock()
	for key, saved := range observationCache.items {
		if observationCache.bytes+size <= observationCacheBytes && len(observationCache.items) < 64 {
			break
		}
		if saved.completed {
			observationCache.bytes -= saved.bytes
			delete(observationCache.items, key)
		}
	}
	if observationCache.bytes+size > observationCacheBytes || len(observationCache.items) >= 64 {
		return errors.New("Trading observation cache is busy; retry shortly")
	}
	observationCache.items[revision] = cachedObservation{scope: scope, pages: pages, bytes: size}
	observationCache.bytes += size
	time.AfterFunc(observationTTL, func() {
		observationCache.Lock()
		defer observationCache.Unlock()
		observationCache.bytes -= observationCache.items[revision].bytes
		delete(observationCache.items, revision)
	})
	return nil
}

type observationBuilder struct {
	page        portfolioObservationPage
	pages       []json.RawMessage
	size, total int
}

func (b *observationBuilder) add(table string, row map[string]any) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// Allow room for field names, delimiters and revision metadata.
	const rowBudget = observationPageBytes - 2048
	if len(raw)+1 > rowBudget {
		return fmt.Errorf("Trading observation row in %s exceeds the page limit", table)
	}
	if b.size+len(raw)+1 > rowBudget {
		if b.page.Version == 1 {
			return errors.New("Trading observation requires pagination; upgrade Strategies")
		}
		if err := b.flush(false); err != nil {
			return err
		}
	}
	b.page.Tables[table] = append(b.page.Tables[table], raw)
	b.size += len(raw) + 1
	return nil
}

func (b *observationBuilder) flush(done bool) error {
	b.page.Done = done && b.page.Version == portfolioObservationVersion
	raw, err := json.Marshal(b.page)
	if err != nil {
		return err
	}
	if len(raw) > observationPageBytes || b.total+len(raw) > observationSnapshotBytes {
		return errors.New("Trading observation exceeds its transfer budget")
	}
	b.pages = append(b.pages, raw)
	b.total += len(raw)
	b.size = 0
	b.page.Page++
	b.page.Tables = make(map[string][]json.RawMessage)
	return nil
}

func observationSessionRange(from, to string) (string, string, error) {
	now := time.Now().UTC()
	defaultFrom, defaultTo := now.AddDate(0, 0, -7).Format(time.DateOnly), now.AddDate(0, 0, 14).Format(time.DateOnly)
	if from == "" {
		from = defaultFrom
	}
	if to == "" {
		to = defaultTo
	}
	if _, err := time.Parse(time.DateOnly, from); err != nil {
		return "", "", errors.New("invalid observation session_start")
	}
	if _, err := time.Parse(time.DateOnly, to); err != nil {
		return "", "", errors.New("invalid observation session_end")
	}
	if from > to {
		return "", "", errors.New("observation session_start must not be after session_end")
	}
	return from, to, nil
}

func capturePortfolioObservation(call context.Context, db *sql.DB, b *observationBuilder, requested []string, from, to string) error {
	tx, err := db.BeginTx(call, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	id, pid := b.page.PortfolioID, b.page.ProjectID
	var pf Portfolio
	symbols, securityIDs, classes, calendars := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, symbol := range requested {
		if symbol = canonicalSymbol(symbol); symbol != "" {
			symbols[symbol] = true
		}
	}
	var universe string
	read := func(table, query string, inspect func(map[string]any), params ...any) error {
		rows, err := tx.QueryContext(call, query, params...)
		if err != nil {
			return err
		}
		return scanResearchRows(rows, func(row map[string]any) error {
			if inspect != nil {
				inspect(row)
			}
			return b.add(table, row)
		})
	}
	err = read("portfolios", "SELECT * FROM portfolios WHERE id=? AND project_id=?", func(row map[string]any) {
		pf.ID = id
		pf.Mode, pf.ExecutionEnvironment, pf.BrokerSlug = strArg(row, "mode"), strArg(row, "execution_environment"), strArg(row, "broker_slug")
	}, id, pid)
	if err != nil {
		return err
	}
	if pf.ID == 0 {
		return errors.New("portfolio not found")
	}
	collect := func(row map[string]any) {
		if symbol := strArg(row, "symbol"); symbol != "" {
			symbols[symbol] = true
		}
		if security := strArg(row, "security_id"); security != "" {
			securityIDs[security] = true
		}
		if class := strArg(row, "asset_class"); class != "" {
			classes[class] = true
		}
		if calendar := strArg(row, "calendar"); calendar != "" {
			calendars[calendar] = true
		}
	}
	for _, table := range []string{"positions", "orders", "fills", "execution_costs", "position_accounting", "portfolio_risk_policies", "portfolio_risk_state", "watchlist", "day_baselines"} {
		if err := read(table, "SELECT * FROM "+table+" WHERE portfolio_id=?", collect, id); err != nil {
			return fmt.Errorf("read Trading observation %s: %w", table, err)
		}
	}
	if err := read("portfolio_universe_policies", "SELECT * FROM portfolio_universe_policies WHERE portfolio_id=?", func(row map[string]any) {
		universe = strArg(row, "reference_universe_id")
	}, id); err != nil {
		return err
	}
	for symbol := range symbols {
		classes[inferAssetClass(symbol)] = true
	}
	symbolSet := observationSetJSON(symbols)
	const requestedSymbols = "(SELECT value FROM json_each(?))"
	for _, table := range []string{"marks", "instruments"} {
		if err := read(table, "SELECT * FROM "+table+" WHERE symbol IN "+requestedSymbols, collect, symbolSet); err != nil {
			return err
		}
	}
	// Match listing aliases for the requested symbols and retain identities
	// already referenced by account rows even if the ticker changed.
	listings, err := tx.QueryContext(call, "SELECT DISTINCT security_id FROM security_listings WHERE symbol IN "+requestedSymbols, symbolSet)
	if err != nil {
		return err
	}
	if err := scanResearchRows(listings, func(row map[string]any) error {
		collect(row)
		return nil
	}); err != nil {
		return err
	}
	securitySet := observationSetJSON(securityIDs)
	if err := read("securities", "SELECT * FROM securities WHERE id IN "+requestedSymbols, nil, securitySet); err != nil {
		return err
	}
	for _, table := range []string{"security_identifiers", "security_listings"} {
		if err := read(table, "SELECT * FROM "+table+" WHERE security_id IN "+requestedSymbols, nil, securitySet); err != nil {
			return err
		}
	}
	if universe != "" {
		if err := read("universe_memberships", "SELECT * FROM universe_memberships WHERE universe_id=? AND security_id IN "+requestedSymbols, nil, universe, securitySet); err != nil {
			return err
		}
	}

	if err := read("venue_execution_profiles", "SELECT * FROM venue_execution_profiles WHERE venue_slug=? AND asset_class IN "+requestedSymbols+
		" AND (symbol='*' OR symbol IN "+requestedSymbols+")", collect, executionVenue(&pf), observationSetJSON(classes), symbolSet); err != nil {
		return err
	}
	if classes["equity"] || classes["etf"] {
		calendars[calendarUSEquity] = true
	}
	if err := read("exchange_sessions", "SELECT * FROM exchange_sessions WHERE venue IN "+requestedSymbols+" AND session_date BETWEEN ? AND ?", nil,
		observationSetJSON(calendars), from, to); err != nil {
		return err
	}
	return tx.Commit()
}

func observationSetJSON(set map[string]bool) string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	raw, _ := json.Marshal(values)
	return string(raw)
}
