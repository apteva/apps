package main

import (
	"fmt"
	"strconv"
	"strings"
)

// seedRefKey marks an object inside a seed input as a reference to the result
// of an earlier seed: {"$ref": "<index>.<path>"}. The index is the position of
// a preceding entry in spec.seeds and the path walks that seed's result, so a
// seed can consume ids created by the seeds before it.
const seedRefKey = "$ref"

// seedRefResolver turns one reference string into the value it stands for.
type seedRefResolver func(ref string) (any, error)

// resolveSeedRefs returns a copy of input with every reference replaced by the
// value it points at. results holds the outputs of the seeds that already ran,
// in spec order.
func resolveSeedRefs(input map[string]any, results []any, targets ...map[string]string) (map[string]any, error) {
	return walkSeedRefs(input, func(ref string) (any, error) {
		if name, ok := strings.CutPrefix(ref, "target:"); ok {
			if len(targets) > 0 {
				if value, ok := targets[0][name]; ok {
					return value, nil
				}
			}
			return nil, fmt.Errorf("unknown runtime target %q", name)
		}
		return lookupSeedRef(ref, results)
	})
}

// validateSeedRefs rejects references the seed at index can never resolve, so a
// malformed or forward reference surfaces when the definition is written rather
// than part-way through a start.
func validateSeedRefs(input map[string]any, index int, targets ...map[string]RuntimeTarget) error {
	_, err := walkSeedRefs(input, func(ref string) (any, error) {
		if name, ok := strings.CutPrefix(ref, "target:"); ok {
			if !validID(name) {
				return nil, fmt.Errorf("invalid runtime target %q", name)
			}
			if len(targets) > 0 {
				if _, exists := targets[0][name]; !exists {
					return nil, fmt.Errorf("unknown runtime target %q", name)
				}
			}
			return nil, nil
		}
		target, _, err := parseSeedRef(ref)
		if err != nil {
			return nil, err
		}
		if target >= index {
			return nil, fmt.Errorf("%s %q: seed %d may only reference earlier seeds", seedRefKey, ref, index)
		}
		return nil, nil
	})
	return err
}

func walkSeedRefs(input map[string]any, resolve seedRefResolver) (map[string]any, error) {
	if len(input) == 0 {
		return input, nil
	}
	resolved, err := resolveSeedRefValue(input, resolve)
	if err != nil {
		return nil, err
	}
	out, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input resolved to %T, want an object", resolved)
	}
	return out, nil
}

func resolveSeedRefValue(value any, resolve seedRefResolver) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed["$target"]; ok {
			name, ok := raw.(string)
			if !ok || len(typed) != 1 {
				return nil, fmt.Errorf("$target requires one named runtime target")
			}
			return resolve("target:" + name)
		}
		if raw, ok := typed[seedRefKey]; ok {
			if len(typed) > 1 {
				return nil, fmt.Errorf("%s object must not carry other keys", seedRefKey)
			}
			ref, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string, got %T", seedRefKey, raw)
			}
			return resolve(ref)
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := resolveSeedRefValue(item, resolve)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			resolved, err := resolveSeedRefValue(item, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

// lookupSeedRef resolves "<index>.<path>" against the results collected so far.
// An unknown index or a path that misses is an error: an unresolved reference
// handed to a tool fails later as a confusing argument error, or worse, silently.
func lookupSeedRef(ref string, results []any) (any, error) {
	index, path, err := parseSeedRef(ref)
	if err != nil {
		return nil, err
	}
	if index >= len(results) {
		return nil, fmt.Errorf("%s %q: seed %d has not run yet", seedRefKey, ref, index)
	}
	value, ok := jsonPathLookup(results[index], path)
	if !ok {
		return nil, fmt.Errorf("%s %q: seed %d result has no %q", seedRefKey, ref, index, path)
	}
	return value, nil
}

// parseSeedRef splits "<index>.<path>" into the seed index and the path within
// that seed's result. An empty path references the whole result.
func parseSeedRef(ref string) (int, string, error) {
	head, path, _ := strings.Cut(strings.TrimSpace(ref), ".")
	index, err := strconv.Atoi(head)
	if err != nil {
		return 0, "", fmt.Errorf("%s %q: must start with a seed index", seedRefKey, ref)
	}
	if index < 0 {
		return 0, "", fmt.Errorf("%s %q: seed index must not be negative", seedRefKey, ref)
	}
	return index, path, nil
}
