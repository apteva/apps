package main

// Prospective (live) evidence and the follow-up plan.
//
// Prospective results come only from forecasts recorded in intel_forecasts
// before their question resolved, scored by the same one-forecast-per-
// question rule as the ledger. They are computed at read time from the
// live tables and never mixed with backtest numbers.

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

func prospectiveSection(db *sql.DB, pid string, lockHours float64, B int, seed int64, now time.Time) (map[string]any, error) {
	resolved, err := listQuestions(db, pid, "resolved", 5000)
	if err != nil {
		return nil, err
	}
	recs := []scoredRec{}
	adjusted, lateOnly, noMarket := 0, 0, 0
	for _, q := range resolved {
		if q.Outcome == nil {
			continue
		}
		fs, err := forecastsFor(db, q.ID)
		if err != nil {
			return nil, err
		}
		f := pickScoredForecast(fs, q, lockHours)
		if f == nil {
			lateOnly++
			continue
		}
		if f.MarketPrice == nil {
			noMarket++
			continue
		}
		cluster := fmt.Sprintf("q%d", q.ID)
		if q.EventKey != "" {
			cluster = q.Venue + ":" + q.EventKey
		}
		recs = append(recs, scoredRec{item: int(q.ID), cluster: cluster, cutoff: f.EvidenceCutoff, outcome: *q.Outcome,
			p: map[string]float64{"model": *f.Probability, "market": *f.MarketPrice, "uniform": 0.5}})
		if f.Adjustment != 0 {
			adjusted++
		}
	}
	out := map[string]any{"as_of": ts(now), "n_resolved": len(resolved), "n_scored": len(recs), "n_ai_adjusted": adjusted,
		"unscored_no_forecast_before_outcome": lateOnly, "unscored_no_market_price": noMarket,
		"rule": fmt.Sprintf("One forecast per question: the latest made at least %.0fh before the deadline, else the latest before resolution. Forecasts made after the outcome was known are never scored.", lockHours)}
	if len(recs) > 0 {
		out["brier_model"] = mkCI(recs, B, seed, func(r scoredRec) float64 { return brier(r.p["model"], r.outcome) })
		out["brier_market"] = mkCI(recs, B, seed+1, func(r scoredRec) float64 { return brier(r.p["market"], r.outcome) })
		out["brier_uniform"] = 0.25
		d := mkCI(recs, B, seed+2, func(r scoredRec) float64 { return brier(r.p["model"], r.outcome) - brier(r.p["market"], r.outcome) })
		out["brier_model_minus_market"] = d
		out["verdict"] = verdictFor(len(recs), d, 30)
		bins, ece := calBins(recs, "model")
		out["calibration_model"], out["ece_model"] = bins, ece
	} else {
		out["verdict"] = "no resolved questions with a forecast recorded before the outcome yet"
	}
	return out, nil
}

// followUpPlan lists open questions with their scoring dates, and when the
// prospective sample can first reach the size a verdict needs.
func followUpPlan(db *sql.DB, pid string, lockHours float64, scoredSoFar int, now time.Time) (map[string]any, error) {
	open, err := listQuestions(db, pid, "open", 5000)
	if err != nil {
		return nil, err
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Deadline.Before(open[j].Deadline) })
	rows := []map[string]any{}
	locked := 0
	cum := scoredSoFar
	var reach30, reach100 *time.Time
	for _, q := range open {
		fs, err := forecastsFor(db, q.ID)
		if err != nil {
			return nil, err
		}
		row := map[string]any{"question_id": q.ID, "question": q.Text, "venue": q.Venue, "deadline": ts(q.Deadline),
			"score_after": ts(q.Deadline.Add(48 * time.Hour))}
		var last *Forecast
		for i := range fs {
			if !fs[i].Abstained && fs[i].Probability != nil {
				last = &fs[i]
			}
		}
		if last != nil {
			row["forecast"], row["market_at_forecast"], row["ai_adjustment"], row["forecast_at"] = *last.Probability, last.MarketPrice, last.Adjustment, ts(last.CreatedAt)
			if !last.CreatedAt.After(q.Deadline.Add(-time.Duration(lockHours * float64(time.Hour)))) {
				locked++
				row["locked"] = true
			}
			cum++
			at := q.Deadline.Add(48 * time.Hour)
			if cum >= 30 && reach30 == nil {
				reach30 = &at
			}
			if cum >= 100 && reach100 == nil {
				reach100 = &at
			}
		} else {
			row["forecast"] = nil
		}
		rows = append(rows, row)
	}
	out := map[string]any{"as_of": ts(now), "open_questions": len(open), "with_locked_forecast": locked, "questions": rows,
		"steps": []string{
			"Keep the scheduled cycle running: it records forecasts before deadlines and resolves market questions from their venues.",
			"Re-score with evaluation_report after each score_after date; nothing is scored before its outcome is final.",
			"Do not shorten observation periods: a verdict on AI needs at least 30 scored prospective questions, and a stable one about 100.",
			"Keep importing questions so the prospective sample grows; backtest numbers never substitute for it.",
		}}
	if reach30 != nil {
		out["earliest_n30"] = ts(*reach30)
	} else {
		out["earliest_n30"] = "not reachable with the questions open today; import more"
	}
	if reach100 != nil {
		out["earliest_n100"] = ts(*reach100)
	} else {
		out["earliest_n100"] = "not reachable with the questions open today; import more"
	}
	return out, nil
}
