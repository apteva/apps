package ruleengine

import (
	"fmt"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

// ScheduleInputs adds deterministic clock observations inside the supplied
// tape horizon. It never creates a price, liquidity, or events beyond that tape.
func ScheduleInputs(p *Program, inputs []sim.Input) ([]sim.Input, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return inputs, nil
	}
	start, end := inputs[0].AvailableAt, inputs[0].AvailableAt
	ids := map[string]bool{}
	for _, in := range inputs {
		if in.AvailableAt.Before(start) {
			start = in.AvailableAt
		}
		if in.AvailableAt.After(end) {
			end = in.AvailableAt
		}
		ids[in.ID] = true
	}
	loc, _ := time.LoadLocation(p.Timezone)
	local := start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	out := append([]sim.Input(nil), inputs...)
	for !day.After(end) {
		for name, clock := range p.Schedules {
			m, _ := minute(clock)
			at := time.Date(day.Year(), day.Month(), day.Day(), m/60, m%60, 0, 0, loc)
			// Reject DST-skipped wall times rather than silently move an action.
			if at.Hour()*60+at.Minute() != m {
				return nil, fmt.Errorf("schedule %s falls in a DST gap", name)
			}
			id := "schedule/" + name + "/" + day.Format("2006-01-02")
			if !at.Before(start) && !at.After(end) && !ids[id] {
				out = append(out, sim.Input{ID: id, Type: "clock", Source: "rule_schedule", EventTime: at, AvailableAt: at, Metadata: map[string]string{"schedule": name}})
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out, nil
}
