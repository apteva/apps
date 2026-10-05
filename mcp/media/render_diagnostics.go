package main

import "strings"

// Only normal cache telemetry is removed. Integrity, scratch-space, download,
// encoding and upload errors remain part of the primary failure.
func splitRemoteRenderDiagnostics(output string) (primary string, hits, misses int) {
	lines := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "REMOTE_SOURCE_CACHE_HIT "):
			hits++
		case strings.HasPrefix(line, "REMOTE_SOURCE_CACHE_MISS "):
			misses++
		default:
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), hits, misses
}
