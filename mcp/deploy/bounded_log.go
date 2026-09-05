package main

import (
	"io"
	"os"
	"sync"
)

const maxLogBytes int64 = 16 << 20

// Both stdout and stderr share this writer. Rotation retains one bounded prior
// segment and keeps the existing descriptor so subprocess ownership is stable.
type boundedLogWriter struct {
	mu   sync.Mutex
	file *os.File
	size int64
}

func newBoundedLogWriter(file *os.File) *boundedLogWriter {
	info, _ := file.Stat()
	w := &boundedLogWriter{file: file}
	if info != nil {
		w.size = info.Size()
	}
	return w
}
func (w *boundedLogWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(body)
	if int64(len(body)) > maxLogBytes {
		body = body[len(body)-int(maxLogBytes):]
	}
	if w.size+int64(len(body)) > maxLogBytes {
		previous, err := os.Open(w.file.Name())
		if err != nil {
			return 0, err
		}
		backup, err := os.OpenFile(w.file.Name()+".1", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			previous.Close()
			return 0, err
		}
		_, err = io.Copy(backup, io.LimitReader(previous, maxLogBytes))
		previous.Close()
		closeErr := backup.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if err = w.file.Truncate(0); err != nil {
			return 0, err
		}
		w.size = 0
	}
	n, err := w.file.Write(body)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	return original, nil
}

// Runtime children write directly to files so they survive supervisor upgrades.
// Use copy/truncate rotation rather than making them depend on a parent pipe.
func rotateRuntimeLog(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= maxLogBytes {
		return nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = newBoundedLogWriter(file).Write(nil)
	return err
}
