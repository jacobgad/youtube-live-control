package controller

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// A safety bound on one button press's readback loop, not behaviour anyone tunes.
const (
	transitionWait     = 90 * time.Second
	transitionInterval = 3 * time.Second
)

func (c *Controller) save(ctx context.Context) {
	c.mu.Lock()
	id := c.session.selectedID
	title := c.session.draftTitle
	start := c.session.draftStart
	thumbnail := c.session.thumbnail
	c.mu.Unlock()
	if id == "" {
		c.log.Warn("save_rejected", "reason", "no_broadcast_selected")
		return
	}

	fresh, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil {
		c.log.Error("operation_failed", "operation", "save_verify", "id", id, "error", err)
		return
	}
	if !ok {
		c.log.Warn("broadcast_missing", "id", id)
		c.deselectMissing(ctx, id)
		return
	}
	if title != "" {
		fresh.Title = title
	}
	if !start.IsZero() {
		fresh.ScheduledStart = start
	}
	if _, err := c.yt.UpdateBroadcast(ctx, fresh); err != nil {
		c.log.Error("operation_failed", "operation", "save_update", "id", id, "error", err)
		return
	}
	c.uploadThumbnail(ctx, id, thumbnail)

	readback, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil || !ok {
		c.log.Error("operation_failed", "operation", "save_readback", "id", id, "error", err)
		return
	}
	c.log.Info("broadcast_saved", "id", id, "title", readback.Title, "scheduledStart", readback.ScheduledStart)
	c.mu.Lock()
	c.session.apply(readback)
	if c.session.selectedID == id {
		c.session.loadDrafts()
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) create(ctx context.Context) {
	c.mu.Lock()
	selected := c.session.selectedID
	title := c.session.draftTitle
	start := c.session.draftStart
	thumbnail := c.session.thumbnail
	c.mu.Unlock()
	if selected != "" {
		c.log.Warn("create_rejected", "reason", "existing_broadcast_selected")
		return
	}
	if title == "" {
		c.log.Warn("create_rejected", "reason", "missing_title")
		return
	}
	if start.IsZero() {
		start = nextQuarterHour(c.now())
		c.log.Info("create_default_start", "scheduledStart", start)
	}

	created, err := c.yt.InsertBroadcast(ctx, title, start, c.opts.Privacy)
	if err != nil {
		c.log.Error("operation_failed", "operation", "create_insert", "error", err)
		return
	}
	c.log.Info("broadcast_created", "id", created.ID, "title", title, "scheduledStart", start, "privacy", c.opts.Privacy)

	streamID, err := c.yt.DefaultStreamID(ctx)
	switch {
	case err != nil:
		c.log.Error("operation_failed", "operation", "create_find_stream", "id", created.ID, "error", err)
	case streamID == "":
		c.log.Warn("no_stream_key", "id", created.ID, "detail", "channel has no liveStream; Go Live stays unavailable until one is bound")
	default:
		if err := c.yt.Bind(ctx, created.ID, streamID); err != nil {
			c.log.Error("operation_failed", "operation", "create_bind", "id", created.ID, "streamId", streamID, "error", err)
		} else {
			c.log.Info("broadcast_bound", "id", created.ID, "streamId", streamID)
		}
	}
	c.uploadThumbnail(ctx, created.ID, thumbnail)

	readback, ok, err := c.yt.GetBroadcast(ctx, created.ID)
	if err != nil || !ok {
		c.log.Error("operation_failed", "operation", "create_readback", "id", created.ID, "error", err)
		readback = created
	}
	c.mu.Lock()
	c.session.apply(readback)
	c.session.selectedID = readback.ID
	c.session.loadDrafts()
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}

func (c *Controller) goLive(ctx context.Context) {
	id, b, stream, ok := c.verifySelected(ctx, "go_live")
	if !ok {
		return
	}
	g := computeGates(true, &b, stream, false)
	if !g.goLive {
		c.log.Warn("go_live_refused", "id", id, "lifeCycleStatus", b.LifeCycleStatus, "streamStatus", stream.Status)
		c.pub.update(ctx, c.snapshot)
		return
	}
	c.setPending(ctx, pendingGoLive)
	defer c.clearPending(ctx)

	err := c.yt.Transition(ctx, id, youtube.TransitionLive)
	if err != nil && youtube.HasReason(err, "invalidTransition") && b.MonitorEnabled {
		// Studio-created broadcasts keep a monitor stream and cannot jump ready → live.
		c.log.Info("go_live_via_testing", "id", id)
		if err := c.yt.Transition(ctx, id, youtube.TransitionTesting); err != nil {
			c.log.Error("operation_failed", "operation", "transition_testing", "id", id, "error", err)
			return
		}
		if !c.waitForLifecycle(ctx, id, youtube.LifeTesting) {
			return
		}
		err = c.yt.Transition(ctx, id, youtube.TransitionLive)
	}
	if err != nil {
		c.log.Error("operation_failed", "operation", "transition_live", "id", id, "error", err)
		return
	}
	if c.waitForLifecycle(ctx, id, youtube.LifeLive) {
		c.log.Info("broadcast_live", "id", id)
	}
}

func (c *Controller) endStream(ctx context.Context) {
	id, b, stream, ok := c.verifySelected(ctx, "end_stream")
	if !ok {
		return
	}
	if b.LifeCycleStatus != youtube.LifeLive && b.LifeCycleStatus != youtube.LifeLiveStarting {
		c.log.Warn("end_stream_refused", "id", id, "lifeCycleStatus", b.LifeCycleStatus)
		c.pub.update(ctx, c.snapshot)
		return
	}
	if stream.Status == youtube.StreamActive {
		c.log.Warn("end_stream_refused", "id", id, "reason", "stream_still_active", "detail", "waiting for stream to stop")
		c.pub.update(ctx, c.snapshot)
		return
	}
	c.setPending(ctx, pendingEnd)
	defer c.clearPending(ctx)

	if err := c.yt.Transition(ctx, id, youtube.TransitionComplete); err != nil {
		c.log.Error("operation_failed", "operation", "transition_complete", "id", id, "error", err)
		return
	}
	if c.waitForLifecycle(ctx, id, youtube.LifeComplete) {
		c.log.Info("broadcast_completed", "id", id)
	}
}

// Commands decide on a fresh read, never on the last poll.
func (c *Controller) verifySelected(ctx context.Context, op string) (string, youtube.Broadcast, youtube.StreamStatus, bool) {
	c.mu.Lock()
	id := c.session.selectedID
	c.mu.Unlock()
	if id == "" {
		c.log.Warn("command_refused", "command", op, "reason", "no_broadcast_selected")
		return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
	}
	b, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil {
		c.log.Error("operation_failed", "operation", "verify", "command", op, "id", id, "error", err)
		return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
	}
	if !ok {
		c.log.Warn("broadcast_missing", "id", id)
		c.deselectMissing(ctx, id)
		return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
	}
	var stream youtube.StreamStatus
	if b.BoundStreamID != "" {
		stream, err = c.yt.StreamStatus(ctx, b.BoundStreamID)
		if err != nil {
			c.log.Error("operation_failed", "operation", "stream_status", "command", op, "id", id, "error", err)
			return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
		}
	}
	c.mu.Lock()
	c.session.apply(b)
	if c.session.selectedID == id {
		c.session.stream = stream
	}
	c.mu.Unlock()
	return id, b, stream, true
}

func (c *Controller) waitForLifecycle(ctx context.Context, id, want string) bool {
	ctx, cancel := context.WithTimeout(ctx, transitionWait)
	defer cancel()
	for {
		b, ok, err := c.yt.GetBroadcast(ctx, id)
		if err != nil {
			c.log.Warn("transition_readback_unavailable", "id", id, "error", err)
		} else if !ok {
			c.log.Warn("broadcast_missing", "id", id)
			return false
		} else {
			c.mu.Lock()
			c.session.apply(b)
			c.mu.Unlock()
			c.pub.update(ctx, c.snapshot)
			if b.LifeCycleStatus == want {
				return true
			}
		}
		select {
		case <-ctx.Done():
			c.log.Warn("transition_readback_timeout", "id", id, "want", want)
			return false
		case <-time.After(transitionInterval):
		}
	}
}

func (c *Controller) setPending(ctx context.Context, p pendingOp) {
	c.mu.Lock()
	c.session.pending = p
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) clearPending(ctx context.Context) {
	c.mu.Lock()
	c.session.pending = pendingNone
	// Re-armed so post-transition health and viewers land without another tap.
	c.session.armFast(c.now())
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}

func (c *Controller) deselectMissing(ctx context.Context, id string) {
	c.mu.Lock()
	c.session.setBroadcasts(slices.DeleteFunc(c.session.broadcasts, func(b youtube.Broadcast) bool { return b.ID == id }))
	if c.session.selectedID == id {
		c.session.selectedID = ""
		c.session.pending = pendingNone
		c.session.loadDrafts()
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

// YouTube insists on a scheduled start; the next :00/:15/:30/:45 reads naturally on the watch page.
func nextQuarterHour(now time.Time) time.Time {
	floor := now.Truncate(15 * time.Minute)
	if floor.Equal(now) {
		return floor
	}
	return floor.Add(15 * time.Minute)
}

// A failed thumbnail must not fail the save or create it rides on.
func (c *Controller) uploadThumbnail(ctx context.Context, videoID, label string) {
	if label == keepCurrentLabel || label == "" {
		return
	}
	path := filepath.Join(c.opts.ThumbnailsDir, filepath.Base(label))
	image, err := os.ReadFile(path) //nolint:gosec // constrained to the configured thumbnails directory
	if err != nil {
		c.log.Error("operation_failed", "operation", "thumbnail_read", "path", path, "error", err)
		return
	}
	contentType := "image/jpeg"
	if strings.EqualFold(filepath.Ext(label), ".png") {
		contentType = "image/png"
	}
	if err := c.yt.SetThumbnail(ctx, videoID, contentType, image); err != nil {
		c.log.Error("operation_failed", "operation", "thumbnail_upload", "videoId", videoID, "file", label, "error", err)
		return
	}
	c.log.Info("thumbnail_uploaded", "videoId", videoID, "file", label)
}
