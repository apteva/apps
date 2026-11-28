//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const clockTicks = 100 // USER_HZ on every Linux Workspaces runs on

func listProcesses() ([]localProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	uptime := 0.0
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			uptime, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	pageKB := int64(os.Getpagesize() / 1024)
	var out []localProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		stat := string(raw)
		open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
		if open < 0 || end < open {
			continue
		}
		fields := strings.Fields(stat[end+1:])
		if len(fields) < 22 {
			continue
		}
		p := localProcess{PID: pid, State: fields[0], UID: processUID(filepath.Join(dir, "status"))}
		p.PPID, _ = strconv.Atoi(fields[1])
		p.PGID, _ = strconv.Atoi(fields[2])
		if start, err := strconv.ParseFloat(fields[19], 64); err == nil && uptime > 0 {
			p.RunningSeconds = int64(uptime - start/clockTicks)
		}
		if rss, err := strconv.ParseInt(fields[21], 10, 64); err == nil {
			p.RSSKB = rss * pageKB
		}
		if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(cmdline) > 0 {
			p.Command = strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		} else {
			p.Command = "[" + stat[open+1:end] + "]"
		}
		if len(p.Command) > 300 {
			p.Command = p.Command[:300] + "…"
		}
		out = append(out, p)
	}
	return out, nil
}
