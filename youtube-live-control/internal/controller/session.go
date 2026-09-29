package controller

import (
	"fmt"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// Sentinel option labels in the two selects.
const (
	NewStreamLabel   = "New stream…"
	KeepCurrentLabel = "Keep current"
)

const displayTimeFormat = "2006-01-02 15:04"

type pendingOp int

const (
	pendingNone pendingOp = iota
	pendingGoLive
	pendingEnd
)

// session is the single-broadcast working state behind the Home Assistant panel:
// the option lists, the current selection, the volunteer's draft edits, and the
// latest verified broadcast/stream status. Guarded by the controller's mutex.
type session struct {
	authorized bool
	broadcasts []youtube.Broadcast
	labels     []string // aligned with broadcasts
	selectedID string   // "" selects NewStreamLabel
	draftTitle string
	draftStart time.Time
	thumbnail  string
	thumbFiles []string
	stream     youtube.StreamStatus
	viewers    int
	pending    pendingOp
	tun        tunables
	fastUntil  time.Time // zero while the fast-refresh window is off
}

// armFast (re)starts the fast-refresh window; every panel interaction routes
// through here so the switch is a fallback, not something to remember.
func (s *session) armFast(now time.Time) {
	s.fastUntil = now.Add(s.tun.fastWindow())
}

func (s *session) fastActive(now time.Time) bool {
	return !s.fastUntil.IsZero() && now.Before(s.fastUntil)
}

// fastRemainingMinutes rounds up so the countdown reads 5,4,…,1 and hits 0 exactly at expiry.
func (s *session) fastRemainingMinutes(now time.Time) int {
	if !s.fastActive(now) {
		return 0
	}
	return int((s.fastUntil.Sub(now) + time.Minute - 1) / time.Minute)
}

func (s *session) setBroadcasts(list []youtube.Broadcast) {
	s.broadcasts = list
	s.labels = broadcastLabels(list)
}

// apply merges one freshly read broadcast into the list (replacing or appending).
func (s *session) apply(b youtube.Broadcast) {
	for i := range s.broadcasts {
		if s.broadcasts[i].ID == b.ID {
			s.broadcasts[i] = b
			s.labels = broadcastLabels(s.broadcasts)
			return
		}
	}
	s.broadcasts = append(s.broadcasts, b)
	s.labels = broadcastLabels(s.broadcasts)
}

func (s *session) selected() *youtube.Broadcast {
	if s.selectedID == "" {
		return nil
	}
	for i := range s.broadcasts {
		if s.broadcasts[i].ID == s.selectedID {
			b := s.broadcasts[i]
			return &b
		}
	}
	return nil
}

func (s *session) selectOptions() []string {
	return append([]string{NewStreamLabel}, s.labels...)
}

func (s *session) selectedLabel() string {
	if s.selectedID == "" {
		return NewStreamLabel
	}
	for i := range s.broadcasts {
		if s.broadcasts[i].ID == s.selectedID {
			return s.labels[i]
		}
	}
	return NewStreamLabel
}

// idForLabel resolves a select option back to a broadcast id; "" with ok means new.
func (s *session) idForLabel(label string) (id string, ok bool) {
	if label == NewStreamLabel {
		return "", true
	}
	for i, l := range s.labels {
		if l == label {
			return s.broadcasts[i].ID, true
		}
	}
	return "", false
}

func (s *session) thumbnailOptions() []string {
	return append([]string{KeepCurrentLabel}, s.thumbFiles...)
}

func (s *session) validThumbnail(label string) bool {
	if label == KeepCurrentLabel {
		return true
	}
	for _, f := range s.thumbFiles {
		if f == label {
			return true
		}
	}
	return false
}

// loadDrafts re-seeds the editable fields from the selected broadcast so the panel
// pre-fills on every selection change.
func (s *session) loadDrafts() {
	s.thumbnail = KeepCurrentLabel
	s.stream = youtube.StreamStatus{}
	s.viewers = 0
	b := s.selected()
	if b == nil {
		s.draftTitle = ""
		s.draftStart = time.Time{}
		return
	}
	s.draftTitle = b.Title
	s.draftStart = b.ScheduledStart
}

// broadcastLabels renders unique select options: "Mon 2 Jan 15:04 · Title", with a
// numeric suffix when two broadcasts would otherwise collide.
func broadcastLabels(list []youtube.Broadcast) []string {
	labels := make([]string, 0, len(list))
	seen := map[string]int{}
	for _, b := range list {
		label := b.Title
		if !b.ScheduledStart.IsZero() {
			label = b.ScheduledStart.Local().Format("Mon 2 Jan 15:04") + " · " + b.Title
		}
		seen[label]++
		if n := seen[label]; n > 1 {
			label = fmt.Sprintf("%s (%d)", label, n)
		}
		labels = append(labels, label)
	}
	return labels
}

// gates are the per-button availabilities derived from verified state, never from
// anything this add-on merely intends to be true.
type gates struct {
	save   bool
	create bool
	goLive bool
	end    bool
}

// computeGates enforces the panel's safety rules: Go Live only while the encoder's
// stream is active; End Stream only while live and after the stream has stopped
// (streamStatus lags OBS by up to a minute, which is exactly the protection).
func computeGates(authorized bool, b *youtube.Broadcast, stream youtube.StreamStatus, busy bool) gates {
	if !authorized || busy {
		return gates{}
	}
	if b == nil {
		return gates{create: true}
	}
	active := stream.Status == youtube.StreamActive
	switch b.LifeCycleStatus {
	case youtube.LifeCreated, youtube.LifeReady, youtube.LifeTestStarting, youtube.LifeTesting:
		return gates{save: true, goLive: active}
	case youtube.LifeLiveStarting, youtube.LifeLive:
		return gates{save: true, end: !active}
	default:
		return gates{}
	}
}

// statusText renders the Broadcast status sensor, including the transitional states
// and the "waiting for stream to stop" hint while streamStatus lags a stopped encoder.
func statusText(b *youtube.Broadcast, stream youtube.StreamStatus, pending pendingOp) string {
	switch pending {
	case pendingGoLive:
		return "starting"
	case pendingEnd:
		return "ending"
	}
	if b == nil {
		return "new (not created)"
	}
	if b.LifeCycleStatus == youtube.LifeLive && stream.Status == youtube.StreamActive && stream.Health == youtube.HealthNoData {
		return "live (waiting for stream to stop)"
	}
	return b.LifeCycleStatus
}

// healthText renders the Stream health sensor: ingestion state until frames flow,
// then YouTube's health verdict.
func healthText(b *youtube.Broadcast, stream youtube.StreamStatus) string {
	if b == nil {
		return "none"
	}
	if b.BoundStreamID == "" {
		return "no stream bound"
	}
	if stream.Status != youtube.StreamActive {
		if stream.Status == "" {
			return "unknown"
		}
		return stream.Status
	}
	if stream.Health == "" {
		return "unknown"
	}
	return stream.Health
}

func formatWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(displayTimeFormat)
}

// parseWhen accepts the published format, the same with a T, and full RFC 3339;
// an empty string clears the draft. Times without a zone are local time.
func parseWhen(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", displayTimeFormat} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a recognised date-time; use YYYY-MM-DD HH:MM", raw)
}
