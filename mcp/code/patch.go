package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type PatchFileResult struct {
	Path      string `json:"path"`
	Hunks     int    `json:"hunks"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	Created   bool   `json:"created,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
}

type PatchResult struct {
	DryRun        bool              `json:"dry_run"`
	Applied       bool              `json:"applied"`
	ChangedFiles  []PatchFileResult `json:"changed_files"`
	RejectedHunks []string          `json:"rejected_hunks,omitempty"`
}

type patchFile struct {
	oldPath string
	newPath string
	hunks   []patchHunk
}

type patchHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []string
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func applyUnifiedPatch(store FileStore, slug, patch string, dryRun bool) (*PatchResult, error) {
	files, err := parseUnifiedPatch(patch)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("patch contains no file hunks")
	}
	result := &PatchResult{DryRun: dryRun, Applied: !dryRun}
	type pendingWrite struct {
		path    string
		body    []byte
		deleted bool
	}
	pending := []pendingWrite{}
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
		oldBody := []byte{}
		oldSHA := ""
		created := pf.oldPath == "/dev/null"
		deleted := pf.newPath == "/dev/null"
		if !created {
			body, sha, err := readWithSHA(store, slug, clean)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", clean, err)
			}
			oldBody = body
			oldSHA = sha
		}
		nextBody, err := applyFilePatch(oldBody, pf.hunks)
		if err != nil {
			result.Applied = false
			result.RejectedHunks = append(result.RejectedHunks, fmt.Sprintf("%s: %v", clean, err))
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
		return result, nil
	}
	for i, p := range pending {
		if p.deleted {
			if err := store.Delete(slug, p.path); err != nil {
				return nil, err
			}
			continue
		}
		meta, err := store.Write(slug, p.path, p.body)
		if err != nil {
			return nil, err
		}
		result.ChangedFiles[i].NewSHA256 = meta.SHA256
		result.ChangedFiles[i].NewSize = meta.Size
	}
	return result, nil
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
		i++
		for ; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "@@ ") || strings.HasPrefix(lines[i], "--- ") {
				i--
				break
			}
			if i == len(lines)-1 && lines[i] == "" {
				break
			}
			if strings.HasPrefix(lines[i], `\ No newline at end of file`) {
				continue
			}
			if lines[i] == "" {
				h.lines = append(h.lines, " ")
				continue
			}
			prefix := lines[i][0]
			if prefix != ' ' && prefix != '+' && prefix != '-' {
				return nil, fmt.Errorf("line %d: invalid hunk line prefix %q", i+1, prefix)
			}
			h.lines = append(h.lines, lines[i])
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
	oldStart, _ := strconv.Atoi(m[1])
	oldCount := 1
	if m[2] != "" {
		oldCount, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newCount := 1
	if m[4] != "" {
		newCount, _ = strconv.Atoi(m[4])
	}
	return patchHunk{oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount}, nil
}

func patchPath(raw string) string {
	p := strings.Fields(raw)
	if len(p) == 0 {
		return ""
	}
	path := strings.Trim(p[0], `"`)
	if path == "/dev/null" {
		return path
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

func applyFilePatch(body []byte, hunks []patchHunk) ([]byte, error) {
	lines, trailingNL := patchSplitLines(string(body))
	out := make([]string, 0, len(lines))
	cursor := 0
	for idx, h := range hunks {
		start := h.oldStart - 1
		if h.oldStart == 0 {
			start = 0
		}
		if start < cursor || start > len(lines) {
			return nil, fmt.Errorf("hunk #%d starts outside file", idx+1)
		}
		out = append(out, lines[cursor:start]...)
		pos := start
		for _, pline := range h.lines {
			prefix := pline[0]
			text := pline[1:]
			switch prefix {
			case ' ':
				if pos >= len(lines) || lines[pos] != text {
					return nil, fmt.Errorf("hunk #%d context mismatch at old line %d", idx+1, pos+1)
				}
				out = append(out, text)
				pos++
			case '-':
				if pos >= len(lines) || lines[pos] != text {
					return nil, fmt.Errorf("hunk #%d removal mismatch at old line %d", idx+1, pos+1)
				}
				pos++
			case '+':
				out = append(out, text)
			}
		}
		cursor = pos
	}
	out = append(out, lines[cursor:]...)
	joined := strings.Join(out, "\n")
	if trailingNL || len(out) > 0 {
		joined += "\n"
	}
	return []byte(joined), nil
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
