package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func broadcast(lifecycle string) *youtube.Broadcast {
	return &youtube.Broadcast{ID: "b1", Title: "Sunday Service", LifeCycleStatus: lifecycle, BoundStreamID: "s1"}
}

func TestStage(t *testing.T) {
	active := youtube.StreamStatus{Status: youtube.StreamActive, Health: "good"}
	noData := youtube.StreamStatus{Status: youtube.StreamActive, Health: "noData"}
	inactive := youtube.StreamStatus{Status: "inactive"}
	none := youtube.StreamStatus{}
	unbound := &youtube.Broadcast{ID: "b1", LifeCycleStatus: youtube.LifeReady}

	cases := []struct {
		name    string
		b       *youtube.Broadcast
		stream  youtube.StreamStatus
		pending pendingOp
		want    string
	}{
		{"nothing selected", nil, none, pendingNone, stageNoBroadcast},
		{"no stream key", unbound, none, pendingNone, stageNoStreamKey},
		{"ready, encoder off", broadcast(youtube.LifeReady), inactive, pendingNone, stageWaitingForEncoder},
		{"ready, no report yet", broadcast(youtube.LifeReady), none, pendingNone, stageWaitingForEncoder},
		{"ready, encoder on", broadcast(youtube.LifeReady), active, pendingNone, stageReadyToGoLive},
		{"created, encoder on", broadcast(youtube.LifeCreated), active, pendingNone, stageReadyToGoLive},
		{"go live pressed", broadcast(youtube.LifeReady), active, pendingGoLive, stageStarting},
		{"liveStarting", broadcast(youtube.LifeLiveStarting), active, pendingNone, stageStarting},
		{"live streaming", broadcast(youtube.LifeLive), active, pendingNone, stageLive},
		{"live, encoder gone, youtube lagging", broadcast(youtube.LifeLive), noData, pendingNone, stageStreamStopping},
		{"live, stream stopped", broadcast(youtube.LifeLive), inactive, pendingNone, stageReadyToEnd},
		{"end pressed", broadcast(youtube.LifeLive), inactive, pendingEnd, stageEnding},
		{"complete", broadcast(youtube.LifeComplete), none, pendingNone, stageEnded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stage(tc.b, tc.stream, tc.pending); got != tc.want {
				t.Fatalf("stage() = %q, want %q", got, tc.want)
			}
		})
	}
	for _, s := range []string{stageNoBroadcast, stageNoStreamKey, stageWaitingForEncoder, stageReadyToGoLive, stageStarting, stageLive, stageStreamStopping, stageReadyToEnd, stageEnding, stageEnded} {
		found := false
		for _, o := range mqtt.StageOptions {
			if o == s {
				found = true
			}
		}
		if !found {
			t.Fatalf("stage %q missing from mqtt.StageOptions", s)
		}
	}
}

func TestGatesFollowStage(t *testing.T) {
	if g := computeGates(true, stageReadyToGoLive); !g.goLive || g.end {
		t.Fatalf("ready_to_go_live gates = %+v", g)
	}
	if g := computeGates(true, stageReadyToEnd); g.goLive || !g.end {
		t.Fatalf("ready_to_end gates = %+v", g)
	}
	for _, s := range []string{stageLive, stageStreamStopping, stageStarting, stageEnding, stageWaitingForEncoder, stageNoStreamKey, stageNoBroadcast, stageEnded} {
		if g := computeGates(true, s); g.goLive || g.end {
			t.Fatalf("%s should gate both buttons off: %+v", s, g)
		}
	}
	if g := computeGates(false, stageReadyToGoLive); g.goLive {
		t.Fatal("unauthorized must gate off")
	}
	for _, s := range []string{stageLive, stageStreamStopping, stageReadyToEnd} {
		if !isOnAir(s) {
			t.Fatalf("%s should count as on air", s)
		}
	}
	if isOnAir(stageStarting) || isOnAir(stageEnded) {
		t.Fatal("starting/ended are not on air")
	}
}

func TestCanDelete(t *testing.T) {
	for _, s := range []string{stageWaitingForEncoder, stageReadyToGoLive, stageNoStreamKey, stageEnded} {
		if !canDelete(true, s) {
			t.Fatalf("%s should allow delete", s)
		}
	}
	for _, s := range []string{stageLive, stageStreamStopping, stageReadyToEnd, stageStarting, stageEnding, stageNoBroadcast} {
		if canDelete(true, s) {
			t.Fatalf("%s must refuse delete", s)
		}
	}
	if canDelete(false, stageReadyToGoLive) {
		t.Fatal("unauthorized must refuse delete")
	}
}

