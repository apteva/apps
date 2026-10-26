package main

import (
	"encoding/json"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNetworkEvidenceValidationAndLiveProbe(t *testing.T) {
	for _, ip := range []string{"example.com", "127.0.0.1; touch /tmp/unsafe", "$(whoami)", ""} {
		if _, err := networkEvidenceCommand(ip, 443, 1); err == nil {
			t.Fatal("unsafe target accepted", ip)
		}
	}
	for _, v := range [][2]int{{0, 1}, {65536, 1}, {443, 0}, {443, 6}} {
		if _, err := networkEvidenceCommand("127.0.0.1", v[0], v[1]); err == nil {
			t.Fatal("invalid budget accepted")
		}
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	cmd, err := networkEvidenceCommand("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, code, err := runLocal(cmd, 10*time.Second)
	if err != nil || code != 0 {
		t.Fatal(code, err, raw)
	}
	var got struct {
		Samples []struct {
			Connected bool   `json:"connected"`
			At        string `json:"at"`
		} `json:"samples"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Samples) != 2 || !got.Samples[0].Connected || got.Samples[0].At == "" {
		t.Fatal(raw)
	}
	ln.Close()
	raw, code, err = runLocal(cmd, 10*time.Second)
	if err != nil || code != 0 || !strings.Contains(raw, `"connected": false`) {
		t.Fatal("failure not captured", raw, err)
	}
}
