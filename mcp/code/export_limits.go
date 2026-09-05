package main

import (
	"bytes"
	"errors"
)

var errInlineExportLimit = errors.New("inline export exceeds limit; use authenticated download_url")

type inlineExportBuffer struct {
	bytes.Buffer
	limit int
}

func (w *inlineExportBuffer) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, errInlineExportLimit
	}
	return w.Buffer.Write(p)
}
