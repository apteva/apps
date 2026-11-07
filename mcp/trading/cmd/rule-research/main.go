// rule-research adapts archived minute candles to a explicitly sampled execution
// experiment. It runs the production evaluator; it never invents intrabar paths.
package main

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	rules "github.com/apteva/apps/mcp/trading/internal/ruleengine"
)

type candle struct {
	At            time.Time
	O, H, L, C, V float64
}
type point struct {
	At        time.Time `json:"at"`
	Equity    float64   `json:"equity"`
	Benchmark float64   `json:"benchmark_equity"`
}
type trade struct {
	EntryAt time.Time `json:"entry_at"`
	ExitAt  time.Time `json:"exit_at"`
	Side    string    `json:"side"`
	Qty     float64   `json:"qty"`
	Entry   float64   `json:"entry_price"`
	Exit    float64   `json:"exit_price"`
	Fees    float64   `json:"fees"`
	PnL     float64   `json:"net_pnl"`
}

func readCSV(path string) ([]candle, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	hash := sha256.New()
	r := csv.NewReader(io.TeeReader(f, hash))
	var rows []candle
	for {
		fields, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		stamp, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil && len(rows) == 0 && fields[0] == "open_time" {
			continue
		}
		if err != nil || len(fields) < 7 {
			return nil, "", errors.New("expected Binance kline CSV")
		}
		if stamp > 100000000000000 {
			stamp /= 1000
		}
		c := candle{At: time.UnixMilli(stamp).UTC()}
		for i, dst := range []*float64{&c.O, &c.H, &c.L, &c.C, &c.V} {
			*dst, err = strconv.ParseFloat(fields[i+1], 64)
			if err != nil || math.IsNaN(*dst) || math.IsInf(*dst, 0) {
				return nil, "", errors.New("invalid candle number")
			}
		}
		if len(rows) > 0 && !c.At.After(rows[len(rows)-1].At) {
			return nil, "", errors.New("unordered/duplicate candle")
		}
		rows = append(rows, c)
	}
	return rows, fmt.Sprintf("%x", hash.Sum(nil)), nil
}
func bar(symbol, frame string, c candle, d time.Duration) sim.Input {
	return sim.Input{ID: "bar/" + frame + "/" + c.At.Format(time.RFC3339), Type: "market.bar.close", Symbol: symbol, Source: "binance_um_archived_klines", EventTime: c.At, AvailableAt: c.At.Add(d), Data: map[string]float64{"open": c.O, "high": c.H, "low": c.L, "price": c.C, "volume": c.V}, Metadata: map[string]string{"timeframe": frame}}
}
func quote(symbol string, at time.Time, price float64, origin string) sim.Input {
	return sim.Input{ID: "sample/" + origin + "/" + at.Format(time.RFC3339), Type: "market.quote", Symbol: symbol, Source: "binance_um_kline_price_sample", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": price}, Metadata: map[string]string{"execution_model": "sampled_kline_price", "origin": origin}}
}
func run() error {
	examplePath := flag.String("example", "", "generic example JSON")
	csvPath := flag.String("csv", "", "archived 1m Binance kline CSV")
	warmPath := flag.String("warmup", "", "preceding archived 1h kline CSV")
	symbol := flag.String("symbol", "", "adapted program instrument")
	startText := flag.String("start", "2025-01-01T00:00:00Z", "inclusive trading start")
	endText := flag.String("end", "2026-01-01T00:00:00Z", "exclusive trading end; flatten at final close sample")
	outPath := flag.String("output", "", "compact result JSON")
	fee := flag.Float64("fee-bps", 5, "assumed fee per fill")
	slip := flag.Float64("slippage-bps", 1, "assumed slippage per fill")
	spread := flag.Float64("spread-bps", 2, "assumed total bid/ask spread")
	flag.Parse()
	if *examplePath == "" || *csvPath == "" || *symbol == "" || *outPath == "" {
		return errors.New("required: --example --csv --symbol --output")
	}
	start, err := time.Parse(time.RFC3339, *startText)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339, *endText)
	if err != nil || !end.After(start) {
		return errors.New("invalid date range")
	}
	raw, err := os.ReadFile(*examplePath)
	if err != nil {
		return err
	}
	var sample struct {
		ID         string `json:"id"`
		Definition struct {
			Engine   string        `json:"engine"`
			Universe []string      `json:"universe"`
			Cadence  string        `json:"cadence"`
			Program  rules.Program `json:"program"`
		} `json:"definition"`
	}
	if err = json.Unmarshal(raw, &sample); err != nil {
		return err
	}
	if sample.Definition.Engine != "rules" {
		return errors.New("rules example required")
	}
	p := &sample.Definition.Program
	p.Symbol = *symbol
	sample.Definition.Universe = []string{*symbol}
	if p.Timeframe != "1m" && p.Timeframe != "1h" {
		return errors.New("CSV adapter currently supports 1m/1h base")
	}
	rows, csvHash, err := readCSV(*csvPath)
	if err != nil {
		return err
	}
	var tape []sim.Input
	warmHash := ""
	if *warmPath != "" {
		warm, digest, e := readCSV(*warmPath)
		if e != nil {
			return e
		}
		warmHash = digest
		warmFrames := p.Timeframe == "1h"
		for _, x := range p.Calculations {
			if x.Timeframe == "1h" {
				warmFrames = true
			}
		}
		if warmFrames {
			for _, c := range warm {
				if c.At.Before(start) {
					tape = append(tape, bar(*symbol, "1h", c, time.Hour))
				}
			}
		}
	}
	count, gaps := 0, 0
	var hour candle
	hourCount := 0
	var prev time.Time
	lastClose := 0.0
	for _, c := range rows {
		if c.At.Before(start) || !c.At.Before(end) {
			continue
		}
		if !prev.IsZero() && c.At.Sub(prev) != time.Minute {
			gaps++
		}
		prev = c.At
		count++
		lastClose = c.C
		tape = append(tape, quote(*symbol, c.At, c.O, "minute_open"))
		if p.Timeframe == "1m" {
			tape = append(tape, bar(*symbol, "1m", c, time.Minute))
		} else {
			at := c.At.Truncate(time.Hour)
			if hourCount == 0 || !hour.At.Equal(at) {
				hour = candle{At: at, O: c.O, H: c.H, L: c.L}
				hourCount = 0
			}
			hour.H = math.Max(hour.H, c.H)
			hour.L = math.Min(hour.L, c.L)
			hour.C = c.C
			hour.V += c.V
			hourCount++
			if c.At.Add(time.Minute).Equal(at.Add(time.Hour)) && hourCount == 60 {
				tape = append(tape, bar(*symbol, "1h", hour, time.Hour))
			}
		}
	}
	if count == 0 || gaps != 0 || !prev.Add(time.Minute).Equal(end) {
		return fmt.Errorf("incomplete minute dataset: rows=%d gaps=%d last=%s", count, gaps, prev)
	}
	tape = append(tape, quote(*symbol, end, lastClose, "final_close"))
	tape, err = rules.ScheduleInputs(p, tape)
	if err != nil {
		return err
	}
	config := sim.Config{Seed: 1, StartingCash: 100000, NotifyFills: true, BenchmarkSymbol: *symbol, Contracts: map[string]sim.Contract{*symbol: {Multiplier: 1, CurrencyRate: 1, MarginFraction: 1}}, Costs: sim.Costs{FeeBps: *fee, SlippageBps: *slip, SpreadBps: *spread, QtyStep: .000001}, Risk: sim.RiskLimits{MaxOrderPct: 100, MaxPositionPct: 100, MaxGrossExposurePct: 100}}
	active, err := rules.Strategy(p, config)
	if err != nil {
		return err
	}
	warm, err := rules.ObserveOnly(p)
	if err != nil {
		return err
	}
	terminal, err := rules.Strategy(&rules.Program{Version: rules.Version, Symbol: *symbol, Timeframe: p.Timeframe, Timezone: p.Timezone, Rules: []rules.Rule{{ID: "research_terminal_liquidation", On: "any", Repeat: "always", Actions: []rules.Action{{Kind: "flatten"}}}}}, config)
	if err != nil {
		return err
	}
	strategy := func(s *sim.State, in sim.Input) ([]sim.Command, error) {
		if !in.AvailableAt.Before(end) {
			return terminal(s, in)
		}
		if in.AvailableAt.Before(start) || in.Type == "market.bar.close" && in.EventTime.Before(start) {
			return warm(s, in)
		}
		return active(s, in)
	}
	e, err := sim.New(config, tape, nil, strategy)
	if err != nil {
		return err
	}
	if err = rules.ValidateTape(p, e.Inputs); err != nil {
		return err
	}
	inputDigest := sha256.New()
	for _, in := range e.Inputs {
		b, _ := json.Marshal(in)
		inputDigest.Write(b)
		inputDigest.Write([]byte{'\n'})
	}
	outputDigest := sha256.New()
	var curve []point
	var trades []trade
	var open *trade
	var audit []sim.Output
	rejections := map[string]int{}
	var lastDay, lastProgress string
	for !e.State.Finished {
		outputs, err := e.Advance()
		if err != nil {
			return err
		}
		for _, out := range outputs {
			b, _ := json.Marshal(out)
			outputDigest.Write(b)
			outputDigest.Write([]byte{'\n'})
			if out.Type != "input" && out.Type != "strategy.report" && out.Type != "execution.notification" && out.Type != "portfolio.snapshot" {
				audit = append(audit, out)
			}
			if out.Type == "order.rejected" {
				var fields map[string]any
				json.Unmarshal(out.Data, &fields)
				rejections[fmt.Sprint(fields["reason"])]++
			}
			if out.Type != "fill" {
				continue
			}
			var f struct {
				OrderID string  `json:"order_id"`
				Side    string  `json:"side"`
				Qty     float64 `json:"qty"`
				Price   float64 `json:"price"`
				Fee     float64 `json:"fee"`
			}
			if err = json.Unmarshal(out.Data, &f); err != nil {
				return err
			}
			var order *sim.Order
			for _, o := range e.State.Orders {
				if o.ID == f.OrderID {
					order = o
					break
				}
			}
			if order == nil {
				return errors.New("unknown fill order")
			}
			if !order.ReduceOnly {
				if open != nil {
					return errors.New("CSV analysis expects unpyramided complete fills")
				}
				open = &trade{EntryAt: out.At, Side: f.Side, Qty: f.Qty, Entry: f.Price, Fees: f.Fee}
			} else {
				if open == nil || math.Abs(f.Qty-open.Qty) > 1e-7 {
					return errors.New("CSV analysis expects complete protective exits")
				}
				open.ExitAt = out.At
				open.Exit = f.Price
				open.Fees += f.Fee
				sign := 1.0
				if open.Side == "sell" {
					sign = -1
				}
				open.PnL = (open.Exit-open.Entry)*open.Qty*sign - open.Fees
				trades = append(trades, *open)
				open = nil
			}
		}
		if !e.State.Now.Before(start) {
			day := e.State.Now.UTC().Format("2006-01-02")
			m := e.Metrics()
			pt := point{At: e.State.Now, Equity: m["equity"], Benchmark: m["benchmark_equity"]}
			if day == lastDay && len(curve) > 0 {
				curve[len(curve)-1] = pt
			} else {
				curve = append(curve, pt)
				lastDay = day
			}
			month := e.State.Now.Format("2006-01")
			if month != lastProgress {
				fmt.Fprintf(os.Stderr, "%s %s: %s, %d trades\n", *symbol, sample.ID, month, len(trades))
				lastProgress = month
			}
		}
	}
	if open != nil || len(e.State.Positions) > 0 {
		return errors.New("terminal position was not closed")
	}
	wins := 0
	gains, losses := 0.0, 0.0
	worst := 0.0
	firstHalf, secondHalf := 0.0, 0.0
	midpoint := time.Date(start.Year(), 7, 1, 0, 0, 0, 0, time.UTC)
	for _, t := range trades {
		if t.PnL > 0 {
			wins++
			gains += t.PnL
		} else {
			losses -= t.PnL
		}
		worst = math.Min(worst, t.PnL)
		if t.ExitAt.Before(midpoint) {
			firstHalf += t.PnL
		} else {
			secondHalf += t.PnL
		}
	}
	profitFactor := any(nil)
	if losses > 0 {
		profitFactor = gains / losses
	}
	winRate := 0.0
	if len(trades) > 0 {
		winRate = 100 * float64(wins) / float64(len(trades))
	}
	result := map[string]any{"schema": "apteva.rule-research/v1", "data_class": "historical_candles_sampled_execution", "strategy": sample.ID, "symbol": *symbol, "start": start, "end": end, "definition": sample.Definition, "simulation": config, "minute_rows": count, "missing_minute_intervals": gaps, "source_csv": *csvPath, "source_csv_sha256": csvHash, "warmup_csv": *warmPath, "warmup_csv_sha256": warmHash, "input_ndjson_sha256": fmt.Sprintf("%x", inputDigest.Sum(nil)), "output_ndjson_sha256": fmt.Sprintf("%x", outputDigest.Sum(nil)), "execution_source_sha256": sim.SourceHash(), "rule_source_sha256": rules.SourceHash(), "metrics": e.Metrics(), "trade_count": len(trades), "win_rate_pct": winRate, "profit_factor": profitFactor, "worst_trade_pnl": worst, "first_half_closed_trade_pnl": firstHalf, "second_half_closed_trade_pnl": secondHalf, "order_rejections": rejections, "trades": trades, "daily_curve": curve, "audit_events": audit, "assumptions": []string{"Minute-open trade prices are sampled execution proxies, not historical executable bid/ask quotes. No invented intraminute path; stops/trails can miss intervening moves.", "USD-M perpetual price history; fixed USDT account conversion 1; no funding, financing or exchange maintenance liquidation. Actual spread and market impact are not measured.", "100000 starting capital, fixed 100 planned stop risk, no pyramiding and maximum 100% gross exposure. Illustrative quantity increment 0.000001; unrestricted quote liquidity.", "Warmup observations do not trade. Remaining orders/positions are cancelled/flattened at the final observed close proxy, with costs.", "Screenshot parameters are fixed. Only instrument is substituted; the session rule retains Europe/Helsinki 08–11 range and 18:00 exit, including crypto weekends.", "Fees/spread/slippage are explicit research assumptions, not a claim about a particular account tier. Historical exploratory results are not prospective returns."}}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(*outPath, append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("%s %s: trades=%d net=%.2f return=%.3f%% DD=%.3f%% win=%.1f%%\n", *symbol, sample.ID, len(trades), e.Metrics()["total_pnl"], e.Metrics()["return_pct"], e.Metrics()["max_drawdown_pct"], winRate)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
