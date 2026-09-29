package controller

import (
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

var testOptions = config.Options{
	FastPollInterval: 3 * time.Second,
	FastModeDuration: 5 * time.Minute,
	LivePollInterval: time.Minute,
	IdlePollInterval: 10 * time.Minute,
}

func TestFastWindow(t *testing.T) {
	now := time.Now()
	s := session{}
	if s.fastActive(now) || s.fastRemainingMinutes(now) != 0 {
		t.Fatal("fast window should start off")
	}
	s.armFast(now, testOptions.FastModeDuration)
	if !s.fastActive(now) {
		t.Fatal("armFast did not arm")
	}
	if got := s.fastRemainingMinutes(now); got != 5 {
		t.Fatalf("remaining at arm = %d", got)
	}
	if got := s.fastRemainingMinutes(now.Add(4*time.Minute + 30*time.Second)); got != 1 {
		t.Fatalf("remaining near expiry = %d", got)
	}
	expiry := now.Add(testOptions.FastModeDuration)
	if s.fastActive(expiry) || s.fastRemainingMinutes(expiry) != 0 {
		t.Fatal("window should be over exactly at expiry")
	}
}

func TestStatusDelayTiers(t *testing.T) {
	c := &Controller{now: time.Now, opts: testOptions}

	if got := c.statusDelay(); got != testOptions.IdlePollInterval {
		t.Fatalf("idle tier = %v", got)
	}

	c.session.setBroadcasts([]youtube.Broadcast{{ID: "b1", Title: "Service", LifeCycleStatus: youtube.LifeLive}}, time.Now())
	c.session.selectedID = "b1"
	if got := c.statusDelay(); got != testOptions.LivePollInterval {
		t.Fatalf("live tier = %v", got)
	}

	c.session.armFast(time.Now(), testOptions.FastModeDuration)
	if got := c.statusDelay(); got != testOptions.FastPollInterval {
		t.Fatalf("fast tier = %v", got)
	}
}
