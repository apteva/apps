//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hostedStopHelperPIDs struct {
	Server  int `json:"server"`
	Sidecar int `json:"sidecar"`
	Static  int `json:"static"`
}

func TestHostedProcessControlStopsSeparateGroupsInTenantSession(t *testing.T) {
	if mode := os.Getenv("FLEET_HOSTED_STOP_HELPER"); mode != "" {
		runHostedStopHelper(mode)
		return
	}

	dataDir := t.TempDir()
	port := freeTCPPort(t)
	readyPath := filepath.Join(t.TempDir(), "ready.json")
	root := exec.Command(os.Args[0], "-test.run=TestHostedProcessControlStopsSeparateGroupsInTenantSession")
	root.Env = append(os.Environ(),
		"FLEET_HOSTED_STOP_HELPER=root",
		"FLEET_HOSTED_STOP_READY="+readyPath,
		"FLEET_HOSTED_STOP_PORT="+strconv.Itoa(port),
		"APTEVA_HOME="+dataDir,
	)
	root.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupPIDs := []int{root.Process.Pid}
	t.Cleanup(func() {
		for _, pid := range cleanupPIDs {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_, _ = root.Process.Wait()
	})
	if err := os.WriteFile(filepath.Join(dataDir, "fleet.pid"), []byte(strconv.Itoa(root.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var children hostedStopHelperPIDs
	waitForFileJSON(t, readyPath, &children)
	cleanupPIDs = append(cleanupPIDs, children.Server, children.Sidecar, children.Static)

	script := hostedProcessControlScript(dataDir, port, 2, true)
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("stop script: %v: %s", err, out)
	}
	if hostedStopState(string(out)) != "stopped" {
		t.Fatalf("stop output did not confirm postcondition: %s", out)
	}
	for _, pid := range []int{root.Process.Pid, children.Server, children.Sidecar} {
		if processAlive(pid) {
			t.Errorf("tenant process %d survived full-session stop", pid)
		}
	}
	if !processAlive(children.Static) {
		t.Fatal("detached static deployment process was signalled")
	}
}

func runHostedStopHelper(mode string) {
	switch mode {
	case "server":
		port, _ := strconv.Atoi(os.Getenv("FLEET_HOSTED_STOP_PORT"))
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			os.Exit(2)
		}
		defer listener.Close()
		if err := os.WriteFile(os.Getenv("FLEET_HOSTED_STOP_SERVER_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		select {}
	case "root":
		serverReady := filepath.Join(filepath.Dir(os.Getenv("FLEET_HOSTED_STOP_READY")), "server-ready")
		server := exec.Command(os.Args[0], "-test.run=TestHostedProcessControlStopsSeparateGroupsInTenantSession")
		server.Env = append(os.Environ(), "FLEET_HOSTED_STOP_HELPER=server", "FLEET_HOSTED_STOP_SERVER_READY="+serverReady)
		server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := server.Start(); err != nil {
			os.Exit(4)
		}
		sidecar := exec.Command("sh", "-c", "while :; do sleep 60; done")
		sidecar.Env = os.Environ()
		sidecar.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := sidecar.Start(); err != nil {
			os.Exit(5)
		}
		static := exec.Command("sh", "-c", "while :; do sleep 60; done", "--static-server")
		static.Env = os.Environ()
		static.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := static.Start(); err != nil {
			os.Exit(6)
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(serverReady); err == nil {
				break
			}
		}
		payload, _ := json.Marshal(hostedStopHelperPIDs{Server: server.Process.Pid, Sidecar: sidecar.Process.Pid, Static: static.Process.Pid})
		if err := os.WriteFile(os.Getenv("FLEET_HOSTED_STOP_READY"), payload, 0o600); err != nil {
			os.Exit(7)
		}
		select {}
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForFileJSON(t *testing.T, path string, out any) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		payload, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(payload, out) == nil {
			return
		}
	}
	t.Fatalf("timed out waiting for helper state at %s", path)
}

func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat))
	return len(fields) < 3 || fields[2] != "Z"
}
