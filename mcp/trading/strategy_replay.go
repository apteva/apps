package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func normalizeStrategyReplayInterval(def *StrategyDefinition, interval string) (string, error) {
	if strings.TrimSpace(interval) == "" {
		interval = strategyHistoryInterval(def)
	}
	interval, err := normalizeBacktestInterval(interval)
	if err != nil {
		return "", err
	}
	return interval, validateStrategyReplayInterval(def, interval)
}

func validateStrategyReplayInterval(def *StrategyDefinition, interval string) error {
	signalInterval := strategyHistoryInterval(def)
	source, target := backtestIntervalDuration(interval), backtestIntervalDuration(signalInterval)
	if source <= 0 || source > target || target%source != 0 {
		return fmt.Errorf("replay interval %s cannot form %s strategy candles; use %s or a finer interval that divides it", interval, signalInterval, signalInterval)
	}
	return nil
}

func strategyReplayWarmupSteps(def *StrategyDefinition, interval string) int {
	n := strategyRequiredBars(def) - 1
	if interval == strategyHistoryInterval(def) {
		return n
	}
	// Include a partial leading candle. Calendar gaps only reduce the number
	// of replay rows needed to cover each complete strategy candle.
	ratio := float64(backtestIntervalDuration(strategyHistoryInterval(def))) / float64(backtestIntervalDuration(interval))
	return (n + 1) * int(math.Ceil(ratio))
}

func backtestStrategyMarket(run *BacktestRun, def *StrategyDefinition, step int) (strategyMarket, error) {
	market := strategyMarket{prices: map[string]float64{}, history: map[string][]float64{}, asOf: backtestReplayTime(run, step)}
	if err := validateStrategyReplayInterval(def, run.Interval); err != nil {
		return market, err
	}
	bars, err := dbBacktestMarketHistory(globalCtx.AppDB(), run.ID, step)
	if err != nil {
		return market, err
	}
	for _, bar := range bars {
		appendStrategyReplayBar(run, def, &market, bar)
	}
	return market, nil
}

func advanceBacktestStrategyMarket(run *BacktestRun, def *StrategyDefinition, step int, market *strategyMarket) ([]map[string]any, error) {
	prices, err := backtestMarks(run, step)
	if err != nil {
		return nil, err
	}
	for _, row := range prices {
		at, err := time.Parse(time.RFC3339, fmt.Sprint(row["time"]))
		if err != nil {
			return nil, fmt.Errorf("invalid replay bar timestamp: %w", err)
		}
		appendStrategyReplayBar(run, def, market, &BacktestMarketBar{
			Step: step, Symbol: fmt.Sprint(row["symbol"]), T: at.Unix(), C: anyFloat(row["price"]),
		})
	}
	return prices, nil
}

// Indicators use only completed candles at the strategy's own cadence. The
// finer replay bars still supply execution prices and equity marks. No forming
// strategy candle can generate a signal or execute at its own close.
func appendStrategyReplayBar(run *BacktestRun, def *StrategyDefinition, market *strategyMarket, bar *BacktestMarketBar) {
	if bar == nil || bar.C <= 0 {
		return
	}
	at := time.Unix(bar.T, 0).UTC()
	signalAt := at
	if run.Interval != strategyHistoryInterval(def) {
		var complete bool
		signalAt, complete = completedReplaySignalBar(def, run.Interval, at)
		if !complete {
			return
		}
	}
	symbol := strings.ToUpper(strings.TrimSpace(bar.Symbol))
	market.history[symbol] = append(market.history[symbol], bar.C)
	market.prices[symbol] = bar.C
	market.asOf = signalAt
	if len(run.Symbols) > 0 && strings.EqualFold(symbol, run.Symbols[0]) {
		market.signalStep = bar.Step
		if bar.Step > 0 {
			market.signalCount++
		}
	}
}

func strategyReplaySignalDue(def *StrategyDefinition, step int, market strategyMarket) bool {
	return step > 0 && market.signalStep == step && market.signalCount > 0 &&
		shouldRebalanceStrategy(def, strategyHistoryInterval(def), market.signalCount)
}

func completedReplaySignalBar(def *StrategyDefinition, sourceInterval string, at time.Time) (time.Time, bool) {
	targetInterval := strategyHistoryInterval(def)
	sourceDuration := backtestIntervalDuration(sourceInterval)
	targetDuration := backtestIntervalDuration(targetInterval)
	var start, end time.Time
	sourceEnd := at.Add(sourceDuration)
	if strategyHasStocks(def) {
		session := usEquitySessionAt(at)
		if !session.OpenDay {
			return time.Time{}, false
		}
		// Daily provider timestamps can be midnight in the exchange timezone.
		if sourceInterval == "1d" {
			sourceEnd = session.Close
		} else {
			if at.Before(session.Open) || !at.Before(session.Close) {
				return time.Time{}, false
			}
			if sourceEnd.After(session.Close) {
				sourceEnd = session.Close
			}
		}
		switch targetInterval {
		case "1w":
			local := session.Open
			monday := local.AddDate(0, 0, -(int(local.Weekday())+6)%7)
			for day := 0; day < 5; day++ {
				s := usEquitySessionAt(monday.AddDate(0, 0, day))
				if s.OpenDay {
					if start.IsZero() {
						start = s.Open
					}
					end = s.Close
				}
			}
		case "1d":
			start, end = session.Open, session.Close
		default:
			start = session.Open.Add(at.Sub(session.Open) / targetDuration * targetDuration)
			end = start.Add(targetDuration)
			if end.After(session.Close) {
				end = session.Close
			}
		}
	} else if targetInterval == "1w" {
		day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		start = day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		end = start.AddDate(0, 0, 7)
	} else {
		start = at.Truncate(targetDuration)
		end = start.Add(targetDuration)
	}
	return start.UTC(), sourceEnd.Equal(end)
}
