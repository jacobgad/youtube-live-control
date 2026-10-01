package mqtt

import (
	"log/slog"
	"testing"
)

func TestRouterDispatch(t *testing.T) {
	var got []string
	record := func(name string) func() {
		return func() { got = append(got, name) }
	}
	recordPayload := func(name string) func(string) {
		return func(p string) { got = append(got, name+":"+p) }
	}
	recordDevice := func(name string) func(string, string) {
		return func(deviceID, p string) { got = append(got, name+"@"+deviceID+":"+p) }
	}
	recordPress := func(name string) func(string) {
		return func(deviceID string) { got = append(got, name+"@"+deviceID) }
	}
	router := NewRouter(Actions{
		BroadcastSelected: recordDevice("broadcast"),
		TitleEntered:      recordDevice("title"),
		PrivacySelected:   recordDevice("privacy"),
		FastModeSwitched: func(on bool) {
			if on {
				got = append(got, "fast:on")
			} else {
				got = append(got, "fast:off")
			}
		},
		GoLivePressed:       recordPress("go_live"),
		EndPressed:          recordPress("end"),
		DeletePressed:       recordPress("delete"),
		PresetSelected:      recordPayload("preset"),
		StartEntered:        recordPayload("start"),
		SchedulePrivacy:     recordPayload("schedule_privacy"),
		SchedulePressed:     record("schedule"),
		HomeAssistantOnline: record("ha_online"),
	}, slog.Default())

	s1 := StreamTopics{Device: "s1"}
	s2 := StreamTopics{Device: "s2"}
	router(s1.BroadcastSet(), []byte(" Sunday · Service "))
	router(s1.TitleSet(), []byte("Sunday Service"))
	router(s2.PrivacySet(), []byte(" unlisted "))
	router(FastModeSet, []byte("ON"))
	router(FastModeSet, []byte(" off "))
	router(FastModeSet, []byte("maybe"))
	router(s1.GoLivePress(), []byte(PayloadPress))
	router(s2.EndPress(), []byte(PayloadPress))
	router(s1.DeletePress(), []byte(PayloadPress))
	router(PresetSet, []byte("Sunday"))
	router(StartSet, []byte("2025-01-05T09:30:00+10:00"))
	router(SchedulePrivacySet, []byte("unlisted"))
	router(SchedulePress, []byte(PayloadPress))
	router(HAStatusTopic, []byte("online"))
	router(HAStatusTopic, []byte("offline"))
	router("ylc/unknown", []byte("x"))
	router("ylc/stream/s1/unknown/set", []byte("x"))
	router("ylc/stream/s1/broadcast", []byte("x"))

	want := []string{
		"broadcast@s1:Sunday · Service",
		"title@s1:Sunday Service",
		"privacy@s2:unlisted",
		"fast:on", "fast:off",
		"go_live@s1", "end@s2", "delete@s1",
		"preset:Sunday", "start:2025-01-05T09:30:00+10:00", "schedule_privacy:unlisted", "schedule",
		"ha_online",
	}
	if len(got) != len(want) {
		t.Fatalf("dispatched %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dispatched %v, want %v", got, want)
		}
	}
}
