// Package controller owns the Home Assistant session, polling and every command's
// write → read back path; nothing is published optimistically.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

const (
	mqttStartupWait = 10 * time.Second
	commandBuffer   = 8
)

// Lock scopes name what an in-flight write makes unavailable: each stream device is
// its own scope (keyed by DeviceID), scheduling is one, and web operations touching
// nothing shown in Home Assistant share a fallback scope for double-submit protection.
const (
	scopeScheduling = "scheduling"
	scopeWeb        = "web"
)

// settingPublishedDevices remembers which stream devices were announced to Home
// Assistant, so keys deleted while the add-on was off still get retired.
const settingPublishedDevices = "published_devices"

// Deps are the controller's collaborators.
type Deps struct {
	YouTube *youtube.Client
	Auth    *youtube.Auth
	MQTT    mqtt.Connection
	Store   *store.Store
	Options config.Options
	Log     *slog.Logger
	Origin  mqtt.Origin
	Now     func() time.Time
}

// Controller is the add-on's core; New, then Start and Stop.
type Controller struct {
	yt    *youtube.Client
	auth  *youtube.Auth
	mqtt  mqtt.Connection
	store *store.Store
	opts  config.Options
	log   *slog.Logger
	now   func() time.Time
	pub   *publisher

	mu        sync.Mutex
	session   session
	locked    map[string]bool
	published []string

	ops      chan queuedOp
	pollKick chan struct{}

	// lifetime spans New to Stop. It is the one context this type owns: broker callbacks
	// arrive with no context of their own and may fire before Start.
	lifetime context.Context
	endLife  context.CancelFunc
	inflight sync.WaitGroup
	started  atomic.Bool
	opsDone  chan struct{}
	pollDone chan struct{}
}

type queuedOp struct {
	name   string
	scopes []string
	run    func(context.Context) error
	done   chan error
}

// New wires the controller; nothing talks to YouTube until Start.
func New(deps Deps) *Controller {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	c := &Controller{
		yt:       deps.YouTube,
		auth:     deps.Auth,
		mqtt:     deps.MQTT,
		store:    deps.Store,
		opts:     deps.Options,
		log:      log,
		now:      now,
		pub:      newPublisher(deps.MQTT, deps.Origin, log),
		locked:   map[string]bool{},
		ops:      make(chan queuedOp, commandBuffer),
		pollKick: make(chan struct{}, 1),
		opsDone:  make(chan struct{}),
		pollDone: make(chan struct{}),
	}
	c.lifetime, c.endLife = context.WithCancel(context.Background())

	c.mqtt.OnMessage(mqtt.NewRouter(mqtt.Actions{
		BroadcastSelected: c.selectBroadcast,
		TitleEntered:      c.enterTitle,
		PrivacySelected:   c.selectPrivacy,
		FastModeSwitched:  c.switchFastMode,
		GoLivePressed: func(deviceID string) {
			c.pressed("go_live", deviceID, func(ctx context.Context) error { return c.goLive(ctx, deviceID) })
		},
		EndPressed: func(deviceID string) {
			c.pressed("end_stream", deviceID, func(ctx context.Context) error { return c.endStream(ctx, deviceID) })
		},
		DeletePressed: func(deviceID string) {
			c.pressed("delete", deviceID, func(ctx context.Context) error { return c.deleteSelected(ctx, deviceID) })
		},
		PresetSelected:  c.selectPreset,
		StartEntered:    c.enterStart,
		SchedulePrivacy: c.selectSchedulePrivacy,
		SchedulePressed: c.schedulePressed,
		HomeAssistantOnline: func() {
			c.background(func(ctx context.Context) { c.pub.everything(ctx, c.snapshot) })
		},
	}, log))
	c.mqtt.OnConnect(func() { c.background(c.onMQTTConnected) })
	c.auth.OnChange(func(authorized bool) {
		c.background(func(ctx context.Context) { c.authChanged(ctx, authorized) })
	})
	return c
}

