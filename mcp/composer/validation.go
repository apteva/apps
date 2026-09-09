package main

import (
	"fmt"
	"strings"
)

func validateOutputValues(o Output) error {
	switch o.Format {
	case "", "mp4", "mp3", "wav", "m4a", "aac":
	default:
		return fmt.Errorf("unsupported output format %q", o.Format)
	}
	switch o.Resolution {
	case "", "sd", "hd", "fullhd", "4k":
	default:
		return fmt.Errorf("unsupported resolution %q", o.Resolution)
	}
	switch o.Aspect {
	case "", "16:9", "9:16", "1:1", "4:3":
	default:
		return fmt.Errorf("unsupported aspect %q", o.Aspect)
	}
	switch o.FPS {
	case 0, 24, 25, 30, 60:
	default:
		return fmt.Errorf("fps must be 24, 25, 30, or 60")
	}
	return nil
}

func ensureClipUIDs(e *Edit) {
	if e == nil {
		return
	}
	used := map[string]bool{}
	for ti := range e.Timeline.Tracks {
		for ci := range e.Timeline.Tracks[ti].Clips {
			c := &e.Timeline.Tracks[ti].Clips[ci]
			id := strings.TrimSpace(c.UID)
			if id == "" || used[id] {
				id = fmt.Sprintf("track-%d-clip-%d", ti+1, ci+1)
				for used[id] {
					id += "-copy"
				}
			}
			c.UID = id
			used[id] = true
		}
	}
}
