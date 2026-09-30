package controller

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// noBroadcastLabel is the Broadcast select's state while nothing is selected and its
// only option while nothing is scheduled; MQTT selects need at least one option.
const noBroadcastLabel = "No broadcast selected"

// staleAfter hides scheduled-but-never-started broadcasts from the volunteer's select;
// they stay visible (greyed) in the web UI so a producer knows to clean up in Studio.
const staleAfter = 24 * time.Hour

type pendingOp int

const (
	pendingNone pendingOp = iota
	pendingGoLive
	pendingEnd
)

const (
	stageNoBroadcast       = "no_broadcast"
	stageNoStreamKey       = "no_stream_key"
	stageWaitingForEncoder = "waiting_for_encoder"
	stageReadyToGoLive     = "ready_to_go_live"
	stageStarting          = "starting"
	stageLive              = "live"
	stageStreamStopping    = "stream_stopping"
	stageReadyToEnd        = "ready_to_end"
	stageEnding            = "ending"
	stageEnded             = "ended"
)

// session is guarded by Controller.mu; an empty selectedID means nothing selected and
// a zero fastUntil means the fast-refresh window is off.
type session struct {
	authorized bool
	channel    string
	country    string
	broadcasts []youtube.Broadcast
	stale      []youtube.Broadcast
	labels     []string
	selectedID string
	stream     youtube.StreamStatus
	pending    pendingOp
	fastUntil  time.Time
	sched      scheduling
}

func (s *session) armFast(now time.Time, window time.Duration) {
	s.fastUntil = now.Add(window)
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

// Nothing but a human changes the selection: a vanished broadcast clears it, and
// the sorted list puts the soonest stream first so choosing is one tap.
func (s *session) setBroadcasts(list []youtube.Broadcast, now time.Time) (selectionLost bool) {
	s.broadcasts, s.stale = nil, nil
	for _, b := range list {
		if isStale(b, now) {
			s.stale = append(s.stale, b)
		} else {
			s.broadcasts = append(s.broadcasts, b)
		}
	}
	s.labels = broadcastLabels(s.broadcasts)
	if s.selectedID != "" && s.selected() == nil {
		s.selectedID = ""
		s.resetLiveState()
		return true
	}
	return false
}

func isStale(b youtube.Broadcast, now time.Time) bool {
	return !isLive(b) && !b.ScheduledStart.IsZero() && now.Sub(b.ScheduledStart) > staleAfter
}

func isLive(b youtube.Broadcast) bool {
	return b.LifeCycleStatus == youtube.LifeLive || b.LifeCycleStatus == youtube.LifeLiveStarting
}

func (s *session) apply(b youtube.Broadcast) {
	if i := slices.IndexFunc(s.broadcasts, func(x youtube.Broadcast) bool { return x.ID == b.ID }); i >= 0 {
		s.broadcasts[i] = b
	} else {
		s.broadcasts = append(s.broadcasts, b)
	}
	slices.SortStableFunc(s.broadcasts, compareBroadcasts)
	s.labels = broadcastLabels(s.broadcasts)
}

// Live first, then soonest, then title: the stream the volunteer wants is the first option.
func compareBroadcasts(a, b youtube.Broadcast) int {
	if isLive(a) != isLive(b) {
		if isLive(a) {
			return -1
		}
		return 1
	}
	if a.ScheduledStart.IsZero() != b.ScheduledStart.IsZero() {
		if a.ScheduledStart.IsZero() {
			return 1
		}
		return -1
	}
	return cmp.Or(a.ScheduledStart.Compare(b.ScheduledStart), cmp.Compare(a.Title, b.Title))
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

func (s *session) resetLiveState() {
	s.stream = youtube.StreamStatus{}
	s.pending = pendingNone
}

func (s *session) selectOptions() []string {
	if len(s.labels) == 0 {
		return []string{noBroadcastLabel}
	}
	return s.labels
}

func (s *session) selectedLabel() string {
	for i := range s.broadcasts {
		if s.broadcasts[i].ID == s.selectedID {
			return s.labels[i]
		}
	}
	return noBroadcastLabel
}

func (s *session) idForLabel(label string) (id string, ok bool) {
	for i, l := range s.labels {
		if l == label {
			return s.broadcasts[i].ID, true
		}
	}
	return "", false
}

func (s *session) allStarts() []time.Time {
	starts := make([]time.Time, 0, len(s.broadcasts)+len(s.stale))
	for _, b := range s.broadcasts {
		starts = append(starts, b.ScheduledStart)
	}
	for _, b := range s.stale {
		starts = append(starts, b.ScheduledStart)
	}
	return starts
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

// stream_stopping is live + active + noData: the encoder has gone but YouTube has not
// noticed yet, which is exactly why End stays unavailable.
func stage(b *youtube.Broadcast, stream youtube.StreamStatus, pending pendingOp) string {
	switch pending {
	case pendingGoLive:
		return stageStarting
	case pendingEnd:
		return stageEnding
	}
	if b == nil {
		return stageNoBroadcast
	}
	if b.BoundStreamID == "" {
		return stageNoStreamKey
	}
	active := stream.Status == youtube.StreamActive
	switch b.LifeCycleStatus {
	case youtube.LifeCreated, youtube.LifeReady, youtube.LifeTestStarting, youtube.LifeTesting:
		if active {
			return stageReadyToGoLive
		}
		return stageWaitingForEncoder
	case youtube.LifeLiveStarting:
		return stageStarting
	case youtube.LifeLive:
		switch {
		case !active:
			return stageReadyToEnd
		case stream.Health == "noData":
			return stageStreamStopping
		default:
			return stageLive
		}
	default:
		return stageEnded
	}
}

type gates struct {
	goLive bool
	end    bool
}

func computeGates(authorized bool, current string) gates {
	if !authorized {
		return gates{}
	}
	return gates{goLive: current == stageReadyToGoLive, end: current == stageReadyToEnd}
}

func isOnAir(current string) bool {
	return current == stageLive || current == stageStreamStopping || current == stageReadyToEnd
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