// Start publishes state and begins the poll loop and command worker.
func (c *Controller) Start(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	c.session.authorized = c.auth.Authorized()
	c.mu.Unlock()
	c.loadPresets(ctx)
	c.loadPublished(ctx)
	c.log.Info("controller_started", "authorized", c.auth.Authorized(), "options", c.opts)

	waitCtx, cancel := context.WithTimeout(ctx, mqttStartupWait)
	err := c.mqtt.AwaitConnection(waitCtx)
	cancel()
	if err != nil {
		c.log.Warn("mqtt_not_ready", "detail", "continuing; state will be republished on connect")
	} else {
		c.onMQTTConnected(ctx)
	}

	if c.auth.Authorized() {
		c.identifyChannel(ctx)
		c.poll(ctx)
	}
	c.resetScheduleDefaults()
	c.pub.update(ctx, c.snapshot)

	go c.opsLoop()
	go c.pollLoop()
	return nil
}

// Stop ends background work and closes MQTT; ctx bounds the wait so a stuck API call
// cannot block shutdown.
func (c *Controller) Stop(ctx context.Context) {
	c.endLife()
	if c.started.Load() {
		waitFor(ctx, c.opsDone)
		waitFor(ctx, c.pollDone)
	}
	waitFor(ctx, whenDone(c.inflight.Wait))
	if c.mqtt.Connected() {
		c.pub.controllerOffline(ctx)
	}
	if err := c.mqtt.Close(ctx); err != nil {
		c.log.Warn("operation_failed", "operation", "mqtt_close", "error", err)
	}
	c.log.Info("controller_stopped")
}

