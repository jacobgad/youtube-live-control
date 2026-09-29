package controller

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func (c *Controller) listLoop() {
	defer close(c.listDone)
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-time.After(c.opts.ListPollInterval):
			c.refreshList(c.lifetime)
		}
	}
}

func (c *Controller) statusLoop() {
	defer close(c.statDone)
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-time.After(c.statusDelay()):
			c.reapFastMode(c.lifetime)
			c.pollStatus(c.lifetime)
		case <-c.statusKick:
			c.pollStatus(c.lifetime)
		}
	}
}

func (c *Controller) statusDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.fastActive(c.now()) {
		return c.opts.FastPollInterval
	}
	if b := c.session.selected(); b != nil && isLive(*b) {
		return c.opts.LivePollInterval
	}
	return c.opts.IdlePollInterval
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

func (c *Controller) refreshList(ctx context.Context) {
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
	merged := make([]youtube.Broadcast, 0, len(upcoming)+len(active))
	seen := map[string]bool{}
	for _, b := range slices.Concat(active, upcoming) {
		if !seen[b.ID] && !b.IsDefault {
			seen[b.ID] = true
			merged = append(merged, b)
		}
	}
	slices.SortStableFunc(merged, compareBroadcasts)

	c.mu.Lock()
	// A selected broadcast that is mid-transition or live is pinned even if YouTube's
	// list filters momentarily omit it, so the panel cannot lose it mid-service.
	if pinned := c.session.selected(); pinned != nil && !seen[pinned.ID] && pinned.LifeCycleStatus != youtube.LifeComplete {
		merged = append(merged, *pinned)
	}
	lost := c.session.setBroadcasts(merged, c.now())
	staleCount := len(c.session.stale)
	c.mu.Unlock()

	c.log.Info("broadcast_list_refreshed", "upcoming", len(upcoming), "active", len(active), "hiddenStale", staleCount)
	if lost {
		c.log.Info("selection_cleared", "reason", "broadcast_no_longer_listed")
	}
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) pollStatus(ctx context.Context) {
	c.mu.Lock()
	id := c.session.selectedID
	authorized := c.session.authorized
	pending := c.session.pending
	c.mu.Unlock()
	if !authorized || id == "" || pending != pendingNone {
		return
	}

	b, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil {
		c.logListError("status_poll", err)
		return
	}
	if !ok {
		c.log.Warn("broadcast_missing", "id", id)
		c.dropMissing(ctx, id)
		return
	}
	var stream youtube.StreamStatus
	if b.BoundStreamID != "" {
		if stream, err = c.yt.StreamStatus(ctx, b.BoundStreamID); err != nil {
			c.logListError("status_poll_stream", err)
			return
		}
	}
	c.mu.Lock()
	c.session.apply(b)
	if c.session.selectedID == id {
		c.session.stream = stream
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
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
