package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	maxCases       = 20000
	maxCaseMessage = 8 << 10
	maxCaseKey     = 1000
)

// TestCase is one case read from a JUnit report.
type TestCase struct {
	Key        string `json:"case_key"`
	File       string `json:"file,omitempty"`
	Suite      string `json:"suite,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"` // passed, failed, error, skipped (flaky is set by the runner)
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Stack      string `json:"stack,omitempty"`
	// Quarantined failures are recorded but do not fail the check.
	Quarantined bool `json:"quarantined,omitempty"`
}

// parseJUnit reads Bun, Vitest and Jest JUnit reports, including nested
// <testsuite> elements (Bun nests describe blocks inside a file suite).
func parseJUnit(data []byte) ([]TestCase, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	type frame struct{ name, file string }
	var stack []frame
	var cases []TestCase
	seen := map[string]int{}
	var current *TestCase
	var body strings.Builder
	inResult := ""
	root := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("JUnit report is not valid XML: %v", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuites":
				root = true
			case "testsuite":
				root = true
				stack = append(stack, frame{name: attr(t, "name"), file: attr(t, "file")})
			case "testcase":
				root = true
				if current != nil {
					return nil, errors.New("JUnit report nests a testcase inside a testcase")
				}
				if len(cases) >= maxCases {
					return nil, fmt.Errorf("JUnit report has more than %d test cases", maxCases)
				}
				c := TestCase{Name: strings.TrimSpace(attr(t, "name")), Status: "passed", File: attr(t, "file")}
				classname := strings.TrimSpace(attr(t, "classname"))
				if c.File == "" {
					for i := len(stack) - 1; i >= 0 && c.File == ""; i-- {
						c.File = stack[i].file
					}
				}
				// The describe path below the file: Bun nests describe suites;
				// Vitest puts "describe > name" in the name; Jest uses classname.
				var path []string
				for _, f := range stack {
					if f.name != "" && f.name != c.File && f.file != "" {
						path = append(path, f.name)
					}
				}
				switch {
				case len(path) > 0:
					c.Suite = strings.Join(path, " > ")
				case classname != "" && classname != c.File && classname != c.Name:
					c.Suite = classname
				case c.File == "" && len(stack) > 0:
					c.Suite = stack[len(stack)-1].name
				}
				if c.File == "" && classname != "" && looksLikeFile(classname) {
					c.File, c.Suite = classname, ""
				}
				if seconds, err := strconv.ParseFloat(attr(t, "time"), 64); err == nil && seconds > 0 && !math.IsInf(seconds, 0) {
					c.DurationMS = int64(math.Round(seconds * 1000))
				}
				current = &c
			case "failure", "error", "skipped":
				if current == nil {
					continue
				}
				switch t.Name.Local {
				case "skipped":
					if current.Status == "passed" {
						current.Status = "skipped"
					}
				case "failure":
					if current.Status != "error" {
						current.Status = "failed"
					}
				case "error":
					current.Status = "error"
				}
				if t.Name.Local != "skipped" && current.Message == "" {
					current.Message = attr(t, "message")
					if current.Message == "" {
						current.Message = attr(t, "type")
					}
				}
				inResult = t.Name.Local
				body.Reset()
			}
		case xml.CharData:
			if inResult != "" && body.Len() < maxCaseMessage*2 {
				body.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "testsuite":
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			case "failure", "error":
				if current != nil && inResult == t.Name.Local {
					text := strings.TrimSpace(body.String())
					if current.Stack == "" {
						current.Stack = text
					}
					if current.Message == "" || current.Message == "AssertionError" || current.Message == "Error" {
						if first := headLine(text); first != "" {
							current.Message = first
						}
					}
				}
				inResult = ""
			case "skipped":
				inResult = ""
			case "testcase":
				if current == nil {
					continue
				}
				c := *current
				current = nil
				c.Message = truncateText(c.Message, maxCaseMessage)
				c.Stack = truncateText(c.Stack, maxCaseMessage)
				c.Key = caseKey(c.File, c.Suite, c.Name)
				if n := seen[c.Key]; n > 0 {
					seen[c.Key] = n + 1
					c.Key = fmt.Sprintf("%s #%d", c.Key, n+1)
				} else {
					seen[c.Key] = 1
				}
				cases = append(cases, c)
			}
		}
	}
	if !root {
		return nil, errors.New("file is not a JUnit report (no testsuites, testsuite or testcase element)")
	}
	return cases, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func looksLikeFile(s string) bool {
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"} {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

func headLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && cut > limit-4 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return text[:cut] + "…"
}

