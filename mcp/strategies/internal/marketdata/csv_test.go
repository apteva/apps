package marketdata

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMappedBidAskAndClosedBars(t *testing.T) {
	quotes := "Time,Bid,Ask\n2025-01-02T10:01:00Z,100,102\n2025-01-02T10:01:00Z,101,103\n"
	bars := "timestamp,open,high,low,close\n2025-01-02T10:00:00Z,100,102,99,101\n2025-01-02T10:02:00Z,101,103,100,102\n"
	req := Request{Symbol: "ANY", Streams: []Stream{
		{Kind: "bars", CSV: bars, Source: "broker-export", PriceBasis: "bid", Timeframe: "1m"},
		{Kind: "quotes", CSV: quotes, Source: "broker-export", Columns: map[string]string{"timestamp": "Time", "bid": "Bid", "ask": "Ask"}},
	}}
	d, err := Import(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Inputs) != 4 || d.Streams[0].BarGaps != 1 {
		t.Fatal("missing bars were invented or gap omitted")
	}
	if d.Inputs[0].Type != "market.bar.close" || d.Inputs[0].AvailableAt.Sub(d.Inputs[0].EventTime) != time.Minute {
		t.Fatal("bar exposed before closing")
	}
	if d.Inputs[1].Data["bid"] != 100 || d.Inputs[1].Data["ask"] != 102 || d.Inputs[1].Data["price"] != 101 {
		t.Fatal("observed quotes not retained")
	}
	if d.Streams[1].SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(quotes))) {
		t.Fatal("incorrect raw byte fingerprint")
	}
	again, err := Import(req)
	if err != nil || d.InputSHA256 != again.InputSHA256 {
		t.Fatal("import not deterministic")
	}
}

func TestRejectInvalidExports(t *testing.T) {
	for _, csv := range []string{
		"timestamp,bid,ask\n2025-01-02T10:00:00Z,102,100\n",
		"timestamp,bid,ask\n2025-01-02T10:00:00Z,NaN,100\n",
		"timestamp,bid,ask\n2025-01-02T10:00:00Z,0,100\n",
		"timestamp,bid,ask\n2025-01-02T10:01:00Z,99,100\n2025-01-02T10:00:00Z,99,100\n",
		"timestamp,bid,bid,ask\n2025-01-02T10:00:00Z,99,99,100\n",
		"timestamp,bid,ask\n",
		"timestamp,bid,ask\n2025-01-02T10:00:00Z,99\n",
	} {
		if _, err := Import(Request{Symbol: "ANY", Streams: []Stream{{Kind: "quotes", Source: "export", CSV: csv}}}); err == nil {
			t.Fatal("accepted invalid CSV", csv)
		}
	}
	if _, err := Import(Request{Symbol: "ANY", Streams: []Stream{{Kind: "quotes", Source: "export", CSV: strings.Repeat("x", MaxBytes+1)}}}); err == nil {
		t.Fatal("byte budget not enforced")
	}
}

func TestTimestampUnitsAndDST(t *testing.T) {
	for _, format := range []string{"unix_s", "unix_ms", "unix_us"} {
		if _, err := stamp("9223372036854775807", Stream{TimestampFormat: format}); err == nil {
			t.Fatal("accepted overflowing timestamp", format)
		}
	}
	got, err := stamp("1735812000000", Stream{TimestampFormat: "unix_ms"})
	if err != nil || got.Format(time.RFC3339) != "2025-01-02T10:00:00Z" {
		t.Fatal(got, err)
	}
	s := Stream{TimestampFormat: "2006-01-02 15:04:05", Timezone: "Europe/Helsinki"}
	for _, bad := range []string{"2025-03-30 03:30:00", "2025-10-26 03:30:00"} {
		if _, err := stamp(bad, s); err == nil {
			t.Fatal("accepted ambiguous/nonexistent local time", bad)
		}
	}
	got, err = stamp("2025-07-01 11:00:00", s)
	if err != nil || got.Format(time.RFC3339) != "2025-07-01T08:00:00Z" {
		t.Fatal(got, err)
	}
}
