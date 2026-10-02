package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// A safety bound on one button press's readback loop, not behaviour anyone tunes.
const (
	transitionWait     = 90 * time.Second
	transitionInterval = 3 * time.Second
)

// ErrNoSelection is returned while nothing is selected on the device.
var ErrNoSelection = errors.New("no broadcast selected")

// snapTopic is re-sent on failure so the Home Assistant field reverts.
func (c *Controller) editSelected(ctx context.Context, deviceID, snapTopic string, mutate func(*youtube.Broadcast)) error {
	id := c.selectedOn(deviceID)
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

func (c *Controller) selectedOn(deviceID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.session.device(deviceID); d != nil {
		return d.selectedID
	}
	return ""
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

func (c *Controller) goLive(ctx context.Context, deviceID string) error {
	id, b, stream, ok := c.selectedState(deviceID, "go_live")
	if !ok {
		return ErrNoSelection
	}
	if stage(&b, stream, pendingNone) != stageReadyToGoLive {
		c.log.Warn("command_refused", "command", "go_live", "id", id, "lifeCycleStatus", b.LifeCycleStatus, "streamStatus", stream.Status)
		c.pub.update(ctx, c.snapshot)
		return errors.New("stream is not active")
	}
	c.setPending(ctx, deviceID, pendingGoLive)
	defer c.clearPending(ctx, deviceID)

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

func (c *Controller) endStream(ctx context.Context, deviceID string) error {
	id, b, stream, ok := c.selectedState(deviceID, "end_stream")
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
	c.setPending(ctx, deviceID, pendingEnd)
	defer c.clearPending(ctx, deviceID)

	if err := c.yt.Transition(ctx, id, youtube.TransitionComplete); err != nil {
		c.log.Error("operation_failed", "operation", "transition_complete", "id", id, "error", err)
		return err
	}
	if !c.waitForLifecycle(ctx, id, youtube.LifeComplete) {
		return errors.New("broadcast did not reach complete")
	}
	c.log.Info("broadcast_completed", "id", id)
	// A finished broadcast leaves the panel at once, the same done-signal Schedule
	// and Delete give, rather than lingering until the list poll drops it.
	c.dropMissing(ctx, id)
	return nil
}

func (c *Controller) deleteSelected(ctx context.Context, deviceID string) error {
	id, b, _, ok := c.selectedState(deviceID, "delete")
	if !ok {
		return ErrNoSelection
	}
	if isLive(b) {
		c.log.Warn("command_refused", "command", "delete", "id", id, "lifeCycleStatus", b.LifeCycleStatus)
		c.pub.update(ctx, c.snapshot)
		return errors.New("broadcast is live")
	}
	return c.deleteBroadcast(ctx, id)
}

// A vanished broadcast counts as deleted so a stale list cannot make Delete fail.
func (c *Controller) deleteBroadcast(ctx context.Context, id string) error {
	if err := c.yt.DeleteBroadcast(ctx, id); err != nil && !youtube.HasReason(err, "liveBroadcastNotFound") {
		c.log.Error("operation_failed", "operation", "delete", "id", id, "error", err)
		return err
	}
	c.log.Info("broadcast_deleted", "id", id)
	c.dropMissing(ctx, id)
	return nil
}

// Commands act on the session as polled — the same state the Home Assistant gates
// were computed from; YouTube itself rejects a transition that is no longer valid.
func (c *Controller) selectedState(deviceID, op string) (string, youtube.Broadcast, youtube.StreamStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.session.device(deviceID)
	if d == nil || d.selected() == nil {
		c.log.Warn("command_refused", "command", op, "deviceId", deviceID, "reason", "no_broadcast_selected")
		return "", youtube.Broadcast{}, youtube.StreamStatus{}, false
	}
	return d.selectedID, *d.selected(), d.stream.Status, true
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

func (c *Controller) setPending(ctx context.Context, deviceID string, p pendingOp) {
	c.mu.Lock()
	if d := c.session.device(deviceID); d != nil {
		d.pending = p
	}
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
}

func (c *Controller) clearPending(ctx context.Context, deviceID string) {
	c.mu.Lock()
	if d := c.session.device(deviceID); d != nil {
		d.pending = pendingNone
	}
	// Re-armed so the post-transition stage lands without another tap.
	c.session.armFast(c.now(), c.opts.FastRefreshDuration)
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickPoll()
}

func (c *Controller) dropMissing(ctx context.Context, id string) {
	c.mu.Lock()
	c.session.removeBroadcast(id)
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	c.kickPoll()
}
