package controller

import (
	"fmt"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

const (
	newStreamLabel   = "New stream…"
	keepCurrentLabel = "Keep current"
)

const displayTimeFormat = "2006-01-02 15:04"

type pendingOp int

const (
	pendingNone pendingOp = iota
	pendingGoLive
	pendingEnd
)

// session is guarded by Controller.mu; an empty selectedID means New stream… and a
// zero fastUntil means the fast-refresh window is off.
type session struct {
	authorized bool
	broadcasts []youtube.Broadcast
	labels     []string
	selectedID string
	draftTitle string
	draftStart time.Time
	thumbnail  string
	thumbFiles []string
	stream     youtube.StreamStatus
	viewers    int
	pending    pendingOp
	tun        tunables
	fastUntil  time.Time
}

func (s *session) armFast(now time.Time) {
	s.fastUntil = now.Add(s.tun.fastWindow())
}

func (s *session) fastActive(now time.Time) bool {
	return !s.fastUntil.IsZero() && now.Before(s.fastUntil)
}

// Rounded up so the countdown reads 5,4,…,1 and hits 0 exactly at expiry.
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
	return append([]string{newStreamLabel}, s.labels...)
}

func (s *session) selectedLabel() string {
	if s.selectedID == "" {
		return newStreamLabel
	}
	for i := range s.broadcasts {
		if s.broadcasts[i].ID == s.selectedID {
			return s.labels[i]
		}
	}
	return newStreamLabel
}

func (s *session) idForLabel(label string) (id string, ok bool) {
	if label == newStreamLabel {
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
	return append([]string{keepCurrentLabel}, s.thumbFiles...)
}

func (s *session) validThumbnail(label string) bool {
	if label == keepCurrentLabel {
		return true
	}
	for _, f := range s.thumbFiles {
		if f == label {
			return true
		}
	}
	return false
}

func (s *session) loadDrafts() {
	s.thumbnail = keepCurrentLabel
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

type gates struct {
	save   bool
	create bool
	goLive bool
	end    bool
}

// End requires the stream to have stopped: streamStatus lags OBS by up to a minute,
// and that lag is the protection against ending a broadcast still receiving frames.
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

// While live, an active stream is the only thing keeping End Stream unavailable, so
// that combination reads as the wait rather than as a plain "live".
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
	if b.LifeCycleStatus == youtube.LifeLive && stream.Status == youtube.StreamActive {
		return "live (waiting for stream to stop)"
	}
	return b.LifeCycleStatus
}

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

// Zone-less input is Home Assistant's local time, which is what a volunteer types.
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
