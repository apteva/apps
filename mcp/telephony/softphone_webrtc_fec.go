package main

import (
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/pion/sdp/v3"
)

// One immutable preference per authenticated media attachment. Omitting it
// retains today's behavior and never changes another call's codec.
func parseRTCFECPreference(query url.Values) (*bool, error) {
	values, exists := query["webrtc_fec"]
	if !exists {
		return nil, nil
	}
	if len(values) != 1 || values[0] != "true" && values[0] != "false" {
		return nil, errors.New("invalid WebRTC FEC preference")
	}
	fec := values[0] == "true"
	return &fec, nil
}

// Pion retains the remote fmtp in its negotiated codecs. Local registration
// alone does not override a browser's useinbandfec=1 offer. Normalize just the
// Opus FEC preference for this opted-out connection so the answer tells the
// browser not to send redundancy either. No other codec, ICE or media setting
// is altered; the default enabled path retains its existing SDP.
func disableRTCSDPFEC(raw string) (string, error) {
	var description sdp.SessionDescription
	if err := description.Unmarshal([]byte(raw)); err != nil {
		return "", err
	}
	for _, media := range description.MediaDescriptions {
		if media.MediaName.Media != "audio" {
			continue
		}
		opus := map[string]bool{}
		for _, attribute := range media.Attributes {
			fields := strings.Fields(attribute.Value)
			if attribute.Key == "rtpmap" && len(fields) == 2 && strings.EqualFold(strings.Split(fields[1], "/")[0], "opus") {
				opus[fields[0]] = false
			}
		}
		for i := range media.Attributes {
			attribute := &media.Attributes[i]
			if attribute.Key != "fmtp" {
				continue
			}
			fields := strings.SplitN(attribute.Value, " ", 2)
			if _, ok := opus[fields[0]]; !ok {
				continue
			}
			var parameters []string
			if len(fields) == 2 {
				for _, parameter := range strings.Split(fields[1], ";") {
					parameter = strings.TrimSpace(parameter)
					key := strings.TrimSpace(strings.SplitN(parameter, "=", 2)[0])
					if parameter != "" && !strings.EqualFold(key, "useinbandfec") {
						parameters = append(parameters, parameter)
					}
				}
			}
			parameters = append(parameters, "useinbandfec=0")
			attribute.Value = fields[0] + " " + strings.Join(parameters, ";")
			opus[fields[0]] = true
		}
		var missing []string
		for payload, present := range opus {
			if !present {
				missing = append(missing, payload)
			}
		}
		slices.Sort(missing)
		for _, payload := range missing {
			media.Attributes = append(media.Attributes, sdp.Attribute{Key: "fmtp", Value: payload + " useinbandfec=0"})
		}
	}
	result, err := description.Marshal()
	return string(result), err
}
