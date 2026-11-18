package main

import (
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/code/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func caseByName(cases []TestCase, name string) *TestCase {
	for i := range cases {
		if cases[i].Name == name || strings.HasSuffix(cases[i].Name, name) {
			return &cases[i]
		}
	}
	return nil
}

// The fixtures are real reports: bun test 1.3.13, vitest 3.2.4 and
// jest 29.7.0 with jest-junit 16.0.0, run on the same five tests.
func TestParseJUnitFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, failKey string
		total         int
		wantMessage   string
	}{
		{"bun-junit.xml", "src/math.test.ts :: add > fails on purpose", 6, "AssertionError"},
		{"vitest-junit.xml", "vt/math.test.ts :: add > fails on purpose", 5, "expected 4 to be 5"},
		{"jest-junit.xml", "add > add fails on purpose", 5, "Error: expect(received).toBe(expected)"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			cases, err := parseJUnit(readFixture(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			s := summarize(cases)
			if s.Tests != tc.total || s.Failed != 2 || s.Skipped != 1 || s.Passed != tc.total-3 {
				t.Fatalf("summary %+v cases %+v", s, cases)
			}
			var failed *TestCase
			for i := range cases {
				if cases[i].Key == tc.failKey {
					failed = &cases[i]
				}
			}
			if failed == nil {
				keys := []string{}
				for _, c := range cases {
					keys = append(keys, c.Key)
				}
				t.Fatalf("no case %q in %q", tc.failKey, keys)
			}
			if failed.Status != "failed" || !strings.Contains(failed.Message, tc.wantMessage) {
				t.Fatalf("failed case %+v", failed)
			}
		})
	}
}

func TestParseJUnitBunNestedSuites(t *testing.T) {
	cases, err := parseJUnit(readFixture(t, "bun-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	top := caseByName(cases, "top level passes")
	if top == nil || top.Key != "src/other.test.ts :: top level passes" || top.File != "src/other.test.ts" {
		t.Fatalf("%+v", top)
	}
	skipped := caseByName(cases, "skipped one")
	if skipped == nil || skipped.Status != "skipped" || skipped.Suite != "div" {
		t.Fatalf("%+v", skipped)
	}
	adds := caseByName(cases, "adds")
	if adds == nil || adds.DurationMS != 1 {
		t.Fatalf("%+v", adds)
	}
}

func TestParseJUnitEdgeCases(t *testing.T) {
	if _, err := parseJUnit([]byte(`<html><body>nope</body></html>`)); err == nil {
		t.Fatal("accepted a non-JUnit document")
	}
	if _, err := parseJUnit([]byte(`<testsuite><testcase name="a">`)); err == nil {
		t.Fatal("accepted truncated XML")
	}
	cases, err := parseJUnit([]byte(`<testsuite name="s" file="a.test.ts">
		<testcase name="same"/><testcase name="same"><error message="boom">trace</error></testcase>
		<testcase name="msg"><failure>first line
second line</failure></testcase></testsuite>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 || cases[1].Key != "a.test.ts :: s > same #2" || cases[1].Status != "error" || cases[1].Message != "boom" || cases[1].Stack != "trace" {
		t.Fatalf("%+v", cases)
	}
	if cases[2].Message != "first line" {
		t.Fatalf("%+v", cases[2])
	}
	long := strings.Repeat("x", maxCaseMessage+100)
	cases, _ = parseJUnit([]byte(`<testsuite><testcase name="l"><failure message="` + long + `"/></testcase></testsuite>`))
	if len(cases[0].Message) > maxCaseMessage+4 {
		t.Fatal("message not truncated")
	}
}

func TestParseLcov(t *testing.T) {
	cov, err := parseLcov(readFixture(t, "bun-lcov.info"))
	if err != nil {
		t.Fatal(err)
	}
	if cov.Files != 1 || cov.LinesFound != 3 || cov.LinesHit != 3 || cov.LinesPct != 100 || cov.FunctionsPct == nil || *cov.FunctionsPct != 66.67 || cov.BranchesPct != nil {
		t.Fatalf("%+v", cov)
	}
	cov, err = parseLcov([]byte("SF:a.ts\nDA:1,1\nDA:2,0\nBRF:4\nBRH:1\nend_of_record\nSF:b.ts\nLF:2\nLH:1\nend_of_record\n"))
	if err != nil || cov.Files != 2 || cov.LinesFound != 4 || cov.LinesHit != 2 || cov.LinesPct != 50 || *cov.BranchesPct != 25 {
		t.Fatalf("%+v %v", cov, err)
	}
	if _, err = parseLcov([]byte("not coverage")); err == nil {
		t.Fatal("accepted empty coverage")
	}
}
