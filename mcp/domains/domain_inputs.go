package main

import (
	"errors"
	"fmt"
	"strings"
)

// Accept existing single/CSV inputs as well as JSON arrays used by agents.
func domainInputs(args map[string]any, limit int, allowCSV bool) ([]string, error) {
	var names []string
	if value, ok := args["domains"]; ok {
		switch v := value.(type) {
		case []string:
			names = v
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, errors.New("domains must contain strings only")
				}
				names = append(names, s)
			}
		case string:
			if !allowCSV {
				return nil, errors.New("domains must be an array")
			}
			if v == "" {
				v = strArg(args, "domain")
			}
			names = strings.Split(v, ",")
		default:
			return nil, errors.New("domains must be an array of domain names")
		}
	} else if name, ok := args["domain"].(string); ok {
		names = []string{name}
	}
	if len(names) == 0 || len(names) > limit {
		return nil, fmt.Errorf("provide between 1 and %d domains", limit)
	}
	return names, nil
}

func optionalBool(args map[string]any, name string) (bool, error) {
	v, ok := args[name]
	if !ok {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return b, nil
}
