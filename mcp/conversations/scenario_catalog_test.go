package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Fail before spending LLM tokens if a renamed/deleted workflow would cause Go
// to exit successfully with "no tests to run", or if a new workflow has no YAML.
func TestScenarioCatalogCoversEveryWorkflow(t *testing.T) {
	functions := map[string]bool{}
	for _, path := range []string{"scenario_test.go", "scenario_ownership_test.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "TestScenario_") {
				functions[fn.Name.Name] = false
			}
		}
	}
	files, err := filepath.Glob("scenarios/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var scenario struct {
			Setup struct {
				Driver []string `yaml:"driver"`
			} `yaml:"setup"`
		}
		if err := yaml.Unmarshal(raw, &scenario); err != nil {
			t.Fatal(err)
		}
		found := ""
		for i, arg := range scenario.Setup.Driver {
			if arg == "-run" && i+1 < len(scenario.Setup.Driver) {
				found = strings.TrimSuffix(strings.TrimPrefix(scenario.Setup.Driver[i+1], "^"), "$")
			}
		}
		used, exists := functions[found]
		if !exists || used {
			t.Fatalf("%s points to missing or duplicate workflow %q", path, found)
		}
		functions[found] = true
	}
	for name, used := range functions {
		if !used {
			t.Errorf("workflow %s has no apteva test scenario", name)
		}
	}
}
