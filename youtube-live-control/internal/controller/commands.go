package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// A safety bound on one button press's readback loop, not behaviour anyone tunes.
const (
	transitionWait     = 90 * time.Second
	transitionInterval = 3 * time.Second
)

// ErrNoSelection is returned by panel edits while nothing is selected.
var ErrNoSelection = errors.New("no broadcast selected")

// snapTopic is re-sent on failure so the Home Assistant field reverts.
func (c *Controller) editSelected(ctx context.Context, snapTopic string, mutate func(*youtube.Broadcast)) error {
	c.mu.Lock()
	id := c.session.selectedID
	c.mu.Unlock()
	if id == "" {
		c.snapBack(snapTopic)
		return ErrNoSelection
	}
	if _, err := c.updateBroadcast(ctx, id, mutate); err != nil {
		c.snapBack(snapTopic)
		return err
	}
	return nil
}

func (c *Controller) updateBroadcast(ctx context.Context, id string, mutate func(*youtube.Broadcast)) (youtube.Broadcast, error) {
	fresh, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil {
		c.log.Error("operation_failed", "operation", "update_verify", "id", id, "error", err)
		return youtube.Broadcast{}, err
	}
	if !ok {
		c.log.Warn("broadcast_missing", "id", id)
		c.dropMissing(ctx, id)
		return youtube.Broadcast{}, errors.New("broadcast no longer exists on YouTube")
	}
	mutate(&fresh)
	if _, err := c.yt.UpdateBroadcast(ctx, fresh); err != nil {
		c.log.Error("operation_failed", "operation", "update_write", "id", id, "error", err)
		return youtube.Broadcast{}, err
	}
	readback, ok, err := c.yt.GetBroadcast(ctx, id)
	if err != nil || !ok {
		c.log.Error("operation_failed", "operation", "update_readback", "id", id, "error", err)
		return youtube.Broadcast{}, fmt.Errorf("read back after update: %w", err)
	}
	c.log.Info("broadcast_updated", "id", id, "title", readback.Title, "privacy", readback.PrivacyStatus, "scheduledStart", readback.ScheduledStart)
	c.applyAndPublish(ctx, readback)
	return readback, nil
}

func (c *Controller) applyAndPublish(ctx context.Context, b youtube.Broadcast) {
	c.mu.Lock()
	c.session.apply(b)
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) goLive(ctx context.Context) error {
	id, b, stream, ok := c.verifySelected(ctx, "go_live")
	if !ok {
		return ErrNoSelection
	}
	if stage(&b, stream, pendingNone) != stageReadyToGoLive {
		c.log.Warn("command_refused", "command", "go_live", "id", id, "lifeCycleStatus", b.LifeCycleStatus, "streamStatus", stream.Status)
		c.pub.update(ctx, c.snapshot)
		return errors.New("stream is not active")
	}
	c.setPending(ctx, pendingGoLive)
	defer c.clearPending(ctx)

	err := c.yt.Transition(ctx, id, youtube.TransitionLive)
	if err != nil && youtube.HasReason(err, "invalidTransition") && b.MonitorEnabled {
		// Studio-created broadcasts keep a monitor stream and cannot jump ready → live.
		c.log.Info("go_live_via_testing", "id", id)
		if err := c.yt.Transition(ctx, id, youtube.TransitionTesting); err != nil {
			c.log.Error("operation_failed", "operation", "transition_testing", "id", id, "error", err)
			return err
		}
		if !c.waitForLifecycle(ctx, id, youtube.LifeTesting) {
			return errors.New("broadcast did not reach testing")
		}
		err = c.yt.Transition(ctx, id, youtube.TransitionLive)
	}
	if err != nil {
		c.log.Error("operation_failed", "operation", "transition_live", "id", id, "error", err)
		return err
	}
	if !c.waitForLifecycle(ctx, id, youtube.LifeLive) {
		return errors.New("broadcast did not reach live")
	}
	c.log.Info("broadcast_live", "id", id)
	return nil
}

func (c *Controller) endStream(ctx context.Context) error {
	id, b, stream, ok := c.verifySelected(ctx, "end_stream")
	if !ok {
		return ErrNoSelection
	}
	if !isLive(b) {
		c.log.Warn("command_refused", "command", "end_stream", "id", id, "lifeCycleStatus", b.LifeCycleStatus)
		c.pub.update(ctx, c.snapshot)
		return errors.New("broadcast is not live")
	}
	if stream.Status == youtube.StreamActive {
		c.log.Warn("command_refused", "command", "end_stream", "id", id, "reason", "stream_still_active")
		c.pub.update(ctx, c.snapshot)
		return errors.New("stream is still active; stop the encoder first")
	}
	c.setPending(ctx, pendingEnd)
	defer c.clearPending(ctx)

	if err := c.yt.Transition(ctx, id, youtube.TransitionComplete); err != nil {
		c.log.Error("operation_failed", "operation", "transition_complete", "id", id, "error", err)
		return err
	}
	if !c.waitForLifecycle(ctx, id, youtube.LifeComplete) {
		return errors.New("broadcast did not reach complete")
	}
	c.log.Info("broadcast_completed", "id", id)
	return nil
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
		c.dropMissing(ctx, id)
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
			c.applyAndPublish(ctx, b)
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
	// Re-armed so the post-transition stage lands without another tap.
	c.session.armFast(c.now(), c.opts.FastModeDuration)
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}

func (c *Controller) dropMissing(ctx context.Context, id string) {
	c.mu.Lock()
	remaining := slices.DeleteFunc(slices.Concat(c.session.broadcasts, c.session.stale), func(b youtube.Broadcast) bool { return b.ID == id })
	c.session.setBroadcasts(remaining, c.now())
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickStatusPoll()
}
