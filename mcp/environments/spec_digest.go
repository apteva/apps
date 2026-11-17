package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// specDigest is the canonical digest of a stored EnvironmentSpec: the
// lowercase hex SHA-256 of its canonical JSON. Canonical JSON is the stored
// spec decoded with UseNumber (numbers keep their stored text) and re-encoded
// by encoding/json, which sorts object keys and emits no insignificant
// whitespace. Every spec_digest this app reports or compares comes from here.
func specDigest(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("spec digest: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", errors.New("spec digest: trailing data after spec")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("spec digest: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

var specDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func shortDigest(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

const (
	maxSeedVars          = 16
	maxSeedVarValueBytes = 4096
)

var seedVarNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// validateSeedVars checks seed_vars supplied with a pinned run and returns
// them as strings.
func validateSeedVars(raw map[string]any) (map[string]string, error) {
	if len(raw) > maxSeedVars {
		return nil, fmt.Errorf("seed_vars may contain at most %d entries", maxSeedVars)
	}
	out := make(map[string]string, len(raw))
	for name, value := range raw {
		if !seedVarNamePattern.MatchString(name) {
			return nil, fmt.Errorf("seed_vars name %q must match ^[a-z][a-z0-9_]{0,63}$", name)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("seed_vars %s must be a string", name)
		}
		if len(text) > maxSeedVarValueBytes {
			return nil, fmt.Errorf("seed_vars %s exceeds %d bytes", name, maxSeedVarValueBytes)
		}
		if strings.ContainsAny(text, "\r\n") {
			return nil, fmt.Errorf("seed_vars %s must not contain CR or LF", name)
		}
		out[name] = text
	}
	return out, nil
}

// applySeedVars substitutes {{name}} placeholders inside seeds[].input. A
// string that is exactly {{name}} becomes the value; occurrences inside larger
// strings are replaced textually. Unknown names are left untouched so callers
// can keep their own placeholders. Only string values are rewritten; object
// keys are not.
func applySeedVars(seeds []SeedStep, vars map[string]string) []SeedStep {
	if len(vars) == 0 || len(seeds) == 0 {
		return seeds
	}
	out := make([]SeedStep, len(seeds))
	for i, seed := range seeds {
		out[i] = seed
		if seed.Input != nil {
			out[i].Input, _ = substituteSeedVars(seed.Input, vars).(map[string]any)
		}
	}
	return out
}

func substituteSeedVars(value any, vars map[string]string) any {
	switch v := value.(type) {
	case string:
		if !strings.Contains(v, "{{") {
			return v
		}
		if strings.HasPrefix(v, "{{") && strings.HasSuffix(v, "}}") {
			if got, ok := vars[v[2:len(v)-2]]; ok {
				return got
			}
		}
		var b strings.Builder
		rest := v
		for {
			start := strings.Index(rest, "{{")
			if start < 0 {
				b.WriteString(rest)
				break
			}
			end := strings.Index(rest[start+2:], "}}")
			if end < 0 {
				b.WriteString(rest)
				break
			}
			name := rest[start+2 : start+2+end]
			b.WriteString(rest[:start])
			if got, ok := vars[name]; ok {
				b.WriteString(got)
				rest = rest[start+2+end+2:]
				continue
			}
			// Unknown placeholder: keep "{{" and continue scanning after it so a
			// nested known placeholder later in the string is still replaced.
			b.WriteString("{{")
			rest = rest[start+2:]
		}
		return b.String()
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = substituteSeedVars(item, vars)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = substituteSeedVars(item, vars)
		}
		return out
	default:
		return value
	}
}
