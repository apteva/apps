package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	for _, name := range []string{"001_init.sql", "002_versions.sql", "003_function_url.sql", "004_retention_indexes.sql"} {
		b, e := os.ReadFile(filepath.Join("migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(b)); e != nil {
			t.Fatal(e)
		}
	}
	_, err = db.Exec(`CREATE TABLE _migrations(filename TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP);
 INSERT INTO functions(id,project_id,name,runtime,source_hash) VALUES(1,'test','original','node','hash');
 INSERT INTO function_versions(id,project_id,function_id,version,source_hash,build_status) VALUES(1,'test',1,1,'hash','ready');
 INSERT INTO function_invocations(project_id,function_id,status,trigger_kind,response_body) VALUES('test',1,'ok','manual','keep response');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func oldExecutionStatements(t *testing.T) []string {
	t.Helper()
	b, e := os.ReadFile("testdata/005_execution_identity_v1.8.0.sql")
	if e != nil {
		t.Fatal(e)
	}
	return strings.Split(strings.TrimSpace(string(b)), ";")[:16]
}

func assertExecutionSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, col := range executionColumns {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM pragma_table_info(?) WHERE name=?", col.table, col.name).Scan(&n); e != nil || n != 1 {
			t.Fatalf("column %s: %d %v", col.name, n, e)
		}
	}
	var n int
	if e := db.QueryRow("SELECT count(*) FROM _migrations WHERE filename=?", executionMigration).Scan(&n); e != nil || n != 1 {
		t.Fatalf("receipt: %d %v", n, e)
	}
	var response string
	if e := db.QueryRow("SELECT response_body FROM function_invocations WHERE id=1").Scan(&response); e != nil || response != "keep response" {
		t.Fatalf("data changed: %q %v", response, e)
	}
	var integrity string
	if e := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); e != nil || integrity != "ok" {
		t.Fatalf("integrity %s %v", integrity, e)
	}
}

// Every committed prefix from 1.8.0, including the exact production prefix
// (11 statements; no index/timing columns/receipt), must recover identically.
func TestExecutionMigrationEveryInterruptedPrefix(t *testing.T) {
	statements := oldExecutionStatements(t)
	for count := 0; count <= len(statements); count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := legacyMigrationDB(t)
			for _, statement := range statements[:count] {
				if _, e := db.Exec(statement); e != nil {
					t.Fatal(e)
				}
			}
			var before string
			if count >= 2 {
				if e := db.QueryRow("SELECT instance_key FROM functions WHERE id=1").Scan(&before); e != nil {
					t.Fatal(e)
				}
			}
			if e := migrateExecutionIdentity(context.Background(), db, nil); e != nil {
				t.Fatal(e)
			}
			assertExecutionSchema(t, db)
			var after string
			if e := db.QueryRow("SELECT instance_key FROM functions WHERE id=1").Scan(&after); e != nil {
				t.Fatal(e)
			}
			var artifact string
			if e := db.QueryRow("SELECT artifact_key FROM function_versions WHERE id=1").Scan(&artifact); e != nil || artifact != after {
				t.Fatalf("artifact identity %q != %q: %v", artifact, after, e)
			}
			if len(after) != 32 || (before != "" && after != before) {
				t.Fatalf("identity changed %q -> %q", before, after)
			}
			if e := migrateExecutionIdentity(context.Background(), db, nil); e != nil {
				t.Fatal(e)
			}
			var again string
			db.QueryRow("SELECT instance_key FROM functions WHERE id=1").Scan(&again)
			if after != again {
				t.Fatal("retry changed identity")
			}
		})
	}
}

func TestExecutionMigrationCancellationRollsBack(t *testing.T) {
	for _, cancelAt := range []int{1, 13, 14, 15} {
		t.Run(fmt.Sprint(cancelAt), func(t *testing.T) {
			db := legacyMigrationDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := migrateExecutionIdentity(ctx, db, func(done, total int) {
				if done == cancelAt {
					cancel()
				}
			})
			if e == nil {
				t.Fatal("canceled migration succeeded")
			}
			var n int
			if e = db.QueryRow("SELECT count(*) FROM pragma_table_info('functions') WHERE name='instance_key'").Scan(&n); e != nil || n != 0 {
				t.Fatalf("DDL survived cancellation: %d %v", n, e)
			}
			if e = db.QueryRow("SELECT count(*) FROM _migrations").Scan(&n); e != nil || n != 0 {
				t.Fatalf("receipt survived: %d %v", n, e)
			}
			if e = migrateExecutionIdentity(context.Background(), db, nil); e != nil {
				t.Fatal(e)
			}
			assertExecutionSchema(t, db)
		})
	}
}

func TestExecutionMigrationRejectsConflictingSchema(t *testing.T) {
	for _, bad := range []string{"ALTER TABLE functions ADD COLUMN instance_key INTEGER", "CREATE INDEX ix_inv_fn_id ON function_invocations(status)"} {
		t.Run(bad, func(t *testing.T) {
			db := legacyMigrationDB(t)
			if _, e := db.Exec(bad); e != nil {
				t.Fatal(e)
			}
			if e := migrateExecutionIdentity(context.Background(), db, nil); e == nil {
				t.Fatal("accepted conflicting schema")
			}
			var n int
			db.QueryRow("SELECT count(*) FROM _migrations").Scan(&n)
			if n != 0 {
				t.Fatal("recorded incompatible schema")
			}
		})
	}
}
