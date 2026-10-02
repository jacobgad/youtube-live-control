package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

type fakeConn struct {
	mu      sync.Mutex
	handler mqtt.MessageHandler
	states  map[string]string
	history []string
	byTopic map[string][]string
}

func newFakeConn() *fakeConn {
	return &fakeConn{states: map[string]string{}, byTopic: map[string][]string{}}
}

func (f *fakeConn) Publish(_ context.Context, topic, payload string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[topic] = payload
	f.history = append(f.history, topic+" = "+payload)
	f.byTopic[topic] = append(f.byTopic[topic], payload)
	return nil
}

func (f *fakeConn) Subscribe(context.Context, []string) error { return nil }
func (f *fakeConn) OnMessage(h mqtt.MessageHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}
func (f *fakeConn) OnConnect(func())                      {}
func (f *fakeConn) Connected() bool                       { return true }
func (f *fakeConn) AwaitConnection(context.Context) error { return nil }
func (f *fakeConn) Close(context.Context) error           { return nil }

func (f *fakeConn) deliver(topic, payload string) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	h(topic, []byte(payload))
}

func (f *fakeConn) state(topic string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[topic]
}

func (f *fakeConn) payloads(topic string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.byTopic[topic]...)
}

type fakeYouTube struct {
	latency                     time.Duration
	mu                          sync.Mutex
	lifecycle                   string
	boundStream                 string
	streamOn                    bool
	monitor                     bool
	startingFor                 int
	omitBindingBeforeTransition bool
	transitions                 []string
	log                         []string
}

