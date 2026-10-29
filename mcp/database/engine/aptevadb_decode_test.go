package engine

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	native "github.com/apteva/db"
)

func nativeCodecFixture(t testing.TB) (Collection, Record) {
	t.Helper()
	c, e := newCollection(Request{Collection: "items", Fields: []Field{{"text", "text", true}, {"n", "number", true}, {"i", "integer", true}, {"flag", "boolean", true}, {"date", "datetime", true}, {"payload", "json", true}}})
	if e != nil {
		t.Fatal(e)
	}
	r := Record{"id": "a", "text": "a\x00雪😀\"\\", "n": math.SmallestNonzeroFloat64, "i": "9223372036854775807", "flag": true, "date": "2026-10-13T00:00:00.000000Z", "payload": map[string]any{"exact": json.Number("9007199254740993")}, "_created_at": "2026-10-13T00:00:00.000000Z", "_updated_at": "2026-10-13T00:00:00.000000Z", "_version": "1"}
	return c, r
}

func TestNativeProjectedCodecAndOwnership(t *testing.T) {
	c, r := nativeCodecFixture(t)
	for _, wide := range []bool{false, true} {
		if wide {
			r["text"] = strings.Repeat("x", 80000)
			var deep any
			checkErr(t, decode([]byte(strings.Repeat("[", 400)+"9007199254740993"+strings.Repeat("]", 400)), &deep))
			r["payload"] = deep
		}
		data, e := encodeNativeRecord(c, r)
		checkErr(t, e)
		for _, fields := range [][]string{nil, {}, {"id", "i"}, {"payload", "text"}} {
			d := newNativeScanDecoder(c, fields, &Filter{Field: "n", Op: "gt", Value: 0.})
			got, e := d.decode(data)
			checkErr(t, e)
			want := Record{}
			for name := range d.row {
				want[name] = r[name]
			}
			if !equalJSON(got, want) {
				t.Fatalf("projection mismatch: got %s want %s", marshal(got), marshal(want))
			}
			retained := retainSelected(got, Query{})
			// Mutating the borrowed cursor bytes and reusing the scratch row may
			// not change strings, JSON numbers or records previously retained.
			owned := append([]byte(nil), data...)
			_, e = d.decode(owned)
			checkErr(t, e)
			for i := range owned {
				owned[i] = 0
			}
			for _, f := range c.Fields {
				r[f.Name] = nil
			}
			r["id"] = "next"
			next, e := encodeNativeRecord(c, r)
			checkErr(t, e)
			_, e = d.decode(next)
			checkErr(t, e)
			if !equalJSON(retained, want) {
				t.Fatal("retained values borrowed scratch/cursor bytes")
			}
			// Reset the source for each projection.
			full, e := decodeNativeRecord(c, data)
			checkErr(t, e)
			r = full
		}
	}
}

// The fast path silently fell back to the public decoder from alpha.6 through
// alpha.8 because its version guard was never widened. Fail loudly instead.
func TestNativeFlatDecoderCoversPinnedVersion(t *testing.T) {
	c, r := nativeCodecFixture(t)
	data, e := encodeNativeRecord(c, r)
	checkErr(t, e)
	if ok, e := newNativeScanDecoder(c, nil, nil).decodeFlat(data); !ok || e != nil {
		t.Fatalf("flat decoder disabled for AptevaDB %s (format %d): audit doc.go and extend nativeFlatCodecVersions", native.Version, native.FormatVersion)
	}
}

func TestNativeProjectionCompatibilityAndCorruption(t *testing.T) {
	c, e := newCollection(Request{Collection: "items", Fields: []Field{{"a", "text", false}, {"b", "text", false}, {"i", "integer", false}}})
	checkErr(t, e)
	r := Record{"id": "a", "a": strings.Repeat("x", 40000), "b": strings.Repeat("y", 40000), "i": "-9223372036854775808", "_created_at": "2026-10-13T00:00:00.000000Z", "_updated_at": "2026-10-13T00:00:00.000000Z", "_version": "1"}
	data, e := encodeNativeRecord(c, r)
	checkErr(t, e)
	n := len(allFields(c))
	start := len(data) - 9 - 4*n
	legacy := append([]byte(nil), data[:start]...)
	for i := 0; i < n; i++ {
		legacy = binary.LittleEndian.AppendUint16(legacy, uint16(binary.LittleEndian.Uint32(data[start+4*i:])))
	}
	legacy = append(legacy, byte(n))
	d := newNativeScanDecoder(c, []string{"a", "b", "i"}, nil)
	got, e := d.decode(legacy)
	checkErr(t, e)
	if got["a"] != r["a"] || got["b"] != r["b"] || got["i"] != r["i"] {
		t.Fatal("wrapped-offset compatibility")
	}
	// The public encoder uses a different field order. Fall back safely.
	stored := map[string]any{}
	for name, value := range r {
		stored[name] = value
	}
	stored["i"], _ = strconv.ParseInt(r["i"].(string), 10, 64)
	stored["_version"] = int64(1)
	foreign, e := native.Encode(stored)
	checkErr(t, e)
	got, e = d.decode(foreign)
	checkErr(t, e)
	if got["i"] != r["i"] || got["b"] != r["b"] {
		t.Fatal("alternate encoder compatibility")
	}
	for _, bad := range [][]byte{nil, {0}, data[:len(data)-7], append([]byte(nil), data...)} {
		if len(bad) == len(data) {
			binary.LittleEndian.PutUint32(bad[start:], math.MaxUint32)
		}
		if _, e := d.decode(bad); e == nil {
			t.Fatal("malformed document accepted")
		}
	}
}

func FuzzNativeScanDecoder(f *testing.F) {
	c, r := nativeCodecFixture(f)
	data, e := encodeNativeRecord(c, r)
	if e != nil {
		f.Fatal(e)
	}
	f.Add(data)
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2<<20 {
			t.Skip()
		}
		full, wantErr := decodeNativeRecord(c, data)
		d := newNativeScanDecoder(c, nil, nil)
		got, gotErr := d.decode(data)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("decoder error mismatch: %v vs %v", wantErr, gotErr)
		}
		if wantErr == nil {
			want := Record{}
			for name := range d.row {
				want[name] = full[name]
			}
			if !equalJSON(got, want) {
				t.Fatal("decoder differs from public library")
			}
		}
	})
}
