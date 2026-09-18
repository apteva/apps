package engine

import (
	"math"

	"github.com/valyala/fastjson"
)

// scanDecoder is local to one scan. Its row map is reused; consumers must copy
// records they retain. Values placed in the map own their data and never alias
// the parser arena, which is invalidated by the next ParseBytes call.
// Only trusted, normalized on-disk records use this path; API decoding stays
// with encoding/json, including its exact-number handling for JSON fields.
type scanDecoder struct {
	parser fastjson.Parser
	fields []Field
	row    Record
}

func newScanDecoder(c Collection, names []string, where *Filter) *scanDecoder {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
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
	d := &scanDecoder{row: Record{}}
	for _, f := range allFields(c) {
		if names == nil || wanted[f.Name] {
			d.fields = append(d.fields, f)
		}
	}
	return d
}

func (d *scanDecoder) decode(b []byte) (Record, error) {
	v, err := d.parser.ParseBytes(b)
	if err != nil {
		// The portable JSON contract permits deeper nesting than fastjson's
		// parser. Preserve compatibility with records accepted by encoding/json.
		var full Record
		if e := decode(b, &full); e != nil || full == nil {
			return nil, fail("storage_error", "invalid record JSON: %v", err)
		}
		for _, f := range d.fields {
			d.row[f.Name] = full[f.Name]
		}
		return d.row, nil
	}
	obj, err := v.Object()
	if err != nil {
		return nil, fail("storage_error", "record must be an object")
	}
	for _, f := range d.fields {
		value := obj.Get(f.Name)
		if value == nil || value.Type() == fastjson.TypeNull {
			d.row[f.Name] = nil
			continue
		}
		switch f.Type {
		case "number":
			var n float64
			n, err = value.Float64()
			if err == nil && (math.IsNaN(n) || math.IsInf(n, 0)) {
				return nil, fail("storage_error", "invalid numeric record value")
			}
			d.row[f.Name] = n
		case "boolean":
			d.row[f.Name], err = value.Bool()
		case "json":
			var data any
			err = decode(value.MarshalTo(nil), &data)
			d.row[f.Name] = data
		default:
			var text []byte
			text, err = value.StringBytes()
			d.row[f.Name] = string(text) // copy out of the reusable parser arena
		}
		if err != nil {
			return nil, fail("storage_error", "invalid stored field %s: %v", f.Name, err)
		}
	}
	return d.row, nil
}
