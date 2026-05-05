package main

import (
	"strings"
	"testing"
)

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"books", true},
		{"reading_log", true},
		{"a", true},
		{"a_1", true},
		{"a1", true},
		{"", false},
		{"Books", false},                // uppercase
		{"1books", false},               // leading digit
		{"books-list", false},           // hyphen
		{"books table", false},          // space
		{"books;DROP TABLE x;", false},  // injection attempt
		{strings.Repeat("a", 65), false}, // too long
	}
	for _, c := range cases {
		err := validateIdentifier("table", c.name)
		if c.ok && err != nil {
			t.Errorf("%q: expected ok, got %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: expected error", c.name)
		}
	}
}

func TestCoerceForStorage_Types(t *testing.T) {
	cases := []struct {
		col     Column
		in      any
		wantErr bool
	}{
		{Column{Name: "t", Type: "text"}, "hello", false},
		{Column{Name: "t", Type: "text"}, 42, true},
		{Column{Name: "n", Type: "number"}, 42.5, false},
		{Column{Name: "n", Type: "number"}, 42, false},
		{Column{Name: "n", Type: "number"}, "42", true}, // strings aren't accepted
		{Column{Name: "b", Type: "bool"}, true, false},
		{Column{Name: "b", Type: "bool"}, 1, true},
		{Column{Name: "d", Type: "datetime"}, "2026-04-12T09:00:00Z", false},
		{Column{Name: "d", Type: "datetime"}, "yesterday", true},
		{Column{Name: "j", Type: "json"}, map[string]any{"a": 1}, false},
		{Column{Name: "f", Type: "file_id"}, 42.0, false},
		{Column{Name: "f", Type: "file_id"}, "42", true},
	}
	for i, c := range cases {
		_, err := coerceForStorage(c.col, c.in)
		if c.wantErr && err == nil {
			t.Errorf("case %d (%s/%T): expected error", i, c.col.Type, c.in)
		}
		if !c.wantErr && err != nil {
			t.Errorf("case %d (%s/%T): unexpected error %v", i, c.col.Type, c.in, err)
		}
	}
}

func TestCoerceForStorage_NullableEnforcement(t *testing.T) {
	required := Column{Name: "x", Type: "text", Nullable: false}
	if _, err := coerceForStorage(required, nil); err == nil {
		t.Error("expected error for nil on non-nullable column")
	}
	optional := Column{Name: "x", Type: "text", Nullable: true}
	if v, err := coerceForStorage(optional, nil); err != nil || v != nil {
		t.Errorf("nil on nullable: got (%v, %v)", v, err)
	}
}

func TestValidateReadOnlySQL(t *testing.T) {
	good := []string{
		"SELECT * FROM x",
		"  select 1",
		"WITH a AS (SELECT 1) SELECT * FROM a",
	}
	bad := []string{
		"DELETE FROM x",
		"UPDATE x SET y = 1",
		"DROP TABLE x",
		"PRAGMA foo",
		"SELECT 1; DELETE FROM x",
		"SELECT 1; DROP TABLE x;",
		"",
	}
	for _, s := range good {
		if err := validateReadOnlySQL(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validateReadOnlySQL(s); err == nil {
			t.Errorf("%q accepted but should have been refused", s)
		}
	}
}
