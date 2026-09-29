package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func TestEveryPageRenders(t *testing.T) {
	pages := parsePages()
	start := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	live := youtube.Broadcast{ID: "b1", Title: "Sunday Service", ScheduledStart: start, PrivacyStatus: "public", LifeCycleStatus: youtube.LifeLive, BoundStreamID: "s1", ThumbnailURL: "https://i.ytimg.com/x.jpg"}
	stale := youtube.Broadcast{ID: "b0", Title: "Old", ScheduledStart: start.AddDate(0, 0, -14), LifeCycleStatus: youtube.LifeReady}
	streams := []youtube.Stream{{ID: "s1", Title: "OBS", StreamKey: "abcd-efgh-ijkl", Resolution: "1080p", FrameRate: "30fps"}}
	p := preset.Preset{ID: "abc123", Name: "Sunday", TitleTemplate: "Sunday Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30", ThumbnailFile: "abc123.jpg"}

	cases := map[string]any{
		"connection":     connectionData{Configured: true, RedirectURI: "http://localhost:8098/oauth/callback", AuthURL: "https://accounts.google.com/x"},
		"broadcasts":     broadcastsData{Listing: controller.Listing{Broadcasts: []youtube.Broadcast{live}, Stale: []youtube.Broadcast{stale}, SelectedID: "b1", Channel: "Church", Authorized: true}, Presets: []preset.Preset{p}},
		"broadcast_form": broadcastForm{ID: "b1", Title: "Sunday Service", Start: start, Privacy: "public", StreamID: "s1", ThumbnailURL: live.ThumbnailURL, Streams: streams, Lifecycle: "ready"},
		"presets":        presetsData{Presets: []preset.Preset{p}, Streams: map[string]youtube.Stream{"s1": streams[0]}},
		"preset_form":    presetFormData{Preset: p, Streams: streams},
	}
	for name, data := range cases {
		var buf bytes.Buffer
		err := pages[name].ExecuteTemplate(&buf, "layout.html", page{Base: "/api/hassio_ingress/tok", Title: name, Tab: name, Channel: "Church", Authorized: true, Data: data})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(buf.String(), `href="/api/hassio_ingress/tok/`) {
			t.Fatalf("%s: links are not prefixed with the ingress path", name)
		}
	}

	var buf bytes.Buffer
	newForm := broadcastForm{PresetID: "abc123", Title: "Sunday Service – 5 Jan 2025", Start: start, Privacy: "public", StreamID: "s1", Streams: streams, HasPresetThumb: true}
	if err := pages["broadcast_form"].ExecuteTemplate(&buf, "layout.html", page{Base: "", Tab: "broadcast_form", Authorized: true, Data: newForm}); err != nil {
		t.Fatalf("new broadcast form: %v", err)
	}
	if !strings.Contains(buf.String(), `action="/broadcasts/new"`) || !strings.Contains(buf.String(), "presets/abc123/thumbnail") {
		t.Fatalf("new form missing action or preset thumbnail: %s", buf.String())
	}
}
