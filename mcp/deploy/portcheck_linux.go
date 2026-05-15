//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pidOwnsPort reports whether pid is the process holding a LISTEN
// socket on port (any local address). Returns false on any read error
// — callers should treat "can't prove ownership" as "not owned".
//
// The check is procfs-only: parse /proc/net/tcp + tcp6 for LISTEN
// rows on `port`, collect inodes, then walk /proc/<pid>/fd looking
// for a socket whose target matches `socket:[<inode>]`. This is what
// `ss -tlnp` does under the hood — without the dependency.
//
// Why this matters: two apteva-servers on one host can race to grab
// the same port, and the supervisor's "is the process still alive"
// check can pass even when the listening process is now somebody
// else's. PID-owns-port is the only authoritative answer.
func pidOwnsPort(pid, port int) bool {
	if pid <= 0 || port <= 0 {
		return false
	}
	inodes := listenInodesForPort(port)
	if len(inodes) == 0 {
		return false
	}
	fdDir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		// `socket:[12345]` is the canonical procfs link form.
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode := target[len("socket:[") : len(target)-1]
		if inodes[inode] {
			return true
		}
	}
	return false
}

// systemListeningPorts returns every TCP port currently in LISTEN
// state across all PIDs. Used by the allocator to reject ports
// another instance is already holding before we even bind-probe.
// On read errors returns an empty map — bind-probe still catches
// what we missed.
func systemListeningPorts() map[int]bool {
	out := map[int]bool{}
	collectListenPorts("/proc/net/tcp", out)
	collectListenPorts("/proc/net/tcp6", out)
	return out
}

// listenInodesForPort returns the set of socket inodes for LISTEN
// rows on a specific port, across IPv4 and IPv6.
func listenInodesForPort(port int) map[string]bool {
	out := map[string]bool{}
	collectListenInodesForPort("/proc/net/tcp", port, out)
	collectListenInodesForPort("/proc/net/tcp6", port, out)
	return out
}

// procnet row layout (post-header):
//   sl  local_address  rem_address  st  ...  uid  timeout  inode  ...
// LISTEN state == "0A".
func collectListenPorts(path string, out map[int]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "0A" {
			continue
		}
		port, ok := parseHexPort(fields[1])
		if !ok {
			continue
		}
		out[port] = true
	}
}

func collectListenInodesForPort(path string, want int, out map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		// Need at least up through the inode column (index 9).
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" {
			continue
		}
		port, ok := parseHexPort(fields[1])
		if !ok || port != want {
			continue
		}
		out[fields[9]] = true
	}
}

// parseHexPort extracts the port half of a "addr:port" field where
// port is uppercase hex (kernel format). The address half is ignored
// — for LISTEN rows we only need the port.
func parseHexPort(addrPort string) (int, bool) {
	i := strings.LastIndexByte(addrPort, ':')
	if i < 0 || i == len(addrPort)-1 {
		return 0, false
	}
	n, err := strconv.ParseInt(addrPort[i+1:], 16, 32)
	if err != nil {
		return 0, false
	}
	return int(n), true
}
