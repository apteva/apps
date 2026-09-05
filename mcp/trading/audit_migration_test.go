package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrityUpgradeFrom090PreservesAccountingAndDisarmsUnknownBroker(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.Contains(file, "018_") {
			break
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	for i := 1; i <= 5; i++ {
		_, err := db.Exec(`INSERT INTO portfolios(id,project_id,name,starting_cash,cash,mode,broker_slug,execution_environment,live_armed) VALUES(?,'test-proj',?,1000,1000,'live','binance-trading','broker_live',1)`, i, fmt.Sprintf("legacy-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO journal(project_id,portfolio_id,kind,body,metadata) VALUES('test-proj',1,'rationale','prior broker binding','{"broker_connection_id":7}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO position_accounting(portfolio_id,symbol,outcome,gross_realized_pnl,fees_paid) VALUES(1,'BTC-USD','',50,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO orders(id,project_id,portfolio_id,symbol,asset_class,side,type,qty,tif,status,rationale,source,filled_qty,avg_fill_price) VALUES('legacy-fee','test-proj',1,'BTC-USD','crypto','buy','market',2,'gtc','filled','migration fixture','test',2,100)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO journal(project_id,portfolio_id,kind,body,metadata) VALUES('test-proj',1,'fill','legacy fee','{"order_id":"legacy-fee","raw_commissions":[{"amount":1.25,"currency":"USD"}]}')`); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range [][2]int{{3, 8}, {3, 9}, {4, 10}, {5, 10}} {
		if _, err := db.Exec(`INSERT INTO journal(project_id,portfolio_id,kind,body,metadata) VALUES('test-proj',?,'rationale','ambiguous ownership',?)`, binding[0], fmt.Sprintf(`{"broker_connection_id":%d}`, binding[1])); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("migrations/018_audit_integrity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var feeWatermark float64
	if err := db.QueryRow(`SELECT amount FROM order_commissions WHERE order_id='legacy-fee' AND currency='USD'`).Scan(&feeWatermark); err != nil || feeWatermark != 1.25 {
		t.Fatalf("legacy fee watermark=%v err=%v", feeWatermark, err)
	}
	var pinned int64
	if err := db.QueryRow(`SELECT connection_id FROM broker_bindings WHERE portfolio_id=1`).Scan(&pinned); err != nil || pinned != 7 {
		t.Fatalf("pin=%d err=%v", pinned, err)
	}
	var required, armed int
	if err := db.QueryRow(`SELECT broker_binding_required,live_armed FROM portfolios WHERE id=2`).Scan(&required, &armed); err != nil {
		t.Fatal(err)
	}
	if required != 1 || armed != 0 {
		t.Fatalf("unknown legacy binding required=%d armed=%d", required, armed)
	}
	var unsafe int
	if err := db.QueryRow(`SELECT COUNT(*) FROM portfolios WHERE id IN (3,4,5) AND (broker_binding_required<>1 OR live_armed<>0)`).Scan(&unsafe); err != nil || unsafe != 0 {
		t.Fatalf("ambiguous legacy accounts were armed: %d %v", unsafe, err)
	}
	if err := dbRebuildPositionAccounting(db); err != nil {
		t.Fatal(err)
	}
	gross, fees, err := dbPortfolioAccounting(db, 1)
	if err != nil || gross != 48 || fees != 2 {
		t.Fatalf("accounting=%v fees=%v err=%v", gross, fees, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := dbRebuildPositionAccounting(reopened); err != nil {
		t.Fatal(err)
	}
	again, _, err := dbPortfolioAccounting(reopened, 1)
	if err != nil || again != gross {
		t.Fatalf("restart changed accounting: %v %v", again, err)
	}
}

func FuzzBrokerOrderParsers(f *testing.F) {
	for _, seed := range []string{`{}`, `[]`, `null`, `{"id":"1","status":"accepted","amount":"2","price":"100"}`, `{"orderId":1,"status":"PARTIALLY_FILLED","executedQty":"1","cummulativeQuoteQty":"100"}`, `{"id":"1","status":"filled","filled_qty":"NaN"}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		if len(payload) > 65536 {
			t.Skip()
		}
		if !json.Valid([]byte(payload)) {
			return
		}
		for _, slug := range registeredSlugs() {
			adapter := adapterBySlug(slug)
			if adapter == nil {
				continue
			}
			_, _ = adapter.ParseOrder(json.RawMessage(payload))
		}
	})
}

func BenchmarkIntegrityPortfolioValuation(b *testing.B) {
	for _, markCount := range []int{100, 10000} {
		b.Run(fmt.Sprintf("universe_%d_positions_100", markCount), func(b *testing.B) {
			db, err := sql.Open("sqlite", filepath.Join(b.TempDir(), "valuation.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			files, _ := filepath.Glob("migrations/*.sql")
			for _, file := range files {
				raw, err := os.ReadFile(file)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := db.Exec(string(raw)); err != nil {
					b.Fatal(err)
				}
			}
			id, err := dbCreatePortfolio(db, &Portfolio{ProjectID: "bench", Name: "Valuation", AllowedClasses: []string{"equity"}, StartingCash: 100000})
			if err != nil {
				b.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < markCount; i++ {
				symbol := fmt.Sprintf("ASSET%d", i)
				if _, err := tx.Exec(`INSERT INTO marks(symbol,asset_class,price,marked_at) VALUES(?,'equity',100,CURRENT_TIMESTAMP)`, symbol); err != nil {
					b.Fatal(err)
				}
				if i < 100 {
					if _, err := tx.Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,qty,avg_cost) VALUES('bench',?,?,'equity',1,100)`, id, symbol); err != nil {
						b.Fatal(err)
					}
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			pf := &Portfolio{ID: id}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				equity, err := computeEquity(db, pf)
				if err != nil || equity != 110000 {
					b.Fatalf("equity=%v err=%v", equity, err)
				}
			}
		})
	}
}
