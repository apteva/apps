package main

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteLinuxNetworkMetrics(t *testing.T) {
	// /proc/net/dev pads short interface names, but long names start in column 1.
	// Give byte and packet counters distinct values to detect shifted columns.
	fixture := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 99 3 0 0 0 0 0 0 99 4 0 0 0 0 0 0
  eth0: 4294967297 265 0 0 0 0 0 0 8589934593 217 0 0 0 0 0 0
enp1s0: 146981 266 0 0 0 0 0 0 123456 218 0 0 0 0 0 0
veth1234567: 777 8 0 0 0 0 0 0 999 9 0 0 0 0 0 0
ens"0: 10 1 0 0 0 0 0 0 20 2 0 0 0 0 0 0 0
ens\0: 30 1 0 0 0 0 0 0 40 2 0 0 0 0 0 0 0
`
	cmd := exec.Command("awk", "-F:", remoteLinuxNetworkAWK)
	cmd.Stdin = strings.NewReader(fixture)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("awk: %v: %s", err, output)
	}
	var got []NetMetrics
	if err := json.Unmarshal([]byte("["+strings.TrimSuffix(strings.TrimSpace(string(output)), ",")+"]"), &got); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	want := []NetMetrics{
		{Iface: "eth0", RxBytes: 4294967297, TxBytes: 8589934593},
		{Iface: "enp1s0", RxBytes: 146981, TxBytes: 123456},
		{Iface: "veth1234567", RxBytes: 777, TxBytes: 999},
		{Iface: `ens"0`, RxBytes: 10, TxBytes: 20},
		{Iface: `ens\0`, RxBytes: 30, TxBytes: 40},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMetricsCache_DeduplicatesConcurrentCollection(t *testing.T) {
	inst := &Instance{ID: 991, Provider: "manual", Status: "ready"}
	clearMetricsCache(inst.ID)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	collector := func() (*Metrics, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &Metrics{Timestamp: nowUTC()}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = collectMetricsCached(inst, collector) }()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls=%d, want 1", got)
	}
}
