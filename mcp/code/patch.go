package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PatchFileResult struct {
	Path      string `json:"path"`
	Relocated []int  `json:"relocated_hunks,omitempty"`
	Hunks     int    `json:"hunks"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	Created   bool   `json:"created,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
}

type PatchResult struct {
	DryRun          bool                `json:"dry_run"`
	Applied         bool                `json:"applied"`
	PatchID         string              `json:"patch_id,omitempty"`
	ChangedFiles    []PatchFileResult   `json:"changed_files"`
	RejectedHunks   []string            `json:"rejected_hunks,omitempty"`
	RejectedContext []PatchRejectDetail `json:"rejected_context,omitempty"`
	Hint            string              `json:"hint,omitempty"`
}

type PatchRejectDetail struct {
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	StartLine int    `json:"start_line,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

type patchFile struct {
	oldPath string
	newPath string
	hunks   []patchHunk
}

type patchHunk struct {
	oldStart   int
	oldCount   int
	newStart   int
	newCount   int
	lines      []string
	oldNoNL    bool
	newNoNL    bool
	allowFuzzy bool
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

const patchFormatHint = "expected unified diff format: --- a/path, +++ b/path, then @@ -old,count +new,count @@ hunks with lines prefixed by space, -, or +"

type patchPreview struct {
	Slug      string
	Patch     string
	CreatedAt time.Time
	Expected  map[string]string
	Fuzzy     bool
}

var patchPreviewStore = struct {
	sync.Mutex
	items map[string]patchPreview
}{items: map[string]patchPreview{}}

func applyUnifiedPatch(store FileStore, slug, patch string, dryRun bool) (*PatchResult, error) {
	return applyUnifiedPatchOptions(store, slug, patch, dryRun, false)
}
func applyUnifiedPatchOptions(store FileStore, slug, patch string, dryRun, fuzzy bool) (*PatchResult, error) {
	return withRepoWrite(store, slug, func(raw FileStore) (*PatchResult, error) {
		return applyUnifiedPatchUnlocked(raw, slug, patch, dryRun, fuzzy)
	})
}
func applyUnifiedPatchUnlocked(store FileStore, slug, patch string, dryRun, fuzzy bool) (*PatchResult, error) {
	if int64(len(patch)) > maxFileBytes()*2 {
		return nil, errors.New("patch exceeds configured size limit")
	}
	files, err := parseUnifiedPatch(patch)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("patch contains no file hunks; %s", patchFormatHint)
	}
	result := &PatchResult{DryRun: dryRun, Applied: !dryRun}
	type pendingWrite struct {
		path    string
		body    []byte
		deleted bool
	}
	pending := []pendingWrite{}
	seen := map[string]bool{}
	for _, pf := range files {
		path := pf.newPath
		if path == "/dev/null" {
			path = pf.oldPath
		}
		if path == "" || path == "/dev/null" {
			return nil, errors.New("patch file path required")
		}
		clean, err := normalisePath(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(pf.hunks) == 0 {
			return nil, errors.New("file section has no hunks; binary or mode-only patches are unsupported")
		}
		if seen[clean] {
			return nil, fmt.Errorf("duplicate patch section for %s", clean)
		}
		seen[clean] = true
		if pf.oldPath != "/dev/null" && pf.newPath != "/dev/null" && pf.oldPath != pf.newPath {
			return nil, errors.New("patch renames are unsupported; use move")
		}
		for i := range pf.hunks {
			pf.hunks[i].allowFuzzy = fuzzy
		}
		oldBody := []byte{}
		oldSHA := ""
		created := pf.oldPath == "/dev/null"
		deleted := pf.newPath == "/dev/null"
		if created {
			if occupied, e := sourcePathExists(store, slug, clean); occupied {
				return nil, fmt.Errorf("%w: %s exists", errRevisionConflict, clean)
			} else if e != nil {
				return nil, e
			}
		}
		if !created {
			body, sha, err := readWithSHA(store, slug, clean)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", clean, err)
			}
			oldBody = body
			oldSHA = sha
		}
		relocated := []int{}
		nextBody, err := applyFilePatchReport(oldBody, pf.hunks, &relocated)
		if err != nil {
			result.Applied = false
			result.RejectedHunks = append(result.RejectedHunks, fmt.Sprintf("%s: %v", clean, err))
			result.RejectedContext = append(result.RejectedContext, patchRejectDetail(clean, oldBody, err))
			result.Hint = "patch was not applied; use rejected_context to rebuild the hunk with current file context"
			return result, nil
		}
		newSHA := ""
		newSize := int64(len(nextBody))
		if !deleted {
			sum := sha256.Sum256(nextBody)
			newSHA = hex.EncodeToString(sum[:])
		}
		result.ChangedFiles = append(result.ChangedFiles, PatchFileResult{
			Path:      clean,
			Relocated: relocated,
			Hunks:     len(pf.hunks),
			OldSHA256: oldSHA,
			NewSHA256: newSHA,
			NewSize:   newSize,
			Created:   created,
			Deleted:   deleted,
		})
		pending = append(pending, pendingWrite{path: clean, body: nextBody, deleted: deleted})
	}
	if dryRun {
		id := patchPreviewID(slug, patch+fmt.Sprintf("%d", time.Now().UnixNano()))
		preview := patchPreview{Slug: slug, Patch: patch, CreatedAt: time.Now(), Expected: map[string]string{}, Fuzzy: fuzzy}
		for _, f := range result.ChangedFiles {
			preview.Expected[f.Path] = f.OldSHA256
		}
		storePatchPreview(id, preview)
		result.PatchID = id
		result.Hint = "dry run succeeded; call code_apply_patch again with the same slug and patch_id to apply this exact patch"
		return result, nil
	}
	changes := make([]fileMutation, 0, len(pending))
	for _, p := range pending {
		changes = append(changes, fileMutation{Path: p.path, Body: p.body, Delete: p.deleted})
	}
	if err := applyFileMutations(store, slug, changes); err != nil {
		return nil, err
	}
	return result, nil
}

func patchPreviewID(slug, patch string) string {
	sum := sha256.Sum256([]byte(slug + "\x00" + patch))
	return hex.EncodeToString(sum[:])[:24]
}

func storePatchPreview(id string, preview patchPreview) {
	patchPreviewStore.Lock()
	defer patchPreviewStore.Unlock()
	now := time.Now()
	for k, v := range patchPreviewStore.items {
		if now.Sub(v.CreatedAt) > 30*time.Minute {
			delete(patchPreviewStore.items, k)
		}
	}
	totalBytes := len(preview.Patch)
	for _, item := range patchPreviewStore.items {
		totalBytes += len(item.Patch)
	}
	for len(patchPreviewStore.items) >= 128 || totalBytes > 32<<20 {
		var oldest string
		var at time.Time
		for k, v := range patchPreviewStore.items {
			if oldest == "" || v.CreatedAt.Before(at) {
				oldest = k
				at = v.CreatedAt
			}
		}
		if oldest == "" {
			break
		}
		totalBytes -= len(patchPreviewStore.items[oldest].Patch)
		delete(patchPreviewStore.items, oldest)
	}
	patchPreviewStore.items[id] = preview
}

func loadPatchPreview(id, slug string) (string, error) {
	patchPreviewStore.Lock()
	defer patchPreviewStore.Unlock()
	v, ok := patchPreviewStore.items[id]
	if !ok {
		return "", fmt.Errorf("patch_id %q not found or expired; pass patch again", id)
	}
	if v.Slug != slug {
		return "", fmt.Errorf("patch_id %q belongs to a different repository", id)
	}
	if time.Since(v.CreatedAt) > 30*time.Minute {
		delete(patchPreviewStore.items, id)
		return "", fmt.Errorf("patch_id %q expired; pass patch again", id)
	}
	return v.Patch, nil
}

type patchApplyError struct {
	Hunk    int
	OldLine int
	Reason  string
}

func (e patchApplyError) Error() string {
	if e.OldLine > 0 {
		return fmt.Sprintf("hunk #%d %s at old line %d", e.Hunk, e.Reason, e.OldLine)
	}
	return fmt.Sprintf("hunk #%d %s", e.Hunk, e.Reason)
}

func patchRejectDetail(path string, oldBody []byte, err error) PatchRejectDetail {
	d := PatchRejectDetail{Path: path, Reason: err.Error()}
	var pe patchApplyError
	if errors.As(err, &pe) && pe.OldLine > 0 {
		d.StartLine = pe.OldLine
		d.Excerpt = patchContextExcerpt(oldBody, pe.OldLine, 3)
	}
	return d
}

func patchContextExcerpt(body []byte, around, radius int) string {
	lines := patchSplitVisibleLines(string(body))
	if around < 1 {
		around = 1
	}
	start := around - radius
	if start < 1 {
		start = 1
	}
	end := around + radius
	if end > len(lines) {
		end = len(lines)
	}
	width := numWidth(end)
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%*d\t%s\n", width, i, lines[i-1])
	}
	return b.String()
}

func parseUnifiedPatch(patch string) ([]patchFile, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	var files []patchFile
	var cur *patchFile
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") {
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("line %d: --- without +++", i+1)
			}
			files = append(files, patchFile{
				oldPath: patchPath(line[4:]),
				newPath: patchPath(lines[i+1][4:]),
			})
			cur = &files[len(files)-1]
			i++
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "@@ ") {
			continue
		}
		h, err := parseHunkHeader(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		oldN, newN := 0, 0
		for oldN < h.oldCount || newN < h.newCount {
			i++
			if i >= len(lines) || lines[i] == "" {
				return nil, fmt.Errorf("line %d: hunk shorter than declared counts", i+1)
			}
			row := lines[i]
			if row == `\ No newline at end of file` {
				return nil, fmt.Errorf("line %d: misplaced newline marker", i+1)
			}
			switch row[0] {
			case ' ':
				oldN++
				newN++
			case '-':
				oldN++
			case '+':
				newN++
			default:
				return nil, fmt.Errorf("line %d: invalid hunk prefix", i+1)
			}
			if oldN > h.oldCount || newN > h.newCount {
				return nil, fmt.Errorf("line %d: hunk exceeds declared counts", i+1)
			}
			h.lines = append(h.lines, row)
			if i+1 < len(lines) && lines[i+1] == `\ No newline at end of file` {
				if row[0] != '+' {
					h.oldNoNL = true
				}
				if row[0] != '-' {
					h.newNoNL = true
				}
				i++
			}
		}
		if i+1 < len(lines) && len(lines[i+1]) > 0 && (lines[i+1][0] == ' ' || lines[i+1][0] == '+' || lines[i+1][0] == '-') && !strings.HasPrefix(lines[i+1], "--- ") {
			return nil, fmt.Errorf("line %d: hunk exceeds declared counts", i+2)
		}
		cur.hunks = append(cur.hunks, h)
	}
	return files, nil
}

func parseHunkHeader(line string) (patchHunk, error) {
	m := hunkHeaderRe.FindStringSubmatch(line)
	if m == nil {
		return patchHunk{}, fmt.Errorf("invalid hunk header %q", line)
	}
	oldStart, err := strconv.Atoi(m[1])
	if err != nil {
		return patchHunk{}, err
	}
	oldCount := 1
	if m[2] != "" {
		oldCount, err = strconv.Atoi(m[2])
		if err != nil {
			return patchHunk{}, err
		}
	}
	newStart, err := strconv.Atoi(m[3])
	if err != nil {
		return patchHunk{}, err
	}
	newCount := 1
	if m[4] != "" {
		newCount, err = strconv.Atoi(m[4])
		if err != nil {
			return patchHunk{}, err
		}
	}
	if (oldStart == 0 && oldCount > 0) || (newStart == 0 && newCount > 0) {
		return patchHunk{}, errors.New("invalid zero-based nonempty range")
	}
	return patchHunk{oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount}, nil
}

func patchPath(raw string) string {
	path := strings.SplitN(raw, "\t", 2)[0]
	if strings.HasPrefix(path, `"`) {
		decoded, err := strconv.Unquote(path)
		if err != nil {
			return ""
		}
		path = decoded
	}
	if path == "/dev/null" {
		return path
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

func applyFilePatch(body []byte, hunks []patchHunk) ([]byte, error) {
	return applyFilePatchReport(body, hunks, nil)
}
func applyFilePatchReport(body []byte, hunks []patchHunk, relocated *[]int) ([]byte, error) {
	lines, trailingNL := patchSplitLines(string(body))
	out := make([]string, 0, len(lines))
	cursor := 0
	for idx, h := range hunks {
		start := h.oldStart - 1
		if h.oldCount == 0 {
			start = h.oldStart
		}
		if start < cursor || start > len(lines) {
			return nil, patchApplyError{Hunk: idx + 1, OldLine: start + 1, Reason: "starts outside file"}
		}
		wantNew := len(out) + start - cursor
		if h.newCount > 0 {
			wantNew++
		}
		if !h.allowFuzzy && h.newStart != wantNew {
			return nil, patchApplyError{Hunk: idx + 1, Reason: "inconsistent new range position"}
		}
		applied, err := applyHunk(lines, cursor, start, h)
		if err != nil {
			return nil, withHunkNumber(err, idx+1)
		}
		if relocated != nil && (start != applied.start || applied.drifts > 0) {
			*relocated = append(*relocated, idx+1)
		}
		start = applied.start
		out = append(out, lines[cursor:start]...)
		out = append(out, applied.lines...)
		if h.oldNoNL && (applied.next != len(lines) || trailingNL) {
			return nil, fmt.Errorf("invalid old newline marker")
		}
		if applied.next == len(lines) {
			trailingNL = !h.newNoNL
		}
		if h.newNoNL && applied.next != len(lines) {
			return nil, fmt.Errorf("new newline marker must be at EOF")
		}
		cursor = applied.next
	}
	out = append(out, lines[cursor:]...)
	joined := strings.Join(out, "\n")
	if trailingNL && len(out) > 0 {
		joined += "\n"
	}
	return []byte(joined), nil
}

type hunkApplyResult struct {
	start  int
	next   int
	lines  []string
	drifts int
}

func applyHunk(lines []string, cursor, nominalStart int, h patchHunk) (hunkApplyResult, error) {
	res, firstErr := applyHunkAt(lines, nominalStart, h, false)
	if firstErr == nil {
		return res, nil
	}
	if !h.allowFuzzy {
		return hunkApplyResult{}, firstErr
	}
	if res, ok := findStrictHunkLocation(lines, cursor, nominalStart, h); ok {
		return res, nil
	}
	if !hunkHasRemoval(h) {
		return hunkApplyResult{}, firstErr
	}
	if res, err := applyHunkAt(lines, nominalStart, h, true); err == nil && res.drifts <= maxContextDrifts(h) {
		return res, nil
	}
	if res, ok := findContextDriftHunkLocation(lines, cursor, nominalStart, h); ok {
		return res, nil
	}
	return hunkApplyResult{}, firstErr
}

func applyHunkAt(lines []string, start int, h patchHunk, allowContextDrift bool) (hunkApplyResult, error) {
	if start < 0 || start > len(lines) {
		return hunkApplyResult{}, patchApplyError{OldLine: start + 1, Reason: "starts outside file"}
	}
	out := []string{}
	pos := start
	drifts := 0
	for _, pline := range h.lines {
		prefix := pline[0]
		text := pline[1:]
		switch prefix {
		case ' ':
			if pos >= len(lines) {
				return hunkApplyResult{}, patchApplyError{OldLine: pos + 1, Reason: "context mismatch"}
			}
			if lines[pos] != text {
				if !allowContextDrift {
					return hunkApplyResult{}, patchApplyError{OldLine: pos + 1, Reason: "context mismatch"}
				}
				drifts++
				out = append(out, lines[pos])
			} else {
				out = append(out, text)
			}
			pos++
		case '-':
			if pos >= len(lines) || lines[pos] != text {
				return hunkApplyResult{}, patchApplyError{OldLine: pos + 1, Reason: "removal mismatch"}
			}
			pos++
		case '+':
			out = append(out, text)
		}
	}
	return hunkApplyResult{start: start, next: pos, lines: out, drifts: drifts}, nil
}

func findStrictHunkLocation(lines []string, cursor, nominalStart int, h patchHunk) (hunkApplyResult, bool) {
	var found hunkApplyResult
	matches := 0
	for start := cursor; start <= len(lines); start++ {
		if start == nominalStart {
			continue
		}
		res, err := applyHunkAt(lines, start, h, false)
		if err != nil {
			continue
		}
		found = res
		matches++
		if matches > 1 {
			return hunkApplyResult{}, false
		}
	}
	return found, matches == 1
}

func findContextDriftHunkLocation(lines []string, cursor, nominalStart int, h patchHunk) (hunkApplyResult, bool) {
	firstRemoval, oldOffset, ok := firstRemovalAnchor(h)
	if !ok {
		return hunkApplyResult{}, false
	}
	var best hunkApplyResult
	bestDistance := 0
	matches := 0
	for pos := cursor; pos < len(lines); pos++ {
		if lines[pos] != firstRemoval {
			continue
		}
		start := pos - oldOffset
		if start < cursor || start < 0 {
			continue
		}
		res, err := applyHunkAt(lines, start, h, true)
		if err != nil || res.drifts > maxContextDrifts(h) {
			continue
		}
		distance := start - nominalStart
		if distance < 0 {
			distance = -distance
		}
		if matches == 0 || res.drifts < best.drifts || (res.drifts == best.drifts && distance < bestDistance) {
			best = res
			bestDistance = distance
			matches = 1
			continue
		}
		if res.drifts == best.drifts && distance == bestDistance {
			matches++
		}
	}
	return best, matches == 1
}

func firstRemovalAnchor(h patchHunk) (string, int, bool) {
	oldOffset := 0
	for _, pline := range h.lines {
		switch pline[0] {
		case ' ':
			oldOffset++
		case '-':
			return pline[1:], oldOffset, true
		}
	}
	return "", 0, false
}

func hunkHasRemoval(h patchHunk) bool {
	for _, line := range h.lines {
		if len(line) > 0 && line[0] == '-' {
			return true
		}
	}
	return false
}

func maxContextDrifts(h patchHunk) int {
	context := 0
	for _, line := range h.lines {
		if len(line) > 0 && line[0] == ' ' {
			context++
		}
	}
	if context < 2 {
		return context
	}
	return 2
}

func withHunkNumber(err error, n int) error {
	var pe patchApplyError
	if errors.As(err, &pe) {
		pe.Hunk = n
		return pe
	}
	return fmt.Errorf("hunk #%d: %w", n, err)
}

func patchSplitVisibleLines(body string) []string {
	lines := strings.Split(body, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func patchSplitLines(body string) ([]string, bool) {
	if body == "" {
		return []string{}, false
	}
	trailingNL := strings.HasSuffix(body, "\n")
	lines := strings.Split(body, "\n")
	if trailingNL {
		lines = lines[:len(lines)-1]
	}
	return lines, trailingNL
}

func applyPatchPreview(store FileStore, slug, id string) (*PatchResult, error) {
	patchPreviewStore.Lock()
	preview, ok := patchPreviewStore.items[id]
	patchPreviewStore.Unlock()
	if !ok || preview.Slug != slug || time.Since(preview.CreatedAt) > 30*time.Minute {
		return nil, errors.New("patch preview missing, expired, or belongs to another repository")
	}
	patch := preview.Patch
	return withRepoWrite(store, slug, func(raw FileStore) (*PatchResult, error) {
		for path, expected := range preview.Expected {
			meta, err := raw.Stat(slug, path)
			if expected == "" {
				if occupied, e := sourcePathExists(raw, slug, path); e != nil || occupied {
					return nil, errRevisionConflict
				}
			} else if err != nil || meta.SHA256 != expected {
				return nil, errRevisionConflict
			}
		}
		return applyUnifiedPatchUnlocked(raw, slug, patch, false, preview.Fuzzy)
	})
}
