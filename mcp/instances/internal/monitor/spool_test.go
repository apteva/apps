package monitor

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestStreamedSpoolPreservesLegacyHistory(t *testing.T) {
	e := NewEngine()
	now := time.Now().UnixMilli()
	m := reading(now, 98, 100)
	e.Latest = &m
	e.Points = []Point{readingPoint(m, now/1000*1000-2000, 1000), readingPoint(m, now/1000*1000-1000, 1000), readingPoint(m, now/1000*1000, 1000)}
	e.Incidents = []Incident{{ID: "recording", Start: now - 2000, Updated: now, Peak: 98, PeakAt: now, Reason: "CPU ≥90%", Recordings: []Reading{{Time: now, CPU: 98, Core: 100, IntervalMS: 250}}, Processes: []Process{{PID: 42, Name: "name with \"quotes\" and λ", CPU: 50}}}}
	legacy, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spool.json.gz")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.Export(0), updated.Export(0)) {
		t.Fatal("streaming checkpoint changed legacy history, metrics or incident detail")
	}
	// Rotate the point ring, close the active recording and save again. Reused
	// members must remain valid JSON as the first element of each array changes.
	e.Points = append(e.Points[1:], readingPoint(m, now/1000*1000+1000, 1000))
	e.Incidents[0].End = now
	e.Incidents[0].Updated = now + 1000
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	e.Incidents[0].Updated++
	e.Incidents[0].Peak = 99
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	updated, err = Load(path)
	if err != nil || !reflect.DeepEqual(e.Export(0), updated.Export(0)) {
		t.Fatalf("cached checkpoint lost updated or rotated data: %v", err)
	}
	if e.checkpoint.bytes > checkpointCacheBytes || len(e.checkpoint.points) > len(e.Points) || len(e.checkpoint.incidents) > len(e.Incidents) {
		t.Fatal("checkpoint cache outgrew retained source history")
	}
	// Budget expiry clears detail without changing the incident's Updated time.
	// Cached members must not resurrect expired detail on the next checkpoint.
	e.Incidents[0].Recordings = nil
	e.Incidents[0].DetailExpired = true
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	updated, err = Load(path)
	if err != nil || !reflect.DeepEqual(e.Export(0), updated.Export(0)) {
		t.Fatalf("cached checkpoint resurrected expired detail: %v", err)
	}
}

func TestSpoolSaveFailureKeepsLastCheckpoint(t *testing.T) {
	e := NewEngine()
	m := reading(time.Now().UnixMilli(), 10, 10)
	e.Latest = &m
	path := filepath.Join(t.TempDir(), "spool.json.gz")
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	m.CPU.TotalPct = math.NaN()
	if err := e.Save(path); err == nil {
		t.Fatal("invalid reading checkpoint succeeded")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed checkpoint replaced recoverable spool")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("failed checkpoint left temporary file")
	}
	// A failed encoder must also leave the pooled compressor reusable.
	m.CPU.TotalPct = 10
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestSpoolLimitRejectsExcessBeforeWriting(t *testing.T) {
	var out bytes.Buffer
	w := spoolLimitWriter{out: &out, remaining: 8}
	if n, err := w.Write([]byte("12345678")); n != 8 || err != nil {
		t.Fatalf("valid compressed bytes rejected: %d %v", n, err)
	}
	if n, err := w.Write([]byte("9")); n != 0 || err == nil || out.Len() != 8 {
		t.Fatalf("spool exceeded compressed limit: %d %v %d", n, err, out.Len())
	}
}

func TestCheckpointWhileSampling(t *testing.T) {
	e := NewEngine()
	base := time.Now().Add(-3 * time.Minute).UnixMilli()
	dir := t.TempDir()
	var workers sync.WaitGroup
	errors := make(chan error, 2)
	for i := range 2 {
		path := filepath.Join(dir, string(rune('a'+i))+".json.gz")
		workers.Go(func() {
			for range 10 {
				if err := e.Save(path); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	for i := 1; i <= 500; i++ {
		e.Add(reading(base+int64(i)*250, 98, 100))
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "final.json.gz")
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(e.Export(0), loaded.Export(0)) {
		t.Fatalf("concurrent checkpoint lost completed samples or incident detail: %v", err)
	}
}
