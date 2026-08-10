package selectorclick

import (
	"strings"
	"testing"
)

func TestSelectorErrorsRemainActionable(t *testing.T) {
	if _, err := Resolve(nil, "  "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty selector error=%v", err)
	}
}
