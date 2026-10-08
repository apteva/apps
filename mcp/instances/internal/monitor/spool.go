package monitor

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"sync"
)

// Reusing the fast compressor avoids allocating its tables for every checkpoint
// member. JSON stays compatible with existing collectors and app stores.
var fastCompressors = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return &fastCompressor{writer: w, buffered: bufio.NewWriterSize(w, 64<<10)}
}}

type fastCompressor struct {
	writer   *gzip.Writer
	buffered *bufio.Writer
}

func compressFast(out io.Writer, encode func(io.Writer) error) error {
	c := fastCompressors.Get().(*fastCompressor)
	c.writer.Reset(out)
	c.buffered.Reset(c.writer)
	defer func() { c.writer.Reset(io.Discard); c.buffered.Reset(io.Discard); fastCompressors.Put(c) }()
	// Small JSON entries otherwise force many tiny compressor/file writes.
	if err := encode(c.buffered); err != nil {
		return err
	}
	if err := c.buffered.Flush(); err != nil {
		return err
	}
	return c.writer.Close()
}

type spoolLimitWriter struct {
	out       io.Writer
	remaining int64
}

func (w *spoolLimitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errors.New("collector spool exceeds 32MiB limit")
	}
	n, err := w.out.Write(data)
	w.remaining -= int64(n)
	return n, err
}