func TestSetBroadcastsNeverSelectsAndHidesStale(t *testing.T) {
	now := time.Date(2025, 1, 4, 12, 0, 0, 0, time.Local)
	next := youtube.Broadcast{ID: "next", Title: "This Sunday", ScheduledStart: now.Add(21 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	later := youtube.Broadcast{ID: "later", Title: "Next Sunday", ScheduledStart: now.Add(8 * 24 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	stale := youtube.Broadcast{ID: "old", Title: "Never started", ScheduledStart: now.Add(-3 * 24 * time.Hour), LifeCycleStatus: youtube.LifeReady}

	s := session{}
	if lost := s.setBroadcasts([]youtube.Broadcast{next, later, stale}, now); lost || s.selectedID != "" {
		t.Fatalf("selection must stay empty until a human picks; got %q", s.selectedID)
	}
	if len(s.broadcasts) != 2 || len(s.stale) != 1 || s.stale[0].ID != "old" {
		t.Fatalf("visible %d stale %d", len(s.broadcasts), len(s.stale))
	}
	if s.selectedLabel() != noBroadcastLabel || s.selectOptions()[0] != s.labels[0] {
		t.Fatalf("label %q options %v", s.selectedLabel(), s.selectOptions())
	}

	s.selectedID = "next"
	if lost := s.setBroadcasts([]youtube.Broadcast{next, later}, now); lost || s.selectedID != "next" {
		t.Fatal("selection must survive a refresh that still lists it")
	}
	if lost := s.setBroadcasts([]youtube.Broadcast{later}, now); !lost || s.selectedID != "" {
		t.Fatalf("selection must clear when the broadcast vanishes; got %q", s.selectedID)
	}
	if lost := s.setBroadcasts(nil, now); lost {
		t.Fatal("clearing an already-empty selection is not a loss")
	}
	if opts := s.selectOptions(); len(opts) != 1 || opts[0] != noBroadcastLabel {
		t.Fatalf("empty options = %v", opts)
	}
	if len(s.allStarts()) != 0 {
		t.Fatal("allStarts should be empty")
	}
}

func TestStaleKeepsLiveBroadcast(t *testing.T) {
	now := time.Now()
	b := youtube.Broadcast{ID: "x", ScheduledStart: now.Add(-48 * time.Hour), LifeCycleStatus: youtube.LifeLive}
	if isStale(b, now) {
		t.Fatal("a live broadcast is never stale, however old its schedule")
	}
	b.LifeCycleStatus = youtube.LifeReady
	if !isStale(b, now) {
		t.Fatal("a ready broadcast two days past its start is stale")
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
	s := session{}
	s.setBroadcasts([]youtube.Broadcast{{ID: "a", Title: "Service", ScheduledStart: start}}, start.Add(-time.Hour))
	if id, ok := s.idForLabel(s.labels[0]); !ok || id != "a" {
		t.Fatalf("idForLabel = %q, %v", id, ok)
	}
	if _, ok := s.idForLabel("nonsense"); ok {
		t.Fatal("unknown label accepted")
	}
}

func TestBroadcastAttributes(t *testing.T) {
	if broadcastAttributes(nil) != "{}" {
		t.Fatal("nil should render an empty object")
	}
	b := broadcast(youtube.LifeReady)
	b.ScheduledStart = time.Date(2025, 1, 5, 9, 30, 0, 0, time.UTC)
	b.ThumbnailURL = "https://i.ytimg.com/x.jpg"
	var attrs map[string]any
	if err := json.Unmarshal([]byte(broadcastAttributes(b)), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["watch_url"] != "https://www.youtube.com/watch?v=b1" || attrs["thumbnail_url"] != b.ThumbnailURL || attrs["scheduled_start"] != "2025-01-05T09:30:00Z" {
		t.Fatalf("attributes = %v", attrs)
	}
	if _, has := attrs["description"]; has {
		t.Fatal("description must stay out of attributes")
	}
	if _, has := attrs["entity_picture"]; has {
		t.Fatal("entity_picture is blocked by Home Assistant's MQTT integration; the image entity carries it")
	}
}

func TestBroadcastOrderLiveFirstThenSoonest(t *testing.T) {
	now := time.Now()
	later := youtube.Broadcast{ID: "later", Title: "B", ScheduledStart: now.Add(48 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	soon := youtube.Broadcast{ID: "soon", Title: "A", ScheduledStart: now.Add(2 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	live := youtube.Broadcast{ID: "live", Title: "C", ScheduledStart: now.Add(72 * time.Hour), LifeCycleStatus: youtube.LifeLive}
	untimed := youtube.Broadcast{ID: "untimed", Title: "D", LifeCycleStatus: youtube.LifeReady}

	s := session{}
	s.setBroadcasts([]youtube.Broadcast{later, soon}, now)
	s.apply(untimed)
	s.apply(live)
	got := []string{s.broadcasts[0].ID, s.broadcasts[1].ID, s.broadcasts[2].ID, s.broadcasts[3].ID}
	want := []string{"live", "soon", "later", "untimed"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if s.selectOptions()[0] != s.labels[0] {
		t.Fatal("first option must be the first broadcast")
	}
}
