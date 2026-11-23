package main

import (
	"strings"
	"testing"
	"time"
)

func TestRemoteEnvScriptQuotesEveryByte(t *testing.T) {
	got := remoteEnvScript(map[string]string{"B": "it's\n$x `y`", "A": ""})
	if got != "export A=''\nexport B='it'\\''s\n$x `y`'\n" {
		t.Fatalf("%q", got)
	}
}

func TestRemoteArchiveLimit(t *testing.T) {
	if remoteArchiveLimitError(16<<20) != nil {
		t.Fatal("16 MiB should be allowed")
	}
	if err := remoteArchiveLimitError(16<<20 + 1); err == nil || !strings.Contains(err.Error(), "max_archive_bytes") {
		t.Fatalf("limit: %v", err)
	}
}

func TestRemoteCallsGetLongerTimeouts(t *testing.T) {
	if executionCallTimeout(LocalDocker{}, 10*time.Second) != 10*time.Second {
		t.Fatal("local timeout changed")
	}
	if executionCallTimeout(&RemoteDocker{}, 10*time.Second) < time.Minute {
		t.Fatal("remote timeout too short for an SSH round trip")
	}
}
