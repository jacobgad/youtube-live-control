package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/jacobgad/youtube-live-control/internal/config"
)

// PahoOptions configures the production Connection.
type PahoOptions struct {
	Settings config.MQTT
	ClientID string
	Will     Will
	Log      *slog.Logger
}

type pahoConnection struct {
	cm        *autopaho.ConnectionManager
	cancel    context.CancelFunc
	log       *slog.Logger
	mu        sync.RWMutex
	connected bool
	onMessage []MessageHandler
	onConnect []func()
}

// Connect starts a self-reconnecting MQTT 5 session that lives until Close is called;
// ctx only bounds the initial setup, otherwise cancelling it would tear the session
// down with a clean DISCONNECT and suppress the Last Will.
func Connect(ctx context.Context, opts PahoOptions) (Connection, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	pc := &pahoConnection{log: opts.Log}
	cfg, err := clientConfig(opts, pc)
	if err != nil {
		return nil, err
	}
	opts.Log.Info("mqtt_connecting", "host", opts.Settings.Host, "port", opts.Settings.Port, "tls", opts.Settings.TLS)
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cm, err := autopaho.NewConnection(sessionCtx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	pc.cm = cm
	pc.cancel = cancel
	return pc, nil
}

func clientConfig(opts PahoOptions, pc *pahoConnection) (autopaho.ClientConfig, error) {
	scheme := "mqtt"
	var tlsCfg *tls.Config
	if opts.Settings.TLS {
		scheme = "tls"
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	serverURL, err := url.Parse(fmt.Sprintf("%s://%s:%d", scheme, opts.Settings.Host, opts.Settings.Port))
	if err != nil {
		return autopaho.ClientConfig{}, err
	}
	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		TlsCfg:                        tlsCfg,
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ReconnectBackoff:              autopaho.NewExponentialBackoff(2*time.Second, 60*time.Second, 5*time.Second, 2),
		ConnectTimeout:                10 * time.Second,
		ConnectUsername:               opts.Settings.Username,
		ConnectPassword:               []byte(opts.Settings.Password),
		WillMessage:                   &paho.WillMessage{Topic: opts.Will.Topic, Payload: []byte(opts.Will.Payload), QoS: 1, Retain: true},
		OnConnectionUp: func(_ *autopaho.ConnectionManager, _ *paho.Connack) {
			opts.Log.Info("mqtt_connected", "host", opts.Settings.Host, "port", opts.Settings.Port)
			pc.setConnected(true)
			for _, h := range pc.connectHandlers() {
				h()
			}
		},
		OnConnectionDown: func() bool {
			opts.Log.Warn("mqtt_disconnected")
			pc.setConnected(false)
			return true
		},
		OnConnectError: func(err error) {
			opts.Log.Error("mqtt_connect_error", "error", err.Error())
		},
		ClientConfig: paho.ClientConfig{
			ClientID: opts.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					// Retained deliveries are broker replays, never live input; a stale
					// command replayed at (re)subscribe must not touch the broadcast.
					if pr.Packet.Retain {
						opts.Log.Warn("mqtt_retained_message_dropped", "topic", pr.Packet.Topic)
						return true, nil
					}
					for _, h := range pc.messageHandlers() {
						h(pr.Packet.Topic, pr.Packet.Payload)
					}
					return true, nil
				},
			},
			OnClientError: func(err error) { opts.Log.Warn("mqtt_client_error", "error", err.Error()) },
		},
	}
	if opts.Settings.Password == "" {
		cfg.ConnectPassword = nil
	}
	return cfg, nil
}

func (p *pahoConnection) setConnected(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = v
}

func (p *pahoConnection) connectHandlers() []func() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]func(){}, p.onConnect...)
}

func (p *pahoConnection) messageHandlers() []MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]MessageHandler{}, p.onMessage...)
}

// Publish relies on autopaho's own view of the session rather than the connected flag:
// autopaho signals AwaitConnection before it runs OnConnectionUp, so the flag can lag
// the moment publishing actually becomes possible.
func (p *pahoConnection) Publish(ctx context.Context, topic string, payload string, retain bool) error {
	_, err := p.cm.Publish(ctx, &paho.Publish{Topic: topic, QoS: 1, Retain: retain, Payload: []byte(payload)})
	return sessionError(err)
}

func (p *pahoConnection) Subscribe(ctx context.Context, topics []string) error {
	_, err := p.cm.Subscribe(ctx, subscription(topics))
	return sessionError(err)
}

// 2 is the MQTT 5 Retain Handling option "do not send retained messages at subscribe".
const neverReplayRetained = 2

func subscription(topics []string) *paho.Subscribe {
	subs := make([]paho.SubscribeOptions, len(topics))
	for i, t := range topics {
		subs[i] = paho.SubscribeOptions{Topic: t, QoS: 1, RetainHandling: neverReplayRetained}
	}
	return &paho.Subscribe{Subscriptions: subs}
}

func sessionError(err error) error {
	if errors.Is(err, autopaho.ConnectionDownError) {
		return ErrNotConnected
	}
	return err
}

func (p *pahoConnection) OnMessage(handler MessageHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessage = append(p.onMessage, handler)
}

func (p *pahoConnection) OnConnect(handler func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onConnect = append(p.onConnect, handler)
}

func (p *pahoConnection) Connected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

func (p *pahoConnection) AwaitConnection(ctx context.Context) error {
	return p.cm.AwaitConnection(ctx)
}

func (p *pahoConnection) Close(ctx context.Context) error {
	defer p.cancel()
	err := p.cm.Disconnect(ctx)
	select {
	case <-p.cm.Done():
	case <-ctx.Done():
	}
	return err
}
