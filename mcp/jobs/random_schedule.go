package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	randomSchedulePeriodDay = "day"
	randomScheduleMaxRuns   = 100
	randomScheduleVersion   = "random-day-v1"
)

// RandomSchedule is the persisted configuration for schedule.kind=random.
// The first release supports calendar days; the explicit period keeps the
// wire format extensible without adding daily_random/weekly_random variants.
type RandomSchedule struct {
	Period            string `json:"period"`
	RunsPerPeriod     int    `json:"runs_per_period"`
	WindowStart       string `json:"window_start"`
	WindowEnd         string `json:"window_end"`
	MinSpacingMinutes int    `json:"min_spacing_minutes"`
}

func parseRandomSchedule(raw map[string]any) (RandomSchedule, error) {
	cfg := RandomSchedule{
		Period:            strings.ToLower(strings.TrimSpace(strArg(raw, "period"))),
		RunsPerPeriod:     intArg(raw, "runs_per_period", 0),
		WindowStart:       strings.TrimSpace(strArg(raw, "window_start")),
		WindowEnd:         strings.TrimSpace(strArg(raw, "window_end")),
		MinSpacingMinutes: intArg(raw, "min_spacing_minutes", 0),
	}
	if cfg.Period == "" {
		cfg.Period = randomSchedulePeriodDay
	}
	if cfg.Period != randomSchedulePeriodDay {
		return RandomSchedule{}, fmt.Errorf("schedule.period %q is unsupported; use day", cfg.Period)
	}
	if cfg.RunsPerPeriod < 1 || cfg.RunsPerPeriod > randomScheduleMaxRuns {
		return RandomSchedule{}, fmt.Errorf("schedule.runs_per_period must be between 1 and %d", randomScheduleMaxRuns)
	}
	start, err := parseWallMinute(cfg.WindowStart)
	if err != nil {
		return RandomSchedule{}, fmt.Errorf("schedule.window_start: %w", err)
	}
	end, err := parseWallMinute(cfg.WindowEnd)
	if err != nil {
		return RandomSchedule{}, fmt.Errorf("schedule.window_end: %w", err)
	}
	if end < start {
		return RandomSchedule{}, errors.New("schedule.window_end must not be before window_start")
	}
	if cfg.MinSpacingMinutes < 0 || cfg.MinSpacingMinutes > 1440 {
		return RandomSchedule{}, errors.New("schedule.min_spacing_minutes must be between 0 and 1440")
	}
	gap := max(1, cfg.MinSpacingMinutes)
	if end-start < (cfg.RunsPerPeriod-1)*gap {
		return RandomSchedule{}, errors.New("random schedule cannot fit the requested runs and minimum spacing inside the window")
	}
	return cfg, nil
}

func parseWallMinute(raw string) (int, error) {
	if len(raw) != 5 || raw[2] != ':' {
		return 0, errors.New("must use HH:MM")
	}
	hour, errHour := strconv.Atoi(raw[:2])
	minute, errMinute := strconv.Atoi(raw[3:])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, errors.New("must use a valid 24-hour HH:MM time")
	}
	return hour*60 + minute, nil
}

func newRandomScheduleSeed() (string, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	return hex.EncodeToString(seed), nil
}

func validateRandomScheduleSeed(seed string) error {
	b, err := hex.DecodeString(seed)
	if err != nil || len(b) != 32 {
		return errors.New("schedule_seed must be 64 hexadecimal characters")
	}
	return nil
}

