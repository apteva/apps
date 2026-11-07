package main

import (
	"github.com/pion/sdp/v3"
	"strconv"
	"strings"
	"time"
)

type sipCodecMapping struct {
	Payload   string `json:"payload"`
	Codec     string `json:"codec"`
	ClockRate int    `json:"clock_rate"`
}
type sipCodecEvidence struct {
	At              string            `json:"at"`
	Source          string            `json:"source"`
	LocalPreference []string          `json:"local_preference"`
	Remote          []sipCodecMapping `json:"remote_formats"`
	Selected        string            `json:"selected"`
}

// Whitelisted codec fields only. SDP addresses, keys, credentials, arbitrary
// attributes and raw responses must never enter diagnostic persistence.
func codecEvidence(body []byte, local []string, selected, source string) *sipCodecEvidence {
	if len(body) > 64<<10 {
		return nil
	}
	var description sdp.SessionDescription
	if description.Unmarshal(body) != nil {
		return nil
	}
	e := &sipCodecEvidence{At: ringTime(time.Now()), Source: source, Selected: selected}
	for _, c := range local {
		if c == "G722" || c == "PCMU" || c == "PCMA" {
			e.LocalPreference = append(e.LocalPreference, c)
		}
	}
	if len(e.LocalPreference) == 0 {
		e.LocalPreference = []string{"G722", "PCMU", "PCMA"}
	}
	for _, m := range description.MediaDescriptions {
		if m.MediaName.Media != "audio" {
			continue
		}
		mappings := map[string]string{}
		for _, a := range m.Attributes {
			if strings.EqualFold(a.Key, "rtpmap") {
				f := strings.Fields(a.Value)
				if len(f) >= 2 {
					mappings[f[0]] = f[1]
				}
			}
		}
		for _, f := range m.MediaName.Formats {
			n, err := strconv.Atoi(f)
			if err != nil || n < 0 || n > 127 || len(e.Remote) >= 16 {
				continue
			}
			c := sipCodecMapping{Payload: strconv.Itoa(n), Codec: "unrecognized"}
			if raw, ok := mappings[f]; ok {
				parts := strings.Split(strings.ToUpper(raw), "/")
				switch parts[0] {
				case "G722", "PCMU", "PCMA", "OPUS", "TELEPHONE-EVENT":
					c.Codec = parts[0]
				}
				if len(parts) > 1 {
					rate, _ := strconv.Atoi(parts[1])
					if rate > 0 && rate <= 192000 {
						c.ClockRate = rate
					}
				}
			} else {
				switch n {
				case 9:
					c.Codec = "G722"
					c.ClockRate = 8000
				case 0:
					c.Codec = "PCMU"
					c.ClockRate = 8000
				case 8:
					c.Codec = "PCMA"
					c.ClockRate = 8000
				}
			}
			e.Remote = append(e.Remote, c)
		}
		break
	}
	return e
}
func (d *sipLiveDiagnostics) codecs(body []byte, local []string, selected, source string) {
	if d == nil {
		return
	}
	e := codecEvidence(body, local, selected, source)
	d.mu.Lock()
	d.signal.Negotiation = e
	d.mu.Unlock()
}
