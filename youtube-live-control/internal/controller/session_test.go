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

func mainKey() youtube.Stream  { return youtube.Stream{ID: "s1", Title: "Main Auditorium"} }
func youthKey() youtube.Stream { return youtube.Stream{ID: "s2", Title: "Youth Hall"} }

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
		t.Fatalf("stageReadyToGoLive gates = %+v", g)
	}
	if g := computeGates(true, stageReadyToEnd); g.goLive || !g.end {
		t.Fatalf("stageReadyToEnd gates = %+v", g)
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

func TestSetAllPartitionsByBindingAndNeverSelects(t *testing.T) {
	now := time.Date(2025, 1, 4, 12, 0, 0, 0, time.Local)
	onMain := youtube.Broadcast{ID: "next", Title: "This Sunday", BoundStreamID: "s1", ScheduledStart: now.Add(21 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	onYouth := youtube.Broadcast{ID: "youth", Title: "Youth Night", BoundStreamID: "s2", ScheduledStart: now.Add(30 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	unbound := youtube.Broadcast{ID: "loose", Title: "No key yet", ScheduledStart: now.Add(48 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	stale := youtube.Broadcast{ID: "old", Title: "Never started", BoundStreamID: "s1", ScheduledStart: now.Add(-3 * 24 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	defaultKey := youtube.Stream{ID: "sd", Title: "Default stream key", IsDefault: true}

	s := session{}
	lost := s.setAll([]youtube.Stream{youthKey(), mainKey(), defaultKey}, []youtube.Broadcast{onMain, onYouth, unbound, stale}, now)
	if len(lost) != 0 {
		t.Fatalf("nothing was selected, nothing can be lost: %v", lost)
	}
	if len(s.devices) != 2 {
		t.Fatalf("default keys must not become devices: %d", len(s.devices))
	}
	if s.devices[0].stream.ID != "s1" || s.devices[1].stream.ID != "s2" {
		t.Fatalf("devices must sort by key title: %s, %s", s.devices[0].stream.ID, s.devices[1].stream.ID)
	}
	main := s.device(mqtt.DeviceID("s1"))
	if main == nil || len(main.broadcasts) != 1 || main.broadcasts[0].ID != "next" {
		t.Fatalf("main device broadcasts = %+v", main)
	}
	if main.selectedID != "" || main.selectedLabel() != mqttNone {
		t.Fatal("selection must stay empty until a human picks")
	}
	if len(s.unbound) != 1 || s.unbound[0].ID != "loose" {
		t.Fatalf("unbound = %+v", s.unbound)
	}
	if len(s.stale) != 1 || s.stale[0].ID != "old" {
		t.Fatalf("stale = %+v", s.stale)
	}
	if len(s.allBroadcasts()) != 3 {
		t.Fatalf("allBroadcasts = %+v", s.allBroadcasts())
	}
	if len(s.allStarts()) != 4 {
		t.Fatalf("allStarts should include stale and unbound: %d", len(s.allStarts()))
	}
}

func TestSetAllKeepsOrLosesSelectionPerDevice(t *testing.T) {
	now := time.Now()
	onMain := youtube.Broadcast{ID: "b1", Title: "Service", BoundStreamID: "s1", ScheduledStart: now.Add(2 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	onYouth := youtube.Broadcast{ID: "b2", Title: "Youth", BoundStreamID: "s2", ScheduledStart: now.Add(3 * time.Hour), LifeCycleStatus: youtube.LifeReady}

	s := session{}
	s.setAll([]youtube.Stream{mainKey(), youthKey()}, []youtube.Broadcast{onMain, onYouth}, now)
	s.device(mqtt.DeviceID("s1")).selectedID = "b1"
	s.device(mqtt.DeviceID("s2")).selectedID = "b2"

	if lost := s.setAll([]youtube.Stream{mainKey(), youthKey()}, []youtube.Broadcast{onMain, onYouth}, now); len(lost) != 0 {
		t.Fatalf("selections must survive a refresh that still lists them: %v", lost)
	}
	lost := s.setAll([]youtube.Stream{mainKey(), youthKey()}, []youtube.Broadcast{onYouth}, now)
	if len(lost) != 1 || lost[0] != mqtt.DeviceID("s1") {
		t.Fatalf("main's selection must clear when its broadcast vanishes: %v", lost)
	}
	if s.device(mqtt.DeviceID("s2")).selectedID != "b2" {
		t.Fatal("youth's selection must survive main's loss")
	}
	if opts := s.device(mqtt.DeviceID("s1")).selectOptions(); len(opts) != 1 || opts[0] != noBroadcastLabel {
		t.Fatalf("empty options = %v", opts)
	}
}

func TestSetAllRetiresVanishedKeysButKeepsSelectionOnSurvivors(t *testing.T) {
	now := time.Now()
	onMain := youtube.Broadcast{ID: "b1", Title: "Service", BoundStreamID: "s1", ScheduledStart: now.Add(2 * time.Hour), LifeCycleStatus: youtube.LifeReady}

	s := session{}
	s.setAll([]youtube.Stream{mainKey(), youthKey()}, []youtube.Broadcast{onMain}, now)
	s.device(mqtt.DeviceID("s1")).selectedID = "b1"
	s.setAll([]youtube.Stream{mainKey()}, []youtube.Broadcast{onMain}, now)
	if len(s.devices) != 1 {
		t.Fatalf("devices = %d", len(s.devices))
	}
	if s.device(mqtt.DeviceID("s1")).selectedID != "b1" {
		t.Fatal("selection must survive another key's removal")
	}
}

func TestApplyMovesBroadcastBetweenDevices(t *testing.T) {
	now := time.Now()
	b := youtube.Broadcast{ID: "b1", Title: "Service", BoundStreamID: "s1", ScheduledStart: now.Add(2 * time.Hour), LifeCycleStatus: youtube.LifeReady}

	s := session{}
	s.setAll([]youtube.Stream{mainKey(), youthKey()}, []youtube.Broadcast{b}, now)
	s.device(mqtt.DeviceID("s1")).selectedID = "b1"

	b.Title = "Renamed"
	s.apply(b)
	main := s.device(mqtt.DeviceID("s1"))
	if main.broadcasts[0].Title != "Renamed" || main.selectedID != "b1" {
		t.Fatal("an in-place update must keep the selection")
	}

	b.BoundStreamID = "s2"
	s.apply(b)
	if len(main.broadcasts) != 0 || main.selectedID != "" {
		t.Fatalf("rebinding away must clear the old device: %+v", main)
	}
	youth := s.device(mqtt.DeviceID("s2"))
	if len(youth.broadcasts) != 1 || youth.selectedID != "" {
		t.Fatalf("the new device gains the broadcast unselected: %+v", youth)
	}

	b.BoundStreamID = ""
	s.apply(b)
	if len(youth.broadcasts) != 0 || len(s.unbound) != 1 {
		t.Fatal("unbinding must move the broadcast to unbound")
	}

	s.removeBroadcast("b1")
	if len(s.unbound) != 0 {
		t.Fatal("removeBroadcast must clear unbound too")
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
	d := deviceState{broadcasts: []youtube.Broadcast{{ID: "a", Title: "Service", ScheduledStart: start}}}
	d.refresh()
	if id, ok := d.idForLabel(d.labels[0]); !ok || id != "a" {
		t.Fatalf("idForLabel = %q, %v", id, ok)
	}
	if _, ok := d.idForLabel("nonsense"); ok {
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
	later := youtube.Broadcast{ID: "later", Title: "B", BoundStreamID: "s1", ScheduledStart: now.Add(48 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	soon := youtube.Broadcast{ID: "soon", Title: "A", BoundStreamID: "s1", ScheduledStart: now.Add(2 * time.Hour), LifeCycleStatus: youtube.LifeReady}
	live := youtube.Broadcast{ID: "live", Title: "C", BoundStreamID: "s1", ScheduledStart: now.Add(72 * time.Hour), LifeCycleStatus: youtube.LifeLive}
	untimed := youtube.Broadcast{ID: "untimed", Title: "D", BoundStreamID: "s1", LifeCycleStatus: youtube.LifeReady}

	s := session{}
	s.setAll([]youtube.Stream{mainKey()}, []youtube.Broadcast{later, soon}, now)
	s.apply(untimed)
	s.apply(live)
	d := s.device(mqtt.DeviceID("s1"))
	got := []string{d.broadcasts[0].ID, d.broadcasts[1].ID, d.broadcasts[2].ID, d.broadcasts[3].ID}
	want := []string{"live", "soon", "later", "untimed"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if d.selectOptions()[0] != d.labels[0] {
		t.Fatal("first option must be the first broadcast")
	}
}

func TestHealthTextIsKeyScoped(t *testing.T) {
	cases := map[string]youtube.StreamStatus{
		"unknown":  {},
		"inactive": {Status: "inactive"},
		"ready":    {Status: "ready"},
		"good":     {Status: youtube.StreamActive, Health: "good"},
		"noData":   {Status: youtube.StreamActive, Health: "noData"},
	}
	for want, status := range cases {
		if got := healthText(status); got != want {
			t.Fatalf("healthText(%+v) = %q, want %q", status, got, want)
		}
	}
	if healthText(youtube.StreamStatus{Status: youtube.StreamActive}) != "unknown" {
		t.Fatal("active without a health report is unknown")
	}
}

func TestAnyLiveSelected(t *testing.T) {
	now := time.Now()
	liveB := youtube.Broadcast{ID: "b1", Title: "Service", BoundStreamID: "s1", LifeCycleStatus: youtube.LifeLive}
	s := session{}
	s.setAll([]youtube.Stream{mainKey()}, []youtube.Broadcast{liveB}, now)
	if s.anyLiveSelected() {
		t.Fatal("a live broadcast nobody selected must not count")
	}
	s.device(mqtt.DeviceID("s1")).selectedID = "b1"
	if !s.anyLiveSelected() {
		t.Fatal("a selected live broadcast must count")
	}
}
