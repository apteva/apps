package backtest

import "embed"

//go:embed engine.go contracts.go protection.go
var sourceFiles embed.FS

func SourceHash() string {
	files := map[string]string{}
	for _, name := range []string{"engine.go", "contracts.go", "protection.go"} {
		raw, _ := sourceFiles.ReadFile(name)
		files[name] = string(raw)
	}
	return Hash(files)
}
