// Package controller owns the Home Assistant session, polling and every command's
// verify → write → read back path; nothing is published optimistically.
package controller

import (
	"context"
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

	mu      sync.Mutex
	session session
	busy    map[string]bool

	ops        chan queuedOp
	statusKick chan struct{}

	// lifetime spans New to Stop. It is the one context this type owns: broker callbacks
	// arrive with no context of their own and may fire before Start.
	lifetime context.Context
	endLife  context.CancelFunc
	inflight sync.WaitGroup
	started  atomic.Bool
	opsDone  chan struct{}
	listDone chan struct{}
	statDone chan struct{}
}

type queuedOp struct {
	name string
	run  func(context.Context) error
	done chan error
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
		yt:         deps.YouTube,
		auth:       deps.Auth,
		mqtt:       deps.MQTT,
		store:      deps.Store,
		opts:       deps.Options,
		log:        log,
		now:        now,
		pub:        newPublisher(deps.MQTT, deps.Origin, log),
		busy:       map[string]bool{},
		ops:        make(chan queuedOp, commandBuffer),
		statusKick: make(chan struct{}, 1),
		opsDone:    make(chan struct{}),
		listDone:   make(chan struct{}),
		statDone:   make(chan struct{}),
	}
	c.lifetime, c.endLife = context.WithCancel(context.Background())

	c.mqtt.OnMessage(mqtt.NewRouter(mqtt.Actions{
		BroadcastSelected: c.selectBroadcast,
		TitleEntered:      c.enterTitle,
		PrivacySelected:   c.selectPrivacy,
		FastModeSwitched:  c.switchFastMode,
		GoLivePressed:     func() { c.pressed("go_live", c.goLive) },
		EndPressed:        func() { c.pressed("end_stream", c.endStream) },
		PresetSelected:    c.selectPreset,
		StartEntered:      c.enterStart,
		SchedulePressed:   c.schedulePressed,
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

// Start publishes state and begins the poll loops and command worker.
func (c *Controller) Start(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	c.session.authorized = c.auth.Authorized()
	c.mu.Unlock()
	c.loadPresets(ctx)
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
		c.refreshList(ctx)
	}
	c.resetScheduleDefaults()
	c.pub.update(ctx, c.snapshot)

	go c.opsLoop()
	go c.listLoop()
	go c.statusLoop()
	return nil
}

// Stop ends background work and closes MQTT; ctx bounds the wait so a stuck API call
// cannot block shutdown.
func (c *Controller) Stop(ctx context.Context) {
	c.endLife()
	if c.started.Load() {
		waitFor(ctx, c.opsDone)
		waitFor(ctx, c.listDone)
		waitFor(ctx, c.statDone)
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

// ErrInProgress is returned when the same command is already queued or running.
var ErrInProgress = errors.New("that action is already in progress")

// One in-flight instance per command name is the real protection against double
// taps; greyed buttons and disabled forms are only the visible side of it.
func (c *Controller) claim(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[name] {
		return false
	}
	c.busy[name] = true
	return true
}

func (c *Controller) release(name string) {
	c.mu.Lock()
	delete(c.busy, name)
	c.mu.Unlock()
}

// The slot is claimed inline so presses run in press order; a duplicate press or a
// full queue drops the press rather than block the broker's delivery goroutine.
func (c *Controller) enqueue(name string, op func(context.Context) error) {
	if !c.claim(name) {
		c.log.Info("command_ignored_in_progress", "command", name)
		return
	}
	c.publishUpdate()
	select {
	case c.ops <- queuedOp{name: name, run: op}:
	default:
		c.release(name)
		c.log.Warn("command_dropped_busy", "command", name)
	}
}

// Edits are not single-flight: a second title typed while the first is being written
// must still land, and the readback settles the field either way.
func (c *Controller) enqueueEdit(name string, op func(context.Context) error) {
	select {
	case c.ops <- queuedOp{name: name, run: op}:
	default:
		c.log.Warn("command_dropped_busy", "command", name)
	}
}

// ctx bounds only the wait; the operation runs under lifetime. An op whose caller has
// already given up is skipped, so a timed-out web request cannot create a duplicate.
func (c *Controller) run(ctx context.Context, name string, op func(context.Context) error) error {
	if !c.claim(name) {
		return ErrInProgress
	}
	done := make(chan error, 1)
	guarded := func(opCtx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return op(opCtx)
	}
	select {
	case c.ops <- queuedOp{name: name, run: guarded, done: done}:
	case <-ctx.Done():
		c.release(name)
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
			c.release(op.name)
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
		c.refreshList(ctx)
		c.resetScheduleDefaults()
		c.pub.update(ctx, c.snapshot)
		c.kickStatusPoll()
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

func (c *Controller) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.session.selected()
	now := c.now()
	current := stage(b, c.session.stream, c.session.pending)
	snap := snapshot{
		authorized: c.session.authorized,
		channel:    c.session.channel,
		options: mqtt.Options{
			Broadcasts: c.session.selectOptions(),
			Presets:    c.session.sched.presetOptions(),
		},
		selectedLabel: c.session.selectedLabel(),
		attributes:    broadcastAttributes(b),
		stage:         current,
		live:          isOnAir(current),
		encoder:       b != nil && c.session.stream.Status == youtube.StreamActive,
		health:        healthText(b, c.session.stream),
		status:        "none",
		fastMode:      c.session.fastActive(now),
		fastRemaining: c.session.fastRemainingMinutes(now),
		gates:         computeGates(c.session.authorized, current, c.busy["go_live"], c.busy["end_stream"]),
		presetLabel:   c.session.sched.presetLabel(),
		start:         c.session.sched.startPayload(),
		canSchedule:   c.session.authorized && !c.busy["schedule"] && c.session.sched.canSchedule(now),
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

func (c *Controller) kickStatusPoll() {
	select {
	case c.statusKick <- struct{}{}:
	default:
	}
}

// Armed before the operation runs: a refused End press is exactly "human waiting
// for streamStatus to catch up", the moment a fast poll is wanted most.
func (c *Controller) pressed(name string, op func(context.Context) error) {
	c.armFastMode(name + "_press")
	c.enqueue(name, op)
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
		c.kickStatusPoll()
	}
}

func (c *Controller) selectBroadcast(label string) {
	c.armFastMode("broadcast_select")
	c.mu.Lock()
	id, ok := c.session.idForLabel(label)
	if !ok {
		c.mu.Unlock()
		c.log.Warn("select_rejected", "label", label, "reason", "unknown_option")
		c.snapBack(mqtt.BroadcastState)
		return
	}
	c.session.selectedID = id
	c.session.resetLiveState()
	c.mu.Unlock()
	c.log.Info("broadcast_selected", "id", id, "label", label)
	c.publishUpdate()
	c.kickStatusPoll()
}

func (c *Controller) enterTitle(raw string) {
	c.armFastMode("title")
	title := strings.TrimSpace(raw)
	if length := utf8.RuneCountInString(title); length == 0 || length > mqtt.MaxTitleLength {
		c.log.Warn("title_rejected", "length", length)
		c.snapBack(mqtt.TitleState)
		return
	}
	c.enqueueEdit("title", func(ctx context.Context) error {
		return c.editSelected(ctx, mqtt.TitleState, func(b *youtube.Broadcast) { b.Title = title })
	})
}

func (c *Controller) selectPrivacy(privacy string) {
	c.armFastMode("privacy")
	if !youtube.ValidPrivacy(privacy) {
		c.log.Warn("privacy_rejected", "payload", privacy)
		c.snapBack(mqtt.PrivacyState)
		return
	}
	c.enqueueEdit("privacy", func(ctx context.Context) error {
		return c.editSelected(ctx, mqtt.PrivacyState, func(b *youtube.Broadcast) { b.PrivacyStatus = privacy })
	})
}

func (c *Controller) armFastMode(reason string) {
	c.mu.Lock()
	c.session.armFast(c.now(), c.opts.FastRefreshDuration)
	c.mu.Unlock()
	c.log.Debug("fast_mode_armed", "reason", reason)
	c.kickStatusPoll()
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

// Scheduling never touches the Home Assistant selection; only a human does that.
func (c *Controller) schedulePressed() {
	c.mu.Lock()
	p := c.session.sched.preset()
	start := c.session.sched.start
	ok := c.session.sched.canSchedule(c.now())
	c.mu.Unlock()
	if !ok || p == nil {
		c.log.Warn("command_refused", "command", "schedule", "reason", "gate_closed")
		c.publishUpdate()
		return
	}
	req := NewBroadcast{Edit: Edit{Title: p.Title(start), Description: p.Description, Start: start, Privacy: p.Privacy, StreamID: p.StreamID, CategoryID: p.CategoryID}}
	if p.ImageID != "" {
		image, ct, err := c.store.ImageBytes(c.lifetime, p.ImageID)
		if err != nil {
			c.log.Warn("preset_image_unreadable", "preset", p.ID, "image", p.ImageID, "error", err.Error())
		} else {
			req.Thumbnail, req.ThumbnailType = image, ct
		}
	}
	c.enqueue("schedule", func(ctx context.Context) error {
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
