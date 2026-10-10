package ruleengine

import (
	"embed"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

//go:embed schema.go market.go runtime.go schedule.go
var sourceFiles embed.FS

func SourceHash() string {
	files := map[string]string{}
	for _, name := range []string{"schema.go", "market.go", "runtime.go", "schedule.go"} {
		raw, _ := sourceFiles.ReadFile(name)
		files[name] = string(raw)
	}
	return sim.Hash(files)
}
