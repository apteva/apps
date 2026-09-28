package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The chart uses ledger quantities at each sample, including holdings that
// have since closed. A current holdings row alone cannot describe the past.
type historyAmount struct {
	at    time.Time
	value float64
	sum   float64
}

type historyPrice struct {
	at    time.Time
	value int64
}

type historyAccount struct {
	id       int64
	currency string
	opening  int64
	started  time.Time
	cash     []historyAmount
	holdings []*historyHolding
}

type historyHolding struct {
	id, instrumentID int64
	quoteCurrency    string
	currentQuantity  float64
	baseline         float64
	quantityChanges  []historyAmount
	tradePrices      []historyPrice
	storedPrices     []historyPrice
}

func reportNetWorthSeries(ctx *sdk.AppCtx, args map[string]any, series, base string) (any, error) {
	if series != "daily" && series != "weekly" && series != "monthly" {
		return nil, errors.New("series must be daily, weekly or monthly")
	}
	now := time.Now().UTC()
	from := strArg(args, "from", now.AddDate(-1, 0, 0).Format(time.RFC3339))
	to := strArg(args, "to", now.Format(time.RFC3339))
	toT, err := parseFlexibleTime(to)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if len(to) == len("2006-01-02") {
		toT = toT.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	var fromT time.Time
	if from == "all" {
		fromT = firstFinanceHistoryDate(ctx, now)
	} else {
		fromT, err = parseFlexibleTime(from)
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
	}
	if toT.Before(fromT) {
		return nil, errors.New("to must be on or after from")
	}
	samples, err := netWorthSampleTimes(fromT, toT, series)
	if err != nil {
		return nil, err
	}
	accounts, err := loadNetWorthHistory(ctx)
	if err != nil {
		return nil, err
	}
	points := make([]map[string]any, 0, len(samples))
	for _, at := range samples {
		var total int64
		for _, account := range accounts {
			if at.Before(account.started) {
				continue
			}
			value := account.opening + int64(historySum(account.cash, at))
			for _, holding := range account.holdings {
				quantity := holding.baseline + historySum(holding.quantityChanges, at)
				if quantity <= 1e-9 {
					continue
				}
				price := lastHistoryPrice(holding.storedPrices, at)
				tradePrice := lastHistoryPrice(holding.tradePrices, at)
				if tradePrice.at.After(price.at) {
					price = tradePrice
				}
				if price.value > 0 {
					value += convertCcy(ctx, int64(math.Round(quantity*float64(price.value))), holding.quoteCurrency, account.currency)
				}
			}
			total += convertCcy(ctx, value, account.currency, base)
		}
		points = append(points, map[string]any{"as_of": at.Format(time.RFC3339), "total": total})
	}
	return map[string]any{
		"series": series, "base_currency": base,
		"from": fromT.Format(time.RFC3339), "to": toT.Format(time.RFC3339),
		"points": points,
	}, nil
}

func firstFinanceHistoryDate(ctx *sdk.AppCtx, fallback time.Time) time.Time {
	first := fallback
	for _, query := range []string{
		`SELECT MIN(opening_at) FROM accounts WHERE project_id=? AND archived=0`,
		`SELECT MIN(t.posted_at) FROM transactions t JOIN accounts a ON a.id=t.account_id WHERE a.project_id=? AND a.archived=0`,
	} {
		var raw sql.NullString
		if ctx.AppDB().QueryRow(query, projectID(ctx)).Scan(&raw) == nil && raw.Valid {
			if at, err := parseFlexibleTime(raw.String); err == nil && at.Before(first) {
				first = at
			}
		}
	}
	return first
}

func netWorthSampleTimes(from, to time.Time, series string) ([]time.Time, error) {
	from, to = from.UTC(), to.UTC()
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	points := make([]time.Time, 0, 64)
	for !day.After(to) {
		var next time.Time
		switch series {
		case "daily":
			next = day.AddDate(0, 0, 1)
		case "weekly":
			days := 8 - int(day.Weekday())
			if day.Weekday() == time.Sunday {
				days = 1
			}
			next = day.AddDate(0, 0, days)
		case "monthly":
			next = time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		}
		at := next.Add(-time.Nanosecond)
		if at.After(to) {
			at = to
		}
		points = append(points, at)
		if len(points) > 500 {
			return nil, errors.New("date range has more than 500 points; choose a coarser resolution")
		}
		if !at.Before(to) {
			break
		}
		day = next
	}
	return points, nil
}

func loadNetWorthHistory(ctx *sdk.AppCtx) ([]*historyAccount, error) {
	rows, err := ctx.AppDB().Query(`SELECT id, currency, opening_balance, opening_at FROM accounts WHERE project_id=? AND archived=0 ORDER BY id`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	accounts := []*historyAccount{}
	byAccount := map[int64]*historyAccount{}
	for rows.Next() {
		a := &historyAccount{}
		var started string
		if err := rows.Scan(&a.id, &a.currency, &a.opening, &started); err != nil {
			rows.Close()
			return nil, err
		}
		a.started, _ = parseFlexibleTime(started)
		accounts = append(accounts, a)
		byAccount[a.id] = a
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = ctx.AppDB().Query(`SELECT h.id, h.account_id, h.instrument_id, h.quantity, i.quote_currency
		FROM holdings h JOIN accounts a ON a.id=h.account_id JOIN instruments i ON i.id=h.instrument_id
		WHERE a.project_id=? AND a.archived=0`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	byHolding := map[int64]*historyHolding{}
	byInstrument := map[int64][]*historyHolding{}
	for rows.Next() {
		h := &historyHolding{}
		var accountID int64
		if err := rows.Scan(&h.id, &accountID, &h.instrumentID, &h.currentQuantity, &h.quoteCurrency); err != nil {
			rows.Close()
			return nil, err
		}
		h.baseline = h.currentQuantity
		byAccount[accountID].holdings = append(byAccount[accountID].holdings, h)
		byHolding[h.id] = h
		byInstrument[h.instrumentID] = append(byInstrument[h.instrumentID], h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = ctx.AppDB().Query(`SELECT t.account_id, t.holding_id, t.posted_at, t.kind, t.amount, t.quantity, t.price
		FROM transactions t JOIN accounts a ON a.id=t.account_id
		WHERE a.project_id=? AND a.archived=0 ORDER BY t.posted_at, t.id`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var accountID, amount int64
		var holdingID, price sql.NullInt64
		var posted, kind string
		var quantity float64
		if err := rows.Scan(&accountID, &holdingID, &posted, &kind, &amount, &quantity, &price); err != nil {
			rows.Close()
			return nil, err
		}
		at, err := parseFlexibleTime(posted)
		if err != nil {
			continue
		}
		a := byAccount[accountID]
		if at.Before(a.started) {
			a.started = at
		}
		if kind != "valuation" {
			a.cash = append(a.cash, historyAmount{at: at, value: float64(amount)})
		}
		if h := byHolding[holdingID.Int64]; holdingID.Valid && h != nil && quantity != 0 {
			h.quantityChanges = append(h.quantityChanges, historyAmount{at: at, value: quantity})
			if price.Valid && price.Int64 > 0 && (kind == "buy" || kind == "sell") {
				h.tradePrices = append(h.tradePrices, historyPrice{at: at, value: price.Int64})
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = ctx.AppDB().Query(`SELECT p.instrument_id, p.as_of, p.price FROM prices p
		WHERE p.instrument_id IN (SELECT DISTINCT h.instrument_id FROM holdings h JOIN accounts a ON a.id=h.account_id WHERE a.project_id=? AND a.archived=0)
		ORDER BY p.as_of`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var instrumentID, price int64
		var raw string
		if err := rows.Scan(&instrumentID, &raw, &price); err != nil {
			rows.Close()
			return nil, err
		}
		at, err := parseFlexibleTime(raw)
		if err == nil {
			for _, h := range byInstrument[instrumentID] {
				h.storedPrices = append(h.storedPrices, historyPrice{at: at, value: price})
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, a := range accounts {
		prepareHistoryAmounts(a.cash)
		for _, h := range a.holdings {
			prepareHistoryAmounts(h.quantityChanges)
			if len(h.quantityChanges) > 0 {
				h.baseline -= h.quantityChanges[len(h.quantityChanges)-1].sum
			}
			for i := range h.tradePrices {
				h.tradePrices[i].value = convertCcy(ctx, h.tradePrices[i].value, a.currency, h.quoteCurrency)
			}
			sort.Slice(h.tradePrices, func(i, j int) bool { return h.tradePrices[i].at.Before(h.tradePrices[j].at) })
			sort.Slice(h.storedPrices, func(i, j int) bool { return h.storedPrices[i].at.Before(h.storedPrices[j].at) })
		}
	}
	return accounts, nil
}

func prepareHistoryAmounts(events []historyAmount) {
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	var sum float64
	for i := range events {
		sum += events[i].value
		events[i].sum = sum
	}
}

func historySum(events []historyAmount, at time.Time) float64 {
	i := sort.Search(len(events), func(i int) bool { return events[i].at.After(at) })
	if i == 0 {
		return 0
	}
	return events[i-1].sum
}

func lastHistoryPrice(prices []historyPrice, at time.Time) historyPrice {
	i := sort.Search(len(prices), func(i int) bool { return prices[i].at.After(at) })
	if i == 0 {
		return historyPrice{}
	}
	return prices[i-1]
}
