package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"strings"
)

// Protocol adapters isolate wire aliases and native GPIO policy from the
// generic resource, ingestion and command lifecycle.
type protocolAdapter interface {
	Decode(string, string) (dataMessage, error)
	Encode(string, string, string, map[string]any, int) map[string]any
	Validate(Device, string, string, map[string]any, string) error
}
type nativeAdapter struct{}
type genericAdapter struct{}
type dataMessage struct {
	MessageID   string         `json:"message_id"`
	ObservedAt  string         `json:"observed_at"`
	SessionID   string         `json:"session_id"`
	Sequence    *int64         `json:"sequence,omitempty"`
	Mode        string         `json:"mode"`
	Data        any            `json:"data"`
	Metadata    map[string]any `json:"metadata"`
	UpdateState bool           `json:"update_state"`
}

func adapterFor(protocol string) (protocolAdapter, error) {
	switch protocol {
	case "apteva.devices/v1":
		return nativeAdapter{}, nil
	case "apteva.devices/v2":
		return genericAdapter{}, nil
	}
	return nil, fmt.Errorf("unsupported device protocol %q", protocol)
}
func (nativeAdapter) Decode(kind, payload string) (dataMessage, error) {
	var values map[string]any
	err := json.Unmarshal([]byte(payload), &values)
	if err != nil || values == nil {
		return dataMessage{}, errors.New("native payload must be a JSON object")
	}
	if variables, ok := values["variables"].(map[string]any); ok {
		values = variables
	}
	// The v1 adapter retains the original v1 field convention. v2 data is opaque.
	return dataMessage{Data: withoutEnvelopeFields(values), Metadata: map[string]any{}, UpdateState: true, Mode: "patch"}, nil
}
func (genericAdapter) Decode(kind, payload string) (dataMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return dataMessage{}, err
	}
	if _, ok := raw["data"]; !ok {
		return dataMessage{}, errors.New("data is required (may be any JSON value)")
	}
	var m dataMessage
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	if m.MessageID == "" || len(m.MessageID) > 128 {
		return m, errors.New("message_id must contain 1 to 128 characters")
	}
	if m.ObservedAt == "" {
		return m, errors.New("observed_at required")
	}
	if len(m.SessionID) > 128 {
		return m, errors.New("session_id exceeds 128 characters")
	}
	if m.Sequence != nil && (*m.Sequence < 0 || m.SessionID == "") {
		return m, errors.New("sequence requires session_id and a nonnegative integer")
	}
	if m.Mode == "" {
		m.Mode = "patch"
	}
	if m.Mode != "patch" && m.Mode != "snapshot" {
		return m, errors.New("mode must be patch or snapshot")
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	if kind == "state" {
		m.UpdateState = true
	}
	return m, nil
}
func (nativeAdapter) Encode(id, operation, target string, args map[string]any, timeout int) map[string]any {
	return buildWireCommand(id, operation, target, args, timeout)
}
func (genericAdapter) Encode(id, operation, target string, args map[string]any, timeout int) map[string]any {
	return map[string]any{"protocol": "apteva.devices/v2", "command_id": id, "operation": operation, "target": target, "arguments": args, "timeout_ms": timeout}
}
func (nativeAdapter) Validate(d Device, operation, target string, args map[string]any, key string) error {
	return validateCommand(d, operation, target, args, key)
}
func (genericAdapter) Validate(d Device, operation, target string, args map[string]any, key string) error {
	if !operationPattern.MatchString(operation) || len(target) > 128 || len(key) > 128 {
		return errors.New("invalid command operation, target, or idempotency key")
	}
	operations, _ := d.Manifest["operations"].([]any)
	for _, value := range operations {
		op, _ := value.(map[string]any)
		if str(op, "name") == operation {
			if schema, ok := op["input_schema"].(map[string]any); ok {
				return validateJSONSchema(schema, args)
			}
			return nil
		}
	}
	return errors.New("operation is not advertised in capabilities")
}

// No network loader is installed: remote schema references are rejected.
func compileSchema(schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(denySchemaLoader{})
	if err := c.AddResource("https://devices.invalid/schema.json", schema); err != nil {
		return nil, err
	}
	return c.Compile("https://devices.invalid/schema.json")
}

type denySchemaLoader struct{}

func (denySchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference is not allowed: %s", url)
}
func validateJSONSchema(schema map[string]any, value any) error {
	compiled, err := compileSchema(schema)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	normalized, e := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if e != nil {
		err = e

		return err
	}
	return compiled.Validate(normalized)
}
func validateOperations(manifest map[string]any) error {
	raw, exists := manifest["operations"]
	if !exists {
		return nil
	}
	ops, ok := raw.([]any)
	if !ok || len(ops) > 256 {
		return errors.New("operations must be an array of at most 256 entries")
	}
	seen := map[string]bool{}
	for _, v := range ops {
		op, ok := v.(map[string]any)
		if !ok {
			return errors.New("operation descriptor must be an object")
		}
		name := str(op, "name")
		if !operationPattern.MatchString(name) || seen[name] {
			return errors.New("operation names must be valid and unique")
		}
		seen[name] = true
		for _, field := range []string{"input_schema", "output_schema"} {
			if v, exists := op[field]; exists {
				s, ok := v.(map[string]any)
				if !ok {
					return fmt.Errorf("%s must be an object", field)
				}
				if _, err := compileSchema(s); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
