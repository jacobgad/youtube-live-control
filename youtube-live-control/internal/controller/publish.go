package controller

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
)

const publishTimeout = 5 * time.Second

type snapshot struct {
	authorized       bool
	broadcastOptions []string
	thumbnailOptions []string
	selectedLabel    string
	title            string
	scheduled        string
	thumbnail        string
	health           string
	status           string
	viewers          int
	fastMode         bool
	fastRemaining    int
	channel          string
	gates            gates
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
	out := make([]message, 0, 32)
	out = append(out, message{mqtt.ControllerAvailability, mqtt.PayloadOnline})
	for _, m := range mqtt.Messages(origin, snap.broadcastOptions, snap.thumbnailOptions) {
		out = append(out, message{m.Topic, m.JSON()})
	}
	auth := mqtt.PayloadUnauthorized
	if snap.authorized {
		auth = mqtt.PayloadAuthorized
	}
	fastMode := mqtt.PayloadOff
	if snap.fastMode {
		fastMode = mqtt.PayloadOn
	}
	for _, topic := range mqtt.RetiredConfigTopics {
		out = append(out, message{topic, ""})
	}
	out = append(out,
		message{mqtt.AuthState, auth},
		message{mqtt.ChannelState, snap.channel},
		message{mqtt.BroadcastState, snap.selectedLabel},
		message{mqtt.TitleState, snap.title},
		message{mqtt.ScheduledState, snap.scheduled},
		message{mqtt.ThumbnailState, snap.thumbnail},
		message{mqtt.FastModeState, fastMode},
		message{mqtt.FastRemainingState, strconv.Itoa(snap.fastRemaining)},
		message{mqtt.HealthState, snap.health},
		message{mqtt.StatusState, snap.status},
		message{mqtt.ViewersState, strconv.Itoa(snap.viewers)},
		message{mqtt.SaveAvailability, availability(snap.gates.save)},
		message{mqtt.CreateAvailability, availability(snap.gates.create)},
		message{mqtt.GoLiveAvailability, availability(snap.gates.goLive)},
		message{mqtt.EndAvailability, availability(snap.gates.end)},
	)
	return out
}

func availability(enabled bool) string {
	if enabled {
		return mqtt.PayloadOnline
	}
	return mqtt.PayloadOffline
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
