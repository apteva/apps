package main

import (
	_ "embed"
	"fmt"
)

//go:embed scripts/extract_tenant.py
var remoteArchiveExtractor string

func remoteExtractCommand(source, destination string) string {
	return fmt.Sprintf("python3 - %s %s <<'FLEET_SAFE_EXTRACT'\n%s\nFLEET_SAFE_EXTRACT\n", sh(source), sh(destination), remoteArchiveExtractor)
}
func remotePublishCommand(source, destination string) string {
	return fmt.Sprintf("python3 - --publish %s %s <<'FLEET_SAFE_PUBLISH'\n%s\nFLEET_SAFE_PUBLISH\n", sh(source), sh(destination), remoteArchiveExtractor)
}
