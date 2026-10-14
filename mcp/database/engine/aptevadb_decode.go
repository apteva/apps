package engine

import (
	"encoding/binary"
	"math"
	"strconv"

	native "github.com/apteva/db"
)

// nativeScanDecoder reads the flat scalar codec emitted by encodeNativeRecord.
// It validates every entry but materializes only requested/filter fields into
// a query-local scratch map. Retained rows must be copied by the consumer.
// Unknown field order, codec layout or historical wrapped offsets use the
// library decoder, preserving compatibility when the pinned library changes.
// All returned strings/JSON own their bytes; none borrow the cursor buffer.
type nativeScanDecoder struct {
	c      Collection
	fields []Field
	wanted []bool
	row    Record
}

func newNativeScanDecoder(c Collection, names []string, where *Filter) *nativeScanDecoder {
	d := &nativeScanDecoder{c: c, fields: allFields(c), row: Record{}}
	wanted := map[string]bool{}
	for _, f := range names {
		wanted[f] = true
	}
	var visit func(*Filter)
	visit = func(f *Filter) {
		if f == nil {
			return
		}
		if f.Field != "" {
			wanted[f.Field] = true
		}
		for i := range f.And {
			visit(&f.And[i])
		}
		for i := range f.Or {
			visit(&f.Or[i])
		}
	}
	visit(where)
	for _, f := range d.fields {
		use := names == nil || wanted[f.Name]
		d.wanted = append(d.wanted, use)
		if use {
			d.row[f.Name] = nil
		}
	}
	return d
}

func (d *nativeScanDecoder) decode(data []byte) (Record, error) {
	if ok, e := d.decodeFlat(data); ok {
		return d.row, e
	}
	full, e := decodeNativeRecord(d.c, data)
	if e != nil {
		return nil, e
	}
	for i, f := range d.fields {
		if d.wanted[i] {
			d.row[f.Name] = full[f.Name]
		}
	}
	return d.row, nil
}

func (d *nativeScanDecoder) decodeFlat(data []byte) (bool, error) {
	// The storage library owns this binary representation. Re-audit this fast
	// path on a dependency upgrade; newer codecs safely use the public decoder.
	// documents.go is byte-identical between alpha.4 and alpha.5.
	if native.Version != "0.2.0-alpha.5" || native.FormatVersion != 10 {
		return false, nil
	}
	if len(data) == 0 || len(data) > native.MaxDocumentSize {
		return false, nil
	}
	count, trailer, width := int(data[len(data)-1]), 1, 2
	if count == 0 {
		if len(data) < 9 || binary.LittleEndian.Uint32(data[len(data)-5:]) != 0x31445741 {
			return false, nil
		}
		count = int(binary.LittleEndian.Uint32(data[len(data)-9:]))
		trailer, width = 9, 4
	} else if len(data) > 65535 {
		return false, nil // alpha.1's recoverable wrapped offset table
	}
	if count != len(d.fields) {
		return false, nil
	}
	end := len(data) - trailer - width*count
	if end < 0 {
		return false, nil
	}
	offset := func(i int) int {
		pos := end + width*i
		if width == 2 {
			return int(binary.LittleEndian.Uint16(data[pos:]))
		}
		return int(binary.LittleEndian.Uint32(data[pos:]))
	}
	previous := 0
	for i, f := range d.fields {
		off := offset(i)
		if off != previous || off >= end {
			return false, nil
		}
		p := off + 1 + int(data[off])
		if p >= end || string(data[off+1:p]) != f.Name {
			return false, nil
		}
		tag := data[p]
		p++
		finish := end
		if i+1 < count {
			finish = offset(i + 1)
		}
		if finish < p || finish > end {
			return false, nil
		}
		var value any
		if tag == 1 {
			if p != finish {
				return false, nil
			}
		} else {
			switch f.Type {
			case "integer":
				if tag != 3 || finish-p != 8 {
					return false, nil
				}
				if d.wanted[i] {
					value = strconv.FormatInt(int64(binary.LittleEndian.Uint64(data[p:])), 10)
				}
			case "number":
				if tag != 4 || finish-p != 8 {
					return false, nil
				}
				n := math.Float64frombits(binary.LittleEndian.Uint64(data[p:]))
				if math.IsInf(n, 0) || math.IsNaN(n) {
					return false, nil
				}
				if d.wanted[i] {
					value = n
				}
			case "boolean":
				if tag != 2 || finish-p != 1 || data[p] > 1 {
					return false, nil
				}
				if d.wanted[i] {
					value = data[p] == 1
				}
			default:
				sizeWidth := 2
				if tag == 8 {
					sizeWidth = 4
				} else if tag != 5 {
					return false, nil
				}
				if finish-p < sizeWidth {
					return false, nil
				}
				size := 0
				if sizeWidth == 2 {
					size = int(binary.LittleEndian.Uint16(data[p:]))
				} else {
					size = int(binary.LittleEndian.Uint32(data[p:]))
				}
				p += sizeWidth
				if size != finish-p {
					return false, nil
				}
				if d.wanted[i] {
					if f.Type == "json" {
						if e := decode(data[p:finish], &value); e != nil {
							return true, e
						}
					} else {
						value = string(data[p:finish])
					}
				}
			}
		}
		if d.wanted[i] {
			d.row[f.Name] = value
		}
		previous = finish
	}
	return true, nil
}
