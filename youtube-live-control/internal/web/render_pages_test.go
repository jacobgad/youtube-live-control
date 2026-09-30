package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func TestRenderPagesToDisk(t *testing.T) {
	dir := os.Getenv("YLC_RENDER_DIR")
	if dir == "" {
		t.Skip("set YLC_RENDER_DIR to write rendered pages")
	}
	pages := parsePages()
	start := time.Date(2025, 1, 5, 9, 30, 0, 0, time.Local)
	live := youtube.Broadcast{ID: "b1", Title: "Sunday Service – 5 Jan 2025", ScheduledStart: start, PrivacyStatus: "public", LifeCycleStatus: youtube.LifeLive, BoundStreamID: "s1", ThumbnailURL: "https://i.ytimg.com/vi/dQw4w9WgXcQ/maxresdefault.jpg"}
	next := youtube.Broadcast{ID: "b2", Title: "Sunday Service – 12 Jan 2025", ScheduledStart: start.AddDate(0, 0, 7), PrivacyStatus: "unlisted", LifeCycleStatus: youtube.LifeReady}
	stale := youtube.Broadcast{ID: "b0", Title: "Christmas Eve", ScheduledStart: start.AddDate(0, 0, -14), LifeCycleStatus: youtube.LifeReady}
	streams := []youtube.Stream{{ID: "s1", Title: "OBS main", StreamKey: "abcd-efgh-ijkl-mnop", Resolution: "1080p", FrameRate: "30fps"}}
	library := []store.Image{{ID: "0123456789ab", File: "0123456789ab.jpg", Size: 204800, UsedBy: 1}, {ID: "ba9876543210", File: "ba9876543210.png", Size: 51200}}
	p := preset.Preset{ID: "abc123", Name: "Sunday morning", TitleTemplate: "Sunday Service – {date}", Description: "Join us", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30", ImageID: "0123456789ab"}
	listing := controller.Listing{Broadcasts: []youtube.Broadcast{live, next}, Stale: []youtube.Broadcast{stale}, SelectedID: "b1", Channel: "St Mary's", Authorized: true}
	cases := map[string]any{
		"connection":     connectionData{Configured: true, RedirectURI: "http://localhost:8098/oauth/callback", AuthURL: "#"},
		"broadcasts":     broadcastsData{Listing: listing, Presets: []preset.Preset{p}},
		"broadcast_form": broadcastForm{ID: "b1", Title: live.Title, Description: "Join us", Start: start, Privacy: "public", StreamID: "s1", ThumbnailURL: live.ThumbnailURL, Streams: streams, Lifecycle: "ready", Images: library},
		"presets":        presetsData{Presets: []preset.Preset{p}, Streams: map[string]youtube.Stream{"s1": streams[0]}},
		"preset_form":    presetFormData{Preset: p, Streams: streams, Images: library},
		"images":         imagesData{Images: library},
	}
	for name, data := range cases {
		f, err := os.Create(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pages[name].ExecuteTemplate(f, "layout.html", page{Base: ".", Title: name, Tab: name, Channel: "St Mary's", Authorized: true, Data: data}); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
