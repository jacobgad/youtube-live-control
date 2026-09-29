package controller

import (
	"cmp"
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
	if b := c.session.selected(); b != nil && (b.LifeCycleStatus == youtube.LifeLive || b.LifeCycleStatus == youtube.LifeLiveStarting) {
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

// The selected broadcast is pinned into the list so it cannot vanish from the select
// mid-service (a live broadcast leaves "upcoming"); drafts are never touched here.
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
	for _, b := range slices.Concat(active, upcoming) {
		if !seen[b.ID] {
			seen[b.ID] = true
			merged = append(merged, b)
		}
	}
	thumbs := scanThumbnails(c.opts.ThumbnailsDir)

	c.mu.Lock()
	if c.session.selectedID != "" && !seen[c.session.selectedID] {
		if pinned := c.session.selected(); pinned != nil {
			merged = append(merged, *pinned)
		}
	}
	slices.SortStableFunc(merged, compareBroadcasts)
	c.session.setBroadcasts(merged)
	c.session.thumbFiles = thumbs
	if !c.session.validThumbnail(c.session.thumbnail) {
		c.session.thumbnail = keepCurrentLabel
	}
	c.mu.Unlock()

	c.log.Info("broadcast_list_refreshed", "upcoming", len(upcoming), "active", len(active), "thumbnails", len(thumbs))
	c.pub.update(ctx, c.snapshot)
}

// Unscheduled broadcasts sort last; otherwise by start, then title for a stable label order.
func compareBroadcasts(a, b youtube.Broadcast) int {
	if a.ScheduledStart.IsZero() != b.ScheduledStart.IsZero() {
		if a.ScheduledStart.IsZero() {
			return 1
		}
		return -1
	}
	return cmp.Or(a.ScheduledStart.Compare(b.ScheduledStart), cmp.Compare(a.Title, b.Title))
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
	c.session.apply(b)
	if c.session.selectedID == id {
		c.session.stream = stream
		c.session.viewers = viewers
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
