//go:build !linux

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

func listProcesses() ([]localProcess, error) {
	raw, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,uid=,rss=,stat=,etime=,command=").Output()
	if err != nil {
		return nil, err
	}
	var out []localProcess
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		p := localProcess{State: fields[5], Command: strings.Join(fields[7:], " ")}
		p.PID, _ = strconv.Atoi(fields[0])
		p.PPID, _ = strconv.Atoi(fields[1])
		p.PGID, _ = strconv.Atoi(fields[2])
		p.UID, _ = strconv.Atoi(fields[3])
		p.RSSKB, _ = strconv.ParseInt(fields[4], 10, 64)
		p.RunningSeconds = parseEtime(fields[6])
		if len(p.Command) > 300 {
			p.Command = p.Command[:300] + "…"
		}
		out = append(out, p)
	}
	return out, nil
}

// parseEtime reads ps's [[dd-]hh:]mm:ss.
func parseEtime(value string) int64 {
	var days int64
	if d, rest, ok := strings.Cut(value, "-"); ok {
		days, _ = strconv.ParseInt(d, 10, 64)
		value = rest
	}
	var total int64
	for _, part := range strings.Split(value, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		total = total*60 + n
	}
	return days*86400 + total
}
