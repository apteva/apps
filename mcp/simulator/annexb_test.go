package main

import (
	"bufio"
	"bytes"
	"testing"
)

// startCode4 prefixes a NAL body with a 4-byte Annex-B start code.
func nal(nalType byte, body ...byte) []byte {
	out := []byte{0, 0, 0, 1, nalType & 0x1f}
	return append(out, body...)
}

// The framer must group SPS+PPS+IDR into one access unit and emit a
// non-IDR slice as its own AU, marking keyframe correctly.
func TestAnnexBFramer_AccessUnits(t *testing.T) {
	var stream []byte
	stream = append(stream, nal(nalTypeSPS, 0x01, 0x02)...)
	stream = append(stream, nal(nalTypePPS, 0x03)...)
	stream = append(stream, nal(nalTypeIDR, 0xAA, 0xBB)...)   // AU 1 (keyframe)
	stream = append(stream, nal(nalTypeNonIDR, 0xCC)...)      // AU 2 (delta)
	stream = append(stream, nal(nalTypeNonIDR, 0xDD)...)      // AU 3 (delta)

	type au struct {
		key  bool
		size int
	}
	var got []au
	f := newAnnexBFramer(func(b []byte, key bool) {
		got = append(got, au{key: key, size: len(b)})
	})
	if err := f.feed(bufio.NewReader(bytes.NewReader(stream))); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d access units, want 3: %+v", len(got), got)
	}
	if !got[0].key {
		t.Error("AU 1 should be a keyframe (carries SPS/PPS/IDR)")
	}
	if got[1].key || got[2].key {
		t.Error("AU 2/3 should be delta frames")
	}
	// AU1 must include all three NALs (SPS+PPS+IDR) — i.e. larger than
	// a lone IDR would be.
	if got[0].size <= got[1].size {
		t.Errorf("keyframe AU (%d) should be larger than a delta AU (%d)", got[0].size, got[1].size)
	}
}

func TestNalType(t *testing.T) {
	if nalType(nal(nalTypeIDR)) != nalTypeIDR {
		t.Error("4-byte start code IDR misclassified")
	}
	// 3-byte start code variant.
	if nalType([]byte{0, 0, 1, 0x67}) != nalTypeSPS {
		t.Error("3-byte start code SPS misclassified")
	}
	// Start code with no header byte after it → -1.
	if nalType([]byte{0, 0, 0, 1}) != -1 {
		t.Error("start code with no NAL header should return -1")
	}
}
