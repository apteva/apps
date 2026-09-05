package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A reproducible synthetic baseline; it excludes provider/network latency.
func BenchmarkInvoiceSearch10000(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		b.Fatal(err)
	}
	for _, p := range paths {
		raw, e := os.ReadFile(p)
		if e != nil {
			b.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			b.Fatal(e)
		}
	}
	_, err = db.Exec(`INSERT INTO customers(id,project_id,name,email) VALUES(1,'bench','Benchmark','bench@example.com');
 WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000)
 INSERT INTO invoices(project_id,customer_id,currency,number,status,total_cents,created_at,notes)
 SELECT 'bench',1,'USD','INV-'||x,'open',1200,datetime('2026-01-01','+'||x||' minutes'),'reference '||x FROM n;`)
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		f    invoiceFilters
	}{
		{"first_page", invoiceFilters{limit: 50}},
		{"deep_page", invoiceFilters{limit: 50, offset: 9000}},
		{"text_search", invoiceFilters{limit: 50, q: "reference 9999"}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, e := dbInvoiceSearch(db, "bench", tc.f); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
