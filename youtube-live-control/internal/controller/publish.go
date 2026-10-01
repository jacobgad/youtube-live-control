package controller

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

const publishTimeout = 5 * time.Second

// mqttNone is Home Assistant's MQTT null payload; an empty string on a timestamp
// sensor is logged as an invalid state instead.
const mqttNone = "None"

type snapshot struct {
	authorized     bool
	channel        string
	options        mqtt.Options
	selectedLabel  string
	attributes     string
	title          string
	privacy        string
	scheduledStart string
	thumbnailURL   string
	stage          string
	live           bool
	encoder        bool
	health         string
	status         string
	fastMode       bool
	fastRemaining  int
	unlocked       bool
	gates          gates
	canDelete      bool
	presetLabel    string
	start          string
	schedPrivacy   string
	canSchedule    bool
}

type message struct {
	topic   string
	payload string
}

// publisher holds mu for a whole sweep and builds the snapshot under it: retained
// topics keep only the last message, so a later snapshot must never be sent first.
type publisher struct {
	conn   mqtt.Connection
	origin mqtt.Origin
	log    *slog.Logger

	mu   sync.Mutex
	last map[string]string
}

func newPublisher(conn mqtt.Connection, origin mqtt.Origin, log *slog.Logger) *publisher {
	return &publisher{conn: conn, origin: origin, log: log, last: map[string]string{}}
}

func (p *publisher) everything(ctx context.Context, snap func() snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = map[string]string{}
	p.send(ctx, snap())
}

func (p *publisher) update(ctx context.Context, snap func() snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.send(ctx, snap())
}

// invalidate is how a rejected command snaps the Home Assistant field back: the
// unchanged value would otherwise be deduped away.
func (p *publisher) invalidate(topics ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range topics {
		delete(p.last, t)
	}
}

// controllerOffline skips mu on purpose: Stop calls it under a deadline and must not
// queue behind a sweep that is still waiting on the broker.
func (p *publisher) controllerOffline(ctx context.Context) {
	p.publish(ctx, mqtt.ControllerAvailability, mqtt.PayloadOffline)
}

func (p *publisher) send(ctx context.Context, snap snapshot) {
	for _, m := range render(snap, p.origin) {
		if prev, ok := p.last[m.topic]; ok && prev == m.payload {
			continue
		}
		if p.publish(ctx, m.topic, m.payload) {
			p.last[m.topic] = m.payload
		}
	}
}

func render(snap snapshot, origin mqtt.Origin) []message {
	configs := mqtt.Messages(origin, snap.options)
	out := make([]message, 0, 1+len(configs)+len(mqtt.RetiredConfigTopics)+24)
	out = append(out, message{mqtt.ControllerAvailability, mqtt.PayloadOnline})
	for _, m := range configs {
		out = append(out, message{m.Topic, m.JSON()})
	}
	for _, topic := range mqtt.RetiredConfigTopics {
		out = append(out, message{topic, ""})
	}
	out = append(out,
		message{mqtt.Lock, onOff(snap.unlocked, mqtt.PayloadOnline, mqtt.PayloadOffline)},
		message{mqtt.AuthState, onOff(snap.authorized, mqtt.PayloadAuthorized, mqtt.PayloadUnauthorized)},
		message{mqtt.ChannelState, snap.channel},
		message{mqtt.BroadcastState, snap.selectedLabel},
		message{mqtt.BroadcastAttributes, snap.attributes},
		message{mqtt.TitleState, snap.title},
		message{mqtt.PrivacyState, snap.privacy},
		message{mqtt.ScheduledStartState, cmp.Or(snap.scheduledStart, mqttNone)},
		message{mqtt.ThumbnailURLState, snap.thumbnailURL},
		message{mqtt.ThumbnailAvail, onOff(snap.thumbnailURL != "", mqtt.PayloadOnline, mqtt.PayloadOffline)},
		message{mqtt.StageState, snap.stage},
		message{mqtt.LiveState, onOff(snap.live, mqtt.PayloadOn, mqtt.PayloadOff)},
		message{mqtt.EncoderState, onOff(snap.encoder, mqtt.PayloadOn, mqtt.PayloadOff)},
		message{mqtt.FastModeState, onOff(snap.fastMode, mqtt.PayloadOn, mqtt.PayloadOff)},
		message{mqtt.FastRemainingState, strconv.Itoa(snap.fastRemaining)},
		message{mqtt.HealthState, snap.health},
		message{mqtt.StatusState, snap.status},
		message{mqtt.GoLiveAvailability, onOff(snap.gates.goLive, mqtt.PayloadOnline, mqtt.PayloadOffline)},
		message{mqtt.EndAvailability, onOff(snap.gates.end, mqtt.PayloadOnline, mqtt.PayloadOffline)},
		message{mqtt.DeleteAvailability, onOff(snap.canDelete, mqtt.PayloadOnline, mqtt.PayloadOffline)},
		message{mqtt.PresetState, snap.presetLabel},
		message{mqtt.StartState, snap.start},
		message{mqtt.SchedulePrivacyState, snap.schedPrivacy},
		message{mqtt.ScheduleAvailability, onOff(snap.canSchedule, mqtt.PayloadOnline, mqtt.PayloadOffline)},
	)
	return out
}

func onOff(v bool, on, off string) string {
	if v {
		return on
	}
	return off
}

// Attributes cost nothing: every field is already in the broadcast we poll.
func broadcastAttributes(b *youtube.Broadcast) string {
	if b == nil {
		return "{}"
	}
	attrs := map[string]any{
		"id":            b.ID,
		"privacy":       b.PrivacyStatus,
		"lifecycle":     b.LifeCycleStatus,
		"thumbnail_url": b.ThumbnailURL,
		"watch_url":     "https://www.youtube.com/watch?v=" + b.ID,
	}
	if !b.ScheduledStart.IsZero() {
		attrs["scheduled_start"] = b.ScheduledStart.Format(time.RFC3339)
	}
	data, _ := json.Marshal(attrs)
	return string(data)
}

func (p *publisher) publish(ctx context.Context, topic, payload string) bool {
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	err := p.conn.Publish(pubCtx, topic, payload, true)
	switch {
	case err == nil:
		return true
	case errors.Is(err, mqtt.ErrNotConnected):
		p.log.Debug("mqtt_publish_deferred", "topic", topic)
	default:
		p.log.Warn("mqtt_publish_failed", "topic", topic, "error", err)
	}
	return false
}
