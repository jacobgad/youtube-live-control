package controller

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// noBroadcastLabel is a Broadcast select's only option while nothing is bound to its
// key; MQTT selects need at least one option. "Nothing selected" is published as None,
// since Home Assistant ignores a select state that is not one of the options.
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

// deviceState is one custom stream key's Home Assistant device: the key itself, the
// broadcasts bound to it, and the volunteer's selection among them.
type deviceState struct {
	stream     youtube.Stream
	deviceID   string
	broadcasts []youtube.Broadcast
	labels     []string
	selectedID string
	pending    pendingOp
}

// session is guarded by Controller.mu; an empty selectedID on a device means nothing
// selected there and a zero fastUntil means the fast-refresh window is off.
type session struct {
	authorized bool
	channel    string
	devices    []*deviceState
	unbound    []youtube.Broadcast
	stale      []youtube.Broadcast
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

// setAll rebuilds the device roster from the custom stream keys and partitions the
// broadcasts onto them by binding. Selections and pending operations carry over per
// key; nothing but a human ever fills an empty selection, and a selected broadcast
// that is no longer bound to its device clears (selection lost).
func (s *session) setAll(streams []youtube.Stream, broadcasts []youtube.Broadcast, now time.Time) (lost []string) {
	prev := make(map[string]*deviceState, len(s.devices))
	for _, d := range s.devices {
		prev[d.stream.ID] = d
	}
	s.devices = nil
	for _, st := range streams {
		if st.IsDefault {
			continue
		}
		d := &deviceState{stream: st, deviceID: mqtt.DeviceID(st.ID)}
		if p, ok := prev[st.ID]; ok {
			d.selectedID, d.pending = p.selectedID, p.pending
		}
		s.devices = append(s.devices, d)
	}
	slices.SortStableFunc(s.devices, compareDevices)

	byStream := make(map[string]*deviceState, len(s.devices))
	for _, d := range s.devices {
		byStream[d.stream.ID] = d
	}
	s.unbound, s.stale = nil, nil
	for _, b := range broadcasts {
		switch d := byStream[b.BoundStreamID]; {
		case isStale(b, now):
			s.stale = append(s.stale, b)
		case d != nil:
			d.broadcasts = append(d.broadcasts, b)
		default:
			s.unbound = append(s.unbound, b)
		}
	}
	slices.SortStableFunc(s.unbound, compareBroadcasts)
	for _, d := range s.devices {
		d.refresh()
		if d.selectedID != "" && d.selected() == nil {
			d.selectedID = ""
			d.pending = pendingNone
			lost = append(lost, d.deviceID)
		}
	}
	return lost
}

func compareDevices(a, b *deviceState) int {
	return cmp.Or(cmp.Compare(a.stream.Title, b.stream.Title), cmp.Compare(a.stream.ID, b.stream.ID))
}

func isStale(b youtube.Broadcast, now time.Time) bool {
	return !isLive(b) && !b.ScheduledStart.IsZero() && now.Sub(b.ScheduledStart) > staleAfter
}

func isLive(b youtube.Broadcast) bool {
	return b.LifeCycleStatus == youtube.LifeLive || b.LifeCycleStatus == youtube.LifeLiveStarting
}

// apply routes a freshly read broadcast to the device its binding names, moving it
// between devices (or to unbound) when the binding changed. A selection follows the
// broadcast only while it stays on the same device.
func (s *session) apply(b youtube.Broadcast) {
	target := s.deviceByStream(b.BoundStreamID)
	for _, d := range s.devices {
		if d == target {
			d.upsert(b)
		} else {
			d.remove(b.ID)
		}
	}
	s.unbound = slices.DeleteFunc(s.unbound, func(x youtube.Broadcast) bool { return x.ID == b.ID })
	s.stale = slices.DeleteFunc(s.stale, func(x youtube.Broadcast) bool { return x.ID == b.ID })
	if target == nil {
		s.unbound = append(s.unbound, b)
		slices.SortStableFunc(s.unbound, compareBroadcasts)
	}
}

// removeBroadcast drops a vanished broadcast everywhere, clearing any selection of it.
func (s *session) removeBroadcast(id string) {
	for _, d := range s.devices {
		d.remove(id)
	}
	s.unbound = slices.DeleteFunc(s.unbound, func(x youtube.Broadcast) bool { return x.ID == id })
	s.stale = slices.DeleteFunc(s.stale, func(x youtube.Broadcast) bool { return x.ID == id })
}

func (s *session) device(deviceID string) *deviceState {
	for _, d := range s.devices {
		if d.deviceID == deviceID {
			return d
		}
	}
	return nil
}

func (s *session) deviceByStream(streamID string) *deviceState {
	if streamID == "" {
		return nil
	}
	for _, d := range s.devices {
		if d.stream.ID == streamID {
			return d
		}
	}
	return nil
}

func (s *session) deviceFor(broadcastID string) *deviceState {
	for _, d := range s.devices {
		for i := range d.broadcasts {
			if d.broadcasts[i].ID == broadcastID {
				return d
			}
		}
	}
	return nil
}

func (s *session) find(id string) (youtube.Broadcast, bool) {
	for _, list := range s.collections() {
		for _, b := range list {
			if b.ID == id {
				return b, true
			}
		}
	}
	return youtube.Broadcast{}, false
}

func (s *session) collections() [][]youtube.Broadcast {
	out := make([][]youtube.Broadcast, 0, len(s.devices)+2)
	for _, d := range s.devices {
		out = append(out, d.broadcasts)
	}
	return append(out, s.unbound, s.stale)
}

// allBroadcasts is the web UI's flat upcoming list: every non-stale broadcast on any
// device plus the unbound ones, in one sorted sweep.
func (s *session) allBroadcasts() []youtube.Broadcast {
	var out []youtube.Broadcast
	for _, d := range s.devices {
		out = append(out, d.broadcasts...)
	}
	out = append(out, s.unbound...)
	slices.SortStableFunc(out, compareBroadcasts)
	return out
}

func (s *session) allStarts() []time.Time {
	var starts []time.Time
	for _, list := range s.collections() {
		for _, b := range list {
			starts = append(starts, b.ScheduledStart)
		}
	}
	return starts
}

func (s *session) anyLiveSelected() bool {
	for _, d := range s.devices {
		if b := d.selected(); b != nil && isLive(*b) {
			return true
		}
	}
	return false
}

func (d *deviceState) refresh() {
	slices.SortStableFunc(d.broadcasts, compareBroadcasts)
	d.labels = broadcastLabels(d.broadcasts)
}

func (d *deviceState) upsert(b youtube.Broadcast) {
	if i := slices.IndexFunc(d.broadcasts, func(x youtube.Broadcast) bool { return x.ID == b.ID }); i >= 0 {
		d.broadcasts[i] = b
	} else {
		d.broadcasts = append(d.broadcasts, b)
	}
	d.refresh()
}

func (d *deviceState) remove(id string) {
	n := len(d.broadcasts)
	d.broadcasts = slices.DeleteFunc(d.broadcasts, func(x youtube.Broadcast) bool { return x.ID == id })
	if len(d.broadcasts) != n {
		d.refresh()
	}
	if d.selectedID == id {
		d.selectedID = ""
		d.pending = pendingNone
	}
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

func (d *deviceState) selected() *youtube.Broadcast {
	if d.selectedID == "" {
		return nil
	}
	for i := range d.broadcasts {
		if d.broadcasts[i].ID == d.selectedID {
			b := d.broadcasts[i]
			return &b
		}
	}
	return nil
}

func (d *deviceState) selectOptions() []string {
	if len(d.labels) == 0 {
		return []string{noBroadcastLabel}
	}
	return d.labels
}

func (d *deviceState) selectedLabel() string {
	for i := range d.broadcasts {
		if d.broadcasts[i].ID == d.selectedID {
			return d.labels[i]
		}
	}
	return mqttNone
}

func (d *deviceState) idForLabel(label string) (id string, ok bool) {
	for i, l := range d.labels {
		if l == label {
			return d.broadcasts[i].ID, true
		}
	}
	return "", false
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

func canDelete(authorized bool, current string) bool {
	if !authorized || current == stageNoBroadcast {
		return false
	}
	return !isOnAir(current) && current != stageStarting && current != stageEnding
}

func isOnAir(current string) bool {
	return current == stageLive || current == stageStreamStopping || current == stageReadyToEnd
}

// healthText is key-scoped: the encoder's ingestion state is a fact about the stream
// key, meaningful whether or not a broadcast is selected.
func healthText(stream youtube.StreamStatus) string {
	if stream.Status == "" {
		return "unknown"
	}
	if stream.Status != youtube.StreamActive {
		return stream.Status
	}
	if stream.Health == "" {
		return "unknown"
	}
	return stream.Health
}
