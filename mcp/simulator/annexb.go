package main

// Minimal H.264 Annex-B framer. Both stream sources (android
// screenrecord, ios idb video-stream) emit a raw Annex-B elementary
// stream: a sequence of NAL units each prefixed by a 00 00 01 or
// 00 00 00 01 start code. WebCodecs' VideoDecoder wants one
// EncodedVideoChunk per access unit (≈ one frame, possibly preceded by
// SPS/PPS), so we reassemble access units server-side and send one WS
// message per AU.
//
// AU boundary rule (simplified but correct for these encoders): a new
// access unit begins at the first VCL NAL (types 1–5) that follows a
// previous VCL NAL. Parameter-set NALs (SPS=7, PPS=8) and AUD (9) that
// precede a VCL NAL are attached to the upcoming AU so each keyframe
// AU carries its own SPS/PPS — letting a late-joining decoder
// configure from in-band parameter sets.

import "bufio"

const (
	nalTypeNonIDR = 1
	nalTypeIDR    = 5
	nalTypeSPS    = 7
	nalTypePPS    = 8
	nalTypeAUD    = 9
)

// annexBFramer accumulates NAL units and flushes complete access units
// to a sink. Not safe for concurrent use; one framer per stream.
type annexBFramer struct {
	buf         []byte // bytes of the current access unit (with start codes)
	sawVCL      bool   // a VCL NAL has been added to the current AU
	emit        func(au []byte, keyframe bool)
	auHasIDR    bool
}

func newAnnexBFramer(emit func(au []byte, keyframe bool)) *annexBFramer {
	return &annexBFramer{emit: emit}
}

// feed reads an Annex-B byte stream from r and drives the framer until
// EOF or the first read error. Returns that error (io.EOF on a clean
// end). Uses a SplitFunc that yields one NAL (including its leading
// start code) per token.
func (f *annexBFramer) feed(r *bufio.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20) // up to 8 MiB per NAL
	sc.Split(splitNAL)
	for sc.Scan() {
		nal := sc.Bytes()
		f.addNAL(append([]byte(nil), nal...))
	}
	if err := sc.Err(); err != nil {
		return err
	}
	f.flush()
	return nil
}

// addNAL appends one NAL (with its start code) to the current AU,
// flushing the previous AU first when this NAL opens a new one.
func (f *annexBFramer) addNAL(nal []byte) {
	t := nalType(nal)
	isVCL := t == nalTypeNonIDR || t == nalTypeIDR

	// A VCL NAL after we've already seen a VCL NAL in this AU starts a
	// new AU. Parameter sets / AUD that arrive after a VCL also start
	// the next AU (they belong to the upcoming frame).
	if f.sawVCL && (isVCL || t == nalTypeSPS || t == nalTypePPS || t == nalTypeAUD) {
		f.flush()
	}
	f.buf = append(f.buf, nal...)
	if isVCL {
		f.sawVCL = true
	}
	if t == nalTypeIDR {
		f.auHasIDR = true
	}
}

func (f *annexBFramer) flush() {
	if len(f.buf) == 0 {
		return
	}
	f.emit(f.buf, f.auHasIDR)
	f.buf = nil
	f.sawVCL = false
	f.auHasIDR = false
}

// nalType returns the NAL unit type given a NAL slice that begins with
// a start code. Returns -1 when the slice is too short to classify.
func nalType(nal []byte) int {
	i := 0
	// skip start code
	if len(nal) >= 4 && nal[0] == 0 && nal[1] == 0 && nal[2] == 0 && nal[3] == 1 {
		i = 4
	} else if len(nal) >= 3 && nal[0] == 0 && nal[1] == 0 && nal[2] == 1 {
		i = 3
	}
	if i >= len(nal) {
		return -1
	}
	return int(nal[i] & 0x1f)
}

// splitNAL is a bufio.SplitFunc that yields one NAL unit per token,
// each including its leading start code. It scans for the NEXT start
// code to delimit the current NAL.
func splitNAL(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	// Find the first start code (start of token).
	start := indexStartCode(data, 0)
	if start < 0 {
		if atEOF {
			// No start code at all — discard.
			return len(data), nil, nil
		}
		return 0, nil, nil // need more data
	}
	// Find the next start code after this one (end of token).
	next := indexStartCode(data, start+3)
	if next < 0 {
		if atEOF {
			return len(data), data[start:], nil
		}
		return start, nil, nil // advance to start, keep buffering
	}
	return next, data[start:next], nil
}

// indexStartCode returns the index of the next Annex-B start code
// (00 00 01 or 00 00 00 01) in data at or after from, or -1.
func indexStartCode(data []byte, from int) int {
	for i := from; i+3 <= len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				return i
			}
			if i+4 <= len(data) && data[i+2] == 0 && data[i+3] == 1 {
				return i
			}
		}
	}
	return -1
}