// randomRunsForDate returns one deterministic set of minute-resolution runs
// for a local calendar date. Window endpoints are inclusive. Non-existent
// wall-clock minutes during a DST jump are omitted; an ambiguous wall-clock
// minute is represented once, which keeps the number of local-day runs stable.
func randomRunsForDate(cfg RandomSchedule, seed string, localDate time.Time, loc *time.Location) ([]time.Time, error) {
	if err := validateRandomScheduleSeed(seed); err != nil {
		return nil, err
	}
	start, _ := parseWallMinute(cfg.WindowStart)
	end, _ := parseWallMinute(cfg.WindowEnd)
	year, month, day := localDate.In(loc).Date()

	candidates := make([]time.Time, 0, end-start+1)
	for wallMinute := start; wallMinute <= end; wallMinute++ {
		hour, minute := wallMinute/60, wallMinute%60
		candidate := time.Date(year, month, day, hour, minute, 0, 0, loc)
		local := candidate.In(loc)
		if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })

	gap := max(1, cfg.MinSpacingMinutes)
	required := 1 + (cfg.RunsPerPeriod-1)*gap
	if len(candidates) < required {
		return nil, errors.New("random schedule cannot fit on this local date because of its timezone transition")
	}

	// Transform a spacing-constrained selection into an ordinary choice of N
	// distinct slots. Hash-ranking all transformed slots gives a deterministic,
	// unbiased subset without depending on math/rand implementation details.
	transformedCount := len(candidates) - (cfg.RunsPerPeriod-1)*(gap-1)
	type rankedSlot struct {
		index int
		score [sha256.Size]byte
	}
	ranked := make([]rankedSlot, transformedCount)
	key, _ := hex.DecodeString(seed)
	dateKey := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	for i := range ranked {
		mac := hmac.New(sha256.New, key)
		fmt.Fprintf(mac, "%s\x00%s\x00%d", randomScheduleVersion, dateKey, i)
		copy(ranked[i].score[:], mac.Sum(nil))
		ranked[i].index = i
	}
	sort.Slice(ranked, func(i, j int) bool {
		cmp := bytes.Compare(ranked[i].score[:], ranked[j].score[:])
		if cmp == 0 {
			return ranked[i].index < ranked[j].index
		}
		return cmp < 0
	})

	selected := make([]int, cfg.RunsPerPeriod)
	for i := range selected {
		selected[i] = ranked[i].index
	}
	sort.Ints(selected)
	runs := make([]time.Time, cfg.RunsPerPeriod)
	for i, transformed := range selected {
		runs[i] = candidates[transformed+i*(gap-1)]
		if i > 0 && runs[i].Sub(runs[i-1]) < time.Duration(cfg.MinSpacingMinutes)*time.Minute {
			return nil, errors.New("generated random schedule violates minimum spacing")
		}
	}
	return runs, nil
}

func nextRandomRunAfter(cfg RandomSchedule, seed string, loc *time.Location, after time.Time) (time.Time, error) {
	localAfter := after.In(loc)
	date := time.Date(localAfter.Year(), localAfter.Month(), localAfter.Day(), 0, 0, 0, 0, loc)
	for dayOffset := 0; dayOffset < 370; dayOffset++ {
		runs, err := randomRunsForDate(cfg, seed, date.AddDate(0, 0, dayOffset), loc)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if run.After(after) {
				return run, nil
			}
		}
	}
	return time.Time{}, errors.New("could not generate a random occurrence in the next year")
}

func previewRandomSchedule(schedule map[string]any, timezone, seed string, now time.Time, limit int) ([]string, string, error) {
	cfg, err := parseRandomSchedule(schedule)
	if err != nil {
		return nil, "", err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, "", fmt.Errorf("unknown timezone %q", timezone)
	}
	if seed == "" {
		seed, err = newRandomScheduleSeed()
		if err != nil {
			return nil, "", err
		}
	} else if err := validateRandomScheduleSeed(seed); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 50 {
		limit = 5
	}
	runs := make([]string, 0, limit)
	date := now.In(loc)
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	for offset := 0; offset < 370 && len(runs) < limit; offset++ {
		daily, err := randomRunsForDate(cfg, seed, date.AddDate(0, 0, offset), loc)
		if err != nil {
			continue
		}
		for _, run := range daily {
			if run.After(now) {
				runs = append(runs, run.UTC().Format(time.RFC3339))
				if len(runs) == limit {
					break
				}
			}
		}
	}
	if len(runs) < limit {
		return nil, "", errors.New("could not generate preview within one year")
	}
	return runs, seed, nil
}
