package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Editing engine. All operations take a FileStore and a repo slug;
// none of them know whether the bytes live on disk, in Storage, or
// in S3. The MCP tools call directly into this layer.

// ─── Read ───────────────────────────────────────────────────────────

// ReadResult is what code_read_file returns. Content carries
// cat -n line numbers prefixed (one tab between number and line) so
// agents can reference path:line directly. TotalLines is the line
// count of the whole file even when the read was partial; agents use
// it to decide whether to re-read with a different offset.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// readWithLineNumbers fetches a file and renders it with line numbers.
// offset is 1-indexed; <=0 means "start at line 1". limit is the max
// lines to return; <=0 means "default" (defaultReadLimit). When the
// file is very large and offset+limit doesn't cover it all, Truncated
// is set.
const defaultReadLimit = 2000

func readWithLineNumbers(store FileStore, slug, p string, offset, limit int) (*ReadResult, error) {
	body, sha, err := readWithSHA(store, slug, p)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(body), "\n")
	// strings.Split appends a trailing empty entry when the file ends
	// with \n — drop it so TotalLines matches what `wc -l + 1` says
	// about visible lines.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}
	start := offset - 1
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	view := lines[start:end]

	// cat -n style: right-aligned line number, tab, content. Agents
	// trained on Claude Code recognise this pattern.
	width := numWidth(end)
	var b strings.Builder
	for i, ln := range view {
		fmt.Fprintf(&b, "%*d\t%s\n", width, start+i+1, ln)
	}

	return &ReadResult{
		Path:       p,
		Content:    b.String(),
		TotalLines: total,
		StartLine:  start + 1,
		EndLine:    end,
		Size:       int64(len(body)),
		SHA256:     sha,
		Truncated:  end < total,
	}, nil
}

func numWidth(n int) int {
	if n <= 0 {
		return 1
	}
	w := 0
	for n > 0 {
		w++
		n /= 10
	}
	if w < 4 {
		w = 4 // match Claude Code's minimum width
	}
	return w
}

// ─── Edit (exact-string replacement, uniqueness enforced) ───────────

// EditResult is what code_edit_file returns when an edit succeeds.
type EditResult struct {
	Path        string `json:"path"`
	Replacements int   `json:"replacements"`
	OldSHA256   string `json:"old_sha256"`
	NewSHA256   string `json:"new_sha256"`
	NewSize     int64  `json:"new_size"`
}

// applyEdit performs a single string-replace in `body` with the same
// rules code_edit_file enforces. Returns the new body and how many
// replacements were made. Errors are user-visible — they're what the
// agent sees if uniqueness fails.
func applyEdit(body, oldStr, newStr string, replaceAll bool) (string, int, error) {
	if oldStr == "" {
		return "", 0, errors.New("old_string required")
	}
	if oldStr == newStr {
		return body, 0, errors.New("old_string and new_string are identical — no change requested")
	}
	occurrences := strings.Count(body, oldStr)
	if occurrences == 0 {
		return "", 0, fmt.Errorf("old_string not found")
	}
	if !replaceAll && occurrences > 1 {
		// Surface the line numbers of the first few matches so the
		// agent can disambiguate by adding context. Mirrors Claude
		// Code's behaviour.
		matches := findMatchLines(body, oldStr, 5)
		return "", 0, fmt.Errorf(
			"old_string is not unique (%d matches at lines %s); add context or pass replace_all=true",
			occurrences, joinInts(matches),
		)
	}
	if replaceAll {
		return strings.ReplaceAll(body, oldStr, newStr), occurrences, nil
	}
	return strings.Replace(body, oldStr, newStr, 1), 1, nil
}

func findMatchLines(body, target string, max int) []int {
	out := []int{}
	idx := 0
	line := 1
	cursor := 0
	for cursor < len(body) && len(out) < max {
		off := strings.Index(body[cursor:], target)
		if off < 0 {
			break
		}
		// Count newlines between idx and (cursor+off) to derive the
		// line where the match starts.
		for i := idx; i < cursor+off; i++ {
			if body[i] == '\n' {
				line++
			}
		}
		out = append(out, line)
		idx = cursor + off
		cursor = idx + len(target)
	}
	return out
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ", ")
}

// editFile reads a file, applies one Edit, writes back atomically.
// The store's Write is atomic so partial writes never linger.
func editFile(store FileStore, slug, p, oldStr, newStr string, replaceAll bool) (*EditResult, error) {
	body, oldSHA, err := readWithSHA(store, slug, p)
	if err != nil {
		return nil, err
	}
	updated, n, err := applyEdit(string(body), oldStr, newStr, replaceAll)
	if err != nil {
		return nil, err
	}
	meta, err := store.Write(slug, p, []byte(updated))
	if err != nil {
		return nil, err
	}
	return &EditResult{
		Path:         p,
		Replacements: n,
		OldSHA256:    oldSHA,
		NewSHA256:    meta.SHA256,
		NewSize:      meta.Size,
	}, nil
}

// ─── MultiEdit (atomic, sequential) ─────────────────────────────────

