//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pidOwnsPort reports whether pid OR any of its descendants is the
// process holding a LISTEN socket on port. Returns false on any read
// error — callers should treat "can't prove ownership" as "not
// owned".
//
// The check is procfs-only: parse /proc/net/tcp + tcp6 for LISTEN
// rows on `port`, collect inodes, then walk /proc/<each>/fd in the
// pid's process tree looking for a socket whose target matches
// `socket:[<inode>]`. This is what `ss -tlnp` does under the hood —
// without the dependency.
//
// Why descendants matter: wrappers like `bun run start` are the
// recorded release.pid but the actual port-owner is a child (the
// bun script process). pre-v0.11.1 only checked the wrapper, so bun
// releases never promoted from starting→live. node/python/java
// wrappers behave the same way.
//
// Why this matters at all: two apteva-servers on one host can race
// to grab the same port, and "is the process still alive" can pass
// when the listening process is now somebody else's. PID-tree-owns-
// port is the authoritative answer.
func pidOwnsPort(pid, port int) bool {
	if pid <= 0 || port <= 0 {
		return false
	}
	inodes := listenInodesForPort(port)
	if len(inodes) == 0 {
		return false
	}
	for _, p := range processTreePids(pid) {
		if pidHoldsAnyInode(p, inodes) {
			return true
		}
	}
	return false
}

// pidHoldsAnyInode checks /proc/<pid>/fd for a socket entry whose
// inode is in want. Matches the inner loop of the old single-pid
// pidOwnsPort.
func pidHoldsAnyInode(pid int, want map[string]bool) bool {
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
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode := target[len("socket:[") : len(target)-1]
		if want[inode] {
			return true
		}
	}
	return false
}

// processTreePids returns root and every descendant pid, BFS'd through
// the procfs parent→child relationship. Cost: one /proc dirread + one
// /proc/<pid>/stat read per process on the box (a few ms for ~200
// procs). pidOwnsPort calls this on every probe tick, which is fine
// — probes run for 5s every 200ms during a spawn (25 ticks total)
// and once/minute from the watchdog.
//
// Cycle-proof via a visited map; depth-cap not needed because the
// BFS naturally terminates when no new children are found.
func processTreePids(root int) []int {
	out := []int{root}
	parentOf := map[int]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := readPPID(pid)
		if !ok {
			continue
		}
		parentOf[pid] = ppid
	}
	// Invert parent→child mapping for BFS.
	children := map[int][]int{}
	for c, p := range parentOf {
		children[p] = append(children[p], c)
	}
	visited := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if visited[c] {
				continue
			}
			visited[c] = true
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// findPidListeningOn returns the pid of the process holding a LISTEN
// socket on `port`, or 0 if none. Walks /proc + matches socket
// inodes — same machinery as pidOwnsPort, just inverted (we don't
// know the pid going in). Used by stopReleaseAuthoritative to find
// what's actually bound to a port when the in-memory handle is gone.
func findPidListeningOn(port int) int {
	inodes := listenInodesForPort(port)
	if len(inodes) == 0 {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pidHoldsAnyInode(pid, inodes) {
			return pid
		}
	}
	return 0
}

// readPPID extracts field 4 of /proc/<pid>/stat (parent pid). The
// stat format is "<pid> (<comm>) <state> <ppid> ..." — but <comm>
// can contain spaces and parentheses (bash -c 'foo bar' etc.), so
// the safe parse finds the LAST ')' and reads ppid as the field
// immediately after it.
func readPPID(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+4 > len(s) {
		return 0, false
	}
	// After ')' the layout is: " <state> <ppid> ..."
	rest := strings.Fields(s[i+1:])
	if len(rest) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
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
