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
	if sc.date.Day() != 5 || sc.timeOfDay != "09:30" {
		t.Fatalf("defaults = %v %s", sc.date, sc.timeOfDay)
	}
	firstSunday := sc.start()
	sc.applyDefaults(now, []time.Time{firstSunday})
	if sc.date.Day() != 12 {
		t.Fatalf("taken slot not skipped: %v", sc.date)
	}
	if !sc.canSchedule(now) {
		t.Fatal("a preset with a stream key and a future slot should be schedulable")
	}
	if sc.start().Hour() != 9 || sc.start().Minute() != 30 {
		t.Fatalf("start = %v", sc.start())
	}
}

func TestScheduleGate(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	if sc.canSchedule(now) {
		t.Fatal("no preset must gate off")
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
	sc.date = dayOf(now.AddDate(0, 0, -1))
	sc.timeOfDay = "09:30"
	if sc.canSchedule(now) {
		t.Fatal("a past slot must gate off")
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

func TestDateAndTimeOptions(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	opts := sc.dateOptions(now)
	if len(opts) != dateWindowDays || opts[0] != "Wed 1 Jan" || opts[4] != "Sun 5 Jan" {
		t.Fatalf("date options = %v", opts)
	}
	sc.date = dayOf(now.AddDate(0, 0, 40))
	if opts := sc.dateOptions(now); len(opts) != dateWindowDays+1 || opts[dateWindowDays] != sc.date.Format(dateLabelFormat) {
		t.Fatal("a chosen date beyond the window must be appended")
	}
	day, ok := sc.parseDateLabel("Sun 12 Jan", now)
	if !ok || day.Day() != 12 || day.Month() != time.January {
		t.Fatalf("parseDateLabel = %v %v", day, ok)
	}
	if _, ok := sc.parseDateLabel("Someday", now); ok {
		t.Fatal("garbage label accepted")
	}

	times := timeOptions()
	if times[0] != "06:00" || times[len(times)-1] != "23:30" || len(times) != 36 {
		t.Fatalf("time options = %v", times)
	}
	for label, want := range map[string]bool{"09:30": true, "09:15": false, "05:30": false, "23:30": true, "x": false} {
		if preset.ValidSlot(label) != want {
			t.Fatalf("ValidSlot(%q) = %v", label, !want)
		}
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
