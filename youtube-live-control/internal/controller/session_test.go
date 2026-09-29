package controller

import (
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func broadcast(lifecycle string) *youtube.Broadcast {
	return &youtube.Broadcast{ID: "b1", Title: "Sunday Service", LifeCycleStatus: lifecycle, BoundStreamID: "s1"}
}

func TestComputeGates(t *testing.T) {
	active := youtube.StreamStatus{Status: youtube.StreamActive, Health: "good"}
	inactive := youtube.StreamStatus{Status: "inactive"}
	none := youtube.StreamStatus{}

	tests := []struct {
		name       string
		authorized bool
		b          *youtube.Broadcast
		stream     youtube.StreamStatus
		busy       bool
		want       gates
	}{
		{"unauthorized", false, broadcast(youtube.LifeReady), active, false, gates{}},
		{"busy", true, broadcast(youtube.LifeReady), active, true, gates{}},
		{"new stream selected", true, nil, none, false, gates{create: true}},
		{"ready with active stream", true, broadcast(youtube.LifeReady), active, false, gates{save: true, goLive: true}},
		{"ready without stream data", true, broadcast(youtube.LifeReady), inactive, false, gates{save: true}},
		{"created with active stream", true, broadcast(youtube.LifeCreated), active, false, gates{save: true, goLive: true}},
		{"live while stream still active", true, broadcast(youtube.LifeLive), active, false, gates{save: true}},
		{"live after stream stopped", true, broadcast(youtube.LifeLive), inactive, false, gates{save: true, end: true}},
		{"live with no stream report", true, broadcast(youtube.LifeLive), none, false, gates{save: true, end: true}},
		{"complete", true, broadcast(youtube.LifeComplete), none, false, gates{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeGates(tt.authorized, tt.b, tt.stream, tt.busy); got != tt.want {
				t.Fatalf("computeGates() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStatusTextWaitingForStreamToStop(t *testing.T) {
	lagging := youtube.StreamStatus{Status: youtube.StreamActive, Health: youtube.HealthNoData}
	if got := statusText(broadcast(youtube.LifeLive), lagging, pendingNone); got != "live (waiting for stream to stop)" {
		t.Fatalf("statusText() = %q", got)
	}
	healthy := youtube.StreamStatus{Status: youtube.StreamActive, Health: "good"}
	if got := statusText(broadcast(youtube.LifeLive), healthy, pendingNone); got != "live" {
		t.Fatalf("statusText() = %q", got)
	}
}

func TestStatusText(t *testing.T) {
	if got := statusText(nil, youtube.StreamStatus{}, pendingNone); got != "new (not created)" {
		t.Fatalf("statusText(nil) = %q", got)
	}
	if got := statusText(broadcast(youtube.LifeReady), youtube.StreamStatus{}, pendingGoLive); got != "starting" {
		t.Fatalf("statusText(pendingGoLive) = %q", got)
	}
	if got := statusText(broadcast(youtube.LifeLive), youtube.StreamStatus{}, pendingEnd); got != "ending" {
		t.Fatalf("statusText(pendingEnd) = %q", got)
	}
}

func TestHealthText(t *testing.T) {
	if got := healthText(nil, youtube.StreamStatus{}); got != "none" {
		t.Fatalf("healthText(nil) = %q", got)
	}
	unbound := &youtube.Broadcast{ID: "b1", LifeCycleStatus: youtube.LifeReady}
	if got := healthText(unbound, youtube.StreamStatus{}); got != "no stream bound" {
		t.Fatalf("healthText(unbound) = %q", got)
	}
	if got := healthText(broadcast(youtube.LifeReady), youtube.StreamStatus{}); got != "unknown" {
		t.Fatalf("healthText(no report) = %q", got)
	}
	if got := healthText(broadcast(youtube.LifeReady), youtube.StreamStatus{Status: "inactive"}); got != "inactive" {
		t.Fatalf("healthText(inactive) = %q", got)
	}
	if got := healthText(broadcast(youtube.LifeLive), youtube.StreamStatus{Status: youtube.StreamActive, Health: "good"}); got != "good" {
		t.Fatalf("healthText(active good) = %q", got)
	}
}

func TestBroadcastLabelsDeduplicate(t *testing.T) {
	start := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	labels := broadcastLabels([]youtube.Broadcast{
		{ID: "a", Title: "Service", ScheduledStart: start},
		{ID: "b", Title: "Service", ScheduledStart: start},
		{ID: "c", Title: "Untimed"},
	})
	if labels[0] == labels[1] {
		t.Fatalf("duplicate labels not disambiguated: %q", labels[0])
	}
	if labels[2] != "Untimed" {
		t.Fatalf("zero-time label = %q", labels[2])
	}
}

func TestSessionSelection(t *testing.T) {
	s := session{}
	start := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	s.setBroadcasts([]youtube.Broadcast{{ID: "a", Title: "Service", ScheduledStart: start, LifeCycleStatus: youtube.LifeReady}})

	if id, ok := s.idForLabel(NewStreamLabel); !ok || id != "" {
		t.Fatalf("idForLabel(new) = %q, %v", id, ok)
	}
	if _, ok := s.idForLabel("nonsense"); ok {
		t.Fatal("unknown label accepted")
	}
	id, ok := s.idForLabel(s.labels[0])
	if !ok || id != "a" {
		t.Fatalf("idForLabel = %q, %v", id, ok)
	}
	s.selectedID = id
	s.loadDrafts()
	if s.draftTitle != "Service" || !s.draftStart.Equal(start) || s.thumbnail != KeepCurrentLabel {
		t.Fatalf("drafts not seeded from selection: %+v", s)
	}
	if s.selectedLabel() != s.labels[0] {
		t.Fatalf("selectedLabel = %q", s.selectedLabel())
	}
	if s.selectOptions()[0] != NewStreamLabel {
		t.Fatal("New stream option missing or not first")
	}
}

func TestParseWhen(t *testing.T) {
	want := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	for _, raw := range []string{"2025-01-05 09:30", "2025-01-05T09:30", want.Format(time.RFC3339)} {
		got, err := parseWhen(raw)
		if err != nil || !got.Equal(want) {
			t.Fatalf("parseWhen(%q) = %v, %v", raw, got, err)
		}
	}
	if got, err := parseWhen(""); err != nil || !got.IsZero() {
		t.Fatalf("parseWhen(empty) = %v, %v", got, err)
	}
	if _, err := parseWhen("next sunday"); err == nil {
		t.Fatal("parseWhen accepted garbage")
	}
	if formatWhen(want) != "2025-01-05 09:30" {
		t.Fatalf("formatWhen = %q", formatWhen(want))
	}
	if formatWhen(time.Time{}) != "" {
		t.Fatal("formatWhen(zero) not empty")
	}
	roundTripped, err := parseWhen(formatWhen(want))
	if err != nil || !roundTripped.Equal(want) {
		t.Fatalf("round trip = %v, %v", roundTripped, err)
	}
}
