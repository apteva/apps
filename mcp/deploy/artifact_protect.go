package main

// Sealed-artifact protection (v0.32.1).
//
// A sealed artifact must not change after its digest is recorded: a stray
// file (for example a Python __pycache__ entry written by a script that
// imported a module from inside the artifact) used to break the digest and
// block Tests verdicts and release policies with an unexplained error.
//
//   - Artifacts that never run in place (static sites, mobile packages and
//     command-pipeline outputs without a start command) are made read-only
//     before the digest is computed, so nothing can add or change files.
//   - Service artifacts still run with their working directory inside the
//     artifact, so they stay writable; workloads get PYTHONDONTWRITEBYTECODE.
//   - Each seal records its file list next to the artifact, so a mismatch
//     reports which paths were added, removed or changed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const attestedFilesSuffix = ".attested-files.json"

// artifactRunsInPlace reports whether a release executes a process with its
// working directory inside the artifact (see LocalRuntime.Start).
func artifactRunsInPlace(framework, startCmd string) bool {
	switch framework {
	case "static", "android", "ios", "macos":
		return false
	case "command":
		return strings.TrimSpace(startCmd) != ""
	default:
		return true
	}
}

// sealBuildArtifactProtected seals a finished build. Artifacts that never run
// in place are made read-only first, so the recorded digest covers the final
// modes and later writes fail instead of silently changing the digest.
func sealBuildArtifactProtected(b *Build, startCmd string) error {
	return sealProtected(b, "", !artifactRunsInPlace(b.Framework, startCmd))
}

// sealProtected seals the build (v2 digest unless want is given), then makes
// the artifact read-only when protect is set. The v2 digest ignores write
// bits, so the chmod does not change the recorded identity.
func sealProtected(b *Build, want string, protect bool) error {
	if b == nil {
		return nil
	}
	if err := sealBuildArtifactAs(b, want); err != nil {
		return err
	}
	if protect {
		if err := makeTreeReadOnly(b.ArtifactPath); err != nil {
			return fmt.Errorf("protect artifact: %w", err)
		}
	}
	return recordAttestedFiles(b.ArtifactPath)
}

func makeTreeReadOnly(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Chmod(root, info.Mode().Perm()&^0o222)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, fi.Mode().Perm()&^0o222)
	})
}

// makeTreeWritable restores owner write access so a protected tree can be
// removed. Directories are fixed before their entries are visited.
func makeTreeWritable(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Chmod(root, info.Mode().Perm()|0o200)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return os.Chmod(path, fi.Mode().Perm()|0o700)
		}
		return nil
	})
}

// removeArtifactTree removes a build path that may have been sealed read-only.
func removeArtifactTree(p string) error {
	if _, err := os.Lstat(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_ = makeTreeWritable(p)
	return os.RemoveAll(p)
}

func attestedFilesPath(artifact string) string {
	return filepath.Clean(artifact) + attestedFilesSuffix
}

// artifactTreeEntries describes every entry the tree digest covers, keyed by
// slash path, with the same mode and content inputs as artifactTreeDigest.
func artifactTreeEntries(root string) (map[string]string, error) {
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || path == filepath.Join(root, artifactManifestFilename) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		value := info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += " -> " + target
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			value += " " + hex.EncodeToString(h.Sum(nil))
		}
		entries[filepath.ToSlash(rel)] = value
		return nil
	})
	return entries, err
}

func recordAttestedFiles(artifact string) error {
	info, err := os.Stat(artifact)
	if err != nil || !info.IsDir() {
		return err
	}
	entries, err := artifactTreeEntries(artifact)
	if err != nil {
		return err
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(attestedFilesPath(artifact), body, 0o444)
}

// describeArtifactDrift names the paths that differ from the sealed file list.
func describeArtifactDrift(artifact string) string {
	body, err := os.ReadFile(attestedFilesPath(artifact))
	if err != nil {
		return "the sealed file list was not recorded (build sealed before Deploy 0.32.1); rebuild to get per-file detail"
	}
	var sealed map[string]string
	if err := json.Unmarshal(body, &sealed); err != nil {
		return "the sealed file list is unreadable"
	}
	current, err := artifactTreeEntries(artifact)
	if err != nil {
		return "could not read the current artifact: " + err.Error()
	}
	var added, removed, changed []string
	for p, v := range current {
		if old, ok := sealed[p]; !ok {
			added = append(added, p)
		} else if old != v {
			changed = append(changed, p)
		}
	}
	for p := range sealed {
		if _, ok := current[p]; !ok {
			removed = append(removed, p)
		}
	}
	parts := []string{}
	for _, group := range []struct {
		label string
		paths []string
	}{{"added", added}, {"removed", removed}, {"changed", changed}} {
		if len(group.paths) == 0 {
			continue
		}
		sort.Strings(group.paths)
		shown := group.paths
		if len(shown) > 10 {
			shown = shown[:10]
		}
		text := group.label + ": " + strings.Join(shown, ", ")
		if extra := len(group.paths) - len(shown); extra > 0 {
			text += fmt.Sprintf(" (+%d more)", extra)
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "no file differences found against the sealed file list"
	}
	return strings.Join(parts, "; ")
}
