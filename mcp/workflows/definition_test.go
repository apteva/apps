package main

import (
	"strings"
	"testing"
)

const validYAML = `
name: test-flow
trigger:
  kind: manual
steps:
  - id: greet
    kind: emit
    topic: hello
    data: { msg: world }
  - id: maybe
    kind: branch
    when: "input.go == true"
    else: { goto: greet }
`

func TestParseValid(t *testing.T) {
	d, err := ParseDefinition([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if d.Name != "test-flow" {
		t.Errorf("Name = %q", d.Name)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(d.Steps))
	}
}

func TestParseRejectsDuplicateStepID(t *testing.T) {
	src := `
name: dup
steps:
  - { id: a, kind: emit, topic: t }
  - { id: a, kind: emit, topic: t }
`
	_, err := ParseDefinition([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate error, got %v", err)
	}
}

func TestParseRejectsBadKind(t *testing.T) {
	src := `
name: bad
steps:
  - { id: x, kind: superpower }
`
	_, err := ParseDefinition([]byte(src))
	if err == nil {
		t.Error("expected validation error for unknown kind")
	}
}

func TestParseRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"http needs url or app/path": `
name: t
steps:
  - { id: x, kind: http }
`,
		"function needs name": `
name: t
steps:
  - { id: x, kind: function }
`,
		"branch needs when": `
name: t
steps:
  - { id: x, kind: branch }
`,
	}
	for label, src := range cases {
		if _, err := ParseDefinition([]byte(src)); err == nil {
			t.Errorf("%s: expected error", label)
		}
	}
}

func TestParseRejectsBadGotoTarget(t *testing.T) {
	src := `
name: t
steps:
  - id: a
    kind: branch
    when: "true"
    else: { goto: nonexistent }
`
	if _, err := ParseDefinition([]byte(src)); err == nil {
		t.Error("expected error for unknown goto target")
	}
}

func TestParseAcceptsJSON(t *testing.T) {
	src := `{"name":"j","steps":[{"id":"a","kind":"emit","topic":"t"}]}`
	d, err := ParseDefinition([]byte(src))
	if err != nil {
		t.Fatalf("ParseDefinition (json): %v", err)
	}
	if d.Name != "j" {
		t.Errorf("Name = %q", d.Name)
	}
}
