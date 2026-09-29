package controller

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func TestLoadTunablesMissingFileUsesDefaults(t *testing.T) {
	got := loadTunables(filepath.Join(t.TempDir(), "settings.json"), slog.Default())
	if got != defaultTunables {
		t.Fatalf("loadTunables = %+v, want defaults %+v", got, defaultTunables)
	}
}

func TestTunablesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	saved := defaultTunables
	saved.FastPollSeconds = 10
	saved.IdlePollMinutes = 30
	if err := saved.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadTunables(path, slog.Default()); got != saved {
		t.Fatalf("round trip = %+v, want %+v", got, saved)
	}
}

func TestLoadTunablesClampsOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"fast_poll_seconds":0,"idle_poll_minutes":9999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadTunables(path, slog.Default())
	if got.FastPollSeconds != 1 || got.IdlePollMinutes != 60 {
		t.Fatalf("clamped = %+v", got)
	}
	if got.ListPollMinutes != defaultTunables.ListPollMinutes {
		t.Fatalf("missing keys should keep defaults: %+v", got)
	}
}

func TestTunableFieldsCoverEverySpec(t *testing.T) {
	tun := defaultTunables
	for object := range tunableFields {
		if _, ok := tunableSpec(object); !ok {
			t.Fatalf("field %s has no spec", object)
		}
		if *tunableFields[object](&tun) == 0 {
			t.Fatalf("default for %s is zero", object)
		}
	}
	if len(tunableFields) != 5 {
		t.Fatalf("tunable count = %d", len(tunableFields))
	}
}

func TestNextQuarterHour(t *testing.T) {
	day := func(h, m, s int) time.Time { return time.Date(2025, 1, 5, h, m, s, 0, time.Local) }
	cases := map[time.Time]time.Time{
		day(15, 31, 0): day(15, 45, 0),
		day(15, 45, 0): day(15, 45, 0),
		day(15, 45, 1): day(16, 0, 0),
		day(15, 0, 30): day(15, 15, 0),
		day(23, 50, 0): day(24, 0, 0),
	}
	for in, want := range cases {
		if got := nextQuarterHour(in); !got.Equal(want) {
			t.Fatalf("nextQuarterHour(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestParseIntPayload(t *testing.T) {
	for raw, want := range map[string]int{"5": 5, " 5 ": 5, "5.0": 5, "0": 0} {
		got, ok := parseIntPayload(raw)
		if !ok || got != want {
			t.Fatalf("parseIntPayload(%q) = %d, %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"", "x", "5.5"} {
		if _, ok := parseIntPayload(raw); ok {
			t.Fatalf("parseIntPayload(%q) accepted", raw)
		}
	}
}

func TestFastWindow(t *testing.T) {
	now := time.Now()
	s := session{tun: defaultTunables}
	if s.fastActive(now) || s.fastRemainingMinutes(now) != 0 {
		t.Fatal("fast window should start off")
	}
	s.armFast(now)
	if !s.fastActive(now) {
		t.Fatal("armFast did not arm")
	}
	if got := s.fastRemainingMinutes(now); got != defaultTunables.FastModeMinutes {
		t.Fatalf("remaining at arm = %d", got)
	}
	if got := s.fastRemainingMinutes(now.Add(4*time.Minute + 30*time.Second)); got != 1 {
		t.Fatalf("remaining near expiry = %d", got)
	}
	expiry := now.Add(s.tun.fastWindow())
	if s.fastActive(expiry) || s.fastRemainingMinutes(expiry) != 0 {
		t.Fatal("window should be over exactly at expiry")
	}
}

func TestStatusDelayTiers(t *testing.T) {
	c := &Controller{now: time.Now, opts: config.Options{}}
	c.s.tun = defaultTunables

	if got := c.statusDelay(); got != defaultTunables.idlePoll() {
		t.Fatalf("idle tier = %v", got)
	}

	c.s.setBroadcasts([]youtube.Broadcast{{ID: "b1", Title: "Service", LifeCycleStatus: youtube.LifeLive}})
	c.s.selectedID = "b1"
	if got := c.statusDelay(); got != defaultTunables.livePoll() {
		t.Fatalf("live tier = %v", got)
	}

	c.s.armFast(time.Now())
	if got := c.statusDelay(); got != defaultTunables.fastPoll() {
		t.Fatalf("fast tier = %v", got)
	}
}
