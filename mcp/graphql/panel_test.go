package main

import (
	"os"
	"strings"
	"testing"
)

func TestPanelImportsUseEffectUnderItsRuntimeName(t *testing.T) {
	body, err := os.ReadFile("ui/GraphQLPanel.mjs")
	if err != nil {
		t.Fatal(err)
	}
	panel := string(body)
	if !strings.Contains(panel, `import{useEffect,`) {
		t.Fatal("GraphQLPanel must import useEffect under the name used by the component")
	}
	if !strings.Contains(panel, `useEffect(()=>`) {
		t.Fatal("GraphQLPanel must register its schema-loading effect")
	}
}
