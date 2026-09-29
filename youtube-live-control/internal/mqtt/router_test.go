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
		ScheduledEntered:  recordPayload("scheduled"),
		ThumbnailSelected: recordPayload("thumbnail"),
		FastModeSwitched: func(on bool) {
			if on {
				got = append(got, "fast:on")
			} else {
				got = append(got, "fast:off")
			}
		},
		NumberEntered:       func(object, raw string) { got = append(got, "number:"+object+"="+raw) },
		SavePressed:         record("save"),
		CreatePressed:       record("create"),
		GoLivePressed:       record("go_live"),
		EndPressed:          record("end"),
		HomeAssistantOnline: record("ha_online"),
	}, slog.Default())

	router(BroadcastSet, []byte(" Sunday · Service "))
	router(TitleSet, []byte("Sunday Service"))
	router(ScheduledSet, []byte("2025-01-05 09:30"))
	router(ThumbnailSet, []byte("cover.jpg"))
	router(FastModeSet, []byte("ON"))
	router(FastModeSet, []byte(" off "))
	router(FastModeSet, []byte("maybe"))
	router(NumberSet("fast_poll_seconds"), []byte(" 5 "))
	router(SavePress, []byte(PayloadPress))
	router(CreatePress, []byte(PayloadPress))
	router(GoLivePress, []byte(PayloadPress))
	router(EndPress, []byte(PayloadPress))
	router(HAStatusTopic, []byte("online"))
	router(HAStatusTopic, []byte("offline"))
	router("ylc/unknown", []byte("x"))

	want := []string{
		"broadcast:Sunday · Service",
		"title:Sunday Service",
		"scheduled:2025-01-05 09:30",
		"thumbnail:cover.jpg",
		"fast:on", "fast:off",
		"number:fast_poll_seconds=5",
		"save", "create", "go_live", "end",
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
