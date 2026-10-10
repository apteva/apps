package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
)

// Appcircle exports a ZIP of all provider outputs. Select our single sealed
// archive without importing provider logs or other files into the artifact tree.
func extractProviderArtifactArchive(file, name string) (string, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return "", err
	}
	defer z.Close()
	var selected *zip.File
	for _, entry := range z.File {
		if path.Base(entry.Name) != name {
			continue
		}
		if entry.Mode()&os.ModeType != 0 || entry.UncompressedSize64 > uint64(maxCloudArtifactBytes) {
			return "", errors.New("invalid provider artifact archive entry")
		}
		if selected != nil {
			return "", errors.New("provider returned multiple matching artifact archives")
		}
		selected = entry
	}
	if selected == nil {
		return "", fmt.Errorf("provider output contains no %s", name)
	}
	in, err := selected.Open()
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(file), "provider-artifact-*")
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, maxCloudArtifactBytes+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || n > maxCloudArtifactBytes {
		_ = os.Remove(out.Name())
		return "", errors.New("provider artifact archive extraction failed")
	}
	return out.Name(), nil
}
