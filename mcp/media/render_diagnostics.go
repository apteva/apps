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

// Keep both context and the final cause, even after thousands of FFmpeg lines.
func truncateRenderFailure(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	marker := "\n... earlier output omitted ...\n"
	tail := (limit - len(marker)) * 3 / 4
	head := limit - len(marker) - tail
	return output[:head] + marker + output[len(output)-tail:]
}
