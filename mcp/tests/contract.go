package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type operation struct {
	id, method string
	segments   []string
	literal    int
}

type contract []operation

func parseContract(raw json.RawMessage) (contract, error) {
	var doc struct {
		Version string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || !strings.HasPrefix(doc.Version, "3.") {
		return nil, errors.New("expected an OpenAPI 3 document")
	}
	var ops contract
	ids := map[string]bool{}
	shapes := map[string]bool{}
	for path, item := range doc.Paths {
		if err := validateGatewayPath(path); err != nil || path == "" || len(path) > 2048 {
			return nil, fmt.Errorf("invalid OpenAPI path %q", path)
		}
		for method, raw := range item {
			switch method {
			case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			default:
				continue
			}
			var value struct {
				ID string `json:"operationId"`
			}
			if err := json.Unmarshal(raw, &value); err != nil || value.ID == "" || len(value.ID) > 200 || ids[value.ID] {
				return nil, fmt.Errorf("operation %s %s needs a unique operationId", method, path)
			}
			o := operation{id: value.ID, method: strings.ToUpper(method), segments: pathSegments(path)}
			shape := append([]string(nil), o.segments...)
			for i, segment := range o.segments {
				if parameterSegment(segment) {
					shape[i] = "{}"
				} else {
					o.literal++
				}
			}
			key := method + " " + strings.Join(shape, "/")
			if shapes[key] {
				return nil, fmt.Errorf("ambiguous OpenAPI template %s", key)
			}
			shapes[key], ids[o.id] = true, true
			ops = append(ops, o)
		}
	}
	if len(ops) == 0 || len(ops) > 500 {
		return nil, errors.New("OpenAPI must have 1–500 operations")
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].literal != ops[j].literal {
			return ops[i].literal > ops[j].literal
		}
		return ops[i].id < ops[j].id
	})
	return ops, nil
}

func pathSegments(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

func parameterSegment(segment string) bool {
	return len(segment) > 2 && segment[0] == '{' && segment[len(segment)-1] == '}'
}

func (ops contract) match(method, path string) (string, error) {
	parts := pathSegments(path)
	var match operation
	for _, op := range ops {
		if match.id != "" && op.literal < match.literal {
			break
		}
		if op.method != method || len(op.segments) != len(parts) {
			continue
		}
		matches := true
		for i, segment := range op.segments {
			if segment != parts[i] && (!parameterSegment(segment) || parts[i] == "") {
				matches = false
				break
			}
		}
		if matches {
			if match.id != "" {
				return "", errors.New("request matches multiple OpenAPI operations")
			}
			match = op
		}
	}
	return match.id, nil
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external schema references are disabled")
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var document any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("expected one JSON schema")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localSchemaLoader{})
	const uri = "https://tests.invalid/response-schema.json"
	if err := compiler.AddResource(uri, document); err != nil {
		return nil, err
	}
	return compiler.Compile(uri)
}
