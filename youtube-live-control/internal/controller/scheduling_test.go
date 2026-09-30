package controller

import (
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
	if start := sc.start(); start.Day() != 5 || start.Hour() != 9 || start.Minute() != 30 {
		t.Fatalf("default start = %v", start)
	}
	firstSunday := sc.start()
	sc.applyDefaults(now, []time.Time{firstSunday})
	if sc.start().Day() != 12 {
		t.Fatalf("taken slot not skipped: %v", sc.start())
	}
	if !sc.canSchedule(now) {
		t.Fatal("a preset with a stream key and a future slot should be schedulable")
	}
	if sc.datePayload() != "2025-01-12" || sc.timePayload() != "09:30:00" {
		t.Fatalf("payloads = %q %q", sc.datePayload(), sc.timePayload())
	}
}

func TestScheduleGate(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	if sc.canSchedule(now) || sc.datePayload() != mqttNone || sc.timePayload() != mqttNone {
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
	sc.date = dayOf(now)
	sc.timeOfDay, sc.timeSet = 8*time.Hour, true
	if sc.canSchedule(now) {
		t.Fatal("a past slot must gate off")
	}
	sc.timeSet = false
	if sc.canSchedule(now) || !sc.start().IsZero() {
		t.Fatal("an unset time must gate off")
	}
}

func TestParseDateAndTime(t *testing.T) {
	day, err := parseDate("2025-01-05")
	if err != nil || day.Day() != 5 || day.Hour() != 0 {
		t.Fatalf("parseDate = %v %v", day, err)
	}
	for _, raw := range []string{"09:30:00", "09:30:00.123456", "09:30"} {
		tod, err := parseTimeOfDay(raw)
		if err != nil || tod != 9*time.Hour+30*time.Minute {
			t.Fatalf("parseTimeOfDay(%q) = %v %v", raw, tod, err)
		}
	}
	for _, raw := range []string{"", "Sun 5 Jan", "5/1/2025"} {
		if _, err := parseDate(raw); err == nil {
			t.Fatalf("parseDate(%q) accepted", raw)
		}
	}
	if _, err := parseTimeOfDay("25:00"); err == nil {
		t.Fatal("parseTimeOfDay accepted 25:00")
	}
}

func TestClearAfterSchedule(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	sc.setPresets([]preset.Preset{sundayPreset()})
	sc.presetID = "abc"
	sc.applyDefaults(now, nil)
	sc.clear()
	if sc.presetID != "" || !sc.start().IsZero() || sc.canSchedule(now) || sc.presetLabel() != noPresetLabel {
		t.Fatalf("not cleared: %+v", sc)
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
