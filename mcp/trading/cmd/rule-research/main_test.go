package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveCSVTimestampUnitsAndOrdering(t *testing.T) {
	for _, timestamp := range []string{"1735689600000", "1735689600000000"} {
		path := filepath.Join(t.TempDir(), "klines.csv")
		if err := os.WriteFile(path, []byte("open_time,open,high,low,close,volume,close_time\n"+timestamp+",100,101,99,100,5,1735689659999\n"), 0644); err != nil {
			t.Fatal(err)
		}
		rows, hash, err := readCSV(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || !rows[0].At.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) || len(hash) != 64 {
			t.Fatal("archive timestamp or source identity differs")
		}
	}
	path := filepath.Join(t.TempDir(), "duplicate.csv")
	if err := os.WriteFile(path, []byte("1735689600000,100,101,99,100,5,0\n1735689600000,100,101,99,100,5,0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCSV(path); err == nil {
		t.Fatal("duplicate source rows accepted")
	}
}
