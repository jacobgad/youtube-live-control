package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
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
		t.Fatalf("seconds truncated, minutes kept: %v", got)
	}
	for _, raw := range []string{"", "2025-01-05T09:30:00", "Sun 5 Jan", "09:30"} {
		if _, err := parseStart(raw); err == nil {
			t.Fatalf("parseStart(%q) accepted", raw)
		}
	}
}

type nullConn struct{}

func (nullConn) Publish(context.Context, string, string, bool) error { return nil }
func (nullConn) Subscribe(context.Context, []string) error           { return nil }
func (nullConn) OnMessage(mqtt.MessageHandler)                       {}
func (nullConn) OnConnect(func())                                    {}
func (nullConn) Connected() bool                                     { return true }
func (nullConn) AwaitConnection(context.Context) error               { return nil }
func (nullConn) Close(context.Context) error                         { return nil }

func TestEnterStartRejectsInvalidPayload(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions, log: slog.Default(), pub: newPublisher(nullConn{}, mqtt.Origin{}, slog.Default())}
	c.lifetime, c.endLife = context.WithCancel(context.Background())
	defer c.endLife()
	c.enterStart("Sun 5 Jan")
	if !c.session.sched.start.IsZero() {
		t.Fatal("invalid payload must not set start")
	}
	c.enterStart("2025-01-05T09:30:00+10:00")
	if c.session.sched.start.IsZero() {
		t.Fatal("valid payload must set start")
	}
	c.inflight.Wait()
}

func TestLockIsExclusive(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions, log: slog.Default(), pub: newPublisher(nullConn{}, mqtt.Origin{}, slog.Default())}
	if !c.snapshot().unlocked {
		t.Fatal("inputs must start available")
	}
	if !c.lock() || c.lock() {
		t.Fatal("the lock must be exclusive")
	}
	if c.snapshot().unlocked {
		t.Fatal("a held lock must publish the inputs unavailable")
	}
	c.unlock()
	if !c.snapshot().unlocked || !c.lock() {
		t.Fatal("unlock must release")
	}
}

func TestEnqueueDropsWhileLockedAndReleasesAfterFailure(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions, log: slog.Default(), pub: newPublisher(nullConn{}, mqtt.Origin{}, slog.Default()), ops: make(chan queuedOp, commandBuffer), opsDone: make(chan struct{})}
	c.lifetime, c.endLife = context.WithCancel(context.Background())
	go c.opsLoop()

	started := make(chan struct{})
	finish := make(chan struct{})
	runs := 0
	c.enqueue("first", func(context.Context) error { runs++; close(started); <-finish; return errors.New("boom") })
	<-started
	c.enqueue("second", func(context.Context) error { runs++; return nil })
	if c.snapshot().unlocked {
		t.Fatal("inputs must be unavailable while the first command runs")
	}
	close(finish)
	deadline := time.Now().Add(2 * time.Second)
	for !c.snapshot().unlocked {
		if time.Now().After(deadline) {
			t.Fatal("lock not released after the failing command")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if runs != 1 {
		t.Fatalf("second press must be dropped while locked; ran %d", runs)
	}
	c.endLife()
	<-c.opsDone
	c.inflight.Wait()
}

func TestClearAfterSchedule(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local)
	sc := scheduling{}
	sc.setPresets([]preset.Preset{sundayPreset()})
	sc.presetID = "abc"
	sc.applyDefaults(now, nil)
	if sc.privacy != "public" {
		t.Fatalf("privacy should follow the preset, got %q", sc.privacy)
	}
	sc.clear()
	if sc.presetID != "" || !sc.start.IsZero() || sc.privacy != "" || sc.canSchedule(now) || sc.presetLabel() != noPresetLabel {
		t.Fatalf("not cleared: %+v", sc)
	}
}

func TestSelectSchedulePrivacySnapsBackOnInvalid(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions, log: slog.Default(), pub: newPublisher(nullConn{}, mqtt.Origin{}, slog.Default())}
	c.lifetime, c.endLife = context.WithCancel(context.Background())
	defer c.endLife()
	c.selectSchedulePrivacy("secret")
	if c.session.sched.privacy != "" {
		t.Fatal("invalid privacy must not be stored")
	}
	c.selectSchedulePrivacy("unlisted")
	if c.session.sched.privacy != "unlisted" {
		t.Fatal("valid privacy must be stored")
	}
	c.inflight.Wait()
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
