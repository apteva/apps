// Package marketdata normalizes explicitly sourced exports without synthesizing
// prices, market sessions, missing observations, or intrabar execution paths.
package marketdata

import (
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

const MaxBytes = 16 << 20
const MaxRows = 1000000

type Stream struct {
	Kind            string            `json:"kind"` // quotes or bars
	CSV             string            `json:"csv"`
	Source          string            `json:"source"`
	SourceURL       string            `json:"source_url,omitempty"`
	Columns         map[string]string `json:"columns,omitempty"` // normalized field -> exact CSV header
	Delimiter       string            `json:"delimiter,omitempty"`
	TimestampFormat string            `json:"timestamp_format,omitempty"` // RFC3339, unix_s, unix_ms, unix_us or Go layout
	Timezone        string            `json:"timezone,omitempty"`
	Timeframe       string            `json:"timeframe,omitempty"`
	PriceBasis      string            `json:"price_basis,omitempty"` // bid, ask, mid or last for bar exports
}

type Request struct {
	Symbol  string   `json:"symbol"`
	Streams []Stream `json:"streams"`
}

type StreamInfo struct {
	Kind              string  `json:"kind"`
	Source            string  `json:"source"`
	SourceURL         string  `json:"source_url,omitempty"`
	SHA256            string  `json:"csv_sha256"`
	Rows              int     `json:"rows"`
	Timeframe         string  `json:"timeframe,omitempty"`
	PriceBasis        string  `json:"price_basis,omitempty"`
	BarGaps           int     `json:"bar_gaps"`
	LargestGapSeconds float64 `json:"largest_gap_seconds"`
}

type Dataset struct {
	Schema      string       `json:"schema"`
	Symbol      string       `json:"symbol"`
	Inputs      []sim.Input  `json:"inputs"`
	InputSHA256 string       `json:"input_sha256"`
	Streams     []StreamInfo `json:"streams"`
	Notes       string       `json:"notes"`
}

func stamp(value string, s Stream) (time.Time, error) {
	format := s.TimestampFormat
	if format == "" || format == "RFC3339" {
		return time.Parse(time.RFC3339Nano, value)
	}
	unit := int64(0)
	switch format {
	case "unix_s":
		unit = 1000000000
	case "unix_ms":
		unit = 1000000
	case "unix_us":
		unit = 1000
	}
	if unit != 0 {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/unit {
			return time.Time{}, errors.New("invalid or overflowing Unix timestamp")
		}
		return time.Unix(0, n*unit).UTC(), nil
	}
	if s.Timezone == "" {
		return time.Time{}, errors.New("custom timestamp layouts require timezone")
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	at, err := time.ParseInLocation(format, value, loc)
	if err != nil {
		return time.Time{}, err
	}
	if at.Format(format) != value {
		return time.Time{}, errors.New("nonexistent local timestamp; export UTC instead")
	}
	// Refuse ambiguous wall times at DST fallback rather than silently choosing
	// one occurrence. UTC or explicit RFC3339 offsets resolve this ambiguity.
	for _, delta := range []time.Duration{-time.Hour, time.Hour} {
		if at.Add(delta).In(loc).Format(format) == value {
			return time.Time{}, errors.New("ambiguous local timestamp; export UTC or explicit offsets")
		}
	}
	return at.UTC(), nil
}

func Import(req Request) (*Dataset, error) {
	if strings.TrimSpace(req.Symbol) == "" || len(req.Streams) == 0 || len(req.Streams) > 16 {
		return nil, errors.New("symbol and 1–16 streams required")
	}
	bytes := 0
	for _, s := range req.Streams {
		bytes += len(s.CSV)
	}
	if bytes > MaxBytes {
		return nil, errors.New("CSV byte budget exceeded (16 MiB); partition exports")
	}
	d := &Dataset{Schema: "apteva.market-dataset/v1", Symbol: req.Symbol, Notes: "Caller-supplied exports and provenance; hashes identify bytes, not authenticity. Quotes use observed bid/ask; bars retain the declared price basis. No prices, fills, missing bars or calendar sessions are invented. Bar gaps may be scheduled closures or missing history and require review. Import does not configure contract size, broker timezone, fees, financing or currency conversion."}
	for si, s := range req.Streams {
		if strings.TrimSpace(s.Source) == "" || (s.Kind != "quotes" && s.Kind != "bars") {
			return nil, errors.New("each stream requires source and kind=quotes|bars")
		}
		var duration time.Duration
		if s.Kind == "bars" {
			duration = map[string]time.Duration{"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour}[s.Timeframe]
			if duration == 0 {
				return nil, errors.New("bars require a supported rule timeframe: 1m, 5m, 15m, 1h, 4h or 1d")
			}
			if s.PriceBasis != "bid" && s.PriceBasis != "ask" && s.PriceBasis != "mid" && s.PriceBasis != "last" {
				return nil, errors.New("bars require price_basis=bid|ask|mid|last")
			}
		}
		r := csv.NewReader(strings.NewReader(s.CSV))
		if s.Delimiter != "" {
			runes := []rune(s.Delimiter)
			if len(runes) != 1 || runes[0] == '\r' || runes[0] == '\n' || runes[0] == '"' || runes[0] == 0 {
				return nil, errors.New("invalid CSV delimiter")
			}
			r.Comma = runes[0]
		}
		head, err := r.Read()
		if err != nil {
			return nil, fmt.Errorf("stream %d header: %w", si, err)
		}
		columns := map[string]int{}
		for i, name := range head {
			name = strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")
			if _, exists := columns[name]; exists {
				return nil, errors.New("duplicate CSV header")
			}
			columns[name] = i
		}
		column := func(field string) (int, bool) {
			name := field
			if mapped, ok := s.Columns[field]; ok {
				name = mapped
			}
			i, ok := columns[name]
			return i, ok
		}
		fields := []string{"timestamp", "bid", "ask"}
		if s.Kind == "bars" {
			fields = []string{"timestamp", "open", "high", "low", "close"}
		}
		for _, field := range fields {
			if _, ok := column(field); !ok {
				return nil, fmt.Errorf("stream %d missing %s column", si, field)
			}
		}
		info := StreamInfo{Kind: s.Kind, Source: s.Source, SourceURL: s.SourceURL, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(s.CSV))), Timeframe: s.Timeframe, PriceBasis: s.PriceBasis}
		var previous time.Time
		for {
			row, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("stream %d row %d: %w", si, info.Rows+1, err)
			}
			if len(d.Inputs) >= MaxRows {
				return nil, errors.New("input row budget exceeded; partition exports")
			}
			idx, _ := column("timestamp")
			at, err := stamp(strings.TrimSpace(row[idx]), s)
			if err != nil {
				return nil, fmt.Errorf("stream %d row %d timestamp: %w", si, info.Rows+1, err)
			}
			if !previous.IsZero() && (at.Before(previous) || s.Kind == "bars" && at.Equal(previous)) {
				return nil, errors.New("unordered observations or duplicate bar timestamp")
			}
			gap := at.Sub(previous)
			if !previous.IsZero() {
				info.LargestGapSeconds = math.Max(info.LargestGapSeconds, gap.Seconds())
				if s.Kind == "bars" && gap != duration {
					info.BarGaps++
				}
			}
			previous = at
			data := map[string]float64{}
			for _, field := range append(fields[1:], "volume") {
				i, exists := column(field)
				if !exists {
					continue
				}
				n, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || field != "volume" && n == 0 {
					return nil, fmt.Errorf("invalid %s at stream %d row %d", field, si, info.Rows+1)
				}
				data[field] = n
			}
			in := sim.Input{ID: fmt.Sprintf("csv/%d/%d", si, info.Rows), Symbol: req.Symbol, Source: s.Source, EventTime: at, AvailableAt: at, Data: data, Metadata: map[string]string{"csv_sha256": info.SHA256}}
			if s.SourceURL != "" {
				in.Metadata["source_url"] = s.SourceURL
			}
			if s.Kind == "quotes" {
				if data["bid"] > data["ask"] {
					return nil, errors.New("crossed bid/ask quote")
				}
				in.Type = "market.quote"
				data["price"] = data["bid"] + (data["ask"]-data["bid"])/2
			} else {
				if at.UnixNano()%int64(duration) != 0 {
					return nil, errors.New("bar start must align to declared UTC timeframe")
				}
				if data["high"] < math.Max(data["open"], data["close"]) || data["low"] > math.Min(data["open"], data["close"]) || data["low"] > data["high"] {
					return nil, errors.New("invalid OHLC range")
				}
				in.Type = "market.bar.close"
				data["price"] = data["close"]
				delete(data, "close")
				in.AvailableAt = at.Add(duration)
				in.Metadata["timeframe"] = s.Timeframe
				in.Metadata["price_basis"] = s.PriceBasis
			}
			d.Inputs = append(d.Inputs, in)
			info.Rows++
		}
		if info.Rows == 0 {
			return nil, errors.New("empty observation stream")
		}
		d.Streams = append(d.Streams, info)
	}
	sort.SliceStable(d.Inputs, func(i, j int) bool { return d.Inputs[i].AvailableAt.Before(d.Inputs[j].AvailableAt) })
	d.InputSHA256 = sim.Hash(d.Inputs)
	return d, nil
}