func waitFor(ctx context.Context, done <-chan struct{}) {
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func whenDone(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

func (c *Controller) background(fn func(context.Context)) {
	c.inflight.Add(1)
	go func() {
		defer c.inflight.Done()
		fn(c.lifetime)
	}()
}

// ErrInProgress is returned while another change for the same device is in flight.
var ErrInProgress = errors.New("another change is still in progress")

// One lock per device is the real protection against double taps and overlapping
// edits there; unavailable entities and disabled forms are its visible side. The
// serial ops queue keeps writes to YouTube from ever interleaving across devices.
func (c *Controller) lock(scopes []string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range scopes {
		if c.locked[s] {
			return false
		}
	}
	for _, s := range scopes {
		c.locked[s] = true
	}
	return true
}

func (c *Controller) unlock(scopes []string) {
	c.mu.Lock()
	for _, s := range scopes {
		delete(c.locked, s)
	}
	c.mu.Unlock()
}

// The lock is taken inline so a press while its device is locked is dropped rather
// than queued behind the running change, which is what makes a double tap harmless.
func (c *Controller) enqueue(name string, scopes []string, op func(context.Context) error) {
	if !c.lock(scopes) {
		c.log.Info("command_ignored_locked", "command", name, "scopes", scopes)
		return
	}
	c.publishUpdate()
	select {
	case c.ops <- queuedOp{name: name, scopes: scopes, run: op}:
	default:
		c.unlock(scopes)
		c.log.Warn("command_dropped_busy", "command", name)
	}
}

// ctx bounds only the wait; the operation runs under lifetime. An op whose caller has
// already given up is skipped, so a timed-out web request cannot create a duplicate.
func (c *Controller) run(ctx context.Context, name string, scopes []string, op func(context.Context) error) error {
	if !c.lock(scopes) {
		return ErrInProgress
	}
	c.publishUpdate()
	done := make(chan error, 1)
	guarded := func(opCtx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return op(opCtx)
	}
	select {
	case c.ops <- queuedOp{name: name, scopes: scopes, run: guarded, done: done}:
	case <-ctx.Done():
		c.unlock(scopes)
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) opsLoop() {
	defer close(c.opsDone)
	for {
		select {
		case <-c.lifetime.Done():
			return
		case op := <-c.ops:
			c.log.Info("command_started", "command", op.name)
			err := op.run(c.lifetime)
			c.unlock(op.scopes)
			c.log.Info("command_finished", "command", op.name, "ok", err == nil)
			if op.done != nil {
				op.done <- err
			}
			c.pub.update(c.lifetime, c.snapshot)
		}
	}
}

func (c *Controller) onMQTTConnected(ctx context.Context) {
	if err := c.mqtt.Subscribe(ctx, mqtt.Subscriptions); err != nil {
		c.log.Error("operation_failed", "operation", "subscribe", "error", err)
	}
	c.pub.everything(ctx, c.snapshot)
}

func (c *Controller) authChanged(ctx context.Context, authorized bool) {
	c.mu.Lock()
	c.session.authorized = authorized
	if authorized {
		c.session.armFast(c.now(), c.opts.FastRefreshDuration)
	} else {
		c.session.channel = ""
	}
	c.mu.Unlock()
	c.log.Info("authorization_changed", "authorized", authorized)
	c.pub.update(ctx, c.snapshot)
	if authorized {
		c.identifyChannel(ctx)
		c.poll(ctx)
		c.resetScheduleDefaults()
		c.pub.update(ctx, c.snapshot)
		c.kickPoll()
	}
}

// The channel is what Google's account chooser selected, which for a Brand Account is
// not the Google account itself; surfacing it is what makes an empty list explainable.
func (c *Controller) identifyChannel(ctx context.Context) {
	channel, err := c.yt.MyChannel(ctx)
	if err != nil {
		c.log.Error("operation_failed", "operation", "identify_channel", "error", err)
		return
	}
	c.mu.Lock()
	c.session.channel = channel.Title
	c.mu.Unlock()
	c.log.Info("channel_connected", "channelId", channel.ID, "title", channel.Title)
}

func (c *Controller) loadPublished(ctx context.Context) {
	raw, ok, err := c.store.Setting(ctx, settingPublishedDevices)
	if err != nil {
		c.log.Error("operation_failed", "operation", "load_published_devices", "error", err)
		return
	}
	if !ok {
		return
	}
	var devices []string
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		c.log.Warn("published_devices_unreadable", "error", err.Error())
		return
	}
	c.mu.Lock()
	c.published = devices
	c.mu.Unlock()
}

func (c *Controller) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	snap := snapshot{
		authorized:    c.session.authorized,
		channel:       c.session.channel,
		fastMode:      c.session.fastActive(now),
		fastRemaining: c.session.fastRemainingMinutes(now),
		presets:       c.session.sched.presetOptions(),
		presetLabel:   c.session.sched.presetLabel(),
		start:         c.session.sched.startPayload(),
		schedPrivacy:  c.session.sched.privacy,
		schedUnlocked: !c.locked[scopeScheduling],
		canSchedule:   c.session.authorized && c.session.sched.canSchedule(now),
	}
	for _, d := range c.session.devices {
		snap.devices = append(snap.devices, c.deviceSnap(d))
	}
	return snap
}

func (c *Controller) deviceSnap(d *deviceState) deviceSnap {
	b := d.selected()
	current := stage(b, d.stream.Status, d.pending)
	snap := deviceSnap{
		deviceID:      d.deviceID,
		name:          d.stream.Title,
		streamKey:     d.stream.StreamKey,
		options:       d.selectOptions(),
		selectedLabel: d.selectedLabel(),
		attributes:    broadcastAttributes(b),
		stage:         current,
		live:          isOnAir(current),
		encoder:       d.stream.Status.Status == youtube.StreamActive,
		health:        healthText(d.stream.Status),
		status:        "none",
		unlocked:      !c.locked[d.deviceID],
		gates:         computeGates(c.session.authorized, current),
		canDelete:     canDelete(c.session.authorized, current),
	}
	if b != nil {
		snap.title = b.Title
		snap.privacy = b.PrivacyStatus
		snap.status = b.LifeCycleStatus
		if !b.ScheduledStart.IsZero() {
			snap.scheduledStart = b.ScheduledStart.Format(time.RFC3339)
		}
		snap.thumbnailURL = b.ThumbnailURL
	}
	return snap
}

func (c *Controller) kickPoll() {
	select {
	case c.pollKick <- struct{}{}:
	default:
	}
}

// Armed before the operation runs: a refused End press is exactly "human waiting
// for streamStatus to catch up", the moment a fast poll is wanted most.
func (c *Controller) pressed(name, deviceID string, op func(context.Context) error) {
	if !c.deviceExists(deviceID) {
		c.log.Warn("command_refused", "command", name, "deviceId", deviceID, "reason", "unknown_device")
		return
	}
	c.armFastMode(name + "_press")
	c.enqueue(name, []string{deviceID}, op)
}

func (c *Controller) deviceExists(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session.device(deviceID) != nil
}

func (c *Controller) switchFastMode(on bool) {
	c.mu.Lock()
	if on {
		c.session.armFast(c.now(), c.opts.FastRefreshDuration)
	} else {
		c.session.fastUntil = time.Time{}
	}
	c.mu.Unlock()
	c.log.Info("fast_mode_switched", "on", on)
	c.publishUpdate()
	if on {
		c.kickPoll()
	}
}

func (c *Controller) selectBroadcast(deviceID, label string) {
	c.armFastMode("broadcast_select")
	c.mu.Lock()
	d := c.session.device(deviceID)
	if d == nil {
		c.mu.Unlock()
		c.log.Warn("select_rejected", "deviceId", deviceID, "label", label, "reason", "unknown_device")
		return
	}
	id, ok := d.idForLabel(label)
	if !ok {
		c.mu.Unlock()
		c.log.Warn("select_rejected", "deviceId", deviceID, "label", label, "reason", "unknown_option")
		c.snapBack(mqtt.StreamTopics{Device: deviceID}.BroadcastState())
		return
	}
	d.selectedID = id
	d.pending = pendingNone
	c.mu.Unlock()
	c.log.Info("broadcast_selected", "deviceId", deviceID, "id", id, "label", label)
	c.publishUpdate()
	c.kickPoll()
}

func (c *Controller) enterTitle(deviceID, raw string) {
	if !c.deviceExists(deviceID) {
		c.log.Warn("title_rejected", "deviceId", deviceID, "reason", "unknown_device")
		return
	}
	c.armFastMode("title")
	title := strings.TrimSpace(raw)
	topic := mqtt.StreamTopics{Device: deviceID}.TitleState()
	if length := utf8.RuneCountInString(title); length == 0 || length > mqtt.MaxTitleLength {
		c.log.Warn("title_rejected", "deviceId", deviceID, "length", length)
		c.snapBack(topic)
		return
	}
	c.enqueue("title", []string{deviceID}, func(ctx context.Context) error {
		return c.editSelected(ctx, deviceID, topic, func(b *youtube.Broadcast) { b.Title = title })
	})
}

func (c *Controller) selectPrivacy(deviceID, privacy string) {
	if !c.deviceExists(deviceID) {
		c.log.Warn("privacy_rejected", "deviceId", deviceID, "reason", "unknown_device")
		return
	}
	c.armFastMode("privacy")
	topic := mqtt.StreamTopics{Device: deviceID}.PrivacyState()
	if !youtube.ValidPrivacy(privacy) {
		c.log.Warn("privacy_rejected", "deviceId", deviceID, "payload", privacy)
		c.snapBack(topic)
		return
	}
	c.enqueue("privacy", []string{deviceID}, func(ctx context.Context) error {
		return c.editSelected(ctx, deviceID, topic, func(b *youtube.Broadcast) { b.PrivacyStatus = privacy })
	})
}

func (c *Controller) armFastMode(reason string) {
	c.mu.Lock()
	c.session.armFast(c.now(), c.opts.FastRefreshDuration)
	c.mu.Unlock()
	c.log.Debug("fast_mode_armed", "reason", reason)
	c.kickPoll()
}

func (c *Controller) publishUpdate() {
	c.background(func(ctx context.Context) { c.pub.update(ctx, c.snapshot) })
}

func (c *Controller) snapBack(topics ...string) {
	c.pub.invalidate(topics...)
	c.publishUpdate()
}

// PresetsChanged reloads presets after the web UI changes them.
func (c *Controller) PresetsChanged(ctx context.Context) {
	c.loadPresets(ctx)
	c.resetScheduleDefaults()
	c.publishUpdate()
}

func (c *Controller) loadPresets(ctx context.Context) {
	list, err := c.store.ListPresets(ctx)
	if err != nil {
		c.log.Error("presets_list_failed", "error", err.Error())
		return
	}
	c.mu.Lock()
	c.session.sched.setPresets(list)
	c.mu.Unlock()
}

func (c *Controller) resetScheduleDefaults() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session.sched.applyDefaults(c.now(), c.session.allStarts())
}

