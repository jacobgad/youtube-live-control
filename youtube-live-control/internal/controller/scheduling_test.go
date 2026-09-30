package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/preset"
)

func sundayPreset() preset.Preset {
	return preset.Preset{ID: "abc", Name: "Sunday", TitleTemplate: "Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30"}
}

func TestPresetDefaultsSkipTakenSlots(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local) // Wednesday
	sc := scheduling{}
	sc.setPresets([]preset.Preset{sundayPreset()})
	sc.presetID = "abc"

	sc.applyDefaults(now, nil)
	if sc.start.Day() != 5 || sc.start.Hour() != 9 || sc.start.Minute() != 30 {
		t.Fatalf("default start = %v", sc.start)
	}
	firstSunday := sc.start
	sc.applyDefaults(now, []time.Time{firstSunday})
	if sc.start.Day() != 12 {
		t.Fatalf("taken slot not skipped: %v", sc.start)
	}
	if !sc.canSchedule(now) {
		t.Fatal("a preset with a stream key and a future slot should be schedulable")
	}
	if sc.startPayload() != sc.start.Format(time.RFC3339) {
		t.Fatalf("payload = %q", sc.startPayload())
	}
}

func TestScheduleGate(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	if sc.canSchedule(now) || sc.startPayload() != mqttNone {
		t.Fatal("no preset must gate off and publish None")
	}
	noKey := sundayPreset()
	noKey.StreamID = ""
	sc.setPresets([]preset.Preset{noKey})
	sc.presetID = "abc"
	sc.applyDefaults(now, nil)
	if sc.canSchedule(now) {
		t.Fatal("a preset without a stream key must gate off")
	}
	sc.setPresets([]preset.Preset{sundayPreset()})
	sc.start = now.Add(-time.Hour)
	if sc.canSchedule(now) {
		t.Fatal("a past slot must gate off")
	}
}

func TestParseStart(t *testing.T) {
	for _, raw := range []string{"2025-01-05T09:30:00+10:00", "2025-01-05T09:30:00.123456+10:00", "2025-01-04T23:30:00Z"} {
		if _, err := parseStart(raw); err != nil {
			t.Fatalf("parseStart(%q): %v", raw, err)
		}
	}
	got, _ := parseStart("2025-01-05T09:47:12+10:00")
	if got.Second() != 0 || got.Minute() != 47 {
		t.Fatalf("seconds should be truncated, minutes kept: %v", got)
	}
	for _, raw := range []string{"", "2025-01-05T09:30:00", "Sun 5 Jan", "09:30"} {
		if _, err := parseStart(raw); err == nil {
			t.Fatalf("parseStart(%q) accepted", raw)
		}
	}
}

func TestPresetListChangesKeepOrDropSelection(t *testing.T) {
	sc := scheduling{}
	sc.setPresets([]preset.Preset{sundayPreset(), {ID: "def", Name: "Sunday"}})
	if sc.labels[1] != "Sunday (2)" {
		t.Fatalf("duplicate names not disambiguated: %v", sc.labels)
	}
	sc.presetID = "def"
	if id, ok := sc.presetIDForLabel("Sunday (2)"); !ok || id != "def" {
		t.Fatalf("label lookup = %q %v", id, ok)
	}
	sc.setPresets([]preset.Preset{sundayPreset()})
	if sc.presetID != "" || sc.presetLabel() != noPresetLabel {
		t.Fatalf("deleted preset should clear the selection: %q", sc.presetID)
	}
	sc.setPresets(nil)
	if opts := sc.presetOptions(); len(opts) != 1 || opts[0] != noPresetLabel {
		t.Fatalf("empty options = %v", opts)
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if st, err := loadState(path); err != nil || st.PresetID != "" {
		t.Fatalf("missing file: %+v %v", st, err)
	}
	if err := saveState(path, persistedState{PresetID: "abc"}); err != nil {
		t.Fatal(err)
	}
	if st, err := loadState(path); err != nil || st.PresetID != "abc" {
		t.Fatalf("round trip: %+v %v", st, err)
	}
}
