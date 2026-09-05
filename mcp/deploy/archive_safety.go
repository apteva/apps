package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxArchiveExpandedBytes int64 = 2 << 30
const maxArchiveEntries = 100000

func extractBoundedZip(zr *zip.Reader, destination string, budget int64, allowLinks bool) error {
	if len(zr.File) > maxArchiveEntries {
		return errors.New("archive contains too many entries")
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	var links []struct{ name, target string }
	var created []string
	success := false
	defer func() {
		if !success {
			for i := len(created) - 1; i >= 0; i-- {
				_ = root.Remove(created[i])
			}
		}
	}()
	var total int64
	for _, entry := range zr.File {
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe archive entry %q", entry.Name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate archive entry %q", entry.Name)
		}
		seen[name] = true
		mode := entry.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported archive entry %q", entry.Name)
		}
		if mode.IsDir() {
			if err = root.MkdirAll(name, 0755); err != nil {
				return err
			}
			continue
		}
		if entry.UncompressedSize64 > uint64(budget-total) {
			return errors.New("archive exceeds expanded byte budget")
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			if !allowLinks {
				reader.Close()
				return errors.New("source archive symlinks are not supported")
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, 4097))
			reader.Close()
			if readErr != nil {
				return readErr
			}
			if len(body) > 4096 {
				return errors.New("archive symlink target too long")
			}
			target := string(body)
			resolved := filepath.Clean(filepath.Join(filepath.Dir(name), target))
			if target == "" || filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return errors.New("archive symlink escapes root")
			}
			total += int64(len(body))
			links = append(links, struct{ name, target string }{name, target})
			continue
		}
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			reader.Close()
			return err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()&0777)
		if err != nil {
			reader.Close()
			return err
		}
		created = append(created, name)
		n, copyErr := io.Copy(file, io.LimitReader(reader, budget-total+1))
		reader.Close()
		closeErr := file.Close()
		total += n
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if total > budget || uint64(n) != entry.UncompressedSize64 {
			return errors.New("archive entry size mismatch or expanded budget exceeded")
		}
	}
	// Links are installed after regular entries so they can never redirect writes.
	for _, link := range links {
		if err = root.MkdirAll(filepath.Dir(link.name), 0755); err != nil {
			return err
		}
		if err = root.Symlink(link.target, link.name); err != nil {
			return err
		}
		created = append(created, link.name)
	}
	for _, link := range links {
		if _, err = root.Stat(link.name); err != nil {
			return fmt.Errorf("invalid archive symlink %q: %w", link.name, err)
		}
	}
	success = true
	return nil
}
