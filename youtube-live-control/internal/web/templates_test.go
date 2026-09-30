package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func TestEveryPageRenders(t *testing.T) {
	pages := parsePages()
	start := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	live := youtube.Broadcast{ID: "b1", Title: "Sunday Service", ScheduledStart: start, PrivacyStatus: "public", LifeCycleStatus: youtube.LifeLive, BoundStreamID: "s1", ThumbnailURL: "https://i.ytimg.com/x.jpg"}
	stale := youtube.Broadcast{ID: "b0", Title: "Old", ScheduledStart: start.AddDate(0, 0, -14), LifeCycleStatus: youtube.LifeReady}
	streams := []youtube.Stream{{ID: "s1", Title: "OBS", StreamKey: "abcd-efgh-ijkl", Resolution: "1080p", FrameRate: "30fps"}}
	library := []store.Image{{ID: "0123456789ab", File: "0123456789ab.jpg", Size: 204800, UsedBy: 1}, {ID: "ba9876543210", File: "ba9876543210.png", Size: 51200}}
	p := preset.Preset{ID: "abc123", Name: "Sunday", TitleTemplate: "Sunday Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30", ImageID: "0123456789ab"}

	cases := map[string]any{
		"connection":     connectionData{Configured: true, RedirectURI: "http://localhost:8098/oauth/callback", AuthURL: "https://accounts.google.com/x"},
		"broadcasts":     broadcastsData{Listing: controller.Listing{Broadcasts: []youtube.Broadcast{live}, Stale: []youtube.Broadcast{stale}, SelectedID: "b1", Channel: "Church", Authorized: true}, Presets: []preset.Preset{p}},
		"broadcast_form": broadcastForm{ID: "b1", Title: "Sunday Service", Start: start, Privacy: "public", StreamID: "s1", ThumbnailURL: live.ThumbnailURL, Streams: streams, Lifecycle: "ready", Images: library},
		"presets":        presetsData{Presets: []preset.Preset{p}, Streams: map[string]youtube.Stream{"s1": streams[0]}},
		"preset_form":    presetFormData{Preset: p, Streams: streams, Images: library},
		"images":         imagesData{Images: library},
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

	var edit bytes.Buffer
	editForm := broadcastForm{ID: "b1", Title: "T", Start: start, Privacy: "public", Streams: streams, Images: library}
	if err := pages["broadcast_form"].ExecuteTemplate(&edit, "layout.html", page{Tab: "broadcast_form", Authorized: true, Data: editForm}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edit.String(), "Keep current") {
		t.Fatal("editing without a known thumbnail URL must still offer Keep current")
	}

	var buf bytes.Buffer
	newForm := broadcastForm{PresetID: "abc123", Title: "Sunday Service – 5 Jan 2025", Start: start, Privacy: "public", StreamID: "s1", Streams: streams, Images: library, ImageID: "0123456789ab"}
	if err := pages["broadcast_form"].ExecuteTemplate(&buf, "layout.html", page{Base: "", Tab: "broadcast_form", Authorized: true, Data: newForm}); err != nil {
		t.Fatalf("new broadcast form: %v", err)
	}
	if !strings.Contains(buf.String(), `action="/broadcasts/new"`) || !strings.Contains(buf.String(), `name="image_id" value="0123456789ab" checked`) || strings.Contains(buf.String(), "Keep current") {
		t.Fatalf("new form missing action or preset thumbnail: %s", buf.String())
	}
}
