// Run with GOWORK=off go run ./scripts/reproduce-read-cancellation.go.
// Uses a disposable in-memory DB and synthetic data; no Tables instrumentation.
package main

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"time"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	rows, err := db.QueryContext(ctx, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<2000000) SELECT 1 AS n UNION ALL SELECT sum(x) FROM n`)
	if err != nil {
		panic(err)
	}
	count := 0
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			break
		}
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	elapsed := time.Since(start)
	fmt.Printf("deadline_ms=20 elapsed_ms=%.1f rows_read=%d error=%v pool_in_use=%d\n", float64(elapsed)/float64(time.Millisecond), count, err, db.Stats().InUse)
	var n int
	if nextErr := db.QueryRow("SELECT 42").Scan(&n); nextErr != nil || n != 42 {
		fmt.Printf("connection reuse failed: %v\n", nextErr)
		os.Exit(2)
	}
	if elapsed > 250*time.Millisecond {
		fmt.Println("FAIL: cancellation did not promptly interrupt result iteration")
		os.Exit(1)
	}
	fmt.Println("PASS: result iteration stopped promptly")
}
