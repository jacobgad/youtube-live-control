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

const (
	dateLabelFormat = "Mon 2 Jan"
	dateWindowDays  = 28
	noPresetLabel   = "No presets — create one in the web UI"
)

// scheduling is the state behind the YouTube Live Scheduling device. Date and time
// are recomputed from the preset (never persisted) so a stale date cannot linger.
type scheduling struct {
	presets   []preset.Preset
	labels    []string
	presetID  string
	date      time.Time
	timeOfDay string
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
		sc.date, sc.timeOfDay = time.Time{}, ""
		return
	}
	start := nextFreeSlot(*p, now, taken)
	sc.date = dayOf(start)
	sc.timeOfDay = start.Format("15:04")
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

func dayOf(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// The chosen date is appended when the free-slot search lands beyond the window.
func (sc *scheduling) dateOptions(now time.Time) []string {
	today := dayOf(now)
	options := make([]string, 0, dateWindowDays+1)
	included := false
	for i := range dateWindowDays {
		day := today.AddDate(0, 0, i)
		if day.Equal(sc.date) {
			included = true
		}
		options = append(options, day.Format(dateLabelFormat))
	}
	if !sc.date.IsZero() && !included && sc.date.After(today) {
		options = append(options, sc.date.Format(dateLabelFormat))
	}
	return options
}

func (sc *scheduling) dateLabel() string {
	if sc.date.IsZero() {
		return ""
	}
	return sc.date.Format(dateLabelFormat)
}

// Labels carry no year, so a label is resolved by scanning forward from today.
func (sc *scheduling) parseDateLabel(label string, now time.Time) (time.Time, bool) {
	today := dayOf(now)
	for i := range dateWindowDays + 366 {
		day := today.AddDate(0, 0, i)
		if day.Format(dateLabelFormat) == label {
			return day, true
		}
	}
	return time.Time{}, false
}

func timeOptions() []string {
	options := make([]string, 0, int((preset.LastSlot-preset.FirstSlot)/preset.SlotStep)+1)
	for d := preset.FirstSlot; d <= preset.LastSlot; d += preset.SlotStep {
		options = append(options, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Add(d).Format("15:04"))
	}
	return options
}

func (sc *scheduling) start() time.Time {
	if sc.date.IsZero() || sc.timeOfDay == "" {
		return time.Time{}
	}
	tod, err := time.Parse("15:04", sc.timeOfDay)
	if err != nil {
		return time.Time{}
	}
	return time.Date(sc.date.Year(), sc.date.Month(), sc.date.Day(), tod.Hour(), tod.Minute(), 0, 0, sc.date.Location())
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
