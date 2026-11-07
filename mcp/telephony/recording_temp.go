package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var recordingTemps struct {
	sync.Mutex
	root string
}

// The exclusive app lifecycle guarantees the previous process has stopped
// before mount. Clear only this app's transient directory, never recordings/.
func initializeRecordingTemporaryFiles(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	root := filepath.Join(dataDir, "recording-playback-tmp")
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	recordingTemps.Lock()
	recordingTemps.root = root
	recordingTemps.Unlock()
	return nil
}
func recordingTemporaryRoot() (string, error) {
	recordingTemps.Lock()
	defer recordingTemps.Unlock()
	root := recordingTemps.root
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("apteva-telephony-recording-tmp-%d", os.Getpid()))
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid recording temporary directory")
	}
	return root, nil
}
func newRecordingTemporaryFile(pattern string) (*os.File, error) {
	root, err := recordingTemporaryRoot()
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(root, pattern)
}
