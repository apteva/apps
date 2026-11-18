package main

// Line-level diff and three-way merge for native version control (Code 0.16).
//
// Files are compared as lines that keep their "\n" terminator, so a final
// line without a newline is preserved exactly. diffMatches is Myers' O(ND)
// algorithm after trimming the common prefix and suffix; when the edit
// distance exceeds nativeDiffMaxEdits the middle is treated as replaced
// wholesale, which keeps pathological inputs bounded. merge3 is the classic
// diff3 chunking over those matches.

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	nativeDiffMaxEdits   = 4000
	nativeMergeMaxBytes  = 1 << 20  // files above this changed on both sides always conflict
	nativeInlineMaxBytes = 64 << 10 // bodies returned inline to callers
)

func splitLines(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	out := make([]string, 0, bytes.Count(body, []byte("\n"))+1)
	for len(body) > 0 {
		i := bytes.IndexByte(body, '\n')
		if i < 0 {
			out = append(out, string(body))
			break
		}
		out = append(out, string(body[:i+1]))
		body = body[i+1:]
	}
	return out
}

func isText(body []byte) bool {
	probe := body
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	return bytes.IndexByte(probe, 0) < 0 && utf8.Valid(body)
}

// diffMatches returns index pairs (i in a, j in b) of a longest common
// subsequence, strictly increasing in both coordinates.
func diffMatches(a, b []string) [][2]int {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	out := make([][2]int, 0, pre+suf)
	for i := 0; i < pre; i++ {
		out = append(out, [2]int{i, i})
	}
	for _, m := range myersMiddle(a[pre:len(a)-suf], b[pre:len(b)-suf]) {
		out = append(out, [2]int{m[0] + pre, m[1] + pre})
	}
	for k := suf; k > 0; k-- {
		out = append(out, [2]int{len(a) - k, len(b) - k})
	}
	return out
}

func myersMiddle(a, b []string) [][2]int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	// Intern lines so the inner loop compares ints.
	ids := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = len(ids)
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	ai, bi := intern(a), intern(b)
	max := n + m
	off := max + 1
	v := make([]int, 2*max+3)
	trace := [][]int{}
	found := -1
	for d := 0; d <= max && d <= nativeDiffMaxEdits; d++ {
		lo, hi := off-d-1, off+d+2
		if lo < 0 {
			lo = 0
		}
		if hi > len(v) {
			hi = len(v)
		}
		snap := make([]int, d*2+3)
		copy(snap[lo-(off-d-1):], v[lo:hi])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && ai[x] == bi[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		return nil // too different: treat the whole middle as replaced
	}
	at := func(d, k int) int { return trace[d][k+d+1] }
	x, y := n, m
	rev := [][2]int{}
	for d := found; d > 0; d-- {
		k := x - y
		var prevK int
		if k == -d || (k != d && at(d, k-1) < at(d, k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(d, prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, [2]int{x, y})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, [2]int{x, y})
	}
	out := make([][2]int, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

// ─── Unified diff ───────────────────────────────────────────────────────

type nativeLineStats struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// unifiedDiff renders a git-style unified diff with three lines of context.
func unifiedDiff(path string, a, b []string) (string, nativeLineStats) {
	matches := diffMatches(a, b)
	type op struct {
		kind byte // ' ', '-', '+'
		line string
		ai   int
		bi   int
	}
	ops := []op{}
	i, j := 0, 0
	emitUntil := func(ti, tj int) {
		for ; i < ti; i++ {
			ops = append(ops, op{'-', a[i], i, j})
		}
		for ; j < tj; j++ {
			ops = append(ops, op{'+', b[j], i, j})
		}
	}
	for _, m := range matches {
		emitUntil(m[0], m[1])
		ops = append(ops, op{' ', a[i], i, j})
		i++
		j++
	}
	emitUntil(len(a), len(b))
	stats := nativeLineStats{}
	for _, o := range ops {
		switch o.kind {
		case '-':
			stats.Removed++
		case '+':
			stats.Added++
		}
	}
	if stats.Added == 0 && stats.Removed == 0 {
		return "", stats
	}
	const ctx = 3
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	for start := 0; start < len(ops); {
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start >= len(ops) {
			break
		}
		lo := start - ctx
		if lo < 0 {
			lo = 0
		}
		end := start
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run >= len(ops) || run-end > 2*ctx {
				break
			}
			end = run
		}
		hi := end + ctx
		if hi > len(ops) {
			hi = len(ops)
		}
		aStart, bStart, aLen, bLen := ops[lo].ai, ops[lo].bi, 0, 0
		for _, o := range ops[lo:hi] {
			if o.kind != '+' {
				aLen++
			}
			if o.kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(aStart, aLen), hunkRange(bStart, bLen))
		for _, o := range ops[lo:hi] {
			out.WriteByte(o.kind)
			out.WriteString(o.line)
			if !strings.HasSuffix(o.line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		start = hi
	}
	return out.String(), stats
}

func hunkRange(start, length int) string {
	if length == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if length == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, length)
}

// ─── Three-way merge ────────────────────────────────────────────────────

type merge3Result struct {
	Lines     []string
	Conflicts int
}

type merge3Labels struct {
	Ours, Base, Theirs string
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// merge3 merges ours and theirs against base. Conflicting chunks are written
// with diff3-style markers; callers that must not leave markers check
// Conflicts.
func merge3(base, ours, theirs []string, labels merge3Labels) merge3Result {
	mo := map[int]int{}
	for _, m := range diffMatches(base, ours) {
		mo[m[0]] = m[1]
	}
	mt := map[int]int{}
	for _, m := range diffMatches(base, theirs) {
		mt[m[0]] = m[1]
	}
	res := merge3Result{}
	emitChunk := func(o, a, b []string) {
		switch {
		case equalLines(a, b):
			res.Lines = append(res.Lines, a...)
		case equalLines(o, a):
			res.Lines = append(res.Lines, b...)
		case equalLines(o, b):
			res.Lines = append(res.Lines, a...)
		default:
			res.Conflicts++
			marker := func(prefix, label string) string {
				if label == "" {
					return prefix + "\n"
				}
				return prefix + " " + label + "\n"
			}
			block := func(lines []string) {
				for _, l := range lines {
					res.Lines = append(res.Lines, l)
				}
				if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
					res.Lines[len(res.Lines)-1] += "\n"
				}
			}
			res.Lines = append(res.Lines, marker("<<<<<<<", labels.Ours))
			block(a)
			res.Lines = append(res.Lines, marker("|||||||", labels.Base))
			block(o)
			res.Lines = append(res.Lines, "=======\n")
			block(b)
			res.Lines = append(res.Lines, marker(">>>>>>>", labels.Theirs))
		}
	}
	i, ja, jb := 0, 0, 0
	for {
		k := 0
		for i+k < len(base) {
			x, okA := mo[i+k]
			y, okB := mt[i+k]
			if !okA || !okB || x != ja+k || y != jb+k {
				break
			}
			k++
		}
		if k > 0 {
			res.Lines = append(res.Lines, base[i:i+k]...)
			i, ja, jb = i+k, ja+k, jb+k
			continue
		}
		o := i
		for o < len(base) {
			_, okA := mo[o]
			_, okB := mt[o]
			if okA && okB {
				break
			}
			o++
		}
		if o >= len(base) {
			emitChunk(base[i:], ours[ja:], theirs[jb:])
			return res
		}
		emitChunk(base[i:o], ours[ja:mo[o]], theirs[jb:mt[o]])
		i, ja, jb = o, mo[o], mt[o]
	}
}
