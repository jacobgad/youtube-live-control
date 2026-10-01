package controller

import (
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

var testOptions = config.Options{
	FastRefresh:         3 * time.Second,
	FastRefreshDuration: 5 * time.Minute,
	LiveRefresh:         time.Minute,
	IdleRefresh:         10 * time.Minute,
}

func TestFastWindow(t *testing.T) {
	now := time.Now()
	s := session{}
	if s.fastActive(now) || s.fastRemainingMinutes(now) != 0 {
		t.Fatal("fast window should start off")
	}
	s.armFast(now, testOptions.FastRefreshDuration)
	if !s.fastActive(now) {
		t.Fatal("armFast did not arm")
	}
	if got := s.fastRemainingMinutes(now); got != 5 {
		t.Fatalf("remaining at arm = %d", got)
	}
	if got := s.fastRemainingMinutes(now.Add(4*time.Minute + 30*time.Second)); got != 1 {
		t.Fatalf("remaining near expiry = %d", got)
	}
	expiry := now.Add(testOptions.FastRefreshDuration)
	if s.fastActive(expiry) || s.fastRemainingMinutes(expiry) != 0 {
		t.Fatal("window should be over exactly at expiry")
	}
}

func TestPollDelayTiers(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions}

	if got := c.pollDelay(); got != testOptions.IdleRefresh {
		t.Fatalf("idle tier = %v", got)
	}

	now := time.Now()
	liveB := youtube.Broadcast{ID: "b1", Title: "Service", BoundStreamID: "s1", LifeCycleStatus: youtube.LifeLive}
	c.session.setAll([]youtube.Stream{{ID: "s1", Title: "Main"}}, []youtube.Broadcast{liveB}, now)
	if got := c.pollDelay(); got != testOptions.IdleRefresh {
		t.Fatalf("an unselected live broadcast stays on the idle tier, got %v", got)
	}
	c.session.device(mqtt.DeviceID("s1")).selectedID = "b1"
	if got := c.pollDelay(); got != testOptions.LiveRefresh {
		t.Fatalf("live tier = %v", got)
	}

	c.session.armFast(time.Now(), testOptions.FastRefreshDuration)
	if got := c.pollDelay(); got != testOptions.FastRefresh {
		t.Fatalf("fast tier = %v", got)
	}
}

func TestDifference(t *testing.T) {
	if got := difference([]string{"a", "b", "c"}, []string{"b"}); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("difference = %v", got)
	}
	if got := difference(nil, []string{"a"}); got != nil {
		t.Fatalf("difference = %v", got)
	}
}
