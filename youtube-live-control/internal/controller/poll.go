package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func (c *Controller) pollLoop() {
	defer close(c.pollDone)
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-time.After(c.pollDelay()):
			c.reapFastMode(c.lifetime)
			c.poll(c.lifetime)
		case <-c.pollKick:
			c.poll(c.lifetime)
		}
	}
}

func (c *Controller) pollDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.fastActive(c.now()) {
		return c.opts.FastRefresh
	}
	if c.session.anyLiveSelected() {
		return c.opts.LiveRefresh
	}
	return c.opts.IdleRefresh
}

// The service, not Home Assistant, owns the switch's OFF transition; publishing on
// every armed tick is also what keeps the countdown sensor moving.
func (c *Controller) reapFastMode(ctx context.Context) {
	c.mu.Lock()
	armed := !c.session.fastUntil.IsZero()
	expired := armed && !c.now().Before(c.session.fastUntil)
	if expired {
		c.session.fastUntil = time.Time{}
	}
	c.mu.Unlock()
	if expired {
		c.log.Info("fast_mode_expired")
	}
	if armed {
		c.pub.update(ctx, c.snapshot)
	}
}

// poll is the one refresh cycle: three list calls (3 quota units, flat regardless of
// device count) cover the device roster, every key's ingestion status, and every
// broadcast's state.
func (c *Controller) poll(ctx context.Context) {
	if !c.auth.Authorized() {
		return
	}
	upcoming, err := c.yt.ListBroadcasts(ctx, "upcoming")
	if err != nil {
		c.logListError("list_upcoming", err)
		return
	}
	active, err := c.yt.ListBroadcasts(ctx, "active")
	if err != nil {
		c.logListError("list_active", err)
		return
	}
	streams, err := c.yt.ListStreams(ctx)
	if err != nil {
		c.logListError("list_streams", err)
		return
	}
	merged := make([]youtube.Broadcast, 0, len(upcoming)+len(active))
	seen := map[string]bool{}
	for _, b := range slices.Concat(active, upcoming) {
		if !seen[b.ID] && !b.IsDefault {
			seen[b.ID] = true
			merged = append(merged, b)
		}
	}

	c.mu.Lock()
	// A selected broadcast that is mid-transition or live is pinned even if YouTube's
	// list filters momentarily omit it, so Home Assistant cannot lose it mid-service.
	for _, d := range c.session.devices {
		if pinned := d.selected(); pinned != nil && !seen[pinned.ID] && pinned.LifeCycleStatus != youtube.LifeComplete {
			seen[pinned.ID] = true
			merged = append(merged, *pinned)
		}
	}
	lost := c.session.setAll(streams, merged, c.now())
	roster := make([]string, 0, len(c.session.devices))
	for _, d := range c.session.devices {
		roster = append(roster, d.deviceID)
	}
	previous := c.published
	staleCount := len(c.session.stale)
	unboundCount := len(c.session.unbound)
	c.mu.Unlock()

	c.log.Info("poll_refreshed", "devices", len(roster), "upcoming", len(upcoming), "active", len(active), "unbound", unboundCount, "hiddenStale", staleCount)
	for _, deviceID := range lost {
		c.log.Info("selection_cleared", "deviceId", deviceID, "reason", "broadcast_no_longer_listed")
	}
	// A device stays remembered until every retirement publish lands, so a broker
	// blip while its key disappears cannot leak retained configs forever.
	pending := difference(previous, roster)
	if len(pending) > 0 {
		retired := c.pub.retire(ctx, pending)
		if len(retired) > 0 {
			c.log.Info("devices_retired", "deviceIds", retired)
		}
		pending = difference(pending, retired)
		if len(pending) > 0 {
			c.log.Warn("device_retirement_deferred", "deviceIds", pending)
		}
	}
	next := slices.Concat(roster, pending)
	slices.Sort(next)
	c.mu.Lock()
	changed := !slices.Equal(c.published, next)
	if changed {
		c.published = next
	}
	c.mu.Unlock()
	if changed {
		c.persistPublished(ctx, next)
	}
	c.pub.update(ctx, c.snapshot)
}

func difference(previous, current []string) []string {
	var out []string
	for _, p := range previous {
		if !slices.Contains(current, p) {
			out = append(out, p)
		}
	}
	return out
}

func (c *Controller) persistPublished(ctx context.Context, roster []string) {
	data, _ := json.Marshal(roster)
	if err := c.store.SetSetting(ctx, settingPublishedDevices, string(data)); err != nil {
		c.log.Error("operation_failed", "operation", "persist_published_devices", "error", err)
	}
}

// logListError keeps an unauthorized state quiet at error level: the Authorization
// sensor and the web UI already tell the operator what to do.
func (c *Controller) logListError(operation string, err error) {
	if errors.Is(err, youtube.ErrNotAuthorized) {
		c.log.Debug("poll_skipped_unauthorized", "operation", operation)
		return
	}
	c.log.Error("operation_failed", "operation", operation, "error", err)
}