// EditOp is one edit inside a code_multi_edit call.
type EditOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// MultiEditResult summarises the batch.
type MultiEditResult struct {
	Path           string `json:"path"`
	OperationCount int    `json:"operation_count"`
	Replacements   int    `json:"replacements"`
	OldSHA256      string `json:"old_sha256"`
	NewSHA256      string `json:"new_sha256"`
	NewSize        int64  `json:"new_size"`
}

// multiEditFile applies edits in order, each on the state after the
// previous one. Atomic: if any step fails uniqueness, nothing is
// written. The error includes the failing op index so the agent can
// debug without re-running each edit one-by-one.
func multiEditFile(store FileStore, slug, p string, ops []EditOp) (*MultiEditResult, error) {
	if len(ops) == 0 {
		return nil, errors.New("edits required (non-empty array)")
	}
	body, oldSHA, err := readWithSHA(store, slug, p)
	if err != nil {
		return nil, err
	}
	current := string(body)
	totalRepl := 0
	for i, op := range ops {
		next, n, err := applyEdit(current, op.OldString, op.NewString, op.ReplaceAll)
		if err != nil {
			return nil, fmt.Errorf("edit #%d: %w", i+1, err)
		}
		current = next
		totalRepl += n
	}
	meta, err := store.Write(slug, p, []byte(current))
	if err != nil {
		return nil, err
	}
	return &MultiEditResult{
		Path:           p,
		OperationCount: len(ops),
		Replacements:   totalRepl,
		OldSHA256:      oldSHA,
		NewSHA256:      meta.SHA256,
		NewSize:        meta.Size,
	}, nil
}

// ─── Glob ───────────────────────────────────────────────────────────

// globRepo matches paths in a repo against a doublestar glob (e.g.
// "**/*.tsx", "app/**/*.ts"). Uses the doublestar library so "**"
// crosses directory boundaries the way agents expect.
func globRepo(store FileStore, slug, pattern string) ([]string, error) {
	if pattern == "" {
		return nil, errors.New("pattern required")
	}
	files, err := store.List(slug, "", true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		ok, err := doublestar.PathMatch(pattern, f.Path)
		if err != nil {
			return nil, fmt.Errorf("invalid glob: %w", err)
		}
		if ok {
			out = append(out, f.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ─── Grep ───────────────────────────────────────────────────────────

// GrepMatch is one hit returned by code_grep.
type GrepMatch struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Col    int      `json:"col"`
	Match  string   `json:"match"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// GrepOptions mirrors what callers can configure.
type GrepOptions struct {
	Pattern     string
	Path        string // sub-tree prefix
	FilePattern string // glob; empty = all files
	Regex       bool   // false = literal, true = regex
	Context     int    // lines of before/after context
	IgnoreCase  bool
	Limit       int // max matches
}

const grepDefaultLimit = 500

// grepRepo scans every textual file in a repo for matches. Cheap
// enough for repos up to a few thousand files; v0.2 adds a content
// cache + FTS5 for larger trees.
func grepRepo(store FileStore, slug string, o GrepOptions) ([]GrepMatch, error) {
	if o.Pattern == "" {
		return nil, errors.New("pattern required")
	}
	if o.Limit <= 0 || o.Limit > 5000 {
		o.Limit = grepDefaultLimit
	}
	files, err := store.List(slug, o.Path, true)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if o.Regex {
		expr := o.Pattern
		if o.IgnoreCase {
			expr = "(?i)" + expr
		}
		re, err = regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
	}
	needle := o.Pattern
	if !o.Regex && o.IgnoreCase {
		needle = strings.ToLower(needle)
	}

	out := []GrepMatch{}
	for _, f := range files {
		if len(out) >= o.Limit {
			break
		}
		if o.FilePattern != "" {
			ok, err := doublestar.PathMatch(o.FilePattern, f.Path)
			if err != nil || !ok {
				continue
			}
		}
		body, err := store.Read(slug, f.Path)
		if err != nil {
			continue
		}
		if !isLikelyText(body) {
			continue
		}
		lines := strings.Split(string(body), "\n")
		// Drop trailing empty from final \n so line counts match
		// readWithLineNumbers' convention.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		for i, ln := range lines {
			var matched bool
			var col int
			if re != nil {
				loc := re.FindStringIndex(ln)
				if loc != nil {
					matched = true
					col = loc[0] + 1
				}
			} else {
				probe := ln
				if o.IgnoreCase {
					probe = strings.ToLower(probe)
				}
				idx := strings.Index(probe, needle)
				if idx >= 0 {
					matched = true
					col = idx + 1
				}
			}
			if !matched {
				continue
			}
			match := GrepMatch{
				Path:  f.Path,
				Line:  i + 1,
				Col:   col,
				Match: ln,
			}
			if o.Context > 0 {
				lo := i - o.Context
				if lo < 0 {
					lo = 0
				}
				hi := i + o.Context + 1
				if hi > len(lines) {
					hi = len(lines)
				}
				match.Before = append([]string{}, lines[lo:i]...)
				match.After = append([]string{}, lines[i+1:hi]...)
			}
			out = append(out, match)
			if len(out) >= o.Limit {
				break
			}
		}
	}
	return out, nil
}
