package controller

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

func (c *Controller) listLoop() {
	defer close(c.listDone)
	for {
		select {
		case <-c.lifetime.Done():
			return
		case <-time.After(c.listDelay()):
			c.refreshList(c.lifetime)
		case <-c.listKick:
			c.refreshList(c.lifetime)
		}
	}
}

func (c *Controller) listDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.s.tun.listPoll()
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

// statusDelay picks the polling tier: fast while the fast-refresh window is armed,
// live cadence while the selected broadcast is on air, otherwise the idle baseline.
func (c *Controller) statusDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s.fastActive(c.now()) {
		return c.s.tun.fastPoll()
	}
	if b := c.s.selected(); b != nil && (b.LifeCycleStatus == youtube.LifeLive || b.LifeCycleStatus == youtube.LifeLiveStarting) {
		return c.s.tun.livePoll()
	}
	return c.s.tun.idlePoll()
}

// reapFastMode expires the window — the service, not Home Assistant, owns the
// switch's OFF transition — and keeps the countdown ticking while armed.
func (c *Controller) reapFastMode(ctx context.Context) {
	c.mu.Lock()
	armed := !c.s.fastUntil.IsZero()
	expired := armed && !c.now().Before(c.s.fastUntil)
	if expired {
		c.s.fastUntil = time.Time{}
	}
	c.mu.Unlock()
	if expired {
		c.log.Info("fast_mode_expired")
	}
	if armed {
		c.pub.update(ctx, c.snapshot)
	}
}

// refreshList is the slow poll: upcoming and active broadcasts (2 quota units) plus a
// re-scan of the thumbnails directory. The selected broadcast is pinned so it never
// drops out of the options while in use. Drafts are never touched here.
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
	merged := make([]youtube.Broadcast, 0, len(upcoming)+len(active)+1)
	seen := map[string]bool{}
	for _, b := range append(active, upcoming...) {
		if !seen[b.ID] {
			seen[b.ID] = true
			merged = append(merged, b)
		}
	}
	thumbs := scanThumbnails(c.opts.ThumbnailsDir)

	c.mu.Lock()
	if c.s.selectedID != "" && !seen[c.s.selectedID] {
		if pinned := c.s.selected(); pinned != nil {
			merged = append(merged, *pinned)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.ScheduledStart.IsZero() != b.ScheduledStart.IsZero() {
			return b.ScheduledStart.IsZero()
		}
		if !a.ScheduledStart.Equal(b.ScheduledStart) {
			return a.ScheduledStart.Before(b.ScheduledStart)
		}
		return a.Title < b.Title
	})
	c.s.setBroadcasts(merged)
	c.s.thumbFiles = thumbs
	if !c.s.validThumbnail(c.s.thumbnail) {
		c.s.thumbnail = KeepCurrentLabel
	}
	c.mu.Unlock()

	c.log.Debug("broadcast_list_refreshed", "count", len(merged), "thumbnails", len(thumbs))
	c.pub.update(ctx, c.snapshot)
}

// pollStatus re-reads the selected broadcast: the broadcast (1 unit), its bound
// stream (1 unit) and, while live, the viewer count (1 unit). Every publish comes
// from these reads, never from intent.
func (c *Controller) pollStatus(ctx context.Context) {
	c.mu.Lock()
	id := c.s.selectedID
	authorized := c.s.authorized
	pending := c.s.pending
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
		c.deselectMissing(ctx, id)
		return
	}
	var stream youtube.StreamStatus
	if b.BoundStreamID != "" {
		if stream, err = c.yt.StreamStatus(ctx, b.BoundStreamID); err != nil {
			c.logListError("status_poll_stream", err)
			return
		}
	}
	viewers := 0
	if b.LifeCycleStatus == youtube.LifeLive {
		if viewers, err = c.yt.ConcurrentViewers(ctx, id); err != nil {
			c.log.Warn("viewers_unavailable", "id", id, "error", err)
			viewers = 0
		}
	}

	c.mu.Lock()
	c.s.apply(b)
	if c.s.selectedID == id {
		c.s.stream = stream
		c.s.viewers = viewers
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
