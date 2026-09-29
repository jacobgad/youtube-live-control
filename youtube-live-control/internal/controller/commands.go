package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// Transition readback pacing is a safety timeout, not behaviour anyone tunes:
// it only bounds how long a single button press keeps re-reading the API.
const (
	transitionWait     = 90 * time.Second
	transitionInterval = 3 * time.Second
)

// save applies the draft title, scheduled start and thumbnail to the selected
// broadcast: verify it still exists, write, then read back before publishing.
func (c *Controller) save(ctx context.Context) {
	c.mu.Lock()
	id := c.s.selectedID
	title := c.s.draftTitle
	start := c.s.draftStart
	thumbnail := c.s.thumbnail
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
	c.s.apply(readback)
	if c.s.selectedID == id {
		c.s.loadDrafts()
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

// create inserts a new scheduled broadcast from the drafts, binds it to the channel's
// persistent stream key, then reads it back and selects it.
func (c *Controller) create(ctx context.Context) {
	c.mu.Lock()
	selected := c.s.selectedID
	title := c.s.draftTitle
	start := c.s.draftStart
	thumbnail := c.s.thumbnail
	c.mu.Unlock()
	if selected != "" {
		c.log.Warn("create_rejected", "reason", "existing_broadcast_selected")
		return
	}
	if strings.TrimSpace(title) == "" {
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
	c.s.apply(readback)
	c.s.selectedID = readback.ID
	c.s.loadDrafts()
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}

// goLive transitions the selected broadcast to live. The gate is re-verified against
// the API first: the stream must be actively receiving before YouTube will accept it.
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
		// A monitor-enabled broadcast (created in YouTube Studio) cannot jump
		// ready → live; it must pass through testing first.
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

// endStream completes the selected broadcast. The stream must have stopped first:
// while streamStatus is still active (it lags OBS by up to a minute) the press is
// refused and the status sensor keeps showing the waiting state.
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

// verifySelected re-reads the selected broadcast and its bound stream so command
// decisions rest on the API's current view, not the last poll.
func (c *Controller) verifySelected(ctx context.Context, op string) (string, youtube.Broadcast, youtube.StreamStatus, bool) {
	c.mu.Lock()
	id := c.s.selectedID
	c.mu.Unlock()
	if id == "" {
		c.log.Warn(op+"_refused", "reason", "no_broadcast_selected")
		return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
	}
	b, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil {
		c.log.Error("operation_failed", "operation", op+"_verify", "id", id, "error", err)
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
			c.log.Error("operation_failed", "operation", op+"_stream_status", "id", id, "error", err)
			return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
		}
	}
	c.mu.Lock()
	c.s.apply(b)
	if c.s.selectedID == id {
		c.s.stream = stream
	}
	c.mu.Unlock()
	return id, b, stream, true
}

// waitForLifecycle polls the broadcast after a transition until YouTube reports the
// target lifecycle, publishing each verified read; the sensors move only on readback.
func (c *Controller) waitForLifecycle(ctx context.Context, id, want string) bool {
	deadline := c.now().Add(transitionWait)
	for {
		b, ok, err := c.yt.GetBroadcast(ctx, id)
		if err != nil {
			c.log.Warn("transition_readback_unavailable", "id", id, "error", err)
		} else if !ok {
			c.log.Warn("broadcast_missing", "id", id)
			return false
		} else {
			c.mu.Lock()
			c.s.apply(b)
			c.mu.Unlock()
			c.pub.update(ctx, c.snapshot)
			if b.LifeCycleStatus == want {
				return true
			}
		}
		if c.now().After(deadline) {
			c.log.Warn("transition_readback_timeout", "id", id, "want", want)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(transitionInterval):
		}
	}
}

func (c *Controller) setPending(ctx context.Context, p pendingOp) {
	c.mu.Lock()
	c.s.pending = p
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) clearPending(ctx context.Context) {
	c.mu.Lock()
	c.s.pending = pendingNone
	// A finished transition re-arms the window so post-Go-Live health and viewers
	// land immediately without another interaction.
	c.s.armFast(c.now())
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}

// deselectMissing drops a broadcast that vanished from YouTube (deleted in Studio)
// and returns the panel to "New stream…".
func (c *Controller) deselectMissing(ctx context.Context, id string) {
	c.mu.Lock()
	kept := c.s.broadcasts[:0]
	for _, b := range c.s.broadcasts {
		if b.ID != id {
			kept = append(kept, b)
		}
	}
	c.s.setBroadcasts(kept)
	if c.s.selectedID == id {
		c.s.selectedID = ""
		c.s.pending = pendingNone
		c.s.loadDrafts()
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

// nextQuarterHour is the default scheduled start for Create: YouTube insists on
// one, and the next :00/:15/:30/:45 reads naturally on the watch page.
func nextQuarterHour(now time.Time) time.Time {
	floor := now.Truncate(15 * time.Minute)
	if floor.Equal(now) {
		return floor
	}
	return floor.Add(15 * time.Minute)
}

// uploadThumbnail uploads the chosen file, if any; failures are logged but do not
// fail the surrounding save or create.
func (c *Controller) uploadThumbnail(ctx context.Context, videoID, label string) {
	if label == KeepCurrentLabel || label == "" {
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
