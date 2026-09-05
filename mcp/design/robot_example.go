package main

import (
	_ "embed"
	"encoding/json"
)

//go:embed examples/open-rover.json
var openRoverExampleJSON []byte

func openRoverExample() map[string]any {
	var definition map[string]any
	if err := json.Unmarshal(openRoverExampleJSON, &definition); err != nil {
		panic("invalid embedded open rover example: " + err.Error())
	}
	return definition
}
