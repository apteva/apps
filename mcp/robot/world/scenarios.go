package world

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// LoadAll reads every *.json file under dir on the supplied fs.FS,
// finalises each, and returns them keyed by id. Use embed.FS so the
// scenarios travel with the binary — sidecars are spawned with a CWD
// that's not the cloned source tree, so disk-relative loading would
// silently return zero scenarios at runtime.
//
// dir is interpreted using io/fs path semantics (forward slashes,
// relative to the embed root) — passing "." or "scenarios" both
// work, depending on how the embed directive is shaped.
func LoadAll(fsys fs.FS, dir string) (map[string]*Scenario, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		// Missing dir on the embed FS is a hard error — different from
		// disk loading where "no scenarios installed yet" was a sane
		// empty-result. Embedded sidecars must ship scenarios.
		return nil, fmt.Errorf("read scenarios dir %q: %w", dir, err)
	}
	out := make(map[string]*Scenario)
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		body, err := fs.ReadFile(fsys, path.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		var s Scenario
		if err := json.Unmarshal(body, &s); err != nil {
			return nil, fmt.Errorf("%s: parse: %w", n, err)
		}
		if err := s.Finalize(); err != nil {
			return nil, err
		}
		if _, dup := out[s.ID]; dup {
			return nil, fmt.Errorf("scenario id %q declared twice", s.ID)
		}
		out[s.ID] = &s
	}
	return out, nil
}
