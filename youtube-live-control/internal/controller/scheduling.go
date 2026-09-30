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

// scheduling is the state behind the YouTube Live Scheduling device. The start is
// recomputed from the preset (never persisted) so a stale date cannot linger.
type scheduling struct {
	presets  []preset.Preset
	labels   []string
	presetID string
	start    time.Time
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
		sc.start = time.Time{}
		return
	}
	sc.start = nextFreeSlot(*p, now, taken)
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

// startPayload is what the datetime entity expects: ISO 8601 with an offset, or
// Home Assistant's null payload while unset.
func (sc *scheduling) startPayload() string {
	if sc.start.IsZero() {
		return mqttNone
	}
	return sc.start.Format(time.RFC3339)
}

// parseStart accepts the datetime entity's command payload (isoformat of an aware
// datetime, with or without fractional seconds).
func parseStart(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999-07:00", "2006-01-02T15:04-07:00"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Truncate(time.Minute), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO 8601 date-time with a timezone", raw)
}

func (sc *scheduling) canSchedule(now time.Time) bool {
	p := sc.preset()
	if p == nil || p.StreamID == "" {
		return false
	}
	return !sc.start.IsZero() && sc.start.After(now)
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
