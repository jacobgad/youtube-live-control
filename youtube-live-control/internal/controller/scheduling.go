package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/atomicfile"
	"github.com/jacobgad/youtube-live-control/internal/preset"
)

const noPresetLabel = "No presets — create one in the web UI"

// scheduling is the state behind the YouTube Live Scheduling device. Date and time
// are recomputed from the preset (never persisted) so a stale date cannot linger.
type scheduling struct {
	presets   []preset.Preset
	labels    []string
	presetID  string
	date      time.Time
	timeOfDay time.Duration
	timeSet   bool
}

func (sc *scheduling) setPresets(list []preset.Preset) {
	sc.presets = list
	sc.labels = make([]string, 0, len(list))
	seen := map[string]int{}
	for _, p := range list {
		label := p.Name
		seen[label]++
		if n := seen[label]; n > 1 {
			label = fmt.Sprintf("%s (%d)", label, n)
		}
		sc.labels = append(sc.labels, label)
	}
	if sc.preset() == nil {
		sc.presetID = ""
	}
}

func (sc *scheduling) preset() *preset.Preset {
	for i := range sc.presets {
		if sc.presets[i].ID == sc.presetID {
			p := sc.presets[i]
			return &p
		}
	}
	return nil
}

func (sc *scheduling) presetOptions() []string {
	if len(sc.labels) == 0 {
		return []string{noPresetLabel}
	}
	return sc.labels
}

func (sc *scheduling) presetLabel() string {
	for i := range sc.presets {
		if sc.presets[i].ID == sc.presetID {
			return sc.labels[i]
		}
	}
	return noPresetLabel
}

func (sc *scheduling) presetIDForLabel(label string) (string, bool) {
	for i, l := range sc.labels {
		if l == label {
			return sc.presets[i].ID, true
		}
	}
	return "", false
}

func (sc *scheduling) applyDefaults(now time.Time, taken []time.Time) {
	p := sc.preset()
	if p == nil {
		sc.date, sc.timeOfDay, sc.timeSet = time.Time{}, 0, false
		return
	}
	start := nextFreeSlot(*p, now, taken).Local()
	sc.date = dayOf(start)
	sc.timeOfDay = time.Duration(start.Hour())*time.Hour + time.Duration(start.Minute())*time.Minute
	sc.timeSet = true
}

func dayOf(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// start combines the chosen day and time-of-day in local time; zero while either is unset.
func (sc *scheduling) start() time.Time {
	if sc.date.IsZero() || !sc.timeSet {
		return time.Time{}
	}
	return sc.date.Add(sc.timeOfDay)
}

func nextFreeSlot(p preset.Preset, now time.Time, taken []time.Time) time.Time {
	start := p.NextStart(now)
	for range 52 {
		if !slotTaken(start, taken) {
			break
		}
		start = start.AddDate(0, 0, 7)
	}
	return start
}

func slotTaken(start time.Time, taken []time.Time) bool {
	for _, t := range taken {
		if !t.IsZero() && t.Truncate(time.Minute).Equal(start.Truncate(time.Minute)) {
			return true
		}
	}
	return false
}

// The date and time entities exchange plain ISO values (2006-01-02, 15:04:05) or
// Home Assistant's null payload.
func (sc *scheduling) datePayload() string {
	if sc.date.IsZero() {
		return mqttNone
	}
	return sc.date.Format(time.DateOnly)
}

func (sc *scheduling) timePayload() string {
	if !sc.timeSet {
		return mqttNone
	}
	return time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Add(sc.timeOfDay).Format(time.TimeOnly)
}

func parseDate(raw string) (time.Time, error) {
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return dayOf(t), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO date", raw)
}

func parseTimeOfDay(raw string) (time.Duration, error) {
	for _, layout := range []string{time.TimeOnly, "15:04:05.999999", "15:04"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
		}
	}
	return 0, fmt.Errorf("%q is not an ISO time", raw)
}

func (sc *scheduling) canSchedule(now time.Time) bool {
	p := sc.preset()
	if p == nil || p.StreamID == "" {
		return false
	}
	start := sc.start()
	return !start.IsZero() && start.After(now)
}

type persistedState struct {
	PresetID string `json:"preset_id"`
}

func loadState(path string) (persistedState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is fixed by the add-on
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{}, nil
	}
	if err != nil {
		return persistedState{}, err
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return persistedState{}, err
	}
	return st, nil
}

func saveState(path string, st persistedState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}
