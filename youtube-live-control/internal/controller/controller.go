// Package controller orchestrates the add-on: the broadcast selector session behind
// the Home Assistant panel, quota-aware polling of the YouTube Data API, and command
// handling that verifies, writes, then reads back — never publishing optimistically.
package controller

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
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
	Options config.Options
	Log     *slog.Logger
	Origin  mqtt.Origin
	Now     func() time.Time
}

// Controller is the add-on's long-lived core. Create it with New and drive it with Start/Stop.
type Controller struct {
	yt   *youtube.Client
	auth *youtube.Auth
	mqtt mqtt.Connection
	opts config.Options
	log  *slog.Logger
	now  func() time.Time
	pub  *publisher

	mu      sync.Mutex
	session session

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
	run  func(context.Context)
}

// New wires the controller to its MQTT connection; nothing talks to YouTube until Start.
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
		opts:       deps.Options,
		log:        log,
		now:        now,
		pub:        newPublisher(deps.MQTT, deps.Origin, log),
		ops:        make(chan queuedOp, commandBuffer),
		statusKick: make(chan struct{}, 1),
		opsDone:    make(chan struct{}),
		listDone:   make(chan struct{}),
		statDone:   make(chan struct{}),
	}
	c.lifetime, c.endLife = context.WithCancel(context.Background())
	c.session.thumbnail = keepCurrentLabel

	c.mqtt.OnMessage(mqtt.NewRouter(mqtt.Actions{
		BroadcastSelected: c.selectBroadcast,
		TitleEntered:      c.enterTitle,
		ScheduledEntered:  c.enterScheduled,
		ThumbnailSelected: c.selectThumbnail,
		FastModeSwitched:  c.switchFastMode,
		SavePressed:       func() { c.pressed("save", c.save) },
		CreatePressed:     func() { c.pressed("create", c.create) },
		GoLivePressed:     func() { c.pressed("go_live", c.goLive) },
		EndPressed:        func() { c.pressed("end_stream", c.endStream) },
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

// Start publishes discovery and state, loads the broadcast list if already authorized,
// and begins the two poll loops and the command worker.
func (c *Controller) Start(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	c.session.authorized = c.auth.Authorized()
	c.mu.Unlock()
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

	go c.opsLoop()
	go c.listLoop()
	go c.statusLoop()
	return nil
}

// Stop ends background work, publishes the controller offline and closes MQTT.
// It gives up waiting when ctx expires so a stuck API call cannot block shutdown.
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

// The slot is claimed inline so presses run in press order; a full queue drops the
// press rather than block the broker's delivery goroutine.
func (c *Controller) enqueue(name string, op func(context.Context)) {
	select {
	case c.ops <- queuedOp{name: name, run: op}:
	default:
		c.log.Warn("command_dropped_busy", "command", name)
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
			op.run(c.lifetime)
			c.log.Info("command_finished", "command", op.name)
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
		c.session.armFast(c.now(), c.opts.FastModeDuration)
	} else {
		c.session.channel = ""
	}
	c.mu.Unlock()
	c.log.Info("authorization_changed", "authorized", authorized)
	c.pub.update(ctx, c.snapshot)
	if authorized {
		c.identifyChannel(ctx)
		c.refreshList(ctx)
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
	return snapshot{
		authorized:       c.session.authorized,
		broadcastOptions: c.session.selectOptions(),
		thumbnailOptions: c.session.thumbnailOptions(),
		selectedLabel:    c.session.selectedLabel(),
		title:            c.session.draftTitle,
		scheduled:        formatWhen(c.session.draftStart),
		thumbnail:        c.session.thumbnail,
		health:           healthText(b, c.session.stream),
		status:           statusText(b, c.session.stream, c.session.pending),
		viewers:          c.session.viewers,
		fastMode:         c.session.fastActive(now),
		fastRemaining:    c.session.fastRemainingMinutes(now),
		channel:          c.session.channel,
		gates:            computeGates(c.session.authorized, b, c.session.stream, c.session.pending != pendingNone),
	}
}

// Armed before the operation runs: a refused End press is exactly "human waiting
// for streamStatus to catch up", the moment a fast poll is wanted most.
func (c *Controller) pressed(name string, op func(context.Context)) {
	c.mu.Lock()
	c.session.armFast(c.now(), c.opts.FastModeDuration)
	c.mu.Unlock()
	c.log.Debug("fast_mode_armed", "reason", name+"_press")
	c.kickStatusPoll()
	c.enqueue(name, op)
}

func (c *Controller) switchFastMode(on bool) {
	c.mu.Lock()
	if on {
		c.session.armFast(c.now(), c.opts.FastModeDuration)
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

func (c *Controller) kickStatusPoll() {
	select {
	case c.statusKick <- struct{}{}:
	default:
	}
}

func (c *Controller) selectBroadcast(label string) {
	c.mu.Lock()
	c.session.armFast(c.now(), c.opts.FastModeDuration)
	id, ok := c.session.idForLabel(label)
	if !ok {
		c.mu.Unlock()
		c.log.Warn("select_rejected", "label", label, "reason", "unknown_option")
		c.snapBack(mqtt.BroadcastState)
		c.kickStatusPoll()
		return
	}
	c.session.selectedID = id
	c.session.pending = pendingNone
	c.session.loadDrafts()
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
	c.mu.Lock()
	c.session.draftTitle = title
	c.mu.Unlock()
	c.publishUpdate()
}

func (c *Controller) enterScheduled(raw string) {
	c.armFastMode("scheduled_start")
	when, err := parseWhen(raw)
	if err != nil {
		c.log.Warn("scheduled_start_rejected", "payload", raw, "error", err.Error())
		c.snapBack(mqtt.ScheduledState)
		return
	}
	c.mu.Lock()
	c.session.draftStart = when
	c.mu.Unlock()
	c.publishUpdate()
}

func (c *Controller) selectThumbnail(label string) {
	c.armFastMode("thumbnail")
	c.mu.Lock()
	if !c.session.validThumbnail(label) {
		c.mu.Unlock()
		c.log.Warn("thumbnail_rejected", "label", label, "reason", "unknown_option")
		c.snapBack(mqtt.ThumbnailState)
		return
	}
	c.session.thumbnail = label
	c.mu.Unlock()
	c.publishUpdate()
}

func (c *Controller) armFastMode(reason string) {
	c.mu.Lock()
	c.session.armFast(c.now(), c.opts.FastModeDuration)
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

func scanThumbnails(dir string) []string {
	var files []string
	for _, pattern := range []string{"*.jpg", "*.jpeg", "*.png"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			files = append(files, filepath.Base(m))
		}
	}
	slices.Sort(files)
	return files
}