// caseKey identifies a case across runs: file, describe path and name.
func caseKey(file, suite, name string) string {
	parts := []string{}
	if file != "" {
		parts = append(parts, file)
	}
	title := name
	if suite != "" {
		title = suite + " > " + name
	}
	parts = append(parts, title)
	return truncateText(strings.Join(parts, " :: "), maxCaseKey)
}

// Coverage is the lcov total over all source files.
type Coverage struct {
	Files          int      `json:"files"`
	LinesFound     int      `json:"lines_found"`
	LinesHit       int      `json:"lines_hit"`
	LinesPct       float64  `json:"lines_pct"`
	FunctionsFound int      `json:"functions_found"`
	FunctionsHit   int      `json:"functions_hit"`
	FunctionsPct   *float64 `json:"functions_pct,omitempty"`
	BranchesFound  int      `json:"branches_found"`
	BranchesHit    int      `json:"branches_hit"`
	BranchesPct    *float64 `json:"branches_pct,omitempty"`
	// DeltaLinesPct compares with this check's previous run with coverage.
	DeltaLinesPct *float64 `json:"delta_lines_pct,omitempty"`
}

// parseLcov totals LF/LH, FNF/FNH and BRF/BRH. Files without LF/LH fall back
// to counting DA lines.
func parseLcov(data []byte) (Coverage, error) {
	var cov Coverage
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var lf, lh, da, daHit int
	inFile, haveLF := false, false
	flush := func() {
		if !inFile {
			return
		}
		cov.Files++
		if haveLF {
			cov.LinesFound += lf
			cov.LinesHit += lh
		} else {
			cov.LinesFound += da
			cov.LinesHit += daHit
		}
		lf, lh, da, daHit, haveLF, inFile = 0, 0, 0, 0, false, false
	}
	number := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		if n < 0 {
			return 0
		}
		return n
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, _ := strings.Cut(line, ":")
		switch key {
		case "SF":
			flush()
			inFile = true
		case "LF":
			lf, haveLF = number(value), true
		case "LH":
			lh = number(value)
		case "DA":
			parts := strings.Split(value, ",")
			if len(parts) >= 2 {
				da++
				if number(parts[1]) > 0 {
					daHit++
				}
			}
		case "FNF":
			cov.FunctionsFound += number(value)
		case "FNH":
			cov.FunctionsHit += number(value)
		case "BRF":
			cov.BranchesFound += number(value)
		case "BRH":
			cov.BranchesHit += number(value)
		case "end_of_record":
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return cov, fmt.Errorf("coverage report: %v", err)
	}
	flush()
	if cov.Files == 0 {
		return cov, errors.New("coverage report has no lcov records (SF: lines)")
	}
	cov.LinesPct = percent(cov.LinesHit, cov.LinesFound)
	if cov.FunctionsFound > 0 {
		p := percent(cov.FunctionsHit, cov.FunctionsFound)
		cov.FunctionsPct = &p
	}
	if cov.BranchesFound > 0 {
		p := percent(cov.BranchesHit, cov.BranchesFound)
		cov.BranchesPct = &p
	}
	return cov, nil
}

func percent(hit, found int) float64 {
	if found == 0 {
		return 100
	}
	return math.Round(float64(hit)*10000/float64(found)) / 100
}

// caseSummary counts cases by outcome.
type caseSummary struct {
	Tests               int `json:"tests"`
	Passed              int `json:"passed"`
	Failed              int `json:"failed"`
	Errors              int `json:"errors"`
	Skipped             int `json:"skipped"`
	Flaky               int `json:"flaky"`
	QuarantinedFailures int `json:"quarantined_failures"`
}

func summarize(cases []TestCase) caseSummary {
	var s caseSummary
	for _, c := range cases {
		s.Tests++
		switch c.Status {
		case "passed":
			s.Passed++
		case "skipped":
			s.Skipped++
		case "flaky":
			s.Flaky++
		case "failed", "error":
			if c.Quarantined {
				s.QuarantinedFailures++
			} else if c.Status == "error" {
				s.Errors++
			} else {
				s.Failed++
			}
		}
	}
	return s
}
