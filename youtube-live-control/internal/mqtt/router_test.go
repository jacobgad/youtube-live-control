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
	router := NewRouter(Actions{
		BroadcastSelected: recordPayload("broadcast"),
		TitleEntered:      recordPayload("title"),
		PrivacySelected:   recordPayload("privacy"),
		FastModeSwitched: func(on bool) {
			if on {
				got = append(got, "fast:on")
			} else {
				got = append(got, "fast:off")
			}
		},
		GoLivePressed:       record("go_live"),
		EndPressed:          record("end"),
		PresetSelected:      recordPayload("preset"),
		StartEntered:        recordPayload("start"),
		SchedulePressed:     record("schedule"),
		HomeAssistantOnline: record("ha_online"),
	}, slog.Default())

	router(BroadcastSet, []byte(" Sunday · Service "))
	router(TitleSet, []byte("Sunday Service"))
	router(PrivacySet, []byte(" unlisted "))
	router(FastModeSet, []byte("ON"))
	router(FastModeSet, []byte(" off "))
	router(FastModeSet, []byte("maybe"))
	router(GoLivePress, []byte(PayloadPress))
	router(EndPress, []byte(PayloadPress))
	router(PresetSet, []byte("Sunday"))
	router(StartSet, []byte("2025-01-05T09:30:00+10:00"))
	router(SchedulePress, []byte(PayloadPress))
	router(HAStatusTopic, []byte("online"))
	router(HAStatusTopic, []byte("offline"))
	router("ylc/unknown", []byte("x"))

	want := []string{
		"broadcast:Sunday · Service",
		"title:Sunday Service",
		"privacy:unlisted",
		"fast:on", "fast:off",
		"go_live", "end",
		"preset:Sunday", "start:2025-01-05T09:30:00+10:00", "schedule",
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
