package main

// Readiness explains whether a semantic procedure is ready to be assigned and
// deployed. It is advisory: saving a draft remains separate from activation.
type Readiness struct {
	Valid                  bool     `json:"valid"`
	Errors                 []string `json:"errors"`
	Warnings               []string `json:"warnings"`
	SuggestedNextActions   []string `json:"suggested_next_actions"`
	StepCount              int      `json:"step_count"`
	RequiredParameterCount int      `json:"required_parameter_count"`
	AssignmentCount        int      `json:"assignment_count"`
}

func readiness(d Definition, status string, assignments []Assignment) Readiness {
	r := Readiness{
		Errors:               []string{},
		Warnings:             []string{},
		SuggestedNextActions: []string{},
		StepCount:            len(d.Steps),
		AssignmentCount:      len(assignments),
	}
	for _, parameter := range d.Parameters {
		if parameter.Required {
			r.RequiredParameterCount++
		}
	}
	copy := d.procedureOnly()
	if err := copy.validate(); err != nil {
		r.Errors = append(r.Errors, err.Error())
	}
	if status != "" && len(assignments) == 0 {
		r.SuggestedNextActions = append(r.SuggestedNextActions, "Create a paused assignment to choose the executor and parameter values.")
	}
	if status != "" && status != "active" {
		r.SuggestedNextActions = append(r.SuggestedNextActions, "Activate the reviewed process explicitly before enabling assignments or starting runs.")
	}
	r.Valid = len(r.Errors) == 0
	return r
}