func (c *Controller) selectPreset(label string) {
	c.mu.Lock()
	id, ok := c.session.sched.presetIDForLabel(label)
	if !ok {
		c.mu.Unlock()
		c.log.Warn("preset_rejected", "label", label, "reason", "unknown_option")
		c.snapBack(mqtt.PresetState)
		return
	}
	c.session.sched.presetID = id
	c.session.sched.applyDefaults(c.now(), c.session.allStarts())
	c.mu.Unlock()
	c.log.Info("preset_selected", "id", id, "name", label)
	c.publishUpdate()
}

func (c *Controller) selectSchedulePrivacy(privacy string) {
	if !youtube.ValidPrivacy(privacy) {
		c.log.Warn("privacy_rejected", "payload", privacy)
		c.snapBack(mqtt.SchedulePrivacyState)
		return
	}
	c.mu.Lock()
	c.session.sched.privacy = privacy
	c.mu.Unlock()
	c.publishUpdate()
}

func (c *Controller) enterStart(raw string) {
	start, err := parseStart(raw)
	if err != nil {
		c.log.Warn("start_rejected", "payload", raw, "error", err.Error())
		c.snapBack(mqtt.StartState)
		return
	}
	c.mu.Lock()
	c.session.sched.start = start
	c.mu.Unlock()
	c.publishUpdate()
}

