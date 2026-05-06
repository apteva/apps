package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openSchemaDB applies the v1 migration into an in-memory SQLite. Same
// pattern as the deploy app's smoke test — keep them in sync.
func openSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for _, mig := range []string{"migrations/001_init.sql"} {
		body, err := os.ReadFile(mig)
		if err != nil {
			t.Fatal(err)
		}
		cleaned := stripSQLComments(string(body))
		for _, stmt := range strings.Split(cleaned, ";") {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := db.Exec(s); err != nil {
				t.Fatalf("%s: %v\n%s", mig, err, s)
			}
		}
	}
	return db
}

func stripSQLComments(s string) string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestStoreSmoke(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	c, err := dbInsertOrTouchCert(db, "p1", "app.acme.com")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if c.Status != "pending" || c.FQDN != "app.acme.com" {
		t.Fatalf("unexpected row: %+v", c)
	}
	// Idempotent — touching an existing row updates last_attempt_at.
	c2, err := dbInsertOrTouchCert(db, "p1", "app.acme.com")
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if c2.ID != c.ID {
		t.Fatalf("expected same id, got %d vs %d", c2.ID, c.ID)
	}

	// Pretend issuance succeeded.
	expires := time.Now().Add(60 * 24 * time.Hour)
	if err := dbSetCertIssued(db, c.ID,
		[]byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"),
		[]byte("-----BEGIN PRIVATE KEY-----\nFAKE\n-----END PRIVATE KEY-----\n"),
		"deadbeef",
		time.Now(), expires,
	); err != nil {
		t.Fatalf("set issued: %v", err)
	}
	got, _ := dbGetCert(db, c.ID)
	if got.Status != "live" || got.Serial != "deadbeef" {
		t.Fatalf("not live: %+v", got)
	}

	// Material is fetched only for live certs.
	mat, err := dbCertMaterial(db, "p1", "app.acme.com")
	if err != nil || mat == nil {
		t.Fatalf("material: %v %v", mat, err)
	}
	if mat.FQDN != "app.acme.com" || len(mat.CertPEM) == 0 {
		t.Fatalf("material content: %+v", mat)
	}
}

func TestRenewalScan(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	// Two live certs: one expiring in 5 days, one in 90.
	for fqdn, days := range map[string]int{
		"a.example.com": 5,
		"b.example.com": 90,
	} {
		row, _ := dbInsertOrTouchCert(db, "p1", fqdn)
		_ = dbSetCertIssued(db, row.ID, []byte("c"), []byte("k"), "x",
			time.Now().Add(-30*24*time.Hour),
			time.Now().Add(time.Duration(days)*24*time.Hour),
		)
	}
	due, err := dbDueForRenewal(db, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(due) != 1 || due[0].FQDN != "a.example.com" {
		t.Fatalf("expected only a.example.com, got %+v", due)
	}
}

func TestChallengeRecordName(t *testing.T) {
	cases := []struct {
		apex, sub, want string
	}{
		{"acme.com", "", "_acme-challenge"},
		{"acme.com", "app", "_acme-challenge.app"},
		{"acme.com", "deep.app", "_acme-challenge.deep.app"},
	}
	for _, tc := range cases {
		got := challengeRecordName(tc.apex, tc.sub)
		if got != tc.want {
			t.Errorf("apex=%q sub=%q: got %q want %q", tc.apex, tc.sub, got, tc.want)
		}
	}
}
