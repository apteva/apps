//go:build darwin || linux

package main

import (
	"database/sql"
	"fmt"
	"syscall"
	"testing"
	"time"
)

func headBenchmarkCPU() int64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return int64(usage.Utime.Sec+usage.Stime.Sec)*1e9 + int64(usage.Utime.Usec+usage.Stime.Usec)*1000
}
func headIndexBenchmarkDB(b *testing.B, indexed bool) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE projection_generations(projection_id INTEGER,generation TEXT,lease_token TEXT,created_at_ms INTEGER,PRIMARY KEY(projection_id,generation)); CREATE INDEX projection_generations_gc_idx ON projection_generations(projection_id,created_at_ms,generation); CREATE TABLE ph_75(scope_key TEXT PRIMARY KEY,generation TEXT NOT NULL); CREATE TABLE projection_queue(projection_id INTEGER,project_id TEXT,scope_key TEXT,lease_token TEXT,claimed_until TEXT,PRIMARY KEY(projection_id,project_id,scope_key)); CREATE TABLE projection_retired_leases(projection_id INTEGER,lease_token TEXT,claimed_until TEXT,PRIMARY KEY(projection_id,lease_token)); CREATE TABLE writer_guard(id INTEGER PRIMARY KEY); INSERT INTO writer_guard VALUES(1);`)
	if err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO projection_generations VALUES(?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	counts := map[int]int{}
	for i := 0; i < 87037; i++ {
		p := i%87 + 1
		counts[p]++
		if _, err := stmt.Exec(p, fmt.Sprintf("g%06d", counts[p]), "", counts[p]); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	stmt, err = tx.Prepare(`INSERT INTO ph_75 VALUES(?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= 953; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("scope%04d", i), fmt.Sprintf("g%06d", i)); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if indexed {
		if _, err := db.Exec(`CREATE INDEX phg_75 ON ph_75(generation)`); err != nil {
			b.Fatal(err)
		}
	}
	return db
}
func BenchmarkProjectionHeadIndexDiscovery(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(map[bool]string{false: "legacy_scan", true: "generation_index"}[indexed], func(b *testing.B) {
			db := headIndexBenchmarkDB(b, indexed)
			query := `SELECT g.generation FROM projection_generations g WHERE g.projection_id=? AND NOT EXISTS(SELECT 1 FROM ph_75 h WHERE h.generation=g.generation) AND NOT EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=g.projection_id AND q.lease_token=g.lease_token AND q.claimed_until>CURRENT_TIMESTAMP) AND NOT EXISTS(SELECT 1 FROM projection_retired_leases l WHERE l.projection_id=g.projection_id AND l.lease_token=g.lease_token AND l.claimed_until>CURRENT_TIMESTAMP) ORDER BY g.created_at_ms,g.generation LIMIT 1`
			var generation string
			b.ReportAllocs()
			b.ResetTimer()
			cpu := headBenchmarkCPU()
			for i := 0; i < b.N; i++ {
				if err := db.QueryRow(query, 75).Scan(&generation); err != nil {
					b.Fatal(err)
				}
			}
			cpu = headBenchmarkCPU() - cpu
			b.StopTimer()
			b.ReportMetric(float64(cpu)/float64(b.N), "process_cpu_ns/op")
			if generation != "g000954" {
				b.Fatal("candidate changed", generation)
			}
		})
	}
}
func BenchmarkProjectionHeadIndexPublicationLock(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(map[bool]string{false: "legacy_scan", true: "generation_index"}[indexed], func(b *testing.B) {
			db := headIndexBenchmarkDB(b, indexed)
			var held, wait time.Duration
			b.ReportAllocs()
			b.ResetTimer()
			cpu := headBenchmarkCPU()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				tx, err := db.Begin()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.Exec(`UPDATE writer_guard SET id=id WHERE id=1`); err != nil {
					tx.Rollback()
					b.Fatal(err)
				}
				locked := time.Now()
				wait += locked.Sub(start)
				if _, err := tx.Exec(`UPDATE ph_75 SET generation=? WHERE scope_key IN (SELECT scope_key FROM ph_75 ORDER BY scope_key LIMIT 128)`, fmt.Sprintf("published_%d", i)); err != nil {
					tx.Rollback()
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				held += time.Since(locked)
			}
			cpu = headBenchmarkCPU() - cpu
			b.StopTimer()
			b.ReportMetric(float64(cpu)/float64(b.N), "process_cpu_ns/op")
			b.ReportMetric(float64(wait.Nanoseconds())/float64(b.N), "writer_wait_ns/op")
			b.ReportMetric(float64(held.Nanoseconds())/float64(b.N), "writer_held_ns/op")
		})
	}
}
