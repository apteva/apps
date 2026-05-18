//go:build !linux

package main

// pidOwnsPort: non-Linux platforms don't have /proc/net/tcp + procfs
// fd symlinks, so we can't prove ownership cheaply. Multi-tenant
// hosting is Linux-only; dev hosts (darwin) shouldn't have the gate
// rejecting genuinely-correct adopts and probes. Treat "can't check"
// as "trusted".
func pidOwnsPort(pid, port int) bool { return true }

// systemListeningPorts: same rationale — no portable enumeration. The
// bind-probe fallback in allocatePort is the safety net on dev hosts.
func systemListeningPorts() map[int]bool { return map[int]bool{} }

// findPidListeningOn: non-Linux stub. Returns 0 → stopReleaseAuthoritative
// falls through after the registry-stop attempt. Single-tenant dev hosts
// don't have the orphan class this guards against.
func findPidListeningOn(port int) int { return 0 }
