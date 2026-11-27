package main

import (
	"encoding/json"
	"regexp"
)

// functionStagePattern matches Functions stage names (functions/stages.go validateStageName).
var functionStagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// functionInvokeInput builds the functions_invoke arguments for a routing decision or AI
// context lookup. Without a stage Functions runs the function's active version with its
// own settings; a stage runs that stage's promoted version, vars and access, so one flow
// definition can target the staging or production release of the same function.
func functionInvokeInput(id int64, stage string, event []byte) map[string]any {
	input := map[string]any{"id": id, "event": json.RawMessage(event)}
	if stage != "" {
		input["stage"] = stage
	}
	return input
}
