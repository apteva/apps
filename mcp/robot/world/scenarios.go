package world

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadAll reads every *.json file in dir, finalises each, and returns
// them keyed by id. Returns an error on the first malformed scenario
// — better to fail closed at sidecar boot than silently drop a
// scenario the operator expects to be installed.
func LoadAll(dir string) (map[string]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*Scenario{}, nil
		}
		return nil, fmt.Errorf("read scenarios dir: %w", err)
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
		body, err := os.ReadFile(filepath.Join(dir, n))
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
