package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

// Use a retained production spool to compare full-history CPU/allocation cost:
// INSTANCES_MONITOR_BENCH_SPOOL=/path/spool.json.gz go test ./internal/monitor -run '^$' -bench BenchmarkSpoolSave -benchmem
func BenchmarkSpoolSave(b *testing.B) {
	path := os.Getenv("INSTANCES_MONITOR_BENCH_SPOOL")
	if path == "" {
		b.Skip("set INSTANCES_MONITOR_BENCH_SPOOL to a retained collector spool")
	}
	e, err := Load(path)
	if err != nil {
		b.Fatal(err)
	}
	out := filepath.Join(b.TempDir(), "spool.json.gz")
	// Compare steady-state saves once immutable entries are cached. The cold
	// checkpoint runs only once after a collector start, outside the timed loop.
	if err := e.Save(out); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := e.Save(out); err != nil {
			b.Fatal(err)
		}
	}
	if destination := os.Getenv("INSTANCES_MONITOR_BENCH_OUTPUT"); destination != "" {
		data, err := os.ReadFile(out)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			b.Fatal(err)
		}
	}
}