func (y *fakeYouTube) broadcastJSON() string {
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{
		"id": "b1",
		"snippet": {"title": "Service", "scheduledStartTime": %q, "isDefaultBroadcast": false},
		"status": {"lifeCycleStatus": %q, "privacyStatus": "public"},
		"contentDetails": {"boundStreamId": %q, "monitorStream": {"enableMonitorStream": %v}}
	}`, start, y.lifecycle, y.boundStream, y.monitor)
}

func (y *fakeYouTube) streamJSON() string {
	status := "inactive"
	if y.streamOn {
		status = "active"
	}
	return fmt.Sprintf(`{
		"id": "s1",
		"snippet": {"title": "Main", "isDefaultStream": false},
		"cdn": {"resolution": "1080p", "frameRate": "30fps", "ingestionInfo": {"streamName": "key-1"}},
		"status": {"streamStatus": %q, "healthStatus": {"status": "good"}}
	}`, status)
}

func (y *fakeYouTube) RoundTrip(req *http.Request) (*http.Response, error) {
	if y.latency > 0 {
		time.Sleep(y.latency)
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	q := req.URL.Query()
	y.log = append(y.log, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
	respond := func(code int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    req,
		}, nil
	}
	switch {
	case req.URL.Host == "oauth2.googleapis.com":
		return respond(200, `{"access_token":"tok","expires_in":3600}`)
	case strings.HasSuffix(req.URL.Path, "/liveBroadcasts/transition"):
		want := q.Get("broadcastStatus")
		y.transitions = append(y.transitions, want)
		if want == "live" && y.monitor && y.lifecycle == "ready" {
			return respond(403, `{"error":{"message":"Invalid transition","errors":[{"reason":"invalidTransition"}]}}`)
		}
		switch want {
		case "live":
			y.lifecycle = "liveStarting"
			y.startingFor = 2
		case "testing":
			y.lifecycle = "testStarting"
			y.startingFor = 2
		default:
			y.lifecycle = want
		}
		return respond(200, `{}`)
	case strings.HasSuffix(req.URL.Path, "/liveBroadcasts"):
		if y.lifecycle == "liveStarting" || y.lifecycle == "testStarting" {
			if y.startingFor > 0 {
				y.startingFor--
			} else if y.lifecycle == "liveStarting" {
				y.lifecycle = "live"
			} else {
				y.lifecycle = "testing"
			}
		}
		if q.Get("id") == "b1" && y.omitBindingBeforeTransition && len(y.transitions) == 0 {
			saved := y.boundStream
			y.boundStream = ""
			body := `{"items":[` + y.broadcastJSON() + `]}`
			y.boundStream = saved
			return respond(200, body)
		}
		status := q.Get("broadcastStatus")
		switch {
		case q.Get("id") == "b1",
			status == "upcoming" && (y.lifecycle == "ready" || y.lifecycle == "created"),
			status == "active" && (y.lifecycle == "live" || y.lifecycle == "testing"):
			return respond(200, `{"items":[`+y.broadcastJSON()+`]}`)
		default:
			return respond(200, `{"items":[]}`)
		}
	case strings.HasSuffix(req.URL.Path, "/liveStreams"):
		return respond(200, `{"items":[`+y.streamJSON()+`]}`)
	case strings.HasSuffix(req.URL.Path, "/channels"):
		return respond(200, `{"items":[{"id":"c1","snippet":{"title":"Chan"}}]}`)
	}
	return respond(404, `{"error":{"message":"unhandled `+req.URL.Path+`"}}`)
}

type staticTokens struct{}

func (staticTokens) LoadToken(context.Context) (string, bool, error) { return "refresh", true, nil }
func (staticTokens) SaveToken(context.Context, string) error         { return nil }
func (staticTokens) ClearToken(context.Context) error                { return nil }

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestGoLivePressKeepsSelectionAndGoesLive(t *testing.T) {
	runGoLivePress(t, false, 0, time.Hour)
}

func TestGoLivePressStudioBroadcastWithMonitorStream(t *testing.T) {
	runGoLivePress(t, true, 0, time.Hour)
}

func TestGoLivePressUnderConstantFastPolling(t *testing.T) {
	runGoLivePress(t, false, 30*time.Millisecond, 20*time.Millisecond)
}

func runGoLivePress(t *testing.T, monitor bool, latency, fastRefresh time.Duration) {
	yt := &fakeYouTube{lifecycle: "ready", boundStream: "s1", streamOn: true, monitor: monitor, latency: latency, omitBindingBeforeTransition: true}
	prev := http.DefaultTransport
	http.DefaultTransport = yt
	defer func() { http.DefaultTransport = prev }()

	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db.sqlite"), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	auth := youtube.NewAuth("id", "secret", staticTokens{}, nil)
	if err := auth.Load(ctx); err != nil {
		t.Fatal(err)
	}
	conn := newFakeConn()
	c := New(Deps{
		YouTube: youtube.NewClient(auth),
		Auth:    auth,
		MQTT:    conn,
		Store:   db,
		Options: config.Options{IdleRefresh: time.Hour, LiveRefresh: time.Hour, FastRefresh: fastRefresh, FastRefreshDuration: 5 * time.Minute},
	})
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Stop(stopCtx)
	}()

	topics := mqtt.StreamTopics{Device: mqtt.DeviceID("s1")}
	label := func() string {
		c.mu.Lock()
		defer c.mu.Unlock()
		d := c.session.device(mqtt.DeviceID("s1"))
		if d == nil || len(d.labels) == 0 {
			return ""
		}
		return d.labels[0]
	}
	if !waitUntil(t, 5*time.Second, func() bool { return label() != "" }) {
		t.Fatalf("device never got a broadcast; yt log:\n%s", strings.Join(yt.log, "\n"))
	}

	conn.deliver(topics.BroadcastSet(), label())
	if !waitUntil(t, 5*time.Second, func() bool {
		return conn.state(topics.BroadcastState()) != "None" && conn.state(topics.BroadcastState()) != ""
	}) {
		t.Fatalf("broadcast never selected; state=%q", conn.state(topics.BroadcastState()))
	}
	if !waitUntil(t, 5*time.Second, func() bool { return conn.state(topics.GoLiveAvailability()) == "online" }) {
		t.Fatalf("go_live gate never opened; stage=%q", conn.state(topics.StageState()))
	}

	selectedAt := len(conn.payloads(topics.BroadcastState()))
	conn.deliver(topics.GoLivePress(), "PRESS")

	if !waitUntil(t, 15*time.Second, func() bool { return conn.state(topics.StageState()) == "live" }) {
		yt.mu.Lock()
		apiLog := strings.Join(yt.log, "\n")
		transitions := fmt.Sprintf("%v", yt.transitions)
		yt.mu.Unlock()
		t.Fatalf("never went live; stage=%q select=%q transitions=%s\napi:\n%s\nmqtt history:\n%s",
			conn.state(topics.StageState()), conn.state(topics.BroadcastState()), transitions, apiLog, strings.Join(conn.history, "\n"))
	}

	for _, p := range conn.payloads(topics.BroadcastState())[selectedAt:] {
		if p == "None" {
			t.Fatalf("broadcast select was reset during go live:\n%s", strings.Join(conn.history, "\n"))
		}
	}
}