// Scheduling never touches a device's selection; only a human does that. The create
// locks the Scheduling device alone: the target key's device keeps working and its
// select gains the new broadcast on readback.
func (c *Controller) schedulePressed() {
	c.mu.Lock()
	p := c.session.sched.preset()
	start := c.session.sched.start
	privacy := c.session.sched.privacy
	ok := c.session.sched.canSchedule(c.now())
	c.mu.Unlock()
	if !ok || p == nil {
		c.log.Warn("command_refused", "command", "schedule", "reason", "gate_closed")
		c.publishUpdate()
		return
	}
	req := NewBroadcast{Edit: Edit{Title: p.Title(start), Description: p.Description, Start: start, Privacy: privacy, StreamID: p.StreamID, CategoryID: p.CategoryID}}
	if p.ImageID != "" {
		image, ct, err := c.store.ImageBytes(c.lifetime, p.ImageID)
		if err != nil {
			c.log.Warn("preset_image_unreadable", "preset", p.ID, "image", p.ImageID, "error", err.Error())
		} else {
			req.Thumbnail, req.ThumbnailType = image, ct
		}
	}
	c.enqueue("schedule", []string{scopeScheduling}, func(ctx context.Context) error {
		if _, err := c.createBroadcast(ctx, req); err != nil {
			return err
		}
		// Cleared entities are the visible confirmation that the press worked.
		c.mu.Lock()
		c.session.sched.clear()
		c.mu.Unlock()
		c.pub.update(ctx, c.snapshot)
		return nil
	})
}
