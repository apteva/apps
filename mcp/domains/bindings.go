package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// Read the public install identity once; resolving a selected binding must not
// fetch every account's credentials/metadata just to choose one connection.
func boundIDs(raw any) (map[int64]bool, int64, error) {
	out := map[int64]bool{}
	if raw == nil {
		return out, 0, nil
	}
	integer := func(v any) (int64, error) {
		n, err := strictIntArg(map[string]any{"binding": v}, "binding", 0, 1, 9007199254740991)
		return int64(n), err
	}
	if m, ok := raw.(map[string]any); ok {
		values := []any{}
		switch v := m["ids"].(type) {
		case []any:
			values = v
		case []int64:
			for _, id := range v {
				values = append(values, id)
			}
		case []float64:
			for _, id := range v {
				values = append(values, id)
			}
		case nil:
			return out, 0, nil
		default:
			return nil, 0, errors.New("invalid integration binding IDs")
		}
		var first int64
		for _, v := range values {
			id, err := integer(v)
			if err != nil {
				return nil, 0, err
			}
			out[id] = true
			if first == 0 {
				first = id
			}
		}
		if v, ok := m["default_id"]; ok && v != nil {
			id, err := integer(v)
			if err != nil {
				return nil, 0, err
			}
			if !out[id] {
				return nil, 0, errors.New("default connection is not in binding")
			}
			return out, id, nil
		}
		return out, first, nil
	}
	id, err := integer(raw)
	if err != nil {
		return nil, 0, err
	}
	out[id] = true
	return out, id, nil
}
func selectedConnectionID(ctx *sdk.AppCtx, roles ...string) (int64, error) {
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return 0, fmt.Errorf("read integration bindings: %w", err)
	}
	if identity == nil {
		return 0, errors.New("install identity missing")
	}
	for _, role := range roles {
		_, id, err := boundIDs(identity.Bindings[role])
		if err != nil {
			return 0, err
		}
		if id > 0 {
			return id, nil
		}
	}
	return 0, nil
}
