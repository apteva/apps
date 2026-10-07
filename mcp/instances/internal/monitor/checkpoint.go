package monitor

import (
	"bytes"
	"encoding/json"
	"io"
)

const checkpointCacheBytes = 8 << 20

type cachedIncident struct {
	updated       int64
	recordings    int
	detailExpired bool
	comma         bool
	data          []byte
}

type checkpointCache struct {
	points    map[int64][]byte
	incidents map[string]cachedIncident
	bytes     int
	middle    []byte
	end       []byte
}

func compressedPart(encode func(io.Writer) error) ([]byte, error) {
	var out bytes.Buffer
	if err := compressFast(&out, encode); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func compressedJSON(value any, comma bool) ([]byte, error) {
	return compressedPart(func(out io.Writer) error {
		if comma {
			if _, err := io.WriteString(out, ","); err != nil {
				return err
			}
		}
		return json.NewEncoder(out).Encode(value)
	})
}

func compressedText(text string) ([]byte, error) {
	return compressedPart(func(out io.Writer) error { _, err := io.WriteString(out, text); return err })
}

// Gzip readers transparently concatenate members. The decompressed file remains
// exactly one JSON snapshot, readable by older collectors. Completed seconds and
// closed incidents are immutable, so reuse their compressed JSON members rather
// than encoding the entire retained history on every ten-second checkpoint.
func (c *checkpointCache) write(out io.Writer, latest *Metrics, points []Point, incidents []Incident) error {
	if c.points == nil {
		c.points = map[int64][]byte{}
		c.incidents = map[string]cachedIncident{}
		var err error
		c.middle, err = compressedText(`],"incidents":[`)
		if err != nil {
			return err
		}
		c.end, err = compressedText("]}\n")
		if err != nil {
			return err
		}
	}
	// Source points are ordered and only removed from the front of the ring.
	for ts, data := range c.points {
		if len(points) == 0 || ts <= points[0].Time || ts > points[len(points)-1].Time {
			delete(c.points, ts)
			c.bytes -= cap(data)
		}
	}
	retained := make(map[string]bool, len(incidents))
	for _, incident := range incidents {
		retained[incident.ID] = true
	}
	for id, entry := range c.incidents {
		if !retained[id] {
			delete(c.incidents, id)
			c.bytes -= cap(entry.data)
		}
	}
	header, err := json.Marshal(struct {
		Latest *Metrics `json:"latest"`
	}{latest})
	if err != nil {
		return err
	}
	prefix, err := compressedPart(func(w io.Writer) error {
		if _, err := w.Write(header[:len(header)-1]); err != nil {
			return err
		}
		_, err := io.WriteString(w, `,"points":[`)
		return err
	})
	if err != nil {
		return err
	}
	if _, err := out.Write(prefix); err != nil {
		return err
	}
	for i, point := range points {
		data := c.points[point.Time]
		if i == 0 || data == nil {
			data, err = compressedJSON(point, i > 0)
			if err != nil {
				return err
			}
			if i > 0 && c.bytes+cap(data) <= checkpointCacheBytes {
				c.points[point.Time] = data
				c.bytes += cap(data)
			}
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}
	if _, err := out.Write(c.middle); err != nil {
		return err
	}
	for i, incident := range incidents {
		entry, exists := c.incidents[incident.ID]
		if exists && (incident.End == 0 || entry.updated != incident.Updated || entry.recordings != len(incident.Recordings) || entry.detailExpired != incident.DetailExpired || entry.comma != (i > 0)) {
			delete(c.incidents, incident.ID)
			c.bytes -= cap(entry.data)
			exists = false
		}
		data := entry.data
		if !exists {
			data, err = compressedJSON(incident, i > 0)
			if err != nil {
				return err
			}
			if incident.End > 0 && c.bytes+cap(data) <= checkpointCacheBytes {
				c.incidents[incident.ID] = cachedIncident{updated: incident.Updated, recordings: len(incident.Recordings), detailExpired: incident.DetailExpired, comma: i > 0, data: data}
				c.bytes += cap(data)
			}
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}
	_, err = out.Write(c.end)
	return err
}
